// Package supervisor is the process and log hub of scnetm.
//
// It owns the lifecycle of a single SurvivalcraftNet server instance:
//
//   - Runner   launches/stops/restarts one process (process group isolated, PTY
//     preferred with a pipe fallback), implements the §5.4 stop ladder, and
//     exposes a command channel.
//   - LogPipe  frames the raw byte stream, keeps an in-memory ring buffer of
//     ANSI-bearing lines, dual-writes logs/<date>.log (raw) and
//     logs/<date>.plain.log (stripped), and fans out to subscribers.
//   - StateMachine drives Created/Starting/Running/Stopping/Stopped/Crashed/
//     Failed from process liveness *plus* log anchors (§5.1).
//   - Metrics  samples /proc/<pid>/{stat,status,io,fd} (§6.6).
//   - Manager  a concurrency-safe instanceID -> *Runner registry plus a global
//     event bus for the API's /ws/events.
//
// Target platform is Linux only; all process, PTY and /proc code lives behind
// //go:build linux.
package supervisor

import (
	"fmt"
	"time"

	"scnetm/internal/ansi"
)

// Default tuning values, applied by normalizeOptions when a field is zero.
const (
	DefaultTerm         = "xterm-256color"
	DefaultColorMode    = ColorModeAuto
	DefaultRows         = 40
	DefaultCols         = 160
	DefaultStopTimeout  = 30 * time.Second
	DefaultKillTimeout  = 15 * time.Second
	DefaultRingLines    = 2000
	DefaultStartTimeout = 90 * time.Second

	// MaxCommandBytes bounds a single console command line (§5.3).
	MaxCommandBytes = 512
)

// Log filenames written next to LogDir.
const (
	logFileRawSuffix   = ".log"
	logFilePlainSuffix = ".plain.log"
)

// Options configures a single Runner.
type Options struct {
	// ID is the instance identifier; purely informational for the supervisor.
	ID int64

	// Dir is the working directory of the child process. For SurvivalcraftNet
	// this is the instance directory: the server derives every generated file
	// from CWD, so this is the isolation boundary (§4.3). Mandatory.
	Dir string

	// Executable is the program to run, e.g. "/usr/bin/dotnet". Never a shell.
	Executable string

	// Args are passed as an array (no shell interpolation, no injection).
	Args []string

	// Env holds extra "KEY=VALUE" entries appended to os.Environ().
	Env []string

	// Term is the TERM value; default "xterm-256color".
	Term string

	// ColorMode is ColorModeEnhanced, ColorModeBasic or ColorModeAuto.
	// enhanced/auto attempt a PTY; basic goes straight to pipes.
	ColorMode string

	// Rows/Cols are the initial PTY window size; default 40x160.
	Rows, Cols uint16

	// DOTNETRoot, when non-empty, sets DOTNET_ROOT. The panel injects it so the
	// child finds the runtime the panel probed (internal/runtime).
	DOTNETRoot string

	// TZ sets the child's timezone (default: inherit).
	TZ string

	// StopTimeout is how long the graceful stage may take (default 30s).
	StopTimeout time.Duration

	// KillTimeout is how long SIGTERM may take before SIGKILL (default 15s).
	KillTimeout time.Duration

	// StartTimeout is how long Starting may last before Failed (default 90s).
	StartTimeout time.Duration

	// RingLines is the size of the in-memory replay ring (default 2000).
	RingLines int

	// LogDir is where <date>.log and <date>.plain.log are written. Empty
	// disables disk logging (the ring buffer and fan-out still work).
	LogDir string

	// FlushInterval is the log writer flush period; default 1s.
	FlushInterval time.Duration

	// OnLineFn, when non-nil, is called synchronously from the reader goroutine
	// for every decoded line. It must not block: it runs on the path that reads
	// the child's stdout.
	OnLineFn func(ansi.Line)

	// OnEventFn, when non-nil, is called synchronously (and possibly from
	// several goroutines) for every state/process event. It must not block.
	OnEventFn func(Event)
}

func normalizeOptions(o Options) Options {
	if o.Term == "" {
		o.Term = DefaultTerm
	}
	if o.ColorMode == "" {
		o.ColorMode = DefaultColorMode
	}
	if o.Rows == 0 {
		o.Rows = DefaultRows
	}
	if o.Cols == 0 {
		o.Cols = DefaultCols
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = DefaultStopTimeout
	}
	if o.KillTimeout <= 0 {
		o.KillTimeout = DefaultKillTimeout
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = DefaultStartTimeout
	}
	if o.RingLines <= 0 {
		o.RingLines = DefaultRingLines
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = time.Second
	}
	return o
}

// Validate reports whether the options can launch a process.
func (o Options) Validate() error {
	if o.Dir == "" {
		return fmt.Errorf("%w: Options.Dir is mandatory (working directory = instance dir)", ErrInvalidOptions)
	}
	if o.Executable == "" {
		return fmt.Errorf("%w: Options.Executable is mandatory", ErrInvalidOptions)
	}
	switch o.ColorMode {
	case ColorModeEnhanced, ColorModeBasic, ColorModeAuto, "":
	default:
		return fmt.Errorf("%w: unknown ColorMode %q", ErrInvalidOptions, o.ColorMode)
	}
	if o.RingLines < 0 {
		return fmt.Errorf("%w: RingLines must be >= 0", ErrInvalidOptions)
	}
	return nil
}

// wantsPTY reports whether the configured color mode asks for a PTY.
func (o Options) wantsPTY() bool {
	return o.ColorMode == ColorModeEnhanced || o.ColorMode == ColorModeAuto || o.ColorMode == ""
}
