package auth

import (
	"net/http"
	"strings"
	"time"
)

// SetSessionCookie writes a signed-in session's raw token as an HttpOnly,
// SameSite=Lax cookie. HttpOnly means client-side JavaScript can never read
// this cookie (closing off a whole class of XSS-steals-your-session
// attack) — there's no equivalent concern to manage on the Angular/SPA side
// of DeleteBoard, where the token lives in memory instead, but this app's
// server-rendered pages make a cookie the natural, simpler choice; see
// ARCHITECTURE.md for why.
func SetSessionCookie(w http.ResponseWriter, name, rawToken string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    rawToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(ttl),
	})
}

// ClearSessionCookie signs a browser out by expiring its cookie
// immediately.
func ClearSessionCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// CookieShouldBeSecure reports whether the Secure cookie flag should be
// set, based on the app's own public base URL. Secure cookies are only
// ever sent over HTTPS — correct for a real deployment, but it would break
// local development over plain http://localhost, so this is derived from
// config rather than hardcoded true.
//
// The input is lowercased and trimmed before the comparison, which is not
// fussiness. Every way this check can fail to recognise an HTTPS URL has
// the same consequence — a session cookie shipped without Secure, and so
// sent in clear text over any downgraded connection — and PUBLIC_BASE_URL
// is a hand-written environment variable. URL schemes are case-insensitive
// per RFC 3986, so "HTTPS://app.example.com" is a perfectly legal spelling
// that a plain prefix test would reject; leading whitespace is the other
// easy way to get there, since a Compose file or ECS task definition will
// happily pass through a value with a stray space. Both used to silently
// downgrade the cookie. Erring toward recognising HTTPS is the safe
// direction: the cost of a false positive is a cookie that a local
// http://localhost session drops, which fails loudly at sign-in, while a
// false negative is invisible.
func CookieShouldBeSecure(publicBaseURL string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(publicBaseURL)), "https://")
}
