package world

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScanFindsWorldsWithBOM builds a two-world instance and checks the scan.
func TestScanFindsWorldsWithBOM(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "MyWorldA", "ShowNameX", "guid-a", 20, "Harmless", 3, 100)
	makeWorld(t, inst, "World", "ScWorld", "guid-b", 10, "Survival", 2, 50)

	worlds, err := Scan(inst)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(worlds) != 2 {
		t.Fatalf("Scan found %d worlds; want 2 (%+v)", len(worlds), worlds)
	}
	// Sorted by directory name.
	if worlds[0].DirName != "MyWorldA" || worlds[1].DirName != "World" {
		t.Fatalf("directory names = %q, %q; want MyWorldA, World", worlds[0].DirName, worlds[1].DirName)
	}

	a := worlds[0]
	if a.DisplayName != "ShowNameX" {
		t.Errorf("DisplayName = %q; want ShowNameX", a.DisplayName)
	}
	if a.Guid != "guid-a" {
		t.Errorf("Guid = %q; want guid-a", a.Guid)
	}
	if a.Mode != "Harmless" {
		t.Errorf("Mode = %q; want the STRING Harmless", a.Mode)
	}
	if a.MaxPlayers != 20 {
		t.Errorf("MaxPlayers = %d; want 20", a.MaxPlayers)
	}
	if len(a.Issues) != 0 {
		t.Errorf("Issues = %v; want none", a.Issues)
	}
	if a.Path != filepath.Join(inst, "Worlds", "MyWorldA") {
		t.Errorf("Path = %q", a.Path)
	}
	// 3 regions of 100 bytes each.
	if a.RegionsBytes != 300 {
		t.Errorf("RegionsBytes = %d; want 300", a.RegionsBytes)
	}
	if a.RegionsCount != 3 {
		t.Errorf("RegionsCount = %d; want 3", a.RegionsCount)
	}
	if a.SizeBytes < a.RegionsBytes {
		t.Errorf("SizeBytes (%d) < RegionsBytes (%d)", a.SizeBytes, a.RegionsBytes)
	}
	if a.ProjectMTime == 0 {
		t.Error("ProjectMTime was not populated")
	}

	b := worlds[1]
	if b.Mode != "Survival" || b.MaxPlayers != 10 {
		t.Errorf("world World = mode %q, max %d; want Survival, 10", b.Mode, b.MaxPlayers)
	}
}

// TestDirNameAndDisplayNameDiverge is the A.5.1 trap test in its purest form.
//
// The fixture is built exactly like the decisive experiment: the DIRECTORY is
// MyWorldA while GameInfo.WorldName is ShowNameX. Everything that addresses the
// save must use MyWorldA; nothing may ever derive a path from ShowNameX.
func TestDirNameAndDisplayNameDiverge(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "MyWorldA", "ShowNameX", "guid-a", 20, "Harmless", 1, 10)
	writeServerSetting(t, inst, "app:/Worlds/MyWorldA", "ShowNameX")

	worlds, err := Scan(inst)
	if err != nil {
		t.Fatal(err)
	}
	if len(worlds) != 1 {
		t.Fatalf("got %d worlds", len(worlds))
	}
	w := worlds[0]

	// The two identity fields must be the two DIFFERENT values.
	if w.DirName != "MyWorldA" {
		t.Fatalf("DirName = %q; want the on-disk directory MyWorldA", w.DirName)
	}
	if w.DisplayName != "ShowNameX" {
		t.Fatalf("DisplayName = %q; want the GameInfo value ShowNameX", w.DisplayName)
	}
	if w.DirName == w.DisplayName {
		t.Fatal("the fixture failed to make the two names differ; the trap is not exercised")
	}

	// The path must be built from DirName.
	if filepath.Base(w.Path) != "MyWorldA" {
		t.Fatalf("Path = %q; want it to end in the DIRECTORY name MyWorldA", w.Path)
	}
	if strings.Contains(w.Path, "ShowNameX") {
		t.Fatalf("Path = %q contains the DISPLAY name; the two concepts were confused", w.Path)
	}

	// The world is correctly detected as active, because WorldPath's last
	// segment matches the directory name.
	if !w.Active {
		t.Error("Active = false; want true (WorldPath ends in MyWorldA)")
	}

	// Get must be addressable by DIRECTORY name, and must not be addressable by
	// the display name.
	got, err := Get(inst, "MyWorldA")
	if err != nil {
		t.Fatalf("Get by directory name: %v", err)
	}
	if got.DisplayName != "ShowNameX" {
		t.Errorf("Get(MyWorldA).DisplayName = %q", got.DisplayName)
	}
	if !got.Active {
		t.Error("Get(MyWorldA).Active = false; want true")
	}
	if _, err := Get(inst, "ShowNameX"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get by DISPLAY name = %v; want ErrNotFound — the display name is not a directory", err)
	}

	// And the §2.7 payload: switching saves rewrites WorldPath, not WorldName.
	writeServerSetting(t, inst, "app:/Worlds/MyWorldA", "SomethingElse")
	worlds2, err := Scan(inst)
	if err != nil {
		t.Fatal(err)
	}
	if !worlds2[0].Active {
		t.Error("changing WorldName alone changed which world is active; it must not (§2.7)")
	}
}

