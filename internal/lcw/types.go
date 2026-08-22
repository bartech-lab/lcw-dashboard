package lcw

// Optional numbers are *float64 because the API omits fields for coins it has
// no data for. A zero would render as "$0" instead of "-".

// Delta holds multipliers where 1.0 means no change. See DeltaPct.
type Delta struct {
	Hour    *float64 `json:"hour"`
	Day     *float64 `json:"day"`
	Week    *float64 `json:"week"`
	Month   *float64 `json:"month"`
	Quarter *float64 `json:"quarter"`
	Year    *float64 `json:"year"`
}

// Coin fields beyond code/rate/volume/cap arrive only with meta: true.
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

type HistoryPoint struct {
	Date   int64    `json:"date"` // UNIX milliseconds
	Rate   *float64 `json:"rate"`
	Volume *float64 `json:"volume"`
	Cap    *float64 `json:"cap"`
}

type CoinHistory struct {
	Coin
	History []HistoryPoint `json:"history"`
}

type Overview struct {
	Cap       *float64 `json:"cap"`
	Volume    *float64 `json:"volume"`
	Liquidity *float64 `json:"liquidity"`
	// Fraction, not percentage: 0.5423 means 54.23%.
	BTCDominance *float64 `json:"btcDominance"`
}

type OverviewPoint struct {
	Date int64 `json:"date"` // UNIX milliseconds
	Overview
}

type Credits struct {
	DailyCreditsRemaining int `json:"dailyCreditsRemaining"`
	DailyCreditsLimit     int `json:"dailyCreditsLimit"`
}

type Fiat struct {
	Code      string   `json:"code"`
	Name      string   `json:"name"`
	Symbol    string   `json:"symbol"`
	Flag      string   `json:"flag"`
	Countries []string `json:"countries"`
}

// ---------------------------------------------------------------- requests

// SortField is the complete set the API accepts. It contains no delta window,
// so the API cannot rank by price change over any period.
type SortField string

const (
	SortRank   SortField = "rank"
	SortPrice  SortField = "price"
	SortVolume SortField = "volume"
	SortCode   SortField = "code"
	SortName   SortField = "name"
	SortAge    SortField = "age"
)

func ValidSortFields() []SortField {
	return []SortField{SortRank, SortPrice, SortVolume, SortCode, SortName, SortAge}
}

func (s SortField) Valid() bool {
	for _, v := range ValidSortFields() {
		if s == v {
			return true
		}
	}
	return false
}

type SortOrder string

const (
	OrderAscending  SortOrder = "ascending"
	OrderDescending SortOrder = "descending"
)

func (o SortOrder) Valid() bool { return o == OrderAscending || o == OrderDescending }

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
