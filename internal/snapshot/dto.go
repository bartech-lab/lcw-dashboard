// Package snapshot defines the wire contract between the Go server and the
// browser, and the immutable world state the HTTP layer reads without locking.
//
// Raw API deltas never appear here. Only ChangePct crosses the wire, so the
// frontend cannot repeat the multiplier-versus-percentage mistake.
package snapshot

import (
	"time"

	"github.com/bartech/lcw-dashboard/internal/lcw"
)

type Icons struct {
	PNG32  string `json:"png32"`
	PNG64  string `json:"png64"`
	WebP32 string `json:"webp32"`
	WebP64 string `json:"webp64"`
}

// CoinRow is one table row. Every optional number is a pointer so a missing
// value renders as a dash rather than as zero.
type CoinRow struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
	// Rank is the coin's global rank, not its position in this list. The visible
	// sequence can read 1, 2, 4, 7 when coins are filtered out.
	Rank  int    `json:"rank"`
	Age   int    `json:"age"`
	Color string `json:"color"`
	Icons Icons  `json:"icons"`

	Rate      *float64 `json:"rate"`
	Volume    *float64 `json:"volume"`
	Cap       *float64 `json:"cap"`
	Liquidity *float64 `json:"liquidity"`
	TotalCap  *float64 `json:"totalCap"`

	AllTimeHighUSD    *float64 `json:"allTimeHighUSD"`
	CirculatingSupply *float64 `json:"circulatingSupply"`
	TotalSupply       *float64 `json:"totalSupply"`
	MaxSupply         *float64 `json:"maxSupply"`

	Exchanges  int      `json:"exchanges"`
	Markets    int      `json:"markets"`
	Pairs      int      `json:"pairs"`
	Categories []string `json:"categories,omitempty"`

	ChangePct lcw.ChangePct `json:"changePct"`
	// FromATHPct is derived here so the frontend has no arithmetic to get wrong.
	FromATHPct *float64 `json:"fromAthPct"`
}

func RowFromCoin(c lcw.Coin) CoinRow {
	return CoinRow{
		Code: c.Code, Name: c.Name, Symbol: c.Symbol,
		Rank: c.Rank, Age: c.Age, Color: c.Color,
		Icons: Icons{PNG32: c.PNG32, PNG64: c.PNG64, WebP32: c.WebP32, WebP64: c.WebP64},

		Rate: c.Rate, Volume: c.Volume, Cap: c.Cap,
		Liquidity: c.Liquidity, TotalCap: c.TotalCap,

		AllTimeHighUSD:    c.AllTimeHighUSD,
		CirculatingSupply: c.CirculatingSupply,
		TotalSupply:       c.TotalSupply,
		MaxSupply:         c.MaxSupply,

		Exchanges: c.Exchanges, Markets: c.Markets, Pairs: c.Pairs,
		Categories: c.Categories,

		ChangePct:  c.Delta.Convert(),
		FromATHPct: lcw.ATHDistancePct(c.Rate, c.AllTimeHighUSD),
	}
}

// View names which set of coins is being shown.
type View string

const (
	ViewTop        View = "top"
	ViewFavourites View = "favourites"
)

// Coins is the table payload.
type Coins struct {
	View     View   `json:"view"`
	Currency string `json:"currency"`
	Sort     string `json:"sort"`
	Order    string `json:"order"`

	AsOf  time.Time `json:"asOf"`
	AgeMs int64     `json:"ageMs"`
	// Stale means the data is being served after a failed refresh. It is shown,
	// never blanked.
	Stale       bool       `json:"stale"`
	StaleSince  *time.Time `json:"staleSince"`
	Error       *WireError `json:"error"`
	CreditsUsed int        `json:"creditsUsed"`
	// Rotating means several view keys share the poll loop, so this view refreshes
	// slower than the configured interval.
	Rotating bool `json:"rotating"`
	// UnknownCodes are watchlist entries the API did not return, surfaced instead
	// of silently vanishing.
	UnknownCodes []string  `json:"unknownCodes,omitempty"`
	Coins        []CoinRow `json:"coins"`
}

type Overview struct {
	Currency  string     `json:"currency"`
	AsOf      time.Time  `json:"asOf"`
	Stale     bool       `json:"stale"`
	Error     *WireError `json:"error"`
	Cap       *float64   `json:"cap"`
	Volume    *float64   `json:"volume"`
	Liquidity *float64   `json:"liquidity"`
	// BTCDominance is a fraction; the UI multiplies by 100.
	BTCDominance *float64 `json:"btcDominance"`
}

type WireError struct {
	Code        int       `json:"code"`
	Status      string    `json:"status"`
	Description string    `json:"description"`
	At          time.Time `json:"at"`
}

// PollState is what the connection indicator shows.
type PollState string

const (
	PollInitializing PollState = "initializing"
	PollActive       PollState = "active"
	PollIdle         PollState = "idle"
	PollConserve     PollState = "conserve"
	PollCritical     PollState = "critical"
	PollExhausted    PollState = "exhausted"
	PollAuthFailed   PollState = "auth_failed"
	PollNoKey        PollState = "no_key"
)

type IndexStatus struct {
	Ready    bool      `json:"ready"`
	Coins    int       `json:"coins"`
	BuiltAt  time.Time `json:"builtAt"`
	Building bool      `json:"building"`
}

