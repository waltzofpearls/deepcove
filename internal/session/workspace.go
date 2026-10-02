package session

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// WorkspaceVM is the shared machine on this node: one VM, several sessions, each working in its own
// folder under HostRoot.
//
// A record of its own rather than a session's sandbox, so the machine outlives every session on it and
// keeps its identity across restarts.
type WorkspaceVM struct {
	// HostRoot is the host folder shared into the VM. Sessions get a subfolder of it.
	HostRoot string `json:"host_root"`
	// GuestRoot is where that folder appears inside the VM, and the root every session's folder lives
	// under. Never empty: the store and SaveWorkspaceVM both resolve it.
	GuestRoot string `json:"guest_root"`
	Image     string `json:"image"`
	Runtime   string `json:"runtime"`
	// SetupScript runs once when the machine is built. Empty means the agent's default.
	SetupScript string    `json:"setup_script"`
	Resources   Resources `json:"resources"`
	// SandboxID is the sandbox currently backing the machine. It changes on every rebuild.
	SandboxID string    `json:"sandbox_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// workspaceVMHomeDiskID names the persistent /root the shared machine comes back to. A session passes
// its own id here and gets a home of its own; the machine has no session id, and a fixed name is the
// point: it is rebuilt on every restart and must remount the same home.
const workspaceVMHomeDiskID = "workspace"

// ErrNoWorkspaceVM means this node has no workspace VM, the normal state until someone creates one.
var ErrNoWorkspaceVM = errors.New("session: this node has no workspace VM")

// ErrWorkspaceVMInUse refuses to delete a machine that still has sessions on it.
var ErrWorkspaceVMInUse = errors.New("session: sessions are still on the workspace VM")

// WorkspaceVMStore persists the one workspace VM record.
type WorkspaceVMStore interface {
	Load(ctx context.Context) (*WorkspaceVM, error)
	Save(ctx context.Context, vm *WorkspaceVM) error
	SetSandbox(ctx context.Context, sandboxID string, now time.Time) error
	Delete(ctx context.Context) error
}

type duckdbWorkspaceVMStore struct{ db *sql.DB }

// NewDuckDBWorkspaceVMStore constructs a WorkspaceVMStore over the given database.
func NewDuckDBWorkspaceVMStore(database *sql.DB) WorkspaceVMStore {
	return &duckdbWorkspaceVMStore{db: database}
}

func (s *duckdbWorkspaceVMStore) Load(ctx context.Context) (*WorkspaceVM, error) {
	var (
		vm            WorkspaceVM
		guestRoot     sql.NullString
		setupScript   sql.NullString
		resourcesJSON sql.NullString
		sandboxID     sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT host_root, guest_root, image, runtime, setup_script, resources, sandbox_id, created_at, updated_at
		FROM workspace_vms ORDER BY created_at LIMIT 1
	`).Scan(&vm.HostRoot, &guestRoot, &vm.Image, &vm.Runtime, &setupScript, &resourcesJSON, &sandboxID,
		&vm.CreatedAt, &vm.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoWorkspaceVM
	}
	if err != nil {
		return nil, fmt.Errorf("session: load workspace vm: %w", err)
	}
	if resourcesJSON.Valid && strings.TrimSpace(resourcesJSON.String) != "" && resourcesJSON.String != "null" {
		if err := json.Unmarshal([]byte(resourcesJSON.String), &vm.Resources); err != nil {
			return nil, fmt.Errorf("session: unmarshal workspace resources: %w", err)
		}
	}
	if sandboxID.Valid {
		vm.SandboxID = sandboxID.String
	}
	vm.SetupScript = setupScript.String
	// A row written before the column existed has no guest root, and there was only one place it could
	// have been. Resolved here so nothing downstream has to handle an empty one.
	vm.GuestRoot = cmp.Or(guestRoot.String, workspaceDir)
	return &vm, nil
}

