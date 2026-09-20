# Software Requirements Specification

**Project:** MRF Sentinel
**Version:** 1.0 (MVP)
**Date:** 2026-09-20
**Status:** Requirements reflect what the code in this repository actually does as of this date.
Anything specified but not built is marked **NOT BUILT** rather than described in the present tense.

Related documents: [ARCHITECTURE.md](ARCHITECTURE.md) (how it is put together), [AUTH.md](AUTH.md)
(sign-in), [UI-DESIGN.md](UI-DESIGN.md) (the interface), [DEPLOY.md](DEPLOY.md) (shipping it),
[README.md](README.md) (quick start, and an honest verification table).

---

## 1. Executive Summary

MRF Sentinel fetches the machine-readable file (MRF) of standard charges that a U.S. hospital is
required to publish, parses it, and scores it against a checklist of what CMS actually requires that
file to contain — reporting which rules pass, which fail, and which specific rows are the problem.

The purpose is to let a hospital find its own compliance gaps before CMS does, with particular
attention to the CY2026 additions (dollar percentiles, Type 2 NPI, a named attester) that many
existing files do not yet carry.

**Stakeholders**

| Role | Interest |
|---|---|
| Hospital compliance officer | Primary user. Needs to know whether the file their hospital publishes is compliant, and what to fix. |
| Hospital IT / revenue cycle team | Acts on the specific findings (a column that is missing, rows with no negotiated charge). |
| Project maintainer | Owns the checklist's fidelity to the regulation as CMS updates it. |

---

## 2. System Overview

### 2.1 The problem

Every U.S. hospital must publish a machine-readable file of its standard charges under CMS's Hospital
Price Transparency rule (45 CFR 180). The CY2026 OPPS/ASC final rule adds required content —
effective **January 1 2026**, enforced from **April 1 2026** — that a file compliant under the prior
rules will not have.

Checking such a file by hand is not practical. CMS permits three different layouts (one JSON, two
CSV), a large hospital system's file can run to multiple gigabytes, and the question being asked is
not "is this field present" once but "is this field present on every one of several million rows."

### 2.2 The users

Hospital compliance officers, and the technical staff they hand findings to. The defining
characteristics: they are accountable for the file's correctness, they are not necessarily technical,
and they need to re-check the same hospitals repeatedly as the file is corrected and republished.

### 2.3 Main features

1. **Passwordless sign-in** by emailed one-time link.
2. **Track hospitals** by name and the public URL of the MRF each one publishes.
3. **Validate on demand** — fetch, stream-parse, and score a hospital's MRF against the checklist.
4. **Read a compliance report** — per-rule pass/fail at the hospital level, per-rule compliance rate
   with sampled failing rows at the item level.
5. **Review history** — every past check of a hospital, so improvement over time is visible.

### 2.4 Scope boundary

This system reports on a file a hospital already publishes. It does **not** generate, correct, or
republish that file, and it is not an official CMS determination. See §7.3.

---

## 3. Functional Requirements

Requirement IDs are stable and referenced elsewhere in this document.

### FR-1 — Authentication

