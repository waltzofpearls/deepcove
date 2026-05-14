// Package ui serves the SvelteKit SPA. In development (IS_DEV=true) it
// reverse-proxies to a Vite dev server for HMR. Otherwise it serves
// the embedded build output from //go:embed at the module root.
package ui

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	deepcove "github.com/waltzofpearls/deepcove"
	"github.com/waltzofpearls/deepcove/internal/config"
)

type UI struct {
	config *config.Config
	mux    *http.ServeMux
}

type Option func(*UI)

func WithConfig(conf *config.Config) Option {
	return func(ui *UI) {
		ui.config = conf
	}
}

func WithMux(mux *http.ServeMux) Option {
	return func(ui *UI) {
		ui.mux = mux
	}
}

func New(options ...Option) *UI {
	ui := &UI{}
	for _, option := range options {
		option(ui)
	}
	return ui
}

func (ui *UI) AttachRoutes() error {
	if ui.config.IsDev {
		// In development, a separate Vite dev server (npm run dev in
		// ./ui) runs on port 5173. Proxy all non-API requests to it
		// so HMR works end-to-end through deepcove's listener.
		//
		// Routing shape is taken from tradecopia-server's working
		// setup: an explicit /ws route plus a catch-all, both
		// pointing at the same proxy. The explicit /ws is
		// belt-and-suspenders for the WebSocket Upgrade handshake;
		// the catch-all serves everything else (HTML, /_app/* JS
		// chunks, etc).
		//
		// httputil.ReverseProxy handles WebSocket Upgrade natively
		// since Go 1.12. FlushInterval = -1 makes sure streamed
		// payloads (HMR notifications, SSE) reach the browser
		// immediately rather than being buffered.
		viteURL, err := url.Parse(ui.config.ViteURL)
		if err != nil {
			return fmt.Errorf("failed to parse vite url: %w", err)
		}
		proxy := httputil.NewSingleHostReverseProxy(viteURL)
		proxy.FlushInterval = -1
		ui.mux.Handle("/ws", proxy)
		ui.mux.Handle("/", proxy)
		return nil
	}

	// Production: serve the embedded SvelteKit build with SPA
	// fallback for client-side routes.
	sub, err := fs.Sub(deepcove.Assets(), "ui/build")
	if err != nil {
		return fmt.Errorf("embedded build dir not found: %w", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	ui.mux.Handle("/", spaHandler(sub, fileServer))
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
func spaHandler(sub fs.FS, fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath := strings.TrimPrefix(r.URL.Path, "/")

		if reqPath != "" {
			if _, err := fs.Stat(sub, reqPath); err != nil {
				// File doesn't exist in the embed.FS.
				if isAssetShape(reqPath) {
					http.NotFound(w, r)
					return
				}
				// SPA fallback for client-side routes.
				r.URL.Path = "/"
				reqPath = ""
			}
		}

		setCacheHeaders(w, reqPath)
		fileServer.ServeHTTP(w, r)
	})
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
