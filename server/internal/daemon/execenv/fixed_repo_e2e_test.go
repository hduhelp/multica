package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixedRepoPrepareRunsInUserDirectoryAndProtectsIt is an end-to-end check of
// the fixed-repo execution environment against a real on-disk working
// directory: Prepare must run the agent inside the user's existing directory
// (not a fresh worktree), write the brief there with the Fixed Repo guidance,
// and Cleanup must never delete the user's directory or its contents.
func TestFixedRepoPrepareRunsInUserDirectoryAndProtectsIt(t *testing.T) {
	t.Parallel()

	// Simulate a user's pre-existing repo/working directory with real content.
	fixedRepo := t.TempDir()
	sentinel := filepath.Join(fixedRepo, "IMPORTANT_USER_FILE.txt")
	if err := os.WriteFile(sentinel, []byte("do not delete me"), 0o644); err != nil {
		t.Fatalf("seed fixed repo: %v", err)
	}
	workspacesRoot := t.TempDir()

	env, err := Prepare(PrepareParams{
		WorkspacesRoot: workspacesRoot,
		WorkspaceID:    "ws-fixed-001",
		TaskID:         "c0ffee00-dead-beef-cafe-000000000001",
		AgentName:      "Fixed Repo Agent",
		// LocalWorkDir redirects the agent's cwd to the pre-existing directory —
		// the same slot the daemon fills from the server-locked fixed_repo_path.
		LocalWorkDir: fixedRepo,
		Task: TaskContextForEnv{
			IssueID:          "c0ffee00-dead-beef-cafe-000000000001",
			FixedRepoMode:    true,
			FixedRepoPath:    fixedRepo,
			FixedRepoVcsType: "git",
		},
	}, testLogger())
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	// The agent runs IN the user's directory, not under the daemon RootDir.
	if env.WorkDir != fixedRepo {
		t.Fatalf("WorkDir = %q, want fixed repo %q", env.WorkDir, fixedRepo)
	}
	if env.RootDir == fixedRepo || strings.HasPrefix(fixedRepo, env.RootDir) {
		t.Fatalf("RootDir %q must be separate from the fixed repo %q", env.RootDir, fixedRepo)
	}
	// LocalDirectory drives the GC override that prevents deletion of WorkDir.
	if !env.LocalDirectory {
		t.Fatal("expected env.LocalDirectory=true so GC never deletes the user directory")
	}

	// The brief is written inside the user's directory and carries the Fixed
	// Repo guidance (path + checkout ban).
	brief, err := os.ReadFile(filepath.Join(fixedRepo, ".agent_context", "issue_context.md"))
	if err != nil {
		t.Fatalf("read brief in fixed repo: %v", err)
	}
	for _, want := range []string{"## Fixed Repo", fixedRepo, "multica repo checkout"} {
		if !strings.Contains(string(brief), want) {
			t.Errorf("brief missing %q", want)
		}
	}

	// Cleanup(removeAll=true) must remove only the daemon RootDir, never the
	// user's directory or its pre-existing content.
	env.Cleanup(true)
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("user's sentinel file was destroyed by Cleanup: %v", err)
	}
	if _, err := os.Stat(fixedRepo); err != nil {
		t.Fatalf("user's fixed repo directory was destroyed by Cleanup: %v", err)
	}
	if _, err := os.Stat(env.RootDir); !os.IsNotExist(err) {
		t.Fatalf("daemon RootDir should be removed by Cleanup, stat err=%v", err)
	}
}