| ID | Requirement |
|---|---|
| FR-1.1 | A visitor may request a sign-in link by submitting an email address. The address must parse as a valid address (RFC 5322, via Go's `net/mail`); an invalid one is rejected inline on the same page. |
| FR-1.2 | The confirmation shown after a request must be identical whether or not an account exists for that address. There is no separate sign-up: an unrecognized address becomes an account on first successful sign-in. |
| FR-1.3 | A sign-in link expires **15 minutes** after issue and may be consumed **exactly once**, enforced atomically at the database so two concurrent clicks cannot both succeed. |
| FR-1.4 | Consuming a valid link establishes a session and redirects to the dashboard. Consuming an invalid, expired, or already-used link returns the user to the sign-in page with an explanatory message. |
| FR-1.5 | A session lasts `SESSION_TTL_HOURS` (default **14 days**) and is carried in an `HttpOnly`, `SameSite=Lax` cookie, marked `Secure` whenever the configured public base URL is `https://`. |
| FR-1.6 | Signing out must invalidate the session on the server as well as clearing the cookie, so the cookie value cannot be replayed. |
| FR-1.7 | Only the raw token is ever sent to the user; the database stores a SHA-256 hash of it, for both magic links and sessions. |

### FR-2 — Hospital management

| ID | Requirement |
|---|---|
| FR-2.1 | A signed-in user may add a hospital by supplying a display name and the URL of its published MRF. Both are required; the URL must begin with `http://` or `https://`. |
| FR-2.2 | Adding a hospital immediately starts its first validation run, so the user lands on a report rather than an empty page. |
| FR-2.3 | A user may see only the hospitals they added. Ownership is enforced inside every query that reads a hospital, a run, or a report — not as a separate check a caller could omit. |
| FR-2.4 | A rejected "add hospital" submission must re-render the form with the user's own values still present and an explanation, at HTTP 422. |
| FR-2.5 | The dashboard lists every tracked hospital with the status of its most recent check. |

### FR-3 — Validation runs

| ID | Requirement |
|---|---|
| FR-3.1 | A user may start a new check of an already-tracked hospital at any time. Runs are additive; none replaces an earlier one. |
| FR-3.2 | A run is recorded as `pending` before any fetching begins, so the user sees it exists immediately rather than waiting on a multi-gigabyte download. |
| FR-3.3 | A run progresses `pending` → `running` → either `succeeded` (the file was checked, whatever the verdict) or `failed` (the file could not be fetched, parsed at all, or saved). |
| FR-3.4 | The system must auto-detect which of CMS's three layouts the file uses — JSON, CSV tall, or CSV wide — and report which it detected. |
| FR-3.5 | The file must be read as a stream. Peak memory use must not scale with file size. |
| FR-3.6 | A download must stop and the run must fail if the file exceeds `MAX_MRF_MEBIBYTES` (default **4096 MiB**) or the run exceeds `FETCH_TIMEOUT_MINUTES` (default **15 minutes**). |
| FR-3.7 | A file that parses correctly and then becomes malformed partway through must still produce a report covering the rows read up to that point, with the parse error recorded alongside. A partial report is required behavior, not a degraded mode. |
| FR-3.8 | Every terminal outcome, including an internal failure to save results, must be written to the run's status. A run must never remain visibly in progress after its worker has stopped. |
| FR-3.9 | A run's results — summary, hospital-level checks, item-level checks — must be persisted atomically. |

### FR-4 — The compliance checklist

**FR-4.1 Hospital-level rules** — scored once per file, pass or fail.

| Rule ID | Checks |
|---|---|
| `HOSP-NAME` | The hospital's name is present. |
| `HOSP-LAST-UPDATED` | A last-updated date is present. |
| `HOSP-LICENSE` | Licensure information is present. Fails only if *both* the number and the state are absent — CMS permits omitting a license number a hospital genuinely does not hold. |
| `CY26-TYPE2-NPI` | At least one Type 2 (organizational) NPI is present and is exactly ten digits. **Shape only** — see §7.3. |
| `CY26-ATTESTER-NAME` | A named attester is present. |
| `CY26-ATTESTATION` | An attestation block exists *and* is affirmatively confirmed. The two failure modes are reported distinctly, because they need different fixes. |

**FR-4.2 Item-level rules** — scored against every row, reported as a compliance rate.

| Rule ID | Checks |
|---|---|
| `ITEM-DESCRIPTION` | The item has a description. |
| `ITEM-CODE` | At least one billing code, together with its code type. |
| `ITEM-GROSS-CHARGE` | A gross charge is reported. |
| `ITEM-DISCOUNTED-CASH` | A discounted cash price is reported. |
| `ITEM-NEGOTIATED-CHARGE` | At least one payer-specific negotiated charge, in any of the three forms CMS accepts (dollar amount, percentage, or a described algorithm). |
| `ITEM-DEIDENTIFIED-MINMAX` | Both the de-identified minimum and maximum are reported. |
| `CY26-PERCENTILES` | The CY2026 dollar percentiles — median, 10th, 90th, and a count — that replace the pre-2026 single estimated allowed amount. |

| ID | Requirement |
|---|---|
| FR-4.3 | A field reported as absent must be distinguishable from a field reported as zero. A `$0` charge is data; a blank cell is a finding. |
| FR-4.4 | Each item-level rule reports an exact count of rows checked and rows failed, plus up to **20** sampled human-readable pointers to failing rows (item number and description). The count stays exact regardless of the sample cap. |
| FR-4.5 | A rule that checked zero rows is reported as failed. "No data to check" is not compliance. |
| FR-4.6 | The overall verdict passes only if every hospital-level check passed, every item-level rule was satisfied by 100% of rows, and no parse error occurred. |

### FR-5 — Reporting

| ID | Requirement |
|---|---|
| FR-5.1 | A run's report shows the detected format, the number of rows checked, every hospital-level result with its explanatory detail, and every item-level rule's compliance rate, tallies, and sampled failures. |
| FR-5.2 | While a run is in flight, its report page must indicate that and update on its own when the run finishes, without the user reloading. |
| FR-5.3 | A `failed` run must show why it failed. |
| FR-5.4 | A partial report (FR-3.7) must say plainly that it is partial and over how many rows. |
| FR-5.5 | A hospital's page lists its full run history, newest first. |

### FR-6 — Operations

| ID | Requirement |
|---|---|
| FR-6.1 | The service exposes an unauthenticated `GET /healthz` returning 200 when the process is serving. It deliberately does **not** check the database: a transient database fault should fail requests, not cause an orchestrator to kill the process. |
| FR-6.2 | Schema migrations apply automatically and idempotently at startup. |
| FR-6.3 | All configuration comes from environment variables, each with a working local-development default. An unparseable numeric setting must fail startup rather than silently fall back. |
| FR-6.4 | Logs are structured JSON on stdout. |
| FR-6.5 | The process shuts down gracefully on `SIGINT`/`SIGTERM`, with a 15-second drain. |

---

## 4. Non-Functional Requirements

### 4.1 Performance

| ID | Requirement | Status |
|---|---|---|
| NFR-1.1 | Peak memory during validation must be bounded by parser buffers, not file size. | Met by design — see ARCHITECTURE.md's streaming-parser section. |
| NFR-1.2 | Page renders must not block on a validation run. | Met — runs execute on a background goroutine; pages read state from the database. |
| NFR-1.3 | An in-flight report page polls every 3 seconds (5 after a transient error). | Met. Polling is chosen deliberately over SSE/WebSocket for runs that take seconds to minutes. |
| NFR-1.4 | No throughput or latency target is set for the MVP. | **Not specified.** Stating a number no one has measured would be worse than admitting none exists. |

### 4.2 Security

| ID | Requirement | Status |
|---|---|---|
| NFR-2.1 | No passwords are stored, because none exist. | Met. |
| NFR-2.2 | Magic-link and session tokens are 256 bits from a CSPRNG, stored only as SHA-256 hashes. | Met. |
| NFR-2.3 | Session cookies are `HttpOnly` and `SameSite=Lax`, and `Secure` whenever deployed over HTTPS. | Met. |
| NFR-2.4 | Sign-in must not reveal whether an email address has an account. | Met. |
| NFR-2.5 | Every read of a hospital, run, or report is scoped to its owner within the SQL itself. | Met. |
| NFR-2.6 | All SQL uses bound parameters; no query is assembled by string concatenation. | Met. |
| NFR-2.7 | All user-supplied and file-supplied content is rendered through `html/template`, which escapes contextually. | Met. |
| NFR-2.8 | Outbound fetching is bounded in both size and time (FR-3.6). | Met. |
| NFR-2.9 | Rate limiting on sign-in requests. | **NOT BUILT.** Nothing limits how many links can be requested for an address. Documented rather than implied. |
| NFR-2.10 | SMTP credentials for a production relay. | **NOT BUILT.** `SendMail` is called with nil auth, which suits Mailhog and local relays only. Flagged in DEPLOY.md. |

### 4.3 Reliability

| ID | Requirement | Status |
|---|---|---|
| NFR-3.1 | A run's results save atomically or not at all. | Met — single transaction. |
| NFR-3.2 | Each migration applies inside its own transaction. | Met — relies on Postgres's transactional DDL. |
| NFR-3.3 | A failed save must still mark the run failed, so no run is stuck "checking" forever. | Met — this was a real bug, found by end-to-end execution and fixed. |
| NFR-3.4 | The initial database connection retries for ~20 seconds at startup. | Met. |
| NFR-3.5 | No uptime target is committed. | **Not specified** — single instance, no SLA. |

### 4.4 Scalability

| ID | Requirement | Status |
|---|---|---|
| NFR-4.1 | The MVP runs as exactly **one** instance. | Accepted limitation. The job queue is "start a goroutine," which has no cross-process coordination; running two replicas is not safe without a real queue. See ARCHITECTURE.md's honest-scoping section. |
| NFR-4.2 | In-flight runs are lost if the process restarts. | Accepted limitation, same root cause. |
| NFR-4.3 | The dashboard issues one query per tracked hospital to find its latest run. | Accepted N+1, at a scale where it does not matter (a user tracks a handful of hospitals). Named rather than hidden. |

### 4.5 Maintainability and portability

| ID | Requirement | Status |
|---|---|---|
| NFR-5.1 | Every exported and unexported declaration carries a doc comment explaining what it does and how it relates to the rest of the codebase. | Met — 166/166 declarations as of this date. |
| NFR-5.2 | CI must enforce `gofmt`, `go vet`, `go build`, and `go test` on every push and pull request. | Met — `.github/workflows/ci.yml`. |
| NFR-5.3 | The deployable artifact is a single binary with the frontend compiled in; no separate asset build or deploy step exists. | Met — `go:embed`. |
| NFR-5.4 | The app runs on Linux, macOS, and Windows, with one-command local startup on each. | Met — `run.sh` and `run.cmd`. |
| NFR-5.5 | Test coverage of `internal/store`, `internal/auth`, `internal/validation`, `internal/web`. | **NOT BUILT.** These have no `_test.go` files; they were verified by real end-to-end execution instead. A real gap — see README.md's verification table. |

---

## 5. User Stories

1. As a compliance officer, I want to sign in without managing another password, so that access is one click from my inbox.
2. As a compliance officer, I want to add our hospital's published MRF URL and get a verdict immediately, so that I learn where we stand without scheduling anything.
3. As a compliance officer, I want to know *which* CY2026 fields our file is missing, so that I can hand IT a specific list rather than "make it compliant."
4. As a compliance officer, I want the percentage of rows failing each rule, so that I can tell a systemic gap (0% compliant — a column is missing) from a data-quality gap (98% compliant — a few rows are wrong).
5. As a compliance officer, I want example rows for a failing rule, so that someone can open the file and see the problem rather than search for it.
6. As a compliance officer, I want to re-check after we republish and compare against previous checks, so that I can show the gap closed.
7. As a compliance officer, I want a usable report even when our file is malformed partway through, so that a single bad row does not cost me the other million.
8. As a compliance officer tracking several facilities, I want one page showing each one's current status, so that I know where to spend attention.
9. As an operator, I want the service to migrate its own schema and report its own health, so that deployment needs no manual step.

---

## 6. Success Criteria

**Functional**

- A hospital's MRF in any of the three CMS layouts is fetched, parsed, and scored end to end.
- A file missing CY2026 content is flagged specifically, naming the rules and the rows.
- A multi-gigabyte file completes without memory growth proportional to its size.
- A file that breaks mid-stream still yields a partial report.

**Quality** — all of these are enforced in CI and currently pass:

- `gofmt -l .` reports nothing; `go vet ./...` reports nothing; `go build ./...` succeeds.
- `go test ./...` passes (7 tests, across `internal/mrf` and `internal/rules`).
- Every declaration carries a doc comment.

**Product metrics** — **not instrumented.** There is no analytics or telemetry in this codebase.
Were this to become a real service, the first metrics worth having would be: runs per hospital over
time (does a hospital re-check after fixing?), the distribution of failing rules across all checked
files (which requirement is hardest industry-wide?), and time-to-first-report.

---

## 7. Constraints

### 7.1 Regulatory

- The checklist must track 45 CFR 180 and the CY2026 OPPS/ASC final rule. Field names come from CMS's
  own CSV and JSON data dictionaries — cited in README.md's Sources section, not inferred.
- CY2026 requirements are enforced from April 1 2026, which sets the product's relevance window.
- CMS may revise field names or requirements. Item-level rules are a single table (`itemRules` in
  `internal/rules/checklist.go`) specifically so that adding one is a one-entry change.

### 7.2 Technical

- **Go 1.24**, standard library first: no web framework, no ORM, no CSS framework, no frontend
  framework, no migration tool. Each omission is argued in ARCHITECTURE.md rather than assumed.
- **PostgreSQL**, required — not interchangeable. The schema uses `gen_random_uuid()` and a native
  enum, and migrations depend on transactional DDL.
- **`lib/pq`** rather than the more actively maintained `pgx`: a build constraint in the environment
  this was developed in, not a preference. See ARCHITECTURE.md.
- **Docker** is needed locally only for Postgres and Mailhog; the app itself runs as a plain binary.

### 7.3 Known limitations — explicitly out of scope for the MVP

These are stated here so they are not mistaken for oversights:

1. **NPI validation is structural only.** `CY26-TYPE2-NPI` checks for ten digits. It does not verify
   the NPI is real, active, or carries a hospital taxonomy code — that requires a live NPPES lookup.
   The report says so in its own detail text rather than implying more than it checked.
2. **Wide-format CSV is checked at reduced fidelity.** A wide row is collapsed to "is there
   negotiated-charge data anywhere on this line" rather than expanded into one row per payer-plan, so
   a wide file's per-payer gaps are not individually reported.
3. **One JSON field placement is inferred.** The pre-2026 `estimated_amount` is read at the per-payer
   level by analogy with the CSV tall format, not independently confirmed against the JSON
   dictionary. If that inference is wrong the field simply never populates, and `CY26-PERCENTILES`
   still judges files on the presence of the new fields — the check that actually matters.
4. **JSON metadata after the charges array is not read.** The parser stops scanning top-level keys
   when it reaches `standard_charge_information`.
5. **No notifications, no scheduled re-checking, no multi-user organizations, no export.** Every
   check is manually triggered by its owner and read in the browser.
6. **This is not an official compliance determination.** It reports what a file contains against a
   documented checklist. It cannot speak for CMS.

### 7.4 Resource

Single maintainer, no budget for paid data sources (which is part of why NPPES verification is
deferred), and no production deployment currently exists — DEPLOY.md describes a target
architecture that has not been provisioned.
