# Create and push a version tag so GitHub Actions publishes binaries.
# Usage: .\scripts\release.ps1 v0.6.0
param(
    [Parameter(Mandatory = $true)]
    [string]$Version
)

$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)

if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
    Write-Error "Install GitHub CLI: https://cli.github.com/"
}

gh auth status | Out-Null

$exists = git rev-parse $Version 2>$null
if ($LASTEXITCODE -eq 0) {
    Write-Host "Tag $Version already exists."
} else {
    git tag -a $Version -m "Folio $Version"
    git push origin $Version
    Write-Host "Pushed tag $Version — Actions will build Windows + Linux assets."
}

Write-Host "Watch runs: gh run list --workflow=release.yml"
Write-Host "View release: gh release view $Version"
