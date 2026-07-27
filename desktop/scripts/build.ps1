# Build Folio with the correct Wails production tags.
# Prefer: wails build
# This script is for environments where you want a plain go build.

$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)

New-Item -ItemType Directory -Force -Path "build\bin" | Out-Null

if (Get-Command wails -ErrorAction SilentlyContinue) {
    Write-Host "Using wails build..."
    wails build
    exit $LASTEXITCODE
}

Write-Host "wails CLI not found; falling back to: go build -tags production (no console)"
# -H windowsgui = no terminal window on Windows
go build -tags production -ldflags "-s -w -H windowsgui" -o "build\bin\folio.exe" .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "Built build\bin\folio.exe"
