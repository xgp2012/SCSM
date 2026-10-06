#!/usr/bin/env bash
# V0-2 (direct) — stop behaviour measured WITHOUT the panel.
#
# Runs the real server three times from clean copies and applies one stop
# method each time, recording:
#   exit code, wall time to exit, and whether Project.json / .bak mtime advanced.
#
# Methods:
#   command   write "/stop\n" to stdin pipe, then Ctrl+C (\x03) to stdin pipe
#   sigterm   SIGTERM to the process group
#   sigkill   SIGKILL to the process group
#   fifo      write "/stop" to a named FIFO, then SIGTERM
#
# Usage: bash v0_2_direct.sh [method ...]
set -uo pipefail

BASE=/tmp/v0/exp/v02_direct
SRC=/tmp/scnetsrv/net10.0
DOTNET=/tmp/dotnet10/dotnet
export DOTNET_ROOT=/tmp/dotnet10

methods=("$@"); [ ${#methods[@]} -eq 0 ] && methods=(command sigterm sigkill fifo)

setup() { # setup <dir>
  rm -rf "$1"; mkdir -p "$1/srv"
  cp -a "$SRC"/. "$1/srv/"
  rm -rf "$1/srv/Worlds"
  python3 - "$1/srv/ServerSetting.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/W",
          "WorldName":"W2","WorldSeed":"909","GameMode":1})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY
  mkfifo "$1/stdin.fifo"
}

proj_mtime() { stat -c '%Y' "$1/srv/Worlds/W/Project.json" 2>/dev/null || echo 0; }
bak_mtime()  { stat -c '%Y' "$1/srv/Worlds/W/Project.json.bak" 2>/dev/null || echo 0; }

for m in "${methods[@]}"; do
  D="$BASE/$m"; setup "$D"
  echo
  echo "################ METHOD: $m ################"
  cd "$D/srv"
  # `setsid` puts the server in its OWN session/process group.  This is
  # essential: without it the server shares this script's process group and a
  # `kill -TERM -$PGID` would kill the probe itself.
  setsid env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color LANG=C.UTF-8 LC_ALL=C.UTF-8 \
      "$DOTNET" Survivalcraft.dll < "$D/stdin.fifo" > "$D/out.raw" 2>&1 &
  RUNNER=$!
  exec 9> "$D/stdin.fifo"     # hold write end so stdin never EOFs

  # wait for ready
  READY=0
  for i in $(seq 1 120); do
    if grep -q 'Entered screen "Game"' "$D/out.raw" 2>/dev/null; then READY=$i; break; fi
    sleep 1
  done
  echo "ready after ${READY}s"
  [ "$READY" = 0 ] && { echo "SERVER NEVER BECAME READY - aborting method"; exec 9>&-; kill -KILL $RUNNER 2>/dev/null; continue; }

  PID=$(pgrep -x dotnet | while read p; do [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D/srv" ] && echo $p; done | head -1)
  PGID=$(ps -o pgid= -p "$PID" 2>/dev/null | tr -d ' ')
  echo "pid=$PID pgid=$PGID"

  # settle, then snapshot mtimes
  sleep 3
  P0=$(proj_mtime "$D"); B0=$(bak_mtime "$D")
  echo "before stop: proj_mtime=$P0 bak_mtime=$B0"
  ls -la --time-style=full-iso "$D/srv/Worlds/W/" | grep Project

  T0=$(date +%s.%N)
  case "$m" in
    command)
      echo ">>> stdin: /stop"
      printf '/stop\n' >&9; sleep 8
      if kill -0 "$PID" 2>/dev/null; then
        echo ">>> still alive after /stop; stdin: Ctrl+C (0x03)"
        printf '\003' >&9; sleep 8
      else echo ">>> exited after /stop"; fi
      if kill -0 "$PID" 2>/dev/null; then
        echo ">>> still alive after Ctrl+C; escalating to SIGTERM on group"
        kill -TERM -"$PGID" 2>/dev/null
      fi
      ;;
    fifo)
      echo ">>> writing /stop to a named FIFO in CWD (server never created one; testing if it opens it)"
      mkfifo "$D/srv/command-fifo" 2>/dev/null
      ( printf '/stop\n' > "$D/srv/command-fifo" ) & sleep 8
      if kill -0 "$PID" 2>/dev/null; then echo ">>> alive; SIGTERM group"; kill -TERM -"$PGID" 2>/dev/null; fi
      ;;
    sigterm)
      echo ">>> SIGTERM to process group $PGID"
      kill -TERM -"$PGID" 2>/dev/null
      ;;
    sigkill)
      echo ">>> SIGKILL to process group $PGID"
      kill -KILL -"$PGID" 2>/dev/null
      ;;
  esac

  # wait for exit up to 60s
  EXITED=0
  for i in $(seq 1 60); do
    kill -0 "$PID" 2>/dev/null || { EXITED=$i; break; }
    sleep 1
  done
  T1=$(date +%s.%N)
  wait "$RUNNER" 2>/dev/null; RC=$?
  echo "exited after ${EXITED}s (0 means already gone); shell wait rc=$RC"
  echo "wall time: $(python3 -c "print(f'{$T1-$T0:.2f}s')")"
  echo "exit status from /proc: $(cat /proc/$PID/stat 2>/dev/null | awk '{print $3}') (Z=zombie)"

  sleep 2
  P1=$(proj_mtime "$D"); B1=$(bak_mtime "$D")
  echo "after stop:  proj_mtime=$P1 bak_mtime=$B1"
  ls -la --time-style=full-iso "$D/srv/Worlds/W/" | grep Project
  echo "PROJ advanced: $([ "$P0" != "$P1" ] && echo YES || echo NO)   BAK advanced: $([ "$B0" != "$B1" ] && echo YES || echo NO)"
  echo "--- last 12 lines of output ---"; tail -12 "$D/out.raw" | cat -v

  # cleanup
  kill -KILL "$PID" 2>/dev/null
  exec 9>&-
  kill "$RUNNER" 2>/dev/null
  sleep 1
done
echo; echo "all methods done; artifacts under $BASE"