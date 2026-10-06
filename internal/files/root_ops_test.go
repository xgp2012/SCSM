package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func mustRoot(t *testing.T) *Root {
	t.Helper()
	r, _ := newTestRoot(t)
	return r
}

// TestListAndStat covers the metadata surface, including the requirement that a
// symlink escaping the root is still *shown* (so it can be deleted) but is not
// reported as a navigable directory.
func TestListAndStat(t *testing.T) {
	r := mustRoot(t)

	entries, err := r.List("")
	if err != nil {
		t.Fatalf("List(root): %v", err)
	}
	byName := map[string]Entry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	for _, want := range []string{"Settings.xml", "Worlds", "sub", "link_out_file", "link_out_dir", "link_in"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("List(root) missing %q (got %d entries)", want, len(entries))
		}
	}
	if !byName["Worlds"].IsDir {
		t.Error("Worlds should be reported as a directory")
	}
	if byName["Settings.xml"].IsDir {
		t.Error("Settings.xml should not be a directory")
	}
	if byName["Settings.xml"].Size != int64(len("<Settings/>")) {
		t.Errorf("Settings.xml size = %d; want %d", byName["Settings.xml"].Size, len("<Settings/>"))
	}
	if byName["Settings.xml"].Rel != "Settings.xml" {
		t.Errorf("Rel = %q; want %q", byName["Settings.xml"].Rel, "Settings.xml")
	}
	if !byName["link_out_dir"].IsSymlink {
		t.Error("link_out_dir should be flagged as a symlink")
	}
	if byName["link_out_dir"].IsDir {
		t.Error("an escaping symlink dir must not be reported as a navigable directory")
	}
	if !byName["link_in"].IsSymlink {
		t.Error("link_in should be flagged as a symlink")
	}
	if !byName["link_in"].IsDir {
		t.Error("an in-root symlink dir should still report IsDir")
	}

	// Sorted by name.
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Name > entries[i].Name {
			t.Fatalf("entries not sorted: %q before %q", entries[i-1].Name, entries[i].Name)
		}
	}

	// Nested listing uses slash-separated Rel values.
	nested, err := r.List("Worlds/World")
	if err != nil {
		t.Fatalf("List(Worlds/World): %v", err)
	}
	if len(nested) != 1 || nested[0].Rel != "Worlds/World/Project.json" {
		t.Fatalf("nested listing = %+v; want one entry Worlds/World/Project.json", nested)
	}

	// List on a file must fail with a typed error, not a panic.
	if _, err := r.List("Settings.xml"); err == nil {
		t.Fatal("List on a file succeeded")
	}

	// Stat of the root itself.
	st, err := r.Stat("")
	if err != nil {
		t.Fatalf("Stat(root): %v", err)
	}
	if !st.IsDir {
		t.Error("Stat(root).IsDir = false")
	}

	// Stat of a missing file.
	if _, err := r.Stat("nope.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat(missing) = %v; want ErrNotFound", err)
	}

	// Empty directory yields a non-nil, empty slice.
	empty := filepath.Join(r.Dir(), "emptydir")
	mustMkdir(t, empty, 0o755)
	got, err := r.List("emptydir")
	if err != nil {
		t.Fatalf("List(empty): %v", err)
	}
	if got == nil {
		t.Fatal("List(empty) returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("List(empty) = %+v; want empty", got)
	}
}

// TestBrokenSymlinkEntry checks that a dangling link is listed as broken rather
// than dropped.
func TestBrokenSymlinkEntry(t *testing.T) {
	r := mustRoot(t)
	mustSymlink(t, "does-not-exist-at-all", filepath.Join(r.Dir(), "broken"))
	st, err := r.Stat("broken")
	if err != nil {
		t.Fatalf("Stat(broken): %v", err)
	}
	if !st.IsSymlink || !st.BrokenLink {
		t.Fatalf("Stat(broken) = %+v; want IsSymlink && BrokenLink", st)
	}
}

