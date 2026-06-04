# Builds both front-ends into ./bin.
#   uitest      — CLI (pure Go, no CGO)
#   uitest-gui  — GUI (webview, requires CGO + a C toolchain + WebView2 runtime)
#
# Usage:  pwsh ./build.ps1            # build both
#         pwsh ./build.ps1 -CliOnly   # skip the GUI (no C compiler needed)
param([switch]$CliOnly)

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path bin | Out-Null

Write-Host "Building uitest (CLI)..." -ForegroundColor Cyan
$env:CGO_ENABLED = "0"
go build -o bin/uitest.exe ./cmd/uitest
Write-Host "  -> bin/uitest.exe" -ForegroundColor Green

if (-not $CliOnly) {
    Write-Host "Building uitest-gui (webview, CGO)..." -ForegroundColor Cyan
    # Ensure a MinGW gcc is reachable (installed via: choco install mingw).
    $mingw = "C:\ProgramData\mingw64\mingw64\bin"
    if (Test-Path $mingw) { $env:PATH = "$env:PATH;$mingw" }
    if (-not (Get-Command gcc -ErrorAction SilentlyContinue)) {
        Write-Warning "gcc not found on PATH. Install with 'choco install mingw' or run with -CliOnly."
        exit 1
    }
    $env:CGO_ENABLED = "1"
    go build -o bin/uitest-gui.exe ./cmd/uitest-gui
    Write-Host "  -> bin/uitest-gui.exe" -ForegroundColor Green
}

Write-Host "Done." -ForegroundColor Cyan
