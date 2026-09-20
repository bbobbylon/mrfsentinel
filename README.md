# MRF Sentinel

Compliance-as-a-service for hospital price transparency. Every U.S. hospital has to publish a
machine-readable file (MRF) of its standard charges under CMS's Hospital Price Transparency rule
(45 CFR 180) — and as of the CY2026 update (effective January 1 2026, enforced from April 1 2026),
that file has to contain new fields (dollar percentiles, a CY2026-compliant Type 2 NPI, a named
attester) that a lot of hospitals' existing files don't have yet. MRF Sentinel fetches a hospital's
published file, parses it — JSON or CSV, in either of CMS's tall/wide CSV shapes — and runs it
against a checklist of what the file is actually required to contain, item by item, so you can see
exactly which rules pass, which fail, and which specific rows are the problem.

See `ARCHITECTURE.md` for how the system is put together, `AUTH.md` for how sign-in works, and
`DEPLOY.md` for shipping it to AWS.

## Verification status — read this before trusting anything below

This project was built in the same kind of sandboxed environment as DeleteBoard, but hit a
**different** set of network restrictions — `proxy.golang.org` (Go's module proxy) is blocked, but
`github.com` itself is reachable, and Postgres 16 and a Go 1.24 toolchain are actually installed
locally. That combination meant almost the entire stack could be build- and run-verified for real,
which is a meaningfully stronger story than DeleteBoard's — worth reading honestly rather than
assuming "sandboxed" means "unverified" across the board.

| Component | Status | What was actually checked |
|---|---|---|
| Go build (`go build ./...`) | **Verified, repeatedly** | Builds cleanly as of this writing — most recently re-checked immediately before packaging this handoff, not just once during early development. `gofmt -l .` reports no unformatted files; `go vet ./...` reports nothing. |
| `internal/mrf` (JSON + CSV parsing) | **Unit-tested, real** | `go test ./internal/mrf/...` — 4 tests, all passing, against hand-built JSON and CSV (tall + wide) fixtures covering the CMS field set. |
| `internal/rules` (checklist engine) | **Unit-tested, real** | `go test ./internal/rules/...` — 3 tests, all passing, including one that deliberately feeds a partially-noncompliant file and checks the report flags exactly the right rules. |
| `internal/store`, `internal/auth`, `internal/validation`, `internal/web` | **No automated tests — verified by real end-to-end execution instead** | These packages have no `_test.go` files yet — a real gap, called out explicitly in `ARCHITECTURE.md`'s "What's intentionally not here yet." What they do have: this project was actually run — the compiled binary, a real local PostgreSQL 16 instance, and a hand-rolled SMTP catcher script — through the full user journey via `curl`: request a magic link, receive it (from the fake SMTP catcher), consume it, get a session cookie, create a hospital, trigger a validation run against a test MRF fixture, and read back the rendered compliance report. That run surfaced and led to fixing two real bugs (below) that `go build`/`go vet` could never have caught. Most recently re-verified: a fresh build against this exact on-disk code started cleanly against the same Postgres instance, applied its (already-applied) migrations idempotently with no error, and answered `/healthz` and `/` correctly. |
| Two real bugs found this way | **Found and fixed** | (1) `pq.Array(nil)` for a rule with zero sample failures serialized to SQL `NULL`, violating a `NOT NULL` column constraint — only appeared when a real save happened against a real schema. Fixed in `internal/store/runs.go`. (2) `html/template`'s `{{if}}` on a `*bool` only checks non-nil, never dereferences — the dashboard showed "Compliant" for runs that had actually failed. Fixed via a `derefBool` template function (`internal/web/templates.go`). Both fixes were re-verified by re-running the same end-to-end flow and confirming correct output. See `ARCHITECTURE.md` for the second one's mechanism in detail — it's a general Go gotcha worth understanding, not just a one-off fix. |
| `docker-compose.yml` | **Syntax-validated only** | `docker compose config` parses it cleanly. The Docker CLI is present in this sandbox but **no Docker daemon is running**, so `docker compose up --build` has never actually executed here — the app itself was run as a plain local binary against a real (non-containerized) Postgres instead. |
| `Dockerfile` / `docker build` | **Not run** | Same reason — no daemon available. The multi-stage build (`golang:1.24-alpine` → `alpine:3.24`) follows standard, well-established patterns and both base image tags were checked against Docker Hub's actual current listings, but "the pattern is standard" isn't the same as "this exact Dockerfile has built successfully." Treat your first `docker build` as the real first test. |
| `run.sh` | **Actually executed, partially** | The build step (`go build`), the health-check polling loop, and the graceful-shutdown trap all ran for real in this sandbox and worked as written. The Docker Compose step it calls first (to start Postgres + Mailhog) could not be exercised here for the same no-daemon reason above — on a machine with Docker actually running, that step is untested beyond its `docker compose config` syntax check. |
| `run.cmd` | **Not run** | Windows-only; this sandbox is Linux. Its `for`/`goto`/`errorlevel` health-check loop follows the same well-established batch patterns as DeleteBoard's `run.cmd`, but has never executed anywhere. |
| GitHub Actions CI (`.github/workflows/ci.yml`) | **Not run** | GitHub's runners aren't subject to this sandbox's restrictions, so it should succeed at everything this table already verified locally (build, vet, test, and — unlike this sandbox — an actual `docker build`, since GitHub's runners do have a working daemon). That's a reasoned expectation, not a result — treat the first real CI run as the first time `docker build` for this project has ever actually happened. |
| GitHub Actions CD (`.github/workflows/cd.yml`) | **Not run** | Requires AWS infrastructure this sandbox has no access to provision. See `DEPLOY.md`. |

