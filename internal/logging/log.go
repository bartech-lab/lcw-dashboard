package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/bartech/lcw-dashboard/internal/config"
)

// Setup builds the logger. The redacting handler is not optional: the API key
// reaches this process through config and env, and one careless log line would
// put it in a file or a journal.
func Setup(cfg config.Log, apiKey string) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(&redactor{inner: h, secret: apiKey})
}

type redactor struct {
	inner  slog.Handler
	secret string
}

func (r *redactor) Enabled(ctx context.Context, l slog.Level) bool { return r.inner.Enabled(ctx, l) }

func (r *redactor) Handle(ctx context.Context, rec slog.Record) error {
	if r.secret == "" {
		return r.inner.Handle(ctx, rec)
	}
	out := slog.NewRecord(rec.Time, rec.Level, r.scrub(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(r.scrubAttr(a))
		return true
	})
	return r.inner.Handle(ctx, out)
}

func (r *redactor) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		scrubbed[i] = r.scrubAttr(a)
	}
	return &redactor{inner: r.inner.WithAttrs(scrubbed), secret: r.secret}
}

func (r *redactor) WithGroup(name string) slog.Handler {
	return &redactor{inner: r.inner.WithGroup(name), secret: r.secret}
}

const mask = "[redacted]"

func (r *redactor) scrub(s string) string {
	if r.secret == "" {
		return s
	}
	return strings.ReplaceAll(s, r.secret, mask)
}

func (r *redactor) scrubAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, r.scrub(a.Value.String()))
	case slog.KindGroup:
		grp := a.Value.Group()
		out := make([]any, 0, len(grp))
		for _, g := range grp {
			out = append(out, r.scrubAttr(g))
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok && err != nil {
			return slog.String(a.Key, r.scrub(err.Error()))
		}
	}
	return a
}
