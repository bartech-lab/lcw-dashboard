package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/bartech/lcw-dashboard/internal/alerts"
	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/config"
	"github.com/bartech/lcw-dashboard/internal/credits"
	"github.com/bartech/lcw-dashboard/internal/history"
	"github.com/bartech/lcw-dashboard/internal/hub"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/notify"
	"github.com/bartech/lcw-dashboard/internal/searchindex"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
	"github.com/bartech/lcw-dashboard/internal/watchlist"
)

// Controller is the single writer of all scheduling state: timers, presence,
// rotation, in-flight flags, failure counters and world publication. Because one
// goroutine owns all of it, none of it needs a lock and no interleaving can
// double-fire a tick.
type Controller struct {
	cfg    config.Config
	clk    clock.Clock
	log    *slog.Logger
	client *lcw.Client
	guard  *credits.Guard
	hub    *hub.Hub
	world  *snapshot.Holder
	watch  *watchlist.List
	hist   *history.Store
	engine *alerts.Engine
	index  *searchindex.Index
	notify *notify.Fanout

	cmdCh    chan command
	resultCh chan result

	// Controller-owned. Never touched from another goroutine.
	presence         *presence
	rotation         []ViewKey
	rotIdx           int
	inFlightCoins    bool
	inFlightOverview bool
	lastCoinSuccess  time.Time
	// lastSuccessByKey makes freshness per view. A single timestamp let a top
	// fetch mark favourites as fresh, so returning to a favourites tab skipped
	// the fetch it needed.
	lastSuccessByKey map[string]time.Time
	lastFocusRefresh time.Time
	failures         int
	lastError        *snapshot.WireError
	staleSince       *time.Time
	revision         uint64
	startedAt        time.Time

	envPath string

	coinTimer     clock.Timer
	overviewTimer clock.Timer
	houseTimer    clock.Timer
	interval      time.Duration
	nextTickAt    time.Time
}

type Deps struct {
	Cfg    config.Config
	Clk    clock.Clock
	Log    *slog.Logger
	Client *lcw.Client
	Guard  *credits.Guard
	Hub    *hub.Hub
	World  *snapshot.Holder
	Watch  *watchlist.List
	Hist   *history.Store
	Engine *alerts.Engine
	Index  *searchindex.Index
	Notify *notify.Fanout
}

func New(d Deps) *Controller {
	return &Controller{
		cfg: d.Cfg, clk: d.Clk, log: d.Log, client: d.Client, guard: d.Guard,
		hub: d.Hub, world: d.World, watch: d.Watch, hist: d.Hist,
		engine: d.Engine, index: d.Index, notify: d.Notify,

		cmdCh:            make(chan command, 64),
		resultCh:         make(chan result, 8),
		lastSuccessByKey: make(map[string]time.Time),
		presence:         newPresence(d.Cfg.Poll.PresenceTTL.D()),
		startedAt:        d.Clk.Now(),
	}
}

type cmdKind int

const (
	cmdPresence cmdKind = iota
	cmdDisconnect
	cmdRefresh
	cmdWatchlistChanged
	cmdReconciled
	cmdIndexDone
	cmdPrime
	cmdClientState
)

type command struct {
	kind       cmdKind
	clientID   string
	view       snapshot.View
	currency   string
	sort       lcw.SortField
	order      lcw.SortOrder
	visible    bool
	what       string
	reply      chan RefreshReply
	stateReply chan [4]string
}

// RefreshReply tells the caller what the controller decided, so the UI can show
// a reason instead of guessing whether its request landed.
type RefreshReply struct {
	Accepted   bool   `json:"accepted"`
	Reason     string `json:"reason,omitempty"`
	ViewKey    string `json:"viewKey"`
	IntervalMs int64  `json:"intervalMs"`
	Revision   uint64 `json:"revision"`
	RetryAfter int64  `json:"retryAfterMs,omitempty"`
}

type resultKind int

const (
	resCoins resultKind = iota
	resOverview
)

type result struct {
	kind    resultKind
	key     ViewKey
	coins   []snapshot.CoinRow
	over    *snapshot.Overview
	credits int
	unknown []string
	err     error
}

