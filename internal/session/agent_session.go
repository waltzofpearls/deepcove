package session

import (
	"context"
	"fmt"
	"time"

	"github.com/waltzofpearls/deepcove/internal/llmproxy"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
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

	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return err
	}
	newSession := []string{
		"new-session", "-d",
		"-s", name,
		"-n", AgentTmuxWindowName,
		"-c", workdir,
	}
	// env rather than a shell: nothing parses a string we built, and it works on any tmux version.
	newSession = append(newSession, s.agentLaunchCommand(sess, runtime)...)
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
func (s *Service) agentLaunchCommand(sess *Session, runtime sandbox.Sandbox) []string {
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
	return append(argv, agentLaunchPath)
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
