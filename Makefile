# scnetm — build and run
#
# Linux only. CGO is disabled everywhere: the SQLite driver is pure Go, so the
# panel is a single static binary with no libc or libsqlite3 dependency.
#
# Targets:
#   make build   copy the frontend build into internal/webui/dist, then go build
#   make run     build and run the panel against ./config.yaml
#   make test    go test ./...
#   make web     placeholder that delegates to web/ (owned by the frontend)
#   make clean   remove build artefacts

SHELL := /bin/bash

BINARY      := scnetm
CMD_PKG     := ./cmd/scnetm
BUILD_DIR   := build
DIST_SRC    := web/dist
DIST_DST    := internal/webui/dist
CONFIG      ?= config.yaml

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
# `git rev-parse HEAD` prints the literal string "HEAD" (and fails) when the
# repository has no commits yet, so `|| echo unknown` never fires and the value
# leaks a newline into -ldflags, breaking the build. `--verify --quiet` exits
# with no output instead, which the fallback handles correctly.
COMMIT      ?= $(shell git rev-parse --verify --quiet HEAD 2>/dev/null || echo unknown)
BUILD_TIME  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# VERSION and COMMIT can come from the environment (CI passes GITHUB_REF_NAME /
# GITHUB_SHA). Sanitise them: a stray single quote or newline would terminate
# the -ldflags string below and produce a confusing shell syntax error.
VERSION     := $(shell printf '%s' '$(VERSION)' | tr -d "'\"\n")
COMMIT      := $(shell printf '%s' '$(COMMIT)' | tr -d "'\"\n")

LDFLAGS := -s -w \
	-X 'scnetm/internal/version.Version=$(VERSION)' \
	-X 'scnetm/internal/version.Commit=$(COMMIT)' \
	-X 'scnetm/internal/version.BuildTime=$(BUILD_TIME)'

GOFLAGS := -trimpath
export CGO_ENABLED := 0

.PHONY: all build run test vet fmt tidy web clean help

all: build

## build: copy the frontend into the embed directory, then compile the panel.
##
## The copy is not optional. go:embed cannot reference paths outside its own
## package directory, so ../../web/dist is a compile error; internal/webui/dist
## is a build artefact that exists only to be embedded. See docs/README.md.
build: | $(DIST_DST)
	@if [ -d "$(DIST_SRC)" ] && [ -n "$$(ls -A $(DIST_SRC) 2>/dev/null | grep -v '^\.gitkeep$$')" ]; then \
		echo "==> copying $(DIST_SRC) -> $(DIST_DST)"; \
		rm -rf $(DIST_DST)/*; \
		cp -a $(DIST_SRC)/. $(DIST_DST)/; \
	else \
		echo "==> no frontend build in $(DIST_SRC); the panel will serve a placeholder page"; \
		echo "    run 'make web' first to embed the real UI"; \
	fi
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) $(CMD_PKG)

$(DIST_DST):
	@mkdir -p $(DIST_DST)
	@touch $(DIST_DST)/.gitkeep

## run: build, then start the panel. Override the config with CONFIG=path.
run: build
	./$(BUILD_DIR)/$(BINARY) --config $(CONFIG)

## test: run the unit tests.
test:
	CGO_ENABLED=0 go test ./...

## vet: run static analysis.
vet:
	CGO_ENABLED=0 go vet ./...

## fmt: format all Go source.
fmt:
	gofmt -w .

## tidy: sync go.mod/go.sum.
tidy:
	go mod tidy

## web: delegate to the frontend project.
##
## The frontend is owned by web/ (Vue 3 + Vite + fuxsto-design). This target
## exists so the panel build has one entry point; it intentionally does not
## assume a package manager beyond what web/package.json declares.
web:
	@if [ -f web/package.json ]; then \
		if [ -d web/node_modules ]; then \
			echo "==> building frontend"; \
			cd web && (npm run build || pnpm run build); \
		else \
			echo "web/node_modules missing — run 'cd web && npm install' first" >&2; \
			exit 1; \
		fi \
	else \
		echo "web/package.json not found; nothing to build" >&2; \
		exit 1; \
	fi

## clean: remove build output and the copied frontend.
clean:
	rm -rf $(BUILD_DIR)
	@if [ -d $(DIST_DST) ]; then \
		find $(DIST_DST) -mindepth 1 ! -name '.gitkeep' -delete; \
	fi

## help: list targets.
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
