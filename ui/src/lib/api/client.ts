// Thin typed wrapper over fetch for the /api/* endpoints. Same-origin
// for now (SPA served from the Go binary or proxied via vite dev);
// the relative base path "/api" works in both cases.

import type {
	Agent,
	BootstrapResponse,
	CreateSessionRequest,
	CreateTmuxWindowRequest,
	MountRequest,
	Preflight,
	Session,
	SessionPreview,
	TmuxWindow,
} from './types';

const BASE = '/api';

export class ApiError extends Error {
	// code is the stable machine tag from the {error, code} envelope,
	// undefined for generic/older errors. Callers map it to friendly
	// copy + actions (see lib/api/errors.ts).
	constructor(
		public status: number,
		message: string,
		public code?: string,
	) {
		super(message);
		this.name = 'ApiError';
	}
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const resp = await fetch(BASE + path, init);
	if (!resp.ok) {
		// API errors come back as {"error": "...", "code": "..."} per
		// writeError/writeErrorCode on the Go side. Fall back to the
		// status line if the body isn't shaped that way.
		let msg = resp.statusText;
		let code: string | undefined;
		try {
			const body = (await resp.json()) as { error?: string; code?: string };
			if (body.error) msg = body.error;
			code = body.code;
		} catch {
			/* non-JSON body */
		}
		throw new ApiError(resp.status, msg, code);
	}
	// 204 No Content — caller doesn't get a body. Cast to satisfy T.
	if (resp.status === 204) return undefined as T;
	return (await resp.json()) as T;
}

// Read endpoints accept an optional AbortSignal so callers can cancel an
// in-flight request on component unmount. Without this, a fetch from an
// unmounted component leaks — its keep-alive connection stays occupied
// until the server responds. With 4+ session cards each polling /preview,
// a single leaked request is enough to push past Firefox's default
// 6-connections-per-origin limit and queue the next /preview indefinitely.

export const api = {
	listAgents(): Promise<Agent[]> {
		return request('/agents');
	},

	// health is the liveness probe; the Settings "About" section reads
	// the version from it.
	health(): Promise<{ ok: boolean; version: string }> {
		return request('/health');
	},

	// preflight reports environment readiness (Docker reachable?). Read
	// on app load and before New Session so we can warn/block instead of
	// failing a create with a raw daemon error.
	preflight(signal?: AbortSignal): Promise<Preflight> {
		return request('/preflight', { signal });
	},

	// imageExists reports whether an image ref is already downloaded, so
	// the New Session form can show a "will download" hint before create.
	imageExists(image: string, signal?: AbortSignal): Promise<boolean> {
		return request<{ present: boolean }>(
			`/images/exists?image=${encodeURIComponent(image)}`,
			{ signal },
		).then((r) => r.present);
	},

	// pullImageStream downloads an image, reporting progress via
	// onProgress (percent is -1 while still indeterminate). Resolves when
	// the pull completes; rejects with ApiError on a known-up-front
	// failure (e.g. 503 Docker down) or an in-band stream error.
	async pullImageStream(
		image: string,
		onProgress: (p: { status: string; percent: number }) => void,
	): Promise<void> {
		const resp = await fetch(`${BASE}/images/pull`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ image }),
		});
		if (!resp.ok) {
			let msg = resp.statusText;
			let code: string | undefined;
			try {
				const b = (await resp.json()) as { error?: string; code?: string };
				if (b.error) msg = b.error;
				code = b.code;
			} catch {
				/* non-JSON */
			}
			throw new ApiError(resp.status, msg, code);
		}
		if (!resp.body) return; // no stream — assume done
		const reader = resp.body.getReader();
		const decoder = new TextDecoder();
		let buf = '';
		const handleLine = (line: string) => {
			const t = line.trim();
			if (!t) return;
			const ev = JSON.parse(t) as {
				status?: string;
				percent?: number;
				done?: boolean;
				error?: string;
			};
			if (ev.error) throw new ApiError(resp.status, ev.error);
			if (ev.done) return;
			onProgress({
				status: ev.status ?? '',
				percent: typeof ev.percent === 'number' ? ev.percent : -1,
			});
		};
		for (;;) {
			const { done, value } = await reader.read();
			if (done) break;
			buf += decoder.decode(value, { stream: true });
			const lines = buf.split('\n');
			buf = lines.pop() ?? '';
			for (const line of lines) handleLine(line);
		}
		if (buf.trim()) handleLine(buf); // trailing line without newline
	},

	listSessions(opts: { all?: boolean; signal?: AbortSignal } = {}): Promise<Session[]> {
		const q = opts.all ? '?all=true' : '';
		return request(`/sessions${q}`, { signal: opts.signal });
	},

	getSession(id: string, signal?: AbortSignal): Promise<Session> {
		return request(`/sessions/${encodeURIComponent(id)}`, { signal });
	},

	// uploadFile streams a pasted/dropped file into the session's container
	// (written via docker exec, never the host) and returns its in-container
	// path for injection into the agent's prompt. Uses XHR (not fetch) so the
	// caller gets real upload progress via onProgress (0–100).
	uploadFile(
		sessionId: string,
		file: File,
		onProgress?: (pct: number) => void,
	): Promise<{ path: string; name: string }> {
		const url = `${BASE}/sessions/${encodeURIComponent(sessionId)}/uploads?name=${encodeURIComponent(file.name)}`;
		return new Promise((resolve, reject) => {
			const xhr = new XMLHttpRequest();
			xhr.open('PUT', url);
			xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
			xhr.upload.onprogress = (e) => {
				if (onProgress && e.lengthComputable) onProgress(Math.round((e.loaded / e.total) * 100));
			};
			xhr.onload = () => {
				if (xhr.status >= 200 && xhr.status < 300) {
					try {
						resolve(JSON.parse(xhr.responseText));
					} catch {
						resolve({ path: '', name: '' });
					}
					return;
				}
				let msg = xhr.statusText;
				let code: string | undefined;
				try {
					const b = JSON.parse(xhr.responseText) as { error?: string; code?: string };
					if (b.error) msg = b.error;
					code = b.code;
				} catch {
					/* non-JSON */
				}
				reject(new ApiError(xhr.status, msg, code));
			};
			xhr.onerror = () => reject(new ApiError(0, 'network error during upload'));
			xhr.send(file);
		});
	},

	// renameSession updates a session's display name; returns the updated session.
	renameSession(id: string, name: string): Promise<Session> {
		return request(`/sessions/${encodeURIComponent(id)}`, {
			method: 'PATCH',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ name }),
		});
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

	sessionPreview(id: string, signal?: AbortSignal): Promise<SessionPreview> {
		return request(`/sessions/${encodeURIComponent(id)}/preview`, { signal });
	},

	sessionBootstrap(id: string, signal?: AbortSignal): Promise<BootstrapResponse> {
		return request(`/sessions/${encodeURIComponent(id)}/bootstrap`, { signal });
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
