package lcw

import (
	"math"
	"testing"
)

func f(v float64) *float64 { return &v }

func TestDeltaPct(t *testing.T) {
	tests := []struct {
		name  string
		in    *float64
		want  *float64
		about string
	}{
		{"no change", f(1.0), f(0), "1.0 is the API's neutral value"},
		{"small gain", f(1.0214), f(2.14), "float noise must be rounded away"},
		{"small loss", f(0.9975), f(-0.25), ""},
		{"ten percent loss", f(0.9), f(-10), ""},
		{"double", f(2.0), f(100), "the documented upper bound is not a real bound"},
		{
			"ZEC year delta from live data", f(16.9399), f(1593.99),
			"the docs claim 0..2; Live Coin Watch's own site returned 16.9399",
		},
		{"nil is no data", nil, nil, "the API omits the field entirely"},
		{"zero is no data, not -100%", f(0), nil, "rendering -100% on a healthy coin is worse than a dash"},
		{"NaN is rejected", f(math.NaN()), nil, ""},
		{"Inf is rejected", f(math.Inf(1)), nil, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeltaPct(tc.in)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("DeltaPct(%v) = %v, want nil (%s)", deref(tc.in), *got, tc.about)
			case tc.want == nil:
				return
			case got == nil:
				t.Fatalf("DeltaPct(%v) = nil, want %v (%s)", deref(tc.in), *tc.want, tc.about)
			case math.Abs(*got-*tc.want) > 1e-9:
				t.Fatalf("DeltaPct(%v) = %v, want %v (%s)", deref(tc.in), *got, *tc.want, tc.about)
			}
		})
	}
}

// TestDeltaPctIsNotClamped guards the specific mistake the documentation invites:
// treating the delta as bounded and clamping it into range.
func TestDeltaPctIsNotClamped(t *testing.T) {
	for _, in := range []float64{2.5, 10, 16.9399, 500} {
		got := DeltaPct(&in)
		if got == nil {
			t.Fatalf("DeltaPct(%v) returned nil; large deltas are legitimate", in)
		}
		want := (in - 1) * 100
		if math.Abs(*got-want) > 1e-6 {
			t.Fatalf("DeltaPct(%v) = %v, want %v, value was clamped or truncated", in, *got, want)
		}
	}
}

func TestDeltaConvert(t *testing.T) {
	d := Delta{Hour: f(1.0015), Day: f(0.9), Week: nil, Month: f(0), Quarter: f(1), Year: f(16.9399)}
	got := d.Convert()

	if got.Hour == nil || math.Abs(*got.Hour-0.15) > 1e-9 {
		t.Errorf("Hour = %v, want 0.15", got.Hour)
	}
	if got.Day == nil || math.Abs(*got.Day-(-10)) > 1e-9 {
		t.Errorf("Day = %v, want -10", got.Day)
	}
	if got.Week != nil {
		t.Errorf("Week = %v, want nil (absent in source)", *got.Week)
	}
	if got.Month != nil {
		t.Errorf("Month = %v, want nil (zero means no data)", *got.Month)
	}
	if got.Quarter == nil || *got.Quarter != 0 {
		t.Errorf("Quarter = %v, want 0", got.Quarter)
	}
	if got.Year == nil || math.Abs(*got.Year-1593.99) > 1e-9 {
		t.Errorf("Year = %v, want 1593.99", got.Year)
	}
}

func TestChangePctGet(t *testing.T) {
	c := ChangePct{Hour: f(1), Day: f(2), Week: f(3), Month: f(4), Quarter: f(5), Year: f(6)}
	for i, w := range ValidWindows() {
		got := c.Get(w)
		if got == nil || *got != float64(i+1) {
			t.Errorf("Get(%q) = %v, want %v", w, got, i+1)
		}
	}
	if c.Get(Window("decade")) != nil {
		t.Error("Get on an unknown window should return nil, not panic or guess")
	}
}

func TestATHDistancePct(t *testing.T) {
	tests := []struct {
		name      string
		rate, ath *float64
		want      *float64
	}{
		{"at the high", f(100), f(100), f(0)},
		{"half way down", f(50), f(100), f(-50)},
		{"above the recorded high", f(110), f(100), f(10)},
		{"missing rate", nil, f(100), nil},
		{"missing high", f(50), nil, nil},
		{"zero high must not divide", f(50), f(0), nil},
		{"negative high is nonsense", f(50), f(-1), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ATHDistancePct(tc.rate, tc.ath)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if got != nil && math.Abs(*got-*tc.want) > 1e-9 {
				t.Fatalf("got %v, want %v", *got, *tc.want)
			}
		})
	}
}

func TestSortFieldValidRejectsDeltaWindows(t *testing.T) {
	// The API cannot sort by any delta window. If this ever starts passing, the
	// frontend's market-scope sort restriction can be relaxed.
	for _, w := range ValidWindows() {
		if SortField(w).Valid() {
			t.Errorf("SortField(%q) reports valid, but the API does not accept delta windows", w)
		}
	}
	for _, s := range ValidSortFields() {
		if !s.Valid() {
			t.Errorf("SortField(%q) should be valid", s)
		}
	}
}

func deref(p *float64) any {
	if p == nil {
		return "nil"
	}
	return *p
}
