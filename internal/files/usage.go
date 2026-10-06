package files

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// DiskUsage walks the tree at rel and returns its total allocated size in
// bytes. A missing path yields 0 (not an error), because callers use this for
// quota reporting where "not there yet" is a normal state.
//
// Guarantees:
//
//   - symlinks are never followed, so a link to / cannot make the panel
//     traverse the whole host; a link contributes 0 bytes;
//   - the walk is bounded by [MaxWalkDepth], so a hostile deep tree fails fast
//     instead of pinning a worker;
//   - only regular files are counted (directories' own sizes are filesystem
//     noise, and device nodes report meaningless sizes).
func (r *Root) DiskUsage(rel string) (int64, error) {
	abs, err := r.Resolve(rel)
	if err != nil {
		return 0, err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return 0, nil // a link occupies no data blocks we should attribute here
	}
	if !fi.IsDir() {
		if fi.Mode().IsRegular() {
			return fi.Size(), nil
		}
		return 0, nil
	}
	var total int64
	err = r.walkUsage(abs, 0, &total)
	return total, err
}

func (r *Root) walkUsage(dir string, depth int, total *int64) error {
	if depth > MaxWalkDepth {
		return fmt.Errorf("%w: at %s", ErrDepthExceeded, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("files: read %q: %w", dir, err)
	}
	for _, de := range entries {
		// de.Type() comes from readdir and does not follow symlinks, which is
		// exactly what we want: a link is not descended into.
		if de.Type()&os.ModeSymlink != 0 {
			continue
		}
		child := filepath.Join(dir, de.Name())
		if de.IsDir() {
			if err := r.walkUsage(child, depth+1, total); err != nil {
				return err
			}
			continue
		}
		info, ierr := de.Info()
		if ierr != nil {
			continue // vanished mid-walk: not an error worth failing on
		}
		if info.Mode().IsRegular() {
			*total += info.Size()
		}
	}
	return nil
}

// DirUsage sums the sizes of the entries of the directory at rel WITHOUT
// recursing, and additionally returns the recursive total for each child
// directory. It exists for the world scanner, which needs "whole world size"
// and "Regions/ size" from one traversal pass.
func (r *Root) DirUsage(rel string) (total int64, perChild map[string]int64, err error) {
	abs, err := r.Resolve(rel)
	if err != nil {
		return 0, nil, err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, map[string]int64{}, nil
		}
		return 0, nil, err
	}
	if !fi.IsDir() {
		return fi.Size(), map[string]int64{}, nil
	}
	perChild = map[string]int64{}
	if err := r.walkUsage(abs, 0, &total); err != nil {
		return 0, nil, err
	}
	entries, rerr := os.ReadDir(abs)
	if rerr != nil {
		return total, perChild, nil
	}
	for _, de := range entries {
		if !de.IsDir() || de.Type()&os.ModeSymlink != 0 {
			continue
		}
		var child int64
		if werr := r.walkUsage(filepath.Join(abs, de.Name()), 1, &child); werr != nil {
			continue
		}
		perChild[de.Name()] = child
	}
	return total, perChild, nil
}

// FreeSpace reports the free bytes available on the filesystem holding the
// instance directory. It is used by the backup and quota features (§6.6 wants
// the host's free space cross-checked against the server's own log line).
func (r *Root) FreeSpace() (int64, error) {
	return FreeSpace(r.dir)
}

// FreeSpace reports the free bytes available on the filesystem containing path.
func FreeSpace(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("files: statfs %q: %w", path, err)
	}
	// Bavail is the space available to an unprivileged process, which is the
	// number that actually matters for "will this backup fit".
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// TotalSpace reports the total size of the filesystem containing path.
func TotalSpace(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("files: statfs %q: %w", path, err)
	}
	return int64(st.Blocks) * int64(st.Bsize), nil
}

// CountFiles returns the number of regular files under rel and their total
// size, without following symlinks. It is bounded like [Root.DiskUsage].
func (r *Root) CountFiles(rel string) (count int, total int64, err error) {
	abs, err := r.Resolve(rel)
	if err != nil {
		return 0, 0, err
	}
	fi, lerr := os.Lstat(abs)
	if lerr != nil {
		if os.IsNotExist(lerr) {
			return 0, 0, nil
		}
		return 0, 0, lerr
	}
	if !fi.IsDir() {
		return 1, fi.Size(), nil
	}
	err = r.countWalk(abs, 0, &count, &total)
	return count, total, err
}

func (r *Root) countWalk(dir string, depth int, count *int, total *int64) error {
	if depth > MaxWalkDepth {
		return fmt.Errorf("%w: at %s", ErrDepthExceeded, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, de := range entries {
		if de.Type()&os.ModeSymlink != 0 {
			continue
		}
		child := filepath.Join(dir, de.Name())
		if de.IsDir() {
			if err := r.countWalk(child, depth+1, count, total); err != nil {
				return err
			}
			continue
		}
		info, ierr := de.Info()
		if ierr != nil || !info.Mode().IsRegular() {
			continue
		}
		*count++
		*total += info.Size()
	}
	return nil
}
