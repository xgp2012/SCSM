#!/usr/bin/env bash
# V0-1(b) — determine WHEN the command-fifo is readable.
#
# Evidence so far:
#   t2  : FIFO created BEFORE boot, 4 writes at t≈+0s,4s,9s,14s -> 2 landed (writes 2,3)
#   fifo2: FIFO created BEFORE boot, 8 writes at t≈+0s..28s -> 2 landed (writes 1,2)
#   fifo3: FIFO created BEFORE boot, 9s idle, then 11 writes -> 0 landed
#
# Hypothesis H: the server reads the FIFO only during a short window around
# startup (a "consume whatever is there" read, like CommandFifoMonitor reading
# once), OR a reader process must be attached when the server calls open().
#
# Method: one boot per timing, measuring exactly how many writes land.
#   * pre-write  : write the command BEFORE starting the server (data sits in the
#                  FIFO buffer; no writer needed at open time)
#   * immediate  : write continuously from t=0
#   * +5s/+15s   : idle first, then write continuously
#
# Usage: bash v0_1_fifo_timing.sh
set -uo pipefail

BASE=/tmp/v0/exp/v01_fifo4
SRC=/tmp/scnetsrv/net10.0
DOTNET=/tmp/dotnet10/dotnet
export DOTNET_ROOT=/tmp/dotnet10
rm -rf "$BASE"; mkdir -p "$BASE"

mk_inst() { # mk_inst <tag>
  local D="$BASE/$1"; rm -rf "$D"; mkdir -p "$D/srv"
  cp -a "$SRC"/. "$D/srv/"; rm -rf "$D/srv/Worlds"
  python3 - "$D/srv/ServerSetting.json" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/T",
          "WorldName":"Timing","WorldSeed":"111","GameMode":1})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY
  echo "$D"
}

stop_inst() { # stop_inst <D>
  for p in $(pgrep -x dotnet 2>/dev/null); do
    [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$1/srv" ] && kill -TERM "$p" 2>/dev/null
  done
  sleep 3
  for p in $(pgrep -x dotnet 2>/dev/null); do
    [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$1/srv" ] && kill -KILL "$p" 2>/dev/null
  done
  sleep 1
}

run_case() { # run_case <tag> <delay_seconds_before_writing>
  local tag="$1" delay="$2"
  local D; D=$(mk_inst "$tag")
  cd "$D/srv"; mkfifo command-fifo
  echo
  echo "############ CASE $tag  (idle ${delay}s then write /pws every 2s x20) ############"
  setsid env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color LANG=C.UTF-8 LC_ALL=C.UTF-8 \
      "$DOTNET" Survivalcraft.dll > "$D/out.raw" 2>&1 < /dev/null &
  local RUNNER=$!
  [ "$delay" -gt 0 ] && sleep "$delay"
  for n in $(seq 1 20); do
    timeout 3 bash -c "printf '/pws\n' > '$D/command-fifo'" 2>/dev/null
    sleep 2
  done
  LANDED=$(grep -c '\[pws\]' "$D/out.raw" 2>/dev/null || echo 0)
  READY=$(grep -c 'Entered screen "Game"' "$D/out.raw" 2>/dev/null || echo 0)
  echo ">>> RESULT $tag: replies_to_/pws=$LANDED  ready=$READY"
  grep -A2 '\[pws\]' "$D/out.raw" 2>/dev/null | head -8
  stop_inst "$D"
  kill "$RUNNER" 2>/dev/null
  echo "$tag landed=$LANDED"
}

# Case A: write BEFORE the server starts — data waits in the FIFO buffer.
D=$(mk_inst prewrite); cd "$D/srv"; mkfifo command-fifo
echo "############ CASE prewrite (write command BEFORE boot) ############"
# A FIFO open-for-write blocks until a reader appears; use a background writer.
( for i in $(seq 1 200); do printf '/pws\n' > "$D/srv/command-fifo" 2>/dev/null || sleep 0.2; sleep 0.2; done ) &
WPID=$!
setsid env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color LANG=C.UTF-8 LC_ALL=C.UTF-8 \
    "$DOTNET" Survivalcraft.dll > "$D/out.raw" 2>&1 < /dev/null &
RUNNER=$!
sleep 45
LANDED=$(grep -c '\[pws\]' "$D/out.raw" 2>/dev/null || echo 0)
echo ">>> RESULT prewrite: replies_to_/pws=$LANDED"
grep -A2 '\[pws\]' "$D/out.raw" 2>/dev/null | head -6
kill $WPID 2>/dev/null; stop_inst "$D"; kill "$RUNNER" 2>/dev/null

run_case idle0 0
run_case idle5 5
run_case idle15 15

echo
echo "==================== TIMING SUMMARY ===================="
grep '^>>> RESULT' /tmp/v0/exp/v01_fifo4/../v01_fifo4_run.log 2>/dev/null || true