package config

import (
	"time"

	"github.com/bartech/lcw-dashboard/internal/lcw"
)

func d(v time.Duration) Duration { return Duration(v) }

// Default returns a fully populated config. Every zero value a user can leave
// out of their YAML comes from here, so this is the single source of defaults.
//
// The interval choices are budget-driven. At 15s the coin loop costs 5,760
// credits/day; overview at 300s costs 288; index and reconcile add ~116. That
// totals ~6,164 against a 10,000 limit. A 10s coin loop plus a 60s overview
// totals 10,196 and does not fit, which is why 15s is the default and
// ProjectSpend warns rather than the guard silently taking over every afternoon.
func Default() Config {
	return Config{
		Server: Server{
			Listen:           "127.0.0.1:8787",
			ReadTimeout:      d(10 * time.Second),
			ShutdownTimeout:  d(5 * time.Second),
			OpenBrowser:      false,
			AllowNonLoopback: false,
		},
		Log: Log{Level: "info", Format: "text", HTTPRequests: false},
		API: API{
			BaseURL:   lcw.DefaultBaseURL,
			Timeout:   d(15 * time.Second),
			UserAgent: "lcw-dashboard",
		},
		Poll: Poll{
			ActiveInterval:        d(15 * time.Second),
			IdleIntervalHidden:    d(120 * time.Second),
			IdleIntervalNoClients: d(300 * time.Second),
			CriticalInterval:      d(600 * time.Second),
			FocusRefreshThreshold: d(10 * time.Second),
			FocusDebounce:         d(2 * time.Second),
			PresenceHeartbeat:     d(20 * time.Second),
			PresenceTTL:           d(45 * time.Second),
			MaxRotationKeys:       3,
			SSEHeartbeat:          d(15 * time.Second),
		},
		Coins: Coins{
			DefaultView:  ViewTop,
			Limit:        lcw.MaxListLimit,
			Sort:         lcw.SortRank,
			Order:        lcw.OrderAscending,
			Meta:         true,
			WatchlistMax: 300,
			ChunkSize:    lcw.MaxListLimit,
		},
		Overview: Overview{
			Enabled:        true,
			Interval:       d(300 * time.Second),
			HiddenInterval: d(900 * time.Second),
			HistoryEnabled: false,
		},
		Currency: Currency{
			Default:   "USD",
			Allowlist: nil,
			Denylist:  nil,
			FiatsTTL:  d(720 * time.Hour),
		},
		Credits: Credits{
			DailyCeiling:       9000,
			APIDailyLimit:      10000,
			ConserveAt:         0.80,
			ConserveRecoverAt:  0.75,
			CriticalAt:         0.95,
			CriticalRecoverAt:  0.90,
			ReconcileInterval:  d(15 * time.Minute),
			MinRequestGap:      d(2 * time.Second),
			Burst:              20,
			ReserveForOnDemand: 300,
		},
		Cache: Cache{
			DetailTTL:       d(15 * time.Minute),
			HistoryTTLShort: d(30 * time.Minute),
			HistoryTTLLong:  d(6 * time.Hour),
			DetailLRUSize:   200,
			PersistLastGood: true,
		},
		History: History{
			Enabled: true,
			Tiers: []HistoryTier{
				{Resolution: d(time.Minute), Retention: d(24 * time.Hour)},
				{Resolution: d(15 * time.Minute), Retention: d(30 * 24 * time.Hour)},
				{Resolution: d(time.Hour), Retention: d(365 * 24 * time.Hour)},
			},
			MaxCoins:      250,
			FlushInterval: d(60 * time.Second),
		},
		SearchIndex: SearchIndex{
			Enabled:    true,
			Coins:      2000,
			PageSize:   lcw.MaxListLimit,
			RefreshAt:  "00:15",
			BuildDelay: d(5 * time.Second),
			PageGap:    d(300 * time.Millisecond),
			MaxResults: 25,
			MinScore:   0.35,
		},
		Watchlist: Watchlist{
			Source:  WatchlistServer,
			Initial: []string{"BTC", "ETH", "SOL"},
		},
		Alerts: Alerts{
			Enabled:           true,
			RestartGrace:      d(60 * time.Second),
			DefaultCooldown:   d(30 * time.Minute),
			MaxEventsKept:     200,
			PollWhenNoClients: true,
			Sinks:             []string{SinkNative, SinkBrowser, SinkLog},
			Rules:             nil,
		},
		Debug: Debug{Enabled: false, PProf: false},
	}
}

const (
	ViewTop        = "top"
	ViewFavourites = "favourites"

	WatchlistServer = "server"
	WatchlistClient = "client"

	SinkNative  = "native"
	SinkBrowser = "browser"
	SinkLog     = "log"
)

// HistoryBytesPerCoin is the fixed on-disk size of one coin's ring file:
// float64 rate plus float32 volume and cap.
const HistoryBytesPerCoin = 16

func (h History) SlotsPerCoin() int {
	n := 0
	for _, t := range h.Tiers {
		n += t.Slots()
	}
	return n
}

// BytesPerCoin and TotalBytes are constant for the process lifetime, which is
// the point of using ring buffers instead of appending.
func (h History) BytesPerCoin() int64 {
	return int64(h.SlotsPerCoin()) * HistoryBytesPerCoin
}

func (h History) TotalBytes() int64 {
	return h.BytesPerCoin() * int64(h.MaxCoins)
}

// Spend projects worst-case daily credit use from the configured intervals.
type Spend struct {
	Coins       int
	Overview    int
	SearchIndex int
	Reconcile   int
	Total       int
	Ceiling     int
	APILimit    int
}

// OverCeiling and OverAPILimit are what main logs loudly at startup, so a
// too-fast interval is visible on the first run rather than discovered as an
// afternoon slowdown.
func (s Spend) OverCeiling() bool  { return s.Total > s.Ceiling }
func (s Spend) OverAPILimit() bool { return s.Total > s.APILimit }

// ProjectSpend assumes a tab is visible all day, which is the configured worst
// case and also the stated use case.
func (c Config) ProjectSpend() Spend {
	const day = 24 * time.Hour

	perDay := func(interval Duration) int {
		if interval.D() <= 0 {
			return 0
		}
		return int(day / interval.D())
	}

	s := Spend{
		Coins:     perDay(c.Poll.ActiveInterval),
		Reconcile: perDay(c.Credits.ReconcileInterval),
		Ceiling:   c.Credits.DailyCeiling,
		APILimit:  c.Credits.APIDailyLimit,
	}
	if c.Overview.Enabled {
		s.Overview = perDay(c.Overview.Interval)
	}
	if c.SearchIndex.Enabled && c.SearchIndex.PageSize > 0 {
		pages := (c.SearchIndex.Coins + c.SearchIndex.PageSize - 1) / c.SearchIndex.PageSize
		s.SearchIndex = pages
	}
	s.Total = s.Coins + s.Overview + s.SearchIndex + s.Reconcile
	return s
}
