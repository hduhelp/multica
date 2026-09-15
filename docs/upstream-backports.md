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
| **Last surveyed upstream** | **`96aa80ff9`** (2026-09-16) — merged |
| **Fork migration range** | **9001+** — never renumber into upstream's range again |

> Everything at or below `96aa80ff9` is upstream code we already have.
> Next survey: `git log 96aa80ff9..upstream/main`.

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

## 2026-09-09 — fourth sync (`15280617b..b5a7ee1e0`, 135 commits)

Nine days and 135 commits — five times the previous two syncs — for 11
conflicts and 16 upstream migrations (441-456). None of them touched the
reserved 9001+ range.

Migration numbering has now been a non-event for two syncs running. The
tax the first two paid was real: a renumber re-runs the SQL under a new
stem in every environment that already applied the old one.

### What the merge could not see

Three breaks landed with zero conflict markers, all caught by build or
typecheck rather than by git:

| Break | How it happened |
| --- | --- |
| `Config.LogPath` vanished | The conflict was "our two lines vs upstream's one"; taking upstream's side wholesale dropped the fork's field. |
| `NewFeishuResolverSet` arity | Upstream added a test calling it with six arguments; this fork's agent-sender work made it eight. |
| `initiateListModels` split in half | A mechanical union cut through a method upstream had rewritten, leaving the fork's old signature stranded above the new body. |

The same shape appeared again in `client.test.ts`, where a union dropped
a `describe` block's closing braces. **A union merge is only safe when
both sides are complete statements** — where a conflict cuts through one,
it has to be resolved by reading.

### Dead code the sync exposed

`GetChannelInstallationByBotOpenID` and its store wrapper were removed.
They were left behind by 0.6.2, which replaced open_id matching with the
chat bot roster; upstream deleting an adjacent query surfaced them as an
unreferenced pair.

### Test suites

`internal/handler` and `packages/views` both fail a rotating handful of
tests when their whole suite runs in parallel, and pass in isolation. The
handler failures were confirmed against the pre-merge commit — a
different set fails there, which is the signature of suite-level
contention rather than a regression. CI, which shards these, is green.

## 2026-09-10 — fifth sync (`b5a7ee1e0..9fab6da91`, 15 commits)

Zero conflicts and a clean build on the first try — the first sync since
the re-fork where nothing had to be hand-resolved. No new migration
numbers.

### One data divergence worth knowing about

Upstream edited migration **451**, which this fork had already applied in
0.6.5, to drop its backfill:

```sql
-UPDATE agent_task_queue SET comment_thread_id = comment_thread_root_id(...)
+-- Existing rows intentionally retain a NULL thread scope.
```

A stem already in `schema_migrations` does not re-run, so the edit only
reaches fresh installs. Production therefore has `comment_thread_id`
populated on historical rows where an upstream install would have NULL.
That is a superset, not a conflict: 452's unique index built without
collision, and upstream's own note says pre-migration tasks drain under
the claim fence regardless. Nothing to undo — recorded so a future schema
comparison against upstream does not read as drift needing repair.

---

## 2026-09-15 — sixth sync (`9fab6da91..cf52ba33c`, 84 commits)

22 new migrations (457-478), none colliding with 9001+. Four conflicts,
three of them in the Lark outbound path — because upstream built its own
version of the threading feature this fork carries.

### Upstream has absorbed most of the Lark reply work

`sendWithThreadFallback` is now upstream's `sendWithReplyFallback`, same
body; `threadReplyTarget` was generalized. Post-merge, upstream's
`outbound.go` has no function this fork lacks, and this fork has two it
does not: `sendAgentReply` / `sendPlainReply`, the adaptive format (plain
prose as text, markdown as a headerless schema-2.0 card) with a degrade
path when the card BUILD fails. This fork's `defaultRenderer` also points
at the real product card; upstream's is still the placeholder its own
comment says will be replaced.

### Three things upstream does better, now inherited

1. **Per-task trigger snapshot** (migration 461). This fixes a real bug in
   the fork's design, which read `binding.LastMessageID` — one row per
   chat_session, so only ever the LATEST trigger. A debounced or slow run
   would reply to and quote a message that arrived after the one it was
   answering. `binding` is now built from `channel_task_delivery`.
   Upstream's migration header records the two wrong designs they passed
   through first, including why resolving the open_id from
   `initiator_user_id` at send time is unsound: `channel_user_binding` is
   unique on `(installation_id, channel_user_id)`, not on the Multica user.
2. **Native `<at>` mention** of the asker, never inferred from the "@name"
   in the model's prose — duplicate or guessed names would ping the wrong
   colleague. Two wire shapes: `<at user_id=…>` for text, `<at id=…>` for
   the schema-2.0 card.
3. **`topicSendWithoutTrigger`** — a failure mode this fork had not
   considered. A topic-isolated session with no trigger falls through to
   the chat-level send, so an answer to a question asked in one topic
   surfaces in the main group. Upstream declines to send instead.

### The divergence that stays: this fork opens the 话题

Upstream replies natively in an ordinary group and leaves
`reply_in_thread` false, so the whole group shares ONE session. This fork
sends it true: `larkSessionRouting` keys a group conversation on the
thread ROOT message, and the root only becomes a thread because of that
send. Marked `FORK DIVERGENCE` in both `threadReplyTarget` and
`inboundReplyTarget`, which stay in lockstep because a user cannot tell
which one answered them.

