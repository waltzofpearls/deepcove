-- Why deepcove stopped a session on its own, so the session page can say so. Empty when you stopped it.
ALTER TABLE sessions ADD COLUMN stop_reason TEXT;
