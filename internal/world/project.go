// Package world implements save (world) management for a SurvivalcraftNet
// instance: scanning `Worlds/*/`, import/export, activation, and deletion.
//
// # The single most important fact in this package
//
// Plan §2.7 and its decisive experiment (appendix A.5.1) establish that the
// on-disk directory name and the in-game world name are DIFFERENT CONCEPTS:
//
//	ServerSetting.json:  "WorldPath": "app:/Worlds/MyWorldA",  // decides the DIRECTORY
//	                     "WorldName": "ShowNameX"              // display only
//	Project.json GameInfo: "WorldName": "ShowNameX"
//	                       "WorldDirectoryName": "app:/Worlds/MyWorldA"
//
// The world directory is therefore always taken from the LAST PATH SEGMENT of
// `WorldPath` (surfaced here as [World.DirName]), never derived from a display
// name. Switching saves means rewriting `WorldPath`; rewriting `WorldName` alone
// does not switch anything. The plan flags confusing the two as a
// HIGH-probability risk, so this package keeps them in separate fields
// everywhere and never uses DisplayName to build a path.
//
// # Save format (§2.3)
//
// `Worlds/<dir>/Project.json` is UTF-8 **with a BOM**, and its values are stored
// as type-annotated pairs:
//
//	{"Version":["string","2.4"], "Guid":["System.Guid","..."],
//	 "Subsystems":{"GameInfo":{"WorldName":["string","ShowNameX"], ...}}}
//
// The BOM must be stripped before parsing or encoding/json rejects the file.
// The format is never deserialised into a struct: the panel does point reads
// (name, guid, player cap, game mode) and whole-directory copy/backup/delete.
//
// # Dependency note
//
// `internal/config` owns the authoritative ServerSetting.json reader and is
// developed in parallel. To avoid blocking, this package defines the narrow
// interface it needs plus a minimal BOM-aware reader ([ReadServerSetting]). The
// two functions that touch ServerSetting.json — [Activate] and the active-world
// detection in [Scan] — go through [SettingStore], so swapping in
// `config.LoadProject` later is a one-line change at the call site and requires
// no edits here.
package world

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"scnetm/internal/config"
)

// UTF8BOM is the byte-order mark that prefixes a SurvivalcraftNet Project.json
// (plan §2.3, appendix A.4). Go's encoding/json does NOT skip it, so every read
// path in this package strips it and every write path must not add it back
// accidentally.
var UTF8BOM = []byte{0xEF, 0xBB, 0xBF}

// Errors returned by this package. Callers map them onto HTTP statuses.
var (
	// ErrNoProjectFile means the world directory has no Project.json.
	ErrNoProjectFile = errors.New("world: Project.json not found")

	// ErrCorruptProject means Project.json exists but could not be parsed.
	ErrCorruptProject = errors.New("world: Project.json is corrupt")

	// ErrNotFound means no such world directory.
	ErrNotFound = errors.New("world: world not found")

	// ErrExists means the target world directory already exists.
	ErrExists = errors.New("world: world already exists")

	// ErrInvalidDirName means the directory name is not a legal WorldPath
	// segment (empty, a separator, "..", a drive letter, or a NUL byte).
	ErrInvalidDirName = errors.New("world: invalid world directory name")

	// ErrRunning means the operation requires a stopped instance but the
	// instance is running.
	ErrRunning = errors.New("world: instance is running")

	// ErrActiveWorld means the operation would disturb the world currently
	// selected by ServerSetting.WorldPath.
	ErrActiveWorld = errors.New("world: world is currently active")

	// ErrZipSlip means an archive entry tried to escape the destination.
	ErrZipSlip = errors.New("world: archive entry escapes the destination")
)