// TestActivateChangesWorldPathNotWorldName is the behavioural half of §2.7.
func TestActivateChangesWorldPathNotWorldName(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "WorldA", "DisplayA", "g1", 10, "Harmless", 0, 0)
	makeWorld(t, inst, "WorldB", "DisplayB", "g2", 10, "Survival", 0, 0)
	ssPath := writeServerSetting(t, inst, "app:/Worlds/WorldA", "DisplayA")

	res, err := Activate(ssPath, "WorldB", false, nil)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !res.Changed {
		t.Error("Changed = false; want true")
	}
	if res.WorldPath != "app:/Worlds/WorldB" {
		t.Errorf("WorldPath = %q; want app:/Worlds/WorldB", res.WorldPath)
	}

	// Read the file back: WorldPath changed, WorldName must be untouched.
	raw, err := os.ReadFile(ssPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(raw, UTF8BOM), &doc); err != nil {
		t.Fatalf("ServerSetting.json is no longer parseable: %v", err)
	}
	if doc["WorldPath"] != "app:/Worlds/WorldB" {
		t.Errorf("persisted WorldPath = %v; want app:/Worlds/WorldB", doc["WorldPath"])
	}
	if doc["WorldName"] != "DisplayA" {
		t.Errorf("persisted WorldName = %v; Activate must not touch it", doc["WorldName"])
	}
	// Unrelated fields must survive the rewrite.
	// Unrelated ServerSetting.json fields must survive the rewrite. (The port
	// is NOT among them — it lives in Settings.xml per §2.2.)
	if doc["WorldMaxPlayers"] != float64(20) || doc["Autorun"] != true {
		t.Errorf("Activate dropped unrelated fields: %+v", doc)
	}
	if _, present := doc["ServerPort"]; present {
		t.Errorf("the fixture wrongly puts ServerPort in ServerSetting.json; it belongs in Settings.xml: %+v", doc)
	}
	// A write-ahead backup must exist (§6.2).
	if _, err := os.Stat(ssPath + ".panel.bak"); err != nil {
		t.Errorf("no .panel.bak written before modifying the config: %v", err)
	}

	// The scan must now report WorldB as active.
	worlds, err := Scan(inst)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range worlds {
		wantActive := w.DirName == "WorldB"
		if w.Active != wantActive {
			t.Errorf("after Activate, %s.Active = %v; want %v", w.DirName, w.Active, wantActive)
		}
	}

	// Activating the same world again is a no-op.
	res2, err := Activate(ssPath, "WorldB", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Changed {
		t.Error("re-activating the same world reported Changed = true")
	}
}

// TestActivateRefusesWhileRunning is the explicit guard test.
func TestActivateRefusesWhileRunning(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "WorldA", "A", "g1", 10, "Harmless", 0, 0)
	makeWorld(t, inst, "WorldB", "B", "g2", 10, "Survival", 0, 0)
	ssPath := writeServerSetting(t, inst, "app:/Worlds/WorldA", "A")

	res, err := Activate(ssPath, "WorldB", true, nil)
	if !errors.Is(err, ErrRunning) {
		t.Fatalf("Activate while running = %v; want ErrRunning", err)
	}
	if res.WorldPath != "" {
		t.Errorf("a refused Activate still reported a WorldPath: %q", res.WorldPath)
	}

	// The config must be untouched.
	raw, err := os.ReadFile(ssPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(bytes.TrimPrefix(raw, UTF8BOM), &doc)
	if doc["WorldPath"] != "app:/Worlds/WorldA" {
		t.Fatalf("a refused Activate modified WorldPath to %v", doc["WorldPath"])
	}

	// The guard-callback form.
	_, err = ActivateWithGuard(ssPath, "WorldB", RefuseWhileRunning(func() bool { return true }), nil)
	if !errors.Is(err, ErrRunning) {
		t.Fatalf("ActivateWithGuard while running = %v; want ErrRunning", err)
	}
	_, err = ActivateWithGuard(ssPath, "WorldB", RefuseWhileRunning(func() bool { return false }), nil)
	if err != nil {
		t.Fatalf("ActivateWithGuard while stopped: %v", err)
	}
	// A stopped instance with no guard at all.
	if _, err := ActivateWithGuard(ssPath, "WorldB", nil, nil); err != nil {
		t.Fatalf("ActivateWithGuard with a nil guard: %v", err)
	}
}

