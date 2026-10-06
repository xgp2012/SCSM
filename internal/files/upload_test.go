package files

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUploadWhitelistRejectsDisallowedExtension is the explicit §5.7 test: an
// extension outside the allow-list must be refused.
func TestUploadWhitelistRejectsDisallowedExtension(t *testing.T) {
	pol := DefaultPolicy()
	allowed := []string{".zip", ".dll", ".scpak", ".json", ".xml", ".txt"}
	disallowed := []string{".sh", ".exe", ".php", ".jsp", ".so", ".py", ".bash", ".cgi", ".htaccess", ".", ""}

	for _, ext := range allowed {
		name := "file" + ext
		if _, err := ValidateUploadName(name, &pol); err != nil {
			t.Errorf("ValidateUploadName(%q) = %v; want allowed (it is in §5.7's list)", name, err)
		}
	}
	for _, ext := range disallowed {
		name := "file" + ext
		if _, err := ValidateUploadName(name, &pol); err == nil {
			t.Errorf("ValidateUploadName(%q) was allowed; want rejection", name)
		} else if !errors.Is(err, ErrNotAllowed) {
			t.Errorf("ValidateUploadName(%q) = %v; want an error wrapping ErrNotAllowed", name, err)
		}
	}

	// Case-insensitivity: an upper-case allowed extension still passes, but an
	// upper-case disallowed one still fails.
	if _, err := ValidateUploadName("PLUGIN.DLL", &pol); err != nil {
		t.Errorf("upper-case allowed extension rejected: %v", err)
	}
	if _, err := ValidateUploadName("EVIL.SH", &pol); err == nil {
		t.Error("upper-case disallowed extension allowed")
	}
	// Double extension is judged by the final one.
	if _, err := ValidateUploadName("evil.json.sh", &pol); err == nil {
		t.Error("double extension with a disallowed tail allowed")
	}
	if _, err := ValidateUploadName("good.sh.json", &pol); err != nil {
		t.Errorf("double extension with an allowed tail rejected: %v", err)
	}
}

