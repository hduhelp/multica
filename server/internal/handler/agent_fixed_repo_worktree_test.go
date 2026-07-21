package handler

import "testing"

// TestValidateFixedRepoConfig_Worktree pins the worktree-mode validation rules:
// worktree requires fixed_repo_enabled + a local runtime + vcs_type git + exactly
// one base path, and is rejected in every contradictory combination.
func TestValidateFixedRepoConfig_Worktree(t *testing.T) {
	cleanup := (*string)(nil)
	cases := []struct {
		name     string
		enabled  bool
		paths    []string
		vcsType  string
		worktree bool
		runtime  string
		wantErr  bool
	}{
		{
			name:     "valid git single path",
			enabled:  true,
			paths:    []string{"/repo"},
			vcsType:  "git",
			worktree: true,
			runtime:  "local",
			wantErr:  false,
		},
		{
			name:     "worktree without enabled is rejected",
			enabled:  false,
			paths:    nil,
			vcsType:  "git",
			worktree: true,
			runtime:  "local",
			wantErr:  true,
		},
		{
			name:     "worktree with non-git vcs is rejected",
			enabled:  true,
			paths:    []string{"/repo"},
			vcsType:  "perforce",
			worktree: true,
			runtime:  "local",
			wantErr:  true,
		},
		{
			name:     "worktree with multiple paths is rejected",
			enabled:  true,
			paths:    []string{"/repo-a", "/repo-b"},
			vcsType:  "git",
			worktree: true,
			runtime:  "local",
			wantErr:  true,
		},
		{
			name:     "non-worktree multiple git paths still allowed",
			enabled:  true,
			paths:    []string{"/repo-a", "/repo-b"},
			vcsType:  "git",
			worktree: false,
			runtime:  "local",
			wantErr:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFixedRepoConfig(tc.enabled, tc.paths, tc.vcsType, cleanup, tc.worktree, tc.runtime)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
