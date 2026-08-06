import { describe, it, expect } from 'vitest';
import { clusterBadgeKind, nodeClusterLabel, DEFAULT_CLUSTER, UNKNOWN_CLUSTER } from './cluster';

describe('clusterBadgeKind', () => {
  const cases: Array<[string, ReturnType<typeof clusterBadgeKind>]> = [
    ['dev', 'named'],
    ['prod', 'named'],
    [DEFAULT_CLUSTER, 'none'],
    ['', 'none'],
    [UNKNOWN_CLUSTER, 'unknown'],
  ];
  it.each(cases)('%s -> %s', (cluster, want) => {
    expect(clusterBadgeKind(cluster)).toBe(want);
  });
});

describe('nodeClusterLabel', () => {
  it('returns the mapped cluster when the node is present', () => {
    expect(nodeClusterLabel({ n1: 'dev' }, 'n1')).toBe('dev');
  });
  it('returns the default bucket when reported explicitly (not unknown)', () => {
    expect(nodeClusterLabel({ n1: DEFAULT_CLUSTER }, 'n1')).toBe(DEFAULT_CLUSTER);
  });
  it('returns unknown when the node is absent (offline, never observed)', () => {
    expect(nodeClusterLabel({}, 'n1')).toBe(UNKNOWN_CLUSTER);
  });
});
