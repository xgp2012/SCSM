package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file implements the path-traversal defense required by §5.7:
//
//	所有文件 API 的 path 必须经 filepath.Clean + filepath.EvalSymlinks 后校验
//	strings.HasPrefix(resolved, instanceDir)；拒绝符号链接逃逸。
//
// It is deliberately a single, small, heavily-tested function so that every file
// endpoint (and the delete-instance directory removal) funnels through exactly
// one implementation. A second, subtly different check elsewhere is how traversal
// bugs are born.

// Path traversal errors. Handlers map all of them to HTTP 403 with code
// "forbidden" — deliberately not 404, because the request is a clear attack
// rather than a missing file, and operators want it visible in logs.
var (
	// ErrPathTraversal is the umbrella error for any rejected path.
	ErrPathTraversal = errors.New("api: path escapes the instance directory")
	// ErrPathEmpty is returned for an empty path.
	ErrPathEmpty = errors.New("api: path must not be empty")
	// ErrPathNUL is returned when the path contains a NUL byte.
	ErrPathNUL = errors.New("api: path must not contain NUL bytes")
	// ErrPathAbsolute is returned for an absolute path.
	ErrPathAbsolute = errors.New("api: absolute paths are not allowed")
	// ErrPathSymlinkEscape is returned when resolving symlinks lands outside
	// the root.
	ErrPathSymlinkEscape = errors.New("api: symlink resolves outside the instance directory")
	// ErrPathNotExist is returned when the target does not exist and the caller
	// required existence.
	ErrPathNotExist = errors.New("api: path does not exist")
)

// ResolveInside resolves a client-supplied relative path against root and
// guarantees the result stays inside root.
//
// The algorithm, in order:
//
//  1. Reject empty paths and any path containing a NUL byte. A NUL is rejected
//     outright rather than truncated: some syscalls stop at NUL, so "safe\x00../../etc"
//     can behave differently in the validator and in the kernel.
//  2. Reject absolute paths (POSIX "/x" and Windows "C:\x" or "\x"). An
//     absolute path always escapes root, so there is no need to resolve it
//     first — rejecting early avoids depending on EvalSymlinks semantics for
//     paths that do not exist.
//  3. Normalise Windows-style backslashes to the host separator *before*
//     cleaning, so "..\\..\\etc" is caught on Linux too.
//  4. filepath.Clean the joined path, which collapses "..", ".", and duplicate
//     separators.
//  5. Verify the cleaned path is root or sits under root, using a
//     separator-aware prefix comparison. This is the check that a naive
//     strings.HasPrefix gets wrong: "/instances/a" is a prefix of
//     "/instances/ab" as a string but is not a parent directory.
//  6. filepath.EvalSymlinks the *deepest existing ancestor* of the target and
//     re-verify containment against the symlink-resolved root. This catches a
//     symlink inside the instance directory pointing at /etc, which steps 1–5
//     cannot see because they never touch the filesystem.
//
// The final check compares resolved-to-resolved, which is important: if
// instances_dir itself lives under a symlink (a very common deployment, e.g.
// /var/lib -> /mnt/var/lib), comparing a resolved child against an unresolved
// root would reject every legitimate request.
//
// requireExists makes a missing path an error. When false, the *deepest
// existing ancestor* is still validated — so creating a new file inside a
// symlinked-out directory is caught before the file is created.
//
// The returned path is absolute and safe to pass to os.* directly.
func ResolveInside(root, rel string, requireExists bool) (string, error) {
	if rel == "" {
		return "", ErrPathEmpty
	}
	if strings.ContainsRune(rel, 0) {
		return "", ErrPathNUL
	}
	if root == "" {
		return "", fmt.Errorf("api: instance directory is not configured")
	}

	// Step 2: absolute paths (POSIX, Windows drive-letter and UNC).
	if isAbsoluteLike(rel) {
		return "", fmt.Errorf("%w: %q", ErrPathAbsolute, rel)
	}

	// Step 3: normalise Windows separators so the check is host-independent.
	rel = strings.ReplaceAll(rel, "\\", string(filepath.Separator))

	// Resolve root itself first, so all later comparisons are apples to apples.
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("api: resolving instance directory %q: %w", root, err)
	}
	cleanRoot := filepath.Clean(rootAbs)

	resolvedRoot, err := evalSymlinksBestEffort(cleanRoot)
	if err != nil {
		return "", err
	}

	// Step 4: join and clean.
	target := filepath.Clean(filepath.Join(cleanRoot, rel))

	// Step 5: lexical containment (separator-aware).
	if !isInside(cleanRoot, target) {
		return "", fmt.Errorf("%w: %q", ErrPathTraversal, rel)
	}

	// Step 6: symlink-aware containment.
	resolvedTarget, err := evalSymlinksBestEffort(target)
	if err != nil {
		return "", err
	}
	if !isInside(resolvedRoot, resolvedTarget) {
		return "", fmt.Errorf("%w: %q", ErrPathSymlinkEscape, rel)
	}

	if requireExists {
		if _, err := os.Lstat(target); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("%w: %q", ErrPathNotExist, rel)
			}
			return "", fmt.Errorf("api: stating %q: %w", rel, err)
		}
	}

	return target, nil
}

