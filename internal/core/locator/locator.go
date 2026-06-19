// Package locator is the durable store behind self-healing `find:` targets
// (docs/02 "Phase 3"). When the AI locates a described element on screen, the
// runner harvests the UI Automation selector under that point and saves it
// here, so every later run resolves the element deterministically — no AI call,
// no pixel coordinates. Entries start as unapproved candidates; a human
// reviews them (uitest locators) and flips them to approved.
package locator

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Selector is the durable UIA query harvested for a find: description. It
// mirrors the `uia:` block authors write by hand.
type Selector struct {
	AutomationID string `yaml:"automationId,omitempty"`
	Name         string `yaml:"name,omitempty"`
	ControlType  string `yaml:"controlType,omitempty"`
}

// IsZero reports whether the selector has no identifying property.
func (s Selector) IsZero() bool {
	return s.AutomationID == "" && s.Name == "" && s.ControlType == ""
}

func (s Selector) String() string {
	var parts []string
	if s.AutomationID != "" {
		parts = append(parts, "automationId="+s.AutomationID)
	}
	if s.Name != "" {
		parts = append(parts, "name="+s.Name)
	}
	if s.ControlType != "" {
		parts = append(parts, "controlType="+s.ControlType)
	}
	return strings.Join(parts, " ")
}

// Entry is one cached locator, keyed by the authored find: description.
type Entry struct {
	Find        string    `yaml:"find"`             // the authored description (key)
	Window      string    `yaml:"window,omitempty"` // window title the selector was harvested in
	UIA         Selector  `yaml:"uia"`
	Approved    bool      `yaml:"approved"`             // human reviewed and confirmed
	HarvestedAt time.Time `yaml:"harvestedAt"`
	Provider    string    `yaml:"provider,omitempty"` // AI provider that located the element
	Note        string    `yaml:"note,omitempty"`     // free text; healing history is recorded here
}

// Store is the YAML-backed locator collection for one session file.
type Store struct {
	Version  int      `yaml:"version"`
	Locators []*Entry `yaml:"locators"`

	path  string
	dirty bool
}

// PathFor derives the locator store path for a session file:
// <dir>/<base>.locators.yaml next to the session YAML.
func PathFor(sessionPath string) string {
	dir := filepath.Dir(sessionPath)
	base := filepath.Base(sessionPath)
	if ext := filepath.Ext(base); ext == ".yaml" || ext == ".yml" {
		base = strings.TrimSuffix(base, ext)
	}
	return filepath.Join(dir, base+".locators.yaml")
}

// Load reads a store; a missing file yields an empty store bound to the path.
func Load(path string) (*Store, error) {
	s := &Store{Version: 1, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	s.path = path
	if s.Version == 0 {
		s.Version = 1
	}
	return s, nil
}

// Path returns the file the store loads from / saves to.
func (s *Store) Path() string { return s.path }

// Get returns the entry for a find description, or nil.
func (s *Store) Get(find string) *Entry {
	find = strings.TrimSpace(find)
	for _, e := range s.Locators {
		if e.Find == find {
			return e
		}
	}
	return nil
}

// Put upserts an entry by its Find key and marks the store dirty.
func (s *Store) Put(e Entry) {
	e.Find = strings.TrimSpace(e.Find)
	s.dirty = true
	for i, old := range s.Locators {
		if old.Find == e.Find {
			s.Locators[i] = &e
			return
		}
	}
	s.Locators = append(s.Locators, &e)
}

// Remove deletes an entry; reports whether it existed.
func (s *Store) Remove(find string) bool {
	find = strings.TrimSpace(find)
	for i, e := range s.Locators {
		if e.Find == find {
			s.Locators = append(s.Locators[:i], s.Locators[i+1:]...)
			s.dirty = true
			return true
		}
	}
	return false
}

// Approve flips an entry to approved; reports whether it existed.
func (s *Store) Approve(find string) bool {
	e := s.Get(find)
	if e == nil {
		return false
	}
	if !e.Approved {
		e.Approved = true
		s.dirty = true
	}
	return true
}

// ApproveAll approves every entry and returns how many changed.
func (s *Store) ApproveAll() int {
	n := 0
	for _, e := range s.Locators {
		if !e.Approved {
			e.Approved = true
			n++
		}
	}
	if n > 0 {
		s.dirty = true
	}
	return n
}

// Save writes the store if anything changed. Entries are sorted by Find so the
// file diffs cleanly under version control.
func (s *Store) Save() error {
	if !s.dirty {
		return nil
	}
	if s.path == "" {
		return fmt.Errorf("locator store has no path")
	}
	sort.Slice(s.Locators, func(i, j int) bool { return s.Locators[i].Find < s.Locators[j].Find })

	var b strings.Builder
	b.WriteString("# Self-healing locators harvested for `find:` targets (managed by uitest).\n")
	b.WriteString("# Review each entry, then mark it approved (uitest locators --approve).\n")
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	b.Write(data)
	if err := os.WriteFile(s.path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	s.dirty = false
	return nil
}
