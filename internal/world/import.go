package world

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ImportOptions configures [ImportZip].
type ImportOptions struct {
	// Overwrite replaces an existing `Worlds/<newDirName>` directory.
	//
	// Default false is the safe behaviour required by §6.3 ("导入时校验目录名不与
	// 现有冲突"): an import never destroys an existing save unless asked.
	Overwrite bool
	// RenameOnConflict picks a deterministic, non-colliding name instead of
	// failing, e.g. "World" -> "World-2". It is ignored when Overwrite is set.
	RenameOnConflict bool
	// MaxUncompressedBytes caps the total decompressed size (zip-bomb defence).
	// Zero uses [DefaultImportMaxBytes].
	MaxUncompressedBytes int64
	// MaxEntries caps the number of archive entries. Zero uses
	// [DefaultImportMaxEntries].
	MaxEntries int
	// MaxDepth caps the directory depth an archive may create. Zero uses
	// [DefaultImportMaxDepth].
	MaxDepth int
	// Filter, when non-nil, may veto an entry by returning an error. It runs
	// after every safety check, so it can only narrow the import.
	Filter func(rel string, f *zip.File) error
	// ExpectedDirNameInsideArchive, when non-empty, requires every entry to live
	// under that prefix, which is then stripped. Servers and third-party tools
	// often wrap a save in a top-level directory.
	ExpectedDirNameInsideArchive string
}

// Import defaults.
const (
	DefaultImportMaxBytes   int64 = 8 << 30 // 8 GiB decompressed
	DefaultImportMaxEntries       = 200_000
	DefaultImportMaxDepth         = 32
)

// ImportResult reports what an import produced.
type ImportResult struct {
	// DirName is the directory that was actually created (after conflict
	// resolution), i.e. the last segment the new WorldPath must contain.
	DirName string `json:"dirName"`
	// Path is the absolute path of the created world directory.
	Path string `json:"path"`
	// Files is the number of files written.
	Files int `json:"files"`
	// Bytes is the number of bytes written.
	Bytes int64 `json:"bytes"`
	// Skipped lists entries that were not written, with a reason.
	Skipped []SkippedEntry `json:"skipped,omitempty"`
	// Renamed reports whether conflict resolution changed the requested name.
	Renamed bool `json:"renamed"`
}

// SkippedEntry records an archive entry that was rejected, and why.
type SkippedEntry struct {
	// Name is the raw entry name.
	Name string `json:"name"`
	// Reason is a short machine-readable cause.
	Reason string `json:"reason"`
	// Detail elaborates.
	Detail string `json:"detail,omitempty"`
}

// Skip reasons.
const (
	SkipTraversal = "traversal"
	SkipAbsolute  = "absolute"
	SkipSymlink   = "symlink"
	SkipUnsafe    = "unsafe-name"
	SkipTooDeep   = "too-deep"
	SkipTooLarge  = "too-large"
	SkipDuplicate = "duplicate"
	SkipFiltered  = "filtered"
	SkipNotReg    = "not-regular"
	SkipPrefix    = "outside-wrapper"
)

