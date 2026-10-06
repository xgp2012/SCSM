package files

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

// UnzipOptions configures [Root.Unzip].
type UnzipOptions struct {
	// Policy overrides the limits and allow-lists. Nil means [DefaultPolicy].
	Policy *Policy
	// StripComponents removes leading path components from each entry, like
	// `tar --strip-components`. A value of 1 turns "Content.scpak" in a nested
	// wrapper directory into a top-level "Content.scpak". Entries that do not
	// have enough components after stripping are skipped and reported.
	StripComponents int
	// Overwrite allows replacing existing files. When false (the default), an
	// entry whose destination already exists is skipped and reported rather
	// than silently clobbering instance files.
	Overwrite bool
	// Filter, when non-nil, is called with each entry's sanitised relative
	// destination and may veto it by returning an error. It runs AFTER all
	// safety checks, so a filter can only narrow the set, never widen it.
	Filter func(rel string, entry *zip.File) error
	// MaxSizeOverride, when positive, replaces the policy's total-decompressed
	// cap for this call.
	MaxSizeOverride int64
}

// SkippedEntry records an entry that was not extracted, and why. Reporting
// every skip (rather than failing the whole archive) is a §6.4 requirement: a
// server plugin zip often carries macOS cruft and nested wrappers that the
// panel should tolerate while still telling the user what happened.
type SkippedEntry struct {
	// Name is the raw entry name as it appeared in the archive.
	Name string `json:"name"`
	// Reason is a short machine-readable cause: "traversal", "absolute",
	// "symlink", "unsafe-name", "too-deep", "too-large", "duplicate",
	// "exists", "filtered", "directory", "not-regular".
	Reason string `json:"reason"`
	// Detail is a human-readable elaboration.
	Detail string `json:"detail,omitempty"`
}

// UnzipResult reports what an extraction actually did.
type UnzipResult struct {
	// Extracted lists the archive-relative names that were written, in order.
	Extracted []string `json:"extracted"`
	// Directories lists directories that were created.
	Directories []string `json:"directories"`
	// Skipped lists every entry that was not extracted, with a reason.
	Skipped []SkippedEntry `json:"skipped"`
	// TotalBytes is the number of bytes actually written.
	TotalBytes int64 `json:"totalBytes"`
	// Entries is the number of entries examined.
	Entries int `json:"entries"`
}

// entrySkipped is a reason constant set used by tests and callers.
const (
	ReasonTraversal = "traversal"
	ReasonAbsolute  = "absolute"
	ReasonSymlink   = "symlink"
	ReasonUnsafe    = "unsafe-name"
	ReasonTooDeep   = "too-deep"
	ReasonTooLarge  = "too-large"
	ReasonDuplicate = "duplicate"
	ReasonExists    = "exists"
	ReasonFiltered  = "filtered"
	ReasonDir       = "directory"
	ReasonNotReg    = "not-regular"
)

