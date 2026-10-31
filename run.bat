@echo off
REM HollowTone Server one-click launcher for Windows.
REM Builds (if needed) and runs the server with local defaults.
setlocal
cd /d "%~dp0"

if "%HT_ADDR%"=="" set HT_ADDR=:8080
if "%HT_DATA_DIR%"=="" set HT_DATA_DIR=%~dp0data

echo [hollowtone] building server...
go build -o hollowtone-server.exe ./cmd/hollowtone-server || goto :error

echo [hollowtone] data dir: %HT_DATA_DIR%
echo [hollowtone] listening on %HT_ADDR%
hollowtone-server.exe
goto :eof

:error
echo [hollowtone] build failed
exit /b 1