// Save replaces the record. Delete then insert in one transaction, which is how a table with no key
// stays single rowed.
func (s *duckdbWorkspaceVMStore) Save(ctx context.Context, vm *WorkspaceVM) error {
	resourcesJSON, err := json.Marshal(vm.Resources)
	if err != nil {
		return fmt.Errorf("session: marshal workspace resources: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("session: save workspace vm: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_vms`); err != nil {
		return fmt.Errorf("session: save workspace vm: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workspace_vms (host_root, guest_root, image, runtime, setup_script, resources, sandbox_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, vm.HostRoot, vm.GuestRoot, vm.Image, vm.Runtime, vm.SetupScript, string(resourcesJSON),
		vm.SandboxID, vm.CreatedAt, vm.UpdatedAt); err != nil {
		return fmt.Errorf("session: save workspace vm: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("session: save workspace vm: %w", err)
	}
	return nil
}

// SetSandbox records the sandbox now backing the machine.
func (s *duckdbWorkspaceVMStore) SetSandbox(ctx context.Context, sandboxID string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE workspace_vms SET sandbox_id = ?, updated_at = ?`, sandboxID, now); err != nil {
		return fmt.Errorf("session: record workspace sandbox: %w", err)
	}
	return nil
}

func (s *duckdbWorkspaceVMStore) Delete(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM workspace_vms`); err != nil {
		return fmt.Errorf("session: delete workspace vm: %w", err)
	}
	return nil
}

// SetDataDir records where this node keeps its state, which is the volume a workspace VM is sized
// against.
func (s *Service) SetDataDir(dir string) { s.dataDir = dir }

// SetShareRoots records which host folders may be shared into a sandbox, so a workspace VM can be
// refused when you pick it rather than when it is built. Same roots the drivers enforce.
func (s *Service) SetShareRoots(roots sandbox.ShareRoots) { s.shareRoots = roots }

// SetWorkspaceVMStore wires persistence for the shared machine.
func (s *Service) SetWorkspaceVMStore(store WorkspaceVMStore) { s.workspaceVM = store }

// WorkspaceVMRequest creates or reconfigures the shared machine. Empty fields take node defaults.
type WorkspaceVMRequest struct {
	HostRoot string `json:"host_root"`
	// GuestRoot is where the folder appears inside the VM. Empty means /workspace.
	GuestRoot   string    `json:"guest_root,omitempty"`
	Runtime     string    `json:"runtime,omitempty"`
	Image       string    `json:"image,omitempty"`
	SetupScript string    `json:"setup_script,omitempty"`
	Resources   Resources `json:"resources,omitempty"`
}

// workspaceRuntime returns the runtime for the shared machine, refusing one that cannot enumerate its
// sandboxes.
//
// EnsureWorkspaceVM asks "is the machine still there" on every call. A driver that cannot answer makes
// that a permanent no, so every call would build another machine and leak the last one.
func (s *Service) workspaceRuntime(name string) (sandbox.Sandbox, error) {
	runtime, err := s.runtimeByName(name)
	if err != nil {
		return nil, err
	}
	if _, ok := runtime.(sandbox.Lister); !ok {
		return nil, fmt.Errorf("%w: the %s runtime cannot host a workspace VM, because it cannot list its sandboxes",
			ErrInvalidInput, name)
	}
	return runtime, nil
}

// WorkspaceVM returns the record, or ErrNoWorkspaceVM when this node has none.
func (s *Service) WorkspaceVM(ctx context.Context) (*WorkspaceVM, error) {
	if s.workspaceVM == nil {
		return nil, ErrNoWorkspaceVM
	}
	return s.workspaceVM.Load(ctx)
}

// SaveWorkspaceVM records the shared machine without building it, so a mistyped folder fails here
// rather than halfway through a boot. A resize applies at the next rebuild.
func (s *Service) SaveWorkspaceVM(ctx context.Context, req WorkspaceVMRequest) (*WorkspaceVM, error) {
	if s.workspaceVM == nil {
		return nil, ErrNoWorkspaceVM
	}
	if err := s.ensureHostRoot(req.HostRoot); err != nil {
		return nil, err
	}
	guestRoot, err := validateGuestRoot(req.GuestRoot)
	if err != nil {
		return nil, err
	}
	if err := req.Resources.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	agent, err := resolveAgent(defaultAgent)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	// A workspace VM is a VM, whatever this node happens to default to.
	runtime, err := s.pickVMRuntime(req.Runtime)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	vm := &WorkspaceVM{
		HostRoot:    filepath.Clean(req.HostRoot),
		GuestRoot:   guestRoot,
		Image:       cmp.Or(req.Image, agent.Sandboxed.OfficialImage),
		Runtime:     runtime,
		SetupScript: req.SetupScript,
		Resources:   req.Resources,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// Refused here, when you pick it, rather than at the first session that lands on it.
	if _, err := s.workspaceRuntime(vm.Runtime); err != nil {
		return nil, err
	}
	// A machine that is already running keeps running: this changes the record, not the VM.
	if existing, err := s.workspaceVM.Load(ctx); err == nil {
		vm.SandboxID, vm.CreatedAt = existing.SandboxID, existing.CreatedAt
	}
	if err := s.workspaceVM.Save(ctx, vm); err != nil {
		return nil, err
	}
	s.logger.Info("workspace vm saved", "root", vm.HostRoot, "runtime", vm.Runtime)
	return vm, nil
}

// EnsureWorkspaceVM returns the shared machine with a running sandbox behind it, building one if there
// is none. Serialised, so sessions created at the same moment share a machine instead of racing to
// build two.
func (s *Service) EnsureWorkspaceVM(ctx context.Context) (*WorkspaceVM, error) {
	if s.workspaceVM == nil {
		return nil, ErrNoWorkspaceVM
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()

	vm, err := s.workspaceVM.Load(ctx)
	if err != nil {
		return nil, err
	}
	runtime, err := s.workspaceRuntime(vm.Runtime)
	if err != nil {
		return nil, err
	}
	if vm.SandboxID != "" && sandboxRunningOn(ctx, runtime, vm.SandboxID) {
		return vm, nil
	}
	agent, err := resolveAgent(defaultAgent)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}

	spec, err := workspaceVMSpec(vm, agent)
	if err != nil {
		return nil, err
	}
	sandboxID, err := runtime.Create(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("session: create workspace vm: %w", err)
	}
	// Point the record at the new sandbox before booting it: booting takes seconds, and a sandbox
	// nothing claims is reported as a leak.
	if err := s.workspaceVM.SetSandbox(ctx, string(sandboxID), time.Now().UTC()); err != nil {
		return nil, err
	}
	vm.SandboxID = string(sandboxID)

	if err := runtime.Start(ctx, sandboxID); err != nil {
		_ = runtime.Destroy(context.Background(), sandboxID)
		return nil, fmt.Errorf("session: start workspace vm: %w", err)
	}
	s.logger.Info("workspace vm built", "sandbox", sandboxID, "root", vm.HostRoot)
	return vm, nil
}

// DeleteWorkspaceVM destroys the machine and forgets it.
//
// withSessions deletes the sessions on it too. They cannot outlive the machine, since it is their
// sandbox, but losing them is the caller's call rather than a side effect: without it this refuses
// while any remain.
func (s *Service) DeleteWorkspaceVM(ctx context.Context, withSessions bool) error {
	if s.workspaceVM == nil {
		return ErrNoWorkspaceVM
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()

	vm, err := s.workspaceVM.Load(ctx)
	if err != nil {
		return err
	}
	sessions, err := s.List(ctx, ListFilter{IncludeAll: true})
	if err != nil {
		return err
	}
	var onIt []*Session
	for _, sess := range sessions {
		if !sess.OnWorkspaceVM {
			continue
		}
		if !withSessions {
			return ErrWorkspaceVMInUse
		}
		onIt = append(onIt, sess)
	}
	// Rows first: a session pointing at a machine that is already gone is the broken state this avoids.
	for _, sess := range onIt {
		if err := s.repo.Delete(ctx, placeholderOwner, sess.ID); err != nil {
			return fmt.Errorf("session: delete %s: %w", sess.ID, err)
		}
		s.previewCache.Delete(sess.ID)
		s.logger.Info("session deleted with the workspace vm", "id", sess.ID, "name", sess.Name)
	}

	if vm.SandboxID != "" {
		runtime, err := s.runtimeByName(vm.Runtime)
		if err != nil {
			return err
		}
		if err := runtime.Destroy(ctx, sandbox.ID(vm.SandboxID)); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
			return fmt.Errorf("session: destroy workspace vm: %w", err)
		}
		if err := runtime.RemoveStorage(ctx, workspaceVMHomeDiskID); err != nil &&
			!errors.Is(err, sandbox.ErrNotSupported) && !errors.Is(err, sandbox.ErrNotFound) {
			s.logger.Warn("could not remove workspace vm storage", "err", err)
		}
	}
	if err := s.workspaceVM.Delete(ctx); err != nil {
		return err
	}
	s.logger.Info("workspace vm deleted")
	return nil
}

// joinWorkspaceVM points a new session at the shared machine: its runtime, image and sandbox, plus a
// folder of the session's own to work in.
func (s *Service) joinWorkspaceVM(ctx context.Context, sess *Session, req CreateRequest) error {
	if len(req.Mounts) > 0 {
		return fmt.Errorf("%w: the workspace VM brings its own folder, so a session on it takes no mounts", ErrInvalidInput)
	}
	vm, err := s.EnsureWorkspaceVM(ctx)
	if err != nil {
		return err
	}
	workDir, err := workspaceWorkDir(vm.GuestRoot, req.WorkDir, sess.Name)
	if err != nil {
		return err
	}
	// The machine decides these: one VM, one runtime, one image, one size.
	sess.Runtime, sess.Image, sess.Resources = vm.Runtime, vm.Image, Resources{}
	sess.SandboxID = vm.SandboxID
	sess.OnWorkspaceVM = true
	sess.WorkDir = workDir
	return nil
}

// startOnWorkspaceVM brings a session back onto the shared machine, whatever sandbox is behind it now.
func (s *Service) startOnWorkspaceVM(ctx context.Context, sess *Session) error {
	vm, err := s.EnsureWorkspaceVM(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if vm.SandboxID != sess.SandboxID {
		if err := s.repo.UpdateMountsAndSandbox(ctx, placeholderOwner, sess.ID, sess.Mounts, vm.SandboxID, now); err != nil {
			return fmt.Errorf("session: rejoin workspace vm: %w", err)
		}
		sess.SandboxID = vm.SandboxID
	}
	if err := s.repo.MarkRunning(ctx, placeholderOwner, sess.ID, now); err != nil {
		return fmt.Errorf("session: mark running: %w", err)
	}
	s.background(func() { s.ensureAgentAfterSetup(sess, s.sessionWorkDir(sess)) })
	s.logger.Info("session started on the workspace vm", "id", sess.ID, "name", sess.Name)
	return nil
}

// workspaceVMSpec describes the shared machine to the driver: the host root shared at its guest root, and
// storage keyed to the machine rather than to a session. The guest root is checked again here, not just
// when it was saved, since every build and restart comes through this.
func workspaceVMSpec(vm *WorkspaceVM, agent AgentSpec) (sandbox.SandboxSpec, error) {
	if _, err := validateGuestRoot(vm.GuestRoot); err != nil {
		return sandbox.SandboxSpec{}, err
	}
	return newSandboxSpec(sandboxRequest{
		ID:        workspaceVMHomeDiskID,
		Name:      "workspace",
		Agent:     agent,
		Image:     vm.Image,
		Setup:     vm.SetupScript,
		Resources: vm.Resources,
		Mounts:    []sandbox.Mount{{Source: vm.HostRoot, Target: vm.GuestRoot}},
		WorkDir:   vm.GuestRoot,
	}), nil
}

// ensureHostRoot rejects a workspace root deepcove may not share, and creates one that is not there.
func (s *Service) ensureHostRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("%w: pick a folder for the workspace VM", ErrInvalidInput)
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("%w: %s is not an absolute path", ErrInvalidInput, root)
	}
	if err := s.ensureSharedFolder(filepath.Clean(root)); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}
	return nil
}

// workspaceWorkDir resolves a session's folder to an absolute path under the machine's root. A bare name
// is taken as a folder there, and no name means one named after the session: sharing the root would put
// every agent in the same directory.
func workspaceWorkDir(root, workDir, sessionName string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		workDir = sessionName
	}
	full := path.Clean(strings.TrimSpace(workDir))
	if !path.IsAbs(full) {
		full = path.Join(root, full)
	}
	if full == root || !strings.HasPrefix(full, root+"/") {
		return "", fmt.Errorf("%w: %s is not a folder inside %s", ErrInvalidInput, workDir, root)
	}
	return full, nil
}

// reservedGuestRoots are the directories a VM needs for itself; mounting the shared folder over any of
// these breaks the machine. The agent's home is checked separately, subfolders included, by
// underAgentHome: the home disk mounts there and the guest writes the agent's login into it.
var reservedGuestRoots = []string{
	"/", "/etc", "/usr", "/bin", "/sbin", "/lib", "/proc", "/sys", "/dev", "/var", "/tmp",
}

// validateGuestRoot resolves where the shared folder appears inside the VM. Empty means /workspace.
func validateGuestRoot(root string) (string, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return workspaceDir, nil
	}
	clean := path.Clean(trimmed)
	if !path.IsAbs(clean) {
		return "", fmt.Errorf("%w: %s is not an absolute path inside the VM", ErrInvalidInput, root)
	}
	if slices.Contains(reservedGuestRoots, clean) || underAgentHome(clean) {
		return "", fmt.Errorf("%w: %s is reserved inside the VM", ErrInvalidInput, clean)
	}
	return clean, nil
}
