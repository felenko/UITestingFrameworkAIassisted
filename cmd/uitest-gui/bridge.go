package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	webview "github.com/webview/webview_go"

	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/report"
	"github.com/felenko/uitest/internal/core/runner"
	"github.com/felenko/uitest/internal/core/session"
)

// app holds GUI state and the Go↔JS bridge (docs/04 §4).
type app struct {
	w webview.WebView

	mu          sync.Mutex
	sessionPath string
	outDir      string
	reportPath  string
	cancel      context.CancelFunc
	running     bool
}

func newApp(w webview.WebView) *app { return &app{w: w} }

// bind exposes Go functions to the UI.
func (a *app) bind() {
	a.w.Bind("pickSession", a.pickSession)
	a.w.Bind("loadSession", a.loadSession)
	a.w.Bind("startRun", a.startRun)
	a.w.Bind("cancelRun", a.cancelRun)
	a.w.Bind("openReport", a.openReport)
	a.w.Bind("openOutputDir", a.openOutputDir)
}

// pickSession opens a native file dialog and returns the chosen path.
func (a *app) pickSession() (string, error) {
	return openFileDialog()
}

// sessionSummary is sent to the UI after a successful load.
type sessionSummary struct {
	Name     string        `json:"name"`
	App      string        `json:"app"`
	Provider string        `json:"provider"`
	Path     string        `json:"path"`
	Cases    []caseSummary `json:"cases"`
	Totals   totals        `json:"totals"`
}

type caseSummary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Folder      string   `json:"folder"`
	Tags        []string `json:"tags"`
	Steps       []string `json:"steps"`
	Asserts     []string `json:"asserts"`
}

type totals struct {
	Cases   int `json:"cases"`
	Steps   int `json:"steps"`
	Asserts int `json:"asserts"`
}

// loadSession parses + validates a session and returns its summary.
func (a *app) loadSession(path string) (sessionSummary, error) {
	sess, err := session.Load(path)
	if err != nil {
		return sessionSummary{}, err
	}
	a.mu.Lock()
	a.sessionPath = path
	a.mu.Unlock()

	sum := sessionSummary{
		Name:     sess.Session.Name,
		App:      sess.Session.Application.Path,
		Provider: sess.Session.AI.Provider,
		Path:     path,
	}
	for i := range sess.TestCases {
		tc := &sess.TestCases[i]
		cs := caseSummary{ID: tc.ID, Name: tc.Name, Description: tc.Description, Folder: tc.Folder, Tags: tc.Tags}
		for _, st := range tc.Steps {
			cs.Steps = append(cs.Steps, st.Human)
			sum.Totals.Steps++
		}
		for _, as := range tc.Validation.Assert {
			label := as.Human
			if label == "" {
				label = as.Question
			}
			cs.Asserts = append(cs.Asserts, label)
			sum.Totals.Asserts++
		}
		sum.Cases = append(sum.Cases, cs)
		sum.Totals.Cases++
	}
	return sum, nil
}

// runOptions are sent from the UI when starting a run.
type runOptions struct {
	Provider string   `json:"provider"`
	OutDir   string   `json:"outDir"`
	Filter   string   `json:"filter"`
	FailFast bool     `json:"failFast"`
	Embed    bool     `json:"embed"`
	Cases    []string `json:"cases"` // explicit case ids; empty = all
}

// startRun begins execution and streams events to the UI.
func (a *app) startRun(optsJSON string) error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return fmt.Errorf("a run is already in progress")
	}
	path := a.sessionPath
	a.mu.Unlock()
	if path == "" {
		return fmt.Errorf("no session loaded")
	}

	var o runOptions
	if optsJSON != "" {
		_ = json.Unmarshal([]byte(optsJSON), &o)
	}

	sess, err := session.Load(path)
	if err != nil {
		return err
	}

	// Resolve the output dir up front so the /art/ server can serve live
	// screenshots while the run is in progress.
	outDir := o.OutDir
	if outDir == "" {
		base := sess.Session.Settings.OutDir
		if base == "" {
			base = session.DefaultOutDir
		}
		outDir = filepath.Join(base, time.Now().Format("20060102-150405"))
	}
	if abs, aerr := filepath.Abs(outDir); aerr == nil {
		outDir = abs
	}

	opts := runner.Options{
		OutDir:        outDir,
		Provider:      o.Provider,
		Filter:        o.Filter,
		IDs:           o.Cases,
		Frontend:      "gui",
		RunnerVersion: Version,
	}
	if o.FailFast {
		ff := true
		opts.FailFast = &ff
	}

	bus := event.New()
	bus.Subscribe(a.pushEvent)

	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.cancel = cancel
	a.running = true
	a.outDir = outDir
	a.reportPath = ""
	a.mu.Unlock()

	r := runner.New(sess, opts, bus)
	go func() {
		defer cancel()
		results, _ := r.Run(ctx)
		a.mu.Lock()
		a.running = false
		a.mu.Unlock()
		if results != nil {
			if rp, werr := report.WriteAll(r.OutDir(), results, o.Embed); werr == nil {
				a.mu.Lock()
				a.reportPath = rp
				a.mu.Unlock()
				a.evalAsync(fmt.Sprintf("window.uitestReport(%s,%s)", jsStr(rp), jsStr(r.OutDir())))
			}
		}
	}()
	return nil
}

func (a *app) cancelRun() error {
	a.mu.Lock()
	c := a.cancel
	a.mu.Unlock()
	if c != nil {
		c()
	}
	return nil
}

func (a *app) openReport() error {
	a.mu.Lock()
	p := a.reportPath
	a.mu.Unlock()
	if p == "" {
		return fmt.Errorf("no report yet")
	}
	return openFile(p)
}

func (a *app) openOutputDir() error {
	a.mu.Lock()
	p := a.outDir
	a.mu.Unlock()
	if p == "" {
		return fmt.Errorf("no output yet")
	}
	// explorer.exe returns a non-zero exit even on success, so don't Wait.
	return exec.Command("explorer.exe", p).Start()
}

// pushEvent forwards a core event to the UI on the main thread.
func (a *app) pushEvent(e event.Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	a.evalAsync(fmt.Sprintf("window.uitestEvent(%s)", string(data)))
}

// jsStr returns a JSON-quoted string safe to embed in an Eval call.
func jsStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// evalAsync runs JS on the webview's UI thread.
func (a *app) evalAsync(js string) {
	a.w.Dispatch(func() { a.w.Eval(js) })
}

// openFile opens a file (e.g. report.html) in its default handler. rundll32 +
// FileProtocolHandler is the most reliable way to launch the default browser
// for a local .html file from a GUI process.
func openFile(path string) error {
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path).Start()
}
