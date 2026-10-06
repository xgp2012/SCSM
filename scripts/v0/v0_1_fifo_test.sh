#!/usr/bin/env bash
# V0-1(b)/V0-2 — rigorous proof that a named FIFO called `command-fifo` in the
# server's CWD is a REAL command channel.
#
# The first pass created the FIFO itself, so we must rule out coincidence and
# nail down the exact contract:
#   1. Does the server create `command-fifo` itself, or must it pre-exist?
#   2. Does writing `help` (not /stop) to it produce a `[help]:` reply?
#   3. Does the reply land on stdout (the log) or back on the FIFO?
#   4. Does a wrong name (`command-fifo2`, `fifo`, `commandpipe`) work? (control)
#   5. Does /player list 0 work through it?
#   6. Chinese round-trip through the channel?
#
# Usage: bash v0_1_fifo_test.sh
set -uo pipefail

BASE=/tmp/v0/exp/v01_fifo
SRC=/tmp/scnetsrv/net10.0
DOTNET=/tmp/dotnet10/dotnet
export DOTNET_ROOT=/tmp/dotnet10
rm -rf "$BASE"; mkdir -p "$BASE"

boot() { # boot <tag> ; sets D, RUNNER ; waits for ready
  D="$BASE/$1"; rm -rf "$D"; mkdir -p "$D/srv"
  cp -a "$SRC"/. "$D/srv/"; rm -rf "$D/srv/Worlds"
  python3 - "$D/srv/ServerSetting.json" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/F",
          "WorldName":"FifoTest","WorldSeed":"8888","GameMode":1})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY
  cd "$D/srv"
  setsid env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color LANG=C.UTF-8 LC_ALL=C.UTF-8 \
      "$DOTNET" Survivalcraft.dll > "$D/out.raw" 2>&1 < /dev/null &
  RUNNER=$!
  for i in $(seq 1 120); do
    grep -q 'Entered screen "Game"' "$D/out.raw" 2>/dev/null && return 0
    sleep 1
  done
  return 1
}

shutdown() { # shutdown <dir>
  for p in $(pgrep -x dotnet 2>/dev/null); do
    [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$1/srv" ] && kill -TERM "$p" 2>/dev/null
  done
  sleep 3
  for p in $(pgrep -x dotnet 2>/dev/null); do
    [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$1/srv" ] && kill -KILL "$p" 2>/dev/null
  done
  sleep 1
}

# ===========================================================================
echo "############ TEST 1: does the server create command-fifo BY ITSELF? ############"
boot t1_autocreate || { echo "boot FAILED"; exit 1; }
echo "server CWD: $D/srv"
echo "--- named pipes present after boot (server-created?) ---"
find "$D/srv" -maxdepth 1 -type p -printf 'FIFO: %p\n' 2>/dev/null || true
if [ -z "$(find "$D/srv" -maxdepth 1 -type p 2>/dev/null)" ]; then
  echo "RESULT-1: server does NOT create command-fifo on its own."
else
  echo "RESULT-1: server DOES create a FIFO."
fi
shutdown "$D"

# ===========================================================================
echo
echo "############ TEST 2: pre-create command-fifo, send 'help', watch reply ############"
boot t2_help || { echo "boot FAILED"; exit 1; }
cd "$D/srv"
mkfifo command-fifo
echo "created $D/srv/command-fifo"
# Keep a persistent reader open on the FIFO so our writes do not block, and so
# we can see whether the server writes a reply BACK to the FIFO.
( cat command-fifo > "$D/fifo_reply.txt" ) &
CATPID=$!
sleep 1
MARK=$(wc -l < "$D/out.raw")
echo "--- writing 'help' ---"
printf 'help\n' > command-fifo
sleep 5
echo "--- writing '/player list 0' ---"
printf '/player list 0\n' > command-fifo
sleep 5
echo "--- writing bare 'player list 0' (no slash) ---"
printf 'player list 0\n' > command-fifo
sleep 5
echo "--- writing control: 'zzzznosuchcmd' ---"
printf 'zzzznosuchcmd\n' > command-fifo
sleep 5

echo
echo "===== STDOUT (log) NEW LINES SINCE MARK $MARK ====="
tail -n "+$((MARK+1))" "$D/out.raw"
echo "===== END ====="
echo
echo "===== FILES THE SERVER WROTE BACK TO THE FIFO ($D/fifo_reply.txt) ====="
cat "$D/fifo_reply.txt" 2>/dev/null; echo "(bytes: $(stat -c %s "$D/fifo_reply.txt" 2>/dev/null))"
echo "===== END ====="
kill $CATPID 2>/dev/null
shutdown "$D"

# ===========================================================================
echo
echo "############ TEST 3: control — WRONG fifo names should do nothing ############"
boot t3_control || { echo "boot FAILED"; exit 1; }
cd "$D/srv"
mkfifo command-fifo2 fifo commandpipe 2>/dev/null
MARK=$(wc -l < "$D/out.raw")
for n in command-fifo2 fifo commandpipe; do
  ( printf '/stop\n' > "$n" ) 2>/dev/null &
done
sleep 10
echo "new lines since mark: $(( $(wc -l < "$D/out.raw") - MARK ))"
tail -n "+$((MARK+1))" "$D/out.raw" | head -10
if kill -0 "$(pgrep -x dotnet | head -1)" 2>/dev/null; then
  echo "RESULT-3: wrong FIFO names -> server still ALIVE (no effect). Control passes."
else
  echo "RESULT-3: server died from a wrong FIFO name — unexpected!"
fi
shutdown "$D"

# ===========================================================================
echo
echo "############ TEST 4: Chinese + player list through command-fifo ############"
boot t4_cn || { echo "boot FAILED"; exit 1; }
cd "$D/srv"
mkfifo command-fifo
MARK=$(wc -l < "$D/out.raw")
printf 'help 1\n' > command-fifo; sleep 4
printf '/player list 0\n' > command-fifo; sleep 4
printf '/time\n' > command-fifo; sleep 4
printf '/auth\n' > command-fifo; sleep 4
echo "===== STDOUT NEW LINES SINCE MARK $MARK ====="
tail -n "+$((MARK+1))" "$D/out.raw"
echo "===== END ====="
shutdown "$D"
echo
echo "done. artifacts in $BASE"