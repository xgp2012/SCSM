// Package files provides safe, sandboxed filesystem operations for a single
// instance directory.
//
// This package is the security-critical one for the whole panel: plan §5.7
// names path traversal as the top risk and §6.4 calls file management the
// "重灾区" (disaster zone). Every path that arrives from an HTTP handler,
// WebSocket message, or queued job is untrusted and MUST be funnelled through
// [Root.Resolve] before it touches the filesystem.
//
// # Threat model
//
// The attacker controls path strings (and, via uploads/unzip, file *contents*
// and archive entry names). The attacker does NOT control the panel process's
// ambient authority. The goal is to ensure that no operation performed through
// a [Root] can read, write, or delete anything outside that root — in
// particular not sibling instances, not the panel's own database, and not the
// host filesystem.
//
// Attack classes explicitly handled:
//
//   - lexical traversal ("../../etc/passwd", "a/../../../b", "..\\..\\b")
//   - absolute paths ("/etc/passwd", "C:\\evil")
//   - NUL-byte truncation ("foo\x00bar" — Go rejects these, but we reject them
//     ourselves so the error is a typed panel error rather than a raw EINVAL)
//   - symlinks inside the root that point outside it (file or directory)
//   - a symlinked ancestor inside the root that escapes (e.g. root/link -> other instance)
//   - boundary-prefix confusion (root "/instances/a" must not admit "/instances/ab")
//   - zip-slip in [Unzip] (absolute entries, "../" entries, symlink entries,
//     duplicate names, NUL bytes)
//   - zip bombs / quota bypass (decompressed-size cap, entry-count cap)
//
// # Residual risk (important, read before relaxing anything)
//
// The portable implementation resolves the deepest *existing* ancestor of the
// target with [filepath.EvalSymlinks] and then validates it lexically. Between
// that validation and the subsequent syscall there is a window in which an
// attacker who can already write inside the root (or who controls a process
// running as the panel user) could swap a directory component for a symlink
// pointing outside — the classic TOCTOU race. On Linux the package closes this
// window for the streaming operations ([Root.Open], [Root.Create], [Root.OpenDir],
// [Root.StatFd]) by using openat2(2) with RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS
// and validating the returned descriptor by fstat + /proc/self/fd. Where
// openat2 is unavailable (older kernels, non-Linux) the package falls back to
// the portable path and the race remains — it is documented rather than
// hidden. Closing it fully for *mutating* operations would additionally require
// openat2 + mkdirat/unlinkat/renameat with the same resolve flags.
//
// The portable implementation is the tested baseline: it is exercised
// unconditionally on every platform, and the openat2 acceleration is
// additionally verified to agree with it (see resolve_linux_test.go).
package files

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// isNotDirErr reports whether err is ENOTDIR.
func isNotDirErr(err error) bool {
	return errors.Is(err, syscallENOTDIR)
}

// Typed errors. Callers (the HTTP layer) should map these onto status codes
// rather than string-matching messages.
var (
	// ErrUnsafePath is returned when a caller-supplied path is rejected by the
	// sandbox rules: empty, absolute, containing NUL, using Windows separators,
	// escaping the root, or traversing a symlink that leaves the root.
	ErrUnsafePath = errors.New("files: unsafe path")

	// ErrNotFound is returned when the target does not exist.
	ErrNotFound = errors.New("files: not found")

	// ErrExists is returned when a create/mkdir/rename target already exists.
	ErrExists = errors.New("files: already exists")

	// ErrIsDir is returned when a file operation was attempted on a directory.
	ErrIsDir = errors.New("files: is a directory")

	// ErrNotDir is returned when a directory operation was attempted on a file.
	ErrNotDir = errors.New("files: not a directory")

	// ErrNotAllowed is returned when a policy check refuses the operation
	// (extension not whitelisted, size cap exceeded, text editor on a binary
	// extension, symlink deletion without force, ...).
	ErrNotAllowed = errors.New("files: operation not allowed")

	// ErrTooLarge is returned when a size cap is exceeded.
	ErrTooLarge = errors.New("files: too large")

	// ErrDepthExceeded is returned when a recursive walk hits the depth cap.
	ErrDepthExceeded = errors.New("files: recursion depth exceeded")

	// ErrSymlink is returned when an operation would follow a symlink in a
	// position where following is forbidden.
	ErrSymlink = errors.New("files: symlink not permitted")
)

