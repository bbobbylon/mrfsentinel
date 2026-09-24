package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"
)

// newTestStore connects to the Postgres named by DATABASE_URL, applies the
// migrations, and hands back a ready *Store.
//
// The unset and unreachable cases are deliberately treated differently. An
// unset DATABASE_URL means a developer is running `go test ./...` without a
// database, so these tests skip and the rest of the suite still runs. A
// DATABASE_URL that is set but cannot be reached is a hard failure instead:
// CI exports one pointing at its Postgres service container, and a skip
// there would silently retire this entire file the first time the service
// broke. README.md's verification table only means something if a green run
// really did exercise what it claims, which a quiet skip would not.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set — skipping tests that need a real Postgres")
	}

	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(DATABASE_URL) failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("DATABASE_URL is set but the database is unreachable: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	return NewStore(db)
}

// testContext returns a context bounded by the test's own deadline, so a
// query that hangs fails the test instead of the whole `go test` run.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// uniqueSuffix returns random hex, used to keep rows from different tests
// (and different runs against the same database) from colliding on the
// unique email column. Tests here share one Postgres and do not truncate
// between runs, so every identifier they insert has to be unique by
// construction.
func uniqueSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generating unique suffix: %v", err)
	}
	return hex.EncodeToString(b)
}

// newUser creates a throwaway account and returns it, which most tests here
// need only as an owner to hang other rows off.
func newUser(t *testing.T, s *Store) User {
	t.Helper()
	u, err := s.GetOrCreateUserByEmail(testContext(t), "test-"+uniqueSuffix(t)+"@example.test")
	if err != nil {
		t.Fatalf("GetOrCreateUserByEmail failed: %v", err)
	}
	return u
}

// TestMigrate_IsIdempotent is the property cmd/server/main.go depends on: it
// migrates on every single startup, so a second run against an
// already-migrated database must be a no-op rather than an error about a
// duplicate table.
func TestMigrate_IsIdempotent(t *testing.T) {
	s := newTestStore(t) // already migrated once
	ctx := testContext(t)

	if err := Migrate(ctx, s.db); err != nil {
		t.Fatalf("second Migrate failed, so restarting the server would crash: %v", err)
	}
	if err := Migrate(ctx, s.db); err != nil {
		t.Fatalf("third Migrate failed: %v", err)
	}
}

// TestGetOrCreateUserByEmail_IsStableForTheSameAddress covers the sign-in
// path's central assumption: requesting a second magic link must land on the
// existing account, not mint a duplicate one that would orphan the first
// user's hospitals.
func TestGetOrCreateUserByEmail_IsStableForTheSameAddress(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	email := "stable-" + uniqueSuffix(t) + "@example.test"

	first, err := s.GetOrCreateUserByEmail(ctx, email)
	if err != nil {
		t.Fatalf("first GetOrCreateUserByEmail failed: %v", err)
	}
	if first.ID == "" {
		t.Fatal("created user has an empty ID")
	}
	if first.Email != email {
		t.Errorf("created user email = %q, want %q", first.Email, email)
	}

	second, err := s.GetOrCreateUserByEmail(ctx, email)
	if err != nil {
		t.Fatalf("second GetOrCreateUserByEmail failed: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("same email produced two different users: %q then %q", first.ID, second.ID)
	}
}

// TestConsumeMagicLink_WorksExactlyOnce is the whole security property of a
// magic link. A link that could be consumed twice would stay a valid
// credential in the user's inbox (and in any mail relay's logs) forever
// after it was used.
func TestConsumeMagicLink_WorksExactlyOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	email := "magic-" + uniqueSuffix(t) + "@example.test"
	tokenHash := uniqueSuffix(t) + uniqueSuffix(t)

	if err := s.CreateMagicLink(ctx, tokenHash, email, time.Now().Add(15*time.Minute)); err != nil {
		t.Fatalf("CreateMagicLink failed: %v", err)
	}

	got, err := s.ConsumeMagicLink(ctx, tokenHash)
	if err != nil {
		t.Fatalf("first ConsumeMagicLink failed: %v", err)
	}
	if got != email {
		t.Errorf("ConsumeMagicLink returned %q, want %q", got, email)
	}

	if _, err := s.ConsumeMagicLink(ctx, tokenHash); !errors.Is(err, ErrNotFound) {
		t.Errorf("replaying a consumed magic link gave error %v, want ErrNotFound", err)
	}
}

