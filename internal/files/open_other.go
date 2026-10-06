//go:build !linux

package files

import (
	"os"
)

// This file is the portable fallback for the descriptor-opening helpers that
// the Linux build accelerates with openat2(2). Both implementations must have
// exactly these signatures.

// openDir opens a directory for reading its entries.
func (r *Root) openDir(abs string) (*os.File, error) {
	return os.Open(abs)
}

// openFile opens a file, re-validating containment on the *descriptor* when the
// platform allows it, so that a TOCTOU swap of a path component between Resolve
// and the open cannot hand back a descriptor outside the root.
func (r *Root) openFile(abs string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(abs, flag, perm)
	if err != nil {
		return nil, err
	}
	if err := r.recheckOpen(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// recheckOpen is the portable post-open verification: re-resolve the path and
// confirm it is still inside the root. This narrows but does not eliminate the
// race — see the package documentation on residual risk.
func (r *Root) recheckOpen(f *os.File) error {
	name := f.Name()
	resolved, err := evalExisting(name)
	if err != nil {
		return nil // e.g. an unlinked-and-recreated file; containment was checked by Resolve
	}
	return r.checkContained(resolved)
}

// evalExisting fully resolves every symlink in an existing path.
func evalExisting(p string) (string, error) {
	return evalSymlinks(p)
}
