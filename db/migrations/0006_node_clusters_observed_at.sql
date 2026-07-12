-- observed_at records when a peer's cluster was last observed, so cross-node gossip can pick the
-- freshest evidence when nodes disagree. Existing rows have no timestamp (read as the oldest).
ALTER TABLE node_clusters ADD COLUMN observed_at TIMESTAMP;
