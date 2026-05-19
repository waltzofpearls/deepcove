-- Adds last_activity_at to sessions. The canvas sorts cards by this
-- column (most recently touched first) — the Claude-Code-style
-- "what was I just doing?" ordering — rather than created_at, which
-- would bury an active session under a freshly-spawned idle one.
--
-- DuckDB does not currently support ADD COLUMN with a NOT NULL
-- DEFAULT constraint (Parser Error: "Adding columns with constraints
-- not yet supported"). So we add the column nullable, then backfill
-- existing rows to created_at. New rows always populate the column
-- explicitly via the Go INSERT, so NULL is only a possibility for
-- legacy rows and the Go scanner tolerates it.

ALTER TABLE sessions ADD COLUMN last_activity_at TIMESTAMP;

UPDATE sessions SET last_activity_at = created_at WHERE last_activity_at IS NULL;

CREATE INDEX IF NOT EXISTS sessions_activity_idx ON sessions(last_activity_at DESC);
