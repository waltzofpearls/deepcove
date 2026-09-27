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

// startAgentSession starts the agent for one session, in its own tmux session and working directory.
//
// Idempotent: an existing tmux session is left alone, which makes Start safe to repeat.
func (s *Service) startAgentSession(ctx context.Context, sess *Session, workdir string) error {
	// A stop before the agent exists kills nothing, so starting it now would undo the stop.
	if current, err := s.Get(ctx, sess.ID); err == nil && current.Status == StatusStopped {
		return fmt.Errorf("session: not starting the agent, %s is stopped", sess.ID)
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

// agentLaunchCommand starts one session's agent with the environment that belongs to the session rather
// than to the machine. Two sessions on one machine can differ because of this.
func (s *Service) agentLaunchCommand(sess *Session, runtime sandbox.Sandbox) ([]string, error) {
	proxyURL := s.LLMProxyURL()
	proxied := sess.UseLLMProxy && proxyURL != ""
	// env's options have to come before any assignment, or env reads them as the command to run.
	//
	// Cleared either way. Off has to mean off, whatever the machine, the image or a setup script left
	// behind, and a machine built before this setting existed still holds the address on its init.
	argv := []string{"env", "-u", "ANTHROPIC_API_KEY", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL"}
	argv = append(argv, "DEEPCOVE_SESSION_ID="+sess.ID)
	if proxied {
		argv = append(argv,
			"ANTHROPIC_BASE_URL="+llmProxyEndpoint(runtime, proxyURL),
			// Carries the session id, which is how the host tells apart sessions sharing a machine. The
			// proxy never sees it: it is replaced on the way out.
			"ANTHROPIC_AUTH_TOKEN="+llmproxy.AuthToken(sess.ID))
	}
	provisioner, ok := runtime.(sandbox.AgentProvisioner)
	if !ok {
		return nil, fmt.Errorf("session: the %s runtime cannot say what starts its agent", sess.Runtime)
	}
	launch, err := provisioner.AgentLaunchCommand(sandbox.ID(sess.SandboxID))
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
	mounts, err := validateMountRequests(sess.Mounts)
	if err != nil {
		return ""
	}
	return provisioner.WorkDir(sess.ID, mounts)
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
