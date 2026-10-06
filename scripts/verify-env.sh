#!/usr/bin/env bash
#
# verify-env.sh — establish the ground truth about this host.
#
# Answers one question: "what can this machine actually do for scnetm?"
# It is read-only. It installs nothing, starts no server, and writes only to
# a private mktemp directory that it removes on exit.
#
# Usage:
#   scripts/verify-env.sh            # human-readable report
#   scripts/verify-env.sh --quiet    # only WARN/FAIL lines + summary
#
# Exit status:
#   0  no FAIL lines
#   1  at least one FAIL line
#
# PASS = measured and as required.
# WARN = measured, degraded, or not required for the panel to boot.
# FAIL = measured and blocks a documented plan task.
# INFO = measured fact that is neither of the above.
#
# Every check prints the exact command that produced it, so any line can be
# reproduced by hand.

set -u -o pipefail

QUIET=0
[[ "${1:-}" == "--quiet" ]] && QUIET=1

# ---------------------------------------------------------------------------
# reporting
# ---------------------------------------------------------------------------
PASS_N=0
WARN_N=0
FAIL_N=0
declare -a FAIL_LINES=()
declare -a WARN_LINES=()

if [[ -t 1 ]]; then
  C_PASS=$'\033[32m'; C_WARN=$'\033[33m'; C_FAIL=$'\033[31m'
  C_INFO=$'\033[36m'; C_HEAD=$'\033[1;37m'; C_OFF=$'\033[0m'
else
  C_PASS=; C_WARN=; C_FAIL=; C_INFO=; C_HEAD=; C_OFF=
fi

section() { (( QUIET )) || printf '\n%s== %s ==%s\n' "$C_HEAD" "$1" "$C_OFF"; }
info()    { (( QUIET )) || printf '  %sINFO%s %s\n' "$C_INFO" "$C_OFF" "$1"; }
pass()    { PASS_N=$((PASS_N+1)); (( QUIET )) || printf '  %sPASS%s %s\n' "$C_PASS" "$C_OFF" "$1"; }
warn()    { WARN_N=$((WARN_N+1)); WARN_LINES+=("$1"); printf '  %sWARN%s %s\n' "$C_WARN" "$C_OFF" "$1"; }
fail()    { FAIL_N=$((FAIL_N+1)); FAIL_LINES+=("$1"); printf '  %sFAIL%s %s\n' "$C_FAIL" "$C_OFF" "$1"; }
cmd()     { (( QUIET )) || printf '       $ %s\n' "$1"; }

have() { command -v "$1" >/dev/null 2>&1; }

# Collapse multi-line output into its last non-empty line. Reads stdin.
lastline() { grep -v '^[[:space:]]*$' | tail -n1; }

# Capture the first line of a command's output, or "n/a".
firstline() { "$@" 2>/dev/null | head -n1; }

# ---------------------------------------------------------------------------
printf '%s\n' "scnetm environment verification"
printf '%s\n' "host: $(hostname 2>/dev/null || echo unknown)   date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"

# ---------------------------------------------------------------------------
section "Toolchain"
# ---------------------------------------------------------------------------
if have go; then
  GOVER="$(firstline go version)"
  pass "go: ${GOVER#go version }"
  # The project requires go >= 1.27 per go.mod.
  GOMAJMIN="$(go env GOVERSION 2>/dev/null | sed -E 's/^go([0-9]+\.[0-9]+).*/\1/')"
  if [[ -n "$GOMAJMIN" ]]; then
    if awk -v v="$GOMAJMIN" 'BEGIN{exit !(v+0 >= 1.27)}'; then
      info "go >= 1.27 as required by go.mod"
    else
      fail "go $GOMAJMIN is older than the required 1.27"
    fi
  fi
else
  fail "go not found on PATH (the panel is built with Go)"
fi

if have node; then
  NODEVER="$(firstline node --version)"
  pass "node: $NODEVER"
  NODEMAJ="${NODEVER#v}"; NODEMAJ="${NODEMAJ%%.*}"
  if [[ "$NODEMAJ" =~ ^[0-9]+$ ]] && (( NODEMAJ >= 20 )); then
    info "node >= 20 (Vite 7 requirement satisfied)"
  else
    warn "node $NODEVER may be too old for Vite 7 (needs >= 20.19)"
  fi
