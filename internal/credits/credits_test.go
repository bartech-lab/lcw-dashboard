package credits

import (
	"math/rand"
	"testing"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/config"
	"github.com/bartech/lcw-dashboard/internal/lcw"
)

var noon = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

type harness struct {
	clk *clock.Fake
	cfg config.Credits
	led *Ledger
	lim *Limiter
	g   *Guard
}

func newHarness(t *testing.T, tweak func(*config.Credits)) *harness {
	t.Helper()
	cfg := config.Default().Credits
	if tweak != nil {
		tweak(&cfg)
	}
	clk := clock.NewFake(noon)
	led := NewLedger(clk, cfg.APIDailyLimit)
	lim := NewLimiter(cfg, clk)
	return &harness{clk: clk, cfg: cfg, led: led, lim: lim,
		g: NewGuard(cfg, clk, led, lim, true)}
}

// spend advances past the minimum gap and commits n credits.
func (h *harness) spend(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		h.clk.Advance(h.cfg.MinRequestGap.D())
		if reason, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); !ok {
			t.Fatalf("reserve %d/%d refused: %s", i+1, n, reason)
		}
		h.g.Commit(KindCoinsList, 1)
	}
}

func TestReserveCommitAccounting(t *testing.T) {
	h := newHarness(t, nil)

	if _, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); !ok {
		t.Fatal("first reserve refused")
	}
	if got := h.led.Used(); got != 1 {
		t.Errorf("Used = %d, want 1 while in flight", got)
	}
	if got := h.led.Committed(); got != 0 {
		t.Errorf("Committed = %d, want 0 while in flight", got)
	}

	h.g.Commit(KindCoinsList, 1)
	if got := h.led.Committed(); got != 1 {
		t.Errorf("Committed = %d, want 1", got)
	}
	if got := h.led.Report().InFlight; got != 0 {
		t.Errorf("InFlight = %d, want 0 after commit", got)
	}
	if got := h.led.Report().ByKind[KindCoinsList]; got != 1 {
		t.Errorf("ByKind[coins_list] = %d, want 1", got)
	}
}

// A request that never reached the API cost nothing.
func TestRefundReleasesTheReservation(t *testing.T) {
	h := newHarness(t, nil)
	if _, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); !ok {
		t.Fatal("reserve refused")
	}
	h.g.Refund(1)

	if got := h.led.Used(); got != 0 {
		t.Errorf("Used = %d, want 0 after refund", got)
	}
}

func TestInFlightReservationsCountTowardTheCeiling(t *testing.T) {
	// Without this, concurrent callers both pass a ceiling check and overspend.
	h := newHarness(t, func(c *config.Credits) {
		c.DailyCeiling = 3
		c.ReserveForOnDemand = 0
		c.MinRequestGap = config.Duration(0)
		c.Burst = 100
	})

	for i := 0; i < 3; i++ {
		if _, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); !ok {
			t.Fatalf("reserve %d refused too early", i+1)
		}
	}
	if _, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); ok {
		t.Error("a fourth reserve should be refused: three are in flight against a ceiling of 3")
	}
}

func TestBudgetStateTransitions(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.DailyCeiling = 100
		c.ReserveForOnDemand = 0
		c.MinRequestGap = config.Duration(0)
		c.Burst = 1000
	})

	if got := h.g.State(); got != StateNormal {
		t.Fatalf("state = %s, want normal", got)
	}

	h.spend(t, 80)
	if got := h.g.Refresh(); got != StateConserve {
		t.Errorf("at 80%%: state = %s, want conserve", got)
	}

	h.spend(t, 15)
	if got := h.g.Refresh(); got != StateCritical {
		t.Errorf("at 95%%: state = %s, want critical", got)
	}

	h.spend(t, 5)
	if got := h.g.Refresh(); got != StateExhausted {
		t.Errorf("at 100%%: state = %s, want exhausted", got)
	}
}

