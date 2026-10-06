package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testdataPath resolves a fixture path relative to this package.
func testdataPath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("resolve testdata %s: %v", name, err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture %s missing: %v", p, err)
	}
	return p
}

// stageFixture copies a fixture into a temp dir so a test can write to it
// without touching the repository copy.
func stageFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(testdataPath(t, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	dest := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		t.Fatalf("stage fixture %s: %v", name, err)
	}
	return dest
}

// TestServerSettingFixtureMatchesPlanSample pins the fixture against the
// measured sample in plan §2.2 so that later edits cannot silently drift.
func TestServerSettingFixtureMatchesPlanSample(t *testing.T) {
	s, raw, err := LoadServerSetting(testdataPath(t, "ServerSetting.json"))
	if err != nil {
		t.Fatalf("LoadServerSetting: %v", err)
	}

	if s.WorldPath != "app:/Worlds/MyWorldA" {
		t.Errorf("WorldPath = %q, want app:/Worlds/MyWorldA", s.WorldPath)
	}
	if s.WorldName != "ShowNameX" {
		t.Errorf("WorldName = %q, want ShowNameX", s.WorldName)
	}
	if s.GameMode != 1 {
		t.Errorf("GameMode = %d, want 1 (the verified Harmless anchor)", s.GameMode)
	}
	if s.WorldMaxPlayers != 20 {
		t.Errorf("WorldMaxPlayers = %d, want 20", s.WorldMaxPlayers)
	}
	if !s.SeasonChanging || !s.PVPEnabled {
		t.Errorf("SeasonChanging/PVPEnabled = %v/%v, want true/true", s.SeasonChanging, s.PVPEnabled)
	}
	if s.WorldSeed != "999" {
		t.Errorf("WorldSeed = %q, want the STRING \"999\"", s.WorldSeed)
	}

	// The raw map must carry the unknown future field.
	if got, ok := raw["SomeFutureOption"]; !ok {
		t.Fatalf("raw map dropped the unknown key SomeFutureOption: %v", raw)
	} else if n, ok := got.(float64); !ok || n != 42 {
		t.Errorf("raw[SomeFutureOption] = %v (%T), want 42", got, got)
	}
}

