-- deepcove:no-transaction
--
-- One login mode per session replaces the LLM proxy and sharing switches. Sessions with no sandbox keep
-- their machine's login, the rest share.
--
-- DuckDB will not commit a transaction that adds a column and then writes it, so this runs without one.
-- Both statements survive a re-run.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS login_mode TEXT;
UPDATE sessions SET login_mode = CASE
	WHEN use_llm_proxy THEN 'llm_proxy'
	WHEN runtime = 'sandboxless' THEN 'in_session'
	ELSE 'shared'
END
WHERE login_mode IS NULL;
