package web

import (
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/bobbylon127/mrfsentinel/internal/auth"
)

// loginPageData backs login.html. Error is non-empty only when the page is
// being re-rendered after a failed attempt — a malformed email address, or
// a magic link that was expired or already used — so that the template can
// show the message inline instead of bouncing the user to a separate error
// page.
type loginPageData struct {
	baseData
	Error string
}

// LoginPage shows the "sign in with your email" form.
func (h *Handlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.tmpl.render(w, http.StatusOK, "login.html", loginPageData{})
}

// RequestMagicLink handles the login form's submission: validates the
// email address is at least well-formed, issues a magic-link token, emails
// it, and shows a "check your email" page. It always shows that same
// confirmation regardless of whether the email belongs to an existing
// account — GetOrCreateUserByEmail creates one on the fly if not — since
// there's no meaningful difference in this app between "sign up" and "sign
// in," and revealing which emails already have accounts would be a minor
// but needless information leak.
func (h *Handlers) RequestMagicLink(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	if _, err := mail.ParseAddress(email); err != nil {
		h.tmpl.render(w, http.StatusUnprocessableEntity, "login.html", loginPageData{Error: "That doesn't look like a valid email address."})
		return
	}

	rawToken, tokenHash, err := auth.NewToken()
	if err != nil {
		h.logger.Error("generating magic link token", "err", err)
		http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	if err := h.store.CreateMagicLink(r.Context(), tokenHash, email, expiresAt); err != nil {
		h.logger.Error("creating magic link", "err", err)
		http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
		return
	}

	link := fmt.Sprintf("%s/auth/callback?token=%s", strings.TrimRight(h.cfg.PublicBaseURL, "/"), rawToken)
	if err := h.mailer.SendMagicLink(email, link); err != nil {
		h.logger.Error("sending magic link email", "err", err)
		http.Error(w, "We couldn't send that email. Please try again in a moment.", http.StatusInternalServerError)
		return
	}

	h.tmpl.render(w, http.StatusOK, "check_email.html", baseData{})
}

// AuthCallback is what a clicked magic link hits: it consumes the token
// (exactly once — see store.ConsumeMagicLink), creates a session, and
// signs the browser in.
func (h *Handlers) AuthCallback(w http.ResponseWriter, r *http.Request) {
	rawToken := r.URL.Query().Get("token")
	if rawToken == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	email, err := h.store.ConsumeMagicLink(r.Context(), auth.HashToken(rawToken))
	if err != nil {
		h.tmpl.render(w, http.StatusUnauthorized, "login.html", loginPageData{
			Error: "That sign-in link is invalid, expired, or already used. Request a new one below.",
		})
		return
	}

	user, err := h.store.GetOrCreateUserByEmail(r.Context(), email)
	if err != nil {
		h.logger.Error("resolving user after magic link", "err", err)
		http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
		return
	}

	sessionToken, sessionHash, err := auth.NewToken()
	if err != nil {
		h.logger.Error("generating session token", "err", err)
		http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
		return
	}
	if err := h.store.CreateSession(r.Context(), sessionHash, user.ID, time.Now().Add(h.cfg.SessionTTL)); err != nil {
		h.logger.Error("creating session", "err", err)
		http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
		return
	}

	auth.SetSessionCookie(w, h.cfg.SessionCookieName, sessionToken, h.cfg.SessionTTL, auth.CookieShouldBeSecure(h.cfg.PublicBaseURL))
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// Logout clears the session both in the browser (the cookie) and on the
// server (the sessions row), so a stolen-but-expired cookie can't be
// replayed and a signed-out session can't be un-signed-out by resending an
// old cookie value.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(h.cfg.SessionCookieName); err == nil {
		if err := h.store.DeleteSession(r.Context(), auth.HashToken(cookie.Value)); err != nil {
			h.logger.Warn("deleting session on logout", "err", err)
		}
	}
	auth.ClearSessionCookie(w, h.cfg.SessionCookieName, auth.CookieShouldBeSecure(h.cfg.PublicBaseURL))
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