// evalSymlinksBestEffort resolves symlinks for the deepest existing ancestor of
// p, appending the not-yet-existing remainder unchanged.
//
// The naive implementation — plain filepath.EvalSymlinks(p) — fails with
// ENOENT for a target that does not exist yet, which is exactly the "create a
// new file / mkdir" case. Walking up to the deepest existing ancestor lets the
// symlink check still apply to the directory the new entry would be created in.
func evalSymlinksBestEffort(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) && !isNotExistErr(err) {
		return "", fmt.Errorf("api: resolving symlinks for %q: %w", p, err)
	}

	// Walk up until something exists.
	remainder := make([]string, 0, 4)
	cur := p
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the filesystem root without finding anything that
			// exists, which should be impossible on a sane system. Fall back
			// to the cleaned input rather than failing the request.
			return p, nil
		}
		remainder = append(remainder, filepath.Base(cur))
		cur = parent

		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			// Rebuild downward. Nothing here is a symlink yet (it does not
			// exist), so plain joins are correct.
			out := resolved
			for i := len(remainder) - 1; i >= 0; i-- {
				out = filepath.Join(out, remainder[i])
			}
			// Re-verify: a *parent* directory may itself be the symlink we
			// care about, and we have just resolved it.
			return out, nil
		}
		if !errors.Is(err, os.ErrNotExist) && !isNotExistErr(err) {
			return "", fmt.Errorf("api: resolving symlinks for %q: %w", cur, err)
		}
	}
}

// isNotExistErr copes with errors that wrap ENOENT without matching errors.Is
// (some filesystems and older Go paths report syscall.ENOTDIR for a path whose
// parent is a file — treat that as "does not exist" too).
func isNotExistErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "not a directory") ||
		strings.Contains(msg, "ENOENT") ||
		strings.Contains(msg, "ENOTDIR")
}

// isInside reports whether child is root itself or lives beneath root.
//
// The comparison is done on cleaned absolute paths and is separator-aware, so
// "/a/ab" is NOT considered inside "/a" (the prefix-sibling bug called out in
// the task), while "/a/b" and "/a" are.
func isInside(root, child string) bool {
	if root == child {
		return true
	}
	// Ensure the root has a trailing separator exactly once, then compare
	// prefixes. This is the canonical fix for prefix-sibling confusion.
	withSep := root
	if !strings.HasSuffix(withSep, string(filepath.Separator)) {
		withSep += string(filepath.Separator)
	}
	return strings.HasPrefix(child, withSep)
}

