package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// destructiveDDL matches the statements that make a release unsafe to deploy
// with a rolling update: they remove or narrow something the PREVIOUS version
// of the server still reads. During a rolling update the new pod migrates at
// boot while the old pod keeps serving against the migrated schema, so a
// dropped column turns every query that selects it into a 500 until the old
// pod is terminated.
//
// Adding things is safe — the old code simply does not know about them.
var destructiveDDL = []struct {
	name string
	re   *regexp.Regexp
}{
	{"DROP COLUMN", regexp.MustCompile(`(?i)\bDROP\s+COLUMN\b`)},
	{"DROP TABLE", regexp.MustCompile(`(?i)\bDROP\s+TABLE\b`)},
	{"RENAME COLUMN", regexp.MustCompile(`(?i)\bRENAME\s+COLUMN\b`)},
	{"RENAME TO", regexp.MustCompile(`(?i)\bALTER\s+TABLE[^;]*\bRENAME\s+TO\b`)},
	{"ALTER COLUMN … TYPE", regexp.MustCompile(`(?i)\bALTER\s+COLUMN\b[^;]*\bTYPE\b`)},
	{"SET NOT NULL", regexp.MustCompile(`(?i)\bSET\s+NOT\s+NULL\b`)},
}

// Deliberately NOT listed: DROP CONSTRAINT. Removing a constraint only ever
// makes writes more permissive, so the previous version of the server keeps
// working, and the overwhelmingly common use here is the widen-an-enum pattern
// — drop a CHECK and immediately re-add it with more allowed values (see
// migrations 366, 403). Flagging it would fire on a dozen safe migrations and
// train everyone to append to the ledger without reading. The case this misses
// is a CHECK re-added NARROWER than before, which would reject writes the old
// code still makes; that is not reliably detectable from the text.

// knownDestructiveUpMigrations is the set that already shipped. It is a ledger,
// not a permission list: the point of pinning it is that a NEW destructive
// migration fails this test and has to be acknowledged, because the release
// carrying it must deploy with strategy: Recreate (see backend.strategy in
// deploy/helm/multica/values.yaml) rather than a rolling update.
//
// When you add one deliberately, append it here and say so in the release notes.
var knownDestructiveUpMigrations = map[string]bool{
	"004_agent_runtime_loop":                     true,
	"007_drop_issue_repository":                  true,
	"008_structured_skills":                      true,
	"025_comment_workspace_id":                   true,
	"029_drop_daemon_pairing":                    true,
	"032_drop_agent_triggers":                    true,
	"043_fix_orphaned_autopilot_runs":            true,
	"046_drop_runtime_usage":                     true,
	"053_drop_orphan_onboarding_current_step":    true,
	"058_drop_autopilot_priority_and_project_id": true,
	"069_drop_task_last_heartbeat":               true,
	"098_user_onboarding_runtime_choice":         true,
	"103_drop_legacy_daily_rollups":              true,
	"104_drop_runtime_timezone":                  true,
	"109_drop_agent_skills_local":                true,
	"112_issue_dates_to_date":                    true,
	"113_lark_inbound_dedup_per_installation":    true,
	"318_drop_workspace_mcp_config":              true,
	"344_plugin_v2_reset":                        true,
	"392_plugin_package_publishing":              true,
	// Upstream renamed agent.starter_prompts -> conversation_starters. A
	// rolling update runs this while the previous version is still selecting
	// the old name, so the release carrying it needs the deploy note below.
	"432_agent_conversation_starters_rename": true,
}

// A destructive migration is not forbidden — sometimes a column really has to
// go. What it must not be is a surprise at deploy time: it decides whether the
// release can roll or has to Recreate, and nobody re-reads 471 files to find
// out.
func TestUpMigrationsAreAdditive(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no migrations found — the glob path is wrong, not the migrations")
	}

	var unlisted, staleEntries []string
	found := map[string]bool{}

	for _, path := range paths {
		stem := strings.TrimSuffix(filepath.Base(path), ".up.sql")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sql := stripSQLComments(string(raw))

		var hits []string
		for _, d := range destructiveDDL {
			if d.re.MatchString(sql) {
				hits = append(hits, d.name)
			}
		}
		if len(hits) == 0 {
			continue
		}
		found[stem] = true
		if !knownDestructiveUpMigrations[stem] {
			unlisted = append(unlisted, stem+" ("+strings.Join(hits, ", ")+")")
		}
	}

	for stem := range knownDestructiveUpMigrations {
		if !found[stem] {
			staleEntries = append(staleEntries, stem)
		}
	}
	sort.Strings(unlisted)
	sort.Strings(staleEntries)

	for _, m := range unlisted {
		t.Errorf("destructive DDL in a new up migration: %s\n"+
			"  A rolling update would run this while the previous server is still serving.\n"+
			"  Deploy the release carrying it with backend.strategy.type=Recreate, then add\n"+
			"  the migration to knownDestructiveUpMigrations in this file.", m)
	}
	for _, m := range staleEntries {
		t.Errorf("knownDestructiveUpMigrations lists %s, which no longer contains destructive DDL — remove the entry", m)
	}
}

// stripSQLComments removes -- line comments and /* */ blocks so that a
// migration DESCRIBING a drop in prose is not mistaken for one performing it.
func stripSQLComments(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	for rest := sql; rest != ""; {
		line, tail, _ := strings.Cut(rest, "\n")
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
		rest = tail
	}
	out := b.String()
	for {
		start := strings.Index(out, "/*")
		if start < 0 {
			break
		}
		end := strings.Index(out[start:], "*/")
		if end < 0 {
			out = out[:start]
			break
		}
		out = out[:start] + " " + out[start+end+2:]
	}
	return out
}