// The point of hysteresis: flapping the state would flap the poll interval,
// which changes the spend rate that drives the state.
func TestNoFlappingAcrossAThreshold(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.DailyCeiling = 100
		c.ReserveForOnDemand = 0
		c.MinRequestGap = config.Duration(0)
		c.Burst = 1000
	})

	h.spend(t, 81)
	if got := h.g.Refresh(); got != StateConserve {
		t.Fatalf("state = %s, want conserve at 81%%", got)
	}

	// Reconcile down to 79%: still above the 75% recover threshold, so it holds.
	h.led.Reconcile(h.cfg.APIDailyLimit-79, h.cfg.APIDailyLimit)
	h.led.mu.Lock()
	h.led.spend = 79
	h.led.mu.Unlock()
	if got := h.g.Refresh(); got != StateConserve {
		t.Errorf("at 79%%: state = %s, want conserve to hold (recover is 75%%)", got)
	}

	h.led.mu.Lock()
	h.led.spend = 74
	h.led.mu.Unlock()
	if got := h.g.Refresh(); got != StateNormal {
		t.Errorf("at 74%%: state = %s, want normal", got)
	}
}

func TestOscillationAroundConserveProducesOneTransition(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.DailyCeiling = 100
		c.ReserveForOnDemand = 0
		c.MinRequestGap = config.Duration(0)
		c.Burst = 1000
	})

	set := func(n int) State {
		h.led.mu.Lock()
		h.led.spend = n
		h.led.mu.Unlock()
		return h.g.Refresh()
	}

	transitions := 0
	prev := h.g.State()
	for i := 0; i < 50; i++ {
		for _, n := range []int{79, 81} {
			if s := set(n); s != prev {
				transitions++
				prev = s
			}
		}
	}
	if transitions != 1 {
		t.Errorf("%d transitions across 50 oscillations of 79%%<->81%%, want 1", transitions)
	}
}

func TestNoKeyAndAuthFailedRefuseEverything(t *testing.T) {
	h := newHarness(t, nil)

	h.g.SetNoKey()
	if reason, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); ok || reason != ReasonNoKey {
		t.Errorf("no key: got (%s, %v), want (no_key, false)", reason, ok)
	}

	h.g.ClearKeyFailure()
	h.g.SetAuthFailed()
	if reason, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); ok || reason != ReasonAuth {
		t.Errorf("auth failed: got (%s, %v), want (auth_failed, false)", reason, ok)
	}
	// A probe must not sneak past a bad key either: retrying costs no credits
	// but hammers upstream.
	if _, ok := h.g.Reserve(KindCredits, 1, SourceProbe); ok {
		t.Error("a probe should not be allowed while the key is known bad")
	}
}

func TestExhaustedAllowsOnlyProbes(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) { c.MinRequestGap = config.Duration(0) })
	h.g.AdoptExhausted()

	if _, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); ok {
		t.Error("polling should be refused when exhausted")
	}
	if _, ok := h.g.Reserve(KindSingle, 1, SourceOnDemand); ok {
		t.Error("on-demand should be refused when exhausted")
	}
	if reason, ok := h.g.Reserve(KindCredits, 1, SourceProbe); !ok {
		t.Errorf("a probe must be allowed so the guard can learn the allowance reset: %s", reason)
	}
}

// The reserve keeps a detail-view click working after polling has been cut off.
func TestOnDemandReserveIsProtectedFromPolling(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.DailyCeiling = 100
		c.ReserveForOnDemand = 20
		c.MinRequestGap = config.Duration(0)
		c.Burst = 1000
		// Keep the state machine out of it; this tests the limit arithmetic.
		c.ConserveAt = 0.99
		c.ConserveRecoverAt = 0.98
		c.CriticalAt = 0.995
		c.CriticalRecoverAt = 0.99
	})

	h.spend(t, 80)
	if _, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); ok {
		t.Error("polling should stop at ceiling minus reserve (80 of 100)")
	}
	if reason, ok := h.g.Reserve(KindSingle, 1, SourceOnDemand); !ok {
		t.Errorf("on-demand should still work from the reserve: %s", reason)
	}
}

