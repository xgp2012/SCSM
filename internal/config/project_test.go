package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadFixtureProject loads the pristine save fixture from testdata.
func loadFixtureProject(t *testing.T) *Project {
	t.Helper()
	p, err := LoadProject(testdataPath(t, "Project.json"))
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	return p
}

// loadProject loads a Project from an arbitrary path, failing the test on error.
func loadProject(t *testing.T, path string) *Project {
	t.Helper()
	p, err := LoadProject(path)
	if err != nil {
		t.Fatalf("LoadProject(%s): %v", path, err)
	}
	return p
}

// TestProjectFixtureHasBOM is the plan §2.3 / §A.5.2 requirement: Project.json is
// a BOM-prefixed document. The fixture's BOM is written byte-explicitly by
// gen_fixtures.go so an editor cannot silently strip it.
func TestProjectFixtureHasBOM(t *testing.T) {
	raw, err := os.ReadFile(testdataPath(t, "Project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasBOM(raw) {
		t.Fatalf("the fixture lost its BOM; first bytes are %x", raw[:min(8, len(raw))])
	}
	if !bytes.Equal(raw[:3], []byte{0xEF, 0xBB, 0xBF}) {
		t.Errorf("BOM bytes = %x, want efbbbf", raw[:3])
	}

	p := loadFixtureProject(t)
	if !p.HasBOM() {
		t.Error("Project.HasBOM() = false for a BOM-prefixed fixture")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestProjectBOMRoundTrip is the explicit BOM round-trip requirement: the BOM is
// stripped on read and re-emitted on write, because the server writes one and
// losing it may break compatibility.
func TestProjectBOMRoundTrip(t *testing.T) {
	// Case 1: the source has a BOM; the output must too.
	withBOM := stageFixture(t, "Project.json")
	p, err := ProjectEditor(withBOM, true)
	if err != nil {
		t.Fatalf("ProjectEditor: %v", err)
	}
	if err := p.SetField("MaxOnlinePlayerCount", "ushort", 32); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := p.SaveTo(withBOM); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	out, err := os.ReadFile(withBOM)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBOM(out) {
		t.Errorf("the BOM was lost when saving a BOM-prefixed file; first bytes %x", out[:min(8, len(out))])
	}

	// Case 2: a source WITHOUT a BOM still gets one on write — the server always
	// writes a BOM, so the panel must too.
	noBOM := filepath.Join(t.TempDir(), "Project.json")
	body, err := os.ReadFile(testdataPath(t, "Project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noBOM, trimBOM(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p2, err := LoadProject(noBOM)
	if err != nil {
		t.Fatalf("LoadProject (no BOM): %v", err)
	}
	if p2.HasBOM() {
		t.Error("HasBOM() = true for a document without a BOM")
	}
	if _, err := p2.GameInfo(); err != nil {
		t.Fatalf("GameInfo on a BOM-less document: %v", err)
	}
	p2.AllowAdvancedEdit = true
	if err := p2.SetField("MaxOnlinePlayerCount", "ushort", 8); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := p2.SaveTo(noBOM); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	out2, err := os.ReadFile(noBOM)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBOM(out2) {
		t.Error("a BOM-less source was written back without a BOM; the server always emits one")
	}
}

// TestProjectGameInfoDecodedView checks the convenience view: annotation pairs
// are unwrapped to plain Go values.
func TestProjectGameInfoDecodedView(t *testing.T) {
	p := loadFixtureProject(t)
	gi, err := p.GameInfo()
	if err != nil {
		t.Fatalf("GameInfo: %v", err)
	}
	if len(gi) < 34 {
		t.Errorf("GameInfo has %d fields, want at least the 34+ documented in plan §6.3.1", len(gi))
	}

	checks := []struct {
		key  string
		want any
	}{
		// GameMode is a STRING in the save (plan §6.3.1 note 1).
		{"GameMode", "Harmless"},
		{"WorldName", "ShowNameX"},
		{"WorldDirectoryName", "app:/Worlds/MyWorldA"},
		{"MaxOnlinePlayerCount", float64(20)},
		{"WorldSeedString", "999"},
		{"WorldSeed", float64(5130)}, // the DERIVED value
		{"AreSeasonsChanging", true},
		{"IsFriendlyFireEnabled", true},
		{"RandomSpawnPosition", false},
		{"IslandSize", "400,400"},
		{"TerrainLevel", float64(64)},
		{"ShoreRoughness", 0.5},
		{"DaySpeed", float64(1)},
		{"RecoverFator", float64(1)},
		{"EnvironmentBehaviorMode", "Living"},
		{"TimeOfDayMode", "Changing"},
		{"TerrainGenerationMode", "Continent"},
		{"StartingPositionMode", "Easy"},
		{"RunServer", true},
		{"OriginalSerializationVersion", "2.4"},
	}
	for _, c := range checks {
		got, ok := gi[c.key]
		if !ok {
			t.Errorf("GameInfo is missing %q", c.key)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("GameInfo[%q] = %#v, want %#v", c.key, got, c.want)
		}
	}
	// An unmodelled but present field must also be visible.
	if _, ok := gi["BlockTextureName"]; !ok {
		t.Error("GameInfo dropped the unmodelled BlockTextureName field")
	}
	if _, ok := gi["NoSuchFieldAtAll"]; ok {
		t.Error("GameInfo invented a field")
	}
}

// TestProjectTypedAccessors covers the typed read helpers named in the API.
func TestProjectTypedAccessors(t *testing.T) {
	p := loadFixtureProject(t)

	if n, ok := p.MaxOnlinePlayerCount(); !ok || n != 20 {
		t.Errorf("MaxOnlinePlayerCount() = %d,%v; want 20,true", n, ok)
	}
	if name, ok := p.DisplayName(); !ok || name != "ShowNameX" {
		t.Errorf("DisplayName() = %q,%v; want ShowNameX,true", name, ok)
	}
	if guid, ok := p.Guid(); !ok || guid != "9e9a67f8-3d3e-4a5f-9c1a-7b6d5e4f3a21" {
		t.Errorf("Guid() = %q,%v", guid, ok)
	}
	if mode, ok := p.WorldMode(); !ok || mode != "Harmless" {
		t.Errorf("WorldMode() = %q,%v; want the STRING Harmless", mode, ok)
	}
	if dir, ok := p.WorldDirectoryName(); !ok || dir != "app:/Worlds/MyWorldA" {
		t.Errorf("WorldDirectoryName() = %q,%v", dir, ok)
	}
	// The directory segment must agree with the WorldPath in ServerSetting.json
	// for the same instance (plan §2.7, §6.3.1).
	seg, ok := p.WorldDirectorySegment()
	if !ok || seg != "MyWorldA" {
		t.Errorf("WorldDirectorySegment() = %q,%v; want MyWorldA,true", seg, ok)
	}
	if s, ok := p.SeedString(); !ok || s != "999" {
		t.Errorf("SeedString() = %q,%v; want 999,true", s, ok)
	}
	if n, ok := p.Seed(); !ok || n != 5130 {
		t.Errorf("Seed() = %d,%v; want the derived 5130,true", n, ok)
	}

	// RawField returns the annotation and the raw value bytes.
	typ, raw, ok := p.RawField("GameMode")
	if !ok || typ != "Game.GameMode" {
		t.Errorf("RawField(GameMode) type = %q,%v; want Game.GameMode,true", typ, ok)
	}
	if string(raw) != `"Harmless"` {
		t.Errorf("RawField(GameMode) value = %s, want \"Harmless\"", raw)
	}
	typ, raw, ok = p.RawField("MaxOnlinePlayerCount")
	if !ok || typ != "ushort" || string(raw) != "20" {
		t.Errorf("RawField(MaxOnlinePlayerCount) = %q,%s,%v; want ushort,20,true", typ, raw, ok)
	}
	if _, _, ok := p.RawField("Nope"); ok {
		t.Error("RawField reported a missing field as present")
	}

	// TypedField checks the annotation.
	if typ, v, ok := p.TypedField("MaxOnlinePlayerCount", "ushort"); !ok || typ != "ushort" || v.(float64) != 20 {
		t.Errorf("TypedField = %q,%v,%v", typ, v, ok)
	}
	if _, _, ok := p.TypedField("MaxOnlinePlayerCount", "int"); ok {
		t.Error("TypedField accepted the wrong type annotation")
	}
}

// TestProjectSavePreservesUnknownFields is the essential save-file requirement
// (plan §2.3, §6.3.1): changing one field must not drop the 34+ other GameInfo
// fields or the Subsystems.Players subtree.
func TestProjectSavePreservesUnknownFields(t *testing.T) {
	path := stageFixture(t, "Project.json")

	before := loadFixtureProject(t)
	beforeInfo, err := before.GameInfo()
	if err != nil {
		t.Fatal(err)
	}
	beforePlayers, ok := before.Players()
	if !ok {
		t.Fatal("the fixture has no Subsystems.Players subtree to protect")
	}
	// Snapshot the raw document parsed generically, so nothing typed hides a loss.
	beforeRoot := parseGeneric(t, path)

	// Change exactly ONE field.
	p, err := ProjectEditor(path, true)
	if err != nil {
		t.Fatalf("ProjectEditor: %v", err)
	}
	if err := p.SetField("MaxOnlinePlayerCount", "ushort", 32); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := p.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	after := loadProject(t, path)
	afterInfo, err := after.GameInfo()
	if err != nil {
		t.Fatal(err)
	}
	afterPlayers, ok := after.Players()
	if !ok {
		t.Fatal("Subsystems.Players was DROPPED by the write")
	}
	afterRoot := parseGeneric(t, path)

	// The one intended change.
	if n, _ := after.MaxOnlinePlayerCount(); n != 32 {
		t.Errorf("MaxOnlinePlayerCount = %d, want the written 32", n)
	}
	if beforeInfo["MaxOnlinePlayerCount"] == afterInfo["MaxOnlinePlayerCount"] {
		t.Error("the field did not actually change")
	}

	// Every OTHER GameInfo field must be deep-equal.
	if len(afterInfo) != len(beforeInfo) {
		t.Errorf("GameInfo field count changed: %d -> %d", len(beforeInfo), len(afterInfo))
	}
	for k, want := range beforeInfo {
		if k == "MaxOnlinePlayerCount" {
			continue
		}
		got, present := afterInfo[k]
		if !present {
			t.Errorf("GameInfo field %q was DROPPED", k)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("GameInfo[%q] changed: %#v -> %#v", k, want, got)
		}
	}

	// The Players subtree must be deep-equal.
	if !reflect.DeepEqual(beforePlayers, afterPlayers) {
		t.Errorf("Subsystems.Players changed:\nbefore=%v\nafter =%v", beforePlayers, afterPlayers)
	}

	// And at the generic JSON level, nothing outside the edited field moved.
	// Subsystems differs only inside GameInfo.MaxOnlinePlayerCount, so compare it
	// with that one leaf excluded.
	beforeSubs := excludeGameInfoLeaf(t, beforeRoot, "MaxOnlinePlayerCount")
	afterSubs := excludeGameInfoLeaf(t, afterRoot, "MaxOnlinePlayerCount")
	if !reflect.DeepEqual(beforeSubs, afterSubs) {
		t.Error("Subsystems changed at the generic JSON level outside the edited field")
	}
	// The Players subtree must be byte-identical in the generic view too.
	if !reflect.DeepEqual(playersGeneric(t, beforeRoot), playersGeneric(t, afterRoot)) {
		t.Error("Subsystems.Players changed in the generic JSON view")
	}
	for _, key := range []string{"Version", "Guid", "Name"} {
		if !reflect.DeepEqual(beforeRoot[key], afterRoot[key]) {
			t.Errorf("top-level %q changed: %v -> %v", key, beforeRoot[key], afterRoot[key])
		}
	}
}

// excludeGameInfoLeaf returns the Subsystems subtree with one GameInfo leaf
// removed, so a diff can ignore the single field a test deliberately changed.
func excludeGameInfoLeaf(t *testing.T, root map[string]any, leaf string) map[string]any {
	t.Helper()
	clone := deepCopyMap(t, root["Subsystems"])
	gi, ok := clone["GameInfo"].(map[string]any)
	if !ok {
		t.Fatalf("no Subsystems.GameInfo in the generic view")
	}
	delete(gi, leaf)
	return clone
}

// playersGeneric returns the generic Subsystems.Players subtree.
func playersGeneric(t *testing.T, root map[string]any) any {
	t.Helper()
	subs, ok := root["Subsystems"].(map[string]any)
	if !ok {
		t.Fatal("no Subsystems in the generic view")
	}
	return subs["Players"]
}

// deepCopyMap round-trips a value through JSON so a test may mutate the copy.
func deepCopyMap(t *testing.T, v any) map[string]any {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// parseGeneric parses a save file into plain Go values, deliberately ignoring
// the type annotations, so a test can diff the whole document.
func parseGeneric(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(trimBOM(raw), &root); err != nil {
		t.Fatalf("generic parse of %s: %v", path, err)
	}
	return root
}

// TestProjectSetFieldPreservesTypeAnnotation is requirement 3: writes must keep
// the ["type", value] envelope intact.
func TestProjectSetFieldPreservesTypeAnnotation(t *testing.T) {
	path := stageFixture(t, "Project.json")
	p, err := ProjectEditor(path, true)
	if err != nil {
		t.Fatal(err)
	}
	// Passing an empty type must reuse the field's EXISTING annotation.
	if err := p.SetField("GameMode", "", "Creative"); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := p.SetField("DaySpeed", "", 2.5); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := p.SetField("IslandSize", "Vector2", "600,600"); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := p.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	// Inspect the raw JSON text: values must still be two-element arrays.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		`"GameMode": ["Game.GameMode","Creative"]`,
		`"DaySpeed": ["float",2.5]`,
		`"IslandSize": ["Vector2","600,600"]`,
	} {
		if !bytes.Contains(onDisk, []byte(needle)) {
			t.Errorf("expected %s in the saved file", needle)
		}
	}

	// And the typed read-back agrees.
	again := loadProject(t, path)
	if mode, ok := again.WorldMode(); !ok || mode != "Creative" {
		t.Errorf("WorldMode after write = %q,%v; want Creative,true", mode, ok)
	}
	if typ, raw, ok := again.RawField("GameMode"); !ok || typ != "Game.GameMode" {
		t.Errorf("the type annotation was lost: %q,%s,%v", typ, raw, ok)
	}
	if typ, raw, ok := again.RawField("DaySpeed"); !ok || typ != "float" || string(raw) != "2.5" {
		t.Errorf("DaySpeed after write = %q,%s,%v; want float,2.5,true", typ, raw, ok)
	}
}

// TestProjectSetFieldUnknownFieldAddsAnnotation checks the default annotation
// for a field the save does not have yet.
func TestProjectSetFieldUnknownFieldAddsAnnotation(t *testing.T) {
	path := stageFixture(t, "Project.json")
	p, err := ProjectEditor(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetField("ABrandNewSetting", "", true); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := p.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	again := loadProject(t, path)
	typ, raw, ok := again.RawField("ABrandNewSetting")
	if !ok {
		t.Fatal("the new field was not written")
	}
	if typ != "string" {
		t.Errorf("default annotation for a new field = %q, want \"string\"", typ)
	}
	if string(raw) != "true" {
		t.Errorf("new field value = %s, want true", raw)
	}
	// The pre-existing fields are untouched.
	if n, _ := again.MaxOnlinePlayerCount(); n != 20 {
		t.Errorf("MaxOnlinePlayerCount = %d, want the untouched 20", n)
	}
}

// TestProjectRefusesWorldSeedWrite is requirement 4: WorldSeed is DERIVED from
// WorldSeedString ("999" -> 5130), so writing it must be refused unless the
// caller passes an explicit allowDerived flag.
func TestProjectRefusesWorldSeedWrite(t *testing.T) {
	path := stageFixture(t, "Project.json")
	p, err := ProjectEditor(path, true)
	if err != nil {
		t.Fatal(err)
	}

	// Without the opt-in: a typed error naming the field.
	err = p.SetField("WorldSeed", "int", 12345)
	if err == nil {
		t.Fatal("SetField(WorldSeed) succeeded without an explicit opt-in")
	}
	var derived *ErrDerivedField
	if !errors.As(err, &derived) {
		t.Fatalf("error = %T (%v), want *ErrDerivedField", err, err)
	}
	if derived.Field != "WorldSeed" {
		t.Errorf("ErrDerivedField.Field = %q, want WorldSeed", derived.Field)
	}
	if !strings.Contains(err.Error(), "WorldSeedString") {
		t.Errorf("the error must point at WorldSeedString as the writable field: %v", err)
	}
	if p.Dirty() {
		t.Error("a refused write marked the Project dirty")
	}

	// WorldDirectoryName and the other engine-owned fields are refused too.
	for _, f := range []string{"WorldDirectoryName", "TotalElapsedGameTime", "OriginalSerializationVersion", "RunServer"} {
		if err := p.SetField(f, "", "x"); err == nil {
			t.Errorf("SetField(%s) succeeded without an explicit opt-in", f)
		}
	}

	// With the explicit opt-in it is allowed — that is how a byte-faithful
	// restore of an engine-produced value is performed.
	if err := p.SetField("WorldSeed", "int", 12345, AllowDerivedField()); err != nil {
		t.Fatalf("SetField(WorldSeed, AllowDerivedField()) = %v, want nil", err)
	}
	if !p.Dirty() {
		t.Error("an allowed write did not mark the Project dirty")
	}
	if err := p.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	again := loadProject(t, path)
	if n, ok := again.Seed(); !ok || n != 12345 {
		t.Errorf("Seed() = %d,%v; want the explicitly written 12345,true", n, ok)
	}
	// WorldSeedString must be untouched: it remains the source of truth.
	if s, ok := again.SeedString(); !ok || s != "999" {
		t.Errorf("SeedString() = %q,%v; want the untouched 999,true", s, ok)
	}
}

// TestProjectAdvancedEditGate is requirement 5: the frozen write policy —
// ServerSetting.json is the single source of write truth, so save edits need an
// explicit opt-in (and the instance stopped).
func TestProjectAdvancedEditGate(t *testing.T) {
	path := stageFixture(t, "Project.json")

	// Loading for display must NOT unlock writing.
	p, err := LoadProject(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.AllowAdvancedEdit {
		t.Error("LoadProject returned a Project with writes already unlocked")
	}
	if err := p.SetField("MaxOnlinePlayerCount", "ushort", 32); err != ErrAdvancedEditRequired {
		t.Errorf("SetField on a display-only Project = %v, want ErrAdvancedEditRequired", err)
	}
	if err := p.SaveTo(path); err != ErrAdvancedEditRequired {
		t.Errorf("SaveTo on a display-only Project = %v, want ErrAdvancedEditRequired", err)
	}
	if p.Dirty() {
		t.Error("a gated write marked the Project dirty")
	}
	// Reading is always allowed.
	if _, err := p.GameInfo(); err != nil {
		t.Errorf("GameInfo on a display-only Project: %v", err)
	}
	// The error must explain the policy.
	if !strings.Contains(ErrAdvancedEditRequired.Error(), "ServerSetting.json") {
		t.Errorf("the policy error must name ServerSetting.json as the write source: %v", ErrAdvancedEditRequired)
	}

	// ProjectEditor(false) refuses outright.
	if _, err := ProjectEditor(path, false); err != ErrAdvancedEditRequired {
		t.Errorf("ProjectEditor(path, false) = %v, want ErrAdvancedEditRequired", err)
	}
	// ProjectEditor(true) unlocks.
	ed, err := ProjectEditor(path, true)
	if err != nil {
		t.Fatalf("ProjectEditor(path, true): %v", err)
	}
	if !ed.AllowAdvancedEdit {
		t.Error("ProjectEditor(true) did not unlock writes")
	}
	if err := ed.SetField("MaxOnlinePlayerCount", "ushort", 32); err != nil {
		t.Errorf("SetField after unlocking: %v", err)
	}

	// nil receiver safety.
	var nilProject *Project
	if err := nilProject.SetField("X", "", 1); err == nil {
		t.Error("SetField on a nil Project did not error")
	}
	if err := nilProject.SaveTo(path); err == nil {
		t.Error("SaveTo on a nil Project did not error")
	}
	if _, err := nilProject.GameInfo(); err == nil {
		t.Error("GameInfo on a nil Project did not error")
	}
}

// TestProjectLoadSaveLoadIdempotent proves stability for the third file type:
// Load -> Save -> Load must be deep-equal, and a second save byte-identical.
func TestProjectLoadSaveLoadIdempotent(t *testing.T) {
	path := stageFixture(t, "Project.json")

	p1, err := ProjectEditor(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := p1.SaveTo(path); err != nil {
		t.Fatalf("first save: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	p2, err := ProjectEditor(path, true)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	gi1, err := p1.GameInfo()
	if err != nil {
		t.Fatal(err)
	}
	gi2, err := p2.GameInfo()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gi1, gi2) {
		t.Errorf("GameInfo is not stable across Load -> Save -> Load")
	}
	if !reflect.DeepEqual(playersOf(p1), playersOf(p2)) {
		t.Error("Players is not stable across Load -> Save -> Load")
	}
	if guidOf(p1) != guidOf(p2) {
		t.Error("Guid is not stable across Load -> Save -> Load")
	}

	if err := p2.SaveTo(path); err != nil {
		t.Fatalf("second save: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("saving twice produced different bytes:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	// The BOM must still be there after both saves.
	if !hasBOM(second) {
		t.Error("the BOM was lost across repeated saves")
	}
}

// playersOf returns the raw Players subtree, or nil when absent.
func playersOf(p *Project) map[string]any {
	m, _ := p.Players()
	return m
}

// guidOf returns the save Guid, or "" when absent.
func guidOf(p *Project) string {
	g, _ := p.Guid()
	return g
}

// TestProjectBackupOnSave verifies the .panel.bak behaviour.
func TestProjectBackupOnSave(t *testing.T) {
	path := stageFixture(t, "Project.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	p, err := ProjectEditor(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetField("MaxOnlinePlayerCount", "ushort", 32); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	backup, err := os.ReadFile(path + BackupSuffix)
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if !bytes.Equal(backup, before) {
		t.Error("the backup does not match the pre-save file")
	}
	if !hasBOM(backup) {
		t.Error("the backup lost the BOM")
	}
}

// TestProjectGameInfoFieldsInventory documents the field set the panel can see,
// and that the derived/engine-owned fields are present in a real save.
func TestProjectGameInfoFieldsInventory(t *testing.T) {
	p := loadFixtureProject(t)
	fields := p.GameInfoFields()

	// The full plan §6.3.1 table must be observable.
	want := []string{
		"WorldName", "GameMode", "EnvironmentBehaviorMode", "TimeOfDayMode",
		"AreSeasonsChanging", "YearDays", "TimeOfYear", "RecoverFator", "DaySpeed",
		"AreWeatherEffectsEnabled", "IsAdventureRespawnAllowed",
		"AreAdventureSurvivalMechanicsEnabled", "AreSupernaturalCreaturesEnabled",
		"IsFriendlyFireEnabled", "Password", "RunServer", "KeywordBlocking",
		"WorldSeedString", "WorldSeed", "TerrainGenerationMode", "IslandSize",
		"TerrainLevel", "ShoreRoughness", "TerrainBlockIndex",
		"TerrainOceanBlockIndex", "TemperatureOffset", "HumidityOffset",
		"SeaLevelOffset", "BiomeSize", "StartingPositionMode", "BlockTextureName",
		"Palette", "MaxOnlinePlayerCount", "DisableBlocks", "RandomSpawnPosition",
		"WorldDirectoryName", "OriginalSerializationVersion",
	}
	for _, f := range want {
		if _, ok := fields[f]; !ok {
			t.Errorf("GameInfo is missing the documented field %q", f)
		}
	}
	if len(want) < 34 {
		t.Errorf("the documented table has only %d fields; the plan says 34+", len(want))
	}

	// Type annotations from the plan's table.
	typeChecks := map[string]string{
		"GameMode":                "Game.GameMode",
		"EnvironmentBehaviorMode": "Game.EnvironmentBehaviorMode",
		"TimeOfDayMode":           "Game.TimeOfDayMode",
		"TerrainGenerationMode":   "Game.TerrainGenerationMode",
		"StartingPositionMode":    "Game.StartingPositionMode",
		"IslandSize":              "Vector2",
		"MaxOnlinePlayerCount":    "ushort",
		"WorldSeedString":         "string",
		"WorldSeed":               "int",
		"AreSeasonsChanging":      "bool",
		"YearDays":                "float",
	}
	for f, wantType := range typeChecks {
		if got := fields[f]; got != wantType {
			t.Errorf("GameInfo[%q] type = %q, want %q", f, got, wantType)
		}
	}
}

// TestProjectOverlappingFieldsPolicy documents the overlap between
// ServerSetting.json and GameInfo, and the frozen write policy the API layer
// must respect (plan §6.3.1 note 3).
func TestProjectOverlappingFieldsPolicy(t *testing.T) {
	overlaps := OverlappingFields()
	if len(overlaps) == 0 {
		t.Fatal("OverlappingFields() is empty; the plan documents a real overlap")
	}

	// Every pair must be real: the ServerSetting key must exist in the struct's
	// JSON, and the GameInfo key must exist in the save fixture (or be a
	// documented engine-owned field).
	s := DefaultServerSetting()
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var settingKeys map[string]any
	if err := json.Unmarshal(encoded, &settingKeys); err != nil {
		t.Fatal(err)
	}
	p := loadFixtureProject(t)
	fields := p.GameInfoFields()

	// Fields the save holds but this fixture deliberately mirrors from elsewhere.
	for _, o := range overlaps {
		if _, ok := settingKeys[o.ServerSettingKey]; !ok {
			t.Errorf("OverlappingFields names %q, which is not a ServerSetting.json key", o.ServerSettingKey)
		}
		if _, ok := fields[o.GameInfoKey]; !ok {
			t.Errorf("OverlappingFields names GameInfo field %q, which is absent from the save", o.GameInfoKey)
		}
	}

	// The specific fields the plan enumerates must all be present.
	mustOverlap := []struct{ setting, info string }{
		{"WorldName", "WorldName"},
		{"GameMode", "GameMode"},
		{"WorldPassword", "Password"},
		{"WorldDaySpeed", "DaySpeed"},
		{"WorldMaxPlayers", "MaxOnlinePlayerCount"},
		{"PVPEnabled", "IsFriendlyFireEnabled"},
		{"WorldDisableBlocks", "DisableBlocks"},
		{"SeasonChanging", "AreSeasonsChanging"},
		{"RandomSpawnPosition", "RandomSpawnPosition"},
		{"WorldRecoverySpeed", "RecoverFator"},
		{"WorldKeywordBlocking", "KeywordBlocking"},
		{"WorldPath", "WorldDirectoryName"},
		{"WorldSeed", "WorldSeedString"},
	}
	found := map[string]bool{}
	for _, o := range overlaps {
		found[o.ServerSettingKey+"->"+o.GameInfoKey] = true
	}
	for _, m := range mustOverlap {
		key := m.setting + "->" + m.info
		if !found[key] {
			t.Errorf("OverlappingFields is missing the documented pair %s -> GameInfo.%s", m.setting, m.info)
		}
	}

	// The GameMode pair must carry the int/string warning.
	for _, o := range overlaps {
		if o.ServerSettingKey == "GameMode" {
			if !strings.Contains(o.Note, "string") || !strings.Contains(o.Note, "int") {
				t.Errorf("the GameMode overlap note must warn about int vs string format: %q", o.Note)
			}
		}
		if o.ServerSettingKey == "WorldPath" && !strings.Contains(o.Note, "equal") {
			t.Errorf("the WorldPath overlap note must say the values must be equal: %q", o.Note)
		}
		if o.Note == "" {
			t.Errorf("overlap %s -> %s has no explanatory note", o.ServerSettingKey, o.GameInfoKey)
		}
	}
}

// TestProjectEndToEndSwitchWorld exercises the real workflow the policy exists
// for: change ServerSetting (the write source of truth), and confirm the save's
// read-only view agrees.
func TestProjectEndToEndSwitchWorld(t *testing.T) {
	dir := t.TempDir()
	settingPath := filepath.Join(dir, "ServerSetting.json")
	projectPath := filepath.Join(dir, "Project.json")

	if err := DefaultServerSetting().Save(settingPath, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(testdataPath(t, "Project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	// Switch the active save by changing WorldPath — NOT WorldName (plan §2.7).
	s, raw, err := LoadServerSetting(settingPath)
	if err != nil {
		t.Fatal(err)
	}
	s.WorldPath = "app:/Worlds/MyWorldB"
	s.WorldName = "我的世界" // display name, may be Chinese
	s.GameMode = 2       // Survival
	if err := s.Save(settingPath, raw); err != nil {
		t.Fatal(err)
	}

	// Re-read and confirm the directory followed WorldPath.
	reloaded, _, err := LoadServerSetting(settingPath)
	if err != nil {
		t.Fatal(err)
	}
	seg, err := reloaded.WorldDirName()
	if err != nil {
		t.Fatal(err)
	}
	if seg != "MyWorldB" {
		t.Errorf("directory = %q, want MyWorldB", seg)
	}
	if problems := ValidateServerSetting(reloaded); HasErrors(problems) {
		t.Errorf("the switched setting is invalid: %v", problems)
	}

	// The GameMode int must map to the save's string form.
	modeName, _ := GameModeName(reloaded.GameMode)
	if modeName != "Survival" {
		t.Errorf("GameModeName(2) = %q, want Survival", modeName)
	}

	// The save file itself is UNCHANGED: the policy says the server applies
	// ServerSetting.json to the save on start, so the panel must not have
	// written it. This is the "禁止两边同时改" rule made concrete.
	after, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, body) {
		t.Error("editing ServerSetting.json also modified Project.json; the write policy forbids this")
	}
}

// TestProjectErrors covers the failure paths.
func TestProjectErrors(t *testing.T) {
	if _, err := LoadProject(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("loading a missing save did not error")
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProject(bad); err == nil {
		t.Error("loading malformed JSON did not error")
	}

	// A document with no Subsystems.GameInfo: reads fail cleanly rather than
	// panicking.
	noGameInfo := filepath.Join(t.TempDir(), "nogameinfo.json")
	if err := os.WriteFile(noGameInfo, []byte(`{"Version":["string","2.4"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProject(noGameInfo)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if _, err := p.GameInfo(); err == nil {
		t.Error("GameInfo succeeded with no Subsystems.GameInfo")
	}
	if _, _, ok := p.RawField("WorldName"); ok {
		t.Error("RawField succeeded with no Subsystems.GameInfo")
	}
	if _, ok := p.MaxOnlinePlayerCount(); ok {
		t.Error("MaxOnlinePlayerCount succeeded with no Subsystems.GameInfo")
	}
	if _, ok := p.Guid(); ok {
		t.Error("Guid succeeded with no top-level Guid")
	}
	if _, ok := p.Players(); ok {
		t.Error("Players succeeded with no Subsystems.Players")
	}
	// Writing still errors rather than silently creating a subtree.
	p.AllowAdvancedEdit = true
	if err := p.SetField("WorldName", "", "x"); err == nil {
		t.Error("SetField succeeded with no Subsystems.GameInfo")
	}
}

// TestProjectBytesIsIndentedJSONWithBOM checks the output format.
func TestProjectBytesIsIndentedJSONWithBOM(t *testing.T) {
	p := loadFixtureProject(t)
	p.AllowAdvancedEdit = true
	out, err := p.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !hasBOM(out) {
		t.Error("Bytes did not emit a BOM")
	}
	if !bytes.Contains(out, []byte("\n  \"Version\":")) {
		t.Errorf("output is not 2-space indented:\n%s", out[:min(300, len(out))])
	}
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Error("output does not end with a newline")
	}
	// The body must be valid JSON once the BOM is removed.
	var probe map[string]any
	if err := json.Unmarshal(trimBOM(out), &probe); err != nil {
		t.Errorf("emitted bytes are not valid JSON: %v", err)
	}
}