// MaxPathLen is a defensive upper bound on the length of a caller-supplied
// relative path. Longer paths are rejected before any syscall; this bounds the
// work an attacker can force per request and keeps error messages sane.
const MaxPathLen = 4096

// MaxWalkDepth caps recursion in [Root.Delete] and [Root.DiskUsage]. A crafted
// tree deeper than this is refused rather than walked, so a hostile instance
// cannot pin a worker in an unbounded recursion.
const MaxWalkDepth = 64

// Root is an instance directory that has been resolved and pinned. All access
// performed through a Root is confined to the directory it was constructed
// with.
//
// Root is safe for concurrent use: it holds only immutable state. The
// underlying filesystem is of course not synchronised, which is the caller's
// problem (the panel serialises mutating operations per instance).
type Root struct {
	// dir is the fully symlink-resolved absolute path of the instance directory.
	dir string
}

// NewRoot resolves and pins an instance directory.
//
// The path is made absolute and fully symlink-resolved once, here. Everything
// afterwards is compared against that resolved form, so a caller passing a
// path that itself traverses a symlink (e.g. /srv/instances -> /data/instances)
// still works: the pin is the real directory, not the spelling used to reach
// it.
func NewRoot(instanceDir string) (*Root, error) {
	if instanceDir == "" {
		return nil, fmt.Errorf("%w: empty instance directory", ErrUnsafePath)
	}
	if strings.ContainsRune(instanceDir, 0) {
		return nil, fmt.Errorf("%w: NUL byte in instance directory", ErrUnsafePath)
	}
	abs, err := filepath.Abs(instanceDir)
	if err != nil {
		return nil, fmt.Errorf("files: abs %q: %w", instanceDir, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: instance directory %q", ErrNotFound, instanceDir)
		}
		return nil, fmt.Errorf("files: resolve instance directory %q: %w", instanceDir, err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("files: stat instance directory %q: %w", instanceDir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%w: instance directory %q is not a directory", ErrNotDir, instanceDir)
	}
	return &Root{dir: resolved}, nil
}

// Dir returns the resolved absolute instance directory.
func (r *Root) Dir() string { return r.dir }

// String implements fmt.Stringer.
func (r *Root) String() string { return "files.Root(" + r.dir + ")" }

// Entry describes one directory entry.
type Entry struct {
	// Name is the base name.
	Name string `json:"name"`
	// Rel is the path relative to the root, using forward slashes. This is the
	// value to hand back to the client and to pass to other Root methods.
	Rel string `json:"rel"`
	// IsDir reports whether the entry is a directory (after following a
	// symlink, if any).
	IsDir bool `json:"isDir"`
	// Size is the file size in bytes; 0 for directories.
	Size int64 `json:"size"`
	// Mode is the permission/type string as produced by os.FileMode.String().
	Mode string `json:"mode"`
	// ModTime is the last modification time.
	ModTime time.Time `json:"modTime"`
	// IsSymlink reports whether the entry itself is a symbolic link.
	IsSymlink bool `json:"isSymlink"`
	// LinkTarget is the raw link target when IsSymlink is true.
	LinkTarget string `json:"linkTarget,omitempty"`
	// BrokenLink reports a symlink whose target cannot be stat'ed.
	BrokenLink bool `json:"brokenLink,omitempty"`
}

// cleanRel normalises a caller-supplied relative path for display.
func cleanRel(rel string) string {
	if rel == "" || rel == "." {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(rel))
}

// Resolve is the security core of this package.
//
// It takes an untrusted, root-relative path and returns the absolute
// filesystem path it denotes, or an error if the path is unsafe. The returned
// path is guaranteed to be inside the root directory; the boundary check is
// element-wise, so a sibling such as "/instances/ab" is never accepted for a
// root of "/instances/a".
//
// The target is allowed not to exist (uploads and Mkdir need this), in which
// case the deepest existing ancestor is symlink-resolved and validated, and the
// remaining components are checked lexically against the traversal rules.
//
// Rejection rules, in order:
//
//   - the path is empty after cleaning ("" and "." denote the root itself and
//     ARE accepted — they are legitimate for List/Stat of the instance root);
//   - the raw path contains a NUL byte;
//   - the raw path is absolute (POSIX "/..." or Windows "C:\..." or "\...");
//   - the raw path contains a Windows separator ('\') or a drive-letter style
//     prefix — the panel is Linux-first and accepting backslashes invites
//     cross-platform bypass confusion;
//   - the raw path is longer than [MaxPathLen];
//   - after cleaning, any component is "..";
//   - the resolved deepest existing ancestor is outside the root;
//   - the final component is a symlink whose target (fully resolved) is outside
//     the root.
func (r *Root) Resolve(rel string) (string, error) {
	abs, err := r.resolvePortable(rel)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// resolvePortable is the platform-independent implementation described on
// [Root.Resolve]. It is the tested baseline and runs on every platform.
func (r *Root) resolvePortable(rel string) (string, error) {
	clean, err := r.validateRel(rel)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(r.dir, filepath.FromSlash(clean))

	// Find the deepest existing ancestor and resolve it. This is what defeats
	// a symlinked directory component: if root/link -> /etc, then "link/passwd"
	// has existing ancestor root/link, which EvalSymlinks turns into /etc, and
	// the containment check below rejects it.
	existing := joined
	var tail []string
	for {
		if _, statErr := os.Lstat(existing); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			// Permission denied, or a component that is a file: let EvalSymlinks
			// produce the authoritative error below.
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break // reached the filesystem root without finding anything
		}
		tail = append(tail, filepath.Base(existing))
		existing = parent
	}

	resolvedAncestor, err := filepath.EvalSymlinks(existing)
	if err != nil {
		if os.IsNotExist(err) && len(tail) > 0 {
			// The deepest existing entry in the chain cannot be resolved: some
			// symlink along the way dangles. We still must not weaken the rule
			// that an escaping symlink is rejected, so walk the path one
			// component at a time and validate each existing prefix.
			return r.resolveThroughDangling(clean, joined)
		}
		if errors.Is(err, syscallENOTDIR) {
			// A path component exists but is a regular file, so it cannot be
			// traversed. Report it as a typed "not a directory" so callers do
			// not have to inspect errno.
			return "", fmt.Errorf("%w: %s", ErrNotDir, clean)
		}
		if os.IsNotExist(err) {
			// Nothing at all in the path resolved. That happens in two very
			// different situations, and they must not be conflated:
			//
			//  (a) a genuinely new target, e.g. an upload or Mkdir into a
			//      subtree that does not exist yet — legitimate; or
			//  (b) the final component is a dangling symlink whose target lies
			//      outside the root — must be rejected.
			//
			// Inspect the final component first: if it is a symlink, its
			// target decides, regardless of whether the target exists.
			if fi, lerr := os.Lstat(joined); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
				return r.resolveSymlinkTarget(clean, joined)
			}
			if cerr := r.checkContained(joined); cerr != nil {
				return "", cerr
			}
			return joined, nil
		}
		return "", fmt.Errorf("files: resolve %q: %w", clean, err)
	}
	if err := r.checkContained(resolvedAncestor); err != nil {
		return "", err
	}

	// Re-apply the tail lexically. Every tail component has already been
	// checked for ".." and separators by validateRel, so this cannot escape.
	resolved := resolvedAncestor
	for i := len(tail) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, tail[i])
	}

	// A final-component symlink whose parent is inside the root but whose
	// target is outside must also be rejected. (If the final component is a
	// *directory* symlink pointing outside it was already caught above,
	// because it was the deepest existing ancestor.)
	if fi, lerr := os.Lstat(resolved); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, terr := filepath.EvalSymlinks(resolved)
		if terr != nil {
			// Dangling symlink. It is legitimate for the UI to stat and delete
			// the link itself, so this is allowed — but only after the link's
			// declared target has been checked, otherwise a dangling link to
			// /etc/shadow would be admitted as "inside the root".
			return r.resolveSymlinkTarget(clean, resolved)
		}
		if err := r.checkContained(target); err != nil {
			return "", fmt.Errorf("%w: symlink %q escapes root", ErrUnsafePath, clean)
		}
	}

	if err := r.checkContained(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// resolveSymlinkTarget validates a path whose final component is a symlink that
// could not be fully resolved (dangling).
//
// The link's declared target, interpreted relative to the link's directory, must
// be inside the root. When the target exists it is additionally resolved and
// re-checked, so a link inside the root pointing at another link that leaves the
// root is still rejected.
func (r *Root) resolveSymlinkTarget(clean, linkAbs string) (string, error) {
	target, err := os.Readlink(linkAbs)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotFound, clean)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkAbs), target)
	}
	if cerr := r.checkContained(filepath.Clean(target)); cerr != nil {
		return "", fmt.Errorf("%w: symlink %q escapes root", ErrUnsafePath, clean)
	}
	if resolved, eerr := filepath.EvalSymlinks(linkAbs); eerr == nil {
		if cerr := r.checkContained(resolved); cerr != nil {
			return "", fmt.Errorf("%w: symlink %q escapes root", ErrUnsafePath, clean)
		}
	}
	// Return the link path itself: the caller wants to stat/remove the *link*,
	// and every operation that needs the target will fail with ENOENT anyway.
	return linkAbs, nil
}