// ImportZip extracts a world archive into `Worlds/<newDirName>`.
//
// Security properties, all tested:
//
//   - newDirName is validated with the SAME rules as a WorldPath segment
//     ([ValidateDirName]): non-empty, no '/' or '\', not "." or "..", no NUL, no
//     drive letter, not over 255 bytes;
//   - every entry name is checked for NUL bytes, backslashes, absolute paths and
//     ".." components;
//   - every entry's resolved destination is verified to stay inside the target
//     directory with an element-wise containment check, so a sibling directory
//     sharing a textual prefix is not accepted;
//   - symlink entries are rejected (a zip can carry a link that a later entry
//     writes through — the classic zip-slip amplifier);
//   - non-regular entries are rejected;
//   - duplicate destinations are rejected after the first;
//   - the total decompressed size, entry count and depth are all capped, and the
//     declared size is not trusted: each entry is read through a LimitReader;
//   - directory components are created one at a time and never through an
//     existing symlink;
//   - the whole import lands in a staging directory that is renamed into place
//     only on success, so a rejected or failed import leaves NO partial world
//     behind for the server to trip over.
func ImportZip(instanceDir, srcZip, newDirName string, opts ImportOptions) (ImportResult, error) {
	var res ImportResult

	if err := ValidateDirName(newDirName); err != nil {
		return res, err
	}
	if strings.TrimSpace(srcZip) == "" {
		return res, fmt.Errorf("world: empty source archive")
	}
	maxBytes := opts.MaxUncompressedBytes
	if maxBytes <= 0 {
		maxBytes = DefaultImportMaxBytes
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultImportMaxEntries
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultImportMaxDepth
	}

	worldsRoot := WorldsDir(instanceDir)
	if err := os.MkdirAll(worldsRoot, 0o755); err != nil {
		return res, fmt.Errorf("world: create %s: %w", worldsRoot, err)
	}
	// Resolve the root once. Every containment check below compares against this
	// real path, so a symlinked Worlds/ cannot be used to redirect the import.
	resolvedRoot, err := resolveExisting(worldsRoot)
	if err != nil {
		return res, fmt.Errorf("world: resolve %s: %w", worldsRoot, err)
	}

	// --- conflict resolution, against the *directory* name only ---
	finalName := newDirName
	target := filepath.Join(resolvedRoot, finalName)
	if _, serr := os.Lstat(target); serr == nil {
		switch {
		case opts.Overwrite:
			// Explicitly requested: the caller has confirmed with the user.
		case opts.RenameOnConflict:
			finalName = pickNonCollidingName(resolvedRoot, newDirName)
			target = filepath.Join(resolvedRoot, finalName)
			res.Renamed = true
		default:
			return res, fmt.Errorf("%w: %s", ErrExists, newDirName)
		}
	}

	// --- staging: nothing is visible under Worlds/ until the import succeeds ---
	stage, err := os.MkdirTemp(resolvedRoot, ".scnetm-import-*")
	if err != nil {
		return res, fmt.Errorf("world: create staging directory: %w", err)
	}
	stageName := filepath.Base(stage)
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stage)
		}
	}()

	zr, err := zip.OpenReader(srcZip)
	if err != nil {
		return res, fmt.Errorf("world: open archive %s: %w", srcZip, err)
	}
	defer zr.Close()

	if len(zr.File) > maxEntries {
		return res, fmt.Errorf("world: archive has %d entries, limit is %d", len(zr.File), maxEntries)
	}

	seen := map[string]struct{}{}
	var total int64

	for _, f := range zr.File {
		rawName := f.Name

		rel, reason, detail := sanitizeImportEntry(rawName, opts.ExpectedDirNameInsideArchive, maxDepth)
		if reason != "" {
			if reason == skipSilent {
				continue
			}
			res.Skipped = append(res.Skipped, SkippedEntry{Name: rawName, Reason: reason, Detail: detail})
			continue
		}

		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			res.Skipped = append(res.Skipped, SkippedEntry{
				Name: rawName, Reason: SkipSymlink, Detail: "symlink entries are never extracted",
			})
			continue
		}
		isDir := f.FileInfo().IsDir() || strings.HasSuffix(rawName, "/")
		destAbs := filepath.Join(stage, filepath.FromSlash(rel))

		// Containment: element-wise, so "<stage>-evil" is not accepted for
		// "<stage>".
		if !within(stage, destAbs) {
			res.Skipped = append(res.Skipped, SkippedEntry{
				Name: rawName, Reason: SkipTraversal, Detail: "destination is outside the world directory",
			})
			continue
		}

		if isDir {
			if reason, detail := makeDirTree(stage, rel); reason != "" {
				res.Skipped = append(res.Skipped, SkippedEntry{Name: rawName, Reason: reason, Detail: detail})
			}
			continue
		}
		if !mode.IsRegular() {
			res.Skipped = append(res.Skipped, SkippedEntry{
				Name: rawName, Reason: SkipNotReg, Detail: "only regular files and directories are extracted",
			})
			continue
		}

		if _, dup := seen[destAbs]; dup {
			res.Skipped = append(res.Skipped, SkippedEntry{
				Name: rawName, Reason: SkipDuplicate, Detail: "duplicate destination",
			})
			continue
		}

		declared := int64(f.UncompressedSize64)
		if total+declared > maxBytes {
			return res, fmt.Errorf("world: archive decompresses to more than the %d byte limit", maxBytes)
		}
		if opts.Filter != nil {
			if ferr := opts.Filter(rel, f); ferr != nil {
				res.Skipped = append(res.Skipped, SkippedEntry{
					Name: rawName, Reason: SkipFiltered, Detail: ferr.Error(),
				})
				continue
			}
		}

		n, werr := writeImportEntry(f, destAbs, declared)
		if werr != nil {
			return res, werr
		}
		seen[destAbs] = struct{}{}
		total += n
		res.Files++
		res.Bytes += n
	}

	// A save without Project.json is not a save; refuse rather than publishing
	// a directory the server cannot load.
	if _, serr := os.Lstat(filepath.Join(stage, ProjectFileName)); serr != nil {
		return res, fmt.Errorf("%w: the archive does not contain %s at its top level", ErrNoProjectFile, ProjectFileName)
	}

	// --- publish ---
	if opts.Overwrite {
		if _, serr := os.Lstat(target); serr == nil {
			// Move the old world aside first, then delete it, so a failure
			// during removal cannot leave the user with nothing.
			old := target + ".scnetm-old"
			_ = os.RemoveAll(old)
			if rerr := os.Rename(target, old); rerr != nil {
				return res, fmt.Errorf("world: set aside existing %s: %w", finalName, rerr)
			}
			if rerr := os.Rename(stage, target); rerr != nil {
				_ = os.Rename(old, target) // best effort restore
				return res, fmt.Errorf("world: publish %s: %w", finalName, rerr)
			}
			_ = os.RemoveAll(old)
		} else if rerr := os.Rename(stage, target); rerr != nil {
			return res, fmt.Errorf("world: publish %s: %w", finalName, rerr)
		}
	} else if rerr := os.Rename(stage, target); rerr != nil {
		return res, fmt.Errorf("world: publish %s: %w", finalName, rerr)
	}
	committed = true

	// Staging marker cleanup is defensive: the directory was renamed away, so
	// this normally does nothing.
	_ = os.Remove(filepath.Join(resolvedRoot, stageName))

	res.DirName = finalName
	res.Path = target
	return res, nil
}

