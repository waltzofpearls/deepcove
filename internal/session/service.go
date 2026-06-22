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

// workspaceDir is the conventional working directory. A user mount can target it (your
// code lives there), and it's the default the New Session form pre-fills. When no mount
// targets it, the entrypoint creates an empty /workspace so the agent always has a
// landing dir. The agent cd's to DEEPCOVE_WORKDIR (the first user mount, else this).
const workspaceDir = "/workspace"

// /root is hardcoded; agent images run as root today. Non-root images would need a
// per-image override (out of scope).
const agentHomeMountTarget = "/root"

// Volume is shared across ALL sessions of the same agent so auth + installed binary
// persist across sessions. Tradeoff: shell history, gitconfig, and Claude conversation
// history (under `.claude/projects/<encoded-cwd>/`) are all shared. Different repos get
// different project subdirs so concurrent sessions on different repos don't conflict.
// Volume is never auto-removed; user runs `docker volume rm deepcove-agent-<name>-home`
// to log out everywhere / reset agent state.
func agentHomeVolumeName(agentName string) string {
	return "deepcove-agent-" + agentName + "-home"
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
// SessionRepository (for persistence) and a sandbox.Sandbox driver
// (for container runtime).
//
// All SQL lives in SessionRepository implementations; Service methods are pure orchestration.
// Owner_email is threaded through to the repo on every call; today every caller passes the
// placeholder owner, but the shape is ready for identity wiring.
type Service struct {
	repo    SessionRepository
	sandbox sandbox.Sandbox
	logger  *slog.Logger

	// federation supplies the cross-node view for ListAggregated (set via SetFederation). Nil means
	// single-node: only this machine's sessions.
	federation Federation

	// previewCache holds the most recent successful capture-pane
	// snapshot per session id. We serve it back when:
	//   - the session is stopped (so the UI can show a dimmed
	//     last-seen view + status overlay instead of a black void),
	//   - a live capture transiently fails (tmux not yet ready
	//     after a stop/start cycle).
	// In-memory only; rebuilt on service restart.
	previewCache *previewCache
}

// NewService constructs a Service.
func NewService(repo SessionRepository, sb sandbox.Sandbox, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Service{
		repo:         repo,
		sandbox:      sb,
		logger:       logger,
		previewCache: newPreviewCache(previewCacheMaxEntries),
	}
}

// Image override is resolved + locked in at create time. SetupScript override is
// stored verbatim; empty means "use agent.DefaultSetup at spec-build time" so registry
// updates flow into existing sessions on recreate.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Session, error) {
	if req.Agent == "" {
		return nil, fmt.Errorf("agent is required")
	}
	agent, err := resolveAgent(req.Agent)
	if err != nil {
		return nil, err
	}

	// Validate before any sandbox work so bad input fails as a clean 400 without
	// rollback. Owns session-layer concerns (reserved /root target, dup targets, mode);
	// the sandbox layer's validator owns host-path concerns (absolute, allowed root).
	userMounts, err := validateMountRequests(req.Mounts)
	if err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("session: generate id: %w", err)
	}
	now := time.Now().UTC()

	image := req.Image
	if image == "" {
		image = agent.DefaultImage
	}

	sess := &Session{
		ID:             id.String(),
		Name:           req.Name,
		Agent:          req.Agent,
		Image:          image,
		OwnerEmail:     placeholderOwner,
		Status:         StatusCreated,
		Env:            req.Env,
		Mounts:         req.Mounts,
		SetupScript:    req.SetupScript,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	if sess.Name == "" {
		sess.Name = generateName(req.Agent)
	}

	spec := s.buildSandboxSpec(sess, agent, userMounts)

	handle, err := s.sandbox.Create(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("session: create sandbox: %w", err)
	}
	sess.ContainerID = string(handle)

	if err := s.sandbox.Start(ctx, handle); err != nil {
		_ = s.sandbox.Destroy(context.Background(), handle)
		return nil, fmt.Errorf("session: start sandbox: %w", err)
	}
	sess.Status = StatusRunning

	if err := s.repo.Insert(ctx, sess); err != nil {
		_ = s.sandbox.Stop(context.Background(), handle)
		_ = s.sandbox.Destroy(context.Background(), handle)
		return nil, fmt.Errorf("session: insert row: %w", err)
	}

	s.logger.Info("session created", "id", sess.ID, "name", sess.Name, "agent", sess.Agent)
	return sess, nil
}

// List returns sessions owned by the current owner (today, the
// placeholder owner). filter.IncludeAll=true returns stopped/errored
// sessions too; default is running only.
//
// Ordering is by last_activity_at DESC (defined by the repo): the canvas needs "what was I just
// doing?" as the top card, not "what did I most recently spawn?" The activity column is bumped on
// every attach connect / 30s tick / disconnect, so it tracks user engagement, not creation order.
func (s *Service) List(ctx context.Context, filter ListFilter) ([]*Session, error) {
	return s.repo.List(ctx, placeholderOwner, filter.IncludeAll)
}

