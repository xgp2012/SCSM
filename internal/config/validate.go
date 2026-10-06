package config

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Severity levels. Warnings must stay distinguishable from errors so that a UI
// can let the user proceed with a warning but block on an error.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Problem is a single, field-addressable validation finding (plan §6.2).
//
// Field is a stable dotted path ("GameMode", "WorldPath") so a front end can
// attach the message to the right form control.
type Problem struct {
	Field    string `json:"field"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

func (p Problem) String() string {
	return fmt.Sprintf("[%s] %s (%s): %s", p.Severity, p.Field, p.Code, p.Message)
}

// IsError reports whether the problem blocks saving.
func (p Problem) IsError() bool { return p.Severity == SeverityError }

func errProblem(field, code, format string, args ...any) Problem {
	return Problem{Field: field, Code: code, Message: fmt.Sprintf(format, args...), Severity: SeverityError}
}

func warnProblem(field, code, format string, args ...any) Problem {
	return Problem{Field: field, Code: code, Message: fmt.Sprintf(format, args...), Severity: SeverityWarning}
}

// HasErrors reports whether any problem has error severity.
func HasErrors(problems []Problem) bool {
	for _, p := range problems {
		if p.IsError() {
			return true
		}
	}
	return false
}

// Limits used by the validators.
const (
	// MinPort and MaxPort bound a legal TCP/UDP port.
	MinPort = 1
	MaxPort = 65535

	// MinPlayers is the smallest sensible player count.
	MinPlayers = 1
	// WarnPlayers is the soft ceiling above which the panel warns: the engine
	// tolerates more, but a SCNET host rarely wants to.
	WarnPlayers = 64
	// MaxPlayers is the hard ceiling. The save stores MaxOnlinePlayerCount as a
	// ushort, so 65535 is the format limit; anything above that cannot survive a
	// round trip through the type-annotated save.
	MaxPlayers = 65535

	// WarnSeedLength warns about a seed long enough that the engine's derived
	// integer hash becomes the only meaningful value.
	WarnSeedLength = 64

	// MaxPasswordLength is the password length cap (plan §6.2: 密码长度).
	MaxPasswordLength = 64
	// WarnPasswordLength warns below the cap but above a comfortable length.
	WarnPasswordLength = 32

	// MaxWorldNameLength caps the world DISPLAY name. Chinese is explicitly
	// allowed, so the bound is generous; it counts runes, not bytes.
	MaxWorldNameLength = 64
	// MaxWorldDirNameLength caps the on-disk directory name.
	MaxWorldDirNameLength = 64
)

// ValidateServerSetting checks a whole ServerSetting and returns every problem
// found, so the UI can show all of them at once.
func ValidateServerSetting(s *ServerSetting) []Problem {
	if s == nil {
		return []Problem{errProblem("", "nil_settings", "server settings are missing")}
	}
	var problems []Problem

	problems = append(problems, ValidateWorldDirName2(s.WorldPath)...)
	problems = append(problems, ValidateWorldDisplayName(s.WorldName)...)
	problems = append(problems, validatePlayerCount(s.WorldMaxPlayers)...)
	problems = append(problems, ValidateSeed(s.WorldSeed)...)
	problems = append(problems, ValidatePassword(s.WorldPassword)...)

	if err := ValidateGameMode(s.GameMode); err != nil {
		problems = append(problems, errProblem("GameMode", "gamemode_range",
			"GameMode %d is outside the known range 0..%d; the integer mapping is inferred and must be verified (plan V0-6)",
			s.GameMode, gameModeMax))
	} else if !GameModeIsVerified(s.GameMode) {
		name, _ := GameModeName(s.GameMode)
		problems = append(problems, warnProblem("GameMode", "gamemode_inferred",
			"GameMode %d -> %s is an INFERRED mapping; only 1=Harmless has been empirically verified", s.GameMode, name))
	}

	if s.WorldDaySpeed < 0 {
		problems = append(problems, errProblem("WorldDaySpeed", "dayspeed_negative",
			"WorldDaySpeed must not be negative (got %v)", s.WorldDaySpeed))
	}
	if s.WorldRecoverySpeed < 0 {
		problems = append(problems, errProblem("WorldRecoverySpeed", "recovery_negative",
			"WorldRecoverySpeed must not be negative (got %v)", s.WorldRecoverySpeed))
	}
	if s.CheckLogin && strings.TrimSpace(s.ScKeyServerId) == "" {
		problems = append(problems, warnProblem("ScKeyServerId", "sckey_missing",
			"CheckLogin is enabled but ScKeyServerId is empty; the server may fall back to guest mode"))
	}
	return problems
}

// ValidateWorldDirName2 validates a whole WorldPath, including the
// `app:/Worlds/<dir>` shape, and reports the last-segment rules under the
// WorldPath field.
func ValidateWorldDirName2(worldPath string) []Problem {
	seg, err := PathSegment(worldPath)
	if err != nil {
		return []Problem{errProblem("WorldPath", "worldpath_invalid", "%s", err.Error())}
	}
	return ValidateWorldDirName(seg)
}

// ValidateWorldDirName validates a bare directory name for use as the final
// segment of WorldPath and as the directory under <instance>/Worlds/.
//
// This is the field that decides which save is loaded, so the checks are strict:
// no path separators, no "." or "..", no Windows-reserved or control characters,
// and a length bound. (plan §6.2, §2.7)
func ValidateWorldDirName(name string) []Problem {
	const field = "WorldPath"

	if name == "" {
		return []Problem{errProblem(field, "dir_empty", "world directory name must not be empty")}
	}
	if strings.ContainsAny(name, `/\`) {
		return []Problem{errProblem(field, "dir_separator",
			"world directory name %q must not contain a path separator (`/` or `\\`); "+
				"WorldPath is app:/Worlds/<single directory name>", name)}
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return []Problem{errProblem(field, "dir_traversal",
			"world directory name %q must not contain %q; it would escape <instance>/Worlds/", name, "..")}
	}
	if !utf8.ValidString(name) {
		return []Problem{errProblem(field, "dir_encoding",
			"world directory name is not valid UTF-8")}
	}
	var problems []Problem
	if strings.HasPrefix(name, " ") || strings.HasSuffix(name, " ") {
		problems = append(problems, warnProblem(field, "dir_whitespace",
			"world directory name %q has leading or trailing spaces, which is legal but error-prone", name))
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return []Problem{errProblem(field, "dir_control",
				"world directory name %q contains a control character", name)}
		}
		if strings.ContainsRune(`:*?"<>|`, r) {
			return []Problem{errProblem(field, "dir_reserved",
				"world directory name %q contains the reserved character %q", name, string(r))}
		}
	}
	if utf8.RuneCountInString(name) > MaxWorldDirNameLength {
		problems = append(problems, errProblem(field, "dir_too_long",
			"world directory name is %d characters; the limit is %d (derived from WorldPath)",
			utf8.RuneCountInString(name), MaxWorldDirNameLength))
	}
	return problems
}

