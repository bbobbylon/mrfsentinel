// Package validation wires internal/mrf (fetch + parse) and internal/rules
// (score against the CY2026 checklist) together into a background job: the
// thing that actually runs when a compliance officer clicks "check this
// hospital."
package validation

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/bobbylon127/mrfsentinel/internal/mrf"
	"github.com/bobbylon127/mrfsentinel/internal/rules"
	"github.com/bobbylon127/mrfsentinel/internal/store"
)

// Worker runs validation jobs. It holds no per-job state — every field
// here is shared, read-only configuration — so one Worker is created once
// in main() and reused for every run.
type Worker struct {
	store            *store.Store
	maxMRFBytes      int64
	fetchTimeout     time.Duration
	allowPrivateAddr bool
	logger           *slog.Logger
}

// NewWorker builds the single Worker that serves every validation run. It
// is called once, from cmd/server/main.go, and the result is handed to
// internal/web's Handlers — there is no per-run construction, because a
// Worker holds no per-run state (see the type's doc comment).
//
// allowPrivateAddr comes from config's AllowPrivateMRFAddresses and is
// passed straight through to mrf.Fetch on every run. It is a constructor
// parameter rather than something read from the environment down in
// internal/mrf so the setting stays visible at the one place that wires the
// app together — the same reason every other dependency here is an argument
// instead of a package-level global.
func NewWorker(st *store.Store, maxMRFBytes int64, fetchTimeout time.Duration, allowPrivateAddr bool, logger *slog.Logger) *Worker {
	return &Worker{
		store:            st,
		maxMRFBytes:      maxMRFBytes,
		fetchTimeout:     fetchTimeout,
		allowPrivateAddr: allowPrivateAddr,
		logger:           logger,
	}
}

// RunAsync starts validating hospital's MRF in a new goroutine and returns
// immediately. Progress and the eventual result land in the database as
// they happen (see internal/store's ValidationRun.Status), which is what
// the dashboard polls — see internal/web's run-status endpoint — rather
// than this function handing back a result directly.
//
// Honest scaling note: "start a goroutine" is this project's entire job
// queue. That's the right choice for an MVP running as one process; it's
// also the reason this app can only ever run as a single replica without a
// real queue (e.g. one backed by Postgres's own SKIP LOCKED, or an actual
// message broker) behind it eventually — see ARCHITECTURE.md's "Honest
// scoping" section.
func (w *Worker) RunAsync(hospital store.Hospital, runID string) {
	go w.run(hospital, runID)
}

// run is the body of one validation job, executed on its own goroutine by
// RunAsync: mark the run started, fetch and stream the hospital's MRF
// (internal/mrf), score it against the CY2026 checklist (internal/rules),
// translate that report into the store's row types, and persist the whole
// thing in one transaction.
//
// Every failure path writes something to the database rather than only
// logging it. The dashboard's sole window into this goroutine is the run's
// status column, so a job that died quietly would leave the page showing
// "Checking..." forever — which is exactly what happened before the
// save-failure branch at the bottom of this function existed.
func (w *Worker) run(hospital store.Hospital, runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), w.fetchTimeout)
	defer cancel()

	if err := w.store.MarkRunRunning(ctx, runID); err != nil {
		w.logger.Error("marking run running", "run_id", runID, "err", err)
	}

	src, closer, err := mrf.Fetch(ctx, hospital.MRFURL, w.maxMRFBytes, w.allowPrivateAddr)
	if err != nil {
		w.logger.Warn("fetching MRF failed", "hospital_id", hospital.ID, "url", hospital.MRFURL, "err", err)
		if markErr := w.store.MarkRunErrored(context.Background(), runID, publicFetchMessage(err)); markErr != nil {
			w.logger.Error("marking run errored", "run_id", runID, "err", markErr)
		}
		return
	}
	defer func() { _ = closer.Close() }()

	report := rules.Evaluate(src)

	hospitalChecks := make([]store.HospitalCheck, len(report.HospitalChecks))
	for i, c := range report.HospitalChecks {
		hospitalChecks[i] = store.HospitalCheck{RuleID: c.RuleID, Description: c.Description, Passed: c.Passed, Detail: c.Detail}
	}
	itemChecks := make([]store.ItemCheck, len(report.ItemChecks))
	for i, c := range report.ItemChecks {
		itemChecks[i] = store.ItemCheck{
			RuleID:         c.RuleID,
			Description:    c.Description,
			RowsChecked:    c.RowsChecked,
			RowsFailed:     c.RowsFailed,
			SampleFailures: c.SampleFailures,
		}
	}

	// A fresh context for the save: ctx above is scoped to the fetch
	// timeout, which may have nearly elapsed by the time a multi-gigabyte
	// file finishes streaming — the database write finishing shouldn't be
	// held hostage to how close that clock is to zero.
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer saveCancel()
	if err := w.store.SaveRunResult(saveCtx, runID, report.Format, report.RowsProcessed, report.OverallPassed(), report.ParseErr, hospitalChecks, itemChecks); err != nil {
		w.logger.Error("saving run result", "run_id", runID, "err", err)
		// Found by actually running this end-to-end: without this, a run
		// whose fetch and checklist scoring both succeeded but whose save
		// failed (a transient DB error, say) stayed stuck showing
		// "Checking…" forever — MarkRunRunning had already flipped its
		// status, and nothing ever flipped it back. Best-effort attempt to
		// at least record that it failed, so the dashboard shows an error
		// instead of a check that silently never finishes.
		if markErr := w.store.MarkRunErrored(context.Background(), runID, "saving the check results failed: "+err.Error()); markErr != nil {
			w.logger.Error("marking run errored after save failure", "run_id", runID, "err", markErr)
		}
	}
}

// publicFetchMessage picks the text that goes into the run's error_message
// column, which internal/web's run.html renders straight to the page.
//
// It is the boundary between what the log may say and what the user may see.
// internal/mrf hands back a *mrf.FetchError carrying both halves; everything
// else — an error type added later, a wrapped nil, anything unanticipated —
// falls through to a fixed string rather than to err.Error(). That default
// is the whole safety property: the old code interpolated err.Error()
// directly, so "connect: connection refused" from 10.0.0.5:22 was published
// on a page, and any new error path would have inherited the same leak
// automatically. Here a new error path has to opt in to being shown.
func publicFetchMessage(err error) string {
	var fetchErr *mrf.FetchError
	if errors.As(err, &fetchErr) && fetchErr.Public != "" {
		return fetchErr.Public
	}
	return "The MRF could not be downloaded or read."
}