// isAbsoluteLike detects absolute paths across platforms, because the panel
// accepts requests that may have been authored on a Windows client.
func isAbsoluteLike(p string) bool {
	if p == "" {
		return false
	}
	// POSIX.
	if p[0] == '/' {
		return true
	}
	// A leading backslash is a root-relative path on Windows.
	if p[0] == '\\' {
		return true
	}
	// Windows drive-letter: "C:\", "C:/", and the odd but real "C:x".
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	// Windows UNC: "\\server\share".
	if strings.HasPrefix(p, "//") {
		return true
	}
	return false
}

// SafeRelPath is ResolveInside's counterpart for callers that want the cleaned
// relative path rather than an absolute one (e.g. to store in the database or
// echo back to the client).
//
// It returns the path in slash form, guaranteed to be relative and free of
// "..", so it is safe to persist and to interpolate into a UI breadcrumb.
func SafeRelPath(root, rel string, requireExists bool) (string, error) {
	abs, err := ResolveInside(root, rel, requireExists)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// Resolve the root the same way ResolveInside did so the Rel computation
	// is consistent even under symlinks.
	if r, rerr := evalSymlinksBestEffort(filepath.Clean(rootAbs)); rerr == nil {
		if a, aerr := evalSymlinksBestEffort(abs); aerr == nil {
			if rRel, rerr := filepath.Rel(r, a); rerr == nil && !strings.HasPrefix(rRel, "..") {
				if rRel == "." {
					// The caller asked for the root itself; represent that as
					// the empty relative path so the UI breadcrumb is blank
					// rather than showing a spurious ".".
					return "", nil
				}
				return filepath.ToSlash(rRel), nil
			}
		}
	}
	rRel, err := filepath.Rel(filepath.Clean(rootAbs), abs)
	if err != nil {
		return "", err
	}
	if rRel == "." {
		return "", nil
	}
	if strings.HasPrefix(rRel, "..") {
		return "", fmt.Errorf("%w: %q", ErrPathTraversal, rel)
	}
	return filepath.ToSlash(rRel), nil
}

// EnsureDirInside creates dir (recursively) but only after resolving it against
// root. It is the safe wrapper used by instance creation and mkdir handlers.
func EnsureDirInside(root, rel string, perm os.FileMode) (string, error) {
	abs, err := ResolveInside(root, rel, false)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, perm); err != nil {
		return "", fmt.Errorf("api: creating directory %q: %w", rel, err)
	}
	return abs, nil
}

// SafeRemoveAll removes a directory tree, but only after verifying it is
// strictly inside root and is not root itself.
//
// This backs DELETE /instances/:id?delete_dir=true. The extra "not root" check
// matters because ResolveInside("") returns root, and a bug that passed an
// empty dir through would otherwise delete the entire instances tree.
func SafeRemoveAll(root, target string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("%w: refusing to remove an empty path", ErrPathTraversal)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cleanRoot := filepath.Clean(rootAbs)

	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	cleanTarget := filepath.Clean(targetAbs)

	resolvedRoot, err := evalSymlinksBestEffort(cleanRoot)
	if err != nil {
		return err
	}

	// The target may not exist; resolve best-effort either way.
	resolvedTarget, err := evalSymlinksBestEffort(cleanTarget)
	if err != nil {
		return err
	}

	if !isInside(cleanRoot, cleanTarget) || !isInside(resolvedRoot, resolvedTarget) {
		return fmt.Errorf("%w: refusing to remove %q which is outside %q", ErrPathTraversal, target, root)
	}
	if resolvedTarget == resolvedRoot || cleanTarget == cleanRoot {
		return fmt.Errorf("%w: refusing to remove the instances root itself", ErrPathTraversal)
	}

	if _, err := os.Lstat(cleanTarget); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // already gone: the handler's goal is satisfied
		}
		return err
	}

	if err := os.RemoveAll(cleanTarget); err != nil {
		return fmt.Errorf("api: removing %q: %w", target, err)
	}
	return nil
}
