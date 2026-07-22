package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/pkg/protocol"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Batch import lets the client import many skills in ONE request — the surface
// the directory-of-skills picker uses when the user selects several sub-skills.
// Doing it server-side (bounded concurrency, per-skill results) avoids the
// client firing N sequential requests, where GitHub secondary-throttling could
// slow one enough that the browser drops the connection mid-import.

const (
	maxBatchImportURLs     = 60
	batchImportConcurrency = 4
	// A retry smooths over a transient GitHub blip (a secondary-rate-limit hit
	// on one file during a burst) without turning the whole skill into a failure.
	batchImportFetchAttempts = 2
)

// BatchImportSkillRequest imports every URL in URLs. OnConflict defaults to
// "skip" so one already-existing skill never fails the rest of the batch;
// "fail" is intentionally rejected (it is meaningless for a set).
type BatchImportSkillRequest struct {
	URLs       []string `json:"urls"`
	OnConflict string   `json:"on_conflict,omitempty"`
}

// BatchImportItem is the per-URL outcome. Result mirrors the single-import
// SkillImportResult shape (status created|updated|skipped|conflict|failed).
type BatchImportItem struct {
	URL    string            `json:"url"`
	Result SkillImportResult `json:"result"`
}

// BatchImportSkillResponse aggregates every item plus rolled-up counts so the
// client can show "Imported N, skipped M, failed K" without re-tallying.
type BatchImportSkillResponse struct {
	Results []BatchImportItem `json:"results"`
	Created int               `json:"created"`
	Skipped int               `json:"skipped"`
	Failed  int               `json:"failed"`
}

// ImportSkillsBatch imports a set of skill URLs concurrently (bounded) and
// returns one result per URL. Order of Results matches the (de-duplicated)
// input order. Always 200 unless the request itself is malformed — per-skill
// failures are reported inside Results, not as an HTTP error.
func (h *Handler) ImportSkillsBatch(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	creatorUUID := parseUUID(creatorID)

	var req BatchImportSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	urls := dedupeNonEmpty(req.URLs)
	if len(urls) == 0 {
		writeError(w, http.StatusBadRequest, "urls is required")
		return
	}
	if len(urls) > maxBatchImportURLs {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d urls per batch", maxBatchImportURLs))
		return
	}
	strategy := req.OnConflict
	if strategy == "" {
		strategy = importOnConflictSkip
	}
	if strategy != importOnConflictSkip && strategy != importOnConflictOverwrite && strategy != importOnConflictRename {
		writeError(w, http.StatusBadRequest, "on_conflict must be one of: skip, overwrite, rename")
		return
	}

	actorType, actorID := h.resolveActor(r, creatorID, workspaceID)

	results := make([]BatchImportItem, len(urls))
	sem := make(chan struct{}, batchImportConcurrency)
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, u string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = BatchImportItem{
				URL: u,
				Result: h.importSkillFromURL(r.Context(), skillImportContext{
					workspaceID:   workspaceID,
					workspaceUUID: workspaceUUID,
					creatorUUID:   creatorUUID,
					creatorID:     creatorID,
					actorType:     actorType,
					actorID:       actorID,
					strategy:      strategy,
				}, u),
			}
		}(i, u)
	}
	wg.Wait()

	resp := BatchImportSkillResponse{Results: results}
	for _, it := range results {
		switch it.Result.Status {
		case "created", "updated":
			resp.Created++
		case "skipped":
			resp.Skipped++
		default:
			resp.Failed++
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// skillImportContext bundles the per-request identity + policy so the batch
// worker signature stays readable.
type skillImportContext struct {
	workspaceID   string
	workspaceUUID pgtype.UUID
	creatorUUID   pgtype.UUID
	creatorID     string
	actorType     string
	actorID       string
	strategy      string
}

// importSkillFromURL fetches a single URL and imports it, returning the outcome.
func (h *Handler) importSkillFromURL(ctx context.Context, ic skillImportContext, rawURL string) SkillImportResult {
	source, normalized, err := detectImportSource(rawURL)
	if err != nil {
		return SkillImportResult{Status: "failed", Reason: err.Error()}
	}
	imported, err := fetchImportedSkillWithRetry(ctx, source, normalized)
	if err != nil {
		return SkillImportResult{Status: "failed", Reason: err.Error()}
	}
	return h.importOneImportedSkill(ctx, ic, imported)
}

// fetchImportedSkillWithRetry dispatches to the per-source fetcher and retries a
// transient failure once, so a single throttled request doesn't sink the skill.
func fetchImportedSkillWithRetry(ctx context.Context, source importSource, normalized string) (*importedSkill, error) {
	var lastErr error
	for attempt := 0; attempt < batchImportFetchAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(750 * time.Millisecond)
		}
		httpClient := &http.Client{Timeout: 60 * time.Second}
		var imported *importedSkill
		var err error
		switch source {
		case sourceClawHub:
			imported, err = fetchFromClawHub(ctx, httpClient, normalized)
		case sourceSkillsSh:
			imported, err = fetchFromSkillsSh(ctx, httpClient, normalized)
		case sourceGitHub:
			imported, err = fetchFromGitHub(ctx, httpClient, normalized)
		}
		if err == nil {
			return imported, nil
		}
		lastErr = err
		// A capped-file error is deterministic; retrying wastes time.
		if isCapError(err) {
			break
		}
	}
	return nil, lastErr
}

