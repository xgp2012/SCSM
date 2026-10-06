# scnetm — developer notes

The scnetm panel (Go) manages SurvivalcraftNet game-server instances: it creates
them from a template, starts/stops them under a PTY, streams their coloured
console to the browser, and edits their configuration.

Plan of record: [`SCNETM-开服面板实现计划.md`](../SCNETM-开服面板实现计划.md).

> **Current status: T1 — project skeleton that runs.**
> The panel boots, migrates its database, serves `/healthz` and serves the
> embedded frontend. Instance lifecycle, config editing, files, worlds and
> backups are other agents' packages and are not wired into the router yet
> (see the marked mount point in `cmd/scnetm/main.go`).

---

## Environment requirements

| Component | Needed for | Present here? |
|---|---|---|
| Go 1.22+ (1.27.1 used) | building the panel | yes |
| A C toolchain | **not needed** — `CGO_ENABLED=0` | n/a |
| Node + a package manager | building the frontend only | partial |
| **.NET 10 runtime** | **starting a game-server instance** | **NO** |
| Game-server package (`Survivalcraft.dll`, `Content.scpak`) | starting an instance | **NO** |

### The .NET 10 runtime and the server package are NOT installed

This is deliberate and by design: the panel is an **out-of-process** manager. It
never loads the server's assemblies, so it has no .NET dependency of its own.
Concretely, on a machine with no .NET at all:

* the panel **starts normally**;
* `/healthz` answers;
* the embedded UI is served;
* the SQLite schema is created and migrated;
* the only thing that fails is **starting an instance**, which will report a
  clear "dotnet not found" error.

Do not "fix" a missing runtime by adding a Go-side .NET dependency. Install the
runtime per plan §9.1 when you actually need to run an instance:

```bash
curl -sSL https://dot.net/v1/dotnet-install.sh | bash -s -- --channel 10.0 --runtime dotnet
mkdir -p /srv/scnetm/templates/server-x26.07.01.01
unzip 服务端X26.07.01.01.zip -d /srv/scnetm/templates/server-x26.07.01.01
```

---

## Build

```bash
make build          # frontend copy + go build -> build/scnetm
make build VERSION=0.2.0
```

`make build` always does two things in order:

1. copies `web/dist/*` into `internal/webui/dist/`;
2. runs `go build -ldflags ... ./cmd/scnetm`.

Plain `go build ./...` also works; it simply embeds whatever is already in
`internal/webui/dist`.

## Run

```bash
make run                              # uses ./config.yaml
./build/scnetm --config /etc/scnetm/config.yaml
./build/scnetm --listen 127.0.0.1:9090 --data-dir /tmp/scnetm-data
./build/scnetm --version
./build/scnetm --help
```

Flags override the config file: `--config`, `--listen`, `--data-dir`,
`--version`. Logging is structured JSON on stdout; `SCNETM_LOG_LEVEL=debug`
raises verbosity.

Start-up sequence, in order: load config -> apply defaults -> validate ->
create `data_dir` / `instances_dir` -> open SQLite -> migrate -> bind -> serve.
A port clash or a bad config fails **before** the listener is announced.

Endpoints:

| Path | Purpose |
|---|---|
| `GET /healthz` | liveness + version; unauthenticated; no .NET needed |
| `/` | embedded SPA, with fallback to `index.html` for client-side routes |
| `/api/v1/*`, `/ws/*` | **not mounted yet** — see `cmd/scnetm/main.go` |

## Test

```bash
make test           # go test ./...
make vet            # go vet ./...
gofmt -l .          # must print nothing
```

---

## The `go:embed` workaround (read this before touching the frontend build)

**`go:embed` cannot reference a path outside the directory containing the
directive.** `//go:embed ../../web/dist` is a compile error — the `..` is
rejected outright, there is no flag that relaxes it.

The frontend therefore cannot be embedded directly from `web/dist`. The layout
is:

```
web/dist/                    <- Vite build output (frontend's own directory)
internal/webui/dist/         <- COPY of the above; this is what gets embedded
internal/webui/embed.go      <- //go:embed all:dist
```

`make build` performs the copy. `internal/webui/dist` is a build artefact:
git-ignored except for `.gitkeep`, which exists only so the directory is present
and `go build` succeeds on a fresh checkout before any frontend build.

