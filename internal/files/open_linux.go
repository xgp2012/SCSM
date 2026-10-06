//go:build linux

package files

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// This file accelerates descriptor opening with openat2(2) and
// RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS, which is the kernel-level fix for the
// TOCTOU window documented in the package comment.
//
// The portable implementations in open_other.go remain the tested baseline:
// every operation is verified by tests on all platforms, and
// resolve_linux_test.go additionally asserts that this path agrees with the
// portable one and that it refuses the same escapes.
//
// If openat2 is unavailable at runtime (kernel older than 5.6, seccomp filter,
// non-Linux build) openat2Supported() reports false once and the portable path
// is used forever after — no per-call probe overhead.

const (
	sysOpenat2 = 437 // x86_64/arm64/riscv64 share this number

	// resolveBeneath requires the resulting path to be inside the directory
	// referenced by dirfd, and rejects absolute paths and ".." escapes.
	resolveBeneath = 0x08
	// resolveNoMagiclinks rejects /proc/self/fd-style magic links, which would
	// otherwise let a procfs symlink escape a directory tree.
	resolveNoMagiclinks = 0x04
	// resolveNoSymlinks would forbid ALL symlinks. We deliberately do NOT set
	// it: in-root symlinks are legitimate (Worlds symlinks, shared Content),
	// and RESOLVE_BENEATH already contains them.
)

type openHow struct {
	Flags   uint64
	Mode    uint64
	Resolve uint64
}

var (
	openat2Once      sync.Once
	openat2Available bool
	// openat2Override, when non-zero, forces the probe result. It is only ever
	// set by tests; a plain atomic-free int is fine because it is written before
	// any goroutine starts using the package in that test.
	openat2Override int8 // 0 = unset, 1 = force available, -1 = force unavailable
)

// setOpenat2ForTest forces the availability flag. It exists so the portable
// fallback path stays testable on kernels that do support openat2 — otherwise
// that fallback would only ever run in production on older kernels, which is the
// worst place for untested code.
func setOpenat2ForTest(available bool) (restore func()) {
	prev := openat2Override
	if available {
		openat2Override = 1
	} else {
		openat2Override = -1
	}
	return func() { openat2Override = prev }
}

// openat2Supported reports whether the running kernel implements openat2 with
// the resolve flags this package needs.
func openat2Supported() bool {
	switch openat2Override {
	case 1:
		return true
	case -1:
		return false
	}
	openat2Once.Do(func() {
		rootFD, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
		if err != nil {
			return
		}
		defer syscall.Close(rootFD)
		name := []byte(".\x00")
		how := openHow{Flags: syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_DIRECTORY, Resolve: resolveBeneath}
		fd, _, errno := syscall.Syscall6(sysOpenat2,
			uintptr(rootFD),
			uintptr(unsafe.Pointer(&name[0])),
			uintptr(unsafe.Pointer(&how)),
			unsafe.Sizeof(how), 0, 0)
		if errno != 0 {
			return
		}
		syscall.Close(int(fd))
		openat2Available = true
	})
	return openat2Available
}

// openBeneath opens abs relative to the root using openat2, guaranteeing at the
// kernel level that the resolved object is inside the root.
func (r *Root) openBeneath(abs string, flag int, perm os.FileMode) (*os.File, bool, error) {
	if !openat2Supported() {
		return nil, false, nil
	}
	rel, err := relWithin(r.dir, abs)
	if err != nil {
		return nil, true, err
	}
	if rel == "" {
		rel = "."
	}
	rootFD, err := syscall.Open(r.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, nil // cannot even open the root: fall back
	}
	defer syscall.Close(rootFD)

	name := append([]byte(rel), 0)
	how := openHow{
		Flags:   uint64(flag) | syscall.O_CLOEXEC,
		Mode:    uint64(perm.Perm()),
		Resolve: resolveBeneath | resolveNoMagiclinks,
	}
	fd, _, errno := syscall.Syscall6(sysOpenat2,
		uintptr(rootFD),
		uintptr(unsafe.Pointer(&name[0])),
		uintptr(unsafe.Pointer(&how)),
		unsafe.Sizeof(how), 0, 0)
	runtime.KeepAlive(name)
	if errno != 0 {
		// EXDEV/ELOOP/EAGAIN/ENOSYS from the resolver mean the path tried to
		// leave the root or the syscall is not really supported. Report a typed
		// unsafe-path error instead of silently falling back, because falling
		// back here would reintroduce the escape we just blocked.
		switch errno {
		case syscall.ENOSYS, syscall.EPERM, syscall.EINVAL, syscall.E2BIG:
			// Not usable on this kernel/seccomp profile: give up on openat2
			// permanently and let the portable path handle it. openat2Once is
			// intentionally NOT re-run here — the flag is simply cleared, so a
			// later call goes straight to the portable path.
			openat2Available = false
			return nil, false, nil
		case syscall.EXDEV, syscall.ELOOP:
			return nil, true, fmt.Errorf("%w: %q escapes the instance root", ErrUnsafePath, rel)
		default:
			return nil, true, &os.PathError{Op: "openat2", Path: rel, Err: errno}
		}
	}
	return os.NewFile(uintptr(fd), abs), true, nil
}

// relWithin computes the slash-separated relative form of abs inside root, and
// reports an error when abs is not inside root.
func relWithin(root, abs string) (string, error) {
	if abs == root {
		return "", nil
	}
	if !isWithin(root, abs) {
		return "", fmt.Errorf("%w: %q is outside %q", ErrUnsafePath, abs, root)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("%w: %q is outside %q", ErrUnsafePath, abs, root)
	}
	return strings.TrimPrefix(rel, "./"), nil
}

// openDir opens a directory for reading its entries.
func (r *Root) openDir(abs string) (*os.File, error) {
	f, handled, err := r.openBeneath(abs, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if handled {
		return f, err
	}
	return os.Open(abs)
}

// openFile opens a file, re-validating containment on the *descriptor* when the
// platform allows it, so that a TOCTOU swap of a path component between Resolve
// and the open cannot hand back a descriptor outside the root.
func (r *Root) openFile(abs string, flag int, perm os.FileMode) (*os.File, error) {
	f, handled, err := r.openBeneath(abs, flag, perm)
	if handled {
		return f, err
	}
	f, err = os.OpenFile(abs, flag, perm)
	if err != nil {
		return nil, err
	}
	if err := r.recheckOpen(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// recheckOpen re-validates an already-open descriptor by asking the kernel for
// the *actual* file it refers to. On Linux /proc/self/fd/<n> resolves to the
// real path, which catches a directory component swapped for a symlink after
// Resolve ran.
//
// This is a best-effort second line of defence: it is only used when openat2 is
// unavailable, and it can itself be defeated by a sufficiently determined
// attacker who swaps the path back before the read. openat2 is the real fix.
func (r *Root) recheckOpen(f *os.File) error {
	if openat2Supported() {
		return nil // the descriptor came from openat2: already kernel-verified
	}
	actual, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
	if err != nil {
		return nil // no procfs (unusual); containment was checked by Resolve
	}
	// The link is decorated with " (deleted)" when the file was unlinked.
	actual = strings.TrimSuffix(actual, " (deleted)")
	if !strings.HasPrefix(actual, "/") {
		return nil
	}
	return r.checkContained(actual)
}
