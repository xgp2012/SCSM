package files

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zipEntry describes one entry to place in a hostile archive.
type zipEntry struct {
	name   string
	body   string
	mode   os.FileMode
	isDir  bool
	method uint16
}

// buildZip writes a zip with exactly the entries given, including entries that
// the archive/zip writer would normally refuse to create (absolute names,
// ".." components, symlinks). It is deliberately low-level so the tests can
// construct archives that a real attacker would produce with a hand-rolled
// writer or the `zip` CLI.
func buildZip(t *testing.T, path string, entries []zipEntry) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: e.method}
		if e.method == 0 {
			hdr.Method = zip.Deflate
		}
		if e.isDir || strings.HasSuffix(e.name, "/") {
			hdr.SetMode(os.ModeDir | 0o755)
		} else if e.mode != 0 {
			hdr.SetMode(e.mode)
		} else {
			hdr.SetMode(0o644)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create zip header %q: %v", e.name, err)
		}
		if e.body != "" {
			if _, err := io.WriteString(w, e.body); err != nil {
				t.Fatalf("write zip entry %q: %v", e.name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip file: %v", err)
	}
}

// TestUnzipAttacks is the core zip-slip test. It builds one archive containing
// every classic attack and asserts that NOTHING lands outside the destination.
func TestUnzipAttacks(t *testing.T) {
	r, base := newTestRoot(t)
	dest := filepath.Join(r.Dir(), "extract")
	mustMkdir(t, dest, 0o755)

	// Canaries that must survive: these are what a successful zip-slip would
	// overwrite.
	outsideFile := filepath.Join(base, "outside", "secret.txt")
	rootFile := filepath.Join(r.Dir(), "Settings.xml")
	parentFile := filepath.Join(base, "canary-parent.txt")
	mustWrite(t, parentFile, []byte("canary"))
	rootCanary := filepath.Join(r.Dir(), "canary-root.txt")
	mustWrite(t, rootCanary, []byte("canary"))

	zipPath := filepath.Join(base, "attack.zip")
	buildZip(t, zipPath, []zipEntry{
		{name: "../evil.txt", body: "evil-traversal"},
		{name: "a/../../evil2.txt", body: "evil-traversal-2"},
		{name: "a/b/../../../evil3.txt", body: "evil-traversal-3"},
		{name: "/abs/evil.txt", body: "evil-absolute"},
		{name: "../../outside/secret.txt", body: "OVERWRITTEN"},
		{name: "../../canary-parent.txt", body: "OVERWRITTEN"},
		{name: "../canary-root.txt", body: "OVERWRITTEN"},
		{name: "good.txt", body: "legitimate"},
		{name: "sub/good2.txt", body: "legitimate-2"},
		// Symlink entry: must never be created.
		{name: "link_to_etc", body: "/etc/passwd", mode: os.ModeSymlink | 0o777},
		// An entry that writes THROUGH a previously created symlink.
		{name: "through_link", body: "x"},
		// NUL byte in the name.
		{name: "nul\x00evil.txt", body: "evil-nul"},
		// Windows separators.
		{name: `..\..\win-evil.txt`, body: "evil-win"},
		{name: `C:\abs-evil.txt`, body: "evil-drive"},
		// Deeply nested traversal hidden behind a directory entry.
		{name: "okdir/", isDir: true},
		{name: "okdir/../../../evil4.txt", body: "evil-traversal-4"},
	})

	res, err := r.Unzip(zipPath, "extract", UnzipOptions{})
	if err != nil {
		t.Fatalf("Unzip returned a hard error (it should report skips instead): %v", err)
	}

	// The good entries must be there.
	goodGot, err := os.ReadFile(filepath.Join(dest, "good.txt"))
	if err != nil || string(goodGot) != "legitimate" {
		t.Fatalf("legitimate entry was not extracted: %v (%q)", err, goodGot)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "sub", "good2.txt")); err != nil || string(b) != "legitimate-2" {
		t.Fatalf("legitimate nested entry was not extracted: %v", err)
	}

	// The canaries must be untouched.
	if b, _ := os.ReadFile(outsideFile); string(b) != "secret" {
		t.Fatalf("zip-slip overwrote a file outside the root: %q", b)
	}
	if b, _ := os.ReadFile(parentFile); string(b) != "canary" {
		t.Fatalf("zip-slip overwrote a file outside the instance: %q", b)
	}
	if b, _ := os.ReadFile(rootFile); string(b) != "<Settings/>" {
		t.Fatalf("zip-slip overwrote a file in the instance root: %q", b)
	}
	if b, _ := os.ReadFile(rootCanary); string(b) != "canary" {
		t.Fatalf("zip-slip overwrote a canary in the instance root: %q", b)
	}

	// No symlink was created.
	if fi, err := os.Lstat(filepath.Join(dest, "link_to_etc")); err == nil {
		t.Fatalf("symlink entry was materialised: %v", fi.Mode())
	}

	// Nothing escaped the destination: walk the whole temp tree and check that
	// no unexpected file appeared.
	walkAndAssertNoStrays(t, base, dest, []string{"evil", "abs-evil", "win-evil", "OVERWRITTEN"})

	// The result must account for every entry.
	// Every entry must be accounted for: extracted + directories + skipped.
	// (The exact fixture count depends on how archive/zip coalesces the two
	// "okdir/" directory entries, so assert the invariant rather than a
	// brittle constant.)
	if accounted := len(res.Extracted) + len(res.Directories) + len(res.Skipped); accounted != res.Entries {
		t.Errorf("accounted for %d of %d entries: extracted=%v dirs=%v skipped=%d",
			accounted, res.Entries, res.Extracted, res.Directories, len(res.Skipped))
	}
	if len(res.Extracted) != 3 {
		t.Errorf("Extracted = %v; want exactly the three benign files", res.Extracted)
	}
	// The benign set must be exactly these.
	wantExtracted := map[string]bool{"good.txt": true, "sub/good2.txt": true, "through_link": true}
	for _, e := range res.Extracted {
		if !wantExtracted[e] {
			t.Errorf("unexpected file extracted: %q", e)
		}
	}
	reasons := map[string]int{}
	for _, s := range res.Skipped {
		reasons[s.Reason]++
	}
	for _, want := range []string{ReasonTraversal, ReasonAbsolute, ReasonSymlink, ReasonUnsafe} {
		if reasons[want] == 0 {
			t.Errorf("no entry was skipped with reason %q; skips were %+v", want, res.Skipped)
		}
	}
	t.Logf("skips: %+v", res.Skipped)
}