`webui.Available()` reports whether a real build was embedded (anything beyond
`.gitkeep`). When it is false — the normal state of a fresh clone — `main` logs
a warning and serves `webui.PlaceholderPage` at `/` instead of failing to start.
That page links to `/healthz` and explains how to build the UI.

If you change the frontend output directory, update `DIST_SRC` in the Makefile.

---

## Configuration

`configs/config.example.yaml` is annotated and is the reference. Every key is
optional; omitted keys take their default. **Unknown keys are a start-up
error** — strict decoding, so `instance_dir:` (singular, a typo) fails loudly
instead of silently using the default.

Relative paths in `data_dir` / `instances_dir` / `template_dir` resolve against
the **config file's directory**, not the process CWD, so behaviour does not
depend on how the binary was invoked.

The default `port_pool: [28887, 28900]` in the example is the inclusive range
28887..28900 (14 UDP ports); it expands to a concrete list at load time.
Duplicate or out-of-range ports are rejected.

---

## Layout

```
cmd/scnetm/main.go          entry point: flags, boot, HTTP, graceful shutdown
internal/version/           version string + build info (ldflags-injected)
internal/config/panel.go    panel's own config.yaml loader   [T1]
internal/store/migrate.go   schema DDL + migration runner    [T1]
internal/webui/embed.go     go:embed of the built frontend   [T1]
internal/{api,supervisor,ansi,auth,files,world,config,store}/  other agents
web/                        Vue 3 + Vite frontend
configs/config.example.yaml annotated config reference
docs/README.md              this file
```

`internal/config` and `internal/store` are shared packages: several agents add
files to them. `panel.go` and `migrate.go` are the T1-owned files; do not
rewrite them wholesale.

### Known cleanup item: `internal/supervisor/stub/ansi/`

`internal/supervisor/stub/ansi/` is a **temporary stub** of the `internal/ansi`
package. It existed only because the supervisor package needed an `ansi.Line`
type before the real package landed. The real implementation now exists at
`internal/ansi/ansi.go`, so this stub is dead weight and should be deleted.

It is **not** deleted here because the directory belongs to the supervisor
owner, and deleting another agent's package mid-flight would break their build.
The supervisor owner should remove it once `internal/supervisor` imports
`scnetm/internal/ansi` directly.

### Local build environment

`.goenv.sh` at the repo root pins the caches inside the repo so concurrent
agents share them:

```bash
source .goenv.sh && go build ./...
```

It sets `GOCACHE`/`GOMODCACHE` to `.gocache/`/`.gomodcache/`, `GOFLAGS=-mod=mod`
(so builds may update `go.mod`), `GOSUMDB=off` and `GOTOOLCHAIN=local`. Those
directories are git-ignored.

---

## Database

SQLite via `modernc.org/sqlite` (pure Go, so `CGO_ENABLED=0` works). The DSN
sets `busy_timeout(5000)`, `journal_mode(WAL)` and `foreign_keys(1)`, and the
pool is pinned to **one connection** because SQLite permits a single writer —
a wider pool turns concurrent writes into `SQLITE_BUSY` instead of queueing
them.

Migrations live in an ordered slice in `internal/store/migrate.go`, are recorded
in `schema_migrations`, and each runs in a transaction with its ledger insert.
`store.Migrate` is idempotent and runs on every start, so upgrading is "replace
the binary". **Append new migrations; never edit or renumber a released one.**

On an empty `users` table, migration seeds `id=1, username=admin, role=admin`
with `password_hash = store.FirstRunPasswordMarker`. That value is deliberately
not a valid bcrypt hash, so it can never be matched by a password check; the API
must detect it (`store.IsFirstRunHash`) and force password setup before serving
anything else.

---

## Security notes for contributors

* The panel binds `127.0.0.1` by default and has **no TLS**. Terminate TLS at a
  reverse proxy before exposing it (plan §5.7).
* File handling is the highest-risk area (plan §5.7): every path from HTTP/WS
  goes through `internal/files` path validation; never concatenate paths.
* Never build a shell command string. `exec.Command` with an argument vector
  only; command injection into the console is a privilege escalation.
