# Building & Running

The framework is built in Go as two front-ends over one shared Runner Core:

| Binary | Build needs | Notes |
| --- | --- | --- |
| `uitest` (CLI) | Go 1.25+ only | Pure Go (`CGO_ENABLED=0`). Cross-compiles. |
| `uitest-gui` (GUI) | Go 1.25+ **and** a C toolchain (gcc/clang) **and** WebView2 | Uses `webview/webview_go` which is CGO. |

## Prerequisites

- **Go** 1.25 or newer.
- **For the GUI only:** a C compiler. On Windows: `choco install mingw`
  (installs to `C:\ProgramData\mingw64\mingw64\bin`). The WebView2 runtime
  ships with Windows 11; on Windows 10 install the Evergreen runtime.

If gcc isn't on `PATH`, point Go at it once:

```powershell
go env -w CC="C:\ProgramData\mingw64\mingw64\bin\gcc.exe"
```

## Build

```powershell
./build.ps1            # builds both front-ends into ./bin
./build.ps1 -CliOnly   # CLI only (no C compiler required)
```

(Works in both Windows PowerShell 5.1 and PowerShell 7+. If execution policy
blocks it: `powershell -ExecutionPolicy Bypass -File ./build.ps1`.)

Or manually:

```powershell
# CLI
$env:CGO_ENABLED="0"; go build -o bin/uitest.exe ./cmd/uitest

# GUI (needs gcc on PATH)
$env:PATH="$env:PATH;C:\ProgramData\mingw64\mingw64\bin"
$env:CGO_ENABLED="1"; go build -o bin/uitest-gui.exe ./cmd/uitest-gui
```

## Run

```powershell
# Verify your environment (AI CLIs reachable, screen capture works)
./bin/uitest.exe doctor

# Validate / list a session without running it
./bin/uitest.exe validate examples/TestSessionCases.yaml
./bin/uitest.exe list     examples/TestSessionCases.yaml

# Execute a session (drives the real desktop)
./bin/uitest.exe run notepad-demo.yaml --open

# Same session in the GUI
./bin/uitest-gui.exe notepad-demo.yaml
```

`notepad-demo.yaml` is a minimal, runnable sample that targets a freshly
launched Notepad (`Untitled - Notepad`) so window matching is unambiguous.

## Test

```powershell
go test ./internal/...
```

These tests cover the parser, validator, durations, and AI verdict parsing —
they don't require a desktop or an AI provider.

## AI providers

The assertion engine shells out to a provider CLI (`claude`, `codex`, or
`cursor-agent`) with the screenshot path. The default is `claude`. You can
retune any invocation without recompiling via environment variables:

```
UITEST_CLAUDE_CMD="claude -p {prompt} --allowedTools Read"
UITEST_CODEX_CMD="codex exec {prompt}"
UITEST_CURSOR_CMD="cursor-agent -p --trust --approve-mcps --output-format json --workspace %TEMP% {prompt}"
UITEST_CURSOR_BIN="C:\\Users\\you\\AppData\\Local\\cursor-agent\\cursor-agent.cmd"
```

On Windows, if `cursor-agent` is not on `PATH` (common when launching from the
GUI), the runner auto-detects `%LOCALAPPDATA%\\cursor-agent\\cursor-agent.cmd`.
Set `UITEST_CURSOR_BIN` to override that path.
