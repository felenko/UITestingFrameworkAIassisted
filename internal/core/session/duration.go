package session

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that understands the session-file format:
// a number with a unit ("500ms", "2s", "1m") or a bare number meaning
// milliseconds (per docs/03 §8).
type Duration struct {
	time.Duration
}

// D is a convenience constructor.
func D(d time.Duration) Duration { return Duration{d} }

// UnmarshalYAML parses durations from either a scalar number (ms) or a
// number+unit string.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	raw := strings.TrimSpace(node.Value)
	if raw == "" {
		return nil
	}
	parsed, err := ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalYAML renders durations back to a compact string.
func (d Duration) MarshalYAML() (any, error) {
	return d.String(), nil
}

// ParseDuration accepts "500ms", "2s", "1m", or a bare number (milliseconds).
func ParseDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	// Bare number => milliseconds.
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		return time.Duration(n * float64(time.Millisecond)), nil
	}
	return time.ParseDuration(raw)
}

// Or returns the duration if set, otherwise the fallback.
func (d Duration) Or(fallback time.Duration) time.Duration {
	if d.Duration == 0 {
		return fallback
	}
	return d.Duration
}
