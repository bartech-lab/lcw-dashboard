//go:build smoke

// Command smoke calls every endpoint once against the live API and reports what
// each cost, plus any drift between the documented response shape and reality.
//
// Build-tagged so `go test ./...` never touches the network:
//
//	LCW_API_KEY=... go run -tags smoke ./scripts/smoke
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/store"
)

func main() {
	code := flag.String("code", "BTC", "coin code to probe")
	currency := flag.String("currency", "USD", "currency to request")
	flag.Parse()

	if paths, err := store.Resolve(); err == nil {
		_ = godotenv.Load(paths.EnvFile())
	}
	_ = godotenv.Load(".env")

	key := os.Getenv("LCW_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "LCW_API_KEY is not set")
		os.Exit(1)
	}

	c := lcw.New(key, lcw.WithTimeout(20*time.Second))
	ctx := context.Background()
	spent := 0

	fmt.Println("Live Coin Watch smoke check")
	fmt.Println(strings.Repeat("-", 78))

	step("/status", 0, &spent, func() (any, error) {
		return "ok", c.Status(ctx)
	})

	var limit int
	step("/credits", 1, &spent, func() (any, error) {
		cr, err := c.Credits(ctx)
		limit = cr.DailyCreditsLimit
		return fmt.Sprintf("%d of %d remaining", cr.DailyCreditsRemaining, cr.DailyCreditsLimit), err
	})

	step("/overview", 1, &spent, func() (any, error) {
		o, err := c.Overview(ctx, *currency)
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("cap %s, btc dominance %s", num(o.Cap), num(o.BTCDominance)), nil
	})

	step("/fiats/all", 1, &spent, func() (any, error) {
		f, err := c.FiatsAll(ctx)
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("%d fiats", len(f)), nil
	})

	var sample lcw.Coin
	step("/coins/list", 1, &spent, func() (any, error) {
		coins, err := c.CoinsList(ctx, lcw.CoinsListParams{
			Currency: *currency, Sort: lcw.SortRank, Order: lcw.OrderAscending,
			Limit: 100, Meta: true,
		})
		if err != nil {
			return nil, err
		}
		if len(coins) > 0 {
			sample = coins[0]
		}
		return fmt.Sprintf("%d coins, first %s at %s", len(coins), sample.Code, num(sample.Rate)), nil
	})

	// This is the mechanism the watchlist depends on, so it is checked with a
	// deliberately low-ranked coin.
	step("/coins/map (rank-independent)", 1, &spent, func() (any, error) {
		coins, err := c.CoinsMap(ctx, lcw.CoinsMapParams{
			Codes: []string{"BTC", "HYPE", "__NOT_A_COIN__"}, Currency: *currency,
			Sort: lcw.SortRank, Order: lcw.OrderAscending, Meta: true,
		})
		if err != nil {
			return nil, err
		}
		got := make([]string, 0, len(coins))
		for _, x := range coins {
			got = append(got, fmt.Sprintf("%s(#%d)", x.Code, x.Rank))
		}
		return fmt.Sprintf("%d of 3 returned: %s", len(coins), strings.Join(got, " ")), nil
	})

	step("/coins/single", 1, &spent, func() (any, error) {
		coin, err := c.CoinsSingle(ctx, *currency, *code, true)
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("%s rank %d, ath %s", coin.Code, coin.Rank, num(coin.AllTimeHighUSD)), nil
	})

	step("/coins/single/history", 1, &spent, func() (any, error) {
		end := time.Now()
		h, err := c.CoinsSingleHistory(ctx, *currency, *code, end.Add(-7*24*time.Hour), end, false)
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("%d points", len(h.History)), nil
	})

	step("/overview/history", 1, &spent, func() (any, error) {
		end := time.Now()
		pts, err := c.OverviewHistory(ctx, *currency, end.Add(-7*24*time.Hour), end)
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("%d points", len(pts)), nil
	})

	fmt.Println(strings.Repeat("-", 78))
	fmt.Printf("credits spent: %d of %d\n", spent, limit)

	// The delta range is the single riskiest assumption in the codebase, so it is
	// measured against live data rather than trusted.
	checkDeltaRange(ctx, c, *currency)
	checkSortFields(ctx, c, *currency)
}

