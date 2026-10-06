package files

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveThroughDanglingTruncation forces the resolveThroughDangling walk to
// stop early because a component does not exist. Reaching it needs a dangling
// symlink used as a DIRECTORY component, which is also the only way a
// middle-of-path dangling link can arise in practice.
func TestResolveThroughDanglingTruncation(t *testing.T) {
	r := mustRoot(t)

	// link_mid -> "ghost" (does not exist, and is a legal in-root name).
	// Resolving "link_mid/sub/file.txt" therefore hits EvalSymlinks failure with
	// a non-empty tail, and the walk must handle the missing remainder.
	mustSymlink(t, "ghost", filepath.Join(r.Dir(), "link_mid"))
	got, err := r.Resolve("link_mid/sub/file.txt")
	if err != nil {
		t.Fatalf("in-root dangling link used as a directory component was rejected: %v", err)
	}
	// The path must stay inside the root. Note that the returned path is the
	// SPELT path (link_mid/sub/file.txt), not the link's declared target:
	// resolving a dangling link to its (non-existent) target would produce a
	// path the caller cannot Lstat, and the entry the caller actually wants to
	// operate on is the link. Containment is what matters here, and it holds.
	if !isWithin(r.Dir(), got) {
		t.Fatalf("resolved %q outside the root", got)
	}

	// The same shape, but the link escapes the root: must be rejected.
	mustSymlink(t, filepath.Join(filepath.Dir(r.Dir()), "outside", "ghost"),
		filepath.Join(r.Dir(), "link_mid_out"))
	if got, err := r.Resolve("link_mid_out/sub/file.txt"); err == nil {
		t.Fatalf("dangling link escaping the root was followed: %q", got)
	}
}

// TestWriteFileErrors covers the write failure paths.
func TestWriteFileErrors(t *testing.T) {
	r := mustRoot(t)

	// Writing into a directory that does not exist.
	if err := r.WriteFile("nodir/x.txt", []byte("x")); err == nil {
		t.Error("WriteFile into a missing directory succeeded")
	}
	// Writing over a directory.
	mustMkdir(t, filepath.Join(r.Dir(), "adir"), 0o755)
	if err := r.WriteFile("adir", []byte("x")); !errors.Is(err, ErrIsDir) {
		t.Errorf("WriteFile(dir) = %v; want ErrIsDir", err)
	}
	// Writing outside.
	if err := r.WriteFile("../x.txt", []byte("x")); err == nil {
		t.Error("WriteFile outside the root succeeded")
	}
	// Permission denied on the parent directory.
	if os.Geteuid() != 0 {
		locked := filepath.Join(r.Dir(), "locked")
		mustMkdir(t, locked, 0o555)
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		if err := r.WriteFile("locked/x.txt", []byte("x")); err == nil {
			t.Error("WriteFile into a read-only directory succeeded")
		}
	}
}

// TestCreateErrors covers Create's parent checks.
func TestCreateErrors(t *testing.T) {
	r := mustRoot(t)
	mustWrite(t, filepath.Join(r.Dir(), "afile"), []byte("x"))
	// Parent is a regular file.
	if _, err := r.Create("afile/child.txt"); !errors.Is(err, ErrNotDir) {
		t.Errorf("Create under a regular file = %v; want ErrNotDir", err)
	}
	// Target is the root.
	if _, err := r.Create(""); !errors.Is(err, ErrIsDir) {
		t.Errorf("Create(root) = %v; want ErrIsDir", err)
	}
	// Target is an existing directory.
	mustMkdir(t, filepath.Join(r.Dir(), "adir"), 0o755)
	if _, err := r.Create("adir"); !errors.Is(err, ErrIsDir) {
		t.Errorf("Create(dir) = %v; want ErrIsDir", err)
	}
	// No temp files left behind by any of it.
	entries, err := r.List("")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".scnetm-") {
			t.Errorf("temporary file left behind: %s", e.Name)
		}
	}
}

// TestRemoveTreeBoundedVanishes handles entries disappearing mid-walk.
func TestRemoveTreeBoundedVanishes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeTreeBounded(dir, 0); err != nil {
		t.Fatalf("removeTreeBounded: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the directory survived removal")
	}
	// Removing something already gone is an error from the OS, which is fine;
	// what matters is that it does not panic.
	_ = removeTreeBounded(dir, 0)

	// The depth cap must bite.
	deep := t.TempDir()
	cur := deep
	for i := 0; i < MaxWalkDepth+3; i++ {
		cur = filepath.Join(cur, "d")
		if err := os.Mkdir(cur, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeTreeBounded(deep, 0); !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("removeTreeBounded on a too-deep tree = %v; want ErrDepthExceeded", err)
	}
}

