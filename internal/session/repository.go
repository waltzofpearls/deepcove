package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SessionRepository owns persistence for Session entities. The interface is owner-scoped: every
// read/mutation takes an owner email so each call is correct in isolation, even without a prior
// authorization check (today every caller passes the placeholder owner; the identity slice threads
// the real value through).
//
// Implementations must return ErrNotFound for a missing row in Get,
// MarkRunning, MarkStopped, UpdateActivity, and Delete.
type SessionRepository interface {
	// Insert persists a new session. Caller fills CreatedAt /
	// UpdatedAt / LastActivityAt; the repo doesn't touch them.
	Insert(ctx context.Context, sess *Session) error

	// Get fetches a session by id within an owner's scope. Returns
	// ErrNotFound if no row matches.
	Get(ctx context.Context, owner, id string) (*Session, error)

	// List returns sessions owned by the given owner, ordered by
	// last_activity_at DESC. When includeAll is false, only running
	// sessions are returned (the default `session ps` view).
	List(ctx context.Context, owner string, includeAll bool) ([]*Session, error)

	// MarkRunning sets status=running and bumps both updated_at and
	// last_activity_at to `now`. Used by Service.Start.
	MarkRunning(ctx context.Context, owner, id string, now time.Time) error

	// MarkStopped sets status=stopped and bumps updated_at. Used by
	// Service.Stop.
	MarkStopped(ctx context.Context, owner, id string, now time.Time) error

	// UpdateActivity bumps last_activity_at. Called per attach
	// open / 30s tick / disconnect; cheap by design.
	UpdateActivity(ctx context.Context, owner, id string, at time.Time) error

	// Rename sets the session's display name and bumps updated_at.
	Rename(ctx context.Context, owner, id, name string, now time.Time) error

	// UpdateMountsAndContainer replaces the mounts JSON and
	// container_id on a session, and bumps updated_at. Used by
	// Service.EditMounts after the recreate-with-new-spec flow:
	// the container instance changes, but the session row
	// (especially the id and name) is preserved.
	UpdateMountsAndContainer(ctx context.Context, owner, id string, mounts []MountRequest, containerID string, now time.Time) error

	// Delete removes the session row.
	Delete(ctx context.Context, owner, id string) error
}

// duckdbSessionRepository is the SessionRepository backed by the
// DuckDB store from internal/db. Holds DuckDB-specific quirks
// (JSON null round-trip, nullable last_activity_at) so the service
// layer never has to know about them.
type duckdbSessionRepository struct {
	db *sql.DB
}

// NewDuckDBSessionRepository constructs a SessionRepository over the
// given *sql.DB (obtained from db.Open + db.Migrate).
func NewDuckDBSessionRepository(database *sql.DB) SessionRepository {
	return &duckdbSessionRepository{db: database}
}

