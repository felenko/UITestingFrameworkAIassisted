package ai

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ResolveCursorAgent returns the cursor-agent executable to invoke. GUI apps and
// services often launch without the user's shell PATH, so we fall back to the
// default Windows install location after LookPath.
//
// Override with UITEST_CURSOR_BIN (full path to cursor-agent.cmd).
func ResolveCursorAgent() string {
	if p := os.Getenv("UITEST_CURSOR_BIN"); p != "" {
		return p
	}
	if p, err := exec.LookPath("cursor-agent"); err == nil {
		return p
	}
	if p, err := exec.LookPath("cursor-agent.cmd"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		for _, base := range windowsLocalAppDataDirs() {
			for _, name := range []string{"cursor-agent.cmd", "cursor-agent.exe", "agent.cmd"} {
				p := filepath.Join(base, "cursor-agent", name)
				if fileExists(p) {
					return p
				}
			}
		}
	}
	return "cursor-agent"
}

func cursorAgentAvailable() bool {
	p := ResolveCursorAgent()
	if p == "cursor-agent" {
		_, err := exec.LookPath(p)
		return err == nil
	}
	return fileExists(p)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func windowsLocalAppDataDirs() []string {
	var dirs []string
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		dirs = append(dirs, v)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "AppData", "Local"))
	}
	return dirs
}