// TestUploadNameSanitization attacks the name validator.
func TestUploadNameSanitization(t *testing.T) {
	bad := []string{
		"", ".", "..", "../evil.txt", "a/b.txt", `a\b.txt`, "/abs.txt",
		"nul\x00.txt", "C:\\evil.txt", "C:evil.txt", "a\nb.txt", "a\tb.txt",
		strings.Repeat("x", 300) + ".txt",
	}
	for _, name := range bad {
		if got, err := SanitizeUploadName(name); err == nil {
			t.Errorf("SanitizeUploadName(%q) = %q; want rejection", name, got)
		}
	}
	good := map[string]string{
		"ok.txt":          "ok.txt",
		"插件.zip":          "插件.zip",
		"a b.json":        "a b.json",
		"Plugin.DLL":      "Plugin.DLL",
		"Content.scpak":   "Content.scpak",
		".hidden.txt":     ".hidden.txt",
		"weird..name.xml": "weird..name.xml",
	}
	for in, want := range good {
		got, err := SanitizeUploadName(in)
		if err != nil {
			t.Errorf("SanitizeUploadName(%q) = %v; want %q", in, err, want)
			continue
		}
		if got != want {
			t.Errorf("SanitizeUploadName(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestSaveUpload covers the happy path plus every guard.
func TestSaveUpload(t *testing.T) {
	r := mustRoot(t)
	mustMkdir(t, filepath.Join(r.Dir(), "Plugins"), 0o755)

	// Happy path.
	res, err := r.SaveUpload(UploadRequest{DirRel: "Plugins", Name: "MyPlugin.dll"}, strings.NewReader("dll-bytes"))
	if err != nil {
		t.Fatalf("SaveUpload: %v", err)
	}
	if res.Rel != "Plugins/MyPlugin.dll" || res.Size != int64(len("dll-bytes")) {
		t.Fatalf("UploadResult = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir(), "Plugins", "MyPlugin.dll")); string(b) != "dll-bytes" {
		t.Fatalf("uploaded content = %q", b)
	}

	// Disallowed extension.
	if _, err := r.SaveUpload(UploadRequest{DirRel: "Plugins", Name: "evil.sh"}, strings.NewReader("#!/bin/sh")); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("upload of .sh = %v; want ErrNotAllowed", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "Plugins", "evil.sh")); !os.IsNotExist(err) {
		t.Fatal(".sh upload was written to disk despite being rejected")
	}

	// Traversal in the name.
	if _, err := r.SaveUpload(UploadRequest{Name: "../escaped.txt"}, strings.NewReader("x")); err == nil {
		t.Fatal("upload with a traversal name succeeded")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.Dir()), "escaped.txt")); !os.IsNotExist(err) {
		t.Fatal("upload escaped the instance root")
	}

	// Traversal in the directory.
	if _, err := r.SaveUpload(UploadRequest{DirRel: "../outside", Name: "x.txt"}, strings.NewReader("x")); err == nil {
		t.Fatal("upload into an escaping directory succeeded")
	}
	// Escaping symlink directory.
	if _, err := r.SaveUpload(UploadRequest{DirRel: "link_out_dir", Name: "x.txt"}, strings.NewReader("x")); err == nil {
		t.Fatal("upload through an escaping symlink succeeded")
	}

	// Missing directory.
	if _, err := r.SaveUpload(UploadRequest{DirRel: "nodir", Name: "x.txt"}, strings.NewReader("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("upload into a missing directory = %v; want ErrNotFound", err)
	}

	// Existing file without Overwrite.
	if _, err := r.SaveUpload(UploadRequest{DirRel: "Plugins", Name: "MyPlugin.dll"}, strings.NewReader("SECOND")); !errors.Is(err, ErrExists) {
		t.Fatalf("upload over an existing file = %v; want ErrExists", err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir(), "Plugins", "MyPlugin.dll")); string(b) != "dll-bytes" {
		t.Fatalf("rejected upload still modified the file: %q", b)
	}
	if _, err := r.SaveUpload(UploadRequest{DirRel: "Plugins", Name: "MyPlugin.dll", Overwrite: true}, strings.NewReader("SECOND")); err != nil {
		t.Fatalf("overwriting upload: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir(), "Plugins", "MyPlugin.dll")); string(b) != "SECOND" {
		t.Fatalf("overwrite did not take effect: %q", b)
	}

	// Size cap: a body larger than the cap must be refused AND removed.
	small := DefaultPolicy()
	small.MaxUploadSize = 16
	if _, err := r.SaveUpload(UploadRequest{DirRel: "Plugins", Name: "big.zip", Policy: &small},
		strings.NewReader(strings.Repeat("A", 1024))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized upload = %v; want ErrTooLarge", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "Plugins", "big.zip")); !os.IsNotExist(err) {
		t.Fatal("oversized upload left a partial file behind")
	}
	// A lying Content-Length cannot get past the reader cap either.
	if _, err := r.SaveUpload(UploadRequest{DirRel: "Plugins", Name: "lier.zip", Size: 8, Policy: &small},
		strings.NewReader(strings.Repeat("A", 1024))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("upload with a lying declared size = %v; want ErrTooLarge", err)
	}
	// Exactly at the cap is fine.
	if _, err := r.SaveUpload(UploadRequest{DirRel: "Plugins", Name: "fit.zip", Policy: &small},
		strings.NewReader(strings.Repeat("A", 16))); err != nil {
		t.Fatalf("upload exactly at the cap failed: %v", err)
	}

	// No temporary files left behind.
	entries, err := r.List("Plugins")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".scnetm-upload-") {
			t.Fatalf("temporary upload file left behind: %s", e.Name)
		}
	}
}

// TestSaveUploadDoesNotFollowPlantedSymlink proves an upload cannot be
// redirected through a pre-existing link.
func TestSaveUploadDoesNotFollowPlantedSymlink(t *testing.T) {
	r := mustRoot(t)
	base := filepath.Dir(r.Dir())
	victim := filepath.Join(base, "outside", "secret.txt")
	before, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(r.Dir(), "up"), 0o755)
	mustSymlink(t, victim, filepath.Join(r.Dir(), "up", "evil.txt"))

	if _, err := r.SaveUpload(UploadRequest{DirRel: "up", Name: "evil.txt", Overwrite: true},
		strings.NewReader("PWNED")); err != nil {
		t.Fatalf("SaveUpload: %v", err)
	}
	after, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("upload followed a planted symlink and modified a file outside the root: %q -> %q", before, after)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir(), "up", "evil.txt")); string(b) != "PWNED" {
		t.Fatalf("upload did not land as a real file: %q", b)
	}
}

