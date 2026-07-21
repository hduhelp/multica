-- Fixed repo path locks (fixed-repo-mode design PR2). Relationships are
-- enforced in application code; there are no foreign keys. released_at IS NULL
-- marks a live lock; the partial unique indexes (migrations 204/205) guarantee
-- one active lock per (agent, path) and per task.

-- AcquireFixedRepoLock atomically selects the agent's first configured
-- fixed_repo_path that has no active lock (array order = priority) and inserts
-- a lock for the task. If every configured path is already held, no row is
-- inserted and the query returns no rows, which the caller treats as "this
-- agent has no free fixed repo capacity right now". ON CONFLICT DO NOTHING
-- makes concurrent claims for the same path safe: the loser simply returns no
-- rows instead of erroring.
-- name: AcquireFixedRepoLock :one
WITH candidate AS (
    SELECT elem.path
    FROM agent a,
         jsonb_array_elements_text(a.fixed_repo_paths) WITH ORDINALITY AS elem(path, ord)
    WHERE a.id = @agent_id
      AND NOT EXISTS (
          SELECT 1
          FROM agent_fixed_repo_lock existing
          WHERE existing.agent_id = a.id
            AND existing.path = elem.path
            AND existing.released_at IS NULL
      )
    ORDER BY elem.ord
    LIMIT 1
)
INSERT INTO agent_fixed_repo_lock (workspace_id, agent_id, path, task_id, runtime_id)
SELECT @workspace_id, @agent_id, candidate.path, @task_id, @runtime_id
FROM candidate
ON CONFLICT (agent_id, path) WHERE released_at IS NULL DO NOTHING
RETURNING *;

-- GetActiveFixedRepoLockByTask returns the live lock a task already holds, used
-- by duplicate-claim recovery to reuse the existing path instead of locking a
-- second one.
-- name: GetActiveFixedRepoLockByTask :one
SELECT * FROM agent_fixed_repo_lock
WHERE task_id = @task_id AND released_at IS NULL;

-- ReleaseFixedRepoLockByTask releases whatever active lock a task holds. Called
-- from the task terminal-state transition layer (complete/fail/cancel/sweep).
-- Idempotent: releasing an already-released or nonexistent lock is a no-op.
-- name: ReleaseFixedRepoLockByTask :exec
UPDATE agent_fixed_repo_lock
SET released_at = now()
WHERE task_id = @task_id AND released_at IS NULL;

-- ListActiveFixedRepoLocksByAgent lists an agent's live locks (observability and
-- validation, e.g. surfacing which paths are busy).
-- name: ListActiveFixedRepoLocksByAgent :many
SELECT * FROM agent_fixed_repo_lock
WHERE agent_id = @agent_id AND released_at IS NULL
ORDER BY locked_at ASC;

-- ReleaseFixedRepoLocksByAgent releases every live lock for an agent. Called
-- when an agent is archived/deleted or its fixed repo config is disabled, so
-- stale locks never wedge future claims.
-- name: ReleaseFixedRepoLocksByAgent :exec
UPDATE agent_fixed_repo_lock
SET released_at = now()
WHERE agent_id = @agent_id AND released_at IS NULL;

-- ReleaseFixedRepoLocksByRuntime releases every live lock held on behalf of a
-- runtime. Called during runtime removal/orphan recovery so a vanished daemon
-- does not leave paths permanently locked.
-- name: ReleaseFixedRepoLocksByRuntime :exec
UPDATE agent_fixed_repo_lock
SET released_at = now()
WHERE runtime_id = @runtime_id AND released_at IS NULL;
