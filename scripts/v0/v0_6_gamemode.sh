#!/usr/bin/env bash
# V0-6 — GameMode mapping, tested empirically for values 0..6 (and 7 as an
# out-of-range control).
#
# For each value we start the real server from a CLEAN world with
# ServerSetting.json GameMode=<n>, wait for readiness, and record:
#   * the `Loaded world, GameMode=XXX` log anchor  (what the server calls it)
#   * the in-save `Subsystems.GameInfo.GameMode` string  (what got persisted)
#   * whether this value is accepted at all
#
# Usage: bash v0_6_gamemode.sh [values...]     (default: 0 1 2 3 4 5 6 7)
set -uo pipefail

BASE=/tmp/v0/exp/v06
SRC=/tmp/scnetsrv/net10.0
DOTNET=/tmp/dotnet10/dotnet
export DOTNET_ROOT=/tmp/dotnet10

vals=("$@"); [ ${#vals[@]} -eq 0 ] && vals=(0 1 2 3 4 5 6 7)
mkdir -p "$BASE"
RESULT="$BASE/results.tsv"
: > "$RESULT"
printf 'gamemode\tlog_gamemode\tsave_gamemode\tready\tother_gamemode_hits\n' >> "$RESULT"

for v in "${vals[@]}"; do
  D="$BASE/gm$v"; rm -rf "$D"; mkdir -p "$D/srv"
  cp -a "$SRC"/. "$D/srv/"
  rm -rf "$D/srv/Worlds"
  python3 - "$D/srv/ServerSetting.json" "$v" <<'PY'
import json,sys
p,v=sys.argv[1],int(sys.argv[2])
d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/G",
          "WorldName":"GM%d"%v,"WorldSeed":"1234","GameMode":v})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY
  echo "########## GameMode=$v ##########"
  cd "$D/srv"
  ( sleep 200 ) | env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color \
      LANG=C.UTF-8 LC_ALL=C.UTF-8 "$DOTNET" Survivalcraft.dll > "$D/out.raw" 2>&1 &
  RUNNER=$!
  READY=no
  for i in $(seq 1 100); do
    grep -q 'Entered screen "Game"' "$D/out.raw" 2>/dev/null && { READY=yes; break; }
    # bail early on a hard failure
    grep -qE 'Unhandled exception|Failed to load world' "$D/out.raw" 2>/dev/null && break
    sleep 1
  done
  sleep 3
  LOGGM=$(grep -o 'Loaded world, GameMode=[A-Za-z]*' "$D/out.raw" | head -1 | sed 's/.*=//')
  SAVEGM=$(python3 -c "
import json
try:
    d=json.load(open('$D/srv/Worlds/G/Project.json',encoding='utf-8-sig'))
    gi=d.get('Subsystems',{}).get('GameInfo',{})
    v=gi.get('GameMode')
    print(v[1] if isinstance(v,list) and len(v)>1 else v)
except Exception as e: print('ERR:'+str(e)[:40])
" 2>/dev/null)
  HITS=$(grep -c 'Loaded world' "$D/out.raw")
  echo "  log GameMode=$LOGGM | save GameMode=$SAVEGM | ready=$READY"
  printf '%s\t%s\t%s\t%s\t%s\n' "$v" "${LOGGM:-<none>}" "${SAVEGM:-<none>}" "$READY" "$HITS" >> "$RESULT"

  # stop
  for p in $(pgrep -x dotnet 2>/dev/null); do
    [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D/srv" ] && kill -TERM "$p" 2>/dev/null
  done
  sleep 3
  for p in $(pgrep -x dotnet 2>/dev/null); do
    [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D/srv" ] && kill -KILL "$p" 2>/dev/null
  done
  kill "$RUNNER" 2>/dev/null
  pkill -f 'sleep 200' 2>/dev/null
  sleep 1
done

echo; echo "================ VERIFIED GAMEMODE TABLE ================"
column -t -s $'\t' "$RESULT"