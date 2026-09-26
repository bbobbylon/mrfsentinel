package validation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bobbylon127/mrfsentinel/internal/store"
)

// sampleMRF is a small but fully compliant CY2026 JSON MRF: every
// hospital-level field the checklist looks for, one item carrying a complete
// set of charges, and a second item with none — the latter so a run over
// this file produces a mix of passing and failing item-level rules rather
// than a uniform result that would hide an ordering or tallying mistake.
const sampleMRF = `{
  "hospital_name": "Test General Hospital",
  "last_updated_on": "2026-06-01",
  "version": "3.0",
  "license_information": {"license_number": "12345", "state": "CA"},
  "type_2_npi": ["1234567890"],
  "attestation": {
    "attestation": "true, accurate, and complete",
    "confirm_attestation": true,
    "attester_name": "Jane Doe, CEO"
  },
  "standard_charge_information": [
    {
      "description": "Basic metabolic panel",
      "code_information": [{"code": "80048", "type": "CPT"}],
      "standard_charges": [
        {
          "gross_charge": 200.0,
          "discounted_cash": 150.0,
          "setting": "outpatient",
          "minimum": 50.0,
          "maximum": 300.0,
          "payers_information": [
            {
              "payer_name": "Acme Health",
              "plan_name": "PPO Gold",
              "standard_charge_dollar": 175.5,
              "median_amount": 170.0,
              "10th_percentile": 120.0,
              "90th_percentile": 250.0,
              "count": 42
            }
          ]
        }
      ]
    },
    {
      "description": "Missing everything item",
      "code_information": [],
      "standard_charges": []
    }
  ]
}`

// newTestStore mirrors the helper in internal/store's own tests: skip when
// no DATABASE_URL is configured, but fail hard when one is configured and
// unreachable, so a broken CI database can never quietly retire this file.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set — skipping tests that need a real Postgres")
	}

	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("DATABASE_URL is set but the database is unreachable: %v", err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("store.Migrate failed: %v", err)
	}
	return store.NewStore(db)
}

// newOwnedHospital creates a throwaway user and one hospital belonging to
// them, pointed at mrfURL. Every test here needs that pair before it has
// anything for the worker to validate.
func newOwnedHospital(t *testing.T, s *store.Store, mrfURL string) (store.User, store.Hospital) {
	t.Helper()
	ctx := context.Background()

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generating unique email suffix: %v", err)
	}

	user, err := s.GetOrCreateUserByEmail(ctx, "worker-"+hex.EncodeToString(b)+"@example.test")
	if err != nil {
		t.Fatalf("GetOrCreateUserByEmail failed: %v", err)
	}
	hospital, err := s.CreateHospital(ctx, user.ID, "Test General Hospital", mrfURL)
	if err != nil {
		t.Fatalf("CreateHospital failed: %v", err)
	}
	return user, hospital
}

// newTestWorker builds a Worker with generous limits and a silent logger.
// The logger is discarded rather than routed to t.Log because the worker
// logs from its own goroutine, which can outlive the test that started it.
//
// allowPrivateAddr is true here because every fixture below is served by
// httptest, which listens on 127.0.0.1 — production's dial-time filter (see
// internal/mrf/fetch.go) would refuse every one of them. That is the filter
// working, so the test that pins it, TestWorker_RefusesAPrivateAddress,
// builds its Worker directly with false rather than through this helper.
func newTestWorker(s *store.Store) *Worker {
	return NewWorker(s, 8<<20, 30*time.Second, true, slog.New(slog.DiscardHandler))
}

