//go:build ignore

// gen_fixtures writes the internal/config/testdata fixtures with exact bytes.
//
// The BOM in Project.json is emitted explicitly here rather than trusted to an
// editor: a stray editor save would silently strip it and the BOM round-trip
// test would then be testing nothing. Run with:
//
//	go run gen_fixtures.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const bom = "\ufeff"

// annotation is one ["type", value] pair in a save's Project.json.
type annotation = []any

func a(typ string, value any) annotation { return annotation{typ, value} }

func main() {
	if err := writeServerSetting(); err != nil {
		panic(err)
	}
	if err := writeSettingsXML(); err != nil {
		panic(err)
	}
	if err := writeSettingsXMLOdd(); err != nil {
		panic(err)
	}
	if err := writeProject(); err != nil {
		panic(err)
	}
	fmt.Println("fixtures written")
}

// writeServerSetting emits a ServerSetting.json matching plan §2.2, plus one
// unknown future key that the panel must round-trip untouched.
func writeServerSetting() error {
	obj := map[string]any{
		"CheckLogin":           false,
		"ScKeyServerId":        "",
		"ScKeyServerName":      "",
		"Autorun":              false,
		"AutoGenerateWorld":    false,
		"WorldPath":            "app:/Worlds/MyWorldA",
		"WorldName":            "ShowNameX",
		"WorldSeed":            "999",
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

		// Unknown to this panel version: a field a future server build added.
		// The panel must preserve it verbatim across a load -> save round trip.
		"SomeFutureOption": 42,
	}
	// Marshal with sorted keys for a stable fixture (encoding/json sorts map
	// keys), then re-indent by hand so the file reads like a real config.
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("testdata/ServerSetting.json", append(data, '\n'), 0o644)
}

// writeSettingsXML emits a Settings.xml with ServerPort plus unrelated entries
// in a specific order, mirroring the engine's file.
func writeSettingsXML() error {
	doc := `<?xml version="1.0" encoding="utf-8"?>
<Settings>
  <ServerPort Value="28887" />
  <BroadcastPort Value="28888" />
  <VisibilityRange Value="128" />
  <Resolution Value="High" />
  <Language Value="zh-CN" />
  <MaxFPS Value="60" />
  <EnableSound Value="true" />
  <WillEnterServer Value="false" />
</Settings>
`
	return os.WriteFile("testdata/Settings.xml", []byte(doc), 0o644)
}

// writeSettingsXMLOdd emits a deliberately awkward Settings.xml: a BOM, a
// comment, CRLF line endings, mixed attribute quoting, unusual indentation and
// out-of-order entries. A Set("ServerPort", ...) must leave every other byte
// alone, so this fixture is the regression guard for that promise.
func writeSettingsXMLOdd() error {
	doc := bom + "<?xml version=\"1.0\" encoding=\"utf-8\"?>\r\n" +
		"<!-- Hand-edited by an operator. Do not reformat; the engine is picky. -->\r\n" +
		"<Settings>\r\n" +
		"\t<VisibilityRange Value=\"128\" />\r\n" +
		"        <ServerPort    Value='28887'   />\r\n" +
		"\t<Resolution Value=\"High\"></Resolution>\r\n" +
		"  <Language Value=\"zh-CN\" />\r\n" +
		"<!-- port comment -->\r\n" +
		"\t\t<MaxFPS Value=\"60\"/>\r\n" +
		"  <EnableSound Value=\"true\" />\r\n" +
		"<WillEnterServer Value=\"false\" />\r\n" +
		"</Settings>\r\n"
	return os.WriteFile("testdata/Settings.odd.xml", []byte(doc), 0o644)
}

