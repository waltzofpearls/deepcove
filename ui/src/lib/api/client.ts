// Thin typed wrapper over the /api/* endpoints. All traffic goes through
// `transport` (lib/api/transport.ts): Direct (native fetch/WebSocket) today and
// WASM later, so this file never has to know how the bytes reach the cluster.

import { transport, transportForHost, type DeepcoveSocket, type Transport } from './transport';
import { mergeAggregated, nodeHost } from './sessions';
import type {
	Agent,
	BootstrapResponse,
	ConnectStatus,
	CreateSessionRequest,
	CreateTmuxWindowRequest,
	FsListResponse,
	MountRequest,
	NodeInfo,
	Preflight,
	Session,
	SessionHost,
	SessionListResponse,
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

// request runs an API call through a transport. It defaults to the boot transport (local/this node);
// session-scoped calls pass transportForHost(session.host) so per-session traffic reaches the node
// that owns the session, over WASM when that node is a remote peer.
async function request<T>(path: string, init?: RequestInit, t: Transport = transport): Promise<T> {
	const resp = await t.fetch(BASE + path, init);
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
	// 204 No Content means the caller doesn't get a body. Cast to satisfy T.
	if (resp.status === 204) return undefined as T;
	return (await resp.json()) as T;
}

// peerFanoutTimeoutMs bounds each per-node /api/sessions/local fetch in the browser-side fan-out
// (dashboard mode), mirroring the Go peerFanoutTimeout. A node that doesn't answer in time is reported
// unreachable so one slow node never stalls the whole cross-node list.
const peerFanoutTimeoutMs = 2000;

// fetchNodeSessions GETs one node's own sessions over WASM, rejecting after peerFanoutTimeoutMs. The
// timeout is a racing promise, not an AbortSignal: tailscale-web's fetch doesn't honor signals, so a
// signal alone wouldn't cut a slow dial. The orphaned fetch may still settle later; its result is
// discarded.
function fetchNodeSessions(
	node: NodeInfo,
	opts: { all?: boolean; signal?: AbortSignal },
): Promise<Session[]> {
	const q = opts.all ? '?all=true' : '';
	const fetchP = request<Session[]>(
		`/sessions/local${q}`,
		{ signal: opts.signal },
		transportForHost(nodeHost(node)),
	);
	return new Promise<Session[]>((resolve, reject) => {
		const timer = setTimeout(() => reject(new Error('node fan-out timeout')), peerFanoutTimeoutMs);
		const onAbort = () => reject(new DOMException('aborted', 'AbortError'));
		opts.signal?.addEventListener('abort', onAbort);
		const done = () => {
			clearTimeout(timer);
			opts.signal?.removeEventListener('abort', onAbort);
		};
		fetchP.then(
			(v) => {
				done();
				resolve(v);
			},
			(e) => {
				done();
				reject(e);
			},
		);
	});
}

// uploadViaWasm PUTs a file to a remote session's owning node over the WASM transport (file paste/drop
// on a peer-hosted session). tailscale-web's fetch has no upload-progress events, so onProgress fires
// once as indeterminate (-1), and the file is buffered into bytes and sent as the request body.
async function uploadViaWasm(
	sessionId: string,
	file: File,
	host: SessionHost,
	onProgress?: (pct: number) => void,
): Promise<{ path: string; name: string }> {
	onProgress?.(-1);
	const body = new Uint8Array(await file.arrayBuffer());
	return request<{ path: string; name: string }>(
		`/sessions/${encodeURIComponent(sessionId)}/uploads?name=${encodeURIComponent(file.name)}`,
		{
			method: 'PUT',
			headers: { 'Content-Type': file.type || 'application/octet-stream' },
			body,
		},
		transportForHost(host),
	);
}

// Read endpoints accept an optional AbortSignal so callers can cancel an
// in-flight request on component unmount. Without this, a fetch from an
// unmounted component leaks, because its keep-alive connection stays occupied
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
	// host (optional) checks a specific node's readiness instead of this one, e.g. the chosen create
	// target in the New Session form. Omit for the local/primary node.
	preflight(host?: SessionHost, signal?: AbortSignal): Promise<Preflight> {
		return request('/preflight', { signal }, transportForHost(host));
	},

	// imageExists reports whether an image ref is already downloaded, so
	// the New Session form can show a "will download" hint before create.
	imageExists(image: string, host?: SessionHost, signal?: AbortSignal): Promise<boolean> {
		return request<{ present: boolean }>(
			`/images/exists?image=${encodeURIComponent(image)}`,
			{ signal },
			transportForHost(host),
		).then((r) => r.present);
	},

	// pullImageStream downloads an image, reporting progress via
	// onProgress (percent is -1 while still indeterminate). Resolves when
	// the pull completes; rejects with ApiError on a known-up-front
	// failure (e.g. 503 Docker down) or an in-band stream error.
	async pullImageStream(
		image: string,
		onProgress: (p: { status: string; percent: number }) => void,
		host?: SessionHost,
	): Promise<void> {
		const resp = await transportForHost(host).fetch(`${BASE}/images/pull`, {
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
		if (!resp.body) return; // no stream, so assume done
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

	// fsList returns the subdirectories of a host path (scoped to $HOME on
	// the deepcove host) for the mount host-path autocomplete + Browse modal.
	// Omit path to start at the home directory.
	// host (optional) browses a remote node's filesystem instead of this one, e.g. when editing the
	// mounts of a session that lives on a peer. Omit for this node.
	fsList(path?: string, host?: SessionHost, signal?: AbortSignal): Promise<FsListResponse> {
		const q = path ? `?path=${encodeURIComponent(path)}` : '';
		return request(`/fs/list${q}`, { signal }, transportForHost(host));
	},

	// connect starts (or returns) the Tailscale node for the "Connect" wizard;
	// connectStatus polls its state (phase / login_url / url / tls / owner).
	connect(): Promise<ConnectStatus> {
		return request('/connect', { method: 'POST' });
	},
	connectStatus(signal?: AbortSignal): Promise<ConnectStatus> {
		return request('/connect/status', { signal });
	},
	// disconnect stops + keeps tsnet state (quick reconnect); reset logs the node
	// out of the tailnet and wipes state (fresh login next time).
	disconnect(): Promise<ConnectStatus> {
		return request('/connect/disconnect', { method: 'POST' });
	},
	resetConnect(): Promise<ConnectStatus> {
		return request('/connect/reset', { method: 'POST' });
	},

	// nodes lists the user's deepcove machines (this one + same-user tailnet peers). On rung 1 it's
	// just this machine.
	nodes(signal?: AbortSignal): Promise<NodeInfo[]> {
		return request('/nodes', { signal });
	},

	// webNodes lists the user's in-browser web nodes on the tailnet (online-only, os=js). Kept separate
	// from nodes() so browsers never enter session fan-out; used only for the Nodes page.
	webNodes(signal?: AbortSignal): Promise<NodeInfo[]> {
		return request('/nodes/web', { signal });
	},

	// Aggregated across all the user's nodes (each session tagged with its host; unreachable peers
	// reported). On a single node it's just this machine's sessions.
	listSessions(opts: { all?: boolean; signal?: AbortSignal } = {}): Promise<SessionListResponse> {
		const q = opts.all ? '?all=true' : '';
		return request(`/sessions${q}`, { signal: opts.signal });
	},

	// Browser-side fan-out for dashboard mode: with no server node to aggregate, the browser itself
	// queries each discovered node's /api/sessions/local in parallel over WASM and merges the results
	// (tagging hosts, recording unreachable nodes), mirroring the Go session.ListAggregated. Offline
	// nodes are skipped, not awaited. `self` is the browser's own tailnet identity so every session
	// reads as remote and shows its host. The whole thing is abortable via opts.signal.
	async listSessionsAcrossNodes(
		nodes: NodeInfo[],
		self: SessionHost | null,
		opts: { all?: boolean; signal?: AbortSignal } = {},
	): Promise<SessionListResponse> {
		const online = nodes.filter((n) => n.online);
		const perNode = await Promise.all(
			online.map(async (node) => {
				try {
					return { node, sessions: await fetchNodeSessions(node, opts) };
				} catch {
					return { node, sessions: null }; // timed out, aborted, or errored, so unreachable
				}
			}),
		);
		if (opts.signal?.aborted) throw new DOMException('aborted', 'AbortError');
		return mergeAggregated(perNode, self);
	},

	// Session-scoped reads/writes take an optional host: the node that owns the session, from the
	// aggregated list. Pass it for remote sessions so the call routes to that node over WASM; omit it
	// (or pass this node's host) for local sessions to use the boot transport.
	getSession(id: string, host?: SessionHost, signal?: AbortSignal): Promise<Session> {
		return request(`/sessions/${encodeURIComponent(id)}`, { signal }, transportForHost(host));
	},

	// uploadFile streams a pasted/dropped file into the session's container (written via docker exec,
	// never the host) and returns its in-container path for injection into the agent's prompt.
	//
	// Local sessions use XHR so the caller gets real upload progress via onProgress (0–100). A remote
	// session (host set) rides the WASM transport to its owning node instead; tailscale-web's fetch has
	// no upload-progress events, so onProgress fires once with -1 (indeterminate) and the UI shows a
	// loading state rather than a percentage.
	uploadFile(
		sessionId: string,
		file: File,
		onProgress?: (pct: number) => void,
		host?: SessionHost,
	): Promise<{ path: string; name: string }> {
		if (host) return uploadViaWasm(sessionId, file, host, onProgress);
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
	renameSession(id: string, name: string, host?: SessionHost): Promise<Session> {
		return request(
			`/sessions/${encodeURIComponent(id)}`,
			{
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ name }),
			},
			transportForHost(host),
		);
	},

	// suggestName returns a generated session name to pre-fill the New Session
	// form's (required) Name field, the same generator the server uses by default.
	suggestName(agent: string): Promise<string> {
		return request<{ name: string }>(
			`/sessions/suggest-name?agent=${encodeURIComponent(agent)}`,
		).then((r) => r.name);
	},

	// Create always posts to the node serving this page (the boot transport) and lets it route by
	// req.node_id: it creates locally if it's the target, else proxies to the target over its own
	// tailnet. This works in both connected and dashboard mode without needing the in-browser WASM node
	// signed in, because host-routing the create would hang when WASM isn't up.
	createSession(req: CreateSessionRequest): Promise<Session> {
		return request('/sessions', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(req),
		});
	},

	startSession(id: string, host?: SessionHost): Promise<void> {
		return request(
			`/sessions/${encodeURIComponent(id)}/start`,
			{ method: 'POST' },
			transportForHost(host),
		);
	},

	stopSession(id: string, host?: SessionHost): Promise<void> {
		return request(
			`/sessions/${encodeURIComponent(id)}/stop`,
			{ method: 'POST' },
			transportForHost(host),
		);
	},

	destroySession(id: string, host?: SessionHost): Promise<void> {
		return request(
			`/sessions/${encodeURIComponent(id)}`,
			{ method: 'DELETE' },
			transportForHost(host),
		);
	},

	// editMounts replaces the session's user mounts. Server-side this
	// destroys the running container and recreates it with the new
	// spec. The shared agent-home volume (/root) survives, but the
	// writable layer (apt-installed packages, and a no-mount
	// /workspace's scratch contents) is gone. A user-mounted folder is
	// host-owned and unaffected. UI is expected to warn before calling.
	// Returns the updated Session.
	editMounts(id: string, mounts: MountRequest[], host?: SessionHost): Promise<Session> {
		return request(
			`/sessions/${encodeURIComponent(id)}/mounts`,
			{
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ mounts }),
			},
			transportForHost(host),
		);
	},

	sessionPreview(id: string, host?: SessionHost, signal?: AbortSignal): Promise<SessionPreview> {
		return request(`/sessions/${encodeURIComponent(id)}/preview`, { signal }, transportForHost(host));
	},

	sessionBootstrap(
		id: string,
		host?: SessionHost,
		signal?: AbortSignal,
	): Promise<BootstrapResponse> {
		return request(
			`/sessions/${encodeURIComponent(id)}/bootstrap`,
			{ signal },
			transportForHost(host),
		);
	},

	// createTmuxWindow creates a new tmux window in the session.
	// Empty body → server auto-picks a name (shell / shell-2 / ...).
	// 409 means a window with that name already exists, so callers that
	// need idempotent "ensure exists" semantics should treat 409 as
	// success.
	createTmuxWindow(
		sessionId: string,
		req: CreateTmuxWindowRequest = {},
		host?: SessionHost,
	): Promise<TmuxWindow> {
		return request(
			`/sessions/${encodeURIComponent(sessionId)}/tmux-windows`,
			{
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(req),
			},
			transportForHost(host),
		);
	},

	// openAttachSocket opens a live PTY stream to a session's tmux, via the
	// transport (a native WebSocket on Direct; the tailnet WS adapter on WASM).
	// The optional `tmuxWindow` argument targets a specific window so two
	// simultaneous attaches can each pin to their own window (per-client window
	// focus is a tmux native feature). Omit for legacy follow-the-session's-
	// current-window behavior.
	openAttachSocket(id: string, tmuxWindow?: string, host?: SessionHost): DeepcoveSocket {
		const q = tmuxWindow ? `?tmux_window=${encodeURIComponent(tmuxWindow)}` : '';
		return transportForHost(host).openSocket(
			`${BASE}/sessions/${encodeURIComponent(id)}/attach${q}`,
			'deepcove.pty.v1',
		);
	},
};
