// Singleton store of environment readiness (GET /api/preflight). The
// layout banner and the New Session dialog both read it; one shared
// poller avoids duplicate pings. Ref-counted + lazy like the sessions
// store: starts on first subscriber, stops when the last leaves.
//
// Cadence is adaptive — poll fast while Docker is unreachable so the
// banner clears promptly once it's back, slow when healthy.

import { api } from '$lib/api/client';
import type { DockerStatus } from '$lib/api/types';
import { usePolling } from '$lib/polling';

const HEALTHY_MS = 30_000;
const UNHEALTHY_MS = 5_000;

// Optimistic default: assume reachable until the first poll says
// otherwise, so we don't flash a "Docker down" banner on load.
let docker = $state<DockerStatus>({ reachable: true, version: '', message: '' });
let loaded = $state(false);

let subscriberCount = 0;
let stopPolling: (() => void) | undefined;
let abortCtrl: AbortController | undefined;

async function check(signal?: AbortSignal): Promise<void> {
	try {
		const p = await api.preflight(signal);
		docker = p.docker;
		loaded = true;
	} catch {
		// A failed preflight fetch itself (the deepcove server, not
		// Docker) — keep the last known state; the banner reacts only to
		// docker.reachable, and the sessions store surfaces server-down.
		if (signal?.aborted) return;
	}
}

function startIfIdle(): void {
	if (stopPolling) return;
	abortCtrl = new AbortController();
	const signal = abortCtrl.signal;
	stopPolling = usePolling(
		() => check(signal),
		() => (docker.reachable ? HEALTHY_MS : UNHEALTHY_MS),
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
	get docker(): DockerStatus {
		return docker;
	},
	get loaded(): boolean {
		return loaded;
	},
	get dockerReachable(): boolean {
		return docker.reachable;
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

	// refresh forces an immediate re-check — used by the banner's Retry
	// button so the user gets instant feedback rather than waiting for
	// the next poll tick.
	refresh(): Promise<void> {
		return check();
	},
};