// walkAndAssertNoStrays fails if any file under base (outside dest) contains one
// of the poison markers.
func walkAndAssertNoStrays(t *testing.T, base, dest string, markers []string) {
	t.Helper()
	err := filepath.Walk(base, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		if strings.HasPrefix(rel, "outside") || strings.HasSuffix(rel, "attack.zip") {
			return nil
		}
		isInDest := strings.HasPrefix(p, dest+string(filepath.Separator)) || p == dest
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		for _, m := range markers {
			if bytes.Contains(b, []byte(m)) {
				t.Errorf("poison marker %q found in %s (inside dest: %v)", m, p, isInDest)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestUnzipAbsoluteAndNulTargets pins the distinction between skip reasons.
func TestUnzipAbsoluteAndNulTargets(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "cases.zip")
	buildZip(t, zipPath, []zipEntry{
		{name: "/etc/evil", body: "x"},
		{name: `C:\windows\evil`, body: "x"},
		{name: "nul\x00name", body: "x"},
		{name: "../up", body: "x"},
		{name: "ok.txt", body: "fine"},
	})
	res, err := r.Unzip(zipPath, "out", UnzipOptions{})
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if len(res.Extracted) != 1 || res.Extracted[0] != "ok.txt" {
		t.Fatalf("Extracted = %v; want [ok.txt]", res.Extracted)
	}
	if len(res.Skipped) != 4 {
		t.Fatalf("Skipped = %+v; want 4 entries", res.Skipped)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "etc")); !os.IsNotExist(err) {
		t.Fatal("an absolute entry created a path under the destination")
	}
}

// TestUnzipDuplicateNames proves the second occurrence of a name cannot
// overwrite the first one's validated target.
func TestUnzipDuplicateNames(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "dup.zip")

	// Two entries with the same name but different content, plus a third that
	// reaches the same destination through a different spelling.
	buildZip(t, zipPath, []zipEntry{
		{name: "dup.txt", body: "first"},
		{name: "dup.txt", body: "second"},
		{name: "./dup.txt", body: "third"},
	})
	res, err := r.Unzip(zipPath, "out", UnzipOptions{})
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	got, rerr := os.ReadFile(filepath.Join(r.Dir(), "out", "dup.txt"))
	if rerr != nil {
		t.Fatalf("dup.txt missing: %v", rerr)
	}
	if string(got) != "first" {
		t.Fatalf("dup.txt = %q; want the FIRST entry to win", got)
	}
	if len(res.Extracted) != 1 {
		t.Fatalf("Extracted = %v; want one entry", res.Extracted)
	}
	dups := 0
	for _, s := range res.Skipped {
		if s.Reason == ReasonDuplicate {
			dups++
		}
	}
	if dups != 2 {
		t.Fatalf("duplicate skips = %d; want 2 (%+v)", dups, res.Skipped)
	}
}

// TestUnzipOverwriteSemantics covers the default "never clobber" behaviour and
// the explicit opt-in.
func TestUnzipOverwriteSemantics(t *testing.T) {
	r, base := newTestRoot(t)
	dest := filepath.Join(r.Dir(), "out")
	mustMkdir(t, dest, 0o755)
	mustWrite(t, filepath.Join(dest, "keep.txt"), []byte("original"))

	zipPath := filepath.Join(base, "ow.zip")
	buildZip(t, zipPath, []zipEntry{{name: "keep.txt", body: "REPLACED"}})

	res, err := r.Unzip(zipPath, "out", UnzipOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "keep.txt")); string(b) != "original" {
		t.Fatalf("existing file was clobbered without Overwrite: %q", b)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonExists {
		t.Fatalf("Skipped = %+v; want one %q", res.Skipped, ReasonExists)
	}

	res, err = r.Unzip(zipPath, "out", UnzipOptions{Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "keep.txt")); string(b) != "REPLACED" {
		t.Fatalf("Overwrite did not replace the file: %q", b)
	}
	if len(res.Extracted) != 1 {
		t.Fatalf("Extracted = %+v; want one", res.Extracted)
	}
}

// TestUnzipBombIsCapped proves the decompressed-size cap stops a zip bomb.
func TestUnzipBombIsCapped(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "bomb.zip")

	// 64 MiB of zeros compresses to a few KiB.
	big := make([]byte, 64<<20)
	buildZip(t, zipPath, []zipEntry{
		{name: "bomb.bin", body: string(big), method: zip.Deflate},
		{name: "small.txt", body: "tiny"},
	})

	// Cap the total at 1 MiB: the bomb must be skipped and the small file must
	// still be extracted (per-entry reporting, not a hard failure).
	pol := DefaultPolicy()
	pol.MaxUnzipSize = 1 << 20
	pol.MaxUnzipEntrySize = 1 << 20
	res, err := r.Unzip(zipPath, "out", UnzipOptions{Policy: &pol})
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "out", "bomb.bin")); !os.IsNotExist(err) {
		t.Fatal("a bomb entry exceeding the cap was written to disk")
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "out", "small.txt")); err != nil {
		t.Fatalf("the benign entry was not extracted: %v", err)
	}
	found := false
	for _, s := range res.Skipped {
		if s.Reason == ReasonTooLarge {
			found = true
		}
	}
	if !found {
		t.Fatalf("no too-large skip reported: %+v", res.Skipped)
	}
	if res.TotalBytes > 1<<20 {
		t.Fatalf("TotalBytes = %d exceeds the 1 MiB cap", res.TotalBytes)
	}
}

