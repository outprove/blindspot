@echo off
setlocal

set "WORKSPACE_ROOT=%~dp0"
cd /d "%WORKSPACE_ROOT%"

if not exist ".gocache" mkdir ".gocache"
if not exist ".gomodcache" mkdir ".gomodcache"

set "GOROOT=%WORKSPACE_ROOT%tools\go-sdk"
set "GOCACHE=%WORKSPACE_ROOT%.gocache"
set "GOMODCACHE=%WORKSPACE_ROOT%.gomodcache"

"%WORKSPACE_ROOT%tools\go-sdk\bin\go.exe" build -mod=mod -o "%WORKSPACE_ROOT%main.exe" .
if errorlevel 1 exit /b 1

"%WORKSPACE_ROOT%main.exe" serve --http=127.0.0.1:8091