// Get returns the session with the given id, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (*Session, error) {
	return s.repo.Get(ctx, placeholderOwner, id)
}

// maxSessionNameLen bounds the user-set display name. The name is
// display-only (ids drive routing/containers), so this is just a
// sanity cap, not a hard constraint.
const maxSessionNameLen = 100

// Rename sets a session's display name and returns the updated session.
// The name is trimmed; empty or over-long input is a user error.
func (s *Service) Rename(ctx context.Context, id, name string) (*Session, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("session: name is required")
	}
	if len(name) > maxSessionNameLen {
		return nil, fmt.Errorf("session: name too long (max %d characters)", maxSessionNameLen)
	}
	if err := s.repo.Rename(ctx, placeholderOwner, id, name, time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// RecordActivity bumps last_activity_at to NOW for a session. Called
// by the attach handler on connect, periodic 30s ticker while attached, and on disconnect. Cheap
// (single indexed UPDATE) and safe to call frequently. Errors are returned but never fatal; callers
// may log-and-ignore.
func (s *Service) RecordActivity(ctx context.Context, id string) error {
	return s.repo.UpdateActivity(ctx, placeholderOwner, id, time.Now().UTC())
}

// Start brings a stopped session back to running. The underlying container is reused (Docker
// preserves its filesystem on stop), so the agent's workspace and any in-container state survive,
// but the original entrypoint Cmd runs again, which means tmux gets a fresh session named "main".
// In-container ephemeral state (running processes, scrollback) does NOT survive a stop/start cycle.
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

// Destroy stops the sandbox (if running), removes it, and removes the
// row. The agent-home volume is shared, so it's left intact; the
// container's own /workspace (and writable layer) goes with the
// container. Any user-mounted folder is the user's own host dir and
// is never touched.
func (s *Service) Destroy(ctx context.Context, id string) error {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	handle := sandbox.Handle(sess.ContainerID)
	// Best-effort sandbox teardown: if the daemon already lost the container, we still want to clear
	// our row.
	if err := s.sandbox.Destroy(ctx, handle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return fmt.Errorf("session: sandbox destroy: %w", err)
	}
	// Don't remove the agent-home volume; it's shared (see agentHomeVolumeName).
	if err := s.repo.Delete(ctx, placeholderOwner, id); err != nil {
		return fmt.Errorf("session: delete row: %w", err)
	}
	s.previewCache.Delete(id)
	return nil
}

// Docker can't change bind mounts on a live container; this destroys + recreates.
// The session id and the shared /root agent-home volume survive. Everything in the
// writable layer (apt-installed packages, /etc edits, and a no-mount /workspace's
// scratch contents) does NOT survive the recreate. A user-mounted /workspace folder
// is host-owned, so its contents are unaffected. Accepts 0..n mounts.
func (s *Service) EditMounts(ctx context.Context, id string, newMounts []MountRequest) (*Session, error) {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	userMounts, err := validateMountRequests(newMounts)
	if err != nil {
		return nil, err
	}

	oldHandle := sandbox.Handle(sess.ContainerID)
	wasRunning := sess.Status == StatusRunning

	if wasRunning {
		if err := s.sandbox.Stop(ctx, oldHandle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
			return nil, fmt.Errorf("session: stop sandbox: %w", err)
		}
	}
	if err := s.sandbox.Destroy(ctx, oldHandle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return nil, fmt.Errorf("session: destroy old sandbox: %w", err)
	}

	agent, err := resolveAgent(sess.Agent)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}

	spec := s.buildSandboxSpec(sess, agent, userMounts)

	newHandle, err := s.sandbox.Create(ctx, spec)
	if err != nil {
		// Half-state: row exists but no container. User's recovery is destroy.
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
		return nil, fmt.Errorf("session: update mounts row: %w", err)
	}
	// Keep the just-reconfigured session at the top of the canvas.
	_ = s.repo.UpdateActivity(ctx, placeholderOwner, id, now)

	updated, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.logger.Info("session mounts edited", "id", id, "name", sess.Name, "mounts", len(newMounts))
	return updated, nil
}