// ValidateWorldDisplayName validates ServerSetting.WorldName.
//
// It is deliberately permissive: Chinese display names are expected and must
// pass. Only empty, control characters and overlong values are rejected.
func ValidateWorldDisplayName(name string) []Problem {
	const field = "WorldName"

	if strings.TrimSpace(name) == "" {
		return []Problem{errProblem(field, "name_empty", "world display name must not be empty")}
	}
	if !utf8.ValidString(name) {
		return []Problem{errProblem(field, "name_encoding", "world display name is not valid UTF-8")}
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return []Problem{errProblem(field, "name_control",
				"world display name contains a control character (U+%04X)", r)}
		}
	}
	if utf8.RuneCountInString(name) > MaxWorldNameLength {
		return []Problem{errProblem(field, "name_too_long",
			"world display name is %d characters; the limit is %d",
			utf8.RuneCountInString(name), MaxWorldNameLength)}
	}
	return nil
}

// validatePlayerCount checks the player ceiling.
func validatePlayerCount(n int) []Problem {
	const field = "WorldMaxPlayers"
	switch {
	case n < MinPlayers:
		return []Problem{errProblem(field, "players_min",
			"player count must be at least %d (got %d)", MinPlayers, n)}
	case n > MaxPlayers:
		return []Problem{errProblem(field, "players_max",
			"player count must not exceed %d, the limit of the save format's ushort field (got %d)", MaxPlayers, n)}
	case n > WarnPlayers:
		return []Problem{warnProblem(field, "players_high",
			"player count %d is above the recommended maximum of %d", n, WarnPlayers)}
	}
	return nil
}