// TestTextEditor covers the read/write path, its allow-list and UTF-8 rule.
func TestTextEditor(t *testing.T) {
	r := mustRoot(t)
	pol := DefaultPolicy()

	// Allowed extensions.
	for _, name := range []string{"a.json", "a.xml", "a.txt", "a.properties"} {
		if err := r.WriteTextFile(name, `{"k":"v"}`, &pol); err != nil {
			t.Fatalf("WriteTextFile(%q): %v", name, err)
		}
		got, err := r.ReadTextFile(name, &pol)
		if err != nil {
			t.Fatalf("ReadTextFile(%q): %v", name, err)
		}
		if got != `{"k":"v"}` {
			t.Fatalf("ReadTextFile(%q) = %q", name, got)
		}
	}
	// Disallowed extension, in both directions.
	if err := r.WriteTextFile("evil.sh", "#!/bin/sh", &pol); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("WriteTextFile(.sh) = %v; want ErrNotAllowed", err)
	}
	mustWrite(t, filepath.Join(r.Dir(), "binary.dll"), []byte("\x00\x01\x02"))
	if _, err := r.ReadTextFile("binary.dll", &pol); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ReadTextFile(.dll) = %v; want ErrNotAllowed", err)
	}

	// UTF-8 validation on read.
	mustWrite(t, filepath.Join(r.Dir(), "bad.txt"), []byte{0xff, 0xfe, 0xfd})
	if _, err := r.ReadTextFile("bad.txt", &pol); err == nil {
		t.Fatal("ReadTextFile accepted invalid UTF-8")
	}
	// UTF-8 validation on write.
	if err := r.WriteTextFile("bad2.txt", string([]byte{0xff, 0xfe}), &pol); err == nil {
		t.Fatal("WriteTextFile accepted invalid UTF-8")
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "bad2.txt")); !os.IsNotExist(err) {
		t.Fatal("a rejected text write still created the file")
	}
	// Valid multi-byte UTF-8 round-trips.
	if err := r.WriteTextFile("cn.txt", "存档名称：测试世界 🌍", &pol); err != nil {
		t.Fatalf("WriteTextFile(utf8): %v", err)
	}
	if got, _ := r.ReadTextFile("cn.txt", &pol); got != "存档名称：测试世界 🌍" {
		t.Fatalf("UTF-8 round-trip lost data: %q", got)
	}

	// Size cap.
	small := DefaultPolicy()
	small.MaxTextFileSize = 32
	if err := r.WriteTextFile("big.txt", strings.Repeat("x", 100), &small); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("WriteTextFile over the cap = %v; want ErrTooLarge", err)
	}
	mustWrite(t, filepath.Join(r.Dir(), "bigread.txt"), []byte(strings.Repeat("x", 100)))
	if _, err := r.ReadTextFile("bigread.txt", &small); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadTextFile over the cap = %v; want ErrTooLarge", err)
	}

	// The editor must not reach outside the root.
	if _, err := r.ReadTextFile("../outside/secret.txt", &pol); err == nil {
		t.Fatal("text editor read outside the root")
	}
	if err := r.WriteTextFile("../escape.txt", "x", &pol); err == nil {
		t.Fatal("text editor wrote outside the root")
	}
	if _, err := r.ReadTextFile("link_out_file", &pol); err == nil {
		t.Fatal("text editor read through an escaping symlink")
	}

	// IsTextExt / IsUploadExt agree with the policy.
	if !IsTextExt("a.json", nil) || IsTextExt("a.dll", nil) {
		t.Error("IsTextExt disagrees with the default policy")
	}
	if !IsUploadExt("a.dll", nil) || IsUploadExt("a.sh", nil) {
		t.Error("IsUploadExt disagrees with the default policy")
	}
}

