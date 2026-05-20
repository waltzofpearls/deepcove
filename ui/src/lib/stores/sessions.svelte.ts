// Singleton store of the sessions list. The dashboard, the
// sidebar's session submenu (desktop), and the topbar's tab
// carousel (mobile) all want the same polled data — three
// independent pollers would triple the request rate for the same
// information.
//
// API shape mirrors `preferences.svelte.ts`: import the store,
// read getter-backed reactive properties. Polling is lazy
// (starts on first subscriber) and ref-counted (stops when the
// last subscriber unsubscribes), so when no consumer is mounted
// the store is dormant — important for SSR and idle pages.
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
import type { Session } from '$lib/api/types';
import { usePolling } from '$lib/polling';
import { preferences } from './preferences.svelte';

let list = $state<Session[]>([]);
let loading = $state(true);
let error = $state<string | null>(null);

// Reference-counted lifecycle. `subscriberCount` tracks active
// consumers; `stopPolling` is the cleanup returned by usePolling.
let subscriberCount = 0;
let stopPolling: (() => void) | undefined;

function startIfIdle(): void {
	if (stopPolling) return;
	stopPolling = usePolling(
		async () => {
			try {
				list = await api.listSessions({ all: true });
				error = null;
			} catch (e) {
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
	stopPolling?.();
	stopPolling = undefined;
}

export const sessions = {
	get list(): Session[] {
		return list;
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

	// remove drops a session from the list synchronously — used as an
	// optimistic update after DELETE /api/sessions/{id} succeeds, so
	// the dashboard doesn't briefly render a card for a session that
	// no longer exists (its preview fetch would 404 and the card
	// would flash "waiting for first snapshot…" until the next
	// poll tick reconciled). Next poll is the source of truth.
	remove(id: string): void {
		list = list.filter((s) => s.id !== id);
	},
};
