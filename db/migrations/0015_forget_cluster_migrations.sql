-- 0005 and 0006 created the node_clusters table, which 0014 dropped. Both files are gone, so these rows
-- name migrations that no longer exist.
DELETE FROM schema_migrations WHERE version IN ('0005_node_clusters.sql', '0006_node_clusters_observed_at.sql');
