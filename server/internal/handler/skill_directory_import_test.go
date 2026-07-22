package handler

import (
	"reflect"
	"testing"
)

// TestImmediateSubSkillDirs pins the container-directory discovery: only
// directories directly under the pointed prefix that hold a SKILL.md are
// sub-skills; the container's own SKILL.md and deeper nested SKILL.md files are
// not, and duplicates collapse.
func TestImmediateSubSkillDirs(t *testing.T) {
	cases := []struct {
		name   string
		paths  []string
		prefix string
		want   []string
	}{
		{
			name: "lark-style container",
			paths: []string{
				"skills/lark-base/SKILL.md",
				"skills/lark-base/references/api.md", // not a SKILL.md → ignored
				"skills/lark-calendar/SKILL.md",
				"skills/lark-shared/util.go", // no SKILL.md → not listed
				"README.md",
			},
			prefix: "skills/",
			want:   []string{"lark-base", "lark-calendar"},
		},
		{
			name:   "repo root container",
			paths:  []string{"alpha/SKILL.md", "beta/SKILL.md", "SKILL.md"},
			prefix: "",
			// The root's own SKILL.md (rest == "SKILL.md", no slash) is not a
			// sub-skill; alpha/beta are.
			want: []string{"alpha", "beta"},
		},
		{
			name: "deeper nested SKILL.md is not an immediate sub-skill",
			paths: []string{
				"skills/lark-base/SKILL.md",
				"skills/lark-base/examples/nested/SKILL.md", // two levels deep → skip
			},
			prefix: "skills/",
			want:   []string{"lark-base"},
		},
		{
			name:   "no sub-skills",
			paths:  []string{"skills/SKILL.md", "other/file.txt"},
			prefix: "skills/",
			want:   []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := immediateSubSkillDirs(tc.paths, tc.prefix)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("immediateSubSkillDirs(%v, %q) = %v, want %v", tc.paths, tc.prefix, got, tc.want)
			}
		})
	}
}

// TestBuildGitHubTreeURL pins that a discovered candidate URL round-trips back
// through parseGitHubURL to the same owner/repo/ref/dir — so re-importing a
// picked candidate resolves to exactly that sub-skill directory.
func TestBuildGitHubTreeURL(t *testing.T) {
	url := buildGitHubTreeURL("larksuite", "cli", "main", "skills/lark-base")
	want := "https://github.com/larksuite/cli/tree/main/skills/lark-base"
	if url != want {
		t.Fatalf("buildGitHubTreeURL = %q, want %q", url, want)
	}
	spec, err := parseGitHubURL(url)
	if err != nil {
		t.Fatalf("parseGitHubURL(%q): %v", url, err)
	}
	if spec.owner != "larksuite" || spec.repo != "cli" || spec.ref != "main" || spec.skillDir != "skills/lark-base" {
		t.Errorf("round-trip mismatch: %+v", spec)
	}
}

// TestDedupeNonEmpty pins the batch-import URL normalization: trims, drops
// empties, de-duplicates, preserves first-seen order.
func TestDedupeNonEmpty(t *testing.T) {
	got := dedupeNonEmpty([]string{" a ", "b", "a", "", "  ", "b", "c"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedupeNonEmpty = %v, want %v", got, want)
	}
}
