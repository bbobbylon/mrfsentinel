package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bobbylon127/mrfsentinel/internal/auth"
	"github.com/bobbylon127/mrfsentinel/internal/config"
	"github.com/bobbylon127/mrfsentinel/internal/store"
)

// bptr takes the address of a bool literal, which Go does not allow inline.
// store.ValidationRun.OverallPassed is a *bool so that "still running" stays
// distinguishable from "finished and failed", and these tests need to build
// all three states by hand.
func bptr(b bool) *bool { return &b }

// newTestHandlers builds a Handlers with a nil store and a discarding
// logger.
//
// A nil store is safe for everything exercised below because none of these
// tests reach a handler that touches the database: requireAuth checks for a
// session cookie and redirects before it ever calls the store, and the
// template tests render page data built by hand. A test that did reach the
// store would panic loudly rather than pass on a lie, which is the behavior
// to want from a stand-in this blunt.
func newTestHandlers(t *testing.T) *Handlers {
	t.Helper()
	h, err := NewHandlers(
		nil,
		auth.Mailer{},
		nil,
		config.Config{SessionCookieName: "mrfsentinel_session"},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("NewHandlers failed: %v", err)
	}
	return h
}

// renderRunPage renders run.html with the supplied page data and returns the
// HTML. It goes through templates.render rather than executing the template
// directly, so the buffering and header behavior the real handlers rely on
// is on the tested path too.
func renderRunPage(t *testing.T, data runPageData) string {
	t.Helper()
	h := newTestHandlers(t)
	rec := httptest.NewRecorder()
	h.tmpl.render(rec, http.StatusOK, "run.html", data)

	if rec.Code != http.StatusOK {
		t.Fatalf("rendering run.html gave status %d, body: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// succeededRunPage builds the page data for a finished run with the given
// verdict — the state in which the compliance banner is chosen.
func succeededRunPage(overallPassed *bool) runPageData {
	return runPageData{
		Report: store.RunReport{
			Run: store.ValidationRun{
				ID:            "run-1",
				HospitalID:    "hospital-1",
				Status:        store.RunSucceeded,
				Format:        "json",
				RowsProcessed: 10,
				OverallPassed: overallPassed,
			},
		},
	}
}

// TestLoadTemplates_AllPagesParse is the startup guard NewHandlers depends
// on. A malformed template is meant to kill the process immediately rather
// than 500 the first request unlucky enough to render it, which only works
// if this returns an error — so it is worth asserting that it does not
// return one for the templates actually shipped.
func TestLoadTemplates_AllPagesParse(t *testing.T) {
	tmpl, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates failed: %v", err)
	}
	for _, page := range pageFiles {
		if _, ok := tmpl.pages[page]; !ok {
			t.Errorf("template %q is listed in pageFiles but missing from the parsed set", page)
		}
	}
}

// TestDerefBool covers the template function directly. The bug it exists to
// prevent is subtle enough to be worth pinning at this level as well as
// through a rendered page: html/template's {{if}} on a pointer tests only
// non-nil, so a *bool pointing at false is truthy to a bare {{if}}.
func TestDerefBool(t *testing.T) {
	derefBool, ok := templateFuncs["derefBool"].(func(*bool) bool)
	if !ok {
		t.Fatal("derefBool is missing from templateFuncs or has changed signature")
	}

	if derefBool(nil) {
		t.Error("derefBool(nil) = true, want false")
	}
	if derefBool(bptr(false)) {
		t.Error("derefBool(&false) = true, want false — this is the exact bug the function exists to prevent")
	}
	if !derefBool(bptr(true)) {
		t.Error("derefBool(&true) = false, want true")
	}
}

// TestRunPage_VerdictBanner is the regression test for the bug recorded in
// ARCHITECTURE.md and in templates.go: a run that failed its checklist once
// rendered a "passed" banner, because {{if}} on a non-nil *bool is true
// whatever it points at. go vet cannot catch that — both sides type-check —
// so the only guard is rendering the page and reading what it says.
func TestRunPage_VerdictBanner(t *testing.T) {
	const passBanner = "Every checklist rule passed."
	const failBanner = "compliance issues"

	t.Run("passing run shows the pass banner", func(t *testing.T) {
		html := renderRunPage(t, succeededRunPage(bptr(true)))
		if !strings.Contains(html, passBanner) {
			t.Errorf("a passing run did not render %q", passBanner)
		}
	})

	t.Run("failing run must not claim it passed", func(t *testing.T) {
		html := renderRunPage(t, succeededRunPage(bptr(false)))
		if strings.Contains(html, passBanner) {
			t.Errorf("a FAILING run rendered %q — the derefBool bug is back", passBanner)
		}
		if !strings.Contains(html, failBanner) {
			t.Errorf("a failing run did not render a failure banner containing %q", failBanner)
		}
	})

	t.Run("run with no verdict must not claim it passed", func(t *testing.T) {
		html := renderRunPage(t, succeededRunPage(nil))
		if strings.Contains(html, passBanner) {
			t.Errorf("a run with a nil verdict rendered %q", passBanner)
		}
	})
}

// TestRunPage_InFlightBannerIsALiveRegion guards the accessibility fix for
// gap 2 in UI-DESIGN.md §5.4. app.js writes a completion message into
// #in-flight-message and relies on the surrounding role="status" to have it
// announced; losing either attribute silently returns the page to reloading
// with no indication to a screen reader that anything happened.
func TestRunPage_InFlightBannerIsALiveRegion(t *testing.T) {
	data := succeededRunPage(nil)
	data.Report.Run.Status = store.RunRunning
	data.InFlight = true

	html := renderRunPage(t, data)

	if !strings.Contains(html, `role="status"`) {
		t.Error(`the in-flight banner lost role="status", so completion is announced to nobody`)
	}
	if !strings.Contains(html, `id="in-flight-message"`) {
		t.Error(`#in-flight-message is missing — app.js has nothing to announce into`)
	}
}

// TestLayout_HasSkipLinkAndMainTarget guards the accessibility fix for gap 1
// in UI-DESIGN.md §5.4. The two halves only work together: the link needs
// its target, and the target needs tabindex="-1" or focus stays in the
// header after the jump.
func TestLayout_HasSkipLinkAndMainTarget(t *testing.T) {
	html := renderRunPage(t, succeededRunPage(bptr(true)))

	if !strings.Contains(html, `href="#main-content"`) {
		t.Error("the skip-to-content link is missing from the layout")
	}
	if !strings.Contains(html, `id="main-content"`) {
		t.Error("the skip link's target is missing from <main>")
	}
	if !strings.Contains(html, `tabindex="-1"`) {
		t.Error(`<main> lost tabindex="-1", so the skip link moves the scroll position but not focus`)
	}
}

// TestTables_HeaderCellsAreScoped guards the accessibility fix for gap 3 in
// UI-DESIGN.md §5.4 across every rendered table: no bare <th> should survive
// anywhere, since an unscoped header cell leaves a screen reader guessing
// which cells it labels.
func TestTables_HeaderCellsAreScoped(t *testing.T) {
	html := renderRunPage(t, succeededRunPage(bptr(false)))

	if strings.Contains(html, "<th>") {
		t.Error(`the run report renders a bare <th> — every header cell needs scope="col"`)
	}
	if !strings.Contains(html, `<th scope="col">`) {
		t.Error(`no scoped header cells found, so this test is no longer checking anything`)
	}
}

// TestRender_UnknownPageIs500 covers render's own guard rather than any
// page. Asking for a template that does not exist is a programming mistake,
// and it should surface as a server error rather than an empty 200.
func TestRender_UnknownPageIs500(t *testing.T) {
	h := newTestHandlers(t)
	rec := httptest.NewRecorder()

	h.tmpl.render(rec, http.StatusOK, "no_such_page.html", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("rendering an unknown template gave status %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// TestRender_SetsContentType checks that rendered pages are served as HTML.
// Without the header a browser may sniff the content type, which is both a
// rendering hazard and a mild security one.
func TestRender_SetsContentType(t *testing.T) {
	h := newTestHandlers(t)
	rec := httptest.NewRecorder()

	h.tmpl.render(rec, http.StatusOK, "login.html", loginPageData{})

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

// TestHealthz covers the endpoint docker-compose.yml's HEALTHCHECK and both
// run scripts poll. It must answer 200 without consulting the database — the
// liveness-versus-readiness distinction router.go explains — which is
// implicitly asserted here by the store being nil.
func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz gave status %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "ok" {
		t.Errorf("GET /healthz body = %q, want %q", body, "ok")
	}
}

// TestProtectedRoutes_RedirectWhenSignedOut is the authorization gate seen
// from outside. Every route wrapped in requireAuth must send an anonymous
// visitor to /login rather than rendering anything, and this covers the
// whole list at once so a route added to router.go without the wrapper is
// caught here.
func TestProtectedRoutes_RedirectWhenSignedOut(t *testing.T) {
	router := NewRouter(newTestHandlers(t))

	protected := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/dashboard"},
		{http.MethodPost, "/hospitals"},
		{http.MethodGet, "/hospitals/some-id"},
		{http.MethodPost, "/hospitals/some-id/runs"},
		{http.MethodGet, "/runs/some-id"},
		{http.MethodGet, "/runs/some-id/status"},
	}

	for _, route := range protected {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status %d, want %d (a redirect to the login page)", rec.Code, http.StatusSeeOther)
			}
			if loc := rec.Header().Get("Location"); loc != "/login" {
				t.Errorf("redirected to %q, want %q", loc, "/login")
			}
		})
	}
}

// TestProtectedRoute_RejectsUnknownSessionCookie covers the second half of
// the gate: presenting a cookie is not the same as presenting a valid one.
// The store is nil here, so reaching the lookup at all would panic — which
// is why this asserts only on requests that carry no usable session.
func TestProtectedRoute_RejectsEmptySessionCookie(t *testing.T) {
	router := NewRouter(newTestHandlers(t))

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "some_other_cookie", Value: "irrelevant"})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("a request with no session cookie gave status %d, want a redirect", rec.Code)
	}
}

// TestRootRedirectsToDashboard covers the one convenience route, which
// exists so the bare domain lands somewhere useful instead of 404ing.
func TestRootRedirectsToDashboard(t *testing.T) {
	router := NewRouter(newTestHandlers(t))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET / gave status %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard" {
		t.Errorf("GET / redirected to %q, want %q", loc, "/dashboard")
	}
}

// TestStaticAssetsAreEmbedded checks that the CSS and JS really are compiled
// into the binary. This is the property the whole single-binary deploy story
// rests on: if go:embed stopped picking these up, the app would still build
// and start, and only look broken in a browser.
func TestStaticAssetsAreEmbedded(t *testing.T) {
	router := NewRouter(newTestHandlers(t))

	for _, path := range []string{"/static/style.css", "/static/app.js"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s gave status %d, want 200", path, rec.Code)
			}
			if rec.Body.Len() == 0 {
				t.Errorf("GET %s served an empty body", path)
			}
		})
	}
}

// TestLoginPageIsPublic confirms the sign-in page itself is not behind the
// auth gate, which would be an unbreakable loop.
func TestLoginPageIsPublic(t *testing.T) {
	router := NewRouter(newTestHandlers(t))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET /login gave status %d, want 200", rec.Code)
	}
}