// Shared by Create + EditMounts so the entrypoint, mount layout, and network mode
// stay identical between initial and recreated containers. Mounts: the shared
// agent-home volume at /root, then user mounts. /root is mounted first so a stray
// user mount can't shadow it (the validator also rejects /root); /workspace is
// intentionally user-mountable, so it's not protected. No deepcove-managed scratch
// mount; a no-mount session just gets an empty in-container /workspace (created by
// the entrypoint). User env wins on key conflicts with agent.Env; DEEPCOVE_WORKDIR
// is set last so the agent always lands in the right place.
func (s *Service) buildSandboxSpec(
	sess *Session,
	agent AgentInfo,
	userMounts []sandbox.Mount,
) sandbox.SandboxSpec {
	mounts := []sandbox.Mount{
		{
			Source: agentHomeVolumeName(agent.Name),
			Target: agentHomeMountTarget,
			Type:   sandbox.MountVolume,
		},
	}
	mounts = append(mounts, userMounts...)

	effectiveSetup := sess.SetupScript
	if effectiveSetup == "" {
		effectiveSetup = agent.DefaultSetup
	}

	mergedEnv := make(map[string]string, len(agent.Env)+len(sess.Env)+1)
	for k, v := range agent.Env {
		mergedEnv[k] = v
	}
	for k, v := range sess.Env {
		mergedEnv[k] = v
	}
	// The agent cd's here at launch: the first user mount (your code), else the
	// empty /workspace the entrypoint creates.
	mergedEnv["DEEPCOVE_WORKDIR"] = workdirFor(userMounts)

	return sandbox.SandboxSpec{
		Image:  sess.Image,
		Cmd:    buildEntrypoint(sess.Name, effectiveSetup, agent.LaunchCmd),
		Env:    mergedEnv,
		Mounts: mounts,
		// NetNAT for outbound LLM API calls + package installs. Zero value (NetIsolated)
		// would block `apk add` at startup.
		Network: sandbox.NetworkConfig{Mode: sandbox.NetNAT},
	}
}

// LaunchCmd is written to a file rather than passed inline to tmux; quoting a
// multi-line shell script as a tmux new-window argument is nightmare-tier. Heredocs
// use the single-quoted `<<'TERM'` form so user-supplied session names can't inject
// shell metacharacters into the MOTD.
func buildEntrypoint(sessName, setupScript, launchCmd string) []string {
	body := fmt.Sprintf(`mkdir -p /etc /usr/local/bin /var/log /workspace

# deepcove preamble: drop the SSH-mode MOTD. Refreshed every boot so name/path
# changes propagate; not in user setup because it's not theirs to edit.
cat > /etc/deepcove-motd <<'DEEPCOVE_MOTD_END'

Welcome to deepcove session "%s".

  /workspace  ← your working folder (your mounted code, or empty scratch)
  /root       ← your home dir (persists across container recreates)

Type 'claude' to start the coding agent. Switch to Agent mode to attach
to the agent's running session in the main tmux window.

DEEPCOVE_MOTD_END

# Default to installing so the UI sees the right state if it polls before setup writes.
echo installing > /etc/deepcove-bootstrap-status
: > /var/log/deepcove-bootstrap.log

{
  set -e
  echo "[deepcove] Starting setup at $(date -u +%%H:%%M:%%SZ)"
%s
  echo "[deepcove] Setup complete"
} >> /var/log/deepcove-bootstrap.log 2>&1
setup_rc=$?

if [ $setup_rc -ne 0 ]; then
  echo errored > /etc/deepcove-bootstrap-status
  echo "[deepcove] Setup failed (exit $setup_rc); see log above" >> /var/log/deepcove-bootstrap.log
  # Park so the user can inspect via SSH mode.
  exec sleep infinity
fi

cat > /usr/local/bin/deepcove-agent-launch <<'DEEPCOVE_LAUNCH_END'
#!/bin/sh
%s
DEEPCOVE_LAUNCH_END
chmod +x /usr/local/bin/deepcove-agent-launch

# A fresh tmux refuses to reuse a stale socket from a previous boot.
rm -rf /tmp/tmux-* 2>/dev/null || true
tmux new-session -d -s main -n main /usr/local/bin/deepcove-agent-launch

# Flip to ready only after tmux is up. The frontend gates WS attach on this.
echo ready > /etc/deepcove-bootstrap-status

exec sleep infinity
`, sessName, setupScript, launchCmd)

	return []string{"sh", "-c", body}
}

// workdirFor returns the directory the agent should start in: the first user mount's
// target (your code), or the default /workspace when nothing is mounted there.
func workdirFor(userMounts []sandbox.Mount) string {
	if len(userMounts) > 0 {
		return userMounts[0].Target
	}
	return workspaceDir
}

// Errors wrap ErrMountInvalid with a [N] index so the UI can point at the bad row.
// Host-path checks (absolute, cleaned, under an allowed root) live in the sandbox
// layer's validator.
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
		// /root is the shared agent-home volume; a user mount there would shadow the
		// agent's binary + credentials. /workspace is intentionally NOT reserved: it's
		// the conventional place to mount your code.
		if target == agentHomeMountTarget {
			return nil, fmt.Errorf("%w: mounts[%d]: container_path %q is reserved for the agent home",
				ErrMountInvalid, i, m.ContainerPath)
		}
		// Sub-paths of system dirs are deliberately allowed; only the root itself
		// is blocked (mounting over / would clobber the whole filesystem).
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