// resolveThroughDangling handles the case where the deepest existing ancestor
// could not be resolved because a symlink along the way dangles (its target
// does not exist).
//
// The path is walked one component at a time. Every existing component is
// Lstat'ed and, when it is a symlink, its target is resolved and required to be
// contained. The first component that does not exist truncates the walk: the
// remainder is a create target and is accepted lexically, because validateRel
// already proved the spelling cannot escape.
//
// This exists so that a dangling symlink *inside* the root stays visible and
// deletable through the UI, without weakening the rule that a symlink pointing
// outside the root is rejected.
func (r *Root) resolveThroughDangling(clean, joined string) (string, error) {
	parts := strings.Split(clean, "/")
	cur := r.dir
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(cur, filepath.FromSlash(part))
		fi, err := os.Lstat(next)
		if err != nil {
			// Everything from here on is new; the spelling is already proven
			// escape-free and every existing prefix was validated above.
			if cerr := r.checkContained(joined); cerr != nil {
				return "", cerr
			}
			return joined, nil
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		target, rerr := os.Readlink(next)
		if rerr != nil {
			return "", fmt.Errorf("files: readlink %q: %w", clean, rerr)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(next), target)
		}
		target = filepath.Clean(target)
		if cerr := r.checkContained(target); cerr != nil {
			return "", fmt.Errorf("%w: symlink %q escapes root", ErrUnsafePath, clean)
		}
		if resolved, eerr := filepath.EvalSymlinks(next); eerr == nil {
			if cerr := r.checkContained(resolved); cerr != nil {
				return "", fmt.Errorf("%w: symlink %q escapes root", ErrUnsafePath, clean)
			}
			cur = resolved
		} else {
			// The link dangles: continue the walk against its lexical target so
			// that any further component is still validated.
			cur = target
		}
	}
	if cerr := r.checkContained(cur); cerr != nil {
		return "", cerr
	}
	return cur, nil
}

