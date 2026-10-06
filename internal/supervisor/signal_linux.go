//go:build linux

package supervisor

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// Signals are named in one place so the runner's ladder reads clearly and the
// names survive a future move to a platform abstraction.
var (
	syscallSIGTERM = syscall.SIGTERM
	syscallSIGKILL = syscall.SIGKILL
)

// signalNames maps signal numbers to their names, for exit-code reporting.
var signalNames = map[int]string{
	int(syscall.SIGHUP):  "SIGHUP",
	int(syscall.SIGINT):  "SIGINT",
	int(syscall.SIGQUIT): "SIGQUIT",
	int(syscall.SIGILL):  "SIGILL",
	int(syscall.SIGABRT): "SIGABRT",
	int(syscall.SIGFPE):  "SIGFPE",
	int(syscall.SIGKILL): "SIGKILL",
	int(syscall.SIGSEGV): "SIGSEGV",
	int(syscall.SIGPIPE): "SIGPIPE",
	int(syscall.SIGALRM): "SIGALRM",
	int(syscall.SIGTERM): "SIGTERM",
	int(unix.SIGBUS):     "SIGBUS",
	int(unix.SIGUSR1):    "SIGUSR1",
	int(unix.SIGUSR2):    "SIGUSR2",
	int(unix.SIGXCPU):    "SIGXCPU",
	int(unix.SIGXFSZ):    "SIGXFSZ",
}

// unixSignalName returns the name for a signal number, or "".
func unixSignalName(n int) string { return signalNames[n] }
