package session

import (
	"context"
	"errors"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Restarting deepcove destroys every VM while its session rows survive saying "running". Reconcile is what
// makes the table tell the truth again, so the session page stops waiting on a sandbox that cannot answer.
func TestReconcileMarksSessionsWhoseSandboxIsGone(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	if sess.Status != StatusRunning {
		t.Fatalf("setup: status = %q, want running", sess.Status)
	}
	lister.instances = nil // what a restart looks like to a VM runtime

	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", got.Status)
	}
}

// A session whose sandbox is still there must be left alone, or a restart would stop everything.
func TestReconcileLeavesLiveSessionsRunning(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	lister.instances = []sandbox.Instance{{
		Handle: sandbox.Handle(sess.ContainerID),
		State:  sandbox.InstanceRunning,
	}}

	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusRunning {
		t.Errorf("status = %q, want the session left running", got.Status)
	}
}

// "The driver cannot enumerate" is not "the sandbox is gone". Acting on a runtime that cannot list would
// stop sessions that are running perfectly well, which is worse than a stale row.
func TestReconcileSkipsRuntimesThatCannotList(t *testing.T) {
	svc, _, _, _ := newTestService(t) // plain fake, no Lister
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")

	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusRunning {
		t.Errorf("status = %q; a runtime that cannot list must not have its sessions stopped", got.Status)
	}
}

// Reconcile only reports the truth. Start is what makes the session usable again, by building a new
// sandbox instead of failing forever on a handle no driver has ever heard of.
func TestStartRebuildsAVanishedSandbox(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	oldHandle := sess.ContainerID

	// Simulate the restart: the driver forgets everything, and the row is reconciled to stopped.
	lister.forget()
	lister.instances = nil
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if err := svc.Start(ctx, sess.ID); err != nil {
		t.Fatalf("Start after a restart: %v", err)
	}

	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.ContainerID == "" || got.ContainerID == oldHandle {
		t.Errorf("container id = %q; want a NEW handle, not the dead %q", got.ContainerID, oldHandle)
	}
	// The row itself has to survive: losing the id or name would make this a delete-and-recreate, which is
	// exactly the manual step being removed.
	if got.ID != sess.ID || got.Name != sess.Name {
		t.Errorf("identity changed: %q/%q, want %q/%q", got.ID, got.Name, sess.ID, sess.Name)
	}
}

// A driver that fails to start a sandbox it DOES have is a real error. Rebuilding then would abandon a
// working sandbox and hide the fault, so only a missing one is recovered.
func TestStartDoesNotRebuildOnARealStartFailure(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	if err := svc.Stop(ctx, sess.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	lister.instances = []sandbox.Instance{{Handle: sandbox.Handle(sess.ContainerID)}}
	lister.startErr = errors.New("the daemon refused to start it")

	if err := svc.Start(ctx, sess.ID); err == nil {
		t.Fatal("Start returned nil after the driver failed to start an existing sandbox")
	}

	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ContainerID != sess.ContainerID {
		t.Errorf("container id changed to %q; a real start failure must not rebuild", got.ContainerID)
	}
}
