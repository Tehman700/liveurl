package edge

import (
	"errors"
	"html"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Tehman700/liveurl/internal/store"
)

// OfflineHandler serves a request for a tunnel whose agent is currently
// disconnected. Stage 2/3 implementations classify the request (webhook vs.
// browser) and either buffer it or serve a cached snapshot.
type OfflineHandler interface {
	ServeOffline(w http.ResponseWriter, r *http.Request, tunnel store.Tunnel)
}

// Router is liveurld's public-facing HTTP handler. It resolves the
// subdomain from the Host header and dispatches to a live proxy or to the
// OfflineHandler depending on agent presence. Build one with NewRouter, not
// a bare struct literal, so the rate limiters are properly initialized.
type Router struct {
	BaseHost string // e.g. "lvh.me:8080"
	Store    *store.Store
	Presence *store.Presence
	Registry *Registry
	Offline  OfflineHandler

	// Dashboard serves the embedded web dashboard's static assets at
	// /dashboard, and DashboardAPI serves its data calls at
	// /dashboard/api/* (mounted with the "/dashboard" prefix stripped, so
	// a request to /dashboard/api/tunnels reaches DashboardAPI as
	// /api/tunnels — matching internal/control's route patterns exactly).
	// Both are on the bare apex host, alongside the landing page. Either
	// may be left nil to disable the dashboard.
	Dashboard    http.Handler
	DashboardAPI http.Handler

	tunnelLimiter    *IPLimiter
	dashboardLimiter *IPLimiter
}

// RouterConfig configures NewRouter. Rate-limit fields left at zero fall
// back to sane defaults.
type RouterConfig struct {
	BaseHost     string
	Store        *store.Store
	Presence     *store.Presence
	Registry     *Registry
	Offline      OfflineHandler
	Dashboard    http.Handler
	DashboardAPI http.Handler

	// TunnelRateRPS/Burst gate proxied tunnel traffic, keyed on
	// (client IP, subdomain) — never on IP alone, since webhook providers
	// like Stripe send from a shared pool of IPs used across all their
	// customers; keying on IP alone would let one tenant's traffic
	// throttle every other tenant sharing that source IP.
	TunnelRateRPS   float64
	TunnelRateBurst int
	// DashboardRateRPS/Burst gate the apex host (landing page + the
	// dashboard and its API), keyed on client IP alone.
	DashboardRateRPS   float64
	DashboardRateBurst int
}

const rateLimiterIdleTTL = 5 * time.Minute

func NewRouter(cfg RouterConfig) *Router {
	tunnelRPS := cfg.TunnelRateRPS
	if tunnelRPS <= 0 {
		tunnelRPS = 20
	}
	tunnelBurst := cfg.TunnelRateBurst
	if tunnelBurst <= 0 {
		tunnelBurst = 40
	}
	dashboardRPS := cfg.DashboardRateRPS
	if dashboardRPS <= 0 {
		dashboardRPS = 10
	}
	dashboardBurst := cfg.DashboardRateBurst
	if dashboardBurst <= 0 {
		dashboardBurst = 30
	}

	return &Router{
		BaseHost:         cfg.BaseHost,
		Store:            cfg.Store,
		Presence:         cfg.Presence,
		Registry:         cfg.Registry,
		Offline:          cfg.Offline,
		Dashboard:        cfg.Dashboard,
		DashboardAPI:     cfg.DashboardAPI,
		tunnelLimiter:    NewIPLimiter(tunnelRPS, tunnelBurst, rateLimiterIdleTTL),
		dashboardLimiter: NewIPLimiter(dashboardRPS, dashboardBurst, rateLimiterIdleTTL),
	}
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sub := rt.subdomain(r.Host)
	ip := clientIP(r)

	if sub == "" {
		if !rt.dashboardLimiter.Allow(ip) {
			tooManyRequests(w)
			return
		}
		rt.serveApex(w, r)
		return
	}

	if !rt.tunnelLimiter.Allow(ip + "|" + sub) {
		tooManyRequests(w)
		return
	}

	ctx := r.Context()
	tunnel, err := rt.Store.TunnelBySubdomain(ctx, sub)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, brandedPage("404 Not Found", "No tunnel is registered for \""+sub+"\"."), http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("tunnel lookup error: %v", err)
		http.Error(w, brandedPage("500 Internal Error", "Something went wrong looking up this tunnel."), http.StatusInternalServerError)
		return
	}

	online, err := rt.Presence.IsOnline(ctx, sub)
	if err != nil {
		log.Printf("presence check error: %v", err)
	}

	if online {
		if sess, ok := rt.Registry.Get(sub); ok {
			if err := forwardLive(w, r, sess, tunnel.ID, rt.Store); err != nil {
				log.Printf("proxy error for %s: %v", sub, err)
			}
			return
		}
	}

	rt.serveOffline(w, r, tunnel)
}

// serveApex handles requests to the bare registered domain (no tunnel
// subdomain): the dashboard, its API, or the landing page.
func (rt *Router) serveApex(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/dashboard/api/"):
		if rt.DashboardAPI == nil {
			rt.serveLanding(w, r)
			return
		}
		http.StripPrefix("/dashboard", rt.DashboardAPI).ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, "/dashboard"):
		if rt.Dashboard == nil {
			rt.serveLanding(w, r)
			return
		}
		http.StripPrefix("/dashboard", rt.Dashboard).ServeHTTP(w, r)
	default:
		rt.serveLanding(w, r)
	}
}

func (rt *Router) serveOffline(w http.ResponseWriter, r *http.Request, tunnel store.Tunnel) {
	if rt.Offline != nil {
		rt.Offline.ServeOffline(w, r, tunnel)
		return
	}
	http.Error(w, brandedPage("503 Offline", "This tunnel's agent is not connected right now."), http.StatusServiceUnavailable)
}

func (rt *Router) subdomain(host string) string {
	host = strings.ToLower(host)
	base := strings.ToLower(rt.BaseHost)
	if host == base {
		return ""
	}
	suffix := "." + base
	if strings.HasSuffix(host, suffix) {
		return strings.TrimSuffix(host, suffix)
	}
	return ""
}

func (rt *Router) serveLanding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(brandedPage("liveurl", "The tunnel URL that survives your laptop going to sleep.")))
}

// clientIP extracts the connecting IP from r.RemoteAddr. There is
// deliberately no X-Forwarded-For/CF-Connecting-IP handling here: this
// deployment's Cloudflare DNS records are "DNS only" (not proxied), since
// Cloudflare's proxy can't forward the tunnel's custom port, so there is no
// trusted reverse proxy in front of this process and those headers would be
// fully attacker-forgeable.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func tooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "429 Too Many Requests\n", http.StatusTooManyRequests)
}

func brandedPage(title, message string) string {
	title = html.EscapeString(title)
	message = html.EscapeString(message)
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + title + `</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0c10;color:#e8e8ec;display:flex;
align-items:center;justify-content:center;height:100vh;margin:0}
.box{text-align:center;max-width:32rem;padding:2rem}
h1{font-size:1.5rem;margin-bottom:.5rem}
p{color:#9a9aa5}</style></head>
<body><div class="box"><h1>` + title + `</h1><p>` + message + `</p></div></body></html>`
}
