package classify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsWebhook(t *testing.T) {
	tests := []struct {
		name        string
		bufferRules []string
		method      string
		path        string
		headers     map[string]string
		body        string
		want        bool
	}{
		{
			name:   "stripe signature header",
			method: http.MethodPost,
			path:   "/any/path",
			headers: map[string]string{
				"Stripe-Signature": "t=1,v1=abc",
				"Content-Type":     "application/json",
			},
			want: true,
		},
		{
			name:   "github event header",
			method: http.MethodPost,
			path:   "/hooks",
			headers: map[string]string{
				"X-GitHub-Event": "push",
				"Content-Type":   "application/json",
			},
			want: true,
		},
		{
			name:        "explicit buffer rule wins even for GET",
			bufferRules: []string{"/webhooks/*"},
			method:      http.MethodGet,
			path:        "/webhooks/ping",
			want:        true,
		},
		{
			name:   "json POST with no html accept looks like a webhook",
			method: http.MethodPost,
			path:   "/api/events",
			headers: map[string]string{
				"Content-Type": "application/json",
			},
			want: true,
		},
		{
			name:   "form POST from a browser is not a webhook",
			method: http.MethodPost,
			path:   "/submit",
			headers: map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
				"Accept":       "text/html,application/xhtml+xml",
			},
			want: false,
		},
		{
			name:   "plain browser GET",
			method: http.MethodGet,
			path:   "/",
			headers: map[string]string{
				"Accept": "text/html",
			},
			want: false,
		},
		{
			name:   "GET without signature or buffer rule is never a webhook",
			method: http.MethodGet,
			path:   "/webhooks/ping",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			got := IsWebhook(tt.bufferRules, req)
			if got != tt.want {
				t.Errorf("IsWebhook() = %v, want %v", got, tt.want)
			}
		})
	}
}
