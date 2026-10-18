package session

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// fakeProvisioner is a runtime that installs the agent itself, like the sandboxless one.
type fakeProvisioner struct {
	*fakeSandbox
	state          sandbox.SetupState
	launch         []string
	workDir        string
	setupErr       error
	agentInstalled bool
	adopted        []sandbox.ID
	adoptedSpecs   map[sandbox.ID]sandbox.SandboxSpec
}

func (f *fakeProvisioner) Setup(context.Context, sandbox.ID) (sandbox.SetupState, error) {
	return f.state, f.setupErr
}

func (f *fakeProvisioner) AgentLaunchCommand(sandbox.ID, string) ([]string, error) {
	return f.launch, nil
}

func (f *fakeProvisioner) WorkDir(string, []sandbox.SharedFolder) string { return f.workDir }

func (f *fakeProvisioner) WorkspaceRoot() string { return f.workDir }

func (f *fakeProvisioner) AgentInstalled(sandbox.Agent) bool { return f.agentInstalled }

func (f *fakeProvisioner) Adopt(_ context.Context, sandboxes map[sandbox.ID]sandbox.SandboxSpec) ([]sandbox.ID, error) {
	f.adoptedSpecs = sandboxes
	f.adopted = append(f.adopted, slices.Sorted(maps.Keys(sandboxes))...)
	return f.adopted, nil
}

func TestBootstrapOf(t *testing.T) {
	cases := []struct {
		name  string
		state sandbox.SetupState
		err   error
		want  BootstrapStatus
	}{
		{name: "installing", state: sandbox.SetupState{Status: sandbox.SetupInstalling}, want: BootstrapInstalling},
		{name: "ready", state: sandbox.SetupState{Status: sandbox.SetupReady}, want: BootstrapReady},
		{name: "failed", state: sandbox.SetupState{Status: sandbox.SetupFailed, Log: "no disk"}, want: BootstrapErrored},
		{name: "it cannot say", err: errFolderGone, want: BootstrapInstalling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newBareService(t)
			svc.RegisterRuntime(RuntimeSandboxless, &fakeProvisioner{
				fakeSandbox: newFakeSandbox(),
				state:       tc.state,
				setupErr:    tc.err,
			})
			sess := &Session{ID: "s1", Runtime: RuntimeSandboxless}
			got := svc.bootstrapOf(context.Background(), sess)
			if got.Status != tc.want {
				t.Errorf("status %q, want %q", got.Status, tc.want)
			}
			if tc.err == nil && got.Log != tc.state.Log {
				t.Errorf("log %q, want %q", got.Log, tc.state.Log)
			}
		})
	}
}

func TestReportWorkDir(t *testing.T) {
	provisioner := &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: "/data/sandboxless/workspaces/s1"}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), provisioner, RuntimeSandboxless)
	ctx := context.Background()

	created, err := svc.Create(ctx, CreateRequest{Agent: "claude", Name: "folder-please"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AgentWorkDir != provisioner.workDir {
		t.Errorf("Get says %q, want %q", got.AgentWorkDir, provisioner.workDir)
	}

	list, err := svc.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d sessions, want 1", len(list))
	}
	if list[0].AgentWorkDir != provisioner.workDir {
		t.Errorf("List says %q, want %q", list[0].AgentWorkDir, provisioner.workDir)
	}
}

func TestService_Create_RecordsNoImageOnThisMachine(t *testing.T) {
	provisioner := &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: "/data/sandboxless/workspaces/s1"}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), provisioner, RuntimeSandboxless)
	ctx := context.Background()

	sess, err := svc.Create(ctx, CreateRequest{Agent: "claude", Name: "no-image"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Image != "" {
		t.Errorf("the row says the image is %q", sess.Image)
	}

	if _, err := svc.Create(ctx, CreateRequest{
		Agent: "claude", Name: "with-image", Image: "alpine:3.20",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("asking for an image got %v, want it refused", err)
	}
}

func TestService_Create_RefusesWhatThisMachineCannotUse(t *testing.T) {
	cases := []struct {
		name string
		req  CreateRequest
	}{
		{name: "an image", req: CreateRequest{Image: "alpine:3.20"}},
		{name: "a setup script", req: CreateRequest{SetupScript: "apk add --no-cache curl"}},
		{
			name: "a shared folder",
			req:  CreateRequest{SharedFolders: []SharedFolderRequest{{HostPath: "/tmp/shared", GuestPath: "/workspace"}}},
		},
		{name: "cpu", req: CreateRequest{Resources: Resources{CPU: 2}}},
		{name: "memory", req: CreateRequest{Resources: Resources{MemoryMB: 4096}}},
		{name: "disk", req: CreateRequest{Resources: Resources{DiskMB: 8192}}},
	}
	provisioner := &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: "/data/sandboxless/workspaces/s1"}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), provisioner, RuntimeSandboxless)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.Agent, req.Name = "claude", "asking-for-"+tc.name
			if _, err := svc.Create(context.Background(), req); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("asking for %s got %v, want it refused", tc.name, err)
			}
		})
	}
}

