package session

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// AttachRoutes registers the session API on mux. Routes follow
// Go 1.22's method-aware patterns so each verb has its own handler.
func (s *Service) AttachRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents", s.handleAgents)
	mux.HandleFunc("GET /api/preflight", s.handlePreflight)
	mux.HandleFunc("POST /api/runtimes/{runtime}/setup", s.handleSetupRuntime)
	mux.HandleFunc("GET /api/runtimes", s.handleRuntimes)
	mux.HandleFunc("DELETE /api/runtimes/{runtime}/sandboxes/{handle}", s.handleDestroySandbox)
	mux.HandleFunc("GET /api/fs/list", s.handleFsList)
	mux.HandleFunc("GET /api/images/exists", s.handleImageExists)
	mux.HandleFunc("POST /api/images/pull", s.handlePullImage)
	mux.HandleFunc("POST /api/sessions", s.handleCreate)
	mux.HandleFunc("GET /api/sessions", s.handleList)
	mux.HandleFunc("GET /api/sessions/local", s.handleListLocal)
	mux.HandleFunc("GET /api/cluster", s.handleGetCluster)
	mux.HandleFunc("PUT /api/cluster", s.handleSetCluster)
	mux.HandleFunc("GET /api/sessions/suggest-name", s.handleSuggestName)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGet)
	mux.HandleFunc("PATCH /api/sessions/{id}", s.handleRename)
	mux.HandleFunc("POST /api/sessions/{id}/start", s.handleStart)
	mux.HandleFunc("POST /api/sessions/{id}/stop", s.handleStop)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDestroy)
	mux.HandleFunc("GET /api/sessions/{id}/attach", s.handleAttach)
	mux.HandleFunc("GET /api/sessions/{id}/bootstrap", s.handleBootstrap)
	mux.HandleFunc("PUT /api/sessions/{id}/mounts", s.handleEditMounts)
	mux.HandleFunc("PUT /api/sessions/{id}/uploads", s.handleUpload)
	mux.HandleFunc("GET /api/sessions/{id}/preview", s.handlePreview)
	mux.HandleFunc("GET /api/sessions/{id}/tmux-windows", s.handleListTmuxWindows)
	mux.HandleFunc("POST /api/sessions/{id}/tmux-windows", s.handleCreateTmuxWindow)
	mux.HandleFunc("POST /api/sessions/{id}/tmux-windows/{name}/select", s.handleSelectTmuxWindow)
	mux.HandleFunc("DELETE /api/sessions/{id}/tmux-windows/{name}", s.handleDeleteTmuxWindow)
}

func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Start(r.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Agents())
}

