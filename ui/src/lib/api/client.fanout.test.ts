import { describe, it, expect, beforeEach, vi } from 'vitest';

// Exercises the browser-side fan-out orchestration (dashboard mode): listSessionsAcrossNodes hits each
// online node's /api/sessions/local through transportForHost, tags hosts, marks failures unreachable,
// and skips offline nodes. transportForHost is mocked to return a per-node fetch keyed by dns_name so
// the test can make one node answer and another fail.
const responses = new Map<string, unknown>();
const failing = new Set<string>();

vi.mock('./transport', () => ({
	transport: { mode: 'wasm' },
	transportForHost: (host?: { dns_name?: string }) => ({
		mode: 'wasm',
		fetch: async (path: string) => {
			const dns = host?.dns_name ?? '';
			if (failing.has(dns)) throw new Error(`dial ${dns} failed`);
			const body = JSON.stringify(responses.get(dns) ?? []);
			void path;
			return new Response(body, { status: 200, headers: { 'content-type': 'application/json' } });
		},
		openSocket: () => ({}),
	}),
}));

import { api } from './client';
import type { NodeInfo } from './types';

const node = (id: string, online = true): NodeInfo => ({
	stable_node_id: id,
	hostname: id,
	dns_name: `${id}.ts`,
	online,
	self: false,
});

beforeEach(() => {
	responses.clear();
	failing.clear();
});

describe('listSessionsAcrossNodes', () => {
	it('tags hosts, marks a failed node unreachable, and skips offline nodes', async () => {
		responses.set('a.ts', [{ id: 'a1', last_activity_at: '2026-06-01T10:00:00Z' }]);
		failing.add('b.ts'); // online but errors -> unreachable
		const self = { stable_node_id: 'web', hostname: 'deepcove-web' };

		const out = await api.listSessionsAcrossNodes([node('a'), node('b'), node('c', false)], self);

		expect(out.sessions.map((s) => s.id)).toEqual(['a1']);
		expect(out.sessions[0].host?.stable_node_id).toBe('a');
		// b failed -> unreachable; c is offline -> skipped entirely (not unreachable).
		expect(out.unreachable?.map((u) => u.stable_node_id)).toEqual(['b']);
		expect(out.self).toEqual(self);
	});

	it('returns an empty list (no unreachable) when every online node answers empty', async () => {
		const out = await api.listSessionsAcrossNodes([node('a'), node('b')], null);
		expect(out.sessions).toEqual([]);
		expect(out.unreachable).toBeUndefined();
		expect(out.self).toBeUndefined();
	});
});