// TestActivateValidatesDirName attacks the name validation.
func TestActivateValidatesDirName(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "WorldA", "A", "g1", 10, "Harmless", 0, 0)
	ssPath := writeServerSetting(t, inst, "app:/Worlds/WorldA", "A")

	for _, bad := range []string{"", ".", "..", "../WorldA", "a/b", `a\b`, "nul\x00x", "C:evil", strings.Repeat("x", 300)} {
		if _, err := Activate(ssPath, bad, false, nil); err == nil {
			t.Errorf("Activate(%q) succeeded; want rejection", bad)
		} else if !errors.Is(err, ErrInvalidDirName) {
			t.Errorf("Activate(%q) = %v; want ErrInvalidDirName", bad, err)
		}
	}
	// A name that does not exist on disk is refused: activating it would make
	// the server silently generate a fresh world.
	if _, err := Activate(ssPath, "NoSuchWorld", false, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Activate on a missing world = %v; want ErrNotFound", err)
	}
}

// TestPathSegment covers the §2.7 extraction rule.
func TestPathSegment(t *testing.T) {
	good := map[string]string{
		"app:/Worlds/MyWorldA": "MyWorldA",
		"app:/Worlds/World":    "World",
		"/Worlds/World":        "World",
		"World":                "World",
		"app:/Worlds/World/":   "World",
		"a/b/c":                "c",
		"中文目录":                 "中文目录",
	}
	for in, want := range good {
		got, err := PathSegment(in)
		if err != nil {
			t.Errorf("PathSegment(%q) = %v; want %q", in, err, want)
			continue
		}
		if got != want {
			t.Errorf("PathSegment(%q) = %q; want %q", in, got, want)
		}
	}
	// Adopting config.PathSegment tightened two cases, both correctly:
	//
	//   - SURROUNDING WHITESPACE is trimmed before the segment is taken, so
	//     "  spaced  " yields "spaced";
	//   - "app:/Worlds/" is refused because it names the Worlds CONTAINER, not
	//     a world directory — activating it would be a mistake.
	if got, err := PathSegment("  spaced  "); err != nil || got != "spaced" {
		t.Errorf("PathSegment(%q) = %q, %v; want spaced", "  spaced  ", got, err)
	}
	if got, err := PathSegment("app:/Worlds/"); err == nil {
		t.Errorf("PathSegment(%q) = %q; want rejection (it names the container, not a world)", "app:/Worlds/", got)
	}
	bad := []string{"", "/", "..", "app:/Worlds/..", "nul\x00x", "app:/Worlds/a\x00b", "app:/Worlds/.", "app:/Worlds/"}
	for _, in := range bad {
		if got, err := PathSegment(in); err == nil {
			t.Errorf("PathSegment(%q) = %q; want rejection", in, got)
		}
	}
}

// TestValidateDirName pins the rules shared with the WorldPath validator.
func TestValidateDirName(t *testing.T) {
	good := []string{"World", "MyWorldA", "world-2", "存档", "a b", ".hidden", "a.b.c"}
	for _, n := range good {
		if err := ValidateDirName(n); err != nil {
			t.Errorf("ValidateDirName(%q) = %v; want nil", n, err)
		}
	}
	bad := []string{"", ".", "..", "a/b", `a\b`, "nul\x00x", "C:x", "c:x", "a\nb", "a\tb", strings.Repeat("x", 256)}
	for _, n := range bad {
		if err := ValidateDirName(n); err == nil {
			t.Errorf("ValidateDirName(%q) = nil; want an error", n)
		} else if !errors.Is(err, ErrInvalidDirName) {
			t.Errorf("ValidateDirName(%q) = %v; want ErrInvalidDirName", n, err)
		}
	}
	// Exactly 255 bytes is allowed.
	if err := ValidateDirName(strings.Repeat("x", 255)); err != nil {
		t.Errorf("ValidateDirName at the 255-byte limit = %v", err)
	}
}

