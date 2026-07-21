package daemon

import (
	"fmt"
	"strings"
)

// Fixed repo mode (fixed-repo-mode design, PR3 daemon execution).
//
// When the server claims a task for an agent configured with fixed repo mode,
// the claim response carries FixedRepoMode=true plus the server-locked
// FixedRepoPath. The daemon runs the agent directly inside that pre-existing
// directory instead of cloning a repo and checking out a worktree. This reuses
// the existing local_directory execution machinery wholesale — the resolved
// assignment is a *localDirectoryAssignment with FixedRepo=true, so GC
// protection, the per-path mutex, per-task config homes, and sidecar cleanup
// all apply unchanged. The only fixed-repo-specific behaviour layered on top is
// the MULTICA_FIXED_REPO_* environment and the `multica repo checkout`
// rejection.

// fixedRepoVcsTypes mirrors the server-side enum (handler validation). Keep in
// sync if the allowed set ever changes.
var fixedRepoVcsTypes = map[string]struct{}{
	"git":      {},
	"perforce": {},
	"none":     {},
	"custom":   {},
}

// normalizeFixedRepoVcsType returns a known VCS type, defaulting to "git" for
// empty/unknown values so an older/newer server can never wedge the daemon.
func normalizeFixedRepoVcsType(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "git"
	}
	if _, ok := fixedRepoVcsTypes[v]; !ok {
		return "custom"
	}
	return v
}

// fixedRepoAssignmentForTask resolves the fixed-repo directory a task must run
// in, or nil when the task is not in fixed repo mode. The returned assignment
// shares the localDirectoryAssignment shape so it flows through the same
// in-place execution path; FixedRepo is set so the caller can layer the
// fixed-repo env vars. The path is validated with the same daemon-side guards
// used for local_directory resources (absolute, not a system/home root, exists,
// is a writable directory, symlink target also safe) so a misconfigured path
// fails fast with a clear message instead of the agent writing somewhere unsafe.
func fixedRepoAssignmentForTask(task Task) (*localDirectoryAssignment, error) {
	if !task.FixedRepoMode {
		return nil, nil
	}
	rawPath := strings.TrimSpace(task.FixedRepoPath)
	if rawPath == "" {
		return nil, fmt.Errorf("fixed_repo: task is in fixed repo mode but the server sent no fixed_repo_path")
	}
	absPath, err := normalizeLocalPath(rawPath)
	if err != nil {
		return nil, fmt.Errorf("fixed_repo: %w", err)
	}
	if err := validateLocalPath(absPath); err != nil {
		return nil, fmt.Errorf("fixed_repo: %w", err)
	}
	realPath, err := resolveRealPath(absPath)
	if err != nil {
		return nil, err
	}
	// Worktree mode reuses the daemon's local_directory worktree machinery
	// wholesale: expressing it as the standard execution mode means
	// UsesWorktree() drives setup, path-mutex exemption, GC exemption and
	// cleanup exactly as it does for a worktree-mode project resource, instead
	// of a second worktree implementation keyed on fixed repo.
	// Worktree granularity is per issue, so a task with no issue key has nothing
	// to key a worktree on and must serialize on the path mutex instead of
	// silently running unisolated. The server already gates the flag on an
	// issue-bound task; this is the daemon-side backstop.
	executionMode := localDirectoryModeInPlace
	if task.FixedRepoWorktree && strings.TrimSpace(task.IssueID) != "" {
		executionMode = localDirectoryModeWorktree
	}
	assignment := &localDirectoryAssignment{
		Ref: localDirectoryRef{
			LocalPath:     absPath,
			DaemonID:      task.RuntimeID,
			ExecutionMode: executionMode,
		},
		AbsPath:   absPath,
		RealPath:  realPath,
		FixedRepo: true,
		VcsType:   normalizeFixedRepoVcsType(task.FixedRepoVcsType),
	}
	return assignment, nil
}
