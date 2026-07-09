package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type Tunnel struct {
	ID          int64
	UserID      int64
	Subdomain   string
	BufferRules []string
}

var ErrSubdomainTaken = errors.New("subdomain already in use")

// ClaimTunnel returns the tunnel for (userID, subdomain), creating it if it
// doesn't exist. If the subdomain exists but belongs to another user, it
// returns ErrSubdomainTaken.
func (s *Store) ClaimTunnel(ctx context.Context, userID int64, subdomain string, bufferRules []string) (Tunnel, error) {
	rules, err := json.Marshal(bufferRules)
	if err != nil {
		return Tunnel{}, err
	}

	var t Tunnel
	var rulesRaw []byte
	err = s.pool.QueryRow(ctx,
		`SELECT id, user_id, subdomain, buffer_rules FROM tunnels WHERE subdomain=$1`, subdomain,
	).Scan(&t.ID, &t.UserID, &t.Subdomain, &rulesRaw)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = s.pool.QueryRow(ctx,
			`INSERT INTO tunnels(user_id, subdomain, buffer_rules) VALUES($1, $2, $3)
			 RETURNING id, user_id, subdomain, buffer_rules`,
			userID, subdomain, rules,
		).Scan(&t.ID, &t.UserID, &t.Subdomain, &rulesRaw)
		if err != nil {
			return Tunnel{}, fmt.Errorf("create tunnel: %w", err)
		}
	case err != nil:
		return Tunnel{}, fmt.Errorf("lookup tunnel: %w", err)
	default:
		if t.UserID != userID {
			return Tunnel{}, ErrSubdomainTaken
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE tunnels SET buffer_rules=$1 WHERE id=$2`, rules, t.ID); err != nil {
			return Tunnel{}, fmt.Errorf("update buffer rules: %w", err)
		}
	}

	if err := json.Unmarshal(rulesRaw, &t.BufferRules); err != nil {
		return Tunnel{}, err
	}
	return t, nil
}

func (s *Store) TunnelByID(ctx context.Context, id int64) (Tunnel, error) {
	var t Tunnel
	var rulesRaw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, subdomain, buffer_rules FROM tunnels WHERE id=$1`, id,
	).Scan(&t.ID, &t.UserID, &t.Subdomain, &rulesRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tunnel{}, ErrNotFound
	}
	if err != nil {
		return Tunnel{}, err
	}
	if err := json.Unmarshal(rulesRaw, &t.BufferRules); err != nil {
		return Tunnel{}, err
	}
	return t, nil
}

func (s *Store) TunnelBySubdomain(ctx context.Context, subdomain string) (Tunnel, error) {
	var t Tunnel
	var rulesRaw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, subdomain, buffer_rules FROM tunnels WHERE subdomain=$1`, subdomain,
	).Scan(&t.ID, &t.UserID, &t.Subdomain, &rulesRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tunnel{}, ErrNotFound
	}
	if err != nil {
		return Tunnel{}, err
	}
	if err := json.Unmarshal(rulesRaw, &t.BufferRules); err != nil {
		return Tunnel{}, err
	}
	return t, nil
}

func (s *Store) ListTunnels(ctx context.Context, userID int64) ([]Tunnel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, subdomain, buffer_rules FROM tunnels WHERE user_id=$1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Tunnel
	for rows.Next() {
		var t Tunnel
		var rulesRaw []byte
		if err := rows.Scan(&t.ID, &t.UserID, &t.Subdomain, &rulesRaw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(rulesRaw, &t.BufferRules); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
