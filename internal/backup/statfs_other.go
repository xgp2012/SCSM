//go:build !linux

package backup

import "errors"

// defaultStatFS is unavailable on non-Linux platforms.
//
// Returning an error rather than guessing is safe here: Manager.checkSpace
// treats "cannot determine free space" as "do not block the backup", so a
// non-Linux build simply skips the precheck instead of refusing to work. The
// plan targets Linux (§9.1/§9.2), so this exists only to keep the package
// buildable elsewhere.
func defaultStatFS(string) (uint64, error) {
	return 0, errors.New("backup: free-space probing is not implemented on this platform")
}
