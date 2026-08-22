package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/bartech/lcw-dashboard/internal/alerts"
	"github.com/bartech/lcw-dashboard/internal/credits"
	"github.com/bartech/lcw-dashboard/internal/history"
	"github.com/bartech/lcw-dashboard/internal/hub"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

// dispatchCoins starts one fetch. The in-flight flag is the entire double-fire
// defence: a tick arriving during a fetch is dropped, not queued.
func (c *Controller) dispatchCoins(ctx context.Context, why string) bool {
	if c.inFlightCoins {
		return false
	}
	if c.resolveInterval() <= 0 && why == "tick" {
		c.resetCoinTimer()
		return false
	}

	key := c.activeKey()
	cost := 1
	if key.View == snapshot.ViewFavourites {
		cost = c.watch.ChunkCount()
	}
	src := credits.SourcePoll
	if why == "manual" || why == "focus" {
		src = credits.SourceOnDemand
	}
	// The breaker is checked before reserving, so a failing upstream does not
	// churn the ledger.
	if c.guard.BreakerOpen() && why == "tick" {
		c.log.Debug("coin fetch skipped, circuit open", "until", c.guard.BreakerOpenUntil())
		c.resetCoinTimer()
		return false
	}
	if reason, ok := c.guard.Reserve(credits.KindCoinsList, cost, src); !ok {
		c.log.Debug("coin fetch refused", "why", why, "reason", reason, "key", key)
		c.resetCoinTimer()
		return false
	}

	c.inFlightCoins = true
	// Advance rotation now so the next tick fetches the next key even if this
	// fetch fails.
	if len(c.rotation) > 1 {
		c.rotIdx = (c.rotIdx + 1) % len(c.rotation)
	}

	go func() {
		coins, unknown, err := c.fetchCoins(ctx, key)
		c.resultCh <- result{kind: resCoins, key: key, coins: coins,
			credits: cost, unknown: unknown, err: err}
	}()
	return true
}

func (c *Controller) fetchCoins(ctx context.Context, key ViewKey) ([]snapshot.CoinRow, []string, error) {
	if key.View == snapshot.ViewFavourites {
		return c.fetchFavourites(ctx, key)
	}
	coins, err := c.client.CoinsList(ctx, lcw.CoinsListParams{
		Currency: key.Currency,
		Sort:     c.cfg.Coins.Sort,
		Order:    c.cfg.Coins.Order,
		Limit:    c.cfg.Coins.Limit,
		Meta:     c.cfg.Coins.Meta,
	})
	if err != nil {
		return nil, nil, err
	}
	return rows(coins), nil, nil
}

// fetchFavourites uses /coins/map, which ignores rank entirely. That is how a
// coin at rank 731 costs the same as Bitcoin and arrives in the same request.
func (c *Controller) fetchFavourites(ctx context.Context, key ViewKey) ([]snapshot.CoinRow, []string, error) {
	chunks := c.watch.Chunks()
	if len(chunks) == 0 {
		return []snapshot.CoinRow{}, nil, nil
	}
	var all []lcw.Coin
	var requested []string
	for _, chunk := range chunks {
		got, err := c.client.CoinsMap(ctx, lcw.CoinsMapParams{
			Codes:    chunk,
			Currency: key.Currency,
			Sort:     c.cfg.Coins.Sort,
			Order:    c.cfg.Coins.Order,
			Meta:     c.cfg.Coins.Meta,
		})
		if err != nil {
			return nil, nil, err
		}
		all = append(all, got...)
		requested = append(requested, chunk...)
	}
	returned := make([]string, 0, len(all))
	for _, coin := range all {
		returned = append(returned, coin.Code)
	}
	// The API omits codes it does not know rather than reporting them, so the
	// difference is computed here and surfaced instead of silently vanishing.
	unknown := c.watch.MarkUnknown(requested, returned)
	return rows(all), unknown, nil
}

