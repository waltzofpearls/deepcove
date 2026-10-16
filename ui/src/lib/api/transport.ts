// The single seam for all runtime traffic to the deepcove API.
//
//   - Direct: native fetch + WebSocket, same-origin. Used when the browser
//     can reach that node's API directly, such as rung 1 localhost, or a node whose
//     tailnet this browser is on.
//   - WASM (lands in #148 step B): routes through an in-browser Tailscale node
//     for nodes the browser can't reach directly (a tailnet the OS isn't on).
//
// The mode is chosen ONCE at boot from server-injected runtime config; nothing
// else in the app branches on it. The API client and the terminal both go
// through `transport`, so adding WASM is a new implementation here, not edits
// scattered across call sites.

import { ensureTailnet } from './wasm';
import { openWasmSocket } from './wasm-ws';

export type TransportMode = 'direct' | 'wasm';

// The subset of the WebSocket API the terminal uses. A native WebSocket
// satisfies it directly (Direct); the WASM adapter implements the same surface
// over a tailnet TCP stream, so the terminal never knows which it holds.
export type DeepcoveSocket = Pick<
  WebSocket,
  'binaryType' | 'readyState' | 'send' | 'close' | 'onopen' | 'onerror' | 'onclose' | 'onmessage'
>;

export interface Transport {
  readonly mode: TransportMode;
  // fetch a root-relative path ('/api/...'); the transport picks the real target.
  fetch(path: string, init?: RequestInit): Promise<Response>;
  // open a WebSocket-like stream for a root-relative path ('/api/.../attach').
  openSocket(path: string, protocols?: string | string[]): DeepcoveSocket;
}

class DirectTransport implements Transport {
  readonly mode = 'direct' as const;

  fetch(path: string, init?: RequestInit): Promise<Response> {
    return fetch(path, init); // same-origin; a root-relative path resolves to this host
  }

  openSocket(path: string, protocols?: string | string[]): DeepcoveSocket {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return new WebSocket(`${proto}//${location.host}${path}`, protocols);
  }
}

// WasmTransport routes traffic through the in-browser Tailscale node, for
// nodes the browser can't reach directly. fetch() rides tailscale-web's
// fetch; the terminal socket (Milestone 2) will ride a dialTCP→WebSocket adapter.
class WasmTransport implements Transport {
  readonly mode = 'wasm' as const;
  private targetBase: string;

  // targetBase is the deepcove node's tailnet address the in-browser node dials, e.g.
  // "http://deepcove-a.tailnet.ts.net". The boot transport uses the server-injected target (the
  // node that served this page); transportForHost builds one per remote session host so per-session
  // traffic reaches the node that actually owns the session, not the dashboard node.
  constructor(targetBase: string) {
    this.targetBase = targetBase.replace(/\/+$/, '');
  }

  // setTargetBase re-points this transport. Used only on the boot transport in dashboard discover
  // mode, where there is no injected target: discovery picks a primary node once the in-browser node
  // is up and points incidental app-level calls at it. No-op for per-host transports.
  setTargetBase(base: string): void {
    this.targetBase = base.replace(/\/+$/, '');
  }

  async fetch(path: string, init?: RequestInit): Promise<Response> {
    // tailscale-web's fetch cannot be cancelled once sent, so a call that is already unwanted never
    // starts.
    init?.signal?.throwIfAborted();
    const net = await ensureTailnet();
    init?.signal?.throwIfAborted();
    // Read once: discovery can move the boot transport to another node while this call waits.
    const targetBase = this.targetBase;
    const nodeName = new URL(targetBase).hostname;
    // The wait is what stops instead: on abort, or once the node stops answering. A long call keeps
    // going for as long as the node answers.
    const answer = this.fetchWithRetry(net, targetBase + path, {
      method: init?.method,
      headers: toRecord(init?.headers),
      // tailscale-web's fetch buffers the body. Strings cover JSON calls; Uint8Array/ArrayBuffer
      // cover binary uploads to a remote node (file paste/drop on a peer-hosted session).
      body: toWasmBody(init?.body),
    });
    const r = await waitWhileNodeAnswers(answer, nodeName, init?.signal, () =>
      nodeAnswersHealth(net, targetBase),
    );
    // Re-wrap as a real Response so client.ts stays transport-agnostic. 204/304
    // forbid a body.
    const noBody = r.status === 204 || r.status === 304;
    return new Response(noBody ? null : await r.arrayBuffer(), {
      status: r.status,
      statusText: r.statusText,
      headers: r.headers,
    });
  }