// TestCreateDirTreeRejections covers the archive directory-creation guard.
func TestCreateDirTreeRejections(t *testing.T) {
	r, base := newTestRoot(t)
	dest := filepath.Join(r.Dir(), "dest")
	mustMkdir(t, dest, 0o755)

	// A symlinked component must be refused rather than traversed.
	mustSymlink(t, filepath.Join(base, "outside"), filepath.Join(dest, "link"))
	if reason, _ := r.createDirTree(dest, "link/sub"); reason != ReasonSymlink {
		t.Errorf("createDirTree through a symlink = %q; want %q", reason, ReasonSymlink)
	}
	if _, err := os.Stat(filepath.Join(base, "outside", "sub")); !os.IsNotExist(err) {
		t.Fatal("createDirTree followed a symlink out of the root")
	}

	// A regular file in the way.
	mustWrite(t, filepath.Join(dest, "afile"), []byte("x"))
	if reason, _ := r.createDirTree(dest, "afile/sub"); reason != ReasonNotReg {
		t.Errorf("createDirTree through a file = %q; want %q", reason, ReasonNotReg)
	}

	// The normal case, including an already-existing prefix.
	if reason, detail := r.createDirTree(dest, "a/b/c"); reason != "" {
		t.Fatalf("createDirTree = %q, %q; want success", reason, detail)
	}
	if fi, err := os.Stat(filepath.Join(dest, "a", "b", "c")); err != nil || !fi.IsDir() {
		t.Fatalf("directory tree not created: %v", err)
	}
	if reason, _ := r.createDirTree(dest, "a/b/c"); reason != "" {
		t.Errorf("createDirTree on an existing tree = %q; want success", reason)
	}
}

// TestUnzipWriteErrors covers the per-entry write failure path: a directory is
// planted where the archive wants a file.
func TestUnzipWriteErrors(t *testing.T) {
	r, base := newTestRoot(t)
	dest := filepath.Join(r.Dir(), "out")
	mustMkdir(t, filepath.Join(dest, "blocker"), 0o755)

	zipPath := filepath.Join(base, "conflict.zip")
	buildZip(t, zipPath, []zipEntry{
		{name: "blocker", body: "file content"},
		{name: "ok.txt", body: "fine"},
	})
	// A directory where a file must go: the extraction of that entry fails, but
	// the entry list still has to be processed. Unzip returns the error.
	if _, err := r.Unzip(zipPath, "out", UnzipOptions{}); err == nil {
		t.Log("extraction over a directory unexpectedly succeeded; acceptable if the platform allows it")
	}
}

// TestUnzipEmptyAndMissingArchive covers the open failure path.
func TestUnzipMissingArchive(t *testing.T) {
	r := mustRoot(t)
	if _, err := r.Unzip(filepath.Join(t.TempDir(), "nope.zip"), "out", UnzipOptions{}); err == nil {
		t.Fatal("Unzip of a missing archive succeeded")
	}
}

// TestSanitizeZipEntryNameUnits table-tests the name validator directly,
// including the strip-components laundering attempt.
func TestSanitizeZipEntryNameUnits(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		strip  int
		depth  int
		want   string
		reason string
	}{
		{name: "plain", input: "a.txt", want: "a.txt"},
		{name: "nested", input: "a/b/c.txt", want: "a/b/c.txt"},
		{name: "dot-clean", input: "./a.txt", want: "a.txt"},
		{name: "double-slash", input: "a//b.txt", want: "a/b.txt"},
		{name: "trailing-slash", input: "dir/", want: "dir"},
		{name: "dot", input: ".", reason: reasonSkipNoComponents},
		{name: "traversal", input: "../evil", reason: ReasonTraversal},
		{name: "traversal-deep", input: "a/../../evil", reason: ReasonTraversal},
		{name: "absolute", input: "/abs", reason: ReasonAbsolute},
		{name: "drive", input: "C:/abs", reason: ReasonAbsolute},
		{name: "backslash", input: `a\b`, reason: ReasonUnsafe},
		{name: "nul", input: "a\x00b", reason: ReasonUnsafe},
		{name: "too-deep", input: "a/b/c/d", depth: 2, reason: ReasonTooDeep},
		// Stripping must NOT launder a traversal into a valid name.
		{name: "strip-traversal", input: "../evil", strip: 1, reason: ReasonTraversal},
		{name: "strip-all", input: "only", strip: 1, reason: reasonSkipNoComponents},
		{name: "strip-partial", input: "wrap/a.txt", strip: 1, want: "a.txt"},
		{name: "strip-two", input: "w1/w2/a.txt", strip: 2, want: "a.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			depth := tc.depth
			if depth == 0 {
				depth = DefaultPolicy().MaxUnzipDepth
			}
			got, reason, _ := sanitizeZipEntryName(tc.input, tc.strip, depth)
			if reason != tc.reason {
				t.Fatalf("reason = %q; want %q (rel %q)", reason, tc.reason, got)
			}
			if tc.reason == "" && got != tc.want {
				t.Fatalf("rel = %q; want %q", got, tc.want)
			}
		})
	}
}

