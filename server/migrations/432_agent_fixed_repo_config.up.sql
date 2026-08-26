ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS fixed_repo_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS fixed_repo_paths JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS fixed_repo_vcs_type TEXT NOT NULL DEFAULT 'git',
    ADD COLUMN IF NOT EXISTS fixed_repo_cleanup_script TEXT;

-- Postgres has no ADD CONSTRAINT IF NOT EXISTS, so drop first. Every other
-- statement in this fixed-repo set is already idempotent; these two were not,
-- and that matters because the set has been renumbered once (404-408 -> 432-436,
-- when an upstream sync claimed 404/407/408) and could be again. A renumber
-- gives the file a new stem, which is what schema_migrations keys on, so an
-- environment that already ran the old stem re-runs the SQL under the new one.
-- Dropping a CHECK only to re-add the same predicate is a no-op there and the
-- normal path on a fresh database.
ALTER TABLE agent
    DROP CONSTRAINT IF EXISTS agent_fixed_repo_paths_array_check,
    DROP CONSTRAINT IF EXISTS agent_fixed_repo_vcs_type_check;

ALTER TABLE agent
    ADD CONSTRAINT agent_fixed_repo_paths_array_check
        CHECK (jsonb_typeof(fixed_repo_paths) = 'array'),
    ADD CONSTRAINT agent_fixed_repo_vcs_type_check
        CHECK (fixed_repo_vcs_type IN ('git', 'perforce', 'none', 'custom'));
