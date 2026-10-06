package world

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realProjectJSON builds a small but realistic Project.json for a world whose
// DIRECTORY is worldDirName and whose DISPLAY name is displayName.
//
// The two are deliberately separate parameters: appendix A.5.1 showed the server
// writes `WorldDirectoryName: "app:/Worlds/MyWorldA"` and
// `WorldName: "ShowNameX"` into the same file, and every path decision must use
// the former. Tests that pass different values for the two are the ones that
// catch a confused implementation.
//
// gameInfoExtra adds fields from the §6.3.1 table.
func realProjectJSON(worldDirName, displayName, guid string, maxPlayers int, gameMode string, extra map[string]any) []byte {
	gi := map[string]any{
		"WorldName":                            []any{"string", displayName},
		"GameMode":                             []any{"Game.GameMode", gameMode},
		"EnvironmentBehaviorMode":              []any{"Game.EnvironmentBehaviorMode", "Living"},
		"TimeOfDayMode":                        []any{"Game.TimeOfDayMode", "Changing"},
		"AreSeasonsChanging":                   []any{"bool", true},
		"YearDays":                             []any{"float", 24},
		"TimeOfYear":                           []any{"float", 0.125},
		"RecoverFator":                         []any{"float", 1},
		"DaySpeed":                             []any{"float", 1},
		"AreWeatherEffectsEnabled":             []any{"bool", true},
		"IsAdventureRespawnAllowed":            []any{"bool", true},
		"AreAdventureSurvivalMechanicsEnabled": []any{"bool", true},
		"AreSupernaturalCreaturesEnabled":      []any{"bool", true},
		"IsFriendlyFireEnabled":                []any{"bool", true},
		"Password":                             []any{"string", ""},
		"RunServer":                            []any{"bool", true},
		"KeywordBlocking":                      []any{"string", ""},
		"WorldSeedString":                      []any{"string", "999"},
		"WorldSeed":                            []any{"int", 5130},
		"TerrainGenerationMode":                []any{"Game.TerrainGenerationMode", "Continent"},
		"IslandSize":                           []any{"Vector2", "400,400"},
		"TerrainLevel":                         []any{"int", 64},
		"ShoreRoughness":                       []any{"float", 0.5},
		"TerrainBlockIndex":                    []any{"int", 8},
		"TerrainOceanBlockIndex":               []any{"int", 18},
		"TemperatureOffset":                    []any{"float", 0},
		"HumidityOffset":                       []any{"float", 0},
		"SeaLevelOffset":                       []any{"int", 0},
		"BiomeSize":                            []any{"float", 1},
		"StartingPositionMode":                 []any{"Game.StartingPositionMode", "Easy"},
		"BlockTextureName":                     []any{"string", ""},
		"Palette":                              []any{"object", map[string]any{"Colors": "", "Names": ""}},
		"MaxOnlinePlayerCount":                 []any{"ushort", maxPlayers},
		"DisableBlocks":                        []any{"string", ""},
		"RandomSpawnPosition":                  []any{"bool", false},
		"WorldDirectoryName":                   []any{"string", "app:/Worlds/" + worldDirName},
		"OriginalSerializationVersion":         []any{"string", "2.4"},
	}
	for k, v := range extra {
		gi[k] = v
	}

	doc := map[string]any{
		"Version": []any{"string", "2.4"},
		"Guid":    []any{"System.Guid", guid},
		"Name":    []any{"string", "GameProject"},
		"Subsystems": map[string]any{
			"GameInfo": gi,
			"Players": map[string]any{
				"BlackPlayerGuidList": map[string]any{},
				"NoMsgPlayerGuidList": map[string]any{},
				"GlobalSpawnPosition": []any{"Vector3", "0,0,0"},
				"PlayersData":         []any{},
				"NextPlayerIndex":     []any{"int", 1},
			},
			"TimeOfDay": map[string]any{
				"TimeOfDay": []any{"double", 0.25},
			},
		},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(err)
	}
	return out
}

