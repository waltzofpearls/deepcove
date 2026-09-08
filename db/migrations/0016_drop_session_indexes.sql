-- These covered a table holding tens of rows, so they never paid for themselves. And a DuckDB index that
-- outlives an unclean exit can quietly answer with no rows (duckdb#23848), which hid every session.
DROP INDEX IF EXISTS sessions_owner_idx;
DROP INDEX IF EXISTS sessions_status_idx;
DROP INDEX IF EXISTS sessions_activity_idx;
DROP INDEX IF EXISTS session_grants_user_idx;