// World describes one save directory under `<instance>/Worlds/`.
//
// DirName and DisplayName are deliberately separate fields. DirName is what
// appears on disk and what WorldPath must contain; DisplayName is only ever
// shown to a human.
type World struct {
	// DirName is the on-disk directory name, i.e. the last segment of
	// ServerSetting.WorldPath. This is the identity of the save.
	DirName string `json:"dirName"`
	// DisplayName is GameInfo.WorldName — display only. NEVER use it to build a
	// path (plan §2.7).
	DisplayName string `json:"displayName"`
	// Guid is the project GUID, when present.
	Guid string `json:"guid,omitempty"`
	// Path is the absolute path of the world directory.
	Path string `json:"path"`
	// Mode is GameInfo.GameMode rendered as a STRING (e.g. "Harmless"). Note
	// that ServerSetting.json stores the same concept as an INTEGER; the two
	// representations must not be mixed (plan §6.3.1 note 1).
	Mode string `json:"mode,omitempty"`
	// ModeUnknown is true when GameMode could not be read as a string.
	ModeUnknown bool `json:"modeUnknown,omitempty"`
	// MaxPlayers is GameInfo.MaxOnlinePlayerCount.
	MaxPlayers int `json:"maxPlayers"`
	// SizeBytes is the total size of the world directory.
	SizeBytes int64 `json:"sizeBytes"`
	// RegionsBytes is the size of Regions/ alone — the main driver of growth
	// (plan §2.3).
	RegionsBytes int64 `json:"regionsBytes"`
	// RegionsCount is the number of files under Regions/.
	RegionsCount int `json:"regionsCount"`
	// ProjectMTime is the modification time of Project.json.
	ProjectMTime int64 `json:"projectMTime"`
	// HasBak reports whether Project.json.bak exists. Its presence tells the
	// server has its own backup mechanism (plan §2.3, §A.5.3).
	HasBak bool `json:"hasBak"`
	// LastBakMTime is the modification time of Project.json.bak.
	LastBakMTime int64 `json:"lastBakMTime"`
	// Active reports whether this world is the one currently selected by
	// ServerSetting.WorldPath.
	Active bool `json:"active"`
	// Issues records non-fatal problems found while scanning this world, e.g. a
	// missing or corrupt Project.json. A world with issues is still returned so
	// the UI can show and repair it — only a failure to stat the directory
	// itself removes it from the list.
	Issues []string `json:"issues,omitempty"`
	// project is the parsed Project.json, retained so Get/Activate do not have
	// to re-read it.
	project *Project
}

// ProjectModeString returns the GameMode as a string, reporting whether it was
// available.
func (w *World) ProjectModeString() (string, bool) {
	if w.ModeUnknown || w.Mode == "" {
		return "", false
	}
	return w.Mode, true
}

// Project is a lazily-parsed, read-only view of one Project.json.
//
// Values are kept in their raw type-annotated form. This is the plan's §2.3
// requirement: the format mixes types and must not be deserialised into a
// struct. Point reads go through [Project.String], [Project.Int],
// [Project.GameInfoString] and friends.
type Project struct {
	// raw is the whole document with the BOM already stripped.
	raw []byte
	// doc is the decoded generic JSON.
	doc map[string]any
	// path is where it was read from.
	path string
	// hadBOM records whether the file carried a UTF-8 BOM, so the panel can
	// write the file back with the same shape the server expects.
	hadBOM bool
}

// Path returns the file the project was read from.
func (p *Project) Path() string { return p.path }

// HadBOM reports whether the source file was prefixed with a UTF-8 BOM.
func (p *Project) HadBOM() bool { return p.hadBOM }

// Raw returns the document bytes with the BOM stripped.
func (p *Project) Raw() []byte { return p.raw }

// Doc returns the decoded JSON object. Treat it as read-only.
func (p *Project) Doc() map[string]any { return p.doc }

// ReadProject reads and parses a Project.json, stripping the UTF-8 BOM.
//
// The BOM is the single most common reason a naive implementation fails on a
// real save file, so it is handled here and recorded in [Project.HadBOM] rather
// than being silently forgotten.
func ReadProject(path string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNoProjectFile, path)
		}
		return nil, fmt.Errorf("world: read %s: %w", path, err)
	}
	return parseProject(path, data)
}