func rows(coins []lcw.Coin) []snapshot.CoinRow {
	out := make([]snapshot.CoinRow, 0, len(coins))
	for _, coin := range coins {
		out = append(out, snapshot.RowFromCoin(coin))
	}
	return out
}

func (c *Controller) dispatchOverview(ctx context.Context) {
	if c.inFlightOverview || !c.cfg.Overview.Enabled {
		c.overviewTimer.Reset(c.overviewInterval())
		return
	}
	currency := c.activeKey().Currency
	if reason, ok := c.guard.Reserve(credits.KindOverview, 1, credits.SourcePoll); !ok {
		c.log.Debug("overview fetch refused", "reason", reason)
		c.overviewTimer.Reset(c.overviewInterval())
		return
	}
	c.inFlightOverview = true

	go func() {
		ov, err := c.client.Overview(ctx, currency)
		res := result{kind: resOverview, credits: 1, err: err,
			key: ViewKey{Currency: currency}}
		if err == nil {
			res.over = &snapshot.Overview{
				Currency: currency, AsOf: c.clk.Now(),
				Cap: ov.Cap, Volume: ov.Volume,
				Liquidity: ov.Liquidity, BTCDominance: ov.BTCDominance,
			}
		}
		c.resultCh <- res
	}()
}

func (c *Controller) handleResult(ctx context.Context, res result) {
	now := c.clk.Now()

	switch res.kind {
	case resOverview:
		c.inFlightOverview = false
		c.settle(res.err, res.credits, credits.KindOverview)
		if res.err == nil && res.over != nil {
			c.world.SetOverview(res.over.Currency, res.over)
			if err := c.hub.Broadcast(hub.EventOverview, "", res.over); err != nil {
				c.log.Warn("overview broadcast failed", "err", err)
			}
		} else if res.err != nil {
			c.markOverviewStale(res.key.Currency, res.err, now)
		}
		c.overviewTimer.Reset(c.overviewInterval())
		c.publishCredits()
		return
	}

	c.inFlightCoins = false
	kind := credits.KindCoinsList
	if res.key.View == snapshot.ViewFavourites {
		kind = credits.KindCoinsMap
	}
	c.settle(res.err, res.credits, kind)

	if res.err != nil {
		c.failures++
		c.lastError = wireError(res.err, now)
		if c.staleSince == nil {
			t := now
			c.staleSince = &t
		}
		// Stale data is kept and re-broadcast with a reason. It is never blanked.
		c.markCoinsStale(res.key, res.err, now)
		c.resetCoinTimer()
		c.publishStatus()
		c.publishCredits()
		return
	}

	c.failures = 0
	c.lastError = nil
	c.staleSince = nil
	c.lastCoinSuccess = now

	payload := &snapshot.Coins{
		View: res.key.View, Currency: res.key.Currency,
		Sort: string(c.cfg.Coins.Sort), Order: string(c.cfg.Coins.Order),
		AsOf: now, AgeMs: 0, CreditsUsed: res.credits,
		Rotating:     len(c.rotation) > 1,
		UnknownCodes: res.unknown,
		Coins:        res.coins,
	}
	c.world.SetCoins(res.key.String(), payload)
	if err := c.hub.Broadcast(hub.EventCoins, res.key.String(), payload); err != nil {
		c.log.Warn("coins broadcast failed", "err", err)
	}

	c.recordHistory(res.coins, now)
	c.evaluateAlerts(ctx, payload, now)

	c.resetCoinTimer()
	c.publishStatus()
	c.publishCredits()
}

// settle commits or refunds. A request that reached the API cost a credit even
// if it returned an error; only a pre-flight failure is refunded.
func (c *Controller) settle(err error, cost int, kind credits.Kind) {
	if err != nil && neverReachedAPI(err) {
		c.guard.Refund(cost)
	} else {
		c.guard.Commit(kind, cost)
	}
	if err == nil {
		c.guard.RecordSuccess()
		return
	}
	c.guard.RecordFailure()
	if c.guard.Classify(err) {
		c.log.Warn("budget state changed from upstream error", "state", c.guard.State(), "err", err)
	}
}