// writeProject emits a save file with a real UTF-8 BOM and the type-annotated
// structure measured in plan §6.3.1 / §A.5.2, including the Subsystems.Players
// subtree and several fields the panel does not model.
func writeProject() error {
	gameInfo := map[string]any{
		// Fields the panel surfaces.
		"WorldName":                            a("string", "ShowNameX"),
		"GameMode":                             a("Game.GameMode", "Harmless"),
		"EnvironmentBehaviorMode":              a("Game.EnvironmentBehaviorMode", "Living"),
		"TimeOfDayMode":                        a("Game.TimeOfDayMode", "Changing"),
		"AreSeasonsChanging":                   a("bool", true),
		"YearDays":                             a("float", 24),
		"TimeOfYear":                           a("float", 0.125),
		"RecoverFator":                         a("float", 1),
		"DaySpeed":                             a("float", 1),
		"AreWeatherEffectsEnabled":             a("bool", true),
		"IsAdventureRespawnAllowed":            a("bool", true),
		"AreAdventureSurvivalMechanicsEnabled": a("bool", true),
		"AreSupernaturalCreaturesEnabled":      a("bool", true),
		"IsFriendlyFireEnabled":                a("bool", true),
		"Password":                             a("string", ""),
		"RunServer":                            a("bool", true),
		"KeywordBlocking":                      a("string", ""),
		"WorldSeedString":                      a("string", "999"),
		// Derived: input "999" produced 5130 (plan §6.3.1). Read-only.
		"WorldSeed":                    a("int", 5130),
		"TerrainGenerationMode":        a("Game.TerrainGenerationMode", "Continent"),
		"IslandSize":                   a("Vector2", "400,400"),
		"TerrainLevel":                 a("int", 64),
		"ShoreRoughness":               a("float", 0.5),
		"TerrainBlockIndex":            a("int", 8),
		"TerrainOceanBlockIndex":       a("int", 18),
		"TemperatureOffset":            a("float", 0),
		"HumidityOffset":               a("float", 0),
		"SeaLevelOffset":               a("int", 0),
		"BiomeSize":                    a("float", 1),
		"StartingPositionMode":         a("Game.StartingPositionMode", "Easy"),
		"BlockTextureName":             a("string", ""),
		"Palette":                      a("object", map[string]any{"Colors": "", "Names": ""}),
		"MaxOnlinePlayerCount":         a("ushort", 20),
		"DisableBlocks":                a("string", ""),
		"RandomSpawnPosition":          a("bool", false),
		"WorldDirectoryName":           a("string", "app:/Worlds/MyWorldA"),
		"OriginalSerializationVersion": a("string", "2.4"),
	}

	// Fields present in a real save that the panel deliberately does NOT model.
	// Their survival across a write is part of the contract.
	otherSubsystems := map[string]any{
		"GameInfo": gameInfo,
		"Players": map[string]any{
			"BlackPlayerGuidList": map[string]any{},
			"NoMsgPlayerGuidList": map[string]any{},
			"GlobalSpawnPosition": a("Vector3", map[string]any{"X": 0.5, "Y": 12.0, "Z": 0.5}),
			"NextPlayerGuid":      a("System.Guid", "5c1e0e0f-6c11-4a55-9f2b-2a4a2c9e10ab"),
			"PlayerDataList":      []any{},
		},
		"Time": map[string]any{
			"GameTime": a("double", 1234.5),
		},
	}

	// Hand-roll the JSON so key ORDER is stable and readable, and so the BOM is
	// explicit rather than an artefact of the marshaller.
	var b strings.Builder
	b.WriteString(bom)
	b.WriteString("{\n")
	b.WriteString("  \"Version\": " + mustJSON(a("string", "2.4")) + ",\n")
	b.WriteString("  \"Guid\": " + mustJSON(a("System.Guid", "9e9a67f8-3d3e-4a5f-9c1a-7b6d5e4f3a21")) + ",\n")
	b.WriteString("  \"Name\": " + mustJSON(a("string", "GameProject")) + ",\n")
	b.WriteString("  \"Subsystems\": {\n")
	b.WriteString("    \"GameInfo\": {\n")

	order := []string{
		"WorldName", "GameMode", "EnvironmentBehaviorMode", "TimeOfDayMode",
		"AreSeasonsChanging", "YearDays", "TimeOfYear", "RecoverFator", "DaySpeed",
		"AreWeatherEffectsEnabled", "IsAdventureRespawnAllowed",
		"AreAdventureSurvivalMechanicsEnabled", "AreSupernaturalCreaturesEnabled",
		"IsFriendlyFireEnabled", "Password", "RunServer", "KeywordBlocking",
		"WorldSeedString", "WorldSeed", "TerrainGenerationMode", "IslandSize",
		"TerrainLevel", "ShoreRoughness", "TerrainBlockIndex",
		"TerrainOceanBlockIndex", "TemperatureOffset", "HumidityOffset",
		"SeaLevelOffset", "BiomeSize", "StartingPositionMode", "BlockTextureName",
		"Palette", "MaxOnlinePlayerCount", "DisableBlocks",
		"RandomSpawnPosition", "WorldDirectoryName",
		"OriginalSerializationVersion",
	}
	for i, k := range order {
		v, ok := gameInfo[k]
		if !ok {
			panic("fixture field missing: " + k)
		}
		sep := ","
		if i == len(order)-1 {
			sep = ""
		}
		b.WriteString("      " + mustJSON(k) + ": " + mustJSON(v) + sep + "\n")
	}
	b.WriteString("    },\n")
	b.WriteString("    \"Players\": " + mustJSON(otherSubsystems["Players"]) + ",\n")
	b.WriteString("    \"Time\": " + mustJSON(otherSubsystems["Time"]) + "\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")

	return os.WriteFile("testdata/Project.json", []byte(b.String()), 0o644)
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}