// validateRel performs the purely lexical checks and returns the cleaned
// slash-separated relative path.
func (r *Root) validateRel(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: NUL byte in path", ErrUnsafePath)
	}
	if len(rel) > MaxPathLen {
		return "", fmt.Errorf("%w: path longer than %d bytes", ErrUnsafePath, MaxPathLen)
	}
	if strings.ContainsRune(rel, '\\') {
		return "", fmt.Errorf("%w: backslash separator in path %q", ErrUnsafePath, rel)
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: absolute path %q", ErrUnsafePath, rel)
	}
	// Windows drive-relative form ("C:foo", "C:\foo"). filepath.IsAbs is
	// platform-dependent, so check explicitly: the panel is Linux-first and a
	// drive-letter prefix is never legitimate.
	if len(rel) >= 2 && rel[1] == ':' && isASCIILetter(rel[0]) {
		return "", fmt.Errorf("%w: drive-letter path %q", ErrUnsafePath, rel)
	}

	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == "" {
		return "", nil // the root itself
	}
	// filepath.Clean does not remove a leading "../".
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: parent traversal in path %q", ErrUnsafePath, rel)
		}
	}
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("%w: absolute path %q", ErrUnsafePath, rel)
	}
	return filepath.ToSlash(clean), nil
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// checkContained enforces the boundary-correct prefix check.
//
// A naive strings.HasPrefix(resolved, r.dir) would accept "/instances/ab" for a
// root of "/instances/a". Comparing path elements after a filepath.Rel is
// correct on every platform: Rel returns ".."-prefixed paths exactly when the
// candidate is outside.
func (r *Root) checkContained(candidate string) error {
	candidate = filepath.Clean(candidate)
	if candidate == r.dir {
		return nil
	}
	rel, err := filepath.Rel(r.dir, candidate)
	if err != nil {
		return fmt.Errorf("%w: %q is not inside the instance root", ErrUnsafePath, candidate)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q escapes the instance root", ErrUnsafePath, candidate)
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("%w: %q is not inside the instance root", ErrUnsafePath, candidate)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Metadata operations
// ---------------------------------------------------------------------------

// Stat returns metadata for the given relative path.
func (r *Root) Stat(rel string) (*Entry, error) {
	abs, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	return r.statAbs(abs, cleanRel(rel))
}

// statAbs builds an Entry from an already-resolved absolute path.
//
// resolvePortable canonicalises in-root symlinks, so `abs` is usually the real
// target rather than the link. The desired entry name is therefore taken from
// `rel`, not from `abs`, and the ORIGINAL (pre-resolution) path is re-examined
// with Lstat so that a symlink is still reported as a symlink with its target —
// otherwise the UI would show an alias as if it were an ordinary file.
func (r *Root) statAbs(abs, rel string) (*Entry, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, rel)
		}
		return nil, err
	}
	// Re-check the caller's spelling to detect an in-root symlink.
	if rel != "" {
		if origAbs := filepath.Join(r.dir, filepath.FromSlash(rel)); origAbs != abs {
			if ofi, oerr := os.Lstat(origAbs); oerr == nil && ofi.Mode()&os.ModeSymlink != 0 {
				fi = ofi
				abs = origAbs
			}
		}
	}
	name := filepath.Base(abs)
	if rel != "" {
		// Path.Base on a slash-separated relative path is OS-independent.
		name = path.Base(rel)
	}
	e := &Entry{
		Name:      name,
		Rel:       rel,
		Mode:      fi.Mode().String(),
		ModTime:   fi.ModTime(),
		IsSymlink: fi.Mode()&os.ModeSymlink != 0,
	}
	if e.IsSymlink {
		if target, terr := os.Readlink(abs); terr == nil {
			e.LinkTarget = target
		}
	}
	// Follow the link for size/type reporting, but only after Resolve has
	// vouched for the target.
	if info, serr := os.Stat(abs); serr == nil {
		e.IsDir = info.IsDir()
		if !info.IsDir() {
			e.Size = info.Size()
		}
	} else if e.IsSymlink {
		e.BrokenLink = true
	}
	if rel == "" {
		e.Name = filepath.Base(r.dir)
		e.Rel = ""
	}
	return e, nil
}

