package execenv

import (
	"strings"
	"testing"
)

func TestRenderFixedRepoSection_NotFixedMode(t *testing.T) {
	if got := renderFixedRepoSection(TaskContextForEnv{FixedRepoMode: false}); got != "" {
		t.Fatalf("expected empty section for non-fixed task, got %q", got)
	}
}

func TestRenderFixedRepoSection_IncludesPathAndCheckoutBan(t *testing.T) {
	got := renderFixedRepoSection(TaskContextForEnv{
		FixedRepoMode:    true,
		FixedRepoPath:    "/data/repos/alpha",
		FixedRepoVcsType: "git",
	})
	for _, want := range []string{
		"## Fixed Repo",
		"/data/repos/alpha",
		"multica repo checkout",
		"git",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("section missing %q; got:\n%s", want, got)
		}
	}
}

func TestRenderFixedRepoSection_VcsVariants(t *testing.T) {
	cases := map[string]string{
		"perforce": "Perforce",
		"none":     "not under version control",
		"custom":   "custom",
	}
	for vcs, want := range cases {
		got := renderFixedRepoSection(TaskContextForEnv{
			FixedRepoMode:    true,
			FixedRepoPath:    "/x",
			FixedRepoVcsType: vcs,
		})
		if !strings.Contains(got, want) {
			t.Errorf("vcs=%s: section missing %q; got:\n%s", vcs, want, got)
		}
	}
}
