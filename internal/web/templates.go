package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
)

// pageFiles lists every top-level page template. Each one is parsed
// together with layout.html into its OWN *template.Template instance (see
// loadTemplates) — a deliberate choice, not an oversight: if all pages were
// parsed into a single shared template set, every page's {{define
// "content"}} block would collide with every other page's, since
// html/template's define blocks share one namespace per *template.Template.
// Keeping one instance per page sidesteps that entirely.
var pageFiles = []string{
	"login.html",
	"check_email.html",
	"dashboard.html",
	"hospital.html",
	"run.html",
}

var templateFuncs = template.FuncMap{
	"percent": func(rate float64) string { return fmt.Sprintf("%.0f%%", rate*100) },
	// derefBool exists because html/template's {{if}} does NOT dereference
	// a pointer to check what it points to — for reflect.Ptr, its truth
	// test is only "is this pointer non-nil," so {{if .SomeBoolPointer}}
	// is true for ANY non-nil *bool, including one pointing at false. This
	// was found by actually running the app end-to-end against a real
	// database (a run that failed its checklist still showed a "passed"
	// banner) — `go vet` has no way to catch it, since both sides of that
	// expression type-check fine. Every template that branches on
	// store.ValidationRun.OverallPassed (a *bool, nil while a run is still
	// in progress) uses this function instead of a bare {{if}}.
	"derefBool": func(b *bool) bool { return b != nil && *b },
}

type templates struct {
	pages map[string]*template.Template
}

func loadTemplates() (*templates, error) {
	t := &templates{pages: make(map[string]*template.Template, len(pageFiles))}
	for _, page := range pageFiles {
		tmpl, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(templateFS, "templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, fmt.Errorf("web: parsing template %s: %w", page, err)
		}
		t.pages[page] = tmpl
	}
	return t, nil
}

// render executes page's layout+content into a buffer first, and only then
// writes it to w — so a template error partway through never leaves the
// client with a half-sent page and a 200 status it can't trust. This is
// the render-time equivalent of building a whole response object before
// calling ResponseEntity.ok(...) in a Spring @RestController, rather than
// streaming pieces of it out as they're computed.
func (t *templates) render(w http.ResponseWriter, status int, page string, data any) {
	tmpl, ok := t.pages[page]
	if !ok {
		http.Error(w, fmt.Sprintf("web: no such template %q", page), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		http.Error(w, "web: rendering page: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