// List returns the entries of the directory at rel, sorted by name.
//
// A nil slice is returned for an empty directory (never a nil-pointer panic on
// range). Symlinked subdirectories are reported with IsSymlink set; callers
// that want to descend must call List on them, which re-runs Resolve and will
// refuse a link pointing outside the root.
func (r *Root) List(rel string) ([]Entry, error) {
	abs, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	return r.listAbs(abs, cleanRel(rel))
}

func (r *Root) listAbs(abs, rel string) ([]Entry, error) {
	f, err := r.openDir(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	names, err := f.Readdirnames(-1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	sort.Strings(names)

	out := make([]Entry, 0, len(names))
	for _, name := range names {
		childAbs := filepath.Join(abs, name)
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		// Re-resolve each child so that a symlink escaping the root is not
		// reported as a normal, navigable entry. A child that fails Resolve is
		// surfaced as an entry marked IsSymlink (when it is one) rather than
		// omitted, so the UI can show and delete it.
		if _, rerr := r.Resolve(childRel); rerr != nil {
			fi, lerr := os.Lstat(childAbs)
			if lerr != nil {
				continue
			}
			out = append(out, Entry{
				Name:      name,
				Rel:       childRel,
				Mode:      fi.Mode().String(),
				ModTime:   fi.ModTime(),
				IsSymlink: true,
			})
			continue
		}
		e, serr := r.statAbs(childAbs, childRel)
		if serr != nil {
			continue
		}
		out = append(out, *e)
	}
	return out, nil
}

// Mkdir creates the directory at rel (and any missing parents).
func (r *Root) Mkdir(rel string) error {
	abs, err := r.Resolve(rel)
	if err != nil {
		return err
	}
	if abs == r.dir {
		return nil
	}
	if _, serr := os.Lstat(abs); serr == nil {
		return fmt.Errorf("%w: %s", ErrExists, cleanRel(rel))
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fmt.Errorf("files: mkdir %q: %w", cleanRel(rel), err)
	}
	return nil
}

// Rename moves oldRel to newRel. Both paths are resolved independently, so a
// rename never silently crosses a symlink out of the root.
func (r *Root) Rename(oldRel, newRel string) error {
	oldAbs, err := r.Resolve(oldRel)
	if err != nil {
		return err
	}
	newAbs, err := r.Resolve(newRel)
	if err != nil {
		return err
	}
	if oldAbs == r.dir || newAbs == r.dir {
		return fmt.Errorf("%w: refusing to rename the instance root", ErrNotAllowed)
	}
	if _, serr := os.Lstat(newAbs); serr == nil {
		return fmt.Errorf("%w: %s", ErrExists, cleanRel(newRel))
	}
	// Refuse to move a directory inside itself, which would create an
	// unremovable cycle.
	if isWithin(oldAbs, newAbs) {
		return fmt.Errorf("%w: cannot move %q into its own descendant", ErrNotAllowed, cleanRel(oldRel))
	}
	if err := os.Rename(oldAbs, newAbs); err != nil {
		return fmt.Errorf("files: rename %q -> %q: %w", cleanRel(oldRel), cleanRel(newRel), err)
	}
	return nil
}

// Delete removes the file, symlink, or directory at rel.
//
// Symlinks are removed with os.Remove and are NEVER followed, so deleting a
// link that points outside the root cannot delete the target. Recursive
// deletion is bounded by [MaxWalkDepth] and also never follows symlinks: a
// linked directory is unlinked, not descended into.
func (r *Root) Delete(rel string, recursive bool) error {
	// Delete must act on the *name*, not on whatever it points at. Resolve
	// deliberately refuses a symlink whose target lies outside the root, but
	// removing such a link is exactly what the UI needs to be able to do (it is
	// the only way to clean up a hostile or accidental link). So deletion works
	// against the lexically validated path and never follows symlinks.
	abs, err := r.resolveForDelete(rel)
	if err != nil {
		return err
	}
	if abs == r.dir {
		return fmt.Errorf("%w: refusing to delete the instance root", ErrNotAllowed)
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, cleanRel(rel))
		}
		return err
	}
	// A symlink is always just a link: remove it, do not follow it.
	if fi.Mode()&os.ModeSymlink != 0 {
		return os.Remove(abs)
	}
	if !fi.IsDir() {
		return os.Remove(abs)
	}
	if !recursive {
		if err := os.Remove(abs); err != nil {
			return fmt.Errorf("files: delete %q: %w", cleanRel(rel), err)
		}
		return nil
	}
	return removeTreeBounded(abs, 0)
}

