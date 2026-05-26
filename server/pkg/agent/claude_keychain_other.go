//go:build !darwin

package agent

// readHostClaudeOAuthToken is a no-op on non-Darwin platforms. Linux and
// Windows Claude Code persists the OAuth token to
// `$CLAUDE_CONFIG_DIR/.credentials.json` — a file that lives inside the
// config dir, so mirrorHostClaudeExceptSkills already symlinks it into the
// per-run scratch dir alongside everything else. The isolated child finds
// it via the normal config-dir path; we do not need to surface anything
// through CLAUDE_CODE_OAUTH_TOKEN here.
//
// The macOS keychain workaround exists specifically because that platform
// derives the keychain-lookup suffix from the config-dir path — see
// claude_keychain_darwin.go for the full story.
func readHostClaudeOAuthToken() (string, error) {
	return "", nil
}
