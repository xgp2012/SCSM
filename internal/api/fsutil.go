package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// This file holds the small filesystem helpers shared by the handlers that do
// write anything themselves (instance provisioning, the config fallback path).
//
// The invariant they all preserve: a crash must never leave a half-written
// configuration file. ServerSetting.json is the panel's single write entry point
// for world settings (§6.3.1), so a truncated one would break the instance.
//
// Optionally, SC_STATE_DIR determines a directory for the server to persist
// state.

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by an atomic rename.
//
// The temp file is created in the *same* directory so the rename cannot cross a
// filesystem boundary (which would degrade to a copy). It is fsynced before the
// rename, so a power loss after the rename cannot leave an empty file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()

	// Remove the temp file if anything below fails, so a failed write does not
	// litter the instance directory.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	// CreateTemp uses 0600; apply the caller's mode explicitly (umask-safe).
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpName, path, err)
	}
	cleanup = false
	return nil
}

// writeJSONAtomic marshals v with indentation and writes it atomically.
//
// MarshalIndent (not Marshal) is required by §6.2: the config file must stay
// human-readable, because operators do edit it by hand.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling %s: %w", path, err)
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, 0o644)
}

// readFileLimited reads at most limit bytes and reports whether the file was
// truncated, so a multi-gigabyte log or archive cannot exhaust memory.
func readFileLimited(path string, limit int64) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if st.IsDir() {
		return nil, false, fmt.Errorf("%s is a directory", path)
	}

	truncated := st.Size() > limit
	if truncated {
		// Read the *tail*, which is what matters for logs.
		if _, err := f.Seek(-limit, os.SEEK_END); err != nil {
			return nil, false, err
		}
	}
	buf := make([]byte, 0, min64(limit, st.Size()))
	chunk := make([]byte, 64*1024)
	for {
		n, err := f.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if int64(len(buf)) >= limit {
				buf = buf[:limit]
				break
			}
		}
		if err != nil {
			break
		}
	}
	return buf, truncated, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// humanSize renders a byte count for the UI ("19.2 MiB").
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value := float64(n)
	for _, u := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, u)
		}
	}
	return fmt.Sprintf("%.1f EiB", value/unit)
}

// dirSize walks a directory summing regular-file sizes. Symlinks are skipped
// rather than followed, so a link loop cannot make this run forever and so the
// reported size never counts data outside the tree.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory must not abort the whole walk; the
			// size is informational.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

// newestModTime returns the most recent mtime in a tree, or the zero time.
func newestModTime(root string) time.Time {
	var newest time.Time
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest
}
