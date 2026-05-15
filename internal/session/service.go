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
}

// NewService constructs a Service. dataDir is the node's data
// directory (e.g. ~/Library/Application Support/deepcove); sessions
// live under $dataDir/sessions/<id>/.
func NewService(db *sql.DB, sb sandbox.Sandbox, dataDir string, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Service{
		db:       db,
		sandbox:  sb,
		dataDir:  dataDir,
		sessions: filepath.Join(dataDir, "sessions"),
		logger:   logger,
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
	sess := &Session{
		ID:         id.String(),
		Name:       req.Name,
		Agent:      req.Agent,
		Image:      image,
		OwnerEmail: placeholderOwner,
		Status:     StatusCreated,
		Env:        req.Env,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
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
		// Placeholder Cmd: install tmux at runtime and start a
		// detached tmux session named "main" — `deepcove session
		// attach` exec's `tmux attach -t main` against it. The
		// outer `sleep infinity` keeps PID 1 alive so the container
		// itself doesn't exit when tmux detaches. Real agent images
		// will bake tmux + the agent CLI in and replace this Cmd.
		Cmd: []string{"sh", "-c",
			"apk add --no-cache tmux >/dev/null 2>&1 && " +
				"tmux new-session -d -s main 'sh' && " +
				"sleep infinity",
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
func (s *Service) List(ctx context.Context, filter ListFilter) ([]*Session, error) {
	query := `
		SELECT id, name, agent, image, owner_email, status, env,
		       container_id, created_at, updated_at
		FROM sessions
		WHERE owner_email = ?
	`
	args := []any{placeholderOwner}
	if !filter.IncludeAll {
		query += ` AND status = ?`
		args = append(args, string(StatusRunning))
	}
	query += ` ORDER BY created_at DESC`

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
		       container_id, created_at, updated_at
		FROM sessions WHERE id = ? AND owner_email = ?
	`, id, placeholderOwner)
	sess, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sess, err
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
		`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ?`,
		string(StatusStopped), time.Now().UTC(), id)
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
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("session: delete row: %w", err)
	}
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
		INSERT INTO sessions (id, name, agent, image, owner_email, status, env, container_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		sess.ID, sess.Name, sess.Agent, sess.Image, sess.OwnerEmail,
		string(sess.Status), string(envJSON), sess.ContainerID,
		sess.CreatedAt, sess.UpdatedAt,
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
		sess        Session
		envJSON     sql.NullString
		containerID sql.NullString
		status      string
	)
	if err := r.Scan(
		&sess.ID, &sess.Name, &sess.Agent, &sess.Image, &sess.OwnerEmail,
		&status, &envJSON, &containerID, &sess.CreatedAt, &sess.UpdatedAt,
	); err != nil {
		return nil, err
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
