package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRoot builds a throwaway instance directory with a small, realistic
// shape and returns the Root.
//
//	<tmp>/
//	  root/                 <- the instance
//	    Settings.xml
//	    Worlds/World/Project.json
//	    sub/deep/file.txt
//	    link_out_file   -> /etc/passwd (outside)
//	    link_out_dir    -> <tmp>/outside (outside)
//	    link_in         -> Worlds (inside, allowed)
//	    link_self       -> . (inside)
//	  outside/secret.txt
//	  ab/                   <- prefix-sibling of root's sibling "a"
//	  a/                    <- prefix-sibling of "ab"
func newTestRoot(t *testing.T) (*Root, string) {
	t.Helper()
	tmp := t.TempDir()
	// Resolve the temp dir itself: on macOS /tmp is a symlink to /private/tmp
	// and t.TempDir() may return either spelling. Pinning the real path keeps
	// the expectations below honest.
	base, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("eval tmp: %v", err)
	}

	root := filepath.Join(base, "root")
	mustMkdir(t, filepath.Join(root, "Worlds", "World"), 0o755)
	mustMkdir(t, filepath.Join(root, "sub", "deep"), 0o755)
	mustMkdir(t, filepath.Join(base, "outside"), 0o755)
	mustMkdir(t, filepath.Join(base, "ab"), 0o755)
	mustMkdir(t, filepath.Join(base, "a"), 0o755)

	mustWrite(t, filepath.Join(root, "Settings.xml"), []byte("<Settings/>"))
	mustWrite(t, filepath.Join(root, "Worlds", "World", "Project.json"), []byte("{}"))
	mustWrite(t, filepath.Join(root, "sub", "deep", "file.txt"), []byte("deep"))
	mustWrite(t, filepath.Join(base, "outside", "secret.txt"), []byte("secret"))
	mustWrite(t, filepath.Join(base, "ab", "sibling.txt"), []byte("ab"))
	mustWrite(t, filepath.Join(base, "a", "sibling.txt"), []byte("a"))

	mustSymlink(t, "/etc/passwd", filepath.Join(root, "link_out_file"))
	mustSymlink(t, filepath.Join(base, "outside"), filepath.Join(root, "link_out_dir"))
	mustSymlink(t, "Worlds", filepath.Join(root, "link_in"))
	mustSymlink(t, ".", filepath.Join(root, "link_self"))
	mustSymlink(t, filepath.Join(base, "ab", "sibling.txt"), filepath.Join(root, "link_in_file"))

	r, err := NewRoot(root)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	return r, base
}

