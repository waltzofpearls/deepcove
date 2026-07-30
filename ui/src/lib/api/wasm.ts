// Lazy loader + auth lifecycle for the in-browser Tailscale node (tailscale-web
// WASM).
//
// The wasm is large (~35 MB), so it's dynamically imported. It loads only in
// the browser, and only when the WASM transport is actually used (a cluster the
// browser can't reach directly). The node is brought up once per page; the
// import + init are memoized so concurrent callers share one node.
//
// `tailnetAuth` exposes the login lifecycle for the UI (TailnetAuthBanner):
// connecting → needs_login(url) → running. Used by WasmTransport (transport.ts);
// Direct (rung 1 / same-tailnet) never touches this.

import { writable, type Writable } from 'svelte/store';
import type { InitOptions } from 'tailscale-web';
import { machineName, tailnetDomain } from '../util/tailnet';

// The resolved `network` object the wasm bundle exports (fetch, dialTCP, …).
type TailnetNetwork = typeof import('tailscale-web').network;

export type TailnetAuth =
  | { phase: 'idle' }
  | { phase: 'connecting' }
  | { phase: 'needs_login'; url: string }
  | { phase: 'running' }
  | { phase: 'error'; message: string };

export const tailnetAuth: Writable<TailnetAuth> = writable({ phase: 'idle' });

// localStorage key prefix tailscale-web persists its node state under (keys are "{prefix}_{stateKey}").
// Pinned explicitly (it's also the library default) so clearWebNodeSignIn clears exactly these keys.
const STORAGE_PREFIX = 'tailscale-web';

let netPromise: Promise<TailnetNetwork> | undefined;

// hostname for the in-browser node, server-injected or a sane default.
export function wasmHostname(): string {
  const cfg = (globalThis as { __deepcove?: { hostname?: string } }).__deepcove;
  return cfg?.hostname ?? 'deepcove-web';
}

// WebNodeInfo describes the in-browser Tailscale node for the device info dialog.
export interface WebNodeInfo {
  name: string;
  dnsName: string; // full MagicDNS FQDN, e.g. deepcove-web.tailXXXX.ts.net
  tailnet: string; // the tailnet domain (dnsName with the host label stripped)
  ipv4: string;
  ipv6: string;
}

// webNodeInfo returns the in-browser node's name, MagicDNS name, tailnet, and addresses. Only meaningful
// once it's running (the info dialog is shown only then); awaits the memoized node and reads live status.
export async function webNodeInfo(): Promise<WebNodeInfo> {
  const net = await ensureTailnet();
  const self = net.status().self;
  const dnsName = (self?.dnsName ?? '').replace(/\.$/, '');
  // The real machine name carries Tailscale's dedup suffix (deepcove-web-8), which lives in the
  // MagicDNS name, not the requested hostName; fall back to the hostname before the node has a name.
  return {
    name: machineName(dnsName) || self?.hostName || wasmHostname(),
    dnsName,
    tailnet: tailnetDomain(dnsName),
    ipv4: net.localIPv4(),
    ipv6: net.localIPv6(),
  };
}

// hasWebNodeSignIn reports whether the in-browser node has saved Tailscale credentials (the user signed
// it in before). A cheap synchronous localStorage scan, so the app can decide whether to auto-reconnect
// the web node on load without paying the ~35MB wasm load just to ask.
export function hasWebNodeSignIn(): boolean {
  if (typeof window === 'undefined') return false;
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i);
    if (key && key.startsWith(STORAGE_PREFIX)) return true;
  }
  return false;
}

// clearWebNodeSignIn removes the in-browser node's saved Tailscale credentials from localStorage, so
// the next deepcove session signs in fresh. tailscale-web has no logout or shutdown call, so the node
// already running in this tab stays connected until the tab is closed; this only changes the next load.
export function clearWebNodeSignIn(): void {
  if (typeof window === 'undefined') return;
  for (let i = localStorage.length - 1; i >= 0; i--) {
    const key = localStorage.key(i);
    if (key && key.startsWith(STORAGE_PREFIX)) localStorage.removeItem(key);
  }
}

// ensureTailnet loads the wasm client and brings the in-browser node up,
// returning the live `network` API and driving `tailnetAuth` through its login
// lifecycle. Memoized, so subsequent calls return the same node. On failure it
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
        storagePrefix: STORAGE_PREFIX,
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
