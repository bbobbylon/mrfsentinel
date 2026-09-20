package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bobbylon127/mrfsentinel/internal/store"
)

type hospitalRow struct {
	store.Hospital
	LatestRun *store.ValidationRun // nil if this hospital has never been checked
}

type dashboardPageData struct {
	baseData
	Hospitals []hospitalRow
	Error     string
}

// Dashboard lists every hospital the signed-in user is tracking, each with
// its most recent run's status — the "outstanding vs. completed" overview
// a compliance officer checks repeatedly (see README.md's product
// description). Fetching each hospital's latest run separately (rather
// than one bigger join) is an N+1-query pattern that would matter at real
// scale; it doesn't here, because this app has no notion of a compliance
// officer tracking more than a modest handful of hospitals — see
// ARCHITECTURE.md.
func (h *Handlers) Dashboard(w http.ResponseWriter, r *http.Request, user store.User) {
	hospitals, err := h.store.ListHospitalsByOwner(r.Context(), user.ID)
	if err != nil {
		h.logger.Error("listing hospitals", "err", err)
		http.Error(w, "Something went wrong loading your dashboard.", http.StatusInternalServerError)
		return
	}

	rows := make([]hospitalRow, len(hospitals))
	for i, hosp := range hospitals {
		row := hospitalRow{Hospital: hosp}
		runs, err := h.store.ListRunsByHospital(r.Context(), hosp.ID)
		if err != nil {
			h.logger.Error("listing runs for dashboard", "hospital_id", hosp.ID, "err", err)
		} else if len(runs) > 0 {
			row.LatestRun = &runs[0]
		}
		rows[i] = row
	}

	h.tmpl.render(w, http.StatusOK, "dashboard.html", dashboardPageData{baseData: authedData(user), Hospitals: rows})
}

// CreateHospital adds a hospital to track and immediately kicks off its
// first validation run, so submitting the form takes an officer straight
// to a report in progress rather than an empty hospital page they'd have
// to remember to come back and run themselves.
func (h *Handlers) CreateHospital(w http.ResponseWriter, r *http.Request, user store.User) {
	name := strings.TrimSpace(r.FormValue("name"))
	mrfURL := strings.TrimSpace(r.FormValue("mrf_url"))
	if name == "" || mrfURL == "" {
		h.renderDashboardError(w, r, user, "Both a hospital name and its MRF URL are required.")
		return
	}
	if !strings.HasPrefix(mrfURL, "http://") && !strings.HasPrefix(mrfURL, "https://") {
		h.renderDashboardError(w, r, user, "The MRF URL must start with http:// or https://.")
		return
	}

	hospital, err := h.store.CreateHospital(r.Context(), user.ID, name, mrfURL)
	if err != nil {
		h.logger.Error("creating hospital", "err", err)
		h.renderDashboardError(w, r, user, "Something went wrong adding that hospital.")
		return
	}

	run, err := h.store.CreateValidationRun(r.Context(), hospital.ID)
	if err != nil {
		h.logger.Error("creating initial validation run", "err", err)
		// The hospital itself was saved successfully; a failure only to
		// kick off its first run isn't worth losing that, so this falls
		// through to the hospital page rather than erroring the whole
		// request — the officer can just click "run now" there.
	} else {
		h.worker.RunAsync(hospital, run.ID)
	}

	http.Redirect(w, r, "/hospitals/"+hospital.ID, http.StatusSeeOther)
}

func (h *Handlers) renderDashboardError(w http.ResponseWriter, r *http.Request, user store.User, message string) {
	hospitals, _ := h.store.ListHospitalsByOwner(r.Context(), user.ID)
	rows := make([]hospitalRow, len(hospitals))
	for i, hosp := range hospitals {
		rows[i] = hospitalRow{Hospital: hosp}
	}
	h.tmpl.render(w, http.StatusUnprocessableEntity, "dashboard.html", dashboardPageData{baseData: authedData(user), Hospitals: rows, Error: message})
}

type hospitalPageData struct {
	baseData
	Hospital store.Hospital
	Runs     []store.ValidationRun
}

// HospitalDetail shows one hospital's full run history and a "run it
// again" button.
func (h *Handlers) HospitalDetail(w http.ResponseWriter, r *http.Request, user store.User) {
	id := r.PathValue("id")
	hospital, err := h.store.GetHospital(r.Context(), id, user.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	runs, err := h.store.ListRunsByHospital(r.Context(), hospital.ID)
	if err != nil {
		h.logger.Error("listing runs", "hospital_id", hospital.ID, "err", err)
		http.Error(w, "Something went wrong loading this hospital's history.", http.StatusInternalServerError)
		return
	}
	h.tmpl.render(w, http.StatusOK, "hospital.html", hospitalPageData{baseData: authedData(user), Hospital: hospital, Runs: runs})
}

// TriggerRun starts a new validation run for an already-tracked hospital
// and sends the officer straight to that run's (initially still-in-
// progress) report page.
func (h *Handlers) TriggerRun(w http.ResponseWriter, r *http.Request, user store.User) {
	id := r.PathValue("id")
	hospital, err := h.store.GetHospital(r.Context(), id, user.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	run, err := h.store.CreateValidationRun(r.Context(), hospital.ID)
	if err != nil {
		h.logger.Error("creating validation run", "err", err)
		http.Error(w, "Something went wrong starting that run.", http.StatusInternalServerError)
		return
	}
	h.worker.RunAsync(hospital, run.ID)

	http.Redirect(w, r, "/runs/"+run.ID, http.StatusSeeOther)
}

type itemCheckView struct {
	store.ItemCheck
	ComplianceRate float64
	Passed         bool
}

type runPageData struct {
	baseData
	Report   store.RunReport
	Items    []itemCheckView
	InFlight bool // still pending/running — the page includes a small poller while true
}

// RunReport shows one run's full checklist results. While the run is still
// pending or running, the page instead shows a lightweight in-progress
// state and polls RunStatus in the background (see static/app.js) —
// there's deliberately no server-push here (no SSE, no WebSocket): a run
// finishes in anywhere from seconds to several minutes for the largest
// files, and a client polling every few seconds is simple, stateless, and
// plenty responsive at that timescale.
func (h *Handlers) RunReport(w http.ResponseWriter, r *http.Request, user store.User) {
	id := r.PathValue("id")
	report, err := h.store.GetRunReport(r.Context(), id, user.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	items := make([]itemCheckView, len(report.ItemChecks))
	for i, c := range report.ItemChecks {
		items[i] = itemCheckView{
			ItemCheck:      c,
			ComplianceRate: complianceRate(c),
			Passed:         c.RowsChecked > 0 && c.RowsFailed == 0,
		}
	}

	inFlight := report.Run.Status == store.RunPending || report.Run.Status == store.RunRunning

	h.tmpl.render(w, http.StatusOK, "run.html", runPageData{baseData: authedData(user), Report: report, Items: items, InFlight: inFlight})
}

// RunStatus is a tiny JSON endpoint the report page's poller hits — just
// enough for it to know when to stop polling and reload for the finished
// report, without re-sending the whole page on every tick.
func (h *Handlers) RunStatus(w http.ResponseWriter, r *http.Request, user store.User) {
	id := r.PathValue("id")
	report, err := h.store.GetRunReport(r.Context(), id, user.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": report.Run.Status,
	})
}

func complianceRate(c store.ItemCheck) float64 {
	if c.RowsChecked == 0 {
		return 0
	}
	return float64(c.RowsChecked-c.RowsFailed) / float64(c.RowsChecked)
}
