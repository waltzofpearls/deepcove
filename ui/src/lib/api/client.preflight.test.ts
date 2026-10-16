import { describe, it, expect, vi } from 'vitest';

// The signal the transport was handed, so a test can see whether the request was aborted.
let lastSignal: AbortSignal | undefined;
// fetchCalls counts requests the transport was asked to start.
let fetchCalls = 0;

// A WASM fetch that never answers and ignores its abort signal, like tailscale-web's.
vi.mock('./transport', () => ({
  transport: { mode: 'wasm' },
  transportForHost: () => ({
    mode: 'wasm',
    fetch: (_url: string, init?: RequestInit) => {
      fetchCalls++;
      lastSignal = init?.signal ?? undefined;
      return new Promise<Response>(() => {});
    },
    openSocket: () => ({}),
  }),
}));

import { api } from './client';

describe('preflight', () => {
  it('gives up on a node that never answers, even when the transport ignores abort', async () => {
    const host = { stable_node_id: 'n', hostname: 'droplet', dns_name: 'droplet.ts' };
    await expect(api.preflight(host, undefined, 50)).rejects.toThrow('timed out');
  });

  it('aborts the request once it gives up, so a transport that honours abort closes it', async () => {
    const host = { stable_node_id: 'n', hostname: 'droplet', dns_name: 'droplet.ts' };
    await expect(api.preflight(host, undefined, 50)).rejects.toThrow('timed out');
    expect(lastSignal?.aborted).toBe(true);
  });

  it('starts no request for a caller that already gave up', async () => {
    const host = { stable_node_id: 'n', hostname: 'droplet', dns_name: 'droplet.ts' };
    const caller = new AbortController();
    caller.abort();
    const callsBefore = fetchCalls;
    await expect(api.preflight(host, caller.signal, 50)).rejects.toMatchObject({
      name: 'AbortError',
    });
    expect(fetchCalls).toBe(callsBefore);
  });

  it("lets go of the caller's signal once it finishes", async () => {
    const host = { stable_node_id: 'n', hostname: 'droplet', dns_name: 'droplet.ts' };
    const caller = new AbortController();
    const removed = vi.spyOn(caller.signal, 'removeEventListener');
    await expect(api.preflight(host, caller.signal, 50)).rejects.toThrow('timed out');
    expect(removed).toHaveBeenCalledWith('abort', expect.any(Function));
  });
});

describe('listSessionsAcrossNodes', () => {
  it('aborts a node request that runs out of time, so its node watch stops too', async () => {
    vi.useFakeTimers();
    try {
      const droplet = {
        stable_node_id: 'n',
        hostname: 'droplet',
        dns_name: 'droplet.ts',
        online: true,
        self: false,
      };
      const list = api.listSessionsAcrossNodes([droplet], null);
      await vi.advanceTimersByTimeAsync(2000);
      await expect(list).resolves.toMatchObject({ unreachable: [{ hostname: 'droplet' }] });
      expect(lastSignal?.aborted).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});
