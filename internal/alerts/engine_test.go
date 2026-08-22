package alerts

import (
	"testing"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

var t0 = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

func engine(t *testing.T, clk *clock.Fake, rules ...Rule) *Engine {
	t.Helper()
	// Zero grace by default so tests do not have to wait it out.
	return NewEngine(clk, rules, 30*time.Minute, 0, 100)
}

func snap(clk *clock.Fake, coins ...snapshot.CoinRow) Snapshot {
	return Snapshot{Currency: "USD", FetchedAt: clk.Now(), Coins: coins}
}

func btc(rate float64) snapshot.CoinRow {
	return snapshot.CoinRow{Code: "BTC", Name: "Bitcoin", Rank: 1, Rate: f(rate)}
}

func priceRule(op Op, value float64, rearm Rearm, hyst float64) Rule {
	return Rule{
		ID: "r", Name: "test", Severity: SeverityWarn,
		Scope:         Scope{Coin: "BTC"},
		Condition:     Condition{Metric: MetricPrice, Op: op, Value: value},
		Cooldown:      Dur(0),
		Rearm:         rearm,
		HysteresisPct: hyst,
	}
}

func TestLevelRuleFires(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpGT, 100000, RearmOnExit, 0.5))

	if got := e.Evaluate(snap(clk, btc(99000)), nil); len(got) != 0 {
		t.Fatalf("below the threshold should not fire: %+v", got)
	}
	clk.Advance(time.Second)
	got := e.Evaluate(snap(clk, btc(101000)), nil)
	if len(got) != 1 {
		t.Fatalf("got %d alerts, want 1", len(got))
	}
	if got[0].Value != 101000 || got[0].Threshold != 100000 {
		t.Errorf("alert = %+v", got[0])
	}
	if !got[0].FiredAt.Equal(clk.Now()) {
		t.Error("FiredAt must be the server's time, not the browser's receive time")
	}
}

// An edge op needs a previous observation, so a restart cannot announce a
// threshold that was already crossed.
func TestCrossesAboveNeedsAnEdge(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpCrossesAbove, 100000, RearmOnExit, 0.5))

	if got := e.Evaluate(snap(clk, btc(150000)), nil); len(got) != 0 {
		t.Fatalf("first observation must only record, not fire: %+v", got)
	}
	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(160000)), nil); len(got) != 0 {
		t.Errorf("still above with no crossing should not fire: %+v", got)
	}
}

func TestCrossesAboveFiresOnTheCrossing(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpCrossesAbove, 100000, RearmOnExit, 0.5))

	e.Evaluate(snap(clk, btc(99000)), nil)
	clk.Advance(time.Second)
	got := e.Evaluate(snap(clk, btc(101000)), nil)
	if len(got) != 1 {
		t.Fatalf("got %d alerts, want 1", len(got))
	}
	if got[0].Previous == nil || *got[0].Previous != 99000 {
		t.Errorf("Previous = %v, want 99000", got[0].Previous)
	}
}

func TestCrossesBelow(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpCrossesBelow, 100000, RearmOnExit, 0.5))

	e.Evaluate(snap(clk, btc(101000)), nil)
	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(99000)), nil); len(got) != 1 {
		t.Fatalf("got %d alerts, want 1", len(got))
	}
}

// The property that matters most: oscillation around a threshold must not fire
// on every tick. The cooldown is set to a nanosecond so hysteresis alone is
// doing the work.
func TestFiftyOscillationsFireOnce(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpCrossesAbove, 100000, RearmOnExit, 0.5)
	r.Cooldown = Dur(time.Nanosecond)
	e := engine(t, clk, r)

	total := 0
	for i := 0; i < 50; i++ {
		clk.Advance(time.Second)
		total += len(e.Evaluate(snap(clk, btc(99999)), nil))
		clk.Advance(time.Second)
		total += len(e.Evaluate(snap(clk, btc(100001)), nil))
	}
	if total != 1 {
		t.Errorf("%d alerts across 50 oscillations of 99,999<->100,001, want 1", total)
	}
}

