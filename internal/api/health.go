package api

import (
	"encoding/json"
	"net/http"
)

// handleHealth answers `GET /api/health` with a tiny JSON liveness probe.
// Intentionally minimal — anything user-facing belongs on richer endpoints.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"version": s.version,
	})
}