func TestStartAgentSession_LeavesAPickedFolderAlone(t *testing.T) {
	provisioner := &fakeProvisioner{
		fakeSandbox: newFakeSandbox(),
		state:       sandbox.SetupState{Status: sandbox.SetupReady},
		workDir:     "/data/sandboxless/workspaces/s1",
	}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), provisioner, RuntimeSandboxless)
	root := t.TempDir()
	svc.SetShareRoots(sandbox.ShareRoots{Allowed: []string{root}})
	ctx := context.Background()

	sess, err := svc.Create(ctx, CreateRequest{
		Agent: "claude", Name: "picked", WorkDir: filepath.Join(root, "code"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// has-session fails, then new-session and set-option succeed.
	provisioner.execResponses = []execResponse{{exit: 1}, {exit: 0}, {exit: 0}}
	provisioner.execCalls = nil

	if err := svc.startAgentSession(ctx, sess, svc.sessionWorkDir(sess)); err != nil {
		t.Fatalf("startAgentSession: %v", err)
	}
	for _, call := range provisioner.execCalls {
		// Making it back would hide that your folder is gone, and start the agent in an empty one.
		if call[0] == "mkdir" {
			t.Errorf("deepcove made a folder you picked: %v", call)
		}
	}
}

func TestAdoptRunningSandboxes(t *testing.T) {
	provisioner := &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: "/data/sandboxless/workspaces/s1"}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), provisioner, RuntimeSandboxless)
	svc.RegisterRuntime(RuntimeCloudHV, newFakeSandbox())
	ctx := context.Background()

	running, err := svc.Create(ctx, CreateRequest{Agent: "claude", Name: "running"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stopped, err := svc.Create(ctx, CreateRequest{Agent: "claude", Name: "stopped"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Stop(ctx, stopped.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := svc.AdoptRunningSandboxes(ctx); err != nil {
		t.Fatalf("AdoptRunningSandboxes: %v", err)
	}
	// Only what the table says is running, and only to the runtime that can take its sandboxes back.
	want := []sandbox.ID{sandbox.ID(running.SandboxID)}
	if got := provisioner.adopted; !slices.Equal(got, want) {
		t.Errorf("adopted %v, want %v", got, want)
	}
	// With the spec, a stop and start after a restart can install the agent again. Without it, that
	// fails with "the spec names no agent binary".
	if got := provisioner.adoptedSpecs[sandbox.ID(running.SandboxID)].Agent.Name; got != "claude" {
		t.Errorf("adopted a spec naming agent %q, want claude", got)
	}
}

func TestAdoptRunningSandboxes_ASessionItCannotDescribe(t *testing.T) {
	provisioner := &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: "/data/sandboxless/workspaces/s1"}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), provisioner, RuntimeSandboxless)
	ctx := context.Background()

	now := time.Now().UTC()
	err := svc.repo.Insert(ctx, &Session{
		ID:             "019ff000-0000-7000-8000-000000000042",
		Name:           "made-with-an-agent-we-no-longer-have",
		Agent:          "gone",
		SandboxID:      "sb-gone",
		Runtime:        RuntimeSandboxless,
		OwnerEmail:     placeholderOwner,
		Status:         StatusRunning,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := svc.AdoptRunningSandboxes(ctx); err != nil {
		t.Fatalf("AdoptRunningSandboxes: %v", err)
	}
	// Asked for anyway. A sandbox the runtime is not asked about is one it stops, and this session's
	// agent is running.
	if got := provisioner.adopted; !slices.Contains(got, sandbox.ID("sb-gone")) {
		t.Errorf("asked for %v, which leaves sb-gone to be swept", got)
	}
}

func TestEnsureAgentAfterSetup(t *testing.T) {
	cases := []struct {
		name string
		// gone deletes the session instead of stopping it.
		gone bool
	}{
		{name: "the session was stopped while its agent was being installed"},
		{name: "the session was deleted while its agent was being installed", gone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := machineService(t)
			ctx := context.Background()
			sess, err := svc.Create(ctx, CreateRequest{Agent: "claude", Name: "going-away"})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if tc.gone {
				err = svc.Destroy(ctx, sess.ID)
			} else {
				err = svc.Stop(ctx, sess.ID)
			}
			if err != nil {
				t.Fatalf("arrange: %v", err)
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				svc.ensureAgentAfterSetup(sess, "/data/sandboxless/workspaces/s1")
			}()
			select {
			case <-done:
			// A sandbox that is gone reads as still installing, so this would otherwise sit here for
			// five minutes and then start the agent of a session the user is done with.
			case <-time.After(3 * time.Second):
				t.Fatal("still waiting for a session that is not coming back")
			}
		})
	}
}

func TestStartAgentSession_Refuses(t *testing.T) {
	cases := []struct {
		name string
		// arrange returns the service and the session, in the state the case is about.
		arrange func(t *testing.T) (*Service, *Session)
		wantErr string
	}{
		{
			name: "a session the user stopped",
			arrange: func(t *testing.T) (*Service, *Session) {
				svc := machineService(t)
				sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Name: "stopped"})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if err := svc.Stop(context.Background(), sess.ID); err != nil {
					t.Fatalf("Stop: %v", err)
				}
				return svc, sess
			},
			// A stop before the agent exists kills nothing, so starting it now would undo the stop.
			wantErr: "is stopped",
		},
		{
			name: "a session whose folder nobody can name",
			arrange: func(t *testing.T) (*Service, *Session) {
				return newBareService(t), &Session{ID: "s1", Name: "s", Runtime: RuntimeSandboxless}
			},
			wantErr: "which folder",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, sess := tc.arrange(t)

			err := svc.startAgentSession(context.Background(), sess, svc.sessionWorkDir(sess))
			if err == nil {
				t.Fatal("the agent was started")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// machineService is a service whose only runtime keeps its sessions on the machine, with its agent
// still being installed.
func machineService(t *testing.T) *Service {
	t.Helper()
	provisioner := &fakeProvisioner{
		fakeSandbox: newFakeSandbox(),
		state:       sandbox.SetupState{Status: sandbox.SetupInstalling},
		workDir:     "/data/sandboxless/workspaces/s1",
	}
	return newServiceWithNamedRuntime(t, db.OpenForTest(t), provisioner, RuntimeSandboxless)
}

func TestService_EditSharedFolders_RefusedOnThisMachine(t *testing.T) {
	provisioner := &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: "/data/sandboxless/workspaces/s1"}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), provisioner, RuntimeSandboxless)
	root := t.TempDir()
	svc.SetShareRoots(sandbox.ShareRoots{Allowed: []string{root}})
	shared := filepath.Join(root, "never-made")
	ctx := context.Background()

	sess, err := svc.Create(ctx, CreateRequest{Agent: "claude", Name: "folders"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = svc.EditSharedFolders(ctx, sess.ID, []SharedFolderRequest{{HostPath: shared, GuestPath: "/workspace"}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("EditSharedFolders: %v, want it refused", err)
	}
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Errorf("%s was created for an edit that could never finish", shared)
	}
	after, err := svc.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.SandboxID != sess.SandboxID {
		t.Errorf("the sandbox changed from %q to %q", sess.SandboxID, after.SandboxID)
	}
	if provisioner.destroyed[sandbox.ID(sess.SandboxID)] {
		t.Error("the old sandbox was destroyed by a call that could never finish")
	}
}

func TestService_Create_MakesNoFoldersWhenRefused(t *testing.T) {
	provisioner := &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: "/data/sandboxless/workspaces/s1"}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), provisioner, RuntimeSandboxless)
	root := t.TempDir()
	svc.SetShareRoots(sandbox.ShareRoots{Allowed: []string{root}})
	shared := filepath.Join(root, "never-made")

	_, err := svc.Create(context.Background(), CreateRequest{
		Agent: "claude", Name: "mounted",
		SharedFolders: []SharedFolderRequest{{HostPath: shared, GuestPath: "/workspace"}},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Create: %v, want it refused", err)
	}
	// deepcove makes the folders a request names, and a create that cannot succeed must make none.
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Errorf("%s was created for a request that could never succeed", shared)
	}
}

