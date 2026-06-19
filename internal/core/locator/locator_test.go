package locator

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPathFor(t *testing.T) {
	got := PathFor(filepath.Join("dir", "Smoke.yaml"))
	want := filepath.Join("dir", "Smoke.locators.yaml")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.locators.yaml")

	s, err := Load(path)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(s.Locators) != 0 {
		t.Fatalf("expected empty store")
	}

	s.Put(Entry{
		Find:        "the Save button in the toolbar",
		Window:      "New Person",
		UIA:         Selector{Name: "Save", ControlType: "button"},
		HarvestedAt: time.Now(),
		Provider:    "claude",
	})
	s.Put(Entry{Find: "a", UIA: Selector{AutomationID: "btnA"}})
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	s2, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	e := s2.Get("the Save button in the toolbar")
	if e == nil || e.UIA.Name != "Save" || e.UIA.ControlType != "button" || e.Window != "New Person" {
		t.Fatalf("entry did not round-trip: %+v", e)
	}
	if e.Approved {
		t.Fatalf("new entry must start unapproved")
	}
	// Sorted by Find on save.
	if s2.Locators[0].Find != "a" {
		t.Fatalf("entries not sorted: first=%q", s2.Locators[0].Find)
	}

	// Approve + upsert + remove.
	if !s2.Approve("a") {
		t.Fatalf("approve failed")
	}
	s2.Put(Entry{Find: "a", UIA: Selector{AutomationID: "btnB"}}) // upsert resets approval
	if s2.Get("a").Approved {
		t.Fatalf("upsert must replace the entry (unapproved)")
	}
	if !s2.Remove("a") || s2.Get("a") != nil {
		t.Fatalf("remove failed")
	}
	if err := s2.Save(); err != nil {
		t.Fatalf("save 2: %v", err)
	}

	// Save with no changes must not rewrite (dirty flag).
	fi1, _ := os.Stat(path)
	time.Sleep(10 * time.Millisecond)
	s3, _ := Load(path)
	if err := s3.Save(); err != nil {
		t.Fatalf("noop save: %v", err)
	}
	fi2, _ := os.Stat(path)
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatalf("noop save rewrote the file")
	}
}
