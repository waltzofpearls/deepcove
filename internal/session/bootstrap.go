package session

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Sessions created before this slice landed have no status file in the container — they
// fall through to BootstrapReady so the loading overlay doesn't get stuck on them.
type BootstrapStatus string

const (
	BootstrapInstalling BootstrapStatus = "installing"
	BootstrapReady      BootstrapStatus = "ready"
	BootstrapErrored    BootstrapStatus = "errored"
)

type BootstrapResponse struct {
	Status BootstrapStatus `json:"status"`
	Log    string          `json:"log"`
}

// Paths written by buildEntrypoint in service.go.
const (
	bootstrapStatusPath = "/etc/deepcove-bootstrap-status"
	bootstrapLogPath    = "/var/log/deepcove-bootstrap.log"
)

func (s *Service) Bootstrap(ctx context.Context, id string) (*BootstrapResponse, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// Stopped/errored sessions report ready so the UI doesn't sit on a loading spinner;
	// the stopped/errored states have their own UI elsewhere.
	if sess.Status != StatusRunning {
		return &BootstrapResponse{Status: BootstrapReady}, nil
	}

	execCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	status := BootstrapReady
	if raw, ok := s.execCapture(execCtx, sess.ContainerID, []string{"cat", bootstrapStatusPath}); ok {
		trimmed := strings.TrimSpace(string(raw))
		switch BootstrapStatus(trimmed) {
		case BootstrapInstalling, BootstrapReady, BootstrapErrored:
			status = BootstrapStatus(trimmed)
		}
	}

	var log string
	if raw, ok := s.execCapture(execCtx, sess.ContainerID, []string{"tail", "-c", "8192", bootstrapLogPath}); ok {
		log = string(raw)
	}
	return &BootstrapResponse{Status: status, Log: log}, nil
}

func (s *Service) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.Bootstrap(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