func TestSessionWorkDir(t *testing.T) {
	cases := []struct {
		name      string
		noRuntime bool
		sess      *Session
		want      string
	}{
		{
			name: "the folder the session was given",
			sess: &Session{ID: "s1", Runtime: RuntimeSandboxless, WorkDir: "/home/deep/code"},
			want: "/home/deep/code",
		},
		{
			name: "the folder its runtime makes",
			sess: &Session{ID: "s1", Runtime: RuntimeSandboxless},
			want: "/data/sandboxless/workspaces/s1",
		},
		{
			name:      "nothing, when there is no runtime to ask",
			noRuntime: true,
			sess:      &Session{ID: "s1", Runtime: RuntimeSandboxless},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newBareService(t)
			if !tc.noRuntime {
				svc.RegisterRuntime(RuntimeSandboxless, &fakeProvisioner{
					fakeSandbox: newFakeSandbox(),
					workDir:     "/data/sandboxless/workspaces/s1",
				})
			}
			// /workspace is a folder inside a guest. Guessing it here would make one on the user's disk.
			if got := svc.sessionWorkDir(tc.sess); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildSandboxSpec(t *testing.T) {
	svc := newBareService(t)
	provisioner := &fakeProvisioner{fakeSandbox: newFakeSandbox(), workDir: "/data/sandboxless/workspaces/s1"}
	svc.RegisterRuntime(RuntimeSandboxless, provisioner)
	sess := &Session{ID: "s1", Name: "n", Agent: "claude", Runtime: RuntimeSandboxless}

	spec := svc.buildSandboxSpec(sess, AgentSpec{Name: "claude"}, nil, provisioner)

	// The launch script cds into $DEEPCOVE_WORKDIR, so the spec and the agent start have to name the
	// same folder. They did not, and every sandboxless session died on `cd`.
	if got, want := spec.Env["DEEPCOVE_WORKDIR"], svc.sessionWorkDir(sess); got != want {
		t.Errorf("DEEPCOVE_WORKDIR is %q, want %q", got, want)
	}
	// This runtime has no guest, which is what lets the driver refuse every mount it sees.
	if len(spec.SharedFolders) != 0 {
		t.Errorf("spec carries %v, want no shared folders", spec.SharedFolders)
	}
}

func TestService_Create_KeepsAWorkspaceFolderName(t *testing.T) {
	svc, _ := workspaceService(t)
	svc.SetShareRoots(sandbox.ShareRoots{Allowed: []string{t.TempDir()}})

	// The shared machine resolves a folder name under its own root, so create must not read it as a
	// path on this machine and refuse it.
	sess := onWorkspace(t, svc, "with-a-folder", "my-repo")
	if want := "/workspace/my-repo"; sess.WorkDir != want {
		t.Errorf("work dir %q, want %q", sess.WorkDir, want)
	}
}

func TestService_Create_IgnoresAWorkDirOnAGuestRuntime(t *testing.T) {
	svc := newBareService(t)
	svc.RegisterRuntime(RuntimeCloudHV, newFakeSandbox())
	svc.SetShareRoots(sandbox.ShareRoots{Allowed: []string{t.TempDir()}})

	sess, err := svc.Create(context.Background(), CreateRequest{
		Agent:   "claude",
		Name:    "sandboxed-with-work-dir",
		Runtime: RuntimeCloudHV,
		WorkDir: "/tmp/somewhere-on-the-machine",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.WorkDir != "" {
		t.Errorf("work dir %q, want none: the agent works inside the guest", sess.WorkDir)
	}
}
