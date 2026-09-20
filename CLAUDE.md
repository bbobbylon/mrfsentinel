# CLAUDE.md — MRF Sentinel

Project-specific instructions. These take precedence over the global Spring Boot recipe in
`~/.claude/CLAUDE.md`, which **does not apply here**: this is a Go project, not a Java one. There is
no Maven, no Spring, no Angular, no JPA. Do not scaffold any of them into this repo.

---

## What this is

A compliance checker for hospital price transparency files. It fetches the machine-readable file
(MRF) a U.S. hospital publishes under 45 CFR 180, stream-parses it (JSON, CSV tall, or CSV wide),
and scores it against a CY2026 checklist. See [SRS.md](SRS.md) for requirements and
[ARCHITECTURE.md](ARCHITECTURE.md) for the design.

**Stack:** Go 1.24 (stdlib-first), PostgreSQL, `html/template` server-rendered UI compiled into the
binary via `go:embed`, magic-link auth. One binary, one process, one container image.

---

## Verifying changes when Go isn't installed

Go is **not** installed on this machine, but Docker is. Run the full CI sequence in a container —
this is the standard way to verify work here, and it must pass before anything is committed:

```bash
docker run --rm \
  -v "B:\Documents\Coding\GithubRepository\MRFSentinel:/src" \
  -v mrfsentinel-gomod:/go/pkg/mod \
  -w /src golang:1.24-alpine \
  sh -c 'gofmt -l . && go vet ./... && go build ./... && go test ./...'
```

`gofmt -l .` printing **any** filename is a failure — CI treats it as one. The named volume caches
modules between runs.

Do not claim a change builds, vets, formats, or passes tests without having actually run this.

---

## Conventions that are enforced

### 1. Every declaration carries a doc comment

Every top-level `func`, `type`, `const`, and `var` — exported *and* unexported, including test
helpers — has a doc comment. Coverage is currently **166/166**. Keep it there when adding code.

Comments in this codebase explain **how a thing relates to the rest of the system**, not what the
next line does. They routinely: name the caller, point at the file that holds the other half of a
pattern, explain why an alternative was rejected, and draw a Spring/Angular analogy where it helps
(the author's other projects are Spring Boot). Follow that voice — a one-line restatement of the
function signature is not what this project means by a doc comment.

Audit coverage with:

```bash
python -c "
import io,glob
for p in sorted(glob.glob('**/*.go', recursive=True)):
    lines=io.open(p,encoding='utf-8').read().split('\n')
    for i,l in enumerate(lines):
        if l.startswith(('func ','type ','const ','var ')) and (i==0 or not lines[i-1].lstrip().startswith('//')):
            print('MISSING %s:%d %s'%(p,i+1,l[:70]))
"
```

**gofmt trap:** inserting a comment between two consecutive single-line functions splits the run
gofmt's tabwriter was aligning, so the surviving padding before `{` becomes wrong and `gofmt -l`
flags the file. When documenting one of a pair like `Format()` / `Metadata()`, collapse the padding
to a single space and separate the declarations with a blank line.

### 2. Use em dashes, not `--`

The prose in comments and docs uses `—`. There are zero occurrences of ` -- ` in the Go sources;
keep it that way.

### 3. Line endings are load-bearing

`.gitattributes` pins them. `run.cmd` **must** be CRLF — cmd.exe truncates `REM` lines in an LF-only
batch file and fails with `'M' is not recognized as an internal or external command`. `run.sh`,
`Dockerfile`, and YAML **must** be LF. Don't "normalize" either one.

### 4. Stdlib-first, and the omissions are deliberate

No web framework (`net/http.ServeMux` with Go 1.22+ patterns is enough), no ORM (`database/sql` +
`lib/pq`), no migration tool (a ~60-line hand-rolled runner in `internal/store/migrate.go`), no CSS
framework, no frontend framework, no JavaScript build step. Each omission is argued in
ARCHITECTURE.md. **Do not add a dependency to this project without saying explicitly why the stdlib
is insufficient**, and don't "modernize" these choices into a framework.

`lib/pq` rather than `pgx` is a recorded constraint, not an oversight — see ARCHITECTURE.md before
proposing a swap.

### 5. Honesty about verification is a feature

README.md's verification table states exactly what has and hasn't been run, per component. This is
one of the repo's most valuable documents. When you verify something new, **update that row**; when
you add something unverified, add a row saying so. Never upgrade a row's status without having
actually done the thing.

The same applies in code and docs: limitations are named in place (NPI shape-only checking, wide-CSV
reduced fidelity, one inferred JSON field placement). Don't quietly delete those caveats.

### 6. Documentation set

`README.md`, `SRS.md`, `ARCHITECTURE.md`, `UI-DESIGN.md`, `AUTH.md`, `DEPLOY.md` — all at repo root,
cross-linked from README's documentation map. Keep them current with the code; a feature change that
contradicts SRS.md means SRS.md needs updating in the same change.

### 7. Security invariants

- Tokens: 256 bits from `crypto/rand`, **only the SHA-256 hash is stored**. Never persist a raw
  magic-link or session token.
- Ownership scoping goes **inside the SQL** (`WHERE ... AND owner_user_id = $n`), never as a
  separate check a caller could forget.
- Sign-in must not reveal whether an address has an account.
- All SQL uses bound parameters. No string-concatenated queries, ever.

### 8. Template gotcha

`html/template`'s `{{if}}` on a `*bool` tests non-nil only — it does **not** dereference. Any branch
on a `*bool` (e.g. `ValidationRun.OverallPassed`, nil while a run is in progress) must use the
`derefBool` template function. A bare `{{if}}` here once reported failing runs as "Compliant".

---

## Running it locally

`./run.sh` (Git Bash/macOS/Linux) or `run.cmd` (Windows) starts Postgres and Mailhog in Docker,
builds the binary, waits for `/healthz`, and opens http://localhost:8080. Magic-link emails land in
Mailhog at http://localhost:8025. Both scripts preflight-check `go`, `docker`, and `curl` and name
what's missing.

Go being absent from this machine means `run.sh`/`run.cmd` will stop at the preflight until it's
installed — that's the scripts working correctly, not a bug.

---

## CI

`.github/workflows/ci.yml` runs on every push and PR: `go mod download`, a `go mod tidy` diff check,
`gofmt -l`, `go vet`, `go build`, `go test`, and `docker build`. It must stay green. Before pushing,
run the container command at the top of this file — it covers everything except the tidy check and
the Docker build.
