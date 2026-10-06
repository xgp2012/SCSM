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
func wirePipes(cmd *exec.Cmd) (stdin io.WriteCloser, stdout io.ReadCloser, err error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("StdinPipe: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, nil, fmt.Errorf("StdoutPipe: %w", err)
	}
	// The log pipeline only needs stdout: the server writes its console there.
	// Stderr is folded into the same pipe so nothing is silently lost.
	cmd.Stderr = cmd.Stdout
	return in, out, nil
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