// awaitTerminalRun polls until the run reaches a terminal status, which is
// exactly what the browser does via internal/web's status endpoint.
//
// Polling is the honest way to test RunAsync: it hands back nothing to wait
// on, by design, because the database is the only channel between that
// goroutine and the rest of the app. A run that never reaches a terminal
// status fails here after the deadline — and that failure is the point, not
// an inconvenience, since "stuck on Checking… forever" is a bug this package
// has actually shipped before (see the save-failure branch in worker.go).
func awaitTerminalRun(t *testing.T, s *store.Store, runID, ownerUserID string) store.ValidationRun {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)

	for {
		report, err := s.GetRunReport(context.Background(), runID, ownerUserID)
		if err != nil {
			t.Fatalf("GetRunReport failed while waiting for the run: %v", err)
		}
		switch report.Run.Status {
		case store.RunSucceeded, store.RunFailed:
			return report.Run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never left status %q — it would show \"Checking…\" forever in the UI", runID, report.Run.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestWorker_ValidatesAndPersistsAFullRun is the end-to-end path: an HTTP
// URL serving a real MRF body, through fetch, streaming parse, checklist
// scoring, and into the database in the shape the report page reads back.
// Each of those pieces has its own unit tests; this is the one place they
// are exercised as the single pipeline a user actually triggers.
func TestWorker_ValidatesAndPersistsAFullRun(t *testing.T) {
	s := newTestStore(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, sampleMRF)
	}))
	t.Cleanup(srv.Close)

	user, hospital := newOwnedHospital(t, s, srv.URL+"/mrf.json")
	run, err := s.CreateValidationRun(context.Background(), hospital.ID)
	if err != nil {
		t.Fatalf("CreateValidationRun failed: %v", err)
	}

	newTestWorker(s).RunAsync(hospital, run.ID)
	finished := awaitTerminalRun(t, s, run.ID, user.ID)

	if finished.Status != store.RunSucceeded {
		t.Fatalf("run status = %q, want %q (error was %q)", finished.Status, store.RunSucceeded, finished.ErrorMessage)
	}
	if finished.Format != "json" {
		t.Errorf("detected format = %q, want %q", finished.Format, "json")
	}
	if finished.RowsProcessed != 2 {
		t.Errorf("RowsProcessed = %d, want 2 (the fixture has two items)", finished.RowsProcessed)
	}
	if finished.FinishedAt == nil {
		t.Error("a finished run has no FinishedAt timestamp")
	}
	if finished.OverallPassed == nil {
		t.Fatal("a finished run has a nil OverallPassed verdict")
	}

	report, err := s.GetRunReport(context.Background(), run.ID, user.ID)
	if err != nil {
		t.Fatalf("GetRunReport failed: %v", err)
	}
	if len(report.HospitalChecks) == 0 {
		t.Error("no hospital-level checks were persisted")
	}
	if len(report.ItemChecks) == 0 {
		t.Error("no item-level checks were persisted")
	}

	// The fixture's second item carries no codes and no charges, so at
	// least one item-level rule must have counted a failing row. A report
	// where everything passed would mean the empty item was silently
	// dropped somewhere in the pipeline rather than surfaced as a finding.
	var sawFailure bool
	for _, ic := range report.ItemChecks {
		if ic.RowsChecked == 0 {
			t.Errorf("item rule %s checked 0 rows", ic.RuleID)
		}
		if ic.RowsFailed > 0 {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Error("no item rule recorded a failing row, but the fixture contains an item with no codes or charges")
	}
}

// TestWorker_RecordsFetchFailure covers the branch that keeps a dead URL
// from hanging the UI. An unreachable MRF has to end the run as failed with
// a message on it, because the run's status column is the dashboard's only
// window into this goroutine.
func TestWorker_RecordsFetchFailure(t *testing.T) {
	s := newTestStore(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	user, hospital := newOwnedHospital(t, s, srv.URL+"/gone.json")
	run, err := s.CreateValidationRun(context.Background(), hospital.ID)
	if err != nil {
		t.Fatalf("CreateValidationRun failed: %v", err)
	}

	newTestWorker(s).RunAsync(hospital, run.ID)
	finished := awaitTerminalRun(t, s, run.ID, user.ID)

	if finished.Status != store.RunFailed {
		t.Fatalf("run status = %q, want %q for an unreachable MRF", finished.Status, store.RunFailed)
	}
	if finished.ErrorMessage == "" {
		t.Error("a failed run carries no error message, leaving the report page with nothing to show")
	}
	if finished.FinishedAt == nil {
		t.Error("a failed run has no FinishedAt timestamp")
	}
	// A fetch that never produced a file must not leave behind a compliance
	// verdict — nil is what distinguishes "could not check" from "checked
	// and found wanting".
	if finished.OverallPassed != nil {
		t.Error("a run that failed to fetch still recorded an OverallPassed verdict")
	}
}

// TestWorker_RecordsUnreachableHost is the same failure mode without a
// server at all, which is the more common real-world shape: a hospital takes
// its MRF offline, or the hostname stops resolving.
func TestWorker_RecordsUnreachableHost(t *testing.T) {
	s := newTestStore(t)

	// Reserved by RFC 6761 to never resolve, so this test needs no network.
	user, hospital := newOwnedHospital(t, s, "http://mrf.invalid/file.json")
	run, err := s.CreateValidationRun(context.Background(), hospital.ID)
	if err != nil {
		t.Fatalf("CreateValidationRun failed: %v", err)
	}

	newTestWorker(s).RunAsync(hospital, run.ID)
	finished := awaitTerminalRun(t, s, run.ID, user.ID)

	if finished.Status != store.RunFailed {
		t.Errorf("run status = %q, want %q for an unresolvable host", finished.Status, store.RunFailed)
	}
	if finished.ErrorMessage == "" {
		t.Error("a failed run carries no error message")
	}
}

// TestWorker_RefusesAPrivateAddress is the regression guard for the SSRF
// this package used to have: with the production setting, an MRF URL
// pointing at an address only this server can reach must fail without the
// request ever being made.
//
// The httptest server here stands in for anything on the internal network —
// a metadata endpoint, an admin port, a database's HTTP interface. It
// records whether it was reached, and that flag is the real assertion: a run
// marked failed proves the user saw an error, but only handlerRan proves no
// bytes were sent. The two are different outcomes, and an implementation
// that fetched first and complained afterwards would pass the first check.
func TestWorker_RefusesAPrivateAddress(t *testing.T) {
	s := newTestStore(t)

	// atomic because the handler and the assertion below are on different
	// goroutines — and if the filter ever regressed they would genuinely run
	// concurrently, which should fail this test rather than trip -race.
	var handlerRan atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan.Store(true)
		_, _ = io.WriteString(w, sampleMRF)
	}))
	t.Cleanup(srv.Close)

	user, hospital := newOwnedHospital(t, s, srv.URL+"/internal.json")
	run, err := s.CreateValidationRun(context.Background(), hospital.ID)
	if err != nil {
		t.Fatalf("CreateValidationRun failed: %v", err)
	}

	// false, unlike newTestWorker: this is the one test that wants the
	// production filter switched on.
	worker := NewWorker(s, 8<<20, 30*time.Second, false, slog.New(slog.DiscardHandler))
	worker.RunAsync(hospital, run.ID)
	finished := awaitTerminalRun(t, s, run.ID, user.ID)

	if handlerRan.Load() {
		t.Error("the loopback server was actually reached — the address filter did not stop the request")
	}
	if finished.Status != store.RunFailed {
		t.Errorf("run status = %q, want %q for a loopback MRF URL", finished.Status, store.RunFailed)
	}
	if finished.OverallPassed != nil {
		t.Error("a blocked run still recorded an OverallPassed verdict")
	}
}