// Presence records a client heartbeat, visibility change or view switch.
func (c *Controller) Presence(id string, view snapshot.View, currency string,
	sort lcw.SortField, order lcw.SortOrder, visible bool) RefreshReply {

	reply := make(chan RefreshReply, 1)
	c.send(command{
		kind: cmdPresence, clientID: id, view: view, currency: currency,
		sort: sort, order: order, visible: visible, reply: reply,
	})
	select {
	case r := <-reply:
		return r
	case <-time.After(2 * time.Second):
		return RefreshReply{Reason: "controller busy"}
	}
}

func (c *Controller) Disconnect(id string) {
	c.send(command{kind: cmdDisconnect, clientID: id})
}

func (c *Controller) Refresh(what string) RefreshReply {
	reply := make(chan RefreshReply, 1)
	c.send(command{kind: cmdRefresh, what: what, reply: reply})
	select {
	case r := <-reply:
		return r
	case <-time.After(2 * time.Second):
		return RefreshReply{Reason: "controller busy"}
	}
}

func (c *Controller) WatchlistChanged() { c.send(command{kind: cmdWatchlistChanged}) }

// send never blocks the caller: an HTTP handler must not stall because the
// controller is busy.
func (c *Controller) send(cmd command) {
	select {
	case c.cmdCh <- cmd:
	default:
		c.log.Warn("controller command queue full, dropping", "kind", cmd.kind)
		if cmd.reply != nil {
			cmd.reply <- RefreshReply{Reason: "controller busy"}
		}
	}
}

// Run is the event loop. Everything that mutates scheduling state happens here.
func (c *Controller) Run(ctx context.Context) {
	c.interval = c.resolveInterval()
	c.coinTimer = c.clk.NewTimer(c.interval)
	c.overviewTimer = c.clk.NewTimer(c.overviewInterval())
	c.houseTimer = c.clk.NewTimer(housekeeping)
	c.nextTickAt = c.clk.Now().Add(c.interval)
	c.publishStatus()

	for {
		select {
		case <-ctx.Done():
			c.coinTimer.Stop()
			c.overviewTimer.Stop()
			c.houseTimer.Stop()
			return

		case cmd := <-c.cmdCh:
			c.handleCommand(ctx, cmd)

		case res := <-c.resultCh:
			c.handleResult(ctx, res)

		case <-c.coinTimer.C():
			c.dispatchCoins(ctx, "tick")

		case <-c.overviewTimer.C():
			c.dispatchOverview(ctx)

		case <-c.houseTimer.C():
			c.housekeep(ctx)
			c.houseTimer.Reset(housekeeping)
		}
	}
}

const housekeeping = 10 * time.Second

func (c *Controller) handleCommand(ctx context.Context, cmd command) {
	now := c.clk.Now()

	switch cmd.kind {
	case cmdPresence:
		before := c.activeKey()
		changed := c.presence.upsert(cmd.clientID, cmd.view, cmd.currency,
			cmd.sort, cmd.order, cmd.visible, now)
		c.recomputeRotation()
		// A view or currency switch is an explicit intent, not a wake-up to
		// coalesce, so it must not be swallowed by the focus debounce.
		switched := c.activeKey() != before
		reply := c.maybeFocusRefresh(ctx, changed, switched)
		// Report the caller's own key, not the rotation head. With several tabs
		// on different views the head belongs to whichever was activated last,
		// and telling a favourites tab it is on "top" is actively misleading.
		reply.ViewKey = c.keyFor(cmd.clientID).String()
		c.resetCoinTimer()
		// The overview interval also depends on visibility, so a tab becoming
		// visible must not leave it on the slow hidden schedule.
		c.overviewTimer.Reset(c.overviewInterval())
		c.publishStatus()
		if cmd.reply != nil {
			cmd.reply <- reply
		}

	case cmdDisconnect:
		if c.presence.remove(cmd.clientID) {
			c.recomputeRotation()
			c.resetCoinTimer()
			c.publishStatus()
		}

	case cmdRefresh:
		reply := RefreshReply{ViewKey: c.activeKey().String(), Revision: c.revision}
		switch {
		case c.inFlightCoins:
			reply.Reason = "in_flight"
		case c.clk.Since(c.lastFocusRefresh) < c.cfg.Poll.FocusDebounce.D():
			reply.Reason = "debounced"
			reply.RetryAfter = c.cfg.Poll.FocusDebounce.D().Milliseconds()
		default:
			if cmd.what == "overview" || cmd.what == "both" {
				c.dispatchOverview(ctx)
			}
			if cmd.what != "overview" {
				c.lastFocusRefresh = now
				reply.Accepted = c.dispatchCoins(ctx, "manual")
				if !reply.Accepted {
					reply.Reason = "budget"
				}
			} else {
				reply.Accepted = true
			}
		}
		reply.IntervalMs = c.interval.Milliseconds()
		if cmd.reply != nil {
			cmd.reply <- reply
		}

	case cmdWatchlistChanged:
		c.revision++
		c.recomputeRotation()
		c.hist.Pin(c.watch.Codes())
		if err := c.hub.Broadcast(hub.EventWatchlist, "", c.watch.Snapshot()); err != nil {
			c.log.Warn("watchlist broadcast failed", "err", err)
		}
		// Only refetch if a favourites view is actually being looked at.
		if c.activeKey().View == snapshot.ViewFavourites {
			c.dispatchCoins(ctx, "watchlist")
		}
		c.publishStatus()

	case cmdReconciled, cmdIndexDone:
		c.guard.Refresh()
		c.resetCoinTimer()
		c.publishStatus()
		c.publishCredits()

	case cmdClientState:
		var out [4]string
		if cl, ok := c.presence.clients[cmd.clientID]; ok {
			out = [4]string{string(cl.View), cl.Currency, string(cl.Sort), string(cl.Order)}
		}
		if cmd.stateReply != nil {
			cmd.stateReply <- out
		}

	case cmdPrime:
		// Fetch once at startup regardless of the idle cadence, so the first
		// browser to connect is painted immediately instead of waiting out a
		// full interval.
		c.dispatchCoins(ctx, "prime")
		c.dispatchOverview(ctx)
	}
}

