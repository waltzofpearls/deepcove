package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// A VM's registry lives in this process's memory, so restarting deepcove destroys every VM while every
// session row survives saying "running". Nothing then reconciles the two: the session page waits forever
// on a bootstrap probe that can never answer, and Start fails because the driver has never heard of the
// handle. Container sessions do not have this problem, because Docker outlives us.
//
// The pair of operations here closes that gap. Reconcile tells the truth at startup, and rebuilding a
// vanished sandbox on Start makes the session usable again instead of only diagnosable.

// Reconcile marks a session stopped when its runtime no longer has the sandbox the row points at, and
// returns those sessions. They were running a moment ago, so the caller can bring them back.
//
// Only runtimes that can enumerate their sandboxes are considered. "The driver cannot tell us" is not
// "the sandbox is gone", and guessing would stop sessions that are running fine.
func (s *Service) Reconcile(ctx context.Context) ([]*Session, error) {
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return nil, err
	}

	// One listing per runtime rather than one per session.
	live := make(map[string]map[string]bool, len(s.runtimes))
	for name, driver := range s.runtimes {
		lister, ok := driver.(sandbox.Lister)
		if !ok {
			continue
		}
		instances, err := lister.ListSandboxes(ctx)
		if err != nil {
			// A runtime we cannot read is not a runtime whose sessions are gone. Skip it: a wrong
			// "stopped" here would be a worse outcome than a stale "running".
			s.logger.Warn("skipping reconcile for a runtime that could not be listed", "runtime", name, "err", err)
			continue
		}
		handles := make(map[string]bool, len(instances))
		for _, inst := range instances {
			handles[string(inst.Handle)] = true
		}
		live[name] = handles
	}

	now := time.Now().UTC()
	var lost []*Session
	for _, sess := range sessions {
		if sess.Status != StatusRunning {
			continue
		}
		handles, known := live[sess.Runtime]
		if !known || handles[sess.ContainerID] {
			continue
		}
		if err := s.repo.MarkStopped(ctx, placeholderOwner, sess.ID, now); err != nil {
			return nil, fmt.Errorf("session: reconcile %s: %w", sess.ID, err)
		}
		s.logger.Info("session marked stopped: its sandbox is gone",
			"id", sess.ID, "name", sess.Name, "runtime", sess.Runtime, "handle", sess.ContainerID)
		lost = append(lost, sess)
	}
	return lost, nil
}

// RestartSessions starts sessions again, one at a time. Used at boot to bring back what the previous
// process was running: a VM dies with that process, so otherwise every VM session needs a manual Start.
//
// Sequential on purpose. Booting VMs in parallel spikes memory right when the node is starting up.
// A session that fails to come back is logged and left stopped; one bad session must not block the rest.
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
func (s *Service) rebuildSandbox(ctx context.Context, sess *Session, runtime sandbox.Sandbox) (sandbox.Handle, error) {
	userMounts, err := validateMountRequests(sess.Mounts)
	if err != nil {
		return "", err
	}
	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return "", fmt.Errorf("session: %w", err)
	}

	handle, err := runtime.Create(ctx, s.buildSandboxSpec(sess, agent, userMounts))
	if err != nil {
		return "", fmt.Errorf("session: rebuild sandbox: %w", err)
	}
	if err := runtime.Start(ctx, handle); err != nil {
		_ = runtime.Destroy(context.Background(), handle)
		return "", fmt.Errorf("session: start rebuilt sandbox: %w", err)
	}

	// Reuses the mounts+container update, which exists for the same reason: the sandbox instance changes
	// while the row, and above all its id and name, is preserved.
	if err := s.repo.UpdateMountsAndContainer(ctx, placeholderOwner, sess.ID, sess.Mounts, string(handle), time.Now().UTC()); err != nil {
		return "", fmt.Errorf("session: record rebuilt sandbox: %w", err)
	}
	s.logger.Info("sandbox rebuilt for a session that outlived it",
		"id", sess.ID, "name", sess.Name, "runtime", sess.Runtime, "handle", handle)
	return handle, nil
}

// sandboxVanished reports whether an error means the driver has no such sandbox, as opposed to a real
// failure to start one it does have. Only the first case is safe to recover from by building a new one.
func sandboxVanished(err error, containerID string) bool {
	return containerID == "" || errors.Is(err, sandbox.ErrNotFound)
}
