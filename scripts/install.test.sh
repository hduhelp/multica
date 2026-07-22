#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Build a self-contained sandbox with stub `curl` and a tarball that the
# release-binary fallback path will download. Each test supplies its own
# `brew` stub to model a specific Homebrew failure mode.
_setup_sandbox() {
  local tmp="$1"
  local stub_bin="$tmp/stub-bin"
  local install_bin="$tmp/install-bin"
  local payload_dir="$tmp/payload"
  mkdir -p "$stub_bin" "$install_bin" "$payload_dir"

  cat >"$payload_dir/multica" <<'STUB'
#!/usr/bin/env bash
echo "multica v0.3.2 (commit: test)"
STUB
  chmod +x "$payload_dir/multica"
  tar -czf "$tmp/multica.tar.gz" -C "$payload_dir" multica

  cat >"$stub_bin/curl" <<'STUB'
#!/usr/bin/env bash
if [[ "$*" == *"multica-ai/multica"* ]]; then
  echo "installer must not download from upstream multica-ai/multica" >&2
  exit 64
fi
if [[ "$*" != *"hduhelp/multica"* ]]; then
  echo "installer expected an hduhelp/multica URL, got: $*" >&2
  exit 65
fi
if [[ "$*" == *"-sI"* ]]; then
  printf 'HTTP/2 302\r\nlocation: https://github.com/hduhelp/multica/releases/tag/v0.3.2\r\n'
  exit 0
fi

out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -z "$out" ]]; then
  echo "stub curl expected -o" >&2
  exit 2
fi
cp "$MULTICA_TEST_ARCHIVE" "$out"
STUB
  chmod +x "$stub_bin/curl"
}

_run_installer() {
  local tmp="$1"
  local out="$tmp/install.out"
  local err="$tmp/install.err"
  if ! PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    bash "$ROOT_DIR/scripts/install.sh" >"$out" 2>"$err"; then
    echo "install.sh exited non-zero" >&2
    cat "$out" >&2 || true
    cat "$err" >&2 || true
    return 1
  fi

  if [[ ! -x "$tmp/install-bin/multica" ]]; then
    echo "expected fallback binary at $tmp/install-bin/multica" >&2
    cat "$out" >&2 || true
    cat "$err" >&2 || true
    return 1
  fi

  if ! grep -q "Homebrew output (last 80 lines):" "$err"; then
    echo "expected diagnostic tail in stderr" >&2
    cat "$err" >&2 || true
    return 1
  fi
}

test_brew_install_failure_falls_back_to_release_binary() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  tap)
    [[ "${2:-}" == "hduhelp/tap" ]] || exit 66
    exit 0
    ;;
  install)
    [[ "${2:-}" == "hduhelp/tap/multica" ]] || exit 67
    echo "simulated brew install failure" >&2
    exit 42
    ;;
  list)
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  _run_installer "$tmp"
}

test_brew_tap_failure_falls_back_to_release_binary() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  tap)
    echo "simulated brew tap failure" >&2
    exit 17
    ;;
  *)
    echo "brew $* should not be reached after tap failure" >&2
    exit 99
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  _run_installer "$tmp"
}

test_remote_ssh_install_prints_token_login_hint() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  tap)
    exit 0
    ;;
  install)
    echo "simulated brew install failure" >&2
    exit 42
    ;;
  list)
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  (
    export SSH_CONNECTION="192.0.2.10 54321 198.51.100.20 22"
    _run_installer "$tmp"
  )

  if ! grep -q "Looks like a remote/SSH session" "$tmp/install.out"; then
    echo "expected remote/SSH token-login hint in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if ! grep -q "https://multica.ai/settings?tab=tokens" "$tmp/install.out"; then
    echo "expected direct API Tokens settings URL in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if ! grep -q "Settings > API Tokens" "$tmp/install.out"; then
    echo "expected API Tokens tab name in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if ! grep -q "multica login --token <YOUR_TOKEN>" "$tmp/install.out"; then
    echo "expected token login command in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if grep -q "multica config set server_url" "$tmp/install.out"; then
    echo "did not expect default cloud server config command in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if grep -q "multica config set app_url" "$tmp/install.out"; then
    echo "did not expect default cloud app config command in installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
}

