// Shared connection status for the topbar pill (and anyone else who wants to know whether this node is
// on a tailnet). Polls GET /api/connect/status, lazy + ref-counted like the sessions/preflight stores
// so it only runs while something is mounted. Cadence is adaptive: fast while a connect is in flight so
// the pill flips promptly, slow once it's settled.
//
// Only meaningful in connected/local (direct) mode, the node's own tailnet connection. Dashboard
// (wasm) mode is a thin client with no /api/connect of its own, so the topbar doesn't poll this there.

import { api } from '$lib/api/client';
import type { ConnectStatus } from '$lib/api/types';
import { usePolling } from '$lib/polling';

const RUNNING_MS = 15_000;
const PENDING_MS = 2_500;

let status = $state<ConnectStatus | null>(null);
let loaded = $state(false);

let subscriberCount = 0;
let stopPolling: (() => void) | undefined;
let abortCtrl: AbortController | undefined;

async function check(signal: AbortSignal): Promise<void> {
  try {
    status = await api.connectStatus(signal);
    loaded = true;
  } catch {
    // A failed status fetch (server hiccup) keeps the last known value rather than flapping the pill.
    if (signal.aborted) return;
  }
}

function startIfIdle(): void {
  if (stopPolling) return;
  abortCtrl = new AbortController();
  const signal = abortCtrl.signal;
  stopPolling = usePolling(
    () => check(signal),
    () =>
      status && (status.phase === 'starting' || status.phase === 'needs_login')
        ? PENDING_MS
        : RUNNING_MS,
  );
}

function stopIfDone(): void {
  if (subscriberCount > 0) return;
  abortCtrl?.abort();
  abortCtrl = undefined;
  stopPolling?.();
  stopPolling = undefined;
}

export const connect = {
  get status(): ConnectStatus | null {
    return status;
  },
  get connected(): boolean {
    return status?.phase === 'running';
  },
  // pending: a connect is underway (starting up, or waiting for the sign-in to finish).
  get pending(): boolean {
    return status?.phase === 'starting' || status?.phase === 'needs_login';
  },
  get ownerName(): string {
    return status?.owner ? status.owner.display_name || status.owner.login_name || '' : '';
  },
  get loaded(): boolean {
    return loaded;
  },

  // refresh does one immediate status fetch, used right after kicking off a connect so the UI flips to
  // "starting" now instead of waiting for the next poll tick (up to 15s away while idle).
  async refresh(): Promise<void> {
    const ctrl = new AbortController();
    await check(ctrl.signal);
  },

  // subscribe starts the poll on first subscriber and stops it when the last leaves. Return it from a
  // Svelte $effect: `$effect(() => connect.subscribe())`.
  subscribe(): () => void {
    subscriberCount++;
    startIfIdle();
    return () => {
      subscriberCount--;
      stopIfDone();
    };
  },
};
