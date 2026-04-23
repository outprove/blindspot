$ErrorActionPreference = "Stop"

$workspaceRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $workspaceRoot

$env:GOROOT = (Resolve-Path ".\tools\go-sdk").Path

if (-not (Test-Path ".\.gocache")) {
    New-Item -ItemType Directory -Path ".\.gocache" | Out-Null
}

if (-not (Test-Path ".\.gomodcache")) {
    New-Item -ItemType Directory -Path ".\.gomodcache" | Out-Null
}

$env:GOCACHE = (Resolve-Path ".\.gocache").Path
$env:GOMODCACHE = (Resolve-Path ".\.gomodcache").Path

& ".\tools\go-sdk\bin\go.exe" build -mod=mod -o .\main.exe .
& ".\main.exe" serve --http=127.0.0.1:8091
