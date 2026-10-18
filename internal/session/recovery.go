package session

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// A VM's registry lives in this process's memory, so restarting deepcove destroys every VM while every
// session row survives saying "running". Nothing then squares the two: the session page waits forever
// on a bootstrap probe that can never answer, and Start fails because the driver has never heard of the
// sandbox. Sandboxless sessions do not have this problem, because they outlive us.
//
// The pair of operations here closes that gap. MarkLostSessions tells the truth at startup, and
// rebuilding a vanished sandbox on Start makes the session usable again instead of only diagnosable.

// ReconcileSessions squares the session table with what the runtimes are hosting, and returns the
// sessions to start again: those whose sandbox is gone, and those still waiting from before, on a runtime
// set up since or a restart a shutdown cut short. Startup runs it for every runtime, and a setup for the
// runtime it just added.
func (s *Service) ReconcileSessions(ctx context.Context, runtimeNames ...string) []*Session {
	if err := s.markSessionsOnMissingRuntimes(ctx); err != nil {
		s.logger.Warn("could not stop the sessions on a runtime that is not set up", "err", err)
	}
	if err := s.AdoptRunningSandboxes(ctx, runtimeNames...); err != nil {
		s.logger.Warn("could not take back what a previous run left running", "err", err)
	}
	// Before the check below, which marks sessions lost too: each is queued once.
	waiting := s.sessionsWaitingToRestart(ctx, s.runtimesNamed(runtimeNames))
	lost, err := s.MarkLostSessions(ctx, runtimeNames...)
	if err != nil {
		s.logger.Warn("could not check sessions against their runtimes", "err", err)
	}
	return append(lost, waiting...)
}

// markSessionsOnMissingRuntimes stops the running sessions on a VM runtime this node could not start.
// Their VM died with the previous process, so "running" would leave them waiting on nothing.
func (s *Service) markSessionsOnMissingRuntimes(ctx context.Context) error {
	missing := s.missingRuntimeNames()
	if len(missing) == 0 {
		return nil
	}
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, sess := range sessions {
		if sess.Status != StatusRunning || !runtimeBootsVMs(sess.Runtime) || !slices.Contains(missing, sess.Runtime) {
			continue
		}
		if err := s.repo.MarkStoppedBecause(ctx, placeholderOwner, sess.ID, StopReasonRuntimeMissing, now); err != nil {
			return fmt.Errorf("session: mark %s stopped: %w", sess.ID, err)
		}
		s.logger.Info("session marked stopped: its runtime is not set up",
			"id", sess.ID, "name", sess.Name, "runtime", sess.Runtime)
	}
	return nil
}

// sessionsWaitingToRestart are the sessions on one of runtimes that deepcove stopped to bring back: their
// runtime was not set up, or a shutdown cut their restart short. They were running before, so they start
// again.
func (s *Service) sessionsWaitingToRestart(ctx context.Context, runtimes map[string]sandbox.Sandbox) []*Session {
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		s.logger.Warn("could not find the sessions waiting to restart", "err", err)
		return nil
	}
	var waiting []*Session
	for _, sess := range sessions {
		if sess.Status == StatusStopped && waitsToRestart(sess.StopReason) && runtimes[sess.Runtime] != nil {
			waiting = append(waiting, sess)
		}
	}
	return waiting
}

// waitsToRestart reports whether deepcove stopped a session meaning to start it again.
func waitsToRestart(reason StopReason) bool {
	return reason == StopReasonRuntimeMissing || reason == StopReasonSandboxLost
}

// runtimesNamed is the registered runtimes, or only the named ones when any are named.
func (s *Service) runtimesNamed(names []string) map[string]sandbox.Sandbox {
	runtimes := s.registeredRuntimes()
	if len(names) > 0 {
		maps.DeleteFunc(runtimes, func(name string, _ sandbox.Sandbox) bool {
			return !slices.Contains(names, name)
		})
	}
	return runtimes
}

