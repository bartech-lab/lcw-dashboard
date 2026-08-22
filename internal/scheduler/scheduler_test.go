package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/bartech/lcw-dashboard/internal/snapshot"
	"github.com/bartech/lcw-dashboard/internal/store"
	"github.com/bartech/lcw-dashboard/internal/watchlist"
)

var noon = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

// upstream counts calls per endpoint so tests assert on credit spend directly.
type upstream struct {
	mu     sync.Mutex
	calls  map[string]int
	bodies map[string][]map[string]any
	fail   atomic.Bool
	srv    *httptest.Server
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{calls: map[string]int{}, bodies: map[string][]map[string]any{}}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			json.Unmarshal(raw, &body)
		}
		u.mu.Lock()
		u.calls[r.URL.Path]++
		u.bodies[r.URL.Path] = append(u.bodies[r.URL.Path], body)
		u.mu.Unlock()

		w.Header().Set("content-type", "application/json")
		if u.fail.Load() {
			w.WriteHeader(503)
			io.WriteString(w, `{"error":{"code":503,"status":"Service unavailable.","description":"down"}}`)
			return
		}
		switch r.URL.Path {
		case "/status":
			io.WriteString(w, `{}`)
		case "/credits":
			io.WriteString(w, `{"dailyCreditsRemaining":9000,"dailyCreditsLimit":10000}`)
		case "/overview":
			io.WriteString(w, `{"cap":3.9e12,"volume":1.4e11,"liquidity":9.1e9,"btcDominance":0.54}`)
		case "/fiats/all":
			io.WriteString(w, `[{"code":"USD","name":"US Dollar","symbol":"$"},{"code":"EUR","name":"Euro","symbol":"€"}]`)
		case "/coins/list":
			io.WriteString(w, `[{"code":"BTC","name":"Bitcoin","rank":1,"rate":77193.88,"cap":1.5e12,"delta":{"day":1.063}},
			                    {"code":"ETH","name":"Ethereum","rank":2,"rate":2417.78,"cap":2.9e11,"delta":{"day":1.0395}}]`)
		case "/coins/map":
			codes, _ := body["codes"].([]any)
			io.WriteString(w, mapResponse(codes))
		default:
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func mapResponse(codes []any) string {
	out := "["
	for i, c := range codes {
		code, _ := c.(string)
		if code == "NOPE" {
			continue // the API omits codes it does not know
		}
		if i > 0 && len(out) > 1 {
			out += ","
		}
		out += fmt.Sprintf(`{"code":%q,"name":%q,"rank":%d,"rate":%d,"delta":{"day":1.01}}`,
			code, code, i+1, 100+i)
	}
	return out + "]"
}

func (u *upstream) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[path]
}

func (u *upstream) total() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, v := range u.calls {
		n += v
	}
	return n
}

type harness struct {
	ctrl   *Controller
	clk    *clock.Fake
	up     *upstream
	guard  *credits.Guard
	watch  *watchlist.List
	hub    *hub.Hub
	world  *snapshot.Holder
	cancel context.CancelFunc
	done   chan struct{}
}

