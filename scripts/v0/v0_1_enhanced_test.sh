#!/usr/bin/env bash
# V0-1(d) — does --enhanced change anything under a PIPE?
#
# The server's own banner suggests `--enhanced` to force the enhanced terminal.
# The plan marks this "待实测".  We run the SAME pipe-mode setup twice, once with
# and once without the flag, and compare: the banner lines, the ESC-byte count,
# and whether `help` on stdin is answered.
#
# Usage: bash v0_1_enhanced_test.sh [<base-dir>]
set -uo pipefail

BASE="${1:-/tmp/v0/exp/v01_enhanced}"
SRC="${SRC:-/tmp/scnetsrv/net10.0}"
DOTNET="${DOTNET:-/tmp/dotnet10/dotnet}"
export DOTNET_ROOT="${DOTNET_ROOT:-/tmp/dotnet10}"

rm -rf "$BASE"; mkdir -p "$BASE"

run_one() { # run_one <label> [extra args...]
  local label="$1"; shift
  local RUN="$BASE/$label"
  mkdir -p "$RUN/srv"
  cp -a "$SRC"/. "$RUN/srv/"
  rm -rf "$RUN/srv/Worlds"
  python3 - "$RUN/srv/ServerSetting.json" "$label" <<'PY'
import json,sys
p,label=sys.argv[1],sys.argv[2]
d=json.load(open(p,encoding='utf-8-sig'))
d.update({"Autorun":True,"AutoGenerateWorld":True,"WorldPath":"app:/Worlds/E",
          "WorldName":"E"+label,"WorldSeed":"77","GameMode":1})
json.dump(d,open(p,'w',encoding='utf-8'),ensure_ascii=False,indent=2)
PY
  echo "===================== RUN: $label  (args: $*) ====================="
  cd "$RUN/srv"
  ( sleep 300 ) | env DOTNET_ROOT="$DOTNET_ROOT" TERM=xterm-256color \
      LANG=C.UTF-8 LC_ALL=C.UTF-8 "$DOTNET" Survivalcraft.dll "$@" > "$RUN/out.raw" 2>&1 &
  local runner=$!
  for i in $(seq 1 90); do
    grep -q 'Entered screen "Game"' "$RUN/out.raw" 2>/dev/null && break
    sleep 1
  done
  sleep 2
  echo "--- banner / mode lines ---"
  grep -n '自动检测\|提示\|EnhancedTerminalLogSink\|ANSI\|enhanced\|终端' "$RUN/out.raw" | head -10
  echo "--- boot anchors ---"
  grep -n 'StartServer\|Entered screen "Game"' "$RUN/out.raw" | head -5
  echo "--- ESC byte count: $(python3 -c "print(open('$RUN/out.raw','rb').read().count(b'\x1b'))") ---"
  echo "--- head -6 (repr) ---"
  head -6 "$RUN/out.raw" | cat -v
  # stop it
  local pid
  pid=$(pgrep -P "$runner" 2>/dev/null | head -1)
  pkill -TERM -f 'Survivalcraft.dll --enhanced' 2>/dev/null
  kill -TERM "$runner" 2>/dev/null
  # kill only dotnet processes whose CWD is this run dir
  for p in $(pgrep -x dotnet 2>/dev/null); do
    if [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$RUN/srv" ]; then kill -TERM "$p" 2>/dev/null; fi
  done
  sleep 3
  for p in $(pgrep -x dotnet 2>/dev/null); do
    if [ "$(readlink /proc/$p/cwd 2>/dev/null)" = "$RUN/srv" ]; then kill -KILL "$p" 2>/dev/null; fi
  done
  pkill -f 'sleep 300' 2>/dev/null
  true
}

run_one baseline
run_one enhanced --enhanced
run_one forceansi --force-ansi
run_one noansi --no-ansi

echo
echo "===================== COMPARISON ====================="
for label in baseline enhanced forceansi noansi; do
  f="$BASE/$label/out.raw"
  [ -f "$f" ] || continue
  printf '%-12s ESC=%-6s mode=%-40s ready=%s\n' "$label" \
    "$(python3 -c "print(open('$f','rb').read().count(b'\x1b'))")" \
    "$(grep -o '\[自动检测\][^,]*\|EnhancedTerminalLogSink initialized[^,]*' "$f" | head -1)" \
    "$(grep -c 'Entered screen "Game"' "$f")"
done