#!/usr/bin/env bash
# V0-1(c) — definitive stdin-pipe command-channel test.
#
# Unlike the panel's fallback (which gives the child a pipe it never writes to),
# here we own the write end of the child's stdin and inject a command while the
# server is sitting in the "Game" screen.  We then look for ANY response.
#
# Also answers: does the server echo what we write?  ("echo" would prove the
# input path is wired to a terminal-ish reader even without a TTY.)
#
# Usage: bash v0_1_stdin_test.sh [<run-dir>]
set -uo pipefail

RUN="${1:-/tmp/v0/exp/v01_stdin}"
SRC="${SRC:-/tmp/scnetsrv/net10.0}"
DOTNET="${DOTNET:-/tmp/dotnet10/dotnet}"
export DOTNET_ROOT="${DOTNET_ROOT:-/tmp/dotnet10}"

rm -rf "$RUN"; mkdir -p "$RUN/srv"
cp -a "$SRC"/. "$RUN/srv/"
rm -rf "$RUN/srv/Worlds"
python3 - "$RUN/srv/ServerSetting.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/V01S",
          "WorldName":"V01Stdin","WorldSeed":"4243","GameMode":1})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY

FIFO="$RUN/stdin.fifo"
mkfifo "$FIFO"
cd "$RUN/srv"

echo "starting server with stdin = named pipe $FIFO"
env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color LANG=C.UTF-8 LC_ALL=C.UTF-8 \
    "$DOTNET" Survivalcraft.dll < "$FIFO" > "$RUN/out.raw" 2>&1 &
RUNNER=$!
# Hold the FIFO's write end open so the server never sees EOF on stdin.
exec 9> "$FIFO"

echo "runner pid=$RUNNER"
for i in $(seq 1 90); do
  if grep -q 'Entered screen "Game"' "$RUN/out.raw" 2>/dev/null; then
    echo "REACHED GAME SCREEN at t=${i}s"; break
  fi
  sleep 1
done

DOTNET_PID=$(pgrep -f "Survivalcraft.dll" | head -1)
echo "dotnet pid=$DOTNET_PID"
echo "fd0: $(readlink /proc/$DOTNET_PID/fd/0)  type=$(stat -c '%F' /proc/$DOTNET_PID/fd/0)"
echo "fd0 isatty: $(python3 -c "
import os,sys
try: print(os.isatty(int(sys.argv[1])))
except Exception as e: print('?',e)" "$DOTNET_PID" 2>/dev/null)"
python3 -c "
import os
fd=os.open('/proc/$DOTNET_PID/fd/0',os.O_RDONLY|os.O_NONBLOCK)
print('isatty(fd0) via open:', os.isatty(fd))
os.close(fd)
" 2>&1 | tail -1

MARK=$(wc -l < "$RUN/out.raw")
echo "--- output line mark before injection: $MARK ---"

echo ">>> injecting 'help' into stdin (line 1)"
printf 'help\n' >&9
sleep 6
echo ">>> injecting '/player list 0' into stdin (line 3)"
printf '/player list 0\n' >&9
sleep 6
echo ">>> injecting a bogus command 'zzzz' (control)"
printf 'zzzz\n' >&9
sleep 6
echo ">>> injecting '/help'"
printf '/help\n' >&9
sleep 6

echo
echo "===== NEW OUTPUT SINCE MARK ($MARK) ====="
tail -n "+$((MARK+1))" "$RUN/out.raw" | cat -A | sed 's/\$$//' | head -60
echo "===== END NEW OUTPUT ====="
echo "new line count: $(( $(wc -l < "$RUN/out.raw") - MARK ))"

# cleanup
kill -TERM "$DOTNET_PID" 2>/dev/null
sleep 2
kill -KILL "$DOTNET_PID" 2>/dev/null
exec 9>&-
kill "$RUNNER" 2>/dev/null
echo "raw: $RUN/out.raw"