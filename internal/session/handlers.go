package session

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// AttachRoutes registers the session API on mux, using Go 1.22's method-aware patterns. Anything this
// package serves may belong to another node, so the whole surface goes behind one wrapper.
func (s *Service) AttachRoutes(mux *http.ServeMux) {
	inner := http.NewServeMux()
	inner.HandleFunc("GET /api/agents", s.handleAgents)
	inner.HandleFunc("GET /api/preflight", s.handlePreflight)
	inner.HandleFunc("POST /api/runtimes/{runtime}/setup", s.handleSetupRuntime)
	inner.HandleFunc("GET /api/runtimes", s.handleRuntimes)
	inner.HandleFunc("DELETE /api/runtimes/{runtime}/sandboxes/{sandbox_id}", s.handleDestroySandbox)
	inner.HandleFunc("POST /api/runtimes/{runtime}/sandboxes/{sandbox_id}/restart", s.handleRestartSandbox)
	inner.HandleFunc("GET /api/workspace-vm", s.handleGetWorkspaceVM)
	inner.HandleFunc("PUT /api/workspace-vm", s.handleSaveWorkspaceVM)
	inner.HandleFunc("DELETE /api/workspace-vm", s.handleDeleteWorkspaceVM)
	inner.HandleFunc("GET /api/fs/list", s.handleFsList)
	inner.HandleFunc("GET /api/images/exists", s.handleImageExists)
	inner.HandleFunc("POST /api/images/pull", s.handlePullImage)
	inner.HandleFunc("POST /api/sessions", s.handleCreate)
	inner.HandleFunc("GET /api/sessions", s.handleList)
	inner.HandleFunc("GET /api/sessions/local", s.handleListLocal)
	inner.HandleFunc("GET /api/llm-proxy", s.handleGetLLMProxy)
	inner.HandleFunc("PUT /api/llm-proxy", s.handleSetLLMProxy)
	inner.HandleFunc("POST /api/llm-proxy/test", s.handleTestLLMProxy)
	inner.HandleFunc("GET /api/shared-logins/{agent}", s.handleGetSharedLogin)
	inner.HandleFunc("PUT /api/shared-logins/{agent}", s.handleSaveLoginSettings)
	inner.HandleFunc("POST /api/shared-logins/{agent}/switch", s.handleSwitchSharedLogin)
	inner.HandleFunc("POST /api/shared-logins/{agent}/keep", s.handleKeepSharedLogin)
	inner.HandleFunc("GET /api/sessions/suggest-name", s.handleSuggestName)
	inner.HandleFunc("GET /api/sessions/{id}", s.handleGet)
	inner.HandleFunc("PATCH /api/sessions/{id}", s.handlePatch)
	inner.HandleFunc("POST /api/sessions/{id}/start", s.handleStart)
	inner.HandleFunc("POST /api/sessions/{id}/stop", s.handleStop)
	inner.HandleFunc("DELETE /api/sessions/{id}", s.handleDestroy)
	inner.HandleFunc("GET /api/sessions/{id}/attach", s.handleAttach)
	inner.HandleFunc("GET /api/sessions/{id}/bootstrap", s.handleBootstrap)
	inner.HandleFunc("PUT /api/sessions/{id}/shared-folders", s.handleEditSharedFolders)
	inner.HandleFunc("PUT /api/sessions/{id}/uploads", s.handleUpload)
	inner.HandleFunc("GET /api/sessions/{id}/preview", s.handlePreview)
	inner.HandleFunc("GET /api/sessions/{id}/tmux-windows", s.handleListTmuxWindows)
	inner.HandleFunc("POST /api/sessions/{id}/tmux-windows", s.handleCreateTmuxWindow)
	inner.HandleFunc("POST /api/sessions/{id}/tmux-windows/{name}/select", s.handleSelectTmuxWindow)
	inner.HandleFunc("DELETE /api/sessions/{id}/tmux-windows/{name}", s.handleDeleteTmuxWindow)

	mux.Handle("/api/", s.forwardOrServe(inner))
}

func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Start(r.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeNotHostedHere(w)
			return
		}
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, allAgents())
}

// handleSuggestName returns a generated session name so the web UI can pre-fill
// the (now required) Name field with a predictable default the user can keep or
// edit. Same generator the server uses when a name is omitted.
// GET /api/sessions/suggest-name?agent=claude
func (s *Service) handleSuggestName(w http.ResponseWriter, r *http.Request) {
	// Unique too: the form prefills with this, so offering a taken name just fails on submit.
	name := s.generateUniqueName(r.Context(), r.URL.Query().Get("agent"))
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
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
		// Sandbox sentinels get a precise status and a stable code the SPA can act on (runtime down →
		// 503). Shared folder and other user input failures fall through to 400. See writeSandboxError.
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
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
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
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	if out == nil {
		out = []*Session{}
	}
	writeJSON(w, http.StatusOK, out)
}

// llmProxyView is what the API says about the proxy. Header values are write only, so only the names
// are in it. One read, so a save in between never pairs one proxy's URL with another's names.
func (s *Service) llmProxyView() map[string]any {
	proxy := s.LLMProxy()
	return map[string]any{
		"url":     proxy.URL,
		"headers": sortedHeaderNames(proxy.Headers),
	}
}

// handleGetLLMProxy serves GET /api/llm-proxy: where this node's sessions send LLM requests.
func (s *Service) handleGetLLMProxy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.llmProxyView())
}

// llmProxyRequest is the body of a save and of a test: the URL and the full list of headers.
type llmProxyRequest struct {
	URL     string               `json:"url"`
	Headers []LLMProxyHeaderEdit `json:"headers"`
}

