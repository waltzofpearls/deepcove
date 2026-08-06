import { describe, it, expect } from 'vitest';
import type { NodeStatus, TailnetStatus } from 'tailscale-web';
import {
  discoverableNodes,
  discoverableWebNodes,
  pickReachablePrimary,
  selfHost,
} from './discovery';
import type { NodeInfo } from './types';

function node(over: Partial<NodeStatus> = {}): NodeStatus {
  return {
    id: 'id',
    hostName: 'host',
    dnsName: 'host.tail.ts.net.',
    os: 'linux',
    tailscaleIP: '100.0.0.1',
    userId: 'u1',
    online: true,
    self: false,
    ...over,
  };
}

const browserSelf = node({
  id: 'web',
  hostName: 'deepcove-web',
  dnsName: 'deepcove-web.tail.ts.net.',
  os: 'js',
  userId: 'u1',
  self: true,
});

function status(over: Partial<TailnetStatus>): TailnetStatus {
  return { self: browserSelf, peers: [], ...over };
}

describe('discoverableNodes', () => {
  it('returns [] when the node is not up (no self)', () => {
    expect(discoverableNodes(null)).toEqual([]);
    expect(discoverableNodes({ self: null, peers: [] })).toEqual([]);
  });

  it('keeps same-user deepcove peers, drops others, and sorts by hostname', () => {
    const got = discoverableNodes(
      status({
        peers: [
          node({ id: 'p1', hostName: 'deepcove-server', userId: 'u1' }),
          node({ id: 'p2', hostName: 'deepcove-cloud', userId: 'u1', online: false }),
          // dropped: not a deepcove node
          node({ id: 'p3', hostName: 'alices-phone', userId: 'u1' }),
          // dropped: a different user's deepcove node
          node({ id: 'p4', hostName: 'deepcove-bob', userId: 'u2' }),
          // dropped: another in-browser WASM client (deepcove- prefix + same user, but os js)
          node({ id: 'p5', hostName: 'deepcove-web2', userId: 'u1', os: 'js' }),
        ],
      }),
    );
    expect(got.map((n) => n.hostname)).toEqual(['deepcove-cloud', 'deepcove-server']);
  });

  it('strips the trailing dot from the MagicDNS name', () => {
    const got = discoverableNodes(
      status({ peers: [node({ hostName: 'deepcove-a', dnsName: 'deepcove-a.tail.ts.net.' })] }),
    );
    expect(got[0].dns_name).toBe('deepcove-a.tail.ts.net');
  });
});

describe('discoverableWebNodes', () => {
  it('returns [] when the node is not up (no self)', () => {
    expect(discoverableWebNodes(null)).toEqual([]);
    expect(discoverableWebNodes({ self: null, peers: [] })).toEqual([]);
  });

  it('keeps same-user online js peers, drops offline/other/non-js, sorts by hostname', () => {
    const got = discoverableWebNodes(
      status({
        peers: [
          node({ id: 'w1', hostName: 'deepcove-web-3', userId: 'u1', os: 'js', online: true }),
          node({ id: 'w2', hostName: 'deepcove-web-2', userId: 'u1', os: 'js', online: true }),
          // dropped: offline web node (ephemeral, stale)
          node({ id: 'w3', hostName: 'deepcove-web-4', userId: 'u1', os: 'js', online: false }),
          // dropped: a hosting node (not js)
          node({ id: 's1', hostName: 'deepcove-server', userId: 'u1', os: 'linux', online: true }),
          // dropped: another user's web node
          node({ id: 'w4', hostName: 'deepcove-web-bob', userId: 'u2', os: 'js', online: true }),
        ],
      }),
    );
    expect(got.map((n) => n.hostname)).toEqual(['deepcove-web-2', 'deepcove-web-3']);
  });
});

describe('pickReachablePrimary', () => {
  const mk = (over: Partial<NodeInfo>): NodeInfo => ({
    stable_node_id: 'n',
    hostname: 'n',
    dns_name: 'n.ts',
    online: true,
    self: false,
    ...over,
  });

  it('picks an online node that pings alive, skipping an unreachable one', async () => {
    const got = await pickReachablePrimary(
      [mk({ hostname: 'a', dns_name: 'a.ts' }), mk({ hostname: 'b', dns_name: 'b.ts' })],
      async (dns) => dns === 'b.ts',
    );
    expect(got?.hostname).toBe('b');
  });
  it('is null when no online node is reachable', async () => {
    expect(await pickReachablePrimary([mk({ dns_name: 'a.ts' })], async () => false)).toBeNull();
  });
  it('ignores offline nodes and empty lists', async () => {
    expect(await pickReachablePrimary([mk({ online: false })], async () => true)).toBeNull();
    expect(await pickReachablePrimary([], async () => true)).toBeNull();
  });
});

describe('selfHost', () => {
  it('maps the browser node, stripping the trailing dot', () => {
    expect(selfHost(status({}))).toEqual({
      stable_node_id: 'web',
      hostname: 'deepcove-web',
      dns_name: 'deepcove-web.tail.ts.net',
    });
  });
  it('is null without self', () => {
    expect(selfHost(null)).toBeNull();
  });
});