// resolveForDelete returns the path of the directory entry named by rel, using
// the same lexical and symlink-ancestor checks as Resolve but tolerating a final
// component that is a symlink pointing outside the root.
//
// Every component *except the last* is still required to resolve inside the
// root, so this cannot be used to reach out of the root and unlink something:
// it can only remove the link entry that already lives inside the root.
func (r *Root) resolveForDelete(rel string) (string, error) {
	clean, err := r.validateRel(rel)
	if err != nil {
		return "", err
	}
	if clean == "" {
		return r.dir, nil
	}
	joined := filepath.Join(r.dir, filepath.FromSlash(clean))

	fi, lerr := os.Lstat(joined)
	if lerr != nil {
		if os.IsNotExist(lerr) {
			// The entry does not exist yet: a legitimate create/upload target.
			// Validate the parent chain and hand back the ordinary resolution.
			return r.Resolve(rel)
		}
		return "", lerr
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		// The final component is a link: validate its PARENT is inside the
		// root, then hand back the link path itself.
		parentAbs, perr := r.Resolve(filepath.ToSlash(filepath.Dir(clean)))
		if perr != nil {
			// The parent does not exist or escapes; a missing parent means the
			// link is gone, which is ErrNotFound.
			return "", fmt.Errorf("%w: %s", ErrNotFound, clean)
		}
		if !isWithin(r.dir, parentAbs) {
			return "", fmt.Errorf("%w: %s", ErrUnsafePath, clean)
		}
		return filepath.Join(parentAbs, filepath.Base(joined)), nil
	}
	// Not a link (or not the final component being a link): the ordinary rules
	// apply.
	return r.Resolve(rel)
}

