// Hand-typed mirror of internal/session.Session and adjacent shapes.
// We deliberately don't generate these from Go — the surface is small
// enough that the cost of a generator (tygo etc) outweighs the drift
// risk. Keep this file 1:1 with internal/session/session.go JSON tags.

export type SessionStatus = 'created' | 'running' | 'stopped' | 'errored';

export interface Session {
	id: string;
	name: string;
	agent: string;
	image: string;
	owner_email: string;
	status: SessionStatus;
	env: Record<string, string> | null;
	// User mounts currently attached to the container. The scratch
	// /workspace mount and the agent-home /root volume are NOT
	// included — those are managed by deepcove, not the user.
	mounts: MountRequest[] | null;
	// Empty means "use the agent registry's default".
	setup_script: string;
	container_id?: string;
	created_at: string; // ISO-8601 timestamp
	updated_at: string;
	last_activity_at: string; // added in Phase 4 / migration 0002
}

export interface CreateSessionRequest {
	agent: string;
	name?: string;
	env?: Record<string, string>;
	mounts?: MountRequest[];
	// Empty falls back to agent registry defaults.
	image?: string;
	setup_script?: string;
}

// MountRequest mirrors internal/session.MountRequest. host_path must
// be absolute on the deepcove host; container_path must be absolute
// inside the sandbox. /workspace is the conventional target (your
// code, where the agent starts); /root is reserved. Mode defaults to
// "rw" if omitted.
export interface MountRequest {
	host_path: string;
	container_path: string;
	mode?: 'rw' | 'ro';
}

// FsEntry / FsListResponse mirror internal/session — GET /api/fs/list
// lists subdirectories of a host path (scoped to $HOME) for the host-path
// autocomplete + Browse modal. parent is "" at the home root.
export interface FsEntry {
	name: string;
	path: string;
}
export interface FsListResponse {
	path: string;
	parent: string;
	entries: FsEntry[];
}

// ConnectStatus mirrors internal/connect.Status — the Tailscale "Connect" wizard
// state. phase: stopped → starting/needs_login (login_url set) → running (url set;
// http until https_enabled, then tls). user_id is an int64 (display only).
export interface ConnectOwner {
	user_id: number;
	login_name: string;
	display_name: string;
}
export interface ConnectStatus {
	phase: 'stopped' | 'starting' | 'needs_login' | 'running';
	login_url?: string;
	url?: string;
	tls: boolean;
	magicdns_enabled: boolean;
	https_enabled: boolean;
	owner?: ConnectOwner;
}

// official_image is empty until a prebuilt deepcove image is published — UI uses that
// to gate the "Official deepcove image" radio with a "coming soon" callout.
export interface Agent {
	name: string;
	display_name: string;
	default_image: string;
	official_image: string;
	default_setup: string;
	launch_cmd: string;
	env: Record<string, string>;
}

export interface SessionPreview {
	// Raw `tmux capture-pane -e` output with ANSI escapes. The client
	// feeds this into an unmounted xterm.js Terminal + SerializeAddon
	// to produce HTML, matching what the live xterm.js view would
	// render. Dimensions are derived from the bytes themselves.
	ansi: string;
}

// TmuxWindow mirrors internal/session.TmuxWindow.
export interface TmuxWindow {
	name: string;
	index: number;
}

// CreateTmuxWindowRequest mirrors internal/session.CreateTmuxWindowRequest.
// Name is optional — server auto-generates ("shell", "shell-2", ...)
// when blank.
export interface CreateTmuxWindowRequest {
	name?: string;
}

// Wire shape of an API error body. `code` is a stable machine-readable
// tag (e.g. "docker_unreachable", "image_not_found") the client maps to
// friendly copy + actions; absent on older/generic errors.
export interface ApiError {
	error: string;
	code?: string;
}

// Stable error codes mirrored from internal/session/handlers.go. Keep
// in sync — renames here without a server change break the mapping.
export type ApiErrorCode =
	| 'invalid_input'
	| 'docker_unreachable'
	| 'image_not_found'
	| 'not_found'
	| 'conflict';

// GET /api/preflight — environment readiness the dashboard reads on load
// and the New Session form reads before allowing create. Mirrors
// internal/session.PreflightResponse.
export interface Preflight {
	docker: DockerStatus;
}

export interface DockerStatus {
	reachable: boolean;
	version: string;
	// Human-facing guidance shown when reachable is false.
	message: string;
}

export type BootstrapStatus = 'installing' | 'ready' | 'errored';

export interface BootstrapResponse {
	status: BootstrapStatus;
	log: string;
}
