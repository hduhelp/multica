-- Fixed repo path locks (PR2 of the fixed-repo-mode design,
-- docs/superpowers/specs/2026-05-24-fixed-repo-mode-design.md).
--
-- When an agent runs in fixed repo mode, each claimed task must take exclusive
-- ownership of one of the agent's configured fixed_repo_paths so two tasks
-- never execute in the same directory concurrently. The lock is persisted
-- server-side because the claim decision (and max_concurrent_tasks) is made on
-- the server; daemon restarts, duplicate claims, and task fail/complete all
-- release the lock through the same lifecycle.
--
-- No foreign keys: like every other new table in this codebase, relationships
-- (agent_id, task_id, runtime_id, workspace_id) are enforced in the
-- application layer. Terminal task transitions release the active lock inside
-- the same transaction that finalizes the task; agent/runtime deletion cleanup
-- prunes locks explicitly. released_at IS NULL marks an active lock.
--
-- The two active-lock uniqueness guarantees (one active lock per (agent, path)
-- and one active lock per task) are enforced by partial unique indexes built
-- CONCURRENTLY in follow-up single-statement migrations 204 and 205.
CREATE TABLE agent_fixed_repo_lock (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id     UUID NOT NULL,
    path         TEXT NOT NULL,
    task_id      UUID NOT NULL,
    runtime_id   UUID NOT NULL,
    locked_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at  TIMESTAMPTZ
);
