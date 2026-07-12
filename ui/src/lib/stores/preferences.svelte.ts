// User UI preferences, persisted to localStorage. Runes-based store
// so consumers `import { preferences } from '$lib/stores/preferences.svelte'`
// and read `preferences.viewMode` directly.
//
// We deliberately keep this small. Theme lives under mode-watcher,
// and session ordering / filters land here as the canvas grows. The
// pollIntervalMs key is forward-looking: there's no UI to change it
// in v0, but the canvas reads it so a power user can override via
// browser devtools or a future Settings page.

import { browser } from '$app/environment';

const STORAGE_KEY = 'deepcove.preferences.v1';

type ViewMode = 'cards' | 'list';

interface PreferencesState {
	viewMode: ViewMode;
	pollIntervalMs: number;
	clusterFilter: string; // 'all' or a cluster name; the dashboard's cluster narrowing, remembered
}

const DEFAULTS: PreferencesState = {
	viewMode: 'cards',
	pollIntervalMs: 5_000,
	clusterFilter: 'all',
};

function load(): PreferencesState {
	if (!browser) return DEFAULTS;
	try {
		const raw = localStorage.getItem(STORAGE_KEY);
		if (!raw) return DEFAULTS;
		return { ...DEFAULTS, ...JSON.parse(raw) };
	} catch {
		return DEFAULTS;
	}
}

function save(p: PreferencesState) {
	if (!browser) return;
	try {
		localStorage.setItem(STORAGE_KEY, JSON.stringify(p));
	} catch {
		// localStorage can be disabled (private mode etc); silently fall
		// back to in-memory state.
	}
}

const state = $state<PreferencesState>(load());

export const preferences = {
	get viewMode() {
		return state.viewMode;
	},
	set viewMode(v: ViewMode) {
		state.viewMode = v;
		save(state);
	},
	get pollIntervalMs() {
		return state.pollIntervalMs;
	},
	set pollIntervalMs(v: number) {
		state.pollIntervalMs = v;
		save(state);
	},
	get clusterFilter() {
		return state.clusterFilter;
	},
	set clusterFilter(v: string) {
		state.clusterFilter = v;
		save(state);
	},
};
