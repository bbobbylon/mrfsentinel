#!/bin/bash
# One-command local run: builds the Go binary (which already has the whole
# frontend compiled into it — see internal/web/embed.go) and runs it. The
# whole app ends up on http://localhost:8080 — one process, one binary, no
# npm build step, no separate frontend container. Postgres and Mailhog still
# run as lightweight Docker containers, since there's no local-install-free
# way around needing a real Postgres and something to catch magic-link
# emails.
#
# Compare to DeleteBoard's run.sh, which needs a `bundled` Maven profile and
# frontend-maven-plugin to fold a separately-built Angular app into the
# Spring Boot jar. This script doesn't need an equivalent step because Go's
# go:embed already put the frontend inside the binary at compile time —
# there's nothing else to bundle.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

COLOR_GREEN='\033[0;32m'
COLOR_YELLOW='\033[1;33m'
COLOR_RED='\033[0;31m'
NC='\033[0m'

APP_URL="http://localhost:8080"
HEALTH_URL="$APP_URL/healthz"

echo -e "${COLOR_YELLOW}[1/3] Starting Postgres and Mailhog...${NC}"
if docker compose up -d --wait postgres mailhog; then
    echo -e "${COLOR_GREEN}Postgres and Mailhog are healthy.${NC}\n"
else
    echo -e "${COLOR_RED}Postgres/Mailhog failed to start — is Docker running?${NC}"
    exit 1
fi

echo -e "${COLOR_YELLOW}[2/3] Building the binary...${NC}"
if go build -o ./bin/mrfsentinel ./cmd/server; then
    echo -e "${COLOR_GREEN}Build succeeded.${NC}\n"
else
    echo -e "${COLOR_RED}Build failed — see the output above.${NC}"
    exit 1
fi

# Opens $APP_URL in whatever counts as "the browser" on this platform. Falls
# back to just printing the URL if none of these are found, rather than
# failing the whole script over something this cosmetic.
open_url() {
    if command -v xdg-open >/dev/null 2>&1; then
        xdg-open "$1" >/dev/null 2>&1 &
    elif command -v open >/dev/null 2>&1; then
        open "$1" >/dev/null 2>&1 &
    elif command -v explorer.exe >/dev/null 2>&1; then
        # Git Bash on Windows: explorer.exe opens URLs with the default
        # browser. It sometimes returns a nonzero exit code even when it
        # worked, so don't let `set -e` trip on it.
        explorer.exe "$1" >/dev/null 2>&1 || true
    else
        echo -e "${COLOR_YELLOW}Open $1 in your browser.${NC}"
    fi
}

echo -e "${COLOR_YELLOW}[3/3] Starting MRF Sentinel...${NC}"
DATABASE_URL="postgres://mrfsentinel:mrfsentinel_local_dev@localhost:5432/mrfsentinel?sslmode=disable" \
PUBLIC_BASE_URL="$APP_URL" \
SMTP_HOST=localhost \
SMTP_PORT=1025 \
./bin/mrfsentinel &
APP_PID=$!

# If the script itself gets interrupted (Ctrl+C) or exits, take the app down
# with it — otherwise `run.sh` returning control doesn't actually mean the
# app stopped.
trap 'kill "$APP_PID" 2>/dev/null || true' EXIT INT TERM

echo -n "Waiting for it to come up"
READY=false
for _ in $(seq 1 30); do
    if curl -sf -o /dev/null "$HEALTH_URL"; then
        READY=true
        break
    fi
    echo -n "."
    sleep 1
done

if [ "$READY" = true ]; then
    echo -e " ${COLOR_GREEN}ready.${NC}"
    open_url "$APP_URL"
    echo -e "${COLOR_GREEN}MRF Sentinel is running at $APP_URL — press Ctrl+C to stop it.${NC}"
    echo -e "${COLOR_GREEN}Magic-link emails land in Mailhog: http://localhost:8025${NC}"
else
    echo -e " ${COLOR_RED}still not responding after 30s.${NC}"
    echo -e "${COLOR_YELLOW}Not opening the browser automatically — check the log output above for what's wrong.${NC}"
    echo -e "${COLOR_YELLOW}If it's just slow to start, it may still come up — try $APP_URL yourself, or Ctrl+C and re-run.${NC}"
fi

wait "$APP_PID"