type Status struct {
	PollState     PollState `json:"pollState"`
	ActiveViewKey string    `json:"activeViewKey"`
	Rotating      bool      `json:"rotating"`
	RotationKeys  []string  `json:"rotationKeys"`

	IntervalMs int64      `json:"intervalMs"`
	NextTickAt *time.Time `json:"nextTickAt"`

	LastSuccessAt       *time.Time `json:"lastSuccessAt"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	LastError           *WireError `json:"lastError"`

	VisibleClients int `json:"visibleClients"`
	TotalClients   int `json:"totalClients"`
	// ChunkPenalty is how many requests one refresh takes because the watchlist
	// exceeds a single page. The interval is multiplied to match, so the credit
	// rate stays constant.
	ChunkPenalty int `json:"chunkPenalty"`

	SearchIndex IndexStatus `json:"searchIndex"`
	// Revision acknowledges applied control messages, so the UI knows a currency
	// switch actually landed instead of guessing.
	Revision       uint64 `json:"revision"`
	DegradedReason string `json:"degradedReason,omitempty"`
	SetupHint      string `json:"setupHint,omitempty"`
}

// Credits mirrors the ledger report rather than importing it. This package is a
// pure DTO layer: importing credits would pull in config, which references alert
// rules, which import this package.
type Credits struct {
	Day          string         `json:"utcDay"`
	Spend        int            `json:"localSpend"`
	InFlight     int            `json:"inFlight"`
	ByKind       map[string]int `json:"byKind"`
	APIRemaining int            `json:"apiRemaining"`
	APILimit     int            `json:"apiLimit"`
	ReconciledAt time.Time      `json:"reconciledAt"`
	Drift        int            `json:"drift"`
	ResetsAt     time.Time      `json:"resetsAt"`
	BudgetState  string         `json:"budgetState"`
	Ceiling      int            `json:"dailyCeiling"`
}

type Watchlist struct {
	Codes     []string  `json:"codes"`
	Hash      string    `json:"hash"`
	UpdatedAt time.Time `json:"updatedAt"`
	Max       int       `json:"max"`
}

type Fiat struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
	Flag   string `json:"flag"`
}

type Fiats struct {
	Fiats    []Fiat    `json:"fiats"`
	CachedAt time.Time `json:"cachedAt"`
}

// HelloConfig is the server configuration the browser needs, sent once on
// connect so the client never guesses an interval.
type HelloConfig struct {
	ActiveIntervalMs        int64  `json:"activeIntervalMs"`
	IdleIntervalMs          int64  `json:"idleIntervalMs"`
	OverviewIntervalMs      int64  `json:"overviewIntervalMs"`
	FocusRefreshThresholdMs int64  `json:"focusRefreshThresholdMs"`
	PresenceHeartbeatMs     int64  `json:"presenceHeartbeatMs"`
	SSEHeartbeatMs          int64  `json:"sseHeartbeatMs"`
	CoinLimit               int    `json:"coinLimit"`
	DefaultCurrency         string `json:"defaultCurrency"`
	DefaultView             string `json:"defaultView"`
	WatchlistMax            int    `json:"watchlistMax"`
	WatchlistSource         string `json:"watchlistSource"`
	ProjectedDailyCredits   int    `json:"projectedDailyCredits"`
	DailyCeiling            int    `json:"dailyCeiling"`
	AlertsEnabled           bool   `json:"alertsEnabled"`
	HistoryEnabled          bool   `json:"historyEnabled"`
	// SortableFields is the API's own list. The frontend disables market-scope
	// sorting on any column outside it, notably every delta window.
	SortableFields []string `json:"sortableFields"`
	DeltaWindows   []string `json:"deltaWindows"`
	ChartRanges    []string `json:"chartRanges"`
}

type Hello struct {
	ClientID      string      `json:"clientId"`
	ServerVersion string      `json:"serverVersion"`
	StartedAt     time.Time   `json:"startedAt"`
	Config        HelloConfig `json:"config"`
}

// Detail is the coin detail view payload.
type Detail struct {
	Coin        CoinRow   `json:"coin"`
	Range       string    `json:"range"`
	Currency    string    `json:"currency"`
	History     []Point   `json:"history"`
	Source      string    `json:"source"` // local|api|mixed
	FromCache   bool      `json:"fromCache"`
	CreditsUsed int       `json:"creditsUsed"`
	CachedAt    time.Time `json:"cachedAt"`
}

type Point struct {
	Date   int64    `json:"date"`
	Rate   *float64 `json:"rate"`
	Volume *float64 `json:"volume"`
	Cap    *float64 `json:"cap"`
}

// Alert is one fired rule.
//
// FiredAt is the server's own timestamp, not the browser's receive time.
// Chromium buffers SSE to hidden tabs and flushes on focus, so a notification
// that reports "now" would lie about when the threshold was crossed.
type Alert struct {
	EventID   string     `json:"eventId"`
	RuleID    string     `json:"ruleId"`
	RuleName  string     `json:"ruleName"`
	Severity  string     `json:"severity"`
	Code      string     `json:"code"`
	Name      string     `json:"name"`
	Currency  string     `json:"currency"`
	Metric    string     `json:"metric"`
	Window    string     `json:"window,omitempty"`
	Op        string     `json:"op"`
	Threshold float64    `json:"threshold"`
	Value     float64    `json:"value"`
	Previous  *float64   `json:"previousValue"`
	FiredAt   time.Time  `json:"firedAt"`
	Message   string     `json:"message"`
	Cooldown  *time.Time `json:"cooldownUntil"`
}
