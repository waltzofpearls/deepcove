import { describe, it, expect } from 'vitest';
import {
  hostForSession,
  isRemoteSession,
  sessionHostLabel,
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
    runtime: 'applevf',
    resources: {},
    image: '',
    owner_email: '',
    status: 'running',
    env: null,
    mounts: null,
    setup_script: '',
    login_mode: 'shared',
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
  const s = (id: string, activity: string): Session => ({
    ...sess(),
    id,
    last_activity_at: activity,
  });
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

  it('orders by parsed instant, not raw string (survives timezone offsets, matching Go)', () => {
    const out = mergeAggregated(
      [
        { node: node('a'), sessions: [s('a1', '2026-06-01T09:00:00+02:00')] }, // = 07:00 UTC
        { node: node('b'), sessions: [s('b1', '2026-06-01T08:00:00Z')] }, // 08:00 UTC, more recent
      ],
      browser,
    );
    // Instant order: b1 (08:00 UTC) is newer than a1 (07:00 UTC). A raw-string localeCompare would
    // wrongly put a1 first because "09" > "08".
    expect(out.sessions.map((x) => x.id)).toEqual(['b1', 'a1']);
  });

  it('omits self/unreachable when empty', () => {
    const out = mergeAggregated([{ node: node('a'), sessions: [] }], null);
    expect(out.sessions).toEqual([]);
    expect(out.self).toBeUndefined();
    expect(out.unreachable).toBeUndefined();
  });
});

describe('sessionHostLabel', () => {
  const cases: Array<{
    name: string;
    host: SessionHost | undefined;
    self: SessionHost | null;
    want: string | undefined;
  }> = [
    { name: 'undefined for an untagged session', host: undefined, self, want: undefined },
    { name: 'undefined for a self-hosted (local) session', host: self, self, want: undefined },
    {
      name: 'undefined on a single-node setup (no self)',
      host: { stable_node_id: 'n-peer', hostname: 'peer' },
      self: null,
      want: undefined,
    },
    {
      name: 'first label of the host MagicDNS name (keeping any dedup suffix)',
      host: {
        stable_node_id: 'n-peer',
        hostname: 'deepcove-erebor',
        dns_name: 'deepcove-erebor-1.tail-scale.ts.net',
      },
      self,
      want: 'deepcove-erebor-1',
    },
    {
      name: 'falls back to hostname when the host has no MagicDNS name',
      host: { stable_node_id: 'n-peer', hostname: 'deepcove-peer' },
      self,
      want: 'deepcove-peer',
    },
  ];
  it.each(cases)('$name', ({ host, self: s, want }) => {
    expect(sessionHostLabel(sess(host), s)).toBe(want);
  });
});

describe('hostForSession', () => {
  const peer: SessionHost = {
    stable_node_id: 'n-peer',
    hostname: 'deepcove-peer',
    dns_name: 'peer.ts',
  };
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
