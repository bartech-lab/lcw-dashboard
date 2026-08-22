package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bartech/lcw-dashboard/internal/alerts"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("the shipped defaults must validate:\n%v", err)
	}
}

func TestDefaultSpendFitsTheBudget(t *testing.T) {
	s := Default().ProjectSpend()

	if s.Coins != 5760 {
		t.Errorf("coins = %d/day, want 5760 (15s interval)", s.Coins)
	}
	if s.Overview != 288 {
		t.Errorf("overview = %d/day, want 288 (300s interval)", s.Overview)
	}
	if s.SearchIndex != 20 {
		t.Errorf("search index = %d/day, want 20 (2000 coins in pages of 100)", s.SearchIndex)
	}
	if s.Reconcile != 96 {
		t.Errorf("reconcile = %d/day, want 96 (every 15m)", s.Reconcile)
	}
	if s.Total != 6164 {
		t.Errorf("total = %d/day, want 6164", s.Total)
	}
	if s.OverCeiling() {
		t.Errorf("total %d exceeds the ceiling %d", s.Total, s.Ceiling)
	}
	if s.OverAPILimit() {
		t.Errorf("total %d exceeds the API limit %d", s.Total, s.APILimit)
	}
}

// The original 10s/60s pairing is what made 15s/300s the default. If this ever
// stops exceeding the limit, the reasoning behind the default has changed.
func TestTenSecondIntervalExceedsTheAPILimit(t *testing.T) {
	c := Default()
	c.Poll.ActiveInterval = d(10 * time.Second)
	c.Overview.Interval = d(60 * time.Second)

	s := c.ProjectSpend()
	if s.Coins != 8640 {
		t.Errorf("coins = %d/day, want 8640", s.Coins)
	}
	if s.Overview != 1440 {
		t.Errorf("overview = %d/day, want 1440", s.Overview)
	}
	if s.Total != 10196 {
		t.Errorf("total = %d/day, want 10196", s.Total)
	}
	if !s.OverAPILimit() {
		t.Errorf("total %d should exceed the API limit %d", s.Total, s.APILimit)
	}
}

// 10s is still reachable, just not the default.
func TestTenSecondIntervalStillValidates(t *testing.T) {
	c := Default()
	c.Poll.ActiveInterval = d(10 * time.Second)
	if err := c.Validate(); err != nil {
		t.Fatalf("10s must remain configurable:\n%v", err)
	}
}

func TestHistoryFootprintIsBounded(t *testing.T) {
	h := Default().History

	if got := h.SlotsPerCoin(); got != 13080 {
		t.Errorf("slots per coin = %d, want 13080 (1440 + 2880 + 8760)", got)
	}
	if got := h.BytesPerCoin(); got != 209280 {
		t.Errorf("bytes per coin = %d, want 209280", got)
	}
	// Roughly 50 MB for 250 coins, and constant: a ring buffer overwrites.
	if mb := h.TotalBytes() / (1 << 20); mb < 40 || mb > 60 {
		t.Errorf("total = %d MB, want 40..60", mb)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing config must not error: %v", err)
	}
	if cfg.Server.Listen != Default().Server.Listen {
		t.Errorf("listen = %q, want the default", cfg.Server.Listen)
	}
}

