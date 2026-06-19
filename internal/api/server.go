// Package api implements deepcove's HTTP API surface. The same handler set is mounted on whichever
// listeners the running mode provides:
//   - `deepcove start`: local TCP + Unix socket + tsnet TLS (eventually)
//   - `deepcove dashboard` (no-service): not mounted; dashboard mode is SPA-only.
//
// Handlers live in sibling files (health.go, ...). New handlers register
// themselves in AttachRoutes.
package api

import (
	"net/http"

	"github.com/waltzofpearls/deepcove/internal/config"
)

type Server struct {
	config  *config.Config
	mux     *http.ServeMux
	version string
}

type Option func(*Server)

func WithConfig(cfg *config.Config) Option {
	return func(s *Server) { s.config = cfg }
}

func WithMux(mux *http.ServeMux) Option {
	return func(s *Server) { s.mux = mux }
}

// WithVersion injects the build version string. Passed in from the caller
// (typically internal/cli.Version) to avoid an api → cli import cycle.
func WithVersion(v string) Option {
	return func(s *Server) { s.version = v }
}

func New(options ...Option) *Server {
	s := &Server{}
	for _, opt := range options {
		opt(s)
	}
	return s
}

// AttachRoutes registers all API routes on the configured mux. Go 1.22+ pattern matching handles
// specificity, so the order of registration vs. the UI's catch-all `/` doesn't matter; `/api/health`
// always wins.
func (s *Server) AttachRoutes() error {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	return nil
}
