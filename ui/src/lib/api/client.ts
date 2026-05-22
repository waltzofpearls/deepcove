// Thin typed wrapper over fetch for the /api/* endpoints. Same-origin
// for now (SPA served from the Go binary or proxied via vite dev);
// the relative base path "/api" works in both cases.

import type {
	Agent,
	CreateSessionRequest,
	CreateTmuxWindowRequest,
	MountRequest,
	Session,
	SessionPreview,
	TmuxWindow,
} from './types';

const BASE = '/api';

export class ApiError extends Error {
	constructor(public status: number, message: string) {
		super(message);
		this.name = 'ApiError';
	}
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const resp = await fetch(BASE + path, init);
	if (!resp.ok) {
		// API errors come back as {"error": "..."} per writeError() on
		// the Go side. Fall back to the status line if the body isn't
		// shaped that way.
		let msg = resp.statusText;
		try {
			const body = (await resp.json()) as { error?: string };
			if (body.error) msg = body.error;
		} catch {
			/* non-JSON body */
		}
		throw new ApiError(resp.status, msg);
	}
	// 204 No Content — caller doesn't get a body. Cast to satisfy T.
	if (resp.status === 204) return undefined as T;
	return (await resp.json()) as T;
}

export const api = {
	listAgents(): Promise<Agent[]> {
		return request('/agents');
	},

	listSessions(opts: { all?: boolean } = {}): Promise<Session[]> {
		const q = opts.all ? '?all=true' : '';
		return request(`/sessions${q}`);
	},

	getSession(id: string): Promise<Session> {
		return request(`/sessions/${encodeURIComponent(id)}`);
	},

	createSession(req: CreateSessionRequest): Promise<Session> {
		return request('/sessions', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(req),
		});
	},

	startSession(id: string): Promise<void> {
		return request(`/sessions/${encodeURIComponent(id)}/start`, { method: 'POST' });
	},

	stopSession(id: string): Promise<void> {
		return request(`/sessions/${encodeURIComponent(id)}/stop`, { method: 'POST' });
	},

	destroySession(id: string): Promise<void> {
		return request(`/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' });
	},

	// editMounts replaces the session's user mounts. Server-side this
	// destroys the running container and recreates it with the new
	// spec — agent-home volume (/root) and the scratch /workspace
	// survive, but anything in the writable layer (apt-installed
	// packages, files outside /root and /workspace) is gone. UI is
	// expected to warn before calling. Returns the updated Session.
	editMounts(id: string, mounts: MountRequest[]): Promise<Session> {
		return request(`/sessions/${encodeURIComponent(id)}/mounts`, {
			method: 'PUT',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ mounts }),
		});
	},

	sessionPreview(id: string): Promise<SessionPreview> {
		return request(`/sessions/${encodeURIComponent(id)}/preview`);
	},

	// createTmuxWindow creates a new tmux window in the session.
	// Empty body → server auto-picks a name (shell / shell-2 / ...).
	// 409 means a window with that name already exists — callers that
	// need idempotent "ensure exists" semantics should treat 409 as
	// success.
	createTmuxWindow(
		sessionId: string,
		req: CreateTmuxWindowRequest = {},
	): Promise<TmuxWindow> {
		return request(`/sessions/${encodeURIComponent(sessionId)}/tmux-windows`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(req),
		});
	},

	// attachWebSocketURL returns the WS URL for live-attaching to a
	// session's tmux. The optional `tmuxWindow` argument targets a
	// specific window so two simultaneous attaches with different
	// values can each pin to their own window (per-client window
	// focus is a tmux native feature). Omit for legacy follow-the-
	// session's-current-window behavior.
	attachWebSocketURL(id: string, tmuxWindow?: string): string {
		const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
		const q = tmuxWindow ? `?tmux_window=${encodeURIComponent(tmuxWindow)}` : '';
		return `${proto}//${location.host}${BASE}/sessions/${encodeURIComponent(id)}/attach${q}`;
	},
};