**If you want to re-verify any of this yourself, the fastest path is:** `go build ./... && go vet
./... && go test ./...`, then `docker compose up --build` (or `./run.sh` if you don't want the
Docker path) — in that order, since the Go-level checks are fast and the Docker path is the one
piece of this stack that has never run end-to-end in this sandbox.

## Tech stack, and why

- **Backend: Go 1.24, stdlib-first.** No web framework, no ORM — `net/http.ServeMux`'s enhanced
  routing (Go 1.22+) is enough router for this app's handful of routes, and `database/sql` +
  `lib/pq` is enough database access without pulling in a full ORM's behavior-at-a-distance for a
  schema this size. `lib/pq` specifically (over the more actively developed `pgx`) was a build
  constraint, not a preference — see `ARCHITECTURE.md`'s persistence section for why.
- **Server-rendered HTML (`html/template`), not a separate SPA.** The source proposal for this app
  described the UI need as "a report, not a beautiful experience" and left the frontend choice
  open. A dashboard that's mostly "here's a table, here's a pass/fail badge, here's a drill-down
  page" doesn't need a client-side framework's complexity — Go's own `html/template`, with the
  whole frontend compiled into the same binary via `go:embed`, is the simpler tool that's equally
  correct for this shape of UI. If this product's UI needs ever grow into something more
  interactive, that's a real reason to revisit this choice — it wasn't picked to avoid learning
  Angular.
- **PostgreSQL**, same reasoning as DeleteBoard: real transactional guarantees for `SaveRunResult`
  (a validation run's hospital-checks and item-checks all have to land together or not at all) and
  a schema (`internal/store/migrations/0001_init.sql`) that fits Postgres's native
  `gen_random_uuid()` without an extra extension.
- **Auth: magic links only, no passkeys.** See `AUTH.md` for the full reasoning — the short version
  is that this app's stakes for "who is signed in" are genuinely lower than DeleteBoard's, so the
  extra auth method didn't earn its complexity here.

## Run it locally

### Option A — Docker Compose (closest to how it'll run in production)

```bash
docker compose up --build
```

This builds and starts three containers: Postgres, Mailhog (an SMTP catcher — magic-link emails
land here instead of a real inbox), and the app itself. Once everything's healthy:

- App: http://localhost:8080
- Mailhog inbox (read magic-link emails here): http://localhost:8025
- Health check: http://localhost:8080/healthz

**This is the one path in the verification table above that has never actually run in this
sandbox** (no Docker daemon here) — treat your first run of this command as this project's real
first Docker build, not a formality.

### Option B — One command, one process, no Docker for the app itself

```bash
./run.sh      # macOS/Linux/Git Bash
run.cmd       # Windows, double-click or from cmd/PowerShell
```

Builds the Go binary — which already has the entire frontend compiled in via `go:embed`, so
there's no separate frontend build step the way DeleteBoard's `run.sh` needs one — and runs it
directly. Postgres and Mailhog still run as Docker containers first (there's no getting around
needing a real Postgres), so Docker is still required for this option too; the difference from
Option A is that the app itself runs as a plain local process, not a container, which is faster to
iterate on while developing. Both scripts poll `/healthz` after starting the app and open your
default browser automatically once it's actually ready, rather than firing the browser the instant
the process starts. `run.sh`'s build step, health-check loop, and shutdown handling were run for
real in this sandbox (see the verification table); `run.cmd` has not been executed anywhere.

### Running the test suite

```bash
go test ./... -v
```

`internal/mrf` and `internal/rules` have real unit tests and need no external services.
`internal/store`/`internal/auth`/`internal/validation`/`internal/web` currently have none — see the
verification table for how they were checked instead, and `ARCHITECTURE.md` for what a real test
suite for those packages would need (mainly: a way to spin up a disposable Postgres per test run,
the same `services:` container CI already uses).

### Configuration

All configuration is environment variables, read by `internal/config.Load()` — see that file's doc
comments for the full list, defaults, and a Spring Boot `application.yml` analogy if that's a more
familiar frame. The two you'll actually need for local dev are `DATABASE_URL` and `PUBLIC_BASE_URL`
(both already set for you by `docker-compose.yml` and `run.sh`/`run.cmd`).

## Project layout

```
mrfsentinel/
├── cmd/server/           main() — see ARCHITECTURE.md for the full startup sequence
├── internal/             all application code — see ARCHITECTURE.md for the package breakdown
├── docker-compose.yml    Local dev stack: postgres + mailhog + app
├── run.sh / run.cmd      One-command local run (Option B above)
└── .github/workflows/    CI (build/vet/test/docker build) and CD (AWS ECS deploy) on push/PR
```

## Deploying

See `DEPLOY.md` for the AWS ECS Fargate deployment path — one image, one service, simpler than
DeleteBoard's two-service split since this app has no separate frontend to run.
