#!/usr/bin/env bash
# V0-1(a)(b)(c) — direct command-channel hunt, WITHOUT the panel.
#
# Runs the real server by hand in a clean directory and asks three questions:
#   (a) does the server create a named pipe / "command-fifo" in its CWD?
#   (b) does writing "help\n" to such a file produce a response on stdout?
#   (c) does writing "help\n" to the child's stdin pipe produce a response?
#
# Everything the server prints goes to $RUN/out.raw verbatim.
#
# Usage: bash v0_1_channel_hunt.sh [<run-dir>]
set -uo pipefail

RUN="${1:-/tmp/v0/exp/v01_channel}"
SRC="${SRC:-/tmp/scnetsrv/net10.0}"
DOTNET="${DOTNET:-/tmp/dotnet10/dotnet}"
export DOTNET_ROOT="${DOTNET_ROOT:-/tmp/dotnet10}"

rm -rf "$RUN"; mkdir -p "$RUN/srv"
cp -a "$SRC"/. "$RUN/srv/"
rm -rf "$RUN/srv/Worlds"
# Deterministic autorun config: generate a world and then sit in the Game screen.
python3 - "$RUN/srv/ServerSetting.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/V01",
          "WorldName":"V01Chan","WorldSeed":"4242","GameMode":1})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY

say() { printf '\n===== %s =====\n' "$*"; }

# ---- (a) snapshot CWD before start ----------------------------------------
say "A. CWD listing BEFORE start"
ls -la "$RUN/srv" > "$RUN/ls_before.txt"; cat "$RUN/ls_before.txt" | tail -20
find "$RUN/srv" -type p -printf '%p (fifo)\n' > "$RUN/fifos_before.txt" 2>/dev/null
echo "fifos before: [$(cat "$RUN/fifos_before.txt")]"

# ---- start the server, keeping stdin as a PIPE we own ----------------------
say "A. starting server (stdin=pipe, stdout=pipe, TERM=xterm-256color)"
cd "$RUN/srv"
# `tail -f /dev/null |` gives the child a pipe on stdin that never closes.
# This is exactly the "pipe mode" the panel's fallback uses.
( sleep 900 ) | env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color \
    LANG=C.UTF-8 LC_ALL=C.UTF-8 DOTNET_CLI_TELEMETRY_OPTOUT=1 \
    "$DOTNET" Survivalcraft.dll > "$RUN/out.raw" 2>&1 &
RUNNER=$!
echo "runner pid=$RUNNER (subshell)"
sleep 1
# Find the real dotnet pid
DOTNET_PID=$(pgrep -f "Survivalcraft.dll" | head -1)
echo "dotnet pid=$DOTNET_PID"
echo "$DOTNET_PID" > "$RUN/pid"

# ---- poll for fifo creation while the server boots -------------------------
say "A. polling for named pipes for 45s while server boots"
FOUND=""
for i in $(seq 1 45); do
  F=$(find "$RUN/srv" -type p 2>/dev/null)
  if [ -n "$F" ]; then FOUND="$F"; echo "FIFO FOUND at t=${i}s: $F"; break; fi
  sleep 1
done
[ -z "$FOUND" ] && echo "no named pipe appeared in server CWD after 45s"

say "A. CWD listing AFTER start (diff vs before)"
ls -la "$RUN/srv" > "$RUN/ls_after.txt"
diff "$RUN/ls_before.txt" "$RUN/ls_after.txt" && echo "(no new entries in CWD root)"
echo "--- recursive new files (any type) ---"
find "$RUN/srv" -newer "$RUN/ls_before.txt" -printf '%y %p\n' 2>/dev/null | head -30

say "A. server output so far"
cat "$RUN/out.raw"

# ---- (b) FIFO write test ---------------------------------------------------
say "B. FIFO write test"
if [ -n "$FOUND" ]; then
  echo "writing 'help\\n' into $FOUND"
  printf 'help\n' > "$FOUND" &
  sleep 3
else
  echo "SKIP: no FIFO exists to write to"
fi
echo "--- output after FIFO write ---"; cat "$RUN/out.raw"

# ---- (c) stdin pipe write test --------------------------------------------
say "C. stdin pipe write test"
# We cannot write to the `( sleep 900 )` pipe after the fact, so instead
# demonstrate with a coproc-style FIFO as stdin in run 2.  Here we just record
# that the current server's stdin is a pipe and try the PTY-less approach.
readlink -f "/proc/$DOTNET_PID/fd/0" || true
echo "fd0 type: $(stat -c '%F' /proc/$DOTNET_PID/fd/0 2>/dev/null)"
echo "fd1 type: $(stat -c '%F' /proc/$DOTNET_PID/fd/1 2>/dev/null)"

cat > "$RUN/out_before_stdin" <<< "$(cat "$RUN/out.raw")"
WCLINE_COUNT_BEFORE=$(wc -l < "$RUN/out.raw")

# ---- cleanup ---------------------------------------------------------------
say "cleanup"
kill -TERM "$DOTNET_PID" 2>/dev/null
sleep 3
kill -KILL "$DOTNET_PID" 2>/dev/null
kill "$RUNNER" 2>/dev/null
pkill -f "sleep 900" 2>/dev/null
echo "done; raw output at $RUN/out.raw"