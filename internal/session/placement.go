package session

import (
	"context"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// A sandbox can host several sessions, so a sandbox's lifecycle is no longer a session's lifecycle:
// stopping or deleting one session must leave a machine the others are still using alone.

// sessionsOn returns the sessions sharing a sandbox.
func (s *Service) sessionsOn(ctx context.Context, sandboxID string) ([]*Session, error) {
	if sandboxID == "" {
		return nil, nil
	}
	all, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return nil, err
	}
	var out []*Session
	for _, sess := range all {
		if sess.SandboxID == sandboxID {
			out = append(out, sess)
		}
	}
	return out, nil
}

// sandboxIsShared reports whether a session's sandbox is also hosting another session.
//
// Errs towards shared: a listing that failed is no reason to tear down a machine that might be in use.
func (s *Service) sandboxIsShared(ctx context.Context, sess *Session) bool {
	others, err := s.sessionsOn(ctx, sess.SandboxID)
	if err != nil {
		s.logger.Warn("could not tell whether the sandbox is shared", "id", sess.ID, "err", err)
		return true
	}
	for _, other := range others {
		if other.ID != sess.ID {
			return true
		}
	}
	return false
}

// sandboxRunning reports whether the session's sandbox is already up, which is the case when another
// session is using it. Then starting this session is just starting its agent.
func (s *Service) sandboxRunning(ctx context.Context, sess *Session) bool {
	if sess.SandboxID == "" {
		return false
	}
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return false
	}
	return sandboxRunningOn(ctx, runtime, sess.SandboxID)
}

// sandboxRunningOn reports whether that runtime still has the sandbox, and has it up. A driver that
// cannot enumerate answers no: it cannot tell us, and building a new machine is the safer wrong guess.
func sandboxRunningOn(ctx context.Context, runtime sandbox.Sandbox, sandboxID string) bool {
	lister, ok := runtime.(sandbox.Lister)
	if !ok {
		return false
	}
	instances, err := lister.ListSandboxes(ctx)
	if err != nil {
		return false
	}
	for _, inst := range instances {
		if string(inst.ID) == sandboxID {
			return inst.State == sandbox.InstanceRunning
		}
	}
	return false
}
