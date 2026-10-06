#!/usr/bin/env bash
#
# install-dotnet.sh — install the .NET runtime that SurvivalcraftNet SERVERS need.
#
# Scope: this installs the .NET *runtime* only (--runtime dotnet). It does NOT
# install the SDK, and it deliberately does NOT install Microsoft.WindowsDesktop.App
# — that framework is a Windows-only, CLIENT-side requirement (see
# docs/环境与可运行性验证.md §2). The server never needs it.
#
# The panel itself does not need .NET at all; only game instances do.
#
# Idempotent: if a suitable runtime is already present, the script exits 0
# without touching anything. Re-running is always safe.
#
# Usage:
#   scripts/install-dotnet.sh [options]
#
# Options:
#   --dir DIR        install into DIR                (default: $HOME/.dotnet)
#   --channel VER    release channel                 (default: 10.0)
#   --dry-run        print what would happen, change nothing
#   --force          install even if a suitable runtime exists
#   --arch ARCH      x64|arm64|arm          (default: host architecture)
#   -h, --help       show this help
#
# Environment:
#   DOTNET_INSTALL_DIR   same as --dir
#   DOTNET_CHANNEL       same as --channel
#
# Exit codes:
#   0  a suitable runtime is present (installed now or already there)
#   1  installation failed
#   2  usage error / unsupported platform

set -euo pipefail

DEFAULT_CHANNEL="10.0"
INSTALL_DIR="${DOTNET_INSTALL_DIR:-$HOME/.dotnet}"
CHANNEL="${DOTNET_CHANNEL:-$DEFAULT_CHANNEL}"
DRY_RUN=0
FORCE=0
ARCH=""

usage() { sed -n '2,32p' "$0" | sed 's/^# \?//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir)      INSTALL_DIR="${2:?--dir needs a value}"; shift 2 ;;
    --channel)  CHANNEL="${2:?--channel needs a value}";  shift 2 ;;
    --arch)     ARCH="${2:?--arch needs a value}";        shift 2 ;;
    --dry-run)  DRY_RUN=1; shift ;;
    --force)    FORCE=1; shift ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "install-dotnet.sh: unknown option '$1'" >&2; usage >&2; exit 2 ;;
  esac
done

log()  { printf '==> %s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Platform guard — the project targets Linux only.
# ---------------------------------------------------------------------------
case "$(uname -s)" in
  Linux) : ;;
  *) die "unsupported OS '$(uname -s)': scnetm targets Linux only. On Windows the CLIENT needs Microsoft.WindowsDesktop.App, which this script never installs." ;;
esac

case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH="x64" ;;
  aarch64|arm64) HOST_ARCH="arm64" ;;
  armv7l|armv6l) HOST_ARCH="arm" ;;
  *) die "unsupported architecture '$(uname -m)'" ;;
esac
[[ -z "$ARCH" ]] && ARCH="$HOST_ARCH"

# ---------------------------------------------------------------------------
# Is a suitable runtime already present?
#
# "Suitable" means: some dotnet on PATH or in INSTALL_DIR reports a
# Microsoft.NETCore.App runtime whose major version matches CHANNEL.
# ---------------------------------------------------------------------------
channel_major() {
  # "10.0" -> "10"; "8.0.1" -> "8"; "LTS" -> "" (unknown, accept anything)
  local c="${1%%.*}"
  [[ "$c" =~ ^[0-9]+$ ]] && printf '%s' "$c" || printf ''
}

find_dotnet() {
  # Prefer the requested install dir, then PATH.
  if [[ -x "$INSTALL_DIR/dotnet" ]]; then
    printf '%s' "$INSTALL_DIR/dotnet"; return 0
  fi
  if command -v dotnet >/dev/null 2>&1; then
    command -v dotnet; return 0
  fi
  return 1
}

have_suitable_runtime() {
  local dn major
  dn="$(find_dotnet)" || return 1
  # `dotnet --list-runtimes` prints lines like:
  #   Microsoft.NETCore.App 10.0.0 [/path/to/shared/Microsoft.NETCore.App]
  local runtimes
  runtimes="$("$dn" --list-runtimes 2>/dev/null || true)"
  [[ -z "$runtimes" ]] && return 1

  major="$(channel_major "$CHANNEL")"
  if [[ -z "$major" ]]; then
    # Unknown channel form: any Microsoft.NETCore.App counts.
    grep -q '^Microsoft\.NETCore\.App ' <<<"$runtimes"
    return $?
  fi
  grep -qE "^Microsoft\.NETCore\.App ${major}\." <<<"$runtimes"
}

