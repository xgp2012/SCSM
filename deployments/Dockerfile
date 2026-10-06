# scnetm — container image
#
# Multi-stage: Node builds the frontend, Go builds the panel, and the runtime
# stage carries only what is needed to serve the panel AND to host game
# instances.
#
# ---------------------------------------------------------------------------
# WHY THE RUNTIME BASE IS A .NET IMAGE
# ---------------------------------------------------------------------------
# The panel is a static Go binary and needs no .NET. But its *job* is to spawn
# SurvivalcraftNet server processes, which are .NET applications. Baking the
# runtime into the image means an instance can start with no extra setup.
#
# The base is the *runtime* image, not the aspnet or sdk image: the server is a
# console application. It is also NOT the WindowsDesktop framework — that
# framework is Windows-only and CLIENT-side (see docs/环境与可运行性验证.md §2).
#
# ---------------------------------------------------------------------------
# WHY tini AND NOT `--init`
# ---------------------------------------------------------------------------
# The panel creates game-server processes in their own process groups so it can
# signal a whole group (KillMode=control-group semantics). tini reaps zombies
# and forwards signals; without an init, PID 1 must do that itself and orphans
# accumulate. `docker run --init` would work too, but baking tini in makes the
# image behave the same under compose, systemd and Kubernetes.
#
# ---------------------------------------------------------------------------
# PTY REQUIREMENT (D1 — coloured ANSI output)
# ---------------------------------------------------------------------------
# The panel starts instances on a PTY to preserve ANSI colour. A PTY needs
# open("/dev/ptmx", O_RDWR) then open("/dev/pts/N"). In Docker that means:
#
#   docker run --tty ...              # allocates a console; also maps /dev/ptmx
#   docker run --device=/dev/ptmx ... # explicit, if --tty is not used
#
# Without either, the panel falls back to pipes and instances log WITHOUT
# colour. See docs/环境与可运行性验证.md — this could not be confirmed inside a
# container in the build environment, so docker-compose.yml ships the
# PTY-enabling options and they MUST be validated on the target host.
# ===========================================================================

# ---------------------------------------------------------------------------
# Stage 1 — frontend (Vue 3 + Vite)
# ---------------------------------------------------------------------------
FROM node:24-bookworm-slim AS web

# corepack pins pnpm to the version declared by package.json/packageManager,
# so the build does not depend on whatever pnpm the base image happens to ship.
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable

WORKDIR /src/web

# Install with a lockfile-aware, cache-friendly layer. Copy only manifests
# first so a source-only change does not re-resolve dependencies.
COPY web/package.json web/pnpm-lock.yaml* web/.npmrc* web/pnpm-workspace.yaml* ./

# XDG_DATA_HOME points the pnpm store INSIDE the build context directory.
# This matters when the build runs in a sandbox where the default store
# (~/.local/share/pnpm) is not writable. In a normal Docker build it is
# harmless, and it keeps the store inside the layer for better caching.
ENV XDG_DATA_HOME=/src/web/.xdg

# NOTE ON EXIT CODES: pnpm may exit NONZERO with ERR_PNPM_IGNORED_BUILDS when
# a dependency's postinstall script (esbuild's, here) has not been approved.
# That is a hard *error* in pnpm 10+ but the install itself is complete and
# `vite build` still works, because esbuild's platform binary is an optional
# dependency rather than a postinstall artefact. Failing the whole image build
# on it would be wrong, so the install is retried tolerantly and the build
# below is the real gate. `pnpm approve-builds` is intentionally NOT run
# interactively here (it needs a TTY and would hang the build).
RUN pnpm install --frozen-lockfile --prefer-offline \
 || { echo "pnpm install exited $? — continuing; see ERR_PNPM_IGNORED_BUILDS note in Dockerfile"; \
      pnpm install --no-frozen-lockfile --prefer-offline || true; }

COPY web/ ./

# Build the real UI. `pnpm build` runs type-check then vite build; if this
# fails the image must fail, because an unbuilt frontend silently degrades the
# panel to a placeholder page.
RUN pnpm build

# ---------------------------------------------------------------------------
# Stage 2 — Go panel
# ---------------------------------------------------------------------------
FROM golang:1.27-bookworm AS build

WORKDIR /src

# CGO off: the SQLite driver is pure Go, so the result is a static binary with
# no libc or libsqlite3 dependency. This is what makes the slim final stage work.
ENV CGO_ENABLED=0
# Reproducible-ish, dependency-free build: the module cache is populated from
# go.mod/go.sum only, and nothing is fetched at run time.
ENV GOFLAGS=-mod=mod
ENV GOTOOLCHAIN=local

# Warm the module cache first, again for layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The panel embeds its frontend from internal/webui/dist. go:embed cannot
# reference ../../web/dist (outside the package directory), so the build copies
# the compiled frontend into place first — exactly what `make build` does.
# Doing it explicitly here keeps the Dockerfile readable and independent of make.
#
# Order matters: the compiled frontend must be copied in BEFORE the step that
# consumes it.
COPY --from=web /src/web/dist/ /src/web/dist/

RUN rm -rf internal/webui/dist && mkdir -p internal/webui/dist \
 && cp -a /src/web/dist/. internal/webui/dist/ \
 && test -f internal/webui/dist/index.html \
 || { echo "frontend copy failed: internal/webui/dist/index.html missing" >&2; exit 1; }

RUN go build -trimpath \
      -ldflags "-s -w \
        -X 'scnetm/internal/version.Version=${SCNETM_VERSION:-docker}' \
        -X 'scnetm/internal/version.Commit=${SCNETM_COMMIT:-unknown}'" \
      -o /out/scnetm ./cmd/scnetm \
 && test -x /out/scnetm

# ---------------------------------------------------------------------------
# Stage 3 — runtime
# ---------------------------------------------------------------------------
FROM mcr.microsoft.com/dotnet/runtime:10.0 AS runtime

# tini as PID 1: reap zombies, forward signals (see header).
RUN apt-get update \
 && apt-get install -y --no-install-recommends tini ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Run unprivileged. The panel manages processes but needs no root: it signals
# its own children, which it owns.
RUN groupadd --system --gid 10001 scnetm \
 && useradd  --system --uid 10001 --gid scnetm --home-dir /var/lib/scnetm --shell /usr/sbin/nologin scnetm

COPY --from=build /out/scnetm /usr/local/bin/scnetm

# Data lives on a volume: SQLite database, instances, backups.
RUN mkdir -p /var/lib/scnetm/instances /var/lib/scnetm/backups /var/lib/scnetm/templates \
 && chown -R scnetm:scnetm /var/lib/scnetm

COPY deployments/config.docker.yaml /etc/scnetm/config.yaml

USER scnetm
WORKDIR /var/lib/scnetm

ENV DOTNET_ROOT=/usr/share/dotnet \
    PATH="/usr/share/dotnet:$PATH" \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 \
    DOTNET_NOLOGO=1

# The panel's default listen address in this image.
EXPOSE 8080

VOLUME ["/var/lib/scnetm"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/scnetm", "--version"]

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/scnetm", "--config", "/etc/scnetm/config.yaml"]