test_local_install_does_not_print_token_login_hint() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
case "${1:-}" in
  tap)
    exit 0
    ;;
  install)
    echo "simulated brew install failure" >&2
    exit 42
    ;;
  list)
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  (
    unset SSH_CONNECTION SSH_CLIENT SSH_TTY
    _run_installer "$tmp"
  )

  if grep -q "Looks like a remote/SSH session" "$tmp/install.out"; then
    echo "did not expect remote/SSH token-login hint in local installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
  if grep -q "multica login --token <YOUR_TOKEN>" "$tmp/install.out"; then
    echo "did not expect token login command in local installer output" >&2
    cat "$tmp/install.out" >&2 || true
    return 1
  fi
}

test_distribution_references_use_hduhelp() {
  local upstream_pattern='raw.githubusercontent.com/multica-ai/multica|api.github.com/repos/multica-ai/multica|github.com/multica-ai/multica/releases|multica-ai/tap'
  if grep -En "$upstream_pattern" \
    "$ROOT_DIR/scripts/install.sh" \
    "$ROOT_DIR/scripts/install.ps1" \
    "$ROOT_DIR/README.md" \
    "$ROOT_DIR/Makefile" | grep -v 'LEGACY_BREW_PACKAGE='; then
    echo "distribution references must use hduhelp/multica" >&2
    return 1
  fi
}

test_upstream_brew_install_migrates_to_hduhelp_tap() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cp "$tmp/payload/multica" "$tmp/install-bin/multica"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$MULTICA_TEST_BREW_LOG"
case "${1:-}" in
  list)
    [[ "${2:-}" == "multica-ai/tap/multica" ]]
    ;;
  unpin|uninstall)
    [[ "${2:-}" == "multica-ai/tap/multica" ]]
    ;;
  tap)
    [[ "${2:-}" == "hduhelp/tap" ]]
    ;;
  install)
    [[ "${2:-}" == "hduhelp/tap/multica" ]]
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    MULTICA_TEST_BREW_LOG="$tmp/brew.log" \
    bash "$ROOT_DIR/scripts/install.sh" >"$tmp/install.out" 2>"$tmp/install.err"

  grep -qx 'unpin multica-ai/tap/multica' "$tmp/brew.log"
  grep -qx 'uninstall multica-ai/tap/multica' "$tmp/brew.log"
  grep -qx 'tap hduhelp/tap' "$tmp/brew.log"
  grep -qx 'install hduhelp/tap/multica' "$tmp/brew.log"
}

test_installed_but_unlinked_formula_is_relinked() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  cat >"$tmp/stub-bin/brew" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$MULTICA_TEST_BREW_LOG"
case "${1:-}" in
  tap)
    exit 0
    ;;
  install)
    exit 1
    ;;
  list)
    [[ "${2:-}" == "hduhelp/tap/multica" ]]
    ;;
  link)
    [[ "${2:-}" == "--overwrite" && "${3:-}" == "hduhelp/tap/multica" ]] || exit 68
    cp "$MULTICA_TEST_PAYLOAD" "$MULTICA_TEST_INSTALL_BIN/multica"
    chmod +x "$MULTICA_TEST_INSTALL_BIN/multica"
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "$tmp/stub-bin/brew"

  PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    MULTICA_TEST_PAYLOAD="$tmp/payload/multica" \
    MULTICA_TEST_INSTALL_BIN="$tmp/install-bin" \
    MULTICA_TEST_BREW_LOG="$tmp/brew.log" \
    bash "$ROOT_DIR/scripts/install.sh" >"$tmp/install.out" 2>"$tmp/install.err"

  grep -qx 'link --overwrite hduhelp/tap/multica' "$tmp/brew.log"
  test -x "$tmp/install-bin/multica"
}

test_brew_install_failure_falls_back_to_release_binary
test_brew_tap_failure_falls_back_to_release_binary
test_remote_ssh_install_prints_token_login_hint
test_local_install_does_not_print_token_login_hint
test_distribution_references_use_hduhelp
test_upstream_brew_install_migrates_to_hduhelp_tap
test_installed_but_unlinked_formula_is_relinked
echo "install.sh tests passed"