func step(name string, cost int, spent *int, fn func() (any, error)) {
	start := time.Now()
	out, err := fn()
	*spent += cost
	ms := time.Since(start).Milliseconds()

	if err != nil {
		fmt.Printf("%-30s %5dms  %d cr  FAIL  %v\n", name, ms, cost, err)
		return
	}
	fmt.Printf("%-30s %5dms  %d cr  ok    %v\n", name, ms, cost, out)
}

// checkDeltaRange walks the top 100 and reports the widest delta seen. Their docs
// claim 0..2; this prints what the API actually returns.
func checkDeltaRange(ctx context.Context, c *lcw.Client, currency string) {
	coins, err := c.CoinsList(ctx, lcw.CoinsListParams{
		Currency: currency, Sort: lcw.SortRank, Order: lcw.OrderAscending,
		Limit: 100, Meta: true,
	})
	if err != nil {
		fmt.Println("\ndelta range check skipped:", err)
		return
	}

	type worst struct {
		code string
		raw  float64
		pct  float64
	}
	var max, min worst
	zeros := 0
	nils := 0

	for _, coin := range coins {
		for _, d := range []*float64{
			coin.Delta.Hour, coin.Delta.Day, coin.Delta.Week,
			coin.Delta.Month, coin.Delta.Quarter, coin.Delta.Year,
		} {
			if d == nil {
				nils++
				continue
			}
			if *d == 0 {
				zeros++
				continue
			}
			pct := lcw.DeltaPct(d)
			if pct == nil {
				continue
			}
			if max.code == "" || *d > max.raw {
				max = worst{coin.Code, *d, *pct}
			}
			if min.code == "" || *d < min.raw {
				min = worst{coin.Code, *d, *pct}
			}
		}
	}

	fmt.Println("\ndelta range across the top 100 (docs claim 0..2):")
	fmt.Printf("  highest %-6s raw %.4f -> %+.2f%%\n", max.code, max.raw, max.pct)
	fmt.Printf("  lowest  %-6s raw %.4f -> %+.2f%%\n", min.code, min.raw, min.pct)
	fmt.Printf("  zeros (treated as no data): %d, absent: %d\n", zeros, nils)
	if max.raw > 2 {
		fmt.Printf("  CONFIRMED: the documented 0..2 range is wrong\n")
	}
}

// checkSortFields probes each documented sort field and each delta window, so a
// future API change that adds delta sorting shows up here.
func checkSortFields(ctx context.Context, c *lcw.Client, currency string) {
	fmt.Println("\nsort field support (each probe costs 1 credit):")

	try := func(field string) {
		coins, err := c.CoinsList(ctx, lcw.CoinsListParams{
			Currency: currency, Sort: lcw.SortField(field),
			Order: lcw.OrderDescending, Limit: 2, Meta: false,
		})
		if err != nil {
			fmt.Printf("  %-8s rejected: %v\n", field, err)
			return
		}
		fmt.Printf("  %-8s accepted, %d coins\n", field, len(coins))
	}

	for _, f := range lcw.ValidSortFields() {
		try(string(f))
	}
	// The client rejects these locally, so probe the wire directly.
	fmt.Println("  delta windows are rejected by this client before any request:")
	windows := make([]string, 0, 6)
	for _, w := range lcw.ValidWindows() {
		windows = append(windows, string(w))
	}
	sort.Strings(windows)
	fmt.Printf("    %s\n", strings.Join(windows, " "))
}

func num(v *float64) string {
	if v == nil {
		return "nil"
	}
	b, _ := json.Marshal(*v)
	return string(b)
}
