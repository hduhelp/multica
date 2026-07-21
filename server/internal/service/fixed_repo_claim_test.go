package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fixedRepoFixture provisions a local runtime and a fixed-repo agent with two
// configured paths and a high concurrency cap, plus `taskCount` queued tasks.
// Returns the agent id and the ordered task ids. Path existence is not checked
// server-side (the daemon validates locally), so arbitrary paths are fine here.
func fixedRepoFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskCount int) (string, []string) {
	t.Helper()
	suffix := time.Now().UnixNano()

	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"Fixed Repo Test", fmt.Sprintf("fixed-repo-%d@multica.ai", suffix)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var workspaceID string
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		"Fixed Repo Test", fmt.Sprintf("fixed-repo-%d", suffix), "fixed repo lock test", "FRL").Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		workspaceID, userID); err != nil {
		t.Fatalf("create member: %v", err)
	}
	var runtimeID string
	if err := pool.QueryRow(ctx, `INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		) VALUES ($1, $2, $3, 'local', 'codex', 'online', 'test', '{}'::jsonb, now(), 'private', $4)
		RETURNING id`, workspaceID, fmt.Sprintf("fixed-repo-daemon-%d", suffix), "Fixed Repo Runtime", userID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	var agentID string
	if err := pool.QueryRow(ctx, `INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id,
			fixed_repo_enabled, fixed_repo_paths, fixed_repo_vcs_type
		) VALUES ($1, $2, '', 'local', '{}'::jsonb, $3, 'private', 5, $4,
			true, '["/data/repos/alpha","/data/repos/beta"]'::jsonb, 'git')
		RETURNING id`, workspaceID, "Fixed Repo Agent", runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM agent_fixed_repo_lock WHERE agent_id = $1`, agentID)
		pool.Exec(c, `DELETE FROM agent_task_queue WHERE agent_id = $1`, agentID)
		pool.Exec(c, `DELETE FROM issue WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM agent WHERE id = $1`, agentID)
		pool.Exec(c, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
		pool.Exec(c, `DELETE FROM member WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM "user" WHERE id = $1`, userID)
	})

	taskIDs := make([]string, 0, taskCount)
	for i := 0; i < taskCount; i++ {
		var issueID string
		if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
			VALUES ($1, $2, 'in_progress', 'none', $3, 'member', $4, $5) RETURNING id`,
			workspaceID, fmt.Sprintf("fixed repo issue %d", i+1), userID, 800000+int(suffix%1000)*10+i, i).Scan(&issueID); err != nil {
			t.Fatalf("create issue %d: %v", i+1, err)
		}
		var taskID string
		if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, issue_id, status, priority, context, runtime_id)
			VALUES ($1, $2, 'queued', 0, '{}'::jsonb, $3) RETURNING id`, agentID, issueID, runtimeID).Scan(&taskID); err != nil {
			t.Fatalf("create task %d: %v", i+1, err)
		}
		taskIDs = append(taskIDs, taskID)
	}
	return agentID, taskIDs
}

func activeLockCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_fixed_repo_lock WHERE agent_id = $1 AND released_at IS NULL`, agentID).Scan(&n); err != nil {
		t.Fatalf("count active locks: %v", err)
	}
	return n
}