  // network.fetch throws on a failed dial. Right after a (re)connect the
  // deepcove node's listener can still be warming up (its :443 cert domains lag
  // the Running state). A refused connection never reached the server, so
  // retrying is safe for any method. Retry with backoff for a short window, then
  // surface the error.
  private async fetchWithRetry(
    net: Awaited<ReturnType<typeof ensureTailnet>>,
    url: string,
    init: { method?: string; headers?: Record<string, string>; body?: string | Uint8Array },
  ) {
    const start = Date.now();
    let delay = 250;
    for (;;) {
      try {
        return await net.fetch(url, init);
      } catch (err) {
        if (Date.now() - start >= 3000) throw err;
        await new Promise((r) => setTimeout(r, delay));
        delay = Math.min(delay * 2, 1000);
      }
    }
  }

  openSocket(path: string, protocols?: string | string[]): DeepcoveSocket {
    // dialTCP gives a raw stream; openWasmSocket speaks WebSocket over it to the
    // node's existing :80 WS endpoint (WireGuard encrypts, so no TLS needed).
    const sub = typeof protocols === 'string' ? protocols : protocols?.[0];
    return openWasmSocket(new URL(this.targetBase).hostname, path, sub);
  }
}

// nodeCheckEveryMs is how often a node is checked while a call to it is waiting. missedChecksLimit
// checks in a row going unanswered fail the call.
const nodeCheckEveryMs = 5000;
const missedChecksLimit = 2;
const nodeCheckTimeoutMs = 3000;

// waitWhileNodeAnswers waits for answer, but gives up when signal aborts or when nodeAnswers reports
// the node gone missedChecksLimit times in a row.
export function waitWhileNodeAnswers<T>(
  answer: Promise<T>,
  nodeName: string,
  signal: AbortSignal | null | undefined,
  nodeAnswers: () => Promise<boolean>,
): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    let done = false;
    let missedChecks = 0;
    let checkTimer: ReturnType<typeof setTimeout> | undefined;
    const finish = () => {
      done = true;
      clearTimeout(checkTimer);
      signal?.removeEventListener('abort', onAbort);
    };
    const onAbort = () => {
      finish();
      reject(new DOMException('The request was aborted.', 'AbortError'));
    };
    // Attached first, so the answer's own failure is always handled, even when it is no longer wanted.
    answer.then(
      (value) => {
        if (done) return;
        finish();
        resolve(value);
      },
      (err) => {
        if (done) return;
        finish();
        reject(err);
      },
    );
    if (signal?.aborted) {
      onAbort();
      return;
    }
    signal?.addEventListener('abort', onAbort, { once: true });
    const checkNode = async () => {
      const answered = await nodeAnswers();
      if (done) return;
      missedChecks = answered ? 0 : missedChecks + 1;
      if (missedChecks >= missedChecksLimit) {
        finish();
        reject(new Error(`${nodeName} stopped answering`));
        return;
      }
      checkTimer = setTimeout(checkNode, nodeCheckEveryMs);
    };
    checkTimer = setTimeout(checkNode, nodeCheckEveryMs);
  });
}

// nodeAnswersHealth asks the node's health endpoint, on the same address and port as the call itself,
// so an ACL that blocks ping but allows the call never reads as a dead node. A dead node can hang the
// check, so it is raced.
async function nodeAnswersHealth(
  net: Awaited<ReturnType<typeof ensureTailnet>>,
  targetBase: string,
): Promise<boolean> {
  try {
    const health = await Promise.race([
      net.fetch(targetBase + '/api/health'),
      new Promise<never>((_, reject) =>
        setTimeout(() => reject(new Error('health check timed out')), nodeCheckTimeoutMs),
      ),
    ]);
    return health.ok;
  } catch {
    return false;
  }
}

// toWasmBody narrows a fetch body to what tailscale-web's buffered fetch accepts: a string, or bytes
// for binary uploads. Other BodyInit shapes (Blob, streams, FormData) aren't used on this path.
function toWasmBody(body: BodyInit | null | undefined): string | Uint8Array | undefined {
  if (typeof body === 'string') return body;
  if (body instanceof Uint8Array) return body;
  if (body instanceof ArrayBuffer) return new Uint8Array(body);
  return undefined;
}

