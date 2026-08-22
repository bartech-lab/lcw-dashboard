package scheduler

import (
	"context"
	"time"

	"github.com/bartech/lcw-dashboard/internal/credits"
	"github.com/bartech/lcw-dashboard/internal/hub"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

// Bootstrap runs the startup probes. Every step is best-effort: the HTTP server
// is already listening, so a dead upstream or a missing key still yields a UI
// that explains itself rather than a process that refuses to start.
func (c *Controller) Bootstrap(ctx context.Context) {
	// /status needs no key and costs nothing, so it is the first thing tried.
	if err := c.client.Status(ctx); err != nil {
		c.log.Warn("Live Coin Watch status check failed", "err", err)
	} else {
		c.log.Info("upstream reachable")
	}

	if !c.client.HasKey() {
		c.guard.SetNoKey()
		c.log.Warn("no API key configured; serving a setup page only",
			"hint", "put LCW_API_KEY in "+c.envPath)
		c.publishStatus()
		return
	}

	if cr, err := c.client.Credits(ctx); err != nil {
		c.log.Warn("credits probe failed", "err", err)
		c.settle(err, 1, credits.KindCredits)
	} else {
		c.guard.Commit(credits.KindCredits, 1)
		c.guard.Ledger().Reconcile(cr.DailyCreditsRemaining, cr.DailyCreditsLimit)
		c.guard.ClearKeyFailure()
		c.log.Info("credits", "remaining", cr.DailyCreditsRemaining, "limit", cr.DailyCreditsLimit)
	}
	c.publishCredits()

	c.loadFiats(ctx)
	c.send(command{kind: cmdReconciled})
	c.send(command{kind: cmdPrime})
}

func (c *Controller) loadFiats(ctx context.Context) {
	if c.fiats != nil && c.clk.Since(c.fiats.CachedAt) < c.cfg.Currency.FiatsTTL.D() {
		if err := c.hub.Broadcast(hub.EventFiats, "", c.fiats); err != nil {
			c.log.Warn("fiats broadcast failed", "err", err)
		}
		c.world.SetFiats(c.fiats)
		return
	}
	if reason, ok := c.guard.Reserve(credits.KindFiats, 1, credits.SourceProbe); !ok {
		c.log.Debug("fiat list fetch refused", "reason", reason)
		return
	}
	list, err := c.client.FiatsAll(ctx)
	c.settle(err, 1, credits.KindFiats)
	if err != nil {
		c.log.Warn("fiat list fetch failed", "err", err)
		return
	}
	c.SetFiats(list)
}

// SetFiats applies the allowlist and denylist, so the picker only offers what
// the config permits.
func (c *Controller) SetFiats(list []lcw.Fiat) {
	allow := make(map[string]bool, len(c.cfg.Currency.Allowlist))
	for _, code := range c.cfg.Currency.Allowlist {
		allow[lcw.NormalizeCode(code)] = true
	}
	deny := make(map[string]bool, len(c.cfg.Currency.Denylist))
	for _, code := range c.cfg.Currency.Denylist {
		deny[lcw.NormalizeCode(code)] = true
	}

	out := make([]snapshot.Fiat, 0, len(list))
	for _, f := range list {
		code := lcw.NormalizeCode(f.Code)
		if deny[code] {
			continue
		}
		if len(allow) > 0 && !allow[code] {
			continue
		}
		out = append(out, snapshot.Fiat{Code: f.Code, Name: f.Name, Symbol: f.Symbol, Flag: f.Flag})
	}
	f := &snapshot.Fiats{Fiats: out, CachedAt: c.clk.Now()}
	c.fiats = f
	c.world.SetFiats(f)
	if err := c.hub.Broadcast(hub.EventFiats, "", f); err != nil {
		c.log.Warn("fiats broadcast failed", "err", err)
	}
}

func (c *Controller) Fiats() *snapshot.Fiats { return c.fiats }

// RunReconcile keeps the local ledger honest against the API's own count.
func (c *Controller) RunReconcile(ctx context.Context) {
	interval := c.cfg.Credits.ReconcileInterval.D()
	if interval <= 0 {
		return
	}
	t := c.clk.NewTimer(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			c.reconcileOnce(ctx)
			t.Reset(interval)
		}
	}
}

