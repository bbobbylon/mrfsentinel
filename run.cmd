@echo off
REM One-command local run for Windows (double-click, or run from cmd/PowerShell).
REM Same steps as run.sh — see that file's header comment for what this does
REM and why it's simpler than DeleteBoard's run.cmd (no separate frontend
REM build to bundle in).
setlocal

cd /d "%~dp0"

set APP_URL=http://localhost:8080
set HEALTH_URL=%APP_URL%/healthz

REM Preflight. Without this, a missing tool surfaces several steps in as a
REM bare "'go' is not recognized...", which says nothing about what to
REM install. Checking up front costs nothing and the message is actionable.
where go >nul 2>&1
if errorlevel 1 (
    echo Go is not on your PATH.
    echo Install Go 1.24+ from https://go.dev/dl/, then open a NEW terminal so PATH updates.
    exit /b 1
)
where docker >nul 2>&1
if errorlevel 1 (
    echo Docker is not on your PATH.
    echo Install Docker Desktop from https://docs.docker.com/get-docker/ - it runs Postgres and Mailhog.
    exit /b 1
)
where curl >nul 2>&1
if errorlevel 1 (
    echo curl is not on your PATH.
    echo It ships with Windows 10 1803 and later as C:\Windows\System32\curl.exe - if it's missing, get it from https://curl.se/windows/
    exit /b 1
)

echo [1/3] Starting Postgres and Mailhog...
docker compose up -d --wait postgres mailhog
if errorlevel 1 (
    echo Postgres/Mailhog failed to start - is Docker Desktop running?
    exit /b 1
)

echo [2/3] Building the binary...
go build -o bin\mrfsentinel.exe .\cmd\server
if errorlevel 1 (
    echo Build failed - see the output above.
    exit /b 1
)

echo [3/3] Starting MRF Sentinel...
set DATABASE_URL=postgres://mrfsentinel:mrfsentinel_local_dev@localhost:5432/mrfsentinel?sslmode=disable
set PUBLIC_BASE_URL=%APP_URL%
set SMTP_HOST=localhost
set SMTP_PORT=1025

REM Runs in its own window (kept open with /k so you can see the log, and
REM closing that window is how you stop the server) rather than this
REM window, which needs to stay free to poll for health and then open the
REM browser below.
start "MRF Sentinel Server" cmd /k "bin\mrfsentinel.exe"

echo Waiting for it to come up...
set READY=0
for /l %%i in (1,1,30) do (
    curl -sf -o nul "%HEALTH_URL%" >nul 2>&1
    if not errorlevel 1 (
        set READY=1
        goto :ready_check
    )
    timeout /t 1 /nobreak >nul
)
:ready_check

if "%READY%"=="1" (
    echo Ready - opening %APP_URL%
    start "" "%APP_URL%"
    echo Magic-link emails land in Mailhog: http://localhost:8025
) else (
    echo Still not responding after 30s.
    echo Not opening the browser automatically - check the "MRF Sentinel Server" window for what's wrong.
    echo If it's just slow to start, it may still come up - try %APP_URL% yourself in a browser.
)

echo MRF Sentinel is running in the "MRF Sentinel Server" window. Close that window to stop it.