// Unzip extracts the archive at zipPath into the directory relDest inside the
// root.
//
// Every entry is validated individually:
//
//   - the raw name is checked for NUL bytes and Windows separators;
//   - the name is converted to a clean, slash-separated, root-relative path and
//     rejected if it is absolute or contains "..";
//   - the destination is re-resolved through [Root.Resolve], so a parent
//     directory that is (or becomes) a symlink out of the root is caught;
//   - the destination is verified to be inside the destination directory with
//     an element-wise check, so "a/../../evil" cannot land beside the root;
//   - symlink entries are refused outright (a zip can carry a symlink that a
//     later entry then writes *through*, the classic zip-slip amplifier);
//   - non-regular entries (devices, FIFOs) are refused;
//   - the total decompressed size, per-entry size, entry count, and directory
//     depth are all capped;
//   - duplicate names are reported and the second occurrence is skipped, so a
//     later entry cannot overwrite an earlier one's validated target;
//   - existing files are left alone unless [UnzipOptions.Overwrite] is set.
//
// Files are written with 0600 & the entry's permission bits, and directories
// with 0755, so an archive cannot create setuid executables.
func (r *Root) Unzip(zipPath, relDest string, opts UnzipOptions) (UnzipResult, error) {
	var res UnzipResult

	pol := r.withPolicy(opts.Policy)
	maxSize := pol.MaxUnzipSize
	if opts.MaxSizeOverride > 0 {
		maxSize = opts.MaxSizeOverride
	}

	destRoot, err := r.Resolve(relDest)
	if err != nil {
		return res, err
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return res, fmt.Errorf("files: create destination %q: %w", relDest, err)
	}
	// Re-resolve now that the destination exists, so the containment checks
	// below compare against the real path.
	destRoot, err = r.Resolve(relDest)
	if err != nil {
		return res, err
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return res, fmt.Errorf("files: open zip %q: %w", zipPath, err)
	}
	defer zr.Close()

	if len(zr.File) > pol.MaxUnzipEntries {
		return res, tooLargeErr(fmt.Sprintf("archive has %d entries, cap is %d", len(zr.File), pol.MaxUnzipEntries))
	}
	res.Entries = len(zr.File)

	seen := make(map[string]struct{}, len(zr.File))
	var total int64

	for _, f := range zr.File {
		name := f.Name

		skip := func(reason, detail string) {
			res.Skipped = append(res.Skipped, SkippedEntry{Name: name, Reason: reason, Detail: detail})
		}

		// --- lexical validation of the entry name ---
		rel, reason, detail := sanitizeZipEntryName(name, opts.StripComponents, pol.MaxUnzipDepth)
		if reason != "" {
			if reason == reasonSkipNoComponents {
				continue // a stripped-away wrapper entry: silently ignored
			}
			skip(reason, detail)
			continue
		}

		// --- type validation ---
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			skip(ReasonSymlink, "symlink entries are never extracted")
			continue
		}
		isDir := f.FileInfo().IsDir() || strings.HasSuffix(name, "/")
		if isDir {
			// Directories are created but not counted as extracted files.
			//
			// createDirTree never follows a symlink: if any component of the
			// requested directory already exists as a symlink, that component
			// is refused rather than traversed. Otherwise a planted link inside
			// the destination could redirect the whole subtree outside the root.
			if reason, detail := r.createDirTree(destRoot, rel); reason != "" {
				skip(reason, detail)
				continue
			}
			res.Directories = append(res.Directories, rel)
			continue
		}
		if !mode.IsRegular() {
			skip(ReasonNotReg, "only regular files and directories are extracted")
			continue
		}

		// --- destination validation ---
		destAbs := filepath.Join(destRoot, filepath.FromSlash(rel))
		if !isWithin(destRoot, destAbs) {
			skip(ReasonTraversal, "resolved target outside the destination")
			continue
		}
		// Re-run the full sandbox resolution (catches a symlinked ancestor).
		//
		// resolveForDelete is used rather than Resolve on purpose: it behaves
		// identically for every component EXCEPT the last, where a symlink is
		// returned as the link's own path instead of being followed. That is
		// exactly what an extraction needs. If we used Resolve here, an entry
		// whose destination is an existing symlink pointing outside the root
		// would either be rejected (fine) or — worse, if the link pointed
		// *inside* — cause writeZipEntry to write through it. Returning the
		// link path means writeZipEntry unlinks the link and creates a real
		// file, which is the only safe behaviour.
		relFromRoot := path.Join(cleanRel(relDest), rel)
		resolvedDest, rerr := r.resolveForDelete(relFromRoot)
		if rerr != nil {
			skip(ReasonTraversal, "target rejected by sandbox: "+rerr.Error())
			continue
		}
		if !isWithin(destRoot, resolvedDest) {
			skip(ReasonTraversal, "target outside the destination after resolution")
			continue
		}
		// Also verify the parent chain fully resolves inside the destination,
		// so a symlinked directory component cannot redirect the write.
		if parentResolved, perr := r.Resolve(path.Dir(relFromRoot)); perr == nil {
			if !isWithin(destRoot, parentResolved) {
				skip(ReasonTraversal, "parent directory outside the destination after resolution")
				continue
			}
			resolvedDest = filepath.Join(parentResolved, filepath.Base(resolvedDest))
		} else {
			skip(ReasonTraversal, "parent directory rejected by sandbox: "+perr.Error())
			continue
		}

		// --- duplicate detection ---
		if _, dup := seen[resolvedDest]; dup {
			skip(ReasonDuplicate, "an entry with this destination was already extracted")
			continue
		}

		// --- size caps, checked before writing a single byte ---
		entrySize := int64(f.UncompressedSize64)
		if entrySize > pol.MaxUnzipEntrySize {
			skip(ReasonTooLarge, fmt.Sprintf("entry is %d bytes, per-entry cap is %d", entrySize, pol.MaxUnzipEntrySize))
			continue
		}
		if total+entrySize > maxSize {
			skip(ReasonTooLarge, fmt.Sprintf("total decompressed size would exceed the cap of %d bytes", maxSize))
			continue
		}
		if pol.MaxCompressionRatio > 0 && f.CompressedSize64 > 0 {
			ratio := entrySize / int64(f.CompressedSize64)
			if ratio > pol.MaxCompressionRatio {
				skip(ReasonTooLarge, fmt.Sprintf("compression ratio %d exceeds the cap of %d",
					ratio, pol.MaxCompressionRatio))
				continue
			}
		}

		if opts.Filter != nil {
			if ferr := opts.Filter(rel, f); ferr != nil {
				skip(ReasonFiltered, ferr.Error())
				continue
			}
		}

		if _, serr := os.Lstat(resolvedDest); serr == nil && !opts.Overwrite {
			skip(ReasonExists, "destination already exists and overwrite is disabled")
			continue
		}

		written, werr := r.writeZipEntry(f, resolvedDest, entrySize)
		if werr != nil {
			return res, werr
		}
		seen[resolvedDest] = struct{}{}
		total += written
		res.TotalBytes += written
		res.Extracted = append(res.Extracted, rel)
	}

	return res, nil
}

