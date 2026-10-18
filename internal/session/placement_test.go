package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// workspaceService is a service whose default runtime can list, with a workspace VM recorded but not
// yet built. Building it is what the tests are about.
func workspaceService(t *testing.T) (*Service, *listerSandbox) {
	t.Helper()
	lister := &listerSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), lister, RuntimeAppleVF)
	if _, err := svc.SaveWorkspaceVM(context.Background(), WorkspaceVMRequest{HostRoot: t.TempDir()}); err != nil {
		t.Fatalf("SaveWorkspaceVM: %v", err)
	}
	return svc, lister
}

// onWorkspace creates a session on the shared machine. Named rather than generated, so a test can never
// fail on two generated names colliding.
func onWorkspace(t *testing.T, svc *Service, name, workDir string) *Session {
	t.Helper()
	sess, err := svc.Create(context.Background(), CreateRequest{
		Agent:         "claude",
		Name:          name,
		OnWorkspaceVM: true,
		WorkDir:       workDir,
	})
	if err != nil {
		t.Fatalf("Create on the workspace vm: %v", err)
	}
	return sess
}

func TestCreateOnWorkspaceVM(t *testing.T) {
	ctx := context.Background()

	t.Run("sessions share one machine and get a folder each", func(t *testing.T) {
		svc, lister := workspaceService(t)

		first := onWorkspace(t, svc, "first", "")
		second := onWorkspace(t, svc, "deepcove", "deepcove")

		if first.SandboxID != second.SandboxID {
			t.Errorf("sandboxes %q and %q, want one shared", first.SandboxID, second.SandboxID)
		}
		if got := len(lister.created); got != 1 {
			t.Errorf("built %d machines for two sessions, want 1", got)
		}
		if !first.OnWorkspaceVM || !second.OnWorkspaceVM {
			t.Error("sessions are not marked as living on the workspace vm")
		}
		// No folder given means one named after the session, so two agents never land in the same
		// directory by accident.
		if want := workspaceDir + "/" + first.Name; first.WorkDir != want {
			t.Errorf("work dir: got %q, want %q", first.WorkDir, want)
		}
		if second.WorkDir != workspaceDir+"/deepcove" {
			t.Errorf("work dir: got %q, want %s/deepcove", second.WorkDir, workspaceDir)
		}
		if tmuxSessionName(first.ID) == tmuxSessionName(second.ID) {
			t.Error("both sessions would share one tmux session")
		}
	})

	t.Run("the machine decides the runtime, image and size", func(t *testing.T) {
		svc, _ := workspaceService(t)
		vm, err := svc.WorkspaceVM(ctx)
		if err != nil {
			t.Fatalf("WorkspaceVM: %v", err)
		}

		sess, err := svc.Create(ctx, CreateRequest{
			Agent:         "claude",
			OnWorkspaceVM: true,
			Image:         "alpine:3.20",
			Resources:     Resources{CPU: 8, MemoryMB: 16384},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if sess.Image != vm.Image || sess.Runtime != vm.Runtime {
			t.Errorf("got %s/%s, want the machine's %s/%s", sess.Runtime, sess.Image, vm.Runtime, vm.Image)
		}
		if sess.Resources != (Resources{}) {
			t.Errorf("resources %+v; a session cannot size a machine it shares", sess.Resources)
		}
	})

	t.Run("shared folders are refused", func(t *testing.T) {
		svc, _ := workspaceService(t)
		_, err := svc.Create(ctx, CreateRequest{
			Agent:         "claude",
			OnWorkspaceVM: true,
			SharedFolders: []SharedFolderRequest{{HostPath: t.TempDir(), GuestPath: "/workspace/x"}},
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("got %v, want ErrInvalidInput", err)
		}
	})

	t.Run("a node with no workspace vm says so", func(t *testing.T) {
		svc, _ := newListerService(t)
		_, err := svc.Create(ctx, CreateRequest{Agent: "claude", OnWorkspaceVM: true})
		if !errors.Is(err, ErrNoWorkspaceVM) {
			t.Errorf("got %v, want ErrNoWorkspaceVM", err)
		}
	})
}

func TestWorkspaceWorkDir(t *testing.T) {
	cases := []struct {
		name    string
		workDir string
		want    string
		wantErr bool
	}{
		{name: "empty falls back to the session name", want: workspaceDir + "/my-session"},
		{name: "a bare name is a folder in the workspace", workDir: "deepcove", want: workspaceDir + "/deepcove"},
		{name: "a nested name keeps its shape", workDir: "work/deepcove", want: workspaceDir + "/work/deepcove"},
		{name: "an absolute path inside the workspace is kept", workDir: "/workspace/deepcove", want: workspaceDir + "/deepcove"},
		{name: "the workspace root itself is allowed", workDir: "/workspace", want: workspaceDir},
		{name: "a dot is the workspace root", workDir: ".", want: workspaceDir},
		{name: "escaping the workspace is refused", workDir: "../etc", wantErr: true},
		{name: "somewhere else entirely is refused", workDir: "/etc", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspaceWorkDir(workspaceDir, tc.workDir, "my-session")
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("got %q, %v; want ErrInvalidInput", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnsureWorkspaceVM(t *testing.T) {
	ctx := context.Background()

	t.Run("builds once, then reuses", func(t *testing.T) {
		svc, lister := workspaceService(t)

		first, err := svc.EnsureWorkspaceVM(ctx)
		if err != nil {
			t.Fatalf("EnsureWorkspaceVM: %v", err)
		}
		second, err := svc.EnsureWorkspaceVM(ctx)
		if err != nil {
			t.Fatalf("EnsureWorkspaceVM again: %v", err)
		}
		if first.SandboxID != second.SandboxID {
			t.Errorf("second call returned %q, want the running %q", second.SandboxID, first.SandboxID)
		}
		if got := len(lister.created); got != 1 {
			t.Errorf("built %d machines, want 1", got)
		}
	})

	t.Run("concurrent callers get one machine", func(t *testing.T) {
		svc, lister := workspaceService(t)

		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := svc.EnsureWorkspaceVM(ctx); err != nil {
					t.Errorf("EnsureWorkspaceVM: %v", err)
				}
			}()
		}
		wg.Wait()
		if got := len(lister.created); got != 1 {
			t.Errorf("built %d machines, want 1", got)
		}
	})

	t.Run("a machine that is gone is rebuilt", func(t *testing.T) {
		svc, lister := workspaceService(t)

		before, err := svc.EnsureWorkspaceVM(ctx)
		if err != nil {
			t.Fatalf("EnsureWorkspaceVM: %v", err)
		}
		lister.forget() // what a restart does to a VM runtime

		after, err := svc.EnsureWorkspaceVM(ctx)
		if err != nil {
			t.Fatalf("EnsureWorkspaceVM after a restart: %v", err)
		}
		if after.SandboxID == before.SandboxID {
			t.Error("still pointing at the machine that is gone")
		}
		stored, err := svc.WorkspaceVM(ctx)
		if err != nil {
			t.Fatalf("WorkspaceVM: %v", err)
		}
		if stored.SandboxID != after.SandboxID {
			t.Errorf("record says %q, want the rebuilt %q", stored.SandboxID, after.SandboxID)
		}
	})
}

func TestSaveWorkspaceVM(t *testing.T) {
	ctx := context.Background()

	t.Run("a folder that is not there is refused", func(t *testing.T) {
		svc, _ := newListerService(t)
		_, err := svc.SaveWorkspaceVM(ctx, WorkspaceVMRequest{HostRoot: "/no/such/folder"})
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("got %v, want ErrInvalidInput", err)
		}
	})

	t.Run("a relative folder is refused", func(t *testing.T) {
		svc, _ := newListerService(t)
		_, err := svc.SaveWorkspaceVM(ctx, WorkspaceVMRequest{HostRoot: "Dev"})
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("got %v, want ErrInvalidInput", err)
		}
	})

	t.Run("saving again keeps the machine that is running", func(t *testing.T) {
		svc, _ := workspaceService(t)
		built, err := svc.EnsureWorkspaceVM(ctx)
		if err != nil {
			t.Fatalf("EnsureWorkspaceVM: %v", err)
		}

		root := t.TempDir()
		saved, err := svc.SaveWorkspaceVM(ctx, WorkspaceVMRequest{HostRoot: root, Resources: Resources{CPU: 6}})
		if err != nil {
			t.Fatalf("SaveWorkspaceVM: %v", err)
		}
		if saved.SandboxID != built.SandboxID {
			t.Errorf("sandbox %q, want the running %q kept", saved.SandboxID, built.SandboxID)
		}
		if saved.HostRoot != root || saved.Resources.CPU != 6 {
			t.Errorf("got %+v, want the new root and size recorded", saved)
		}
	})
}

func TestSandboxIsShared(t *testing.T) {
	ctx := context.Background()
	svc, _ := workspaceService(t)

	only := onWorkspace(t, svc, "first", "")
	if svc.sandboxIsShared(ctx, only) {
		t.Error("a session alone on a machine reads as shared")
	}
	onWorkspace(t, svc, "second", "second")
	if !svc.sandboxIsShared(ctx, only) {
		t.Error("a machine with two sessions does not read as shared")
	}
}

// Ending a session must never take the shared machine with it, whether or not anyone else is on it.
func TestWorkspaceVMSurvivesItsSessions(t *testing.T) {
	ctx := context.Background()

	t.Run("stop ends the session, not the machine", func(t *testing.T) {
		svc, lister := workspaceService(t)
		first := onWorkspace(t, svc, "first", "")
		second := onWorkspace(t, svc, "second", "second")

		lister.stopped = map[sandbox.ID]bool{}
		if err := svc.Stop(ctx, second.ID); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if len(lister.stopped) != 0 {
			t.Errorf("the shared machine was stopped: %v", lister.stopped)
		}
		if got, err := svc.Get(ctx, second.ID); err != nil || got.Status != StatusStopped {
			t.Errorf("session status %v (err %v), want stopped", got.Status, err)
		}
		if got, err := svc.Get(ctx, first.ID); err != nil || got.Status != StatusRunning {
			t.Errorf("the other session is %v (err %v), want still running", got.Status, err)
		}
	})

	t.Run("the last session out leaves the machine up", func(t *testing.T) {
		svc, lister := workspaceService(t)
		only := onWorkspace(t, svc, "first", "")

		lister.destroyed = map[sandbox.ID]bool{}
		if err := svc.Destroy(ctx, only.ID); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if len(lister.destroyed) != 0 {
			t.Errorf("the machine was destroyed with its last session: %v", lister.destroyed)
		}
		if _, err := svc.Get(ctx, only.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("the session row survived: %v", err)
		}
		vm, err := svc.WorkspaceVM(ctx)
		if err != nil {
			t.Fatalf("WorkspaceVM: %v", err)
		}
		if vm.SandboxID == "" {
			t.Error("the record forgot its machine")
		}
	})

	// A session with its own machine still takes it down, which is the behaviour every session had
	// before the shared machine existed.
	t.Run("a private session still destroys its own machine", func(t *testing.T) {
		svc, lister := workspaceService(t)
		private := mustCreate(t, svc, "claude")

		lister.destroyed = map[sandbox.ID]bool{}
		if err := svc.Destroy(ctx, private.ID); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if len(lister.destroyed) != 1 {
			t.Errorf("destroyed %v, want the session's own machine", lister.destroyed)
		}
	})
}

func TestDeleteWorkspaceVM(t *testing.T) {
	ctx := context.Background()

	t.Run("refused while sessions are on it", func(t *testing.T) {
		svc, _ := workspaceService(t)
		sess := onWorkspace(t, svc, "first", "")

		if err := svc.DeleteWorkspaceVM(ctx, false); !errors.Is(err, ErrWorkspaceVMInUse) {
			t.Fatalf("got %v, want ErrWorkspaceVMInUse", err)
		}
		if err := svc.Destroy(ctx, sess.ID); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if err := svc.DeleteWorkspaceVM(ctx, false); err != nil {
			t.Fatalf("DeleteWorkspaceVM: %v", err)
		}
		if _, err := svc.WorkspaceVM(ctx); !errors.Is(err, ErrNoWorkspaceVM) {
			t.Errorf("got %v, want ErrNoWorkspaceVM", err)
		}
	})

	t.Run("the machine goes with it", func(t *testing.T) {
		svc, lister := workspaceService(t)
		vm, err := svc.EnsureWorkspaceVM(ctx)
		if err != nil {
			t.Fatalf("EnsureWorkspaceVM: %v", err)
		}

		if err := svc.DeleteWorkspaceVM(ctx, false); err != nil {
			t.Fatalf("DeleteWorkspaceVM: %v", err)
		}
		if !lister.destroyed[sandbox.ID(vm.SandboxID)] {
			t.Errorf("destroyed %v, want the machine %q", lister.destroyed, vm.SandboxID)
		}
	})
}

// A stopped session has to find its way back onto whatever machine is running now, which is the
// association that used to be lost the moment the VM was rebuilt.
func TestStartRejoinsARebuiltWorkspaceVM(t *testing.T) {
	ctx := context.Background()
	svc, lister := workspaceService(t)

	sess := onWorkspace(t, svc, "first", "")
	old := sess.SandboxID
	if err := svc.Stop(ctx, sess.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	lister.forget()

	if err := svc.Start(ctx, sess.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusRunning {
		t.Errorf("status %q, want running", got.Status)
	}
	if got.SandboxID == old || got.SandboxID == "" {
		t.Errorf("sandbox %q, want the rebuilt machine rather than the dead %q", got.SandboxID, old)
	}
	vm, err := svc.WorkspaceVM(ctx)
	if err != nil {
		t.Fatalf("WorkspaceVM: %v", err)
	}
	if got.SandboxID != vm.SandboxID {
		t.Errorf("session is on %q, machine is %q", got.SandboxID, vm.SandboxID)
	}
	if !strings.HasPrefix(got.WorkDir, workspaceDir+"/") {
		t.Errorf("work dir %q, want a folder under %s", got.WorkDir, workspaceDir)
	}
}

// The machine's root is a setting, so a session's folder has to resolve under whatever it is rather
// than under a hardcoded /workspace.
func TestWorkspaceWorkDirUsesTheMachinesRoot(t *testing.T) {
	cases := []struct {
		name    string
		root    string
		workDir string
		want    string
		wantErr bool
	}{
		{name: "a bare name lands under the machine's root", root: "/repos", workDir: "deepcove", want: "/repos/deepcove"},
		{name: "no name uses the session name", root: "/repos", want: "/repos/my-session"},
		{name: "an absolute path inside it is kept", root: "/repos", workDir: "/repos/deepcove", want: "/repos/deepcove"},
		{name: "the old default is still refused at its root", root: "/repos", workDir: "/workspace/x", wantErr: true},
		{name: "the root itself is allowed", root: "/repos", workDir: "/repos", want: "/repos"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspaceWorkDir(tc.root, tc.workDir, "my-session")
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("got %q, %v; want ErrInvalidInput", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got %q (err %v), want %q", got, err, tc.want)
			}
		})
	}
}

func TestValidateGuestRoot(t *testing.T) {
	cases := []struct {
		name    string
		root    string
		want    string
		wantErr bool
	}{
		{name: "empty is the default", want: workspaceDir},
		{name: "an absolute path is kept", root: "/repos", want: "/repos"},
		{name: "a trailing slash is cleaned", root: "/repos/", want: "/repos"},
		{name: "a relative path is refused", root: "repos", wantErr: true},
		{name: "the agent's home is refused", root: "/root", wantErr: true},
		{name: "a folder inside the agent's home is refused", root: "/root/.claude", wantErr: true},
		{name: "the filesystem root is refused", root: "/", wantErr: true},
		{name: "a system directory is refused", root: "/etc", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateGuestRoot(tc.root)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("got %q, %v; want ErrInvalidInput", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got %q (err %v), want %q", got, err, tc.want)
			}
		})
	}
}

// Asked to take the sessions with it, the machine goes and so do they: a session cannot outlive the
// sandbox it lives on, and a row pointing at a machine that is gone is the state worth avoiding.
func TestDeleteWorkspaceVMWithItsSessions(t *testing.T) {
	ctx := context.Background()
	svc, lister := workspaceService(t)

	first := onWorkspace(t, svc, "first", "")
	second := onWorkspace(t, svc, "second", "second")
	vm, err := svc.WorkspaceVM(ctx)
	if err != nil {
		t.Fatalf("WorkspaceVM: %v", err)
	}

	if err := svc.DeleteWorkspaceVM(ctx, true); err != nil {
		t.Fatalf("DeleteWorkspaceVM: %v", err)
	}

	for _, sess := range []*Session{first, second} {
		if _, err := svc.Get(ctx, sess.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %s survived: %v", sess.Name, err)
		}
	}
	if !lister.destroyed[sandbox.ID(vm.SandboxID)] {
		t.Errorf("destroyed %v, want the machine %q", lister.destroyed, vm.SandboxID)
	}
	if _, err := svc.WorkspaceVM(ctx); !errors.Is(err, ErrNoWorkspaceVM) {
		t.Errorf("got %v, want ErrNoWorkspaceVM", err)
	}
}

// A machine that fails to boot leaves the record pointing at a sandbox that was destroyed. The next
// call has to build a fresh one rather than trust that id forever.
func TestEnsureWorkspaceVMRecoversFromAFailedStart(t *testing.T) {
	ctx := context.Background()
	svc, lister := workspaceService(t)

	lister.startErr = errors.New("boom")
	if _, err := svc.EnsureWorkspaceVM(ctx); err == nil {
		t.Fatal("EnsureWorkspaceVM: want the start failure")
	}
	failed, err := svc.WorkspaceVM(ctx)
	if err != nil {
		t.Fatalf("WorkspaceVM: %v", err)
	}
	if failed.SandboxID == "" {
		t.Error("the record forgot the machine it tried to build")
	}

	lister.startErr = nil
	built, err := svc.EnsureWorkspaceVM(ctx)
	if err != nil {
		t.Fatalf("EnsureWorkspaceVM after the failure: %v", err)
	}
	if built.SandboxID == failed.SandboxID {
		t.Errorf("reused %q, the sandbox the failed start destroyed", failed.SandboxID)
	}
	stored, err := svc.WorkspaceVM(ctx)
	if err != nil {
		t.Fatalf("WorkspaceVM: %v", err)
	}
	if stored.SandboxID != built.SandboxID {
		t.Errorf("record says %q, want the rebuilt %q", stored.SandboxID, built.SandboxID)
	}
}

// A runtime that cannot enumerate its sandboxes cannot host the shared machine: Ensure would build a
// new one on every call and leak the last.
func TestWorkspaceVMNeedsARuntimeThatCanList(t *testing.T) {
	ctx := context.Background()
	// newTestService registers a plain fake, which is not a Lister.
	svc, _, _, _ := newTestService(t)

	_, err := svc.SaveWorkspaceVM(ctx, WorkspaceVMRequest{HostRoot: t.TempDir()})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("got %v, want ErrInvalidInput", err)
	}
}

// The folder has to be one this node may share. Refused when it is picked, not when the VM is built.
func TestWorkspaceVMRefusesAFolderOutsideTheAllowedRoots(t *testing.T) {
	ctx := context.Background()
	svc, _ := workspaceService(t)
	svc.SetShareRoots(sandbox.ShareRoots{Allowed: []string{t.TempDir()}})

	_, err := svc.SaveWorkspaceVM(ctx, WorkspaceVMRequest{HostRoot: t.TempDir()})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("got %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(fmt.Sprint(err), "only folders inside") {
		t.Errorf("error %v, want it to say which folders can be shared", err)
	}
}

// The machine's setup script is what installs the agent, so it has to reach the driver's spec.
func TestWorkspaceVMSetupScriptReachesTheSpec(t *testing.T) {
	ctx := context.Background()
	svc, lister := workspaceService(t)

	const script = "apk add --no-cache ripgrep"
	if _, err := svc.SaveWorkspaceVM(ctx, WorkspaceVMRequest{
		HostRoot:    t.TempDir(),
		SetupScript: script,
	}); err != nil {
		t.Fatalf("SaveWorkspaceVM: %v", err)
	}
	vm, err := svc.EnsureWorkspaceVM(ctx)
	if err != nil {
		t.Fatalf("EnsureWorkspaceVM: %v", err)
	}

	lister.mu.Lock()
	spec := lister.created[sandbox.ID(vm.SandboxID)]
	lister.mu.Unlock()
	if !slices.ContainsFunc(spec.Cmd, func(arg string) bool { return strings.Contains(arg, script) }) {
		t.Errorf("the entrypoint does not carry the setup script: %v", spec.Cmd)
	}
}

func TestSaveWorkspaceVMNeedsAVMRuntime(t *testing.T) {
	// A runtime that boots no VM has none to share, so recording one would hand the user something
	// labelled VM that is not.
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), newFakeSandbox(), RuntimeSandboxless)
	_, err := svc.SaveWorkspaceVM(context.Background(), WorkspaceVMRequest{HostRoot: t.TempDir()})
	if err == nil {
		t.Fatal("wanted a refusal on a node with no VM runtime")
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "VM runtime") {
		t.Errorf("err = %v, want it to say why", err)
	}
}

func TestWorkspaceVMSpec_RefusesAGuestRootInsideTheAgentHome(t *testing.T) {
	agent, err := resolveAgent("claude")
	if err != nil {
		t.Fatal(err)
	}
	saved := &WorkspaceVM{HostRoot: "/Users/me/code", GuestRoot: "/root/code", Image: "img"}

	if _, err := workspaceVMSpec(saved, agent, workspaceVMSizes(0, 0)); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("error = %v, want the saved guest root refused", err)
	}
}
