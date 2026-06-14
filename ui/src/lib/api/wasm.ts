// Lazy loader + auth lifecycle for the in-browser Tailscale node (tailscale-web
// WASM).
//
// The wasm is large (~35 MB), so it's dynamically imported — it loads only in
// the browser, and only when the WASM transport is actually used (a cluster the
// browser can't reach directly). The node is brought up once per page; the
// import + init are memoized so concurrent callers share one node.
//
// `tailnetAuth` exposes the login lifecycle for the UI (TailnetAuthBanner):
// connecting → needs_login(url) → running. Used by WasmTransport (transport.ts);
// Direct (rung 1 / same-tailnet) never touches this.

import { writable, type Writable } from 'svelte/store';
import type { InitOptions } from 'tailscale-web';

// The resolved `network` object the wasm bundle exports (fetch, dialTCP, …).
type TailnetNetwork = typeof import('tailscale-web').network;

export type TailnetAuth =
	| { phase: 'idle' }
	| { phase: 'connecting' }
	| { phase: 'needs_login'; url: string }
	| { phase: 'running' }
	| { phase: 'error'; message: string };

export const tailnetAuth: Writable<TailnetAuth> = writable({ phase: 'idle' });

let netPromise: Promise<TailnetNetwork> | undefined;

// hostname for the in-browser node, server-injected or a sane default.
function wasmHostname(): string {
	const cfg = (globalThis as { __deepcove?: { hostname?: string } }).__deepcove;
	return cfg?.hostname ?? 'deepcove-web';
}

// ensureTailnet loads the wasm client and brings the in-browser node up,
// returning the live `network` API and driving `tailnetAuth` through its login
// lifecycle. Memoized — subsequent calls return the same node. On failure it
// clears the memo so a later call (e.g. a Retry) can start over.
export function ensureTailnet(): Promise<TailnetNetwork> {
	if (typeof window === 'undefined') {
		return Promise.reject(new Error('tailnet WASM client is browser-only'));
	}
	if (!netPromise) {
		netPromise = (async () => {
			tailnetAuth.set({ phase: 'connecting' });
			const { network } = await import('tailscale-web');
			await network.init({
				hostname: wasmHostname(),
				onAuthRequired: (url) => tailnetAuth.set({ phase: 'needs_login', url }),
				onAuthComplete: () => tailnetAuth.set({ phase: 'running' }),
			} satisfies InitOptions);
			tailnetAuth.set({ phase: 'running' });
			return network;
		})().catch((err) => {
			tailnetAuth.set({
				phase: 'error',
				message: err instanceof Error ? err.message : String(err),
			});
			netPromise = undefined;
			throw err;
		});
	}
	return netPromise;
}