// readProjectViaConfig parses Project.json using the `internal/config` loader,
// which is the authoritative implementation (it preserves key order and knows
// the annotated-value format in full).
//
// It is kept alongside [ReadProject] rather than replacing it because the two
// serve different needs:
//
//   - [ReadProject] is used by the SCANNER, which must read dozens of saves and
//     only needs four fields. It keeps the document as a generic map and never
//     re-serialises, which is the cheapest correct thing to do and matches
//     §2.3's "定点读写" guidance;
//   - this function is the adapter for callers that want the config package's
//     richer Project (key order, SetField/SaveTo for the advanced offline
//     editor that §6.3.1 describes).
//
// Both strip the BOM, and scan_test.go asserts they agree on every fixture.
func readProjectViaConfig(path string) (*config.Project, error) {
	p, err := config.LoadProject(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNoProjectFile, path)
		}
		return nil, fmt.Errorf("world: read %s: %w", path, err)
	}
	return p, nil
}

// parseProject is the BOM-stripping parser, split out so tests can feed it raw
// bytes (including a hand-built BOM) without touching the filesystem.
func parseProject(path string, data []byte) (*Project, error) {
	hadBOM := bytes.HasPrefix(data, UTF8BOM)
	body := bytes.TrimPrefix(data, UTF8BOM)
	// Tolerate a UTF-16 BOM by reporting it clearly rather than emitting a
	// confusing JSON syntax error.
	if bytes.HasPrefix(body, []byte{0xFF, 0xFE}) || bytes.HasPrefix(body, []byte{0xFE, 0xFF}) {
		return nil, fmt.Errorf("%w: %s is UTF-16, expected UTF-8", ErrCorruptProject, path)
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // keep integers exact; float64 would mangle large GUID-ish values
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorruptProject, path, err)
	}
	return &Project{raw: body, doc: doc, path: path, hadBOM: hadBOM}, nil
}

// GameInfo returns the Subsystems.GameInfo subtree, or nil.
//
// This is where every panel-configurable world parameter lives (plan §6.3.1).
func (p *Project) GameInfo() map[string]any {
	return p.subtree("Subsystems", "GameInfo")
}

// Players returns the Subsystems.Players subtree, or nil.
func (p *Project) Players() map[string]any {
	return p.subtree("Subsystems", "Players")
}

// Subsystems returns the Subsystems subtree, or nil.
func (p *Project) Subsystems() map[string]any {
	v, _ := p.doc["Subsystems"].(map[string]any)
	return v
}

// subtree walks a chain of object keys.
func (p *Project) subtree(keys ...string) map[string]any {
	var cur any = p.doc
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[k]
		if !ok {
			return nil
		}
	}
	m, _ := cur.(map[string]any)
	return m
}

// annotated unwraps the `["type", value]` pair the format uses for leaf values
// (plan §2.3). It also accepts a bare value, because a hand-edited or
// future-version file may not annotate every field.
func annotated(v any) (typ string, val any, ok bool) {
	arr, isArr := v.([]any)
	if !isArr {
		return "", v, v != nil
	}
	switch len(arr) {
	case 2:
		t, _ := arr[0].(string)
		return t, arr[1], true
	case 1:
		// `["string"]` is a *type annotation with no value* — it appears for
		// fields the serializer emitted but never populated. Reading it as the
		// value "string" would be actively wrong (the panel would show the
		// literal word "string" as a world name), so the value is reported as
		// absent while the type is surfaced for diagnostics.
		t, _ := arr[0].(string)
		return t, nil, false
	default:
		// Three or more elements is not a type annotation at all: it is a
		// genuine list value (e.g. a Vector3 encoded as several components).
		// Report it as a bare, un-annotated value.
		return "", arr, true
	}
}

// Get looks a field up in the given subtree and returns its annotated value.
func (p *Project) Get(section map[string]any, field string) (typ string, val any, ok bool) {
	if section == nil {
		return "", nil, false
	}
	raw, present := section[field]
	if !present {
		return "", nil, false
	}
	return annotated(raw)
}

// GameInfoString reads a string field from Subsystems.GameInfo.
//
// GameInfo.WorldName (the display name) and GameInfo.WorldDirectoryName (the
// path) both go through here.
func (p *Project) GameInfoString(field string) (string, bool) {
	return p.StringAt(p.GameInfo(), field)
}

