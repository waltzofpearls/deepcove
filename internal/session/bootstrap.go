package session

import (
	"context"
	"errors"
	"net/http"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// BootstrapStatus is how far a session's agent setup has got, which the loading overlay shows. A
// runtime that cannot say reads as installing, never ready (see bootstrapOf).
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

func (s *Service) Bootstrap(ctx context.Context, id string) (*BootstrapResponse, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// Stopped/errored sessions report ready so the UI doesn't sit on a loading spinner; those states
	// have their own UI elsewhere. A starting session is asked, because its machine is coming up.
	if sess.Status != StatusRunning && sess.Status != StatusStarting {
		return &BootstrapResponse{Status: BootstrapReady}, nil
	}
	return s.bootstrapOf(ctx, sess), nil
}

// bootstrapOf asks the runtime how far the agent's setup has got.
//
// A runtime that cannot answer reads as installing, never ready. The UI gates the terminal attach on
// this, and attaching before the agent's tmux session exists leaves a dead terminal.
func (s *Service) bootstrapOf(ctx context.Context, sess *Session) *BootstrapResponse {
	provisioner, err := s.agentProvisionerFor(sess)
	if err != nil {
		return &BootstrapResponse{Status: BootstrapInstalling}
	}
	state, err := provisioner.Setup(ctx, sandbox.ID(sess.SandboxID))
	if err != nil {
		return &BootstrapResponse{Status: BootstrapInstalling}
	}
	status := BootstrapInstalling
	switch state.Status {
	case sandbox.SetupReady:
		status = BootstrapReady
	case sandbox.SetupFailed:
		status = BootstrapErrored
	}
	return &BootstrapResponse{Status: status, Log: state.Log}
}

func (s *Service) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.Bootstrap(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeNotHostedHere(w)
		return
	}
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
