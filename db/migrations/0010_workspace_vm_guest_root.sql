-- Where the shared folder appears inside the VM, and the root every session's folder lives under.
-- Nullable for the DuckDB ALTER reason; the store reads a NULL back as /workspace, the only place it
-- could have been.
ALTER TABLE workspace_vms ADD COLUMN guest_root TEXT;
