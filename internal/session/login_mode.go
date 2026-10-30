package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/waltzofpearls/deepcove/internal/llmproxy"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
	"github.com/waltzofpearls/deepcove/internal/sharedlogin"
)

// loginModeOrDefault is the mode a new session gets. A default it cannot use falls back to its own
// login. A mode asked for that it cannot use is refused.
func (s *Service) loginModeOrDefault(asked LoginMode, agent AgentSpec, runtime sandbox.Sandbox, onWorkspaceVM bool) (LoginMode, error) {
	if asked != "" {
		return asked, s.checkLoginMode(asked, agent, runtime, onWorkspaceVM)
	}
	mode := LoginShared
	if s.sharedLoginStore != nil {
		var err error
		if mode, err = s.sharedLoginStore.DefaultLoginMode(agent.Name); err != nil {
			return "", err
		}
	}
	if s.checkLoginMode(mode, agent, runtime, onWorkspaceVM) != nil {
		return LoginInSession, nil
	}
	return mode, nil
}

func (s *Service) checkLoginMode(mode LoginMode, agent AgentSpec, runtime sandbox.Sandbox, onWorkspaceVM bool) error {
	if !slices.Contains(agent.LoginModes, mode) {
		return fmt.Errorf("%w: %s cannot log in with %q", ErrInvalidInput, agent.Name, mode)
	}
	_, forwards := runtime.(sandbox.LLMForwarder)
	switch mode {
	case LoginShared:
		// The workspace VM's login is the machine's, so any session on it may ask for it.
		if !forwards && !onWorkspaceVM {
			return fmt.Errorf("%w: a session with no sandbox uses the login already on its machine", ErrInvalidInput)
		}
	case LoginWithAPIKey:
		if !forwards {
			return fmt.Errorf("%w: a session with no sandbox cannot use the saved API key", ErrInvalidInput)
		}
		key := ""
		if s.sharedLoginStore != nil {
			var err error
			if key, err = s.sharedLoginStore.APIKey(agent.Name); err != nil {
				return err
			}
		}
		if key == "" {
			return fmt.Errorf("%w: no API key is saved. Add one in Settings", ErrInvalidInput)
		}
	case LoginWithLLMProxy:
		proxy := s.LLMProxy()
		if proxy.URL == "" {
			return fmt.Errorf("%w: this node has no LLM proxy. Add one in Settings", ErrInvalidInput)
		}
		// With no sandbox the agent calls the proxy itself, so nothing on the way can add them.
		if !forwards && len(proxy.Headers) > 0 {
			return fmt.Errorf("%w: a session with no sandbox can't send the LLM proxy's headers", ErrInvalidInput)
		}
	}
	return nil
}

func (s *Service) loginModeChangeable(sess *Session, mode LoginMode) error {
	// A start launches the agent itself, from the mode it read when it began.
	if sess.Status == StatusStarting {
		return fmt.Errorf("%w: the session is starting. Try again in a moment", ErrInvalidInput)
	}
	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return err
	}
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return err
	}
	return s.checkLoginMode(mode, agent, runtime, sess.OnWorkspaceVM)
}

// changesAgentEnv says whether a change needs the agent restarted. Shared and its own login differ only
// in files the guest manages.
func changesAgentEnv(from, to LoginMode) bool {
	routed := func(mode LoginMode) bool { return mode == LoginWithAPIKey || mode == LoginWithLLMProxy }
	return from != to && (routed(from) || routed(to))
}

// loginModeChangeTimeout bounds one change, including the wait for the shared login.
const loginModeChangeTimeout = time.Minute

