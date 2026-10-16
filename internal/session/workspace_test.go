package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func TestWorkspaceVMStore(t *testing.T) {
	ctx := context.Background()
	database := db.OpenForTest(t)
	store := NewDuckDBWorkspaceVMStore(database)
	now := time.Now().UTC().Truncate(time.Second)

	if _, err := store.Load(ctx); !errors.Is(err, ErrNoWorkspaceVM) {
		t.Fatalf("load with nothing stored: got %v, want ErrNoWorkspaceVM", err)
	}

	want := &WorkspaceVM{
		HostRoot:  "/Users/rollie/Dev",
		GuestRoot: "/repos",
		Image:     "ghcr.io/waltzofpearls/deepcove-agents:latest",
		Runtime:   RuntimeAppleVF,
		Resources: Resources{CPU: 4, MemoryMB: 8192, DiskMB: 32768},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.HostRoot != want.HostRoot || got.Image != want.Image || got.Runtime != want.Runtime {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.GuestRoot != want.GuestRoot {
		t.Errorf("guest root: got %q, want %q", got.GuestRoot, want.GuestRoot)
	}
	if got.Resources != want.Resources {
		t.Errorf("resources: got %+v, want %+v", got.Resources, want.Resources)
	}
	if got.SandboxID != "" {
		t.Errorf("sandbox id: got %q, want empty before the machine is built", got.SandboxID)
	}

	// A second save replaces rather than adds: the table has no key holding it to one row.
	want.HostRoot = "/Users/rollie/work"
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save again: %v", err)
	}
	var rows int
	if err := database.QueryRow(`SELECT count(*) FROM workspace_vms`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("got %d rows, want 1", rows)
	}

	later := now.Add(time.Minute)
	if err := store.SetSandbox(ctx, "applevf-abc123", later); err != nil {
		t.Fatalf("SetSandbox: %v", err)
	}
	got, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load after SetSandbox: %v", err)
	}
	if got.SandboxID != "applevf-abc123" {
		t.Errorf("sandbox id: got %q, want applevf-abc123", got.SandboxID)
	}
	if got.HostRoot != want.HostRoot {
		t.Errorf("host root changed to %q; SetSandbox must touch only the handle", got.HostRoot)
	}

	if err := store.Delete(ctx); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Load(ctx); !errors.Is(err, ErrNoWorkspaceVM) {
		t.Errorf("load after delete: got %v, want ErrNoWorkspaceVM", err)
	}
}

// The two new session columns have to survive a write and a read, or a stopped session forgets it
// belongs on the shared machine, which is the bug this replaces.
func TestSessionKeepsItsWorkspacePlacement(t *testing.T) {
	ctx := context.Background()
	database := db.OpenForTest(t)
	repo := NewDuckDBSessionRepository(database)
	now := time.Now().UTC()

	sess := &Session{
		ID:             "019ff000-0000-7000-8000-000000000001",
		Name:           "placement",
		Agent:          "claude",
		Image:          "alpine:3.20",
		OwnerEmail:     placeholderOwner,
		Status:         StatusRunning,
		Runtime:        RuntimeAppleVF,
		OnWorkspaceVM:  true,
		WorkDir:        "/workspace/deepcove",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	if err := repo.Insert(ctx, sess); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.Get(ctx, placeholderOwner, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.OnWorkspaceVM {
		t.Error("on_workspace_vm did not survive the round trip")
	}
	if got.WorkDir != sess.WorkDir {
		t.Errorf("work_dir: got %q, want %q", got.WorkDir, sess.WorkDir)
	}

	// A session that predates the columns reads back as a private session, which is what it is.
	plain := *sess
	plain.ID = "019ff000-0000-7000-8000-000000000002"
	plain.Name = "plain"
	plain.OnWorkspaceVM = false
	plain.WorkDir = ""
	if err := repo.Insert(ctx, &plain); err != nil {
		t.Fatalf("Insert plain: %v", err)
	}
	got, err = repo.Get(ctx, placeholderOwner, plain.ID)
	if err != nil {
		t.Fatalf("Get plain: %v", err)
	}
	if got.OnWorkspaceVM || got.WorkDir != "" {
		t.Errorf("got %+v, want a private session with no work dir", got)
	}
}

// A record written before the guest root was a setting has to read back usable, since the service
// treats an empty one as a bug rather than a case to handle.
func TestWorkspaceVMGuestRootDefaults(t *testing.T) {
	ctx := context.Background()
	database := db.OpenForTest(t)
	store := NewDuckDBWorkspaceVMStore(database)

	if _, err := database.Exec(`
		INSERT INTO workspace_vms (host_root, image, runtime, created_at, updated_at)
		VALUES ('/Users/rollie/Dev', 'img', 'applevf', now(), now())
	`); err != nil {
		t.Fatalf("insert a legacy row: %v", err)
	}

	vm, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if vm.GuestRoot != workspaceDir {
		t.Errorf("guest root %q, want %q", vm.GuestRoot, workspaceDir)
	}
}

func TestWorkspaceVMSpec_CarriesEveryAgent(t *testing.T) {
	agent, _ := resolveAgent(defaultAgent)
	sizes := workspaceVMSizes(32<<10, 4<<10)
	vm := &WorkspaceVM{HostRoot: t.TempDir(), GuestRoot: "/workspace", Resources: Resources{Size: VMSizeSmall}}
	spec, err := workspaceVMSpec(vm, agent, sizes)
	if err != nil {
		t.Fatalf("workspaceVMSpec: %v", err)
	}
	entrypoint := strings.Join(spec.Cmd, " ")
	for _, other := range allAgents() {
		if !strings.Contains(entrypoint, other.Sandboxed.Setup) {
			t.Errorf("the workspace VM does not install %s", other.Name)
		}
		if !strings.Contains(entrypoint, "cat > "+sandbox.SandboxedAgentLaunchPath(other.Name)) {
			t.Errorf("the workspace VM has no launcher for %s", other.Name)
		}
		if !slices.Contains(spec.SharedLoginAgents, other.Name) {
			t.Errorf("the workspace VM cannot get %s's shared login", other.Name)
		}
	}
}

func TestNewSandboxSpec_ASessionMachineCarriesOnlyItsAgent(t *testing.T) {
	codex, _ := resolveAgent("codex")
	spec := newSandboxSpec(sandboxRequest{ID: "s1", Name: "s1", Agent: codex, WorkDir: "/workspace"})
	entrypoint := strings.Join(spec.Cmd, " ")
	if !strings.Contains(entrypoint, "cat > "+sandbox.SandboxedAgentLaunchPath("codex")) {
		t.Error("a Codex session's machine has no Codex launcher")
	}
	if strings.Contains(entrypoint, sandbox.SandboxedAgentLaunchPath("claude")) {
		t.Error("a Codex session's machine also starts Claude")
	}
	if !slices.Equal(spec.SharedLoginAgents, []string{"codex"}) {
		t.Errorf("shared logins %v, want only Codex's", spec.SharedLoginAgents)
	}
}
