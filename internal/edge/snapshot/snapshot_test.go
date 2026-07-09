package snapshot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newResp(status int, headers map[string]string) *http.Response {
	resp := &http.Response{StatusCode: status, Header: http.Header{}}
	for k, v := range headers {
		resp.Header.Set(k, v)
	}
	return resp
}

func TestCacheable(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		reqHeaders  map[string]string
		respStatus  int
		respHeaders map[string]string
		want        bool
	}{
		{
			name:        "plain html 200 GET is cacheable",
			method:      http.MethodGet,
			respStatus:  200,
			respHeaders: map[string]string{"Content-Type": "text/html; charset=utf-8"},
			want:        true,
		},
		{
			name:        "POST is never cacheable",
			method:      http.MethodPost,
			respStatus:  200,
			respHeaders: map[string]string{"Content-Type": "text/html"},
			want:        false,
		},
		{
			name:        "non-200 is not cacheable",
			method:      http.MethodGet,
			respStatus:  404,
			respHeaders: map[string]string{"Content-Type": "text/html"},
			want:        false,
		},
		{
			name:        "request with cookie is skipped (authenticated)",
			method:      http.MethodGet,
			reqHeaders:  map[string]string{"Cookie": "session=abc"},
			respStatus:  200,
			respHeaders: map[string]string{"Content-Type": "text/html"},
			want:        false,
		},
		{
			name:        "request with auth header is skipped",
			method:      http.MethodGet,
			reqHeaders:  map[string]string{"Authorization": "Bearer xyz"},
			respStatus:  200,
			respHeaders: map[string]string{"Content-Type": "text/html"},
			want:        false,
		},
		{
			name:        "response with set-cookie is skipped",
			method:      http.MethodGet,
			respStatus:  200,
			respHeaders: map[string]string{"Content-Type": "text/html", "Set-Cookie": "a=b"},
			want:        false,
		},
		{
			name:        "cache-control private is skipped",
			method:      http.MethodGet,
			respStatus:  200,
			respHeaders: map[string]string{"Content-Type": "text/html", "Cache-Control": "private"},
			want:        false,
		},
		{
			name:        "cache-control no-store is skipped",
			method:      http.MethodGet,
			respStatus:  200,
			respHeaders: map[string]string{"Content-Type": "application/json", "Cache-Control": "no-store"},
			want:        false,
		},
		{
			name:        "unknown content type is skipped",
			method:      http.MethodGet,
			respStatus:  200,
			respHeaders: map[string]string{"Content-Type": "application/octet-stream"},
			want:        false,
		},
		{
			name:        "image content type is cacheable",
			method:      http.MethodGet,
			respStatus:  200,
			respHeaders: map[string]string{"Content-Type": "image/png"},
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/", nil)
			for k, v := range tt.reqHeaders {
				req.Header.Set(k, v)
			}
			resp := newResp(tt.respStatus, tt.respHeaders)
			if got := Cacheable(req, resp); got != tt.want {
				t.Errorf("Cacheable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInjectBanner(t *testing.T) {
	captured := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)

	t.Run("inserts before closing body tag", func(t *testing.T) {
		in := []byte("<html><body><h1>hi</h1></body></html>")
		out := string(InjectBanner(in, captured))
		if !strings.Contains(out, "liveurl") {
			t.Errorf("expected banner text in output, got: %s", out)
		}
		if strings.Index(out, "liveurl") > strings.Index(out, "</body>") {
			t.Errorf("expected banner to be injected before </body>, got: %s", out)
		}
	})

	t.Run("appends when no body tag present", func(t *testing.T) {
		in := []byte("just some text, no html structure")
		out := string(InjectBanner(in, captured))
		if !strings.HasPrefix(out, "just some text") || !strings.Contains(out, "liveurl") {
			t.Errorf("expected original content preserved with banner appended, got: %s", out)
		}
	})
}

func TestHeadersStripsHopByHopAndCookies(t *testing.T) {
	resp := newResp(200, map[string]string{
		"Content-Type": "text/html",
		"Set-Cookie":   "a=b",
		"Connection":   "keep-alive",
	})
	headers := Headers(resp)
	if _, ok := headers["Set-Cookie"]; ok {
		t.Errorf("expected Set-Cookie to be stripped, got headers: %v", headers)
	}
	if _, ok := headers["Connection"]; ok {
		t.Errorf("expected Connection to be stripped, got headers: %v", headers)
	}
	if headers["Content-Type"] != "text/html" {
		t.Errorf("expected Content-Type to be preserved, got: %v", headers)
	}
}