// AdoptRunningSandboxes hands every runtime that can take its sandboxes back the ids its sessions were
// using, or only the named runtimes. Called before MarkLostSessions, so a session whose sandbox survived
// is not seen as lost and rebuilt on top of itself.
func (s *Service) AdoptRunningSandboxes(ctx context.Context, runtimeNames ...string) error {
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return err
	}
	byRuntime := runningSessionsByRuntime(sessions, s.DefaultRuntime())

	var adoptErr error
	for name, driver := range s.runtimesNamed(runtimeNames) {
		adopter, ok := driver.(sandbox.Adopter)
		if !ok {
			continue
		}
		sandboxes := map[sandbox.ID]sandbox.SandboxSpec{}
		for _, sess := range byRuntime[name] {
			// Left out of this map it gets swept, which kills a session that is running fine. So one we
			// cannot describe is handed over with an empty spec rather than dropped.
			spec, err := s.savedSpecForSession(sess)
			if err != nil {
				s.logger.Warn("taking a session back without knowing what it was made with",
					"id", sess.ID, "err", err)
			}
			sandboxes[sandbox.ID(sess.SandboxID)] = spec
		}
		// Even with nothing to take back: this is also when it stops what it left running.
		taken, err := adopter.Adopt(ctx, sandboxes)
		if err != nil {
			adoptErr = errors.Join(adoptErr, fmt.Errorf("session: %s: %w", name, err))
		}
		s.logger.Info("asked a runtime for the sessions it was running",
			"runtime", name, "asked", len(sandboxes), "taken back", len(taken))
	}
	return adoptErr
}

// A row that names no runtime is on the default one, the same answer runtimeByName gives.
func runningSessionsByRuntime(sessions []*Session, defaultRuntime string) map[string][]*Session {
	byRuntime := map[string][]*Session{}
	// A sandbox id means something only to the runtime holding it, so two runtimes can use the same one.
	type sandboxOnRuntime struct{ runtime, sandbox string }
	seen := map[sandboxOnRuntime]bool{}
	for _, sess := range sessions {
		if sess.Status != StatusRunning || sess.SandboxID == "" {
			continue
		}
		name := cmp.Or(sess.Runtime, defaultRuntime)
		if seen[sandboxOnRuntime{name, sess.SandboxID}] {
			continue
		}
		seen[sandboxOnRuntime{name, sess.SandboxID}] = true
		byRuntime[name] = append(byRuntime[name], sess)
	}
	return byRuntime
}

// savedSpecForSession makes no folders, unlike a start: it only describes a sandbox already running.
func (s *Service) savedSpecForSession(sess *Session) (sandbox.SandboxSpec, error) {
	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return sandbox.SandboxSpec{}, fmt.Errorf("session: %s: %w", sess.ID, err)
	}
	sharedFolders, err := validateSharedFolderRequests(sess.SharedFolders)
	if err != nil {
		return sandbox.SandboxSpec{}, fmt.Errorf("session: %s: %w", sess.ID, err)
	}
	runtime, err := s.runtimeByName(sess.Runtime)
	if err != nil {
		return sandbox.SandboxSpec{}, fmt.Errorf("session: %s: %w", sess.ID, err)
	}
	return s.buildSandboxSpec(sess, agent, sharedFolders, runtime), nil
}

// MarkLostSessions marks a session stopped when its runtime no longer has the sandbox the row points
// at, and returns those sessions. They were running a moment ago, so the caller can bring them back. Only
// the named runtimes are checked when any are named.
//
// Only runtimes that can enumerate their sandboxes are considered. "The driver cannot tell us" is not
// "the sandbox is gone", and guessing would stop sessions that are running fine.
func (s *Service) MarkLostSessions(ctx context.Context, runtimeNames ...string) ([]*Session, error) {
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return nil, err
	}

	// One listing per runtime rather than one per session.
	runtimes := s.runtimesNamed(runtimeNames)
	alive := make(map[string]map[string]bool, len(runtimes))
	for name, driver := range runtimes {
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
	defaultRuntime := s.DefaultRuntime()
	var lost []*Session
	for _, sess := range sessions {
		if sess.Status != StatusRunning {
			continue
		}
		sandboxes, known := alive[cmp.Or(sess.Runtime, defaultRuntime)]
		if !known || sandboxes[sess.SandboxID] {
			continue
		}
		if err := s.repo.MarkStoppedBecause(ctx, placeholderOwner, sess.ID, StopReasonSandboxLost, now); err != nil {
			return nil, fmt.Errorf("session: mark %s lost: %w", sess.ID, err)
		}
		s.logger.Info("session marked stopped: its sandbox is gone",
			"id", sess.ID, "name", sess.Name, "runtime", sess.Runtime, "sandbox", sess.SandboxID)
		// As the row now reads, which RestartSessions compares against to see a Stop that came after.
		sess.Status, sess.StopReason = StatusStopped, StopReasonSandboxLost
		lost = append(lost, sess)
	}
	return lost, nil
}

