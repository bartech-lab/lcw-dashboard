package lcw

import "math"

// DeltaPct converts a Live Coin Watch delta multiplier to a percentage.
//
// The docs claim delta ranges 0..2. It does not: their own live data carries
// delta.year = 16.9399 for ZEC, which is +1593.99%. Never clamp it, and never
// key a fixed-width bar or a colour ramp to ±1.
//
// An exact zero means "no data", not -100%, so it returns nil.
func DeltaPct(d *float64) *float64 {
	if d == nil || *d == 0 || math.IsNaN(*d) || math.IsInf(*d, 0) {
		return nil
	}
	pct := round((*d-1)*100, 4)
	return &pct
}

// ChangePct is what crosses the wire: percentages, never raw multipliers.
type ChangePct struct {
	Hour    *float64 `json:"hour"`
	Day     *float64 `json:"day"`
	Week    *float64 `json:"week"`
	Month   *float64 `json:"month"`
	Quarter *float64 `json:"quarter"`
	Year    *float64 `json:"year"`
}

func (d Delta) Convert() ChangePct {
	return ChangePct{
		Hour:    DeltaPct(d.Hour),
		Day:     DeltaPct(d.Day),
		Week:    DeltaPct(d.Week),
		Month:   DeltaPct(d.Month),
		Quarter: DeltaPct(d.Quarter),
		Year:    DeltaPct(d.Year),
	}
}

// Window identifiers are used end to end: config, alert rules, SSE payload and
// frontend column ids.
type Window string

const (
	WindowHour    Window = "hour"
	WindowDay     Window = "day"
	WindowWeek    Window = "week"
	WindowMonth   Window = "month"
	WindowQuarter Window = "quarter"
	WindowYear    Window = "year"
)

func ValidWindows() []Window {
	return []Window{WindowHour, WindowDay, WindowWeek, WindowMonth, WindowQuarter, WindowYear}
}

func (w Window) Valid() bool {
	for _, v := range ValidWindows() {
		if w == v {
			return true
		}
	}
	return false
}

func (c ChangePct) Get(w Window) *float64 {
	switch w {
	case WindowHour:
		return c.Hour
	case WindowDay:
		return c.Day
	case WindowWeek:
		return c.Week
	case WindowMonth:
		return c.Month
	case WindowQuarter:
		return c.Quarter
	case WindowYear:
		return c.Year
	}
	return nil
}

func ATHDistancePct(rate, ath *float64) *float64 {
	if rate == nil || ath == nil || *ath <= 0 {
		return nil
	}
	// The spaces are load-bearing: "*rate/*ath" opens a Go comment.
	pct := round((*rate / *ath - 1)*100, 4)
	return &pct
}

func round(v float64, places int) float64 {
	f := math.Pow(10, float64(places))
	return math.Round(v*f) / f
}