// Hysteresis is what makes that work: only a real retreat re-arms.
func TestOnExitRearmsOnlyAfterRetreatingPastHysteresis(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpCrossesAbove, 100000, RearmOnExit, 1.0)
	// Cooldown and arming are independent gates; neutralise the cooldown so this
	// isolates hysteresis.
	r.Cooldown = Dur(time.Nanosecond)
	e := engine(t, clk, r)

	e.Evaluate(snap(clk, btc(99000)), nil)
	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Fatal("should have fired")
	}

	// 99,500 is only 0.5% below; hysteresis of 1% needs 99,000 or lower.
	clk.Advance(time.Second)
	e.Evaluate(snap(clk, btc(99500)), nil)
	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 0 {
		t.Errorf("re-armed too early: %+v", got)
	}

	clk.Advance(time.Second)
	e.Evaluate(snap(clk, btc(98000)), nil)
	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Error("should have re-armed after a genuine retreat")
	}
}

func TestCooldownBlocksRepeatFires(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpGT, 100000, RearmAfterCooldown, 0)
	r.Cooldown = Dur(30 * time.Minute)
	e := engine(t, clk, r)

	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Fatal("first fire expected")
	}
	clk.Advance(10 * time.Minute)
	if got := e.Evaluate(snap(clk, btc(102000)), nil); len(got) != 0 {
		t.Errorf("fired inside the cooldown: %+v", got)
	}
	clk.Advance(25 * time.Minute)
	if got := e.Evaluate(snap(clk, btc(103000)), nil); len(got) != 1 {
		t.Error("should fire again after the cooldown")
	}
}

func TestMinDurationSuppressesASpike(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpGT, 100000, RearmOnExit, 0)
	r.Condition.MinDuration = Dur(5 * time.Minute)
	e := engine(t, clk, r)

	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 0 {
		t.Fatal("should not fire before min_duration elapses")
	}
	// Drops out, so the clock restarts.
	clk.Advance(time.Minute)
	e.Evaluate(snap(clk, btc(99000)), nil)
	clk.Advance(6 * time.Minute)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 0 {
		t.Error("the duration clock should have restarted")
	}
	clk.Advance(6 * time.Minute)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Error("should fire once the condition has held long enough")
	}
}

func TestMaxFiresPerDay(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpGT, 100000, RearmAfterCooldown, 0)
	r.Cooldown = Dur(time.Minute)
	r.MaxFiresPerDay = 2
	e := engine(t, clk, r)

	fires := 0
	for i := 0; i < 10; i++ {
		clk.Advance(2 * time.Minute)
		fires += len(e.Evaluate(snap(clk, btc(101000)), nil))
	}
	if fires != 2 {
		t.Errorf("fired %d times, want the cap of 2", fires)
	}
}

func TestFireCountResetsAtUTCMidnight(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpGT, 100000, RearmAfterCooldown, 0)
	r.Cooldown = Dur(time.Minute)
	r.MaxFiresPerDay = 1
	e := engine(t, clk, r)

	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Fatal("want one fire")
	}
	clk.Advance(time.Hour)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 0 {
		t.Fatal("cap should hold within the day")
	}

	clk.Set(time.Date(2026, 8, 23, 0, 0, 1, 0, time.UTC))
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Error("the daily cap should reset at UTC midnight")
	}
}

// A frozen price under an exhausted budget must not keep alerting about
// hours-old data.
func TestStaleSnapshotIsNotEvaluated(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpGT, 100000, RearmOnExit, 0))

	s := snap(clk, btc(101000))
	s.Stale = true
	if got := e.Evaluate(s, nil); len(got) != 0 {
		t.Errorf("a stale snapshot fired %d alerts", len(got))
	}
}

func TestSameFetchIsNotEvaluatedTwice(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpGT, 100000, RearmOnExit, 0))

	s := snap(clk, btc(101000))
	if got := e.Evaluate(s, nil); len(got) != 1 {
		t.Fatal("first evaluation should fire")
	}
	if got := e.Evaluate(s, nil); len(got) != 0 {
		t.Errorf("re-evaluating the same fetch fired %d alerts", len(got))
	}
}

func TestRestartGraceSuppressesLevelRulesOnly(t *testing.T) {
	clk := clock.NewFake(t0)
	level := priceRule(OpGT, 100000, RearmOnExit, 0)
	level.ID = "level"
	edge := priceRule(OpCrossesAbove, 100000, RearmOnExit, 0)
	edge.ID = "edge"

	e := NewEngine(clk, []Rule{level, edge}, 30*time.Minute, time.Minute, 100)

	e.Evaluate(snap(clk, btc(99000)), nil)
	clk.Advance(time.Second)
	got := e.Evaluate(snap(clk, btc(101000)), nil)

	for _, a := range got {
		if a.RuleID == "level" {
			t.Error("a level rule should be suppressed during the restart grace")
		}
	}
	// An edge rule is safe: it needed a prior observation anyway.
	found := false
	for _, a := range got {
		if a.RuleID == "edge" {
			found = true
		}
	}
	if !found {
		t.Error("an edge rule should still fire during the grace period")
	}
}