func (r *duckdbSessionRepository) Insert(ctx context.Context, sess *Session) error {
	// Marshal nil maps as "{}" not "null"; the env column is JSON
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
	// Same nil→"[]" rule for mounts: the column is NOT NULL DEFAULT
	// '[]' on disk; marshal nil to "[]" so a session created with
	// no user mounts gets an explicit empty array rather than the
	// scanner ever having to special-case the DB default.
	mounts := sess.Mounts
	if mounts == nil {
		mounts = []MountRequest{}
	}
	mountsJSON, err := json.Marshal(mounts)
	if err != nil {
		return fmt.Errorf("session: marshal mounts: %w", err)
	}
	resourcesJSON, err := json.Marshal(sess.Resources)
	if err != nil {
		return fmt.Errorf("session: marshal resources: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO sessions (id, name, agent, image, owner_email, status, mounts, env, setup_script, runtime, resources, container_id, created_at, updated_at, last_activity_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		sess.ID, sess.Name, sess.Agent, sess.Image, sess.OwnerEmail,
		string(sess.Status), string(mountsJSON), string(envJSON), sess.SetupScript, sess.Runtime, string(resourcesJSON), sess.ContainerID,
		sess.CreatedAt, sess.UpdatedAt, sess.LastActivityAt,
	)
	if err != nil {
		return fmt.Errorf("session: insert: %w", err)
	}
	return nil
}

func (r *duckdbSessionRepository) Get(ctx context.Context, owner, id string) (*Session, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, agent, image, owner_email, status, mounts, env, setup_script, runtime, resources,
		       container_id, created_at, updated_at, last_activity_at
		FROM sessions WHERE id = ? AND owner_email = ?
	`, id, owner)
	sess, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("session: get: %w", err)
	}
	return sess, nil
}

func (r *duckdbSessionRepository) List(ctx context.Context, owner string, includeAll bool) ([]*Session, error) {
	query := `
		SELECT id, name, agent, image, owner_email, status, mounts, env, setup_script, runtime, resources,
		       container_id, created_at, updated_at, last_activity_at
		FROM sessions
		WHERE owner_email = ?
	`
	args := []any{owner}
	if !includeAll {
		query += ` AND status = ?`
		args = append(args, string(StatusRunning))
	}
	query += ` ORDER BY last_activity_at DESC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("session: list query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*Session
	for rows.Next() {
		sess, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("session: list scan: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (r *duckdbSessionRepository) MarkRunning(ctx context.Context, owner, id string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET status = ?, updated_at = ?, last_activity_at = ?
		 WHERE id = ? AND owner_email = ?`,
		string(StatusRunning), now, now, id, owner)
	if err != nil {
		return fmt.Errorf("session: mark running: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

func (r *duckdbSessionRepository) MarkStopped(ctx context.Context, owner, id string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET status = ?, updated_at = ?
		 WHERE id = ? AND owner_email = ?`,
		string(StatusStopped), now, id, owner)
	if err != nil {
		return fmt.Errorf("session: mark stopped: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

func (r *duckdbSessionRepository) UpdateActivity(ctx context.Context, owner, id string, at time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET last_activity_at = ?
		 WHERE id = ? AND owner_email = ?`,
		at, id, owner)
	if err != nil {
		return fmt.Errorf("session: update activity: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

func (r *duckdbSessionRepository) Rename(ctx context.Context, owner, id, name string, now time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET name = ?, updated_at = ?
		 WHERE id = ? AND owner_email = ?`,
		name, now, id, owner)
	if err != nil {
		return fmt.Errorf("session: rename: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

func (r *duckdbSessionRepository) UpdateMountsAndContainer(
	ctx context.Context, owner, id string,
	mounts []MountRequest, containerID string, now time.Time,
) error {
	if mounts == nil {
		mounts = []MountRequest{}
	}
	mountsJSON, err := json.Marshal(mounts)
	if err != nil {
		return fmt.Errorf("session: marshal mounts: %w", err)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET mounts = ?, container_id = ?, updated_at = ?
		 WHERE id = ? AND owner_email = ?`,
		string(mountsJSON), containerID, now, id, owner)
	if err != nil {
		return fmt.Errorf("session: update mounts: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

func (r *duckdbSessionRepository) Delete(ctx context.Context, owner, id string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE id = ? AND owner_email = ?`,
		id, owner)
	if err != nil {
		return fmt.Errorf("session: delete: %w", err)
	}
	return rowsAffectedOrNotFound(res)
}

// rowsAffectedOrNotFound returns ErrNotFound when a mutation matched
// zero rows. Lets the service rely on a single sentinel for "this
// session doesn't exist" without each method shape having to encode
// that semantically.
func rowsAffectedOrNotFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("session: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// rowScanner abstracts *sql.Row and *sql.Rows so scanRow handles both.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRow(r rowScanner) (*Session, error) {
	var (
		sess           Session
		mountsJSON     sql.NullString
		envJSON        sql.NullString
		setupScript    sql.NullString
		runtime        sql.NullString
		resourcesJSON  sql.NullString
		containerID    sql.NullString
		status         string
		lastActivityAt sql.NullTime
	)
	if err := r.Scan(
		&sess.ID, &sess.Name, &sess.Agent, &sess.Image, &sess.OwnerEmail,
		&status, &mountsJSON, &envJSON, &setupScript, &runtime, &resourcesJSON, &containerID,
		&sess.CreatedAt, &sess.UpdatedAt, &lastActivityAt,
	); err != nil {
		return nil, err
	}
	if setupScript.Valid {
		sess.SetupScript = setupScript.String
	}
	// runtime is nullable for rows written before the column existed; the service treats "" as the
	// node default runtime.
	if runtime.Valid {
		sess.Runtime = runtime.String
	}
	// resources is nullable for rows written before the column existed; zero fields read back as "use
	// the runtime's defaults", which is what those sessions already got.
	if resourcesJSON.Valid && strings.TrimSpace(resourcesJSON.String) != "" && resourcesJSON.String != "null" {
		if err := json.Unmarshal([]byte(resourcesJSON.String), &sess.Resources); err != nil {
			return nil, fmt.Errorf("session: unmarshal resources: %w", err)
		}
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
	// Mounts: same null-token defense as env. Empty array on disk
	// unmarshals to a nil slice, which the JSON encoder serializes as
	// `null` on the wire; that's fine because the API consumer just
	// reads it as "no user mounts".
	if mountsJSON.Valid && strings.TrimSpace(mountsJSON.String) != "" && mountsJSON.String != "null" {
		if err := json.Unmarshal([]byte(mountsJSON.String), &sess.Mounts); err != nil {
			return nil, fmt.Errorf("session: unmarshal mounts: %w", err)
		}
	}
	return &sess, nil
}
