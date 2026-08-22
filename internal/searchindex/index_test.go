package searchindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/config"
	"github.com/bartech/lcw-dashboard/internal/lcw"
)

var noon = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func newIndex(t *testing.T, tweak func(*config.SearchIndex)) (*Index, *clock.Fake) {
	t.Helper()
	cfg := config.Default().SearchIndex
	cfg.Coins = 300
	cfg.PageSize = 100
	cfg.PageGap = 0
	if tweak != nil {
		tweak(&cfg)
	}
	clk := clock.NewFake(noon)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, clk, filepath.Join(t.TempDir(), "index.json"), log), clk
}

// fetcher returns synthetic pages plus the fixed coins tests search for.
func fetcher(pages int, fail int) (Fetcher, *int) {
	calls := 0
	return func(_ context.Context, offset, limit int) ([]lcw.Coin, error) {
		calls++
		if fail > 0 && calls == fail {
			return nil, errors.New("upstream said no")
		}
		if offset/100 >= pages {
			return nil, nil
		}
		out := make([]lcw.Coin, 0, limit)
		if offset == 0 {
			out = append(out,
				lcw.Coin{Code: "BTC", Name: "Bitcoin", Symbol: "₿", Rank: 1},
				lcw.Coin{Code: "ETH", Name: "Ethereum", Rank: 2},
				lcw.Coin{Code: "HYPE", Name: "Hyperliquid", Rank: 731},
				lcw.Coin{Code: "HFUN", Name: "Hyperliquid Fun", Rank: 2104},
				lcw.Coin{Code: "BITB", Name: "Bitcoin Bull", Rank: 900},
			)
		}
		for i := len(out); i < limit; i++ {
			n := offset + i
			out = append(out, lcw.Coin{
				Code: fmt.Sprintf("C%04d", n), Name: fmt.Sprintf("Coin %d", n), Rank: n + 10,
			})
		}
		return out, nil
	}, &calls
}

func build(t *testing.T, ix *Index, pages, fail int) error {
	t.Helper()
	f, _ := fetcher(pages, fail)
	return ix.Build(context.Background(), f)
}

func TestPagesIsTheCreditCost(t *testing.T) {
	ix, _ := newIndex(t, func(c *config.SearchIndex) { c.Coins = 2000; c.PageSize = 100 })
	if got := ix.Pages(); got != 20 {
		t.Errorf("Pages = %d, want 20 for 2000 coins in pages of 100", got)
	}
}

func TestBuildThenSearch(t *testing.T) {
	ix, _ := newIndex(t, nil)
	if err := build(t, ix, 3, 0); err != nil {
		t.Fatal(err)
	}
	st := ix.Status()
	if !st.Ready || st.Coins != 300 {
		t.Fatalf("status = %+v, want 300 coins ready", st)
	}
}

// Exact code beats code prefix beats name prefix beats substring, so typing
// "bit" puts BTC first.
func TestSearchRanking(t *testing.T) {
	ix, _ := newIndex(t, nil)
	if err := build(t, ix, 1, 0); err != nil {
		t.Fatal(err)
	}

	if got := ix.Search("btc"); len(got) == 0 || got[0].Code != "BTC" {
		t.Errorf("search btc = %+v, want BTC first", got)
	}
	if got := ix.Search("bitcoin"); len(got) == 0 || got[0].Code != "BTC" {
		t.Errorf("search bitcoin = %+v, want BTC first", got)
	}
	res := ix.Search("hyper")
	if len(res) < 2 {
		t.Fatalf("search hyper = %+v, want both Hyperliquid coins", res)
	}
	// Rank breaks ties, so #731 comes before #2104.
	if res[0].Code != "HYPE" {
		t.Errorf("search hyper first = %s, want HYPE (better rank)", res[0].Code)
	}
}

// "coin" should find "Hyperliquid Fun" style names by word, not just prefix.
func TestSearchMatchesAnyWordStart(t *testing.T) {
	ix, _ := newIndex(t, nil)
	if err := build(t, ix, 1, 0); err != nil {
		t.Fatal(err)
	}
	if got := ix.Search("fun"); len(got) == 0 {
		t.Error("search fun should match Hyperliquid Fun by word start")
	}
}

func TestSearchIsCaseInsensitiveAndTrims(t *testing.T) {
	ix, _ := newIndex(t, nil)
	build(t, ix, 1, 0)
	for _, q := range []string{"BTC", "btc", "  Btc  "} {
		if got := ix.Search(q); len(got) == 0 || got[0].Code != "BTC" {
			t.Errorf("search %q failed: %+v", q, got)
		}
	}
}

