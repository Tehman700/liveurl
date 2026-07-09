package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("not found")

type User struct {
	ID    int64
	Email string
}

// CreateUser inserts a user if it doesn't already exist and returns it
// either way (idempotent, used by `liveurld seed`).
func (s *Store) CreateUser(ctx context.Context, email string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users(email) VALUES($1)
		 ON CONFLICT(email) DO UPDATE SET email=EXCLUDED.email
		 RETURNING id, email`, email).Scan(&u.ID, &u.Email)
	return u, err
}

// NewToken generates a random token, stores its SHA-256 hash against the
// user, and returns the plaintext token (shown to the user exactly once).
func (s *Store) NewToken(ctx context.Context, userID int64) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := "lu_" + hex.EncodeToString(raw)
	hash := hashToken(token)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO auth_tokens(token_hash, user_id) VALUES($1, $2)`, hash, userID); err != nil {
		return "", err
	}
	return token, nil
}

// UserByToken resolves a plaintext token to its owning user.
func (s *Store) UserByToken(ctx context.Context, token string) (User, error) {
	hash := hashToken(token)
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT u.id, u.email FROM users u
		 JOIN auth_tokens t ON t.user_id = u.id
		 WHERE t.token_hash = $1`, hash).Scan(&u.ID, &u.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("lookup token: %w", err)
	}
	return u, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