// Missing data must not disarm a rule or advance its duration clock.
func TestMissingMetricIsSkipped(t *testing.T) {
	clk := clock.NewFake(t0)
	r := Rule{
		ID: "r", Severity: SeverityWarn, Rearm: RearmOnExit,
		Scope:     Scope{Coin: "HYPE"},
		Condition: Condition{Metric: MetricCap, Op: OpLT, Value: 1e9},
	}
	e := engine(t, clk, r)

	// HYPE has no market cap, exactly as in the reference table.
	row := snapshot.CoinRow{Code: "HYPE", Name: "Hyperliquid", Rank: 731, Rate: f(74.34)}
	if got := e.Evaluate(Snapshot{Currency: "USD", FetchedAt: clk.Now(), Coins: []snapshot.CoinRow{row}}, nil); len(got) != 0 {
		t.Errorf("a missing metric fired %d alerts", len(got))
	}
}

func TestDeltaRuleComparesPercent(t *testing.T) {
	clk := clock.NewFake(t0)
	r := Rule{
		ID: "swing", Severity: SeverityInfo, Rearm: RearmOnExit,
		Scope: Scope{Watchlist: true},
		Condition: Condition{Metric: MetricDelta, Window: "day",
			Op: OpAbsGT, Value: 10},
	}
	e := engine(t, clk, r)

	row := btc(100000)
	// A 12.5% move; the rule threshold is 10, meaning percent.
	row.ChangePct.Day = f(12.5)
	got := e.Evaluate(Snapshot{Currency: "USD", FetchedAt: clk.Now(),
		Coins: []snapshot.CoinRow{row}}, []string{"BTC"})
	if len(got) != 1 {
		t.Fatalf("got %d alerts, want 1", len(got))
	}
	if got[0].Value != 12.5 {
		t.Errorf("Value = %v, want 12.5", got[0].Value)
	}
}

// A large delta must not be clamped: their own data has year deltas over 1500%.
func TestLargeDeltaFires(t *testing.T) {
	clk := clock.NewFake(t0)
	r := Rule{
		ID: "moon", Severity: SeverityInfo, Rearm: RearmOnExit,
		Scope:     Scope{Coin: "ZEC"},
		Condition: Condition{Metric: MetricDelta, Window: "year", Op: OpGT, Value: 1000},
	}
	e := engine(t, clk, r)

	row := snapshot.CoinRow{Code: "ZEC", Name: "Zcash", Rank: 40, Rate: f(300)}
	row.ChangePct.Year = f(1593.99)
	got := e.Evaluate(Snapshot{Currency: "USD", FetchedAt: clk.Now(),
		Coins: []snapshot.CoinRow{row}}, nil)
	if len(got) != 1 {
		t.Fatalf("got %d alerts, want 1", len(got))
	}
}

func TestWatchlistScopeFollowsTheList(t *testing.T) {
	clk := clock.NewFake(t0)
	r := Rule{
		ID: "w", Severity: SeverityInfo, Rearm: RearmOnExit,
		Scope:     Scope{Watchlist: true},
		Condition: Condition{Metric: MetricPrice, Op: OpGT, Value: 1},
	}
	e := engine(t, clk, r)

	coins := []snapshot.CoinRow{btc(100000), {Code: "ETH", Name: "Ethereum", Rank: 2, Rate: f(2400)}}
	got := e.Evaluate(Snapshot{Currency: "USD", FetchedAt: clk.Now(), Coins: coins}, []string{"ETH"})
	if len(got) != 1 || got[0].Code != "ETH" {
		t.Errorf("got %+v, want one alert for ETH", got)
	}
}

func TestTopScopeUsesGlobalRank(t *testing.T) {
	clk := clock.NewFake(t0)
	r := Rule{
		ID: "t", Severity: SeverityInfo, Rearm: RearmOnExit,
		Scope:     Scope{Top: 10},
		Condition: Condition{Metric: MetricPrice, Op: OpGT, Value: 1},
	}
	e := engine(t, clk, r)

	coins := []snapshot.CoinRow{
		btc(100000),
		{Code: "HYPE", Name: "Hyperliquid", Rank: 731, Rate: f(74)},
	}
	got := e.Evaluate(Snapshot{Currency: "USD", FetchedAt: clk.Now(), Coins: coins}, nil)
	if len(got) != 1 || got[0].Code != "BTC" {
		t.Errorf("got %+v, want only BTC", got)
	}
}