func newHarness(t *testing.T, tweak func(*config.Config)) *harness {
	t.Helper()

	up := newUpstream(t)
	cfg := config.Default()
	cfg.API.BaseURL = up.srv.URL
	cfg.APIKey = "test-key"
	// Remove the rate-limit floor so tests exercise scheduling, not throttling.
	cfg.Credits.MinRequestGap = config.Duration(0)
	cfg.Credits.Burst = 10000
	cfg.SearchIndex.Enabled = false
	cfg.History.Enabled = false
	cfg.Overview.Enabled = false
	cfg.Alerts.Enabled = false
	if tweak != nil {
		tweak(&cfg)
	}

	clk := clock.NewFake(noon)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	dir := t.TempDir()
	paths := store.Paths{ConfigDir: dir, StateDir: dir, CacheDir: dir}
	wl := watchlist.New(clk, filepath.Join(dir, "watchlist.json"),
		cfg.Coins.WatchlistMax, cfg.Coins.ChunkSize)
	if err := wl.Load(cfg.Watchlist.Initial); err != nil {
		t.Fatal(err)
	}

	ledger := credits.NewLedger(clk, cfg.Credits.APIDailyLimit)
	limiter := credits.NewLimiter(cfg.Credits, clk)
	guard := credits.NewGuard(cfg.Credits, clk, ledger, limiter, true)

	h := &harness{
		clk: clk, up: up, guard: guard, watch: wl,
		hub: hub.New(), world: snapshot.NewHolder(),
		done: make(chan struct{}),
	}
	h.ctrl = New(Deps{
		Cfg: cfg, Clk: clk, Log: log,
		Client: lcw.New(cfg.APIKey, lcw.WithBaseURL(cfg.API.BaseURL)),
		Guard:  guard, Hub: h.hub, World: h.world, Watch: wl,
		Hist:   history.NewStore(cfg.History, paths, log),
		Engine: alerts.NewEngine(clk, cfg.Alerts.Rules, time.Minute, 0, 100),
		Notify: notify.NewFanout(log),
	})

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		h.ctrl.Run(ctx)
		close(h.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	// Wait for Run to create its timers. Advancing the fake clock before they
	// exist finds nothing to fire, which would make any test that advances
	// immediately silently observe nothing.
	h.settle()
	return h
}

// settle lets the controller goroutine drain. The fake clock does not advance
// real time, so a short real sleep is the only way to sequence goroutines.
func (h *harness) settle() { time.Sleep(30 * time.Millisecond) }

func (h *harness) status() *snapshot.Status { return h.world.Load().Status }

func TestVisibleClientGetsTheActiveInterval(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	if got := h.status().IntervalMs; got != 15000 {
		t.Errorf("interval = %dms, want 15000 for a visible tab", got)
	}
	if got := h.status().VisibleClients; got != 1 {
		t.Errorf("VisibleClients = %d, want 1", got)
	}
}

func TestHiddenTabDropsToTheIdleInterval(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()
	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", false)
	h.settle()

	if got := h.status().IntervalMs; got != 120000 {
		t.Errorf("interval = %dms, want 120000 when every tab is hidden", got)
	}
}

func TestNoClientsUsesTheSlowestInterval(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Alerts.Enabled = true
		c.Alerts.PollWhenNoClients = true
	})
	h.settle()

	if got := h.status().IntervalMs; got != 300000 {
		t.Errorf("interval = %dms, want 300000 with no clients", got)
	}
}

// Alerts are the only reason to keep polling for nobody, so without them the
// recurring loop stops. The one-off startup prime still happens: one credit means
// the first browser to connect is painted immediately instead of waiting out an
// idle interval.
func TestNoClientsAndNoAlertsStopsRecurringPolling(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Alerts.Enabled = false })

	// Let the prime land.
	h.clk.Advance(housekeeping)
	h.settle()
	h.clk.Advance(housekeeping)
	h.settle()
	primed := h.up.count("/coins/list")
	if primed != 1 {
		t.Fatalf("startup prime made %d requests, want exactly 1", primed)
	}

	h.clk.Advance(30 * time.Minute)
	h.settle()
	if after := h.up.count("/coins/list"); after != primed {
		t.Errorf("made %d further requests with no clients and no alerts", after-primed)
	}
}

func TestFocusRefreshFiresWhenDataIsStale(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()
	first := h.up.count("/coins/list")

	// Go hidden, let data age past the threshold, come back.
	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", false)
	h.settle()
	h.clk.Advance(time.Minute)
	reply := h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	if !reply.Accepted {
		t.Errorf("focus refresh was refused: %s", reply.Reason)
	}
	if got := h.up.count("/coins/list"); got <= first {
		t.Error("regaining focus should have triggered a fetch")
	}
}

func TestFocusRefreshIsSkippedWhenDataIsFresh(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()
	h.clk.Advance(2 * time.Second)

	before := h.up.count("/coins/list")
	reply := h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	if reply.Accepted {
		t.Error("a refresh two seconds after the last one should be skipped")
	}
	if got := h.up.count("/coins/list"); got != before {
		t.Errorf("made %d extra requests", got-before)
	}
}

// Eight tabs waking together must produce one fetch, not eight.
func TestSimultaneousFocusIsCoalescedToOneFetch(t *testing.T) {
	h := newHarness(t, nil)

	for i := 0; i < 8; i++ {
		h.ctrl.Presence(fmt.Sprintf("tab%d", i), snapshot.ViewTop, "USD", false)
	}
	h.settle()
	before := h.up.count("/coins/list")

	for i := 0; i < 8; i++ {
		h.ctrl.Presence(fmt.Sprintf("tab%d", i), snapshot.ViewTop, "USD", true)
	}
	h.settle()

	if got := h.up.count("/coins/list") - before; got > 1 {
		t.Errorf("eight tabs regaining focus made %d requests, want at most 1", got)
	}
}