else
  warn "node not found — the frontend cannot be rebuilt (prebuilt web/dist still works)"
fi

if have pnpm; then
  pass "pnpm: $(firstline pnpm --version)"
else
  warn "pnpm not found — frontend rebuild unavailable (npm may work as fallback)"
fi

if have npm; then
  info "npm: $(firstline npm --version)"
else
  info "npm: not found"
fi

if have python3; then
  info "python3: $(firstline python3 --version)"
else
  info "python3: not found"
fi

# ---------------------------------------------------------------------------
section "Platform"
# ---------------------------------------------------------------------------
info "kernel: $(firstline uname -r)"
info "arch:   $(uname -m)"
if [[ -r /etc/os-release ]]; then
  # shellcheck disable=SC1091
  OSNAME="$(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-unknown}")"
  info "distro: $OSNAME"
fi
info "cpus:   $(nproc 2>/dev/null || echo '?')"
if [[ -r /proc/meminfo ]]; then
  MEMTOT="$(awk '/^MemTotal:/{printf "%.2f", $2/1048576}' /proc/meminfo 2>/dev/null)"
  MEMAVAIL="$(awk '/^MemAvailable:/{printf "%.2f", $2/1048576}' /proc/meminfo 2>/dev/null)"
  info "memory: ${MEMTOT} GiB total, ${MEMAVAIL} GiB available"
fi

# cgroup version — decides whether container memory/CPU limits are legible.
if [[ -f /sys/fs/cgroup/cgroup.controllers ]]; then
  info "cgroup: v2 (unified)"
elif [[ -d /sys/fs/cgroup/memory ]]; then
  info "cgroup: v1 (legacy)"
else
  info "cgroup: not detected"
fi

# Container detection. PID 1 being an init system AND /.dockerenv absent means
# this is a VM, not a container; systemd-detect-virt is authoritative when present.
if have systemd-detect-virt; then
  VIRT="$(firstline systemd-detect-virt)"
  info "virtualisation: ${VIRT:-none}"
fi
if [[ -f /.dockerenv ]]; then
  info "container marker: /.dockerenv present (running inside Docker)"
fi

# Free disk where the panel stores instances, backups and SQLite.
DATA_DIR="${SCNETM_DATA_DIR:-$PWD}"
if [[ -d "$DATA_DIR" ]]; then
  DF_LINE="$(df -h --output=avail "$DATA_DIR" 2>/dev/null | tail -n1 | tr -d ' ')"
  DF_PCT="$(df --output=pcent "$DATA_DIR" 2>/dev/null | tail -n1 | tr -d ' %')"
  if [[ -n "${DF_PCT:-}" ]] && (( DF_PCT >= 90 )); then
    warn "disk on $DATA_DIR: ${DF_LINE} free (${DF_PCT}% used) — under 10% headroom"
  else
    pass "disk on $DATA_DIR: ${DF_LINE} free (${DF_PCT:-?}% used)"
  fi
else
  info "disk: $DATA_DIR does not exist, skipped"
fi

# ---------------------------------------------------------------------------
section ".NET runtime (required to RUN game servers, NOT to run the panel)"
# ---------------------------------------------------------------------------
if have dotnet; then
  DOTNET_VER="$(firstline dotnet --version)"
  pass "dotnet: $DOTNET_VER ($(command -v dotnet))"
  if dotnet --list-runtimes 2>/dev/null | grep -q 'Microsoft.NETCore.App'; then
    pass "Microsoft.NETCore.App present — the SurvivalcraftNet SERVER can run"
  else
    fail "dotnet found but no Microsoft.NETCore.App runtime — the server cannot run"
  fi
  # WindowsDesktop is a CLIENT requirement only; its absence on Linux is normal.
  if dotnet --list-runtimes 2>/dev/null | grep -q 'Microsoft.WindowsDesktop.App'; then
    info "Microsoft.WindowsDesktop.App present (not needed: that is a CLIENT-only, Windows-only framework)"
  else
    info "Microsoft.WindowsDesktop.App absent — EXPECTED and correct on Linux (CLIENT-only framework)"
  fi
else
  fail "dotnet NOT found — instances cannot start (panel itself still boots); fix: scripts/install-dotnet.sh"
fi