// TestCustomPolicyIsHonoured proves the allow-lists really are configurable.
func TestCustomPolicyIsHonoured(t *testing.T) {
	r := mustRoot(t)
	custom := DefaultPolicy()
	custom.UploadExtensions = []string{".png", ".cfg"}
	if _, err := r.SaveUpload(UploadRequest{Name: "x.png", Policy: &custom}, strings.NewReader("p")); err != nil {
		t.Fatalf("custom-allowed upload rejected: %v", err)
	}
	if _, err := r.SaveUpload(UploadRequest{Name: "x.txt", Policy: &custom}, strings.NewReader("t")); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("upload outside the custom list = %v; want ErrNotAllowed", err)
	}
	customText := DefaultPolicy()
	customText.TextExtensions = []string{".conf"}
	if err := r.WriteTextFile("a.conf", "x=1", &customText); err != nil {
		t.Fatalf("custom text extension rejected: %v", err)
	}
	if err := r.WriteTextFile("a.json", "{}", &customText); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("text extension outside the custom list = %v; want ErrNotAllowed", err)
	}
	// A zero-value Policy still gets safe defaults, not "allow everything".
	empty := Policy{}
	if _, err := ValidateUploadName("x.sh", &empty); err == nil {
		t.Fatal("a zero-value Policy allowed a disallowed extension")
	}
	// An explicitly empty slice means "allow everything" — documented escape
	// hatch for a trusted deployment, and it must not be the default.
	permissive := DefaultPolicy()
	permissive.UploadExtensions = []string{}
	if _, err := ValidateUploadName("x.sh", &permissive); err != nil {
		t.Fatalf("an explicitly empty allow-list should permit anything: %v", err)
	}
}

// TestPolicyNormalize pins the default-filling behaviour.
func TestPolicyNormalize(t *testing.T) {
	p := Policy{MaxUploadSize: 5}.normalize()
	d := DefaultPolicy()
	if p.MaxUploadSize != 5 {
		t.Errorf("MaxUploadSize = %d; want the supplied 5", p.MaxUploadSize)
	}
	if p.MaxUnzipSize != d.MaxUnzipSize || p.MaxTextFileSize != d.MaxTextFileSize {
		t.Error("unset fields were not filled from the defaults")
	}
	if len(p.UploadExtensions) != len(d.UploadExtensions) {
		t.Error("a nil allow-list was not replaced by the default")
	}
}

