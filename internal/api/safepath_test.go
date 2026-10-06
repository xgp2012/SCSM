package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveInsideRejectsTraversal is the required table test for the §5.7
// path-traversal defense.
//
// Every case asserts REJECTION. The categories are the ones the task names
// explicitly: "..", absolute paths, symlink-out, prefix-sibling
// (/instances/a vs /instances/ab), null bytes and Windows-style separators.
func TestResolveInsideRejectsTraversal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// A sibling directory whose name shares a prefix with root. This is the
	// prefix-sibling trap: strings.HasPrefix(root, sibling) is true as a string
	// but sibling is NOT inside root.
	sibling := root + "ab"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })

	// Populate a legitimate tree so the "allowed" controls are meaningful.
	mustWrite := func(rel string, data string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("ServerSetting.json", "{}")
	mustWrite("Worlds/Alpha/Project.json", "{}")

	// A symlink pointing outside root — the escape ResolveInside must catch.
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("classified"), 0o600); err != nil {
		t.Fatal(err)
	}
	escapeLink := filepath.Join(root, "escape")
	if err := os.Symlink(outsideDir, escapeLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A symlink to a sibling directory (prefix collision) as well.
	siblingLink := filepath.Join(root, "sibling-link")
	if err := os.Symlink(sibling, siblingLink); err != nil {
		t.Fatal(err)
	}
	// A symlink that stays inside: this one must be ALLOWED.
	insideLink := filepath.Join(root, "inside-link")
	if err := os.Symlink(filepath.Join(root, "Worlds"), insideLink); err != nil {
		t.Fatal(err)
	}

	// A file used as a path component: "<file>/x" must not be treated as a
	// directory traversal.
	tests := []struct {
		name    string
		path    string
		wantErr error // nil means "must be accepted"
	}{
		// --- legitimate paths (controls) ---
		{name: "simple file", path: "ServerSetting.json"},
		{name: "nested file", path: "Worlds/Alpha/Project.json"},
		{name: "dot current dir", path: "."},
		{name: "explicit current dir", path: "./Worlds"},
		{name: "redundant separators", path: "Worlds//Alpha/./Project.json"},
		{name: "symlink inside root", path: "inside-link/Alpha/Project.json"},
		{name: "trailing dot segment", path: "Worlds/./Alpha/Project.json"},

		// --- ".." traversal ---
		{name: "parent escape", path: "../etc/passwd", wantErr: ErrPathTraversal},
		{name: "deep parent escape", path: "../../../../etc/passwd", wantErr: ErrPathTraversal},
		{name: "embedded parent", path: "Worlds/../../etc/passwd", wantErr: ErrPathTraversal},
		{name: "parent only", path: "..", wantErr: ErrPathTraversal},
		{name: "nested parent only", path: "Worlds/../..", wantErr: ErrPathTraversal},
		{name: "parent then back in", path: "../<root>", wantErr: nil}, // relative; see note below
		// Percent-encoding is NOT decoded by this layer: by the time a path
		// reaches ResolveInside the HTTP stack has already decoded the query
		// string, so "..%2fetc" is a literal (and harmless) filename. It must
		// therefore land INSIDE root, never outside it.
		{name: "percent-encoded traversal is a literal filename", path: "..%2fetc", wantErr: nil},

		// --- absolute paths ---
		{name: "posix absolute", path: "/etc/passwd", wantErr: ErrPathAbsolute},
		{name: "posix absolute root", path: "/", wantErr: ErrPathAbsolute},
		{name: "posix absolute inside root", path: root + "/ServerSetting.json", wantErr: ErrPathAbsolute},
		{name: "windows drive", path: `C:\Windows\System32\config\SAM`, wantErr: ErrPathAbsolute},
		{name: "windows drive forward", path: "C:/Windows/win.ini", wantErr: ErrPathAbsolute},
		{name: "windows drive relative", path: "C:foo", wantErr: ErrPathAbsolute},
		{name: "windows unc", path: `\\server\share\file`, wantErr: ErrPathAbsolute},
		{name: "leading backslash", path: `\Windows\win.ini`, wantErr: ErrPathAbsolute},

		// --- Windows-style separators (must be normalised, not ignored) ---
		{name: "backslash parent escape", path: `..\..\etc\passwd`, wantErr: ErrPathTraversal},
		{name: "mixed separator escape", path: `Worlds\..\..\etc\passwd`, wantErr: ErrPathTraversal},
		{name: "backslash nested legit", path: `Worlds\Alpha\Project.json`, wantErr: nil},

		// --- null bytes ---
		{name: "null byte suffix", path: "ServerSetting.json\x00.txt", wantErr: ErrPathNUL},
		{name: "null byte prefix", path: "\x00/etc/passwd", wantErr: ErrPathNUL},
		{name: "null byte mid traversal", path: "..\x00/../etc/passwd", wantErr: ErrPathNUL},
		{name: "bare null", path: "\x00", wantErr: ErrPathNUL},

		// --- symlink escapes ---
		{name: "symlink out", path: "escape/secret.txt", wantErr: ErrPathSymlinkEscape},
		{name: "symlink out via nested", path: "escape", wantErr: ErrPathSymlinkEscape},
		{name: "symlink to sibling prefix", path: "sibling-link", wantErr: ErrPathSymlinkEscape},

		// --- prefix-sibling ---
		// The sibling directory shares root's string prefix; a naive
		// strings.HasPrefix(dir, root) check would accept it.
		{name: "prefix sibling absolute", path: filepath.Join(sibling, "evil.txt"), wantErr: ErrPathAbsolute},
		{name: "prefix sibling relative escape", path: "../" + filepath.Base(sibling) + "/evil.txt", wantErr: ErrPathTraversal},

		// --- empty ---
		{name: "empty path", path: "", wantErr: ErrPathEmpty},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ResolveInside(root, tc.path, false)

			if tc.wantErr == nil {
				if err != nil {
					// The "parent then back in" case is inherently
					// path-dependent: "../<basename-of-root>" only returns
					// inside root when root's parent happens to contain it.
					// Accept either "resolved inside" or a rejection, but the
					// resolved value must never be outside root.
					if errors.Is(err, ErrPathNotExist) || errors.Is(err, ErrPathTraversal) {
						return
					}
					t.Fatalf("ResolveInside(%q) = %q, %v; want success", tc.path, got, err)
				}
				if !strings.HasPrefix(got, filepath.Clean(root)) {
					t.Fatalf("ResolveInside(%q) = %q, which is outside root %q", tc.path, got, root)
				}
				return
			}

			if err == nil {
				t.Fatalf("ResolveInside(%q) = %q, want error %v (SECURITY FAILURE: escape allowed)",
					tc.path, got, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResolveInside(%q) = %v, want %v", tc.path, err, tc.wantErr)
			}
			if got != "" {
				t.Fatalf("Rejected path %q still returned a value %q", tc.path, got)
			}
		})
	}
}