// reasonSkipNoComponents is an internal sentinel: the entry was an archive
// wrapper directory removed by StripComponents and should be ignored silently.
const reasonSkipNoComponents = "strip-empty"

// sanitizeZipEntryName validates and normalises a zip entry name.
//
// It returns an empty reason when the name is acceptable. When reason is
// reasonSkipNoComponents the caller should ignore the entry entirely.
func sanitizeZipEntryName(name string, strip, maxDepth int) (rel, reason, detail string) {
	if name == "" {
		return "", ReasonUnsafe, "empty entry name"
	}
	if strings.ContainsRune(name, 0) {
		return "", ReasonUnsafe, "NUL byte in entry name"
	}
	if strings.ContainsRune(name, '\\') {
		return "", ReasonUnsafe, "Windows separator in entry name"
	}
	// Absolute (POSIX or Windows drive) names are refused. Note that a leading
	// "/" would also be caught by the ".." check below only by accident, so it
	// is checked explicitly and reported distinctly.
	if strings.HasPrefix(name, "/") {
		return "", ReasonAbsolute, "absolute entry name"
	}
	if len(name) >= 2 && name[1] == ':' && isASCIILetter(name[0]) {
		return "", ReasonAbsolute, "drive-letter entry name"
	}

	// path.Clean (not filepath.Clean) so that a backslash is NOT treated as a
	// separator here — it was already rejected above, and using the platform
	// separator would make the behaviour differ between Linux and Windows.
	clean := path.Clean(name)
	if clean == "." {
		return "", reasonSkipNoComponents, ""
	}
	if path.IsAbs(clean) {
		return "", ReasonAbsolute, "absolute entry name"
	}

	parts := strings.Split(clean, "/")
	// Reject traversal BEFORE stripping, so "../evil" cannot become "evil"
	// after a strip and slip through.
	for _, p := range parts {
		if p == ".." {
			return "", ReasonTraversal, "entry name contains \"..\""
		}
	}
	if strip > 0 {
		if strip >= len(parts) {
			return "", reasonSkipNoComponents, ""
		}
		parts = parts[strip:]
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", ReasonUnsafe, "entry name has an empty or reserved component"
		}
	}
	if len(parts) > maxDepth {
		return "", ReasonTooDeep, fmt.Sprintf("entry depth %d exceeds the cap of %d", len(parts), maxDepth)
	}
	return strings.Join(parts, "/"), "", ""
}