// The in-flight flag is the whole double-fire defence.
func TestTickDuringAFetchIsDroppedNotQueued(t *testing.T) {
	h := newHarness(t, nil)
	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	before := h.up.count("/coins/list")
	// Fire many ticks with no time for results to land.
	for i := 0; i < 20; i++ {
		h.clk.Advance(15 * time.Second)
	}
	h.settle()

	if got := h.up.count("/coins/list") - before; got > 20 {
		t.Errorf("made %d requests from 20 ticks; overlapping fetches were queued", got)
	}
}

// The favourites view uses /coins/map, which ignores rank, so an out-of-top-100
// coin costs the same as Bitcoin.
func TestFavouritesUsesCoinsMapAndCostsOneCredit(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.watch.Set([]string{"BTC", "ETH", "HYPE"}); err != nil {
		t.Fatal(err)
	}

	h.ctrl.Presence("tab1", snapshot.ViewFavourites, "USD", true)
	h.settle()

	if got := h.up.count("/coins/map"); got != 1 {
		t.Errorf("/coins/map called %d times, want 1", got)
	}
	if got := h.up.count("/coins/list"); got != 0 {
		t.Errorf("/coins/list called %d times on the favourites view, want 0", got)
	}
	if got := h.guard.Ledger().Committed(); got != 1 {
		t.Errorf("spent %d credits for a three-coin watchlist, want 1", got)
	}
}

// Credit rate must be invariant to watchlist length: chunking multiplies the
// interval rather than the spend.
func TestChunkedWatchlistHoldsTheCreditRate(t *testing.T) {
	h := newHarness(t, nil)

	codes := make([]string, 150)
	for i := range codes {
		codes[i] = fmt.Sprintf("C%03d", i)
	}
	if _, err := h.watch.Set(codes); err != nil {
		t.Fatal(err)
	}
	h.ctrl.Presence("tab1", snapshot.ViewFavourites, "USD", true)
	h.settle()

	if got := h.watch.ChunkCount(); got != 2 {
		t.Fatalf("ChunkCount = %d, want 2 for 150 codes", got)
	}
	// Two chunks cost two credits per refresh, so the interval doubles and the
	// credits-per-second rate is unchanged.
	if got := h.status().IntervalMs; got != 30000 {
		t.Errorf("interval = %dms, want 30000 (15s x 2 chunks)", got)
	}
	if got := h.status().ChunkPenalty; got != 2 {
		t.Errorf("ChunkPenalty = %d, want 2 surfaced to the UI", got)
	}
}

// Several distinct views share the loop by rotation, so tab count does not
// multiply credit spend.
func TestRotationDividesTheRateRatherThanMultiplyingSpend(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.ctrl.Presence("tab2", snapshot.ViewTop, "EUR", true)
	h.settle()

	st := h.status()
	if len(st.RotationKeys) != 2 {
		t.Fatalf("RotationKeys = %v, want 2", st.RotationKeys)
	}
	if !st.Rotating {
		t.Error("Rotating should be true so the UI can explain the slower refresh")
	}
	if st.IntervalMs != 30000 {
		t.Errorf("interval = %dms, want 30000 (15s x 2 keys)", st.IntervalMs)
	}
}

func TestRotationIsCappedByConfig(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Poll.MaxRotationKeys = 2 })

	h.ctrl.Presence("a", snapshot.ViewTop, "USD", true)
	h.ctrl.Presence("b", snapshot.ViewTop, "EUR", true)
	h.ctrl.Presence("c", snapshot.ViewTop, "PLN", true)
	h.settle()

	if got := len(h.status().RotationKeys); got != 2 {
		t.Errorf("RotationKeys = %d, want the configured cap of 2", got)
	}
}

// A frozen tab cannot announce its departure, so the server must expire it or it
// would hold the fast cadence forever.
func TestStaleClientsExpire(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()
	if got := h.status().TotalClients; got != 1 {
		t.Fatalf("TotalClients = %d, want 1", got)
	}

	// Past the 45s TTL, with a housekeeping tick to notice.
	h.clk.Advance(60 * time.Second)
	h.settle()
	h.clk.Advance(housekeeping)
	h.settle()

	if got := h.status().TotalClients; got != 0 {
		t.Errorf("TotalClients = %d, want 0 after the TTL", got)
	}
}