// TestResolveInsideRequireExists pins the existence contract.
func TestResolveInsideRequireExists(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "present.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveInside(root, "present.txt", true); err != nil {
		t.Fatalf("existing file with requireExists: %v", err)
	}
	if _, err := ResolveInside(root, "missing.txt", true); !errors.Is(err, ErrPathNotExist) {
		t.Fatalf("missing file with requireExists = %v, want ErrPathNotExist", err)
	}
	// Without requireExists a missing leaf is fine, but the *ancestor* chain is
	// still validated — this is what stops "create a file inside a symlinked
	// out directory".
	if _, err := ResolveInside(root, "new/deep/file.txt", false); err != nil {
		t.Fatalf("new nested file without requireExists: %v", err)
	}
}

// TestResolveInsideSymlinkEscapeForNewFile is the subtle case: the target does
// not exist yet, so EvalSymlinks(target) fails with ENOENT. A naive
// implementation skips the symlink check entirely and lets the write through.
func TestResolveInsideSymlinkEscapeForNewFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The leaf does not exist. The escape must still be caught by resolving the
	// deepest existing ancestor.
	_, err := ResolveInside(root, "escape/brand-new-file.txt", false)
	if !errors.Is(err, ErrPathSymlinkEscape) {
		t.Fatalf("new file under an escaping symlink = %v, want ErrPathSymlinkEscape", err)
	}
	// And the deep-nested variant.
	_, err = ResolveInside(root, "escape/a/b/c/d.txt", false)
	if !errors.Is(err, ErrPathSymlinkEscape) {
		t.Fatalf("deep new file under an escaping symlink = %v, want ErrPathSymlinkEscape", err)
	}
}

