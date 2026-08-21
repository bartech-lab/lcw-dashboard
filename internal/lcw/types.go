package lcw

// Wire types mirroring Live Coin Watch responses. These are the raw shapes; the
// snapshot package converts them into what the browser sees.
//
// Every optional number is a *float64, not a float64. The API omits fields for
// coins it has no data for — HYPE has no market cap — and a zero would render
// as "$0" instead of "-".

// Delta holds rates of change as the API sends them: multipliers where 1.0 means
// no change.
//
// The documentation claims the range is 0..2. It is not. Live data from Live Coin
// Watch's own site includes delta.year = 16.9399 for ZEC, which is +1593.99%.
// Never clamp, never validate against [0,2], and never key a colour ramp or a
// fixed-width bar to ±1. Use DeltaPct to convert.
type Delta struct {
	Hour    *float64 `json:"hour"`
	Day     *float64 `json:"day"`
	Week    *float64 `json:"week"`
	Month   *float64 `json:"month"`
	Quarter *float64 `json:"quarter"`
	Year    *float64 `json:"year"`
}

// Coin is a coin as returned by /coins/list, /coins/map, /coins/single and
// /coins/contract. Fields beyond code/rate/volume/cap arrive only when the
// request sets meta: true.
type Coin struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
	Rank   int    `json:"rank"`
	Age    int    `json:"age"` // days
	Color  string `json:"color"`

	PNG32  string `json:"png32"`
	PNG64  string `json:"png64"`
	WebP32 string `json:"webp32"`
	WebP64 string `json:"webp64"`

	Exchanges int `json:"exchanges"`
	Markets   int `json:"markets"`
	Pairs     int `json:"pairs"`

	AllTimeHighUSD    *float64 `json:"allTimeHighUSD"`
	CirculatingSupply *float64 `json:"circulatingSupply"`
	TotalSupply       *float64 `json:"totalSupply"`
	MaxSupply         *float64 `json:"maxSupply"`

	Rate      *float64 `json:"rate"`
	Volume    *float64 `json:"volume"`
	Cap       *float64 `json:"cap"`
	Liquidity *float64 `json:"liquidity"`
	TotalCap  *float64 `json:"totalCap"`

	Categories []string `json:"categories"`
	Delta      Delta    `json:"delta"`
}

// HistoryPoint is one sample from /coins/single/history.
type HistoryPoint struct {
	Date   int64    `json:"date"` // UNIX milliseconds
	Rate   *float64 `json:"rate"`
	Volume *float64 `json:"volume"`
	Cap    *float64 `json:"cap"`
}

// CoinHistory is the /coins/single/history response: coin metadata plus samples.
type CoinHistory struct {
	Coin
	History []HistoryPoint `json:"history"`
}

// Overview is the /overview response, aggregated across all coins.
type Overview struct {
	Cap       *float64 `json:"cap"`
	Volume    *float64 `json:"volume"`
	Liquidity *float64 `json:"liquidity"`
	// BTCDominance is a fraction, not a percentage: 0.5423 means 54.23%.
	BTCDominance *float64 `json:"btcDominance"`
}

// OverviewPoint is one sample from /overview/history.
type OverviewPoint struct {
	Date int64 `json:"date"` // UNIX milliseconds
	Overview
}

// Credits is the /credits response.
type Credits struct {
	DailyCreditsRemaining int `json:"dailyCreditsRemaining"`
	DailyCreditsLimit     int `json:"dailyCreditsLimit"`
}

// Fiat is one entry from /fiats/all.
type Fiat struct {
	Code      string   `json:"code"`
	Name      string   `json:"name"`
	Symbol    string   `json:"symbol"`
	Flag      string   `json:"flag"`
	Countries []string `json:"countries"`
}

// ---------------------------------------------------------------- requests

// SortField is an accepted value for the sort parameter.
//
// This list is the complete set the API supports. Notably it contains no delta
// window, so the API cannot rank coins by price change over any period. That is
// a hard limit, not an oversight in this client.
type SortField string

const (
	SortRank   SortField = "rank"
	SortPrice  SortField = "price"
	SortVolume SortField = "volume"
	SortCode   SortField = "code"
	SortName   SortField = "name"
	SortAge    SortField = "age"
)

// ValidSortFields lists every sortable field, for config validation and for the
// frontend to decide which column headers may offer market-wide sorting.
func ValidSortFields() []SortField {
	return []SortField{SortRank, SortPrice, SortVolume, SortCode, SortName, SortAge}
}

// Valid reports whether s is a sort field the API accepts.
func (s SortField) Valid() bool {
	for _, v := range ValidSortFields() {
		if s == v {
			return true
		}
	}
	return false
}

// SortOrder is an accepted value for the order parameter.
type SortOrder string

const (
	OrderAscending  SortOrder = "ascending"
	OrderDescending SortOrder = "descending"
)

func (o SortOrder) Valid() bool { return o == OrderAscending || o == OrderDescending }

// MaxListLimit is the largest page the API will return from any list endpoint.
const MaxListLimit = 100

type coinsListRequest struct {
	Currency string    `json:"currency"`
	Sort     SortField `json:"sort"`
	Order    SortOrder `json:"order"`
	Offset   int       `json:"offset"`
	Limit    int       `json:"limit"`
	Meta     bool      `json:"meta"`
}

type coinsMapRequest struct {
	Codes    []string  `json:"codes"`
	Currency string    `json:"currency"`
	Sort     SortField `json:"sort"`
	Order    SortOrder `json:"order"`
	Offset   int       `json:"offset"`
	Limit    int       `json:"limit"`
	Meta     bool      `json:"meta"`
}

type coinsSingleRequest struct {
	Currency string `json:"currency"`
	Code     string `json:"code"`
	Meta     bool   `json:"meta"`
}

type coinsHistoryRequest struct {
	Currency string `json:"currency"`
	Code     string `json:"code"`
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Meta     bool   `json:"meta"`
}

type overviewRequest struct {
	Currency string `json:"currency"`
}

type overviewHistoryRequest struct {
	Currency string `json:"currency"`
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
}
