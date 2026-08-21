package clock

import (
	"testing"
	"time"
)

var start = time.Date(2026, 8, 21, 14, 32, 0, 0, time.UTC)

func TestFakeNowOnlyMovesWhenAdvanced(t *testing.T) {
	f := NewFake(start)
	if !f.Now().Equal(start) {
		t.Fatalf("Now = %v, want %v", f.Now(), start)
	}
	// A real clock would have moved by now; a fake must not.
	if !f.Now().Equal(start) {
		t.Fatal("Now moved without an Advance")
	}
	f.Advance(90 * time.Second)
	if got := f.Now(); !got.Equal(start.Add(90 * time.Second)) {
		t.Fatalf("Now = %v, want +90s", got)
	}
}

func TestFakeSince(t *testing.T) {
	f := NewFake(start)
	f.Advance(15 * time.Second)
	if got := f.Since(start); got != 15*time.Second {
		t.Fatalf("Since = %v, want 15s", got)
	}
}

func TestFakeTimerFiresOnlyPastItsDeadline(t *testing.T) {
	f := NewFake(start)
	tm := f.NewTimer(15 * time.Second)

	f.Advance(14 * time.Second)
	select {
	case <-tm.C():
		t.Fatal("timer fired before its deadline")
	default:
	}

	f.Advance(1 * time.Second)
	select {
	case at := <-tm.C():
		if !at.Equal(start.Add(15 * time.Second)) {
			t.Errorf("fired at %v, want %v", at, start.Add(15*time.Second))
		}
	default:
		t.Fatal("timer did not fire at its deadline")
	}
}

func TestFakeTimerFiresOnceOnly(t *testing.T) {
	// This is the property the scheduler depends on: advancing far past a
	// deadline must not queue several ticks, or waking a laptop would produce a
	// burst of fetches.
	f := NewFake(start)
	tm := f.NewTimer(10 * time.Second)
	f.Advance(10 * time.Minute)

	if _, ok := recv(tm); !ok {
		t.Fatal("expected one fire")
	}
	if at, ok := recv(tm); ok {
		t.Fatalf("timer fired a second time at %v; one deadline means one tick", at)
	}
}

func TestFakeTimerStop(t *testing.T) {
	f := NewFake(start)
	tm := f.NewTimer(10 * time.Second)

	if !tm.Stop() {
		t.Error("Stop on an active timer should report true")
	}
	if tm.Stop() {
		t.Error("Stop on an already-stopped timer should report false")
	}
	f.Advance(time.Minute)
	if at, ok := recv(tm); ok {
		t.Fatalf("stopped timer fired at %v", at)
	}
}

func TestFakeTimerResetDrainsPendingFire(t *testing.T) {
	// A reset timer must never deliver the previous deadline's tick. Without the
	// drain, the scheduler would see a stale tick immediately after changing its
	// interval and fetch twice.
	f := NewFake(start)
	tm := f.NewTimer(10 * time.Second)
	f.Advance(10 * time.Second) // timer has now fired, unread

	tm.Reset(30 * time.Second)
	if at, ok := recv(tm); ok {
		t.Fatalf("Reset left a stale tick queued, fired at %v", at)
	}

	f.Advance(29 * time.Second)
	if _, ok := recv(tm); ok {
		t.Fatal("fired before the new deadline")
	}
	f.Advance(1 * time.Second)
	if _, ok := recv(tm); !ok {
		t.Fatal("did not fire at the new deadline")
	}
}

func TestFakeTimersFireInDeadlineOrder(t *testing.T) {
	f := NewFake(start)
	slow := f.NewTimer(60 * time.Second)
	fast := f.NewTimer(10 * time.Second)

	f.Advance(2 * time.Minute)
	if _, ok := recv(fast); !ok {
		t.Error("the 10s timer should have fired")
	}
	if _, ok := recv(slow); !ok {
		t.Error("the 60s timer should have fired")
	}
}

func TestFakeSetJumpsAbsolutely(t *testing.T) {
	// Used to test UTC-midnight rollover in the credit ledger.
	f := NewFake(start)
	midnight := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	f.Set(midnight)
	if !f.Now().Equal(midnight) {
		t.Fatalf("Now = %v, want %v", f.Now(), midnight)
	}

	// And backwards, which a real machine can do after an NTP correction.
	f.Set(start)
	if !f.Now().Equal(start) {
		t.Fatalf("Now = %v, want %v", f.Now(), start)
	}
}

func TestRealTimerResetDrains(t *testing.T) {
	r := Real{}
	tm := r.NewTimer(time.Millisecond)
	time.Sleep(20 * time.Millisecond) // let it fire, unread

	tm.Reset(time.Hour)
	select {
	case at := <-tm.C():
		t.Fatalf("Reset left a stale tick queued, fired at %v", at)
	case <-time.After(20 * time.Millisecond):
	}
	tm.Stop()
}

func TestRealClockSatisfiesInterface(t *testing.T) {
	var c Clock = Real{}
	if c.Now().IsZero() {
		t.Error("Now returned the zero time")
	}
	var f Clock = NewFake(start)
	if !f.Now().Equal(start) {
		t.Error("Fake does not satisfy Clock correctly")
	}
}

func recv(tm Timer) (time.Time, bool) {
	select {
	case at := <-tm.C():
		return at, true
	default:
		return time.Time{}, false
	}
}
