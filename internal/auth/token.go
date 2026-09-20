// Package auth is MRF Sentinel's whole sign-in story: magic-link email
// auth, server-side sessions, and nothing else — no passwords anywhere in
// this codebase to hash, rate-limit reset attempts on, or leak. If you know
// this app's sibling project DeleteBoard: that one uses WebAuthn passkeys
// plus magic links, because identity and its audit trail ARE the product
// there. Here, identity is much lower-stakes — it just answers "whose
// hospital list is this" — so this app only needs the simpler half of that
// pair.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewToken generates a fresh random token (32 bytes / 256 bits of entropy,
// the same size Go's own crypto/rand-based examples use for session
// tokens) for a magic link or a session cookie. It returns both the raw
// token — sent to the user once, in an email or a cookie, and never stored
// — and its SHA-256 hash, which IS what gets stored (see
// internal/store/users.go). This is the same reason a password column
// holds a bcrypt hash rather than the password itself: a stolen copy of
// the magic_links or sessions table shouldn't be a usable sign-in link for
// whoever stole it.
func NewToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("auth: generating random token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashToken(raw), nil
}

// HashToken hashes a raw token the same way NewToken does, so a token
// presented back later (in a clicked link, or a cookie on a request) can be
// looked up by its hash.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
