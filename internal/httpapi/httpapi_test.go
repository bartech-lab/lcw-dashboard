package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bartech/lcw-dashboard/internal/alerts"
	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/config"
	"github.com/bartech/lcw-dashboard/internal/credits"
	"github.com/bartech/lcw-dashboard/internal/history"
	"github.com/bartech/lcw-dashboard/internal/hub"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/notify"
	"github.com/bartech/lcw-dashboard/internal/scheduler"
	"github.com/bartech/lcw-dashboard/internal/searchindex"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
	"github.com/bartech/lcw-dashboard/internal/store"
	"github.com/bartech/lcw-dashboard/internal/watchlist"
)

const secret = "SUPER-SECRET-KEY-9f3a"

type env struct {
	srv    *httptest.Server
	world  *snapshot.Holder
	watch  *watchlist.List
	clk    *clock.Fake
	cfg    config.Config
	cancel context.CancelFunc
}

func newEnv(t *testing.T, tweak func(*config.Config)) *env {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/status":
			io.WriteString(w, `{}`)
		case "/credits":
			io.WriteString(w, `{"dailyCreditsRemaining":9000,"dailyCreditsLimit":10000}`)
		case "/coins/single":
			io.WriteString(w, `{"name":"Bitcoin","rank":1,"rate":77193.88,"delta":{"day":1.063}}`)
		case "/coins/single/history":
			io.WriteString(w, `{"name":"Bitcoin","history":[{"date":1787400000000,"rate":77000},{"date":1787403600000,"rate":77193.88}]}`)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Default()
	cfg.API.BaseURL = upstream.URL
	cfg.APIKey = secret
	cfg.Credits.MinRequestGap = config.Duration(0)
	cfg.Credits.Burst = 10000
	cfg.SearchIndex.Enabled = true
	cfg.History.Enabled = false
	cfg.Overview.Enabled = false
	cfg.Alerts.Enabled = true
	if tweak != nil {
		tweak(&cfg)
	}

	clk := clock.NewFake(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	paths := store.Paths{ConfigDir: dir, StateDir: dir, CacheDir: dir}

	wl := watchlist.New(clk, filepath.Join(dir, "w.json"), cfg.Coins.WatchlistMax, cfg.Coins.ChunkSize)
	if err := wl.Load([]string{"BTC", "ETH"}); err != nil {
		t.Fatal(err)
	}

	ledger := credits.NewLedger(clk, cfg.Credits.APIDailyLimit)
	guard := credits.NewGuard(cfg.Credits, clk, ledger, credits.NewLimiter(cfg.Credits, clk), true)
	client := lcw.New(cfg.APIKey, lcw.WithBaseURL(cfg.API.BaseURL))
	world := snapshot.NewHolder()
	events := hub.New()
	index := searchindex.New(cfg.SearchIndex, clk, filepath.Join(dir, "ix.json"), log)
	engine := alerts.NewEngine(clk, cfg.Alerts.Rules, time.Minute, 0, 50)
	hist := history.NewStore(cfg.History, paths, log)

	ctrl := scheduler.New(scheduler.Deps{
		Cfg: cfg, Clk: clk, Log: log, Client: client, Guard: guard,
		Hub: events, World: world, Watch: wl, Hist: hist,
		Engine: engine, Index: index, Notify: notify.NewFanout(log),
	})
	ctrl.SetEnvPath(filepath.Join(dir, ".env"))

	ctx, cancel := context.WithCancel(context.Background())
	go ctrl.Run(ctx)
	t.Cleanup(cancel)

	api := New(Deps{
		Cfg: cfg, Clk: clk, Log: log, Hub: events, World: world, Ctrl: ctrl,
		Watch: wl, Index: index, Guard: guard, Client: client, Hist: hist,
		Engine: engine, Assets: nil, Version: "test",
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	// Seed a table so state and detail have something to serve.
	world.SetCoins("top|USD|", &snapshot.Coins{
		View: snapshot.ViewTop, Currency: "USD", AsOf: clk.Now(),
		Coins: []snapshot.CoinRow{{Code: "BTC", Name: "Bitcoin", Rank: 1}},
	})
	world.SetFiats(&snapshot.Fiats{Fiats: []snapshot.Fiat{
		{Code: "USD", Name: "US Dollar", Symbol: "$"},
		{Code: "EUR", Name: "Euro", Symbol: "€"},
	}})

	return &env{srv: srv, world: world, watch: wl, clk: clk, cfg: cfg, cancel: cancel}
}

func (e *env) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	res, err := e.srv.Client().Get(e.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(body)
}

func (e *env) send(t *testing.T, method, path string, payload any) (*http.Response, string) {
	t.Helper()
	var buf bytes.Buffer
	if payload != nil {
		json.NewEncoder(&buf).Encode(payload)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	res, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(body)
}

// The API key must not appear in any response, ever.
func TestAPIKeyNeverAppearsInAnyResponse(t *testing.T) {
	e := newEnv(t, nil)

	paths := []string{
		"/api/health", "/api/config", "/api/state", "/api/watchlist",
		"/api/alerts", "/api/fiats", "/api/search?q=btc",
	}
	for _, p := range paths {
		_, body := e.get(t, p)
		if strings.Contains(body, secret) {
			t.Errorf("%s leaked the API key", p)
		}
	}
	// Also on an error path, where a wrapped error could carry it.
	_, body := e.get(t, "/api/coins/")
	if strings.Contains(body, secret) {
		t.Error("the 404 path leaked the API key")
	}
}

func TestConfigIsRedactedButReportsPresence(t *testing.T) {
	e := newEnv(t, nil)
	_, body := e.get(t, "/api/config")

	var out struct {
		HasAPIKey bool           `json:"hasApiKey"`
		Spend     config.Spend   `json:"spend"`
		Config    map[string]any `json:"config"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if !out.HasAPIKey {
		t.Error("hasApiKey should be true so the UI knows a key is configured")
	}
	if out.Spend.Total == 0 {
		t.Error("the credit projection should be served")
	}
}

func TestHealth(t *testing.T) {
	e := newEnv(t, nil)
	res, body := e.get(t, "/api/health")
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("body = %s", body)
	}
}

// A page on another origin must not be able to drive a service with no auth.
func TestLoopbackGuardRejectsASpoofedHost(t *testing.T) {
	e := newEnv(t, nil)

	req, _ := http.NewRequest("GET", e.srv.URL+"/api/health", nil)
	req.Host = "evil.example.com"
	res, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a non-loopback Host", res.StatusCode)
	}
}

func TestCrossSiteMutationIsRejected(t *testing.T) {
	e := newEnv(t, nil)

	req, _ := http.NewRequest("POST", e.srv.URL+"/api/refresh", strings.NewReader(`{}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.StatusCode)
	}

	req2, _ := http.NewRequest("POST", e.srv.URL+"/api/refresh", strings.NewReader(`{}`))
	req2.Header.Set("content-type", "application/json")
	req2.Header.Set("Origin", "http://evil.example.com")
	res2, err := e.srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a foreign Origin", res2.StatusCode)
	}
}

func TestSameOriginMutationIsAllowed(t *testing.T) {
	e := newEnv(t, nil)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/refresh", strings.NewReader(`{"what":"coins"}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	res, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusForbidden {
		t.Error("a same-origin request must not be rejected")
	}
}

func TestSSESendsHelloFirstThenReplay(t *testing.T) {
	e := newEnv(t, nil)

	req, _ := http.NewRequest("GET", e.srv.URL+"/api/stream?client_id=t1&view=top&currency=USD&visible=1", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := e.srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if ct := res.Header.Get("content-type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	if cc := res.Header.Get("cache-control"); cc != "no-store" {
		t.Errorf("cache-control = %q", cc)
	}
	// Proxies buffer event streams by default, which would defeat the point.
	if res.Header.Get("x-accel-buffering") != "no" {
		t.Error("x-accel-buffering should be no")
	}

	buf := make([]byte, 4096)
	n, _ := res.Body.Read(buf)
	head := string(buf[:n])

	// The client needs the config before the first table.
	if !strings.Contains(head, "event: hello") {
		t.Fatalf("first frames do not include hello:\n%s", head)
	}
	helloAt := strings.Index(head, "event: hello")
	if coinsAt := strings.Index(head, "event: coins"); coinsAt >= 0 && coinsAt < helloAt {
		t.Error("a coins frame preceded hello")
	}
	if strings.Contains(head, secret) {
		t.Error("the stream leaked the API key")
	}
}

func TestSSERequiresAClientID(t *testing.T) {
	e := newEnv(t, nil)
	res, _ := e.get(t, "/api/stream")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

func TestControlRejectsUnknownViewAndCurrency(t *testing.T) {
	e := newEnv(t, nil)

	res, _ := e.send(t, "POST", "/api/control", map[string]any{
		"clientId": "c1", "view": "grid", "currency": "USD",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown view: status = %d, want 400", res.StatusCode)
	}

	// Rejected locally rather than spending a credit on a doomed request.
	res, body := e.send(t, "POST", "/api/control", map[string]any{
		"clientId": "c1", "view": "top", "currency": "XYZ",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown currency: status = %d, want 400: %s", res.StatusCode, body)
	}
}

func TestControlRequiresAClientID(t *testing.T) {
	e := newEnv(t, nil)
	res, _ := e.send(t, "POST", "/api/control", map[string]any{"view": "top"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

func TestWatchlistGetPutAndToggle(t *testing.T) {
	e := newEnv(t, nil)

	_, body := e.get(t, "/api/watchlist")
	if !strings.Contains(body, "BTC") {
		t.Fatalf("body = %s", body)
	}

	res, body := e.send(t, "PUT", "/api/watchlist", map[string]any{
		"codes": []string{"btc", "sol", "hype"},
	})
	if res.StatusCode != 200 {
		t.Fatalf("status = %d: %s", res.StatusCode, body)
	}
	var wl snapshot.Watchlist
	json.Unmarshal([]byte(body), &wl)
	if len(wl.Codes) != 3 || wl.Codes[0] != "BTC" {
		t.Errorf("codes = %v, want normalised and sorted", wl.Codes)
	}

	res, body = e.send(t, "POST", "/api/watchlist/toggle", map[string]any{"code": "eth"})
	if res.StatusCode != 200 {
		t.Fatalf("toggle status = %d: %s", res.StatusCode, body)
	}
	if !e.watch.Contains("ETH") {
		t.Error("ETH should have been added")
	}
}

func TestWatchlistRejectsTooMany(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Coins.WatchlistMax = 2 })
	res, _ := e.send(t, "PUT", "/api/watchlist", map[string]any{
		"codes": []string{"A", "B", "C"},
	})
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", res.StatusCode)
	}
}

// A cold index returns 503 rather than pretending there are no matches.
func TestSearchReportsAColdIndex(t *testing.T) {
	e := newEnv(t, nil)
	res, body := e.get(t, "/api/search?q=btc")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	if !strings.Contains(body, `"indexReady":false`) {
		t.Errorf("body = %s", body)
	}
}

func TestCoinDetailRejectsAnUnknownRange(t *testing.T) {
	e := newEnv(t, nil)
	res, body := e.get(t, "/api/coins/BTC?range=decade")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", res.StatusCode, body)
	}
}

func TestCoinDetailFetchesAndThenServesFromCache(t *testing.T) {
	e := newEnv(t, nil)

	res, body := e.get(t, "/api/coins/BTC?range=7d&currency=USD")
	if res.StatusCode != 200 {
		t.Fatalf("status = %d: %s", res.StatusCode, body)
	}
	var first snapshot.Detail
	if err := json.Unmarshal([]byte(body), &first); err != nil {
		t.Fatal(err)
	}
	if first.Coin.Code != "BTC" {
		t.Errorf("code = %q", first.Coin.Code)
	}
	if first.CreditsUsed == 0 {
		t.Error("a cold detail request should report the credits it spent")
	}
	// The stub returns delta.day 1.063, which must arrive as percent.
	if first.Coin.ChangePct.Day == nil || *first.Coin.ChangePct.Day < 6.29 {
		t.Errorf("ChangePct.Day = %v, want about 6.3", first.Coin.ChangePct.Day)
	}
	if strings.Contains(body, `"delta"`) {
		t.Error("a raw delta reached the wire")
	}

	_, body2 := e.get(t, "/api/coins/BTC?range=7d&currency=USD")
	var second snapshot.Detail
	json.Unmarshal([]byte(body2), &second)
	if !second.FromCache {
		t.Error("the second request should be served from cache")
	}
	if second.CreditsUsed != 0 {
		t.Errorf("CreditsUsed = %d on a cache hit, want 0", second.CreditsUsed)
	}
}

func TestStateServesEverySection(t *testing.T) {
	e := newEnv(t, nil)
	_, body := e.get(t, "/api/state")

	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"coins", "overview", "status", "credits", "watchlist", "fiats"} {
		if _, ok := out[key]; !ok {
			t.Errorf("state is missing %q", key)
		}
	}
}

func TestAlertsEndpoints(t *testing.T) {
	e := newEnv(t, nil)

	res, body := e.get(t, "/api/alerts")
	if res.StatusCode != 200 {
		t.Fatalf("status = %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(body, `"enabled":true`) {
		t.Errorf("body = %s", body)
	}

	res, _ = e.send(t, "POST", "/api/alerts/some-rule/enabled", map[string]any{"enabled": false})
	if res.StatusCode != 200 {
		t.Errorf("enable toggle status = %d", res.StatusCode)
	}
	res, _ = e.send(t, "POST", "/api/alerts/some-rule/ack", nil)
	if res.StatusCode != 200 {
		t.Errorf("ack status = %d", res.StatusCode)
	}
}

func TestStaticHandlerExplainsAMissingBundle(t *testing.T) {
	e := newEnv(t, nil)
	res, body := e.get(t, "/")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 with no bundle", res.StatusCode)
	}
	if !strings.Contains(body, "go generate") {
		t.Errorf("the message should say how to build the frontend: %s", body)
	}
}

func TestRefreshReportsItsDecision(t *testing.T) {
	e := newEnv(t, nil)
	res, body := e.send(t, "POST", "/api/refresh", map[string]any{"what": "coins"})

	var reply scheduler.RefreshReply
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatal(err)
	}
	// Either accepted, or refused with a reason the UI can show.
	if !reply.Accepted && reply.Reason == "" {
		t.Errorf("a refused refresh must carry a reason: %s (status %d)", body, res.StatusCode)
	}
}

func TestUpstreamStatusCostsNothing(t *testing.T) {
	e := newEnv(t, nil)
	res, body := e.get(t, "/api/upstream/status")
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if !strings.Contains(body, `"up":true`) {
		t.Errorf("body = %s", body)
	}
}

// The presence heartbeat sends only visibility. Treating an absent field as a
// default reset the client's view every 20 seconds, which is what made the
// favourites view flip back to the top list.
func TestPartialControlMessagePatchesRatherThanResets(t *testing.T) {
	e := newEnv(t, nil)

	res, body := e.send(t, "POST", "/api/control", map[string]any{
		"clientId": "tab1", "view": "favourites", "currency": "USD", "visible": true,
	})
	if res.StatusCode != 200 {
		t.Fatalf("status = %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "favourites|") {
		t.Fatalf("initial switch did not take: %s", body)
	}

	// A heartbeat carrying only visibility, exactly as the client sends it.
	_, body = e.send(t, "POST", "/api/control", map[string]any{
		"clientId": "tab1", "visible": true,
	})
	if !strings.Contains(body, "favourites|") {
		t.Errorf("a visibility-only heartbeat discarded the view: %s", body)
	}

	// And an explicit switch back still works.
	_, body = e.send(t, "POST", "/api/control", map[string]any{
		"clientId": "tab1", "view": "top",
	})
	if !strings.Contains(body, `"viewKey":"top|`) {
		t.Errorf("explicit switch to top failed: %s", body)
	}
}

// An unknown client still needs a sensible starting point.
func TestControlForAnUnknownClientUsesTheConfiguredDefault(t *testing.T) {
	e := newEnv(t, nil)
	_, body := e.send(t, "POST", "/api/control", map[string]any{
		"clientId": "never-seen", "visible": true,
	})
	if !strings.Contains(body, `"viewKey":"top|`) {
		t.Errorf("want the configured default view, got: %s", body)
	}
}
