# MRF Sentinel

Compliance-as-a-service for hospital price transparency. Every U.S. hospital has to publish a
machine-readable file (MRF) of its standard charges under CMS's Hospital Price Transparency rule
(45 CFR 180) — and as of the CY2026 update (effective January 1 2026, enforced from April 1 2026),
that file has to contain new fields (dollar percentiles, a CY2026-compliant Type 2 NPI, a named
attester) that a lot of hospitals' existing files don't have yet. MRF Sentinel fetches a hospital's
published file, parses it — JSON or CSV, in either of CMS's tall/wide CSV shapes — and runs it
against a checklist of what the file is actually required to contain, item by item, so you can see
exactly which rules pass, which fail, and which specific rows are the problem.

### Documentation map

| Document | What it covers |
|---|---|
| [`SRS.md`](SRS.md) | What the system is required to do — numbered functional and non-functional requirements, user stories, and the limitations that are deliberately out of scope. |
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | How it is put together, and why each "we didn't use X" decision was made. |
| [`UI-DESIGN.md`](UI-DESIGN.md) | The interface: design tokens, components, user flows, and measured accessibility contrast. |
| [`AUTH.md`](AUTH.md) | How sign-in works, and why it is magic-links-only. |
| [`DEPLOY.md`](DEPLOY.md) | Shipping it to AWS ECS Fargate, plus the one-time infrastructure setup. |

Every Go declaration in this repository also carries a doc comment explaining what it does and how
it relates to the rest of the codebase — `go doc ./internal/...` is a usable tour of the code.

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
| `Dockerfile` / `docker build` | **Verified 2026-09-20** | `docker build -t mrfsentinel:ci .` completed successfully against a real Docker daemon (Docker Desktop 29.3.1, Windows). Both stages resolve and build: `golang:1.24-alpine` compiles the static binary, `alpine:3.24` receives it. This was the first time this Dockerfile had ever actually been built — the note it replaces correctly warned that "the pattern is standard" was not the same as "it builds." It does. |
| `run.sh` | **Actually executed, partially** | The build step (`go build`), the health-check polling loop, and the graceful-shutdown trap all ran for real in this sandbox and worked as written. The Docker Compose step it calls first (to start Postgres + Mailhog) could not be exercised here for the same no-daemon reason above — on a machine with Docker actually running, that step is untested beyond its `docker compose config` syntax check. |
| `run.cmd` | **Executed 2026-09-20; one real bug found and fixed** | Run for the first time, on Windows 11. It surfaced a genuine defect that had nothing to do with its logic: the file was committed with **LF-only line endings**, which cmd.exe mis-parses — `REM` lines were truncated mid-token and the script emitted `'M' is not recognized as an internal or external command` before doing anything. Fixed by converting the file to CRLF and adding a [`.gitattributes`](.gitattributes) that pins `*.cmd`/`*.bat` to CRLF and `*.sh`/`Dockerfile`/YAML to LF, so Git cannot undo it on a future checkout. After the fix the script parses cleanly and its new preflight check runs correctly. The Docker/build/health-poll path beyond the preflight is still unexercised here, because this machine has no Go toolchain installed. |
| GitHub Actions CI (`.github/workflows/ci.yml`) | **Ran, failed, diagnosed, fixed** | The first push failed at *Verify go.mod/go.sum are tidy*: `lib/pq` was marked `// indirect` in `go.mod` although `internal/store/db.go` imports it directly (a blank `_` import still counts as direct), so `go mod tidy` rewrote the line and `git diff --exit-code` tripped. `go.mod` has been tidied, and the step now prints an explanation rather than a bare diff. Every other step in the workflow was then run locally to completion — `go mod download`, the tidy check, `gofmt -l`, `go vet`, `go build`, `go test`, and `docker build` — all passing. The workflow also now cancels superseded runs and requests read-only permissions. |
| GitHub Actions CD (`.github/workflows/cd.yml`) | **Ran, failed by design; now skips instead** | It failed at *Configure AWS credentials* with `Input required and not supplied: aws-region`, because the AWS infrastructure in `DEPLOY.md` has never been provisioned. That was documented behavior, but it meant every push to `main` showed a red X. The deploy job is now guarded by `if: vars.AWS_REGION != '' && vars.ECS_CLUSTER != ''`, so it skips cleanly until those repository variables are set. The deploy path itself remains genuinely unrun — see `DEPLOY.md`. |
| Doc comments (`go doc`) | **Verified 2026-09-20, mechanically** | Every top-level `func`, `type`, `const`, and `var` in the repository carries a doc comment — **164 of 164**, exported and unexported, test helpers included — checked by script rather than by eye, and re-checked after `gofmt` confirmed the files were still clean. See `CLAUDE.md` for the audit command and the gofmt alignment trap that adding these comments can spring. |
| Documentation set | **Complete as of 2026-09-20** | `README.md`, `SRS.md`, `ARCHITECTURE.md`, `UI-DESIGN.md`, `AUTH.md`, `DEPLOY.md`, all cross-linked from the map at the top of this file. The contrast ratios quoted in `UI-DESIGN.md` were computed from the actual CSS tokens, not estimated. One pair originally failed WCAG AA — the pending badge at 4.18:1 — and the `--pending` token was darkened to `#855c00` (5.31:1) to fix it; every pair in the table now passes. Note this is contrast only: `UI-DESIGN.md` §5.4 lists six accessibility gaps that remain open, and nothing here has been tested with a real screen reader. |

**If you want to re-verify any of this yourself, the fastest path is:** `go build ./... && go vet
./... && go test ./...`, then `docker compose up --build` (or `./run.sh` if you don't want the
Docker path) — in that order, since the Go-level checks are fast and the container path exercises
the most moving parts at once.

**No Go toolchain on your machine?** You can still run every Go-level check, using the same image
CI does — nothing to install beyond Docker:

```bash
docker run --rm -v "$PWD:/src" -v mrfsentinel-gomod:/go/pkg/mod -w /src golang:1.24-alpine \
  sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'
```

(`gofmt -l .` printing any filename is a failure — CI treats it as one.) That is exactly how the
2026-09-20 rows in the table above were verified, on a Windows machine with Docker but no local Go.

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
├── .github/workflows/    CI (build/vet/test/docker build) and CD (AWS ECS deploy) on push/PR
└── *.md                  Documentation — see the map at the top of this file
```

## Deploying

See `DEPLOY.md` for the AWS ECS Fargate deployment path — one image, one service, simpler than
DeleteBoard's two-service split since this app has no separate frontend to run.
