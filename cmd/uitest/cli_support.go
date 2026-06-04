package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// parseWithPath extracts the first positional argument (the session/results
// path) regardless of where it appears, then parses the remaining flags. This
// lets `uitest run a.yaml --out x` and `uitest run --out x a.yaml` both work.
func parseWithPath(fs *flag.FlagSet, args []string, cmd string) (string, bool) {
	var path string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if path == "" && !strings.HasPrefix(a, "-") {
			path = a
			continue
		}
		rest = append(rest, a)
	}
	if path == "" {
		fmt.Fprintf(os.Stderr, "error: %s requires a path argument\n", cmd)
		return "", false
	}
	if err := fs.Parse(rest); err != nil {
		return "", false
	}
	return path, true
}

// flagSet reports whether a flag was explicitly provided.
func flagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// openInBrowser opens a file/URL in the default handler (Windows).
func openInBrowser(path string) {
	cmd := exec.Command("cmd", "/c", "start", "", path)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "could not open %s: %v\n", path, err)
	}
}
