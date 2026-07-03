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
	const cases: Array<{ name: string; injected?: { transport?: string; target?: string }; want: string }> = [
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

	it('upgrades a remote host to a WASM transport even when the boot mode is Direct', async () => {
		const { transport, transportForHost } = await loadTransport();
		expect(transport.mode).toBe('direct');
		const t = transportForHost({ dns_name: 'deepcove-peer.tailnet.ts.net' });
		expect(t).not.toBe(transport);
		expect(t.mode).toBe('wasm');
	});

	it('caches one transport per host, keyed by MagicDNS name', async () => {
		const { transportForHost } = await loadTransport();
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
