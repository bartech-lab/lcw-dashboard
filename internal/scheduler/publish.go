package scheduler

import (
	"github.com/bartech/lcw-dashboard/internal/credits"
	"github.com/bartech/lcw-dashboard/internal/hub"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

func (c *Controller) publishStatus() {
	total, visible := c.presence.counts()
	keys := make([]string, 0, len(c.rotation))
	for _, k := range c.rotation {
		keys = append(keys, k.String())
	}

	st := &snapshot.Status{
		PollState:           c.pollState(visible, total),
		ActiveViewKey:       c.activeKey().String(),
		Rotating:            len(c.rotation) > 1,
		RotationKeys:        keys,
		IntervalMs:          c.interval.Milliseconds(),
		ConsecutiveFailures: c.failures,
		LastError:           c.lastError,
		VisibleClients:      visible,
		TotalClients:        total,
		ChunkPenalty:        c.watch.ChunkCount(),
		Revision:            c.revision,
	}
	if !c.nextTickAt.IsZero() {
		t := c.nextTickAt
		st.NextTickAt = &t
	}
	if !c.lastCoinSuccess.IsZero() {
		t := c.lastCoinSuccess
		st.LastSuccessAt = &t
	}
	if c.index != nil {
		st.SearchIndex = c.index.Status()
	}

	switch c.guard.State() {
	case credits.StateNoKey:
		st.DegradedReason = "no_api_key"
		st.SetupHint = "Create " + c.envPath + " containing LCW_API_KEY=<your key>"
	case credits.StateAuthFailed:
		st.DegradedReason = "api_key_rejected"
		st.SetupHint = "Check LCW_API_KEY in " + c.envPath
	case credits.StateExhausted:
		st.DegradedReason = "daily_credits_exhausted"
	case credits.StateCritical:
		st.DegradedReason = "credit_budget_critical"
	case credits.StateConserve:
		st.DegradedReason = "credit_budget_conserving"
	}

	c.world.SetStatus(st)
	if err := c.hub.Broadcast(hub.EventStatus, "", st); err != nil {
		c.log.Warn("status broadcast failed", "err", err)
	}
}

func (c *Controller) pollState(visible, total int) snapshot.PollState {
	switch c.guard.State() {
	case credits.StateNoKey:
		return snapshot.PollNoKey
	case credits.StateAuthFailed:
		return snapshot.PollAuthFailed
	case credits.StateExhausted:
		return snapshot.PollExhausted
	case credits.StateCritical:
		return snapshot.PollCritical
	case credits.StateConserve:
		return snapshot.PollConserve
	}
	if c.lastCoinSuccess.IsZero() {
		return snapshot.PollInitializing
	}
	if visible > 0 {
		return snapshot.PollActive
	}
	_ = total
	return snapshot.PollIdle
}

func (c *Controller) publishCredits() {
	r := c.guard.Ledger().Report()
	byKind := make(map[string]int, len(r.ByKind))
	for k, v := range r.ByKind {
		byKind[string(k)] = v
	}
	cr := &snapshot.Credits{
		Day: r.Day, Spend: r.Spend, InFlight: r.InFlight, ByKind: byKind,
		APIRemaining: r.APIRemaining, RemainingEstimate: r.RemainingEstimate,
		APILimit:     r.APILimit,
		ReconciledAt: r.ReconciledAt, Drift: r.Drift, ResetsAt: r.ResetsAt,
		BudgetState: string(c.guard.State()), Ceiling: c.cfg.Credits.DailyCeiling,
	}
	c.world.SetCredits(cr)
	if err := c.hub.Broadcast(hub.EventCredits, "", cr); err != nil {
		c.log.Warn("credits broadcast failed", "err", err)
	}
}

// Hello is the per-connection frame. The browser never guesses an interval.
func (c *Controller) Hello(clientID, version string) snapshot.Hello {
	spend := c.cfg.ProjectSpend()

	return snapshot.Hello{
		ClientID:      clientID,
		ServerVersion: version,
		StartedAt:     c.startedAt,
		Config: snapshot.HelloConfig{
			ActiveIntervalMs:        c.cfg.Poll.ActiveInterval.D().Milliseconds(),
			IdleIntervalMs:          c.cfg.Poll.IdleIntervalHidden.D().Milliseconds(),
			OverviewIntervalMs:      c.cfg.Overview.Interval.D().Milliseconds(),
			FocusRefreshThresholdMs: c.cfg.Poll.FocusRefreshThreshold.D().Milliseconds(),
			PresenceHeartbeatMs:     c.cfg.Poll.PresenceHeartbeat.D().Milliseconds(),
			SSEHeartbeatMs:          c.cfg.Poll.SSEHeartbeat.D().Milliseconds(),
			CoinLimit:               c.cfg.Coins.Limit,
			DefaultCurrency:         c.cfg.Currency.Default,
			DefaultView:             c.cfg.Coins.DefaultView,
			WatchlistMax:            c.cfg.Coins.WatchlistMax,
			WatchlistSource:         c.cfg.Watchlist.Source,
			ProjectedDailyCredits:   spend.Total,
			DailyCeiling:            c.cfg.Credits.DailyCeiling,
			AlertsEnabled:           c.cfg.Alerts.Enabled,
			HistoryEnabled:          c.hist.Enabled(),
			SortableFields:          sortableFields(),
			DeltaWindows:            deltaWindows(),
			ChartRanges:             []string{"24h", "7d", "30d", "90d", "1y"},
		},
	}
}

// SetEnvPath tells the status frame where to put the API key, so a missing-key
// UI can name the exact file instead of describing it.
func (c *Controller) SetEnvPath(p string) { c.envPath = p }
