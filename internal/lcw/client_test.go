package lcw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// capture records what the client actually sent, so tests assert on the wire
// format rather than on the client's own idea of it.
type capture struct {
	method   string
	path     string
	headers  http.Header
	bodyJSON map[string]any
	rawBody  string
}

// serve stands up a stub API that records the request and returns the given
// status and body.
func serve(t *testing.T, status int, body string) (*Client, *capture) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		cap.rawBody = string(raw)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &cap.bodyJSON)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return New("test-key", WithBaseURL(srv.URL)), cap
}

func TestPostUsesPOSTWithKeyAndJSON(t *testing.T) {
	c, cap := serve(t, 200, `{"dailyCreditsRemaining":8796,"dailyCreditsLimit":10000}`)

	got, err := c.Credits(context.Background())
	if err != nil {
		t.Fatalf("Credits: %v", err)
	}
	if got.DailyCreditsRemaining != 8796 || got.DailyCreditsLimit != 10000 {
		t.Errorf("got %+v", got)
	}

	// POST is the only method the API accepts; GET returns 405.
	if cap.method != http.MethodPost {
		t.Errorf("method = %s, want POST", cap.method)
	}
	if cap.path != "/credits" {
		t.Errorf("path = %s, want /credits", cap.path)
	}
	if v := cap.headers.Get("x-api-key"); v != "test-key" {
		t.Errorf("x-api-key = %q, want %q", v, "test-key")
	}
	if v := cap.headers.Get("content-type"); v != "application/json" {
		t.Errorf("content-type = %q", v)
	}
}

