// Package notify delivers alerts to the desktop.
//
// A browser tab cannot be the only path: Chrome freezes idle background tabs,
// and a frozen tab's EventSource stops delivering, which is exactly when an
// alert matters most. So the Go process notifies the desktop directly and the
// browser path is an enhancement.
package notify

import (
	"context"
	"log/slog"
	"time"

	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

type Sink interface {
	Name() string
	Available() bool
	Notify(ctx context.Context, a snapshot.Alert) error
}

// Fanout sends to every configured sink and never lets one failure stop another.
type Fanout struct {
	sinks []Sink
	log   *slog.Logger
}

func NewFanout(log *slog.Logger, sinks ...Sink) *Fanout {
	live := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s == nil {
			continue
		}
		if !s.Available() {
			log.Info("notification sink unavailable, skipping", "sink", s.Name())
			continue
		}
		live = append(live, s)
	}
	return &Fanout{sinks: live, log: log}
}

func (f *Fanout) Names() []string {
	out := make([]string, 0, len(f.sinks))
	for _, s := range f.sinks {
		out = append(out, s.Name())
	}
	return out
}

// Notify delivers with a short timeout: a hung notification daemon must not
// block the poll loop.
func (f *Fanout) Notify(ctx context.Context, alerts []snapshot.Alert) {
	if len(f.sinks) == 0 || len(alerts) == 0 {
		return
	}
	for _, a := range alerts {
		for _, s := range f.sinks {
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := s.Notify(c, a); err != nil {
				f.log.Warn("notification failed", "sink", s.Name(), "rule", a.RuleID, "err", err)
			}
			cancel()
		}
	}
}

// LogSink is always available and doubles as the audit trail.
type LogSink struct{ Log *slog.Logger }

func (LogSink) Name() string    { return "log" }
func (LogSink) Available() bool { return true }

func (s LogSink) Notify(_ context.Context, a snapshot.Alert) error {
	s.Log.Info("alert",
		"rule", a.RuleID, "code", a.Code, "severity", a.Severity,
		"value", a.Value, "threshold", a.Threshold,
		"firedAt", a.FiredAt.Format(time.RFC3339), "message", a.Message)
	return nil
}

// title and body are shared by the platform sinks so both read identically.
//
// The body carries the server's own fired-at time. Chromium buffers SSE to
// hidden tabs and flushes on focus, so a notification claiming "now" would lie
// about when the threshold was crossed.
func title(a snapshot.Alert) string {
	if a.RuleName != "" {
		return a.RuleName
	}
	return a.Code + " alert"
}

func body(a snapshot.Alert) string {
	return a.Message + " at " + a.FiredAt.Local().Format("15:04:05")
}
