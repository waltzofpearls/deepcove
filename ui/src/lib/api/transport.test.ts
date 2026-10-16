import { afterEach, describe, expect, it, vi } from 'vitest';

// transport.ts picks its mode once at module load, so each case re-imports the
// module fresh after stubbing the injected global + the location protocol.
async function loadTransport(injected?: { transport?: string; target?: string }) {
  vi.resetModules();
  if (injected) (globalThis as { __deepcove?: unknown }).__deepcove = injected;
  else delete (globalThis as { __deepcove?: unknown }).__deepcove;
  return import('./transport');
}

afterEach(() => {
  delete (globalThis as { __deepcove?: unknown }).__deepcove;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('transport mode selection', () => {
  const cases: Array<{
    name: string;
    injected?: { transport?: string; target?: string };
    want: string;
  }> = [
    { name: 'no injected config -> direct', injected: undefined, want: 'direct' },
    { name: 'explicit direct', injected: { transport: 'direct' }, want: 'direct' },
    { name: 'unknown value -> direct', injected: { transport: 'nonsense' }, want: 'direct' },
    // wasm selects WasmTransport (target provided so its constructor doesn't
    // reach for location in the node test env).
    {
      name: 'wasm selects WasmTransport',
      injected: { transport: 'wasm', target: 'https://node.example.ts.net' },
      want: 'wasm',
    },
  ];

  for (const tc of cases) {
    it(tc.name, async () => {
      const { transport } = await loadTransport(tc.injected);
      expect(transport.mode).toBe(tc.want);
    });
  }
});

describe('transportForHost', () => {
  it('uses the boot transport for a local session (no host)', async () => {
    const { transport, transportForHost } = await loadTransport();
    expect(transportForHost()).toBe(transport);
    expect(transportForHost(null)).toBe(transport);
    expect(transportForHost({})).toBe(transport);
    expect(transportForHost({ dns_name: '' })).toBe(transport);
  });

  it('keeps a remote session on this node in Direct mode, which forwards to the owner', async () => {
    const { transport, transportForHost } = await loadTransport();
    expect(transport.mode).toBe('direct');
    const t = transportForHost({ dns_name: 'deepcove-peer.tailnet.ts.net', stable_node_id: 'n2' });
    expect(t).not.toBe(transport);
    expect(t.mode).toBe('direct');
  });

  it('names the target node in a header, so this node knows who to forward to', async () => {
    const { transportForHost } = await loadTransport();
    const seen: Array<Headers> = [];
    vi.stubGlobal('fetch', async (_url: string, init?: RequestInit) => {
      seen.push(new Headers(init?.headers));
      return new Response('{}', { status: 200 });
    });
    const t = transportForHost({ dns_name: 'peer.ts.net', stable_node_id: 'n2' });
    await t.fetch('/api/preflight');
    expect(seen[0].get('Deepcove-Node')).toBe('n2');
  });

  it('dials the owning peer itself on a web-only node, which has nothing to forward', async () => {
    const { transport, transportForHost } = await loadTransport({
      transport: 'wasm',
      target: 'http://deepcove-a.tailnet.ts.net',
    });
    const t = transportForHost({ dns_name: 'deepcove-peer.tailnet.ts.net' });
    expect(t).not.toBe(transport);
    expect(t.mode).toBe('wasm');
  });

  it('caches one transport per host, keyed by MagicDNS name', async () => {
    // Only a web-only node builds per-host transports; a session node forwards instead.
    const { transportForHost } = await loadTransport({
      transport: 'wasm',
      target: 'http://deepcove-a.tailnet.ts.net',
    });
    const a1 = transportForHost({ dns_name: 'deepcove-a.tailnet.ts.net' });
    const a2 = transportForHost({ dns_name: 'deepcove-a.tailnet.ts.net' });
    const b = transportForHost({ dns_name: 'deepcove-b.tailnet.ts.net' });
    expect(a1).toBe(a2);
    expect(a1).not.toBe(b);
  });
});

describe('DirectTransport.openSocket', () => {
  it('builds ws:// from an http page and passes the subprotocol', async () => {
    vi.stubGlobal('location', { protocol: 'http:', host: 'localhost:8080' });
    const sockets: Array<{ url: string; protocols?: string | string[] }> = [];
    vi.stubGlobal(
      'WebSocket',
      class {
        constructor(url: string, protocols?: string | string[]) {
          sockets.push({ url, protocols });
        }
      },
    );
    const { transport } = await loadTransport();
    transport.openSocket('/api/sessions/s1/attach', 'deepcove.pty.v1');
    expect(sockets).toHaveLength(1);
    expect(sockets[0].url).toBe('ws://localhost:8080/api/sessions/s1/attach');
    expect(sockets[0].protocols).toBe('deepcove.pty.v1');
  });

  it('uses wss:// from an https page', async () => {
    vi.stubGlobal('location', { protocol: 'https:', host: 'deepcove.example.ts.net' });
    const sockets: Array<{ url: string }> = [];
    vi.stubGlobal(
      'WebSocket',
      class {
        constructor(url: string) {
          sockets.push({ url });
        }
      },
    );
    const { transport } = await loadTransport();
    transport.openSocket('/api/x');
    expect(sockets[0].url).toBe('wss://deepcove.example.ts.net/api/x');
  });
});

describe('waitWhileNodeAnswers', () => {
  const never = () => new Promise<string>(() => {});
  const cases: Array<{
    name: string;
    answer: () => Promise<string>;
    // checks is what each node check answers, in order.
    checks: boolean[];
    abortAfterMs?: number;
    waitMs: number;
    want: { value?: string; error?: string };
  }> = [
    {
      name: 'passes the answer through',
      answer: async () => 'ok',
      checks: [],
      waitMs: 0,
      want: { value: 'ok' },
    },
    {
      name: 'fails once the node misses two checks in a row',
      answer: never,
      checks: [false, false],
      waitMs: 10000,
      want: { error: 'droplet stopped answering' },
    },
    {
      name: 'keeps waiting through one missed check',
      answer: () => new Promise((resolve) => setTimeout(() => resolve('late'), 16000)),
      checks: [false, true, false],
      waitMs: 16000,
      want: { value: 'late' },
    },
    {
      name: 'stops waiting when the caller aborts',
      answer: never,
      checks: [],
      abortAfterMs: 100,
      waitMs: 100,
      want: { error: 'The request was aborted.' },
    },
  ];

  for (const tc of cases) {
    it(tc.name, async () => {
      vi.useFakeTimers();
      try {
        const { waitWhileNodeAnswers } = await loadTransport();
        const checks = [...tc.checks];
        const caller = new AbortController();
        if (tc.abortAfterMs !== undefined) setTimeout(() => caller.abort(), tc.abortAfterMs);
        const result = waitWhileNodeAnswers(tc.answer(), 'droplet', caller.signal, async () =>
          checks.length ? checks.shift()! : true,
        );
        const settled = tc.want.error
          ? expect(result).rejects.toThrow(tc.want.error)
          : expect(result).resolves.toBe(tc.want.value);
        await vi.advanceTimersByTimeAsync(tc.waitMs);
        await settled;
      } finally {
        vi.useRealTimers();
      }
    });
  }

  it('fails at once for a signal that is already aborted, and still handles a late failure', async () => {
    const { waitWhileNodeAnswers } = await loadTransport();
    const caller = new AbortController();
    caller.abort();
    const lateFailure = new Promise<string>((_, reject) =>
      setTimeout(() => reject(new Error('dial failed')), 10),
    );
    const result = waitWhileNodeAnswers(lateFailure, 'droplet', caller.signal, async () => true);
    await expect(result).rejects.toMatchObject({ name: 'AbortError' });
    // An unhandled rejection from the late failure fails this run.
    await new Promise((resolve) => setTimeout(resolve, 20));
  });
});

describe('WasmTransport.fetch', () => {
  it('never sends a call that is already aborted', async () => {
    const netFetch = vi.fn();
    vi.doMock('./wasm', () => ({ ensureTailnet: async () => ({ fetch: netFetch }) }));
    try {
      const { transport } = await loadTransport({ transport: 'wasm', target: 'http://droplet.ts' });
      const caller = new AbortController();
      caller.abort();
      await expect(
        transport.fetch('/api/llm-proxy', { signal: caller.signal }),
      ).rejects.toMatchObject({ name: 'AbortError' });
      expect(netFetch).not.toHaveBeenCalled();
    } finally {
      vi.doUnmock('./wasm');
    }
  });

  it('checks the node the call went to, even after the primary node changes', async () => {
    vi.useFakeTimers();
    const urls: string[] = [];
    const netFetch = vi.fn((url: string) => {
      urls.push(url);
      return new Promise(() => {});
    });
    vi.doMock('./wasm', () => ({ ensureTailnet: async () => ({ fetch: netFetch }) }));
    try {
      const { transport, setDiscoveredTarget } = await loadTransport({
        transport: 'wasm',
        target: 'http://old.ts',
      });
      void transport.fetch('/api/sessions').catch(() => {});
      await vi.advanceTimersByTimeAsync(0);
      setDiscoveredTarget('new.ts');
      await vi.advanceTimersByTimeAsync(5000);
      expect(urls).toEqual(['http://old.ts/api/sessions', 'http://old.ts/api/health']);
    } finally {
      vi.doUnmock('./wasm');
      vi.useRealTimers();
    }
  });
});