func TestUTCMidnightRollover(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.MinRequestGap = config.Duration(0)
		c.Burst = 1000
	})
	h.spend(t, 50)
	if got := h.led.Committed(); got != 50 {
		t.Fatalf("Committed = %d, want 50", got)
	}
	yesterday := h.led.Report().Day

	h.clk.Set(time.Date(2026, 8, 23, 0, 0, 1, 0, time.UTC))

	if got := h.led.Committed(); got != 0 {
		t.Errorf("Committed = %d, want 0 after rollover", got)
	}
	r := h.led.Report()
	if r.Day == yesterday {
		t.Errorf("day = %s, want it to have advanced", r.Day)
	}
	if r.Past[yesterday] != 50 {
		t.Errorf("past[%s] = %d, want 50 archived", yesterday, r.Past[yesterday])
	}
	if got := h.g.Refresh(); got != StateNormal {
		t.Errorf("state = %s, want normal after the allowance reset", got)
	}
}

func TestReconcileAdoptsTheLowerRemaining(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.MinRequestGap = config.Duration(0)
		c.Burst = 1000
	})
	h.spend(t, 100)

	// The API says 8,000 remain of 10,000, so 2,000 were spent — far more than
	// our 100. Something else is using the key.
	h.led.Reconcile(8000, 10000)

	r := h.led.Report()
	if r.Spend != 2000 {
		t.Errorf("Spend = %d, want 2000 adopted from the API", r.Spend)
	}
	if r.Drift != 1900 {
		t.Errorf("Drift = %d, want 1900 reported so a shared key is visible", r.Drift)
	}
}

func TestReconcileDoesNotLowerLocalSpend(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.MinRequestGap = config.Duration(0)
		c.Burst = 1000
	})
	h.spend(t, 500)

	// A stale or cached /credits reply claiming less was spent must not hand
	// back credits we know we used.
	h.led.Reconcile(9900, 10000)
	if got := h.led.Report().Spend; got != 500 {
		t.Errorf("Spend = %d, want 500 kept", got)
	}
}

func TestReconcileUpwardsJumpIsTreatedAsAReset(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.MinRequestGap = config.Duration(0)
		c.Burst = 1000
	})
	h.spend(t, 100)
	h.led.Reconcile(5000, 10000)
	if got := h.led.Report().Spend; got != 5000 {
		t.Fatalf("Spend = %d, want 5000", got)
	}

	h.led.Reconcile(10000, 10000)
	if got := h.led.Report().Spend; got != 0 {
		t.Errorf("Spend = %d, want 0: remaining jumped up, so the allowance reset", got)
	}
}

// The property that matters: no sequence of calls can exceed the ceiling.
func TestCeilingIsNeverExceededUnderFuzz(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.DailyCeiling = 500
		c.ReserveForOnDemand = 50
		c.MinRequestGap = config.Duration(100 * time.Millisecond)
		c.Burst = 50
	})

	rng := rand.New(rand.NewSource(1))
	sources := []Source{SourcePoll, SourceOnDemand, SourceProbe}

	for i := 0; i < 10000; i++ {
		h.clk.Advance(time.Duration(rng.Intn(3000)) * time.Millisecond)
		src := sources[rng.Intn(len(sources))]
		if _, ok := h.g.Reserve(KindCoinsList, 1, src); ok {
			if rng.Intn(10) == 0 {
				h.g.Refund(1)
			} else {
				h.g.Commit(KindCoinsList, 1)
			}
		}
		if used := h.led.Used(); used > 500 {
			t.Fatalf("iteration %d: used %d, ceiling is 500", i, used)
		}
	}
	if used := h.led.Used(); used == 0 {
		t.Error("the fuzz never spent anything, so it proved nothing")
	}
}

