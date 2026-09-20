package web

import (
	"io/fs"
	"net/http"
)

// NewRouter builds the whole app's routing table. This uses only
// net/http's standard ServeMux — no chi, no gorilla/mux, no third-party
// router at all. That's possible (and not a compromise) because Go 1.22
// added method- and wildcard-aware patterns directly to ServeMux
// ("GET /hospitals/{id}"), which used to be the entire reason people
// reached for a third-party router in the first place. A project this
// size genuinely doesn't need more routing power than that.
func NewRouter(h *Handlers) http.Handler {
	mux := http.NewServeMux()

	// Static assets (CSS/JS), served straight from the embedded
	// filesystem — no separate static-file server or CDN needed for an
	// app this size.
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: static assets missing from embedded build (this indicates a build/embed misconfiguration, not a runtime condition): " + err.Error())
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))

	mux.HandleFunc("GET /healthz", healthz)

	mux.HandleFunc("GET /login", h.LoginPage)
	mux.HandleFunc("POST /login", h.RequestMagicLink)
	mux.HandleFunc("GET /auth/callback", h.AuthCallback)
	mux.HandleFunc("POST /logout", h.Logout)

	mux.HandleFunc("GET /dashboard", h.requireAuth(h.Dashboard))
	mux.HandleFunc("POST /hospitals", h.requireAuth(h.CreateHospital))
	mux.HandleFunc("GET /hospitals/{id}", h.requireAuth(h.HospitalDetail))
	mux.HandleFunc("POST /hospitals/{id}/runs", h.requireAuth(h.TriggerRun))
	mux.HandleFunc("GET /runs/{id}", h.requireAuth(h.RunReport))
	mux.HandleFunc("GET /runs/{id}/status", h.requireAuth(h.RunStatus))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})

	return mux
}

// healthz backs both docker-compose.yml's HEALTHCHECK and run.sh/run.cmd's
// "is it actually up yet" poll — see those files. It deliberately does NOT
// check the database connection: a transient database hiccup shouldn't
// make an orchestrator conclude the whole process is dead and restart it,
// only that one request is currently failing (which the request itself
// will report). This is the same liveness-vs-readiness distinction Spring
// Boot Actuator draws with /actuator/health/liveness — this project's name
// for it is just shorter.
func healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