// TestScanMissingWorldsDir proves a fresh instance scans cleanly.
func TestScanMissingWorldsDir(t *testing.T) {
	inst := makeInstance(t) // no Worlds/ at all
	worlds, err := Scan(inst)
	if err != nil {
		t.Fatalf("Scan with no Worlds/ directory = %v; want an empty list and no error", err)
	}
	if worlds == nil {
		t.Fatal("Scan returned nil; want an empty non-nil slice")
	}
	if len(worlds) != 0 {
		t.Fatalf("Scan found %d worlds in a fresh instance", len(worlds))
	}
}

// TestScanHandlesMissingAndCorruptProjects proves one broken world does not
// abort the scan.
func TestScanHandlesMissingAndCorruptProjects(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "Good", "GoodDisplay", "g1", 10, "Harmless", 1, 10)

	// A world directory with no Project.json at all.
	bare := filepath.Join(inst, WorldsDirName, "NoManifest")
	if err := os.MkdirAll(filepath.Join(bare, RegionsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, RegionsDirName, "r"), []byte("xx"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A world with a corrupt Project.json.
	corrupt := filepath.Join(inst, WorldsDirName, "Corrupt")
	if err := os.MkdirAll(corrupt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, ProjectFileName), withBOM([]byte("{ this is not json")), 0o644); err != nil {
		t.Fatal(err)
	}

	// A world whose Project.json is valid JSON but missing the GameInfo subtree.
	sparse := filepath.Join(inst, WorldsDirName, "Sparse")
	if err := os.MkdirAll(sparse, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparse, ProjectFileName), withBOM([]byte(`{"Version":["string","2.4"]}`)), 0o644); err != nil {
		t.Fatal(err)
	}

	worlds, err := Scan(inst)
	if err != nil {
		t.Fatalf("Scan = %v; a broken world must not fail the whole scan", err)
	}
	if len(worlds) != 4 {
		t.Fatalf("Scan found %d worlds; want 4 (all of them, including the broken ones)", len(worlds))
	}
	byName := map[string]World{}
	for _, w := range worlds {
		byName[w.DirName] = w
	}

	if len(byName["Good"].Issues) != 0 {
		t.Errorf("Good.Issues = %v; want none", byName["Good"].Issues)
	}
	nm, ok := byName["NoManifest"]
	if !ok {
		t.Fatal("the world with no Project.json was dropped from the scan instead of being reported")
	}
	if len(nm.Issues) == 0 || !strings.Contains(strings.Join(nm.Issues, " "), "missing") {
		t.Errorf("NoManifest.Issues = %v; want a clear 'missing Project.json' issue", nm.Issues)
	}
	// Even with no manifest, the size accounting still works.
	if nm.RegionsBytes != 2 {
		t.Errorf("NoManifest.RegionsBytes = %d; want 2", nm.RegionsBytes)
	}

	c, ok := byName["Corrupt"]
	if !ok {
		t.Fatal("the world with a corrupt Project.json was dropped")
	}
	if len(c.Issues) == 0 || !strings.Contains(strings.Join(c.Issues, " "), "corrupt") {
		t.Errorf("Corrupt.Issues = %v; want a 'corrupt' issue", c.Issues)
	}
	if c.DisplayName != "" || c.Guid != "" {
		t.Errorf("a corrupt project produced display data: %+v", c)
	}

	sp, ok := byName["Sparse"]
	if !ok {
		t.Fatal("the sparse world was dropped")
	}
	if sp.DisplayName != "" || sp.MaxPlayers != 0 {
		t.Errorf("Sparse = display %q, max %d; want zero values", sp.DisplayName, sp.MaxPlayers)
	}
	if len(sp.Issues) != 0 {
		t.Errorf("Sparse.Issues = %v; a valid project with no GameInfo is not an error", sp.Issues)
	}
}