// TestUnzipEntryCountCap covers the entry-count limit.
func TestUnzipEntryCountCap(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "many.zip")
	entries := make([]zipEntry, 0, 40)
	for i := 0; i < 40; i++ {
		entries = append(entries, zipEntry{name: "f" + itoa(i) + ".txt", body: "x"})
	}
	buildZip(t, zipPath, entries)

	pol := DefaultPolicy()
	pol.MaxUnzipEntries = 10
	_, err := r.Unzip(zipPath, "out", UnzipOptions{Policy: &pol})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Unzip with too many entries = %v; want ErrTooLarge", err)
	}
}

// TestUnzipDepthCap covers the depth limit.
func TestUnzipDepthCap(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "deep.zip")
	deep := strings.Repeat("d/", 40) + "file.txt"
	buildZip(t, zipPath, []zipEntry{{name: deep, body: "x"}})

	pol := DefaultPolicy()
	pol.MaxUnzipDepth = 5
	res, err := r.Unzip(zipPath, "out", UnzipOptions{Policy: &pol})
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if len(res.Extracted) != 0 {
		t.Fatalf("a too-deep entry was extracted: %v", res.Extracted)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonTooDeep {
		t.Fatalf("Skipped = %+v; want one %q", res.Skipped, ReasonTooDeep)
	}
}

