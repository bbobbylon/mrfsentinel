package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
)

// CreateValidationRun records a new run in "pending" status — the moment a
// compliance officer clicks "check this hospital," before the background
// worker (internal/validation) has actually fetched anything. Splitting
// "recorded" from "finished" this way is what lets the dashboard show a
// run in progress rather than the officer staring at nothing until a
// multi-gigabyte file finishes downloading.
func (s *Store) CreateValidationRun(ctx context.Context, hospitalID string) (ValidationRun, error) {
	var r ValidationRun
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO validation_runs (hospital_id, status)
		VALUES ($1, $2)
		RETURNING id, hospital_id, status, format, rows_processed, overall_passed, parse_err, error_message, started_at, finished_at`,
		hospitalID, RunPending,
	).Scan(&r.ID, &r.HospitalID, &r.Status, &r.Format, &r.RowsProcessed, &r.OverallPassed, &r.ParseErr, &r.ErrorMessage, &r.StartedAt, &r.FinishedAt)
	if err != nil {
		return ValidationRun{}, fmt.Errorf("store: creating validation run: %w", err)
	}
	return r, nil
}

// MarkRunRunning flips a run from "pending" to "running" once the
// background worker has actually picked it up.
func (s *Store) MarkRunRunning(ctx context.Context, runID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE validation_runs SET status = $1 WHERE id = $2`, RunRunning, runID)
	if err != nil {
		return fmt.Errorf("store: marking run running: %w", err)
	}
	return nil
}

// MarkRunErrored records that a run failed before it could produce any
// checklist results at all (the MRF URL was unreachable, timed out, or
// exceeded the size limit) — distinct from a run that completed but found
// the file non-compliant, or one that parsed part of the file before
// hitting a corrupt row (see SaveRunResult's ParseErr handling, which still
// records whatever checklist results were gathered up to that point).
func (s *Store) MarkRunErrored(ctx context.Context, runID, errorMessage string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE validation_runs
		SET status = $1, error_message = $2, finished_at = now()
		WHERE id = $3`,
		RunFailed, errorMessage, runID,
	)
	if err != nil {
		return fmt.Errorf("store: marking run errored: %w", err)
	}
	return nil
}

// SaveRunResult persists a finished run's full checklist results in one
// transaction: the run's summary row, every hospital-level check, and
// every item-level rule's tally. All three or none — a dashboard page that
// found the run row but none of its checks would be a confusing half-saved
// state, which the transaction rules out.
func (s *Store) SaveRunResult(ctx context.Context, runID string, format string, rowsProcessed int64, overallPassed bool, parseErr string, hospitalChecks []HospitalCheck, itemChecks []ItemCheck) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: beginning transaction to save run result: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		UPDATE validation_runs
		SET status = $1, format = $2, rows_processed = $3, overall_passed = $4, parse_err = $5, finished_at = now()
		WHERE id = $6`,
		RunSucceeded, format, rowsProcessed, overallPassed, parseErr, runID,
	)
	if err != nil {
		return fmt.Errorf("store: updating validation run: %w", err)
	}

	for _, c := range hospitalChecks {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO validation_hospital_checks (run_id, rule_id, description, passed, detail)
			VALUES ($1, $2, $3, $4, $5)`,
			runID, c.RuleID, c.Description, c.Passed, c.Detail,
		)
		if err != nil {
			return fmt.Errorf("store: inserting hospital check %s: %w", c.RuleID, err)
		}
	}

	for _, c := range itemChecks {
		sampleFailures := c.SampleFailures
		if sampleFailures == nil {
			// pq.Array(nil []string) binds a SQL NULL, not an empty
			// array — found by actually running this against a real
			// Postgres instance during development, which is exactly the
			// kind of bug `go build` and `go vet` can't catch (both were
			// clean). sample_failures is NOT NULL, so a rule with zero
			// failures (a nil slice — nothing was ever appended to it)
			// failed this insert until this coerced nil to an explicit
			// empty slice, which pq.Array correctly encodes as '{}'.
			sampleFailures = []string{}
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO validation_item_checks (run_id, rule_id, description, rows_checked, rows_failed, sample_failures)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			runID, c.RuleID, c.Description, c.RowsChecked, c.RowsFailed, pq.Array(sampleFailures),
		)
		if err != nil {
			return fmt.Errorf("store: inserting item check %s: %w", c.RuleID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: committing run result: %w", err)
	}
	return nil
}

// ListRunsByHospital returns a hospital's run history, newest first, for
// the "outstanding vs. completed" style overview a compliance officer
// checks repeatedly rather than just once.
func (s *Store) ListRunsByHospital(ctx context.Context, hospitalID string) ([]ValidationRun, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, hospital_id, status, format, rows_processed, overall_passed, parse_err, error_message, started_at, finished_at
		FROM validation_runs
		WHERE hospital_id = $1
		ORDER BY started_at DESC`,
		hospitalID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: listing runs: %w", err)
	}
	defer rows.Close()

	var out []ValidationRun
	for rows.Next() {
		var r ValidationRun
		if err := rows.Scan(&r.ID, &r.HospitalID, &r.Status, &r.Format, &r.RowsProcessed, &r.OverallPassed, &r.ParseErr, &r.ErrorMessage, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, fmt.Errorf("store: scanning run row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading run rows: %w", err)
	}
	return out, nil
}

