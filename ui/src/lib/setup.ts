// Tracks whether the first-run setup wizard has been completed/dismissed, in localStorage (per
// browser). The dashboard redirects a fresh install (no sessions + not done) to /setup; finishing or
// skipping the wizard marks it done so it doesn't nag. The profile menu can re-open /setup anytime.

import { browser } from '$app/environment';

const KEY = 'deepcove_setup_complete';

export function isSetupDone(): boolean {
  if (!browser) return true;
  try {
    return localStorage.getItem(KEY) === '1';
  } catch {
    return true;
  }
}

export function markSetupDone(): void {
  if (!browser) return;
  try {
    localStorage.setItem(KEY, '1');
  } catch {
    /* storage unavailable, so the wizard just won't be remembered */
  }
}

// Tracks whether the user finished the wizard WITHOUT connecting the session node, so the dashboard can
// nudge them to finish that skipped step (and where to do it). Cleared once they connect or dismiss it.
const SKIPPED_KEY = 'deepcove_connect_skipped';

export function isConnectSkipped(): boolean {
  if (!browser) return false;
  try {
    return localStorage.getItem(SKIPPED_KEY) === '1';
  } catch {
    return false;
  }
}

export function setConnectSkipped(skipped: boolean): void {
  if (!browser) return;
  try {
    if (skipped) localStorage.setItem(SKIPPED_KEY, '1');
    else localStorage.removeItem(SKIPPED_KEY);
  } catch {
    /* ignore */
  }
}