// TestWorker_DoesNotLeakTransportErrorsToTheUser pins the other half of the
// same fix. Blocking the connection stops the request; it does not by itself
// stop the *answer* leaking, because the stored error_message is rendered on
// the run page. If a refused connection and a blocked address produced
// visibly different text, that page would still report on the internal
// network — slower than a real port scan, but just as conclusive.
//
// Both URLs below therefore have to produce the same message, and neither
// may name an address, a port, or a Go network error.
func TestWorker_DoesNotLeakTransportErrorsToTheUser(t *testing.T) {
	s := newTestStore(t)

	// Started and immediately closed, so the port is allocated but nothing
	// is listening: this is the "connection refused" case.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	// Still running, so this one is refused by the address filter rather
	// than by the network.
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, sampleMRF)
	}))
	t.Cleanup(blocked.Close)

	// The two cases need opposite settings to reach opposite code paths.
	// "refused" has to actually dial to be refused, and the filter would
	// otherwise intercept a loopback address before the network saw it — so
	// it runs with the filter off, which is the only way to get a genuine
	// transport error out of a machine with no public host that refuses
	// connections. "blocked" runs with the filter on.
	cases := []struct {
		name         string
		url          string
		allowPrivate bool
	}{
		{"refused", closedURL, true},
		{"blocked", blocked.URL, false},
	}

	messages := make(map[string]string, len(cases))
	for _, tc := range cases {
		name := tc.name
		user, hospital := newOwnedHospital(t, s, tc.url+"/mrf.json")
		run, err := s.CreateValidationRun(context.Background(), hospital.ID)
		if err != nil {
			t.Fatalf("CreateValidationRun failed: %v", err)
		}

		NewWorker(s, 8<<20, 30*time.Second, tc.allowPrivate, slog.New(slog.DiscardHandler)).RunAsync(hospital, run.ID)
		finished := awaitTerminalRun(t, s, run.ID, user.ID)

		if finished.Status != store.RunFailed {
			t.Fatalf("%s: run status = %q, want %q", name, finished.Status, store.RunFailed)
		}
		if finished.ErrorMessage == "" {
			t.Fatalf("%s: a failed run carries no error message, leaving the report page with nothing to show", name)
		}
		for _, leak := range []string{"127.0.0.1", "[::1]", "dial tcp", "connection refused", "connectex"} {
			if strings.Contains(strings.ToLower(finished.ErrorMessage), strings.ToLower(leak)) {
				t.Errorf("%s: error message shown to the user contains %q: %q", name, leak, finished.ErrorMessage)
			}
		}
		messages[name] = finished.ErrorMessage
	}

	if messages["refused"] != messages["blocked"] {
		t.Errorf("a refused connection and a blocked address produce different messages, which tells the user which internal hosts are listening:\n refused: %q\n blocked: %q",
			messages["refused"], messages["blocked"])
	}
}
