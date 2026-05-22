// Package session manages the lifecycle of coding-agent sessions
// hosted on this node — the layer between the API surface and the
// sandbox driver.
//
// A Session is a row in the sessions table plus a live sandbox handle.
// The Service handles create/list/get/stop/destroy with the sandbox
// driver, persisting state in DuckDB so it survives a service restart.
//
// SPEC §5.7 (creation + mounts) and §8 (CLI surface) are the relevant
// design references. Owner identity and authorization (§6) are not
// wired in this slice — Service uses a placeholder owner; the real
// identity layer plugs in via request context once tsnet/WhoIs lands.
package session

import "time"

// Status enumerates the lifecycle states tracked in the sessions
// table. The state machine is small on purpose: created → running →
// stopped, with destroy removing the row outright. Failures
// transition to "errored" so they're visible in `session ps`.
type Status string

const (
	StatusCreated Status = "created"
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusErrored Status = "errored"
)

// Session is the API + DB shape of a coding-agent session. JSON tags
// are the HTTP API contract — keep stable as the SPA grows against it.
//
// Mounts holds the user-supplied mount entries that were active when
// the current container was created. The deepcove-managed scratch
// workspace and agent-home volume are NOT included — those are
// implementation details and would just confuse the API surface.
type Session struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Agent          string            `json:"agent"`
	Image          string            `json:"image"`
	OwnerEmail     string            `json:"owner_email"`
	Status         Status            `json:"status"`
	Env            map[string]string `json:"env"`
	Mounts         []MountRequest    `json:"mounts"`
	ContainerID    string            `json:"container_id,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	LastActivityAt time.Time         `json:"last_activity_at"`
}

// CreateRequest is the body of POST /api/sessions. The session
// Service auto-generates Name if empty.
type CreateRequest struct {
	Agent  string            `json:"agent"`
	Name   string            `json:"name,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Mounts []MountRequest    `json:"mounts,omitempty"`
}

// MountRequest is one entry in CreateRequest.Mounts — a host path
// mapped into the sandbox at a container path. SPEC §5.7. Wire
// format keeps snake_case field names for JSON-by-convention.
//
// The deepcove-managed scratch workspace ($DataDir/sessions/<id>)
// is mounted automatically at /workspace; user mounts here are
// stacked on top. ContainerPath collisions with /workspace and
// duplicate container paths across the user's mounts are rejected.
type MountRequest struct {
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
	// Mode is "rw" (default) or "ro". Empty == "rw". Anything
	// else fails validation.
	Mode string `json:"mode,omitempty"`
}

// ListFilter narrows GET /api/sessions. Today only the "all" toggle
// matters — by default we hide stopped/errored sessions so `session
// ps` matches the Docker convention (only running unless -a).
type ListFilter struct {
	IncludeAll bool
}
