package session

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
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
	// Runtimes is what this node can actually host a session on. A node offers Docker everywhere and a
	// VM runtime only where the platform supports it and its kernel and guest init are present, so the
	// UI has to ask rather than assume.
	Runtimes RuntimesInfo `json:"runtimes"`
}

// RuntimesInfo lists the runtimes a node provides and which one a create request gets by default.
type RuntimesInfo struct {
	Available []string `json:"available"`
	Default   string   `json:"default"`
	// Unavailable maps a runtime this node provides a driver for to why it cannot host a session right
	// now, e.g. Docker with no daemon running. Registration says the driver exists; this says whether it
	// can actually be used, which is a live property and can change without restarting deepcove.
	//
	// Absent from the map means usable. Reported per runtime rather than as one flag because the answer
	// differs between them: a stopped Docker daemon says nothing about the VM runtime.
	Unavailable map[string]string `json:"unavailable,omitempty"`
	// Missing maps a runtime this node knows about but cannot provide to a stable reason code. Different
	// from Unavailable, which has a driver whose backend is not answering: this one has no driver at
	// all, and something has to be installed first. See missing.go.
	Missing map[string]string `json:"missing,omitempty"`
	// Defaults maps a runtime name to what a session gets when it asks for no particular size, so the
	// create form can show real numbers rather than the word "default". A runtime is absent from the map
	// when it imposes no limit of its own, which is different from a limit of zero.
	Defaults map[string]Resources `json:"defaults,omitempty"`
	// WorkspaceDefaults is the size a new workspace VM gets on this host. Separate from Defaults, which
	// is per runtime: this one is a property of the machine.
	WorkspaceDefaults Resources `json:"workspace_defaults"`
}

// runtimeDefaults collects each runtime's own idea of a default size. Drivers that do not allocate a
// fixed amount are left out entirely rather than reported as zeroes.
func (s *Service) runtimeDefaults() map[string]Resources {
	out := make(map[string]Resources, len(s.runtimes))
	for name, driver := range s.runtimes {
		if d, ok := driver.(sandbox.ResourceDefaulter); ok {
			out[name] = fromSandboxResources(d.DefaultResources())
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	writeJSON(w, http.StatusOK, PreflightResponse{
		Docker: s.dockerStatus(r.Context()),
		Runtimes: RuntimesInfo{
			Available:   s.Runtimes(),
			Default:     s.defaultRuntime,
			Defaults:    s.runtimeDefaults(),
			Unavailable: s.unavailableRuntimes(r.Context()),
			Missing:     s.MissingRuntimes(),

			WorkspaceDefaults: WorkspaceDefaults(s.dataDir),
		},
	})
}

// unavailableRuntimes probes every registered runtime and reports the ones that cannot host a session,
// with the reason. Probed concurrently: an unreachable backend costs the full ping timeout, and doing
// them in series would make one dead runtime slow down the answer for all the healthy ones.
//
// A driver with no Pinger cannot report liveness, so it is assumed usable rather than reported as broken.
func (s *Service) unavailableRuntimes(ctx context.Context) map[string]string {
	type result struct{ name, reason string }
	results := make(chan result, len(s.runtimes))

	var wg sync.WaitGroup
	for name := range s.runtimes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if reason := s.runtimeUnavailable(ctx, name); reason != "" {
				results <- result{name: name, reason: reason}
			}
		}()
	}
	wg.Wait()
	close(results)

	out := map[string]string{}
	for r := range results {
		out[r.name] = r.reason
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// unavailableReason is the guidance shown when a runtime cannot be used. Phrased per runtime because
// what the user has to do about it differs.
func unavailableReason(runtime string) string {
	switch runtime {
	case RuntimeDocker:
		return "Docker isn't running. Start it to use this runtime."
	case RuntimeAppleVF:
		return "The virtual machine runtime isn't responding."
	default:
		return "This runtime isn't available right now."
	}
}

func (s *Service) dockerStatus(ctx context.Context) DockerStatus {
	pinger, ok := s.runtimes[RuntimeDocker].(sandbox.Pinger)
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
	runtime, err := s.runtimeByName(r.URL.Query().Get("runtime"))
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, err.Error())
		return
	}
	mgr, ok := runtime.(sandbox.ImageManager)
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
		// Runtime picks which runtime's image store to pull into; empty means the node default. A VM
		// runtime builds its own disk image, so a pull for one runtime does not satisfy another.
		Runtime string `json:"runtime,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, "invalid JSON body: "+err.Error())
		return
	}
	if req.Image == "" {
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, "image is required")
		return
	}
	runtime, err := s.runtimeByName(req.Runtime)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, err.Error())
		return
	}
	puller, ok := runtime.(sandbox.ImageManager)
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
