-- The workspace VM: one machine on this node that several sessions share, instead of a VM each.
--
-- No id column: the table holds exactly one row, so reads take the first and writes replace it. Nothing
-- to key on means no magic row number scattered through the queries.
CREATE TABLE IF NOT EXISTS workspace_vms (
    host_root  TEXT NOT NULL,
    image      TEXT NOT NULL,
    runtime    TEXT NOT NULL,
    resources  TEXT,
    sandbox_id TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Which sessions live on it, and the folder each one works in. Nullable for the same DuckDB ALTER
-- reason as earlier columns: older rows read back as false and empty, which is what they are.
ALTER TABLE sessions ADD COLUMN on_workspace_vm BOOLEAN;
ALTER TABLE sessions ADD COLUMN work_dir TEXT;
