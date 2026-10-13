package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// RestartSandbox stops and starts a sandbox with the settings currently saved for it, so an edited size
// or folder takes effect. Only VM runtimes can do this.
//
// Every session on it loses its agent when the machine reboots, so each one reads as starting until its
// agent is back.
// restartTimeout bounds a restart that has begun. Generous: it covers stopping a busy guest and booting
// it again, and pulling an image the node has not seen.
const restartTimeout = 5 * time.Minute

func (s *Service) RestartSandbox(ctx context.Context, runtimeName, sandboxID string) error {
	runtime, err := s.runtimeByName(runtimeName)
	if err != nil {
		return err
	}
	restarter, ok := runtime.(sandbox.Restarter)
	if !ok {
		return fmt.Errorf("%w: only a virtual machine can be restarted", ErrInvalidInput)
	}
	spec, err := s.savedSpecFor(ctx, sandboxID)
	if err != nil {
		return err
	}
	riders, err := s.sessionsOnSandbox(ctx, sandboxID)
	if err != nil {
		return err
	}
	// Only the ones that were up. A session you stopped must not come back because it happens to share a
	// machine with one that did not.
	riders = filterRunningSessionsOnly(riders)
	release := s.markStarting(riders)
	// Once it has begun, the restart belongs to the node rather than to the request. A browser that
	// navigates away would otherwise cancel it mid cycle, leaving the machine stopped with nothing left
	// to start it and a session row that still says running.
	restartCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartTimeout)
	defer cancel()
	if err := restarter.Restart(restartCtx, sandbox.ID(sandboxID), spec); err != nil {
		release()
		return err
	}
	// The VM answers before its setup has finished, and tmux exists only once it has.
	s.background(func() {
		defer release()
		for _, sess := range riders {
			s.ensureAgentAfterSetup(sess, s.sessionWorkDir(sess))
		}
	})
	return nil
}

// markStarting reports these sessions as starting until the returned function is called.
func (s *Service) markStarting(list []*Session) func() {
	dones := make([]func(), 0, len(list))
	for _, sess := range list {
		if done, ok := s.beginStarting(sess.ID); ok {
			dones = append(dones, done)
		}
	}
	return func() {
		for _, done := range dones {
			done()
		}
	}
}

// savedSpecFor rebuilds the spec from what is stored, which is the point of a restart: the running
// machine has the old settings and the record has the new ones.
func (s *Service) savedSpecFor(ctx context.Context, sandboxID string) (sandbox.SandboxSpec, error) {
	if s.workspaceVM != nil {
		if vm, err := s.workspaceVM.Load(ctx); err == nil && vm.SandboxID == sandboxID {
			agent, err := resolveAgent(defaultAgent)
			if err != nil {
				return sandbox.SandboxSpec{}, fmt.Errorf("session: %w", err)
			}
			return workspaceVMSpec(vm, agent, hostWorkspaceVMSizes())
		}
	}

	sess, err := s.sessionBySandbox(ctx, sandboxID)
	if err != nil {
		return sandbox.SandboxSpec{}, err
	}
	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return sandbox.SandboxSpec{}, fmt.Errorf("session: %w", err)
	}
	userMounts, err := s.prepareMountsRequiringFolders(sess.Mounts)
	if err != nil {
		return sandbox.SandboxSpec{}, err
	}
	runtime, err := s.runtimeByName(sess.Runtime)
	if err != nil {
		return sandbox.SandboxSpec{}, err
	}
	return s.buildSandboxSpec(sess, agent, userMounts, runtime), nil
}

// filterRunningSessionsOnly keeps the sessions whose row says running.
func filterRunningSessionsOnly(list []*Session) []*Session {
	return slices.DeleteFunc(list, func(sess *Session) bool { return sess.Status != StatusRunning })
}

// sessionsOnSandbox lists the sessions a sandbox hosts. A workspace VM hosts several.
func (s *Service) sessionsOnSandbox(ctx context.Context, sandboxID string) ([]*Session, error) {
	list, err := s.repo.List(ctx, placeholderOwner, true)
	if err != nil {
		return nil, err
	}
	var on []*Session
	for _, sess := range list {
		if sess.SandboxID == sandboxID {
			on = append(on, sess)
		}
	}
	return on, nil
}

// sessionBySandbox finds the session a sandbox belongs to.
func (s *Service) sessionBySandbox(ctx context.Context, sandboxID string) (*Session, error) {
	on, err := s.sessionsOnSandbox(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	if len(on) == 0 {
		return nil, fmt.Errorf("%w: no session owns sandbox %s", ErrNotFound, sandboxID)
	}
	return on[0], nil
}

func (s *Service) handleRestartSandbox(w http.ResponseWriter, r *http.Request) {
	err := s.RestartSandbox(r.Context(), r.PathValue("runtime"), r.PathValue("sandbox_id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidInput):
		writeErrorCode(w, http.StatusConflict, codeInvalidInput, err.Error())
	default:
		writeSandboxError(w, err)
	}
}
