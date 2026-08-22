package credits

import (
	"sync"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/config"
	"github.com/bartech/lcw-dashboard/internal/lcw"
)

type State string

const (
	StateNormal     State = "normal"
	StateConserve   State = "conserve"
	StateCritical   State = "critical"
	StateExhausted  State = "exhausted"
	StateAuthFailed State = "auth_failed"
	StateNoKey      State = "no_key"
)

// Polls reports whether the state permits scheduled polling at all.
func (s State) Polls() bool {
	return s == StateNormal || s == StateConserve || s == StateCritical
}

// Reason explains a refusal, for the UI and for logs.
type Reason string

const (
	ReasonOK        Reason = ""
	ReasonCeiling   Reason = "daily_ceiling"
	ReasonReserved  Reason = "reserved_for_ondemand"
	ReasonExhausted Reason = "exhausted"
	ReasonAuth      Reason = "auth_failed"
	ReasonNoKey     Reason = "no_key"
	ReasonCritical  Reason = "critical_budget"
	ReasonBreaker   Reason = "circuit_open"
	ReasonMinGap    Reason = "min_request_gap"
)

// Guard is the single gate every API request passes through.
type Guard struct {
	mu      sync.Mutex
	cfg     config.Credits
	clk     clock.Clock
	ledger  *Ledger
	limiter *Limiter
	state   State
	// onDemandThisHour bounds user-triggered fetches once the budget is critical.
	onDemandHour  int64
	onDemandCount int
	// apiExhausted records that the API itself refused, which outranks the local
	// ledger. A shared key can be spent out while our own count reads low, so
	// local spend must not be allowed to downgrade this.
	apiExhausted    bool
	apiExhaustedDay string
}

const criticalOnDemandPerHour = 10

func NewGuard(cfg config.Credits, clk clock.Clock, ledger *Ledger, limiter *Limiter, hasKey bool) *Guard {
	st := StateNormal
	if !hasKey {
		st = StateNoKey
	}
	return &Guard{cfg: cfg, clk: clk, ledger: ledger, limiter: limiter, state: st}
}

func (g *Guard) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

func (g *Guard) Ledger() *Ledger { return g.ledger }

// SetNoKey and SetAuthFailed are terminal until the key changes. Retrying a bad
// key costs no credits but hammers upstream and floods the log.
func (g *Guard) SetNoKey() { g.force(StateNoKey) }

func (g *Guard) SetAuthFailed() { g.force(StateAuthFailed) }

// ClearKeyFailure returns to the spend-driven state after a key is supplied or
// a probe succeeds.
func (g *Guard) ClearKeyFailure() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == StateNoKey || g.state == StateAuthFailed {
		g.state = StateNormal
		g.recompute()
	}
}

func (g *Guard) force(s State) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = s
}

// AdoptExhausted takes the API's word over the local ledger. It clears only on
// a successful probe or at the next UTC day.
func (g *Guard) AdoptExhausted() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = StateExhausted
	g.apiExhausted = true
	g.apiExhaustedDay = g.ledger.Report().Day
}

// ClearExhausted is called when a probe or reconcile shows credits remaining.
func (g *Guard) ClearExhausted() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.apiExhausted = false
	g.apiExhaustedDay = ""
	if g.state == StateExhausted {
		g.state = StateConserve
		g.recompute()
	}
}

// Classify maps an upstream error onto a state change. It returns true when the
// state changed, so callers log the transition once.
func (g *Guard) Classify(err error) bool {
	switch {
	case err == nil:
		return false
	case lcw.IsAuth(err):
		if g.State() == StateAuthFailed {
			return false
		}
		g.SetAuthFailed()
		return true
	case lcw.IsCreditExhausted(err):
		if g.State() == StateExhausted {
			return false
		}
		g.AdoptExhausted()
		return true
	}
	return false
}