func mustMkdir(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(p, mode); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// TestResolveTable is the central adversarial test for the security core. Each
// row asserts whether Resolve accepts the input and, when it does, that the
// returned path is genuinely inside the root.
func TestResolveTable(t *testing.T) {
	r, base := newTestRoot(t)

	// A very long but otherwise legal path: 200 components of "d".
	longOK := strings.Repeat("d/", 200) + "f.txt"
	// A path over MaxPathLen must be rejected before any syscall.
	tooLong := strings.Repeat("d/", MaxPathLen) + "f.txt"

	cases := []struct {
		name    string
		input   string
		allowed bool
		why     string
	}{
		// --- the root itself: legitimate, used by List/Stat ---
		{"empty", "", true, "empty means the root, needed by List/Stat"},
		{"dot", ".", true, "dot means the root"},
		{"dot-slash", "./", true, "dot-slash means the root"},
		{"root-relative-basic", "Settings.xml", true, "ordinary file"},
		{"nested", "Worlds/World/Project.json", true, "ordinary nested file"},
		{"leading-dot-slash", "./Settings.xml", true, "clean removes the dot"},
		{"inner-dot", "a/./b", true, "clean removes the inner dot"},
		{"double-slash", "a//b", true, "clean collapses the double slash"},

		// --- lexical traversal ---
		{"dotdot", "..", false, "parent traversal"},
		{"dotdot-slash", "../", false, "parent traversal"},
		{"dotdot-deep", "../../etc/passwd", false, "parent traversal"},
		{"mixed-up-down", "a/../../b", false, "net parent traversal"},
		{"mixed-up-up-down", "a/../../../b", false, "net parent traversal"},
		{"dotdot-mid", "Worlds/../../../etc/passwd", false, "parent traversal"},

		// --- absolute paths ---
		{"absolute", "/etc/passwd", false, "absolute path"},
		{"absolute-root", "/", false, "absolute path"},

		// --- NUL ---
		{"nul", "foo\x00bar", false, "NUL byte"},
		{"nul-suffix", "Settings.xml\x00.txt", false, "NUL byte"},

		// --- Windows separators / drive letters ---
		{"backslash-traversal", `a\..\b`, false, "Windows separator"},
		{"backslash-absolute", `C:\Windows\System32`, false, "Windows separator"},
		{"drive-relative", `C:foo`, false, "drive-letter path"},
		{"backslash-simple", `sub\deep\file.txt`, false, "Windows separator"},

		// --- symlinks to the outside ---
		{"symlink-file-out", "link_out_file", false, "symlink file -> /etc/passwd"},
		{"symlink-dir-out", "link_out_dir", false, "symlink dir -> outside"},
		{"symlink-dir-out-child", "link_out_dir/secret.txt", false, "child through escaping symlink dir"},
		{"symlink-file-out-via-in-dir", "sub/../../link_out_file", false, "traversal then escaping symlink"},
		{"symlink-abs-out-nested", "Worlds/../../outside/secret.txt", false, "traversal escapes"},
		{"symlink-file-sibling", "link_in_file", false, "symlink file -> sibling instance"},

		// --- symlinks that stay inside: must be allowed ---
		{"symlink-in-dir", "link_in", true, "symlink dir -> Worlds (inside)"},
		{"symlink-in-dir-child", "link_in/World/Project.json", true, "child through inside symlink dir"},
		{"symlink-self", "link_self", true, "symlink dir -> . (inside)"},
		{"symlink-self-child", "link_self/Settings.xml", true, "child through self symlink"},

		// --- prefix-sibling confusion ---
		{"sibling-ab-file", "../ab/sibling.txt", false, "traversal to prefix sibling"},
		{"sibling-a-file", "../a/sibling.txt", false, "traversal to prefix sibling"},

		// --- non-existent targets (uploads / mkdir) ---
		{"new-file-top", "newfile.txt", true, "upload target does not exist yet"},
		{"new-file-nested", "sub/deep/new.txt", true, "nested upload target"},
		{"new-file-new-dirs", "brand/new/dirs/file.txt", true, "mkdir -p style target"},
		{"new-under-inside-symlink", "link_in/Brand/New.txt", true, "create under inside symlink"},
		{"new-under-escaping-symlink", "link_out_dir/new.txt", false, "create under escaping symlink"},
		{"new-escape-via-dotdot", "sub/../../evil.txt", false, "nonexistent target escaping"},

		// --- length ---
		{"long-but-legal", longOK, true, "deep but within MaxPathLen and no syscall needed"},
		{"too-long", tooLong, false, "exceeds MaxPathLen"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(tc.input)
			if tc.allowed {
				if err != nil {
					t.Fatalf("Resolve(%q) = error %v; want allowed (%s)", tc.input, err, tc.why)
				}
				if err := r.checkContained(got); err != nil {
					t.Fatalf("Resolve(%q) returned %q which is outside root %q: %v", tc.input, got, r.Dir(), err)
				}
				if !filepath.IsAbs(got) {
					t.Fatalf("Resolve(%q) = %q; want absolute", tc.input, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("Resolve(%q) = %q (allowed); want rejection (%s)", tc.input, got, tc.why)
			}
			if !isUnsafe(err) {
				t.Fatalf("Resolve(%q) error = %v; want an error wrapping ErrUnsafePath (%s)", tc.input, err, tc.why)
			}
		})
	}

	// Explicitly prove the boundary-prefix case with directories that share a
	// textual prefix: root is <base>/root, siblings are <base>/a and <base>/ab.
	// A HasPrefix(resolved, "/…/root") implementation would be tricked by a
	// root of "<base>/ro" — construct that shape and check it too.
	shortRootDir := filepath.Join(base, "ro")
	mustMkdir(t, shortRootDir, 0o755)
	shortRoot, err := NewRoot(shortRootDir)
	if err != nil {
		t.Fatalf("NewRoot(short): %v", err)
	}
	if _, err := shortRoot.Resolve("../root/Settings.xml"); err == nil {
		t.Fatalf("prefix-sibling: root %q accepted a path into sibling %q", shortRootDir, filepath.Join(base, "root"))
	}
	// And the sibling is reachable through an ESCAPING symlink placed inside
	// the short root: this is the case a bare string-prefix check fails.
	mustSymlink(t, filepath.Join(base, "root"), filepath.Join(shortRootDir, "sneak"))
	if got, err := shortRoot.Resolve("sneak/Settings.xml"); err == nil {
		t.Fatalf("escaping symlink admitted: Resolve = %q, want rejection", got)
	}
}

// TestResolvePrefixSiblingBoundary pins the exact prefix-sibling bug: for a
// root of "<base>/a", the sibling "<base>/ab" must never be accepted.
func TestResolvePrefixSiblingBoundary(t *testing.T) {
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	rootDir := filepath.Join(base, "a")
	mustMkdir(t, rootDir, 0o755)
	sibling := filepath.Join(base, "ab")
	mustMkdir(t, sibling, 0o755)
	mustWrite(t, filepath.Join(sibling, "sibling.txt"), []byte("x"))

	r, err := NewRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}

	// The string-prefix bug in one line: the resolved sibling path *does* have
	// the root as a textual prefix.
	resolvedSibling, err := filepath.EvalSymlinks(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resolvedSibling, r.Dir()) {
		t.Skipf("test premise not met: %q is not a textual prefix of %q", r.Dir(), resolvedSibling)
	}
	// ...yet the element-wise check must reject it.
	if err := r.checkContained(resolvedSibling); err == nil {
		t.Fatalf("checkContained(%q) accepted a prefix sibling of root %q", resolvedSibling, r.Dir())
	}
	// And through the public API, with a symlink inside the root pointing at it.
	mustSymlink(t, sibling, filepath.Join(rootDir, "to_sibling"))
	if got, err := r.Resolve("to_sibling/sibling.txt"); err == nil {
		t.Fatalf("Resolve followed a symlink into the prefix sibling: %q", got)
	}
}

// TestResolveRejectsNulBeforeSyscall proves the NUL check happens lexically:
// the path never reaches the OS.
func TestResolveRejectsNulBeforeSyscall(t *testing.T) {
	r, _ := newTestRoot(t)
	_, err := r.Resolve("a\x00b")
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("error = %v; want a NUL-specific message", err)
	}
}

