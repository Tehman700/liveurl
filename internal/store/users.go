package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrEmailTaken is returned by SignUp when the email already belongs to
	// an account — including one created via `liveurld seed` without a
	// password, since letting signup attach a password to an existing
	// email would let anyone claim an operator-provisioned account just by
	// knowing its address.
	ErrEmailTaken = errors.New("email already registered")
	// ErrInvalidCredentials covers both "no such account" and "wrong
	// password" so login responses don't leak which one it was.
	ErrInvalidCredentials = errors.New("invalid email or password")
)

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

// SignUp creates a new password-holding account. It fails with
// ErrEmailTaken if the email is already registered — even if that existing
// row has no password set (e.g. an operator-seeded account) — and mints the
// account's first auth token in the same transaction so the caller gets
// back everything a self-serve signup needs in one call.
func (s *Store) SignUp(ctx context.Context, email, password string) (User, string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, "", fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback(ctx)

	var u User
	err = tx.QueryRow(ctx,
		`INSERT INTO users(email, password_hash) VALUES($1, $2) RETURNING id, email`,
		email, string(hash)).Scan(&u.ID, &u.Email)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, "", ErrEmailTaken
		}
		return User{}, "", fmt.Errorf("insert user: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return User{}, "", err
	}
	token := "lu_" + hex.EncodeToString(raw)
	if _, err := tx.Exec(ctx,
		`INSERT INTO auth_tokens(token_hash, user_id) VALUES($1, $2)`, hashToken(token), u.ID); err != nil {
		return User{}, "", fmt.Errorf("mint token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return User{}, "", err
	}
	return u, token, nil
}

// VerifyPassword checks email/password against a signed-up account and
// returns the owning user on success. It returns ErrInvalidCredentials for
// every failure mode (no such account, no password set, wrong password)
// rather than distinguishing them, so a login form can't be used to
// enumerate registered emails.
func (s *Store) VerifyPassword(ctx context.Context, email, password string) (User, error) {
	var u User
	var hash *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash FROM users WHERE email = $1`, email).Scan(&u.ID, &u.Email, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, fmt.Errorf("lookup user: %w", err)
	}
	if hash == nil {
		return User{}, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*hash), []byte(password)); err != nil {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
