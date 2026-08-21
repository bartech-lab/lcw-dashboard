// Package clock abstracts time so the scheduler, budget guard and alert engine
// can be tested deterministically. Production code uses Real; tests use Fake.
package clock

import (
	"sync"
	"time"
)

// Timer is the subset of *time.Timer the scheduler needs. Fake implements it
// without any real waiting.
type Timer interface {
	// C returns the channel the timer fires on.
	C() <-chan time.Time
	// Stop halts the timer. It reports whether the timer was still active.
	Stop() bool
	// Reset restarts the timer with a new duration, draining any pending fire
	// first so a reset timer can never deliver a stale tick.
	Reset(d time.Duration) bool
}

// Clock is the time source. Every package that schedules or measures takes one.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	NewTimer(d time.Duration) Timer
	Sleep(d time.Duration)
}

// ---------------------------------------------------------------- real clock

// Real is the production Clock, backed by the time package.
type Real struct{}

func (Real) Now() time.Time                  { return time.Now() }
func (Real) Since(t time.Time) time.Duration { return time.Since(t) }
func (Real) Sleep(d time.Duration)           { time.Sleep(d) }

func (Real) NewTimer(d time.Duration) Timer { return &realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time { return r.t.C }
func (r *realTimer) Stop() bool          { return r.t.Stop() }

// Reset stops the timer and drains a pending fire before restarting it.
// Go 1.23+ makes Reset itself safe, but draining keeps the "a reset timer never
// delivers a stale tick" guarantee explicit rather than version-dependent.
func (r *realTimer) Reset(d time.Duration) bool {
	active := r.t.Stop()
	if !active {
		select {
		case <-r.t.C:
		default:
		}
	}
	r.t.Reset(d)
	return active
}

// ---------------------------------------------------------------- fake clock

// Fake is a manually advanced Clock for tests. It is safe for concurrent use:
// the scheduler under test runs its own goroutine while the test advances time.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// NewFake returns a Fake started at the given instant. Pass a fixed, readable
// time so failures print recognisable timestamps.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) Since(t time.Time) time.Duration { return f.Now().Sub(t) }

// Sleep on a Fake advances time instead of blocking.
func (f *Fake) Sleep(d time.Duration) { f.Advance(d) }

func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{
		ch:     make(chan time.Time, 1),
		fireAt: f.now.Add(d),
		active: true,
		fake:   f,
	}
	f.timers = append(f.timers, t)
	return t
}

// Advance moves time forward and fires every timer whose deadline has passed.
// Timers fire in deadline order, so a test that advances past several deadlines
// sees them in the order real time would have produced.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now

	due := make([]*fakeTimer, 0, len(f.timers))
	for _, t := range f.timers {
		if t.active && !t.fireAt.After(now) {
			t.active = false
			due = append(due, t)
		}
	}
	f.mu.Unlock()

	for _, t := range due {
		select {
		case t.ch <- now:
		default: // buffered channel already holds an unread fire
		}
	}
}

// Set jumps to an absolute instant. Used to test UTC-midnight rollover and
// backwards clock jumps.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

type fakeTimer struct {
	ch     chan time.Time
	fireAt time.Time
	active bool
	fake   *Fake
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.fake.mu.Lock()
	defer t.fake.mu.Unlock()
	was := t.active
	t.active = false
	return was
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.fake.mu.Lock()
	was := t.active
	t.active = true
	t.fireAt = t.fake.now.Add(d)
	t.fake.mu.Unlock()

	select { // drain, matching realTimer.Reset
	case <-t.ch:
	default:
	}
	return was
}