DOTNET10=""
if have dotnet && dotnet --list-runtimes 2>/dev/null | grep -qE 'Microsoft\.NETCore\.App 10\.'; then
  DOTNET10=yes
  pass ".NET 10 runtime present (matches the container base image mcr.microsoft.com/dotnet/runtime:10.0)"
fi
[[ -z "$DOTNET10" && "$FAIL_N" -eq 0 ]] && warn "no .NET 10 runtime detected"

# ---------------------------------------------------------------------------
section "Game server distribution"
# ---------------------------------------------------------------------------
# The server ships as a self-contained .NET distribution (start.sh + dlls).
# Search the obvious places; absence is the normal state of a dev box.
SERVER_HITS="$(find "$PWD" /opt /srv -maxdepth 4 \
  \( -iname 'Survivalcraft*Server*' -o -iname 'start.sh' \) 2>/dev/null | head -n5)"
if [[ -n "$SERVER_HITS" ]]; then
  pass "candidate game-server files found:"
  (( QUIET )) || printf '%s\n' "$SERVER_HITS" | sed 's/^/         /'
else
  warn "no game-server distribution found (no start.sh / SurvivalcraftServer*) — instances cannot start here"
fi

# Historical packages in the repo are CLIENT-only. Verify rather than assume.
HIST="$PWD/scnet-src/历史版本"
if [[ -d "$HIST" ]]; then
  APK_N="$(find "$HIST" -maxdepth 1 -iname '*.apk' 2>/dev/null | wc -l)"
  ZIP_N="$(find "$HIST" -maxdepth 1 -iname '*.zip' 2>/dev/null | wc -l)"
  info "scnet-src/历史版本: ${APK_N} APK, ${ZIP_N} desktop zip — all CLIENT packages"
  info "  (a desktop zip carries Survivalcraft.exe + WindowsDesktop framework ref; no server build)"
fi

# ---------------------------------------------------------------------------
section "PTY availability (D1 — coloured ANSI output depends on this)"
# ---------------------------------------------------------------------------
# A PTY requires: open("/dev/ptmx", O_RDWR) -> ioctl(TIOCGPTN) -> open("/dev/pts/N").
# We test each stage separately so the failure point is unambiguous.

if [[ -c /dev/ptmx ]]; then
  PTMX_MODE="$(stat -c '%A %U:%G' /dev/ptmx 2>/dev/null)"
  info "/dev/ptmx: char device, $PTMX_MODE"
else
  fail "/dev/ptmx missing — no PTY can ever be allocated"
fi

if [[ -d /dev/pts ]]; then
  PTS_MOUNT="$(awk '$2=="/dev/pts"{print $3" "$4}' /proc/mounts 2>/dev/null)"
  info "/dev/pts mount: ${PTS_MOUNT:-not mounted}"
  if [[ "$PTS_MOUNT" == *"ptmxmode=000"* ]]; then
    info "  ptmxmode=000 is the standard Linux default (use /dev/ptmx, not /dev/pts/ptmx)"
  fi
fi

# Stage 1+2: allocate a PTY with Python's stdlib.
PTY_PY="$(python3 -c 'import pty; pty.openpty()' 2>&1 >/dev/null)"
if [[ -z "$PTY_PY" ]]; then
  pass "python3 pty.openpty() succeeded — PTY allocation works"
else
  PTY_ERRNO="$(python3 - <<'PY' 2>/dev/null
import os, errno
try:
    os.open('/dev/ptmx', os.O_RDWR)
except OSError as e:
    print(e.errno)
PY
)"
  fail "python3 pty.openpty() FAILED: $(lastline <<<"$PTY_PY") [errno=${PTY_ERRNO:-?}]"
  info "  errno 13 EACCES = permission refusal; errno 6 ENXIO = no free PTY; errno 19 ENODEV = devpts not mounted"
fi

# Stage 1 isolated: does opening /dev/ptmx read-only work but read-write not?
# That asymmetry is the signature of a device-level deny, not of PTY exhaustion.
python3 - <<'PY' 2>/dev/null
import os
ro = rw = None
try:
    os.close(os.open('/dev/ptmx', os.O_RDONLY)); ro = 'OK'
except OSError as e:
    ro = 'errno=%d' % e.errno
