#!/usr/bin/env bash
# V0 probe library — shared helpers for the phase-0 verification tasks.
#
# Sourced, not executed. Provides panel/WebSocket/instance helpers so that the
# per-task probe scripts stay short and the raw commands stay auditable.
#
# Usage:  source /home/xgp2012/SCNETM/scripts/v0/lib.sh
#
# Environment knobs:
#   PANEL_URL   default http://127.0.0.1:34550
#   V0_DATA     default /tmp/v0/data
#   V0_INST     default /tmp/v0/inst/v0test   (the instance working directory)
#   INST_ID     default 1

set -uo pipefail

PANEL_URL="${PANEL_URL:-http://127.0.0.1:34550}"
V0_DATA="${V0_DATA:-/tmp/v0/data}"
V0_INST="${V0_INST:-/tmp/v0/inst/v0test}"
INST_ID="${INST_ID:-1}"
TOKEN_FILE="${TOKEN_FILE:-/tmp/v0/.data.token}"

# --- output helpers ---------------------------------------------------------
say()  { printf '\n===== %s =====\n' "$*"; }
note() { printf -- '--- %s\n' "$*"; }
ok()   { printf 'PASS: %s\n' "$*"; }
bad()  { printf 'FAIL: %s\n' "$*"; }

# --- auth -------------------------------------------------------------------
v0_token() { cat "$TOKEN_FILE"; }

v0_api() { # v0_api METHOD PATH [JSON]  -> body on stdout, http code on stderr
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -w '\n%{http_code}' -X "$method" "$PANEL_URL$path" \
      -H "Authorization: Bearer $(v0_token)" -H 'Content-Type: application/json' -d "$body"
  else
    curl -s -w '\n%{http_code}' -X "$method" "$PANEL_URL$path" \
      -H "Authorization: Bearer $(v0_token)"
  fi
}

# Extract `.data` using python (no jq dependency guaranteed).
v0_data_json() { python3 -c 'import sys,json;d=json.load(sys.stdin);print(json.dumps(d.get("data",d),ensure_ascii=False))'; }

# --- instance lifecycle -----------------------------------------------------
v0_start() { v0_api POST "/api/v1/instances/$INST_ID/start" '{}' | head -n -1; }
v0_stop()  { v0_api POST "/api/v1/instances/$INST_ID/stop"  '{}' | head -n -1; }
v0_kill()  { v0_api POST "/api/v1/instances/$INST_ID/kill"  '{}' | head -n -1; }
v0_state() { v0_api GET  "/api/v1/instances/$INST_ID" | head -n -1; }

# Wait until the instance leaves `Starting`, or timeout. Prints final state.
#   v0_wait_state <timeout_s> [<grep-pattern-for-state>]
v0_wait_state() {
  local timeout="${1:-120}" want="${2:-}" waited=0 st
  while [ "$waited" -lt "$timeout" ]; do
    st=$(v0_state | python3 -c 'import sys,json;print(json.load(sys.stdin)["data"]["state"])' 2>/dev/null || echo "?")
    if [ -n "$want" ]; then
      [ "$st" = "$want" ] && { echo "$st"; return 0; }
    else
      case "$st" in Starting|Stopping) ;; *) echo "$st"; return 0 ;; esac
    fi
    sleep 2; waited=$((waited+2))
  done
  echo "TIMEOUT($st)"; return 1
}

# --- console / command channel ---------------------------------------------
# ws_console_raw <seconds> [command] — connect to the console WS, optionally send
# one command (a full line, newline added), print every frame verbatim as
# `<type> <payload>`; exits after <seconds>.  Requires python3 + websocket-client
# OR falls back to the bundled pure-python ws client below.
v0_ws() { python3 /home/xgp2012/SCNETM/scripts/v0/ws_console.py "$@"; }

# --- server-direct helpers --------------------------------------------------
# Paths inside the instance working directory.
v0_world_dir() { ls -d "$V0_INST"/Worlds/*/ 2>/dev/null | head -1; }
v0_project()   { local d; d=$(v0_world_dir); [ -n "$d" ] && echo "${d}Project.json"; }

# Print mtime (epoch.ns) of a file, or MISSING.
v0_mtime() { [ -e "$1" ] && stat -c '%Y.%y' "$1" 2>/dev/null || echo MISSING; }

# Count ESC (0x1B) bytes in a file.
v0_esc_count() { [ -f "$1" ] && python3 -c "import sys;print(open(sys.argv[1],'rb').read().count(b'\x1b'))" "$1" || echo MISSING; }

# Dump GameInfo fields of the active world's Project.json (BOM-tolerant).
v0_gameinfo() {
  local p; p=$(v0_project)
  [ -n "$p" ] || { echo "NO PROJECT"; return 1; }
  python3 - "$p" <<'PY'
import json,sys
d=json.load(open(sys.argv[1],encoding='utf-8-sig'))
gi=d.get('Subsystems',{}).get('GameInfo',{})
for k in sorted(gi):
    print(f"{k:34} {json.dumps(gi[k],ensure_ascii=False)}")
PY
}
