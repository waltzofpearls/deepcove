-- Drops the agent logins the old credential broker stored. Shared logins live under new keys.
DELETE FROM settings WHERE key LIKE 'agent-credentials/%';
