// Package dashboard embeds and serves liveurl's browser dashboard — a
// static HTML/CSS/JS app (no build step) that talks to the existing
// internal/control REST API. It is mounted by internal/edge.Router at
// /dashboard on the public listener's bare apex host.
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web
var webFS embed.FS

// Handler serves the dashboard's static assets rooted at "/" — the caller
// (internal/edge.Router) is responsible for stripping any outer path
// prefix (e.g. "/dashboard") before requests reach it.
func Handler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// Only possible if the "web" directory is missing from the
		// embed at build time — a compile-time packaging mistake, not a
		// runtime condition callers should need to handle.
		panic("dashboard: embedded web assets missing: " + err.Error())
	}
	return http.FileServerFS(sub)
}
