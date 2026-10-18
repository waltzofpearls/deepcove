package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// ResizeVM gives a session's own VM a new size now: a running VM restarts with it.
func (s *Service) ResizeVM(ctx context.Context, id, size string) (*Session, error) {
	sess, err := s.saveVMSize(ctx, id, size)
	if err != nil {
		return nil, err
	}
	// Restarted even when the size was saved already: a "restart later" or a failed restart leaves the
	// VM on the old one.
	if sess.Status == StatusRunning {
		if err := s.RestartSandbox(ctx, sess.Runtime, sess.SandboxID); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, id)
}

// SaveVMSize records a new size without restarting. A running VM keeps its size until it next restarts.
func (s *Service) SaveVMSize(ctx context.Context, id, size string) (*Session, error) {
	if _, err := s.saveVMSize(ctx, id, size); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// saveVMSize records the size. A stopped VM loses its old sandbox, so its next Start builds one at the
// new size; the home disk belongs to the session, so it stays.
func (s *Service) saveVMSize(ctx context.Context, id, size string) (*Session, error) {
	resized := Resources{Size: size}
	if err := validateVMSize(resized, SessionVMSizes); err != nil {
		return nil, err
	}
	// Held for the whole save, so a Start cannot boot the VM this may be about to drop. A resize that
	// restarts lets go before the restart, which takes the claim itself.
	done, claimed := s.beginStarting(id)
	if !claimed {
		return nil, fmt.Errorf("%w: the VM is starting. Try again in a moment", ErrInvalidInput)
	}
	defer done()
	// Read under the claim, and from the store: Get would report this resize's own claim as starting.
	sess, err := s.repo.Get(ctx, placeholderOwner, id)
	if err != nil {
		return nil, err
	}
	if sess.OnWorkspaceVM {
		return nil, fmt.Errorf("%w: this session runs on the workspace VM, so resize the workspace VM instead", ErrInvalidInput)
	}
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return nil, err
	}
	if workDirIsOnThisMachine(runtime) {
		return nil, fmt.Errorf("%w: a session with no sandbox has no VM to resize", ErrInvalidInput)
	}
	if sess.Resources == resized {
		return sess, nil
	}

	now := time.Now().UTC()
	if sess.Status != StatusRunning {
		// Dropped before the size is saved, so a drop that fails leaves nothing half done to retry.
		if err := runtime.Destroy(ctx, sandbox.ID(sess.SandboxID)); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
			return nil, fmt.Errorf("session: drop the old VM: %w", err)
		}
		// No sandbox on the row is what makes Start build a new one.
		if err := s.repo.UpdateSharedFoldersAndSandbox(ctx, placeholderOwner, id, sess.SharedFolders, "", now); err != nil {
			return nil, err
		}
		sess.SandboxID = ""
	}
	if err := s.repo.UpdateResources(ctx, placeholderOwner, id, resized, now); err != nil {
		return nil, err
	}
	sess.Resources = resized
	if sess.Status == StatusRunning {
		// The VM boots from the spec it was created with, so a plain Start would bring back the old size.
		if err := s.updateSandboxSpec(ctx, runtime, sess); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

// updateSandboxSpec hands the runtime the session's saved spec for its next boot. A runtime that boots
// from nothing it stores needs no telling.
func (s *Service) updateSandboxSpec(ctx context.Context, runtime sandbox.Sandbox, sess *Session) error {
	updater, ok := runtime.(sandbox.SpecUpdater)
	if !ok {
		return nil
	}
	spec, err := s.savedSpecFor(ctx, sess.SandboxID)
	if err != nil {
		return err
	}
	return updater.UpdateSpec(ctx, sandbox.ID(sess.SandboxID), spec)
}