func TestLimiterEnforcesMinimumGap(t *testing.T) {
	clk := clock.NewFake(noon)
	cfg := config.Default().Credits
	l := NewLimiter(cfg, clk)

	if !l.Allow(1) {
		t.Fatal("first request should pass")
	}
	if l.Allow(1) {
		t.Error("a second request within the 2s gap should be refused")
	}
	clk.Advance(2 * time.Second)
	if !l.Allow(1) {
		t.Error("after the gap the request should pass")
	}
}

func TestLimiterBucketBoundsABurst(t *testing.T) {
	clk := clock.NewFake(noon)
	cfg := config.Default().Credits
	cfg.MinRequestGap = config.Duration(0)
	cfg.Burst = 5
	l := NewLimiter(cfg, clk)

	allowed := 0
	for i := 0; i < 50; i++ {
		if l.Allow(1) {
			allowed++
		}
	}
	if allowed != 5 {
		t.Errorf("allowed %d with no time passing, want the burst of 5", allowed)
	}
}

func TestLimiterRefills(t *testing.T) {
	clk := clock.NewFake(noon)
	cfg := config.Default().Credits
	cfg.MinRequestGap = config.Duration(0)
	cfg.Burst = 5
	l := NewLimiter(cfg, clk)

	for i := 0; i < 5; i++ {
		l.Allow(1)
	}
	if l.Allow(1) {
		t.Fatal("bucket should be empty")
	}
	// 9000/86400*3 is about 0.3125 tokens per second.
	clk.Advance(10 * time.Second)
	if !l.Allow(1) {
		t.Error("bucket should have refilled")
	}
}

func TestBreakerBacksOffAndClosesOnSuccess(t *testing.T) {
	clk := clock.NewFake(noon)
	l := NewLimiter(config.Default().Credits, clk)

	if l.Open() {
		t.Fatal("breaker starts closed")
	}

	var prev time.Duration
	for i := 0; i < len(backoff); i++ {
		l.Failure()
		if !l.Open() {
			t.Fatalf("failure %d: breaker should be open", i+1)
		}
		d := l.OpenUntil().Sub(clk.Now())
		// Jitter is 0.8..1.2, so successive steps must still grow.
		if i > 0 && d <= prev/2 {
			t.Errorf("failure %d: backoff %s did not grow past %s", i+1, d, prev)
		}
		prev = d
	}
	if got := l.Failures(); got != len(backoff) {
		t.Errorf("Failures = %d, want %d", got, len(backoff))
	}

	l.Success()
	if l.Open() {
		t.Error("breaker should close on success")
	}
	if got := l.Failures(); got != 0 {
		t.Errorf("Failures = %d, want 0 after success", got)
	}
}

func TestBreakerBackoffIsCapped(t *testing.T) {
	clk := clock.NewFake(noon)
	l := NewLimiter(config.Default().Credits, clk)
	for i := 0; i < 50; i++ {
		l.Failure()
	}
	if d := l.OpenUntil().Sub(clk.Now()); d > 15*time.Minute {
		t.Errorf("backoff grew to %s; it must stay capped", d)
	}
}

func TestPollIntervalFollowsBudgetState(t *testing.T) {
	h := newHarness(t, nil)
	base, idle, critical := 15*time.Second, 120*time.Second, 600*time.Second

	if got := h.g.PollInterval(base, idle, critical); got != base {
		t.Errorf("normal: %s, want %s", got, base)
	}

	h.g.force(StateConserve)
	if got := h.g.PollInterval(base, idle, critical); got != idle {
		t.Errorf("conserve: %s, want %s", got, idle)
	}

	h.g.force(StateCritical)
	if got := h.g.PollInterval(base, idle, critical); got != critical {
		t.Errorf("critical: %s, want %s", got, critical)
	}

	h.g.force(StateExhausted)
	if got := h.g.PollInterval(base, idle, critical); got != 0 {
		t.Errorf("exhausted: %s, want 0 (no polling)", got)
	}
}

