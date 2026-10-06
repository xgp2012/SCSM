package config

import (
	"strings"
	"testing"
)

// findProblem returns the first problem for a field, or nil.
func findProblem(problems []Problem, field string) *Problem {
	for i := range problems {
		if problems[i].Field == field {
			return &problems[i]
		}
	}
	return nil
}

func hasCode(problems []Problem, code string) bool {
	for _, p := range problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

// TestValidatePort covers the range check and the UDP-occupancy hook.
func TestValidatePort(t *testing.T) {
	// In range, free.
	if got := ValidatePort(28887, func(int) bool { return false }); len(got) != 0 {
		t.Errorf("ValidatePort(28887, free) = %v, want no problems", got)
	}
	// In range, occupied.
	got := ValidatePort(28887, func(p int) bool { return p == 28887 })
	if len(got) != 1 {
		t.Fatalf("ValidatePort(occupied) = %v, want exactly one problem", got)
	}
	if got[0].Code != "port_taken" || got[0].Severity != SeverityError {
		t.Errorf("occupied port problem = %+v, want code=port_taken severity=error", got[0])
	}
	if got[0].Field != "ServerPort" {
		t.Errorf("problem field = %q, want ServerPort", got[0].Field)
	}

	// Out of range: rejected before the occupancy hook is even consulted.
	called := false
	got = ValidatePort(0, func(int) bool { called = true; return false })
	if len(got) == 0 || got[0].Code != "port_range" {
		t.Errorf("ValidatePort(0) = %v, want a port_range problem", got)
	}
	if called {
		t.Error("the occupancy hook was consulted for an out-of-range port")
	}
	for _, p := range []int{-1, 65536, 99999} {
		if got := ValidatePort(p, nil); len(got) == 0 {
			t.Errorf("ValidatePort(%d) accepted an out-of-range port", p)
		}
	}
	// Boundaries are legal.
	for _, p := range []int{MinPort, MaxPort} {
		if got := ValidatePort(p, nil); len(got) != 0 {
			t.Errorf("ValidatePort(%d) = %v, want no problems at the boundary", p, got)
		}
	}
	// A nil hook must skip occupancy checking, not panic.
	if got := ValidatePort(28887, nil); len(got) != 0 {
		t.Errorf("ValidatePort with a nil hook = %v", got)
	}
}

// TestValidateWorldDirName is the plan §6.2 requirement: the WorldPath's last
// segment must not contain a path separator or "..".
func TestValidateWorldDirName(t *testing.T) {
	good := []string{"World", "MyWorldA", "world_1", "世界", "new world", "a-b.c"}
	for _, n := range good {
		if got := ValidateWorldDirName(n); len(got) != 0 {
			t.Errorf("ValidateWorldDirName(%q) = %v, want no problems", n, got)
		}
	}

	bad := []struct {
		name string
		code string
	}{
		{"", "dir_empty"},
		{"a/b", "dir_separator"},
		{"a\\b", "dir_separator"},
		{"/etc", "dir_separator"},
		{"..", "dir_traversal"},
		{"..etc", "dir_traversal"},
		{"a..b", "dir_traversal"},
		{".", "dir_traversal"},
		{"bad\x00name", "dir_control"},
		{"bad:name", "dir_reserved"},
		{"bad*name", "dir_reserved"},
		{strings.Repeat("x", MaxWorldDirNameLength+1), "dir_too_long"},
	}
	for _, c := range bad {
		got := ValidateWorldDirName(c.name)
		if len(got) == 0 {
			t.Errorf("ValidateWorldDirName(%q) accepted an illegal name", c.name)
			continue
		}
		if got[0].Code != c.code {
			t.Errorf("ValidateWorldDirName(%q) code = %q, want %q (%v)", c.name, got[0].Code, c.code, got)
		}
		if got[0].Severity != SeverityError {
			t.Errorf("ValidateWorldDirName(%q) severity = %q, want error", c.name, got[0].Severity)
		}
	}

	// A padded name is legal but warned about.
	got := ValidateWorldDirName(" World ")
	if len(got) != 1 || got[0].Severity != SeverityWarning || got[0].Code != "dir_whitespace" {
		t.Errorf("ValidateWorldDirName(%q) = %v, want a single warning", " World ", got)
	}
}

// TestValidateWorldDisplayNameAllowsChinese is the plan §6.2 requirement that
// the display name stay permissive: only empty, control characters and
// overlong values are rejected, and Chinese must pass.
func TestValidateWorldDisplayNameAllowsChinese(t *testing.T) {
	good := []string{
		"ScWorld", "ShowNameX",
		"我的世界", "生存战争2 服务器", "世界 with spaces", "emoji 🌍 world",
		strings.Repeat("界", MaxWorldNameLength), // exactly at the rune limit
	}
	for _, n := range good {
		if got := ValidateWorldDisplayName(n); len(got) != 0 {
			t.Errorf("ValidateWorldDisplayName(%q) = %v, want no problems (Chinese must be allowed)", n, got)
		}
	}

	if got := ValidateWorldDisplayName(""); len(got) == 0 || got[0].Code != "name_empty" {
		t.Errorf("empty display name = %v, want name_empty", got)
	}
	if got := ValidateWorldDisplayName("   "); len(got) == 0 {
		t.Error("a whitespace-only display name was accepted")
	}
	if got := ValidateWorldDisplayName("bad\nname"); len(got) == 0 || got[0].Code != "name_control" {
		t.Errorf("control character = %v, want name_control", got)
	}
	// The limit counts RUNES, not bytes: 65 Chinese chars is well over 64 bytes
	// but only one rune over the limit.
	if got := ValidateWorldDisplayName(strings.Repeat("界", MaxWorldNameLength+1)); len(got) == 0 {
		t.Error("an overlong display name was accepted")
	}
}

// TestValidateSeed covers the plan §6.2 seed rules: a STRING here, empty
// allowed, non-numeric warned about but NOT rejected.
func TestValidateSeed(t *testing.T) {
	// Empty means "the engine picks one" and is valid with no complaint.
	if got := ValidateSeed(""); len(got) != 0 {
		t.Errorf("ValidateSeed(\"\") = %v, want no problems", got)
	}
	if got := ValidateSeed("   "); len(got) != 0 {
		t.Errorf("ValidateSeed(whitespace) = %v, want no problems", got)
	}

	// Numeric seeds are clean. "999" is the measured value from plan §6.3.1.
	for _, s := range []string{"999", "0", "5130", "-1", "+7", strings.Repeat("9", WarnSeedLength)} {
		if got := ValidateSeed(s); len(got) != 0 {
			t.Errorf("ValidateSeed(%q) = %v, want no problems", s, got)
		}
	}

	// Non-numeric: a WARNING with no error, because arbitrary text is legal but
	// the derived integer becomes unpredictable.
	got := ValidateSeed("my seed")
	if len(got) != 1 {
		t.Fatalf("ValidateSeed(\"my seed\") = %v, want exactly one problem", got)
	}
	if got[0].Severity != SeverityWarning {
		t.Errorf("non-numeric seed severity = %q, want warning (must not block saving)", got[0].Severity)
	}
	if got[0].Code != "seed_non_numeric" {
		t.Errorf("non-numeric seed code = %q", got[0].Code)
	}
	if HasErrors(got) {
		t.Error("a non-numeric seed must not count as an error")
	}

	if got := ValidateSeed("admin"); !hasCode(got, "seed_non_numeric") {
		t.Errorf("ValidateSeed(\"admin\") = %v, want seed_non_numeric", got)
	}

	// Overlong seed: warning.
	if got := ValidateSeed(strings.Repeat("9", WarnSeedLength+1)); !hasCode(got, "seed_long") {
		t.Errorf("overlong seed = %v, want seed_long", got)
	}
	// Surrounding whitespace changes the derived seed silently: warn.
	if got := ValidateSeed(" 999"); !hasCode(got, "seed_whitespace") {
		t.Errorf("padded seed = %v, want seed_whitespace", got)
	}
}

// TestValidatePassword covers the length cap.
func TestValidatePassword(t *testing.T) {
	// No password is the default and is legal.
	if got := ValidatePassword(""); len(got) != 0 {
		t.Errorf("ValidatePassword(\"\") = %v, want no problems", got)
	}
	if got := ValidatePassword("s3cret"); len(got) != 0 {
		t.Errorf("ValidatePassword(\"s3cret\") = %v, want no problems", got)
	}
	// A Chinese password is fine; the cap counts runes.
	if got := ValidatePassword("密码密码密码"); len(got) != 0 {
		t.Errorf("ValidatePassword(Chinese) = %v, want no problems", got)
	}

	if got := ValidatePassword(strings.Repeat("a", WarnPasswordLength+1)); len(got) == 0 || got[0].Severity != SeverityWarning {
		t.Errorf("a %d-char password = %v, want a warning", WarnPasswordLength+1, got)
	}
	if got := ValidatePassword(strings.Repeat("a", MaxPasswordLength+1)); len(got) == 0 || got[0].Severity != SeverityError {
		t.Errorf("an overlong password = %v, want an error", got)
	}
	if got := ValidatePassword(strings.Repeat("a", MaxPasswordLength)); len(got) != 0 && HasErrors(got) {
		t.Errorf("a password exactly at the cap = %v, want no error", got)
	}
}

// TestValidatePlayerCount covers the bounds.
func TestValidatePlayerCount(t *testing.T) {
	cases := []struct {
		n        int
		wantAny  bool
		severity string
	}{
		{0, true, SeverityError},
		{-5, true, SeverityError},
		{1, false, ""},
		{20, false, ""},
		{64, false, ""},
		{65, true, SeverityWarning},
		{256, true, SeverityWarning},
		{MaxPlayers, true, SeverityWarning},
		{MaxPlayers + 1, true, SeverityError},
	}
	for _, c := range cases {
		got := validatePlayerCount(c.n)
		if !c.wantAny {
			if len(got) != 0 {
				t.Errorf("validatePlayerCount(%d) = %v, want no problems", c.n, got)
			}
			continue
		}
		if len(got) == 0 {
			t.Errorf("validatePlayerCount(%d) = no problems, want severity %q", c.n, c.severity)
			continue
		}
		if got[0].Severity != c.severity {
			t.Errorf("validatePlayerCount(%d) severity = %q, want %q", c.n, got[0].Severity, c.severity)
		}
	}
}

// TestValidateServerSettingAggregates proves the whole-settings validator
// reports every problem at once and keeps warnings distinguishable from errors.
func TestValidateServerSettingAggregates(t *testing.T) {
	// The plan's own defaults must be valid and produce no ERRORS.
	def := DefaultServerSetting()
	problems := ValidateServerSetting(def)
	if HasErrors(problems) {
		t.Errorf("DefaultServerSetting() produced errors: %v", problems)
	}
	if len(problems) != 0 {
		t.Errorf("DefaultServerSetting() should be entirely clean, got %v", problems)
	}

	// Setting the one verified GameMode keeps it clean; every other in-range
	// value must carry the "inferred" warning so the UI can mark it.
	verified := DefaultServerSetting()
	verified.GameMode = 1 // Harmless — the empirical anchor
	if got := ValidateServerSetting(verified); len(got) != 0 {
		t.Errorf("GameMode=1 produced %v, want no problems", got)
	}
	for _, gmValue := range []int{0, 2, 3, 4, 5, 6} {
		inferred := DefaultServerSetting()
		inferred.GameMode = gmValue
		got := ValidateServerSetting(inferred)
		p := findProblem(got, "GameMode")
		if p == nil {
			t.Fatalf("GameMode=%d produced no 'inferred' warning: %v", gmValue, got)
		}
		if p.Severity != SeverityWarning || p.Code != "gamemode_inferred" {
			t.Errorf("GameMode=%d problem = %+v, want a gamemode_inferred warning", gmValue, *p)
		}
		if !strings.Contains(p.Message, "INFERRED") {
			t.Errorf("the GameMode warning must say the mapping is inferred: %q", p.Message)
		}
		if HasErrors(got) {
			t.Errorf("GameMode=%d must warn, not error: %v", gmValue, got)
		}
	}
	// An out-of-range GameMode is an error, not a warning.
	oor := DefaultServerSetting()
	oor.GameMode = 99
	got := ValidateServerSetting(oor)
	if p := findProblem(got, "GameMode"); p == nil || p.Severity != SeverityError {
		t.Errorf("GameMode=99 = %v, want an error-severity problem", got)
	}

	// A thoroughly broken setting: several errors at once.
	bad := &ServerSetting{
		WorldPath:       "app:/Worlds/../etc",
		WorldName:       "",
		WorldMaxPlayers: 0,
		GameMode:        99,
		WorldDaySpeed:   -1,
		WorldPassword:   strings.Repeat("a", MaxPasswordLength+1),
		WorldSeed:       "abc",
	}
	problems = ValidateServerSetting(bad)
	if !HasErrors(problems) {
		t.Fatalf("ValidateServerSetting(bad) reported no errors: %v", problems)
	}
	for _, field := range []string{"WorldPath", "WorldName", "WorldMaxPlayers", "GameMode", "WorldDaySpeed", "WorldPassword"} {
		if findProblem(problems, field) == nil {
			t.Errorf("no problem reported for %s; got %v", field, problems)
		}
	}
	if !hasCode(problems, "seed_non_numeric") {
		t.Errorf("expected a seed warning, got %v", problems)
	}
	// Every problem must be addressable and carry a severity.
	for _, p := range problems {
		if p.Severity != SeverityError && p.Severity != SeverityWarning {
			t.Errorf("problem %+v has an unknown severity", p)
		}
		if p.Code == "" || p.Message == "" {
			t.Errorf("problem %+v is missing a code or message", p)
		}
	}

	// nil settings must not panic.
	if got := ValidateServerSetting(nil); len(got) == 0 || got[0].Severity != SeverityError {
		t.Errorf("ValidateServerSetting(nil) = %v, want an error", got)
	}
}

// TestValidateProblemsAreAddressable checks the structured shape the UI relies
// on: a stable field path, a machine-readable code and a human message.
func TestValidateProblemsAreAddressable(t *testing.T) {
	got := ValidatePort(70000, nil)
	if len(got) != 1 {
		t.Fatalf("ValidatePort(70000) = %v", got)
	}
	p := got[0]
	if p.Field != "ServerPort" || p.Code != "port_range" || p.Severity != SeverityError {
		t.Errorf("problem = %+v", p)
	}
	if !strings.Contains(p.Message, "65535") {
		t.Errorf("port range message should name the legal range: %q", p.Message)
	}
	if !p.IsError() {
		t.Error("IsError() = false for an error-severity problem")
	}
	if s := p.String(); !strings.Contains(s, "error") || !strings.Contains(s, "ServerPort") {
		t.Errorf("String() = %q", s)
	}
	// A warning reports IsError() == false.
	w := warnProblem("X", "c", "m")
	if w.IsError() {
		t.Error("IsError() = true for a warning")
	}
}
