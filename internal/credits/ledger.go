// Package credits accounts for API requests and refuses them when the daily
// allowance runs low.
package credits

import (
	"sync"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
)

type Kind string

const (
	KindCoinsList  Kind = "coins_list"
	KindCoinsMap   Kind = "coins_map"
	KindOverview   Kind = "overview"
	KindOverviewHx Kind = "overview_history"
	KindSingle     Kind = "coins_single"
	KindHistory    Kind = "coins_history"
	KindCredits    Kind = "credits"
	KindFiats      Kind = "fiats"
	KindIndex      Kind = "search_index"
)

// snapshot is the persisted form.
type snapshot struct {
	Day          string         `json:"day"`
	Spend        int            `json:"spend"`
	ByKind       map[Kind]int   `json:"byKind"`
	APIRemaining int            `json:"apiRemaining"`
	APILimit     int            `json:"apiLimit"`
	ReconciledAt time.Time      `json:"reconciledAt"`
	Drift        int            `json:"drift"`
	History      map[string]int `json:"history"`
}

// Ledger tracks spend for the current UTC day. Reserved counts in-flight
// requests so concurrent callers cannot both pass a ceiling check.
type Ledger struct {
	mu    sync.Mutex
	clk   clock.Clock
	day   string
	spend int
	kinds map[Kind]int
	// reserved is in-flight; committed spend moves out of it.
	reserved int
	// apiRemaining is API truth, written only by Reconcile. It is never
	// decremented locally: using one field for both truth and estimate made the
	// allowance-reset detection below unreliable.
	apiRemaining int
	// spendAtReconcile anchors the local estimate to the last known truth.
	spendAtReconcile int
	apiLimit         int
	reconciledAt     time.Time
	drift            int
	past             map[string]int
	dirty            bool
}

const pastDaysKept = 7

func NewLedger(clk clock.Clock, apiLimit int) *Ledger {
	return &Ledger{
		clk:   clk,
		day:   utcDay(clk.Now()),
		kinds: make(map[Kind]int),
		// Assume a full allowance until /credits says otherwise, or the UI would
		// show zero remaining before the first reconcile.
		apiLimit:     apiLimit,
		apiRemaining: apiLimit,
		past:         make(map[string]int),
	}
}

func utcDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

// rollover archives the finished day. Callers must hold the lock.
func (l *Ledger) rollover(now time.Time) {
	today := utcDay(now)
	if today == l.day {
		return
	}
	if l.spend > 0 {
		l.past[l.day] = l.spend
		for len(l.past) > pastDaysKept {
			var oldest string
			for k := range l.past {
				if oldest == "" || k < oldest {
					oldest = k
				}
			}
			delete(l.past, oldest)
		}
	}
	l.day = today
	l.spend = 0
	l.kinds = make(map[Kind]int)
	l.reserved = 0
	l.apiRemaining = l.apiLimit
	l.spendAtReconcile = 0
	l.drift = 0
	l.dirty = true
}

// Committed returns spend excluding in-flight reservations.
func (l *Ledger) Committed() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollover(l.clk.Now())
	return l.spend
}

// Used counts committed plus in-flight, which is what a ceiling check must use.
func (l *Ledger) Used() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollover(l.clk.Now())
	return l.spend + l.reserved
}

// reserve is the gate. limit is the effective ceiling for this call.
func (l *Ledger) reserve(n, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollover(l.clk.Now())
	if l.spend+l.reserved+n > limit {
		return false
	}
	l.reserved += n
	return true
}

// Commit moves a reservation into spend. A request that reached the API cost a
// credit even if it returned an error.
func (l *Ledger) Commit(kind Kind, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollover(l.clk.Now())
	if l.reserved >= n {
		l.reserved -= n
	} else {
		l.reserved = 0
	}
	l.spend += n
	l.kinds[kind] += n
	l.dirty = true
}

// Refund releases a reservation for a request that never reached the API, such
// as a DNS or connection failure. Those cost nothing.
func (l *Ledger) Refund(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reserved >= n {
		l.reserved -= n
	} else {
		l.reserved = 0
	}
}