// TestScanIgnoresFilesAndBadDirNames proves non-directories and unaddressable
// names are skipped without breaking the scan.
func TestScanIgnoresFilesAndBadDirNames(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "Real", "Real", "g", 5, "Harmless", 0, 0)

	stray := filepath.Join(inst, WorldsDirName, "stray.txt")
	if err := os.WriteFile(stray, []byte("not a world"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory the server could never address via WorldPath (it contains a
	// separator-like character is impossible, but a control character is not).
	weird := filepath.Join(inst, WorldsDirName, "bad\nname")
	if err := os.MkdirAll(weird, 0o755); err != nil {
		t.Skip("filesystem refuses control characters in names")
	}

	worlds, err := Scan(inst)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, w := range worlds {
		if w.DirName == "stray.txt" {
			t.Error("a stray file directly under Worlds/ was reported as a world")
		}
		if strings.Contains(w.DirName, "\n") {
			t.Errorf("an unaddressable directory name %q was reported", w.DirName)
		}
	}
	if len(worlds) != 1 || worlds[0].DirName != "Real" {
		t.Fatalf("Scan = %+v; want only Real", worlds)
	}
}

// TestScanDetectsBak proves the .bak companion is reflected (§2.3, §A.5.3).
func TestScanDetectsBak(t *testing.T) {
	inst := makeInstance(t)
	dir := makeWorld(t, inst, "World", "W", "g", 5, "Harmless", 0, 0)
	if err := os.WriteFile(filepath.Join(dir, ProjectBakName), withBOM([]byte(`{"Version":["string","2.4"]}`)), 0o644); err != nil {
		t.Fatal(err)
	}
	worlds, err := Scan(inst)
	if err != nil {
		t.Fatal(err)
	}
	if !worlds[0].HasBak {
		t.Fatal("HasBak = false; want true")
	}
	if worlds[0].LastBakMTime == 0 {
		t.Error("LastBakMTime was not populated")
	}
	// The .bak must count toward the world size.
	if worlds[0].SizeBytes <= 0 {
		t.Error("SizeBytes = 0")
	}
	if worlds[0].RegionsBytes != 0 {
		t.Errorf("RegionsBytes = %d; want 0 when Regions/ is empty", worlds[0].RegionsBytes)
	}
}

// TestScanActiveFlagWithNoServerSetting proves an absent config leaves every
// world inactive rather than guessing from a display name.
func TestScanActiveFlagWithNoServerSetting(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "WorldA", "WorldA", "g", 5, "Harmless", 0, 0)
	worlds, err := Scan(inst)
	if err != nil {
		t.Fatal(err)
	}
	if worlds[0].Active {
		t.Error("Active = true with no ServerSetting.json; the active world is unknown")
	}
	// A WorldPath whose segment matches nothing leaves everything inactive.
	writeServerSetting(t, inst, "app:/Worlds/Ghost", "WorldA")
	worlds, err = Scan(inst)
	if err != nil {
		t.Fatal(err)
	}
	if worlds[0].Active {
		t.Error("Active = true for a WorldPath pointing at a nonexistent directory")
	}
}

// TestScanActiveMatchesByDirectoryNotDisplayName is the reverse trap: a world
// whose DISPLAY name matches the WorldPath segment but whose DIRECTORY does not
// must not be marked active.
func TestScanActiveMatchesByDirectoryNotDisplayName(t *testing.T) {
	inst := makeInstance(t)
	// Directory "DirOne", display "Shared".
	makeWorld(t, inst, "DirOne", "Shared", "g1", 5, "Harmless", 0, 0)
	// Directory "DirTwo", display "Other".
	makeWorld(t, inst, "DirTwo", "Other", "g2", 5, "Survival", 0, 0)

	// WorldPath selects DirTwo. Its display name is "Other".
	writeServerSetting(t, inst, "app:/Worlds/DirTwo", "Shared")

	worlds, err := Scan(inst)
	if err != nil {
		t.Fatal(err)
	}
	active := ""
	for _, w := range worlds {
		if w.Active {
			active = w.DirName
		}
	}
	if active != "DirTwo" {
		t.Fatalf("active world = %q; want DirTwo (matched by directory name, not by the display name Shared)", active)
	}
}

// TestGetErrors covers the Get error paths.
func TestGetErrors(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "WorldA", "Display", "g", 5, "Harmless", 0, 0)

	if _, err := Get(inst, ""); !errors.Is(err, ErrInvalidDirName) {
		t.Errorf("Get(empty) = %v; want ErrInvalidDirName", err)
	}
	if _, err := Get(inst, ".."); !errors.Is(err, ErrInvalidDirName) {
		t.Errorf("Get(..) = %v; want ErrInvalidDirName", err)
	}
	if _, err := Get(inst, "a/b"); !errors.Is(err, ErrInvalidDirName) {
		t.Errorf("Get(a/b) = %v; want ErrInvalidDirName", err)
	}
	if _, err := Get(inst, "Missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v; want ErrNotFound", err)
	}
	// A file where a directory is expected.
	if err := os.WriteFile(filepath.Join(inst, WorldsDirName, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(inst, "file"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(file) = %v; want ErrNotFound", err)
	}

	// The demo that Get cannot be used to probe outside Worlds/.
	outside := filepath.Join(inst, "secrets")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(inst, "../secrets"); err == nil {
		t.Fatal("Get with a traversal name reached outside Worlds/")
	}
}

// TestScanWithCustomStore proves the SettingStore seam works, which is how
// internal/config will be plugged in.
func TestScanWithCustomStore(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "WorldA", "A", "g1", 5, "Harmless", 0, 0)
	makeWorld(t, inst, "WorldB", "B", "g2", 5, "Survival", 0, 0)

	store := &fakeStore{worldPath: "app:/Worlds/WorldB", hasPath: true}
	worlds, err := ScanWithStore(inst, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range worlds {
		wantActive := w.DirName == "WorldB"
		if w.Active != wantActive {
			t.Errorf("%s.Active = %v; want %v", w.DirName, w.Active, wantActive)
		}
	}

	// The store, not the file on disk, must drive Activate.
	if _, err := Activate("", "WorldA", false, store); err != nil {
		t.Fatalf("Activate with a custom store: %v", err)
	}
	if store.setPath != "app:/Worlds/WorldA" {
		t.Errorf("store received %q; want app:/Worlds/WorldA", store.setPath)
	}

	// An empty WorldPath leaves everything inactive.
	empty := &fakeStore{}
	worlds, err = ScanWithStore(inst, empty)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range worlds {
		if w.Active {
			t.Errorf("%s.Active = true with no WorldPath in the store", w.DirName)
		}
	}
}

// TestActiveDirNameAndIsActive covers the small helpers.
func TestActiveDirNameAndIsActive(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "WorldA", "A", "g", 5, "Harmless", 0, 0)
	writeServerSetting(t, inst, "app:/Worlds/WorldA", "A")

	name, err := ActiveDirName(inst, nil)
	if err != nil {
		t.Fatal(err)
	}
	if name != "WorldA" {
		t.Fatalf("ActiveDirName = %q; want WorldA", name)
	}
	ok, err := IsActive(inst, "WorldA", nil)
	if err != nil || !ok {
		t.Fatalf("IsActive(WorldA) = %v, %v; want true", ok, err)
	}
	ok, err = IsActive(inst, "WorldB", nil)
	if err != nil || ok {
		t.Fatalf("IsActive(WorldB) = %v, %v; want false", ok, err)
	}
	if _, err := IsActive(inst, "../x", nil); !errors.Is(err, ErrInvalidDirName) {
		t.Fatalf("IsActive with a bad name = %v; want ErrInvalidDirName", err)
	}

	// A malformed WorldPath selects nothing and must not turn into an error.
	writeServerSetting(t, inst, "app:/Worlds/..", "A")
	ok, err = IsActive(inst, "WorldA", nil)
	if err != nil {
		t.Fatalf("IsActive with a malformed WorldPath = %v; want no error", err)
	}
	if ok {
		t.Error("IsActive = true for a malformed WorldPath")
	}
}