func TestDisabledRuleDoesNotFire(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpGT, 100000, RearmOnExit, 0))

	e.SetEnabled("r", false)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 0 {
		t.Errorf("a disabled rule fired: %+v", got)
	}
	e.SetEnabled("r", true)
	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Error("re-enabling should let it fire")
	}
}

func TestManualRearmNeedsAnAck(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpGT, 100000, RearmManual, 0)
	r.Cooldown = Dur(time.Second)
	e := engine(t, clk, r)

	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Fatal("want a first fire")
	}
	clk.Advance(time.Hour)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 0 {
		t.Error("a manual rule should stay disarmed without an ack")
	}
	e.Ack("r")
	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Error("an ack should re-arm it")
	}
}

func TestStatePersistenceRoundTrip(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpGT, 100000, RearmAfterCooldown, 0)
	r.Cooldown = Dur(time.Hour)
	e := engine(t, clk, r)

	clk.Advance(time.Second)
	if got := e.Evaluate(snap(clk, btc(101000)), nil); len(got) != 1 {
		t.Fatal("want a fire")
	}

	saved := e.Snapshot().(persisted)
	restored := engine(t, clk, r)
	restored.Restore(&saved)

	clk.Advance(time.Minute)
	if got := restored.Evaluate(snap(clk, btc(102000)), nil); len(got) != 0 {
		t.Error("the cooldown should survive a restart")
	}
}

// A deleted rule must not leak arm state forever.
func TestSetRulesDropsStateForRemovedRules(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpGT, 100000, RearmOnExit, 0))
	clk.Advance(time.Second)
	e.Evaluate(snap(clk, btc(101000)), nil)

	if len(e.Snapshot().(persisted).States) == 0 {
		t.Fatal("expected some state")
	}
	e.SetRules(nil)
	if got := len(e.Snapshot().(persisted).States); got != 0 {
		t.Errorf("%d states left after removing the rule", got)
	}
}

func TestEventRingIsBounded(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpGT, 100000, RearmAfterCooldown, 0)
	r.Cooldown = Dur(time.Nanosecond)
	e := NewEngine(clk, []Rule{r}, time.Nanosecond, 0, 5)

	for i := 0; i < 20; i++ {
		clk.Advance(time.Second)
		e.Evaluate(snap(clk, btc(101000+float64(i))), nil)
	}
	if got := len(e.Events()); got > 5 {
		t.Errorf("kept %d events, want at most 5", got)
	}
}

func TestMessageTemplate(t *testing.T) {
	clk := clock.NewFake(t0)
	r := priceRule(OpCrossesAbove, 100000, RearmOnExit, 0)
	r.Message = "{{code}} at {{value}} passed {{threshold}}"
	e := engine(t, clk, r)

	e.Evaluate(snap(clk, btc(99000)), nil)
	clk.Advance(time.Second)
	got := e.Evaluate(snap(clk, btc(101000)), nil)
	if len(got) != 1 {
		t.Fatal("want a fire")
	}
	if got[0].Message != "BTC at 101000 passed 100000" {
		t.Errorf("Message = %q", got[0].Message)
	}
}

func TestDefaultMessageIsReadable(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpGT, 100000, RearmOnExit, 0))
	clk.Advance(time.Second)
	got := e.Evaluate(snap(clk, btc(101000)), nil)
	if len(got) != 1 {
		t.Fatal("want a fire")
	}
	want := "BTC price above 100000 (now 101000)"
	if got[0].Message != want {
		t.Errorf("Message = %q, want %q", got[0].Message, want)
	}
}

func TestStatuses(t *testing.T) {
	clk := clock.NewFake(t0)
	e := engine(t, clk, priceRule(OpGT, 100000, RearmOnExit, 0))
	clk.Advance(time.Second)
	e.Evaluate(snap(clk, btc(101000)), nil)

	st := e.Statuses()
	if len(st) != 1 {
		t.Fatalf("got %d statuses, want 1", len(st))
	}
	if st[0].Armed {
		t.Error("should report disarmed after firing")
	}
	if st[0].FiresToday != 1 {
		t.Errorf("FiresToday = %d, want 1", st[0].FiresToday)
	}
	if st[0].LastFiredAt == nil {
		t.Error("LastFiredAt should be set")
	}
}
