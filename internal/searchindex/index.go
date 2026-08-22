// Package searchindex serves coin search from memory.
//
// The API has no search endpoint, so the index is built by walking /coins/list
// in pages. That costs one credit per page, which is why it happens once a day
// and is cached to disk rather than rebuilt on demand.
package searchindex

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/config"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
	"github.com/bartech/lcw-dashboard/internal/store"
)

type Coin struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
	Rank   int    `json:"rank"`
	PNG32  string `json:"png32"`
	// lowered fields are precomputed so a keystroke does not allocate.
	lowCode string
	lowName string
}

type persisted struct {
	Coins   []Coin    `json:"coins"`
	BuiltAt time.Time `json:"builtAt"`
}

type Index struct {
	mu       sync.RWMutex
	cfg      config.SearchIndex
	clk      clock.Clock
	path     string
	log      *slog.Logger
	coins    []Coin
	builtAt  time.Time
	building bool
}

func New(cfg config.SearchIndex, clk clock.Clock, path string, log *slog.Logger) *Index {
	return &Index{cfg: cfg, clk: clk, path: path, log: log}
}

func (ix *Index) Load() {
	var p persisted
	found, err := store.ReadJSON(ix.path, &p)
	if err != nil {
		ix.log.Warn("search index cache unreadable, will rebuild", "err", err)
		return
	}
	if !found || len(p.Coins) == 0 {
		return
	}
	for i := range p.Coins {
		p.Coins[i].prepare()
	}
	ix.mu.Lock()
	ix.coins = p.Coins
	ix.builtAt = p.BuiltAt
	ix.mu.Unlock()
}

func (c *Coin) prepare() {
	c.lowCode = strings.ToLower(c.Code)
	c.lowName = strings.ToLower(c.Name)
}

func (ix *Index) Status() snapshot.IndexStatus {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return snapshot.IndexStatus{
		Ready:    len(ix.coins) > 0,
		Coins:    len(ix.coins),
		BuiltAt:  ix.builtAt,
		Building: ix.building,
	}
}

// Stale reports whether a rebuild is due.
func (ix *Index) Stale() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if len(ix.coins) == 0 {
		return true
	}
	return ix.clk.Since(ix.builtAt) > 24*time.Hour
}

// Pages is the credit cost of a rebuild.
func (ix *Index) Pages() int {
	if ix.cfg.PageSize <= 0 {
		return 0
	}
	return (ix.cfg.Coins + ix.cfg.PageSize - 1) / ix.cfg.PageSize
}

// Fetcher lets the scheduler own credit accounting; the index only asks for a
// page and is told no when the budget refuses.
type Fetcher func(ctx context.Context, offset, limit int) ([]lcw.Coin, error)

// Build walks pages into a temp slice and swaps only on success. A partial build
// is discarded, so an interrupted rebuild leaves the previous index intact
// rather than a half-populated one.
func (ix *Index) Build(ctx context.Context, fetch Fetcher) error {
	ix.mu.Lock()
	if ix.building {
		ix.mu.Unlock()
		return nil
	}
	ix.building = true
	ix.mu.Unlock()

	defer func() {
		ix.mu.Lock()
		ix.building = false
		ix.mu.Unlock()
	}()

	pages := ix.Pages()
	built := make([]Coin, 0, ix.cfg.Coins)

	for p := 0; p < pages; p++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		limit := ix.cfg.PageSize
		if remaining := ix.cfg.Coins - len(built); remaining < limit {
			limit = remaining
		}
		if limit <= 0 {
			break
		}
		coins, err := fetch(ctx, p*ix.cfg.PageSize, limit)
		if err != nil {
			return fmt.Errorf("index page %d/%d: %w", p+1, pages, err)
		}
		if len(coins) == 0 {
			break
		}
		for _, c := range coins {
			e := Coin{Code: c.Code, Name: c.Name, Symbol: c.Symbol, Rank: c.Rank, PNG32: c.PNG32}
			e.prepare()
			built = append(built, e)
		}
		if p < pages-1 && ix.cfg.PageGap.D() > 0 {
			ix.clk.Sleep(ix.cfg.PageGap.D())
		}
	}

	if len(built) == 0 {
		return fmt.Errorf("index build produced no coins")
	}
	sort.Slice(built, func(i, j int) bool { return built[i].Rank < built[j].Rank })

	ix.mu.Lock()
	ix.coins = built
	ix.builtAt = ix.clk.Now()
	ix.mu.Unlock()

	if err := store.WriteJSONAtomic(ix.path, persisted{Coins: built, BuiltAt: ix.clk.Now()}); err != nil {
		// The in-memory index is usable; only the cache write failed.
		ix.log.Warn("caching search index failed", "err", err)
	}
	return nil
}

type Result struct {
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	Symbol string  `json:"symbol"`
	Rank   int     `json:"rank"`
	PNG32  string  `json:"png32"`
	Score  float64 `json:"score"`
}

// Search ranks by match quality then by rank. Exact code beats code prefix beats
// name prefix beats substring, so typing "bit" puts BTC first.
func (ix *Index) Search(query string) []Result {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	ix.mu.RLock()
	coins := ix.coins
	ix.mu.RUnlock()

	out := make([]Result, 0, ix.cfg.MaxResults)
	for i := range coins {
		c := &coins[i]
		score := score(c, q)
		if score < ix.cfg.MinScore {
			continue
		}
		out = append(out, Result{
			Code: c.Code, Name: c.Name, Symbol: c.Symbol,
			Rank: c.Rank, PNG32: c.PNG32, Score: score,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Rank < out[j].Rank
	})
	if ix.cfg.MaxResults > 0 && len(out) > ix.cfg.MaxResults {
		out = out[:ix.cfg.MaxResults]
	}
	return out
}

func score(c *Coin, q string) float64 {
	switch {
	case c.lowCode == q:
		return 1.0
	case strings.HasPrefix(c.lowCode, q):
		return 0.9
	case c.lowName == q:
		return 0.85
	case strings.HasPrefix(c.lowName, q):
		return 0.8
	case wordPrefix(c.lowName, q):
		return 0.7
	case strings.Contains(c.lowCode, q):
		return 0.6
	case strings.Contains(c.lowName, q):
		return 0.5
	}
	return 0
}

// wordPrefix matches the start of any word, so "coin" finds "Live Coin Watch".
func wordPrefix(name, q string) bool {
	for _, w := range strings.Fields(name) {
		if strings.HasPrefix(w, q) {
			return true
		}
	}
	return false
}

// Lookup resolves an exact code, used to validate a watchlist addition without
// spending a credit.
func (ix *Index) Lookup(code string) (Coin, bool) {
	code = strings.ToLower(strings.TrimSpace(code))
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for i := range ix.coins {
		if ix.coins[i].lowCode == code {
			return ix.coins[i], true
		}
	}
	return Coin{}, false
}

// NextRefresh returns the next occurrence of the configured HH:MM UTC.
func (ix *Index) NextRefresh() time.Time {
	now := ix.clk.Now().UTC()
	hhmm, err := time.Parse("15:04", ix.cfg.RefreshAt)
	if err != nil {
		return now.Add(24 * time.Hour)
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), hhmm.Hour(), hhmm.Minute(), 0, 0, time.UTC)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}