// TestDiskUsage covers recursive size accounting and symlink safety.
func TestDiskUsage(t *testing.T) {
	r := mustRoot(t)

	// Known tree: 10 + 20 bytes.
	mustMkdir(t, filepath.Join(r.Dir(), "du", "sub"), 0o755)
	mustWrite(t, filepath.Join(r.Dir(), "du", "a.bin"), bytes.Repeat([]byte("a"), 10))
	mustWrite(t, filepath.Join(r.Dir(), "du", "sub", "b.bin"), bytes.Repeat([]byte("b"), 20))
	got, err := r.DiskUsage("du")
	if err != nil {
		t.Fatalf("DiskUsage: %v", err)
	}
	if got != 30 {
		t.Fatalf("DiskUsage = %d; want 30", got)
	}

	// A symlink to a huge/outside tree must contribute 0 and must not be
	// followed.
	mustSymlink(t, filepath.Dir(r.Dir()), filepath.Join(r.Dir(), "du", "link_to_base"))
	got2, err := r.DiskUsage("du")
	if err != nil {
		t.Fatalf("DiskUsage with a symlink: %v", err)
	}
	if got2 != 30 {
		t.Fatalf("DiskUsage followed a symlink: got %d; want 30 (unchanged)", got2)
	}
	// A symlink to / must be immediate, not a host-wide walk.
	mustSymlink(t, "/", filepath.Join(r.Dir(), "du", "link_to_root"))
	done := make(chan int64, 1)
	go func() {
		n, _ := r.DiskUsage("du")
		done <- n
	}()
	select {
	case n := <-done:
		if n != 30 {
			t.Fatalf("DiskUsage through a link to / = %d; want 30", n)
		}
	default:
		// It may still be running, which is fine: the point is it is not
		// blocking on a host walk. Wait for it.
		if n := <-done; n != 30 {
			t.Fatalf("DiskUsage = %d; want 30", n)
		}
	}

	// Single file.
	if n, err := r.DiskUsage("du/a.bin"); err != nil || n != 10 {
		t.Fatalf("DiskUsage(file) = %d, %v; want 10", n, err)
	}
	// Missing path is 0, not an error.
	if n, err := r.DiskUsage("nope"); err != nil || n != 0 {
		t.Fatalf("DiskUsage(missing) = %d, %v; want 0, nil", n, err)
	}
	// Escaping path is an error.
	if _, err := r.DiskUsage("../outside"); err == nil {
		t.Fatal("DiskUsage outside the root succeeded")
	}
	// The instance root itself.
	if n, err := r.DiskUsage(""); err != nil || n <= 0 {
		t.Fatalf("DiskUsage(root) = %d, %v; want a positive number", n, err)
	}
}

// TestCountFiles covers the file counter.
func TestCountFiles(t *testing.T) {
	r := mustRoot(t)
	mustMkdir(t, filepath.Join(r.Dir(), "cf"), 0o755)
	mustWrite(t, filepath.Join(r.Dir(), "cf", "1.bin"), bytes.Repeat([]byte("x"), 5))
	mustWrite(t, filepath.Join(r.Dir(), "cf", "2.bin"), bytes.Repeat([]byte("x"), 7))
	mustSymlink(t, "/etc/passwd", filepath.Join(r.Dir(), "cf", "link"))
	n, total, err := r.CountFiles("cf")
	if err != nil {
		t.Fatalf("CountFiles: %v", err)
	}
	if n != 2 || total != 12 {
		t.Fatalf("CountFiles = %d files, %d bytes; want 2, 12 (the symlink must not count)", n, total)
	}
	if _, _, err := r.CountFiles("nope"); err != nil {
		t.Fatalf("CountFiles(missing) = %v; want 0, 0, nil", err)
	}
}

// TestFreeSpace covers the host free-space helper used by the backup feature.
func TestFreeSpace(t *testing.T) {
	r := mustRoot(t)
	free, err := r.FreeSpace()
	if err != nil {
		t.Fatalf("FreeSpace: %v", err)
	}
	if free <= 0 {
		t.Fatalf("FreeSpace = %d; want > 0", free)
	}
	total, err := TotalSpace(r.Dir())
	if err != nil {
		t.Fatalf("TotalSpace: %v", err)
	}
	if total < free {
		t.Fatalf("TotalSpace (%d) < FreeSpace (%d)", total, free)
	}
	if _, err := FreeSpace(filepath.Join(r.Dir(), "definitely", "not", "here")); err == nil {
		t.Log("statfs on a non-existent path succeeded; acceptable on some filesystems")
	}
}

// TestDepthCapOnUsage proves the walk is bounded.
func TestDepthCapOnUsage(t *testing.T) {
	r := mustRoot(t)
	// Build a tree deeper than MaxWalkDepth.
	deep := filepath.Join(r.Dir(), "deep")
	cur := deep
	for i := 0; i < MaxWalkDepth+5; i++ {
		cur = filepath.Join(cur, "d")
	}
	mustMkdir(t, cur, 0o755)
	mustWrite(t, filepath.Join(cur, "leaf.bin"), []byte("x"))

	if _, err := r.DiskUsage("deep"); !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("DiskUsage on a too-deep tree = %v; want ErrDepthExceeded", err)
	}
	if err := r.Delete("deep", true); !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("Delete on a too-deep tree = %v; want ErrDepthExceeded", err)
	}
	if _, _, err := r.CountFiles("deep"); !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("CountFiles on a too-deep tree = %v; want ErrDepthExceeded", err)
	}
}

