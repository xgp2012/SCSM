package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// ServerSetting mirrors ServerSetting.json, the server's primary start-up
// configuration (plan §2.2). JSON key names and capitalisation are matched byte
// for byte against the plan's measured sample; do not "tidy" them.
//
// Two fields deserve special care:
//
//   - WorldPath decides the ON-DISK DIRECTORY (`app:/Worlds/<dir>`), while
//     WorldName is only a display name. Switching saves requires changing
//     WorldPath; changing WorldName alone does nothing (plan §2.7).
//   - GameMode is an INTEGER here, whereas a save's Project.json stores the
//     enum NAME. Use GameModeName / GameModeValue for the mapping.
type ServerSetting struct {
	CheckLogin           bool    `json:"CheckLogin"`
	ScKeyServerId        string  `json:"ScKeyServerId"`
	ScKeyServerName      string  `json:"ScKeyServerName"`
	Autorun              bool    `json:"Autorun"`
	AutoGenerateWorld    bool    `json:"AutoGenerateWorld"`
	WorldPath            string  `json:"WorldPath"`
	WorldName            string  `json:"WorldName"`
	WorldSeed            string  `json:"WorldSeed"`
	WorldPassword        string  `json:"WorldPassword"`
	WorldKeywordBlocking string  `json:"WorldKeywordBlocking"`
	WorldMaxPlayers      int     `json:"WorldMaxPlayers"`
	WorldDaySpeed        float64 `json:"WorldDaySpeed"`
	WorldRecoverySpeed   float64 `json:"WorldRecoverySpeed"`
	WorldDisableBlocks   string  `json:"WorldDisableBlocks"`
	RandomSpawnPosition  bool    `json:"RandomSpawnPosition"`
	GameMode             int     `json:"GameMode"`
	SeasonChanging       bool    `json:"SeasonChanging"`
	PVPEnabled           bool    `json:"PVPEnabled"`
}

// BackupSuffix is appended to a configuration file before the panel overwrites
// it, e.g. ServerSetting.json.panel.bak (plan §6.2: 写前备份).
const BackupSuffix = ".panel.bak"

// knownServerSettingKeys is the set of keys owned by the ServerSetting struct.
// Anything else found in the file is an unknown/future field and is carried
// through load -> save untouched (plan §6.2: 保留未知/新增字段).
var knownServerSettingKeys = map[string]bool{
	"CheckLogin": true, "ScKeyServerId": true, "ScKeyServerName": true,
	"Autorun": true, "AutoGenerateWorld": true,
	"WorldPath": true, "WorldName": true, "WorldSeed": true,
	"WorldPassword": true, "WorldKeywordBlocking": true,
	"WorldMaxPlayers": true, "WorldDaySpeed": true, "WorldRecoverySpeed": true,
	"WorldDisableBlocks": true, "RandomSpawnPosition": true, "GameMode": true,
	"SeasonChanging": true, "PVPEnabled": true,
}

// DefaultServerSetting returns the defaults the server writes on first run,
// copied from the measured sample in plan §2.2.
func DefaultServerSetting() *ServerSetting {
	return &ServerSetting{
		CheckLogin:           false,
		ScKeyServerId:        "",
		ScKeyServerName:      "",
		Autorun:              false,
		AutoGenerateWorld:    false,
		WorldPath:            DefaultWorldPath,
		WorldName:            DefaultWorldName,
		WorldSeed:            "",
		WorldPassword:        "",
		WorldKeywordBlocking: "",
		WorldMaxPlayers:      20,
		WorldDaySpeed:        1,
		WorldRecoverySpeed:   1,
		WorldDisableBlocks:   "",
		RandomSpawnPosition:  false,
		GameMode:             1, // Harmless — the only empirically verified value
		SeasonChanging:       true,
		PVPEnabled:           true,
	}
}

// DefaultWorldPath and DefaultWorldName are the measured first-run defaults.
// Note that these are two INDEPENDENT fields: the directory is "World" because
// WorldPath ends with "World", not because WorldName is "ScWorld".
const (
	DefaultWorldPath = "app:/Worlds/World"
	DefaultWorldName = "ScWorld"
)

