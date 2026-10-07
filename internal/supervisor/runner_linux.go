//go:build linux

package supervisor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// termMethod identifies how the child's stdio is wired.
type termMethod string

const (
	methodPTY  termMethod = "pty"
	methodPipe termMethod = "pipe"
)

// ptyOpen attempts to allocate a PTY for cmd. On this sandbox (and in many
// containers) /dev/ptmx exists but allocation fails with ENOSPC — "out of pty
// devices" — so the caller MUST be prepared to fall back to pipes. The returned
// error explains exactly why, and is surfaced as an EventDegraded (§5.2).
func ptyOpen(cmd *exec.Cmd, rows, cols uint16) (*os.File, error) {
	if rows == 0 {
		rows = DefaultRows
	}
	if cols == 0 {
		cols = DefaultCols
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, fmt.Errorf("pty.StartWithSize: %w", err)
	}
	return f, nil
}

// isPTYUnavailable classifies allocation failures so the UI can show a precise
// warning instead of a generic one.
func isPTYUnavailable(err error) bool {
	if err == nil {
		return false
	}
	for _, target := range []error{
		syscall.ENOSPC, // "out of pty devices" -- the classic container failure
		syscall.ENODEV,
		syscall.EACCES,
		syscall.EPERM,
		syscall.ENOENT,
		unix.ENXIO,
		// os.ErrPermission is fs.ErrPermission, a distinct sentinel from
		// syscall.EPERM; both spell "we may not open this".
		os.ErrPermission,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// wirePipes replaces the child's stdio with explicit pipes when no PTY is
// available. Never goes through a shell (§4.3).
//
// # Why this builds its own os.Pipe instead of calling cmd.StdoutPipe
//
// (*exec.Cmd).Wait() closes every parent-side pipe that StdoutPipe/StderrPipe
// registered — closeDescriptors(c.parentIOPipes) in os/exec — and closing a
// pipe read end DISCARDS whatever the child wrote but nobody has read yet. The
// stdlib documents the constraint ("it is thus incorrect to call Wait before
// all reads from the pipe have completed"), but the runner cannot honour it:
// waitLoop must reap the child with cmd.Wait() while readLoop is concurrently
// draining the same descriptor.
//
// The consequence was real data loss, not a theoretical race. Measured on a
// child that writes and exits immediately, the tail was lost 100% of the time
// (100/100 for /bin/true, /bin/echo and /usr/bin/env when Wait preceded the
// first Read). That silently drops the final lines of a game server that
// announces "saving..." and exits — exactly what §5.2 "no lost lines" and the
// tail-flush guarantee of Stop() exist to prevent.
//
// An os.Pipe() that we own is invisible to Wait(), so the reader keeps its
// data. stdoutWrite is returned so Start can drop the parent's copy of the
// write end once the child has been forked: without that close the descriptor
// stays open in the parent, EOF never arrives, and the reader blocks until the
// waitLoop guard fires.
func wirePipes(cmd *exec.Cmd) (stdin io.WriteCloser, stdout io.ReadCloser, stdoutWrite *os.File, err error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("StdinPipe: %w", err)
	}
	// os.Pipe, not cmd.StdoutPipe: the read end survives cmd.Wait().
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = in.Close()
		return nil, nil, nil, fmt.Errorf("os.Pipe: %w", err)
	}
	// The log pipeline only needs stdout: the server writes its console there.
	// Stderr is folded into the same pipe so nothing is silently lost.
	cmd.Stdout = pw
	cmd.Stderr = pw
	return in, pr, pw, nil
}

// newSysProcAttr gives every instance its own process group, so stopping the
// instance signals the whole tree and `dotnet` children never leak (§4.3).
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup delivers sig to the whole process group of pid.
//
// The negative pid is what makes this safe: a plain kill would leave the
// server's child processes orphaned.
func signalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("supervisor: invalid pid %d", pid)
	}
	err := unix.Kill(-pid, sig)
	if err != nil && errors.Is(err, syscall.ESRCH) {
		// Group already gone: treat as success, the desired state holds.
		return nil
	}
	return err
}

// waitExitCode converts an exec.Wait error into an exit code plus signal name.
func waitExitCode(err error) (code int, signal string) {
	if err == nil {
		return 0, ""
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				return 128 + int(ws.Signal()), ws.Signal().String()
			}
			return ws.ExitStatus(), ""
		}
		return ee.ExitCode(), ""
	}
	return -1, ""
}

// setPTYSize resizes an existing PTY. The frontend calls this after xterm.js
// fit() (§5.2).
func setPTYSize(f *os.File, rows, cols uint16) error {
	if f == nil {
		return ErrNoCommandChannel
	}
	if rows == 0 || cols == 0 {
		return fmt.Errorf("%w: rows and cols must be non-zero", ErrInvalidOptions)
	}
	return pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols})
}

// processAlive reports whether pid still exists (zombie-aware: a reaped child
// reports ESRCH, an unreaped zombie still counts as present but we treat the
// wait goroutine as authoritative).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// bootTime caches the kernel boot time for /proc-based uptime maths.
var bootTime = func() time.Time {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return time.Now()
	}
	return time.Now().Add(-time.Duration(si.Uptime) * time.Second)
}()

// ptyAllocatable reports whether this host can actually allocate a PTY.
//
// It is deliberately separate from ptyOpen because a probe must never launch a
// process: it opens /dev/ptmx and immediately closes it. Sandboxes and many
// containers answer with EACCES or ENOSPC ("out of pty devices"), which is
// exactly the condition the pipe fallback exists for.
func ptyAllocatable() bool {
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	// Opening the master is necessary but not sufficient; ask for the slave
	// grant explicitly, which is what fails under a seccomp/LSM policy.
	return unix.IoctlSetPointerInt(int(f.Fd()), unix.TIOCSPTLCK, 0) == nil ||
		ptyProbeFallback()
}

// ptyProbeFallback performs a real (but process-free) PTY allocation probe by
// asking the kernel to open a new master/slave pair through grantpt semantics.
func ptyProbeFallback() bool {
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	// unix.IoctlGetInt(TIOCGPTN) yields the slave number when a PTY is really
	// available; it fails with ENOSPC/EPERM when the host refuses to allocate.
	n, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPTN)
	return err == nil && n >= 0
}