// TestFixedRepoClaimAssignsDistinctPathsAndBlocks verifies the core PR2 lock
// behaviour: concurrent tasks of a fixed-repo agent get distinct paths, a task
// is refused (no-capacity) once every path is held even though the concurrency
// cap has room, and completing a task frees its path for reuse.
func TestFixedRepoClaimAssignsDistinctPathsAndBlocks(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	agentID, _ := fixedRepoFixture(t, ctx, pool, 3)
	agentUUID := util.MustParseUUID(agentID)

	// Two paths configured → first two claims succeed with distinct paths.
	first, err := svc.ClaimTask(ctx, agentUUID)
	if err != nil || first == nil {
		t.Fatalf("first claim: task=%v err=%v", first, err)
	}
	second, err := svc.ClaimTask(ctx, agentUUID)
	if err != nil || second == nil {
		t.Fatalf("second claim: task=%v err=%v", second, err)
	}
	lock1, err := queries.GetActiveFixedRepoLockByTask(ctx, first.ID)
	if err != nil {
		t.Fatalf("lock for first task: %v", err)
	}
	lock2, err := queries.GetActiveFixedRepoLockByTask(ctx, second.ID)
	if err != nil {
		t.Fatalf("lock for second task: %v", err)
	}
	if lock1.Path == lock2.Path {
		t.Fatalf("expected distinct paths, both got %q", lock1.Path)
	}
	if got := activeLockCount(t, ctx, pool, agentID); got != 2 {
		t.Fatalf("active locks = %d, want 2", got)
	}

	// Third claim: concurrency cap (5) has room, but both paths are locked, so
	// the claim must return no task (rolled back) rather than dispatch.
	third, err := svc.ClaimTask(ctx, agentUUID)
	if err != nil {
		t.Fatalf("third claim errored: %v", err)
	}
	if third != nil {
		t.Fatalf("expected nil (no free path), got task %s", util.UUIDToString(third.ID))
	}
	if got := activeLockCount(t, ctx, pool, agentID); got != 2 {
		t.Fatalf("active locks after blocked claim = %d, want 2", got)
	}

	// Completing the first task releases its path; the next claim reuses it.
	// The daemon moves a task dispatched→running before completing; mirror that
	// so CompleteAgentTask's running-state guard matches.
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("mark first task running: %v", err)
	}
	if _, err := svc.CompleteTask(ctx, first.ID, []byte(`{}`), "", "/data/repos/alpha"); err != nil {
		t.Fatalf("complete first: %v", err)
	}
	if got := activeLockCount(t, ctx, pool, agentID); got != 1 {
		t.Fatalf("active locks after complete = %d, want 1", got)
	}
	reclaimed, err := svc.ClaimTask(ctx, agentUUID)
	if err != nil || reclaimed == nil {
		t.Fatalf("reclaim after release: task=%v err=%v", reclaimed, err)
	}
	reLock, err := queries.GetActiveFixedRepoLockByTask(ctx, reclaimed.ID)
	if err != nil {
		t.Fatalf("lock for reclaimed task: %v", err)
	}
	if reLock.Path != lock1.Path {
		t.Fatalf("reclaimed path = %q, want freed path %q", reLock.Path, lock1.Path)
	}
}

// TestFixedRepoLockReleasedOnFailAndCancel verifies the non-complete terminal
// transitions also free the path lock, so a failed or cancelled task never
// wedges its directory.
func TestFixedRepoLockReleasedOnFailAndCancel(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	agentID, _ := fixedRepoFixture(t, ctx, pool, 2)
	agentUUID := util.MustParseUUID(agentID)

	first, err := svc.ClaimTask(ctx, agentUUID)
	if err != nil || first == nil {
		t.Fatalf("first claim: task=%v err=%v", first, err)
	}
	second, err := svc.ClaimTask(ctx, agentUUID)
	if err != nil || second == nil {
		t.Fatalf("second claim: task=%v err=%v", second, err)
	}
	if got := activeLockCount(t, ctx, pool, agentID); got != 2 {
		t.Fatalf("active locks after 2 claims = %d, want 2", got)
	}

	// Fail the first task (running → failed) — lock released in the fail tx.
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("mark first running: %v", err)
	}
	if _, err := svc.FailTask(ctx, first.ID, "boom", "", "/data/repos/alpha", "agent_error"); err != nil {
		t.Fatalf("fail first: %v", err)
	}
	if got := activeLockCount(t, ctx, pool, agentID); got != 1 {
		t.Fatalf("active locks after fail = %d, want 1", got)
	}

	// Cancel the second task (dispatched → cancelled) — lock released via the
	// non-transactional cancel path.
	if _, err := svc.CancelTask(ctx, second.ID); err != nil {
		t.Fatalf("cancel second: %v", err)
	}
	if got := activeLockCount(t, ctx, pool, agentID); got != 0 {
		t.Fatalf("active locks after cancel = %d, want 0", got)
	}
}