if (( ! FORCE )) && have_suitable_runtime; then
  DN="$(find_dotnet)"
  log "a suitable .NET runtime is already installed; nothing to do"
  log "  dotnet:   $DN"
  log "  version:  $("$DN" --version 2>/dev/null || echo unknown)"
  log "  runtimes:"
  "$DN" --list-runtimes 2>/dev/null | sed 's/^/    /' || true
  log "re-run with --force to install anyway"
  exit 0
fi

# ---------------------------------------------------------------------------
# Locate dotnet-install.sh
#
# Prefer a local copy (offline-friendly, and lets ops pin a reviewed script);
# otherwise download it. The script is Microsoft's official installer.
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLER=""

for cand in "$SCRIPT_DIR/dotnet-install.sh" "$SCRIPT_DIR/vendor/dotnet-install.sh"; do
  if [[ -f "$cand" ]]; then INSTALLER="$cand"; break; fi
done

TMP_INSTALLER=""
cleanup() { [[ -n "$TMP_INSTALLER" && -f "$TMP_INSTALLER" ]] && rm -f "$TMP_INSTALLER"; }
trap cleanup EXIT

if [[ -z "$INSTALLER" ]]; then
  command -v curl >/dev/null 2>&1 || die "curl not found and no local dotnet-install.sh available"
  TMP_INSTALLER="$(mktemp "${TMPDIR:-/tmp}/dotnet-install.XXXXXX.sh")"
  INSTALLER="$TMP_INSTALLER"
  log "downloading dotnet-install.sh from https://dot.net/v1/dotnet-install.sh"
  if (( DRY_RUN )); then
    log "  (dry-run: skipping download)"
  else
    curl -fsSL --retry 3 --retry-delay 2 -o "$INSTALLER" https://dot.net/v1/dotnet-install.sh \
      || die "failed to download dotnet-install.sh (no network?)"
    chmod +x "$INSTALLER"
    log "  saved to $INSTALLER ($(wc -c <"$INSTALLER") bytes)"
  fi
fi

# ---------------------------------------------------------------------------
# Install
# ---------------------------------------------------------------------------
log "installing .NET runtime"
log "  channel:  $CHANNEL"
log "  runtime:  dotnet            (Microsoft.NETCore.App — NOT WindowsDesktop, which is client-only)"
log "  arch:     $ARCH"
log "  dir:      $INSTALL_DIR"

INSTALL_ARGS=(
  --channel "$CHANNEL"
  --runtime dotnet
  --architecture "$ARCH"
  --install-dir "$INSTALL_DIR"
  --no-path
)

if (( DRY_RUN )); then
  log "dry-run; would execute:"
  printf '    %s %s\n' "$INSTALLER" "${INSTALL_ARGS[*]}"
  exit 0
fi

mkdir -p "$INSTALL_DIR"
"$INSTALLER" "${INSTALL_ARGS[@]}" || die "dotnet-install.sh failed"

# ---------------------------------------------------------------------------
# Verify what we just did, rather than trusting the installer's exit code.
# ---------------------------------------------------------------------------
DOTNET_BIN="$INSTALL_DIR/dotnet"
[[ -x "$DOTNET_BIN" ]] || die "expected $DOTNET_BIN to exist after install, but it does not"

log "installed; verifying"
"$DOTNET_BIN" --list-runtimes 2>/dev/null | sed 's/^/    /' || true

if ! "$DOTNET_BIN" --list-runtimes 2>/dev/null | grep -q '^Microsoft\.NETCore\.App '; then
  die "no Microsoft.NETCore.App runtime found after install — the game server would not start"
fi

cat <<EOF

==> done

Make the runtime visible to the panel. Either:

  1. Point the panel at it explicitly (recommended — no PATH games):
       # config.yaml
       dotnet_path: $DOTNET_BIN

  2. Or put it on PATH for the panel's user:
       export DOTNET_ROOT="$INSTALL_DIR"
       export PATH="\$DOTNET_ROOT:\$PATH"

Verify:
  "$DOTNET_BIN" --list-runtimes
  scripts/verify-env.sh

Note: the panel serves /healthz without .NET. Only starting a game instance needs it.
EOF
