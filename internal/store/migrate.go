// Package store is MRF Sentinel's persistence layer: opening the database
// connection, applying schema migrations, and every query the rest of the
// app needs — the Go-idiom equivalent of a Spring Data JPA repository
// layer, except there's no ORM generating SQL for you. Queries here are
// plain SQL strings passed to database/sql, which is the normal, boring way
// to do this in Go; the trade-off for skipping an ORM is that every query
// is visible and reviewable in this package, at the cost of writing them by
// hand.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

// migrationFS holds the schema migrations, compiled into the binary so a
// deployed container carries its own schema and needs no migrations/
// directory mounted beside it — the same reasoning as internal/web's
// embedded templates. Migrate below reads them from here.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies every embedded .sql file under migrations/, in filename
// order, that hasn't already been applied — tracking progress in a
// schema_migrations table it creates itself on first run. Each file runs
// inside its own transaction, so a failing migration doesn't leave the
// schema half-changed.
//
// This is a small hand-rolled alternative to a tool like golang-migrate.
// That wasn't a style preference: golang-migrate's latest release (and the
// latest compatible Postgres driver, pgx) both require a newer Go toolchain
// than this project's sandbox could reach (see ARCHITECTURE.md's "Honest
// scoping" section for the exact version wall hit while researching this).
// For a project this size, "read embedded SQL files in order and apply the
// ones not yet recorded" is the entire feature such a tool provides —
// worth under 60 lines of stdlib code here rather than a dependency this
// project couldn't even verify would build.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("store: creating schema_migrations table: %w", err)
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: reading embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // filenames are zero-padded (0001_, 0002_, ...) precisely so lexical sort is also application order

	for _, name := range names {
		applied, err := migrationAlreadyApplied(ctx, db, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, db, name); err != nil {
			return err
		}
	}

	return nil
}

// migrationAlreadyApplied reports whether a migration file has been
// recorded in schema_migrations, which is what makes Migrate idempotent and
// therefore safe to run on every single startup (see cmd/server/main.go,
// which does exactly that).
func migrationAlreadyApplied(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)`, name,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: checking whether migration %s was already applied: %w", name, err)
	}
	return exists, nil
}

// applyMigration runs one migration file and records it as applied, both
// inside a single transaction. Coupling those two writes is the point: if
// the schema change committed but the bookkeeping row did not, the next
// startup would try to apply it again against a database that already has
// it, and fail on something like a duplicate column.
//
// Postgres supports transactional DDL, which is what makes this possible —
// the same migration strategy would not be safe on MySQL, where a DDL
// statement commits implicitly.
func applyMigration(ctx context.Context, db *sql.DB, name string) error {
	sqlBytes, err := migrationFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("store: reading migration %s: %w", name, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: beginning transaction for migration %s: %w", name, err)
	}
	// A deferred Rollback after a successful Commit is a documented no-op
	// in database/sql (it returns sql.ErrTxDone, which is safe to ignore)
	// — this is the idiomatic Go equivalent of a Java try-with-resources
	// block: "clean up no matter how this function returns," expressed
	// without needing every return path to remember to call Rollback
	// itself.
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("store: applying migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("store: recording migration %s as applied: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: committing migration %s: %w", name, err)
	}
	return nil
}