// TestUnzipStripComponents covers the wrapper-directory convenience.
func TestUnzipStripComponents(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "wrapped.zip")
	buildZip(t, zipPath, []zipEntry{
		{name: "MyPlugin/", isDir: true},
		{name: "MyPlugin/Plugin.dll", body: "dll"},
		{name: "MyPlugin/data.json", body: "{}"},
	})
	res, err := r.Unzip(zipPath, "Plugins", UnzipOptions{StripComponents: 1})
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(r.Dir(), "Plugins", "Plugin.dll")); err != nil || string(b) != "dll" {
		t.Fatalf("strip-components did not unwrap the archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "Plugins", "MyPlugin")); !os.IsNotExist(err) {
		t.Fatal("the wrapper directory was still created")
	}
	if len(res.Extracted) != 2 {
		t.Fatalf("Extracted = %v; want 2", res.Extracted)
	}

	// A traversal hidden inside a stripped archive must STILL be rejected:
	// "../evil" must not become "evil" after stripping.
	zipPath2 := filepath.Join(base, "wrapped-evil.zip")
	buildZip(t, zipPath2, []zipEntry{
		{name: "../evil.txt", body: "evil"},
		{name: "wrap/ok.txt", body: "ok"},
	})
	res2, err := r.Unzip(zipPath2, "Plugins2", UnzipOptions{StripComponents: 1})
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	for _, e := range res2.Extracted {
		if e == "evil.txt" {
			t.Fatal("strip-components laundered a traversal into a valid name")
		}
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "Plugins2", "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("traversal entry was extracted after stripping")
	}
}

