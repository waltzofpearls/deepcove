// Thin typed wrapper over the /api/* endpoints. All traffic goes through
// `transport` (lib/api/transport.ts): Direct (native fetch/WebSocket) today and
// WASM later, so this file never has to know how the bytes reach the node.

import { transport, transportForHost, type DeepcoveSocket, type Transport } from './transport';
import { mergeAggregated, nodeHost } from './sessions';
import type {
  Agent,
  BootstrapResponse,
  ConnectStatus,
  CreateSessionRequest,
  CreateTmuxWindowRequest,
  FsListResponse,
  LLMProxy,
  MountRequest,
  NodeInfo,
  Preflight,
  RuntimesViewResponse,
  Session,
  SessionHost,
  SessionListResponse,
  SessionPreview,
  TmuxWindow,
  WorkspaceVM,
  WorkspaceVMRequest,
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
async function requestRaw<T>(
  path: string,
  init?: RequestInit,
  t: Transport = transport,
  reseeded = false,
): Promise<{ data: T; resp: Response }> {
  const resp = await t.fetch(BASE + path, init);
  // The local token changes on every restart, so an open tab's cookie goes stale and API calls 401.
  // Any non-/api response sets a fresh one. Direct mode only; the tailnet listener uses identity.
  if (resp.status === 401 && !reseeded && t.mode === 'direct') {
    await reseedLocalToken(t);
    return requestRaw<T>(path, init, t, true);
  }
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
  if (resp.status === 204) return { data: undefined as T, resp };
  return { data: (await resp.json()) as T, resp };
}

// Fetch any non-/api path just to pick up a fresh cookie. Errors are ignored; the retry reports them.
async function reseedLocalToken(t: Transport): Promise<void> {
  try {
    await t.fetch('/', { cache: 'no-store' });
  } catch {
    /* server still coming back up */
  }
}

// request is the body-only common case; requestRaw also returns the Response (for a header).
async function request<T>(path: string, init?: RequestInit, t: Transport = transport): Promise<T> {
  return (await requestRaw<T>(path, init, t)).data;
}

// peerFanoutTimeoutMs bounds each per-node /api/sessions/local fetch in the browser-side fan-out
// (dashboard mode), mirroring the Go peerFanoutTimeout. A node that doesn't answer in time is reported
// unreachable so one slow node never stalls the whole cross-node list.
const peerFanoutTimeoutMs = 2000;

// withTimeout rejects if p doesn't settle within ms. For calls over the WASM transport, which can hang
// on a dead peer (it doesn't honor AbortSignal); the orphaned promise may still settle later, discarded.
function withTimeout<T>(p: Promise<T>, ms: number, label: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(label + ' timed out')), ms);
    p.then(
      (v) => {
        clearTimeout(t);
        resolve(v);
      },
      (e) => {
        clearTimeout(t);
        reject(e);
      },
    );
  });
}

