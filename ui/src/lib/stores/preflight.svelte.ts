// Singleton store of environment readiness (GET /api/preflight). The
// layout banner and the New Session dialog both read it; one shared
// poller avoids duplicate pings. Ref-counted + lazy like the sessions
// store: starts on first subscriber, stops when the last leaves.
//
// Cadence is adaptive. We poll fast while nothing can host a session so
// the banner clears promptly once something can, and slow when healthy.

import { api, preflightTimeoutMs } from '$lib/api/client';
import type { RuntimesInfo, SessionResources } from '$lib/api/types';
import { usePolling } from '$lib/polling';
import { usableRuntimes } from '$lib/util/runtime';

const HEALTHY_MS = 30_000;
const UNHEALTHY_MS = 5_000;

// Null until the first poll, so the New Session form can tell "not asked yet" from "nothing to run on"
// and avoid flashing a runtime picker that then disappears.
let runtimes = $state<RuntimesInfo | null>(null);
let loaded = $state(false);

let subscriberCount = 0;
let stopPolling: (() => void) | undefined;
let abortCtrl: AbortController | undefined;

async function check(signal?: AbortSignal): Promise<void> {
  try {
    // Bounded, so one hung check over the WASM transport cannot stop the polling for good.
    const p = await api.preflight(undefined, signal, preflightTimeoutMs);
    runtimes = p.runtimes ?? null;
    loaded = true;
  } catch {
    // A failed preflight fetch itself keeps the last known state. The
    // sessions store surfaces server-down.
    if (signal?.aborted) return;
  }
}

function startIfIdle(): void {
  if (stopPolling) return;
  abortCtrl = new AbortController();
  const signal = abortCtrl.signal;
  stopPolling = usePolling(
    () => check(signal),
    () => (usableRuntimes(runtimes).length > 0 ? HEALTHY_MS : UNHEALTHY_MS),
  );
}

function stopIfDone(): void {
  if (subscriberCount > 0) return;
  abortCtrl?.abort();
  abortCtrl = undefined;
  stopPolling?.();
  stopPolling = undefined;
}

export const preflight = {
  get runtimes(): RuntimesInfo | null {
    return runtimes;
  },
  get loaded(): boolean {
    return loaded;
  },
  get usableRuntimes(): string[] {
    return usableRuntimes(runtimes);
  },
  // What a new workspace VM should be sized at on this host. Null until the first poll lands, so a form
  // can tell "not asked yet" from "this host reports nothing".
  get workspaceDefaults(): SessionResources | null {
    return runtimes?.workspace_defaults ?? null;
  },
  // ready is the question the UI actually asks: can this node run a session at all. Before the first
  // poll lands we say yes, so a healthy node never flashes a "nothing can run" banner on load.
  get ready(): boolean {
    return !loaded || usableRuntimes(runtimes).length > 0;
  },

  // subscribe registers interest; returns a cleanup for use in a
  // Svelte $effect: `$effect(() => preflight.subscribe())`.
  subscribe(): () => void {
    subscriberCount++;
    startIfIdle();
    return () => {
      subscriberCount--;
      stopIfDone();
    };
  },

  // refresh forces an immediate re-check, used by the banner's Retry
  // button so the user gets instant feedback rather than waiting for
  // the next poll tick.
  refresh(): Promise<void> {
    return check();
  },
};
