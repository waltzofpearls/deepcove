import { writable } from 'svelte/store';
import { transport } from './transport';

// The local listener answers only a browser that `deepcove open` unlocked. Any other API call gets a 401
// with this code, and the layout shows the locked screen instead of the app.
export const lockedCode = 'browser_locked';

const checkEveryMs = 2000;
// A deepcove that takes the call and never answers counts as down, rather than leaving a blank page.
const healthTimeoutMs = 5000;

export type LockState = 'checking' | 'locked' | 'unlocked';

export type HealthAnswer = 'locked' | 'open' | 'down';

// Over the tailnet there is no lock. Locally the app waits for the first answer, so it never starts and
// flashes errors first.
export const localLock = writable<LockState>(transport.mode === 'wasm' ? 'unlocked' : 'checking');

export async function checkLocalLock(): Promise<void> {
  localLock.set((await checkHealth()) === 'locked' ? 'locked' : 'unlocked');
}

// waitForUnlock reloads the page once `deepcove open` has set the cookie in this browser. A deepcove that
// is down or restarting keeps it waiting, rather than reloading into the browser's error page.
export function waitForUnlock(): () => void {
  const timer = setInterval(async () => {
    if ((await checkHealth()) === 'open') location.reload();
  }, checkEveryMs);
  return () => clearInterval(timer);
}

// A deepcove that is down is not a lock. The app shows its own errors for that.
export async function checkHealth(): Promise<HealthAnswer> {
  try {
    const resp = await fetch('/api/health', {
      cache: 'no-store',
      signal: AbortSignal.timeout(healthTimeoutMs),
    });
    if (resp.status === 401) return 'locked';
    return resp.ok ? 'open' : 'down';
  } catch {
    return 'down';
  }
}