// Reconcile adopts the API's own count. The API is authoritative about what
// remains; a positive drift means something else is spending the same key,
// which silently halves the budget.
func (l *Ledger) Reconcile(remaining, limit int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	l.rollover(now)

	if limit > 0 {
		l.apiLimit = limit
	}

	// Both sides of this comparison are API truth, so an upward jump really does
	// mean the allowance reset, even if the local day string disagrees because of
	// a clock problem.
	if remaining > l.apiRemaining && l.spend > 0 {
		l.spend = 0
		l.kinds = make(map[Kind]int)
		l.drift = 0
	}

	l.apiRemaining = remaining
	l.spendAtReconcile = l.spend
	if l.apiLimit > 0 {
		apiSpend := l.apiLimit - remaining
		l.drift = apiSpend - l.spend
		// The API is authoritative about what it has served, so adopt a higher
		// count. Never adopt a lower one: we know what we sent.
		if apiSpend > l.spend {
			l.spend = apiSpend
			l.spendAtReconcile = apiSpend
		}
	}
	l.reconciledAt = now
	l.dirty = true
}

type Report struct {
	Day          string         `json:"utcDay"`
	Spend        int            `json:"localSpend"`
	InFlight     int            `json:"inFlight"`
	ByKind       map[Kind]int   `json:"byKind"`
	APIRemaining int            `json:"apiRemaining"`
	APILimit     int            `json:"apiLimit"`
	ReconciledAt time.Time      `json:"reconciledAt"`
	Drift        int            `json:"drift"`
	ResetsAt     time.Time      `json:"resetsAt"`
	Past         map[string]int `json:"past,omitempty"`
	// RemainingEstimate is APIRemaining less what has been spent since the last
	// reconcile. It is what the UI shows between reconciles.
	RemainingEstimate int `json:"remainingEstimate"`
}

func (l *Ledger) Report() Report {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	l.rollover(now)

	kinds := make(map[Kind]int, len(l.kinds))
	for k, v := range l.kinds {
		kinds[k] = v
	}
	past := make(map[string]int, len(l.past))
	for k, v := range l.past {
		past[k] = v
	}
	estimate := l.apiRemaining - (l.spend - l.spendAtReconcile)
	if estimate < 0 {
		estimate = 0
	}
	return Report{
		Day:          l.day,
		Spend:        l.spend,
		InFlight:     l.reserved,
		ByKind:       kinds,
		APIRemaining: l.apiRemaining,
		APILimit:     l.apiLimit,
		ReconciledAt: l.reconciledAt,
		Drift:        l.drift,
		ResetsAt:     nextUTCMidnight(now),
		Past:         past,

		RemainingEstimate: estimate,
	}
}

func nextUTCMidnight(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
}

func (l *Ledger) Snapshot() (any, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.dirty {
		return nil, false
	}
	kinds := make(map[Kind]int, len(l.kinds))
	for k, v := range l.kinds {
		kinds[k] = v
	}
	past := make(map[string]int, len(l.past))
	for k, v := range l.past {
		past[k] = v
	}
	l.dirty = false
	return snapshot{
		Day: l.day, Spend: l.spend, ByKind: kinds,
		APIRemaining: l.apiRemaining, APILimit: l.apiLimit,
		ReconciledAt: l.reconciledAt, Drift: l.drift, History: past,
	}, true
}

// Restore loads persisted state. A snapshot from an earlier UTC day is dropped:
// the allowance has reset since.
func (l *Ledger) Restore(v any) {
	s, ok := v.(*snapshot)
	if !ok || s == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if s.History != nil {
		l.past = s.History
	}
	if s.Day != utcDay(l.clk.Now()) {
		if s.Spend > 0 {
			l.past[s.Day] = s.Spend
		}
		return
	}
	l.day = s.Day
	l.spend = s.Spend
	if s.ByKind != nil {
		l.kinds = s.ByKind
	}
	l.apiRemaining = s.APIRemaining
	l.spendAtReconcile = s.Spend
	if s.APILimit > 0 {
		l.apiLimit = s.APILimit
	}
	l.reconciledAt = s.ReconciledAt
	l.drift = s.Drift
}

// NewSnapshotTarget gives the persistence layer something to decode into.
func NewSnapshotTarget() any { return &snapshot{} }