// TestConsumeMagicLink_RejectsExpiredAndUnknown covers the other two ways a
// presented token must fail. Both have to be ErrNotFound specifically, since
// that is what internal/web turns into a generic "this link is no longer
// valid" rather than a message that distinguishes the cases for an attacker.
func TestConsumeMagicLink_RejectsExpiredAndUnknown(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)

	t.Run("expired", func(t *testing.T) {
		tokenHash := uniqueSuffix(t) + uniqueSuffix(t)
		email := "expired-" + uniqueSuffix(t) + "@example.test"
		if err := s.CreateMagicLink(ctx, tokenHash, email, time.Now().Add(-time.Minute)); err != nil {
			t.Fatalf("CreateMagicLink failed: %v", err)
		}
		if _, err := s.ConsumeMagicLink(ctx, tokenHash); !errors.Is(err, ErrNotFound) {
			t.Errorf("consuming an expired link gave error %v, want ErrNotFound", err)
		}
	})

	t.Run("never issued", func(t *testing.T) {
		if _, err := s.ConsumeMagicLink(ctx, uniqueSuffix(t)+uniqueSuffix(t)); !errors.Is(err, ErrNotFound) {
			t.Errorf("consuming an unknown token gave error %v, want ErrNotFound", err)
		}
	})
}

// TestSession_ResolvesThenDeletes walks one session's whole life: created,
// resolvable by its token hash, then gone after sign-out.
func TestSession_ResolvesThenDeletes(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	user := newUser(t, s)
	tokenHash := uniqueSuffix(t) + uniqueSuffix(t)

	if err := s.CreateSession(ctx, tokenHash, user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	got, err := s.UserBySessionToken(ctx, tokenHash)
	if err != nil {
		t.Fatalf("UserBySessionToken failed: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("session resolved to user %q, want %q", got.ID, user.ID)
	}

	if err := s.DeleteSession(ctx, tokenHash); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}
	if _, err := s.UserBySessionToken(ctx, tokenHash); !errors.Is(err, ErrNotFound) {
		t.Errorf("signed-out session still resolved (error was %v, want ErrNotFound)", err)
	}
}

// TestUserBySessionToken_RejectsExpired checks that expiry is enforced in
// the query rather than left to a cleanup job that may never run. The cookie
// carries its own expiry too, but that one is a client-side hint an attacker
// with a stolen token would simply ignore.
func TestUserBySessionToken_RejectsExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	user := newUser(t, s)
	tokenHash := uniqueSuffix(t) + uniqueSuffix(t)

	if err := s.CreateSession(ctx, tokenHash, user.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if _, err := s.UserBySessionToken(ctx, tokenHash); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired session resolved anyway (error was %v, want ErrNotFound)", err)
	}
}

// TestGetHospital_IsScopedToItsOwner is the test this file exists for.
//
// CLAUDE.md states the invariant plainly: ownership scoping goes inside the
// SQL, never as a separate check a caller could forget. This asserts the
// consequence — one signed-in user guessing another's hospital ID gets
// ErrNotFound, indistinguishable from an ID that never existed.
func TestGetHospital_IsScopedToItsOwner(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	owner := newUser(t, s)
	stranger := newUser(t, s)

	h, err := s.CreateHospital(ctx, owner.ID, "St. Example", "https://example.test/mrf.json")
	if err != nil {
		t.Fatalf("CreateHospital failed: %v", err)
	}

	if got, err := s.GetHospital(ctx, h.ID, owner.ID); err != nil {
		t.Fatalf("the owner could not read their own hospital: %v", err)
	} else if got.ID != h.ID {
		t.Errorf("GetHospital returned hospital %q, want %q", got.ID, h.ID)
	}

	if _, err := s.GetHospital(ctx, h.ID, stranger.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user read a hospital they do not own (error was %v, want ErrNotFound)", err)
	}
}