// GetRunReport returns one run plus its full checklist results — everything
// one report page needs, scoped through ownerUserID so a user can only ever
// fetch a report for a hospital they own.
func (s *Store) GetRunReport(ctx context.Context, runID, ownerUserID string) (RunReport, error) {
	var r ValidationRun
	err := s.db.QueryRowContext(ctx, `
		SELECT vr.id, vr.hospital_id, vr.status, vr.format, vr.rows_processed, vr.overall_passed, vr.parse_err, vr.error_message, vr.started_at, vr.finished_at
		FROM validation_runs vr
		JOIN hospitals h ON h.id = vr.hospital_id
		WHERE vr.id = $1 AND h.owner_user_id = $2`,
		runID, ownerUserID,
	).Scan(&r.ID, &r.HospitalID, &r.Status, &r.Format, &r.RowsProcessed, &r.OverallPassed, &r.ParseErr, &r.ErrorMessage, &r.StartedAt, &r.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RunReport{}, ErrNotFound
	}
	if err != nil {
		return RunReport{}, fmt.Errorf("store: getting run: %w", err)
	}

	hospitalChecks, err := s.hospitalChecksForRun(ctx, runID)
	if err != nil {
		return RunReport{}, err
	}
	itemChecks, err := s.itemChecksForRun(ctx, runID)
	if err != nil {
		return RunReport{}, err
	}

	return RunReport{Run: r, HospitalChecks: hospitalChecks, ItemChecks: itemChecks}, nil
}

func (s *Store) hospitalChecksForRun(ctx context.Context, runID string) ([]HospitalCheck, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rule_id, description, passed, detail
		FROM validation_hospital_checks
		WHERE run_id = $1
		ORDER BY id`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: listing hospital checks: %w", err)
	}
	defer rows.Close()

	var out []HospitalCheck
	for rows.Next() {
		var c HospitalCheck
		if err := rows.Scan(&c.RuleID, &c.Description, &c.Passed, &c.Detail); err != nil {
			return nil, fmt.Errorf("store: scanning hospital check row: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) itemChecksForRun(ctx context.Context, runID string) ([]ItemCheck, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rule_id, description, rows_checked, rows_failed, sample_failures
		FROM validation_item_checks
		WHERE run_id = $1
		ORDER BY id`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: listing item checks: %w", err)
	}
	defer rows.Close()

	var out []ItemCheck
	for rows.Next() {
		var c ItemCheck
		if err := rows.Scan(&c.RuleID, &c.Description, &c.RowsChecked, &c.RowsFailed, pq.Array(&c.SampleFailures)); err != nil {
			return nil, fmt.Errorf("store: scanning item check row: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
