package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestGameModeNamesOrder pins the inferred D5 table order, which is the whole
// basis of the integer <-> name mapping.
func TestGameModeNamesOrder(t *testing.T) {
	want := []string{"Creative", "Harmless", "Survival", "Challenging", "Cruel", "Adventure", "Mirror"}
	got := GameModeNames()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GameModeNames() = %v, want %v", got, want)
	}
	// A mutation of the returned slice must not affect the package.
	got[0] = "Tampered"
	if GameModeNames()[0] != "Creative" {
		t.Error("GameModeNames returned a slice aliasing the internal table")
	}
}

// TestGameModeOnlyHarmlessIsVerified encodes the honest state of the evidence:
// only GameMode 1 == Harmless has ever been observed on a running server.
func TestGameModeOnlyHarmlessIsVerified(t *testing.T) {
	for v := 0; v <= gameModeMax; v++ {
		want := v == 1
		if got := GameModeIsVerified(v); got != want {
			t.Errorf("GameModeIsVerified(%d) = %v, want %v", v, got, want)
		}
	}
	for _, v := range []int{-1, 7, 99, 1000} {
		if GameModeIsVerified(v) {
			t.Errorf("GameModeIsVerified(%d) = true; out-of-range values can never be verified", v)
		}
	}
}

// TestGameModeIntToStringMapping is requirement 2: the bidirectional mapping
// between the INTEGER in ServerSetting.json and the STRING name in a save.
func TestGameModeIntToStringMapping(t *testing.T) {
	cases := []struct {
		v        int
		want     string
		verified bool
	}{
		{0, "Creative", false},
		{1, "Harmless", true}, // the empirical anchor
		{2, "Survival", false},
		{3, "Challenging", false},
		{4, "Cruel", false},
		{5, "Adventure", false},
		{6, "Mirror", false},
	}
	for _, c := range cases {
		name, ok := GameModeName(c.v)
		if name != c.want {
			t.Errorf("GameModeName(%d) = %q, want %q", c.v, name, c.want)
		}
		if ok != c.verified {
			t.Errorf("GameModeName(%d) verified = %v, want %v", c.v, ok, c.verified)
		}

		back, backOK := GameModeValue(c.want)
		if back != c.v {
			t.Errorf("GameModeValue(%q) = %d, want %d", c.want, back, c.v)
		}
		if backOK != c.verified {
			t.Errorf("GameModeValue(%q) verified = %v, want %v", c.want, backOK, c.verified)
		}
	}
}

// TestGameModeOutOfRangeRoundTrips is the "preserve, do not drop" requirement:
// a value a future server build adds must survive a name round trip rather than
// collapsing to 0.
func TestGameModeOutOfRangeRoundTrips(t *testing.T) {
	for _, v := range []int{7, 8, 42, 255, 1000, -3} {
		name, ok := GameModeName(v)
		if ok {
			t.Errorf("GameModeName(%d) reported verified=true for an out-of-range value", v)
		}
		if !strings.HasPrefix(name, "Unknown(") {
			t.Errorf("GameModeName(%d) = %q, want an Unknown(<v>) placeholder", v, name)
		}
		back, _ := GameModeValue(name)
		if back != v {
			t.Errorf("out-of-range value did not round trip: %d -> %q -> %d", v, name, back)
		}
		if IsKnownGameMode(v) {
			t.Errorf("IsKnownGameMode(%d) = true, want false", v)
		}
	}
}

// TestGameModeValueUnknownNames documents the failure mode for a name this
// panel does not know at all (a renamed or brand new enum member).
func TestGameModeValueUnknownNames(t *testing.T) {
	for _, n := range []string{"", "NotAMode", "unknown(3)", "Unknown(x)", "Harmless "} {
		v, ok := GameModeValue(n)
		if ok {
			t.Errorf("GameModeValue(%q) reported verified=true", n)
		}
		if v != 0 {
			t.Errorf("GameModeValue(%q) = %d, want 0 for an unparseable name", n, v)
		}
	}
	// Case matters: the engine writes the exact enum name.
	if v, _ := GameModeValue("harmless"); v != 0 {
		t.Errorf("GameModeValue(\"harmless\") = %d; the match must be case-sensitive like the enum name", v)
	}
}

// TestValidateGameMode checks the range guard and that its message points at the
// verification task rather than pretending certainty.
func TestValidateGameMode(t *testing.T) {
	for v := 0; v <= gameModeMax; v++ {
		if err := ValidateGameMode(v); err != nil {
			t.Errorf("ValidateGameMode(%d) = %v, want nil", v, err)
		}
	}
	for _, v := range []int{-1, 7, 1000} {
		err := ValidateGameMode(v)
		if err == nil {
			t.Fatalf("ValidateGameMode(%d) = nil, want an error", v)
		}
		if !strings.Contains(err.Error(), "V0-6") {
			t.Errorf("ValidateGameMode(%d) error %q should reference the pending verification task", v, err)
		}
	}
}
