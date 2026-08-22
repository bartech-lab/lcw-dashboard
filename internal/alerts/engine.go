package alerts

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

// ruleCoinState is the per-(rule, coin) memory that makes edges, cooldowns and
// re-arming work across ticks and across restarts.
type ruleCoinState struct {
	Armed         bool       `json:"armed"`
	LastValue     *float64   `json:"lastValue"`
	TrueSince     *time.Time `json:"trueSince"`
	LastFiredAt   *time.Time `json:"lastFiredAt"`
	CooldownUntil *time.Time `json:"cooldownUntil"`
	FiresToday    int        `json:"firesToday"`
	Day           string     `json:"day"`
}

type persisted struct {
	States  map[string]*ruleCoinState `json:"states"`
	Enabled map[string]bool           `json:"enabled"`
}

// Snapshot is the coin data the engine evaluates against.
type Snapshot struct {
	Currency  string
	FetchedAt time.Time
	Stale     bool
	Coins     []snapshot.CoinRow
}

type Engine struct {
	mu           sync.Mutex
	clk          clock.Clock
	rules        []Rule
	defaultCool  time.Duration
	restartGrace time.Duration
	startedAt    time.Time
	maxEvents    int

	states  map[string]*ruleCoinState
	enabled map[string]bool
	events  []snapshot.Alert
	// lastFetched suppresses re-evaluating the same snapshot twice.
	lastFetched time.Time
	seq         uint64
}

func NewEngine(clk clock.Clock, rules []Rule, defaultCooldown, restartGrace time.Duration, maxEvents int) *Engine {
	return &Engine{
		clk: clk, rules: rules,
		defaultCool: defaultCooldown, restartGrace: restartGrace,
		startedAt: clk.Now(), maxEvents: maxEvents,
		states:  make(map[string]*ruleCoinState),
		enabled: make(map[string]bool),
	}
}

func (e *Engine) SetRules(rules []Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = rules

	// Drop state for rules that no longer exist, so a deleted rule does not leak
	// arm state forever.
	live := make(map[string]bool, len(rules))
	for _, r := range rules {
		live[r.ID] = true
	}
	for key := range e.states {
		if id, _, ok := splitKey(key); ok && !live[id] {
			delete(e.states, key)
		}
	}
}

func key(ruleID, code string) string { return ruleID + "\x00" + code }

func splitKey(k string) (ruleID, code string, ok bool) {
	i := strings.IndexByte(k, 0)
	if i < 0 {
		return "", "", false
	}
	return k[:i], k[i+1:], true
}

func (e *Engine) IsEnabled(r Rule) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := e.enabled[r.ID]; ok {
		return v
	}
	return r.IsEnabled()
}

func (e *Engine) SetEnabled(ruleID string, on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enabled[ruleID] = on
}

// Ack clears a cooldown and re-arms, for rules with rearm: manual.
func (e *Engine) Ack(ruleID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, st := range e.states {
		if id, _, ok := splitKey(k); ok && id == ruleID {
			st.Armed = true
			st.CooldownUntil = nil
		}
	}
}

// Evaluate returns the alerts a snapshot fires.
//
// A stale snapshot is skipped: a frozen price in an exhausted budget state would
// otherwise keep restarting min_duration clocks and alerting about hours-old
// data. The same fetch is never evaluated twice.
func (e *Engine) Evaluate(s Snapshot, watchlist []string) []snapshot.Alert {
	if s.Stale || len(s.Coins) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if !s.FetchedAt.After(e.lastFetched) {
		return nil
	}
	e.lastFetched = s.FetchedAt

	now := e.clk.Now()
	day := now.UTC().Format("2006-01-02")
	inGrace := now.Sub(e.startedAt) < e.restartGrace

	watch := make(map[string]bool, len(watchlist))
	for _, c := range watchlist {
		watch[c] = true
	}
	byCode := make(map[string]*snapshot.CoinRow, len(s.Coins))
	for i := range s.Coins {
		byCode[s.Coins[i].Code] = &s.Coins[i]
	}

	var fired []snapshot.Alert
	for _, r := range e.rules {
		if !e.isEnabledLocked(r) {
			continue
		}
		for _, coin := range e.scopeCoins(r, s.Coins, watch, byCode) {
			if a := e.evalOne(r, coin, s, now, day, inGrace); a != nil {
				fired = append(fired, *a)
			}
		}
	}

	if len(fired) > 0 {
		e.events = append(e.events, fired...)
		if e.maxEvents > 0 && len(e.events) > e.maxEvents {
			e.events = e.events[len(e.events)-e.maxEvents:]
		}
	}
	return fired
}

func (e *Engine) isEnabledLocked(r Rule) bool {
	if v, ok := e.enabled[r.ID]; ok {
		return v
	}
	return r.IsEnabled()
}

