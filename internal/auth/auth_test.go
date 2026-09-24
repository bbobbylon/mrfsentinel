package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// readCookie pulls the single Set-Cookie header off a recorded response and
// parses it back into an *http.Cookie. The cookie helpers in session.go
// write straight to an http.ResponseWriter and return nothing, so this is
// the only way to assert on what they produced. It fails the test rather
// than returning an error, which keeps the call sites below to one line.
func readCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	res := rec.Result()
	defer res.Body.Close()
	cookies := res.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected exactly 1 Set-Cookie header, got %d", len(cookies))
	}
	return cookies[0]
}

// TestNewToken_ShapeAndEntropy pins the properties the whole magic-link
// scheme rests on: 256 bits from crypto/rand, encoded URL-safely so it can
// be pasted into a link without escaping, and a fresh value every call.
func TestNewToken_ShapeAndEntropy(t *testing.T) {
	raw, hash, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken() returned error: %v", err)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("raw token is not valid RawURLEncoding base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("token carries %d bytes of entropy, want 32 (256 bits)", len(decoded))
	}

	// RawURLEncoding is the padding-free, URL-safe alphabet. Any of these
	// characters appearing would mean a token that needs escaping before it
	// can go in a magic-link URL.
	if strings.ContainsAny(raw, "+/=") {
		t.Errorf("raw token %q contains +, / or =, which are not URL-safe", raw)
	}

	if hash != HashToken(raw) {
		t.Errorf("NewToken's returned hash does not match HashToken(raw)")
	}
}

// TestNewToken_NeverReturnsTheRawTokenAsItsHash guards the single mistake
// that would quietly undo the reason this package hashes at all: if raw and
// hash were ever equal, the stored column would be a working credential and
// a database leak would hand an attacker usable sign-in links.
func TestNewToken_NeverReturnsTheRawTokenAsItsHash(t *testing.T) {
	raw, hash, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken() returned error: %v", err)
	}
	if raw == hash {
		t.Fatal("NewToken returned the raw token as its own hash — the stored value would be a usable credential")
	}
}

// TestNewToken_IsUnique checks that repeated calls do not collide. A fixed
// or predictable token would let anyone forge a sign-in, so this is worth a
// test even though the randomness comes from crypto/rand rather than from
// any code here.
func TestNewToken_IsUnique(t *testing.T) {
	const iterations = 100
	seen := make(map[string]struct{}, iterations)
	for i := 0; i < iterations; i++ {
		raw, _, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken() returned error on call %d: %v", i, err)
		}
		if _, dup := seen[raw]; dup {
			t.Fatalf("NewToken produced a duplicate token after %d calls", i)
		}
		seen[raw] = struct{}{}
	}
}

// TestHashToken_IsDeterministic covers the property that makes lookup work
// at all: a token presented back later must hash to the same value stored
// at issue time, or no one could ever sign in.
func TestHashToken_IsDeterministic(t *testing.T) {
	const token = "a-token-value"
	if first, second := HashToken(token), HashToken(token); first != second {
		t.Errorf("HashToken is not deterministic: %q then %q", first, second)
	}
}

// TestHashToken_KnownVector checks HashToken against the published SHA-256
// digest of the empty string, hex-encoded. This is what catches a change of
// algorithm or encoding that the round-trip tests above would happily keep
// passing — they only ever compare HashToken to itself.
func TestHashToken_KnownVector(t *testing.T) {
	const emptyStringSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := HashToken(""); got != emptyStringSHA256 {
		t.Errorf("HashToken(%q) = %q, want %q", "", got, emptyStringSHA256)
	}
}

// TestHashToken_DistinctInputsDistinctOutputs is a sanity check that the
// function is not, say, truncating its input to a fixed prefix.
func TestHashToken_DistinctInputsDistinctOutputs(t *testing.T) {
	if HashToken("token-a") == HashToken("token-b") {
		t.Error("two different tokens hashed to the same value")
	}
}

// TestSetSessionCookie_SecurityAttributes asserts the flags that make a
// session cookie safe rather than merely functional. HttpOnly is what keeps
// an XSS bug from turning into stolen sessions, and SameSite=Lax is the
// CSRF mitigation this app relies on in place of per-form tokens.
func TestSetSessionCookie_SecurityAttributes(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "mrfsentinel_session", "raw-token-value", time.Hour, true)

	c := readCookie(t, rec)
	if c.Name != "mrfsentinel_session" {
		t.Errorf("cookie name = %q, want %q", c.Name, "mrfsentinel_session")
	}
	if c.Value != "raw-token-value" {
		t.Errorf("cookie value = %q, want the raw token", c.Value)
	}
	if !c.HttpOnly {
		t.Error("cookie is not HttpOnly — client-side JavaScript could read the session token")
	}
	if !c.Secure {
		t.Error("cookie is not Secure although secure=true was requested")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want %v", c.SameSite, http.SameSiteLaxMode)
	}
	if c.Path != "/" {
		t.Errorf("cookie Path = %q, want %q", c.Path, "/")
	}
}

