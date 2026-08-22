package credits

import (
	"math/rand"
	"sync"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/config"
)

// Limiter is the last defence against a runaway loop, independent of the budget
// state machine above it. Two mechanisms, deliberately redundant: a hard minimum
// gap between any two requests, and a token bucket sized from the daily ceiling.
type Limiter struct {
	mu       sync.Mutex
	clk      clock.Clock
	minGap   time.Duration
	burst    float64
	rate     float64
	tokens   float64
	lastFill time.Time
	lastReq  time.Time

	failures  int
	openUntil time.Time
	rng       *rand.Rand
}

func NewLimiter(cfg config.Credits, clk clock.Clock) *Limiter {
	// Three times the average rate implied by the ceiling: enough to absorb a
	// bootstrap burst, far short of exhausting the day.
	rate := float64(cfg.DailyCeiling) / 86400 * 3
	if rate <= 0 {
		rate = 1
	}
	now := clk.Now()
	return &Limiter{
		clk:      clk,
		minGap:   cfg.MinRequestGap.D(),
		burst:    float64(cfg.Burst),
		rate:     rate,
		tokens:   float64(cfg.Burst),
		lastFill: now,
		rng:      rand.New(rand.NewSource(now.UnixNano())),
	}
}

func (l *Limiter) refill(now time.Time) {
	if elapsed := now.Sub(l.lastFill); elapsed > 0 {
		l.tokens += elapsed.Seconds() * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.lastFill = now
	}
}

// Allow consumes n tokens if the gap and bucket permit.
func (l *Limiter) Allow(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()

	if !l.lastReq.IsZero() && now.Sub(l.lastReq) < l.minGap {
		return false
	}
	l.refill(now)
	if l.tokens < float64(n) {
		return false
	}
	l.tokens -= float64(n)
	l.lastReq = now
	return true
}

// Return gives tokens back for a request that never happened.
func (l *Limiter) Return(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tokens += float64(n)
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.lastReq = time.Time{}
}

var backoff = []time.Duration{
	15 * time.Second, 30 * time.Second, 60 * time.Second,
	120 * time.Second, 300 * time.Second, 600 * time.Second,
}

// Failure opens the breaker with jittered backoff. The probe used to close it is
// /status, which costs nothing, so retry storms are free.
func (l *Limiter) Failure() {
	l.mu.Lock()
	defer l.mu.Unlock()
	idx := l.failures
	if idx >= len(backoff) {
		idx = len(backoff) - 1
	}
	d := backoff[idx]
	jitter := 0.8 + 0.4*l.rng.Float64()
	l.failures++
	l.openUntil = l.clk.Now().Add(time.Duration(float64(d) * jitter))
}

func (l *Limiter) Success() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures = 0
	l.openUntil = time.Time{}
}

// Open reports whether the breaker is holding requests back.
func (l *Limiter) Open() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.clk.Now().Before(l.openUntil)
}

func (l *Limiter) OpenUntil() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.openUntil
}

func (l *Limiter) Failures() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failures
}
