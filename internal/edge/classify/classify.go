// Package classify decides whether an incoming request, received while the
// tunnel's agent is offline, should be buffered as a webhook (queued for
// ordered replay) or treated as ordinary browser/API traffic.
package classify

import (
	"net/http"
	"path"
	"strings"
)

// SignatureHeaders are header names whose presence strongly indicates a
// webhook delivery from a known provider. Matched case-insensitively.
var SignatureHeaders = []string{
	"Stripe-Signature",
	"X-GitHub-Event",
	"X-Hub-Signature-256",
	"X-Hub-Signature",
	"X-Twilio-Signature",
	"Svix-Signature",
	"X-Shopify-Hmac-Sha256",
	"X-Slack-Signature",
	"Paypal-Transmission-Sig",
}

// IsWebhook classifies r as a webhook delivery that should be buffered
// while offline, versus ordinary traffic that should see the offline/
// snapshot page. bufferRules are user-declared path globs (e.g.
// "/webhooks/*") from the tunnel config and always take precedence.
func IsWebhook(bufferRules []string, r *http.Request) bool {
	for _, rule := range bufferRules {
		if ok, _ := path.Match(rule, r.URL.Path); ok {
			return true
		}
	}

	for _, h := range SignatureHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}

	return heuristic(r)
}

func heuristic(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	looksLikeAPIBody := strings.Contains(ct, "json") || strings.Contains(ct, "form-urlencoded")
	if !looksLikeAPIBody {
		return false
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "text/html") {
		return false
	}
	return true
}
