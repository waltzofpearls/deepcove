// DEFAULT_CLUSTER is the implicit default bucket (mirrors Go DefaultCluster), a reported value.
// UNKNOWN_CLUSTER is a frontend-only marker for a node absent from the cluster map (offline, never observed).
export const DEFAULT_CLUSTER = 'default';
export const UNKNOWN_CLUSTER = 'unknown';

// clusterBadgeKind: named -> violet tag, default/empty -> nothing, unknown -> muted tag.
export type ClusterBadgeKind = 'named' | 'unknown' | 'none';
export function clusterBadgeKind(cluster: string): ClusterBadgeKind {
  if (cluster === UNKNOWN_CLUSTER) return 'unknown';
  if (!cluster || cluster === DEFAULT_CLUSTER) return 'none';
  return 'named';
}

// nodeClusterLabel resolves a node's cluster, or the unknown sentinel when absent from the map.
export function nodeClusterLabel(clusterMap: Record<string, string>, stableNodeId: string): string {
  return clusterMap[stableNodeId] ?? UNKNOWN_CLUSTER;
}
