# Authentication

MRF Sentinel has **no passwords, anywhere** — same starting principle as DeleteBoard. There's
exactly one sign-in method: **email magic links**. Unlike DeleteBoard, there's no second method
(passkeys) here, and that's a deliberate difference, not a missing feature — see "Why only one
method" below.

## Why only one method

DeleteBoard holds compliance-critical audit records for regulated companies, where the identity
behind an action *is* part of the product's evidentiary value — passkeys earn their complexity
there because phishing resistance matters when the record of who-did-what is the thing being
protected. MRF Sentinel's auth only has to answer one, much lower-stakes question: "which list of
hospitals does this browser get to see and trigger runs for?" There's no audit trail tied to a
specific signed identity, no regulator ever asks "prove this particular person triggered this
particular validation run." Adding WebAuthn here would be real added complexity (a
`webauthn4j`-equivalent Go library, credential storage, the two-step ceremony) in exchange for
security properties this app doesn't actually need yet. If that changes — if MRF Sentinel ever
needs to attribute actions to a specific person for compliance purposes the way DeleteBoard does —
passkeys are the natural thing to add then, not a gap that was overlooked now.

## Magic-link flow

```
 Browser                          Server                            Database / Email
    │                                 │                                    │
    │  POST /auth/request-link        │                                    │
    │  { email }                      │                                    │
    ├────────────────────────────────►│                                    │
    │                                 │  GetOrCreateUserByEmail             │
    │                                 ├───────────────────────────────────►│
    │                                 │◄───────────────────────────────────┤
    │                                 │  generate random token,             │
    │                                 │  store SHA-256(token), email the    │
    │                                 │  raw token as a link                │
    │                                 ├───────────────────────────────────►│
    │  redirect → "check your email"  │                                    │
    │◄────────────────────────────────┤                                    │
    │                                 │                                    │
    │  (user clicks link in email)    │                                    │
    │  GET /auth/callback?token=...    │                                    │
    ├────────────────────────────────►│                                    │
    │                                 │  hash token, ConsumeMagicLink       │
    │                                 │  (atomic: check unexpired +         │
    │                                 │  unconsumed, mark consumed)         │
    │                                 ├───────────────────────────────────►│
    │                                 │  CreateSession                      │
    │                                 ├───────────────────────────────────►│
    │  Set-Cookie: session=...        │                                    │
    │  redirect → /dashboard          │                                    │
    │◄────────────────────────────────┤                                    │
```

Key implementation details:

- **Always creates the account on request, never reveals whether it existed.** Unlike
  DeleteBoard's `requestLink` (which looks up an *existing* `AppUser` and silently no-ops if the
  email isn't found), `GetOrCreateUserByEmail` (`internal/store/users.go`) creates the account on
  first request — there's no separate "sign up" step here, since there's nothing to onboard beyond
  an email address. The HTTP response is the same either way (redirect to "check your email"),
  which keeps the same anti-enumeration property DeleteBoard's version has, just achieved by "there
  is no not-found case" rather than "the not-found case is hidden."
- **Only a hash is ever persisted.** `auth.NewToken()` (`internal/auth/token.go`) generates 32
  random bytes via `crypto/rand`, base64-URL-encodes them for the raw token embedded in the email
  link, and separately computes `HashToken()` — SHA-256 of the raw token — which is the only thing
  `CreateMagicLink` writes to the database. Reading the database never exposes a usable token, the
  same property DeleteBoard's `MagicLinkToken` has.
- **Single-use, time-boxed, and atomic.** `ConsumeMagicLink` (`internal/store/users.go`) checks
  expiry and consumed-state and marks the token consumed in a single database transaction, so two
  near-simultaneous requests with the same token (a link opened twice, or prefetched by an email
  client's link-scanner) can't both succeed.

## Sessions, not JWTs

This is the one structural difference from DeleteBoard worth calling out explicitly, because it's
easy to assume "magic link → JWT" as if that's the only shape this can take. `CreateSession`
(`internal/store/users.go`) generates another random token the same way (`auth.NewToken()`), stores
its hash in a `sessions` table alongside the user id and an expiry, and sets it as an **HttpOnly
cookie** (`internal/auth/session.go`'s `SetSessionCookie`) — not a JWT carried in an
`Authorization` header. `UserBySessionToken` looks the session up on every authenticated request
(`internal/web/middleware.go`'s `requireAuth`).

That means, unlike DeleteBoard's JWTs, sessions here **can be revoked instantly** —
`DeleteSession` (called by `Logout`) removes the row, and the very next request with that cookie
fails to look anything up, no waiting for a token to expire. The trade-off DeleteBoard explicitly
accepted (no server-side revocation, because a JWT's whole point is not needing a database lookup
per request) doesn't apply here, because a server-rendered app is already doing a database round
trip on nearly every request anyway — there's no separate "stay stateless for performance" reason
to give up instant revocation. `CookieShouldBeSecure()` gates the cookie's `Secure` flag on whether
the app is running behind HTTPS (checked via `PublicBaseURL`'s scheme in `internal/config`), so
local HTTP development still works while production cookies are marked `Secure`. That check
lowercases and trims its input before comparing: URL schemes are case-insensitive per RFC 3986,
and `PUBLIC_BASE_URL` is hand-written, so `HTTPS://…` or a value with a stray leading space used
to be read as plain HTTP and silently ship a cookie with no `Secure` flag. Every way that check
can fail has the same invisible consequence, so it errs toward recognising HTTPS — a false
positive breaks local sign-in loudly, a false negative breaks nothing visibly.

There's no refresh flow to reason about either — a session is valid until `SessionTTL`
(`internal/config`, default in `docker-compose.yml`/`.env` examples) or until `Logout` deletes it,
whichever comes first; signing in again just means clicking a fresh magic link.
