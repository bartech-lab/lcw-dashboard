// Package config holds every server tunable. View concerns (columns, theme,
// density, sort) live in the browser's localStorage instead, so changing a
// column needs no restart. The API key never appears here.
package config

import (
	"github.com/bartech/lcw-dashboard/internal/alerts"
	"github.com/bartech/lcw-dashboard/internal/lcw"
)

type Config struct {
	Server      Server      `yaml:"server"`
	Log         Log         `yaml:"log"`
	API         API         `yaml:"api"`
	Poll        Poll        `yaml:"poll"`
	Coins       Coins       `yaml:"coins"`
	Overview    Overview    `yaml:"overview"`
	Currency    Currency    `yaml:"currency"`
	Credits     Credits     `yaml:"credits"`
	Cache       Cache       `yaml:"cache"`
	History     History     `yaml:"history"`
	SearchIndex SearchIndex `yaml:"search_index"`
	Watchlist   Watchlist   `yaml:"watchlist"`
	Alerts      Alerts      `yaml:"alerts"`
	Debug       Debug       `yaml:"debug"`

	APIKey string `yaml:"-" json:"-"`
}

type Server struct {
	Listen string `yaml:"listen"`
	// No WriteTimeout: it would kill every SSE stream on a fixed schedule.
	ReadTimeout      Duration `yaml:"read_timeout"`
	ShutdownTimeout  Duration `yaml:"shutdown_timeout"`
	OpenBrowser      bool     `yaml:"open_browser"`
	AllowNonLoopback bool     `yaml:"allow_non_loopback"`
}

type Log struct {
	Level        string `yaml:"level"`
	Format       string `yaml:"format"`
	HTTPRequests bool   `yaml:"http_requests"`
}

type API struct {
	BaseURL   string   `yaml:"base_url"`
	Timeout   Duration `yaml:"timeout"`
	UserAgent string   `yaml:"user_agent"`
}

type Poll struct {
	ActiveInterval        Duration `yaml:"active_interval"`
	IdleIntervalHidden    Duration `yaml:"idle_interval_hidden"`
	IdleIntervalNoClients Duration `yaml:"idle_interval_no_clients"`
	CriticalInterval      Duration `yaml:"critical_interval"`
	FocusRefreshThreshold Duration `yaml:"focus_refresh_threshold"`
	FocusDebounce         Duration `yaml:"focus_debounce"`
	PresenceHeartbeat     Duration `yaml:"presence_heartbeat"`
	PresenceTTL           Duration `yaml:"presence_ttl"`
	// Each extra rotation key divides the refresh rate rather than multiplying
	// credit spend.
	MaxRotationKeys int `yaml:"max_rotation_keys"`
	// Without a heartbeat, a half-open connection looks like a quiet market.
	SSEHeartbeat Duration `yaml:"sse_heartbeat"`
}

type Coins struct {
	DefaultView string        `yaml:"default_view"`
	Limit       int           `yaml:"limit"`
	Sort        lcw.SortField `yaml:"sort"`
	Order       lcw.SortOrder `yaml:"order"`
	Meta        bool          `yaml:"meta"`
	// Above ChunkSize the fetch splits, and the interval is multiplied to match
	// so the credit rate stays constant.
	WatchlistMax int `yaml:"watchlist_max"`
	ChunkSize    int `yaml:"chunk_size"`
}

type Overview struct {
	Enabled        bool     `yaml:"enabled"`
	Interval       Duration `yaml:"interval"`
	HiddenInterval Duration `yaml:"hidden_interval"`
	HistoryEnabled bool     `yaml:"history_enabled"`
}

type Currency struct {
	Default string `yaml:"default"`
	// Empty allowlist means offer everything /fiats/all returns.
	Allowlist []string `yaml:"allowlist"`
	Denylist  []string `yaml:"denylist"`
	FiatsTTL  Duration `yaml:"fiats_ttl"`
}

type Credits struct {
	DailyCeiling  int `yaml:"daily_ceiling"`
	APIDailyLimit int `yaml:"api_daily_limit"`

	ConserveAt        float64 `yaml:"conserve_at"`
	ConserveRecoverAt float64 `yaml:"conserve_recover_at"`
	CriticalAt        float64 `yaml:"critical_at"`
	CriticalRecoverAt float64 `yaml:"critical_recover_at"`

	ReconcileInterval Duration `yaml:"reconcile_interval"`
	// MinRequestGap is a hard floor below the scheduler: a logic bug above it
	// cannot exceed this rate.
	MinRequestGap      Duration `yaml:"min_request_gap"`
	Burst              int      `yaml:"burst"`
	ReserveForOnDemand int      `yaml:"reserve_for_ondemand"`
}

type Cache struct {
	DetailTTL       Duration `yaml:"detail_ttl"`
	HistoryTTLShort Duration `yaml:"history_ttl_short"`
	HistoryTTLLong  Duration `yaml:"history_ttl_long"`
	DetailLRUSize   int      `yaml:"detail_lru_size"`
	PersistLastGood bool     `yaml:"persist_last_good"`
}

// History records what the poll loop already receives, so it costs no credits.
// Each tier is a preallocated ring buffer: the file reaches its size and stays
// there rather than growing until something prunes it.
type History struct {
	Enabled       bool          `yaml:"enabled"`
	Tiers         []HistoryTier `yaml:"tiers"`
	MaxCoins      int           `yaml:"max_coins"`
	FlushInterval Duration      `yaml:"flush_interval"`
}

type HistoryTier struct {
	Resolution Duration `yaml:"resolution"`
	Retention  Duration `yaml:"retention"`
}

func (t HistoryTier) Slots() int {
	if t.Resolution <= 0 {
		return 0
	}
	return int(t.Retention.D() / t.Resolution.D())
}

type SearchIndex struct {
	Enabled    bool     `yaml:"enabled"`
	Coins      int      `yaml:"coins"`
	PageSize   int      `yaml:"page_size"`
	RefreshAt  string   `yaml:"refresh_at"`
	BuildDelay Duration `yaml:"build_delay"`
	PageGap    Duration `yaml:"page_gap"`
	MaxResults int      `yaml:"max_results"`
	MinScore   float64  `yaml:"min_score"`
}

// Watchlist is server-owned by default: the server needs the list to build a
// /coins/map request, and watchlist alerts must fire with no browser open.
type Watchlist struct {
	Source  string   `yaml:"source"`
	Initial []string `yaml:"initial"`
}

type Alerts struct {
	Enabled           bool     `yaml:"enabled"`
	RestartGrace      Duration `yaml:"restart_grace"`
	DefaultCooldown   Duration `yaml:"default_cooldown"`
	MaxEventsKept     int      `yaml:"max_events_kept"`
	PollWhenNoClients bool     `yaml:"poll_when_no_clients"`
	// native = D-Bus on Linux, osascript on macOS. browser is an enhancement
	// only: Chrome freezes idle background tabs.
	Sinks []string      `yaml:"sinks"`
	Rules []alerts.Rule `yaml:"rules"`
}

type Debug struct {
	Enabled bool `yaml:"enabled"`
	PProf   bool `yaml:"pprof"`
}