func TestSearchEmptyQueryReturnsNothing(t *testing.T) {
	ix, _ := newIndex(t, nil)
	build(t, ix, 1, 0)
	if got := ix.Search("   "); len(got) != 0 {
		t.Errorf("blank query returned %d results", len(got))
	}
}

func TestSearchRespectsMaxResults(t *testing.T) {
	ix, _ := newIndex(t, func(c *config.SearchIndex) { c.MaxResults = 3 })
	build(t, ix, 2, 0)
	if got := ix.Search("coin"); len(got) > 3 {
		t.Errorf("got %d results, want at most 3", len(got))
	}
}

// An interrupted rebuild must leave the previous index intact rather than a
// half-populated one.
func TestPartialBuildIsDiscarded(t *testing.T) {
	ix, _ := newIndex(t, nil)
	if err := build(t, ix, 3, 0); err != nil {
		t.Fatal(err)
	}
	before := ix.Status().Coins

	if err := build(t, ix, 3, 2); err == nil {
		t.Fatal("a failing page should return an error")
	}
	if got := ix.Status().Coins; got != before {
		t.Errorf("index now holds %d coins, want the previous %d", got, before)
	}
	if got := ix.Search("btc"); len(got) == 0 {
		t.Error("the previous index should still be searchable")
	}
}

func TestBuildStopsAtAnEmptyPage(t *testing.T) {
	ix, _ := newIndex(t, func(c *config.SearchIndex) { c.Coins = 1000 })
	f, calls := fetcher(2, 0)
	if err := ix.Build(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	// Two pages of data plus one empty probe, not the ten the config allows.
	if *calls > 3 {
		t.Errorf("made %d calls, want it to stop once pages ran out", *calls)
	}
	if got := ix.Status().Coins; got != 200 {
		t.Errorf("Coins = %d, want 200", got)
	}
}

func TestBuildRespectsContextCancellation(t *testing.T) {
	ix, _ := newIndex(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, _ := fetcher(3, 0)
	if err := ix.Build(ctx, f); err == nil {
		t.Error("want a context error")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	cfg := config.Default().SearchIndex
	cfg.Coins = 100
	cfg.PageGap = 0
	clk := clock.NewFake(noon)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "index.json")

	a := New(cfg, clk, path, log)
	f, _ := fetcher(1, 0)
	if err := a.Build(context.Background(), f); err != nil {
		t.Fatal(err)
	}

	b := New(cfg, clk, path, log)
	b.Load()
	if got := b.Status().Coins; got != 100 {
		t.Fatalf("Coins = %d after reload, want 100", got)
	}
	// Lowered fields are precomputed, so search must work after a reload.
	if got := b.Search("bitcoin"); len(got) == 0 || got[0].Code != "BTC" {
		t.Errorf("search after reload = %+v", got)
	}
}

func TestStaleAfterADay(t *testing.T) {
	ix, clk := newIndex(t, nil)
	if ix.Stale() != true {
		t.Error("an empty index is stale")
	}
	build(t, ix, 1, 0)
	if ix.Stale() {
		t.Error("a fresh index is not stale")
	}
	clk.Advance(25 * time.Hour)
	if !ix.Stale() {
		t.Error("should be stale after a day")
	}
}

func TestLookupResolvesAnExactCode(t *testing.T) {
	ix, _ := newIndex(t, nil)
	build(t, ix, 1, 0)

	if c, ok := ix.Lookup("hype"); !ok || c.Rank != 731 {
		t.Errorf("Lookup(hype) = %+v, %v", c, ok)
	}
	if _, ok := ix.Lookup("NOT_A_COIN"); ok {
		t.Error("an unknown code should not resolve")
	}
}

func TestNextRefreshIsTheConfiguredUTCTime(t *testing.T) {
	ix, clk := newIndex(t, func(c *config.SearchIndex) { c.RefreshAt = "00:15" })
	// Noon UTC, so the next 00:15 is tomorrow.
	want := time.Date(2026, 8, 23, 0, 15, 0, 0, time.UTC)
	if got := ix.NextRefresh(); !got.Equal(want) {
		t.Errorf("NextRefresh = %s, want %s", got, want)
	}

	clk.Set(time.Date(2026, 8, 22, 0, 5, 0, 0, time.UTC))
	want = time.Date(2026, 8, 22, 0, 15, 0, 0, time.UTC)
	if got := ix.NextRefresh(); !got.Equal(want) {
		t.Errorf("NextRefresh = %s, want %s", got, want)
	}
}

func TestConcurrentSearchDuringBuild(t *testing.T) {
	ix, _ := newIndex(t, nil)
	build(t, ix, 1, 0)

	done := make(chan struct{})
	go func() {
		build(t, ix, 3, 0)
		close(done)
	}()
	for i := 0; i < 200; i++ {
		ix.Search("btc")
		ix.Status()
	}
	<-done
}
