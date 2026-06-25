package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// guarded wraps an HTTP handler so a panic is logged and answered with a JSON
// 500 instead of being handled silently by net/http (or worse, lost).
func (a *app) guarded(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.debugLog(fmt.Sprintf("http %s panic: %v\n%s", r.URL.Path, rec, debug.Stack()))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": fmt.Sprintf("internal error: %v", rec),
				})
			}
		}()
		h(w, r)
	}
}

// serve starts a localhost HTTP server that hosts the UI at "/" and run
// artifacts (screenshots) under "/art/". Loading the page over http lets
// WebView2 render local screenshots, which it blocks from inline HTML.
func (a *app) serve() (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(uiHTML))
	})
	mux.HandleFunc("/debug-panel", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(debugPanelHTML))
	})
	mux.HandleFunc("/events", a.guarded(a.handleEvents))
	mux.HandleFunc("/load", a.guarded(a.handleLoad))
	mux.HandleFunc("/run", a.guarded(a.handleRun))
	mux.HandleFunc("/pause", a.guarded(a.handlePause))
	mux.HandleFunc("/resume", a.guarded(a.handleResume))
	mux.HandleFunc("/update-provider", a.guarded(a.handleUpdateProvider))
	// Debug mode endpoints.
	mux.HandleFunc("/debug/state", a.guarded(a.handleDebugState))
	mux.HandleFunc("/debug/verdict", a.guarded(a.handleDebugVerdict))
	mux.HandleFunc("/debug/breakpoint", a.guarded(a.handleDebugBreakpoint))
	mux.HandleFunc("/debug/jump", a.guarded(a.handleDebugJump))
	mux.HandleFunc("/debug/record/stop", a.guarded(a.handleDebugRecordStop))
	mux.HandleFunc("/debug/record/discard", a.guarded(a.handleDebugRecordDiscard))
	mux.HandleFunc("/debug/undo", a.guarded(a.handleDebugUndo))
	mux.HandleFunc("/debug/redo", a.guarded(a.handleDebugRedo))
	mux.HandleFunc("/debug/delete-step", a.guarded(a.handleDebugDeleteStep))
	// New-session recording endpoints.
	mux.HandleFunc("/new-session/start", a.guarded(a.handleNewSessionStart))
	mux.HandleFunc("/new-session/save", a.guarded(a.handleNewSessionSave))
	mux.HandleFunc("/new-session/discard", a.guarded(a.handleNewSessionDiscard))
	mux.HandleFunc("/art/", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		base := a.outDir
		a.mu.Unlock()
		if base == "" {
			http.NotFound(w, r)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, "/art/")
		clean := filepath.Clean(filepath.FromSlash(rel))
		if strings.HasPrefix(clean, "..") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.ServeFile(w, r, filepath.Join(base, clean))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go http.Serve(ln, mux)
	return "http://" + ln.Addr().String() + "/", nil
}
