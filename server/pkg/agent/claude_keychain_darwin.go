//go:build darwin

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// claudeKeychainServiceName is the macOS keychain service name that
// `claude /login` writes to when the user authenticates against
// claude.ai. The payload is a JSON blob whose `claudeAiOauth.accessToken`
// field is what we surface to the isolated child.
const claudeKeychainServiceName = "Claude Code-credentials"

// claudeKeychainReadTimeout caps how long we wait on `/usr/bin/security`.
// Reads are normally instantaneous when the ACL already trusts the caller;
// when macOS surfaces a "allow access" prompt this gives a present user
// enough time to click through. We do NOT want to wait forever: a headless
// run with no one at the console would otherwise stall the agent task
// indefinitely on the very first run.
const claudeKeychainReadTimeout = 10 * time.Second

// readHostClaudeOAuthToken reads the host machine's Claude Code OAuth
// access token from the macOS login keychain (entry
// `Claude Code-credentials`). Returns ("", nil) when the token is not
// available — entry missing, user denied / ignored the keychain prompt,
// payload missing `claudeAiOauth.accessToken`, etc. — so callers in the
// isolation path can fall back to whatever auth the child CLI can find on
// its own (typically ANTHROPIC_API_KEY for headless / API-key-only hosts).
//
// Why this exists: Claude Code 2.x derives the keychain-lookup suffix from
// SHA-256(CLAUDE_CONFIG_DIR)[:8], so the unsuffixed `Claude Code-credentials`
// entry is read only when CLAUDE_CONFIG_DIR is unset or equals the install
// default. The moment MUL-2603 isolation points the child at a scratch dir,
// the CLI starts asking for `Claude Code-credentials-<other-hash>`, finds
// nothing, and exits "Not logged in · Please run /login" even though the
// host's OAuth token is sitting in the default entry. Surfacing the token
// through CLAUDE_CODE_OAUTH_TOKEN sidesteps the suffix scheme entirely.
//
// Returns a non-nil error only for unexpected system failures (e.g.
// `/usr/bin/security` not found) — the caller logs those at warn level so
// operators can diagnose. The common "no token available" case stays
// silent because we cannot tell "user is API-key-only" apart from "user
// denied access" by exit code, and we do not want to spam the daemon log
// on every isolated run for the former.
func readHostClaudeOAuthToken() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), claudeKeychainReadTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", claudeKeychainServiceName, "-w")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Non-zero exit from `security` covers entry-missing, denied,
			// and timeout — all soft failures from our perspective.
			return "", nil
		}
		// `/usr/bin/security` itself is missing or unexecutable — this is
		// unexpected on macOS and worth surfacing so the caller can log it.
		return "", fmt.Errorf("invoke security CLI: %w", err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return "", nil
	}
	var payload struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		// A malformed payload is almost certainly a Claude Code upstream
		// change to the keychain schema; downgrade to soft failure so
		// the child can still attempt API-key auth, but do not pretend
		// the schema is fine — surface so we update the parser.
		return "", fmt.Errorf("parse claude keychain payload: %w", err)
	}
	return payload.ClaudeAiOauth.AccessToken, nil
}