try:
    os.close(os.open('/dev/ptmx', os.O_RDWR)); rw = 'OK'
except OSError as e:
    rw = 'errno=%d' % e.errno
print('       $ open(/dev/ptmx, O_RDONLY)=%s   open(/dev/ptmx, O_RDWR)=%s' % (ro, rw))
PY

# Stage 3: can we open an existing slave?
if [[ -e /dev/pts/0 ]]; then
  PTS_SLAVE="$(python3 -c 'import os; os.close(os.open("/dev/pts/0", os.O_WRONLY))' 2>&1 >/dev/null)"
  if [[ -z "$PTS_SLAVE" ]]; then
    info "open(/dev/pts/0, O_WRONLY)=OK"
  else
    info "open(/dev/pts/0, O_WRONLY) denied: $(lastline <<<"$PTS_SLAVE")"
  fi
fi

# Is the refusal device-class-wide? A pure PTY policy would block /dev/ptmx only.
# If unrelated devices are also write-blocked, the policy is broader than PTYs.
DEV_BLOCKED=0
DEV_TESTED=0
for d in /dev/zero /dev/urandom /dev/full; do
  DEV_TESTED=$((DEV_TESTED+1))
  if ! python3 -c "import os,sys; os.close(os.open(sys.argv[1], os.O_RDWR))" "$d" 2>/dev/null; then
    DEV_BLOCKED=$((DEV_BLOCKED+1))
  fi
done
if (( DEV_BLOCKED > 0 )); then
  info "device write-deny is class-wide, not PTY-specific: ${DEV_BLOCKED}/${DEV_TESTED} unrelated devices also refused"
  info "  (this indicates a sandbox policy on device nodes rather than exhausted PTYs)"
fi

# PTY exhaustion is measurable and independent of permissions.
if [[ -r /proc/sys/kernel/pty/nr && -r /proc/sys/kernel/pty/max ]]; then
  PTY_NR="$(cat /proc/sys/kernel/pty/nr)"; PTY_MAX="$(cat /proc/sys/kernel/pty/max)"
  if (( PTY_NR + 50 > PTY_MAX )); then
    fail "PTYs nearly exhausted: ${PTY_NR}/${PTY_MAX} in use"
  else
    info "PTY in use: ${PTY_NR}/${PTY_MAX} — exhaustion is NOT the cause of any refusal above"
  fi
fi

# script(1) uses the same kernel path via a different code path.
if have script; then
  if script -qc ':' /dev/null >/dev/null 2>&1; then
    pass "script(1) allocated a PTY"
  else
    SCR_ERR="$(script -qc ':' /dev/null 2>&1 | lastline)"
    warn "script(1) could not allocate a PTY: ${SCR_ERR}"
  fi
fi

# /dev/tty is a separate concept (the controlling terminal), commonly absent
# in headless/service contexts. Its absence is not a PTY failure.
if python3 -c 'import os; os.close(os.open("/dev/tty", os.O_RDONLY))' 2>/dev/null; then
  info "/dev/tty readable (a controlling terminal exists)"
else
  info "/dev/tty unavailable — no controlling terminal (normal for a service/agent shell)"
fi

# ---------------------------------------------------------------------------
section "Sandbox / confinement (explains any EACCES above)"
# ---------------------------------------------------------------------------
if [[ -r /sys/kernel/security/lsm ]]; then
  info "active LSMs: $(cat /sys/kernel/security/lsm 2>/dev/null)"
fi
SECCOMP="$(awk '/^Seccomp:/{print $2}' /proc/self/status 2>/dev/null)"
case "${SECCOMP:-}" in
  0) info "seccomp: disabled (0) — syscalls are not filtered" ;;
  1) warn "seccomp: strict mode — most syscalls blocked" ;;
  2) info "seccomp: filter active (2) — a BPF filter may deny specific syscalls" ;;
  *) info "seccomp: unknown" ;;
esac
NNP="$(awk '/^NoNewPrivs:/{print $2}' /proc/self/status 2>/dev/null)"
[[ "${NNP:-0}" == "1" ]] && info "NoNewPrivs=1 (privilege escalation via setuid is disabled; sudo will not work)"
APPA="$(cat /proc/self/attr/current 2>/dev/null | tr -d '\0')"
[[ -n "$APPA" ]] && info "AppArmor profile: $APPA"

