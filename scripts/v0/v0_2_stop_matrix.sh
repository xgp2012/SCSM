#!/usr/bin/env bash
# V0-2 / V0-7 — stop behaviour and save-write timing, through the PANEL runner.
#
# For each stop method we record:
#   * the instance state transition reported by the API
#   * exit code and duration (from the panel's stop response / state detail)
#   * whether Worlds/*/Project.json and Project.json.bak mtimes advanced
#
# Usage: bash v0_2_stop_matrix.sh <method>
#   method ∈ {graceful|kill|sigterm-direct}
set -uo pipefail
source "$(dirname "$0")/lib.sh"

METHOD="${1:-graceful}"
RUN="/tmp/v0/exp/v02_$METHOD"
rm -rf "$RUN"; mkdir -p "$RUN"

say "V0-2 method=$METHOD  instance=$INST_ID  dir=$V0_INST"

# --- reset the instance working dir to a clean server copy ------------------
pkill -x dotnet 2>/dev/null; sleep 1
rm -rf "$V0_INST"; mkdir -p "$V0_INST"
cp -a /tmp/v0/template/. "$V0_INST/"
rm -rf "$V0_INST/Worlds"
python3 - "$V0_INST/ServerSetting.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/V02",
          "WorldName":"V02Stop","WorldSeed":"555","GameMode":1})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY

# --- start ------------------------------------------------------------------
note "starting instance"
v0_start > "$RUN/start.json" 2>&1; cat "$RUN/start.json" | head -5
ST=$(v0_wait_state 120 running); echo "state after wait: $ST"

PROJ="$V0_INST/Worlds/V02/Project.json"; BAK="$V0_INST/Worlds/V02/Project.json.bak"
note "mtimes at RUNNING (save already written at boot?)"
for f in "$PROJ" "$BAK"; do printf '%-60s %s\n' "$f" "$(v0_mtime "$f")"; done
ls -la --time-style=full-iso "$V0_INST/Worlds/V02/" 2>/dev/null

# --- let it idle, watch for periodic writes ---------------------------------
note "idling 30s to detect periodic save writes"
M0=$(v0_mtime "$PROJ"); B0=$(v0_mtime "$BAK")
for i in 5 10 15 20 25 30; do
  sleep 5
  echo "t=${i}s proj=$(v0_mtime "$PROJ") bak=$(v0_mtime "$BAK")"
done
M1=$(v0_mtime "$PROJ")
[ "$M0" = "$M1" ] && echo "RESULT: no periodic save during 30s idle" || echo "RESULT: periodic save detected"

# --- stop -------------------------------------------------------------------
note "issuing stop (method=$METHOD)"
T0=$(date +%s.%N)
case "$METHOD" in
  graceful)
    # Panel graceful path == send the configured stop command through the
    # command channel, wait, then escalate.  Record what the API reports.
    v0_stop > "$RUN/stop.json" 2>&1
    ;;
  kill)
    v0_kill > "$RUN/stop.json" 2>&1
    ;;
  sigterm-direct)
    # Bypass the panel: SIGTERM the process group directly.
    PGID=$(ps -o pgid= -p "$(pgrep -x dotnet | head -1)" 2>/dev/null | tr -d ' ')
    echo "dotnet pgid=$PGID"; kill -TERM -"$PGID" 2>/dev/null
    v0_stop > "$RUN/stop.json" 2>&1
    ;;
esac
T1=$(date +%s.%N)
cat "$RUN/stop.json" | python3 -m json.tool 2>/dev/null || cat "$RUN/stop.json"

ST=$(v0_wait_state 180); echo "final state: $ST"
echo "wall time for stop call: $(python3 -c "print(f'{$T1-$T0:.2f}s')")"

note "mtimes AFTER stop"
for f in "$PROJ" "$BAK"; do printf '%-60s %s\n' "$f" "$(v0_mtime "$f")"; done
M2=$(v0_mtime "$PROJ"); B2=$(v0_mtime "$BAK")
echo "Project.json advanced by stop? $([ "$M1" != "$M2" ] && echo YES || echo NO)"
echo "Project.json.bak advanced by stop? $([ "$B0" != "$B2" ] && echo YES || echo NO)"

note "full state detail"
v0_state | python3 -m json.tool | head -40
note "panel log tail (stop-related)"
grep -i "stop\|exit\|signal\|kill\|escalat" /tmp/v0/panel.log | tail -20