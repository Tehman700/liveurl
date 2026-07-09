package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tehman700/liveurl/internal/config"
	"github.com/Tehman700/liveurl/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg := config.LoadServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := store.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		t.Skipf("postgres not reachable, skipping integration test: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func testEmail(t *testing.T) string {
	// A real-looking domain, not "@localhost" — /api/signup validates email
	// shape (unlike the CLI's `liveurld seed`, which accepts anything).
	return fmt.Sprintf("control-test-%d-%d@example.com", time.Now().UnixNano(), len(t.Name()))
}

// doJSON posts body to the server's mux directly (no real listener needed)
// and decodes the JSON response.
func doJSON(t *testing.T, s *Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "203.0.113.1:5555"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	resp := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, resp
}

func TestSignUpThenLogin(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()
	s := NewServer(st, nil, nil)
	email := testEmail(t)

	code, resp := doJSON(t, s, "POST", "/api/signup", map[string]string{"email": email, "password": "correct-horse-battery"})
	if code != http.StatusCreated {
		t.Fatalf("signup: expected 201, got %d (%v)", code, resp)
	}
	signupToken, _ := resp["token"].(string)
	if signupToken == "" {
		t.Fatalf("signup: expected a token in the response, got %v", resp)
	}

	// Duplicate signup is rejected.
	code, _ = doJSON(t, s, "POST", "/api/signup", map[string]string{"email": email, "password": "another-password"})
	if code != http.StatusConflict {
		t.Fatalf("duplicate signup: expected 409, got %d", code)
	}

	// Login with the right password mints a fresh, independently-valid token.
	code, resp = doJSON(t, s, "POST", "/api/login", map[string]string{"email": email, "password": "correct-horse-battery"})
	if code != http.StatusOK {
		t.Fatalf("login: expected 200, got %d (%v)", code, resp)
	}
	loginToken, _ := resp["token"].(string)
	if loginToken == "" || loginToken == signupToken {
		t.Fatalf("login: expected a distinct fresh token, got %q (signup token %q)", loginToken, signupToken)
	}
	if _, err := st.UserByToken(context.Background(), loginToken); err != nil {
		t.Fatalf("login token does not resolve to a user: %v", err)
	}

	// Login with the wrong (but well-formed) password is rejected without
	// revealing which field was wrong.
	code, resp = doJSON(t, s, "POST", "/api/login", map[string]string{"email": email, "password": "wrong-password"})
	if code != http.StatusUnauthorized {
		t.Fatalf("bad login: expected 401, got %d (%v)", code, resp)
	}
}

func TestSignUpValidatesInput(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()
	s := NewServer(st, nil, nil)

	cases := []struct {
		name     string
		email    string
		password string
	}{
		{"bad email", "not-an-email", "longenoughpassword"},
		{"short password", testEmail(t), "short"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, resp := doJSON(t, s, "POST", "/api/signup", map[string]string{"email": c.email, "password": c.password})
			if code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%v)", code, resp)
			}
		})
	}
}

func TestAuthEndpointsAreRateLimited(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()
	s := NewServer(st, nil, nil)

	// The signup/login limiter allows a burst of 5 per IP; the 6th
	// immediate attempt from the same IP must be rejected with 429 so
	// password guessing against /api/login isn't practical.
	var lastCode int
	for i := 0; i < 6; i++ {
		lastCode, _ = doJSON(t, s, "POST", "/api/login", map[string]string{"email": testEmail(t), "password": "whatever"})
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("expected the 6th rapid attempt to be rate limited with 429, got %d", lastCode)
	}
}