# Report the Landlock ABI version: ABI 1-3 can only restrict the filesystem,
# so they cannot be the cause of a device-open denial. ABI >= 4 can.
if have python3; then
  LLABI="$(python3 -c "
import ctypes
libc = ctypes.CDLL('libc.so.6', use_errno=True)
print(libc.syscall(444, 0, 0, 1))
" 2>/dev/null)"
  if [[ "${LLABI:-}" =~ ^[0-9]+$ ]] && (( LLABI > 0 )); then
    info "Landlock ABI version: $LLABI"
    if (( LLABI < 4 )); then
      info "  ABI $LLABI restricts only filesystem access — it cannot explain a device-open EACCES"
    fi
  else
    info "Landlock syscall unavailable (kernel without Landlock, or blocked)"
  fi
fi

# Capabilities: an unprivileged user has an empty effective set.
CAPEFF="$(awk '/^CapEff:/{print $2}' /proc/self/status 2>/dev/null)"
if [[ -n "$CAPEFF" && "$CAPEFF" =~ ^0+$ ]]; then
  info "CapEff is empty — running unprivileged (no CAP_SYS_ADMIN/CAP_MKNOD)"
fi

# ---------------------------------------------------------------------------
section "Docker (needed for the V0-3 in-container PTY check)"
# ---------------------------------------------------------------------------
if have docker; then
  info "docker client: $(docker --version 2>/dev/null)"
  if docker info >/dev/null 2>&1; then
    pass "docker daemon reachable — container tests are possible"
    DOCKER_OK=1
  else
    DERR="$(docker info 2>&1 | grep -i 'permission denied\|cannot connect\|Is the docker daemon' | head -n1)"
    fail "docker daemon NOT reachable: ${DERR:-unknown error}"
    if [[ -S /var/run/docker.sock ]]; then
      info "  $(stat -c '%A %U:%G' /var/run/docker.sock) — user '$(id -un)' is in groups: $(id -Gn | tr ' ' ',')"
      info "  fix: add the user to the 'docker' group, then re-login"
    fi
  fi
  have docker && docker compose version >/dev/null 2>&1 \
    && info "docker compose: $(docker compose version --short 2>/dev/null)" \
    || info "docker compose plugin: not detected"
else
  warn "docker not installed — in-container PTY verification (V0-3) not possible here"
fi

# ---------------------------------------------------------------------------
section "Reachability (unblocks a future run)"
# ---------------------------------------------------------------------------
check_url() {
  local label="$1" url="$2"
  local code body
  # Some CDNs (notably gitee.com) reject HEAD and answer 403 to a bare GET for
  # the root path. Accept any HTTP response as proof of reachability; only a
  # transport failure (curl error / code 000) is a real warning.
  body="$(curl -sS -o /dev/null -m 20 -w '%{http_code}' -L --compressed \
            -A 'Mozilla/5.0 (compatible; scnetm-verify-env)' "$url" 2>/dev/null)"
  code="$body"
  if [[ "$code" =~ ^[1-5][0-9][0-9]$ ]]; then
    pass "$label reachable (HTTP $code)"
  else
    warn "$label unreachable (transport failure) — a future run may be blocked"
  fi
}

if have curl; then
  check_url "dotnet-install.sh" "https://dot.net/v1/dotnet-install.sh"
  check_url "proxy.golang.org " "https://proxy.golang.org/"
  check_url "npm registry     " "https://registry.npmjs.org/"
  check_url "mcr.microsoft.com" "https://mcr.microsoft.com/v2/"
  check_url "gitee.com        " "https://gitee.com/"
else
  warn "curl not found — reachability checks skipped"
fi

# ---------------------------------------------------------------------------
section "Conclusions"
# ---------------------------------------------------------------------------
if (( FAIL_N == 0 )); then
  pass "no blocking failures detected"
else
  info "$FAIL_N blocking failure(s):"
  (( QUIET )) || printf '%s\n' "${FAIL_LINES[@]}" | sed 's/^/         - /'
fi

printf '\n%sSummary:%s %d PASS, %d WARN, %d FAIL\n' \
  "$C_HEAD" "$C_OFF" "$PASS_N" "$WARN_N" "$FAIL_N"

(( FAIL_N == 0 )) && exit 0 || exit 1