// TestWorldsDirAndPathForDir pin the two path builders that encode §2.7.
func TestWorldsDirAndPathForDir(t *testing.T) {
	if got := WorldsDir("/inst"); got != filepath.Join("/inst", "Worlds") {
		t.Errorf("WorldsDir = %q", got)
	}
	if got := PathForDir("MyWorldA"); got != "app:/Worlds/MyWorldA" {
		t.Errorf("PathForDir = %q; want app:/Worlds/MyWorldA", got)
	}
	// The round trip must be stable: PathForDir then PathSegment is identity.
	for _, name := range []string{"World", "MyWorldA", "存档", "world-2"} {
		seg, err := PathSegment(PathForDir(name))
		if err != nil {
			t.Fatalf("PathSegment(PathForDir(%q)) = %v", name, err)
		}
		if seg != name {
			t.Errorf("round trip %q -> %q", name, seg)
		}
	}
}

// TestServerSettingWorldPathReader covers the minimal BOM-aware reader.
func TestServerSettingWorldPathReader(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
		err  bool
	}{
		{"with-bom", withBOM([]byte(`{"WorldPath":"app:/Worlds/A"}`)), "app:/Worlds/A", false},
		{"without-bom", []byte(`{"WorldPath":"app:/Worlds/A"}`), "app:/Worlds/A", false},
		{"absent", []byte(`{"Other":1}`), "", false},
		{"null", []byte(`{"WorldPath":null}`), "", false},
		{"garbage", []byte(`not json`), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "ServerSetting.json")
			if err := os.WriteFile(p, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := ReadServerSetting(p)
			if tc.err {
				if err == nil {
					t.Fatalf("ReadServerSetting = %q; want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadServerSetting: %v", err)
			}
			if got != tc.want {
				t.Errorf("ReadServerSetting = %q; want %q", got, tc.want)
			}
		})
	}
	if _, err := ReadServerSetting(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("ReadServerSetting on a missing file succeeded")
	}
}