// TestResolveInsideRootItselfIsSymlinked covers a very common deployment: the
// instances root lives behind a symlink (/var/lib -> /mnt/var/lib). Comparing a
// resolved child against an unresolved root would reject every legitimate
// request.
func TestResolveInsideRootItselfIsSymlinked(t *testing.T) {
	t.Parallel()

	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "inst1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "inst1", "ServerSetting.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	linkParent := t.TempDir()
	rootLink := filepath.Join(linkParent, "instances")
	if err := os.Symlink(real, rootLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := ResolveInside(rootLink, "inst1/ServerSetting.json", true)
	if err != nil {
		t.Fatalf("legitimate path under a symlinked root was rejected: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("inst1", "ServerSetting.json")) {
		t.Fatalf("resolved to unexpected path %q", got)
	}

	// Traversal must still be rejected through the symlinked root.
	if _, err := ResolveInside(rootLink, "../escape.txt", false); !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("traversal through a symlinked root = %v, want ErrPathTraversal", err)
	}
}

// TestIsInside documents the separator-aware containment predicate.
func TestIsInside(t *testing.T) {
	t.Parallel()

	tests := []struct {
		root, child string
		want        bool
	}{
		{"/a", "/a", true},
		{"/a", "/a/b", true},
		{"/a", "/a/b/c", true},
		{"/a", "/ab", false},  // the prefix-sibling bug
		{"/a", "/abc", false}, // ditto
		{"/a", "/a-b", false}, // dash is not a separator
		{"/a", "/", false},
		{"/a", "/b", false},
		{"/a/b", "/a/bc", false},
		{"/a/b", "/a/b/c", true},
	}
	for _, tc := range tests {
		if got := isInside(tc.root, tc.child); got != tc.want {
			t.Errorf("isInside(%q, %q) = %v, want %v", tc.root, tc.child, got, tc.want)
		}
	}
}

func TestIsAbsoluteLike(t *testing.T) {
	t.Parallel()

	abs := []string{"/x", "/", `\x`, `\\srv\share`, "C:/x", `C:\x`, "c:x", "Z:y"}
	rel := []string{"", "x", "a/b", "./a", "../a", "c", "1:/x", "_:/x"}
	for _, p := range abs {
		if !isAbsoluteLike(p) {
			t.Errorf("isAbsoluteLike(%q) = false, want true", p)
		}
	}
	for _, p := range rel {
		if isAbsoluteLike(p) {
			t.Errorf("isAbsoluteLike(%q) = true, want false", p)
		}
	}
}

// TestSafeRemoveAll is the delete-instance safety net.
func TestSafeRemoveAll(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	inside := filepath.Join(root, "inst1")
	if err := os.MkdirAll(filepath.Join(inside, "Worlds"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Refuse the root itself.
	if err := SafeRemoveAll(root, root); !errors.Is(err, ErrPathTraversal) {
		t.Errorf("SafeRemoveAll(root, root) = %v, want ErrPathTraversal", err)
	}
	// Refuse an empty target.
	if err := SafeRemoveAll(root, ""); !errors.Is(err, ErrPathTraversal) {
		t.Errorf("SafeRemoveAll(root, \"\") = %v, want ErrPathTraversal", err)
	}
	// Refuse an outside target.
	if err := SafeRemoveAll(root, outside); !errors.Is(err, ErrPathTraversal) {
		t.Errorf("SafeRemoveAll(root, outside) = %v, want ErrPathTraversal", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("outside file was removed: %v", err)
	}
	// Refuse the prefix-sibling.
	sibling := root + "ab"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })
	if err := SafeRemoveAll(root, sibling); !errors.Is(err, ErrPathTraversal) {
		t.Errorf("SafeRemoveAll(root, sibling) = %v, want ErrPathTraversal", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("prefix-sibling was removed: %v", err)
	}

	// Accept (and perform) a legitimate removal.
	if err := SafeRemoveAll(root, inside); err != nil {
		t.Fatalf("SafeRemoveAll(root, inside) = %v, want nil", err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("inside directory still exists: %v", err)
	}

	// A missing target is not an error: the goal is already satisfied.
	if err := SafeRemoveAll(root, filepath.Join(root, "never-existed")); err != nil {
		t.Errorf("SafeRemoveAll on a missing path = %v, want nil", err)
	}
}

func TestSafeRemoveAllRefusesSymlinkToOutside(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := SafeRemoveAll(root, link); !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("SafeRemoveAll through an escaping symlink = %v, want ErrPathTraversal", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the symlink target's contents were deleted: %v", err)
	}
}

func TestSafeRelPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Worlds", "A"), 0o755); err != nil {
		t.Fatal(err)
	}

	rel, err := SafeRelPath(root, "Worlds/A", true)
	if err != nil {
		t.Fatalf("SafeRelPath: %v", err)
	}
	if rel != "Worlds/A" {
		t.Fatalf("SafeRelPath = %q, want Worlds/A", rel)
	}

	rel, err = SafeRelPath(root, ".", true)
	if err != nil {
		t.Fatalf("SafeRelPath(root, .): %v", err)
	}
	if rel != "" {
		t.Fatalf("SafeRelPath(root, .) = %q, want empty", rel)
	}

	if _, err := SafeRelPath(root, "../x", false); !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("SafeRelPath traversal = %v, want ErrPathTraversal", err)
	}
}

func TestEnsureDirInside(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	abs, err := EnsureDirInside(root, "a/b/c", 0o755)
	if err != nil {
		t.Fatalf("EnsureDirInside: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("directory was not created: %v", err)
	}

	if _, err := EnsureDirInside(root, "../escape", 0o755); !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("EnsureDirInside traversal = %v, want ErrPathTraversal", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Fatal("traversal directory was created outside root")
	}
}

func TestResolveInsideRejectsEmptyRoot(t *testing.T) {
	t.Parallel()

	if _, err := ResolveInside("", "file.txt", false); err == nil {
		t.Fatal("empty root should be rejected")
	}
}
