// Package ui serves the SvelteKit SPA embedded from //go:embed at the module root.
package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	deepcove "github.com/waltzofpearls/deepcove"
)

// transportDirect is the default runtime transport: the browser reaches the API
// same-origin (rung 1 localhost, or a node whose tailnet it's on). The tailnet
// remote path injects "wasm" instead (see lib/api/transport.ts).
const transportDirect = "direct"

type UI struct {
	mux *http.ServeMux
	// transport is stamped into the served SPA shell so the client knows how to
	// reach the API before it makes a single request. Each listener can serve a
	// different value; localhost/start-node listeners use the direct default.
	transport string
	// target is the deepcove node's tailnet URL the in-browser node dials in wasm
	// mode (empty in direct mode, where the API is same-origin).
	target string
	// discover marks dashboard mode: the thin client hosts no API of its own, so the in-browser node
	// must discover your nodes itself (no injected target). Only set by `deepcove dashboard`.
	discover bool
}

type Option func(*UI)

func WithMux(mux *http.ServeMux) Option {
	return func(ui *UI) {
		ui.mux = mux
	}
}

// WithTransport overrides the runtime transport mode injected into the SPA.
// Defaults to direct.
func WithTransport(mode string) Option {
	return func(ui *UI) {
		ui.transport = mode
	}
}

// WithTarget sets the tailnet URL injected for wasm mode (the node the in-browser
// client dials). Ignored in direct mode.
func WithTarget(target string) Option {
	return func(ui *UI) {
		ui.target = target
	}
}

// WithDiscover marks dashboard mode: the SPA is told to discover your nodes via its own in-browser
// node rather than talk to a single injected target. Implies wasm transport.
func WithDiscover() Option {
	return func(ui *UI) {
		ui.discover = true
	}
}

// DashboardMode is how a page with no API of its own is served: the in-browser Tailscale node signs in and
// finds your nodes itself. `deepcove dashboard` and the hosted web node both use it.
func DashboardMode() Option {
	return func(ui *UI) {
		WithTransport("wasm")(ui)
		WithDiscover()(ui)
	}
}

// DashboardIndexHTML returns the SPA shell set up for DashboardMode, for the hosted web node to ship.
func DashboardIndexHTML(indexHTML []byte) []byte {
	dashboard := New(DashboardMode())
	return injectRuntimeConfig(indexHTML, dashboard.transport, dashboard.target, dashboard.discover)
}

func New(options ...Option) *UI {
	ui := &UI{transport: transportDirect}
	for _, option := range options {
		option(ui)
	}
	if ui.transport == "" {
		ui.transport = transportDirect
	}
	return ui
}

func (ui *UI) AttachRoutes() error {
	// Serve the embedded SvelteKit build with SPA fallback for client-side routes.
	sub, err := fs.Sub(deepcove.Assets(), "ui/build")
	if err != nil {
		return fmt.Errorf("embedded build dir not found: %w", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	ui.mux.Handle("/", ui.spaHandler(sub, fileServer))
	return nil
}

// spaHandler serves embedded SvelteKit assets with two pieces of
// per-request logic:
//
//  1. Asset-shaped requests (anything with a file extension, or
//     anything under _app/) that don't exist get a real 404. Without
//     this, typos in asset URLs silently serve the SPA shell and
//     turn into "why is my JS request returning HTML?" debugging
//     sessions.
//  2. Path-shaped requests (no extension) that don't exist fall back
//     to index.html so client-side routing works.
//
// Cache-Control:
//   - SvelteKit's _app/immutable/* is content-hashed and safe to
//     cache forever.
//   - Everything else (notably index.html) must revalidate so a new
//     build is picked up immediately.
func (ui *UI) spaHandler(sub fs.FS, fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No other site may show the dashboard in a frame, where it could trick a click.
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		reqPath := strings.TrimPrefix(r.URL.Path, "/")

		if reqPath != "" {
			if _, err := fs.Stat(sub, reqPath); err != nil {
				// File doesn't exist in the embed.FS.
				if isAssetShape(reqPath) {
					http.NotFound(w, r)
					return
				}
				// SPA fallback for client-side routes.
				reqPath = ""
			}
		}

		// The shell (index.html, and every client-side route that falls back to
		// it) is templated per request so the server can stamp in runtime config;
		// hashed assets are served untouched.
		if reqPath == "" || reqPath == "index.html" {
			ui.serveShell(w, sub)
			return
		}

		setCacheHeaders(w, reqPath)
		fileServer.ServeHTTP(w, r)
	})
}

// serveShell serves index.html with the runtime-config script injected, so the
// SPA learns its transport mode (and any future server-chosen settings) before
// the app bundle runs.
func (ui *UI) serveShell(w http.ResponseWriter, sub fs.FS) {
	raw, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "index.html missing from build", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(injectRuntimeConfig(raw, ui.transport, ui.target, ui.discover))
}

// injectRuntimeConfig inserts an inline <script> setting window.__deepcove just
// before </head>, so it runs before SvelteKit's deferred module scripts. If the
// document has no </head> we serve it unmodified rather than corrupt it.
func injectRuntimeConfig(html []byte, transport, target string, discover bool) []byte {
	rc := map[string]any{"transport": transport}
	if target != "" {
		rc["target"] = target
	}
	if discover {
		rc["discover"] = true
	}
	cfg, _ := json.Marshal(rc)
	script := []byte("<script>window.__deepcove=" + string(cfg) + ";</script>")

	marker := []byte("</head>")
	i := bytes.Index(html, marker)
	if i < 0 {
		return html
	}
	out := make([]byte, 0, len(html)+len(script))
	out = append(out, html[:i]...)
	out = append(out, script...)
	out = append(out, html[i:]...)
	return out
}

// isAssetShape returns true for request paths that look like static
// assets (have a file extension, or live under the SvelteKit hashed
// asset prefix). These should 404 when missing rather than fall back
// to index.html.
func isAssetShape(reqPath string) bool {
	if strings.HasPrefix(reqPath, "_app/") {
		return true
	}
	return path.Ext(reqPath) != ""
}

// setCacheHeaders writes Cache-Control based on the request path.
// Long-cache for content-hashed assets, must-revalidate for
// everything else. Called before delegating to http.FileServer.
func setCacheHeaders(w http.ResponseWriter, reqPath string) {
	if strings.HasPrefix(reqPath, "_app/immutable/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
}