// MarkSandboxStopped marks the sessions on a sandbox stopped after their runtime stopped it on its own.
// Left running, Start would refuse to bring them back.
func (s *Service) MarkSandboxStopped(runtime string, sandboxID sandbox.ID) {
	ctx := context.Background()
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		s.logger.Warn("could not mark the sessions on a stopped sandbox", "sandbox", sandboxID, "err", err)
		return
	}
	defaultRuntime := s.DefaultRuntime()
	for _, sess := range sessions {
		if sess.Status != StatusRunning || sess.SandboxID != string(sandboxID) || cmp.Or(sess.Runtime, defaultRuntime) != runtime {
			continue
		}
		if err := s.repo.MarkStoppedBecause(ctx, placeholderOwner, sess.ID, StopReasonUnresponsive, time.Now().UTC()); err != nil {
			s.logger.Warn("could not mark a session stopped", "id", sess.ID, "err", err)
			continue
		}
		s.logger.Info("session marked stopped: its vm stopped answering", "id", sess.ID, "name", sess.Name,
			"runtime", runtime, "sandbox", sandboxID)
	}
}

// RestartSessions starts sessions again at boot: a VM dies with the process that ran it.
//
// One at a time, so booting VMs does not spike memory. A session that fails is logged and left
// stopped. Sessions on the shared machine land back on one machine, because starting the first one
// builds it and the rest join what is already up.
func (s *Service) RestartSessions(ctx context.Context, sessions []*Session) {
	for _, sess := range sessions {
		s.restartIfStillQueued(ctx, sess)
	}
}

// restartIfStillQueued starts sess unless you stopped it after it was queued, which clears the reason it
// was queued for. Under the lock Stop takes, so a Stop either lands before the check and is seen, or
// waits for the start and then stops it.
func (s *Service) restartIfStillQueued(ctx context.Context, sess *Session) {
	unlock, err := s.lockAgent(ctx, sess.ID)
	if err != nil {
		s.logger.Warn("could not restart session", "id", sess.ID, "name", sess.Name, "err", err)
		return
	}
	defer unlock()
	current, err := s.Get(ctx, sess.ID)
	if err != nil {
		s.logger.Warn("could not restart session", "id", sess.ID, "name", sess.Name, "err", err)
		return
	}
	if current.StopReason != sess.StopReason {
		return
	}
	if err := s.Start(ctx, sess.ID); err != nil {
		s.logger.Warn("could not restart session", "id", sess.ID, "name", sess.Name, "err", err)
		// It is not coming back on its own after a real failure, or every startup would retry it. Kept on
		// a shutdown, so the next start tries again.
		if ctx.Err() == nil && waitsToRestart(sess.StopReason) {
			if err := s.repo.MarkStopped(ctx, placeholderOwner, sess.ID, time.Now().UTC()); err != nil {
				s.logger.Warn("could not clear why a session was stopped", "id", sess.ID, "err", err)
			}
		}
		return
	}
	s.logger.Info("session restarted", "id", sess.ID, "name", sess.Name)
}

// SetLifetime is the context restarts run on: the node's own run, cancelled when it shuts down.
func (s *Service) SetLifetime(ctx context.Context) {
	s.lifetime = ctx
}

// RestartSessionsInBackground starts sessions again without holding up the caller. On the node's lifetime
// rather than the caller's: a setup request ends long before its VMs are up.
func (s *Service) RestartSessionsInBackground(sessions []*Session) {
	s.restarts.Go(func() {
		s.RestartSessions(s.lifetime, sessions)
	})
}

// WaitForRestarts returns once every background restart has ended. Shutdown calls it after the lifetime
// is cancelled and before the runtimes close.
func (s *Service) WaitForRestarts() {
	s.restarts.Wait()
}

// rebuildSandbox creates a fresh sandbox for a session whose old one no longer exists, and points the row
// at it. The session keeps its id, name, shared folders and size; what it loses is whatever lived in the previous
// writable layer, which for a VM died with the VM regardless.
func (s *Service) rebuildSandbox(ctx context.Context, sess *Session, runtime sandbox.Sandbox) (sandbox.ID, error) {
	userSharedFolders, err := s.prepareSharedFoldersRequiringExisting(sess.SharedFolders)
	if err != nil {
		return "", err
	}
	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return "", fmt.Errorf("session: %w", err)
	}

	sandboxID, err := runtime.Create(ctx, s.buildSandboxSpec(sess, agent, userSharedFolders, runtime))
	if err != nil {
		return "", fmt.Errorf("session: rebuild sandbox: %w", err)
	}
	// Point the row at the new sandbox before booting it: booting takes seconds, and a sandbox no row
	// claims is reported as a leak.
	if err := s.repo.UpdateSharedFoldersAndSandbox(ctx, placeholderOwner, sess.ID, sess.SharedFolders, string(sandboxID), time.Now().UTC()); err != nil {
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