func (e *Engine) scopeCoins(r Rule, all []snapshot.CoinRow, watch map[string]bool,
	byCode map[string]*snapshot.CoinRow) []*snapshot.CoinRow {

	switch {
	case r.Scope.Coin != "":
		if c, ok := byCode[lcw.NormalizeCode(r.Scope.Coin)]; ok {
			return []*snapshot.CoinRow{c}
		}
		return nil
	case len(r.Scope.Codes) > 0:
		out := make([]*snapshot.CoinRow, 0, len(r.Scope.Codes))
		for _, code := range r.Scope.Codes {
			if c, ok := byCode[lcw.NormalizeCode(code)]; ok {
				out = append(out, c)
			}
		}
		return out
	case r.Scope.Watchlist:
		out := make([]*snapshot.CoinRow, 0, len(watch))
		for i := range all {
			if watch[all[i].Code] {
				out = append(out, &all[i])
			}
		}
		return out
	case r.Scope.Top > 0:
		out := make([]*snapshot.CoinRow, 0, r.Scope.Top)
		for i := range all {
			if all[i].Rank > 0 && all[i].Rank <= r.Scope.Top {
				out = append(out, &all[i])
			}
		}
		return out
	}
	return nil
}

func (e *Engine) evalOne(r Rule, coin *snapshot.CoinRow, s Snapshot,
	now time.Time, day string, inGrace bool) *snapshot.Alert {

	k := key(r.ID, coin.Code)
	st := e.states[k]
	if st == nil {
		st = &ruleCoinState{Armed: true, Day: day}
		e.states[k] = st
	}
	if st.Day != day {
		st.Day = day
		st.FiresToday = 0
		if r.Rearm == RearmOncePerDay {
			st.Armed = true
		}
	}

	value := metricValue(r.Condition, coin)
	if value == nil {
		// Missing data must not disarm a rule or advance its duration clock.
		st.TrueSince = nil
		return nil
	}
	v := *value
	prev := st.LastValue
	// Record the observation before any early return, so the next tick has an
	// edge to compare against.
	defer func() { cp := v; st.LastValue = &cp }()

	if r.Rearm == RearmAfterCooldown && !st.Armed &&
		st.CooldownUntil != nil && !now.Before(*st.CooldownUntil) {
		st.Armed = true
	}

	hit := predicate(r.Condition, v, prev)

	if r.Rearm == RearmOnExit && !st.Armed && !hit {
		// Retreat past the threshold by hysteresis_pct before re-arming, so
		// oscillation around it fires once rather than on every tick.
		if retreated(r, v) {
			st.Armed = true
		}
	}

	if !hit {
		st.TrueSince = nil
		return nil
	}

	// An edge op needs a previous observation, so a restart never announces a
	// threshold that was already crossed.
	if r.Condition.Op.IsEdge() && prev == nil {
		return nil
	}
	if inGrace && !r.Condition.Op.IsEdge() {
		return nil
	}

	if st.TrueSince == nil {
		t := now
		st.TrueSince = &t
	}
	if d := r.Condition.MinDuration.D(); d > 0 && now.Sub(*st.TrueSince) < d {
		return nil
	}

	if !st.Armed {
		return nil
	}
	if st.CooldownUntil != nil && now.Before(*st.CooldownUntil) {
		return nil
	}
	if r.MaxFiresPerDay > 0 && st.FiresToday >= r.MaxFiresPerDay {
		return nil
	}

	cooldown := r.Cooldown.D()
	if cooldown == 0 {
		cooldown = e.defaultCool
	}
	until := now.Add(cooldown)

	st.Armed = false
	st.CooldownUntil = &until
	st.LastFiredAt = &now
	st.FiresToday++
	e.seq++

	currency := r.Condition.Currency
	if currency == "" {
		currency = s.Currency
	}

	return &snapshot.Alert{
		EventID:   fmt.Sprintf("%s-%d", r.ID, e.seq),
		RuleID:    r.ID,
		RuleName:  r.Name,
		Severity:  string(r.Severity),
		Code:      coin.Code,
		Name:      coin.Name,
		Currency:  currency,
		Metric:    string(r.Condition.Metric),
		Window:    string(r.Condition.Window),
		Op:        string(r.Condition.Op),
		Threshold: r.Condition.Value,
		Value:     v,
		Previous:  prev,
		FiredAt:   now,
		Message:   message(r, coin, v),
		Cooldown:  &until,
	}
}

func retreated(r Rule, v float64) bool {
	t := r.Condition.Value
	margin := math.Abs(t) * r.HysteresisPct / 100
	if margin == 0 {
		margin = math.Abs(r.HysteresisPct)
	}
	switch r.Condition.Op {
	case OpGT, OpGTE, OpCrossesAbove:
		return v <= t-margin
	case OpLT, OpLTE, OpCrossesBelow:
		return v >= t+margin
	case OpAbsGT:
		return math.Abs(v) <= t-margin
	}
	return true
}