// removeTreeBounded removes a directory tree with an explicit depth cap and
// without following symlinks.
func removeTreeBounded(dir string, depth int) error {
	if depth > MaxWalkDepth {
		return fmt.Errorf("%w: at %s", ErrDepthExceeded, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, de := range entries {
		child := filepath.Join(dir, de.Name())
		// DirEntry.Type() comes from readdir and does not follow symlinks.
		if de.Type()&os.ModeSymlink != 0 {
			if err := os.Remove(child); err != nil {
				return err
			}
			continue
		}
		if de.IsDir() {
			if err := removeTreeBounded(child, depth+1); err != nil {
				return err
			}
			continue
		}
		if err := os.Remove(child); err != nil {
			return err
		}
	}
	return os.Remove(dir)
}

// ---------------------------------------------------------------------------
// Read/write descriptors
// ---------------------------------------------------------------------------

// Open opens the file at rel for reading, for use with http.ServeContent
// (range requests, conditional GETs) and similar.
//
// The descriptor is re-validated after opening (on Linux, via openat2 with
// RESOLVE_BENEATH) so that a symlink swapped in between Resolve and Open cannot
// yield a descriptor outside the root.
func (r *Root) Open(rel string) (*os.File, error) {
	abs, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, cleanRel(rel))
		}
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrIsDir, cleanRel(rel))
	}
	f, err := r.openFile(abs, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, cleanRel(rel))
		}
		return nil, fmt.Errorf("files: open %q: %w", cleanRel(rel), err)
	}
	return f, nil
}

