package session

import (
	"context"
	"database/sql"
	"encoding/json"
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

// placeholderOwner is the owner_email recorded for new sessions until
// the identity layer (tsnet WhoIs middleware, first-run owner
// persistence) lands. Search for this constant to find everywhere
// the eventual identity wiring needs to slot in.
const placeholderOwner = "local"

// ErrNotFound is returned when a session id doesn't exist. HTTP
// handlers map this to a 404.
var ErrNotFound = errors.New("session: not found")

// Service is the session lifecycle layer. It composes a *sql.DB, a
// sandbox.Sandbox driver, and the host data directory so each session
// can have its own workspace under $DataDir/sessions/<id>/.
type Service struct {
	db       *sql.DB
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
func NewService(db *sql.DB, sb sandbox.Sandbox, dataDir string, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Service{
		db:           db,
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

	spec := sandbox.SandboxSpec{
		Image: image,
		// Placeholder Cmd: install tmux at runtime (first boot
		// only; subsequent restarts find it already installed)
		// and start a detached tmux session named "main" — the
		// attach handler exec's `tmux attach -t main` against it.
		// `exec sleep infinity` keeps PID 1 alive.
		//
		// Restart safety: when a container is stopped + started,
		// the filesystem persists (so tmux stays installed) but
		// the previous tmux server's socket at /tmp/tmux-* is
		// left behind as a stale file. A fresh tmux refuses to
		// reuse it and fails silently from the user's POV. The
		// `rm -rf /tmp/tmux-*` clears any leftover before we
		// recreate the session. Real agent images will bake tmux
		// in and replace this Cmd entirely.
		Cmd: []string{"sh", "-c",
			"command -v tmux >/dev/null 2>&1 || apk add --no-cache tmux >/dev/null 2>&1; " +
				"rm -rf /tmp/tmux-* 2>/dev/null; " +
				"tmux new-session -d -s main sh; " +
				"exec sleep infinity",
		},
		Env: req.Env,
		Mounts: []sandbox.Mount{{
			Source: workspace,
			Target: "/workspace",
			Type:   sandbox.MountBind,
		}},
		// Coding agents need outbound network (LLM APIs, package
		// installs in placeholder images, git, etc.) so the default
		// is NetNAT. The sandbox.NetworkMode zero value (NetIsolated)
		// would block apk add at startup.
		Network: sandbox.NetworkConfig{Mode: sandbox.NetNAT},
	}

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

	if err := s.insert(ctx, sess); err != nil {
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
// Ordering is by last_activity_at DESC — the canvas needs "what was I
// just doing?" as the top card, not "what did I most recently spawn?"
// The activity column is bumped on every attach connect / 30s tick /
// disconnect, so it tracks user engagement, not creation order.
func (s *Service) List(ctx context.Context, filter ListFilter) ([]*Session, error) {
	query := `
		SELECT id, name, agent, image, owner_email, status, env,
		       container_id, created_at, updated_at, last_activity_at
		FROM sessions
		WHERE owner_email = ?
	`
	args := []any{placeholderOwner}
	if !filter.IncludeAll {
		query += ` AND status = ?`
		args = append(args, string(StatusRunning))
	}
	query += ` ORDER BY last_activity_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("session: list query: %w", err)
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		sess, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Get returns the session with the given id, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (*Session, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, agent, image, owner_email, status, env,
		       container_id, created_at, updated_at, last_activity_at
		FROM sessions WHERE id = ? AND owner_email = ?
	`, id, placeholderOwner)
	sess, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sess, err
}

// RecordActivity bumps last_activity_at to NOW for a session. Called
// by the attach handler on connect, periodic 30s ticker while
// attached, and on disconnect. Cheap (single indexed UPDATE) and
// safe to call frequently. Errors are returned but never fatal —
// callers may log-and-ignore.
//
// The owner_email scope is defensive: today every mutation is gated
// by a prior Get() that already filters by owner, but baking the
// scope into the SQL itself makes each query correct in isolation.
func (s *Service) RecordActivity(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_activity_at = ? WHERE id = ? AND owner_email = ?`,
		time.Now().UTC(), id, placeholderOwner)
	if err != nil {
		return fmt.Errorf("session: record activity: %w", err)
	}
	return nil
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
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		`UPDATE sessions SET status = ?, updated_at = ?, last_activity_at = ? WHERE id = ? AND owner_email = ?`,
		string(StatusRunning), now, now, id, placeholderOwner)
	if err != nil {
		return fmt.Errorf("session: update row: %w", err)
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
	_, err = s.db.ExecContext(ctx,
		`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ? AND owner_email = ?`,
		string(StatusStopped), time.Now().UTC(), id, placeholderOwner)
	if err != nil {
		return fmt.Errorf("session: update row: %w", err)
	}
	return nil
}

// Destroy stops the sandbox (if running), removes it, deletes the
// workspace directory, and removes the row.
func (s *Service) Destroy(ctx context.Context, id string) error {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	handle := sandbox.Handle(sess.ContainerID)
	// Best-effort sandbox teardown — if the daemon already lost the
	// container, we still want to clear our row + workspace.
	if err := s.sandbox.Destroy(ctx, handle); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return fmt.Errorf("session: sandbox destroy: %w", err)
	}
	workspace := filepath.Join(s.sessions, id)
	if err := os.RemoveAll(workspace); err != nil {
		return fmt.Errorf("session: remove workspace: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE id = ? AND owner_email = ?`,
		id, placeholderOwner); err != nil {
		return fmt.Errorf("session: delete row: %w", err)
	}
	s.previewCache.Delete(id)
	return nil
}

// WorkspaceRoot is the host directory whose subdirectories the
// sandbox driver may bind-mount. Callers pass this to the driver
// constructor so SPEC §3 (sessions never cross host boundaries) is
// enforced by the mount-source validator.
func (s *Service) WorkspaceRoot() string {
	return s.sessions
}

// ---- helpers ------------------------------------------------------------

func (s *Service) insert(ctx context.Context, sess *Session) error {
	// Marshal nil maps as "{}" not "null" — the env column is JSON
	// NOT NULL, and DuckDB's JSON type round-trips the bare token
	// "null" as SQL NULL on read, which crashes Scan into a string.
	env := sess.Env
	if env == nil {
		env = map[string]string{}
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("session: marshal env: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, name, agent, image, owner_email, status, env, container_id, created_at, updated_at, last_activity_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		sess.ID, sess.Name, sess.Agent, sess.Image, sess.OwnerEmail,
		string(sess.Status), string(envJSON), sess.ContainerID,
		sess.CreatedAt, sess.UpdatedAt, sess.LastActivityAt,
	)
	return err
}

// rowScanner is the subset of *sql.Row / *sql.Rows we need so a
// single scanRow function works for both Get and List.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRow(r rowScanner) (*Session, error) {
	var (
		sess           Session
		envJSON        sql.NullString
		containerID    sql.NullString
		status         string
		lastActivityAt sql.NullTime
	)
	if err := r.Scan(
		&sess.ID, &sess.Name, &sess.Agent, &sess.Image, &sess.OwnerEmail,
		&status, &envJSON, &containerID, &sess.CreatedAt, &sess.UpdatedAt, &lastActivityAt,
	); err != nil {
		return nil, err
	}
	// last_activity_at is nullable on disk (DuckDB ALTER limitation
	// means we can't enforce NOT NULL); fall back to created_at when
	// the column is empty for legacy rows.
	if lastActivityAt.Valid {
		sess.LastActivityAt = lastActivityAt.Time
	} else {
		sess.LastActivityAt = sess.CreatedAt
	}
	sess.Status = Status(status)
	if containerID.Valid {
		sess.ContainerID = containerID.String
	}
	if envJSON.Valid && strings.TrimSpace(envJSON.String) != "" && envJSON.String != "null" {
		if err := json.Unmarshal([]byte(envJSON.String), &sess.Env); err != nil {
			return nil, fmt.Errorf("session: unmarshal env: %w", err)
		}
	}
	return &sess, nil
}
