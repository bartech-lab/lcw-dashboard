package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that reads from YAML as "15s" or "30m" rather
// than as a raw nanosecond count. Writing 15000000000 in a config file is a
// mistake waiting to happen.
type Duration time.Duration

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML accepts a Go duration string. A bare number is rejected with a
// hint: silently treating 15 as 15 nanoseconds would produce a config that
// passes validation and then hammers the API.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		var i int64
		if n.Decode(&i) == nil {
			return fmt.Errorf("line %d: %q needs a unit, for example %ds or %dm", n.Line, n.Value, i, i)
		}
		return fmt.Errorf("line %d: cannot read %q as a duration", n.Line, n.Value)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %q is not a duration (try 15s, 2m, 6h): %w", n.Line, s, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML writes the human-readable form, so a dumped config round-trips.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// MarshalJSON writes milliseconds, which is what the browser wants for
// setTimeout and for displaying an interval.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%d", time.Duration(d).Milliseconds())), nil
}
