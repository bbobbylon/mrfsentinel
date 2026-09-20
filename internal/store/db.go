package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq" // registers the "postgres" driver with database/sql; never called directly, hence the blank import
)

// Open connects to Postgres and configures the connection pool.
// database/sql already pools connections for you — there's no separate
// pool object to construct, the way Spring wires a HikariCP DataSource
// bean; *sql.DB itself IS the pool, and these two calls just size it.
func Open(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: opening database: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

// Store wraps a *sql.DB with every query this app needs, grouped by
// subject across this file, users.go, hospitals.go and runs.go. Passing a
// *Store around (see cmd/server/main.go) is this project's version of
// injecting a repository bean — done by hand, as a plain constructor
// argument, rather than by a framework scanning for annotations.
type Store struct {
	db *sql.DB
}

// NewStore wraps an already-open *sql.DB. It does not open or migrate the
// database itself — see Open and Migrate — so construction order in main()
// is explicit: Open, then Migrate, then NewStore.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// User, Hospital, and the validation-run types below are this package's
// public domain model — what every repository-style method in this
// package returns or accepts. They deliberately have no database/sql tags
// or ORM annotations; each method below spells out its own column mapping,
// which is more typing than a Spring Data JPA @Entity but means there is
// never a mismatch between what a struct field is named and what a query
// actually selects.

// User is a signed-in account, which in this app is nothing more than a
// verified email address — there is no password hash, profile, or role
// here, because magic-link auth needs none of them (see internal/auth).
type User struct {
	ID        string
	Email     string
	CreatedAt time.Time
}

// Hospital is one tracked hospital: a display name and the public URL of
// its published MRF, owned by the user who added it. OwnerUserID is the
// only access control this app has — every query that reads a hospital or
// anything beneath it filters on it (see GetHospital and GetRunReport).
type Hospital struct {
	ID          string
	OwnerUserID string
	Name        string
	MRFURL      string
	CreatedAt   time.Time
}

// RunStatus mirrors the validation_status Postgres enum created in
// migrations/0001_init.sql.
type RunStatus string

// A run's lifecycle: created pending, flipped to running when the worker
// picks it up, and ending as either succeeded (the file was checked,
// whatever the checklist concluded) or failed (the file could not be
// fetched or saved at all). Note that "succeeded" says the check ran, not
// that the hospital passed it — that answer is ValidationRun.OverallPassed.
const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

// ValidationRun is one check of one hospital's MRF, from the moment it is
// queued to whatever it eventually concluded. The pointer fields are the
// ones that are genuinely unknown until it finishes, rather than zero: see
// the bug described in ARCHITECTURE.md for what happened when a template
// treated a non-nil *bool as "passed" without dereferencing it.
type ValidationRun struct {
	ID            string
	HospitalID    string
	Status        RunStatus
	Format        string
	RowsProcessed int64
	OverallPassed *bool // nil until the run finishes
	ParseErr      string
	ErrorMessage  string
	StartedAt     time.Time
	FinishedAt    *time.Time
}

// HospitalCheck is one stored hospital-level checklist result — the
// persisted form of rules.CheckResult, kept as a separate type so the
// database schema and the checklist engine can change independently.
type HospitalCheck struct {
	RuleID      string
	Description string
	Passed      bool
	Detail      string
}

// ItemCheck is one stored item-level rule's tally across the whole file —
// the persisted form of rules.ItemRuleSummary. SampleFailures round-trips
// through a Postgres text[] column; see SaveRunResult for the nil-slice
// trap that lives on that boundary.
type ItemCheck struct {
	RuleID         string
	Description    string
	RowsChecked    int64
	RowsFailed     int64
	SampleFailures []string
}

// RunReport bundles a run with its checklist results, exactly what one page
// of the dashboard needs to render a full compliance report.
type RunReport struct {
	Run            ValidationRun
	HospitalChecks []HospitalCheck
	ItemChecks     []ItemCheck
}