// TestFileSettingStoreSetWorldPath covers the write path, including the
// write-ahead backup and the preservation of unknown fields (§6.2).
func TestFileSettingStoreSetWorldPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ServerSetting.json")
	// UnknownNewField stands in for a field a future server version adds; it
	// must survive a WorldPath rewrite untouched (§6.2). ServerPort is
	// deliberately not used here because it is not a ServerSetting.json field
	// at all (§2.2: it lives in Settings.xml).
	original := withBOM([]byte(`{"WorldPath":"app:/Worlds/Old","UnknownNewField":{"a":1},"Autorun":false}`))
	if err := os.WriteFile(p, original, 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewFileSettingStore(p)

	got, ok := store.WorldPath()
	if !ok || got != "app:/Worlds/Old" {
		t.Fatalf("WorldPath() = %q, %v", got, ok)
	}
	if err := store.SetWorldPath("app:/Worlds/New"); err != nil {
		t.Fatalf("SetWorldPath: %v", err)
	}
	// The write-ahead backup must hold the ORIGINAL bytes.
	bak, err := os.ReadFile(p + ".panel.bak")
	if err != nil {
		t.Fatalf("no write-ahead backup: %v", err)
	}
	if !bytes.Equal(bak, original) {
		t.Error("the backup does not match the original file")
	}
	// The new content must preserve every other field.
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(raw, UTF8BOM), &doc); err != nil {
		t.Fatalf("written file is not parseable: %v", err)
	}
	if doc["WorldPath"] != "app:/Worlds/New" {
		t.Errorf("WorldPath = %v", doc["WorldPath"])
	}
	if doc["Autorun"] != false {
		t.Errorf("Autorun was lost or changed: %v", doc["Autorun"])
	}
	if _, present := doc["UnknownNewField"]; !present {
		t.Error("an unknown field was dropped by the rewrite; §6.2 requires preserving them")
	}

	// A missing file must error rather than silently creating one.
	if err := NewFileSettingStore(filepath.Join(dir, "nope.json")).SetWorldPath("x"); err == nil {
		t.Error("SetWorldPath on a missing file succeeded")
	}
	// So must a corrupt one.
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{not json"), 0o644)
	if err := NewFileSettingStore(bad).SetWorldPath("x"); err == nil {
		t.Error("SetWorldPath on a corrupt file succeeded")
	}
	// A missing WorldPath key reports absence.
	noKey := filepath.Join(dir, "nokey.json")
	_ = os.WriteFile(noKey, []byte(`{"Other":1}`), 0o644)
	if _, ok := NewFileSettingStore(noKey).WorldPath(); ok {
		t.Error("WorldPath() reported presence for a file without the key")
	}
}

// fakeStore is an in-memory SettingStore used to prove the seam.
type fakeStore struct {
	worldPath string
	hasPath   bool
	setPath   string
	setErr    error
}

func (f *fakeStore) WorldPath() (string, bool) { return f.worldPath, f.hasPath }

func (f *fakeStore) SetWorldPath(p string) error {
	f.setPath = p
	if f.setErr != nil {
		return f.setErr
	}
	f.worldPath, f.hasPath = p, true
	return nil
}
