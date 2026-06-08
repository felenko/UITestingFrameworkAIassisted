// Command uitest-gui is the desktop front-end (docs/04): the same Runner Core
// wrapped in a webview window so a user can pick a session, run it, watch it
// live, and review the report — without a terminal.
package main

import (
	_ "embed"
	"fmt"
	"os"

	webview "github.com/webview/webview_go"
)

// Version mirrors the CLI runner version.
const Version = "0.2.1"

//go:embed ui.html
var uiHTML string

func main() {
	args := parseArgs(os.Args[1:])

	w := webview.New(args.debug)
	defer w.Destroy()
	w.SetTitle("uitest — UI Testing Framework")
	w.SetSize(1180, 820, webview.HintNone)

	app := newApp(w)
	app.bind()

	base, err := app.serve()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start UI server: %v\n", err)
		os.Exit(1)
	}
	w.Navigate(base)

	// If a session was passed on the command line, load it once the UI is ready.
	if args.session != "" {
		w.Dispatch(func() {
			w.Eval(fmt.Sprintf("window.uitestPreload(%s)", jsStr(args.session)))
		})
	}

	w.Run()
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
