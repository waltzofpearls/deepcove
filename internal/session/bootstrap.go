package session

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Sessions created before this slice landed have no status file in the container; they
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

	// Each read gets its own budget. Sharing one made the supplementary log read starve the status
	// read, so a slow log turned into a spurious status change and a flickering spinner.
	// A VM exec carries more setup than a container exec, so the budget is per call rather than shared.
	// Default to "installing", NOT "ready": the frontend gates the terminal attach on this, so claiming
	// ready without evidence makes it attach before the agent's tmux session exists. The attach then
	// fails and the user is left on a dead terminal until they switch views and force a re-attach.
	//
	// A failed read is exactly when this happens, because the sandbox is busiest while it is still
	// starting up. Staying on "installing" just keeps the spinner up for another poll.
	status := BootstrapInstalling
	statusCtx, cancelStatus := context.WithTimeout(ctx, bootstrapReadTimeout)
	defer cancelStatus()
	if raw, ok := s.execCapture(statusCtx, sess, []string{"cat", bootstrapStatusPath}); ok {
		trimmed := strings.TrimSpace(string(raw))
		switch BootstrapStatus(trimmed) {
		case BootstrapInstalling, BootstrapReady, BootstrapErrored:
			status = BootstrapStatus(trimmed)
		}
	}

	var log string
	logCtx, cancelLog := context.WithTimeout(ctx, bootstrapReadTimeout)
	defer cancelLog()
	if raw, ok := s.execCapture(logCtx, sess, []string{"tail", "-c", "8192", bootstrapLogPath}); ok {
		log = string(raw)
	}
	return &BootstrapResponse{Status: status, Log: log}, nil
}

// bootstrapReadTimeout bounds each bootstrap read. Generous enough for a VM exec, which pays a vsock
// dial and a process spawn, while still returning promptly when a sandbox is wedged.
const bootstrapReadTimeout = 5 * time.Second

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
