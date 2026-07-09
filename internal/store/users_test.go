package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Tehman700/liveurl/internal/config"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	cfg := config.LoadServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := Open(ctx, cfg.PostgresDSN)
	if err != nil {
		t.Skipf("postgres not reachable, skipping integration test: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func testEmail(t *testing.T) string {
	return fmt.Sprintf("signup-test-%d-%d@localhost", time.Now().UnixNano(), len(t.Name()))
}

func TestSignUpAndVerifyPassword(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()
	ctx := context.Background()
	email := testEmail(t)

	user, token, err := st.SignUp(ctx, email, "correct-horse-battery")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if user.Email != email {
		t.Fatalf("expected email %q, got %q", email, user.Email)
	}
	if token == "" {
		t.Fatal("expected a non-empty token from signup")
	}

	// The token returned by SignUp must resolve back to the same account,
	// exactly like a `liveurld seed`-minted token does.
	byToken, err := st.UserByToken(ctx, token)
	if err != nil {
		t.Fatalf("user by token: %v", err)
	}
	if byToken.ID != user.ID {
		t.Fatalf("expected token to resolve to user %d, got %d", user.ID, byToken.ID)
	}

	// Correct credentials verify.
	verified, err := st.VerifyPassword(ctx, email, "correct-horse-battery")
	if err != nil {
		t.Fatalf("verify password: %v", err)
	}
	if verified.ID != user.ID {
		t.Fatalf("expected verify to resolve to user %d, got %d", user.ID, verified.ID)
	}

	// Wrong password does not.
	if _, err := st.VerifyPassword(ctx, email, "wrong-password"); err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for wrong password, got %v", err)
	}
}

func TestSignUpRejectsDuplicateEmail(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()
	ctx := context.Background()
	email := testEmail(t)

	if _, _, err := st.SignUp(ctx, email, "first-password"); err != nil {
		t.Fatalf("first signup: %v", err)
	}
	if _, _, err := st.SignUp(ctx, email, "second-password"); err != ErrEmailTaken {
		t.Fatalf("expected ErrEmailTaken on duplicate signup, got %v", err)
	}
}

func TestSignUpRejectsEmailAlreadySeededWithoutPassword(t *testing.T) {
	// A password-less account (created via the `liveurld seed` CLI path,
	// i.e. CreateUser) must not be claimable through self-serve signup —
	// otherwise anyone who knows an operator-provisioned email could set a
	// password on it and take over the account.
	st := openTestStore(t)
	defer st.Close()
	ctx := context.Background()
	email := testEmail(t)

	if _, err := st.CreateUser(ctx, email); err != nil {
		t.Fatalf("seed-style create user: %v", err)
	}
	if _, _, err := st.SignUp(ctx, email, "attacker-chosen-password"); err != ErrEmailTaken {
		t.Fatalf("expected ErrEmailTaken when signing up over a seeded account, got %v", err)
	}
}

func TestVerifyPasswordRejectsSeededAccountWithNoPassword(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()
	ctx := context.Background()
	email := testEmail(t)

	if _, err := st.CreateUser(ctx, email); err != nil {
		t.Fatalf("seed-style create user: %v", err)
	}
	if _, err := st.VerifyPassword(ctx, email, "anything"); err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for a password-less account, got %v", err)
	}
}

func TestVerifyPasswordRejectsUnknownEmail(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()
	ctx := context.Background()

	if _, err := st.VerifyPassword(ctx, testEmail(t), "anything"); err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for unknown email, got %v", err)
	}
}
