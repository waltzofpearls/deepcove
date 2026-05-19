// Polling helper. Kicks off an async tick + setTimeout loop and
// returns a cleanup function — designed to be returned from a Svelte
// $effect so cleanup is automatic on component teardown.
//
// Why not setInterval: setInterval queues callbacks at fixed delay
// regardless of whether the previous one finished. A slow API call
// would let ticks pile up. This version awaits the previous tick
// before scheduling the next, so an unhealthy endpoint just slows
// the poll cadence rather than building a backlog.

export function usePolling(
	tick: () => Promise<void>,
	intervalMs: () => number
): () => void {
	let cancelled = false;
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function run() {
		if (cancelled) return;
		try {
			await tick();
		} finally {
			if (!cancelled) {
				timer = setTimeout(run, intervalMs());
			}
		}
	}

	run();

	return () => {
		cancelled = true;
		if (timer) clearTimeout(timer);
	};
}
