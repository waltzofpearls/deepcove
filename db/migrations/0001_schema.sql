-- One row only: this node and its owner, recorded at first run. owner_user_id is what authorization
-- checks. owner_email is for display and can be empty on a tagged node.
CREATE TABLE IF NOT EXISTS node (
    id              INTEGER PRIMARY KEY DEFAULT 1,
    stable_node_id  TEXT NOT NULL,
    owner_user_id   TEXT NOT NULL,
    owner_email     TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (id = 1)
);

-- shared_folders, env and resources hold JSON as TEXT. DuckDB's JSON type scans into a Go map, which
-- does not turn back into the same bytes.
--
-- No indexes: the table holds tens of rows, and a DuckDB index can lose its rows after an unclean exit
-- (duckdb#23848), which hid every session.
CREATE TABLE IF NOT EXISTS sessions (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    agent            TEXT NOT NULL,
    image            TEXT NOT NULL,
    owner_email      TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'created',
    stop_reason      TEXT,
    shared_folders   TEXT NOT NULL DEFAULT '[]',
    env              TEXT NOT NULL DEFAULT '{}',
    setup_script     TEXT,
    runtime          TEXT,
    resources        TEXT,
    sandbox_id       TEXT,
    on_workspace_vm  BOOLEAN,
    work_dir         TEXT,
    login_mode       TEXT,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_activity_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS session_grants (
    session_id TEXT NOT NULL,
    user_email TEXT NOT NULL,
    role       TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, user_email)
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- One row only, so there is no id to key on. Reads take the first row and writes replace it.
CREATE TABLE IF NOT EXISTS workspace_vms (
    host_root    TEXT NOT NULL,
    guest_root   TEXT,
    image        TEXT NOT NULL,
    runtime      TEXT NOT NULL,
    resources    TEXT,
    setup_script TEXT,
    sandbox_id   TEXT,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- What each node last said about each topic. The freshest row wins.
CREATE TABLE IF NOT EXISTS gossip_topic_values (
    topic          TEXT      NOT NULL,
    stable_node_id TEXT      NOT NULL,
    value          TEXT      NOT NULL,
    seen_at        TIMESTAMP NOT NULL,
    PRIMARY KEY (topic, stable_node_id)
);
