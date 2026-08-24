package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The hourly sync rewrites a skill only when this digest moves, so every case
// below is the difference between a silent no-op and an hourly skill:updated
// storm on skills nothing happened to.
func TestSkillBundleDigest(t *testing.T) {
	base := func() (string, string, string, []CreateSkillFileRequest) {
		return "reviewer", "Reviews diffs", "# Reviewer\n", []CreateSkillFileRequest{
			{Path: "a.md", Content: "alpha"},
			{Path: "b.md", Content: "beta"},
		}
	}

	t.Run("identical bundles match", func(t *testing.T) {
		n, d, c, f := base()
		if skillBundleDigest(n, d, c, f) != skillBundleDigest(n, d, c, f) {
			t.Fatal("identical bundles must produce the same digest")
		}
	})

	t.Run("file order is not a change", func(t *testing.T) {
		n, d, c, f := base()
		reordered := []CreateSkillFileRequest{f[1], f[0]}
		if skillBundleDigest(n, d, c, f) != skillBundleDigest(n, d, c, reordered) {
			t.Fatal("reordering files must not read as an upstream change")
		}
	})

	t.Run("each field is covered", func(t *testing.T) {
		n, d, c, f := base()
		want := skillBundleDigest(n, d, c, f)

		cases := map[string]string{
			"name":         skillBundleDigest("renamed", d, c, f),
			"description":  skillBundleDigest(n, "changed", c, f),
			"content":      skillBundleDigest(n, d, "# Changed\n", f),
			"file content": skillBundleDigest(n, d, c, []CreateSkillFileRequest{f[0], {Path: "b.md", Content: "BETA"}}),
			"file path":    skillBundleDigest(n, d, c, []CreateSkillFileRequest{f[0], {Path: "c.md", Content: "beta"}}),
			"file added":   skillBundleDigest(n, d, c, append(append([]CreateSkillFileRequest{}, f...), CreateSkillFileRequest{Path: "c.md", Content: "gamma"})),
			"file removed": skillBundleDigest(n, d, c, f[:1]),
		}
		for field, got := range cases {
			if got == want {
				t.Errorf("a %s change must move the digest", field)
			}
		}
	})

	t.Run("fields cannot alias across the boundary", func(t *testing.T) {
		// Without length prefixing, ("ab","c") and ("a","bc") would concatenate
		// to the same bytes and an upstream edit could hide as a no-op.
		if skillBundleDigest("ab", "c", "", nil) == skillBundleDigest("a", "bc", "", nil) {
			t.Fatal("adjacent fields must not alias")
		}
	})
}

// The stored side has to hash into exactly the shape a fetch produces, or the
// job rewrites every skill on every tick.
func TestStoredSkillBundleDigestMatchesFetchShape(t *testing.T) {
	skill := db.Skill{Name: "reviewer", Description: "Reviews diffs", Content: "# Reviewer\n"}
	stored := []db.SkillFile{
		{Path: "b.md", Content: "beta"},
		{Path: "a.md", Content: "alpha"},
	}
	fetched := []CreateSkillFileRequest{
		{Path: "a.md", Content: "alpha"},
		{Path: "b.md", Content: "beta"},
	}

	if storedSkillBundleDigest(skill, stored) !=
		skillBundleDigest(skill.Name, skill.Description, skill.Content, fetched) {
		t.Fatal("an unchanged upstream must digest identically to what is stored")
	}
}
