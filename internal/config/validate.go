package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/bartech/lcw-dashboard/internal/alerts"
	"github.com/bartech/lcw-dashboard/internal/lcw"
)

// Validate collects every problem rather than stopping at the first, so one run
// fixes a whole broken config.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	// server
	if _, port, err := net.SplitHostPort(c.Server.Listen); err != nil {
		add("server.listen %q is not host:port: %v", c.Server.Listen, err)
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		add("server.listen port %q is not 1..65535", port)
	}
	if !c.IsLoopback() && !c.Server.AllowNonLoopback {
		add("server.listen %q is not loopback; the dashboard has no authentication, "+
			"so set server.allow_non_loopback: true to bind it anyway", c.Server.Listen)
	}
	if c.Server.ReadTimeout.D() <= 0 {
		add("server.read_timeout must be positive")
	}

	// log
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level %q is not debug|info|warn|error", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		add("log.format %q is not text|json", c.Log.Format)
	}

	// api
	if c.API.BaseURL == "" {
		add("api.base_url is empty")
	}
	if c.API.Timeout.D() <= 0 {
		add("api.timeout must be positive")
	}

	// poll
	gap := c.Credits.MinRequestGap.D()
	for _, f := range []struct {
		name string
		v    Duration
	}{
		{"poll.active_interval", c.Poll.ActiveInterval},
		{"poll.idle_interval_hidden", c.Poll.IdleIntervalHidden},
		{"poll.critical_interval", c.Poll.CriticalInterval},
	} {
		if f.v.D() <= 0 {
			add("%s must be positive", f.name)
			continue
		}
		if f.v.D() < gap {
			add("%s (%s) is below credits.min_request_gap (%s); the rate limiter would "+
				"stall every tick", f.name, f.v, c.Credits.MinRequestGap)
		}
	}
	if c.Poll.IdleIntervalNoClients.D() < 0 {
		add("poll.idle_interval_no_clients cannot be negative (0 means stop polling)")
	}
	if c.Poll.PresenceTTL.D() <= c.Poll.PresenceHeartbeat.D() {
		add("poll.presence_ttl (%s) must exceed poll.presence_heartbeat (%s), or every "+
			"client expires between heartbeats", c.Poll.PresenceTTL, c.Poll.PresenceHeartbeat)
	}
	if c.Poll.MaxRotationKeys < 1 {
		add("poll.max_rotation_keys must be at least 1")
	}
	if c.Poll.SSEHeartbeat.D() <= 0 {
		add("poll.sse_heartbeat must be positive, or a half-open connection is " +
			"indistinguishable from a quiet market")
	}

	// coins
	switch c.Coins.DefaultView {
	case ViewTop, ViewFavourites:
	default:
		add("coins.default_view %q is not %s|%s", c.Coins.DefaultView, ViewTop, ViewFavourites)
	}
	if c.Coins.Limit < 1 || c.Coins.Limit > lcw.MaxListLimit {
		add("coins.limit must be 1..%d, got %d", lcw.MaxListLimit, c.Coins.Limit)
	}
	if !c.Coins.Sort.Valid() {
		add("coins.sort %q is not one of %v (the API cannot sort by a delta window)",
			c.Coins.Sort, lcw.ValidSortFields())
	}
	if !c.Coins.Order.Valid() {
		add("coins.order %q is not ascending|descending", c.Coins.Order)
	}
	if c.Coins.ChunkSize < 1 || c.Coins.ChunkSize > lcw.MaxListLimit {
		add("coins.chunk_size must be 1..%d, got %d", lcw.MaxListLimit, c.Coins.ChunkSize)
	}
	if c.Coins.WatchlistMax < 1 {
		add("coins.watchlist_max must be at least 1")
	}

	// overview
	if c.Overview.Enabled {
		if c.Overview.Interval.D() <= 0 {
			add("overview.interval must be positive when overview is enabled")
		} else if c.Overview.Interval.D() < gap {
			add("overview.interval (%s) is below credits.min_request_gap (%s)",
				c.Overview.Interval, c.Credits.MinRequestGap)
		}
	}

	// currency
	if c.Currency.Default == "" {
		add("currency.default is empty")
	}
	if len(c.Currency.Allowlist) > 0 {
		found := false
		for _, code := range c.Currency.Allowlist {
			if strings.EqualFold(code, c.Currency.Default) {
				found = true
				break
			}
		}
		if !found {
			add("currency.default %q is missing from currency.allowlist %v, so the "+
				"dashboard would start on a currency it will not offer",
				c.Currency.Default, c.Currency.Allowlist)
		}
	}
	for _, code := range c.Currency.Denylist {
		if strings.EqualFold(code, c.Currency.Default) {
			add("currency.default %q is also in currency.denylist", c.Currency.Default)
		}
	}

	// credits
	if c.Credits.APIDailyLimit < 1 {
		add("credits.api_daily_limit must be positive")
	}
	if c.Credits.DailyCeiling < 1 {
		add("credits.daily_ceiling must be positive")
	}
	if c.Credits.DailyCeiling > c.Credits.APIDailyLimit {
		add("credits.daily_ceiling (%d) exceeds credits.api_daily_limit (%d), so the "+
			"guard would never engage before the API refused",
			c.Credits.DailyCeiling, c.Credits.APIDailyLimit)
	}
	if c.Credits.MinRequestGap.D() <= 0 {
		add("credits.min_request_gap must be positive; it is the last defence against " +
			"a runaway poll loop")
	}
	if c.Credits.Burst < 1 {
		add("credits.burst must be at least 1")
	}
	if c.Credits.ReconcileInterval.D() <= 0 {
		add("credits.reconcile_interval must be positive")
	}
	if c.Credits.ReserveForOnDemand < 0 {
		add("credits.reserve_for_ondemand cannot be negative")
	}
	if c.Credits.ReserveForOnDemand >= c.Credits.DailyCeiling {
		add("credits.reserve_for_ondemand (%d) leaves nothing for polling under a "+
			"ceiling of %d", c.Credits.ReserveForOnDemand, c.Credits.DailyCeiling)
	}
	// Hysteresis: each recover threshold must sit below its trigger, or the
	// budget state flaps and the interval flaps with it.
	if c.Credits.ConserveRecoverAt >= c.Credits.ConserveAt {
		add("credits.conserve_recover_at (%v) must be below credits.conserve_at (%v)",
			c.Credits.ConserveRecoverAt, c.Credits.ConserveAt)
	}
	if c.Credits.CriticalRecoverAt >= c.Credits.CriticalAt {
		add("credits.critical_recover_at (%v) must be below credits.critical_at (%v)",
			c.Credits.CriticalRecoverAt, c.Credits.CriticalAt)
	}
	if c.Credits.ConserveAt >= c.Credits.CriticalAt {
		add("credits.conserve_at (%v) must be below credits.critical_at (%v)",
			c.Credits.ConserveAt, c.Credits.CriticalAt)
	}
	for _, f := range []struct {
		name string
		v    float64
	}{
		{"credits.conserve_at", c.Credits.ConserveAt},
		{"credits.conserve_recover_at", c.Credits.ConserveRecoverAt},
		{"credits.critical_at", c.Credits.CriticalAt},
		{"credits.critical_recover_at", c.Credits.CriticalRecoverAt},
	} {
		if f.v <= 0 || f.v > 1 {
			add("%s must be in (0, 1], got %v", f.name, f.v)
		}
	}

	// cache
	if c.Cache.DetailLRUSize < 1 {
		add("cache.detail_lru_size must be at least 1")
	}
	for _, f := range []struct {
		name string
		v    Duration
	}{
		{"cache.detail_ttl", c.Cache.DetailTTL},
		{"cache.history_ttl_short", c.Cache.HistoryTTLShort},
		{"cache.history_ttl_long", c.Cache.HistoryTTLLong},
	} {
		if f.v.D() <= 0 {
			add("%s must be positive", f.name)
		}
	}

	// history
	if c.History.Enabled {
		if len(c.History.Tiers) == 0 {
			add("history.tiers is empty but history is enabled")
		}
		if c.History.MaxCoins < 1 {
			add("history.max_coins must be at least 1")
		}
		if c.History.FlushInterval.D() <= 0 {
			add("history.flush_interval must be positive")
		}
		prev := time.Duration(0)
		for i, t := range c.History.Tiers {
			if t.Resolution.D() <= 0 {
				add("history.tiers[%d].resolution must be positive", i)
				continue
			}
			if t.Retention.D() < t.Resolution.D() {
				add("history.tiers[%d] retains %s at %s resolution, which is fewer than "+
					"one sample", i, t.Retention, t.Resolution)
			}
			if t.Resolution.D() <= prev {
				add("history.tiers[%d] resolution %s is not coarser than the previous "+
					"tier's %s; tiers must be ordered fine to coarse",
					i, t.Resolution, Duration(prev))
			}
			prev = t.Resolution.D()
		}
		// The poll interval sets the finest resolution worth keeping.
		if len(c.History.Tiers) > 0 {
			if finest := c.History.Tiers[0].Resolution.D(); finest < c.Poll.ActiveInterval.D() {
				add("history.tiers[0].resolution (%s) is finer than poll.active_interval "+
					"(%s), so most slots would stay empty",
					c.History.Tiers[0].Resolution, c.Poll.ActiveInterval)
			}
		}
	}

	// search index
	if c.SearchIndex.Enabled {
		if c.SearchIndex.PageSize < 1 || c.SearchIndex.PageSize > lcw.MaxListLimit {
			add("search_index.page_size must be 1..%d, got %d", lcw.MaxListLimit, c.SearchIndex.PageSize)
		}
		if c.SearchIndex.Coins < 1 {
			add("search_index.coins must be at least 1")
		}
		if err := validateHHMM(c.SearchIndex.RefreshAt); err != nil {
			add("search_index.refresh_at %q: %v", c.SearchIndex.RefreshAt, err)
		}
		if c.SearchIndex.MaxResults < 1 {
			add("search_index.max_results must be at least 1")
		}
		if c.SearchIndex.MinScore < 0 || c.SearchIndex.MinScore > 1 {
			add("search_index.min_score must be in [0, 1], got %v", c.SearchIndex.MinScore)
		}
	}

	// watchlist
	switch c.Watchlist.Source {
	case WatchlistServer, WatchlistClient:
	default:
		add("watchlist.source %q is not %s|%s", c.Watchlist.Source, WatchlistServer, WatchlistClient)
	}
	if len(c.Watchlist.Initial) > c.Coins.WatchlistMax {
		add("watchlist.initial has %d codes, above coins.watchlist_max (%d)",
			len(c.Watchlist.Initial), c.Coins.WatchlistMax)
	}

	// alerts
	if c.Alerts.Enabled {
		if c.Alerts.MaxEventsKept < 1 {
			add("alerts.max_events_kept must be at least 1")
		}
		if len(c.Alerts.Sinks) == 0 {
			add("alerts.enabled is true but alerts.sinks is empty")
		}
		for _, s := range c.Alerts.Sinks {
			switch s {
			case SinkNative, SinkBrowser, SinkLog:
			default:
				add("alerts.sinks contains %q, not %s|%s|%s", s, SinkNative, SinkBrowser, SinkLog)
			}
		}
		seen := make(map[string]int, len(c.Alerts.Rules))
		for i, r := range c.Alerts.Rules {
			if prev, dup := seen[r.ID]; dup {
				add("alerts.rules[%d] repeats id %q from rules[%d]; ids key persisted "+
					"arm state and must be unique", i, r.ID, prev)
			}
			seen[r.ID] = i
			if err := r.Validate(); err != nil {
				add("alerts.rules[%d]: %v", i, err)
			}
			if r.Condition.Metric == alerts.MetricDelta && r.Cooldown.D() == 0 &&
				r.Rearm == alerts.RearmAfterCooldown {
				add("alerts.rules[%d] (%s) re-arms after a zero cooldown, so it fires "+
					"on every tick the condition holds", i, r.ID)
			}
		}
	}

	return errors.Join(errs...)
}

func validateHHMM(s string) error {
	if _, err := time.Parse("15:04", s); err != nil {
		return fmt.Errorf("not HH:MM in 24-hour form")
	}
	return nil
}