// maybeFocusRefresh implements the focus rule inside the same controller turn as
// the timer reset, so there is no window in which both fire.
//
// switched means the active view key changed. The debounce exists to coalesce
// several tabs waking at once; applying it to a deliberate view switch left the
// table showing the previous view's data.
func (c *Controller) maybeFocusRefresh(ctx context.Context, changed, switched bool) RefreshReply {
	reply := RefreshReply{
		ViewKey:    c.activeKey().String(),
		IntervalMs: c.resolveInterval().Milliseconds(),
		Revision:   c.revision,
	}

	_, visible := c.presence.counts()
	if visible == 0 && !switched {
		reply.Reason = "hidden"
		return reply
	}
	if c.inFlightCoins {
		reply.Reason = "in_flight"
		return reply
	}
	age := c.ageOf(c.activeKey())
	if !switched {
		if !changed && age < c.cfg.Poll.FocusRefreshThreshold.D() {
			reply.Reason = "fresh"
			return reply
		}
		// Debounce is what stops eight tabs waking together from firing eight
		// fetches. A view switch skips it.
		if c.clk.Since(c.lastFocusRefresh) < c.cfg.Poll.FocusDebounce.D() {
			reply.Reason = "debounced"
			return reply
		}
		if _, ok := c.world.Load().Coins[c.activeKey().String()]; ok &&
			age < c.cfg.Poll.FocusRefreshThreshold.D() {
			reply.Reason = "fresh"
			return reply
		}
	}
	c.lastFocusRefresh = c.clk.Now()
	if c.dispatchCoins(ctx, "focus") {
		reply.Accepted = true
	} else {
		reply.Reason = "budget"
	}
	return reply
}

// ageOf reports how stale one view key is. An unfetched key is infinitely old,
// so switching to it always fetches.
func (c *Controller) ageOf(k ViewKey) time.Duration {
	at, ok := c.lastSuccessByKey[k.String()]
	if !ok {
		return time.Duration(1 << 62)
	}
	return c.clk.Since(at)
}

func (c *Controller) housekeep(ctx context.Context) {
	// Retry the startup fetch until it lands. It competes with the search-index
	// build for the rate limiter's slot, and losing once must not leave the first
	// browser looking at an empty table for a whole idle interval.
	if c.lastCoinSuccess.IsZero() && c.guard.State().Polls() {
		c.dispatchCoins(ctx, "prime")
	}

	dropped := c.presence.expire(c.clk.Now())
	if len(dropped) > 0 {
		for _, id := range dropped {
			c.hub.Unregister(id)
		}
		c.log.Debug("expired stale clients", "ids", dropped)
		c.recomputeRotation()
		c.resetCoinTimer()
	}
	c.guard.Refresh()
	c.publishStatus()
	c.publishCredits()
}