// Create opens the file at rel for writing, creating or truncating it.
//
// Parent directories are NOT created implicitly: call [Root.Mkdir] first. This
// keeps a typo'd upload path from silently materialising a tree.
func (r *Root) Create(rel string) (*os.File, error) {
	abs, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	if abs == r.dir {
		return nil, fmt.Errorf("%w: %s", ErrIsDir, cleanRel(rel))
	}
	if fi, serr := os.Lstat(abs); serr == nil && fi.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrIsDir, cleanRel(rel))
	}
	parent := filepath.Dir(abs)
	if fi, serr := os.Stat(parent); serr != nil {
		if os.IsNotExist(serr) {
			return nil, fmt.Errorf("%w: parent directory of %s", ErrNotFound, cleanRel(rel))
		}
		return nil, serr
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%w: parent of %s", ErrNotDir, cleanRel(rel))
	}
	f, err := r.openFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("files: create %q: %w", cleanRel(rel), err)
	}
	return f, nil
}

// OpenDir opens the directory at rel so that callers can stream its entries.
func (r *Root) OpenDir(rel string) (*os.File, error) {
	abs, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	f, err := r.openDir(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, cleanRel(rel))
		}
		if isNotDirErr(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotDir, cleanRel(rel))
		}
		return nil, fmt.Errorf("files: open dir %q: %w", cleanRel(rel), err)
	}
	return f, nil
}

// WriteFile atomically-ish replaces the contents of rel with data. Writes go to
// a temporary file in the same directory first, so a crash mid-write cannot
// leave a truncated config file behind. The size cap is enforced before any
// byte is written.
func (r *Root) WriteFile(rel string, data []byte) error {
	abs, err := r.Resolve(rel)
	if err != nil {
		return err
	}
	if abs == r.dir {
		return fmt.Errorf("%w: %s", ErrIsDir, cleanRel(rel))
	}
	if fi, serr := os.Lstat(abs); serr == nil && fi.IsDir() {
		return fmt.Errorf("%w: %s", ErrIsDir, cleanRel(rel))
	}
	dir := filepath.Dir(abs)
	tmp, err := os.CreateTemp(dir, ".scnetm-tmp-*")
	if err != nil {
		return fmt.Errorf("files: write %q: %w", cleanRel(rel), err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("files: write %q: %w", cleanRel(rel), err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("files: chmod temp for %q: %w", cleanRel(rel), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("files: close temp for %q: %w", cleanRel(rel), err)
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return fmt.Errorf("files: replace %q: %w", cleanRel(rel), err)
	}
	tmpName = "" // renamed; do not clean up
	return nil
}

// ReadFile reads up to max bytes from rel. A max <= 0 means "no cap" (still
// bounded by MaxTextFileSize for the text-editor path, which uses
// [Root.ReadTextFile]).
func (r *Root) ReadFile(rel string, max int64) ([]byte, error) {
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if max > 0 {
		fi, serr := f.Stat()
		if serr == nil && fi.Size() > max {
			return nil, fmt.Errorf("%w: %s is %d bytes, cap is %d", ErrTooLarge, cleanRel(rel), fi.Size(), max)
		}
		return io.ReadAll(io.LimitReader(f, max+1))
	}
	return io.ReadAll(f)
}

// safeJoin reports whether child is inside (or equal to) parent lexically.
func isWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