// TestUnzipFilterNarrowsOnly proves a filter cannot widen access.
func TestUnzipFilterNarrowsOnly(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "filter.zip")
	buildZip(t, zipPath, []zipEntry{
		{name: "keep.txt", body: "k"},
		{name: "drop.txt", body: "d"},
		{name: "../escape.txt", body: "e"},
	})
	var seen []string
	res, err := r.Unzip(zipPath, "out", UnzipOptions{
		Filter: func(rel string, f *zip.File) error {
			seen = append(seen, rel)
			if strings.HasPrefix(rel, "drop") {
				return errors.New("not wanted")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Extracted) != 1 || res.Extracted[0] != "keep.txt" {
		t.Fatalf("Extracted = %v; want [keep.txt]", res.Extracted)
	}
	for _, s := range seen {
		if strings.Contains(s, "..") {
			t.Fatalf("the filter was offered an unsafe path %q (it must run after the checks)", s)
		}
	}
}

// TestUnzipDestinationEscapes covers a destination that is itself hostile.
func TestUnzipDestinationEscapes(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "ok.zip")
	buildZip(t, zipPath, []zipEntry{{name: "a.txt", body: "x"}})

	if _, err := r.Unzip(zipPath, "../outside", UnzipOptions{}); err == nil {
		t.Fatal("Unzip into an escaping destination succeeded")
	}
	if _, err := r.Unzip(zipPath, "link_out_dir", UnzipOptions{}); err == nil {
		t.Fatal("Unzip into a symlinked escaping destination succeeded")
	}
	// An in-root symlink destination is fine.
	if _, err := r.Unzip(zipPath, "link_in", UnzipOptions{}); err != nil {
		t.Fatalf("Unzip into an in-root symlink destination failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "Worlds", "a.txt")); err != nil {
		t.Fatalf("file did not land through the in-root symlink: %v", err)
	}
}

// TestUnzipDoesNotCreateSetuid proves permission bits are sanitised.
func TestUnzipDoesNotCreateSetuid(t *testing.T) {
	r, base := newTestRoot(t)
	zipPath := filepath.Join(base, "suid.zip")
	buildZip(t, zipPath, []zipEntry{
		{name: "evil", body: "#!/bin/sh\n", mode: 0o4755 | os.ModeSetuid},
	})
	if _, err := r.Unzip(zipPath, "out", UnzipOptions{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(r.Dir(), "out", "evil"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetuid != 0 {
		t.Fatalf("setuid bit survived extraction: %v", fi.Mode())
	}
	if fi.Mode().Perm()&0o022 != 0 {
		t.Fatalf("group/world-writable file created: %v", fi.Mode())
	}
}

// TestUnzipArchiveConvenience covers the in-root wrapper and the missing-file
// error.
func TestUnzipArchiveConvenience(t *testing.T) {
	r, base := newTestRoot(t)
	buildZip(t, filepath.Join(r.Dir(), "inner.zip"), []zipEntry{{name: "x.txt", body: "x"}})
	if _, err := r.UnzipArchive("inner.zip", "out", UnzipOptions{}); err != nil {
		t.Fatalf("UnzipArchive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "out", "x.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UnzipArchive("missing.zip", "out", UnzipOptions{}); err == nil {
		t.Fatal("UnzipArchive on a missing archive succeeded")
	}
	_ = base
}

// TestUnzipSymlinkEscapeViaExistingLink is the nastiest zip-slip variant: the
// archive relies on a symlink that ALREADY exists in the destination to redirect
// a write outside the root.
func TestUnzipSymlinkEscapeViaExistingLink(t *testing.T) {
	r, base := newTestRoot(t)
	dest := filepath.Join(r.Dir(), "out")
	mustMkdir(t, dest, 0o755)

	// A pre-existing symlink inside the destination pointing outside, as if a
	// previous archive (or an attacker with file access) planted it.
	planted := filepath.Join(base, "planted")
	mustMkdir(t, planted, 0o755)
	mustSymlink(t, planted, filepath.Join(dest, "escape"))

	zipPath := filepath.Join(base, "through.zip")
	buildZip(t, zipPath, []zipEntry{
		{name: "escape/pwned.txt", body: "PWNED"},
		{name: "safe.txt", body: "safe"},
	})

	res, err := r.Unzip(zipPath, "out", UnzipOptions{})
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(planted, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatal("zip entry was written THROUGH a planted symlink to outside the root")
	}
	if _, err := os.Stat(filepath.Join(dest, "safe.txt")); err != nil {
		t.Fatalf("the safe entry was not extracted: %v", err)
	}
	foundSkip := false
	for _, s := range res.Skipped {
		if s.Reason == ReasonTraversal || s.Reason == ReasonSymlink {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("expected a traversal/symlink skip, got %+v", res.Skipped)
	}
}

// TestUnzipOverwritesPlantedSymlinkWithRealFile proves the last-component link
// is unlinked rather than followed.
func TestUnzipOverwritesPlantedSymlinkWithRealFile(t *testing.T) {
	r, base := newTestRoot(t)
	dest := filepath.Join(r.Dir(), "out")
	mustMkdir(t, dest, 0o755)

	outside := filepath.Join(base, "outside", "secret.txt")
	before, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	// The archive wants to write "secret.txt"; a link with that name already
	// points at a file outside the root.
	mustSymlink(t, outside, filepath.Join(dest, "secret.txt"))

	zipPath := filepath.Join(base, "over-link.zip")
	buildZip(t, zipPath, []zipEntry{{name: "secret.txt", body: "NEW"}})

	if _, err := r.Unzip(zipPath, "out", UnzipOptions{Overwrite: true}); err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	after, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the write followed a planted symlink and modified a file outside the root: %q -> %q", before, after)
	}
	// The link must have been replaced by a regular file with the new content.
	fi, err := os.Lstat(filepath.Join(dest, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the symlink was left in place instead of being replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "secret.txt")); string(b) != "NEW" {
		t.Fatalf("content = %q; want NEW", b)
	}
}
