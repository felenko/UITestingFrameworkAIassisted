package main

import (
	"net"
	"net/http"
	"path/filepath"
	"strings"
)

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
	mux.HandleFunc("/events", a.handleEvents)
	mux.HandleFunc("/load", a.handleLoad)
	mux.HandleFunc("/run", a.handleRun)
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