// createDirTree creates destRoot/rel one component at a time, refusing to
// traverse an existing symlink at any level.
//
// os.MkdirAll would happily walk through a symlinked component, which is enough
// for a planted link to redirect an archive's directory tree out of the root.
// Building the tree component by component removes that possibility for every
// level the archive itself creates.
func (r *Root) createDirTree(destRoot, rel string) (reason, detail string) {
	cur := destRoot
	for _, part := range strings.Split(rel, "/") {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		if !isWithin(destRoot, cur) {
			return ReasonTraversal, "directory target outside the destination"
		}
		fi, err := os.Lstat(cur)
		switch {
		case err == nil && fi.Mode()&os.ModeSymlink != 0:
			return ReasonSymlink, "directory component collides with an existing symlink"
		case err == nil && !fi.IsDir():
			return ReasonNotReg, "directory component exists as a regular file"
		case err == nil:
			continue // already a real directory
		}
		if err := os.Mkdir(cur, 0o755); err != nil {
			if os.IsExist(err) {
				continue
			}
			return ReasonUnsafe, "mkdir failed: " + err.Error()
		}
	}
	return "", ""
}

// writeZipEntry streams one entry to disk. It never trusts the declared
// uncompressed size: the reader is wrapped in a LimitReader so that a lying
// header cannot cause an unbounded write.
func (r *Root) writeZipEntry(f *zip.File, destAbs string, declared int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("files: open zip entry %q: %w", f.Name, err)
	}
	defer rc.Close()

	// Refuse to write through a symlink: if the destination is an existing
	// symlink, remove it first so the write cannot be redirected outside.
	if fi, lerr := os.Lstat(destAbs); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(destAbs); err != nil {
			return 0, fmt.Errorf("files: remove symlink at %q: %w", destAbs, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return 0, fmt.Errorf("files: mkdir for %q: %w", f.Name, err)
	}

	mode := f.Mode().Perm() &^ 0o077 // never group/world-writable, never setuid
	if mode == 0 {
		mode = 0o644
	}
	mode &^= os.ModeSetuid | os.ModeSetgid | os.ModeSticky

	out, err := os.OpenFile(destAbs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("files: create %q: %w", destAbs, err)
	}

	// Allow a little slack over the declared size so that a slightly-wrong
	// header on a benign archive still extracts, while a bomb is still cut off.
	limit := declared + declared/10 + 4096
	n, err := io.Copy(out, io.LimitReader(rc, limit))
	if cerr := out.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(destAbs)
		return n, fmt.Errorf("files: write %q: %w", destAbs, err)
	}
	if n >= limit {
		_ = os.Remove(destAbs)
		return 0, tooLargeErr(fmt.Sprintf("entry %q produced more data than its header declared", f.Name))
	}
	return n, nil
}

// UnzipArchive is a convenience wrapper for extracting an archive that already
// lives inside the root.
func (r *Root) UnzipArchive(relZip, relDest string, opts UnzipOptions) (UnzipResult, error) {
	abs, err := r.Resolve(relZip)
	if err != nil {
		return UnzipResult{}, err
	}
	return r.Unzip(abs, relDest, opts)
}

// ErrZipSlip is returned by the low-level guard when an entry tries to escape.
// The high-level [Root.Unzip] reports such entries in UnzipResult.Skipped
// instead of failing the whole archive, but the sentinel is exported so tests
// and callers can assert on the category.
var ErrZipSlip = errors.New("files: zip entry escapes the destination")
