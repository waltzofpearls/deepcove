// The single seam for all runtime traffic to the deepcove API.
//
//   - Direct: native fetch + WebSocket, same-origin. Used when the browser
//     can reach the cluster's API directly — rung 1 localhost, or a node whose
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
	private readonly targetBase: string;

	constructor() {
		const cfg = (globalThis as { __deepcove?: { target?: string } }).__deepcove;
		// The deepcove node's tailnet address the in-browser node dials. Injected by
		// the server; falls back to this page's origin (the page was served by the
		// node itself over the tailnet).
		this.targetBase = (cfg?.target ?? location.origin).replace(/\/+$/, '');
	}

	async fetch(path: string, init?: RequestInit): Promise<Response> {
		const net = await ensureTailnet();
		const r = await this.fetchWithRetry(net, this.targetBase + path, {
			method: init?.method,
			headers: toRecord(init?.headers),
			// tailscale-web's fetch buffers the body; strings cover every API call
			// (uploads use XHR, which doesn't ride this path).
			body: typeof init?.body === 'string' ? init.body : undefined,
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

	// network.fetch throws on a failed dial — right after a (re)connect the
	// deepcove node's listener can still be warming up (its :443 cert domains lag
	// the Running state). A refused connection never reached the server, so
	// retrying is safe for any method. Retry with backoff for a short window, then
	// surface the error.
	private async fetchWithRetry(
		net: Awaited<ReturnType<typeof ensureTailnet>>,
		url: string,
		init: { method?: string; headers?: Record<string, string>; body?: string },
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

function create(): Transport {
	return detectMode() === 'wasm' ? new WasmTransport() : new DirectTransport();
}

export const transport: Transport = create();
