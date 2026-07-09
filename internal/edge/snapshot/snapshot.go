// Package snapshot implements the passive write-through cache that lets
// browser GETs keep working (in a read-only, possibly-stale form) after a
// tunnel's agent goes offline.
package snapshot

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var cacheableContentTypePrefixes = []string{
	"text/html",
	"text/css",
	"text/plain",
	"text/javascript",
	"application/javascript",
	"application/json",
	"image/",
	"font/",
}

// hopByHopHeaders are stripped before storing/replaying a cached response.
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Set-Cookie", "Content-Length",
}

// Cacheable reports whether req/resp is safe and useful to store for
// offline replay. It deliberately excludes anything that looks
// authenticated or explicitly marked private, since a cached response can
// later be served to a *different* visitor.
func Cacheable(req *http.Request, resp *http.Response) bool {
	if req.Method != http.MethodGet {
		return false
	}
	if resp.StatusCode != http.StatusOK {
		return false
	}
	if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
		return false
	}
	if resp.Header.Get("Set-Cookie") != "" {
		return false
	}
	cc := strings.ToLower(resp.Header.Get("Cache-Control"))
	if strings.Contains(cc, "private") || strings.Contains(cc, "no-store") {
		return false
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	for _, prefix := range cacheableContentTypePrefixes {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}
	return false
}

// Headers extracts the subset of response headers worth replaying later,
// with hop-by-hop and cookie headers removed.
func Headers(resp *http.Response) map[string]string {
	out := make(map[string]string, len(resp.Header))
	for k, vals := range resp.Header {
		if isHopByHop(k) || len(vals) == 0 {
			continue
		}
		out[k] = vals[0]
	}
	return out
}

func isHopByHop(name string) bool {
	for _, h := range hopByHopHeaders {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// InjectBanner inserts a fixed "offline snapshot" notice into an HTML page
// just before </body>, or appends it if no </body> tag is found.
func InjectBanner(body []byte, capturedAt time.Time) []byte {
	banner := []byte(fmt.Sprintf(`<div style="position:fixed;bottom:0;left:0;right:0;z-index:2147483647;
background:#151a23;color:#e8e8ec;font:13px system-ui,sans-serif;padding:8px 14px;
border-top:1px solid #2a3140;text-align:center">
liveurl: live server is offline &mdash; showing a snapshot captured %s
</div>`, capturedAt.Local().Format("2006-01-02 15:04:05 MST")))

	lower := bytes.ToLower(body)
	if idx := bytes.LastIndex(lower, []byte("</body>")); idx != -1 {
		out := make([]byte, 0, len(body)+len(banner))
		out = append(out, body[:idx]...)
		out = append(out, banner...)
		out = append(out, body[idx:]...)
		return out
	}
	return append(body, banner...)
}