// fetchNodeSessions GETs one node's own sessions over WASM, rejecting after peerFanoutTimeoutMs. The
// timeout is a racing promise, not an AbortSignal: tailscale-web's fetch doesn't honor signals, so a
// signal alone wouldn't cut a slow dial. The orphaned fetch may still settle later; its result is
// discarded.
function fetchNodeSessions(
  node: NodeInfo,
  opts: { all?: boolean; signal?: AbortSignal },
): Promise<{ sessions: Session[] }> {
  const q = opts.all ? '?all=true' : '';
  const fetchP = requestRaw<Session[]>(
    `/sessions/local${q}`,
    { signal: opts.signal },
    transportForHost(nodeHost(node)),
  ).then(({ data }) => ({ sessions: data }));
  return new Promise<{ sessions: Session[] }>((resolve, reject) => {
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

// readProgressStream consumes the newline delimited JSON that the image pull and the runtime setup both
// emit. Split out so the two callers cannot drift on framing, trailing lines, or in-band errors.
//
// An in-band error line throws, which matters: the HTTP status is already 200 by the time a download
// fails, so without this a failure would look like success.
export async function readProgressStream(
  resp: Response,
  onProgress: (p: { status: string; percent: number }) => void,
): Promise<void> {
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
}

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

  // runtimes reports what this node's runtimes are hosting, joined against the session table. Always
  // this node: which machine a sandbox runs on is the whole question, so there is nothing to fan out.
  runtimes(host?: SessionHost, signal?: AbortSignal): Promise<RuntimesViewResponse> {
    return request('/runtimes', { signal }, transportForHost(host));
  },

  // destroySandbox removes a sandbox no session owns. The server re-checks that ownership, so a stale
  // view cannot talk it into deleting something still in use.
  destroySandbox(runtime: string, sandboxID: string, host?: SessionHost): Promise<void> {
    return request(
      `/runtimes/${encodeURIComponent(runtime)}/sandboxes/${encodeURIComponent(sandboxID)}`,
      { method: 'DELETE' },
      transportForHost(host),
    );
  },

  // restartSandbox stops and starts a VM with its saved settings, so an edited size or folder applies.
  restartSandbox(runtime: string, sandboxID: string, host?: SessionHost): Promise<void> {
    return request(
      `/runtimes/${encodeURIComponent(runtime)}/sandboxes/${encodeURIComponent(sandboxID)}/restart`,
      { method: 'POST' },
      transportForHost(host),
    );
  },

  // llmProxy is where a node's sessions send LLM requests. The setting is per node, so a session on
  // another node is asked about that node.
  llmProxy(host?: SessionHost, signal?: AbortSignal): Promise<LLMProxy> {
    return request('/llm-proxy', { signal }, transportForHost(host));
  },

  // An empty url sends sessions to the provider. Applies to machines built after it, not running ones.
  saveLLMProxy(url: string): Promise<LLMProxy> {
    return request('/llm-proxy', { method: 'PUT', body: JSON.stringify({ url }) });
  },

  // testLLMProxy asks this node to make one request to the configured proxy, so the UI can report
  // whether it is reachable from here.
  testLLMProxy(): Promise<{ result: string }> {
    return request('/llm-proxy/test', { method: 'POST' });
  },

  // workspaceVM is this node's shared machine, or null when there is none. Null is the normal state
  // until someone creates one, so it is not an error.
  async workspaceVM(host?: SessionHost, signal?: AbortSignal): Promise<WorkspaceVM | null> {
    try {
      return await request<WorkspaceVM>('/workspace-vm', { signal }, transportForHost(host));
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return null;
      throw err;
    }
  },

  // saveWorkspaceVM records the machine or changes it. The VM itself is built when the first session
  // lands on it, which is why this returns immediately.
  saveWorkspaceVM(req: WorkspaceVMRequest, host?: SessionHost): Promise<WorkspaceVM> {
    return request(
      '/workspace-vm',
      { method: 'PUT', body: JSON.stringify(req) },
      transportForHost(host),
    );
  },

  // deleteWorkspaceVM destroys the machine. withSessions deletes the sessions on it too; without it the
  // server refuses with a 409 while any remain.
  deleteWorkspaceVM(withSessions = false, host?: SessionHost): Promise<void> {
    return request(
      `/workspace-vm${withSessions ? '?sessions=delete' : ''}`,
      { method: 'DELETE' },
      transportForHost(host),
    );
  },

  // imageExists reports whether an image ref is already downloaded, so
  // the New Session form can show a "will download" hint before create.
  // runtime scopes the check to that runtime's image store: a VM runtime builds its own disk image, so
  // an image present for Docker is not present for it.
  imageExists(
    image: string,
    host?: SessionHost,
    signal?: AbortSignal,
    runtime?: string,
  ): Promise<boolean> {
    const q = new URLSearchParams({ image });
    if (runtime) q.set('runtime', runtime);
    return request<{ present: boolean }>(
      `/images/exists?${q}`,
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
    runtime?: string,
  ): Promise<void> {
    const resp = await transportForHost(host).fetch(`${BASE}/images/pull`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ image, runtime }),
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
    await readProgressStream(resp, onProgress);
  },

  // setupRuntime downloads whatever a runtime is missing, reporting progress as it goes. On success the
  // node can host sessions on it immediately, so callers refresh rather than asking for a restart.
  async setupRuntime(
    name: string,
    onProgress: (p: { status: string; percent: number }) => void,
    host?: SessionHost,
  ): Promise<void> {
    const resp = await transportForHost(host).fetch(
      `${BASE}/runtimes/${encodeURIComponent(name)}/setup`,
      { method: 'POST' },
    );
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
    await readProgressStream(resp, onProgress);
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
          const { sessions } = await fetchNodeSessions(node, opts);
          return { node, sessions };
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

  // setSessionUseLLMProxy turns the node's LLM proxy on or off for one session, restarting its agent.
  setSessionUseLLMProxy(id: string, use: boolean, host?: SessionHost): Promise<Session> {
    return request(
      `/sessions/${encodeURIComponent(id)}`,
      {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ use_llm_proxy: use }),
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
    return request(
      `/sessions/${encodeURIComponent(id)}/preview`,
      { signal },
      transportForHost(host),
    );
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
