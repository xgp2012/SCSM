#!/usr/bin/env bash
# V0-5 — ServerSetting.json vs save GameInfo precedence, empirically.
#
# Method: for one overlapping field at a time, create a world (so the save
# exists with a known baseline), stop cleanly, then change ONLY that field in
# ServerSetting.json, start again, stop, and diff the save's GameInfo.
#
# The save is the thing the server writes, so if the save's value tracks
# ServerSetting.json, then ServerSetting.json WINS (it overwrote the save on
# load).  If it keeps the old value, the save wins.
#
# Usage: bash v0_5_precedence.sh
set -uo pipefail

BASE=/tmp/v0/exp/v05
SRC=/tmp/scnetsrv/net10.0
DOTNET=/tmp/dotnet10/dotnet
export DOTNET_ROOT=/tmp/dotnet10
D="$BASE/inst"
RESULT="$BASE/precedence.tsv"
mkdir -p "$BASE"; : > "$RESULT"
printf 'field\tsetting_before\tsetting_after\tsave_before\tsave_after\twinner\n' >> "$RESULT"

dump_gameinfo() { # dump_gameinfo <project.json> <field>
  python3 -c "
import json,sys
try:
    d=json.load(open(sys.argv[1],encoding='utf-8-sig'))
    v=d.get('Subsystems',{}).get('GameInfo',{}).get(sys.argv[2])
    if isinstance(v,list) and len(v)>1: print(v[1])
    else: print(v)
except Exception as e: print('ERR:'+str(e)[:50])
" "$1" "$2"
}

set_field() { # set_field <file> <json-field> <python-literal>
  python3 -c "
import json,sys
p,f,val=sys.argv[1],sys.argv[2],sys.argv[3]
d=json.load(open(p,encoding='utf-8-sig'))
d[f]=json.loads(val)
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
" "$1" "$2" "$3"
}

start_stop() { # start_stop <tag>
  local tag="$1"
  cd "$D/srv"
  ( sleep 240 ) | env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color \
      LANG=C.UTF-8 LC_ALL=C.UTF-8 "$DOTNET" Survivalcraft.dll > "$BASE/$tag.raw" 2>&1 &
  local runner=$!
  for i in $(seq 1 120); do
    grep -q 'Entered screen "Game"' "$BASE/$tag.raw" 2>/dev/null && break
    sleep 1
  done
  sleep 4   # allow a save cycle
  # graceful stop
  local pid
  for p in $(pgrep -x dotnet 2>/dev/null); do
    [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D/srv" ] && pid=$p
  done
  [ -n "${pid:-}" ] && { kill -TERM "$pid" 2>/dev/null; }
  sleep 6
  for p in $(pgrep -x dotnet 2>/dev/null); do
    [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$D/srv" ] && kill -KILL "$p" 2>/dev/null
  done
  kill "$runner" 2>/dev/null; pkill -f 'sleep 240' 2>/dev/null
  sleep 1
}

# --- baseline world --------------------------------------------------------
rm -rf "$D"; mkdir -p "$D/srv"; cp -a "$SRC"/. "$D/srv/"; rm -rf "$D/srv/Worlds"
python3 - "$D/srv/ServerSetting.json" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/P",
          "WorldName":"BaseName","WorldSeed":"321","GameMode":1,
          "WorldMaxPlayers":20,"PVPEnabled":True,"WorldDaySpeed":1,
          "WorldPassword":"","WorldRecoverySpeed":1,"SeasonChanging":True,
          "RandomSpawnPosition":False})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY
echo "### baseline start"
start_stop base
PROJ="$D/srv/Worlds/P/Project.json"
echo "baseline GameInfo:"; python3 -c "
import json;d=json.load(open('$PROJ',encoding='utf-8-sig'))
gi=d['Subsystems']['GameInfo']
for k in ['WorldName','GameMode','MaxOnlinePlayerCount','IsFriendlyFireEnabled','DaySpeed','RecoverFator','Password','AreSeasonsChanging','RandomSpawnPosition','WorldDirectoryName']:
    print(f'  {k:26}',gi.get(k))
"

# --- one field at a time ---------------------------------------------------
# field : json value to set in ServerSetting.json : GameInfo key to inspect
CASES=(
  "WorldName|\"RenamedBySetting\"|WorldName"
  "GameMode|3|GameMode"
  "WorldMaxPlayers|7|MaxOnlinePlayerCount"
  "PVPEnabled|false|IsFriendlyFireEnabled"
  "WorldDaySpeed|4|DaySpeed"
  "WorldRecoverySpeed|3|RecoverFator"
  "WorldPassword|\"secretpw\"|Password"
  "SeasonChanging|false|AreSeasonsChanging"
  "RandomSpawnPosition|true|RandomSpawnPosition"
)

for case in "${CASES[@]}"; do
  IFS='|' read -r FIELD NEWVAL GKEY <<< "$case"
  echo; echo "########## FIELD: $FIELD  (ServerSetting '$FIELD' := $NEWVAL) ##########"
  SB=$(python3 -c "
import json;d=json.load(open('$D/srv/ServerSetting.json',encoding='utf-8-sig'));print(json.dumps(d.get('$FIELD'),ensure_ascii=False))")
  SA=$(python3 -c "import json;print(json.dumps(json.loads('$NEWVAL'),ensure_ascii=False))")
  SAVEB=$(dump_gameinfo "$PROJ" "$GKEY")
  set_field "$D/srv/ServerSetting.json" "$FIELD" "$NEWVAL"
  start_stop "f_$FIELD"
  SAVEA=$(dump_gameinfo "$PROJ" "$GKEY")
  if [ "$SAVEA" != "$SAVEB" ]; then W="ServerSetting.json (save was overwritten)"; else W="save kept old value -> NOT overridden"; fi
  echo "  setting $SB -> $SA   |  save $GKEY: $SAVEB -> $SAVEA"
  echo "  ==> $W"
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$FIELD" "$SB" "$SA" "$SAVEB" "$SAVEA" "$W" >> "$RESULT"
  # restore the field to baseline before the next case
  set_field "$D/srv/ServerSetting.json" "$FIELD" "$(python3 -c "
import json;print(json.dumps({'WorldName':'\u0000'}.get('$FIELD','')))" 2>/dev/null || echo '""')" 2>/dev/null || true
  # safer explicit restore:
  python3 -c "
import json,sys
p='$D/srv/ServerSetting.json'; f='$FIELD'; orig=json.loads('''$SB''')
d=json.load(open(p,encoding='utf-8-sig')); d[f]=orig
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
"
done

echo; echo "================ PRECEDENCE TABLE ================"
column -t -s $'\t' "$RESULT"