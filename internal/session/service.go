package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// scratchMountTarget is the container path of the deepcove-managed
// scratch workspace mount. User-supplied mounts can't collide with
// this — see validateMountRequests.
const scratchMountTarget = "/workspace"

// agentHomeMountTarget is where the per-session agent-home volume
// is mounted inside the sandbox. It holds `~/.claude/`, `.gitconfig`,
// shell history, and any other state Claude/codex/gemini write to
// $HOME. The named volume survives container destroy+recreate (used
// by EditMounts), so the user's conversation history isn't lost when
// they reconfigure mounts.
//
// `/root` is the right path today because the deepcove agent images
// (and the placeholder alpine setup) run as root. Future agent
// images that use a non-root user will need a per-image override —
// out of scope here.
const agentHomeMountTarget = "/root"

// agentHomeVolumeName is the Docker named-volume name for the
// agent-home mount. Embedding the session id makes the volume
// trivially traceable back to its owning session ("docker volume ls
// | grep deepcove" picks them all out) and makes cleanup
// straightforward in Destroy.
func agentHomeVolumeName(sessionID string) string {
	return "deepcove-session-" + sessionID + "-home"
}

// ErrMountInvalid is the sentinel for user mount validation failures.
// HTTP handlers map this to 400. The wrapped error message tells the
// caller which mount and why.
var ErrMountInvalid = errors.New("session: mount invalid")

// placeholderOwner is the owner_email recorded for new sessions until
// the identity layer (tsnet WhoIs middleware, first-run owner
// persistence) lands. Search for this constant to find everywhere
// the eventual identity wiring needs to slot in.
const placeholderOwner = "local"

// ErrNotFound is returned when a session id doesn't exist. HTTP
// handlers map this to a 404. Surfaced by both the Service (which
// re-returns repo errors) and SessionRepository implementations
// (returned from Get / Mark* / UpdateActivity / Delete when zero
// rows match).
var ErrNotFound = errors.New("session: not found")

// Service is the session lifecycle layer. It composes a
// SessionRepository (for persistence), a sandbox.Sandbox driver (for
// container runtime), and the host data directory so each session
// can have its own workspace under $DataDir/sessions/<id>/.
//
// All SQL lives in SessionRepository implementations — Service
// methods are pure orchestration. Owner_email is threaded through
// to the repo on every call; today every caller passes the
// placeholder owner, but the shape is ready for identity wiring.
type Service struct {
	repo     SessionRepository
	sandbox  sandbox.Sandbox
	dataDir  string
	sessions string // $dataDir/sessions
	logger   *slog.Logger

	// previewCache holds the most recent successful capture-pane
	// snapshot per session id. We serve it back when:
	//   - the session is stopped (so the UI can show a dimmed
	//     last-seen view + status overlay instead of a black void),
	//   - a live capture transiently fails (tmux not yet ready
	//     after a stop/start cycle).
	// In-memory only; rebuilt on service restart.
	previewCache *previewCache
}

// NewService constructs a Service. dataDir is the node's data
// directory (e.g. ~/Library/Application Support/deepcove); sessions
// live under $dataDir/sessions/<id>/.
func NewService(repo SessionRepository, sb sandbox.Sandbox, dataDir string, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Service{
		repo:         repo,
		sandbox:      sb,
		dataDir:      dataDir,
		sessions:     filepath.Join(dataDir, "sessions"),
		logger:       logger,
		previewCache: newPreviewCache(previewCacheMaxEntries),
	}
}

