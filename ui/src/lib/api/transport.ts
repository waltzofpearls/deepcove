// The single seam for all runtime traffic to the deepcove API.
//
//   - Direct: native fetch + WebSocket, same-origin. Used when the browser
//     can reach the cluster's API directly, such as rung 1 localhost, or a node whose
//     tailnet this browser is on.
//   - WASM (lands in #148 step B): routes through an in-browser Tailscale node
//     for clusters the browser can't reach directly (a tailnet the OS isn't on).
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
// clusters the browser can't reach directly. fetch() rides tailscale-web's
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
    const net = await ensureTailnet();
    const r = await this.fetchWithRetry(net, this.targetBase + path, {
      method: init?.method,
      headers: toRecord(init?.headers),
      // tailscale-web's fetch buffers the body. Strings cover JSON calls; Uint8Array/ArrayBuffer
      // cover binary uploads to a remote node (file paste/drop on a peer-hosted session).
      body: toWasmBody(init?.body),
    });
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
// the cluster itself, so the boot transport has no target until discovery picks a primary node.
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
// discovery (bring up the in-browser node, enumerate the cluster, fan out the sessions list itself)
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

// transportForHost picks the transport for a per-session operation, given the session's host node.
//
// Local sessions (host omitted, or hosted by the node that served this page) ride the boot transport:
// Direct on rung 1, or the boot WASM node on the Tailscale rungs. Remote sessions (a peer node) ride
// a per-host WASM transport: the in-browser Tailscale node dials that peer's MagicDNS name directly,
// keeping the dashboard node out of the per-session stream path. WASM is required here, not just an
// optimization, because the dashboard machine may not be on the cluster's tailnet at all (the OS sits
// on a different tailnet, or none); only the in-browser node can reach the peer. See
// project_multinode_scope (Phase D).
const hostTransports = new Map<string, WasmTransport>();

export function transportForHost(host?: { dns_name?: string } | null): Transport {
  const dnsName = host?.dns_name;
  if (!dnsName) return transport;
  let t = hostTransports.get(dnsName);
  if (!t) {
    t = new WasmTransport('http://' + dnsName);
    hostTransports.set(dnsName, t);
  }
  return t;
}
