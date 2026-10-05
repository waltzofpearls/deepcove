package session

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// restartingSandbox is a fake driver that can restart, keeping the spec it was handed. A VM reads its
// size at boot, so that spec is the whole point of the feature.
type restartingSandbox struct {
	*fakeSandbox
	specs []sandbox.SandboxSpec
	// nextBootSpecs are the specs handed to UpdateSpec, newest last.
	nextBootSpecs []sandbox.SandboxSpec
	// onRestart runs inside Restart, so a test can see the context the driver was handed.
	onRestart func(context.Context)

	installingReads int
	statusReads     int
}

func (r *restartingSandbox) Exec(_ context.Context, _ sandbox.ID, spec sandbox.ExecSpec) (sandbox.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execCalls = append(r.execCalls, append([]string(nil), spec.Cmd...))
	switch {
	case len(spec.Cmd) == 2 && spec.Cmd[0] == "cat" && spec.Cmd[1] == sandbox.SandboxedSetupStatusPath:
		r.statusReads++
		if r.statusReads <= r.installingReads {
			return newScriptedProcess("installing\n", "", 0), nil
		}
		return newScriptedProcess("ready\n", "", 0), nil
	case slices.Contains(spec.Cmd, "has-session"):
		return newScriptedProcess("", "", 1), nil
	}
	return newScriptedProcess("", "", 0), nil
}

// Setup has to name this fake, not the one it embeds, or the reads land on the embedded Exec and the
// status never changes.
func (r *restartingSandbox) Setup(ctx context.Context, sandboxID sandbox.ID) (sandbox.SetupState, error) {
	return sandbox.SandboxedSetup(ctx, r, sandboxID)
}

// ListSandboxes reports what this driver built. A workspace VM is refused on a runtime that cannot
// enumerate its sandboxes, so a fake hosting one has to answer this.
func (r *restartingSandbox) ListSandboxes(context.Context) ([]sandbox.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sandbox.Instance, 0, len(r.created))
	for handle := range r.created {
		out = append(out, sandbox.Instance{ID: handle, State: sandbox.InstanceRunning})
	}
	return out, nil
}

func (r *restartingSandbox) execCount(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, cmd := range r.execCalls {
		if slices.Contains(cmd, name) {
			n++
		}
	}
	return n
}

func (r *restartingSandbox) Restart(ctx context.Context, _ sandbox.ID, spec sandbox.SandboxSpec) error {
	if r.onRestart != nil {
		r.onRestart(ctx)
	}
	r.specs = append(r.specs, spec)
	return nil
}

// UpdateSpec keeps the spec too: what the next plain Start would boot.
func (r *restartingSandbox) UpdateSpec(_ context.Context, _ sandbox.ID, spec sandbox.SandboxSpec) error {
	r.nextBootSpecs = append(r.nextBootSpecs, spec)
	return nil
}

// saveWorkspaceVMRecord stores a built workspace VM, the state a restart is asked about: a machine is
// running, and the record has since been edited.
func saveWorkspaceVMRecord(t *testing.T, svc *Service, sandboxID string, cpu float64) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	vm := &WorkspaceVM{
		HostRoot:  "/tmp",
		GuestRoot: "/workspace",
		Runtime:   RuntimeAppleVF,
		Resources: Resources{CPU: cpu},
		SandboxID: sandboxID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := svc.workspaceVM.Save(context.Background(), vm); err != nil {
		t.Fatalf("setup Save: %v", err)
	}
}

