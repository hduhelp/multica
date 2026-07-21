-- One active lock per task: a task holds at most one fixed repo path. Enforces
-- idempotent release and makes duplicate-claim recovery safe (the reclaim path
-- reuses the existing active lock instead of double-locking). Partial unique on
-- released_at IS NULL. Keep this as the migration's only statement: PostgreSQL
-- rejects CREATE INDEX CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_fixed_repo_lock_active_task
    ON agent_fixed_repo_lock (task_id)
    WHERE released_at IS NULL;
