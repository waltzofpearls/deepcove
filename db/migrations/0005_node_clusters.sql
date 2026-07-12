-- node_clusters: last-known cluster per peer node (by stable node id), the durable backing for the
-- in-memory cache so an offline node keeps its cluster across restarts.
CREATE TABLE IF NOT EXISTS node_clusters (
    stable_node_id TEXT PRIMARY KEY,
    cluster        TEXT NOT NULL
);