// SetLoginMode changes how one session logs in. A change to or from the API key or the LLM proxy
// restarts the agent.
func (s *Service) SetLoginMode(ctx context.Context, id string, mode LoginMode) (*Session, error) {
	// Held for the whole change, so nothing starts the agent between its stop and its start.
	unlock, err := s.lockAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// From here the change belongs to the node, so a browser that gives up cannot leave it half done.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginModeChangeTimeout)
	defer cancel()
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.LoginMode == mode {
		return sess, nil
	}
	if err := s.loginModeChangeable(sess, mode); err != nil {
		return nil, err
	}
	previous := sess.LoginMode
	if err := s.repo.UpdateLoginMode(ctx, placeholderOwner, id, mode, time.Now().UTC()); err != nil {
		return nil, err
	}
	sess.LoginMode = mode
	if sess.Status != StatusRunning || !changesAgentEnv(previous, mode) {
		return sess, nil
	}
	// The row has to match the running agent, so a change that does not take goes back.
	putBack := func() {
		sess.LoginMode = previous
		if err := s.repo.UpdateLoginMode(ctx, placeholderOwner, id, previous, time.Now().UTC()); err != nil {
			s.logger.Error("could not undo the login mode change", "id", id, "err", err)
		}
	}
	if err := s.stopAgentSessionLocked(ctx, sess); err != nil {
		putBack()
		// The kill may have worked even if the check could not tell. Starting it the old way does nothing to
		// a running agent and brings back a killed one.
		if rerr := s.startAgentSessionLocked(ctx, sess, s.sessionWorkDir(sess)); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return nil, fmt.Errorf("session: could not change how it logs in, the agent did not stop: %w", err)
	}
	if mode == LoginShared {
		s.waitForSharedLogin(ctx, sess)
	}
	if err := s.startAgentSessionLocked(ctx, sess, s.sessionWorkDir(sess)); err != nil {
		// The old agent is gone, so start it the way it was rather than leave none.
		putBack()
		if rerr := s.startAgentSessionLocked(ctx, sess, s.sessionWorkDir(sess)); rerr != nil {
			return nil, fmt.Errorf("session: the agent did not restart: %w", errors.Join(err, rerr))
		}
		return nil, fmt.Errorf("session: could not change how it logs in, the agent was restarted unchanged: %w", err)
	}
	return sess, nil
}

// sharesLogin says whether a session uses the shared login now. The workspace VM has no session of its
// own, so it follows the node's default.
func (s *Service) sharesLogin(agent, sessionID string) (bool, error) {
	if sessionID == workspaceVMHomeDiskID {
		mode, err := s.sharedLoginStore.DefaultLoginMode(agent)
		return mode == LoginShared, err
	}
	if mode, ok := s.creatingLoginModes.Load(sessionID); ok {
		return mode == LoginShared, nil
	}
	sess, err := s.repo.Get(context.Background(), placeholderOwner, sessionID)
	if err != nil {
		return false, err
	}
	return sess.LoginMode == LoginShared, nil
}

// APIKeyFor is the key the host adds to requests from session sessionID on VM sandboxID, with the
// provider of that session's agent. The key is empty when that session does not use one. A session on
// another VM is refused, so no VM can spend the key.
func (s *Service) APIKeyFor(sandboxID sandbox.ID, sessionID string) (llmproxy.APIKey, error) {
	sess, err := s.repo.Get(context.Background(), placeholderOwner, sessionID)
	if err != nil {
		return llmproxy.APIKey{}, err
	}
	if sess.SandboxID != string(sandboxID) {
		return llmproxy.APIKey{}, fmt.Errorf("session %s does not run on this VM", sessionID)
	}
	if sess.LoginMode != LoginWithAPIKey {
		return llmproxy.APIKey{}, nil
	}
	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return llmproxy.APIKey{}, err
	}
	key, err := s.sharedLoginStore.APIKey(sess.Agent)
	if err != nil {
		return llmproxy.APIKey{}, err
	}
	if key == "" {
		return llmproxy.APIKey{}, errors.New("no API key is saved. Add one in Settings")
	}
	return llmproxy.APIKey{Key: key, Provider: agent.API.Provider}, nil
}

// waitForSharedLogin waits, up to a limit, for a VM back on sharing to get the login before its agent
// starts. An agent started without it asks the user to log in.
func (s *Service) waitForSharedLogin(ctx context.Context, sess *Session) {
	if sess.OnWorkspaceVM || s.sharedLogins == nil {
		return
	}
	if status, err := s.sharedLogins.Status(sess.Agent); err != nil || !status.LoggedIn {
		return
	}
	files := sharedlogin.LoginFilesFor(sess.Agent, sandbox.SandboxedAgentHome)
	if files == nil {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.sharedLoginWaitLimit)
	defer cancel()
	for {
		if _, ok := s.execCapture(waitCtx, sess, []string{"test", "-s", files.TokenPath()}); ok {
			return
		}
		select {
		case <-waitCtx.Done():
			s.logger.Warn("starting the agent before the shared login reached it", "id", sess.ID)
			return
		case <-time.After(s.sharedLoginWaitEvery):
		}
	}
}
