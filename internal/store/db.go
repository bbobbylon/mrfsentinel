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

type User struct {
	ID        string
	Email     string
	CreatedAt time.Time
}

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

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

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

type HospitalCheck struct {
	RuleID      string
	Description string
	Passed      bool
	Detail      string
}

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
