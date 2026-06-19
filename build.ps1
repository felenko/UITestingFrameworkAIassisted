# Builds both front-ends into ./bin.
#   uitest      — CLI (pure Go, no CGO)
#   uitest-gui  — GUI (webview, requires CGO + a C toolchain + WebView2 runtime)
#
# Usage:  pwsh ./build.ps1            # build both (default)
#         pwsh ./build.ps1 -CliOnly   # skip the GUI (no C compiler needed)
param([switch]$CliOnly)

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path bin | Out-Null

# --- CLI ---
Write-Host "Building uitest (CLI)..." -ForegroundColor Cyan
$env:CGO_ENABLED = "0"
go build -o bin/uitest.exe ./cmd/uitest
Write-Host "  -> bin/uitest.exe" -ForegroundColor Green

# --- GUI ---
if ($CliOnly) {
    Write-Host "Skipping uitest-gui (-CliOnly flag set)." -ForegroundColor Yellow
} else {
    Write-Host "Building uitest-gui (webview, CGO)..." -ForegroundColor Cyan
    $mingw = "C:\ProgramData\mingw64\mingw64\bin"
    if (Test-Path $mingw) { $env:PATH = "$env:PATH;$mingw" }
    if (-not (Get-Command gcc -ErrorAction SilentlyContinue)) {
        Write-Host "ERROR: gcc not found on PATH - cannot build uitest-gui." -ForegroundColor Red
        Write-Host "       Install with: choco install mingw" -ForegroundColor Red
        Write-Host "       Or skip the GUI with: pwsh ./build.ps1 -CliOnly" -ForegroundColor Red
        exit 1
    }
    $env:CGO_ENABLED = "1"
    go build -o bin/uitest-gui.exe ./cmd/uitest-gui
    Write-Host "  -> bin/uitest-gui.exe" -ForegroundColor Green
}

Write-Host "Done." -ForegroundColor Cyan
