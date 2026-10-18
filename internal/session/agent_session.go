package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/waltzofpearls/deepcove/internal/llmproxy"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// A machine can host more than one session, so preparing the machine and starting an agent are separate
// steps. The entrypoint does the first, once per sandbox. This does the second, once per session, which
// is what lets a session join a sandbox that booted long ago.

// tmuxSessionName is the tmux session an agent runs in, one per deepcove session.
//
// Derived from the id rather than stored: it has to be computable from a Session alone, by attach,
// preview and the window APIs, without another round trip. It was the constant "main" while a machine
// held exactly one session.
func tmuxSessionName(sessionID string) string { return "deepcove-" + sessionID }

// lockAgent lets one caller at a time start, stop or restart a session's agent, so an attach cannot
// start one in the middle of a login change.
func (s *Service) lockAgent(ctx context.Context, sessionID string) (unlock func(), err error) {
	unlock, err = s.agentLocks.lock(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session: wait for the agent of %s: %w", sessionID, err)
	}
	return unlock, nil
}

// startAgentSession starts the agent for one session, in its own tmux session and working directory.
//
// Idempotent: an existing tmux session is left alone, which makes Start safe to repeat.
func (s *Service) startAgentSession(ctx context.Context, sess *Session, workdir string) error {
	unlock, err := s.lockAgent(ctx, sess.ID)
	if err != nil {
		return err
	}
	defer unlock()
	return s.startAgentSessionLocked(ctx, sess, workdir)
}

// startAgentSessionLocked is startAgentSession for a caller already holding lockAgent.
func (s *Service) startAgentSessionLocked(ctx context.Context, sess *Session, workdir string) error {
	current, err := s.Get(ctx, sess.ID)
	// A stop before the agent exists kills nothing, so starting it now would undo the stop.
	if err == nil && current.Status == StatusStopped {
		return fmt.Errorf("session: not starting the agent, %s is stopped", sess.ID)
	}
	// A start that waited for setup can hold a mode read before a login change.
	if err == nil && current.LoginMode != sess.LoginMode {
		fresh := *sess
		fresh.LoginMode = current.LoginMode
		sess = &fresh
	}
	name := tmuxSessionName(sess.ID)
	if _, err := s.execTmux(ctx, sess, "has-session", "-t", name); err == nil {
		return nil
	}
	// Nobody could say where this session works. Guessing would make a folder somewhere nobody asked
	// for, and on a machine with no sandbox that is a real path on the user's disk.
	if workdir == "" {
		return fmt.Errorf("session: the %s runtime cannot say which folder this session works in", sess.Runtime)
	}

	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return err
	}

	// deepcove creates the folder it chose for the session. If you chose the folder yourself and it is
	// no longer there, deepcove leaves it alone, and the agent tells you it is gone.
	if sess.WorkDir == "" || !workDirIsOnThisMachine(runtime) {
		if _, ok := s.execCapture(ctx, sess, []string{"mkdir", "-p", workdir}); !ok {
			return fmt.Errorf("session: could not create %s", workdir)
		}
	}
	newSession := []string{
		"new-session", "-d",
		"-s", name,
		"-n", AgentTmuxWindowName,
		"-c", workdir,
	}
	// env rather than a shell: nothing parses a string we built, and it works on any tmux version.
	launch, err := s.agentLaunchCommand(sess, runtime)
	if err != nil {
		return err
	}
	newSession = append(newSession, launch...)
	if _, err := s.execTmux(ctx, sess, newSession...); err != nil {
		return fmt.Errorf("session: start agent: %w", err)
	}

	// Mouse reporting is a server-wide option, so it needs a server, which the first session creates.
	// It is what makes the browser's wheel scroll the pane; tmux's own defaults handle the rest.
	_, _ = s.execTmux(ctx, sess, "set-option", "-g", "mouse", "on")
	return nil
}

// stopAgentSessionLocked stops a session's agent. Only tmux saying the session is gone counts, since
// anything else may leave the old agent running.
func (s *Service) stopAgentSessionLocked(ctx context.Context, sess *Session) error {
	name := tmuxSessionName(sess.ID)
	// Its own result proves nothing: it fails both when there was no agent and when tmux was not reached.
	_, _ = s.execTmux(ctx, sess, "kill-session", "-t", name)
	_, err := s.execTmux(ctx, sess, "has-session", "-t", name)
	if err == nil {
		return fmt.Errorf("session: the agent of %s is still running", sess.ID)
	}
	var tmuxSaidNo *tmuxExitError
	if errors.As(err, &tmuxSaidNo) {
		return nil
	}
	return fmt.Errorf("session: could not tell whether the agent of %s stopped: %w", sess.ID, err)
}

