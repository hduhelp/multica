package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// SyncRemoteOriginSkills re-fetches every URL-imported skill from its stored
// config.origin and rewrites the ones whose upstream actually changed. It backs
// the hourly RemoteSkillSyncJob so an imported skill tracks its origin without
// anyone clicking refresh, and returns how many skills were rewritten.
//
// This is the unattended sibling of RefreshSkill and differs from it in three
// ways that matter for a background job:
//
//   - No permission check. There is no caller to authorize; the source URL is
//     pinned on the skill itself, so the job can only re-fetch what an
//     authorized user already chose to import.
//   - Unchanged skills are skipped. RefreshSkill always rewrites so a manual
//     click gives an unambiguous result; rewriting here would emit a
//     skill:updated storm every hour and bump updated_at on skills nothing
//     happened to.
//   - One skill's failure never fails the run. An unreachable origin, a
//     hand-edited config, or a rename collision is logged and skipped so the
//     rest of the set still syncs.
//
// A rewritten skill emits skill:updated attributed to the system, so the web UI
// refreshes and the next agent task resolves the new bundle. It never runs
// inside the task-prepare path.
func (h *Handler) SyncRemoteOriginSkills(ctx context.Context) (int, error) {
	skills, err := h.Queries.ListRemoteOriginSkills(ctx)
	if err != nil {
		return 0, fmt.Errorf("list remote-origin skills: %w", err)
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	changed := 0
	for _, skill := range skills {
		if ctx.Err() != nil {
			// The job's RunTimeout fired. Report what landed rather than
			// discarding it — the next tick resumes from the oldest updated_at.
			return changed, ctx.Err()
		}
		if h.syncOneRemoteOriginSkill(ctx, httpClient, skill) {
			changed++
		}
	}
	return changed, nil
}

// syncOneRemoteOriginSkill re-fetches a single skill and rewrites it when the
// upstream content differs. It reports whether the skill was rewritten; every
// failure path logs and returns false so the caller can continue the sweep.
func (h *Handler) syncOneRemoteOriginSkill(ctx context.Context, httpClient *http.Client, skill db.Skill) bool {
	skillID := uuidToString(skill.ID)
	workspaceID := uuidToString(skill.WorkspaceID)

	origin, ok := parseSkillOrigin(skill.Config)
	if !ok {
		// Not re-fetchable (manual, runtime-local, archive upload). The query
		// filters on source_url, so this means a hand-edited config.
		return false
	}

	fetchCtx, cancel := context.WithTimeout(ctx, importFetchTimeout)
	defer cancel()

	imported, err := fetchImportedSkillFromOrigin(fetchCtx, httpClient, origin)
	if err != nil {
		slog.Warn("remote skill sync: fetch failed, skipping skill",
			"skill_id", skillID, "workspace_id", workspaceID, "error", err)
		return false
	}

	current, err := h.Queries.ListSkillFiles(ctx, skill.ID)
	if err != nil {
		slog.Warn("remote skill sync: could not read current files, skipping skill",
			"skill_id", skillID, "workspace_id", workspaceID, "error", err)
		return false
	}

	newName := sanitizeNullBytes(imported.name)
	files := importedSkillFileRequests(imported)
	if skillBundleDigest(newName, imported.description, imported.content, files) ==
		storedSkillBundleDigest(skill, current) {
		return false
	}

	resp, err := h.overwriteSkillWithFiles(ctx, skillOverwriteInput{
		WorkspaceID:   skill.WorkspaceID,
		TargetSkillID: skill.ID,
		// The job has no user. Identity (created_by) is preserved by the
		// overwrite itself; AllowOverwrite short-circuits the creator recheck
		// that would otherwise reject an empty user id.
		UserID:         "",
		NewName:        newName,
		AllowOverwrite: func(string, db.Skill) bool { return true },
		Description:    imported.description,
		Content:        imported.content,
		Config:         mergeSkillConfigOrigin(skill.Config, imported.origin),
		Files:          files,
	})
	if err != nil {
		slog.Warn("remote skill sync: overwrite failed, skipping skill",
			"skill_id", skillID, "workspace_id", workspaceID, "error", err)
		return false
	}

	h.publish(protocol.EventSkillUpdated, workspaceID, "system", "", map[string]any{"skill": resp})
	slog.Info("remote skill sync: skill updated from origin",
		"skill_id", skillID, "workspace_id", workspaceID, "name", newName)
	return true
}

// skillBundleDigest fingerprints everything the sync would write, so an
// unchanged upstream is a no-op instead of an hourly rewrite. Files are sorted
// by path because neither the fetch order nor the stored order is guaranteed to
// be stable, and a reordering is not a change.
func skillBundleDigest(name, description, content string, files []CreateSkillFileRequest) string {
	sorted := make([]CreateSkillFileRequest, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	sum := sha256.New()
	writeDigestField(sum, name)
	writeDigestField(sum, description)
	writeDigestField(sum, content)
	for _, f := range sorted {
		writeDigestField(sum, f.Path)
		writeDigestField(sum, f.Content)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// storedSkillBundleDigest fingerprints the skill as it currently sits in the
// database, in the same shape skillBundleDigest produces for a fetch.
func storedSkillBundleDigest(skill db.Skill, files []db.SkillFile) string {
	reqs := make([]CreateSkillFileRequest, 0, len(files))
	for _, f := range files {
		reqs = append(reqs, CreateSkillFileRequest{Path: f.Path, Content: f.Content})
	}
	return skillBundleDigest(skill.Name, skill.Description, skill.Content, reqs)
}

// writeDigestField length-prefixes each field so that concatenation cannot
// alias — ("ab", "c") and ("a", "bc") must hash differently.
func writeDigestField(w io.Writer, s string) {
	fmt.Fprintf(w, "%d:%s", len(s), s)
}
