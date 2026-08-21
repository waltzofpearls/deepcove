-- Whether this session's agent talks to the LLM proxy. Per session, not per machine: several sessions
-- share the workspace VM and each may want a different answer. Off unless asked for.
ALTER TABLE sessions ADD COLUMN use_llm_proxy BOOLEAN DEFAULT FALSE;
