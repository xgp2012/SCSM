package world

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readZipEntries returns every entry name and content of an archive.
func readZipEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read entry %s: %v", f.Name, err)
		}
		out[f.Name] = b
	}
	return out
}

// TestExportImportRoundTrip is the core export/import test: the archive, when
// imported into a fresh instance, must reproduce the save faithfully, BOM and
// all.
func TestExportImportRoundTrip(t *testing.T) {
	src := makeInstance(t)
	srcDir := makeWorld(t, src, "MyWorldA", "ShowNameX", "guid-roundtrip", 20, "Harmless", 4, 128)
	// Add some non-Regions content and the server's own .bak.
	if err := os.MkdirAll(filepath.Join(srcDir, "Players"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "Players", "p1.dat"), []byte("player-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	bakBytes := withBOM(realProjectJSON("MyWorldA", "OldName", "guid-old", 20, "Harmless", nil))
	if err := os.WriteFile(filepath.Join(srcDir, ProjectBakName), bakBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	original, err := os.ReadFile(filepath.Join(srcDir, ProjectFileName))
	if err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(t.TempDir(), "export.zip")
	if err := ExportZip(src, "MyWorldA", zipPath, ExportOptions{IncludeRegions: true, IncludeBak: true}); err != nil {
		t.Fatalf("ExportZip: %v", err)
	}

	entries := readZipEntries(t, zipPath)
	// The archive must be relative to the WORLD directory (no "Worlds/" prefix
	// and no wrapper dir), because that is what "extract into Worlds/<dir>"
	// means.
	for name := range entries {
		if strings.HasPrefix(name, "Worlds/") || strings.HasPrefix(name, "MyWorldA/") {
			t.Errorf("entry %q has a wrapper prefix; the archive must be relative to the world directory", name)
		}
	}
	for _, want := range []string{
		ProjectFileName,
		ProjectBakName,
		"Players/p1.dat",
		"Regions/chunk-00.region",
		"Regions/chunk-03.region",
	} {
		if _, ok := entries[want]; !ok {
			t.Errorf("archive is missing %q (has %v)", want, keysOf(entries))
		}
	}
	// The bytes of Project.json must be preserved EXACTLY, BOM included.
	if !bytes.Equal(entries[ProjectFileName], original) {
		t.Error("Project.json bytes changed during export")
	}
	if !bytes.HasPrefix(entries[ProjectFileName], UTF8BOM) {
		t.Error("the exported Project.json lost its BOM")
	}

	// --- import into a completely fresh instance ---
	dst := makeInstance(t)
	res, err := ImportZip(dst, zipPath, "Restored", ImportOptions{})
	if err != nil {
		t.Fatalf("ImportZip: %v", err)
	}
	if res.DirName != "Restored" {
		t.Errorf("DirName = %q; want Restored", res.DirName)
	}
	if res.Renamed {
		t.Error("Renamed = true for a non-conflicting import")
	}

	restoredProject := filepath.Join(dst, WorldsDirName, "Restored", ProjectFileName)
	got, err := os.ReadFile(restoredProject)
	if err != nil {
		t.Fatalf("restored Project.json: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Error("Project.json differs after the round trip")
	}
	if !bytes.HasPrefix(got, UTF8BOM) {
		t.Error("the BOM was stripped during the round trip")
	}
	if gotBak, err := os.ReadFile(filepath.Join(dst, WorldsDirName, "Restored", ProjectBakName)); err != nil {
		t.Errorf("Project.json.bak was not restored: %v", err)
	} else if !bytes.Equal(gotBak, bakBytes) {
		t.Error("Project.json.bak differs after the round trip")
	}
	if b, err := os.ReadFile(filepath.Join(dst, WorldsDirName, "Restored", "Regions", "chunk-01.region")); err != nil {
		t.Errorf("region file was not restored: %v", err)
	} else if len(b) != 128 {
		t.Errorf("region file size = %d; want 128", len(b))
	}

	// The restored world must scan as a valid, non-active save with the right
	// identity — proving the round trip preserved the semantics, not just bytes.
	worlds, err := Scan(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(worlds) != 1 {
		t.Fatalf("scan after import found %d worlds", len(worlds))
	}
	w := worlds[0]
	if w.DirName != "Restored" {
		t.Errorf("DirName = %q; want Restored", w.DirName)
	}
	if w.DisplayName != "ShowNameX" {
		t.Errorf("DisplayName = %q; want ShowNameX", w.DisplayName)
	}
	if w.Guid != "guid-roundtrip" {
		t.Errorf("Guid = %q", w.Guid)
	}
	if w.RegionsCount != 4 {
		t.Errorf("RegionsCount = %d; want 4", w.RegionsCount)
	}
	if len(w.Issues) != 0 {
		t.Errorf("restored world has issues: %v", w.Issues)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestExportExcludesRegions proves the documented size optimisation: with
// IncludeRegions false the archive is tiny and contains no terrain, while the
// manifest is still valid.
func TestExportExcludesRegions(t *testing.T) {
	inst := makeInstance(t)
	srcDir := makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 5, 4096)

	full := filepath.Join(t.TempDir(), "full.zip")
	if err := ExportZip(inst, "World", full, ExportOptions{IncludeRegions: true}); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(t.TempDir(), "small.zip")
	if err := ExportZip(inst, "World", small, ExportOptions{IncludeRegions: false}); err != nil {
		t.Fatal(err)
	}

	fullEntries := readZipEntries(t, full)
	smallEntries := readZipEntries(t, small)

	for name := range smallEntries {
		if strings.HasPrefix(name, RegionsDirName+"/") {
			t.Errorf("Regions entry %q present in a settings-only export", name)
		}
	}
	if _, ok := smallEntries[ProjectFileName]; !ok {
		t.Fatal("the settings-only export lost Project.json")
	}
	// Regions were the bulk: the trimmed archive must be dramatically smaller.
	fullInfo, _ := os.Stat(full)
	smallInfo, _ := os.Stat(small)
	if smallInfo.Size() >= fullInfo.Size() {
		t.Errorf("settings-only export (%d B) is not smaller than the full one (%d B)", smallInfo.Size(), fullInfo.Size())
	}
	// But the full export must actually contain the regions.
	regionCount := 0
	for name := range fullEntries {
		if strings.HasPrefix(name, RegionsDirName+"/") {
			regionCount++
		}
	}
	if regionCount != 5 {
		t.Errorf("full export has %d region entries; want 5", regionCount)
	}
	_ = srcDir

	// Importing the settings-only archive must still produce a loadable world
	// (it has a manifest) but with no regions.
	dst := makeInstance(t)
	res, err := ImportZip(dst, small, "Trimmed", ImportOptions{})
	if err != nil {
		t.Fatalf("ImportZip of a settings-only export: %v", err)
	}
	worlds, err := Scan(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(worlds) != 1 || worlds[0].DirName != res.DirName {
		t.Fatalf("scan = %+v", worlds)
	}
	if worlds[0].RegionsCount != 0 {
		t.Errorf("RegionsCount = %d; want 0", worlds[0].RegionsCount)
	}
}

// TestExportBakOption covers the .bak switch.
func TestExportBakOption(t *testing.T) {
	inst := makeInstance(t)
	dir := makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 0, 0)
	if err := os.WriteFile(filepath.Join(dir, ProjectBakName), withBOM([]byte(`{}`)), 0o644); err != nil {
		t.Fatal(err)
	}
	withBak := filepath.Join(t.TempDir(), "with.zip")
	if err := ExportZip(inst, "World", withBak, ExportOptions{IncludeBak: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readZipEntries(t, withBak)[ProjectBakName]; !ok {
		t.Error("IncludeBak=true did not include Project.json.bak")
	}
	withoutBak := filepath.Join(t.TempDir(), "without.zip")
	if err := ExportZip(inst, "World", withoutBak, ExportOptions{IncludeBak: false}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readZipEntries(t, withoutBak)[ProjectBakName]; ok {
		t.Error("IncludeBak=false still included Project.json.bak")
	}
}

// TestExportSkipsSymlinks proves a symlink inside a world never reaches the
// archive — an exported archive that carries links is a zip-slip delivery
// vehicle.
func TestExportSkipsSymlinks(t *testing.T) {
	inst := makeInstance(t)
	dir := makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 0, 0)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "evil_link")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Symlink("/", filepath.Join(dir, "root_link")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.zip")
	if err := ExportZip(inst, "World", out, ExportOptions{IncludeRegions: true}); err != nil {
		t.Fatalf("ExportZip: %v", err)
	}
	for name := range readZipEntries(t, out) {
		if strings.Contains(name, "link") {
			t.Errorf("symlink entry %q was included in the archive", name)
		}
	}
}

// TestExportErrors covers the failure paths.
func TestExportErrors(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 1, 10)

	out := filepath.Join(t.TempDir(), "o.zip")
	if err := ExportZip(inst, "../escape", out, ExportOptions{}); !errors.Is(err, ErrInvalidDirName) {
		t.Errorf("ExportZip with a traversal name = %v; want ErrInvalidDirName", err)
	}
	if err := ExportZip(inst, "Missing", out, ExportOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ExportZip of a missing world = %v; want ErrNotFound", err)
	}
	if err := ExportZip(inst, "World", "", ExportOptions{}); err == nil {
		t.Error("ExportZip with an empty destination succeeded")
	}
	// A world with no Project.json cannot be meaningfully exported.
	bare := filepath.Join(inst, WorldsDirName, "Bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ExportZip(inst, "Bare", out, ExportOptions{}); !errors.Is(err, ErrNoProjectFile) {
		t.Errorf("ExportZip of a manifest-less world = %v; want ErrNoProjectFile", err)
	}
	// A size cap must abort rather than produce a partial archive.
	if err := ExportZip(inst, "World", out, ExportOptions{MaxSizeBytes: 1}); err == nil {
		t.Error("ExportZip ignored MaxSizeBytes")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("a failed export left an archive behind")
	}
	// A world that is a file, not a directory.
	if err := os.WriteFile(filepath.Join(inst, WorldsDirName, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ExportZip(inst, "afile", out, ExportOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ExportZip of a file = %v; want ErrNotFound", err)
	}
}

// TestExportToZipReportsCounts covers the summary helper.
func TestExportToZipReportsCounts(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 2, 10)
	out := filepath.Join(t.TempDir(), "o.zip")
	res, err := ExportToZip(inst, "World", out, ExportOptions{IncludeRegions: true})
	if err != nil {
		t.Fatalf("ExportToZip: %v", err)
	}
	if !res.IncludedRegions {
		t.Error("IncludedRegions = false")
	}
	// Project.json + 2 regions.
	if res.Files != 3 {
		t.Errorf("Files = %d; want 3", res.Files)
	}
	if res.Bytes <= 0 {
		t.Error("Bytes = 0")
	}
	if _, err := ExportToZip(inst, "Missing", out, ExportOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ExportToZip of a missing world = %v; want ErrNotFound", err)
	}
}

// TestImportValidatesDirName attacks the newDirName validation with the exact
// rules the WorldPath validator uses.
func TestImportValidatesDirName(t *testing.T) {
	src := makeInstance(t)
	makeWorld(t, src, "World", "W", "g", 10, "Harmless", 0, 0)
	zipPath := filepath.Join(t.TempDir(), "w.zip")
	if err := ExportZip(src, "World", zipPath, ExportOptions{}); err != nil {
		t.Fatal(err)
	}

	dst := makeInstance(t)
	bad := []string{"", ".", "..", "../evil", "a/b", `a\b`, "nul\x00x", "C:evil", strings.Repeat("x", 300)}
	for _, name := range bad {
		if _, err := ImportZip(dst, zipPath, name, ImportOptions{}); err == nil {
			t.Errorf("ImportZip with newDirName %q succeeded; want rejection", name)
		} else if !errors.Is(err, ErrInvalidDirName) {
			t.Errorf("ImportZip(%q) = %v; want ErrInvalidDirName", name, err)
		}
	}
	// Nothing may have been created by any of the rejected attempts. Worlds/
	// is created (or already existed from the export side), but it must be
	// empty.
	entries, err := os.ReadDir(filepath.Join(dst, WorldsDirName))
	if err != nil {
		if os.IsNotExist(err) {
			return // nothing was created at all, which is also fine
		}
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a rejected import left %q behind", e.Name())
	}
}

// TestImportConflictHandling covers the default refusal, Overwrite and the
// deterministic RenameOnConflict.
func TestImportConflictHandling(t *testing.T) {
	src := makeInstance(t)
	makeWorld(t, src, "Origin", "OriginalDisplay", "g-origin", 10, "Harmless", 0, 0)
	zipPath := filepath.Join(t.TempDir(), "w.zip")
	if err := ExportZip(src, "Origin", zipPath, ExportOptions{}); err != nil {
		t.Fatal(err)
	}

	dst := makeInstance(t)
	// An existing world named exactly like the import target.
	existing := makeWorld(t, dst, "Target", "ExistingDisplay", "g-existing", 5, "Survival", 0, 0)

	// 1. Default: refuse.
	if _, err := ImportZip(dst, zipPath, "Target", ImportOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("conflicting import = %v; want ErrExists", err)
	}
	// The existing world must be completely untouched.
	w, err := Get(dst, "Target")
	if err != nil {
		t.Fatal(err)
	}
	if w.DisplayName != "ExistingDisplay" || w.Guid != "g-existing" {
		t.Fatalf("a refused import modified the existing world: %+v", w)
	}
	_ = existing

	// 2. RenameOnConflict: deterministic "<name>-2".
	res, err := ImportZip(dst, zipPath, "Target", ImportOptions{RenameOnConflict: true})
	if err != nil {
		t.Fatalf("ImportZip with RenameOnConflict: %v", err)
	}
	if res.DirName != "Target-2" {
		t.Fatalf("DirName = %q; want Target-2", res.DirName)
	}
	if !res.Renamed {
		t.Error("Renamed = false")
	}
	// The original survives, and the new one is the imported content.
	if w, err := Get(dst, "Target"); err != nil || w.Guid != "g-existing" {
		t.Fatalf("the original world was disturbed: %+v, %v", w, err)
	}
	if w, err := Get(dst, "Target-2"); err != nil || w.Guid != "g-origin" {
		t.Fatalf("the imported world is wrong: %+v, %v", w, err)
	}

	// 3. A third import gets "-3": the scheme is deterministic, not random.
	res3, err := ImportZip(dst, zipPath, "Target", ImportOptions{RenameOnConflict: true})
	if err != nil {
		t.Fatal(err)
	}
	if res3.DirName != "Target-3" {
		t.Fatalf("DirName = %q; want Target-3", res3.DirName)
	}

	// 4. Overwrite: the destination is replaced by the imported content.
	res4, err := ImportZip(dst, zipPath, "Target", ImportOptions{Overwrite: true})
	if err != nil {
		t.Fatalf("ImportZip with Overwrite: %v", err)
	}
	if res4.DirName != "Target" {
		t.Fatalf("DirName = %q; want Target", res4.DirName)
	}
	if res4.Renamed {
		t.Error("Renamed = true with Overwrite")
	}
	if w, err := Get(dst, "Target"); err != nil || w.Guid != "g-origin" {
		t.Fatalf("Overwrite did not replace the world: %+v, %v", w, err)
	}
	// No leftovers from the overwrite dance.
	entries, err := os.ReadDir(filepath.Join(dst, WorldsDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "scnetm-old") || strings.HasPrefix(e.Name(), ".scnetm-") {
			t.Errorf("leftover staging entry %q", e.Name())
		}
	}
}

// hostileZip builds an archive with arbitrary raw entry names, including ones a
// normal writer would refuse to produce.
func hostileZip(t *testing.T, path string, entries []zipEntry) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		} else {
			hdr.SetMode(0o644)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create entry %q: %v", e.name, err)
		}
		if e.body != "" {
			if _, err := io.WriteString(w, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

// TestImportZipSlipAttacks is the world-side zip-slip test. The canaries prove
// nothing outside the destination was touched.
func TestImportZipSlipAttacks(t *testing.T) {
	inst := makeInstance(t)
	if err := os.MkdirAll(filepath.Join(inst, WorldsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	// A sibling world and a file in the instance root: prime zip-slip targets.
	makeWorld(t, inst, "Victim", "VictimDisplay", "g-victim", 5, "Survival", 0, 0)
	victimProject := filepath.Join(inst, WorldsDirName, "Victim", ProjectFileName)
	before, err := os.ReadFile(victimProject)
	if err != nil {
		t.Fatal(err)
	}
	rootCanary := filepath.Join(inst, "canary.txt")
	if err := os.WriteFile(rootCanary, []byte("canary"), 0o644); err != nil {
		t.Fatal(err)
	}

	hostile := filepath.Join(t.TempDir(), "evil.zip")
	hostileZip(t, hostile, []zipEntry{
		{name: "../evil.txt", body: "PWNED"},
		{name: "a/../../evil2.txt", body: "PWNED"},
		{name: "/abs/evil.txt", body: "PWNED"},
		{name: "../../canary.txt", body: "PWNED"},
		{name: "../Victim/Project.json", body: `{"PWNED":true}`},
		{name: `..\..\win.txt`, body: "PWNED"},
		{name: "nul\x00evil.txt", body: "PWNED"},
		{name: "link", body: "/etc/passwd", mode: os.ModeSymlink | 0o777},
		{name: ProjectFileName, body: string(realProjectJSON("Evil", "Evil", "g-evil", 5, "Harmless", nil))},
		{name: "Regions/ok.region", body: "region"},
	})

	res, err := ImportZip(inst, hostile, "Imported", ImportOptions{})
	if err != nil {
		t.Fatalf("ImportZip returned a hard error; it should report skips: %v", err)
	}

	// Canaries.
	if b, _ := os.ReadFile(rootCanary); string(b) != "canary" {
		t.Fatalf("zip-slip overwrote an instance-root file: %q", b)
	}
	if b, _ := os.ReadFile(victimProject); !bytes.Equal(b, before) {
		t.Fatal("zip-slip overwrote a SIBLING WORLD's Project.json")
	}
	// No stray evil files anywhere under the instance.
	err = filepath.Walk(inst, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil || fi.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(p), "evil") || strings.Contains(filepath.Base(p), "win.txt") {
			t.Errorf("stray file created outside the destination: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The legitimate entries landed.
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "Imported", ProjectFileName)); err != nil {
		t.Errorf("the legitimate Project.json was not imported: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "Imported", "Regions", "ok.region")); err != nil {
		t.Errorf("the legitimate region was not imported: %v", err)
	}
	// The symlink entry was refused.
	if _, err := os.Lstat(filepath.Join(inst, WorldsDirName, "Imported", "link")); !os.IsNotExist(err) {
		t.Error("a symlink entry was materialised by the world importer")
	}
	// Every skip is reported.
	reasons := map[string]int{}
	for _, s := range res.Skipped {
		reasons[s.Reason]++
	}
	for _, want := range []string{SkipTraversal, SkipAbsolute, SkipUnsafe, SkipSymlink} {
		if reasons[want] == 0 {
			t.Errorf("no skip with reason %q; skips = %+v", want, res.Skipped)
		}
	}
	t.Logf("world import skips: %+v", res.Skipped)
}

// TestImportRejectsArchiveWithoutManifest proves a random zip cannot be
// published as a world.
func TestImportRejectsArchiveWithoutManifest(t *testing.T) {
	inst := makeInstance(t)
	z := filepath.Join(t.TempDir(), "nom.zip")
	hostileZip(t, z, []zipEntry{{name: "hello.txt", body: "hi"}})

	if _, err := ImportZip(inst, z, "NotAWorld", ImportOptions{}); !errors.Is(err, ErrNoProjectFile) {
		t.Fatalf("ImportZip of a manifest-less archive = %v; want ErrNoProjectFile", err)
	}
	// The staging directory must have been cleaned up: no half-published world.
	entries, err := os.ReadDir(filepath.Join(inst, WorldsDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a failed import left %q behind", e.Name())
	}
}

// TestImportEntryCaps covers the size, count and depth limits.
func TestImportEntryCaps(t *testing.T) {
	inst := makeInstance(t)

	// --- entry count ---
	many := filepath.Join(t.TempDir(), "many.zip")
	var entries []zipEntry
	entries = append(entries, zipEntry{name: ProjectFileName, body: `{}`})
	for i := 0; i < 30; i++ {
		entries = append(entries, zipEntry{name: fmt.Sprintf("f%02d.txt", i), body: "x"})
	}
	hostileZip(t, many, entries)
	if _, err := ImportZip(inst, many, "Many", ImportOptions{MaxEntries: 5}); err == nil {
		t.Error("ImportZip ignored MaxEntries")
	}

	// --- decompressed size ---
	big := filepath.Join(t.TempDir(), "big.zip")
	hostileZip(t, big, []zipEntry{
		{name: ProjectFileName, body: `{}`},
		{name: "big.bin", body: strings.Repeat("A", 1<<20)},
	})
	if _, err := ImportZip(inst, big, "Big", ImportOptions{MaxUncompressedBytes: 1024}); err == nil {
		t.Error("ImportZip ignored MaxUncompressedBytes")
	}

	// --- depth ---
	deep := filepath.Join(t.TempDir(), "deep.zip")
	hostileZip(t, deep, []zipEntry{
		{name: ProjectFileName, body: `{}`},
		{name: strings.Repeat("d/", 20) + "f.txt", body: "x"},
	})
	res, err := ImportZip(inst, deep, "Deep", ImportOptions{MaxDepth: 3})
	if err != nil {
		t.Fatalf("ImportZip: %v", err)
	}
	found := false
	for _, s := range res.Skipped {
		if s.Reason == SkipTooDeep {
			found = true
		}
	}
	if !found {
		t.Errorf("no too-deep skip reported: %+v", res.Skipped)
	}
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "Deep", strings.Repeat("d/", 20), "f.txt")); !os.IsNotExist(err) {
		t.Error("a too-deep entry was extracted")
	}
}

// TestImportDuplicateEntries proves the second occurrence cannot clobber the
// first one's validated destination.
func TestImportDuplicateEntries(t *testing.T) {
	inst := makeInstance(t)
	z := filepath.Join(t.TempDir(), "dup.zip")
	hostileZip(t, z, []zipEntry{
		{name: ProjectFileName, body: `{"first":true}`},
		{name: "dup.txt", body: "FIRST"},
		{name: "dup.txt", body: "SECOND"},
		{name: "./dup.txt", body: "THIRD"},
	})
	res, err := ImportZip(inst, z, "Dup", ImportOptions{})
	if err != nil {
		t.Fatalf("ImportZip: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(inst, WorldsDirName, "Dup", "dup.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "FIRST" {
		t.Fatalf("dup.txt = %q; want the FIRST entry to win", b)
	}
	dups := 0
	for _, s := range res.Skipped {
		if s.Reason == SkipDuplicate {
			dups++
		}
	}
	if dups != 2 {
		t.Errorf("duplicate skips = %d; want 2 (%+v)", dups, res.Skipped)
	}
}

// TestImportWrappedArchive covers the ExpectedDirNameInsideArchive convenience
// and proves it cannot be used to widen access.
func TestImportWrappedArchive(t *testing.T) {
	inst := makeInstance(t)
	z := filepath.Join(t.TempDir(), "wrapped.zip")
	hostileZip(t, z, []zipEntry{
		{name: "MyWorld/", body: ""},
		{name: "MyWorld/" + ProjectFileName, body: `{"wrapped":true}`},
		{name: "MyWorld/Regions/r1", body: "r"},
		{name: "Other/loose.txt", body: "loose"},
		{name: "../escape.txt", body: "escape"},
	})

	res, err := ImportZip(inst, z, "Unwrapped", ImportOptions{ExpectedDirNameInsideArchive: "MyWorld"})
	if err != nil {
		t.Fatalf("ImportZip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "Unwrapped", ProjectFileName)); err != nil {
		t.Fatalf("the wrapper was not stripped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "Unwrapped", "Regions", "r1")); err != nil {
		t.Errorf("wrapped region file missing: %v", err)
	}
	// Entries outside the required prefix are skipped, NOT imported at the top
	// level.
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "Unwrapped", "loose.txt")); !os.IsNotExist(err) {
		t.Error("an entry outside the required prefix was imported anyway")
	}
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "escape.txt")); !os.IsNotExist(err) {
		t.Error("a traversal entry escaped despite the prefix filter")
	}
	// The wrapper directory itself must not appear.
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "Unwrapped", "MyWorld")); !os.IsNotExist(err) {
		t.Error("the wrapper directory was created inside the world")
	}
	sawPrefixSkip := false
	for _, s := range res.Skipped {
		if s.Reason == SkipPrefix {
			sawPrefixSkip = true
		}
	}
	if !sawPrefixSkip {
		t.Errorf("no outside-wrapper skip reported: %+v", res.Skipped)
	}
}

// TestImportFilterNarrowsOnly proves the filter runs after the safety checks.
func TestImportFilterNarrowsOnly(t *testing.T) {
	inst := makeInstance(t)
	z := filepath.Join(t.TempDir(), "f.zip")
	hostileZip(t, z, []zipEntry{
		{name: ProjectFileName, body: `{}`},
		{name: "keep.txt", body: "k"},
		{name: "drop.txt", body: "d"},
		{name: "../escape.txt", body: "e"},
	})
	var offered []string
	res, err := ImportZip(inst, z, "Filtered", ImportOptions{
		Filter: func(rel string, f *zip.File) error {
			offered = append(offered, rel)
			if strings.HasPrefix(rel, "drop") {
				return errors.New("unwanted")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offered {
		if strings.Contains(o, "..") {
			t.Fatalf("the filter was offered an unsafe path %q", o)
		}
	}
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "Filtered", "keep.txt")); err != nil {
		t.Error("the kept file is missing")
	}
	if _, err := os.Stat(filepath.Join(inst, WorldsDirName, "Filtered", "drop.txt")); !os.IsNotExist(err) {
		t.Error("the filtered-out file was imported")
	}
	sawFiltered := false
	for _, s := range res.Skipped {
		if s.Reason == SkipFiltered {
			sawFiltered = true
		}
	}
	if !sawFiltered {
		t.Errorf("no filtered skip reported: %+v", res.Skipped)
	}
}

// TestImportPermissionsAreSanitised proves an archive cannot publish setuid
// files into an instance.
func TestImportPermissionsAreSanitised(t *testing.T) {
	inst := makeInstance(t)
	z := filepath.Join(t.TempDir(), "suid.zip")
	hostileZip(t, z, []zipEntry{
		{name: ProjectFileName, body: `{}`},
		{name: "evil", body: "#!/bin/sh\n", mode: 0o4755 | os.ModeSetuid | 0o777},
	})
	if _, err := ImportZip(inst, z, "Suid", ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(inst, WorldsDirName, "Suid", "evil"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetuid != 0 {
		t.Errorf("setuid bit survived import: %v", fi.Mode())
	}
	if fi.Mode().Perm()&0o022 != 0 {
		t.Errorf("group/world-writable file imported: %v", fi.Mode())
	}
}

// TestImportIntoEscapingWorldsRoot proves a symlinked Worlds/ cannot redirect an
// import outside the instance.
func TestImportIntoEscapingWorldsRoot(t *testing.T) {
	inst := makeInstance(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// Worlds/ is a symlink pointing outside the instance. The importer resolves
	// it once and pins that real directory, so the import lands inside
	// "outside" — which is what the pinned root means — but it must never touch
	// anything beyond it.
	if err := os.Symlink(outside, filepath.Join(inst, WorldsDirName)); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	src := makeInstance(t)
	makeWorld(t, src, "W", "W", "g", 5, "Harmless", 0, 0)
	z := filepath.Join(t.TempDir(), "w.zip")
	if err := ExportZip(src, "W", z, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportZip(inst, z, "Landed", ImportOptions{}); err != nil {
		t.Fatalf("ImportZip: %v", err)
	}
	// The world must be inside the resolved Worlds root, nowhere else.
	if _, err := os.Stat(filepath.Join(outside, "Landed", ProjectFileName)); err != nil {
		t.Errorf("the import did not land in the pinned root: %v", err)
	}
	// And nothing was created in the instance root.
	if _, err := os.Stat(filepath.Join(inst, "Landed")); !os.IsNotExist(err) {
		t.Error("the import landed in the instance root instead of Worlds/")
	}
}

// TestWorldPathPrefixConstants pins the literals §2.7 depends on.
func TestWorldPathPrefixConstants(t *testing.T) {
	if WorldPathPrefix != "app:" {
		t.Errorf("WorldPathPrefix = %q; want app:", WorldPathPrefix)
	}
	if WorldsDirName != "Worlds" {
		t.Errorf("WorldsDirName = %q", WorldsDirName)
	}
	if ProjectFileName != "Project.json" || ProjectBakName != "Project.json.bak" || RegionsDirName != "Regions" {
		t.Error("save layout constants drifted from §2.3/§A.4")
	}
}

// TestAssertSafeEntryName is the export-side guard used by the writer.
func TestAssertSafeEntryName(t *testing.T) {
	good := []string{"Project.json", "Regions/chunk.region", "a/b/c.txt", "中文.txt"}
	for _, n := range good {
		if err := assertSafeEntryName(n); err != nil {
			t.Errorf("assertSafeEntryName(%q) = %v; want nil", n, err)
		}
	}
	bad := []string{"", "../x", "a/../../x", "/abs", `a\b`, "nul\x00x", "C:x"}
	for _, n := range bad {
		if err := assertSafeEntryName(n); err == nil {
			t.Errorf("assertSafeEntryName(%q) = nil; want rejection", n)
		} else if !errors.Is(err, ErrZipSlip) {
			t.Errorf("assertSafeEntryName(%q) = %v; want ErrZipSlip", n, err)
		}
	}
}

// TestWithinBoundary proves the element-wise containment helper used by import.
func TestWithinBoundary(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		child   string
		allowed bool
	}{
		{filepath.Join(base, "a"), true},
		{filepath.Join(base, "a", "b"), true},
		{base, true},
		{filepath.Join(base, "..", filepath.Base(base)+"2"), false},
		{filepath.Dir(base), false},
		{"/etc/passwd", false},
	}
	for _, tc := range cases {
		if got := within(base, tc.child); got != tc.allowed {
			t.Errorf("within(%q, %q) = %v; want %v", base, tc.child, got, tc.allowed)
		}
	}
	// The prefix-sibling case explicitly: base is "<tmp>/inst", sibling is
	// "<tmp>/inst2".
	inst := filepath.Join(base, "inst")
	sibling := filepath.Join(base, "inst2")
	if within(inst, sibling) {
		t.Fatalf("within accepted the prefix sibling %q for %q", sibling, inst)
	}
}

// TestImportJSONErrorRowsIsNavigable is a small guard that the reported skip
// structs serialise (the API returns them to the UI).
func TestImportSkipStructuresSerialise(t *testing.T) {
	res := ImportResult{
		DirName: "W",
		Skipped: []SkippedEntry{{Name: "../x", Reason: SkipTraversal, Detail: "d"}},
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "traversal") || !strings.Contains(string(b), "dirName") {
		t.Errorf("ImportResult JSON = %s", b)
	}
}
