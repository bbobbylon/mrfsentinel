package web

import (
	"net/http"

	"github.com/bobbylon127/mrfsentinel/internal/auth"
	"github.com/bobbylon127/mrfsentinel/internal/store"
)

// requireAuth wraps a handler so it only ever runs for a signed-in
// request, resolving the session cookie to a store.User and passing it on
// as an ordinary argument. An unauthenticated request is redirected to
// /login rather than handed a 401 — this app has no API clients of its
// own to consider, only a browser following links, so a redirect is
// simply the more useful response.
//
// The signed-in user is passed as a parameter rather than tucked into the
// request's context.Context. Go has no equivalent of Java's
// ThreadLocal/SecurityContextHolder for "the current request's principal,"
// and a context value is the usual stand-in — but it is only worth the
// indirection when the code that needs the value cannot be handed it
// directly. Here every handler can be, and a parameter is checked by the
// compiler where a context value is a runtime type assertion that can
// silently come back empty. This middleware did carry a context value in
// addition to the parameter for a while; nothing ever read it, so it was
// removed rather than left as a second, unverified source of truth.
func (h *Handlers) requireAuth(next func(w http.ResponseWriter, r *http.Request, user store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(h.cfg.SessionCookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		user, err := h.store.UserBySessionToken(r.Context(), auth.HashToken(cookie.Value))
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next(w, r, user)
	}
}
