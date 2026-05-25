package session

import (
	"encoding/json"
	"errors"
	"net/http"
)

// AttachRoutes registers the session API on mux. Routes follow
// Go 1.22's method-aware patterns so each verb has its own handler.
func (s *Service) AttachRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents", s.handleAgents)
	mux.HandleFunc("POST /api/sessions", s.handleCreate)
	mux.HandleFunc("GET /api/sessions", s.handleList)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGet)
	mux.HandleFunc("POST /api/sessions/{id}/start", s.handleStart)
	mux.HandleFunc("POST /api/sessions/{id}/stop", s.handleStop)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDestroy)
	mux.HandleFunc("GET /api/sessions/{id}/attach", s.handleAttach)
	mux.HandleFunc("GET /api/sessions/{id}/bootstrap", s.handleBootstrap)
	mux.HandleFunc("PUT /api/sessions/{id}/mounts", s.handleEditMounts)
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

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	sess, err := s.Create(r.Context(), req)
	if err != nil {
		// Mount validation failures and other user-input errors are
		// 400. The sandbox layer's mount-source validator (allowed
		// roots, abs path) also lands here as Create errors — those
		// are also user input, so 400 is right. Real server failures
		// (DB down, docker daemon gone) would benefit from 500 once
		// we have distinguishable sentinels; defer until needed.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	filter := ListFilter{IncludeAll: r.URL.Query().Get("all") == "true"}
	out, err := s.List(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []*Session{}
	}
	writeJSON(w, http.StatusOK, out)
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
// We accept the whole new mount list (not a diff) so the wire format
// matches what the UI's edit panel renders — fewer round-trips to
// keep in sync, simpler conflict semantics.
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
