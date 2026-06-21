package session

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// PreflightResponse is the GET /api/preflight payload: the environment-readiness snapshot the
// dashboard reads on load (and the New Session form reads before it lets you create). Today it covers
// the container backend's reachability; per-image readiness is
// discovered at create time (a missing image comes back as a 422 with
// code "image_not_found", which the UI turns into a download action).
type PreflightResponse struct {
	Docker DockerStatus `json:"docker"`
}

// DockerStatus reports whether the container backend is reachable.
// Named "docker" on the wire because Docker is the v1 backend; the
// field is the place a future backend's readiness would surface too.
type DockerStatus struct {
	Reachable bool   `json:"reachable"`
	Version   string `json:"version"`
	// Message is human-facing guidance shown when Reachable is false
	// (e.g. "Docker isn't running. Start it and retry.").
	Message string `json:"message"`
}

const preflightPingTimeout = 3 * time.Second

func (s *Service) handlePreflight(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, PreflightResponse{Docker: s.dockerStatus(r.Context())})
}

func (s *Service) dockerStatus(ctx context.Context) DockerStatus {
	pinger, ok := s.sandbox.(sandbox.Pinger)
	if !ok {
		// The backend can't report liveness; don't block on a signal
		// we can't read; assume reachable.
		return DockerStatus{Reachable: true}
	}
	pingCtx, cancel := context.WithTimeout(ctx, preflightPingTimeout)
	defer cancel()
	version, err := pinger.Ping(pingCtx)
	if err != nil {
		return DockerStatus{
			Reachable: false,
			Message:   "Docker isn't reachable. Start Docker and try again.",
		}
	}
	return DockerStatus{Reachable: true, Version: version}
}

// ImageExistsResponse is the GET /api/images/exists?image=<ref> payload.
// The New Session form reads it as the user picks an agent/image so it
// can show a "will download" hint before they hit Create.
type ImageExistsResponse struct {
	Present bool `json:"present"`
}

func (s *Service) handleImageExists(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("image")
	if ref == "" {
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, "image query param is required")
		return
	}
	mgr, ok := s.sandbox.(sandbox.ImageManager)
	if !ok {
		// Backend can't report image presence; say "present" so the UI
		// doesn't offer a download it can't do.
		writeJSON(w, http.StatusOK, ImageExistsResponse{Present: true})
		return
	}
	present, err := mgr.ImageExists(r.Context(), ref)
	if err != nil {
		writeSandboxError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ImageExistsResponse{Present: present})
}

// handlePullImage pulls a single image ref so a subsequent create
// succeeds. It streams newline-delimited JSON progress events so the
// SPA can render a download bar:
//
//	{"status":"Downloading","percent":42}
//	{"percent":100,"done":true}        // success
//	{"error":"pull ...: manifest unknown"}  // failure (in-band)
//
// Errors knowable before streaming (backend down, image already
// present) are returned as a normal status + envelope instead.
func (s *Service) handlePullImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, "invalid JSON body: "+err.Error())
		return
	}
	if req.Image == "" {
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, "image is required")
		return
	}
	puller, ok := s.sandbox.(sandbox.ImageManager)
	if !ok {
		writeErrorCode(w, http.StatusNotImplemented, codeInvalidInput,
			"this backend does not support pulling images")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErrorCode(w, http.StatusInternalServerError, codeInvalidInput,
			"streaming unsupported on this connection")
		return
	}

	// Resolve known-up-front outcomes with a clean status before we
	// commit to a streamed 200: a down daemon → 503, an already-present
	// image → instant done (idempotent).
	present, err := puller.ImageExists(r.Context(), req.Image)
	if err != nil {
		writeSandboxError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	emit := func(v any) {
		_ = enc.Encode(v)
		flusher.Flush()
	}

	if present {
		emit(map[string]any{"percent": 100, "done": true})
		return
	}

	// Throttle to whole-percent / status changes so we don't flood the
	// stream with hundreds of byte-level updates.
	lastPct, lastStatus := -1, ""
	pullErr := puller.ImagePull(r.Context(), req.Image, func(p sandbox.PullProgress) {
		pct := -1
		if p.Total > 0 {
			pct = int(p.Current * 100 / p.Total)
		}
		if pct == lastPct && p.Status == lastStatus {
			return
		}
		lastPct, lastStatus = pct, p.Status
		emit(map[string]any{"status": p.Status, "percent": pct})
	})
	if pullErr != nil {
		emit(map[string]any{"error": pullErr.Error()})
		return
	}
	emit(map[string]any{"percent": 100, "done": true})
}