// Create allocates a new session, creates a sandbox, starts it, and
// persists the row. On any failure after the workspace dir is made,
// it rolls back the sandbox and the workspace so the caller doesn't
// leave orphans behind.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Session, error) {
	if req.Agent == "" {
		return nil, fmt.Errorf("agent is required")
	}
	image, err := resolveImage(req.Agent)
	if err != nil {
		return nil, err
	}

	// Validate user mounts BEFORE the workspace dir + sandbox are
	// created — so we reject bad input as a clean 400 without any
	// rollback. The sandbox layer's mount validator runs again on
	// Create with the host-side checks (absolute, cleaned, under an
	// allowed root); this pass owns session-layer concerns:
	// /workspace collisions, duplicate targets, mode validation.
	userMounts, err := validateMountRequests(req.Mounts)
	if err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("session: generate id: %w", err)
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:             id.String(),
		Name:           req.Name,
		Agent:          req.Agent,
		Image:          image,
		OwnerEmail:     placeholderOwner,
		Status:         StatusCreated,
		Env:            req.Env,
		Mounts:         req.Mounts,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	if sess.Name == "" {
		sess.Name = generateName(req.Agent)
	}

	// Workspace dir lives under $dataDir/sessions/<id>/ and gets
	// bind-mounted into the sandbox at /workspace.
	workspace := filepath.Join(s.sessions, sess.ID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, fmt.Errorf("session: mkdir workspace: %w", err)
	}

	spec := s.buildSandboxSpec(sess.ID, image, req.Env, workspace, userMounts)

	handle, err := s.sandbox.Create(ctx, spec)
	if err != nil {
		_ = os.RemoveAll(workspace)
		return nil, fmt.Errorf("session: create sandbox: %w", err)
	}
	sess.ContainerID = string(handle)

	if err := s.sandbox.Start(ctx, handle); err != nil {
		_ = s.sandbox.Destroy(context.Background(), handle)
		_ = os.RemoveAll(workspace)
		return nil, fmt.Errorf("session: start sandbox: %w", err)
	}
	sess.Status = StatusRunning

	if err := s.repo.Insert(ctx, sess); err != nil {
		_ = s.sandbox.Stop(context.Background(), handle)
		_ = s.sandbox.Destroy(context.Background(), handle)
		_ = os.RemoveAll(workspace)
		return nil, fmt.Errorf("session: insert row: %w", err)
	}

	s.logger.Info("session created", "id", sess.ID, "name", sess.Name, "agent", sess.Agent)
	return sess, nil
}

// List returns sessions owned by the current owner (today, the
// placeholder owner). filter.IncludeAll=true returns stopped/errored
// sessions too; default is running only.
//
// Ordering is by last_activity_at DESC (defined by the repo) — the
// canvas needs "what was I just doing?" as the top card, not "what
// did I most recently spawn?" The activity column is bumped on every
// attach connect / 30s tick / disconnect, so it tracks user
// engagement, not creation order.
func (s *Service) List(ctx context.Context, filter ListFilter) ([]*Session, error) {
	return s.repo.List(ctx, placeholderOwner, filter.IncludeAll)
}

// Get returns the session with the given id, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (*Session, error) {
	return s.repo.Get(ctx, placeholderOwner, id)
}

// RecordActivity bumps last_activity_at to NOW for a session. Called
// by the attach handler on connect, periodic 30s ticker while
// attached, and on disconnect. Cheap (single indexed UPDATE) and
// safe to call frequently. Errors are returned but never fatal —
// callers may log-and-ignore.
func (s *Service) RecordActivity(ctx context.Context, id string) error {
	return s.repo.UpdateActivity(ctx, placeholderOwner, id, time.Now().UTC())
}

// Start brings a stopped session back to running. The underlying
// container is reused (Docker preserves its filesystem on stop),
// so the agent's workspace and any in-container state survive — but
// the original entrypoint Cmd runs again, which means tmux gets a
// fresh session named "main". In-container ephemeral state (running
// processes, scrollback) does NOT survive a stop/start cycle.
//
// Idempotent on already-running sessions (no-op).
func (s *Service) Start(ctx context.Context, id string) error {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if sess.Status == StatusRunning {
		return nil
	}
	if err := s.sandbox.Start(ctx, sandbox.Handle(sess.ContainerID)); err != nil {
		// If the Docker container has been removed out of band
		// (e.g. `docker rm` on the host), the session row is now
		// orphaned. Surface as ErrNotFound so the UI can offer a
		// recreate path. Today we just propagate.
		return fmt.Errorf("session: sandbox start: %w", err)
	}
	if err := s.repo.MarkRunning(ctx, placeholderOwner, id, time.Now().UTC()); err != nil {
		return fmt.Errorf("session: mark running: %w", err)
	}
	s.logger.Info("session started", "id", id, "name", sess.Name)
	return nil
}