// handleSuggestName returns a generated session name so the web UI can pre-fill
// the (now required) Name field with a predictable default the user can keep or
// edit. Same generator the server uses when a name is omitted.
// GET /api/sessions/suggest-name?agent=claude
func (s *Service) handleSuggestName(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"name": generateName(r.URL.Query().Get("agent"))})
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	sess, err := s.CreateRouted(r.Context(), req)
	if err != nil {
		if errors.Is(err, ErrNodeNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// Sandbox sentinels get precise status + a stable code the SPA
		// can act on (Docker down → 503, image absent → 422). Mount and
		// other user-input failures fall through to 400. See
		// writeSandboxError.
		writeSandboxError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

// handleList serves GET /api/sessions: the aggregated view across all the user's nodes (this node's
// sessions plus fanned-out peers, each tagged with its host). On a single node it's just this node.
func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	filter := ListFilter{IncludeAll: r.URL.Query().Get("all") == "true"}
	resp, err := s.ListAggregated(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListLocal serves GET /api/sessions/local: only this node's sessions, as a bare array. Used
// by peers during fan-out and by the local CLI (a single-machine tool).
func (s *Service) handleListLocal(w http.ResponseWriter, r *http.Request) {
	filter := ListFilter{IncludeAll: r.URL.Query().Get("all") == "true"}
	out, err := s.List(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []*Session{}
	}
	// Report this node's cluster to fan-out callers via a header, keeping the body a bare array.
	w.Header().Set(ClusterHeader, s.clusterStore.Name())
	writeJSON(w, http.StatusOK, out)
}

// handleGetCluster returns this node's cluster label. GET /api/cluster
func (s *Service) handleGetCluster(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"cluster": s.clusterStore.Name()})
}

// handleSetCluster sets this node's cluster label, persisted and effective immediately. PUT /api/cluster
// with {"cluster": "..."}; an empty value resets to the default cluster.
func (s *Service) handleSetCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// over-length gets a clear 400; Set/NormalizeCluster handles trim and the rest.
	name := strings.TrimSpace(req.Cluster)
	if len([]rune(name)) > MaxClusterNameLen {
		writeError(w, http.StatusBadRequest, "cluster name too long (max 64 characters)")
		return
	}
	if err := s.clusterStore.Set(name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cluster": s.clusterStore.Name()})
}

// handleRename updates a session's display name. PATCH /api/sessions/{id}
// with {"name": "..."}; returns the updated session.
func (s *Service) handleRename(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	sess, err := s.Rename(r.Context(), id, req.Name)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Service) handleStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Stop(r.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EditMountsRequest is the body shape of PUT /api/sessions/{id}/mounts.
// We accept the whole new mount list (not a diff) so the wire format matches what the UI's edit
// panel renders: fewer round-trips to keep in sync, simpler conflict semantics.
type EditMountsRequest struct {
	Mounts []MountRequest `json:"mounts"`
}

func (s *Service) handleEditMounts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req EditMountsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	sess, err := s.EditMounts(r.Context(), id, req.Mounts)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if errors.Is(err, ErrMountInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Service) handleDestroy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Destroy(r.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- tmux window handlers -----------------------------------------------

func (s *Service) handleListTmuxWindows(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.ListTmuxWindows(r.Context(), id)
	if err != nil {
		writeTmuxWindowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) handleCreateTmuxWindow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req CreateTmuxWindowRequest
	// Empty body is valid (server auto-names). Don't error on EOF.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}
	win, err := s.CreateTmuxWindow(r.Context(), id, req)
	if err != nil {
		writeTmuxWindowError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, win)
}

func (s *Service) handleSelectTmuxWindow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	if err := s.SelectTmuxWindow(r.Context(), id, name); err != nil {
		writeTmuxWindowError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleDeleteTmuxWindow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	if err := s.DeleteTmuxWindow(r.Context(), id, name); err != nil {
		writeTmuxWindowError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeTmuxWindowError maps the tmux-window / session sentinels to
// HTTP status codes. Anything unrecognised falls through to a 500.
func writeTmuxWindowError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "session not found")
	case errors.Is(err, ErrTmuxWindowNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrTmuxWindowExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrSessionNotRunning):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrSessionStarting):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrTmuxWindowNameInvalid), errors.Is(err, ErrTmuxWindowNameReserved):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// ---- response helpers ---------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// Error codes carried in the {error, code} envelope. The SPA maps these
// to friendly copy + actions; unknown/absent codes fall back to the raw message. Stable strings;
// don't rename without updating the client.
const (
	codeInvalidInput = "invalid_input"
	// codeRuntimeUnavailable covers any runtime whose backend cannot host a session, whichever one it
	// is. It replaced a Docker specific code: naming one runtime in the protocol made every other one
	// look like a special case, and told a VM user to go start Docker.
	codeRuntimeUnavailable = "runtime_unavailable"
	codeImageNotFound      = "image_not_found"
	codeNotFound           = "not_found"
	codeConflict           = "conflict"
)

func writeErrorCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// writeSandboxError maps sandbox sentinels to an HTTP status + stable code so the SPA never sees a
// raw daemon string for the cases it can act on. Non-sentinel errors (validation, unknown agent,
// and, today, internal failures) default to 400, matching the prior create-handler behavior.
func writeSandboxError(w http.ResponseWriter, err error) {
	var unavailable ErrRuntimeUnavailable
	switch {
	// The node knew up front that this runtime cannot host a session, so it says which one and why
	// instead of relaying a backend's connection error.
	case errors.As(err, &unavailable):
		writeErrorCode(w, http.StatusServiceUnavailable, codeRuntimeUnavailable, unavailable.Reason)
	// Raised by a driver rather than by the availability check above, so which runtime it was is no
	// longer in hand: this is the backend dying between the check and the call. Generic wording beats
	// naming the wrong runtime.
	case errors.Is(err, sandbox.ErrUnavailable):
		writeErrorCode(w, http.StatusServiceUnavailable, codeRuntimeUnavailable,
			"This runtime's backend isn't reachable. Check that it's running, then try again.")
	case errors.Is(err, sandbox.ErrImageNotFound):
		writeErrorCode(w, http.StatusUnprocessableEntity, codeImageNotFound, err.Error())
	case errors.Is(err, ErrNotFound) || errors.Is(err, sandbox.ErrNotFound):
		writeErrorCode(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, sandbox.ErrConflict):
		writeErrorCode(w, http.StatusConflict, codeConflict, err.Error())
	default:
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, err.Error())
	}
}
