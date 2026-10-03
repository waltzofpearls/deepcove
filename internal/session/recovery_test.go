package session

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Restarting deepcove destroys every VM while its session rows survive saying "running". MarkLostSessions is what
// makes the table tell the truth again, so the session page stops waiting on a sandbox that cannot answer.
func TestMarkLostSessionsMarksSessionsWhoseSandboxIsGone(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	if sess.Status != StatusRunning {
		t.Fatalf("setup: status = %q, want running", sess.Status)
	}
	lister.instances = nil // what a restart looks like to a VM runtime

	if _, err := svc.MarkLostSessions(ctx); err != nil {
		t.Fatalf("MarkLostSessions: %v", err)
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
func TestMarkLostSessionsLeavesLiveSessionsRunning(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	lister.instances = []sandbox.Instance{{
		ID:    sandbox.ID(sess.SandboxID),
		State: sandbox.InstanceRunning,
	}}

	if _, err := svc.MarkLostSessions(ctx); err != nil {
		t.Fatalf("MarkLostSessions: %v", err)
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
func TestMarkLostSessionsSkipsRuntimesThatCannotList(t *testing.T) {
	svc, _, _, _ := newTestService(t) // plain fake, no Lister
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")

	if _, err := svc.MarkLostSessions(ctx); err != nil {
		t.Fatalf("MarkLostSessions: %v", err)
	}

	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusRunning {
		t.Errorf("status = %q; a runtime that cannot list must not have its sessions stopped", got.Status)
	}
}

func TestService_MarkSandboxStopped(t *testing.T) {
	cases := []struct {
		name       string
		runtime    string
		startAgain bool
		want       Status
		wantReason StopReason
	}{
		{"its runtime", runtimeFake, false, StatusStopped, StopReasonUnresponsive},
		{"another runtime with the same id", "other", false, StatusRunning, ""},
		{"starting again clears the reason", runtimeFake, true, StatusRunning, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			sess := mustCreate(t, svc, "claude")

			svc.MarkSandboxStopped(tc.runtime, sandbox.ID(sess.SandboxID))
			if tc.startAgain {
				if err := svc.Start(context.Background(), sess.ID); err != nil {
					t.Fatalf("Start: %v", err)
				}
			}

			got, err := svc.Get(context.Background(), sess.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != tc.want || got.StopReason != tc.wantReason {
				t.Errorf("status, reason = %q, %q, want %q, %q", got.Status, got.StopReason, tc.want, tc.wantReason)
			}
		})
	}
}

// MarkLostSessions only reports the truth. Start is what makes the session usable again, by building a new
// sandbox instead of failing forever on a handle no driver has ever heard of.
func TestStartRebuildsAVanishedSandbox(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	oldHandle := sess.SandboxID

	// Simulate the restart: the driver forgets everything, and the row is marked stopped.
	lister.forget()
	lister.instances = nil
	if _, err := svc.MarkLostSessions(ctx); err != nil {
		t.Fatalf("MarkLostSessions: %v", err)
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
	if got.SandboxID == "" || got.SandboxID == oldHandle {
		t.Errorf("sandbox id = %q; want a NEW handle, not the dead %q", got.SandboxID, oldHandle)
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
	lister.instances = []sandbox.Instance{{ID: sandbox.ID(sess.SandboxID)}}
	lister.startErr = errors.New("the daemon refused to start it")

	if err := svc.Start(ctx, sess.ID); err == nil {
		t.Fatal("Start returned nil after the driver failed to start an existing sandbox")
	}

	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SandboxID != sess.SandboxID {
		t.Errorf("sandbox id changed to %q; a real start failure must not rebuild", got.SandboxID)
	}
}

// Boot path: what the previous process was running comes back without anyone clicking Start.
func TestRestartSessionsBringsBackWhatWasRunning(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	oldHandle := sess.SandboxID

	// The restart: the driver forgets every VM, and the table is brought back to the truth.
	lister.forget()
	lost, err := svc.MarkLostSessions(ctx)
	if err != nil {
		t.Fatalf("MarkLostSessions: %v", err)
	}
	if len(lost) != 1 {
		t.Fatalf("MarkLostSessions returned %d lost sessions, want 1", len(lost))
	}

	svc.RestartSessions(ctx, lost)

	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.SandboxID == oldHandle {
		t.Errorf("sandbox id is still the dead %q, want a rebuilt one", oldHandle)
	}
}

// A session that cannot come back must not stop the others.
func TestRestartSessionsContinuesPastAFailure(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	first := mustCreate(t, svc, "claude")
	second := mustCreate(t, svc, "claude")
	lister.forget()
	lost, err := svc.MarkLostSessions(ctx)
	if err != nil {
		t.Fatalf("MarkLostSessions: %v", err)
	}
	lister.createErr = errors.New("no capacity")

	svc.RestartSessions(ctx, lost)

	for _, id := range []string{first.ID, second.ID} {
		got, err := svc.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if got.Status != StatusStopped {
			t.Errorf("session %s status = %q, want stopped after a failed restart", id, got.Status)
		}
	}
}

// Sessions that shared a machine must come back sharing one machine. Restarting each on its own would
// quietly turn one workspace VM into a VM per session, which is the memory blowup the sharing avoids.
func TestRestartSessionsRejoinsTheWorkspaceVM(t *testing.T) {
	svc, lister := workspaceService(t)
	ctx := context.Background()

	first := onWorkspace(t, svc, "first", "")
	second := onWorkspace(t, svc, "second", "second")
	oldSandbox := first.SandboxID

	lister.forget() // the restart: every VM is gone
	lost, err := svc.MarkLostSessions(ctx)
	if err != nil {
		t.Fatalf("MarkLostSessions: %v", err)
	}
	if len(lost) != 2 {
		t.Fatalf("got %d lost sessions, want 2", len(lost))
	}

	built := len(lister.created)
	svc.RestartSessions(ctx, lost)

	if got := len(lister.created) - built; got != 1 {
		t.Errorf("built %d machines for two sessions sharing one, want 1", got)
	}
	back := make([]*Session, 0, 2)
	for _, id := range []string{first.ID, second.ID} {
		sess, err := svc.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		back = append(back, sess)
	}
	if back[0].SandboxID != back[1].SandboxID {
		t.Errorf("sessions landed on different machines: %q and %q", back[0].SandboxID, back[1].SandboxID)
	}
	if back[0].SandboxID == oldSandbox {
		t.Error("still pointing at the sandbox that is gone")
	}
	for _, sess := range back {
		if sess.Status != StatusRunning {
			t.Errorf("session %s: status %q, want running", sess.ID, sess.Status)
		}
	}
}

// A rebuild has to update the Session the caller is holding, not only the row. Start hands that value
// to the background agent launch, so a stale handle there means a session that never gets an agent.
func TestRebuildUpdatesTheSessionItWasGiven(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	old := sess.SandboxID

	lister.forget()
	lister.instances = nil
	if _, err := svc.MarkLostSessions(ctx); err != nil {
		t.Fatalf("MarkLostSessions: %v", err)
	}

	runtime, err := svc.runtimeFor(sess)
	if err != nil {
		t.Fatalf("runtimeFor: %v", err)
	}
	handle, err := svc.rebuildSandbox(ctx, sess, runtime)
	if err != nil {
		t.Fatalf("rebuildSandbox: %v", err)
	}
	if sess.SandboxID != string(handle) {
		t.Errorf("session still points at %q, want the new %q", sess.SandboxID, handle)
	}
	if sess.SandboxID == old {
		t.Error("session still points at the sandbox that is gone")
	}
}

func TestRunningSessionsByRuntime(t *testing.T) {
	cases := []struct {
		name     string
		sessions []*Session
		want     map[string][]string
	}{
		{
			name:     "a row that names no runtime is on the default one",
			sessions: []*Session{{ID: "a", Status: StatusRunning, SandboxID: "sb-a"}},
			want:     map[string][]string{RuntimeSandboxless: {"a"}},
		},
		{
			name: "a stopped session is not asked for",
			sessions: []*Session{
				{ID: "a", Status: StatusRunning, SandboxID: "sb-a", Runtime: RuntimeSandboxless},
				{ID: "b", Status: StatusStopped, SandboxID: "sb-b", Runtime: RuntimeSandboxless},
			},
			want: map[string][]string{RuntimeSandboxless: {"a"}},
		},
		{
			name: "two sessions on one sandbox ask for it once",
			sessions: []*Session{
				{ID: "a", Status: StatusRunning, SandboxID: "sb-a", Runtime: RuntimeCloudHV},
				{ID: "b", Status: StatusRunning, SandboxID: "sb-a", Runtime: RuntimeCloudHV},
			},
			want: map[string][]string{RuntimeCloudHV: {"a"}},
		},
		{
			name: "one id used by two runtimes is asked of both",
			sessions: []*Session{
				{ID: "a", Status: StatusRunning, SandboxID: "sb", Runtime: RuntimeCloudHV},
				{ID: "b", Status: StatusRunning, SandboxID: "sb", Runtime: RuntimeSandboxless},
			},
			want: map[string][]string{RuntimeCloudHV: {"a"}, RuntimeSandboxless: {"b"}},
		},
		{
			name:     "a session with no sandbox yet",
			sessions: []*Session{{ID: "a", Status: StatusRunning, Runtime: RuntimeSandboxless}},
			want:     map[string][]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string][]string{}
			for name, list := range runningSessionsByRuntime(tc.sessions, RuntimeSandboxless) {
				for _, sess := range list {
					got[name] = append(got[name], sess.ID)
				}
			}
			if !maps.EqualFunc(got, tc.want, slices.Equal) {
				t.Errorf("grouped %v, want %v", got, tc.want)
			}
		})
	}
}