// Stop transitions a running session to stopped: tells the sandbox
// to stop the container, updates the row. Idempotent: stopping an
// already-stopped session is a no-op success.
func (s *Service) Stop(ctx context.Context, id string) error {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if sess.Status == StatusStopped {
		return nil
	}
	if err := s.sandbox.Stop(ctx, sandbox.Handle(sess.ContainerID)); err != nil {
		// If the sandbox is already gone, finish the transition anyway.
		if !errors.Is(err, sandbox.ErrNotFound) {
			return fmt.Errorf("session: sandbox stop: %w", err)
		}
	}
	if err := s.repo.MarkStopped(ctx, placeholderOwner, id, time.Now().UTC()); err != nil {
		return fmt.Errorf("session: mark stopped: %w", err)
	}
	return nil
}

// Destroy stops the sandbox (if running), removes it, deletes the
// workspace directory, the agent-home volume, and removes the row.
func (s *Service) Destroy(ctx context.Context, id string) error {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	handle := sandbox.Handle(sess.ContainerID)
	// Best-effort sandbox teardown — if the daemon already lost the
	// container, we still want to clear our row + workspace + volume.
	if err := s.sandbox.Destroy(ctx, handle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return fmt.Errorf("session: sandbox destroy: %w", err)
	}
	// Remove the per-session agent-home volume. It survives container
	// removal by design, so this is the only place we drop it. Errors
	// are logged but don't block destroy — a leaked volume is annoying
	// but not catastrophic, and unblocking the user's destroy beats
	// failing the whole operation on a volume-cleanup edge case.
	if rerr := s.sandbox.RemoveStorage(ctx, agentHomeVolumeName(id)); rerr != nil && !errors.Is(rerr, sandbox.ErrNotFound) && !errors.Is(rerr, sandbox.ErrNotSupported) {
		s.logger.Warn("session: remove agent-home volume", "id", id, "err", rerr)
	}
	workspace := filepath.Join(s.sessions, id)
	if err := os.RemoveAll(workspace); err != nil {
		return fmt.Errorf("session: remove workspace: %w", err)
	}
	if err := s.repo.Delete(ctx, placeholderOwner, id); err != nil {
		return fmt.Errorf("session: delete row: %w", err)
	}
	s.previewCache.Delete(id)
	return nil
}

