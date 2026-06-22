import { describe, it, expect } from 'vitest';
import {
	hostForSession,
	isRemoteSession,
	mergeAggregated,
	onlineHostNodes,
	defaultNodeId,
} from './sessions';
import type { NodeInfo, Session, SessionHost } from './types';

const self: SessionHost = { stable_node_id: 'n-self', hostname: 'deepcove-laptop' };

function sess(host?: SessionHost): Session {
	return {
		id: 's1',
		name: 's1',
		agent: 'claude',
		image: '',
		owner_email: '',
		status: 'running',
		env: null,
		mounts: null,
		setup_script: '',
		created_at: '',
		updated_at: '',
		last_activity_at: '',
		host,
	};
}

function node(id: string, opts: Partial<NodeInfo> = {}): NodeInfo {
	return { stable_node_id: id, hostname: id, dns_name: '', online: true, self: false, ...opts };
}

describe('isRemoteSession', () => {
	it('is false for an untagged session', () => {
		expect(isRemoteSession(sess(), self)).toBe(false);
	});
	it('is false when there is no self (single-node)', () => {
		expect(isRemoteSession(sess({ stable_node_id: 'n-x', hostname: 'x' }), null)).toBe(false);
	});
	it('is false when hosted on self', () => {
		expect(isRemoteSession(sess(self), self)).toBe(false);
	});
	it('is true when hosted on another node', () => {
		expect(isRemoteSession(sess({ stable_node_id: 'n-peer', hostname: 'peer' }), self)).toBe(true);
	});
});

describe('mergeAggregated', () => {
	const node = (id: string): NodeInfo => ({
		stable_node_id: id,
		hostname: id,
		dns_name: `${id}.ts`,
		online: true,
		self: false,
	});
	const s = (id: string, activity: string): Session => ({ ...sess(), id, last_activity_at: activity });
	const browser: SessionHost = { stable_node_id: 'web', hostname: 'deepcove-web' };

	it('tags each session with its host node and merges, newest first', () => {
		const out = mergeAggregated(
			[
				{ node: node('a'), sessions: [s('a1', '2026-06-01T10:00:00Z')] },
				{ node: node('b'), sessions: [s('b1', '2026-06-01T12:00:00Z')] },
			],
			browser,
		);
		expect(out.sessions.map((x) => x.id)).toEqual(['b1', 'a1']); // b1 is more recent
		expect(out.sessions.find((x) => x.id === 'a1')?.host?.stable_node_id).toBe('a');
		expect(out.sessions.find((x) => x.id === 'b1')?.host?.dns_name).toBe('b.ts');
		expect(out.self).toEqual(browser);
		expect(out.unreachable).toBeUndefined();
	});

	it('records a node whose fetch failed (null) as unreachable without dropping the rest', () => {
		const out = mergeAggregated(
			[
				{ node: node('a'), sessions: [s('a1', '2026-06-01T10:00:00Z')] },
				{ node: node('down'), sessions: null },
			],
			browser,
		);
		expect(out.sessions.map((x) => x.id)).toEqual(['a1']);
		expect(out.unreachable?.map((u) => u.stable_node_id)).toEqual(['down']);
	});

	it('omits self/unreachable when empty', () => {
		const out = mergeAggregated([{ node: node('a'), sessions: [] }], null);
		expect(out.sessions).toEqual([]);
		expect(out.self).toBeUndefined();
		expect(out.unreachable).toBeUndefined();
	});
});

describe('hostForSession', () => {
	const peer: SessionHost = { stable_node_id: 'n-peer', hostname: 'deepcove-peer', dns_name: 'peer.ts' };
	it('returns the host for a remote session', () => {
		expect(hostForSession(sess(peer), self)).toEqual(peer);
	});
	it('returns undefined for a local (self-hosted) session', () => {
		expect(hostForSession(sess(self), self)).toBeUndefined();
	});
	it('returns undefined for an untagged session', () => {
		expect(hostForSession(sess(), self)).toBeUndefined();
	});
	it('returns undefined on a single-node setup (no self)', () => {
		expect(hostForSession(sess(peer), null)).toBeUndefined();
	});
});

describe('onlineHostNodes', () => {
	it('drops offline nodes', () => {
		const nodes = [node('a'), node('b', { online: false }), node('c')];
		expect(onlineHostNodes(nodes).map((n) => n.stable_node_id)).toEqual(['a', 'c']);
	});
});

describe('defaultNodeId', () => {
	it('prefers this machine (self)', () => {
		expect(defaultNodeId([node('a'), node('b', { self: true })])).toBe('b');
	});
	it('falls back to the first node when no self', () => {
		expect(defaultNodeId([node('a'), node('b')])).toBe('a');
	});
	it('is empty for no nodes', () => {
		expect(defaultNodeId([])).toBe('');
	});
});
