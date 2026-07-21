-- One active lock per (agent, path): a fixed repo path can be held by at most
-- one live task at a time. Partial unique index on released_at IS NULL so
-- released rows accumulate as history without violating uniqueness. Keep this
-- as the migration's only statement: PostgreSQL rejects CREATE INDEX
-- CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_fixed_repo_lock_active_path
    ON agent_fixed_repo_lock (agent_id, path)
    WHERE released_at IS NULL;
