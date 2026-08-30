# Upstream Sync Ledger

Tracks this fork's relationship to `multica-ai/multica` (upstream): where we
branched, what we carry on top, and what we deliberately dropped.

## How to run the next survey

```bash
git fetch upstream main && git fetch origin main
# Only NEW commits since the last survey — NOT from the fork point:
git log --reverse --no-merges --format='%h %cs %s' <last-surveyed-upstream>..upstream/main
```

Then triage into the tables below and bump the `Last surveyed upstream` marker.

## Markers

| Field | Value |
| --- | --- |
| Fork point (re-fork base) | `3c4288dde` (2026-08-24, #7503) |
| **Last surveyed upstream** | **`15280617b`** (2026-08-30) — merged |
| **Fork migration range** | **9001+** — never renumber into upstream's range again |

> Everything at or below `15280617b` is upstream code we already have.
> Next survey: `git log 15280617b..upstream/main`.

---

## 2026-08-25 — re-fork

The fork previously branched at `dbb515b7b` (2026-07-21) and tracked upstream by
cherry-pick. Five weeks later upstream was **769 commits** ahead, a trial merge
conflicted in **266 files**, and 23 migration numbers had collided. The
cherry-pick model had stopped paying for itself: the cost of each backport was
rising with the gap, and the gap was widening every week.

So the fork was rebased onto upstream's current `main` and this fork's own work
was replayed on top. The decisive fact was what the old fork was actually
carrying: of 101 fork-only commits, **53** were upstream PRs merged early and
**19** were backports and bookkeeping — all of which a re-fork obtains for free
or makes moot. Only ~20 commits were genuinely this fork's, plus the
distribution layer.

Replay was by design intent, not by patch. Upstream had refactored the daemon,
the skills subsystem and the release pipeline underneath these changes, so
several features were rewired onto upstream's newer machinery rather than
re-applied as diffs.

### Carried onto the new base

| Feature | Notes |
| --- | --- |
| Lark threaded replies | Threading, topic-keyed session continuity, no parent re-quote, adaptive reply format. `markdown_tables.go` carried as a dependency — it came from upstream PR #4362, which upstream never merged. |
| Fixed repo mode | Agent runs in a pre-existing directory: config + API, claim-time path locks, daemon execution, settings UI, reclaim recovery, brief guidance. Migrations renumbered 202-205 → 404-407. |
| Fixed repo worktree mode | Rewired onto upstream's `local_directory` worktree machinery (`execution_mode=worktree` → `UsesWorktree()`) instead of the fork's parallel implementation. Migration 243 → 408. |
| Skills: directory import | Import a container directory by picking from discovered sub-skills. |
| Skills: batch import endpoint | `POST /api/skills/import/batch`, now schema-validated per the API compatibility rule. |
| Skills: hourly origin sync | Rebuilt on upstream's refresh machinery (`fetchImportedSkillFromOrigin` → `overwriteSkillWithFiles`) with a bundle digest so an unchanged upstream is a no-op. Plugin-owned skills excluded. |
| Remote daemon logs + restart | Restart barrier rewired onto upstream's `updating` / `pauseClaims` / `claimsInFlight` primitives. |
| Transcript from any agent output | |
| Opus 4.6 effort catalog fix | `xhigh` removed — it arrived with Opus 4.7 and silently degrades on 4.6. Upstream still has this wrong. |
| hduhelp distribution layer | Manifest-driven service release (`release.yml` + GHCR/ACR + OCI chart), upstream's tag-driven workflow renamed to `client-release.yml`, and the identifier rebrand across 56 files. |

### Dropped — upstream shipped its own

| Fork feature | Why dropped |
| --- | --- |
| Custom Issue Status (PR #5505, migrations 208/209 + 235-239) | Upstream never merged #5505; it built MUL-6243 instead (migrations 332-339) with the opposite premise — `issue.status` stays the authoritative TEXT column, so no `status_id`, no backfill, no double-write. Confirmed with the customer that no custom statuses existed in production, so there was no data to migrate. |
| Structured issue relations (PR #5479, migrations 240-242) | Upstream never merged #5479; it built `issue_dependency` instead. |
| Per-agent runtime skill controls | Fork migration 221 vs upstream 206 — divergent lineage; upstream's is now the one we have. |
| Fork's `local_worktree.go` | Upstream landed project-resource execution modes with the same machinery. Keeping both would have put two worktree implementations in one daemon. |
| Claude model discovery cache keying | Upstream removed the cache from the claude branch entirely, so the fix has nothing to fix. |
| Per-agent `queued_ttl_seconds` | Upstream has a global queued TTL in the runtime sweeper. The per-agent override is a separate feature; replay it on its own if it is still wanted. |

### Known remainders

- Four onboarding templates and four zh docs pages this fork had rebranded no
  longer exist upstream — upstream rewrote that onboarding. Any install URLs
  that moved into the new flow still need a branding pass.
- `README.zh-CN.md` is now `README.zh.md` upstream; rebranded in place.

---

## 2026-08-26 — first post-re-fork sync (`3c4288dde..f8ec870f3`, 20 commits)

Merged, not cherry-picked. The merge base was exactly the fork point, so
this cost 11 conflicts — 3 of them sqlc output that `make sqlc` regenerates.
That is the re-fork paying for itself: the same operation against the old
fork produced 287.

Upstream content: per-agent starter prompts, issue source context, prepaid
seat capacity, inbox From/unread filters, and a channel refactor moving
outbound delivery onto a task-level snapshot with `/new` and `/clear`
conversation controls.

### Divergences this sync recorded

| Area | Decision |
| --- | --- |
| Migration numbering | Upstream took 404 and 407-431, colliding with the fixed-repo set this fork had moved to 404-408. Ours renumbered to **432-436** — the prefix lint above 148 requires renumbering, not allowlisting. Safe because all five are idempotent, so they re-run as no-ops and only record new ledger rows. |
| `larkSessionRouting` | **Kept this fork's root-message keying.** Upstream keys a group conversation on `Source.ThreadID`. This fork's bot opens the topic itself by replying in thread, so at the first @-mention no topic exists and `ThreadID` is empty — topic-id keying would file the opening turn under a different key than every reply inside the topic it creates, and would rekey every live session on deploy. Upstream's new binder test was adapted to this contract. |
| `ChatInThread` (daemon.go) | Took upstream's delivery-snapshot rewrite wholesale, restored the Feishu branch upstream does not carry. |

### Debt the re-fork replay left, found by this sync's test run

Both pre-dated the merge and are fixed in it: fork UI written off the
role-named font scale (`text-xs`/`text-sm`/`text-[11px]`), and a
branding-layer test asserting an `appUrl` derivation the implementation
never learned — the `api` label is second in `multica.api.hduhelp.com`
and `deriveAppUrl` only stripped a leading one. Its own comment had
documented the hduhelp convention; only the code was never updated.

Still failing, untouched, confirmed pre-existing: `pkg/agent` codex
timeouts and the `ghsnapshot` trailing-refresh flake.

---

## 2026-08-27 — second sync (`f8ec870f3..5fa65bd12`, 28 commits)

10 conflicts, 4 of them sqlc output. Upstream renamed
`agent.starter_prompts` to `conversation_starters`, which is most of the
hand-resolved set; the rest were union merges where both sides had added
to the same import list or const block.

### Fork migrations moved to a reserved range

Upstream claimed **432** — the number this fork's fixed-repo set had been
moved to *two days earlier*, after upstream claimed 404/407/408. Chasing
the next free number is a standing tax: it recurs every time upstream
lands a migration, and each move re-runs the SQL under a new stem in
every environment that already applied the old one.

The set now lives at **9001-9005**, far above anything upstream will
reach. Ordering is unaffected — these add columns to `agent` and create
one table, all independent of upstream's schema. **New fork migrations
belong in this range, not at the end of upstream's.**

The re-run was verified in both directions again, and this time the
idempotency fix from 0.6.2 is what made it a no-op: 432's two CHECK
constraints already existed and were dropped-then-re-added rather than
aborting the migration.

### The additive lint earned its keep

`TestUpMigrationsAreAdditive` failed on upstream's
`432_agent_conversation_starters_rename` — a `RENAME COLUMN`. That is the
first real catch since the lint landed, and it is exactly the case it was
written for: a rolling update runs that rename while the previous version
is still selecting the old column name.

---

## 2026-08-30 — third sync (`5fa65bd12..15280617b`, 28 commits)

**Zero merge conflicts, and no migration collision** — upstream took
437-440 and this fork sits at 9001+. The reserved range paid for itself
on the very next sync; the two before it had cost a renumber each.

One break the merge could not see: upstream refactored `gcRuntime` to
return `(runtimeGCResult, error)` instead of four values, and this fork's
fixed-repo lock-release backstop inside that function still returned the
old shape. Git merged both sides cleanly because they touch different
lines. A clean merge is not a compiling one — `go build` is what caught
it, one line to fix.

Upstream content: runtime GC for archived agents, a cheaper sweeper scan,
issue-limit recovery UI, property filters for text/number/date/url, MCP
config for the Oh-My-Pi runtime, a 2h agent inactivity budget, openclaw
process-tree ownership, i18n for status/priority/squad labels, and PR
head-SHA indexing.

### A flake this ledger had been carrying is gone

`TestInFlightOldHeadKeepsTrailingRefresh` in `internal/integrations/ghsnapshot`
had been failing intermittently since the re-fork, confirmed each time as
pre-existing rather than ours. Upstream's #7659 closes the pool after
cleanups instead of before them. Three consecutive local runs pass.

Still failing, untouched, still pre-existing: the `pkg/agent` codex
timeouts.

---

## Change log of this ledger

- 2026-07-24 — Initial ledger. Surveyed `dbb515b7b..139cc8920` (67 commits).
- 2026-07-24 — Backport batch shipped (PR #15). Released backend `0.4.11`, web `0.4.12`, chart `0.1.4`.
- 2026-08-25 — Re-fork onto `3c4288dde`. Ledger reset: the backport tables it
  carried described a fork point that no longer exists. What this fork carries
  is now the table above, and the next survey starts from the new base.
- 2026-08-26 — First post-re-fork sync. Merged `3c4288dde..f8ec870f3`
  (20 commits, 11 conflicts). Fork migrations renumbered 404-408 → 432-436.
- 2026-08-27 — Second sync. Merged `f8ec870f3..5fa65bd12` (28 commits, 10
  conflicts). Fork migrations moved to the reserved 9001+ range.
- 2026-08-30 — Third sync. Merged `5fa65bd12..15280617b` (28 commits, zero
  conflicts, no migration collision). Upstream fixed the ghsnapshot flake.