func predicate(c Condition, v float64, prev *float64) bool {
	switch c.Op {
	case OpGT:
		return v > c.Value
	case OpGTE:
		return v >= c.Value
	case OpLT:
		return v < c.Value
	case OpLTE:
		return v <= c.Value
	case OpAbsGT:
		return math.Abs(v) > c.Value
	case OpCrossesAbove:
		return prev != nil && *prev <= c.Value && v > c.Value
	case OpCrossesBelow:
		return prev != nil && *prev >= c.Value && v < c.Value
	}
	return false
}

func metricValue(c Condition, coin *snapshot.CoinRow) *float64 {
	switch c.Metric {
	case MetricPrice:
		return coin.Rate
	case MetricVolume:
		return coin.Volume
	case MetricCap:
		return coin.Cap
	case MetricLiquidity:
		return coin.Liquidity
	case MetricRank:
		if coin.Rank <= 0 {
			return nil
		}
		v := float64(coin.Rank)
		return &v
	case MetricATHDistance:
		return coin.FromATHPct
	case MetricDelta:
		return coin.ChangePct.Get(c.Window)
	}
	return nil
}

func message(r Rule, coin *snapshot.CoinRow, v float64) string {
	if r.Message != "" {
		msg := r.Message
		msg = strings.ReplaceAll(msg, "{{code}}", coin.Code)
		msg = strings.ReplaceAll(msg, "{{name}}", coin.Name)
		msg = strings.ReplaceAll(msg, "{{value}}", trim(v))
		msg = strings.ReplaceAll(msg, "{{threshold}}", trim(r.Condition.Value))
		return msg
	}
	unit := ""
	if r.Condition.Metric == MetricDelta || r.Condition.Metric == MetricATHDistance {
		unit = "%"
	}
	return fmt.Sprintf("%s %s %s %s%s (now %s%s)",
		coin.Code, humanMetric(r.Condition), humanOp(r.Condition.Op),
		trim(r.Condition.Value), unit, trim(v), unit)
}

func humanMetric(c Condition) string {
	if c.Metric == MetricDelta {
		return string(c.Window) + " change"
	}
	return string(c.Metric)
}

func humanOp(o Op) string {
	switch o {
	case OpGT:
		return "above"
	case OpGTE:
		return "at or above"
	case OpLT:
		return "below"
	case OpLTE:
		return "at or below"
	case OpAbsGT:
		return "moved more than"
	case OpCrossesAbove:
		return "crossed above"
	case OpCrossesBelow:
		return "crossed below"
	}
	return string(o)
}

func trim(v float64) string {
	s := fmt.Sprintf("%.4f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func (e *Engine) Events() []snapshot.Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]snapshot.Alert(nil), e.events...)
}

func (e *Engine) Snapshot() any {
	e.mu.Lock()
	defer e.mu.Unlock()
	states := make(map[string]*ruleCoinState, len(e.states))
	for k, v := range e.states {
		cp := *v
		states[k] = &cp
	}
	enabled := make(map[string]bool, len(e.enabled))
	for k, v := range e.enabled {
		enabled[k] = v
	}
	return persisted{States: states, Enabled: enabled}
}

func (e *Engine) Restore(v any) {
	p, ok := v.(*persisted)
	if !ok || p == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if p.States != nil {
		e.states = p.States
	}
	if p.Enabled != nil {
		e.enabled = p.Enabled
	}
}

func NewSnapshotTarget() any { return &persisted{} }

// RuleStatus is what the alerts panel shows.
type RuleStatus struct {
	Rule          Rule       `json:"rule"`
	Enabled       bool       `json:"enabled"`
	Armed         bool       `json:"armed"`
	LastFiredAt   *time.Time `json:"lastFiredAt"`
	CooldownUntil *time.Time `json:"cooldownUntil"`
	FiresToday    int        `json:"firesToday"`
	TrackedCoins  int        `json:"trackedCoins"`
}

func (e *Engine) Statuses() []RuleStatus {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]RuleStatus, 0, len(e.rules))
	for _, r := range e.rules {
		s := RuleStatus{Rule: r, Enabled: e.isEnabledLocked(r), Armed: true}
		for k, st := range e.states {
			id, _, ok := splitKey(k)
			if !ok || id != r.ID {
				continue
			}
			s.TrackedCoins++
			s.FiresToday += st.FiresToday
			if !st.Armed {
				s.Armed = false
			}
			if st.LastFiredAt != nil && (s.LastFiredAt == nil || st.LastFiredAt.After(*s.LastFiredAt)) {
				s.LastFiredAt = st.LastFiredAt
			}
			if st.CooldownUntil != nil && (s.CooldownUntil == nil || st.CooldownUntil.After(*s.CooldownUntil)) {
				s.CooldownUntil = st.CooldownUntil
			}
		}
		out = append(out, s)
	}
	return out
}