// StringAt reads a string field from an arbitrary subtree.
func (p *Project) StringAt(section map[string]any, field string) (string, bool) {
	typ, val, ok := p.Get(section, field)
	if !ok {
		return "", false
	}
	if s, isStr := val.(string); isStr {
		return s, true
	}
	// Some builds store scalars without a type annotation; accept the coercion
	// only when the annotation is absent or explicitly a string.
	if typ == "" || strings.EqualFold(typ, "string") {
		if s, isStr := val.(string); isStr {
			return s, true
		}
	}
	return "", false
}

// IntAt reads an integer field from an arbitrary subtree.
//
// json.Number is used (decoder configured with UseNumber) so this works for
// both `20` and `"20"`, and for values larger than a float64 can represent
// exactly.
func (p *Project) IntAt(section map[string]any, field string) (int, bool) {
	_, val, ok := p.Get(section, field)
	if !ok {
		return 0, false
	}
	return toInt(val)
}

// BoolAt reads a boolean field from an arbitrary subtree.
func (p *Project) BoolAt(section map[string]any, field string) (bool, bool) {
	_, val, ok := p.Get(section, field)
	if !ok {
		return false, false
	}
	if b, isBool := val.(bool); isBool {
		return b, true
	}
	if s, isStr := val.(string); isStr {
		switch strings.ToLower(s) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// FloatAt reads a float field from an arbitrary subtree.
func (p *Project) FloatAt(section map[string]any, field string) (float64, bool) {
	_, val, ok := p.Get(section, field)
	if !ok {
		return 0, false
	}
	switch v := val.(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	case int:
		return float64(v), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// TopLevelString reads a field from the document root, e.g. "Guid" or "Name".
func (p *Project) TopLevelString(field string) (string, bool) {
	return p.StringAt(p.doc, field)
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
		if f, err := n.Float64(); err == nil {
			return int(f), true
		}
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case bool:
		// A bool is sometimes stored where an int is expected; treat as 0/1
		// rather than failing the whole read.
		if n {
			return 1, true
		}
		return 0, true
	case string:
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i, true
		}
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// ServerSetting.json access
// ---------------------------------------------------------------------------
//
// The authoritative reader lives in `internal/config` (`config.LoadServerSetting`,
// `config.PathSegment`, and `config.Project` for Project.json). This package
// originally carried a minimal BOM-aware reader so it would not block on that
// package; now that `internal/config` is available, this file delegates to it
// and the temporary reader is gone.
//
// The [SettingStore] interface is kept as the seam: tests inject an in-memory
// store, and the application layer can pass whichever config-backed
// implementation it wants without this package importing the whole config stack
// at every call site.

// SettingStore is the narrow interface this package needs for the two
// ServerSetting.json operations it performs.
type SettingStore interface {
	// WorldPath returns ServerSetting.WorldPath verbatim, e.g.
	// "app:/Worlds/MyWorldA". The boolean reports presence.
	WorldPath() (string, bool)
	// SetWorldPath writes ServerSetting.WorldPath, preserving every other
	// field in the file. Implementations must be BOM-aware and must back the
	// file up before writing.
	SetWorldPath(path string) error
}

// ReadServerSetting reads ServerSetting.WorldPath using the config package's
// BOM-aware loader.
//
// It returns the raw string, which still carries the `app:/` prefix. Use
// [PathSegment] to reduce it to a directory name.
func ReadServerSetting(path string) (string, error) {
	s, _, err := config.LoadServerSetting(path)
	if err != nil {
		return "", fmt.Errorf("world: read %s: %w", path, err)
	}
	if s == nil {
		return "", nil
	}
	return s.WorldPath, nil
}

// PathSegment extracts the final directory segment from a WorldPath value,
// delegating to `config.PathSegment`.
//
//	"app:/Worlds/MyWorldA" -> "MyWorldA"
//
// It is the §2.7 rule in code: the directory name IS the last segment of
// WorldPath. It rejects values that cannot name a directory, including a path
// containing ".." anywhere.
func PathSegment(worldPath string) (string, error) {
	// A NUL byte can never appear in a filesystem name, and config.PathSegment
	// does not currently check for one (see the residual-risk note in the
	// package report). Checking here keeps the world layer's own guarantee
	// absolute regardless of validator drift.
	if strings.ContainsRune(worldPath, 0) {
		return "", fmt.Errorf("%w: WorldPath contains a NUL byte", ErrInvalidDirName)
	}
	seg, err := config.PathSegment(worldPath)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidDirName, err)
	}
	return seg, nil
}

// rootSettingStore adapts a ServerSetting.json file on disk to [SettingStore]
// using the config package's loader and saver.
type rootSettingStore struct {
	path string
}

// NewFileSettingStore returns a SettingStore backed by a ServerSetting.json
// file. Prefer supplying a config-backed store from the application layer.
func NewFileSettingStore(path string) SettingStore { return &rootSettingStore{path: path} }

func (s *rootSettingStore) WorldPath() (string, bool) {
	ss, _, err := config.LoadServerSetting(s.path)
	if err != nil || ss == nil || ss.WorldPath == "" {
		return "", false
	}
	return ss.WorldPath, true
}

func (s *rootSettingStore) SetWorldPath(worldPath string) error {
	ss, extra, err := config.LoadServerSetting(s.path)
	if err != nil {
		return fmt.Errorf("world: read %s: %w", s.path, err)
	}
	if ss == nil {
		return fmt.Errorf("world: %s did not yield a ServerSetting", s.path)
	}
	ss.WorldPath = worldPath
	if err := ss.Save(s.path, extra); err != nil {
		return fmt.Errorf("world: write %s: %w", s.path, err)
	}
	return nil
}

// ValidateDirName enforces the rules for a world directory name.
//
// This is deliberately redundant with `config.PathSegment`'s own check: this
// function is called directly by Import/Export/Delete/Activate with a
// caller-supplied name that never travelled through a WorldPath, so the check
// must stand on its own. It delegates to PathSegment by round-tripping through
// the canonical `app:/Worlds/<name>` spelling, which is the only form the config
// package validates — that guarantees the two validators can never drift.
//
// Rules (from §6.2 and §2.7):
//
//   - not empty;
//   - no path separators ('/' or '\\');
//   - not "." or "..";
//   - no ".." component anywhere;
//   - no NUL byte;
//   - no drive-letter prefix ("C:...");
//   - no control characters;
//   - not longer than 255 bytes (the ext4 name limit).
//
// The display name (WorldName) is deliberately NOT validated this strictly — it
// may contain Chinese characters and spaces, because it is never a path.
func ValidateDirName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidDirName)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidDirName, name)
	}
	if strings.ContainsAny(name, `/:\\`) {
		return fmt.Errorf("%w: %q contains a path separator", ErrInvalidDirName, name)
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("%w: %q contains a NUL byte", ErrInvalidDirName, name)
	}
	if len(name) >= 2 && name[1] == ':' &&
		((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) {
		return fmt.Errorf("%w: %q looks like a drive-letter path", ErrInvalidDirName, name)
	}
	if len(name) > 255 {
		return fmt.Errorf("%w: %q is longer than 255 bytes", ErrInvalidDirName, name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %q contains a control character", ErrInvalidDirName, name)
		}
	}
	// Cross-check against the config package's validator so the two can never
	// disagree about what names a WorldPath may carry.
	if _, err := config.PathSegment(WorldPathPrefix + "/" + WorldsDirName + "/" + name); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalidDirName, name, err)
	}
	return nil
}

// backupFile copies src to src+".panel.bak" (mode 0644), replacing any previous
// backup. It is the §6.2 "写前备份" rule.
func backupFile(src string, data []byte) error {
	if err := os.WriteFile(src+".panel.bak", data, 0o644); err != nil {
		return fmt.Errorf("world: write backup %s.panel.bak: %w", src, err)
	}
	return nil
}

// writeFileAtomic writes data to path via a temporary file in the same
// directory, so a crash cannot leave a half-written config.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".scnetm-world-*")
	if err != nil {
		return fmt.Errorf("world: create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("world: write temp for %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("world: chmod temp for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("world: close temp for %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("world: replace %s: %w", path, err)
	}
	return nil
}

// stampNow is indirected so tests can freeze the clock for deterministic backup
// names.
var stampNow = time.Now
