-- Adds resources to sessions — the per session CPU, memory, and disk caps, stored as JSON.
--
-- One JSON column rather than three typed ones, matching how mounts and env are already stored: the
-- shape is read and written whole, never queried by field, and a future knob (GPU, network caps) then
-- costs no migration.
--
-- Nullable for the same DuckDB reason as 0007. A NULL, an empty string, and the literal 'null' all read
-- back as the zero Resources, which means "use the runtime's defaults" — the behaviour every session had
-- before this column existed.
ALTER TABLE sessions ADD COLUMN resources TEXT;