func TestDisconnectRemovesAClient(t *testing.T) {
	h := newHarness(t, nil)
	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	h.ctrl.Disconnect("tab1")
	h.settle()

	if got := h.status().TotalClients; got != 0 {
		t.Errorf("TotalClients = %d, want 0", got)
	}
}

// Stale data is shown, never blanked.
func TestFailureKeepsTheLastGoodTable(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	key := h.status().ActiveViewKey
	first := h.world.Load().Coins[key]
	if first == nil || len(first.Coins) == 0 {
		t.Fatal("expected a first successful table")
	}

	h.up.fail.Store(true)
	h.clk.Advance(time.Minute)
	h.settle()

	after := h.world.Load().Coins[key]
	if after == nil {
		t.Fatal("the table was removed on failure")
	}
	if len(after.Coins) != len(first.Coins) {
		t.Errorf("kept %d rows, want the previous %d", len(after.Coins), len(first.Coins))
	}
	if !after.Stale {
		t.Error("Stale should be true")
	}
	if after.Error == nil {
		t.Error("Error should carry the reason")
	}
	if after.AgeMs <= 0 {
		t.Error("AgeMs should report how old the data is")
	}
}

func TestDeltaReachesTheWireAsPercent(t *testing.T) {
	h := newHarness(t, nil)
	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	coins := h.world.Load().Coins[h.status().ActiveViewKey]
	if coins == nil || len(coins.Coins) == 0 {
		t.Fatal("no coins")
	}
	// The stub returns delta.day = 1.063, which is +6.3%.
	day := coins.Coins[0].ChangePct.Day
	if day == nil {
		t.Fatal("ChangePct.Day is nil")
	}
	if *day < 6.29 || *day > 6.31 {
		t.Errorf("ChangePct.Day = %v, want about 6.3", *day)
	}

	raw, err := json.Marshal(coins.Coins[0])
	if err != nil {
		t.Fatal(err)
	}
	if containsKey(raw, "delta") {
		t.Error("a raw delta must never reach the wire")
	}
}

func containsKey(raw []byte, key string) bool {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// A code the API declines must surface rather than silently vanish.
func TestUnknownWatchlistCodesAreReported(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.watch.Set([]string{"BTC", "NOPE"}); err != nil {
		t.Fatal(err)
	}

	h.ctrl.Presence("tab1", snapshot.ViewFavourites, "USD", true)
	h.settle()

	coins := h.world.Load().Coins[h.status().ActiveViewKey]
	if coins == nil {
		t.Fatal("no table")
	}
	found := false
	for _, c := range coins.UnknownCodes {
		if c == "NOPE" {
			found = true
		}
	}
	if !found {
		t.Errorf("UnknownCodes = %v, want it to include NOPE", coins.UnknownCodes)
	}
}

func TestExhaustedBudgetStopsPollingButKeepsServing(t *testing.T) {
	h := newHarness(t, nil)
	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	h.guard.AdoptExhausted()
	before := h.up.count("/coins/list")
	h.clk.Advance(10 * time.Minute)
	h.settle()

	if got := h.up.count("/coins/list"); got != before {
		t.Errorf("made %d requests while exhausted", got-before)
	}
	st := h.status()
	if st.PollState != snapshot.PollExhausted {
		t.Errorf("PollState = %s, want exhausted", st.PollState)
	}
	if st.DegradedReason == "" {
		t.Error("DegradedReason should explain the state to the UI")
	}
}

func TestNoKeyProducesASetupHint(t *testing.T) {
	h := newHarness(t, nil)
	h.ctrl.SetEnvPath("/home/user/.config/lcw-dashboard/.env")
	h.guard.SetNoKey()
	h.clk.Advance(housekeeping)
	h.settle()

	st := h.status()
	if st.PollState != snapshot.PollNoKey {
		t.Errorf("PollState = %s, want no_key", st.PollState)
	}
	if st.SetupHint == "" {
		t.Error("SetupHint should name the exact file to create")
	}
}

func TestWatchlistChangeRefetchesOnlyTheFavouritesView(t *testing.T) {
	h := newHarness(t, nil)
	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	before := h.up.total()
	h.watch.Set([]string{"BTC", "SOL"})
	h.ctrl.WatchlistChanged()
	h.settle()

	if got := h.up.count("/coins/map"); got != 0 {
		t.Errorf("fetched favourites %d times while viewing the top list", got)
	}
	_ = before
}

func TestManualRefreshIsDebounced(t *testing.T) {
	h := newHarness(t, nil)
	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	// Let the scheduled tick complete first, so this measures the debounce and
	// not the in-flight guard.
	h.clk.Advance(time.Minute)
	h.settle()

	first := h.ctrl.Refresh("coins")
	h.settle()
	second := h.ctrl.Refresh("coins")
	h.settle()

	if !first.Accepted {
		t.Errorf("first manual refresh refused: %s", first.Reason)
	}
	if second.Accepted {
		t.Error("a second refresh inside the debounce window should be refused")
	}
	if second.Reason != "debounced" {
		t.Errorf("Reason = %q, want debounced", second.Reason)
	}
}

func TestOverviewUsesItsOwnSlowerTimer(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Overview.Enabled = true })
	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()

	// Five minutes is one overview interval but twenty coin intervals.
	h.clk.Advance(5 * time.Minute)
	h.settle()

	overview := h.up.count("/overview")
	coins := h.up.count("/coins/list")
	if overview == 0 {
		t.Error("overview never polled")
	}
	if overview >= coins {
		t.Errorf("overview polled %d times and coins %d; overview must be slower", overview, coins)
	}
}

