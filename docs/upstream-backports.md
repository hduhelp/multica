# Upstream Backport Ledger

Tracks which `multica-ai/multica` (upstream) changes we have pulled into this
fork, evaluated-and-skipped, or still consider candidates. Keeps us from
re-surveying from the fork point every time.

## How to run the next survey

```bash
git fetch upstream main && git fetch origin main
# Only NEW commits since the last survey — NOT from the fork point:
git log --reverse --no-merges --format='%h %cs %s' <last-surveyed-upstream>..upstream/main
```

Then triage the new commits into the tables below and bump the
`Last surveyed upstream` marker.

## Markers

| Field | Value |
| --- | --- |
| Fork point (merge-base) | `dbb515b7b` (2026-07-21, #5663) |
| Fork HEAD at last survey | `4b7d8f38f` |
| **Last surveyed upstream** | **`139cc8920`** (2026-07-23, #5811) — survey up to here is done |

> Next survey: `git log 139cc8920..upstream/main`. Anything at or below
> `139cc8920` is already triaged in this file.

---

## Ported into the fork

Content is in the fork (reconciled by hand, so `git cherry`/patch-id won't
always detect them — this table is the source of truth).

| Upstream PR | What | Landed as |
| --- | --- | --- |
| #5748 | agent: parse Grok ACP token usage from session/prompt `_meta` | our PR #12 |
| #5752 | agent: inject `--yolo` in Qwen headless runs | our PR #12 |
| #5772 | repocache: repair promisor config on isolated partial-cache checkouts | our PR #12 |
| #5753 | skills: tree-first skill import (stop 504 on large repos) | our PR #13 (reconciled with our directory-import work) |
| #5734 | agent/codex: retry once when model-catalog refresh blocks first turn | present (patch-id identical to upstream) |
| #5803 | runtime: clarify external background work | our PR #15 |
| #5840 | runtime: forbid blocking on external CI in the runtime brief | our PR #15 (builds on #5803) |
| #5780 | agents: rebind Agent Builder carrier on runtime switch | our PR #15 (sqlc regenerated for fork schema) |
| #5759 | agent/codex: diagnose thread/start timeouts fail-closed + orphan cleanup | our PR #15 |
| #5827 | usage: price the Grok catalog + editable custom pricing | our PR #15 |
| #5838 | agent: stop double-counting Grok cached input tokens | our PR #15 |
| #5746 | agents: keep creation errors visible | our PR #15 (dropped fork-absent motion scaffolding in conflict) |
| #5715 | daemon: retry fresh session only when resume was actually rejected | already present (independent backport `af2bb9cf9`); re-cherry-pick was empty |

## Evaluated & deliberately skipped — fork has its own implementation

| Upstream PR | What | Why skipped |
| --- | --- | --- |
| #5686 | agents: per-agent runtime skill controls | Fork already has a parallel impl: migration `221_agent_disabled_runtime_skills`, `disabled_runtime_skills` column, `execenv` filtering, skills-tab UI. Upstream uses migration 206 — a divergent lineage; do NOT take. |

---

## Excluded — architecturally incompatible with the fork

| Upstream PR | What | Why excluded |
| --- | --- | --- |
| #5819 / #5839 | issues: unify/filter working agents across issue views | Both modify `server/internal/handler/issue_table_query.go` and add server-side table queries. **This fork has no server-backed issue-table-query subsystem** (no `issue_table_query.go`, no `/issues/table` route; working agents resolve in `squad.go`). Taking them faithfully means importing the whole `#5100 → #5817 → #5820` server-grouping subsystem the fork deliberately rejected — a broad architectural change, not a PR backport. Cherry-pick aborted cleanly; no residue on `main`. Revisit only if the fork decides to adopt server-backed table grouping. |

---

## Reviewed — not selected (noise / churn / incremental / independent lineage)

All accounted for so they don't resurface as "new" next survey.

- **Resize interaction churn (net-zero, upstream reverted itself):** #5779, #5824, #5830, #5831
- **Issue-table grouping flip-flop (moved to server → reverted → restored):** MUL-5100 (`d43e500ff`), #5777, #5817, #5820, #5778 — churn in an area we've diverged; skip.
- **Onboarding rework:** #5774, #5756, #5786
- **Docs / changelog:** #5724, #5732, #5769, #5814, #5829, #5832
- **Minor UI / i18n:** #5670 (capitalize label), #5718 (animations), #5813 (inbox hover card), #5815 (composer height), #5727 (bubble menu), #5722 (auth OR divider)
- **Desktop env / links:** #5680 (staging VITE_APP_URL), #5826 (open in-app links in tab)
- **Qwen logo/asset churn:** #5713, #5716, #5728
- **Feats with independent fork lineage / incremental (evaluate on demand only):** #5578 (RichContent unify), #5661 (tab-by-object-identity — fork has own `tab-store.ts`), #5738 (table sub-issue), Qwen runtime (`f8bf6cd8b` — fork has `qwen.go`), #5764 (emoji avatars), #5763 (usage/runtime reporting), #5721 (richer sub-issue rows), #5737 (inbox agent activity), #5758 (squads parent status), #5750 (sub-issues refresh), #5770 (draft options), #5767 (open table in new tab), #5730 (table cell editors), #5678 (assign-dialog preview), #5717 (mobile spinner), #5811 (CI status on PR cards), #5821 (Codex Fast mode)
- **Runtime/agent fixes, lower priority (revisit if the area breaks):** #5672 (Codex unsandboxed on Windows), #5711 (cursor prompt on stdin), #5794 (dev diagnostics best-effort), #5789 (block agent CLI in default tests), #5802 (ci self-test)

---

## Change log of this ledger

- 2026-07-24 — Initial ledger. Surveyed upstream `dbb515b7b..139cc8920` (67 commits). Recorded 5 ported (#5748/#5752/#5772/#5753/#5734), 1 skipped-own-impl (#5686), 10 candidates, rest triaged as not-selected.
- 2026-07-24 — Backport batch shipped (PR #15): #5803, #5840, #5780, #5759, #5827, #5838, #5746 ported and reconciled against the fork; verified (build/vet/typecheck, backport unit+component tests, 9/9 semantic-review workflow, live dev-browser smoke). #5715 found already-present (`af2bb9cf9`). #5819/#5839 excluded as architecturally incompatible (no server-backed issue-table subsystem). Released services: backend `0.4.11`, web `0.4.12`, chart `0.1.4`.