func TestLoadOverlaysOntoDefaults(t *testing.T) {
	path := write(t, `
server:
  listen: "127.0.0.1:9999"
poll:
  active_interval: 30s
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:9999" {
		t.Errorf("listen = %q", cfg.Server.Listen)
	}
	if cfg.Poll.ActiveInterval.D() != 30*time.Second {
		t.Errorf("active_interval = %s", cfg.Poll.ActiveInterval)
	}
	// Untouched keys keep their defaults.
	if cfg.Poll.IdleIntervalHidden.D() != 120*time.Second {
		t.Errorf("idle_interval_hidden = %s, want the default 2m", cfg.Poll.IdleIntervalHidden)
	}
	if cfg.Credits.DailyCeiling != 9000 {
		t.Errorf("daily_ceiling = %d, want the default", cfg.Credits.DailyCeiling)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	// A silently ignored key in a scheduler config means the program runs at a
	// rate the user did not ask for.
	path := write(t, "poll:\n  active_intervall: 30s\n")
	if _, err := Load(path); err == nil {
		t.Fatal("a misspelled key must be rejected")
	} else if !strings.Contains(err.Error(), "active_intervall") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestDurationNeedsAUnit(t *testing.T) {
	path := write(t, "poll:\n  active_interval: 15\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("a bare number must be rejected, not read as 15 nanoseconds")
	}
	if !strings.Contains(err.Error(), "unit") {
		t.Errorf("error should mention the missing unit, got: %v", err)
	}
}

func TestDurationParsesGoSyntax(t *testing.T) {
	path := write(t, "poll:\n  active_interval: 2m30s\ncache:\n  detail_ttl: 6h\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Poll.ActiveInterval.D() != 150*time.Second {
		t.Errorf("active_interval = %s", cfg.Poll.ActiveInterval)
	}
	if cfg.Cache.DetailTTL.D() != 6*time.Hour {
		t.Errorf("detail_ttl = %s", cfg.Cache.DetailTTL)
	}
}

func TestValidationCatchesEachProblem(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"bad listen", func(c *Config) { c.Server.Listen = "nonsense" }, "host:port"},
		{"bad port", func(c *Config) { c.Server.Listen = "127.0.0.1:99999" }, "1..65535"},
		{"non-loopback without opt-in", func(c *Config) { c.Server.Listen = "0.0.0.0:8787" }, "allow_non_loopback"},
		{"bad log level", func(c *Config) { c.Log.Level = "verbose" }, "log.level"},
		{"interval below min gap", func(c *Config) { c.Poll.ActiveInterval = d(time.Second) }, "min_request_gap"},
		{"ttl under heartbeat", func(c *Config) { c.Poll.PresenceTTL = d(time.Second) }, "presence_ttl"},
		{"no sse heartbeat", func(c *Config) { c.Poll.SSEHeartbeat = 0 }, "sse_heartbeat"},
		{"limit over 100", func(c *Config) { c.Coins.Limit = 101 }, "coins.limit"},
		{"delta window as sort", func(c *Config) { c.Coins.Sort = "day" }, "coins.sort"},
		{"bad view", func(c *Config) { c.Coins.DefaultView = "grid" }, "default_view"},
		{"ceiling over api limit", func(c *Config) { c.Credits.DailyCeiling = 20000 }, "api_daily_limit"},
		{"no hysteresis", func(c *Config) { c.Credits.ConserveRecoverAt = 0.9 }, "conserve_recover_at"},
		{"conserve above critical", func(c *Config) { c.Credits.ConserveAt = 0.99 }, "conserve_at"},
		{"zero min gap", func(c *Config) { c.Credits.MinRequestGap = 0 }, "min_request_gap"},
		{"reserve eats the ceiling", func(c *Config) { c.Credits.ReserveForOnDemand = 9000 }, "reserve_for_ondemand"},
		{"bad refresh time", func(c *Config) { c.SearchIndex.RefreshAt = "25:00" }, "refresh_at"},
		{"bad watchlist source", func(c *Config) { c.Watchlist.Source = "magic" }, "watchlist.source"},
		{"unknown sink", func(c *Config) { c.Alerts.Sinks = []string{"telegram"} }, "alerts.sinks"},
		{"history finer than poll", func(c *Config) {
			c.History.Tiers[0].Resolution = d(time.Second)
		}, "finer than poll.active_interval"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got:\n%v", tc.want, err)
			}
		})
	}
}

func TestValidationReportsEveryProblemAtOnce(t *testing.T) {
	c := Default()
	c.Log.Level = "verbose"
	c.Coins.Limit = 500
	c.Watchlist.Source = "magic"

	err := c.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{"log.level", "coins.limit", "watchlist.source"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q so one run fixes everything, got:\n%v", want, err)
		}
	}
}

func TestHistoryTiersMustBeOrderedFineToCoarse(t *testing.T) {
	c := Default()
	c.History.Tiers = []HistoryTier{
		{Resolution: d(time.Hour), Retention: d(365 * 24 * time.Hour)},
		{Resolution: d(time.Minute), Retention: d(24 * time.Hour)},
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "fine to coarse") {
		t.Errorf("want an ordering error, got: %v", err)
	}
}

func TestAlertRuleValidation(t *testing.T) {
	tests := []struct {
		name string
		rule alerts.Rule
		want string
	}{
		{"no id", alerts.Rule{Severity: alerts.SeverityWarn}, "needs an id"},
		{
			"two scopes",
			alerts.Rule{ID: "a", Severity: alerts.SeverityWarn, Rearm: alerts.RearmOnExit,
				Scope:     alerts.Scope{Coin: "BTC", Watchlist: true},
				Condition: alerts.Condition{Metric: alerts.MetricPrice, Op: alerts.OpGT, Value: 1}},
			"exactly one",
		},
		{
			"delta without window",
			alerts.Rule{ID: "a", Severity: alerts.SeverityWarn, Rearm: alerts.RearmOnExit,
				Scope:     alerts.Scope{Coin: "BTC"},
				Condition: alerts.Condition{Metric: alerts.MetricDelta, Op: alerts.OpGT, Value: 5}},
			"needs a window",
		},
		{
			"abs_gt with a negative threshold",
			alerts.Rule{ID: "a", Severity: alerts.SeverityWarn, Rearm: alerts.RearmOnExit,
				Scope: alerts.Scope{Coin: "BTC"},
				Condition: alerts.Condition{Metric: alerts.MetricDelta, Window: "day",
					Op: alerts.OpAbsGT, Value: -5}},
			"must be positive",
		},
		{
			"window on a price rule",
			alerts.Rule{ID: "a", Severity: alerts.SeverityWarn, Rearm: alerts.RearmOnExit,
				Scope: alerts.Scope{Coin: "BTC"},
				Condition: alerts.Condition{Metric: alerts.MetricPrice, Window: "day",
					Op: alerts.OpGT, Value: 100}},
			"only to metric delta",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rule.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestDuplicateRuleIDsAreRejected(t *testing.T) {
	c := Default()
	rule := alerts.Rule{
		ID: "dup", Name: "x", Severity: alerts.SeverityWarn, Rearm: alerts.RearmOnExit,
		Scope:     alerts.Scope{Coin: "BTC"},
		Condition: alerts.Condition{Metric: alerts.MetricPrice, Op: alerts.OpCrossesAbove, Value: 100000},
	}
	c.Alerts.Rules = []alerts.Rule{rule, rule}

	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "repeats id") {
		t.Errorf("duplicate ids must be rejected: they key persisted arm state. got: %v", err)
	}
}

func TestValidAlertRulePasses(t *testing.T) {
	c := Default()
	c.Alerts.Rules = []alerts.Rule{{
		ID: "btc-100k", Name: "BTC above 100k", Severity: alerts.SeverityWarn,
		Scope:     alerts.Scope{Coin: "BTC"},
		Condition: alerts.Condition{Metric: alerts.MetricPrice, Op: alerts.OpCrossesAbove, Value: 100000},
		Cooldown:  alerts.Dur(30 * time.Minute), Rearm: alerts.RearmOnExit,
		HysteresisPct: 0.5, MaxFiresPerDay: 20,
	}, {
		ID: "watchlist-swing", Name: "Watchlist 10% day move", Severity: alerts.SeverityInfo,
		Scope: alerts.Scope{Watchlist: true},
		Condition: alerts.Condition{Metric: alerts.MetricDelta, Window: "day",
			Op: alerts.OpAbsGT, Value: 10},
		Cooldown: alerts.Dur(time.Hour), Rearm: alerts.RearmAfterCooldown,
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid rules must pass:\n%v", err)
	}
}

func TestAPIKeyComesFromEnvAndIsRedacted(t *testing.T) {
	t.Setenv("LCW_API_KEY", "  secret-key  ")
	c := Default()
	c.ApplyEnv()

	if c.APIKey != "secret-key" {
		t.Errorf("APIKey = %q, want it trimmed", c.APIKey)
	}
	if !c.HasAPIKey() {
		t.Error("HasAPIKey should be true")
	}
	if r := c.Redacted(); r.APIKey != "" {
		t.Errorf("Redacted still carries the key: %q", r.APIKey)
	}
}

func TestAPIKeyCannotComeFromYAML(t *testing.T) {
	// api_key is not a field, so KnownFields rejects it rather than reading a
	// secret out of a file meant to be committed.
	path := write(t, "api_key: leaked\n")
	if _, err := Load(path); err == nil {
		t.Error("a config carrying an api_key must be rejected")
	}
}

func TestEnvOverridesListenAndLogLevel(t *testing.T) {
	t.Setenv("LCW_LISTEN", "127.0.0.1:1234")
	t.Setenv("LCW_LOG_LEVEL", "debug")
	c := Default()
	c.ApplyEnv()

	if c.Server.Listen != "127.0.0.1:1234" {
		t.Errorf("listen = %q", c.Server.Listen)
	}
	if c.Log.Level != "debug" {
		t.Errorf("level = %q", c.Log.Level)
	}
}

func TestIsLoopback(t *testing.T) {
	for listen, want := range map[string]bool{
		"127.0.0.1:8787": true,
		"[::1]:8787":     true,
		"localhost:8787": true,
		"0.0.0.0:8787":   false,
		"192.168.1.5:80": false,
		":8787":          false,
	} {
		c := Default()
		c.Server.Listen = listen
		if got := c.IsLoopback(); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", listen, got, want)
		}
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