// TestSetSessionCookie_HonoursInsecureFlag is the local-development half of
// CookieShouldBeSecure's reason to exist: over plain http://localhost the
// Secure flag must stay off, or the browser drops the cookie and no one can
// sign in.
func TestSetSessionCookie_HonoursInsecureFlag(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "s", "raw", time.Hour, false)

	if c := readCookie(t, rec); c.Secure {
		t.Error("cookie is Secure although secure=false was requested")
	}
}

// TestSetSessionCookie_ExpiryTracksTTL checks that the requested TTL reaches
// the cookie's Expires attribute.
//
// The assertion is a window rather than an equality check for two reasons.
// The obvious one is that the deadline is computed from time.Now inside the
// function under test. The less obvious one cost this test a first draft:
// Set-Cookie serializes Expires in RFC 1123 form, which has no sub-second
// field, so the value parsed back here is the truncated version and can sit
// slightly *earlier* than the instant the test sampled before calling. Hence
// a second of slack on both ends rather than just the upper one.
func TestSetSessionCookie_ExpiryTracksTTL(t *testing.T) {
	const ttl = 14 * 24 * time.Hour

	before := time.Now()
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "s", "raw", ttl, false)
	after := time.Now()

	earliest := before.Add(ttl).Add(-time.Second)
	latest := after.Add(ttl).Add(time.Second)

	c := readCookie(t, rec)
	if c.Expires.Before(earliest) || c.Expires.After(latest) {
		t.Errorf("cookie expires at %v, want within [%v, %v] (%v from now)", c.Expires, earliest, latest, ttl)
	}
}

// TestClearSessionCookie_ExpiresImmediately covers sign-out. MaxAge below
// zero is what actually deletes the cookie in every current browser; the
// zero Expires time is the belt-and-braces fallback for older ones, so both
// are asserted.
func TestClearSessionCookie_ExpiresImmediately(t *testing.T) {
	rec := httptest.NewRecorder()
	ClearSessionCookie(rec, "mrfsentinel_session", false)

	c := readCookie(t, rec)
	if c.Value != "" {
		t.Errorf("cleared cookie still carries a value: %q", c.Value)
	}
	if c.MaxAge >= 0 {
		t.Errorf("cleared cookie MaxAge = %d, want a negative value to delete it", c.MaxAge)
	}
	if !c.Expires.Before(time.Now()) {
		t.Errorf("cleared cookie expires at %v, which is not in the past", c.Expires)
	}
	if !c.HttpOnly {
		t.Error("cleared cookie dropped HttpOnly — attributes must match the cookie being replaced")
	}
}

// TestCookieShouldBeSecure covers the scheme check that decides the Secure
// flag. The uppercase case is deliberately included and is called out below
// as a known sharp edge rather than treated as correct behavior.
func TestCookieShouldBeSecure(t *testing.T) {
	tests := []struct {
		name          string
		publicBaseURL string
		want          bool
	}{
		{"production https URL", "https://app.example.com", true},
		{"https with port and path", "https://app.example.com:8443/base", true},
		{"local development over http", "http://localhost:8080", false},
		{"empty config value", "", false},
		{"scheme-less host", "app.example.com", false},
		{"http URL that merely mentions https", "http://https.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CookieShouldBeSecure(tt.publicBaseURL); got != tt.want {
				t.Errorf("CookieShouldBeSecure(%q) = %v, want %v", tt.publicBaseURL, got, tt.want)
			}
		})
	}
}

// TestCookieShouldBeSecure_UppercaseSchemeIsNotRecognised documents current
// behavior, and documents it as a defect rather than a guarantee.
//
// URL schemes are case-insensitive per RFC 3986, but CookieShouldBeSecure
// does a plain strings.HasPrefix against the lowercase literal. A
// PUBLIC_BASE_URL of "HTTPS://app.example.com" therefore yields a session
// cookie with no Secure flag, sent in clear text over any downgraded
// connection — the exact failure the flag exists to prevent. The fix is to
// lowercase before comparing. This test asserts what the code does today so
// the behavior is visible instead of latent; it should be inverted, not
// deleted, when the function is fixed.
func TestCookieShouldBeSecure_UppercaseSchemeIsNotRecognised(t *testing.T) {
	if CookieShouldBeSecure("HTTPS://app.example.com") {
		t.Skip("CookieShouldBeSecure now handles uppercase schemes — invert this test and delete the note above")
	}
}
