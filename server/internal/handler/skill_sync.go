package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/protocol"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// errSkillNoRemoteSource is returned when a skill has no config.origin.source_url
// to sync from. The manual endpoint maps it to 400; the batch job skips it.
var errSkillNoRemoteSource = errors.New("skill has no remote source_url to sync from")

// importSkillFromSource re-fetches a skill's remote origin and, when the fetched
// bundle differs from what is stored, rewrites the skill + its files in one
// transaction. It is the shared core behind the manual sync endpoint and the
// hourly RemoteSkillSyncJob.
//
// When skipUnchanged is true (the hourly job) and the fetched bundle hashes to
// the same value recorded on the last sync, it returns changed=false and writes
// nothing — so an unchanged upstream never bumps updated_at or emits a
// skill:updated event. The manual endpoint passes skipUnchanged=false to force a
// re-import on demand.
func (h *Handler) importSkillFromSource(ctx context.Context, skill db.Skill, skipUnchanged bool) (updated db.Skill, files []SkillFileResponse, changed bool, err error) {
	sourceURL, ok := skillOriginSourceURL(skill.Config)
	if !ok {
		return skill, nil, false, errSkillNoRemoteSource
	}
	source, normalized, err := detectImportSource(sourceURL)
	if err != nil {
		return skill, nil, false, err
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	var imported *importedSkill
	switch source {
	case sourceClawHub:
		imported, err = fetchFromClawHub(httpClient, normalized)
	case sourceSkillsSh:
		imported, err = fetchFromSkillsSh(httpClient, normalized)
	case sourceGitHub:
		imported, err = fetchFromGitHub(httpClient, normalized)
	default:
		return skill, nil, false, fmt.Errorf("unsupported skill source %v", source)
	}
	if err != nil {
		return skill, nil, false, err
	}

	importedFiles := importedSkillFiles(imported)
	hash := skillBundleHash(imported.content, importedFiles)

	config := skillConfigMap(skill.Config)
	if skipUnchanged {
		if prev, _ := config["synced_hash"].(string); prev == hash {
			// Upstream is unchanged since the last sync — nothing to write.
			return skill, nil, false, nil
		}
	}

	if imported.origin != nil {
		config["origin"] = imported.origin
	}
	config["last_synced_at"] = time.Now().UTC().Format(time.RFC3339)
	config["synced_hash"] = hash
	configJSON, _ := json.Marshal(config)

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return skill, nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	updated, err = qtx.UpdateSkill(ctx, db.UpdateSkillParams{
		ID:          skill.ID,
		Name:        pgtype.Text{String: sanitizeNullBytes(imported.name), Valid: true},
		Description: pgtype.Text{String: sanitizeNullBytes(imported.description), Valid: true},
		Content:     pgtype.Text{String: sanitizeNullBytes(imported.content), Valid: true},
		Config:      configJSON,
	})
	if err != nil {
		return skill, nil, false, fmt.Errorf("update skill: %w", err)
	}
	if err := qtx.DeleteSkillFilesBySkill(ctx, updated.ID); err != nil {
		return skill, nil, false, fmt.Errorf("delete old skill files: %w", err)
	}
	fileResps := make([]SkillFileResponse, 0, len(importedFiles))
	for _, f := range importedFiles {
		sf, ferr := qtx.UpsertSkillFile(ctx, db.UpsertSkillFileParams{
			SkillID: updated.ID,
			Path:    sanitizeNullBytes(f.Path),
			Content: sanitizeNullBytes(f.Content),
		})
		if ferr != nil {
			return skill, nil, false, fmt.Errorf("upsert skill file: %w", ferr)
		}
		fileResps = append(fileResps, skillFileToResponse(sf))
	}
	if err := tx.Commit(ctx); err != nil {
		return skill, nil, false, fmt.Errorf("commit: %w", err)
	}
	return updated, fileResps, true, nil
}

// skillBundleHash is a stable content signature over a skill's SKILL.md body
// plus every auxiliary file (path + content), so a sync can tell "upstream
// changed" from "nothing to do" without a per-field diff. Files are sorted by
// path so ordering from the source never spuriously flips the hash.
func skillBundleHash(content string, files []CreateSkillFileRequest) string {
	sorted := make([]CreateSkillFileRequest, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	sum := sha256.New()
	sum.Write([]byte(content))
	for _, f := range sorted {
		sum.Write([]byte{0})
		sum.Write([]byte(f.Path))
		sum.Write([]byte{0})
		sum.Write([]byte(f.Content))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// SyncRemoteOriginSkills re-syncs every URL-imported skill across all
// workspaces, skipping any whose upstream is unchanged. It is best-effort: a
// per-skill fetch/persist error is logged and the loop continues, so one dead
// source never stalls the rest. Returns how many skills actually changed.
//
// Backs the hourly RemoteSkillSyncJob. A changed skill emits skill:updated
// (attributed to the system) so the web UI refreshes and the next agent task
// resolves the new bundle. It never runs inside the task-prepare path.
func (h *Handler) SyncRemoteOriginSkills(ctx context.Context) (int, error) {
	skills, err := h.Queries.ListRemoteOriginSkills(ctx)
	if err != nil {
		return 0, fmt.Errorf("list remote-origin skills: %w", err)
	}
	changed := 0
	for _, skill := range skills {
		updated, files, didChange, serr := h.importSkillFromSource(ctx, skill, true)
		if serr != nil {
			slog.Warn("remote skill sync: skill failed, skipping",
				"skill_id", uuidToString(skill.ID),
				"workspace_id", uuidToString(skill.WorkspaceID),
				"error", serr)
			continue
		}
		if !didChange {
			continue
		}
		changed++
		wsID := uuidToString(updated.WorkspaceID)
		resp := SkillWithFilesResponse{SkillResponse: skillToResponse(updated), Files: files}
		h.publish(protocol.EventSkillUpdated, wsID, "system", "", map[string]any{"skill": resp})
		slog.Info("remote skill sync: skill updated from origin",
			"skill_id", uuidToString(updated.ID), "workspace_id", wsID, "name", updated.Name)
	}
	return changed, nil
}
