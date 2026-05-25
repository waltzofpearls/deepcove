-- Adds setup_script to sessions — the shell run inside the container before the agent
-- launches. Stored verbatim so the UI can render it back into the setup-script
-- textarea on future "edit setup" flows, and so EditMounts-style recreates know what
-- to re-run.
--
-- Same DuckDB ALTER limitation 0002 hit: ADD COLUMN with a NOT NULL constraint isn't
-- supported. Adds the column nullable, then backfills existing rows to '' (the "use
-- agent registry default" sentinel the Go layer already understands). New rows always
-- populate via the Go INSERT so NULL is only possible for legacy rows; the scanner
-- defaults sql.NullString → "" on read.
ALTER TABLE sessions ADD COLUMN setup_script TEXT;

UPDATE sessions SET setup_script = '' WHERE setup_script IS NULL;
