package web

import (
	"context"
	"net/http"

	"github.com/bobbylon127/mrfsentinel/internal/auth"
	"github.com/bobbylon127/mrfsentinel/internal/store"
)

// userContextKey is an unexported type specifically so no other package
// can accidentally (or deliberately) collide with this context key — the
// same reason you'd make a context key a private, unexported constant in
// any Go codebase using context.Value. Go doesn't have Java's
// ThreadLocal/SecurityContextHolder for "the current request's
// principal"; a value threaded through context.Context, set once by
// middleware and read wherever it's needed downstream, is the idiomatic
// stand-in.
type userContextKey struct{}

// userFromContext reads back the store.User that requireAuth attached to a
// request's context, reporting false if there is none.
//
// Nothing calls this today. requireAuth also passes the user to its wrapped
// handler as an ordinary argument, which is simpler and checked by the
// compiler, so every handler in this package takes it that way instead.
// This is the accessor to use from code that can only see an http.Request
// and has no such argument; if no such caller has appeared by the time the
// next handler is written, it is dead code worth deleting.
func userFromContext(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userContextKey{}).(store.User)
	return u, ok
}

// requireAuth wraps a handler so it only ever runs for a signed-in
// request, resolving the session cookie to a store.User first and
// attaching it to the request's context. An unauthenticated request is
// redirected to /login rather than handed a 401 — this app has no API
// clients of its own to consider, only a browser following links, so a
// redirect is simply the more useful response.
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

		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		next(w, r.WithContext(ctx), user)
	}
}