// TestErrorUnderHTTP200 is the contract that matters most: this API returns
// error objects with a success status, so checking the status first would accept
// an error body as data.
func TestErrorUnderHTTP200(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{"error":{"code":401,"status":"Unauthorized","description":"Your API key is wrong."}}`)

	_, err := c.Credits(context.Background())
	if err == nil {
		t.Fatal("an error body under HTTP 200 must be reported as an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want *APIError", err)
	}
	if apiErr.Code != 401 {
		t.Errorf("Code = %d, want 401", apiErr.Code)
	}
	if apiErr.HTTPStatus != 200 {
		t.Errorf("HTTPStatus = %d, want 200 — the disagreement is the point", apiErr.HTTPStatus)
	}
	if !IsAuth(err) {
		t.Error("IsAuth should classify a 401 error object")
	}
}

func TestErrorClassifiers(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		auth, credit bool
		server       bool
	}{
		{"unauthorized", 200, `{"error":{"code":401,"status":"Unauthorized","description":"Your API key is wrong."}}`, true, false, false},
		{"forbidden", 403, `{"error":{"code":403,"status":"Forbidden","description":"no"}}`, true, false, false},
		{"rate limited", 429, `{"error":{"code":429,"status":"Too Many Requests","description":"slow down"}}`, false, true, false},
		{"credits by description", 200, `{"error":{"code":400,"status":"Bad Request","description":"Daily credit limit reached."}}`, false, true, false},
		{"maintenance", 503, `{"error":{"code":503,"status":"Service unavailable.","description":"offline for maintenance"}}`, false, false, true},
		{"bare failing status, no error object", 500, `{}`, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := serve(t, tc.status, tc.body)
			_, err := c.Credits(context.Background())
			if err == nil {
				t.Fatal("want an error")
			}
			if IsAuth(err) != tc.auth {
				t.Errorf("IsAuth = %v, want %v (%v)", IsAuth(err), tc.auth, err)
			}
			if IsCreditExhausted(err) != tc.credit {
				t.Errorf("IsCreditExhausted = %v, want %v (%v)", IsCreditExhausted(err), tc.credit, err)
			}
			if IsServerSide(err) != tc.server {
				t.Errorf("IsServerSide = %v, want %v (%v)", IsServerSide(err), tc.server, err)
			}
		})
	}
}

// TestNonJSONBody covers captive portals and proxy error pages, which serve HTML
// while often claiming application/json.
func TestNonJSONBody(t *testing.T) {
	c, _ := serve(t, 200, `<!DOCTYPE html><html><body>Sign in to the network</body></html>`)

	_, err := c.Credits(context.Background())
	var nj *ErrNonJSON
	if !errors.As(err, &nj) {
		t.Fatalf("error is %T (%v), want *ErrNonJSON", err, err)
	}
	if nj.Snippet == "" {
		t.Error("snippet should carry the start of the body for diagnosis")
	}
}

func TestEmptyBodyIsNotJSON(t *testing.T) {
	c, _ := serve(t, 200, ``)
	_, err := c.Credits(context.Background())
	var nj *ErrNonJSON
	if !errors.As(err, &nj) {
		t.Fatalf("error is %T (%v), want *ErrNonJSON", err, err)
	}
}

func TestStatusNeedsNoKeyAndAcceptsEmptyObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "" {
			t.Error("/status must not require a key; sending one is fine but it must work without")
		}
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	c := New("", WithBaseURL(srv.URL)) // no key at all
	if err := c.Status(context.Background()); err != nil {
		t.Fatalf("Status without a key: %v", err)
	}
}

// TestKeyedCallsWithoutKeyNeverHitNetwork matters for the credit ledger: a call
// that never left the process must not be counted as a spend.
func TestKeyedCallsWithoutKeyNeverHitNetwork(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	c := New("", WithBaseURL(srv.URL))
	if _, err := c.Credits(context.Background()); !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("Credits without a key = %v, want ErrNoAPIKey", err)
	}
	if hits != 0 {
		t.Errorf("made %d request(s) without a key; must make none", hits)
	}
	if c.HasKey() {
		t.Error("HasKey should be false")
	}
}

func TestCoinsListRequestBody(t *testing.T) {
	c, cap := serve(t, 200, `[{"code":"BTC","rate":77193.88,"delta":{"day":1.063}}]`)

	coins, err := c.CoinsList(context.Background(), CoinsListParams{
		Currency: "USD", Sort: SortRank, Order: OrderAscending, Offset: 0, Limit: 100, Meta: true,
	})
	if err != nil {
		t.Fatalf("CoinsList: %v", err)
	}
	if len(coins) != 1 || coins[0].Code != "BTC" {
		t.Fatalf("got %+v", coins)
	}

	want := map[string]any{
		"currency": "USD", "sort": "rank", "order": "ascending",
		"offset": float64(0), "limit": float64(100), "meta": true,
	}
	for k, v := range want {
		if cap.bodyJSON[k] != v {
			t.Errorf("body[%q] = %v, want %v (raw: %s)", k, cap.bodyJSON[k], v, cap.rawBody)
		}
	}
}

func TestCoinsListRejectsBadParams(t *testing.T) {
	c, _ := serve(t, 200, `[]`)
	ctx := context.Background()

	if _, err := c.CoinsList(ctx, CoinsListParams{Sort: SortRank, Order: OrderAscending, Limit: 101}); err == nil {
		t.Error("limit above 100 should be rejected locally, not silently truncated upstream")
	}
	if _, err := c.CoinsList(ctx, CoinsListParams{Sort: SortRank, Order: OrderAscending, Limit: 0}); err == nil {
		t.Error("limit 0 should be rejected")
	}
	if _, err := c.CoinsList(ctx, CoinsListParams{Sort: "day", Order: OrderAscending, Limit: 10}); err == nil {
		t.Error("a delta window is not a valid sort field and must be rejected")
	}
	if _, err := c.CoinsList(ctx, CoinsListParams{Sort: SortRank, Order: "up", Limit: 10}); err == nil {
		t.Error("invalid order should be rejected")
	}
}

// TestCoinsMapSendsCodesAndZeroLimit covers the mechanism the watchlist depends
// on: one credit for an arbitrary set of coins, at any rank.
func TestCoinsMapSendsCodesAndZeroLimit(t *testing.T) {
	c, cap := serve(t, 200, `[{"code":"HYPE","rank":731,"rate":74.34,"cap":null}]`)

	coins, err := c.CoinsMap(context.Background(), CoinsMapParams{
		Codes: []string{"BTC", "HYPE"}, Currency: "USD",
		Sort: SortRank, Order: OrderAscending, Meta: true,
	})
	if err != nil {
		t.Fatalf("CoinsMap: %v", err)
	}
	if len(coins) != 1 || coins[0].Rank != 731 {
		t.Fatalf("got %+v", coins)
	}
	// A null cap must survive as nil so the UI can render a dash.
	if coins[0].Cap != nil {
		t.Errorf("Cap = %v, want nil so the table renders '-'", *coins[0].Cap)
	}

	codes, ok := cap.bodyJSON["codes"].([]any)
	if !ok || len(codes) != 2 || codes[0] != "BTC" || codes[1] != "HYPE" {
		t.Errorf("codes = %v (raw: %s)", cap.bodyJSON["codes"], cap.rawBody)
	}
	if cap.bodyJSON["limit"] != float64(0) {
		t.Errorf("limit = %v, want 0 (the API defaults it to len(codes))", cap.bodyJSON["limit"])
	}
}

func TestCoinsMapRejectsBadParams(t *testing.T) {
	c, _ := serve(t, 200, `[]`)
	ctx := context.Background()

	if _, err := c.CoinsMap(ctx, CoinsMapParams{Sort: SortRank, Order: OrderAscending}); err == nil {
		t.Error("an empty code list should be rejected rather than spending a credit")
	}

	tooMany := make([]string, MaxListLimit+1)
	for i := range tooMany {
		tooMany[i] = "AAA"
	}
	if _, err := c.CoinsMap(ctx, CoinsMapParams{Codes: tooMany, Sort: SortRank, Order: OrderAscending}); err == nil {
		t.Error("over 100 codes must be rejected so the caller chunks instead")
	}
}

func TestCoinsSingleFillsInCode(t *testing.T) {
	// /coins/single omits the code from its response body.
	c, _ := serve(t, 200, `{"name":"Ethereum","rate":2417.78}`)

	coin, err := c.CoinsSingle(context.Background(), "USD", "ETH", true)
	if err != nil {
		t.Fatalf("CoinsSingle: %v", err)
	}
	if coin.Code != "ETH" {
		t.Errorf("Code = %q, want ETH — the client should backfill it", coin.Code)
	}
}

func TestCoinsSingleHistorySendsMillis(t *testing.T) {
	c, cap := serve(t, 200, `{"name":"Bitcoin","history":[{"date":1755000000000,"rate":77193.88}]}`)

	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	h, err := c.CoinsSingleHistory(context.Background(), "USD", "BTC", start, end, true)
	if err != nil {
		t.Fatalf("CoinsSingleHistory: %v", err)
	}
	if len(h.History) != 1 {
		t.Fatalf("history = %+v", h.History)
	}
	if cap.bodyJSON["start"] != float64(start.UnixMilli()) {
		t.Errorf("start = %v, want %v milliseconds", cap.bodyJSON["start"], start.UnixMilli())
	}
	if cap.bodyJSON["end"] != float64(end.UnixMilli()) {
		t.Errorf("end = %v, want %v milliseconds", cap.bodyJSON["end"], end.UnixMilli())
	}
}

func TestCoinsSingleHistoryRejectsInvertedRange(t *testing.T) {
	c, _ := serve(t, 200, `{}`)
	now := time.Now()
	if _, err := c.CoinsSingleHistory(context.Background(), "USD", "BTC", now, now.Add(-time.Hour), true); err == nil {
		t.Error("end before start should be rejected locally")
	}
}

func TestContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New("k", WithBaseURL(srv.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if _, err := c.Credits(ctx); err == nil {
		t.Fatal("want a context error")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

func TestNormalizeCode(t *testing.T) {
	for in, want := range map[string]string{
		"btc": "BTC", " eth ": "ETH", "HYPE": "HYPE", "": "",
	} {
		if got := NormalizeCode(in); got != want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
}