func (c *Controller) reconcileOnce(ctx context.Context) {
	state := c.guard.State()
	if state == credits.StateNoKey || state == credits.StateAuthFailed {
		return
	}
	if _, ok := c.guard.Reserve(credits.KindCredits, 1, credits.SourceProbe); !ok {
		return
	}
	cr, err := c.client.Credits(ctx)
	c.settle(err, 1, credits.KindCredits)
	if err != nil {
		c.log.Debug("reconcile failed", "err", err)
		return
	}
	c.guard.Ledger().Reconcile(cr.DailyCreditsRemaining, cr.DailyCreditsLimit)
	if cr.DailyCreditsRemaining > 0 {
		c.guard.ClearExhausted()
	}
	if drift := c.guard.Ledger().Report().Drift; drift > 100 {
		c.log.Warn("credit drift detected; another client may share this API key",
			"drift", drift)
	}
	c.send(command{kind: cmdReconciled})
}

// RunIndex builds the search index after a short delay, then daily. Pages are
// reserved individually so a tightening budget stops the walk cleanly.
func (c *Controller) RunIndex(ctx context.Context) {
	if !c.cfg.SearchIndex.Enabled || c.index == nil {
		return
	}
	t := c.clk.NewTimer(c.cfg.SearchIndex.BuildDelay.D())
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			if c.index.Stale() {
				c.buildIndex(ctx)
			}
			next := time.Until(c.index.NextRefresh())
			if next <= 0 {
				next = 24 * time.Hour
			}
			t.Reset(next)
		}
	}
}

func (c *Controller) buildIndex(ctx context.Context) {
	if !c.guard.State().Polls() {
		return
	}
	pages := c.index.Pages()
	c.log.Info("building search index", "coins", c.cfg.SearchIndex.Coins, "credits", pages)

	err := c.index.Build(ctx, func(ctx context.Context, offset, limit int) ([]lcw.Coin, error) {
		// The index walks many pages in a row, so it can outrun the minimum
		// request gap. Waiting is correct here: a refusal for pacing is not a
		// reason to discard a whole rebuild.
		var reason credits.Reason
		var ok bool
		for attempt := 0; attempt < indexReserveAttempts; attempt++ {
			if reason, ok = c.guard.Reserve(credits.KindIndex, 1, credits.SourcePoll); ok {
				break
			}
			if reason != credits.ReasonMinGap {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.cfg.Credits.MinRequestGap.D() / 2):
			}
		}
		if !ok {
			return nil, &indexHalted{reason: string(reason)}
		}
		coins, err := c.client.CoinsList(ctx, lcw.CoinsListParams{
			Currency: c.cfg.Currency.Default,
			Sort:     lcw.SortRank,
			Order:    lcw.OrderAscending,
			Offset:   offset,
			Limit:    limit,
			// The index needs only code, name, symbol and rank.
			Meta: true,
		})
		c.settle(err, 1, credits.KindIndex)
		return coins, err
	})
	if err != nil {
		c.log.Warn("search index build stopped", "err", err)
		return
	}
	st := c.index.Status()
	c.log.Info("search index ready", "coins", st.Coins)
	c.send(command{kind: cmdIndexDone})
}

// indexReserveAttempts bounds the pacing wait, so a genuinely exhausted budget
// still stops the build rather than looping.
const indexReserveAttempts = 8

type indexHalted struct{ reason string }

func (e *indexHalted) Error() string { return "index build halted: " + e.reason }

// RunHistoryFlush persists dirty rings on a timer, so a crash loses at most one
// interval of samples.
func (c *Controller) RunHistoryFlush(ctx context.Context) {
	if !c.hist.Enabled() {
		return
	}
	interval := c.cfg.History.FlushInterval.D()
	if interval <= 0 {
		interval = time.Minute
	}
	t := c.clk.NewTimer(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			if n := c.hist.Flush(); n > 0 {
				c.log.Debug("flushed history", "rings", n)
			}
			t.Reset(interval)
		}
	}
}

// Warm publishes restored last-good data before the first fetch lands, so the
// first paint is never an empty table.
func (c *Controller) Warm(coins map[string]*snapshot.Coins, overview map[string]*snapshot.Overview) {
	now := c.clk.Now()
	for key, payload := range coins {
		if payload == nil {
			continue
		}
		payload.Stale = true
		payload.AgeMs = now.Sub(payload.AsOf).Milliseconds()
		c.world.SetCoins(key, payload)
	}
	for cur, payload := range overview {
		if payload == nil {
			continue
		}
		payload.Stale = true
		c.world.SetOverview(cur, payload)
	}
}

// LastGood is the persisted snapshot written on shutdown.
type LastGood struct {
	Coins    map[string]*snapshot.Coins    `json:"coins"`
	Overview map[string]*snapshot.Overview `json:"overview"`
	Fiats    *snapshot.Fiats               `json:"fiats"`
}

func (c *Controller) LastGood() LastGood {
	w := c.world.Load()
	return LastGood{Coins: w.Coins, Overview: w.Overview, Fiats: w.Fiats}
}