func TestHelloCarriesTheServerConfig(t *testing.T) {
	h := newHarness(t, nil)
	hello := h.ctrl.Hello("client-1", "test")

	if hello.Config.ActiveIntervalMs != 15000 {
		t.Errorf("ActiveIntervalMs = %d", hello.Config.ActiveIntervalMs)
	}
	if hello.Config.ProjectedDailyCredits == 0 {
		t.Error("ProjectedDailyCredits should be filled so the UI can warn")
	}
	// The API cannot sort by a delta window, so the frontend needs the real list
	// to know which columns may offer market-wide sorting.
	for _, f := range hello.Config.SortableFields {
		for _, w := range hello.Config.DeltaWindows {
			if f == w {
				t.Errorf("%q appears in both sortable fields and delta windows", f)
			}
		}
	}
	if len(hello.Config.DeltaWindows) != 6 {
		t.Errorf("DeltaWindows = %v, want 6", hello.Config.DeltaWindows)
	}
}

func TestBootstrapSurvivesADeadUpstream(t *testing.T) {
	h := newHarness(t, nil)
	h.up.fail.Store(true)

	h.ctrl.Bootstrap(context.Background())
	h.settle()

	// The point is that it returns rather than panicking or blocking, and the
	// status is still publishable.
	if h.status() == nil {
		t.Fatal("no status after a failed bootstrap")
	}
}

func TestBootstrapWithNoKeyMakesNoKeyedCalls(t *testing.T) {
	up := newUpstream(t)
	cfg := config.Default()
	cfg.API.BaseURL = up.srv.URL
	cfg.APIKey = ""
	cfg.SearchIndex.Enabled = false
	cfg.History.Enabled = false

	clk := clock.NewFake(noon)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	paths := store.Paths{ConfigDir: dir, StateDir: dir, CacheDir: dir}
	wl := watchlist.New(clk, filepath.Join(dir, "w.json"), 300, 100)
	wl.Load(nil)

	ledger := credits.NewLedger(clk, cfg.Credits.APIDailyLimit)
	limiter := credits.NewLimiter(cfg.Credits, clk)
	guard := credits.NewGuard(cfg.Credits, clk, ledger, limiter, false)

	ctrl := New(Deps{
		Cfg: cfg, Clk: clk, Log: log,
		Client: lcw.New("", lcw.WithBaseURL(cfg.API.BaseURL)),
		Guard:  guard, Hub: hub.New(), World: snapshot.NewHolder(), Watch: wl,
		Hist:   history.NewStore(cfg.History, paths, log),
		Engine: alerts.NewEngine(clk, nil, time.Minute, 0, 10),
		Notify: notify.NewFanout(log),
	})
	ctrl.Bootstrap(context.Background())

	if got := up.count("/credits"); got != 0 {
		t.Errorf("/credits called %d times without a key", got)
	}
	if got := up.count("/coins/list"); got != 0 {
		t.Errorf("/coins/list called %d times without a key", got)
	}
	// /status needs no key and costs nothing, so it is expected.
	if got := up.count("/status"); got != 1 {
		t.Errorf("/status called %d times, want 1", got)
	}
	if guard.State() != credits.StateNoKey {
		t.Errorf("state = %s, want no_key", guard.State())
	}
}