**Decided 2026-09-15: keep it.** Upstream's native mention solves "which
question is this answering", which was half the reason for topics. It does
not solve the other half — in a busy group upstream's model puts every
member's questions in one session, so context bleeds between them. The
cost of keeping it is conflicts in this file on syncs that touch the reply
path (3 of 4 this time) plus a one-time rekey of live sessions if it is
ever reversed. Do not re-litigate per sync; reopen only if topic-per-@
turns out to bother users.

### Two breaks the merge did not show

Both surfaced only at build/test, not as conflicts: `sendAgentReply` still
called the old `sendWithThreadFallback` name, and
`NewRedisRuntimeCommandStore` took `*redis.Client` where upstream widened
the router's client to `redis.UniversalClient`. The store was widened to
match rather than cast at the call site.

### The additive lint fired, and the migration says so itself

`468_drop_reference_only_column` drops a column whose own header reads:
"It may only run once every instance of the previous release is gone: an
older instance still names the column in its link INSERT." The deployed
release did read it — two `WHERE … AND NOT ipr.reference_only` clauses in
`github.sql` — and the code that stops using it arrived in this same
merge. Rolled anyway; see the release commit for the comparison. Result
was zero 5xx.

### A CI check that does not run locally

`scripts/check-ui-radius-tokens.mjs` arrived in this sync and failed on
two of this fork's own components. It is a standalone node script in the
`frontend-build` job, outside `typecheck` / `test`, so a local green run
proves nothing about it. The rest of that job's steps are worth running
before a sync lands: the two `check-ui-*.mjs` scripts, the three
`scripts/*.test.sh`, and `pnpm generate:reserved-slugs`.

---

## 2026-09-16 — seventh sync (`cf52ba33c..96aa80ff9`, 25 commits)

Two conflicts, both import-list unions in `packages/core/api`, and
upstream did not touch `lark/` at all. 12 new migrations (479-490), none
colliding with 9001+.

### Upstream reversed a feature this fork had already applied

475-477 were **emptied** and 478 reverted: the `triage` status-key
reservation is gone (MUL-7400). Emptying a migration only reaches
databases that have not run it, and upstream's own note says "production
included, since releases deploy from tags and no tag carries them".

**This fork deploys from main merges, so it is the other group** — 0.6.7
ran 475-478 with their original bodies. Upstream wrote `490` for exactly
that case, and it works: verified by migrating a database to the
pre-merge state, confirming `issue_status_key_not_reserved` was present,
then migrating forward and watching 490 drop it.

**One residue 490 does not repair.** 477/478 also rewrote
`issue_effective_status`, and this fork's copy keeps `'triage'` in the
passthrough list where a fresh install does not. It only diverges for a
custom status keyed exactly `triage` whose category is done/cancelled/
closed: this fork would report it open, a fresh install would honour the
category. Production has **zero** such statuses and zero issues with
`status='triage'`, so it is inert today — but 490 removed the reservation
that kept it impossible.

Deliberately NOT fixed with a fork migration. A `9006` redefining a
shared upstream function sorts after every upstream `4xx`, so a future
upstream redefinition would be clobbered by it on fresh installs and in
CI — a permanent hazard traded for an inert one. The repair belongs as a
one-off against production instead; see the change log for when it ran.

### Upstream's new French locale exposed fork debt

`locales/parity.test.ts` failed on 38 keys with no `fr` translation, all
of them this fork's: fixed repo (18), remote daemon control (13), skills
directory import (7). Translated from the English source rather than left
to fall back, because parity is enforced and would fail every sync from
here on.

---

## Dormant: agent-to-agent triggering (needs a Lark scope nobody has granted)

One Multica agent @-mentioning another does **not** trigger a run. The
receiving agent recognizes the @ fine; it cannot recognize that the sender
is one of this workspace's own agents, so the message is dropped as an
unbound stranger.

The code for it shipped in 0.6.2 and is inert, not broken. Recognizing a
bot sender requires reading the chat's bot roster
(`im/v1/chats/{id}/members/bots`), which needs **`im:chat:readonly`** on
**every** participating Lark app — the receiver reads the roster to learn
the sender's name, and each candidate installation reads the same roster
to identify itself by the `bot_open_id` it already stores. As of
2026-09-09 only `cli_aa07a5faf578dd1c` has it; granting was judged not
worth the effort. Granting it later (plus publishing a new app version)
is the only step needed — no code change, no release.

Why the roster and not an id comparison: a Lark open_id is scoped to the
app that observes it, so the same bot is a different open_id to every app
that can see it. `union_id` would be cross-app stable but cannot be
obtained for a bot — `contact/v3/users/batch` rejects a bot open_id with
41012, which is why every installation's `bot_union_id` is NULL. The
roster's `bot_name` is the only identifier that means the same thing to
every app.

Known unfixed side effect: without that scope a bot sender is
indistinguishable from an unbound person, so the receiving agent DMs it a
"bind your account" card it can never act on. A per-open_id cooldown on
that prompt would cap the noise without needing any scope.

---

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
- 2026-09-09 — Fourth sync. Merged `15280617b..b5a7ee1e0` (135 commits, 11
  conflicts, no migration collision).
- 2026-09-10 — Fifth sync. Merged `b5a7ee1e0..9fab6da91` (15 commits, zero
  conflicts, no hand resolution).
- 2026-09-15 — Sixth sync. Merged `9fab6da91..cf52ba33c` (84 commits, 4
  conflicts). Upstream absorbed the Lark reply work; topic opening kept as a
  marked divergence.
- 2026-09-16 — Seventh sync. Merged `cf52ba33c..96aa80ff9` (25 commits, 2
  conflicts). Upstream reversed the triage reservation; 490 repaired this
  fork's applied copy. French added for 38 fork-only keys.
