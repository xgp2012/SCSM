#!/usr/bin/env bash
# V0-1(b) precision pass — nail the EXACT command-fifo contract.
#
# From the first pass we know:
#   * the server does NOT create command-fifo itself; it must pre-exist
#   * /help, /player list 0, /time, /auth all produce replies on STDOUT (the log)
#   * a bare `help` was echoed back to the FIFO instead of executed
#   * `player list 0` (no slash) -> "unknown command: player"
#
# This pass isolates: slash vs no-slash, echo behaviour, the reader/writer
# direction, whether the FIFO must stay open, and blocking semantics.
#
# Usage: bash v0_1_fifo_precise.sh
set -uo pipefail

BASE=/tmp/v0/exp/v01_fifo2
SRC=/tmp/scnetsrv/net10.0
DOTNET=/tmp/dotnet10/dotnet
export DOTNET_ROOT=/tmp/dotnet10
rm -rf "$BASE"; mkdir -p "$BASE"
D="$BASE/srv"; mkdir -p "$D"; cp -a "$SRC"/. "$D/"; rm -rf "$D/Worlds"
python3 - "$D/ServerSetting.json" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/F2",
          "WorldName":"FifoPrecise","WorldSeed":"777","GameMode":1})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY

cd "$D"
mkfifo command-fifo
# start with the FIFO as stdin too? No — this server was started WITHOUT the fifo
# as stdin, i.e. the FIFO is a pure side channel.
setsid env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color LANG=C.UTF-8 LC_ALL=C.UTF-8 \
    "$DOTNET" Survivalcraft.dll > "$BASE/out.raw" 2>&1 < /dev/null &
RUNNER=$!
for i in $(seq 1 120); do grep -q 'Entered screen "Game"' "$BASE/out.raw" 2>/dev/null && break; sleep 1; done
echo "ready after ${i}s"

PID=$(pgrep -x dotnet | while read p; do [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D" ] && echo $p; done | head -1)
echo "pid=$PID"
echo "--- who has command-fifo open? ---"
ls -l /proc/$PID/fd/ 2>/dev/null | grep -i fifo || echo "(server has NO fd on command-fifo at idle)"
echo "--- fifo inode ---"; stat -c '%i %n' command-fifo

run_case() { # run_case <label> <payload-with-newline-escaped>
  local label="$1" payload="$2"
  local mark; mark=$(wc -l < "$BASE/out.raw")
  echo
  echo "=================== CASE: $label   (payload=$(printf '%q' "$payload")) ==================="
  # Open the FIFO for write with a timeout so a blocking open is visible.
  timeout 5 bash -c "printf '%s\n' '$payload' > '$D/command-fifo'" ; local rc=$?
  echo "writer rc=$rc  (124 = timed out => open/block problem)"
  sleep 4
  echo "--- new stdout lines ---"
  tail -n "+$((mark+1))" "$BASE/out.raw"
  echo "--- (end) ---"
}

run_case "slash-help"       "/help"
run_case "bare-help"        "help"
run_case "slash-playerlist" "/player list 0"
run_case "slash-pl"         "/pl"
run_case "slash-pws"        "/pws"
run_case "slash-say-cn"     "/say 你好世界"
run_case "bogus-slash"      "/nosuchcmd"
run_case "empty"            ""

echo
echo "=== ALL OUTPUT TAIL ==="; tail -40 "$BASE/out.raw"

# cleanup
for p in $(pgrep -x dotnet 2>/dev/null); do
  [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D" ] && kill -TERM "$p" 2>/dev/null
done
sleep 3
for p in $(pgrep -x dotnet 2>/dev/null); do
  [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D" ] && kill -KILL "$p" 2>/dev/null
done
kill "$RUNNER" 2>/dev/null
echo "done"