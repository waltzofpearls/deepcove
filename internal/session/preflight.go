package session

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// PreflightResponse is the GET /api/preflight payload: the environment-readiness snapshot the
// dashboard reads on load (and the New Session form reads before it lets you create). Whether an image
// is downloaded yet is a separate question, asked through the image check.
type PreflightResponse struct {
	// Runtimes is what this node can actually host a session on. A VM runtime is offered only where the
	// platform supports it and its kernel and guest init are present, so the UI has to ask rather than
	// assume.
	Runtimes RuntimesInfo `json:"runtimes"`
}

// RuntimesInfo lists the runtimes a node provides and which one a create request gets by default.
type RuntimesInfo struct {
	Available []string `json:"available"`
	// Default is empty when the node has no runtime it will pick for you: No sandbox is all it has, or
	// it has nothing.
	Default string `json:"default"`
	// Unavailable maps a runtime this node provides a driver for to why it cannot host a session right
	// now, e.g. a VM runtime whose hypervisor stopped answering. Registration says the driver exists;
	// this says whether it can actually be used, which is a live property and can change without
	// restarting deepcove.
	//
	// Absent from the map means usable. Reported per runtime rather than as one flag because the answer
	// differs between them.
	Unavailable map[string]string `json:"unavailable,omitempty"`
	// Missing maps a runtime this node knows about but cannot provide to a stable reason code. Different
	// from Unavailable, which has a driver whose backend is not answering: this one has no driver at
	// all, and something has to be installed first. See missing.go.
	Missing map[string]string `json:"missing,omitempty"`
	// SessionVMSizes are the sizes a session VM can have, each with how many fit on this host.
	SessionVMSizes []SessionVMSizeChoice `json:"session_vm_sizes"`
	// WorkspaceVMSizes are the sizes the workspace VM can have, each with about how many agents it runs.
	WorkspaceVMSizes []WorkspaceVMSizeChoice `json:"workspace_vm_sizes"`
	DefaultVMSize    string                  `json:"default_vm_size"`
	// WorkspaceRoots maps a runtime that makes a folder for each session to where those folders live, so
	// the create form can show the path before the session exists. Only a runtime with no guest has one.
	WorkspaceRoots map[string]string `json:"workspace_roots,omitempty"`
	// AgentsToInstall maps a runtime to the agents this node would have to install first, so the create
	// form can say so before you pick one.
	AgentsToInstall map[string][]string `json:"agents_to_install,omitempty"`
}

// runtimeAgentsToInstall lists, per runtime, the agents that are not on this node yet.
func (s *Service) runtimeAgentsToInstall() map[string][]string {
	out := map[string][]string{}
	for name, driver := range s.registeredRuntimes() {
		provisioner, ok := driver.(sandbox.AgentProvisioner)
		if !ok {
			continue
		}
		for _, agent := range allAgents() {
			if !provisioner.AgentInstalled(sandbox.Agent{Name: agent.Name, Binary: agent.Sandboxless.Binary}) {
				out[name] = append(out[name], agent.Name)
			}
		}
		sort.Strings(out[name])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// runtimeWorkspaceRoots collects where each runtime puts the folders it makes for sessions.
func (s *Service) runtimeWorkspaceRoots() map[string]string {
	out := map[string]string{}
	for name, driver := range s.registeredRuntimes() {
		if provisioner, ok := driver.(sandbox.AgentProvisioner); ok {
			if root := provisioner.WorkspaceRoot(); root != "" {
				out[name] = root
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Service) handlePreflight(w http.ResponseWriter, r *http.Request) {
	memoryMB := hostMemoryMB()
	writeJSON(w, http.StatusOK, PreflightResponse{
		Runtimes: RuntimesInfo{
			Available:   s.Runtimes(),
			Default:     s.DefaultRuntime(),
			Unavailable: s.unavailableRuntimes(r.Context()),
			Missing:     s.MissingRuntimes(),

			SessionVMSizes:   sessionVMSizeChoices(memoryMB),
			WorkspaceVMSizes: workspaceVMSizeChoices(workspaceVMSizes(memoryMB, hostMemoryKeptBackMB)),
			DefaultVMSize:    DefaultVMSize,
			WorkspaceRoots:   s.runtimeWorkspaceRoots(),
			AgentsToInstall:  s.runtimeAgentsToInstall(),
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
	registered := s.registeredRuntimes()
	results := make(chan result, len(registered))

	var wg sync.WaitGroup
	for name := range registered {
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
	case RuntimeAppleVF, RuntimeCloudHV:
		return "The virtual machine runtime isn't responding."
	case RuntimeSandboxless:
		return "tmux isn't installed. Sessions with no sandbox need it."
	default:
		return "This runtime isn't available right now."
	}
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
		writeErrorCode(w, http.StatusInternalServerError, codeInternal,
			"streaming unsupported on this connection")
		return
	}

	// Resolve known-up-front outcomes with a clean status before we
	// commit to a streamed 200: a down runtime → 503, an already-present
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
