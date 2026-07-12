-- Node-level key/value settings. Kept separate from the `node` table (which only exists once the node
-- is claimed/owned) so a setting like the cluster label works even on a lone, un-connected rung-1 node.
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