// skipSilent marks an entry (an archive wrapper) that should be ignored without
// being reported, so an ExpectedDirNameInsideArchive import of a normally
// wrapped archive produces no noise.
const skipSilent = "silent"

// sanitizeImportEntry validates and normalises one archive entry name.
func sanitizeImportEntry(name, expectedPrefix string, maxDepth int) (rel, reason, detail string) {
	if name == "" {
		return "", SkipUnsafe, "empty entry name"
	}
	if strings.ContainsRune(name, 0) {
		return "", SkipUnsafe, "NUL byte in entry name"
	}
	if strings.ContainsRune(name, '\\') {
		return "", SkipUnsafe, "backslash in entry name"
	}
	if strings.HasPrefix(name, "/") {
		return "", SkipAbsolute, "absolute entry name"
	}
	if len(name) >= 2 && name[1] == ':' {
		return "", SkipAbsolute, "drive-letter entry name"
	}
	// path.Clean, not filepath.Clean: the separator handling must not depend on
	// the host OS, and backslashes were rejected above.
	clean := path.Clean(name)
	if clean == "." {
		return "", skipSilent, ""
	}
	if path.IsAbs(clean) {
		return "", SkipAbsolute, "absolute entry name"
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", SkipTraversal, `entry name contains ".."`
		}
	}

	if expectedPrefix != "" {
		prefix := strings.Trim(path.Clean(expectedPrefix), "/")
		if clean == prefix {
			return "", skipSilent, ""
		}
		if !strings.HasPrefix(clean, prefix+"/") {
			return "", SkipPrefix, fmt.Sprintf("entry is not under the required %q prefix", prefix)
		}
		clean = strings.TrimPrefix(clean, prefix+"/")
		if clean == "" {
			return "", skipSilent, ""
		}
	}

	parts := strings.Split(clean, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", SkipUnsafe, "empty or reserved path component"
		}
	}
	if len(parts) > maxDepth {
		return "", SkipTooDeep, fmt.Sprintf("depth %d exceeds the limit of %d", len(parts), maxDepth)
	}
	return strings.Join(parts, "/"), "", ""
}

