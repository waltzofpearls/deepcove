-- The setup script belongs to the machine: it installs the agent and whatever else the image lacks, so
-- a session joining a workspace VM has none of its own. Nullable, meaning the agent's default.
ALTER TABLE workspace_vms ADD COLUMN setup_script TEXT;