// ValidatePort validates a port number and, when a taken hook is supplied, that
// the port is free for UDP (the game server and its broadcast socket are UDP).
//
// taken reports whether the port is already in use; pass nil to skip occupancy
// checking (for example when validating a template before any process exists).
func ValidatePort(port int, taken func(int) bool) []Problem {
	problems := ValidatePortRange(port)
	if len(problems) > 0 {
		return problems
	}
	if taken != nil && taken(port) {
		problems = append(problems, errProblem("ServerPort", "port_taken",
			"UDP port %d is already in use; choose another port", port))
	}
	return problems
}

// ValidatePortRange validates only the numeric range of a port.
func ValidatePortRange(port int) []Problem {
	if port < MinPort || port > MaxPort {
		return []Problem{errProblem("ServerPort", "port_range",
			"port %d is outside the legal range %d..%d", port, MinPort, MaxPort)}
	}
	return nil
}

// ValidateSeed validates ServerSetting.WorldSeed.
//
// NOTE: in ServerSetting.json WorldSeed is a STRING — the raw text the user
// typed — while the save derives an integer from it. An empty seed is allowed
// and means "the engine picks one". A non-numeric seed is legal too (the engine
// hashes arbitrary text), but the panel warns because the derived integer is
// then opaque and cannot be reproduced from the UI.
func ValidateSeed(seed string) []Problem {
	const field = "WorldSeed"

	trimmed := strings.TrimSpace(seed)
	if trimmed == "" {
		return nil
	}
	var problems []Problem
	numeric := true
	for i, r := range trimmed {
		if r >= '0' && r <= '9' {
			continue
		}
		if i == 0 && (r == '-' || r == '+') {
			continue
		}
		numeric = false
		break
	}
	if !numeric {
		problems = append(problems, warnProblem(field, "seed_non_numeric",
			"seed %q is not numeric; the engine hashes arbitrary text into an integer seed, "+
				"so the resulting WorldSeed cannot be predicted from the panel", trimmed))
	}
	if utf8.RuneCountInString(trimmed) > WarnSeedLength {
		problems = append(problems, warnProblem(field, "seed_long",
			"seed is %d characters; seeds longer than %d are hard to record or reproduce",
			utf8.RuneCountInString(trimmed), WarnSeedLength))
	}
	if trimmed != seed {
		problems = append(problems, warnProblem(field, "seed_whitespace",
			"seed has surrounding whitespace, which is written verbatim and changes the derived integer seed"))
	}
	if !utf8.ValidString(seed) {
		return []Problem{errProblem(field, "seed_encoding", "seed is not valid UTF-8")}
	}
	return problems
}

// ValidatePassword validates ServerSetting.WorldPassword.
//
// An empty password means "no password" and is allowed. Only the length cap is
// enforced; the game's password handling accepts arbitrary text.
func ValidatePassword(pw string) []Problem {
	const field = "WorldPassword"

	if !utf8.ValidString(pw) {
		return []Problem{errProblem(field, "password_encoding", "password is not valid UTF-8")}
	}
	n := utf8.RuneCountInString(pw)
	if n > MaxPasswordLength {
		return []Problem{errProblem(field, "password_long",
			"password is %d characters; the limit is %d", n, MaxPasswordLength)}
	}
	if n > WarnPasswordLength {
		return []Problem{warnProblem(field, "password_long_warn",
			"password is %d characters; consider staying at or below %d", n, WarnPasswordLength)}
	}
	return nil
}