// agentLaunchCommand starts one session's agent with the environment that belongs to the session rather
// than to the machine. Two sessions on one machine can differ because of this.
func (s *Service) agentLaunchCommand(sess *Session, runtime sandbox.Sandbox) ([]string, error) {
	// The host forwards these, adding the user's API key on the way out so the key never enters the VM.
	baseURL := ""
	switch sess.LoginMode {
	case LoginWithLLMProxy:
		baseURL = llmProxyEndpoint(runtime, s.LLMProxyURL())
	case LoginWithAPIKey:
		if forwarder, ok := runtime.(sandbox.LLMForwarder); ok {
			baseURL = forwarder.LLMEndpoint()
		}
	}
	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return nil, err
	}
	// env's options have to come before any assignment, or env reads them as the command to run.
	//
	// Cleared either way. Off has to mean off, whatever the machine, the image or a setup script left
	// behind, and a machine built before this setting existed still holds the address on its init.
	argv := []string{"env"}
	for _, name := range agent.API.ClearEnv {
		argv = append(argv, "-u", name)
	}
	argv = append(argv, "DEEPCOVE_SESSION_ID="+sess.ID)
	if baseURL != "" {
		argv = append(argv,
			agent.API.BaseURLEnv+"="+baseURL,
			// Carries the session id, which is how the host tells apart sessions sharing a machine. The
			// proxy and the provider never see it: it is replaced on the way out.
			agent.API.TokenEnv+"="+llmproxy.AuthToken(sess.ID))
	}
	provisioner, ok := runtime.(sandbox.AgentProvisioner)
	if !ok {
		return nil, fmt.Errorf("session: the %s runtime cannot say what starts its agent", sess.Runtime)
	}
	launch, err := provisioner.AgentLaunchCommand(sandbox.ID(sess.SandboxID), sess.Agent)
	if err != nil {
		return nil, fmt.Errorf("session: what starts the agent: %w", err)
	}
	return append(argv, launch...), nil
}

// sessionWorkDir is the folder this session's agent works in: the one it was given, else whatever its
// runtime says. Empty when nothing can say, which callers refuse rather than paper over: /workspace is
// a folder inside a guest, and on a machine with no sandbox it would be one at the root of the disk.
func (s *Service) sessionWorkDir(sess *Session) string {
	if sess.WorkDir != "" {
		return sess.WorkDir
	}
	provisioner, err := s.agentProvisionerFor(sess)
	if err != nil {
		return ""
	}
	sharedFolders, err := validateSharedFolderRequests(sess.SharedFolders)
	if err != nil {
		return ""
	}
	return provisioner.WorkDir(sess.ID, sharedFolders)
}

// setupPollInterval is how often the machine is asked whether its setup has finished.
const setupPollInterval = 2 * time.Second

// setupWaitTimeout bounds that wait. Generous: a cold machine installs packages and the agent.
const setupWaitTimeout = 5 * time.Minute

// ensureAgentAfterSetup waits for the machine's setup to finish, then starts the agent.
//
// tmux only exists once setup has installed it, and setup can take a minute, so this runs in the
// background and Create returns without waiting. The UI already shows the machine setting up.
func (s *Service) ensureAgentAfterSetup(sess *Session, workdir string) {
	ctx, cancel := context.WithTimeout(context.Background(), setupWaitTimeout)
	defer cancel()

	for {
		// The session can be deleted or stopped while this waits. Deleted reads as still installing, so
		// the wait would run its full five minutes; stopped would end with this starting the agent for
		// a session the user just stopped.
		current, err := s.Get(ctx, sess.ID)
		if errors.Is(err, ErrNotFound) {
			return
		}
		if err == nil && current.Status == StatusStopped {
			return
		}

		// The machine is asked, not the row: the row reads as starting while this wait runs.
		switch s.bootstrapOf(ctx, sess).Status {
		case BootstrapErrored:
			s.logger.Warn("machine setup failed, so no agent was started", "id", sess.ID)
			return
		case BootstrapReady:
			if err := s.startAgentSession(ctx, sess, workdir); err != nil {
				s.logger.Warn("could not start the agent", "id", sess.ID, "err", err)
			}
			return
		}
		select {
		case <-ctx.Done():
			s.logger.Warn("gave up waiting for the machine to finish setting up", "id", sess.ID)
			return
		case <-time.After(setupPollInterval):
		}
	}
}
