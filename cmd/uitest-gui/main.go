// Command uitest-gui is the desktop front-end (docs/04): the same Runner Core
// wrapped in a webview window so a user can pick a session, run it, watch it
// live, and review the report — without a terminal.
package main

import (
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	webview "github.com/webview/webview_go"
	"golang.org/x/sys/windows"
)

// Version mirrors the CLI runner version.
const Version = "0.2.1"

//go:embed ui.html
var uiHTML string

//go:embed debug_panel.html
var debugPanelHTML string

func main() {
	logLifecycle("started pid=" + strconv.Itoa(os.Getpid()))
	redirectStderr()

	// Last-resort guard: any panic that escapes the handlers below is logged
	// to gui.log before the process dies, so a vanished window is diagnosable.
	defer func() {
		if rec := recover(); rec != nil {
			logLifecycle(fmt.Sprintf("fatal panic: %v\n%s", rec, debug.Stack()))
			os.Exit(1)
		}
	}()

	args := parseArgs(os.Args[1:])

	w := webview.New(args.debug)
	defer w.Destroy()
	w.SetTitle("uitest — UI Testing Framework")
	w.SetSize(1180, 820, webview.HintNone)

	app := newApp(w)
	app.bind()

	base, err := app.serve()
	if err != nil {
		logLifecycle("fatal: server failed: " + err.Error())
		fmt.Fprintf(os.Stderr, "failed to start UI server: %v\n", err)
		os.Exit(1)
	}
	navURL := base
	if args.session != "" {
		navURL = base + "?session=" + url.QueryEscape(args.session)
	}
	w.Navigate(navURL)

	// Intercept WM_CLOSE: confirm when idle, refuse while a run is active.
	installCloseGuard(app, uintptr(w.Window()))

	// Cancel an in-flight run on SIGTERM / Ctrl-C so the runner can write a
	// partial report before the process exits.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				logLifecycle(fmt.Sprintf("signal handler panic: %v", rec))
			}
		}()
		sig := <-sigs
		logLifecycle("signal: " + sig.String() + " — cancelling run")
		_ = app.cancelRun()
	}()

	// Run the WebView2 message loop. Recover from any panic so we can still
	// wait for the runner goroutine to finish and write its report.
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				logLifecycle(fmt.Sprintf("webview panic: %v", rec))
			}
		}()
		w.Run()
	}()

	// WebView2 has exited (window closed, RDP disconnect, etc.).
	// Mark it done so evalAsync stops dispatching to the dead window, then
	// block until the runner goroutine finishes and writes its report.
	app.markWebviewDone()
	logLifecycle("webview exited — waiting for runner")
	app.waitForRun()
	logLifecycle("exited normally")
}

// redirectStderr opens %APPDATA%\uitest\gui-stderr.log and wires Go's stderr
// (fd 2) to it so Go runtime fatal errors (stack overflow, concurrent map write,
// runtime.throw) are captured even though there is no console window.
func redirectStderr() {
	dir := filepath.Join(os.Getenv("APPDATA"), "uitest")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "gui-stderr.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "\n--- %s pid=%d ---\n", time.Now().Format(time.RFC3339), os.Getpid())
	// SetStdHandle redirects the Windows STD_ERROR_HANDLE so the Go runtime's
	// fatal error writes (stack overflow, concurrent map write, runtime.throw)
	// land in the file instead of being silently discarded by a windowless process.
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		f.Close()
		return
	}
	os.Stderr = f
	// Intentional fd leak — the OS reclaims it on exit, and closing early would
	// cut off any late runtime error that fires during shutdown.
	_ = f
}

// logLifecycle appends a timestamped lifecycle event to %APPDATA%\uitest\gui.log
// so crashes can be diagnosed even when the debug-run log is unavailable.
func logLifecycle(msg string) {
	dir := filepath.Join(os.Getenv("APPDATA"), "uitest")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "gui.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), msg)
	f.Close()
}

type cliArgs struct {
	session  string
	provider string
	outDir   string
	debug    bool
}

func parseArgs(in []string) cliArgs {
	var a cliArgs
	for i := 0; i < len(in); i++ {
		switch in[i] {
		case "--ai":
			if i+1 < len(in) {
				i++
				a.provider = in[i]
			}
		case "--out":
			if i+1 < len(in) {
				i++
				a.outDir = in[i]
			}
		case "--debug":
			a.debug = true
		case "run":
			// `uitest-gui run <session>` — accept for parity; treat next as path.
		default:
			if a.session == "" && in[i] != "" && in[i][0] != '-' {
				a.session = in[i]
			}
		}
	}
	return a
}
