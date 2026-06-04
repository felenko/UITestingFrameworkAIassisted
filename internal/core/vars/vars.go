// Package vars provides ${name} interpolation for session strings (docs/03 §7).
package vars

import (
	"os"
	"regexp"
	"strings"
)

var pattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// Bag holds the variable values available during a run.
type Bag struct {
	values map[string]string
}

// New creates a bag seeded with the session variables.
func New(initial map[string]string) *Bag {
	b := &Bag{values: map[string]string{}}
	for k, v := range initial {
		b.values[k] = v
	}
	return b
}

// Set stores or updates a variable (used by read_text_ai store:).
func (b *Bag) Set(name, value string) { b.values[name] = value }

// Get returns a variable value and whether it was found.
func (b *Bag) Get(name string) (string, bool) {
	v, ok := b.values[name]
	return v, ok
}

// Expand replaces every ${name} in s with its value. Unknown variables are
// left untouched so authors can spot typos in output. Built-in variables
// (session.outDir, case.id, step.index, timestamp) are resolved from the bag
// like any other key — the runner seeds them before each step.
func (b *Bag) Expand(s string) string {
	if s == "" || !strings.Contains(s, "${") {
		return s
	}
	return pattern.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimSpace(m[2 : len(m)-1])
		if v, ok := b.values[name]; ok {
			return v
		}
		// Fall back to environment variables for convenience.
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return m
	})
}
