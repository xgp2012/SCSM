//go:build linux

package backup

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// defaultStatFS reports the free bytes available on the filesystem containing
// path, using statfs(2).
//
// Bavail rather than Bfree is used deliberately: on the ext4 default of 5%
// reserved blocks, Bfree counts space the panel's user cannot actually write
// to, so a precheck based on it would happily start a backup that then fails
// with ENOSPC halfway through.
//
// x/sys/unix is already a dependency (the supervisor uses it for /proc and
// signal helpers), so this costs no new module.
func defaultStatFS(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}

	// Bsize is the fundamental block size; Bavail is in units of Frsize on
	// some filesystems, so Frsize is preferred when the kernel reports it.
	blockSize := uint64(st.Bsize)
	if st.Frsize != 0 {
		blockSize = uint64(st.Frsize)
	}

	return st.Bavail * blockSize, nil
}
