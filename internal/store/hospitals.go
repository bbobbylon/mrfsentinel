package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CreateHospital adds a hospital a compliance officer wants to track,
// identified by the public URL of its own published MRF.
func (s *Store) CreateHospital(ctx context.Context, ownerUserID, name, mrfURL string) (Hospital, error) {
	var h Hospital
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO hospitals (owner_user_id, name, mrf_url)
		VALUES ($1, $2, $3)
		RETURNING id, owner_user_id, name, mrf_url, created_at`,
		ownerUserID, name, mrfURL,
	).Scan(&h.ID, &h.OwnerUserID, &h.Name, &h.MRFURL, &h.CreatedAt)
	if err != nil {
		return Hospital{}, fmt.Errorf("store: creating hospital: %w", err)
	}
	return h, nil
}

// ListHospitalsByOwner returns every hospital a user is tracking, newest
// first.
func (s *Store) ListHospitalsByOwner(ctx context.Context, ownerUserID string) ([]Hospital, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner_user_id, name, mrf_url, created_at
		FROM hospitals
		WHERE owner_user_id = $1
		ORDER BY created_at DESC`,
		ownerUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: listing hospitals: %w", err)
	}
	defer rows.Close()

	var out []Hospital
	for rows.Next() {
		var h Hospital
		if err := rows.Scan(&h.ID, &h.OwnerUserID, &h.Name, &h.MRFURL, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scanning hospital row: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading hospital rows: %w", err)
	}
	return out, nil
}

// GetHospital returns one hospital, scoped to its owner so one signed-in
// user can never fetch (or, by extension, trigger a run against) another
// user's hospital just by guessing an ID — the authorization check lives
// in the query itself, not as a separate step callers could forget.
func (s *Store) GetHospital(ctx context.Context, id, ownerUserID string) (Hospital, error) {
	var h Hospital
	err := s.db.QueryRowContext(ctx, `
		SELECT id, owner_user_id, name, mrf_url, created_at
		FROM hospitals
		WHERE id = $1 AND owner_user_id = $2`,
		id, ownerUserID,
	).Scan(&h.ID, &h.OwnerUserID, &h.Name, &h.MRFURL, &h.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Hospital{}, ErrNotFound
	}
	if err != nil {
		return Hospital{}, fmt.Errorf("store: getting hospital: %w", err)
	}
	return h, nil
}