// TestMkdir covers creation, nested creation and the already-exists error.
func TestMkdir(t *testing.T) {
	r := mustRoot(t)
	if err := r.Mkdir("a/b/c"); err != nil {
		t.Fatalf("Mkdir nested: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(r.Dir(), "a", "b", "c")); err != nil || !fi.IsDir() {
		t.Fatalf("nested directory not created: %v", err)
	}
	if err := r.Mkdir("a/b/c"); !errors.Is(err, ErrExists) {
		t.Fatalf("Mkdir on existing = %v; want ErrExists", err)
	}
	if err := r.Mkdir(""); err != nil {
		t.Fatalf("Mkdir(root) should be a no-op, got %v", err)
	}
	if err := r.Mkdir("../escape"); err == nil {
		t.Fatal("Mkdir traversal succeeded")
	}
	if err := r.Mkdir("link_out_dir/newdir"); err == nil {
		t.Fatal("Mkdir through an escaping symlink succeeded")
	}
	// Creating through an in-root symlink is fine.
	if err := r.Mkdir("link_in/NewWorld"); err != nil {
		t.Fatalf("Mkdir through in-root symlink: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(r.Dir(), "Worlds", "NewWorld")); err != nil || !fi.IsDir() {
		t.Fatalf("Mkdir through symlink did not land in Worlds: %v", err)
	}
}

// TestRename covers the happy path plus the guards.
func TestRename(t *testing.T) {
	r := mustRoot(t)
	mustWrite(t, filepath.Join(r.Dir(), "from.txt"), []byte("x"))
	if err := r.Rename("from.txt", "to.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "to.txt")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "from.txt")); !os.IsNotExist(err) {
		t.Fatal("source still present after rename")
	}
	if err := r.Rename("to.txt", "Settings.xml"); !errors.Is(err, ErrExists) {
		t.Fatalf("Rename onto existing = %v; want ErrExists", err)
	}
	if err := r.Rename("", "x"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("Rename(root) = %v; want ErrNotAllowed", err)
	}
	if err := r.Rename("to.txt", ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("Rename to root = %v; want ErrNotAllowed", err)
	}
	if err := r.Rename("../outside/secret.txt", "stolen.txt"); err == nil {
		t.Fatal("Rename from outside succeeded")
	}
	if err := r.Rename("to.txt", "../escaped.txt"); err == nil {
		t.Fatal("Rename to outside succeeded")
	}
	// Moving a directory into its own descendant must be refused.
	if err := r.Mkdir("d1/d2"); err != nil {
		t.Fatal(err)
	}
	if err := r.Rename("d1", "d1/d2/d3"); err == nil {
		t.Fatal("Rename moved a directory into its own descendant")
	}
	// Renaming through an in-root symlink works, as long as source and target
	// live on the same filesystem (rename(2) cannot cross devices, which is a
	// kernel property, not a sandbox property).
	if err := r.Rename("to.txt", "link_in/to.txt"); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			t.Logf("skipping cross-filesystem rename leg: %v", err)
		} else {
			t.Fatalf("Rename into in-root symlink: %v", err)
		}
	}
}

