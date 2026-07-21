-- Worktree mode for fixed-repo agents (方案3): when enabled, each issue-bound
-- task runs in an ephemeral git worktree branched off the configured base repo
-- instead of serializing on the single fixed_repo_path, so the agent's
-- concurrency actually parallelizes. Requires fixed_repo_vcs_type = 'git'
-- (enforced in application code) — no CHECK here because the column pair spans
-- two independently-updatable fields and a table constraint would reject
-- otherwise-valid intermediate states during a two-step config edit.
ALTER TABLE agent
    ADD COLUMN fixed_repo_worktree BOOLEAN NOT NULL DEFAULT FALSE;