// LoadServerSetting reads path and returns the typed view plus a map holding
// ALL raw keys, including unknown/future ones that the struct does not model.
//
// The raw map is what makes the panel upgrade-safe: it must be handed to Save so
// that keys added by a future server build are never dropped.
func LoadServerSetting(path string) (*ServerSetting, map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config: read ServerSetting %q: %w", path, err)
	}
	// Tolerate a BOM: the server does not write one for this file, but hand
	// edited copies sometimes carry one and encoding/json would choke on it.
	data = trimBOM(data)

	s := &ServerSetting{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, nil, fmt.Errorf("config: parse ServerSetting %q: %w", path, err)
	}

	raw := map[string]any{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("config: parse raw ServerSetting %q: %w", path, err)
	}
	return s, raw, nil
}

// Save writes the settings to path as indented 2-space JSON.
//
// Behaviour required by the plan (§6.2):
//   - unknown/future server fields are NEVER lost: keys in extra (normally the
//     raw map returned by LoadServerSetting) that the struct does not model are
//     merged into the output, and struct keys always win over stale extras;
//   - the previous file is backed up to <path>.panel.bak first, unless the
//     target does not exist yet;
//   - the result is stable: Load -> Save -> Load is deep-equal.
//
// extra may be nil. Extra keys equal to a modelled struct key are ignored so
// that a stale raw map cannot silently revert a field the panel just changed.
func (s *ServerSetting) Save(path string, extra map[string]any) error {
	if s == nil {
		return errors.New("config: cannot save a nil ServerSetting")
	}

	merged := make(map[string]any, len(extra)+len(knownServerSettingKeys))
	for k, v := range extra {
		if knownServerSettingKeys[k] {
			continue
		}
		merged[k] = v
	}

	encoded, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("config: marshal ServerSetting: %w", err)
	}
	var modelled map[string]any
	if err := json.Unmarshal(encoded, &modelled); err != nil {
		return fmt.Errorf("config: re-read marshalled ServerSetting: %w", err)
	}
	for k, v := range modelled {
		merged[k] = v
	}

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("config: format ServerSetting: %w", err)
	}
	out = append(out, '\n')

	if err := BackupFile(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config: create directory for %q: %w", path, err)
	}
	if err := writeFileAtomic(path, out); err != nil {
		return fmt.Errorf("config: write ServerSetting %q: %w", path, err)
	}
	return nil
}

// WorldDirName returns the LAST path segment of WorldPath — the on-disk
// directory name under <instance>/Worlds/ — validating it on the way.
//
// This is the field that actually switches saves (plan §2.7). It accepts the
// `app:/Worlds/<dir>` virtual-prefix form, plain relative paths and POSIX
// absolute paths, and accepts Windows-style separators so that a config copied
// from a Windows host is still understood.
func (s *ServerSetting) WorldDirName() (string, error) {
	if s == nil {
		return "", errors.New("config: nil ServerSetting")
	}
	return PathSegment(s.WorldPath)
}