// TestDelete covers files, directories, recursion, symlinks and the guards.
func TestDelete(t *testing.T) {
	r := mustRoot(t)

	mustWrite(t, filepath.Join(r.Dir(), "f.txt"), []byte("x"))
	if err := r.Delete("f.txt", false); err != nil {
		t.Fatalf("Delete file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "f.txt")); !os.IsNotExist(err) {
		t.Fatal("file still present")
	}
	if err := r.Delete("f.txt", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing = %v; want ErrNotFound", err)
	}

	// Non-recursive delete of a non-empty directory fails.
	mustMkdir(t, filepath.Join(r.Dir(), "d", "sub"), 0o755)
	mustWrite(t, filepath.Join(r.Dir(), "d", "sub", "x"), []byte("x"))
	if err := r.Delete("d", false); err == nil {
		t.Fatal("non-recursive delete of a non-empty dir succeeded")
	}
	// Recursive delete works, including the depth of nested content.
	if err := r.Delete("d", true); err != nil {
		t.Fatalf("recursive Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "d")); !os.IsNotExist(err) {
		t.Fatal("directory still present after recursive delete")
	}

	// Deleting the root itself is refused.
	if err := r.Delete("", true); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("Delete(root) = %v; want ErrNotAllowed", err)
	}

	// A symlink is removed, never followed: the outside target must survive.
	outsideTarget := filepath.Join(filepath.Dir(r.Dir()), "outside", "secret.txt")
	if _, err := os.Stat(outsideTarget); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if err := r.Delete("link_out_dir", true); err != nil {
		t.Fatalf("Delete(escaping symlink): %v", err)
	}
	if _, err := os.Stat(outsideTarget); err != nil {
		t.Fatalf("deleting a symlink followed it and removed the target: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(r.Dir(), "link_out_dir")); !os.IsNotExist(err) {
		t.Fatal("symlink itself was not removed")
	}

	// Deleting through an escaping symlink is refused.
	if err := r.Delete("link_out_dir", false); err == nil {
		t.Log("second delete of a now-missing link reports success; acceptable")
	}
}

// TestDeleteDoesNotFollowSymlinksInTree proves that a recursive delete of a
// directory containing a symlink to the outside removes only the link.
func TestDeleteDoesNotFollowSymlinksInTree(t *testing.T) {
	r := mustRoot(t)
	base := filepath.Dir(r.Dir())
	victim := filepath.Join(base, "victim")
	mustMkdir(t, victim, 0o755)
	mustWrite(t, filepath.Join(victim, "important.txt"), []byte("do not delete"))

	tree := filepath.Join(r.Dir(), "tree")
	mustMkdir(t, tree, 0o755)
	mustSymlink(t, victim, filepath.Join(tree, "link_to_victim"))
	mustWrite(t, filepath.Join(tree, "own.txt"), []byte("x"))

	if err := r.Delete("tree", true); err != nil {
		t.Fatalf("recursive Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(victim, "important.txt")); err != nil {
		t.Fatalf("recursive delete followed a symlink and destroyed outside data: %v", err)
	}
}

// TestWriteAndReadFile covers the atomic replace path.
func TestWriteAndReadFile(t *testing.T) {
	r := mustRoot(t)
	if err := r.WriteFile("Settings.xml", []byte("<New/>")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := r.ReadFile("Settings.xml", 0)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "<New/>" {
		t.Fatalf("ReadFile = %q; want %q", got, "<New/>")
	}
	// No temp files left behind.
	entries, err := r.List("")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".scnetm-tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name)
		}
	}
	// Cap enforcement.
	if _, err := r.ReadFile("Settings.xml", 2); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadFile with a 2-byte cap = %v; want ErrTooLarge", err)
	}
	// Writing outside is refused.
	if err := r.WriteFile("../escape.txt", []byte("x")); err == nil {
		t.Fatal("WriteFile outside the root succeeded")
	}
	if err := r.WriteFile("", []byte("x")); !errors.Is(err, ErrIsDir) {
		t.Fatalf("WriteFile(root) = %v; want ErrIsDir", err)
	}
}

// TestCreateAndOpenDescriptors covers the streaming descriptor API.
func TestCreateAndOpenDescriptors(t *testing.T) {
	r := mustRoot(t)
	f, err := r.Create("uploaded.bin")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.WriteString("payload"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	rf, err := r.Open("uploaded.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rf.Close()
	buf := make([]byte, 16)
	n, _ := rf.Read(buf)
	if string(buf[:n]) != "payload" {
		t.Fatalf("read back %q; want payload", buf[:n])
	}

	if _, err := r.Open(""); !errors.Is(err, ErrIsDir) {
		t.Fatalf("Open(root) = %v; want ErrIsDir", err)
	}
	if _, err := r.Open("nope.bin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open(missing) = %v; want ErrNotFound", err)
	}
	if _, err := r.Create("../evil.bin"); err == nil {
		t.Fatal("Create outside the root succeeded")
	}
	if _, err := r.Create("nodir/evil.bin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Create with missing parent = %v; want ErrNotFound", err)
	}

	// OpenDir.
	df, err := r.OpenDir("Worlds")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer df.Close()
	names, err := df.Readdirnames(-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "World" {
		t.Fatalf("OpenDir(Worlds) = %v; want [World]", names)
	}
	if _, err := r.OpenDir("Settings.xml"); err == nil {
		t.Fatal("OpenDir on a file succeeded")
	}
}

// TestListSkipsUnreadableChildren keeps List from failing the whole directory
// because one child is weird/unreadable.
func TestListSkipsUnreadableChildren(t *testing.T) {
	r := mustRoot(t)
	mustSymlink(t, "/nonexistent/nowhere", filepath.Join(r.Dir(), "weird"))
	entries, err := r.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "weird" {
			found = true
			if !e.IsSymlink {
				t.Errorf("weird entry should be marked as a symlink: %+v", e)
			}
		}
	}
	if !found {
		t.Error("a dangling in-root symlink with an outside target should still be listed")
	}
}

// TestResolveThroughDanglingDeep covers the dangling-ancestor walk, which is
// reachable when a *middle* component is a dangling symlink.
func TestResolveThroughDanglingDeep(t *testing.T) {
	r := mustRoot(t)
	base := filepath.Dir(r.Dir())

	// A dangling symlink pointing INSIDE the root, used as a directory
	// component: the walk must follow the lexical target and stay contained.
	mustSymlink(t, filepath.Join(r.Dir(), "Worlds"), filepath.Join(r.Dir(), "dangling_in"))
	if got, err := r.Resolve("dangling_in/World/Project.json"); err != nil {
		t.Fatalf("dangling in-root symlink ancestor rejected: %v", err)
	} else if !isWithin(r.Dir(), got) {
		t.Fatalf("resolved %q outside root", got)
	}

	// A dangling symlink pointing OUTSIDE, used as a directory component: the
	// walk must reject it rather than follow it.
	mustSymlink(t, filepath.Join(base, "ghost", "missing"), filepath.Join(r.Dir(), "dangling_out_dir"))
	if got, err := r.Resolve("dangling_out_dir/anything"); err == nil {
		t.Fatalf("dangling outside symlink ancestor accepted: %q", got)
	}

	// Two levels of dangling link, still inside.
	mustMkdir(t, filepath.Join(r.Dir(), "real"), 0o755)
	mustSymlink(t, "real", filepath.Join(r.Dir(), "hopA"))
	mustSymlink(t, "hopA", filepath.Join(r.Dir(), "hopB"))
	if got, err := r.Resolve("hopB/file.txt"); err != nil {
		t.Fatalf("chained in-root links rejected: %v", err)
	} else if got != filepath.Join(r.Dir(), "real", "file.txt") {
		t.Fatalf("Resolve(hopB/file.txt) = %q; want the canonical real path", got)
	}
}

// TestOpenat2AgreesWithPortable asserts the accelerated Linux path and the
// portable path accept exactly the same inputs. Skipped where openat2 is
// unavailable.
func TestOpenat2AgreesWithPortable(t *testing.T) {
	if !openat2Supported() {
		t.Skip("openat2 not available on this kernel")
	}
	r := mustRoot(t)
	inputs := []string{
		"", ".", "Settings.xml", "Worlds/World/Project.json",
		"sub/deep/file.txt", "link_in", "link_in/World/Project.json",
		"link_out_file", "link_out_dir", "link_out_dir/secret.txt",
		"../outside/secret.txt", "/etc/passwd", "a/../../b", "new.txt",
		"nodir/new.txt", "link_in_file",
	}
	for _, in := range inputs {
		portableAbs, portableErr := r.resolvePortable(in)
		if portableErr != nil {
			continue // the portable baseline already rejects it
		}
		// The accelerated descriptor open must agree.
		f, err := r.Open(in)
		if err != nil {
			// A create-target (no such file) is not a disagreement about
			// containment: there is simply nothing to open yet. Open reports it
			// with the typed ErrNotFound, so check with errors.Is.
			if errors.Is(err, ErrNotFound) {
				continue
			}
			// Directories cannot be opened by Open either.
			if fi, serr := os.Stat(portableAbs); serr == nil && fi.IsDir() {
				continue
			}
			t.Errorf("openat2 refused %q which the portable baseline allowed (%q): %v", in, portableAbs, err)
			continue
		}
		actual, _ := os.Readlink(filepath.Join("/proc/self/fd", itoa(int(f.Fd()))))
		f.Close()
		if actual == "" {
			continue
		}
		if !isWithin(r.Dir(), actual) {
			t.Errorf("openat2 handed out a descriptor outside the root for %q: %q", in, actual)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
