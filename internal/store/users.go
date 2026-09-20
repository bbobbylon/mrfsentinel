package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned by every lookup method in this package when the
// row simply doesn't exist — callers check for it with errors.Is, the same
// way a Spring Data repository's Optional<T> being empty means "not found,"
// not "something broke."
var ErrNotFound = errors.New("store: not found")

// GetOrCreateUserByEmail returns the user with this email, creating one if
// none exists yet. There's no separate sign-up flow in this app — the first
// time an email requests a magic link, it becomes an account. This is safe
// because a magic link only ever proves control of an inbox, never
// confirms a password, so there's no separate "set a password" step to
// skip.
func (s *Store) GetOrCreateUserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, created_at FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.CreatedAt)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("store: looking up user by email: %w", err)
	}

	err = s.db.QueryRowContext(ctx,
		`INSERT INTO users (email) VALUES ($1) RETURNING id, email, created_at`, email,
	).Scan(&u.ID, &u.Email, &u.CreatedAt)
	if err != nil {
		return User{}, fmt.Errorf("store: creating user: %w", err)
	}
	return u, nil
}

// CreateMagicLink records a newly issued magic-link token (already hashed
// by the caller — see internal/auth) so it can later be looked up and
// consumed exactly once.
func (s *Store) CreateMagicLink(ctx context.Context, tokenHash, email string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO magic_links (token_hash, email, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, email, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("store: creating magic link: %w", err)
	}
	return nil
}

// ConsumeMagicLink atomically checks that tokenHash exists, is unexpired,
// and hasn't already been used, marks it used, and returns the email it was
// issued to. Doing the check-and-mark in one SQL statement (rather than a
// SELECT followed by an UPDATE) closes the race where two requests racing
// on the same link could both succeed — the UPDATE's WHERE clause is the
// only place that decides, atomically, whether this link is still usable.
func (s *Store) ConsumeMagicLink(ctx context.Context, tokenHash string) (email string, err error) {
	err = s.db.QueryRowContext(ctx, `
		UPDATE magic_links
		SET consumed_at = now()
		WHERE token_hash = $1
		  AND consumed_at IS NULL
		  AND expires_at > now()
		RETURNING email`,
		tokenHash,
	).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: consuming magic link: %w", err)
	}
	return email, nil
}

// CreateSession records a newly issued session token (already hashed by
// the caller).
func (s *Store) CreateSession(ctx context.Context, tokenHash, userID string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("store: creating session: %w", err)
	}
	return nil
}

// UserBySessionToken resolves a session cookie's hash to the signed-in
// user, or ErrNotFound if the session doesn't exist or has expired.
func (s *Store) UserBySessionToken(ctx context.Context, tokenHash string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`,
		tokenHash,
	).Scan(&u.ID, &u.Email, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: resolving session: %w", err)
	}
	return u, nil
}

// DeleteSession signs a session out. Deleting by token hash (rather than
// session ID) means the caller never needs a second lookup just to sign
// out.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("store: deleting session: %w", err)
	}
	return nil
}