// fixedRepoWorktreeFixture provisions a worktree-mode fixed-repo agent (git,
// single base path, high concurrency) plus `taskCount` issue-bound queued tasks.
func fixedRepoWorktreeFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskCount int) (string, []string) {
	t.Helper()
	suffix := time.Now().UnixNano()

	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"Worktree Test", fmt.Sprintf("worktree-%d@multica.ai", suffix)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var workspaceID string
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		"Worktree Test", fmt.Sprintf("worktree-%d", suffix), "worktree claim test", "WKT").Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		workspaceID, userID); err != nil {
		t.Fatalf("create member: %v", err)
	}
	var runtimeID string
	if err := pool.QueryRow(ctx, `INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		) VALUES ($1, $2, $3, 'local', 'codex', 'online', 'test', '{}'::jsonb, now(), 'private', $4)
		RETURNING id`, workspaceID, fmt.Sprintf("worktree-daemon-%d", suffix), "Worktree Runtime", userID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	var agentID string
	if err := pool.QueryRow(ctx, `INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id,
			fixed_repo_enabled, fixed_repo_paths, fixed_repo_vcs_type, fixed_repo_worktree
		) VALUES ($1, $2, '', 'local', '{}'::jsonb, $3, 'private', 5, $4,
			true, '["/data/repos/base"]'::jsonb, 'git', true)
		RETURNING id`, workspaceID, "Worktree Agent", runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM agent_fixed_repo_lock WHERE agent_id = $1`, agentID)
		pool.Exec(c, `DELETE FROM agent_task_queue WHERE agent_id = $1`, agentID)
		pool.Exec(c, `DELETE FROM issue WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM agent WHERE id = $1`, agentID)
		pool.Exec(c, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
		pool.Exec(c, `DELETE FROM member WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM "user" WHERE id = $1`, userID)
	})

	taskIDs := make([]string, 0, taskCount)
	for i := 0; i < taskCount; i++ {
		var issueID string
		if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
			VALUES ($1, $2, 'in_progress', 'none', $3, 'member', $4, $5) RETURNING id`,
			workspaceID, fmt.Sprintf("worktree issue %d", i+1), userID, 900000+int(suffix%1000)*10+i, i).Scan(&issueID); err != nil {
			t.Fatalf("create issue %d: %v", i+1, err)
		}
		var taskID string
		if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, issue_id, status, priority, context, runtime_id)
			VALUES ($1, $2, 'queued', 0, '{}'::jsonb, $3) RETURNING id`, agentID, issueID, runtimeID).Scan(&taskID); err != nil {
			t.Fatalf("create task %d: %v", i+1, err)
		}
		taskIDs = append(taskIDs, taskID)
	}
	return agentID, taskIDs
}

// TestFixedRepoWorktreeClaimSkipsLockAndParallelizes verifies the crux of
// worktree mode: a git-backed worktree agent's issue-bound tasks are dispatched
// concurrently WITHOUT taking any fixed-repo path lock, so parallelism is bounded
// only by max_concurrent_tasks — unlike the single-path in_place mode that blocks
// once the path is held. No server-side lock also means a daemon restart has
// nothing to leak or unwedge.
func TestFixedRepoWorktreeClaimSkipsLockAndParallelizes(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	agentID, _ := fixedRepoWorktreeFixture(t, ctx, pool, 3)
	agentUUID := util.MustParseUUID(agentID)

	// Only ONE base path is configured, yet three issue-bound tasks all claim
	// successfully — the in_place mode would refuse the 2nd/3rd (no free path).
	for i := 0; i < 3; i++ {
		claimed, err := svc.ClaimTask(ctx, agentUUID)
		if err != nil {
			t.Fatalf("claim %d errored: %v", i+1, err)
		}
		if claimed == nil {
			t.Fatalf("claim %d returned no task: worktree mode must not gate on the single base path", i+1)
		}
	}

	// Not a single fixed-repo lock row was created — worktree isolation is the
	// daemon's job; the server holds no durable lock for these tasks.
	if got := activeLockCount(t, ctx, pool, agentID); got != 0 {
		t.Fatalf("active fixed-repo locks = %d, want 0 (worktree mode skips the lock)", got)
	}
}
