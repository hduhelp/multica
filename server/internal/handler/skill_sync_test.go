package handler

import "testing"

// TestSkillBundleHash pins the change-detection signature the hourly sync uses
// to tell "upstream changed" from "nothing to do".
func TestSkillBundleHash(t *testing.T) {
	files := []CreateSkillFileRequest{
		{Path: "a.md", Content: "alpha"},
		{Path: "b.md", Content: "beta"},
	}

	base := skillBundleHash("SKILL body", files)

	// Deterministic: same input → same hash.
	if again := skillBundleHash("SKILL body", files); again != base {
		t.Errorf("hash must be stable for identical input: %q != %q", base, again)
	}

	// File order from the source must not change the hash — the sync sorts by
	// path so a reordered listing does not look like a change.
	reordered := []CreateSkillFileRequest{
		{Path: "b.md", Content: "beta"},
		{Path: "a.md", Content: "alpha"},
	}
	if got := skillBundleHash("SKILL body", reordered); got != base {
		t.Errorf("hash must be file-order independent: %q != %q", got, base)
	}

	// A changed SKILL.md body changes the hash.
	if got := skillBundleHash("SKILL body v2", files); got == base {
		t.Error("hash must change when the body changes")
	}

	// A changed auxiliary file changes the hash.
	changedFile := []CreateSkillFileRequest{
		{Path: "a.md", Content: "alpha"},
		{Path: "b.md", Content: "beta v2"},
	}
	if got := skillBundleHash("SKILL body", changedFile); got == base {
		t.Error("hash must change when an auxiliary file changes")
	}

	// A new file changes the hash.
	extra := append([]CreateSkillFileRequest{{Path: "c.md", Content: "gamma"}}, files...)
	if got := skillBundleHash("SKILL body", extra); got == base {
		t.Error("hash must change when a file is added")
	}

	// A path/content boundary must not be ambiguous: moving a byte across the
	// path/content split changes the hash (guards the NUL-delimited framing).
	shifted := []CreateSkillFileRequest{{Path: "a.mdx", Content: "alpha"}, {Path: "b.md", Content: "beta"}}
	if got := skillBundleHash("SKILL body", shifted); got == base {
		t.Error("hash must distinguish a path change from a content change")
	}
}