// handleSetLLMProxy serves PUT /api/llm-proxy. An empty url sends sessions to the provider.
func (s *Service) handleSetLLMProxy(w http.ResponseWriter, r *http.Request) {
	var req llmProxyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.SaveLLMProxy(req.URL, req.Headers); err != nil {
		writeLLMProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.llmProxyView())
}

// handleTestLLMProxy serves POST /api/llm-proxy/test: one request with the values in the form, so the
// UI can report whether this node can reach the proxy before they are saved.
func (s *Service) handleTestLLMProxy(w http.ResponseWriter, r *http.Request) {
	var req llmProxyRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	// A UI older than testing the form sends no body, and means the saved proxy.
	if errors.Is(err, io.EOF) {
		req.URL = s.LLMProxyURL()
		err = nil
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := s.TestLLMProxy(r.Context(), req.URL, req.Headers)
	if err != nil {
		writeLLMProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// writeLLMProxyError adds the header's name when one header was refused, so the UI can show the
// reason on that row.
func writeLLMProxyError(w http.ResponseWriter, err error) {
	var headerErr LLMProxyHeaderError
	if !errors.As(err, &headerErr) {
		writeSandboxError(w, err)
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{
		"error":  headerErr.Reason,
		"code":   codeInvalidInput,
		"header": headerErr.Name,
	})
}

// handlePatch updates one session. PATCH /api/sessions/{id} with any of {"name": "...",
// "login_mode": "api_key", "vm_size": "large", "restart_now": false}. Fields left out are left alone,
// which is why they are pointers. A new VM size restarts a running VM unless restart_now is false.
func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name       *string    `json:"name"`
		LoginMode  *LoginMode `json:"login_mode"`
		VMSize     *string    `json:"vm_size"`
		RestartNow *bool      `json:"restart_now"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	// Checked before anything is applied: a patch carrying both fields should not rename and then fail.
	if req.Name != nil {
		if _, err := validateSessionName(*req.Name); err != nil {
			writeSandboxError(w, err)
			return
		}
	}
	if req.LoginMode != nil {
		sess, err := s.Get(r.Context(), id)
		// Asking for the mode it already has changes nothing, so it is not checked, as SetLoginMode does.
		if err == nil && sess.LoginMode != *req.LoginMode {
			err = s.loginModeChangeable(sess, *req.LoginMode)
		}
		if errors.Is(err, ErrNotFound) {
			writeNotHostedHere(w)
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var sess *Session
	var err error
	if req.Name != nil {
		sess, err = s.Rename(r.Context(), id, *req.Name)
	}
	if err == nil && req.LoginMode != nil {
		sess, err = s.SetLoginMode(r.Context(), id, *req.LoginMode)
	}
	switch {
	case err != nil || req.VMSize == nil:
	case req.RestartNow != nil && !*req.RestartNow:
		sess, err = s.SaveVMSize(r.Context(), id, *req.VMSize)
	default:
		sess, err = s.ResizeVM(r.Context(), id, *req.VMSize)
	}
	if err == nil && sess == nil {
		sess, err = s.Get(r.Context(), id)
	}
	if errors.Is(err, ErrNotFound) {
		writeNotHostedHere(w)
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
		writeNotHostedHere(w)
		return
	}
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Service) handleStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Stop(r.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeNotHostedHere(w)
			return
		}
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EditSharedFoldersRequest is the body shape of PUT /api/sessions/{id}/shared-folders.
// We accept the whole new list (not a diff) so the wire format matches what the UI's edit
// panel renders: fewer round-trips to keep in sync, simpler conflict semantics.
type EditSharedFoldersRequest struct {
	SharedFolders []SharedFolderRequest `json:"shared_folders"`
}

func (s *Service) handleEditSharedFolders(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req EditSharedFoldersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	sess, err := s.EditSharedFolders(r.Context(), id, req.SharedFolders)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeNotHostedHere(w)
			return
		}
		if errors.Is(err, ErrSharedFolderInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Service) handleDestroy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Destroy(r.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeNotHostedHere(w)
			return
		}
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
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
		writeNotHostedHere(w)
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
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
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
	// is.
	codeRuntimeUnavailable = "runtime_unavailable"
	codeNotFound           = "not_found"
	codeConflict           = "conflict"
	// codeImageUnavailable means the session's image could not be fetched.
	codeImageUnavailable = "image_unavailable"
	codeInternal         = "internal"
	// codeNotHostedHere means this node does not have that session. It may still exist on another node.
	codeNotHostedHere = "not_hosted_here"
	// codeNodeUnreachable means the node hosting the session did not answer.
	codeNodeUnreachable = "node_unreachable"
)

// writeNotHostedHere replies when this node does not have the session. On a single node that is the
// whole story, so the message stays plain.
func writeNotHostedHere(w http.ResponseWriter) {
	writeErrorCode(w, http.StatusNotFound, codeNotHostedHere, "session not found")
}

func writeErrorCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// writeSandboxError maps sandbox sentinels to an HTTP status + stable code so the SPA never sees a
// raw backend error for the cases it can act on. Non-sentinel errors (validation, unknown agent,
// and, today, internal failures) default to 400.
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
		writeErrorCode(w, http.StatusServiceUnavailable, codeRuntimeUnavailable, "This runtime isn't responding.")
	case errors.Is(err, ErrNotFound) || errors.Is(err, sandbox.ErrNotFound):
		writeErrorCode(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, sandbox.ErrConflict):
		writeErrorCode(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, sandbox.ErrImageUnavailable):
		writeErrorCode(w, http.StatusBadGateway, codeImageUnavailable, err.Error())
	default:
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, err.Error())
	}
}
