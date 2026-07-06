// Singleton store of the sessions list. The dashboard, the
// sidebar's session submenu (desktop), and the topbar's tab
// carousel (mobile) all want the same polled data. Three
// independent pollers would triple the request rate for the same
// information.
//
// API shape mirrors `preferences.svelte.ts`: import the store,
// read getter-backed reactive properties. Polling is lazy
// (starts on first subscriber) and ref-counted (stops when the
// last subscriber unsubscribes), so when no consumer is mounted
// the store is dormant, which is important for SSR and idle pages.
//
// Consumer pattern (in a Svelte component):
//
//   import { sessions } from '$lib/stores/sessions.svelte';
//   $effect(() => sessions.subscribe());   // auto-cleanup on unmount
//   ...
//   {#if sessions.error}
//   {:else if sessions.loading}
//   {:else}
//     {#each sessions.list as s (s.id)} ... {/each}
//   {/if}

import { ApiError, api } from '$lib/api/client';
import { hostForSession, isRemoteSession, sessionHostLabel } from '$lib/api/sessions';
import { discoveryRequired } from '$lib/api/transport';
import type { Session, SessionHost } from '$lib/api/types';
import { usePolling } from '$lib/polling';
import { discovery } from './discovery.svelte';
import { preferences } from './preferences.svelte';

let list = $state<Session[]>([]);
// self: this node's identity; unreachable: peers whose deepcove API didn't answer this poll. Both
// come from the aggregated list and drive the cross-node UI (host badges, "node unreachable" notice).
let self = $state<SessionHost | null>(null);
let unreachable = $state<SessionHost[]>([]);
let loading = $state(true);
let error = $state<string | null>(null);

// Reference-counted lifecycle. `subscriberCount` tracks active
// consumers; `stopPolling` is the cleanup returned by usePolling.
let subscriberCount = 0;
let stopPolling: (() => void) | undefined;

let abortCtrl: AbortController | undefined;

function startIfIdle(): void {
	if (stopPolling) return;
	abortCtrl = new AbortController();
	const signal = abortCtrl.signal;
	stopPolling = usePolling(
		async () => {
			try {
				// Dashboard mode: the browser is the aggregator, so fan out across the discovered nodes
				// ourselves. Connected mode: a server node fans out behind /api/sessions.
				const resp = discoveryRequired()
					? await api.listSessionsAcrossNodes(discovery.nodes, discovery.self, {
							all: true,
							signal,
						})
					: await api.listSessions({ all: true, signal });
				list = resp.sessions ?? [];
				self = resp.self ?? null;
				unreachable = resp.unreachable ?? [];
				error = null;
			} catch (e) {
				if (signal.aborted) return;
				error = e instanceof ApiError ? e.message : String(e);
			} finally {
				loading = false;
			}
		},
		() => preferences.pollIntervalMs
	);
}

function stopIfDone(): void {
	if (subscriberCount > 0) return;
	abortCtrl?.abort();
	abortCtrl = undefined;
	stopPolling?.();
	stopPolling = undefined;
}

export const sessions = {
	get list(): Session[] {
		return list;
	},
	// self is this node; isRemote(s) tells whether a session lives on a different node, which the UI
	// uses to decide whether to badge it with its host.
	get self(): SessionHost | null {
		return self;
	},
	get unreachable(): SessionHost[] {
		return unreachable;
	},
	isRemote(session: Session): boolean {
		return isRemoteSession(session, self);
	},
	// hostLabel is the remote-host badge text, or undefined for a local session. See sessionHostLabel.
	hostLabel(session: Session): string | undefined {
		return sessionHostLabel(session, self);
	},
	// find looks a session up in the aggregated list by id. The session detail page uses it to learn a
	// session's host (which the single-session GET doesn't carry) so its per-session calls can route to
	// the owning node. Undefined until the first poll lands or for an unknown id.
	find(id: string): Session | undefined {
		return list.find((s) => s.id === id);
	},
	// hostFor resolves the host to route a session's per-session API calls to: the owning node for a
	// remote session, undefined for a local one (use the boot transport). See hostForSession.
	hostFor(session: Session): SessionHost | undefined {
		return hostForSession(session, self);
	},
	get loading(): boolean {
		return loading;
	},
	get error(): string | null {
		return error;
	},

	// subscribe registers interest in the sessions list. First call
	// kicks off the poll loop; the returned cleanup decrements the
	// ref-count and stops the loop when it reaches zero. Designed to
	// be returned from a Svelte $effect:
	//
	//   $effect(() => sessions.subscribe());
	subscribe(): () => void {
		subscriberCount++;
		startIfIdle();
		return () => {
			subscriberCount--;
			stopIfDone();
		};
	},

	// remove drops a session from the list synchronously, used as an
	// optimistic update after DELETE /api/sessions/{id} succeeds, so
	// the dashboard doesn't briefly render a card for a session that
	// no longer exists (its preview fetch would 404 and the card
	// would flash "waiting for first snapshot…" until the next
	// poll tick reconciled). Next poll is the source of truth.
	remove(id: string): void {
		list = list.filter((s) => s.id !== id);
	},

	// add pushes a newly-created session onto the list synchronously
	// so the dashboard renders it immediately after POST /api/sessions
	// returns. Without this, the new card wouldn't appear until the
	// next poll tick, which is annoying lag right after a deliberate create.
	// Idempotent: if a session with the same id is already in the
	// list (unlikely but possible if the poll happens to land between
	// the POST resolving and us calling add), we leave it alone.
	add(session: Session): void {
		if (list.some((s) => s.id === session.id)) return;
		list = [...list, session];
	},

	// patch optimistically merges fields into a session already in the
	// list, used by card quick actions (stop/start) so the status
	// indicator flips immediately instead of lagging until the next poll
	// tick. The next poll is still the source of truth and reconciles.
	patch(id: string, fields: Partial<Session>): void {
		list = list.map((s) => (s.id === id ? { ...s, ...fields } : s));
	},
};
