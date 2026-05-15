-- Single-row table: node and owner identity, established at first run.
-- The CHECK (id = 1) constraint enforces "exactly one node record".
--
-- stable_node_id is Tailscale's durable per-node identifier
-- (peer.StableNodeID); it survives logout/relogin while NodeID changes.
-- owner_user_id is Tailscale's UserID for the node's owner — that's
-- the authoritative identity for authorization (SPEC §5.1). owner_email
-- is convenience only (display, addressing) and may be empty if the
-- tailnet has tagged nodes without a real user.
CREATE TABLE IF NOT EXISTS node (
    id              INTEGER PRIMARY KEY DEFAULT 1,
    stable_node_id  TEXT NOT NULL,
    owner_user_id   TEXT NOT NULL,
    owner_email     TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (id = 1)
);

-- Coding-agent sessions hosted on this node.
CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    agent        TEXT NOT NULL,
    image        TEXT NOT NULL,
    owner_email  TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'created',
    -- mounts and env are JSON-shaped blobs, but stored as TEXT.
    -- We never query into them at the DB level — they're produced
    -- and consumed in Go. DuckDB's JSON type auto-decodes to a Go
    -- map on Scan, which doesn't round-trip cleanly back to bytes;
    -- TEXT gives us byte-exact storage with no scanner surprises.
    mounts       TEXT NOT NULL DEFAULT '[]',
    env          TEXT NOT NULL DEFAULT '{}',
    container_id TEXT,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS sessions_owner_idx  ON sessions(owner_email);
CREATE INDEX IF NOT EXISTS sessions_status_idx ON sessions(status);

-- Per-session sharing grants (SPEC.md §6.4). One row per (session, user).
CREATE TABLE IF NOT EXISTS session_grants (
    session_id TEXT NOT NULL,
    user_email TEXT NOT NULL,
    role       TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, user_email)
);
CREATE INDEX IF NOT EXISTS session_grants_user_idx ON session_grants(user_email);