// importOneImportedSkill is the returning form of finishSkillImport: it maps the
// files/config, checks for a name collision, applies the on_conflict strategy,
// and creates the skill — RETURNING the outcome (and publishing events) instead
// of writing to a ResponseWriter, so the batch can aggregate many.
func (h *Handler) importOneImportedSkill(ctx context.Context, ic skillImportContext, imported *importedSkill) SkillImportResult {
	files := make([]CreateSkillFileRequest, 0, len(imported.files))
	for _, f := range imported.files {
		if !validateFilePath(f.path) {
			continue
		}
		files = append(files, CreateSkillFileRequest{Path: f.path, Content: f.content})
	}
	config := map[string]any{}
	if imported.origin != nil {
		config["origin"] = imported.origin
	}
	name := sanitizeNullBytes(imported.name)

	if existing, found, lerr := h.lookupSkillByName(ctx, ic.workspaceUUID, name); lerr != nil {
		return SkillImportResult{Status: "failed", Reason: "failed to check for existing skill: " + lerr.Error()}
	} else if found {
		return h.resolveImportConflictResult(ctx, ic, name, imported, config, files, existing)
	}

	resp, err := h.createImportedSkillWithName(ctx, ic.workspaceUUID, ic.creatorUUID, name, imported, config, files)
	if err != nil {
		if isUniqueViolation(err) {
			// Lost a create race with a sibling item / another request — resolve
			// as a conflict rather than a hard failure.
			if existing, found, lerr := h.lookupSkillByName(ctx, ic.workspaceUUID, name); lerr == nil && found {
				return h.resolveImportConflictResult(ctx, ic, name, imported, config, files, existing)
			}
			return SkillImportResult{Status: "conflict", Reason: skillImportConflictReason()}
		}
		return SkillImportResult{Status: "failed", Reason: "failed to create skill: " + err.Error()}
	}
	h.publish(protocol.EventSkillCreated, ic.workspaceID, ic.actorType, ic.actorID, map[string]any{"skill": resp})
	return SkillImportResult{Status: "created", Skill: &resp}
}

// resolveImportConflictResult is the returning form of resolveImportSkillConflict.
func (h *Handler) resolveImportConflictResult(ctx context.Context, ic skillImportContext, name string, imported *importedSkill, config map[string]any, files []CreateSkillFileRequest, existing db.Skill) SkillImportResult {
	existingInfo := existingSkillIdentity(existing, ic.creatorID)
	switch ic.strategy {
	case importOnConflictSkip:
		return SkillImportResult{Status: "skipped", Reason: "a skill with this name already exists", ExistingSkill: &existingInfo}
	case importOnConflictOverwrite:
		if !canOverwriteSkillByLocalImport(ic.creatorID, existing) {
			return SkillImportResult{Status: "failed", Reason: "only the skill creator can overwrite this skill", ExistingSkill: &existingInfo}
		}
		resp, err := h.overwriteSkillWithFiles(ctx, skillOverwriteInput{
			WorkspaceID:   ic.workspaceUUID,
			TargetSkillID: existing.ID,
			UserID:        ic.creatorID,
			ExpectedName:  name,
			Description:   imported.description,
			Content:       imported.content,
			Config:        config,
			Files:         files,
		})
		if err != nil {
			_, reason := skillImportOverwriteFailure(err)
			return SkillImportResult{Status: "failed", Reason: reason, ExistingSkill: &existingInfo}
		}
		h.publish(protocol.EventSkillUpdated, ic.workspaceID, ic.actorType, ic.actorID, map[string]any{"skill": resp})
		return SkillImportResult{Status: "updated", Skill: &resp, ExistingSkill: &existingInfo}
	case importOnConflictRename:
		resp, err := h.createRenamedImportedSkill(ctx, ic.workspaceUUID, ic.creatorUUID, name, imported, config, files)
		if err != nil {
			return SkillImportResult{Status: "failed", Reason: "failed to create renamed skill: " + err.Error(), ExistingSkill: &existingInfo}
		}
		h.publish(protocol.EventSkillCreated, ic.workspaceID, ic.actorType, ic.actorID, map[string]any{"skill": resp})
		return SkillImportResult{Status: "created", Reason: "renamed to avoid an existing skill", Skill: &resp, ExistingSkill: &existingInfo}
	default:
		return SkillImportResult{Status: "conflict", Reason: skillImportConflictReason(), ExistingSkill: &existingInfo}
	}
}

// dedupeNonEmpty trims, drops empties, and de-duplicates while preserving order.
func dedupeNonEmpty(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
