// Package config reads and writes the SurvivalcraftNet game-server configuration
// files managed by the scnetm panel.
//
// Three on-disk formats are covered:
//
//   - ServerSetting.json — the server's primary start-up configuration. It is
//     plain, human readable JSON. GameMode is stored here as an INTEGER.
//   - Settings.xml — the engine settings file (ServerPort and many others).
//     It is edited in place so that unrelated content survives untouched.
//   - Worlds/<dir>/Project.json — the save file. It is a BOM-prefixed,
//     type-annotated JSON where every value is a ["type", value] pair. Here
//     GameMode is stored as a STRING enum name.
//
// Frozen policy (see plan §6.3.1): ServerSetting.json is the single source of
// write truth. Save-file editing is read-only display plus advanced edits that
// require an explicit opt-in and a stopped instance.
package config

import "fmt"

// The GameMode integer <-> enum-name mapping (plan decision D5).
//
// # Inference warning — NOT fully verified
//
// ServerSetting.json stores GameMode as an integer while a save's Project.json
// stores it as a string enum name (["Game.GameMode","Harmless"]). The mapping
// implemented here is an INFERENCE derived from the plan's evidence:
//
//   - verified anchor: the server was started with `"GameMode": 1` and logged
//     `Loaded world, GameMode=Harmless` (plan §A.7).
//   - SubsystemMatchBlockBehavior.cs:65 compares `GameMode < GameMode.Challenging`,
//     proving the enum is ordered by declaration order.
//   - the assembly strings contain Creative / Harmless / Survival / Challenging / Cruel.
//
// Only GameMode 1 == "Harmless" is EMPIRICALLY CONFIRMED. Everything else is
// inferred and must be re-validated (plan task V0-6) by starting an instance for
// each value and comparing the `Loaded world, GameMode=XXX` log line. Until then
// UI surfaces must label non-verified values as "inferred".
//
// Out-of-range integers are never dropped: they are preserved verbatim and
// round-tripped so that a server-upgrade enum change cannot lose data.
const (
	// GameModeCreative is the inferred name for GameMode 0.
	GameModeCreative = "Creative"
	// GameModeHarmless is the name for GameMode 1 — the ONLY verified entry.
	GameModeHarmless = "Harmless"
	// GameModeSurvival is the inferred name for GameMode 2.
	GameModeSurvival = "Survival"
	// GameModeChallenging is the inferred name for GameMode 3.
	GameModeChallenging = "Challenging"
	// GameModeCruel is the inferred name for GameMode 4.
	GameModeCruel = "Cruel"
	// GameModeAdventure is the inferred name for GameMode 5.
	GameModeAdventure = "Adventure"
	// GameModeMirror is the inferred name for GameMode 6. Its meaning is
	// uncertain (plan §6.2.1 marks it 存疑).
	GameModeMirror = "Mirror"
)

// verifiedGameModes lists the only integer whose name is empirically confirmed.
// Keep this set as small as the evidence demands.
var verifiedGameModes = map[int]bool{1: true}

// gameModeNames is the inferred mapping, ordered by declaration order exactly
// as the plan's D5 table lists it.
var gameModeNames = []string{
	GameModeCreative,    // 0
	GameModeHarmless,    // 1
	GameModeSurvival,    // 2
	GameModeChallenging, // 3
	GameModeCruel,       // 4
	GameModeAdventure,   // 5
	GameModeMirror,      // 6
}

// gameModeIndex maps an enum name back to its inferred integer.
var gameModeIndex = func() map[string]int {
	m := make(map[string]int, len(gameModeNames))
	for i, n := range gameModeNames {
		m[n] = i
	}
	return m
}()

// gameModeMin and gameModeMax bound the inferred mapping.
const (
	gameModeMin = 0
	gameModeMax = 6 // len(gameModeNames)-1, kept literal so it stays constant
)

// compile-time guard: gameModeMax must match the table above.
var _ = [1]struct{}{}[gameModeMax+1-len(gameModeNames)]

// GameModeNames returns the inferred enum names ordered by their integer value,
// i.e. index i holds the name for GameMode == i.
//
// The returned slice is a copy; callers may mutate it freely.
func GameModeNames() []string {
	out := make([]string, len(gameModeNames))
	copy(out, gameModeNames)
	return out
}

// GameModeIsVerified reports whether the integer -> name mapping for v has been
// empirically confirmed against a running server. It is true ONLY for v == 1
// (Harmless); every other value is an inference pending plan task V0-6.
func GameModeIsVerified(v int) bool { return verifiedGameModes[v] }

// GameModeName maps a ServerSetting.json integer to the enum name used inside a
// save's Project.json.
//
// For an in-range value it returns the inferred name. For an out-of-range value
// (a future server enum member) it returns a synthetic, stable placeholder of
// the form "Unknown(<v>)" plus ok=false — the caller can round-trip it through
// GameModeValue without losing information. ok is also false when the mapping
// is merely inferred, so callers can surface the "inferred" marker in a UI.
func GameModeName(v int) (name string, ok bool) {
	if v >= gameModeMin && v <= gameModeMax {
		return gameModeNames[v], GameModeIsVerified(v)
	}
	return fmt.Sprintf("Unknown(%d)", v), false
}

// GameModeValue maps a save's string enum name back to the integer stored in
// ServerSetting.json.
//
// It accepts the names returned by GameModeName as well as the "Unknown(<v>)"
// placeholder, so an unknown value survives a full round trip. The boolean
// reports whether the mapping is empirically verified (true only for Harmless).
func GameModeValue(name string) (v int, ok bool) {
	if i, found := gameModeIndex[name]; found {
		return i, GameModeIsVerified(i)
	}
	var n int
	if _, err := fmt.Sscanf(name, "Unknown(%d)", &n); err == nil {
		if fmt.Sprintf("Unknown(%d)", n) == name {
			return n, false
		}
	}
	return 0, false
}

// IsKnownGameMode reports whether v falls inside the inferred mapping range.
func IsKnownGameMode(v int) bool { return v >= gameModeMin && v <= gameModeMax }

// ValidateGameMode reports whether v is usable as a ServerSetting.json GameMode.
// In-range values pass; anything else is rejected with an explanatory message so
// the caller can present it to a user, while GameModeName/GameModeValue remain
// lossless for round-tripping existing data.
func ValidateGameMode(v int) error {
	if IsKnownGameMode(v) {
		return nil
	}
	names := ""
	for i, n := range gameModeNames {
		if i > 0 {
			names += ", "
		}
		names += fmt.Sprintf("%d=%s", i, n)
	}
	return fmt.Errorf("config: GameMode %d is outside the known range [%d..%d] (%s); "+
		"the mapping is inferred pending plan task V0-6",
		v, gameModeMin, gameModeMax, names)
}
