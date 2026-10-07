# scnetm — developer notes

The scnetm panel (Go) manages SurvivalcraftNet game-server instances: it creates
them from a template, starts/stops them under a PTY, streams their coloured
console to the browser, and edits their configuration.

Plan of record: [`SCNETM-开服面板实现计划.md`](../SCNETM-开服面板实现计划.md).

Companion documents:

| Document | What it is for |
|---|---|
| [`DEVELOPMENT.md`](./DEVELOPMENT.md) | contributor guide: package map, the traps, dev workflow, conventions |
| [`API.md`](./API.md) | `/api/v1` contract, error codes, WebSocket protocol |
| [`验证报告.md`](./验证报告.md) | V0-1…V0-7 verification results, graded A/B/C/D |
| [`环境与可运行性验证.md`](./环境与可运行性验证.md) | environment ground truth and its evidence |

> **Current status: initial implementation complete and running.**
> All packages build, vet and test green; the panel boots, migrates its database,
> serves `/healthz`, serves the embedded frontend, and mounts the full REST +
> WebSocket API at `/api/v1/*` and `/ws/*`. Features whose backend is a `nop` in
> this build answer `501 not_implemented` with a `details.reason` rather than
> silently returning an empty result.
>
> **Language policy: the panel is Simplified Chinese (zh-CN).** Every
> user-facing string — API `message` fields, the `.NET` install guidance, the
> scheduler notice broadcast into the game, the placeholder page, and the
> operator logs — is Chinese. Machine-readable identifiers stay English by
> design: `code` values (`not_implemented`, `conflict`, …), `details.reason`
> slugs, JSON field names and event names. See
> [Localization](#localization-zh-cn) before adding a new user-facing string.

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

If `make` is unavailable, do the same thing by hand — the copy is the part that
matters:

```bash
mkdir -p internal/webui/dist && cp -a web/dist/. internal/webui/dist/
CGO_ENABLED=0 go build -trimpath -o build/scnetm ./cmd/scnetm
```

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
| `/api/v1/*` | REST API (mounted; see `cmd/scnetm/api.go`) |
| `/ws/instances/:id/console` | console stream (raw + ANSI); commands are sent here, **not** over REST |
| `/ws/events` | global panel events (state changes, alerts) |

Console commands travel over the WebSocket as a message frame. There is no REST
command endpoint — do not add one without reading plan §5.5/§6.6 first.

## Test

```bash
make test           # go test ./...
make vet            # go vet ./...
gofmt -l .          # must print nothing
```

---

## Releases

Publishing is automated by [`.github/workflows/go-build.yml`](../.github/workflows/go-build.yml).
Pushing a `v*` tag builds the binary, tests it, and creates a GitHub release:

```bash
git tag -a v0.2.0 -m "SCNETM v0.2.0"
git push origin v0.2.0
```

Each release carries three assets:

| Asset | Contents |
|---|---|
| `scnetm-linux-amd64` | the bare static binary |
| `scnetm-linux-amd64.tar.gz` | binary, `config.example.yaml`, `scnetm.service`, `README.md` |
| `SHA256SUMS` | checksums for the two above |

Verify a download before running it:

```bash
curl -sSLO https://github.com/xgp2012/SCSM/releases/download/v0.2.0/SHA256SUMS
curl -sSLO https://github.com/xgp2012/SCSM/releases/download/v0.2.0/scnetm-linux-amd64
sha256sum -c SHA256SUMS
```

### Things that will bite you

* **`permissions: contents: write` is required on the release job.** With the
  default `contents: read` the build goes green and only the release step fails,
  with `Resource not accessible by integration`. The workflow sets `read` at the
  top level and elevates inside the job.
* **The tag must match the version compiled into the binary.** `VERSION` comes
  from `GITHUB_REF_NAME`, so on a tag push it is the tag name; a step asserts
  `--version` reports exactly that string, and fails the release otherwise.
* **A bare `go build` must never be used for a release.** It succeeds with exit
  code 0 while embedding a placeholder page. The workflow's "Verify frontend is
  embedded" step starts the binary and asserts `/` serves the real UI, because
  grepping the binary for a marker cannot distinguish the two — the placeholder
  text is compiled into every build.
* **Version lines.** The `v0.1.x` tags belong to an earlier Node.js
  implementation of this panel; that history was replaced on `main` and is kept
  on the `legacy-nodejs-archive` branch. The Go implementation starts at
  `v0.2.0`. Do not reuse a `v0.1.x` tag.

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
cmd/scnetm/api.go           wires internal/api's Deps and mounts /api/v1 + /ws
internal/version/           version string + build info (ldflags-injected)
internal/config/            panel config.yaml + ServerSetting.json/Settings.xml/Project.json
internal/store/             SQLite: schema DDL, migration runner, repositories
internal/webui/embed.go     go:embed of the built frontend
internal/api/               REST + WebSocket handlers, DTOs, auth middleware
internal/supervisor/        process lifecycle: PTY, state machine, log pipeline
internal/ansi/              byte-stream framer + ANSI decoder (see ANSI_CONTRACT.md)
internal/auth/              JWT, bcrypt, RBAC
internal/files/             path validation, upload, unzip (highest-risk area)
internal/world/             world scan/import/export/manage
internal/backup/            backup tiers + retention
internal/scheduler/         cron jobs (backup, restart, command, announce)
internal/notify/            webhook channels (DingTalk, WeCom, QQ bot, custom)
internal/runtime/           .NET discovery + instance template provisioning
web/                        Vue 3 + Vite frontend
configs/config.example.yaml annotated config reference
docs/README.md              this file
```

Most packages are self-contained and own their own files. `internal/config` and
`internal/store` are shared: add new files rather than rewriting existing ones
wholesale, and keep `panel.go` / `migrate.go` behaviourally stable.

### Local build environment

The repo pins its Go caches locally so they survive across checkouts and do not
depend on `$HOME` being writable:

```bash
export GOFLAGS=-mod=mod GOSUMDB=off GOPRIVATE='*' GOTOOLCHAIN=local \
       GOMODCACHE=$PWD/.gomodcache GOCACHE=$PWD/.gocache
go build ./...
```

`.gocache/` and `.gomodcache/` are git-ignored. `GOTOOLCHAIN=local` stops Go
from trying to download a different toolchain — the sandbox has no network for
that. A `.goenv.sh` helper that did the same thing used to live at the repo root;
it is gone, so set these explicitly (or add your own untracked `.goenv.sh`).

**The first build after a fresh clone is slow** (several minutes): the module
cache is empty and every dependency is fetched. It can look like a hang. Give it
a generous timeout rather than assuming a deadlock.

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

**`configs/config.example.yaml` ships with `default_admin_password: "adfmin",
which opts out of that setup screen**: the seeded account gets that password and
the panel is immediately usable as `admin` / `adfmin`. Comment the key out to get
the forced-setup behaviour back.

> **This is a known, deliberate weakening and it is worth being explicit about
> it.** The value is committed to a public repository, and the panel's default
> listen address is `0.0.0.0:7000`, so a panel deployed with the example config
> unchanged is accessible to anyone who has read this repo. To make the shipped
> value representable, `auth.MinPasswordLength` was **lowered from 8 to 6**,
> which applies to *every* password in the panel — including the in-panel
> change-password form — not just the seeded one.
>
> If you change the initial password to something longer, raise
> `auth.MinPasswordLength` and `config.MinAdminPasswordLength` back to 8;
> `TestDefaultAdminPasswordLengthsMatchAuthPolicy` fails if only one is changed.

`main.go` hashes the configured value and calls `store.SeedAdminPassword` after
`Migrate`. Two properties keep that from becoming a footgun, and both are
covered by tests:

* The `UPDATE` is conditional on the stored hash still being a first-run marker,
  so it can **never** overwrite a password set later. Leaving the line in
  `config.yaml` after the first start is therefore harmless — verified by
  restarting with the key still present and confirming the changed password
  still works while the configured one is refused.
* The value is validated at load time (`config.MinAdminPasswordLength`, 6), so a
  too-short password fails **before** the database is opened rather than
  producing a panel nobody can log into.

`Panel.DefaultAdminPassword` is `json:"-"`, blanked by `Panel.Redacted()`, and
rendered by `Panel.String()` as `(set)`/`(unset)` only. Do not add a path that
prints it. The length bounds in `internal/config` are copies of
`internal/auth`'s (importing would invert the dependency direction);
`TestDefaultAdminPasswordLengthsMatchAuthPolicy` is the tripwire that keeps them
equal.

---

## Localization (zh-CN)

The panel speaks Simplified Chinese. There is **no i18n framework** — no
`vue-i18n`, no `locales/` directory, no message catalogue. Strings are written
inline in the language they are served in. This is a deliberate choice for a
single-language product; do not introduce an i18n layer without a second locale
actually being required.

### What must be Chinese

| Surface | Where |
|---|---|
| API error/notice `message` | `internal/api/*.go` — `BadRequest`, `Conflict`, `NotFound`, `Forbidden`, `Unavailable`, `Timeout`, `PayloadTooLarge`, `ValidationFailed`, `NotImplemented` |
| Sentinel errors rendered via `Classify` | `internal/api/deps.go`, `internal/api/safepath.go`, `internal/api/supervisor.go` |
| Operator guidance | `internal/runtime/dotnet.go` (`InstallHint`), `internal/api/system.go` |
| Player-visible broadcast | `internal/scheduler/scheduler.go` (sent into the game) |
| Placeholder page | `internal/webui/embed.go` (`PlaceholderPage`) |
| Operator logs | `cmd/scnetm/*.go`, `internal/api/middleware.go` |
| Frontend | `web/src/**` — already fully zh-CN, including `Message.*` toasts |

### What must stay English

These are contracts, not prose. Translating them breaks clients and tests:

* **`code` values** — `not_implemented`, `conflict`, `unauthorized`, … The
  comment on `CodeInternal` in `internal/api/errors.go` states the rule: codes are
  stable so clients keep working *even if messages are reworded*.
* **`details.reason` slugs** — `path_traversal`, `absolute_path`, `null_byte`, …
* **`internal/files` `Reason*` constants** — `traversal`, `unsafe-name`, …
* **Event names** — `term.redirected`, `server.listening`, `world.loaded`, …
* **JSON field names, YAML keys, CLI flags, URL paths.**
* **Font names** in `web/src/components/console/XtermTerminal.vue` — `Cascadia
  Mono`, `Liberation Mono` must match real installed fonts.

### Rules when adding a string

1. Write the user-facing text in Chinese directly; do not add a translation key.
2. Never change a `code`, `reason` or event name to "match" a reworded message.
3. Keep `%s` / `%q` / `%d` verbs intact — several messages interpolate instance
   names, ports and states.
4. If a test asserts the message text, assert on the **intent** in Chinese (e.g.
   `strings.Contains(msg, "停止")` for "tells the operator to stop"), not on a
   full sentence — wording will change again.
5. Run `gofmt -l ./cmd ./internal` and `go test ./...` before committing.

---

## Security notes for contributors

* The panel binds `127.0.0.1` by default and has **no TLS**. Terminate TLS at a
  reverse proxy before exposing it (plan §5.7).
* File handling is the highest-risk area (plan §5.7): every path from HTTP/WS
  goes through `internal/files` path validation; never concatenate paths.
* Never build a shell command string. `exec.Command` with an argument vector
  only; command injection into the console is a privilege escalation.
