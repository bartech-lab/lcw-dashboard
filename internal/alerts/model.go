package alerts

import (
	"fmt"
	"time"

	"github.com/bartech/lcw-dashboard/internal/lcw"
)

type Metric string

const (
	MetricPrice       Metric = "price"
	MetricDelta       Metric = "delta"
	MetricVolume      Metric = "volume"
	MetricCap         Metric = "cap"
	MetricRank        Metric = "rank"
	MetricLiquidity   Metric = "liquidity"
	MetricATHDistance Metric = "ath_distance"
)

func ValidMetrics() []Metric {
	return []Metric{MetricPrice, MetricDelta, MetricVolume, MetricCap,
		MetricRank, MetricLiquidity, MetricATHDistance}
}

func (m Metric) Valid() bool {
	for _, v := range ValidMetrics() {
		if m == v {
			return true
		}
	}
	return false
}

type Op string

const (
	OpGT           Op = "gt"
	OpGTE          Op = "gte"
	OpLT           Op = "lt"
	OpLTE          Op = "lte"
	OpCrossesAbove Op = "crosses_above"
	OpCrossesBelow Op = "crosses_below"
	OpAbsGT        Op = "abs_gt"
)

func ValidOps() []Op {
	return []Op{OpGT, OpGTE, OpLT, OpLTE, OpCrossesAbove, OpCrossesBelow, OpAbsGT}
}

func (o Op) Valid() bool {
	for _, v := range ValidOps() {
		if o == v {
			return true
		}
	}
	return false
}

// IsEdge reports ops needing a previous observation. They never fire on first
// sight, so a restart cannot announce a threshold that was already crossed.
func (o Op) IsEdge() bool { return o == OpCrossesAbove || o == OpCrossesBelow }

type Rearm string

const (
	RearmOnExit        Rearm = "on_exit"
	RearmAfterCooldown Rearm = "after_cooldown"
	RearmOncePerDay    Rearm = "once_per_day"
	RearmManual        Rearm = "manual"
)

func ValidRearms() []Rearm {
	return []Rearm{RearmOnExit, RearmAfterCooldown, RearmOncePerDay, RearmManual}
}

func (r Rearm) Valid() bool {
	for _, v := range ValidRearms() {
		if r == v {
			return true
		}
	}
	return false
}

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarn     Severity = "warn"
	SeverityCritical Severity = "critical"
)

func (s Severity) Valid() bool {
	return s == SeverityInfo || s == SeverityWarn || s == SeverityCritical
}

// Scope selects which coins a rule applies to. Exactly one field may be set.
type Scope struct {
	Coin      string   `yaml:"coin"`
	Codes     []string `yaml:"codes"`
	Watchlist bool     `yaml:"watchlist"`
	Top       int      `yaml:"top"`
}

func (s Scope) count() int {
	n := 0
	if s.Coin != "" {
		n++
	}
	if len(s.Codes) > 0 {
		n++
	}
	if s.Watchlist {
		n++
	}
	if s.Top > 0 {
		n++
	}
	return n
}

type Condition struct {
	Metric Metric     `yaml:"metric"`
	Window lcw.Window `yaml:"window"`
	Op     Op         `yaml:"op"`
	Value  float64    `yaml:"value"`
	// MinDuration suppresses spikes: the condition must hold this long first.
	MinDuration Dur    `yaml:"min_duration"`
	Currency    string `yaml:"currency"`
}

type Rule struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	Enabled  *bool    `yaml:"enabled"`
	Severity Severity `yaml:"severity"`

	Scope     Scope     `yaml:"scope"`
	Condition Condition `yaml:"condition"`

	Cooldown Dur   `yaml:"cooldown"`
	Rearm    Rearm `yaml:"rearm"`
	// HysteresisPct is how far past the threshold the value must retreat before
	// an on_exit rule re-arms. Without it, oscillation around a threshold fires
	// on every tick.
	HysteresisPct  float64 `yaml:"hysteresis_pct"`
	MaxFiresPerDay int     `yaml:"max_fires_per_day"`
	Message        string  `yaml:"message"`
}

func (r Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// Validate checks a rule is coherent before the engine ever sees it.
func (r Rule) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("rule needs an id (it keys persisted arm state)")
	}
	if !r.Severity.Valid() {
		return fmt.Errorf("rule %q: severity %q is not info|warn|critical", r.ID, r.Severity)
	}
	if n := r.Scope.count(); n != 1 {
		return fmt.Errorf("rule %q: scope needs exactly one of coin, codes, watchlist, top (got %d)", r.ID, n)
	}
	if !r.Condition.Metric.Valid() {
		return fmt.Errorf("rule %q: metric %q is not one of %v", r.ID, r.Condition.Metric, ValidMetrics())
	}
	if !r.Condition.Op.Valid() {
		return fmt.Errorf("rule %q: op %q is not one of %v", r.ID, r.Condition.Op, ValidOps())
	}
	if !r.Rearm.Valid() {
		return fmt.Errorf("rule %q: rearm %q is not one of %v", r.ID, r.Rearm, ValidRearms())
	}

	if r.Condition.Metric == MetricDelta {
		if !r.Condition.Window.Valid() {
			return fmt.Errorf("rule %q: metric delta needs a window (%v)", r.ID, lcw.ValidWindows())
		}
	} else if r.Condition.Window != "" {
		return fmt.Errorf("rule %q: window applies only to metric delta", r.ID)
	}

	if r.Condition.Op == OpAbsGT && r.Condition.Value <= 0 {
		return fmt.Errorf("rule %q: abs_gt compares magnitude, so value must be positive, got %v",
			r.ID, r.Condition.Value)
	}
	if r.Condition.MinDuration.D() < 0 {
		return fmt.Errorf("rule %q: min_duration cannot be negative", r.ID)
	}

	if r.Cooldown.D() < 0 {
		return fmt.Errorf("rule %q: cooldown cannot be negative", r.ID)
	}
	if r.HysteresisPct < 0 {
		return fmt.Errorf("rule %q: hysteresis_pct cannot be negative", r.ID)
	}
	if r.MaxFiresPerDay < 0 {
		return fmt.Errorf("rule %q: max_fires_per_day cannot be negative", r.ID)
	}
	return nil
}

// Dur is a YAML duration, duplicated here rather than importing config so that
// config can import alerts without a cycle.
type Dur time.Duration

func (d Dur) D() time.Duration { return time.Duration(d) }

func (d *Dur) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration (try 30m, 2h): %w", s, err)
	}
	*d = Dur(parsed)
	return nil
}

func (d Dur) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

func (d Dur) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%d", time.Duration(d).Milliseconds())), nil
}
