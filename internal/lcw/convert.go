package lcw

import "math"

// This file is the only place in the program that converts a Live Coin Watch
// delta into a percentage. Nothing downstream — not the scheduler, not the alert
// engine, not the browser — ever sees a raw delta, so the conversion cannot be
// duplicated and cannot drift.

// DeltaPct converts a Live Coin Watch delta multiplier into a percentage.
//
//	1.0     ->    0.0     (no change)
//	1.0214  ->    2.14    (up 2.14%)
//	0.9     ->  -10.0     (down 10%)
//	16.9399 -> 1593.99    (up 1593.99% — real data, from ZEC's year delta)
//
// A nil delta returns nil: the API omits the field for coins it has no data for.
//
// An exact zero also returns nil. Arithmetically 0 would be -100%, but the API
// uses zero to mean "no data" rather than "this coin lost all its value", and
// rendering -100% on a healthy coin is worse than rendering a dash.
func DeltaPct(d *float64) *float64 {
	if d == nil || *d == 0 {
		return nil
	}
	if math.IsNaN(*d) || math.IsInf(*d, 0) {
		return nil
	}
	// Round to 4 decimal places. Float64 subtraction near 1.0 leaves noise
	// (1.0214 - 1 is 0.021400000000000019), and four places is well past any
	// precision the display needs.
	pct := round(( *d - 1) * 100, 4)
	return &pct
}

// ChangePct is the converted form of Delta, carrying percentages rather than
// multipliers. This is what crosses the wire to the browser.
type ChangePct struct {
	Hour    *float64 `json:"hour"`
	Day     *float64 `json:"day"`
	Week    *float64 `json:"week"`
	Month   *float64 `json:"month"`
	Quarter *float64 `json:"quarter"`
	Year    *float64 `json:"year"`
}

// Convert turns raw multipliers into percentages.
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

// Window names a delta period. These strings are the identifiers used
// end to end: in config, in alert rules, in the SSE payload and as frontend
// column ids.
type Window string

const (
	WindowHour    Window = "hour"
	WindowDay     Window = "day"
	WindowWeek    Window = "week"
	WindowMonth   Window = "month"
	WindowQuarter Window = "quarter"
	WindowYear    Window = "year"
)

// ValidWindows lists every delta window, in ascending period order.
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

// Get returns the percentage for a named window, so the alert engine can select
// a metric by string without a switch at every call site.
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

// ATHDistancePct returns how far a rate sits from its all-time high, as a
// percentage. At the high it is 0; half way down it is -50.
//
// Returns nil when either input is missing or the high is not positive, rather
// than dividing by zero and emitting Inf.
func ATHDistancePct(rate, ath *float64) *float64 {
	if rate == nil || ath == nil || *ath <= 0 {
		return nil
	}
	// Spaces around the division are load-bearing: "*rate/*ath" would open a
	// Go comment at the "/*".
	pct := round((*rate / *ath - 1) * 100, 4)
	return &pct
}

func round(v float64, places int) float64 {
	f := math.Pow(10, float64(places))
	return math.Round(v*f) / f
}