func TestRestartSandbox(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the sandbox to restart.
		setup   func(t *testing.T, svc *Service) string
		wantErr error
		wantCPU float64
	}{
		{
			name: "workspace VM boots with the saved size, not the running one",
			setup: func(t *testing.T, svc *Service) string {
				saveWorkspaceVMRecord(t, svc, "vm-1", 4)
				return "vm-1"
			},
			wantCPU: 4,
		},
		{
			name: "session VM boots from its own record",
			setup: func(t *testing.T, svc *Service) string {
				sess, err := svc.Create(context.Background(), CreateRequest{
					Agent:     "claude",
					Resources: Resources{CPU: 3},
				})
				if err != nil {
					t.Fatalf("setup Create: %v", err)
				}
				return sess.SandboxID
			},
			wantCPU: 3,
		},
		{
			name:    "a sandbox no record owns",
			setup:   func(*testing.T, *Service) string { return "nobody-owns-this" },
			wantErr: ErrNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
			svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
			id := tc.setup(t, svc)

			err := svc.RestartSandbox(context.Background(), RuntimeAppleVF, id)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("got %v, want errors.Is(err, %v)", err, tc.wantErr)
				}
				if len(driver.specs) != 0 {
					t.Errorf("restarted %d sandboxes, want none", len(driver.specs))
				}
				return
			}
			if err != nil {
				t.Fatalf("RestartSandbox: %v", err)
			}
			if len(driver.specs) != 1 {
				t.Fatalf("got %d restarts, want 1", len(driver.specs))
			}
			if got := driver.specs[0].Resources.CPU; got != tc.wantCPU {
				t.Errorf("restarted with %v CPU, want %v", got, tc.wantCPU)
			}
		})
	}
}

func TestRestartSandboxRefusesARuntimeThatCannot(t *testing.T) {
	svc := newServiceWithRuntime(t, db.OpenForTest(t), newFakeSandbox())
	sess := mustCreate(t, svc, "claude")

	err := svc.RestartSandbox(context.Background(), runtimeFake, sess.SandboxID)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v, want errors.Is(err, ErrInvalidInput)", err)
	}
}

func TestRestartStartsTheAgentAfterSetup(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox(), installingReads: 1}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	ctx := context.Background()
	sess, err := svc.Create(ctx, CreateRequest{Agent: "claude"})
	if err != nil {
		t.Fatalf("setup Create: %v", err)
	}
	svc.background = func(f func()) { f() }

	if err := svc.RestartSandbox(ctx, RuntimeAppleVF, sess.SandboxID); err != nil {
		t.Fatalf("RestartSandbox: %v", err)
	}

	if driver.statusReads < 2 {
		t.Errorf("read the setup status %d times, want at least 2 (installing, then ready)", driver.statusReads)
	}
	if got := driver.execCount("new-session"); got != 1 {
		t.Errorf("started %d tmux sessions, want 1", got)
	}
}

func TestRestartOutlivesTheRequest(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude"})
	if err != nil {
		t.Fatalf("setup Create: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	driver.onRestart = func(restartCtx context.Context) {
		cancel() // the browser goes away part way through
		if restartCtx.Err() != nil {
			t.Error("the restart must carry on when the request is cancelled")
		}
	}

	if err := svc.RestartSandbox(ctx, RuntimeAppleVF, sess.SandboxID); err != nil {
		t.Fatalf("RestartSandbox: %v", err)
	}
	if len(driver.specs) != 1 {
		t.Errorf("got %d restarts, want 1", len(driver.specs))
	}
}

func TestRestartLeavesStoppedSessionsAlone(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	ctx := context.Background()
	if _, err := svc.SaveWorkspaceVM(ctx, WorkspaceVMRequest{HostRoot: t.TempDir()}); err != nil {
		t.Fatalf("setup SaveWorkspaceVM: %v", err)
	}

	up := onWorkspace(t, svc, "still-working", "")
	down := onWorkspace(t, svc, "finished", "")
	if err := svc.Stop(ctx, down.ID); err != nil {
		t.Fatalf("setup Stop: %v", err)
	}

	// Inline from here, so the agents counted below are the restart's own work rather than the setup's.
	svc.background = func(f func()) { f() }
	if err := svc.RestartSandbox(ctx, RuntimeAppleVF, up.SandboxID); err != nil {
		t.Fatalf("RestartSandbox: %v", err)
	}
	if got := driver.execCount("new-session"); got != 1 {
		t.Errorf("started %d agents, want 1, only the session that was running", got)
	}
}
