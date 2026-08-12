-- deepcove:no-transaction
--
-- container_id predates the runtimes work, when a session meant a Docker container. It holds a sandbox
-- id whatever the runtime made it, and Docker is on its way out, so the name goes with it.
--
-- The indexes go first: DuckDB refuses to alter a table anything depends on, and an index counts. That
-- pair cannot run in a transaction, which is what the directive above is for. Every statement is
-- written to survive a re-run, since a failure here leaves the file unrecorded.
DROP INDEX IF EXISTS sessions_owner_idx;
DROP INDEX IF EXISTS sessions_status_idx;
DROP INDEX IF EXISTS sessions_activity_idx;

ALTER TABLE sessions RENAME COLUMN container_id TO sandbox_id;

CREATE INDEX IF NOT EXISTS sessions_owner_idx    ON sessions(owner_email);
CREATE INDEX IF NOT EXISTS sessions_status_idx   ON sessions(status);
CREATE INDEX IF NOT EXISTS sessions_activity_idx ON sessions(last_activity_at DESC);