function toRecord(h: HeadersInit | undefined): Record<string, string> | undefined {
  if (!h) return undefined;
  if (h instanceof Headers) {
    const o: Record<string, string> = {};
    h.forEach((v, k) => (o[k] = v));
    return o;
  }
  if (Array.isArray(h)) return Object.fromEntries(h);
  return h as Record<string, string>;
}

// detectMode reads the transport the server stamped into the page. Absent (vite
// dev, or any non-injected load) means rung-1 direct.
function detectMode(): TransportMode {
  const injected = (globalThis as { __deepcove?: { transport?: string } }).__deepcove?.transport;
  return injected === 'wasm' ? 'wasm' : 'direct';
}

function injectedTarget(): string {
  const cfg = (globalThis as { __deepcove?: { target?: string } }).__deepcove;
  // The deepcove node that served this page. Falls back to this page's origin (the node served the
  // page itself over the tailnet, so the origin already points at it).
  return cfg?.target ?? location.origin;
}

// detectDiscover reads the dashboard discover flag. When set, the page was served by a thin client that
// hosts no API of its own (`deepcove dashboard` cross-tailnet mode): the in-browser node must discover
// your nodes itself, so the boot transport has no target until discovery picks a primary node.
function detectDiscover(): boolean {
  return (globalThis as { __deepcove?: { discover?: boolean } }).__deepcove?.discover === true;
}

let bootNeedsDiscovery = false;

function create(): Transport {
  if (detectMode() !== 'wasm') return new DirectTransport();
  if (detectDiscover()) {
    // Targetless until discovery resolves a primary. The app is gated behind discovery, so no call
    // rides this transport before setDiscoveredTarget runs.
    bootNeedsDiscovery = true;
    return new WasmTransport('');
  }
  return new WasmTransport(injectedTarget());
}

export const transport: Transport = create();

// discoveryRequired reports whether the app is in dashboard discover mode and must run client-side
// discovery (bring up the in-browser node, enumerate your nodes, fan out the sessions list itself)
// rather than talking to a single server that fans out.
export function discoveryRequired(): boolean {
  return bootNeedsDiscovery;
}

// setDiscoveredTarget points the boot transport at the discovered primary node (its MagicDNS name).
// Incidental app-level calls (agent registry, suggest-name, preflight) ride the boot transport; the
// sessions list fans out across all nodes independently, so it never depends on this one node.
export function setDiscoveredTarget(dnsName: string): void {
  if (transport instanceof WasmTransport) transport.setTargetBase('http://' + dnsName);
}

// ForwardingTransport asks this node to answer for another one. The header names the node; this node
// forwards and streams the answer back.
//
// A WebSocket cannot carry a header, so openSocket goes out plain. Session routes do not need one: the
// node finds the owner from the session id.
class ForwardingTransport implements Transport {
  readonly mode = 'direct' as const;
  constructor(private readonly nodeID: string) {}

  fetch(path: string, init?: RequestInit): Promise<Response> {
    const headers = new Headers(init?.headers);
    headers.set('Deepcove-Node', this.nodeID);
    return transport.fetch(path, { ...init, headers });
  }

  openSocket(path: string, protocols?: string | string[]): DeepcoveSocket {
    return transport.openSocket(path, protocols);
  }
}

// transportForHost picks the transport for a per-session operation, given the session's host node.
//
// Local sessions ride the boot transport. So do remote ones when this page is served by a session
// node: that node forwards to the owner, so the browser needs no tailnet device of its own.
//
// A web-only node has no backend to forward, so there the in-browser Tailscale node dials the owning
// peer directly, one transport per host.
const hostTransports = new Map<string, WasmTransport>();
const forwardingTransports = new Map<string, ForwardingTransport>();

export function transportForHost(
  host?: { dns_name?: string; stable_node_id?: string } | null,
): Transport {
  const dnsName = host?.dns_name;
  if (!dnsName) return transport;
  if (transport.mode === 'direct') {
    const nodeID = host?.stable_node_id;
    if (!nodeID) return transport;
    let f = forwardingTransports.get(nodeID);
    if (!f) {
      f = new ForwardingTransport(nodeID);
      forwardingTransports.set(nodeID, f);
    }
    return f;
  }
  let t = hostTransports.get(dnsName);
  if (!t) {
    t = new WasmTransport('http://' + dnsName);
    hostTransports.set(dnsName, t);
  }
  return t;
}