// EditMounts replaces a session's user mounts. Docker can't add or
// remove bind mounts on a live container, so we destroy the current
// container and recreate it with the new spec — the session id stays
// the same, the agent-home volume (and its /root contents) survives,
// and /workspace (host bind mount) survives.
//
// State that DOES NOT survive: anything in the container's writable
// layer that wasn't under /root or /workspace. Today's placeholder
// image installs tmux at first boot, so the new container reinstalls
// it; real agent images will bake tmux in and the recreate is
// near-instant.
//
// On any failure mid-recreate, the session is left as "errored"
// rather than leaking partial state. The caller's UI is expected to
// show the error and offer destroy as the recovery path.
func (s *Service) EditMounts(ctx context.Context, id string, newMounts []MountRequest) (*Session, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// Validate the new mount set BEFORE touching the running
	// container — a clean 400 on invalid input doesn't leave the
	// session in an in-between state.
	userMounts, err := validateMountRequests(newMounts)
	if err != nil {
		return nil, err
	}

	oldHandle := sandbox.Handle(sess.ContainerID)
	wasRunning := sess.Status == StatusRunning

	// Stop the old container if it's running. Idempotent on already-
	// stopped containers via the sandbox layer's ErrNotFound tolerance.
	if wasRunning {
		if err := s.sandbox.Stop(ctx, oldHandle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
			return nil, fmt.Errorf("session: stop sandbox: %w", err)
		}
	}
	// Destroy the old container. Named volumes (agent-home) survive;
	// anonymous volumes are removed by the driver's Destroy. Workspace
	// dir is a host bind mount and untouched.
	if err := s.sandbox.Destroy(ctx, oldHandle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return nil, fmt.Errorf("session: destroy old sandbox: %w", err)
	}

	workspace := filepath.Join(s.sessions, sess.ID)
	spec := s.buildSandboxSpec(sess.ID, sess.Image, sess.Env, workspace, userMounts)

	newHandle, err := s.sandbox.Create(ctx, spec)
	if err != nil {
		// Best-effort: mark errored so the row reflects reality.
		// The session is now in a half-state (no container), and
		// the user's recovery path is destroy.
		_ = s.repo.MarkStopped(ctx, placeholderOwner, id, time.Now().UTC())
		return nil, fmt.Errorf("session: create new sandbox: %w", err)
	}
	if wasRunning {
		if err := s.sandbox.Start(ctx, newHandle); err != nil {
			_ = s.sandbox.Destroy(context.Background(), newHandle)
			_ = s.repo.MarkStopped(ctx, placeholderOwner, id, time.Now().UTC())
			return nil, fmt.Errorf("session: start new sandbox: %w", err)
		}
	}

	now := time.Now().UTC()
	if err := s.repo.UpdateMountsAndContainer(ctx, placeholderOwner, id, newMounts, string(newHandle), now); err != nil {
		// Row update failed AFTER the new container is up — leave the
		// new container running and surface the error. Next Get
		// returns the stale container_id; manual repair via destroy
		// is the fallback.
		return nil, fmt.Errorf("session: update mounts row: %w", err)
	}
	// Bumping last_activity here doesn't undo "stop+recreate is a
	// user action," but it pins the canvas ordering — a session you
	// just reconfigured shouldn't drop to the bottom of the grid.
	_ = s.repo.UpdateActivity(ctx, placeholderOwner, id, now)

	// Re-Get to return the canonical row. Cheaper than mutating the
	// in-memory sess and possibly drifting from the DB representation.
	updated, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.logger.Info("session mounts edited", "id", id, "name", sess.Name, "mounts", len(newMounts))
	return updated, nil
}

// WorkspaceRoot is the host directory whose subdirectories the
// sandbox driver may bind-mount. Callers pass this to the driver
// constructor so SPEC §3 (sessions never cross host boundaries) is
// enforced by the mount-source validator.
func (s *Service) WorkspaceRoot() string {
	return s.sessions
}