// PathSegment extracts the final directory segment from a WorldPath and checks
// that it is a legal single directory name.
//
// It is an error for the segment to be empty, to be "." or "..", or for the path
// to contain a traversal element or a separator inside the segment (that would
// escape <instance>/Worlds/ — the class of bug the plan calls out explicitly in
// §6.2).
//
// NUL and the rest of the control range are rejected here, across the WHOLE
// path, because this function RETURNS a segment that callers use directly as a
// filesystem name without re-validating it (WorldDirName, world.PathSegment).
// A NUL byte is the dangerous one: Go strings carry it, but a syscall truncates
// the path at it, so "a\x00b" and "a" would address the same directory while
// comparing unequal — the classic validation bypass. Rejecting the rest of the
// control range is the conservative choice and costs nothing legitimate: a
// directory name containing a raw newline, tab or escape byte is never
// intentional, and the display name (WorldName), which is where arbitrary user
// text legitimately lands, keeps its own separate validator
// (ValidateWorldDisplayName) and is NOT affected by this.
// The check uses unicode.IsControl so that it also covers DEL and the C1 block
// (U+0080-U+009F), and so that it agrees exactly with ValidateWorldDirName,
// which is the established spelling of this rule in the package.
//
// Deliberately NOT rejected, to avoid over-rejecting:
//   - any non-ASCII Unicode, including Chinese: "世界" is a legal directory
//     name on the Linux hosts this panel targets (already covered by a test);
//   - spaces, including interior and "a b" (only the leading/trailing case is
//     warned about, by ValidateWorldDirName);
//   - Windows-reserved device names (CON, PRN, AUX, NUL, COM1-9, LPT1-9):
//     these are inert on Linux, where the panel actually runs, and rejecting
//     them would refuse names a real user may legitimately have. Note that the
//     literal string "nul" (lowercase, no NUL byte) is therefore still accepted;
//     it is a plain directory name on this platform, not the device.
func PathSegment(worldPath string) (string, error) {
	p := strings.TrimSpace(worldPath)
	if p == "" {
		return "", errors.New("config: WorldPath is empty; it must look like app:/Worlds/<dir>")
	}
	// Normalise Windows separators so a cross-platform config is understood.
	norm := strings.ReplaceAll(p, "\\", "/")
	norm = strings.TrimRight(norm, "/")
	if norm == "" || norm == "app:" {
		return "", fmt.Errorf("config: WorldPath %q has no directory segment; expected app:/Worlds/<dir>", worldPath)
	}
	seg := norm[strings.LastIndex(norm, "/")+1:]
	if seg == "" {
		return "", fmt.Errorf("config: WorldPath %q has an empty directory segment", worldPath)
	}

	// Reject control characters ANYWHERE in the path, not just in the final
	// segment, for the same reason the ".." scan below covers every component:
	// this function accepts multi-segment spellings (relative and POSIX absolute
	// paths), and a NUL in an earlier component is just as dangerous as one in
	// the last — the returned segment would be joined back onto a parent path
	// that a syscall truncates at the NUL.
	//
	// A NUL byte is what makes this a security check rather than a tidiness one
	// (see the doc comment). Checked on the raw normalised path, NOT on the
	// trimmed one, because strings.TrimSpace strips leading/trailing NUL and
	// other C0 bytes and would silently hide exactly the bytes we must refuse.
	for _, r := range norm {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("config: WorldPath %q contains a control character (U+%04X), which is not legal in a directory name",
				worldPath, r)
		}
	}

	// Reject a traversal element ANYWHERE in the path, not merely as the last
	// segment: "app:/Worlds/../etc" has last segment "etc", but the ".." still
	// walks out of <instance>/Worlds/ once the engine resolves the path.
	for _, part := range strings.Split(norm, "/") {
		if part == ".." {
			return "", fmt.Errorf("config: WorldPath %q contains %q, which would escape <instance>/Worlds/", worldPath, "..")
		}
	}

	// The last segment must itself be a clean directory name, and the path must
	// actually end in one (not in "Worlds/" or "app:/").
	if seg == "." || seg == ".." {
		return "", fmt.Errorf("config: WorldPath %q segment %q is not a legal directory name", worldPath, seg)
	}
	if strings.HasSuffix(norm, "/Worlds") {
		return "", fmt.Errorf("config: WorldPath %q names the Worlds container, not a world directory; expected app:/Worlds/<dir>", worldPath)
	}
	return seg, nil
}

// WorldPathForDir builds the canonical WorldPath value for a directory name,
// e.g. WorldPathForDir("MyWorldA") == "app:/Worlds/MyWorldA". The name is
// validated with ValidateWorldDirName.
func WorldPathForDir(dir string) (string, error) {
	if problems := ValidateWorldDirName(dir); len(problems) > 0 {
		return "", fmt.Errorf("config: illegal world directory name %q: %s", dir, problems[0].Message)
	}
	return "app:/Worlds/" + dir, nil
}

// BackupFile copies path to path+BackupSuffix (plan §6.2/§6.3: 写前备份).
// A missing source is not an error — nothing to back up on first write.
func BackupFile(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("config: read %q for backup: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("config: stat %q for backup: %w", path, err)
	}
	target := path + BackupSuffix
	// Write the backup with the original permissions; 0644 when unknown.
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	if err := writeFileAtomic(target, src); err != nil {
		return fmt.Errorf("config: write backup %q: %w", target, err)
	}
	// writeFileAtomic uses 0600 on creation; align it with the source.
	if err := os.Chmod(target, mode); err != nil {
		return fmt.Errorf("config: chmod backup %q: %w", target, err)
	}
	return nil
}

// writeFileAtomic writes data to path via a temporary file in the same
// directory, then renames it into place. A crash therefore cannot leave a
// half-written configuration behind.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeded

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// trimBOM strips a leading UTF-8 BOM, if present.
func trimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

// hasBOM reports whether data starts with a UTF-8 BOM.
func hasBOM(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF
}

// bomPrefix is the UTF-8 byte-order mark written by the server at the head of a
// save's Project.json (plan §2.3, §A.5.2).
var bomPrefix = []byte{0xEF, 0xBB, 0xBF}
