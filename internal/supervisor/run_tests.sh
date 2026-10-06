#!/usr/bin/env bash
# run_tests.sh — thin wrapper that sources the repo's .goenv.sh and runs the Go
# tool for internal/supervisor.
#
# Why a wrapper exists at all: $HOME/go/pkg/mod and $HOME/.cache/go-build are NOT
# writable in this sandbox, so the module and build caches must live inside the
# workspace. .goenv.sh already sets GOMODCACHE/GOCACHE/GOFLAGS correctly, so this
# script only has to source it. There is no stub, no module replace and no
# go.mod rewriting — `internal/ansi` is real and is imported directly.
#
# Usage:  bash internal/supervisor/run_tests.sh test -race -count=1 -v
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

if [ -f .goenv.sh ]; then
  # shellcheck disable=SC1091
  source .goenv.sh
else
  echo "[run_tests] WARNING: .goenv.sh missing; falling back to workspace caches" >&2
  export GOMODCACHE="$ROOT/.gomodcache"
  export GOCACHE="$ROOT/.gocache"
  export GOFLAGS=-mod=mod
  export GOSUMDB=off
fi

echo "[run_tests] module:    $(awk '/^module /{print $2; exit}' go.mod)"
echo "[run_tests] go:        $(go version)"
echo "[run_tests] GOMODCACHE=$GOMODCACHE"
echo "[run_tests] GOCACHE=   $GOCACHE"

# Always target the package path, never ./... — sibling packages are edited by
# other agents in parallel.
exec go "$@" ./internal/supervisor/
