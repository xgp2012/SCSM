package world

import (
	"os"
	"path/filepath"
	"testing"

	"scnetm/internal/config"
)

// TestScannerAgreesWithConfigPackage cross-validates this package's lightweight
// Project reader against the authoritative `internal/config` implementation.
//
// The two exist for different reasons (see [readProjectViaConfig]), so the risk
// is that they drift: the world scanner would then show one thing while the
// advanced offline editor writes another. This test reads the SAME fixture with
// both and asserts every field the panel surfaces matches.
func TestScannerAgreesWithConfigPackage(t *testing.T) {
	inst := makeInstance(t)
	// A save whose DIRECTORY and DISPLAY names differ, with a BOM — the exact
	// shape from appendix A.5.1.
	dir := makeWorld(t, inst, "MyWorldA", "ShowNameX", "guid-parity", 20, "Harmless", 2, 64)
	projectPath := filepath.Join(dir, ProjectFileName)

	mine, err := ReadProject(projectPath)
	if err != nil {
		t.Fatalf("ReadProject: %v", err)
	}
	theirs, err := readProjectViaConfig(projectPath)
	if err != nil {
		t.Fatalf("readProjectViaConfig: %v", err)
	}

	// BOM agreement.
	if mine.HadBOM() != theirs.HasBOM() {
		t.Errorf("BOM disagreement: world.HadBOM=%v config.HasBOM=%v", mine.HadBOM(), theirs.HasBOM())
	}
	if !mine.HadBOM() {
		t.Fatal("the fixture lost its BOM")
	}

	// The display name (WorldName).
	myName, myOK := mine.GameInfoString("WorldName")
	theirName, theirOK := theirs.DisplayName()
	if myOK != theirOK || myName != theirName {
		t.Errorf("DisplayName disagreement: world=(%q,%v) config=(%q,%v)", myName, myOK, theirName, theirOK)
	}

	// The GUID.
	myGuid, myGuidOK := mine.TopLevelString("Guid")
	theirGuid, theirGuidOK := theirs.Guid()
	if myGuidOK != theirGuidOK || myGuid != theirGuid {
		t.Errorf("Guid disagreement: world=(%q,%v) config=(%q,%v)", myGuid, myGuidOK, theirGuid, theirGuidOK)
	}

	// The player cap.
	myMax, myMaxOK := mine.IntAt(mine.GameInfo(), "MaxOnlinePlayerCount")
	theirMax, theirMaxOK := theirs.MaxOnlinePlayerCount()
	if myMaxOK != theirMaxOK || myMax != theirMax {
		t.Errorf("MaxOnlinePlayerCount disagreement: world=(%d,%v) config=(%d,%v)", myMax, myMaxOK, theirMax, theirMaxOK)
	}

	// The game mode STRING (not the ServerSetting integer).
	myMode, myModeOK := mine.GameInfoString("GameMode")
	theirMode, theirModeOK := theirs.WorldMode()
	if myModeOK != theirModeOK || myMode != theirMode {
		t.Errorf("GameMode disagreement: world=(%q,%v) config=(%q,%v)", myMode, myModeOK, theirMode, theirModeOK)
	}

	// WorldDirectoryName must mirror WorldPath, and its last segment must be the
	// DIRECTORY name — the §2.7 identity this whole package is built around.
	myDirName, myDirOK := mine.GameInfoString("WorldDirectoryName")
	theirDirName, theirDirOK := theirs.WorldDirectoryName()
	if myDirOK != theirDirOK || myDirName != theirDirName {
		t.Errorf("WorldDirectoryName disagreement: world=(%q,%v) config=(%q,%v)", myDirName, myDirOK, theirDirName, theirDirOK)
	}
	seg, segErr := config.PathSegment(myDirName)
	if segErr != nil {
		t.Fatalf("config.PathSegment(%q): %v", myDirName, segErr)
	}
	if seg != "MyWorldA" {
		t.Errorf("WorldDirectoryName segment = %q; want the directory MyWorldA", seg)
	}
	if theirSeg, ok := theirs.WorldDirectorySegment(); ok && theirSeg != seg {
		t.Errorf("config.WorldDirectorySegment = %q; world-derived segment = %q", theirSeg, seg)
	}

	// The players subtree must be visible to both.
	if mine.Players() == nil {
		t.Error("world.Project.Players() = nil")
	}
	if _, ok := theirs.Players(); !ok {
		t.Error("config.Project.Players() reported absent")
	}

	// The whole-directory identity the panel would show must match what the
	// config package reports through the same fixture.
	worlds, err := Scan(inst)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(worlds) != 1 {
		t.Fatalf("Scan found %d worlds", len(worlds))
	}
	w := worlds[0]
	if w.DirName != "MyWorldA" {
		t.Errorf("scan DirName = %q", w.DirName)
	}
	if w.DisplayName != theirName {
		t.Errorf("scan DisplayName = %q; config says %q", w.DisplayName, theirName)
	}
	if w.Guid != theirGuid {
		t.Errorf("scan Guid = %q; config says %q", w.Guid, theirGuid)
	}
	if w.Mode != theirMode {
		t.Errorf("scan Mode = %q; config says %q", w.Mode, theirMode)
	}
	if w.MaxPlayers != theirMax {
		t.Errorf("scan MaxPlayers = %d; config says %d", w.MaxPlayers, theirMax)
	}
}