// neverReachedAPI distinguishes transport failures, which cost nothing, from API
// responses, which cost a credit even when they are errors.
func neverReachedAPI(err error) bool {
	if errors.Is(err, lcw.ErrNoAPIKey) || errors.Is(err, context.Canceled) {
		return true
	}
	var apiErr *lcw.APIError
	if errors.As(err, &apiErr) {
		return false
	}
	var nonJSON *lcw.ErrNonJSON
	if errors.As(err, &nonJSON) {
		// A body came back, so the request was served and billed.
		return false
	}
	return true
}

func (c *Controller) markCoinsStale(key ViewKey, err error, now time.Time) {
	prev := c.world.Load().Coins[key.String()]
	next := &snapshot.Coins{
		View: key.View, Currency: key.Currency,
		Sort: string(c.cfg.Coins.Sort), Order: string(c.cfg.Coins.Order),
		Stale: true, StaleSince: c.staleSince, Error: wireError(err, now),
		Rotating: len(c.rotation) > 1,
		Coins:    []snapshot.CoinRow{},
	}
	if prev != nil {
		next.Coins = prev.Coins
		next.AsOf = prev.AsOf
		next.AgeMs = now.Sub(prev.AsOf).Milliseconds()
		next.UnknownCodes = prev.UnknownCodes
	}
	c.world.SetCoins(key.String(), next)
	if err := c.hub.Broadcast(hub.EventCoins, key.String(), next); err != nil {
		c.log.Warn("stale coins broadcast failed", "err", err)
	}
}

func (c *Controller) markOverviewStale(currency string, err error, now time.Time) {
	prev := c.world.Load().Overview[currency]
	next := &snapshot.Overview{Currency: currency, Stale: true, Error: wireError(err, now)}
	if prev != nil {
		next.AsOf = prev.AsOf
		next.Cap, next.Volume = prev.Cap, prev.Volume
		next.Liquidity, next.BTCDominance = prev.Liquidity, prev.BTCDominance
	}
	c.world.SetOverview(currency, next)
	if err := c.hub.Broadcast(hub.EventOverview, "", next); err != nil {
		c.log.Warn("stale overview broadcast failed", "err", err)
	}
}

func (c *Controller) recordHistory(rows []snapshot.CoinRow, now time.Time) {
	if !c.hist.Enabled() {
		return
	}
	samples := make(map[string]history.Sample, len(rows))
	for _, r := range rows {
		if r.Rate == nil {
			continue
		}
		s := history.Sample{At: now, Rate: *r.Rate}
		if r.Volume != nil {
			s.Volume = *r.Volume
		}
		if r.Cap != nil {
			s.Cap = *r.Cap
		}
		samples[r.Code] = s
	}
	c.hist.Record(samples, now)
}

func (c *Controller) evaluateAlerts(ctx context.Context, payload *snapshot.Coins, now time.Time) {
	if !c.cfg.Alerts.Enabled || c.engine == nil {
		return
	}
	fired := c.engine.Evaluate(alerts.Snapshot{
		Currency:  payload.Currency,
		FetchedAt: now,
		Stale:     payload.Stale,
		Coins:     payload.Coins,
	}, c.watch.Codes())

	for _, a := range fired {
		if err := c.hub.Broadcast(hub.EventAlert, "", a); err != nil {
			c.log.Warn("alert broadcast failed", "err", err)
		}
	}
	if len(fired) > 0 && c.notify != nil {
		// Off the controller goroutine: a hung notification daemon must not stall
		// the poll loop.
		go c.notify.Notify(ctx, fired)
	}
}

func wireError(err error, now time.Time) *snapshot.WireError {
	if err == nil {
		return nil
	}
	var apiErr *lcw.APIError
	if errors.As(err, &apiErr) {
		return &snapshot.WireError{
			Code: apiErr.Code, Status: apiErr.Status,
			Description: apiErr.Description, At: now,
		}
	}
	return &snapshot.WireError{Status: "request failed", Description: err.Error(), At: now}
}