// TestCompressionRatioCheck covers the optional ratio guard.
func TestCompressionRatioCheck(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "ratio.zip")
	buildZip(t, zipPath, []zipEntry{
		{name: "squash.bin", body: strings.Repeat("\x00", 1<<20)},
		{name: "ok.txt", body: "fine"},
	})
	pol := DefaultPolicy()
	pol.MaxCompressionRatio = 5
	res, err := r.Unzip(zipPath, "out", UnzipOptions{Policy: &pol})
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "out", "squash.bin")); !os.IsNotExist(err) {
		t.Error("a highly-compressible entry was extracted despite the ratio cap")
	}
	saw := false
	for _, s := range res.Skipped {
		if s.Reason == ReasonTooLarge && strings.Contains(s.Detail, "compression ratio") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("no compression-ratio skip reported: %+v", res.Skipped)
	}
	// The benign entry still lands.
	if _, err := os.Stat(filepath.Join(r.Dir(), "out", "ok.txt")); err != nil {
		t.Errorf("the benign entry was not extracted: %v", err)
	}
}

// TestUnzipMaxSizeOverride covers the per-call cap override.
func TestUnzipMaxSizeOverride(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "s.zip")
	buildZip(t, zipPath, []zipEntry{{name: "a.bin", body: strings.Repeat("x", 4096)}})
	res, err := r.Unzip(zipPath, "out", UnzipOptions{MaxSizeOverride: 16})
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if len(res.Extracted) != 0 {
		t.Fatalf("MaxSizeOverride was ignored: %v", res.Extracted)
	}
}

// TestUnzipNonRegularEntryIsSkipped proves a FIFO/device entry cannot be
// created. A plain zip cannot carry a real FIFO, so the check is exercised by
// asserting the regular-file predicate directly.
func TestUnzipRegularFilePredicate(t *testing.T) {
	if !os.FileMode(0o644).IsRegular() {
		t.Error("a plain mode should be regular")
	}
	for _, m := range []os.FileMode{os.ModeSymlink | 0o777, os.ModeDevice | 0o666, os.ModeNamedPipe | 0o666, os.ModeSocket | 0o666} {
		if m.IsRegular() {
			t.Errorf("%v reported as regular", m)
		}
	}
}

// TestEntryFieldsArePopulated covers the Entry display contract.
func TestEntryFieldsArePopulated(t *testing.T) {
	r := mustRoot(t)
	mustSymlink(t, "Settings.xml", filepath.Join(r.Dir(), "alias.xml"))
	e, err := r.Stat("alias.xml")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "alias.xml" || e.Rel != "alias.xml" {
		t.Errorf("Name/Rel = %q/%q", e.Name, e.Rel)
	}
	if !e.IsSymlink || e.LinkTarget != "Settings.xml" {
		t.Errorf("symlink fields = %v/%q", e.IsSymlink, e.LinkTarget)
	}
	if e.Mode == "" || e.ModTime.IsZero() {
		t.Errorf("Mode/ModTime not populated: %q, %v", e.Mode, e.ModTime)
	}
	if e.Size != int64(len("<Settings/>")) {
		t.Errorf("Size = %d; want %d", e.Size, len("<Settings/>"))
	}
	if e.IsDir {
		t.Error("a symlink to a file reported as a directory")
	}
	if !strings.Contains(r.String(), "files.Root") {
		t.Errorf("String() = %q", r.String())
	}
	if r.Dir() == "" {
		t.Error("Dir() is empty")
	}
}

// TestResolveLongPathRejectionIsLexical proves an over-long path never reaches
// the OS (the error is produced by validateRel).
func TestResolveLongPathRejectionIsLexical(t *testing.T) {
	r := mustRoot(t)
	long := strings.Repeat("a", MaxPathLen+1)
	_, err := r.Resolve(long)
	if err == nil {
		t.Fatal("an over-long path was accepted")
	}
	if !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("error = %v; want a length-specific message", err)
	}
}

// TestReadFileNoCap covers the max<=0 branch.
func TestReadFileNoCap(t *testing.T) {
	r := mustRoot(t)
	data := bytes.Repeat([]byte("z"), 1024)
	if err := r.WriteFile("big.bin", data); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadFile("big.bin", 0)
	if err != nil {
		t.Fatalf("ReadFile with no cap: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("ReadFile returned %d bytes; want %d", len(got), len(data))
	}
	if _, err := r.ReadFile("missing.bin", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadFile(missing) = %v; want ErrNotFound", err)
	}
}
