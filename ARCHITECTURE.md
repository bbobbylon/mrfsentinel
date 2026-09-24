# Architecture

## The shape of the problem

Every U.S. hospital is required, under 45 CFR 180 (CMS's Hospital Price Transparency rule), to
publish a **machine-readable file** (MRF) of its standard charges — one big JSON or CSV file at a
predictable URL. The CY2026 update (effective January 1, 2026, enforced from April 1, 2026) tightened
what that file has to contain: dollar-based negotiated rates now need median/10th-percentile/
90th-percentile/count fields instead of the old single `estimated_amount`, the hospital's Type 2
NPI has to carry a recognized hospital taxonomy prefix, and the attestation has to name a specific
signing officer. MRF Sentinel's entire job is to fetch a hospital's published file, parse it, and
tell you — item by item, rule by rule — whether it actually satisfies those requirements. Three
things fall out of that framing directly:

1. **The files are potentially huge**, and a hospital's file could be malformed or, in principle,
   arbitrarily large. Parsing has to be able to walk a multi-gigabyte JSON array without ever
   holding the whole thing in memory, and fetching has to enforce a hard byte ceiling rather than
   trust whatever the server sends. See "Streaming parsers" below.
2. **Compliance is a checklist, not a single pass/fail bit.** A hospital can be right about its
   name and license and wrong about percentile fields on half its items — the report has to show
   *which* rules passed, *which* failed, and *which specific rows* failed each item-level rule, not
   just a single verdict.
3. **A hospital's data belongs to whoever added it**, the same way DeleteBoard scopes compliance
   data to an organization — except here there's no multi-user "organization" concept at all, just
   individual accounts, each with their own list of hospitals to track. Simpler by design: there
   was no requirement in the source proposal for team-shared hospital lists, so that layer was left
   out rather than guessed at (see "What's intentionally not here yet").

## High-level shape

```
┌─────────────────────────────────────────┐
│              cmd/server (main)            │
│                                             │
│   HTTP :8080 ── internal/web ──────────────┼──► renders html/template pages,
│                     │                       │    serves go:embed'd static assets
│                     ▼                       │
│              internal/auth (magic links,    │
│              sessions)                      │
│                     │                       │
│                     ▼                       │
│              internal/store ────────────────┼──► PostgreSQL (lib/pq)
│                     ▲                       │
│                     │                       │
│              internal/validation (worker) ──┼──► internal/mrf (fetch + parse)
│                     │                       │         │
│                     │                       │         ▼
│              internal/rules (checklist) ◄───┼──  a hospital's published MRF,
│                                             │    fetched over HTTPS
└─────────────────────────────────────────┘
                     │
                     ▼
              net/smtp ──► SMTP relay (Mailhog locally, real SES/etc. in prod)
```

One Go binary, one process, no separate frontend build or container — `internal/web`'s
`html/template` pages and `internal/web/static/*` (a stylesheet and a small polling script) are
compiled directly into the binary via `go:embed` (`internal/web/embed.go`). Compare to
DeleteBoard's Angular-in-nginx-plus-Spring-Boot split: that project genuinely needs two runtimes
because Angular is a real client-rendered SPA. This project doesn't have that kind of UI — the
source proposal explicitly framed the need as "a report, not a beautiful experience" — so a
server-rendered dashboard in the same binary that already has the data is the simpler, equally
correct choice, not a corner cut.

## Package layout

```
mrfsentinel/
├── cmd/server/          main() — wires config → db → migrate → store → mailer → worker →
│                          handlers → router → http.Server with graceful shutdown
├── internal/config/      env-var configuration (see the Spring Boot analogy in its own doc
│                          comments — this is this project's application.yml equivalent)
├── internal/mrf/         MRF fetching + parsing: types, JSON streaming, CSV (tall & wide),
│                          format sniffing, byte-capped HTTP fetch
├── internal/rules/       the CY2026 checklist itself — hospital-level and item-level rules,
│                          evaluated against a parsed internal/mrf.Source
├── internal/store/       PostgreSQL persistence: users, hospitals, validation runs, migrations
├── internal/validation/  the background worker that ties mrf + rules + store together per run
├── internal/auth/        magic-link tokens, session cookies, the mailer
└── internal/web/         HTTP handlers, html/template pages, embedded static assets
```

This is organized by **layer**, not by feature the way DeleteBoard's backend is
(`auth/`, `compliance/`, `audit/`, each with their own model/repository/service/controller). That's
a genuine difference in shape, not just Go-vs-Java convention: DeleteBoard has several independent
features that each touch the database in their own way, so grouping by feature keeps related code
together. MRF Sentinel has one real feature — validate a hospital's MRF against the checklist — with
a handful of supporting concerns (fetch, parse, persist, notify, render) arranged in a pipeline. A
layer-per-package layout makes that pipeline's shape visible in the file tree; a feature-per-package
layout would just recreate the same six folders with one feature in each.

## Streaming parsers (the SAX-vs-DOM decision)

If you've worked with XML, this maps directly onto SAX vs DOM: DOM parses the whole document into
an in-memory tree before you can look at any of it; SAX calls your code once per element as it
scans through, and never buffers more than a small window into the file. `internal/mrf/json.go`
does the JSON equivalent — it uses `encoding/json.Decoder`'s **token-based** API rather than
`json.Unmarshal` into a big struct, reading the `standard_charge_information` array one item at a
time and handing each one to the rest of the pipeline before moving to the next. `internal/mrf/csv.go`
does the same thing with `encoding/csv.Reader`, one row at a time. A hospital's real MRF can be
several gigabytes; `json.Unmarshal`-the-whole-file would mean allocating that whole structure in
memory (often several times its on-disk size, once Go's JSON decoding overhead is counted) before
a single rule could run. The streaming approach means memory use stays roughly constant regardless
of file size — the trade-off, same as SAX, is that you can't randomly jump around the document or
look ahead; everything has to be handled as it's seen, in order. `internal/mrf/fetch.go`'s
`sizeCappedReader` backs this up on the network side — even a well-behaved streaming parser
shouldn't be handed an unbounded `io.Reader` from a server you don't control, so fetching itself
refuses to read past `MaxMRFBytes` (see `internal/config`).

## The checklist engine

`internal/rules/checklist.go` defines two kinds of rule against `internal/rules/result.go`'s
`CheckResult`/`Report` types:

- **Hospital-level rules** (`hospitalChecks()`) run once per file — is there a hospital name, a
  `last_updated_on`, a license number, a CY2026-compliant Type 2 NPI, a named attester, a
  confirmed attestation. Six rules, each either passes or fails for the file as a whole.
- **Item-level rules** (`itemRules`) run once **per row** the parser yields — does this item have a
  description, a code, a gross charge, discounted cash price, at least one negotiated charge, a
  de-identified min/max pair where required, and CY2026 percentile fields where a dollar amount is
  present. `ItemRuleSummary` tracks a pass/fail count and a capped sample of up to 20 failing item
  descriptions per rule (`sampleFailureCap`) — enough to show a human *what's actually wrong* on the
  dashboard without holding every failure for a million-row file.

`Evaluate(src mrf.Source) Report` drives both: it pulls hospital metadata once, then loops
`src.Next()` until `io.EOF`, folding each row into every item rule's running summary. Because this
is fed directly from the streaming parser above, evaluating a checklist against a multi-gigabyte
file never needs the whole file — or even more than one row — in memory at once.

## Persistence and the background worker

`internal/store` is plain `database/sql` + `lib/pq` — no ORM. `internal/store/migrate.go` is a
small hand-rolled runner (`//go:embed migrations/*.sql`, applied in filename order, tracked in a
`schema_migrations` table) rather than Flyway (DeleteBoard's choice) or golang-migrate. That's a
constraint this project's build environment forced, not a preference: golang-migrate's current
release needs a newer Go toolchain than was available, and its own transitive dependencies hit the
same wall `pgx` did — see the sandbox note in `README.md`'s verification table for the full story.
The hand-rolled runner gives the same idempotent-on-restart guarantee (`Migrate()` is called every
time `cmd/server` starts, and running it twice is a no-op) with zero extra dependencies.

`internal/validation/worker.go`'s `Worker.RunAsync()` is what actually runs a validation: it's
launched as a goroutine from the `TriggerRun` HTTP handler, marks the run `running`, fetches +
parses + evaluates, and calls `SaveRunResult` — or, if anything along the way fails,
`MarkRunErrored`, specifically so a run can never get stuck showing "Checking…" forever if the save
step itself fails (a real bug caught during testing — see the verification table). The dashboard
polls `RunStatus` (`internal/web/handlers_dashboard.go`, a small JSON endpoint) every couple of
seconds while a run is in flight, rather than holding a WebSocket or SSE connection open for what's
usually a several-second job.

## A real bug worth understanding: Go template pointer truthiness

`ValidationRun.OverallPassed` is a `*bool` (nullable — a run that's still queued or running has no
verdict yet). Early on, `{{if .OverallPassed}}` in the dashboard templates showed a green
"Compliant" badge for runs that had genuinely failed. The cause: `html/template`'s truthiness check
for a pointer only asks "is this pointer non-nil," the same as `reflect.Value.IsNil()` — it never
follows the pointer to check the boolean *underneath* it. A non-nil pointer to `false` is still
"truthy" to `{{if}}`. This is a real, reproducible Go stdlib behavior, not a guess — see
`internal/web/templates.go`'s `derefBool` function, added specifically to fix it, and its doc
comment for the underlying mechanism. Every template that renders a pass/fail badge from a `*bool`
now calls `derefBool` explicitly rather than relying on bare `{{if}}`.

## Migrations run under an advisory lock

`Migrate` is called by `cmd/server` on every startup, and it is idempotent — it records each applied
file in `schema_migrations` and skips what is already there. What it was *not*, until 2026-09-23, was
safe to run from two processes at once against a database that has no schema yet.

The trap is that `CREATE TABLE IF NOT EXISTS` reads as atomic and is not. Two sessions can both
evaluate the "if not exists" check, both find nothing, and both proceed to create; one wins and the
other fails with `duplicate key value violates unique constraint "pg_type_typname_nsp_index"`. The
same applies to the enum type created in `0001_init.sql`. Postgres documents this: the check and the
creation are not performed as a single atomic step, so `IF NOT EXISTS` protects against a table that
already existed, not against one being created right now.

This surfaced from the test suite rather than from production, because `go test ./...` runs each
package's binary concurrently and both `internal/store` and `internal/validation` migrate on the way
in. It is a production property all the same: two ECS tasks starting together take exactly the same
path, which matters the moment the service in `DEPLOY.md` scales past one task.

`Migrate` now takes `pg_advisory_lock` before touching anything, including the `schema_migrations`
table itself, and releases it at the end. Two details matter in that implementation:

- The lock is taken on a **dedicated `*sql.Conn`**, not through the pool. `pg_advisory_lock` is
  session-scoped, and a pooled `*sql.DB` gives no guarantee that the unlock statement runs on the
  same connection that took the lock.
- The unlock runs on `context.Background()`, not the caller's context, and before the connection is
  returned to the pool. `database/sql` does not reset session state on release, so a lock left
  behind would strand that pooled connection for the life of the process.

The loser of the race no longer errors — it blocks, then finds the migrations already recorded and
skips them, which is the behavior the idempotency was supposed to give in the first place.

## Auth

Magic-link email only — no passkeys. See `AUTH.md` for the full flow and why this project doesn't
need the second auth method DeleteBoard has.

## What's intentionally *not* here yet

- **No tests for `cmd/server`.** Every `internal/` package now has a test file; `cmd/server` does
  not. It is wiring — read config, open the database, migrate, construct the handlers, listen — and
  testing it would mostly assert that the constructor calls happen in the order they are written
  on the screen above. The behavior that matters is covered a layer down. What this does leave
  unguarded is startup *ordering* (Open, then Migrate, then NewStore) and the shutdown path.

  The other packages got their tests on 2026-09-23. Of note in how they are built: `internal/store`
  and `internal/validation` talk to a real Postgres rather than a mock, because what is worth
  testing about them *is* the SQL — an ownership filter that lives inside a `WHERE` clause cannot
  be verified against a fake. CI already provides that database through a `services:` container.
  Those suites skip when `DATABASE_URL` is unset, so a developer without a database still gets a
  green `go test ./...`, but they **fail** rather than skip when `DATABASE_URL` is set and
  unreachable — otherwise a broken CI database would quietly retire them and leave a green tick
  meaning nothing.
- **No distributed run queue.** `RunAsync` is a bare goroutine on whichever process instance
  received the request. Fine for a single running instance; would need a real queue (or at least a
  `SELECT ... FOR UPDATE SKIP LOCKED` claim pattern against `validation_runs`) before running
  multiple instances of `cmd/server` behind a load balancer without risking two instances
  double-processing the same run. Worth revisiting before scaling the ECS service in `DEPLOY.md`
  past one task, same caveat DeleteBoard's `ARCHITECTURE.md` gives its own `@Scheduled` jobs.
- **No re-fetch rate limiting or scheduling.** A user can trigger a validation run as often as they
  click the button; there's no cooldown and no automatic periodic re-check. A hospital's MRF is
  supposed to update monthly, so a scheduled recurring check (the natural next feature) is a clean
  addition on top of the existing `Worker`, not a redesign — it just isn't built yet.
- **No historical trend view.** `ListRunsByHospital` returns every past run, and the hospital page
  lists them, but nothing charts compliance rate over time. The data's already there in
  `validation_runs`/`validation_item_checks`; only the aggregation and a chart are missing.
- **No team/organization scoping.** Hospitals belong to one user (`hospitals.owner_user_id`), full
  stop — there's no sharing a hospital list across a compliance team the way DeleteBoard scopes
  everything to an organization. Nothing in the original proposal called for that, so it wasn't
  speculatively built in; adding it later means adding an `organizations` table and an ownership
  indirection, not reworking the checklist or parsing code at all.
