package daemon

import (
	"path/filepath"
	"testing"
)

func TestNormalizeFixedRepoVcsType(t *testing.T) {
	cases := map[string]string{
		"":         "git",
		"  ":       "git",
		"git":      "git",
		"perforce": "perforce",
		"none":     "none",
		"custom":   "custom",
		"svn":      "custom", // unknown collapses to custom rather than wedging
		"GIT":      "custom", // case-sensitive by design (server stores lowercase)
	}
	for in, want := range cases {
		if got := normalizeFixedRepoVcsType(in); got != want {
			t.Errorf("normalizeFixedRepoVcsType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFixedRepoAssignmentForTask_NotFixedMode(t *testing.T) {
	got, err := fixedRepoAssignmentForTask(Task{FixedRepoMode: false, FixedRepoPath: "/tmp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil assignment for non-fixed task, got %+v", got)
	}
}

func TestFixedRepoAssignmentForTask_EmptyPath(t *testing.T) {
	_, err := fixedRepoAssignmentForTask(Task{FixedRepoMode: true, FixedRepoPath: "  "})
	if err == nil {
		t.Fatal("expected error for fixed mode with empty path, got nil")
	}
}

func TestFixedRepoAssignmentForTask_NonexistentPath(t *testing.T) {
	_, err := fixedRepoAssignmentForTask(Task{
		FixedRepoMode: true,
		FixedRepoPath: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent path, got nil")
	}
}

func TestFixedRepoAssignmentForTask_RelativePathRejected(t *testing.T) {
	_, err := fixedRepoAssignmentForTask(Task{FixedRepoMode: true, FixedRepoPath: "relative/dir"})
	if err == nil {
		t.Fatal("expected error for non-absolute path, got nil")
	}
}

func TestFixedRepoAssignmentForTask_Success(t *testing.T) {
	dir := t.TempDir()
	got, err := fixedRepoAssignmentForTask(Task{
		FixedRepoMode:    true,
		FixedRepoPath:    dir,
		FixedRepoVcsType: "perforce",
		RuntimeID:        "runtime-1",
	})
	if err != nil {
		t.Fatalf("unexpected error for valid dir: %v", err)
	}
	if got == nil {
		t.Fatal("expected assignment, got nil")
	}
	if !got.FixedRepo {
		t.Error("expected FixedRepo=true")
	}
	if got.VcsType != "perforce" {
		t.Errorf("VcsType = %q, want perforce", got.VcsType)
	}
	// AbsPath must be the cleaned real directory; RealPath resolves symlinks
	// (macOS /var/folders temp dirs are symlinked) so only require it non-empty.
	if got.AbsPath != filepath.Clean(dir) {
		t.Errorf("AbsPath = %q, want %q", got.AbsPath, filepath.Clean(dir))
	}
	if got.RealPath == "" {
		t.Error("expected non-empty RealPath")
	}
}

func TestFixedRepoAssignmentForTask_HomeDirRejected(t *testing.T) {
	// The user's home directory is blacklisted: binding an agent there would
	// scope every daemon write to the whole account.
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, err := fixedRepoAssignmentForTask(Task{FixedRepoMode: true, FixedRepoPath: home})
	if err == nil {
		t.Fatal("expected error when fixed repo path is the home directory, got nil")
	}
}

// TestFixedRepoAssignmentForTask_WorktreeWithIssue verifies that a worktree-mode
// fixed-repo task carrying an issue resolves to Mode=worktree keyed on the
// issue, so the gate isolates it in a per-issue git worktree (parallel across
// issues) instead of the serial in_place path mutex.
func TestFixedRepoAssignmentForTask_WorktreeWithIssue(t *testing.T) {
	dir := t.TempDir()
	got, err := fixedRepoAssignmentForTask(Task{
		IssueID:           "issue-abc",
		FixedRepoMode:     true,
		FixedRepoPath:     dir,
		FixedRepoVcsType:  "git",
		FixedRepoWorktree: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Mode != localDirectoryModeWorktree {
		t.Errorf("Mode = %q, want %q", got.Mode, localDirectoryModeWorktree)
	}
	if got.IssueID != "issue-abc" {
		t.Errorf("IssueID = %q, want issue-abc", got.IssueID)
	}
}

// TestFixedRepoAssignmentForTask_WorktreeWithoutIssue verifies that a
// worktree-mode task with NO issue falls back to in_place: worktree granularity
// is per-issue, so a task with no issue key must serialize on the path mutex
// rather than silently run unisolated.
func TestFixedRepoAssignmentForTask_WorktreeWithoutIssue(t *testing.T) {
	dir := t.TempDir()
	got, err := fixedRepoAssignmentForTask(Task{
		FixedRepoMode:     true,
		FixedRepoPath:     dir,
		FixedRepoVcsType:  "git",
		FixedRepoWorktree: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Mode != localDirectoryModeInPlace {
		t.Errorf("Mode = %q, want %q (no issue → in_place)", got.Mode, localDirectoryModeInPlace)
	}
	if got.IssueID != "" {
		t.Errorf("IssueID = %q, want empty", got.IssueID)
	}
}

// TestFixedRepoAssignmentForTask_NonWorktreeDefaultsInPlace guards that an
// ordinary fixed-repo task (worktree flag off) always resolves to in_place even
// when it has an issue, so existing serial behaviour is unchanged.
func TestFixedRepoAssignmentForTask_NonWorktreeDefaultsInPlace(t *testing.T) {
	dir := t.TempDir()
	got, err := fixedRepoAssignmentForTask(Task{
		IssueID:       "issue-abc",
		FixedRepoMode: true,
		FixedRepoPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Mode != localDirectoryModeInPlace {
		t.Errorf("Mode = %q, want %q", got.Mode, localDirectoryModeInPlace)
	}
}