func (c *Controller) recomputeRotation() {
	keys := c.presence.keys(c.watch.Hash(), c.cfg.Poll.MaxRotationKeys)
	if len(keys) == 0 {
		// Nothing visible: keep polling the default so alerts keep evaluating.
		keys = []ViewKey{c.defaultKey()}
	}
	c.rotation = keys
	if c.rotIdx >= len(c.rotation) {
		c.rotIdx = 0
	}
}

func (c *Controller) defaultKey() ViewKey {
	k := ViewKey{
		View:     snapshot.View(c.cfg.Coins.DefaultView),
		Currency: c.cfg.Currency.Default,
		Sort:     c.cfg.Coins.Sort,
		Order:    c.cfg.Coins.Order,
	}
	if k.View == snapshot.ViewFavourites {
		k.WatchHash = c.watch.Hash()
	}
	return k
}

// ClientState reports what a client is currently on, so a partial control
// message can patch rather than reset. Empty values mean the client is unknown.
func (c *Controller) ClientState(clientID string) (view snapshot.View,
	currency string, sortField lcw.SortField, order lcw.SortOrder) {

	reply := make(chan [4]string, 1)
	c.send(command{kind: cmdClientState, clientID: clientID, stateReply: reply})
	select {
	case v := <-reply:
		return snapshot.View(v[0]), v[1], lcw.SortField(v[2]), lcw.SortOrder(v[3])
	case <-time.After(time.Second):
		return "", "", "", ""
	}
}

// keyFor returns the view key a specific client is subscribed to.
func (c *Controller) keyFor(clientID string) ViewKey {
	cl, ok := c.presence.clients[clientID]
	if !ok {
		return c.activeKey()
	}
	k := ViewKey{View: cl.View, Currency: cl.Currency, Sort: cl.Sort, Order: cl.Order}
	if cl.View == snapshot.ViewFavourites {
		k.WatchHash = c.watch.Hash()
	}
	return k
}

func (c *Controller) activeKey() ViewKey {
	if len(c.rotation) == 0 {
		return c.defaultKey()
	}
	return c.rotation[c.rotIdx%len(c.rotation)]
}

// resolveInterval combines visibility, budget, rotation and chunking.
//
// The interval is multiplied by both the rotation size and the chunk count, so
// the credit rate is invariant to how many tabs are open and how long the
// watchlist is. That invariance is the core budget-safety property.
func (c *Controller) resolveInterval() time.Duration {
	total, visible := c.presence.counts()

	var base time.Duration
	switch {
	case visible > 0:
		base = c.cfg.Poll.ActiveInterval.D()
	case total > 0:
		base = c.cfg.Poll.IdleIntervalHidden.D()
	default:
		base = c.cfg.Poll.IdleIntervalNoClients.D()
		if !c.cfg.Alerts.Enabled || !c.cfg.Alerts.PollWhenNoClients {
			return 0
		}
	}
	if base <= 0 {
		return 0
	}

	base = c.guard.PollInterval(base,
		c.cfg.Poll.IdleIntervalHidden.D(),
		c.cfg.Poll.CriticalInterval.D())
	if base <= 0 {
		return 0
	}

	multiplier := len(c.rotation)
	if multiplier < 1 {
		multiplier = 1
	}
	if c.activeKey().View == snapshot.ViewFavourites {
		multiplier *= c.watch.ChunkCount()
	}
	return base * time.Duration(multiplier)
}

func (c *Controller) overviewInterval() time.Duration {
	if !c.cfg.Overview.Enabled {
		return time.Hour
	}
	_, visible := c.presence.counts()
	d := c.cfg.Overview.Interval.D()
	if visible == 0 {
		d = c.cfg.Overview.HiddenInterval.D()
	}
	if state := c.guard.State(); !state.Polls() {
		return time.Hour
	}
	if d <= 0 {
		return time.Hour
	}
	return d
}

func (c *Controller) resetCoinTimer() {
	next := c.resolveInterval()
	if next <= 0 {
		// Not polling, but wake occasionally so a budget or day change is noticed.
		next = time.Hour
	}
	c.interval = next
	c.nextTickAt = c.clk.Now().Add(next)
	c.coinTimer.Reset(next)
}