// TestServerSettingUnknownFieldSurvival is the hard requirement from plan §6.2:
// a load -> save round trip must NOT drop a field the panel does not model.
func TestServerSettingUnknownFieldSurvival(t *testing.T) {
	// Start from a file with an unknown key and add more exotic shapes.
	dir := t.TempDir()
	path := filepath.Join(dir, "ServerSetting.json")
	original := `{
  "Autorun": false,
  "GameMode": 1,
  "WorldPath": "app:/Worlds/World",
  "WorldName": "ScWorld",
  "SomeFutureOption": 42,
  "AnotherFutureOption": {"nested": [1, 2, 3], "flag": true},
  "FutureListOption": ["a", "b"]
}
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	s, raw, err := LoadServerSetting(path)
	if err != nil {
		t.Fatalf("LoadServerSetting: %v", err)
	}
	// 7 keys in the file: 4 modelled (Autorun, GameMode, WorldPath, WorldName)
	// and 3 unknown. The raw map must contain every one of them.
	const wantKeys = 7
	if len(raw) != wantKeys {
		t.Fatalf("raw map has %d keys, want %d: %v", len(raw), wantKeys, raw)
	}

	s.GameMode = 2
	s.WorldPath = "app:/Worlds/OtherWorld"
	if err := s.Save(path, raw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Re-read: the unknown keys must still be there, byte for byte in value.
	reloaded, raw2, err := LoadServerSetting(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.GameMode != 2 {
		t.Errorf("GameMode after round trip = %d, want 2", reloaded.GameMode)
	}
	if reloaded.WorldPath != "app:/Worlds/OtherWorld" {
		t.Errorf("WorldPath after round trip = %q", reloaded.WorldPath)
	}

	if _, ok := raw2["SomeFutureOption"]; !ok {
		t.Errorf("SomeFutureOption was LOST on save; raw=%v", raw2)
	}
	nested, ok := raw2["AnotherFutureOption"].(map[string]any)
	if !ok {
		t.Fatalf("AnotherFutureOption was LOST or retyped: %v (%T)", raw2["AnotherFutureOption"], raw2["AnotherFutureOption"])
	}
	if nested["flag"] != true {
		t.Errorf("nested flag = %v, want true", nested["flag"])
	}
	list, ok := raw2["FutureListOption"].([]any)
	if !ok || len(list) != 2 || list[0] != "a" {
		t.Errorf("FutureListOption mangled: %v", raw2["FutureListOption"])
	}
	if _, ok := raw2["SomeFutureOption"]; ok {
		if n := raw2["SomeFutureOption"].(float64); n != 42 {
			t.Errorf("SomeFutureOption = %v, want 42", n)
		}
	}

	// And the file itself must literally still contain the unknown keys.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"SomeFutureOption", "AnotherFutureOption", "FutureListOption"} {
		if !bytes.Contains(onDisk, []byte(needle)) {
			t.Errorf("saved file does not contain %q:\n%s", needle, onDisk)
		}
	}
}

// TestServerSettingLoadSaveLoadIdempotent proves Load -> Save -> Load is stable
// (plan §6.2): the second save must be byte-identical to the first.
func TestServerSettingLoadSaveLoadIdempotent(t *testing.T) {
	path := stageFixture(t, "ServerSetting.json")

	s1, raw1, err := LoadServerSetting(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if err := s1.Save(path, raw1); err != nil {
		t.Fatalf("first save: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	s2, raw2, err := LoadServerSetting(path)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if !reflect.DeepEqual(s1, s2) {
		t.Errorf("Load -> Save -> Load is not deep-equal:\n first=%+v\nsecond=%+v", s1, s2)
	}
	if !reflect.DeepEqual(raw1, raw2) {
		t.Errorf("raw maps differ across the round trip:\n first=%v\nsecond=%v", raw1, raw2)
	}
	if err := s2.Save(path, raw2); err != nil {
		t.Fatalf("second save: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("saving twice produced different bytes:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestServerSettingSaveIsReadableIndentedJSON checks the formatting requirement:
// json.MarshalIndent with two spaces, so a human can read the file.
func TestServerSettingSaveIsReadableIndentedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ServerSetting.json")
	s := DefaultServerSetting()
	s.GameMode = 2
	if err := s.Save(path, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("\n  \"GameMode\": 2,")) {
		t.Errorf("output is not 2-space indented JSON:\n%s", data)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Errorf("output does not end with a newline: %q", data)
	}
	if bytes.Contains(data, []byte("\t")) {
		t.Errorf("output contains tabs:\n%s", data)
	}
}

// TestServerSettingDefaultMatchesPlan pins the defaults from plan §2.2.
func TestServerSettingDefaultMatchesPlan(t *testing.T) {
	d := DefaultServerSetting()
	// A default that differs from the plan's sample is a bug: the panel writes
	// this file when it creates an instance (plan §6.1).
	want := &ServerSetting{
		CheckLogin: false, ScKeyServerId: "", ScKeyServerName: "",
		Autorun: false, AutoGenerateWorld: false,
		WorldPath: "app:/Worlds/World", WorldName: "ScWorld", WorldSeed: "",
		WorldPassword: "", WorldKeywordBlocking: "", WorldMaxPlayers: 20,
		WorldDaySpeed: 1, WorldRecoverySpeed: 1, WorldDisableBlocks: "",
		RandomSpawnPosition: false, GameMode: 1,
		SeasonChanging: true, PVPEnabled: true,
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("DefaultServerSetting mismatch:\n got=%+v\nwant=%+v", d, want)
	}

	// The defaults must themselves be self-consistent: directory "World" comes
	// from WorldPath, NOT from WorldName "ScWorld" (plan §2.7).
	seg, err := d.WorldDirName()
	if err != nil {
		t.Fatalf("WorldDirName: %v", err)
	}
	if seg != "World" {
		t.Errorf("default WorldDirName = %q, want \"World\" (from WorldPath, not WorldName %q)", seg, d.WorldName)
	}
}

// TestServerSettingJSONKeyNames locks the exact JSON keys, including
// capitalisation, against the plan's byte-for-byte requirement.
func TestServerSettingJSONKeyNames(t *testing.T) {
	data, err := json.Marshal(DefaultServerSetting())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"CheckLogin", "ScKeyServerId", "ScKeyServerName", "Autorun",
		"AutoGenerateWorld", "WorldPath", "WorldName", "WorldSeed",
		"WorldPassword", "WorldKeywordBlocking", "WorldMaxPlayers",
		"WorldDaySpeed", "WorldRecoverySpeed", "WorldDisableBlocks",
		"RandomSpawnPosition", "GameMode", "SeasonChanging", "PVPEnabled",
	}
	if len(got) != len(want) {
		t.Errorf("marshalled %d keys, want %d: %v", len(got), len(want), got)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("marshalled JSON is missing key %q; got %v", k, got)
		}
	}
}

// TestServerSettingBackupOnSave verifies the .panel.bak behaviour, including
// that no backup is attempted when the target does not exist yet.
func TestServerSettingBackupOnSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ServerSetting.json")

	// First write: no existing file, so no backup must be created.
	if err := DefaultServerSetting().Save(path, nil); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if _, err := os.Stat(path + BackupSuffix); !os.IsNotExist(err) {
		t.Errorf("a backup was created even though the target did not exist (stat err = %v)", err)
	}

	// Second write: the previous contents must be preserved in the backup.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, raw, err := LoadServerSetting(path)
	if err != nil {
		t.Fatal(err)
	}
	s.WorldName = "Renamed"
	if err := s.Save(path, raw); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	backup, err := os.ReadFile(path + BackupSuffix)
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if !bytes.Equal(backup, before) {
		t.Errorf("backup does not match the previous file:\n--- backup ---\n%s\n--- previous ---\n%s", backup, before)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(after, before) {
		t.Error("the target was not updated")
	}
}

// TestServerSettingSaveExtraCannotRevertStructFields guards against a stale raw
// map silently undoing a field the panel just changed.
func TestServerSettingSaveExtraCannotRevertStructFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ServerSetting.json")
	s := DefaultServerSetting()
	if err := s.Save(path, nil); err != nil {
		t.Fatal(err)
	}
	_, stale, err := LoadServerSetting(path)
	if err != nil {
		t.Fatal(err)
	}

	s.WorldName = "FreshValue"
	// stale still says ScWorld; the struct must win.
	if err := s.Save(path, stale); err != nil {
		t.Fatal(err)
	}
	reloaded, _, err := LoadServerSetting(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.WorldName != "FreshValue" {
		t.Errorf("stale extra reverted the struct: WorldName = %q, want FreshValue", reloaded.WorldName)
	}
}

// TestServerSettingHandlesBOM proves a BOM-prefixed file (a hand-edited copy)
// still loads.
func TestServerSettingHandlesBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ServerSetting.json")
	body := []byte(`{"GameMode": 1, "WorldPath": "app:/Worlds/World"}`)
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, body...), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _, err := LoadServerSetting(path)
	if err != nil {
		t.Fatalf("LoadServerSetting with BOM: %v", err)
	}
	if s.GameMode != 1 {
		t.Errorf("GameMode = %d, want 1", s.GameMode)
	}
}

// TestServerSettingErrors covers the failure paths.
func TestServerSettingErrors(t *testing.T) {
	if _, _, err := LoadServerSetting(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("loading a missing file did not error")
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadServerSetting(bad); err == nil {
		t.Error("loading malformed JSON did not error")
	}

	var nilSetting *ServerSetting
	if err := nilSetting.Save(filepath.Join(t.TempDir(), "x.json"), nil); err == nil {
		t.Error("saving a nil ServerSetting did not error")
	}
}

// TestPathSegment is the plan §2.7 / §6.2 requirement: the WorldPath's last
// segment decides the on-disk directory, and an illegal segment must be refused.
func TestPathSegment(t *testing.T) {
	ok := map[string]string{
		"app:/Worlds/World":     "World",
		"app:/Worlds/MyWorldA":  "MyWorldA",
		"app:/Worlds/new world": "new world",
		"app:/Worlds/世界":        "世界",
		"Worlds/Trailing/":      "Trailing",
		"app:/Worlds/x\\y":      "y", // Windows separators normalised, last segment taken
		"JustAName":             "JustAName",
	}
	for in, want := range ok {
		got, err := PathSegment(in)
		if err != nil {
			t.Errorf("PathSegment(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("PathSegment(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"",
		"   ",
		"app:/Worlds/",
		"app:/Worlds/..",
		"app:/Worlds/../etc",
		"app:",
	}
	for _, in := range bad {
		if got, err := PathSegment(in); err == nil {
			t.Errorf("PathSegment(%q) = %q, want an error", in, got)
		}
	}
}

// TestPathSegmentRejectsNUL is the regression test for the path-validation
// bypass where an embedded NUL byte was accepted as part of the directory
// segment. Go strings carry a NUL, but the syscall layer truncates the path at
// it, so "a\x00b" and "a" would address the same directory on disk while
// comparing UNEQUAL in Go — a validator that accepts the first while the
// filesystem resolves the second is trivially bypassable.
//
// NUL is covered at the start, middle and end of the segment, and also as a
// non-final path component, since PathSegment accepts multi-segment spellings
// and must not let a NUL through in an earlier component either.
func TestPathSegmentRejectsNUL(t *testing.T) {
	bad := []struct {
		name string
		in   string
	}{
		{"nul-middle", "app:/Worlds/a\x00b"},
		{"nul-start", "app:/Worlds/\x00ab"},
		{"nul-end", "app:/Worlds/ab\x00"},
		{"nul-only", "app:/Worlds/\x00"},
		{"nul-leading-path", "\x00app:/Worlds/World"},
		{"nul-trailing-path", "app:/Worlds/World\x00"},
		{"nul-non-final-component", "app:/Worlds/a\x00b/World"},
		{"nul-relative", "a\x00b/World"},
		{"nul-bare", "a\x00b"},
		{"nul-after-dotdot", "app:/Worlds/../a\x00b"},
		{"nul-before-separator", "app:/Worlds/a\x00/b"},
	}
	for _, c := range bad {
		got, err := PathSegment(c.in)
		if err == nil {
			t.Errorf("%s: PathSegment(%q) = %q, want an error (NUL byte must be rejected)", c.name, c.in, got)
			continue
		}
		if !strings.Contains(err.Error(), "control character") {
			t.Errorf("%s: PathSegment(%q) error = %v; want it to name the control character", c.name, c.in, err)
		}
		if got != "" {
			t.Errorf("%s: PathSegment(%q) returned %q alongside an error; want \"\"", c.name, c.in, got)
		}
	}
}

// TestPathSegmentRejectsControlCharacters covers the rest of the control range
// alongside NUL, including the raw newline and tab that are the realistic
// hand-edited-config mistakes, and DEL at 0x7F. None of these are ever an
// intentional directory name, so refusing them cannot break a legitimate world.
func TestPathSegmentRejectsControlCharacters(t *testing.T) {
	bad := []struct {
		name string
		in   string
	}{
		{"tab", "app:/Worlds/a\tb"},
		{"newline", "app:/Worlds/a\nb"},
		{"carriage-return", "app:/Worlds/a\rb"},
		{"bell", "app:/Worlds/a\ab"},
		{"escape", "app:/Worlds/a\x1bb"},
		{"lowest-c0", "app:/Worlds/a\x01b"},
		{"highest-c0", "app:/Worlds/a\x1fb"},
		{"del", "app:/Worlds/a\x7fb"},
		{"del-only", "app:/Worlds/\x7f"},
		{"tab-bare", "a\tb"},
	}
	for _, c := range bad {
		if got, err := PathSegment(c.in); err == nil {
			t.Errorf("%s: PathSegment(%q) = %q, want an error (control character)", c.name, c.in, got)
		}
	}
}

// TestPathSegmentControlCharactersAreNotTrimmedAway is the subtle half of the
// NUL bug: PathSegment trims surrounding whitespace, and strings.TrimSpace also
// strips NUL and the other C0 bytes. A leading or trailing control character
// must therefore be rejected on the RAW input, not on the trimmed copy,
// otherwise "\x00app:/Worlds/World" silently validates as "World".
func TestPathSegmentControlCharactersAreNotTrimmedAway(t *testing.T) {
	for _, in := range []string{
		"\x00app:/Worlds/World",
		"app:/Worlds/World\x00",
		"\x01app:/Worlds/World",
		"app:/Worlds/World\x1f",
	} {
		if got, err := PathSegment(in); err == nil {
			t.Errorf("PathSegment(%q) = %q, want an error; a control character must not be trimmed away", in, got)
		}
	}
}

// TestPathSegmentAcceptsLegitimateNames pins the other direction: the fix must
// not over-reject. Spaces (interior, and as an ordinary name) and non-ASCII
// Unicode — Chinese in particular — are legal directory names on the Linux
// hosts this panel targets and must keep working.
func TestPathSegmentAcceptsLegitimateNames(t *testing.T) {
	ok := map[string]string{
		"app:/Worlds/new world": "new world",
		"app:/Worlds/a b":       "a b",
		"app:/Worlds/世界":        "世界",
		"app:/Worlds/存档":        "存档",
		"app:/Worlds/Ünïcødé":   "Ünïcødé",
		"app:/Worlds/World-2":   "World-2",
		"app:/Worlds/a.b":       "a.b",
		"app:/Worlds/.hidden":   ".hidden",
		// A bare "nul"/"CON" is the literal string, not a NUL byte and not a
		// device node on Linux — deliberately accepted (see PathSegment's doc).
		"app:/Worlds/nul": "nul",
		"app:/Worlds/CON": "CON",
	}
	for in, want := range ok {
		got, err := PathSegment(in)
		if err != nil {
			t.Errorf("PathSegment(%q) errored: %v; it must stay legal", in, err)
			continue
		}
		if got != want {
			t.Errorf("PathSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateWorldDisplayNameAcceptsChinese is the plan's explicit
// requirement: WorldName is user-facing and may be any language. The directory
// validators were tightened, so this pins that the DISPLAY name was not.
func TestValidateWorldDisplayNameAcceptsChinese(t *testing.T) {
	ok := []string{
		"世界",
		"我的世界",
		"存档 A",
		"Мой мир",
		"サーバー",
		"ScWorld",
		"a b",
	}
	for _, name := range ok {
		if got := ValidateWorldDisplayName(name); len(got) > 0 {
			t.Errorf("ValidateWorldDisplayName(%q) = %v; a display name must accept any language", name, got)
		}
	}
	// The display name is a display name, so a NUL there is still refused —
	// that is a pre-existing rule, restated here so the two stay distinct.
	if got := ValidateWorldDisplayName("a\x00b"); len(got) == 0 {
		t.Error("ValidateWorldDisplayName accepted a NUL byte; it must still refuse control characters")
	}
}

// TestWorldDirNameRejectsNUL checks the exported entry point that the panel
// actually calls, so the fix is reachable from the real code path and not only
// from the helper.
func TestWorldDirNameRejectsNUL(t *testing.T) {
	s := DefaultServerSetting()
	s.WorldPath = "app:/Worlds/a\x00b"
	if seg, err := s.WorldDirName(); err == nil {
		t.Errorf("WorldDirName() = %q, nil; want an error for a NUL byte", seg)
	}
	s.WorldPath = "app:/Worlds/世界"
	seg, err := s.WorldDirName()
	if err != nil {
		t.Fatalf("WorldDirName() on a Chinese name errored: %v", err)
	}
	if seg != "世界" {
		t.Errorf("WorldDirName() = %q, want 世界", seg)
	}
}

// TestServerSettingWorldDirNameIsIndependentOfWorldName is the concrete guard
// for the plan's most error-prone point (§2.7, §A.5.1): WorldPath decides the
// directory, WorldName is only a display string.
func TestServerSettingWorldDirNameIsIndependentOfWorldName(t *testing.T) {
	// Exactly the controlled experiment from plan §A.5.1.
	s := DefaultServerSetting()
	s.WorldPath = "app:/Worlds/MyWorldA"
	s.WorldName = "ShowNameX"

	seg, err := s.WorldDirName()
	if err != nil {
		t.Fatalf("WorldDirName: %v", err)
	}
	if seg != "MyWorldA" {
		t.Errorf("directory = %q, want MyWorldA (from WorldPath)", seg)
	}
	if seg == s.WorldName {
		t.Errorf("directory was derived from WorldName (%q); the two fields must stay independent", s.WorldName)
	}

	// Changing ONLY WorldName must not move the directory — this is the bug the
	// plan warns about ("只改 WorldName 不会切存档").
	s.WorldName = "SomethingElse"
	seg2, err := s.WorldDirName()
	if err != nil {
		t.Fatal(err)
	}
	if seg2 != "MyWorldA" {
		t.Errorf("changing WorldName moved the directory to %q; it must stay MyWorldA", seg2)
	}
}

// TestWorldPathForDir round-trips the helper against PathSegment.
func TestWorldPathForDir(t *testing.T) {
	p, err := WorldPathForDir("MyWorldA")
	if err != nil {
		t.Fatalf("WorldPathForDir: %v", err)
	}
	if p != "app:/Worlds/MyWorldA" {
		t.Errorf("WorldPathForDir = %q, want app:/Worlds/MyWorldA", p)
	}
	seg, err := PathSegment(p)
	if err != nil {
		t.Fatalf("PathSegment: %v", err)
	}
	if seg != "MyWorldA" {
		t.Errorf("round trip produced %q", seg)
	}
	if _, err := WorldPathForDir("../escape"); err == nil {
		t.Error("WorldPathForDir accepted a traversal name")
	}
}
