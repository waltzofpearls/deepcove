package session

import (
	"context"
	"fmt"
	"time"
)

// A machine can host more than one session, so preparing the machine and starting an agent are separate
// steps. The entrypoint does the first, once per sandbox. This does the second, once per session, which
// is what lets a session join a sandbox that booted long ago.

// agentLaunchPath is the script the entrypoint writes and this runs. A constant so the two cannot drift.
const agentLaunchPath = "/usr/local/bin/deepcove-agent-launch"

// tmuxSessionName is the tmux session an agent runs in, one per deepcove session.
//
// Derived from the id rather than stored: it has to be computable from a Session alone, by attach,
// preview and the window APIs, without another round trip. It was the constant "main" while a machine
// held exactly one session.
func tmuxSessionName(sessionID string) string { return "deepcove-" + sessionID }

// startAgentSession starts the agent for one session, in its own tmux session and working directory.
//
// Idempotent: an existing tmux session is left alone. That makes Start safe to repeat, and it heals a
// sandbox that predates this change, where the entrypoint had already created one.
func (s *Service) startAgentSession(ctx context.Context, sess *Session, workdir string) error {
	name := tmuxSessionName(sess.ID)
	if _, err := s.execTmux(ctx, sess, "has-session", "-t", name); err == nil {
		return nil
	}

	// tmux refuses to start in a directory that does not exist, and on the shared machine this folder
	// is the session's own, created here on first use.
	if _, ok := s.execCapture(ctx, sess, []string{"mkdir", "-p", workdir}); !ok {
		return fmt.Errorf("session: could not create %s", workdir)
	}

	newSession := []string{
		"new-session", "-d",
		"-s", name,
		"-n", AgentTmuxWindowName,
		"-c", workdir,
		// env rather than a shell: nothing parses a string we built, and it works on any tmux version.
		"env", "DEEPCOVE_SESSION_ID=" + sess.ID, agentLaunchPath,
	}
	if _, err := s.execTmux(ctx, sess, newSession...); err != nil {
		return fmt.Errorf("session: start agent: %w", err)
	}

	// Mouse reporting is a server-wide option, so it needs a server, which the first session creates.
	// It is what makes the browser's wheel scroll the pane; tmux's own defaults handle the rest.
	_, _ = s.execTmux(ctx, sess, "set-option", "-g", "mouse", "on")
	return nil
}

// sessionWorkdir is the directory this session's agent works in: its own folder on the shared machine,
// else the first mount, else /workspace.
func sessionWorkdir(sess *Session) string {
	if sess.WorkDir != "" {
		return sess.WorkDir
	}
	userMounts, err := validateMountRequests(sess.Mounts)
	if err != nil {
		return workspaceDir
	}
	return workdirFor(userMounts)
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
		resp, err := s.Bootstrap(ctx, sess.ID)
		switch {
		case err != nil:
			s.logger.Warn("could not read setup status", "id", sess.ID, "err", err)
		case resp.Status == BootstrapErrored:
			s.logger.Warn("machine setup failed, so no agent was started", "id", sess.ID)
			return
		case resp.Status == BootstrapReady:
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