// TestListHospitalsByOwner_ExcludesOtherUsers is the list-shaped half of the
// same invariant: the dashboard must never show a row belonging to anyone
// else, whatever else is in the table.
func TestListHospitalsByOwner_ExcludesOtherUsers(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	owner := newUser(t, s)
	stranger := newUser(t, s)

	mine, err := s.CreateHospital(ctx, owner.ID, "Mine", "https://example.test/mine.json")
	if err != nil {
		t.Fatalf("CreateHospital failed: %v", err)
	}
	if _, err := s.CreateHospital(ctx, stranger.ID, "Theirs", "https://example.test/theirs.json"); err != nil {
		t.Fatalf("CreateHospital for the other user failed: %v", err)
	}

	list, err := s.ListHospitalsByOwner(ctx, owner.ID)
	if err != nil {
		t.Fatalf("ListHospitalsByOwner failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("owner sees %d hospitals, want exactly 1", len(list))
	}
	if list[0].ID != mine.ID {
		t.Errorf("owner sees hospital %q, want their own %q", list[0].ID, mine.ID)
	}
}

// TestGetRunReport_IsScopedToTheHospitalsOwner checks that the same scoping
// survives the join. A run is addressed by its own ID and reached through
// its hospital, so this is the one place the ownership filter could be
// dropped without any single-table query looking wrong.
func TestGetRunReport_IsScopedToTheHospitalsOwner(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	owner := newUser(t, s)
	stranger := newUser(t, s)

	h, err := s.CreateHospital(ctx, owner.ID, "St. Example", "https://example.test/mrf.json")
	if err != nil {
		t.Fatalf("CreateHospital failed: %v", err)
	}
	run, err := s.CreateValidationRun(ctx, h.ID)
	if err != nil {
		t.Fatalf("CreateValidationRun failed: %v", err)
	}
	if run.Status != RunPending {
		t.Errorf("new run status = %q, want %q", run.Status, RunPending)
	}
	if run.OverallPassed != nil {
		t.Error("a pending run already has an OverallPassed verdict, which should be nil until it finishes")
	}

	if _, err := s.GetRunReport(ctx, run.ID, owner.ID); err != nil {
		t.Fatalf("the owner could not read their own run report: %v", err)
	}
	if _, err := s.GetRunReport(ctx, run.ID, stranger.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user read a run report they do not own (error was %v, want ErrNotFound)", err)
	}
}

// TestSaveRunResult_PersistsChecklistResults covers the transactional write
// the report page reads back, including the text[] round-trip that
// SaveRunResult's own comment flags as a trap.
func TestSaveRunResult_PersistsChecklistResults(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	owner := newUser(t, s)

	h, err := s.CreateHospital(ctx, owner.ID, "St. Example", "https://example.test/mrf.json")
	if err != nil {
		t.Fatalf("CreateHospital failed: %v", err)
	}
	run, err := s.CreateValidationRun(ctx, h.ID)
	if err != nil {
		t.Fatalf("CreateValidationRun failed: %v", err)
	}

	hospitalChecks := []HospitalCheck{
		{RuleID: "H-1", Description: "Hospital name present", Passed: true, Detail: ""},
		{RuleID: "H-2", Description: "Affirmation present", Passed: false, Detail: "missing"},
	}
	itemChecks := []ItemCheck{
		// A nil SampleFailures is the trap: it must survive the text[]
		// column as an empty list rather than becoming a NULL that fails
		// to scan back.
		{RuleID: "I-1", Description: "Description present", RowsChecked: 10, RowsFailed: 0, SampleFailures: nil},
		{RuleID: "I-2", Description: "Gross charge present", RowsChecked: 10, RowsFailed: 2, SampleFailures: []string{"line 3", "line 7"}},
	}

	if err := s.SaveRunResult(ctx, run.ID, "json", 10, false, "", hospitalChecks, itemChecks); err != nil {
		t.Fatalf("SaveRunResult failed: %v", err)
	}

	report, err := s.GetRunReport(ctx, run.ID, owner.ID)
	if err != nil {
		t.Fatalf("GetRunReport failed: %v", err)
	}
	if report.Run.Status != RunSucceeded {
		t.Errorf("saved run status = %q, want %q", report.Run.Status, RunSucceeded)
	}
	if report.Run.OverallPassed == nil {
		t.Fatal("finished run still has a nil OverallPassed")
	}
	if *report.Run.OverallPassed {
		t.Error("run reports passing although it was saved as failing")
	}
	if report.Run.RowsProcessed != 10 {
		t.Errorf("RowsProcessed = %d, want 10", report.Run.RowsProcessed)
	}
	if report.Run.FinishedAt == nil {
		t.Error("finished run has no FinishedAt timestamp")
	}
	if len(report.HospitalChecks) != len(hospitalChecks) {
		t.Errorf("read back %d hospital checks, want %d", len(report.HospitalChecks), len(hospitalChecks))
	}
	if len(report.ItemChecks) != len(itemChecks) {
		t.Fatalf("read back %d item checks, want %d", len(report.ItemChecks), len(itemChecks))
	}

	for _, ic := range report.ItemChecks {
		if ic.RuleID == "I-2" && len(ic.SampleFailures) != 2 {
			t.Errorf("I-2 has %d sample failures, want 2", len(ic.SampleFailures))
		}
		if ic.RuleID == "I-1" && len(ic.SampleFailures) != 0 {
			t.Errorf("I-1 has %d sample failures, want 0 for a nil slice", len(ic.SampleFailures))
		}
	}
}

// TestMarkRunErrored_RecordsFailureWithoutChecklistResults covers the other
// terminal state: a file that could not be fetched at all, which finishes
// the run without ever producing checks to save.
func TestMarkRunErrored_RecordsFailureWithoutChecklistResults(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	owner := newUser(t, s)

	h, err := s.CreateHospital(ctx, owner.ID, "St. Example", "https://example.test/gone.json")
	if err != nil {
		t.Fatalf("CreateHospital failed: %v", err)
	}
	run, err := s.CreateValidationRun(ctx, h.ID)
	if err != nil {
		t.Fatalf("CreateValidationRun failed: %v", err)
	}

	if err := s.MarkRunRunning(ctx, run.ID); err != nil {
		t.Fatalf("MarkRunRunning failed: %v", err)
	}
	const msg = "fetching MRF: 404 Not Found"
	if err := s.MarkRunErrored(ctx, run.ID, msg); err != nil {
		t.Fatalf("MarkRunErrored failed: %v", err)
	}

	report, err := s.GetRunReport(ctx, run.ID, owner.ID)
	if err != nil {
		t.Fatalf("GetRunReport failed: %v", err)
	}
	if report.Run.Status != RunFailed {
		t.Errorf("run status = %q, want %q", report.Run.Status, RunFailed)
	}
	if report.Run.ErrorMessage != msg {
		t.Errorf("ErrorMessage = %q, want %q", report.Run.ErrorMessage, msg)
	}
	if report.Run.FinishedAt == nil {
		t.Error("errored run has no FinishedAt timestamp")
	}
	// A failed fetch produces no verdict at all — distinct from a verdict
	// of false, which would mean the file was read and found wanting.
	if report.Run.OverallPassed != nil {
		t.Error("errored run has a non-nil OverallPassed, which would render as a compliance verdict")
	}
}

// TestListRunsByHospital_ReturnsNewestFirst pins the ordering the hospital
// page relies on to put the most recent check at the top.
func TestListRunsByHospital_ReturnsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := testContext(t)
	owner := newUser(t, s)

	h, err := s.CreateHospital(ctx, owner.ID, "St. Example", "https://example.test/mrf.json")
	if err != nil {
		t.Fatalf("CreateHospital failed: %v", err)
	}

	var ids []string
	for i := 0; i < 3; i++ {
		run, err := s.CreateValidationRun(ctx, h.ID)
		if err != nil {
			t.Fatalf("CreateValidationRun %d failed: %v", i, err)
		}
		ids = append(ids, run.ID)
	}

	runs, err := s.ListRunsByHospital(ctx, h.ID)
	if err != nil {
		t.Fatalf("ListRunsByHospital failed: %v", err)
	}
	if len(runs) != len(ids) {
		t.Fatalf("got %d runs, want %d", len(runs), len(ids))
	}
	for i := 1; i < len(runs); i++ {
		if runs[i-1].StartedAt.Before(runs[i].StartedAt) {
			t.Errorf("runs are not newest-first: run %d started %v, before run %d at %v",
				i-1, runs[i-1].StartedAt, i, runs[i].StartedAt)
		}
	}
}