// recompute applies the hysteresis thresholds. Callers must hold the lock.
//
// Each recover threshold sits below its trigger because flapping the state
// would flap the poll interval, which changes the spend rate that drives the
// state.
func (g *Guard) recompute() {
	if g.state == StateNoKey || g.state == StateAuthFailed {
		return
	}
	if g.apiExhausted {
		// A new UTC day means a fresh allowance, so the refusal is stale.
		if g.ledger.Report().Day != g.apiExhaustedDay {
			g.apiExhausted = false
			g.apiExhaustedDay = ""
			g.state = StateNormal
		} else {
			g.state = StateExhausted
			return
		}
	}

	ceiling := g.cfg.DailyCeiling
	if ceiling <= 0 {
		return
	}
	ratio := float64(g.ledger.Used()) / float64(ceiling)

	switch g.state {
	case StateNormal:
		if ratio >= g.cfg.CriticalAt {
			g.state = StateCritical
		} else if ratio >= g.cfg.ConserveAt {
			g.state = StateConserve
		}
	case StateConserve:
		if ratio >= g.cfg.CriticalAt {
			g.state = StateCritical
		} else if ratio < g.cfg.ConserveRecoverAt {
			g.state = StateNormal
		}
	case StateCritical:
		if ratio >= 1.0 {
			g.state = StateExhausted
		} else if ratio < g.cfg.CriticalRecoverAt {
			g.state = StateConserve
		}
	case StateExhausted:
		if ratio < g.cfg.CriticalRecoverAt {
			g.state = StateConserve
		}
	}
}

// Refresh recomputes the state without reserving. The controller calls it after
// a day rollover or a reconcile.
func (g *Guard) Refresh() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recompute()
	return g.state
}

// Source distinguishes scheduled polling from a user action, because the two
// degrade differently: polling slows down, user actions keep working from the
// reserve.
type Source int

const (
	SourcePoll Source = iota
	SourceOnDemand
	// SourceProbe is a free or near-free liveness check.
	SourceProbe
)

// Reserve is the gate. On success the caller must call Commit or Refund.
func (g *Guard) Reserve(kind Kind, n int, src Source) (Reason, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.recompute()

	switch g.state {
	case StateNoKey:
		return ReasonNoKey, false
	case StateAuthFailed:
		return ReasonAuth, false
	case StateExhausted:
		// A probe is how the guard learns the allowance came back.
		if src != SourceProbe {
			return ReasonExhausted, false
		}
	case StateCritical:
		if src == SourcePoll {
			// Polling continues, but at critical_interval; the scheduler applies
			// that. Reserving is still allowed so the slow loop keeps running.
			break
		}
		if src == SourceOnDemand && !g.allowCriticalOnDemand() {
			return ReasonCritical, false
		}
	}

	// The on-demand reserve is subtracted from the ceiling for polling only, so
	// clicking into a coin still works late in the day.
	limit := g.cfg.DailyCeiling
	if src == SourcePoll && g.cfg.ReserveForOnDemand > 0 {
		limit -= g.cfg.ReserveForOnDemand
	}

	if !g.limiter.Allow(n) {
		return ReasonMinGap, false
	}
	if !g.ledger.reserve(n, limit) {
		g.limiter.Return(n)
		if src == SourcePoll {
			return ReasonReserved, false
		}
		return ReasonCeiling, false
	}
	g.recompute()
	return ReasonOK, true
}

func (g *Guard) allowCriticalOnDemand() bool {
	hour := g.clk.Now().UTC().Unix() / 3600
	if hour != g.onDemandHour {
		g.onDemandHour = hour
		g.onDemandCount = 0
	}
	if g.onDemandCount >= criticalOnDemandPerHour {
		return false
	}
	g.onDemandCount++
	return true
}

func (g *Guard) Commit(kind Kind, n int) {
	g.ledger.Commit(kind, n)
	g.mu.Lock()
	g.recompute()
	g.mu.Unlock()
}

func (g *Guard) Refund(n int) {
	g.ledger.Refund(n)
	g.limiter.Return(n)
}

// RecordFailure and RecordSuccess drive the circuit breaker. The breaker is
// probed with /status, which costs nothing, so backing off is free.
func (g *Guard) RecordFailure()    { g.limiter.Failure() }
func (g *Guard) RecordSuccess()    { g.limiter.Success() }
func (g *Guard) BreakerOpen() bool { return g.limiter.Open() }

func (g *Guard) BreakerOpenUntil() time.Time { return g.limiter.OpenUntil() }

// PollInterval resolves the effective interval for the current budget state.
// The scheduler owns visibility; this owns budget. A zero result means do not
// poll at all.
func (g *Guard) PollInterval(base, idle, critical time.Duration) time.Duration {
	switch g.State() {
	case StateConserve:
		return maxDur(idle, base)
	case StateCritical:
		return maxDur(critical, idle)
	case StateExhausted, StateAuthFailed, StateNoKey:
		return 0
	}
	return base
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