// TestPathSegmentMatchesConfig proves the world-level PathSegment wrapper and
// the config package agree for every legal input, and that the world wrapper
// adds the NUL guard the config validator lacks.
func TestPathSegmentMatchesConfig(t *testing.T) {
	inputs := []string{
		"app:/Worlds/MyWorldA", "app:/Worlds/World", "/Worlds/World", "World",
		"app:/Worlds/World/", "a/b/c", "中文目录", "world-2",
	}
	for _, in := range inputs {
		theirSeg, theirErr := config.PathSegment(in)
		mySeg, myErr := PathSegment(in)
		if (theirErr == nil) != (myErr == nil) {
			t.Errorf("PathSegment(%q): world err = %v, config err = %v", in, myErr, theirErr)
			continue
		}
		if theirErr == nil && theirSeg != mySeg {
			t.Errorf("PathSegment(%q): world = %q, config = %q", in, mySeg, theirSeg)
		}
	}

	// The NUL guard is this package's addition, because a NUL byte would
	// otherwise reach a filesystem call.
	for _, in := range []string{"nul\x00x", "app:/Worlds/a\x00b"} {
		if _, err := PathSegment(in); err == nil {
			t.Errorf("PathSegment(%q) accepted a NUL byte", in)
		}
	}

	// Illegal inputs must be rejected by BOTH.
	for _, in := range []string{"", "..", "app:/Worlds/..", "/", "app:/Worlds/"} {
		if _, err := PathSegment(in); err == nil {
			t.Errorf("world PathSegment(%q) = nil; want rejection", in)
		}
	}
}

// TestServerSettingReaderMatchesConfig cross-validates the world package's
// ServerSetting accessor against the config loader, including the BOM.
func TestServerSettingReaderMatchesConfig(t *testing.T) {
	inst := makeInstance(t)
	ssPath := writeServerSetting(t, inst, "app:/Worlds/MyWorldA", "ShowNameX")

	mine, err := ReadServerSetting(ssPath)
	if err != nil {
		t.Fatalf("ReadServerSetting: %v", err)
	}
	theirs, _, err := config.LoadServerSetting(ssPath)
	if err != nil {
		t.Fatalf("config.LoadServerSetting: %v", err)
	}
	if mine != theirs.WorldPath {
		t.Errorf("WorldPath disagreement: world = %q, config = %q", mine, theirs.WorldPath)
	}
	if mine != "app:/Worlds/MyWorldA" {
		t.Errorf("WorldPath = %q", mine)
	}

	// The directory segment must be the DIRECTORY, not the display name.
	if seg, err := theirs.WorldDirName(); err != nil || seg != "MyWorldA" {
		t.Errorf("config WorldDirName = %q, %v; want MyWorldA", seg, err)
	}

	// A BOM-less file must read identically.
	noBOM := filepath.Join(t.TempDir(), "ServerSetting.json")
	if err := os.WriteFile(noBOM, []byte(`{"WorldPath":"app:/Worlds/Plain"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadServerSetting(noBOM)
	if err != nil {
		t.Fatalf("ReadServerSetting (no BOM): %v", err)
	}
	if got != "app:/Worlds/Plain" {
		t.Errorf("WorldPath = %q; want app:/Worlds/Plain", got)
	}

	// The write path must round-trip through the config saver and preserve
	// unrelated fields.
	store := NewFileSettingStore(ssPath)
	if err := store.SetWorldPath("app:/Worlds/Other"); err != nil {
		t.Fatalf("SetWorldPath: %v", err)
	}
	after, extra, err := config.LoadServerSetting(ssPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.WorldPath != "app:/Worlds/Other" {
		t.Errorf("persisted WorldPath = %q", after.WorldPath)
	}
	if after.WorldName != "ShowNameX" {
		t.Errorf("SetWorldPath clobbered WorldName: %q", after.WorldName)
	}
	if after.GameMode != 1 || !after.Autorun {
		t.Errorf("SetWorldPath clobbered typed fields: GameMode=%d Autorun=%v", after.GameMode, after.Autorun)
	}
	// The world layer must not assume the port lives in ServerSetting.json: it
	// does not, per §2.2 (it is a Settings.xml element, default 28887). The
	// typed struct has no such field, and this package never reads or writes
	// one. Assert that so a future edit reintroducing the confusion fails here.
	if _, present := extra["ServerPort"]; present {
		t.Errorf("ServerSetting.json unexpectedly carries ServerPort; per §2.2 the port belongs to Settings.xml: %+v", extra)
	}
	// An unknown/future field must still survive the rewrite (§6.2). Use a field
	// that genuinely belongs to ServerSetting.json's domain.
	if err := os.WriteFile(ssPath, withBOM([]byte(
		`{"WorldPath":"app:/Worlds/A","WorldName":"N","WorldMaxPlayers":20,"FutureField":{"x":1}}`)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewFileSettingStore(ssPath).SetWorldPath("app:/Worlds/B"); err != nil {
		t.Fatalf("SetWorldPath: %v", err)
	}
	reloaded, reloadedExtra, err := config.LoadServerSetting(ssPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.WorldPath != "app:/Worlds/B" || reloaded.WorldMaxPlayers != 20 {
		t.Errorf("rewrite lost typed fields: %+v", reloaded)
	}
	if _, present := reloadedExtra["FutureField"]; !present {
		t.Errorf("rewrite dropped a future field; extra = %+v", reloadedExtra)
	}
}