func TestLedgerPersistenceRoundTrip(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) {
		c.MinRequestGap = config.Duration(0)
		c.Burst = 1000
	})
	h.spend(t, 42)

	snap, dirty := h.led.Snapshot()
	if !dirty {
		t.Fatal("ledger should be dirty after spending")
	}
	if _, again := h.led.Snapshot(); again {
		t.Error("a second Snapshot with no change should report clean")
	}

	s := snap.(snapshot)
	restored := NewLedger(h.clk, h.cfg.APIDailyLimit)
	restored.Restore(&s)

	if got := restored.Committed(); got != 42 {
		t.Errorf("Committed = %d, want 42 after restore", got)
	}
}

// State from a previous UTC day must not carry forward: the allowance reset.
func TestRestoreDropsAStaleDay(t *testing.T) {
	clk := clock.NewFake(noon)
	led := NewLedger(clk, 10000)
	led.Restore(&snapshot{Day: "2026-08-01", Spend: 9000, History: map[string]int{}})

	if got := led.Committed(); got != 0 {
		t.Errorf("Committed = %d, want 0: the snapshot was from another day", got)
	}
	if got := led.Report().Past["2026-08-01"]; got != 9000 {
		t.Errorf("past = %d, want the old day archived", got)
	}
}

func TestReportResetsAtIsNextUTCMidnight(t *testing.T) {
	h := newHarness(t, nil)
	want := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	if got := h.led.Report().ResetsAt; !got.Equal(want) {
		t.Errorf("ResetsAt = %s, want %s", got, want)
	}
}

// A shared key can be spent out upstream while the local ledger reads low, so
// local spend must not downgrade an API-declared exhaustion.
func TestAPIExhaustionOutranksLocalSpend(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) { c.MinRequestGap = config.Duration(0) })

	h.g.AdoptExhausted()
	if got := h.g.Refresh(); got != StateExhausted {
		t.Fatalf("state = %s, want exhausted to hold at zero local spend", got)
	}
	if _, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); ok {
		t.Error("polling should stay refused")
	}

	h.g.ClearExhausted()
	if got := h.g.State(); got == StateExhausted {
		t.Error("a successful probe should clear it")
	}
	if _, ok := h.g.Reserve(KindCoinsList, 1, SourcePoll); !ok {
		t.Error("polling should resume after the allowance came back")
	}
}

func TestAPIExhaustionClearsAtTheNextUTCDay(t *testing.T) {
	h := newHarness(t, func(c *config.Credits) { c.MinRequestGap = config.Duration(0) })
	h.g.AdoptExhausted()

	h.clk.Set(time.Date(2026, 8, 23, 0, 0, 1, 0, time.UTC))

	if got := h.g.Refresh(); got != StateNormal {
		t.Errorf("state = %s, want normal: a new UTC day means a fresh allowance", got)
	}
}

func TestClassifyMapsUpstreamErrors(t *testing.T) {
	h := newHarness(t, nil)

	if h.g.Classify(nil) {
		t.Error("a nil error should not change state")
	}

	authErr := &lcw.APIError{Code: 401, Status: "Unauthorized", HTTPStatus: 200}
	if !h.g.Classify(authErr) {
		t.Error("a 401 should change state")
	}
	if got := h.g.State(); got != StateAuthFailed {
		t.Errorf("state = %s, want auth_failed", got)
	}
	if h.g.Classify(authErr) {
		t.Error("a repeat 401 should not report another transition, so it is logged once")
	}

	h.g.ClearKeyFailure()
	creditErr := &lcw.APIError{Code: 429, Status: "Too Many Requests", HTTPStatus: 429}
	if !h.g.Classify(creditErr) {
		t.Error("a 429 should change state")
	}
	if got := h.g.State(); got != StateExhausted {
		t.Errorf("state = %s, want exhausted", got)
	}
}