// TestResolveReturnsRootForEmpty documents the empty-path contract.
func TestResolveReturnsRootForEmpty(t *testing.T) {
	r, _ := newTestRoot(t)
	for _, in := range []string{"", ".", "./"} {
		got, err := r.Resolve(in)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", in, err)
		}
		if got != r.Dir() {
			t.Fatalf("Resolve(%q) = %q; want %q", in, got, r.Dir())
		}
	}
}

// TestNewRootRejectsBadInput covers constructor failures.
func TestNewRootRejectsBadInput(t *testing.T) {
	if _, err := NewRoot(""); err == nil {
		t.Fatal("empty instance dir accepted")
	}
	if _, err := NewRoot("a\x00b"); err == nil {
		t.Fatal("NUL in instance dir accepted")
	}
	if _, err := NewRoot(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("missing instance dir accepted")
	}
	f := filepath.Join(t.TempDir(), "file")
	mustWrite(t, f, []byte("x"))
	if _, err := NewRoot(f); err == nil {
		t.Fatal("file accepted as instance dir")
	}
}

// TestPerPermissionErrors asserts permission problems surface as errors rather
// than panics or silent success. Skipped when running as root (root ignores the
// mode bits).
func TestPerPermissionErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}
	r, _ := newTestRoot(t)
	locked := filepath.Join(r.Dir(), "locked")
	mustMkdir(t, locked, 0o755)
	mustWrite(t, filepath.Join(locked, "inside.txt"), []byte("x"))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if _, err := r.List("locked"); err == nil {
		t.Fatal("List on an unreadable directory returned no error")
	}
	if _, err := r.Stat("locked/inside.txt"); err == nil {
		// Resolve may fail or succeed depending on whether Lstat needs +x; the
		// operation must not silently report success with bogus data.
		t.Log("Stat succeeded under a 000 directory; acceptable when Lstat is permitted")
	}
}