// withBOM prefixes a UTF-8 BOM. Building the bytes in Go (rather than relying on
// an editor) is deliberate: the task requires a fixture with an explicit BOM,
// and a file written by an editor would be at the mercy of its BOM setting.
func withBOM(b []byte) []byte {
	return append(append([]byte{}, UTF8BOM...), b...)
}

// makeWorld writes `Worlds/<dirName>/Project.json` (with BOM) plus a small
// Regions/ tree.
func makeWorld(t *testing.T, instanceDir, dirName, displayName, guid string, maxPlayers int, mode string, regions int, regionFileSize int) string {
	t.Helper()
	dir := filepath.Join(instanceDir, WorldsDirName, dirName)
	if err := os.MkdirAll(filepath.Join(dir, RegionsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	body := realProjectJSON(dirName, displayName, guid, maxPlayers, mode, nil)
	if err := os.WriteFile(filepath.Join(dir, ProjectFileName), withBOM(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < regions; i++ {
		name := fmt.Sprintf("chunk-%02d.region", i)
		if err := os.WriteFile(filepath.Join(dir, RegionsDirName, name),
			bytes.Repeat([]byte("R"), regionFileSize), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// makeInstance creates a bare instance directory.
func makeInstance(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// writeServerSetting writes a ServerSetting.json with the given WorldPath and
// WorldName.
func writeServerSetting(t *testing.T, instanceDir, worldPath, worldName string) string {
	t.Helper()
	p := filepath.Join(instanceDir, "ServerSetting.json")
	// Exactly the §2.2 default field set. There is deliberately NO ServerPort
	// here: the port lives in Settings.xml (default 28887), NOT in
	// ServerSetting.json. The world layer must never assume otherwise.
	doc := map[string]any{
		"CheckLogin":           false,
		"ScKeyServerId":        "",
		"ScKeyServerName":      "",
		"Autorun":              true,
		"AutoGenerateWorld":    false,
		"WorldPath":            worldPath,
		"WorldName":            worldName,
		"WorldSeed":            "",
		"WorldPassword":        "",
		"WorldKeywordBlocking": "",
		"WorldMaxPlayers":      20,
		"WorldDaySpeed":        1,
		"WorldRecoverySpeed":   1,
		"WorldDisableBlocks":   "",
		"RandomSpawnPosition":  false,
		"GameMode":             1,
		"SeasonChanging":       true,
		"PVPEnabled":           true,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, withBOM(b), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestProjectJSONWithExplicitBOM is the fixture test required by the task: it
// builds the bytes in Go, with a real BOM, containing §6.3.1 GameInfo fields and
// a Subsystems.Players subtree, and proves the reader handles it.
func TestProjectJSONWithExplicitBOM(t *testing.T) {
	body := realProjectJSON("MyWorldA", "ShowNameX", "9e9a67f8-1111-2222-3333-444455556666", 20, "Harmless", map[string]any{
		"RecoverFator": []any{"float", 1.5},
		"DaySpeed":     []any{"float", 2.0},
	})
	raw := withBOM(body)

	// The fixture must really carry a BOM, and the BOM must really break naive
	// parsing — otherwise this test proves nothing.
	if !bytes.HasPrefix(raw, UTF8BOM) {
		t.Fatal("fixture is missing its BOM")
	}
	var naive map[string]any
	if err := json.Unmarshal(raw, &naive); err == nil {
		t.Fatal("encoding/json accepted a BOM-prefixed document; the fixture no longer exercises BOM handling")
	}

	path := filepath.Join(t.TempDir(), ProjectFileName)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := ReadProject(path)
	if err != nil {
		t.Fatalf("ReadProject on a BOM-prefixed file: %v", err)
	}
	if !p.HadBOM() {
		t.Error("HadBOM() = false; the reader did not notice the BOM")
	}
	// The stripped body must be valid JSON on its own.
	if !json.Valid(p.Raw()) {
		t.Error("Raw() is not valid JSON after BOM stripping")
	}

	// --- GameInfo fields from §6.3.1 ---
	if got, ok := p.GameInfoString("WorldName"); !ok || got != "ShowNameX" {
		t.Errorf("GameInfo.WorldName = %q, %v; want ShowNameX", got, ok)
	}
	if got, ok := p.GameInfoString("GameMode"); !ok || got != "Harmless" {
		t.Errorf("GameInfo.GameMode = %q, %v; want the STRING Harmless", got, ok)
	}
	if got, ok := p.GameInfoString("WorldDirectoryName"); !ok || got != "app:/Worlds/MyWorldA" {
		t.Errorf("GameInfo.WorldDirectoryName = %q, %v", got, ok)
	}
	if got, ok := p.IntAt(p.GameInfo(), "MaxOnlinePlayerCount"); !ok || got != 20 {
		t.Errorf("MaxOnlinePlayerCount = %d, %v; want 20", got, ok)
	}
	if got, ok := p.GameInfoString("WorldSeedString"); !ok || got != "999" {
		t.Errorf("WorldSeedString = %q, %v; want 999", got, ok)
	}
	if got, ok := p.IntAt(p.GameInfo(), "WorldSeed"); !ok || got != 5130 {
		t.Errorf("WorldSeed = %d, %v; want the derived 5130", got, ok)
	}
	if got, ok := p.BoolAt(p.GameInfo(), "AreSeasonsChanging"); !ok || !got {
		t.Errorf("AreSeasonsChanging = %v, %v; want true", got, ok)
	}
	if got, ok := p.FloatAt(p.GameInfo(), "RecoverFator"); !ok || got != 1.5 {
		t.Errorf("RecoverFator = %v, %v; want 1.5", got, ok)
	}
	if got, ok := p.FloatAt(p.GameInfo(), "DaySpeed"); !ok || got != 2.0 {
		t.Errorf("DaySpeed = %v, %v; want 2.0", got, ok)
	}
	if got, ok := p.GameInfoString("TerrainGenerationMode"); !ok || got != "Continent" {
		t.Errorf("TerrainGenerationMode = %q, %v", got, ok)
	}
	if got, ok := p.GameInfoString("StartingPositionMode"); !ok || got != "Easy" {
		t.Errorf("StartingPositionMode = %q, %v", got, ok)
	}
	if got, ok := p.GameInfoString("IslandSize"); !ok || got != "400,400" {
		t.Errorf("IslandSize = %q, %v", got, ok)
	}

	// --- top-level identity ---
	if got, ok := p.TopLevelString("Guid"); !ok || got != "9e9a67f8-1111-2222-3333-444455556666" {
		t.Errorf("Guid = %q, %v", got, ok)
	}
	if got, ok := p.TopLevelString("Name"); !ok || got != "GameProject" {
		t.Errorf("Name = %q, %v", got, ok)
	}
	if got, ok := p.TopLevelString("Version"); !ok || got != "2.4" {
		t.Errorf("Version = %q, %v", got, ok)
	}

	// --- Subsystems.Players subtree ---
	players := p.Players()
	if players == nil {
		t.Fatal("Subsystems.Players is missing")
	}
	for _, key := range []string{"BlackPlayerGuidList", "NoMsgPlayerGuidList", "GlobalSpawnPosition", "PlayersData", "NextPlayerIndex"} {
		if _, present := players[key]; !present {
			t.Errorf("Subsystems.Players is missing %q", key)
		}
	}
	if got, ok := p.IntAt(players, "NextPlayerIndex"); !ok || got != 1 {
		t.Errorf("Players.NextPlayerIndex = %d, %v; want 1", got, ok)
	}
	if p.Subsystems() == nil {
		t.Error("Subsystems() returned nil")
	}
}

// TestReadProjectWithoutBOM proves a BOM-less file still parses (and reports
// HadBOM false), because a hand-edited or third-party save may lack it.
func TestReadProjectWithoutBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), ProjectFileName)
	body := realProjectJSON("W", "W", "g", 10, "Survival", nil)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := ReadProject(path)
	if err != nil {
		t.Fatalf("ReadProject on a BOM-less file: %v", err)
	}
	if p.HadBOM() {
		t.Error("HadBOM() = true for a file with no BOM")
	}
	if got, _ := p.GameInfoString("GameMode"); got != "Survival" {
		t.Errorf("GameMode = %q", got)
	}
}

// TestReadProjectErrors covers the missing, empty, truncated, UTF-16 and
// non-object cases.
func TestReadProjectErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := ReadProject(filepath.Join(dir, "nope.json")); err == nil {
		t.Error("ReadProject on a missing file succeeded")
	} else if !strings.Contains(err.Error(), ErrNoProjectFile.Error()) {
		t.Errorf("error = %v; want ErrNoProjectFile", err)
	}

	bad := map[string][]byte{
		"empty.json":     {},
		"bomonly.json":   withBOM(nil),
		"truncated.json": withBOM([]byte(`{"Version":["string","2.4"]`)),
		"garbage.json":   withBOM([]byte("not json at all")),
		"utf16.json":     append([]byte{0xFF, 0xFE}, []byte(`{"a":1}`)...),
	}
	for name, data := range bad {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadProject(p); err == nil {
			t.Errorf("ReadProject(%s) succeeded; want an error", name)
		}
	}

	// A JSON array at the root is not a project document.
	arr := filepath.Join(dir, "array.json")
	if err := os.WriteFile(arr, withBOM([]byte(`[1,2,3]`)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProject(arr); err == nil {
		t.Error("ReadProject accepted a JSON array as a project")
	}

	// A UTF-16 file must be reported clearly rather than as a syntax error.
	p16 := filepath.Join(dir, "u16.json")
	if err := os.WriteFile(p16, append([]byte{0xFF, 0xFE}, []byte("{}")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProject(p16); err == nil || !strings.Contains(err.Error(), "UTF-16") {
		t.Errorf("UTF-16 error = %v; want a UTF-16-specific message", err)
	}
}

// TestParseProjectRawBytes exercises the parser directly, including the BOM.
func TestParseProjectRawBytes(t *testing.T) {
	body := []byte(`{"Subsystems":{"GameInfo":{"WorldName":["string","X"]}}}`)
	for _, tc := range []struct {
		name   string
		data   []byte
		hadBOM bool
	}{
		{"with-bom", withBOM(body), true},
		{"without-bom", body, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseProject("fake", tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if p.HadBOM() != tc.hadBOM {
				t.Errorf("HadBOM = %v; want %v", p.HadBOM(), tc.hadBOM)
			}
			if got, _ := p.GameInfoString("WorldName"); got != "X" {
				t.Errorf("WorldName = %q", got)
			}
		})
	}
}

// TestAnnotatedToleratesUnannotatedValues proves the reader is not brittle about
// the type-annotation wrapper.
func TestAnnotatedToleratesUnannotatedValues(t *testing.T) {
	body := []byte(`{"Subsystems":{"GameInfo":{
		"PlainString":"bare",
		"Annotated":["string","wrapped"],
		"PlainInt":42,
		"PlainBool":true,
		"OneElement":["string"],
		"ThreeElements":["a","b","c"]
	}}}`)
	p, err := parseProject("fake", body)
	if err != nil {
		t.Fatal(err)
	}
	gi := p.GameInfo()
	if got, ok := p.StringAt(gi, "PlainString"); !ok || got != "bare" {
		t.Errorf("PlainString = %q, %v", got, ok)
	}
	if got, ok := p.StringAt(gi, "Annotated"); !ok || got != "wrapped" {
		t.Errorf("Annotated = %q, %v", got, ok)
	}
	if got, ok := p.IntAt(gi, "PlainInt"); !ok || got != 42 {
		t.Errorf("PlainInt = %d, %v", got, ok)
	}
	if got, ok := p.BoolAt(gi, "PlainBool"); !ok || !got {
		t.Errorf("PlainBool = %v, %v", got, ok)
	}
	// `["string"]` is a type annotation with NO value. Reading it as the value
	// "string" would make the panel display the literal word "string" as a
	// world name, so it must report absence.
	if got, ok := p.StringAt(gi, "OneElement"); ok {
		t.Errorf("OneElement = %q, %v; want absent (a value-less annotation)", got, ok)
	}
	// Get surfaces the type for diagnostics even though there is no value; the
	// ok flag is what callers must branch on.
	if typ, val, ok := p.Get(gi, "OneElement"); ok || typ != "string" || val != nil {
		t.Errorf("Get(OneElement) = %q, %v, %v; want the type with a nil value and ok=false", typ, val, ok)
	}
	// A three-element array is a genuine list, not an annotation.
	if _, val, ok := p.Get(gi, "ThreeElements"); !ok {
		t.Error("a three-element array should be readable as a bare value")
	} else if arr, isArr := val.([]any); !isArr || len(arr) != 3 {
		t.Errorf("ThreeElements value = %#v; want the 3-element list", val)
	}
	if _, ok := p.StringAt(gi, "Missing"); ok {
		t.Error("a missing field reported as present")
	}
	if _, ok := p.StringAt(nil, "Anything"); ok {
		t.Error("a nil subtree reported a field as present")
	}
}

// TestNullValuedAnnotationIsPresentButNil pins the distinction the format needs:
// a field written as `["string", null]` HAS a type annotation and a present key,
// but no value. That is different from `["string"]`, which is a value-less
// annotation. Callers must branch on the ok flag, and must not receive the
// literal string "string" as a value — which is what a naive unwrapper returns
// and what would make the panel display "string" as a world name.
func TestNullValuedAnnotationIsPresentButNil(t *testing.T) {
	body := []byte(`{"Subsystems":{"GameInfo":{
		"NullValued":["string",null],
		"OneElem":["string"],
		"ThreeElem":["a","b","c"],
		"Plain":["string","x"]
	}}}`)
	p, err := parseProject("fake", body)
	if err != nil {
		t.Fatal(err)
	}
	gi := p.GameInfo()

	// `["string", null]`: present, typed, nil value.
	typ, val, ok := p.Get(gi, "NullValued")
	if !ok {
		t.Error("Get([\"string\",null]) reported the field absent; it is present with a nil value")
	}
	if typ != "string" {
		t.Errorf("type = %q; want string", typ)
	}
	if val != nil {
		t.Errorf("value = %#v; want nil", val)
	}
	// Reading it AS a string must report absence rather than a bogus "".
	if sv, sok := p.StringAt(gi, "NullValued"); sok {
		t.Errorf("StringAt([\"string\",null]) = %q, true; want absent", sv)
	}

	// `["string"]`: a value-less annotation. Reading it must NOT yield the
	// literal "string".
	if sv, sok := p.StringAt(gi, "OneElem"); sok {
		t.Errorf("StringAt([\"string\"]) = %q, true; want absent (there is no value)", sv)
	}

	// A three-element array is a genuine list value.
	if _, lval, lok := p.Get(gi, "ThreeElem"); !lok {
		t.Error("Get(three-element array) reported absent")
	} else if arr, isArr := lval.([]any); !isArr || len(arr) != 3 {
		t.Errorf("three-element array = %#v; want the 3-element list", lval)
	}

	// The ordinary case still works.
	if sv, sok := p.StringAt(gi, "Plain"); !sok || sv != "x" {
		t.Errorf("StringAt([\"string\",\"x\"]) = %q, %v; want x", sv, sok)
	}
}

// TestToIntCoversAllForms pins the numeric coercions.
func TestToIntCoversAllForms(t *testing.T) {
	cases := []struct {
		in   any
		want int
		ok   bool
	}{
		{json.Number("42"), 42, true},
		{json.Number("42.9"), 42, true},
		{json.Number("nope"), 0, false},
		{float64(7), 7, true},
		{int(8), 8, true},
		{int64(9), 9, true},
		{true, 1, true},
		{false, 0, true},
		{"13", 13, true},
		{"not-a-number", 0, false},
		{nil, 0, false},
	}
	for _, tc := range cases {
		got, ok := toInt(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("toInt(%#v) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestStampNowIsIndirectable keeps the backup-name seam working.
func TestStampNowIsIndirectable(t *testing.T) {
	orig := stampNow
	t.Cleanup(func() { stampNow = orig })
	fixed := time.Date(2026, 10, 6, 12, 34, 56, 0, time.UTC)
	stampNow = func() time.Time { return fixed }
	if got := nowStamp(); got != "20261006-123456" {
		t.Fatalf("nowStamp() = %q; want 20261006-123456", got)
	}
}
