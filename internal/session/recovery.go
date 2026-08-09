package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// A VM's registry lives in this process's memory, so restarting deepcove destroys every VM while every
// session row survives saying "running". Nothing then squares the two: the session page waits forever
// on a bootstrap probe that can never answer, and Start fails because the driver has never heard of the
// sandbox. Container sessions do not have this problem, because Docker outlives us.
//
// The pair of operations here closes that gap. MarkLostSessions tells the truth at startup, and
// rebuilding a vanished sandbox on Start makes the session usable again instead of only diagnosable.

// MarkLostSessions marks a session stopped when its runtime no longer has the sandbox the row points
// at, and returns those sessions. They were running a moment ago, so the caller can bring them back.
//
// Only runtimes that can enumerate their sandboxes are considered. "The driver cannot tell us" is not
// "the sandbox is gone", and guessing would stop sessions that are running fine.
func (s *Service) MarkLostSessions(ctx context.Context) ([]*Session, error) {
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return nil, err
	}

	// One listing per runtime rather than one per session.
	alive := make(map[string]map[string]bool, len(s.runtimes))
	for name, driver := range s.runtimes {
		lister, ok := driver.(sandbox.Lister)
		if !ok {
			continue
		}
		instances, err := lister.ListSandboxes(ctx)
		if err != nil {
			// A runtime we cannot read is not a runtime whose sessions are gone. Skip it: a wrong
			// "stopped" here would be a worse outcome than a stale "running".
			s.logger.Warn("skipping lost-session check for a runtime that could not be listed", "runtime", name, "err", err)
			continue
		}
		sandboxes := make(map[string]bool, len(instances))
		for _, inst := range instances {
			sandboxes[string(inst.ID)] = true
		}
		alive[name] = sandboxes
	}

	now := time.Now().UTC()
	var lost []*Session
	for _, sess := range sessions {
		if sess.Status != StatusRunning {
			continue
		}
		sandboxes, known := alive[sess.Runtime]
		if !known || sandboxes[sess.SandboxID] {
			continue
		}
		if err := s.repo.MarkStopped(ctx, placeholderOwner, sess.ID, now); err != nil {
			return nil, fmt.Errorf("session: mark %s lost: %w", sess.ID, err)
		}
		s.logger.Info("session marked stopped: its sandbox is gone",
			"id", sess.ID, "name", sess.Name, "runtime", sess.Runtime, "sandbox", sess.SandboxID)
		lost = append(lost, sess)
	}
	return lost, nil
}

// RestartSessions starts sessions again at boot: a VM dies with the process that ran it.
//
// One at a time, so booting VMs does not spike memory. A session that fails is logged and left
// stopped. Sessions on the shared machine land back on one machine, because starting the first one
// builds it and the rest join what is already up.
func (s *Service) RestartSessions(ctx context.Context, sessions []*Session) {
	for _, sess := range sessions {
		if err := s.Start(ctx, sess.ID); err != nil {
			s.logger.Warn("could not restart session", "id", sess.ID, "name", sess.Name, "err", err)
			continue
		}
		s.logger.Info("session restarted", "id", sess.ID, "name", sess.Name)
	}
}

// rebuildSandbox creates a fresh sandbox for a session whose old one no longer exists, and points the row
// at it. The session keeps its id, name, mounts and size; what it loses is whatever lived in the previous
// writable layer, which for a VM died with the VM regardless.
func (s *Service) rebuildSandbox(ctx context.Context, sess *Session, runtime sandbox.Sandbox) (sandbox.ID, error) {
	userMounts, err := validateMountRequests(sess.Mounts)
	if err != nil {
		return "", err
	}
	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return "", fmt.Errorf("session: %w", err)
	}

	sandboxID, err := runtime.Create(ctx, s.buildSandboxSpec(sess, agent, userMounts, runtime))
	if err != nil {
		return "", fmt.Errorf("session: rebuild sandbox: %w", err)
	}
	// Point the row at the new sandbox before booting it: booting takes seconds, and a sandbox no row
	// claims is reported as a leak.
	if err := s.repo.UpdateMountsAndSandbox(ctx, placeholderOwner, sess.ID, sess.Mounts, string(sandboxID), time.Now().UTC()); err != nil {
		return "", fmt.Errorf("session: record rebuilt sandbox: %w", err)
	}
	// Callers hold this Session, so point it at the new machine too.
	sess.SandboxID = string(sandboxID)

	// A failed start leaves a dead sandbox id on the row, which the next Start rebuilds from.
	if err := runtime.Start(ctx, sandboxID); err != nil {
		_ = runtime.Destroy(context.Background(), sandboxID)
		return "", fmt.Errorf("session: start rebuilt sandbox: %w", err)
	}
	s.logger.Info("sandbox rebuilt for a session that outlived it",
		"id", sess.ID, "name", sess.Name, "runtime", sess.Runtime, "sandbox", sandboxID)
	return sandboxID, nil
}

// sandboxVanished reports whether an error means the driver has no such sandbox, as opposed to a real
// failure to start one it does have. Only the first case is safe to recover from by building a new one.
func sandboxVanished(err error, sandboxID string) bool {
	return sandboxID == "" || errors.Is(err, sandbox.ErrNotFound)
}