// makeDirTree creates base/rel one component at a time, refusing to traverse a
// symlink at any level.
func makeDirTree(base, rel string) (reason, detail string) {
	cur := base
	for _, part := range strings.Split(rel, "/") {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		if !within(base, cur) {
			return SkipTraversal, "directory outside the world directory"
		}
		fi, err := os.Lstat(cur)
		switch {
		case err == nil && fi.Mode()&os.ModeSymlink != 0:
			return SkipSymlink, "directory component collides with an existing symlink"
		case err == nil && !fi.IsDir():
			return SkipNotReg, "directory component exists as a regular file"
		case err == nil:
			continue
		}
		if merr := os.Mkdir(cur, 0o755); merr != nil && !os.IsExist(merr) {
			return SkipUnsafe, "mkdir failed: " + merr.Error()
		}
	}
	return "", ""
}

// writeImportEntry streams one archive entry to disk. The declared size is
// treated as a hint only: a LimitReader bounds the write even if the header
// lies.
func writeImportEntry(f *zip.File, destAbs string, declared int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("world: open archive entry %q: %w", f.Name, err)
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return 0, fmt.Errorf("world: create parent for %q: %w", f.Name, err)
	}
	// Never write through an existing symlink.
	if fi, lerr := os.Lstat(destAbs); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		if rerr := os.Remove(destAbs); rerr != nil {
			return 0, fmt.Errorf("world: remove symlink at %q: %w", destAbs, rerr)
		}
	}

	mode := f.Mode().Perm() &^ 0o077
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(destAbs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("world: create %q: %w", destAbs, err)
	}
	limit := declared + declared/10 + 4096
	n, cerr := io.Copy(out, io.LimitReader(rc, limit))
	if clErr := out.Close(); clErr != nil && cerr == nil {
		cerr = clErr
	}
	if cerr != nil {
		_ = os.Remove(destAbs)
		return n, fmt.Errorf("world: write %q: %w", destAbs, cerr)
	}
	if n >= limit {
		_ = os.Remove(destAbs)
		return 0, fmt.Errorf("world: entry %q produced more data than its header declared", f.Name)
	}
	return n, nil
}

// pickNonCollidingName returns a deterministic alternative to name that does not
// exist under root: "<name>-2", "<name>-3", ...
//
// Determinism matters: the UI shows the resulting name, and the caller needs to
// know what WorldPath to write. A timestamp or random suffix would make the
// result unreproducible and hard to test.
func pickNonCollidingName(root, name string) string {
	for i := 2; i < 10_000; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if _, err := os.Lstat(filepath.Join(root, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
	// Astronomically unlikely; fall back to something certainly unique.
	return fmt.Sprintf("%s-%d", name, os.Getpid())
}

// resolveExisting fully resolves a path that must exist.
func resolveExisting(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// within reports whether child is inside (or equal to) parent, comparing path
// elements so that a prefix-sibling like "/x/ab" is not accepted for "/x/a".
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// ErrNoWorldInArchive is returned when an archive has no Project.json.
var ErrNoWorldInArchive = errors.New("world: archive does not contain a world")