// buildSandboxSpec assembles a SandboxSpec for a session. Shared by
// Create (initial container) and EditMounts (replacement container)
// so the entrypoint Cmd, network mode, scratch workspace mount, and
// agent-home volume entry stay identical across the two paths — drift
// here would mean a recreated container behaves subtly differently
// than the one it replaced.
//
// Mount order:
//  1. scratch workspace bind  (/workspace)
//  2. agent-home volume       (/root, survives recreate)
//  3. user mounts             (caller-supplied, validated upstream)
//
// User mounts are stacked last so a user can't shadow /workspace or
// /root via path-collision tricks — the validator already rejects
// those targets, this order is belt-and-suspenders.
func (s *Service) buildSandboxSpec(
	sessionID, image string,
	env map[string]string,
	workspace string,
	userMounts []sandbox.Mount,
) sandbox.SandboxSpec {
	mounts := []sandbox.Mount{
		{
			Source: workspace,
			Target: scratchMountTarget,
			Type:   sandbox.MountBind,
		},
		{
			Source: agentHomeVolumeName(sessionID),
			Target: agentHomeMountTarget,
			Type:   sandbox.MountVolume,
		},
	}
	mounts = append(mounts, userMounts...)

	return sandbox.SandboxSpec{
		Image: image,
		// Placeholder Cmd: install tmux at runtime (first boot only;
		// subsequent restarts find it already installed) and start a
		// detached tmux session named "main" — the attach handler
		// exec's `tmux attach -t main` against it. `exec sleep
		// infinity` keeps PID 1 alive.
		//
		// Restart safety: when a container is stopped + started, the
		// filesystem persists (so tmux stays installed) but the
		// previous tmux server's socket at /tmp/tmux-* is left behind
		// as a stale file. A fresh tmux refuses to reuse it and fails
		// silently from the user's POV. The `rm -rf /tmp/tmux-*`
		// clears any leftover before we recreate the session. Real
		// agent images will bake tmux in and replace this Cmd
		// entirely.
		Cmd: []string{"sh", "-c",
			"command -v tmux >/dev/null 2>&1 || apk add --no-cache tmux >/dev/null 2>&1; " +
				"rm -rf /tmp/tmux-* 2>/dev/null; " +
				// -n main names the first window "main" explicitly
				// so the windows API has a stable, well-known name
				// to target. Without -n, tmux derives the window
				// name from the running command ("sh"), which is
				// neither meaningful nor stable across agents.
				"tmux new-session -d -s main -n main sh; " +
				"exec sleep infinity",
		},
		Env:    env,
		Mounts: mounts,
		// Coding agents need outbound network (LLM APIs, package
		// installs in placeholder images, git, etc.) so the default
		// is NetNAT. The sandbox.NetworkMode zero value (NetIsolated)
		// would block apk add at startup.
		Network: sandbox.NetworkConfig{Mode: sandbox.NetNAT},
	}
}

// validateMountRequests checks session-layer mount invariants and
// converts requests to sandbox.Mount entries ready to append to a
// SandboxSpec. Host-side concerns (absolute, cleaned, under an
// allowed root) are intentionally left to the sandbox validator —
// running them here too would just duplicate error messages.
//
// Errors wrap ErrMountInvalid with a [N] index so the UI can point
// the user at the offending row.
func validateMountRequests(reqs []MountRequest) ([]sandbox.Mount, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	out := make([]sandbox.Mount, 0, len(reqs))
	seenTargets := make(map[string]int, len(reqs))
	for i, m := range reqs {
		if m.HostPath == "" {
			return nil, fmt.Errorf("%w: mounts[%d]: host_path is required", ErrMountInvalid, i)
		}
		if m.ContainerPath == "" {
			return nil, fmt.Errorf("%w: mounts[%d]: container_path is required", ErrMountInvalid, i)
		}
		target := filepath.Clean(m.ContainerPath)
		if target == scratchMountTarget {
			return nil, fmt.Errorf("%w: mounts[%d]: container_path %q collides with the reserved scratch workspace",
				ErrMountInvalid, i, m.ContainerPath)
		}
		// Block container_path = "/" outright: a mount over root
		// clobbers the entire container filesystem and there's no
		// legitimate use. Sub-paths of system dirs (/etc/foo,
		// /usr/local/bin) are deliberately permitted — a power user
		// who wants to overlay a config snippet can.
		if target == "/" {
			return nil, fmt.Errorf("%w: mounts[%d]: container_path %q cannot be the container root",
				ErrMountInvalid, i, m.ContainerPath)
		}
		if prev, dup := seenTargets[target]; dup {
			return nil, fmt.Errorf("%w: mounts[%d]: container_path %q is also used by mounts[%d]",
				ErrMountInvalid, i, m.ContainerPath, prev)
		}
		seenTargets[target] = i

		var readOnly bool
		switch strings.ToLower(m.Mode) {
		case "", "rw":
			readOnly = false
		case "ro":
			readOnly = true
		default:
			return nil, fmt.Errorf("%w: mounts[%d]: mode %q must be \"rw\" or \"ro\"",
				ErrMountInvalid, i, m.Mode)
		}

		out = append(out, sandbox.Mount{
			Source:   filepath.Clean(m.HostPath),
			Target:   target,
			ReadOnly: readOnly,
			Type:     sandbox.MountBind,
		})
	}
	return out, nil
}
