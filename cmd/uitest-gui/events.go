package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/felenko/uitest/internal/core/event"
)

// eventBuffer holds runner events for HTTP polling. WebView2 Eval from background
// goroutines is unreliable on some setups; polling guarantees the UI updates.
type eventBuffer struct {
	mu     sync.Mutex
	events []event.Event
}

func (b *eventBuffer) reset() {
	b.mu.Lock()
	b.events = nil
	b.mu.Unlock()
}

func (b *eventBuffer) append(e event.Event) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	return len(b.events)
}

func (b *eventBuffer) since(index int) ([]event.Event, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if index < 0 {
		index = 0
	}
	if index >= len(b.events) {
		return nil, len(b.events)
	}
	out := make([]event.Event, len(b.events)-index)
	copy(out, b.events[index:])
	return out, len(b.events)
}

func (a *app) debugLog(msg string) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), msg)
	path := filepath.Join(os.TempDir(), "uitest-gui-run.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	f.Close()
}

// handleLoad loads a session via HTTP (same path as the WebView bind).
func (a *app) handleLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	sum, err := a.loadSession(req.Path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sum)
}

// handleRun starts a run via HTTP so the UI is not blocked on WebView2 RPC.
func (a *app) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var o runOptions
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	optsJSON, _ := json.Marshal(o)
	if err := a.startRun(string(optsJSON)); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (a *app) handleEvents(w http.ResponseWriter, r *http.Request) {
	since := 0
	if s := r.URL.Query().Get("since"); s != "" {
		_, _ = fmt.Sscanf(s, "%d", &since)
	}
	events, next := a.buf.since(since)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"events": events,
		"next":   next,
	})
}