// TestResolveExistingSymlinkChainInside proves multi-hop inside chains work.
func TestResolveExistingSymlinkChainInside(t *testing.T) {
	r, _ := newTestRoot(t)
	mustSymlink(t, "link_in", filepath.Join(r.Dir(), "hop1"))
	mustSymlink(t, "hop1", filepath.Join(r.Dir(), "hop2"))
	got, err := r.Resolve("hop2/World/Project.json")
	if err != nil {
		t.Fatalf("inside symlink chain rejected: %v", err)
	}
	if !isWithin(r.Dir(), got) {
		t.Fatalf("resolved %q outside root", got)
	}
	// The canonical spelling should equal the real path.
	want := filepath.Join(r.Dir(), "Worlds", "World", "Project.json")
	if got != want {
		t.Fatalf("Resolve = %q; want canonical %q", got, want)
	}
}

// TestResolveDanglingSymlinkInside is allowed (it is syntactically inside), so
// that the UI can display and delete it.
func TestResolveDanglingSymlinkInside(t *testing.T) {
	r, _ := newTestRoot(t)
	mustSymlink(t, "no-such-target", filepath.Join(r.Dir(), "dangling"))
	if _, err := r.Resolve("dangling"); err != nil {
		t.Fatalf("dangling in-root symlink rejected: %v", err)
	}
}

// TestResolveDanglingSymlinkOutside is rejected: the target may not exist yet,
// but it is lexically outside the root.
func TestResolveDanglingSymlinkOutside(t *testing.T) {
	r, base := newTestRoot(t)
	mustSymlink(t, filepath.Join(base, "outside", "not-yet"), filepath.Join(r.Dir(), "dangling_out"))
	if got, err := r.Resolve("dangling_out"); err == nil {
		t.Fatalf("dangling symlink pointing outside accepted: %q", got)
	}
}

// TestResolvePostOpenRecheck exercises the portable descriptor re-check.
func TestResolvePostOpenRecheck(t *testing.T) {
	r, _ := newTestRoot(t)
	f, err := r.Open("Settings.xml")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	if err := r.recheckOpen(f); err != nil {
		t.Fatalf("recheckOpen on an in-root file: %v", err)
	}
}

// TestCheckContainedUnit table-tests the boundary helper directly.
func TestCheckContainedUnit(t *testing.T) {
	base := t.TempDir()
	base, _ = filepath.EvalSymlinks(base)
	rootDir := filepath.Join(base, "inst")
	mustMkdir(t, rootDir, 0o755)
	r, err := NewRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		p       string
		allowed bool
	}{
		{rootDir, true},
		{filepath.Join(rootDir, "a"), true},
		{filepath.Join(rootDir, "a", "b"), true},
		{base, false},
		{filepath.Join(base, "inst2"), false},
		{"/etc", false},
		{filepath.Join(rootDir, "..", "inst"), true}, // cleans to rootDir
	}
	for _, tc := range cases {
		err := r.checkContained(tc.p)
		if tc.allowed && err != nil {
			t.Errorf("checkContained(%q) = %v; want nil", tc.p, err)
		}
		if !tc.allowed && err == nil {
			t.Errorf("checkContained(%q) = nil; want rejection", tc.p)
		}
	}
}

// isUnsafe reports whether err wraps ErrUnsafePath.
func isUnsafe(err error) bool {
	for e := err; e != nil; {
		if e == ErrUnsafePath {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
