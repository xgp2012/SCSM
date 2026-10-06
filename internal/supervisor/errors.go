package supervisor

import (
	"errors"
	"fmt"
)

// Sentinel errors. All are typed sentinels so the API layer can map them onto
// HTTP status codes with errors.Is.
var (
	// ErrInvalidOptions is returned by Options.Validate and by Start when the
	// supplied configuration cannot launch a process.
	ErrInvalidOptions = errors.New("supervisor: invalid options")

	// ErrNoCommandChannel is returned by SendCommand when the runner has no
	// stdin path (not started, already stopped, or started without any I/O).
	ErrNoCommandChannel = errors.New("supervisor: no command channel")

	// ErrEmptyCommand is returned by SendCommand for an empty line.
	ErrEmptyCommand = errors.New("supervisor: empty command")

	// ErrCommandTooLong is returned by SendCommand for input longer than
	// MaxCommandBytes.
	ErrCommandTooLong = errors.New("supervisor: command too long")

	// ErrInvalidCommand is returned by SendCommand for input containing CR or
	// LF (command injection guard).
	ErrInvalidCommand = errors.New("supervisor: command contains CR/LF")

	// ErrAlreadyRunning is returned by Start when the instance is not stopped.
	ErrAlreadyRunning = errors.New("supervisor: instance already running")

	// ErrNotRunning is returned by Stop/SendCommand when nothing is running.
	ErrNotRunning = errors.New("supervisor: instance is not running")

	// ErrStopping is returned by Stop when another Stop call already owns the
	// stop sequence.
	ErrStopping = errors.New("supervisor: stop already in progress")

	// ErrAlreadyStopped is returned by Stop for an instance in a terminal
	// state (Stopped/Crashed/Failed/Created): stopping it is a no-op.
	ErrAlreadyStopped = errors.New("supervisor: instance already stopped")

	// ErrProcessExited is returned by the metrics collector once the process is
	// gone (no /proc entry).
	ErrProcessExited = errors.New("supervisor: process has exited")

	// ErrClosed is returned by subscription/manager operations after shutdown.
	ErrClosed = errors.New("supervisor: closed")
)

// ExitError carries the exit status of a child process. It is produced by the
// waiter goroutine and surfaced through events and Metrics.LastExitCode.
type ExitError struct {
	Code   int
	Signal string // non-empty when the process died from a signal
	Stderr string // reserved for future use
}

func (e *ExitError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Signal != "" {
		return fmt.Sprintf("supervisor: process exited via signal %s (code %d)", e.Signal, e.Code)
	}
	return fmt.Sprintf("supervisor: process exited with code %d", e.Code)
}

// startError wraps a launch failure with the stage that failed.
type startError struct {
	Stage string
	Err   error
}

func (e *startError) Error() string { return "supervisor: " + e.Stage + ": " + e.Err.Error() }
func (e *startError) Unwrap() error { return e.Err }
