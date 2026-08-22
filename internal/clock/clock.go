// Package clock abstracts time so the scheduler, budget guard and alert engine
// are testable without waiting.
package clock

import (
	"sync"
	"time"
)

type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	NewTimer(d time.Duration) Timer
	Sleep(d time.Duration)
}

type Real struct{}

func (Real) Now() time.Time                  { return time.Now() }
func (Real) Since(t time.Time) time.Duration { return time.Since(t) }
func (Real) Sleep(d time.Duration)           { time.Sleep(d) }
func (Real) NewTimer(d time.Duration) Timer  { return &realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time { return r.t.C }
func (r *realTimer) Stop() bool          { return r.t.Stop() }

// Reset drains a pending fire so a reset timer never delivers the previous
// deadline's tick, which would double-fetch.
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

// Fake is a manually advanced Clock. Safe for concurrent use: the code under
// test runs its own goroutine while the test advances time.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func NewFake(start time.Time) *Fake { return &Fake{now: start} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) Since(t time.Time) time.Duration { return f.Now().Sub(t) }
func (f *Fake) Sleep(d time.Duration)           { f.Advance(d) }

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
		default:
		}
	}
}

// Set jumps to an absolute instant, for UTC-midnight rollover and backwards
// clock jumps.
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

	select {
	case <-t.ch:
	default:
	}
	return was
}