// TestPolicyErrorIs lets callers use errors.Is on the coarse sentinels.
func TestPolicyErrorIs(t *testing.T) {
	e := policyErr("nope")
	if !errors.Is(e, ErrNotAllowed) {
		t.Error("policyErr is not matched by ErrNotAllowed")
	}
	if errors.Is(e, ErrTooLarge) {
		t.Error("policyErr is wrongly matched by ErrTooLarge")
	}
	tl := tooLargeErr("big")
	if !errors.Is(tl, ErrTooLarge) {
		t.Error("tooLargeErr is not matched by ErrTooLarge")
	}
	if errors.Is(tl, ErrNotAllowed) {
		t.Error("tooLargeErr is wrongly matched by ErrNotAllowed")
	}
	if !strings.Contains(tl.Error(), "big") {
		t.Errorf("message lost the reason: %q", tl.Error())
	}
}

// TestEntryDisplayRel keeps the small helper honest.
func TestCleanRel(t *testing.T) {
	cases := map[string]string{
		"":            "",
		".":           "",
		"a":           "a",
		"./a":         "a",
		"a/b":         "a/b",
		"a//b":        "a/b",
		"a/./b":       "a/b",
		"a/b/":        "a/b",
		"linked/file": "linked/file",
	}
	for in, want := range cases {
		if got := cleanRel(in); got != want {
			t.Errorf("cleanRel(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestDirUsage per-child accounting keeps the world scanner's fast path honest.
func TestDirUsage(t *testing.T) {
	r := mustRoot(t)
	mustMkdir(t, filepath.Join(r.Dir(), "world", "Regions"), 0o755)
	mustMkdir(t, filepath.Join(r.Dir(), "world", "Players"), 0o755)
	mustWrite(t, filepath.Join(r.Dir(), "world", "Project.json"), bytes.Repeat([]byte("p"), 100))
	mustWrite(t, filepath.Join(r.Dir(), "world", "Regions", "r1"), bytes.Repeat([]byte("r"), 40))
	mustWrite(t, filepath.Join(r.Dir(), "world", "Regions", "r2"), bytes.Repeat([]byte("r"), 60))
	mustWrite(t, filepath.Join(r.Dir(), "world", "Players", "p1"), bytes.Repeat([]byte("q"), 7))

	total, per, err := r.DirUsage("world")
	if err != nil {
		t.Fatalf("DirUsage: %v", err)
	}
	if want := int64(100 + 100 + 7); total != want {
		t.Fatalf("total = %d; want %d", total, want)
	}
	if per["Regions"] != 100 {
		t.Fatalf("per[Regions] = %d; want 100", per["Regions"])
	}
	if per["Players"] != 7 {
		t.Fatalf("per[Players] = %d; want 7", per["Players"])
	}
	if _, _, err := r.DirUsage("../outside"); err == nil {
		t.Fatal("DirUsage outside the root succeeded")
	}
	if n, per2, err := r.DirUsage("nope"); err != nil || n != 0 || len(per2) != 0 {
		t.Fatalf("DirUsage(missing) = %d, %v, %v; want 0, empty, nil", n, per2, err)
	}
	if n, _, err := r.DirUsage("world/Project.json"); err != nil || n != 100 {
		t.Fatalf("DirUsage(file) = %d, %v; want 100", n, err)
	}
}

// TestErrZipSlipSentinel documents the exported sentinel's presence.
func TestErrZipSlipSentinel(t *testing.T) {
	if ErrZipSlip == nil {
		t.Fatal("ErrZipSlip is nil")
	}
	if !strings.Contains(ErrZipSlip.Error(), "escape") {
		t.Errorf("ErrZipSlip message = %q", ErrZipSlip.Error())
	}
	_ = fmt.Sprintf("%v", ErrZipSlip)
}
