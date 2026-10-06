#!/usr/bin/env bash
# V0-1(b) — determine the command-fifo LIFETIME / re-open semantics.
#
# The precision pass showed a striking asymmetry: the first two writes produced
# replies, the remaining six produced NOTHING.  Two competing explanations:
#   (A) the server opens the FIFO once (or a bounded number of times) at boot and
#       never re-opens it, so later writers have no reader;
#   (B) the server re-opens per line, but our writes raced / the reader exited.
#
# This script distinguishes them by writing the SAME command repeatedly with a
# gap, and by checking whether ANY reader holds the FIFO open between writes.
#
# Usage: bash v0_1_fifo_lifetime.sh
set -uo pipefail

BASE=/tmp/v0/exp/v01_fifo3
SRC=/tmp/scnetsrv/net10.0
DOTNET=/tmp/dotnet10/dotnet
export DOTNET_ROOT=/tmp/dotnet10
rm -rf "$BASE"; mkdir -p "$BASE"
D="$BASE/srv"; mkdir -p "$D"; cp -a "$SRC"/. "$D/"; rm -rf "$D/Worlds"
python3 - "$D/ServerSetting.json" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/F3",
          "WorldName":"FifoLife","WorldSeed":"999","GameMode":1})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY
cd "$D"
mkfifo command-fifo
setsid env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color LANG=C.UTF-8 LC_ALL=C.UTF-8 \
    "$DOTNET" Survivalcraft.dll > "$BASE/out.raw" 2>&1 < /dev/null &
RUNNER=$!
for i in $(seq 1 120); do grep -q 'Entered screen "Game"' "$BASE/out.raw" 2>/dev/null && break; sleep 1; done
echo "ready after ${i}s"
PID=$(pgrep -x dotnet | while read p; do [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D" ] && echo $p; done | head -1)
echo "pid=$PID"

echo
echo "===== WATCH: does any writer/reader stay on the FIFO over time? ====="
watch_fifo() {
  echo "t=$1  procs-with-fifo-fd:"
  for p in $(pgrep -x dotnet 2>/dev/null); do
    ls -l /proc/$p/fd/ 2>/dev/null | grep -c fifo | sed 's/^/   dotnet fd-count: /'
  done
}
for t in 0 3 6 9; do watch_fifo "$t"; sleep 3; done

echo
echo "===== REPEATED /pws WITH 4s GAPS (same command, check which land) ====="
for n in 1 2 3 4 5 6; do
  mark=$(wc -l < "$BASE/out.raw")
  timeout 5 bash -c "printf '/pws\n' > '$D/command-fifo'"; rc=$?
  sleep 4
  new=$(( $(wc -l < "$BASE/out.raw") - mark ))
  echo "write #$n: writer_rc=$rc new_stdout_lines=$new  $( [ "$new" -gt 0 ] && tail -n 1 "$BASE/out.raw" || echo '(no reply)')"
done

echo
echo "===== /player list 0 through fifo (5 attempts) ====="
for n in 1 2 3 4 5; do
  mark=$(wc -l < "$BASE/out.raw")
  timeout 5 bash -c "printf '/player list 0\n' > '$D/command-fifo'"; rc=$?
  sleep 4
  new=$(( $(wc -l < "$BASE/out.raw") - mark ))
  echo "write #$n: writer_rc=$rc new_stdout_lines=$new"
  [ "$new" -gt 0 ] && tail -n "$new" "$BASE/out.raw"
done

echo
echo "===== FULL stdout tail ====="; tail -30 "$BASE/out.raw"
echo
echo "===== count of '[pws]' replies ====="; grep -c '\[pws\]' "$BASE/out.raw" || true

for p in $(pgrep -x dotnet 2>/dev/null); do
  [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D" ] && kill -TERM "$p" 2>/dev/null
done
sleep 3
for p in $(pgrep -x dotnet 2>/dev/null); do
  [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D" ] && kill -KILL "$p" 2>/dev/null
done
kill "$RUNNER" 2>/dev/null
echo done