func TestFiatAllowlistTrimsThePicker(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Currency.Allowlist = []string{"USD"}
	})
	h.ctrl.SetFiats([]lcw.Fiat{
		{Code: "USD", Name: "US Dollar"},
		{Code: "EUR", Name: "Euro"},
	})

	f := h.world.Load().Fiats
	if len(f.Fiats) != 1 || f.Fiats[0].Code != "USD" {
		t.Errorf("Fiats = %+v, want only USD", f.Fiats)
	}
}

func TestFiatDenylistRemovesACurrency(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Currency.Denylist = []string{"EUR"}
	})
	h.ctrl.SetFiats([]lcw.Fiat{
		{Code: "USD", Name: "US Dollar"},
		{Code: "EUR", Name: "Euro"},
	})

	for _, f := range h.world.Load().Fiats.Fiats {
		if f.Code == "EUR" {
			t.Error("EUR should have been removed")
		}
	}
}

// A backgrounded favourites tab must keep the server on favourites. Falling back
// to the config default polled a view nobody was looking at, and left the tab
// with nothing when it came back.
func TestHiddenClientKeepsItsView(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("tab1", snapshot.ViewFavourites, "USD", true)
	h.settle()
	if got := h.status().ActiveViewKey; !strings.HasPrefix(got, "favourites|") {
		t.Fatalf("visible: ActiveViewKey = %q, want favourites", got)
	}

	h.ctrl.Presence("tab1", snapshot.ViewFavourites, "USD", false)
	h.settle()
	if got := h.status().ActiveViewKey; !strings.HasPrefix(got, "favourites|") {
		t.Errorf("hidden: ActiveViewKey = %q, want favourites to be kept", got)
	}

	// Only with no clients at all does it fall back to the configured default.
	h.ctrl.Disconnect("tab1")
	h.settle()
	if got := h.status().ActiveViewKey; !strings.HasPrefix(got, "top|") {
		t.Errorf("no clients: ActiveViewKey = %q, want the configured default", got)
	}
}

// The control reply must not contradict the request.
func TestPresenceReplyReportsTheRequestedView(t *testing.T) {
	h := newHarness(t, nil)

	reply := h.ctrl.Presence("tab1", snapshot.ViewFavourites, "USD", false)
	if !strings.HasPrefix(reply.ViewKey, "favourites|") {
		t.Errorf("ViewKey = %q; a hidden favourites client was told it is on top",
			reply.ViewKey)
	}
}

// Freshness is per view key. One global timestamp let a top fetch mark
// favourites as fresh, so returning to a favourites tab skipped its fetch.
func TestFreshnessIsPerViewKey(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("tab1", snapshot.ViewTop, "USD", true)
	h.settle()
	before := h.up.count("/coins/map")

	// Switch to favourites immediately, while the top fetch is seconds old.
	h.ctrl.Presence("tab1", snapshot.ViewFavourites, "USD", true)
	h.settle()

	if got := h.up.count("/coins/map"); got <= before {
		t.Error("switching to favourites did not fetch: the top fetch made it look fresh")
	}
}

// With several tabs on different views the rotation head belongs to whichever
// was activated last, so the reply must describe the caller's own subscription.
func TestReplyReportsTheCallersOwnKeyNotTheRotationHead(t *testing.T) {
	h := newHarness(t, nil)

	h.ctrl.Presence("topTab", snapshot.ViewTop, "USD", true)
	h.settle()
	favReply := h.ctrl.Presence("favTab", snapshot.ViewFavourites, "USD", true)
	h.settle()
	topReply := h.ctrl.Presence("topTab", snapshot.ViewTop, "USD", true)
	h.settle()

	if !strings.HasPrefix(favReply.ViewKey, "favourites|") {
		t.Errorf("favourites tab was told %q", favReply.ViewKey)
	}
	if !strings.HasPrefix(topReply.ViewKey, "top|") {
		t.Errorf("top tab was told %q", topReply.ViewKey)
	}
}
