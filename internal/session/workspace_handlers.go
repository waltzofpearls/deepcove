package session

import (
	"encoding/json"
	"errors"
	"net/http"
)

// handleGetWorkspaceVM serves GET /api/workspace-vm. 404 when this node has none, which is a normal
// state rather than a failure: the UI offers to create one.
func (s *Service) handleGetWorkspaceVM(w http.ResponseWriter, r *http.Request) {
	vm, err := s.WorkspaceVM(r.Context())
	if err != nil {
		writeWorkspaceVMError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vm)
}

// handleSaveWorkspaceVM serves PUT /api/workspace-vm: record the machine, or change it.
//
// The VM is not built here. Booting takes a minute, so it happens when the first session lands on it,
// where the UI already shows a machine setting up.
func (s *Service) handleSaveWorkspaceVM(w http.ResponseWriter, r *http.Request) {
	var req WorkspaceVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorCode(w, http.StatusBadRequest, codeInvalidInput, "invalid JSON body: "+err.Error())
		return
	}
	vm, err := s.SaveWorkspaceVM(r.Context(), req)
	if err != nil {
		writeWorkspaceVMError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vm)
}

// handleDeleteWorkspaceVM serves DELETE /api/workspace-vm: destroy the machine and forget it.
func (s *Service) handleDeleteWorkspaceVM(w http.ResponseWriter, r *http.Request) {
	// Opt in, so a plain DELETE stays the safe one: a caller that has not thought about the sessions on
	// the machine gets a 409 naming the problem.
	withSessions := r.URL.Query().Get("sessions") == "delete"
	if err := s.DeleteWorkspaceVM(r.Context(), withSessions); err != nil {
		writeWorkspaceVMError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeWorkspaceVMError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoWorkspaceVM):
		writeErrorCode(w, http.StatusNotFound, codeNotFound, "this node has no workspace VM")
	case errors.Is(err, ErrWorkspaceVMInUse):
		writeErrorCode(w, http.StatusConflict, codeConflict, err.Error())
	default:
		writeSandboxError(w, err)
	}
}
