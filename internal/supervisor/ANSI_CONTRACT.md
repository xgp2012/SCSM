# ANSI_CONTRACT.md — what `internal/supervisor` assumes about `internal/ansi`

`internal/ansi` is authored by a concurrent agent (task T2). `internal/supervisor`
(T3) depends on it but **never creates or edits it** — that directory is T2's
exclusive scope, and T3's writes there are (correctly) refused by the sandbox.

**Status: RESOLVED — `internal/ansi` landed during T3 and the whole suite now
runs against the real decoder.**

The temporary stub (`internal/supervisor/stub/`) has been **deleted**. The
contract below was verified line-by-line against `internal/ansi/ansi.go` and
matched exactly, so no source change was needed beyond removing the stub. The
final verification (`go test ./internal/supervisor/ -race`) exercises the real
package end to end: the rule-matching, raw/plain fidelity, ring-buffer and
log-file tests all assert against real `ansi` output.

Kept as living documentation of the interface the supervisor depends on, and of
the behavioural assumptions that a future refactor of `internal/ansi` must not
break silently.

## Assumed surface (frozen by the task brief)

```go
package ansi

type Line struct {
    Raw, Plain   string
    Truncated    bool
    Partial      bool
}

type DecoderOptions struct {
    MaxLineBytes int
    KeepRaw      bool
    TabWidth     int
}

func NewDecoder(opts DecoderOptions) *Decoder
func (d *Decoder) Write(p []byte) []Line
func (d *Decoder) Flush() []Line
func Strip(s string) string
func HasANSI(s string) bool
```

## How supervisor uses it

| Site | Usage |
|---|---|
| `ansi.go` | `type ansiLine = ansi.Line` — a type **alias**, so the two are identical by construction |
| `ansi.go` | `newLineDecoder` returns `ansi.NewDecoder(ansi.DecoderOptions{MaxLineBytes: 8192, KeepRaw: true, TabWidth: 8})` |
| `options.go` | `Options.OnLineFn func(ansi.Line)` — written in terms of `ansi.Line` directly, so a field/signature drift is a **compile error**, not a silent behaviour change |
| `logpipe.go` | `lineDecoder` interface: `Write([]byte) []ansi.Line`, `Flush() []ansi.Line` |

The `lineDecoder` interface exists **only** as a test seam so `_test.go` files can
inject a deterministic fake. Production always calls `ansi.NewDecoder`; there is
no reflection, no `interface{}` and no duplicated `Line` struct.

## Why this cannot silently drift

1. `ansiLine` is an alias, not a defined type: if T2 renames a field, this package
   stops compiling at `rec.Raw = l.Raw`.
2. `newLineDecoder` names every `DecoderOptions` field, so a rename there is also a
   compile error.
3. `Options.OnLineFn func(ansi.Line)` is part of the public API, so the API layer
   breaks loudly too.

## Behavioural assumptions relied on (verify against T2's tests when it lands)

1. **Framing**: `Write` splits on `\r?\n`, one `Line` per terminator. A `\n`-less
   tail is buffered until `Flush()`, which emits it with `Partial: true`.
2. **`\r` handling**: a bare `\r` (progress-bar redraw) must not split a line.
   Supervisor does not care whether T2 collapses overwrites; it only requires that
   no line is *lost*.
3. **`Raw` is byte-faithful** when `KeepRaw` is true: ESC sequences pass through
   untouched. Supervisor writes `Raw` to `logs/<date>.log` and its own tests assert
   that file contains raw `0x1b` bytes.
4. **`Plain` never contains ANSI.** Supervisor asserts `!HasANSI(Plain)` and greps
   `logs/<date>.plain.log` for `0x1b` in `logpipe_test.go`.
5. **Truncation is bounded**: a line longer than `MaxLineBytes` yields exactly one
   `Line` with `Truncated: true` and the excess is discarded (not carried into the
   next line). Supervisor's ring-buffer memory bound depends on this.
6. **Invalid UTF-8 is tolerated**: `Plain` is valid UTF-8, and no byte sequence
   from `/dev/ptmx` may panic.
7. **No blocking**: `Write`/`Flush` never block on I/O; supervisor treats a slow
   subscriber as its own problem (drop + "dropped N lines" marker).

## Migration checklist for T2

- [ ] Keep the five exported names above byte-compatible.
- [ ] `internal/supervisor` then needs only `rm internal/supervisor/ansi_stub_test.go`
      and `rm -r internal/supervisor/stub/` — **no source change**.
- [ ] Re-run `go test ./internal/supervisor/ -race -count=1`. The log-pipeline,
      ANSI-fidelity and rule-matching tests exercise the real decoder through the
      same code paths the stub covers, so the suite is the migration gate.
