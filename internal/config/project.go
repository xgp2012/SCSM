package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Project is a field-level view of a save's Worlds/<dir>/Project.json.
//
// # Why this is not a typed struct
//
// Project.json is a BOM-prefixed, type-annotated JSON document in which every
// value is a two-element array ["type", value], for example
//
//	{"Version":["string","2.4"],
//	 "Guid":["System.Guid","9e9a67f8-..."],
//	 "Subsystems":{"GameInfo":{"GameMode":["Game.GameMode","Harmless"],
//	                           "IslandSize":["Vector2","400,400"],
//	                           "MaxOnlinePlayerCount":["ushort",20]}}}
//
// Unmarshalling the whole save into a typed struct would silently drop the 34+
// GameInfo fields, the Subsystems.Players subtree and any field a future server
// build adds (plan §2.3, §6.3.1). This type therefore keeps the raw bytes and a
// generic parsed view, and edits single fields by rewrapping the value in its
// ["type", value] envelope.
//
// # Write policy (plan §6.3.1 — frozen)
//
// ServerSetting.json is the SINGLE SOURCE OF WRITE TRUTH. Editing a save is
// intended for read-only display plus advanced, offline corrections — for
// example after importing a world authored elsewhere, or at the user's explicit
// request. It is only safe while the owning instance is stopped.
//
// This type enforces that policy: creating a writable Project requires
// AllowAdvancedEdit to be set, and every derived field additionally requires its
// own explicit opt-in.
type Project struct {
	// AllowAdvancedEdit gates all writes. LoadProject returns a display-only
	// Project; call ProjectEditor (or set this to true) to unlock SetField.
	AllowAdvancedEdit bool

	path    string
	raw     []byte              // original bytes, BOM included when present
	body    []byte              // raw without the BOM
	root    map[string]any      // parsed view; values are []any{type, value}
	order   map[string][]string // document key order, keyed by object path
	hadBOM  bool                // whether the original carried a UTF-8 BOM
	dirty   bool                // whether SetField modified anything
	unknown json.RawMessage     // unused placeholder kept for future extensions
}

// Project sub-objects the panel addresses by name.
const (
	gameInfoPath       = "Subsystems.GameInfo"
	playersPath        = "Subsystems.Players"
	gameInfoFieldSep   = "."
	maxOnlineFieldName = "MaxOnlinePlayerCount"
)

// Derived or identity fields. Writing them is refused unless the caller opts in
// with an explicit allow-derived flag: the engine owns their value.
const (
	// FieldWorldSeed is an int DERIVED from WorldSeedString. The panel must
	// write only WorldSeedString and treat WorldSeed as read-only: writing it
	// directly would desynchronise the seed the engine regenerates on load.
	// (Evidence: input "999" produced WorldSeed=5130 — plan §6.3.1, §A.5.2.)
	FieldWorldSeed = "WorldSeed"
	// FieldWorldDirectoryName must mirror ServerSetting.WorldPath. It is managed
	// by the server, never hand-edited (plan §6.3.1: 与 WorldPath 联动，勿手改).
	FieldWorldDirectoryName = "WorldDirectoryName"
	// FieldTotalElapsedGameTime is engine-maintained play time.
	FieldTotalElapsedGameTime = "TotalElapsedGameTime"
	// FieldOriginalSerializationVersion is the save schema version, read-only.
	FieldOriginalSerializationVersion = "OriginalSerializationVersion"
	// FieldRunServer controls whether the save runs as a server. The plan marks
	// it 勿随意改; it stays writable only behind the advanced-edit gate.
	FieldRunServer = "RunServer"
)

// derivedFields are refused by SetField unless the caller passes an explicit
// allowDerived opt-in (plan requirement 4).
var derivedFields = map[string]bool{
	FieldWorldSeed:                    true,
	FieldWorldDirectoryName:           true,
	FieldTotalElapsedGameTime:         true,
	FieldOriginalSerializationVersion: true,
	FieldRunServer:                    true,
}

// ErrAdvancedEditRequired is returned when a write is attempted on a Project
// that has not opted into save editing. It encodes the frozen policy: the panel
// edits ServerSetting.json, not the save.
var ErrAdvancedEditRequired = errors.New(
	"config: save-file editing requires AllowAdvancedEdit: ServerSetting.json is the single source " +
		"of write truth (plan §6.3.1); save edits are for read-only display plus advanced offline " +
		"corrections with the instance stopped")

// ErrDerivedField is returned when a derived/engine-owned field is written
// without an explicit opt-in.
type ErrDerivedField struct {
	Field string
}

func (e *ErrDerivedField) Error() string {
	extra := ""
	if e.Field == FieldWorldSeed {
		extra = "; write WorldSeedString instead — WorldSeed is derived from it (input \"999\" produced 5130)"
	}
	if e.Field == FieldWorldDirectoryName {
		extra = "; write ServerSetting.WorldPath instead — WorldDirectoryName mirrors it"
	}
	return fmt.Sprintf("config: field %q is derived/engine-owned and must not be written%s", e.Field, extra)
}

// LoadProject reads a save file. The returned Project is display-only until
// AllowAdvancedEdit is set (see ProjectEditor).
func LoadProject(path string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read Project.json %q: %w", path, err)
	}
	p, err := ParseProject(data)
	if err != nil {
		return nil, fmt.Errorf("config: parse Project.json %q: %w", path, err)
	}
	p.path = path
	return p, nil
}

// ProjectEditor loads a save for ADVANCED editing. The boolean must be true and
// is required so that the frozen write policy is impossible to bypass by
// accident; the caller is also responsible for confirming the instance is
// stopped (plan §6.3.1).
func ProjectEditor(path string, allowAdvancedEdit bool) (*Project, error) {
	if !allowAdvancedEdit {
		return nil, ErrAdvancedEditRequired
	}
	p, err := LoadProject(path)
	if err != nil {
		return nil, err
	}
	p.AllowAdvancedEdit = true
	return p, nil
}

// ParseProject parses in-memory save bytes.
func ParseProject(data []byte) (*Project, error) {
	p := &Project{
		raw:    append([]byte(nil), data...),
		hadBOM: hasBOM(data),
	}
	p.body = trimBOM(data)

	root := map[string]any{}
	if err := json.Unmarshal(p.body, &root); err != nil {
		return nil, fmt.Errorf("config: parse JSON: %w", err)
	}
	p.root = root
	p.order = map[string][]string{}
	if err := recordKeyOrder(p.order, p.body); err != nil {
		return nil, err
	}
	return p, nil
}

// recordKeyOrder recovers the DOCUMENT key order of every JSON object.
//
// encoding/json decodes objects into Go maps, which lose ordering. Rendering
// therefore uses this separately captured order so that a save round trip keeps
// its field order instead of being re-sorted alphabetically — an unnecessary and
// noisy diff on a file the engine owns.
//
// Annotation arrays are traversed too, because `Palette` is an object nested
// inside one.
func recordKeyOrder(order map[string][]string, body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	var walk func(path string) error
	walk = func(path string) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '[':
			return skipContainer(dec)
		case '{':
			// handled below
		default:
			return nil
		}

		var keys []string
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("config: unexpected JSON object key %T", keyTok)
			}
			keys = append(keys, key)
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if err := walk(childPath); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil { // closing '}'
			return err
		}
		order[path] = keys
		return nil
	}

	if err := walk(""); err != nil {
		return fmt.Errorf("config: recover key order: %w", err)
	}
	return nil
}

// skipContainer consumes a balanced JSON array or object.
func skipContainer(dec *json.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// Path returns the file this Project was loaded from, if any.
func (p *Project) Path() string { return p.path }

// HasBOM reports whether the loaded document carried a UTF-8 BOM. The server
// writes one; the panel re-emits it on save so compatibility is preserved.
func (p *Project) HasBOM() bool { return p != nil && p.hadBOM }

// Dirty reports whether SetField changed anything.
func (p *Project) Dirty() bool { return p != nil && p.dirty }

// GameInfo returns the decoded Subsystems.GameInfo map: each ["type", value]
// entry is unwrapped to its value, so GameMode reads "Harmless", a bool reads
// true and a number reads 20. The map is a fresh copy; mutating it does not
// affect the Project — use SetField to change the file.
func (p *Project) GameInfo() (map[string]any, error) {
	if p == nil {
		return nil, errors.New("config: nil Project")
	}
	node, err := p.subNode(gameInfoPath)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(node))
	for k, v := range node {
		out[k] = decodeAnnotated(v)
	}
	return out, nil
}

// RawField returns the type annotation and value bytes of a GameInfo field,
// exactly as they appear in the document, plus whether the field exists.
func (p *Project) RawField(name string) (typ string, val json.RawMessage, ok bool) {
	if p == nil {
		return "", nil, false
	}
	node, err := p.subNode(gameInfoPath)
	if err != nil {
		return "", nil, false
	}
	raw, exists := node[name]
	if !exists {
		return "", nil, false
	}
	typ, value, ok := splitAnnotated(raw)
	if !ok {
		return "", nil, false
	}
	return typ, value, true
}

// TypedField is a convenience wrapper: it decodes the field and reports whether
// the type annotation matched one of the requested types.
func (p *Project) TypedField(name string, want ...string) (typ string, val any, ok bool) {
	typ, raw, ok := p.RawField(name)
	if !ok {
		return "", nil, false
	}
	for _, w := range want {
		if strings.EqualFold(w, typ) {
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				return typ, nil, false
			}
			return typ, decoded, true
		}
	}
	return typ, nil, false
}

// SetField rewrites one GameInfo field in its ["type", value] envelope.
//
// The type annotation is taken from typ when non-empty, otherwise from the
// field's existing annotation; an unknown field with no annotation defaults to
// "string". Only that field's bytes change — every other field, the
// Subsystems.Players subtree and any future unknown subtree are preserved.
//
// It refuses:
//   - any write when AllowAdvancedEdit is false (ErrAdvancedEditRequired);
//   - derived/engine-owned fields (ErrDerivedField) unless opts allow them.
func (p *Project) SetField(name string, typ string, value any, opts ...SetOption) error {
	if p == nil {
		return errors.New("config: nil Project")
	}
	var cfg setOptions
	for _, o := range opts {
		o(&cfg)
	}
	if !p.AllowAdvancedEdit {
		return ErrAdvancedEditRequired
	}
	if derivedFields[name] && !cfg.allowDerived {
		return &ErrDerivedField{Field: name}
	}

	node, err := p.subNode(gameInfoPath)
	if err != nil {
		return err
	}
	if typ == "" {
		if existing, exists := node[name]; exists {
			if t, _, ok := splitAnnotated(existing); ok {
				typ = t
			}
		}
	}
	if typ == "" {
		typ = "string"
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("config: encode field %q: %w", name, err)
	}
	node[name] = []any{typ, json.RawMessage(encoded)}
	p.dirty = true
	return nil
}

// SetOption customises SetField.
type SetOption func(*setOptions)

type setOptions struct{ allowDerived bool }

// AllowDerivedField explicitly permits writing a derived/engine-owned field.
//
// Use it ONLY when reproducing a value the engine itself produced (for example
// restoring a backup byte for byte). Writing WorldSeed this way is how a caller
// acknowledges that the seed will be regenerated from WorldSeedString on the
// next load.
func AllowDerivedField() SetOption {
	return func(o *setOptions) { o.allowDerived = true }
}

// SaveTo writes the save back to disk with a UTF-8 BOM, backing the previous
// file up to <path>.panel.bak first (plan §6.2/§6.5: 写前备份).
//
// Writing a save requires AllowAdvancedEdit: the same policy gate as SetField.
func (p *Project) SaveTo(path string) error {
	if p == nil {
		return errors.New("config: nil Project")
	}
	if !p.AllowAdvancedEdit {
		return ErrAdvancedEditRequired
	}
	out, err := p.Bytes()
	if err != nil {
		return err
	}
	if err := BackupFile(path); err != nil {
		return err
	}
	if err := writeFileAtomic(path, out); err != nil {
		return fmt.Errorf("config: write Project.json %q: %w", path, err)
	}
	p.path = path
	p.dirty = false
	return nil
}

// Bytes renders the document: a UTF-8 BOM followed by the JSON.
//
// The panel always re-emits the BOM, whether or not the source had one, because
// the server writes one and losing it may break compatibility (plan §2.3).
//
// Annotation pairs are rendered COMPACTLY, as `["Game.GameMode","Harmless"]`,
// matching the format the server writes (plan §2.3, §A.5.2). A plain
// json.MarshalIndent would instead explode every pair across five lines and the
// file would no longer look like a real save.
func (p *Project) Bytes() ([]byte, error) {
	if p == nil {
		return nil, errors.New("config: nil Project")
	}
	var buf bytes.Buffer
	if err := writeAnnotatedJSON(&buf, p.root, 0, p.order, ""); err != nil {
		return nil, fmt.Errorf("config: format Project.json: %w", err)
	}
	out := make([]byte, 0, len(bomPrefix)+buf.Len()+1)
	out = append(out, bomPrefix...)
	out = append(out, buf.Bytes()...)
	out = append(out, '\n')
	return out, nil
}

// writeAnnotatedJSON renders a decoded JSON tree with two-space indentation,
// emitting annotation arrays inline.
//
// A container collapses onto one line when it IS an annotation, i.e. an array of
// exactly two elements whose first element is a string — the ["type", value]
// envelope the engine writes. Everything else is expanded one level per line,
// which reproduces the shape of a real Project.json.
func writeAnnotatedJSON(w *bytes.Buffer, v any, depth int, order map[string][]string, path string) error {
	if isAnnotation(v) {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		w.Write(raw)
		return nil
	}

	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			w.WriteString("{}")
			return nil
		}
		keys := orderedKeys(order[path], t)
		w.WriteString("{\n")
		for i, k := range keys {
			w.WriteString(strings.Repeat("  ", depth+1))
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			w.Write(kb)
			w.WriteString(": ")
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			if err := writeAnnotatedJSON(w, t[k], depth+1, order, childPath); err != nil {
				return err
			}
			if i < len(keys)-1 {
				w.WriteByte(',')
			}
			w.WriteByte('\n')
		}
		w.WriteString(strings.Repeat("  ", depth))
		w.WriteByte('}')
	case []any:
		if len(t) == 0 {
			w.WriteString("[]")
			return nil
		}
		w.WriteString("[\n")
		for i, e := range t {
			w.WriteString(strings.Repeat("  ", depth+1))
			if err := writeAnnotatedJSON(w, e, depth+1, order, path); err != nil {
				return err
			}
			if i < len(t)-1 {
				w.WriteByte(',')
			}
			w.WriteByte('\n')
		}
		w.WriteString(strings.Repeat("  ", depth))
		w.WriteByte(']')
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		w.Write(raw)
	}
	return nil
}

// orderedKeys returns the keys of an object in document order, appending any
// key the recorded order does not know (a field added by SetField) in sorted
// position so the output stays deterministic.
func orderedKeys(recorded []string, node map[string]any) []string {
	keys := make([]string, 0, len(node))
	seen := make(map[string]bool, len(node))
	for _, k := range recorded {
		if _, ok := node[k]; !ok {
			continue // removed since parsing
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	var extra []string
	for k := range node {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return append(keys, extra...)
}

// isAnnotation reports whether v is an ["type", value] envelope.
func isAnnotation(v any) bool {
	pair, ok := v.([]any)
	if !ok || len(pair) != 2 {
		return false
	}
	_, isStr := pair[0].(string)
	return isStr
}

// MaxOnlinePlayerCount reads GameInfo.MaxOnlinePlayerCount (type "ushort").
func (p *Project) MaxOnlinePlayerCount() (int, bool) {
	v, ok := p.numberField(maxOnlineFieldName)
	if !ok {
		return 0, false
	}
	return int(v), true
}

// DisplayName reads GameInfo.WorldName — the DISPLAY name. It is unrelated to
// the on-disk directory, which is decided by ServerSetting.WorldPath (plan §2.7).
func (p *Project) DisplayName() (string, bool) { return p.stringField("WorldName") }

// Guid reads the save's top-level Guid (["System.Guid", "..."]).
func (p *Project) Guid() (string, bool) {
	if p == nil {
		return "", false
	}
	raw, ok := p.root["Guid"]
	if !ok {
		return "", false
	}
	_, value, ok := splitAnnotated(raw)
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return "", false
	}
	return s, true
}

// WorldMode reads GameInfo.GameMode as a STRING enum name ("Harmless").
//
// The save stores the enum NAME while ServerSetting.json stores the INTEGER;
// use GameModeValue to convert (plan §6.2.1, §6.3.1).
func (p *Project) WorldMode() (string, bool) { return p.stringField("GameMode") }

// WorldDirectoryName reads GameInfo.WorldDirectoryName, which mirrors
// ServerSetting.WorldPath exactly (`app:/Worlds/<dir>`).
func (p *Project) WorldDirectoryName() (string, bool) {
	return p.stringField(FieldWorldDirectoryName)
}

// WorldDirectorySegment returns the last path segment of WorldDirectoryName —
// the actual directory under <instance>/Worlds/ — validated by PathSegment.
func (p *Project) WorldDirectorySegment() (string, bool) {
	dir, ok := p.WorldDirectoryName()
	if !ok {
		return "", false
	}
	seg, err := PathSegment(dir)
	if err != nil {
		return "", false
	}
	return seg, true
}

// SeedString reads GameInfo.WorldSeedString — the raw seed the user typed and
// the ONLY seed field the panel may write.
func (p *Project) SeedString() (string, bool) { return p.stringField("WorldSeedString") }

// Seed reads GameInfo.WorldSeed — the DERIVED integer. Read-only by policy.
func (p *Project) Seed() (int, bool) {
	v, ok := p.numberField(FieldWorldSeed)
	if !ok {
		return 0, false
	}
	return int(v), true
}

// Players returns the raw Subsystems.Players subtree. It is exposed so callers
// can confirm it survives a write; the panel does not model it.
func (p *Project) Players() (map[string]any, bool) {
	if p == nil {
		return nil, false
	}
	subs, ok := p.root["Subsystems"].(map[string]any)
	if !ok {
		return nil, false
	}
	players, ok := subs["Players"].(map[string]any)
	if !ok {
		return nil, false
	}
	return players, true
}

// GameInfoFields returns every GameInfo field name paired with its type
// annotation, in a stable (sorted) order.
func (p *Project) GameInfoFields() map[string]string {
	out := map[string]string{}
	node, err := p.subNode(gameInfoPath)
	if err != nil {
		return out
	}
	for k, v := range node {
		if t, _, ok := splitAnnotated(v); ok {
			out[k] = t
		}
	}
	return out
}

// stringField decodes a GameInfo field as a string.
func (p *Project) stringField(name string) (string, bool) {
	if p == nil {
		return "", false
	}
	_, raw, ok := p.RawField(name)
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// numberField decodes a GameInfo field as a JSON number.
func (p *Project) numberField(name string) (float64, bool) {
	if p == nil {
		return 0, false
	}
	_, raw, ok := p.RawField(name)
	if !ok {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	return f, true
}

// subNode walks a dotted path (e.g. "Subsystems.GameInfo") to a JSON object.
func (p *Project) subNode(path string) (map[string]any, error) {
	if p == nil {
		return nil, errors.New("config: nil Project")
	}
	cur := p.root
	for _, part := range strings.Split(path, gameInfoFieldSep) {
		next, ok := cur[part].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("config: Project.json has no object at %q (missing %q)", path, part)
		}
		cur = next
	}
	return cur, nil
}

// splitAnnotated splits a ["type", value] pair into its two halves.
//
// Tolerant by design: a bare value (no annotation) returns ok=false with the
// caller falling back to a raw read, so an older or hand-edited save is not
// mistaken for corruption.
func splitAnnotated(v any) (typ string, val json.RawMessage, ok bool) {
	pair, isPair := v.([]any)
	if !isPair || len(pair) != 2 {
		return "", nil, false
	}
	t, isStr := pair[0].(string)
	if !isStr {
		return "", nil, false
	}
	encoded, err := json.Marshal(pair[1])
	if err != nil {
		return "", nil, false
	}
	return t, json.RawMessage(encoded), true
}

// decodeAnnotated unwraps one ["type", value] pair for display. A bare value is
// returned as-is.
func decodeAnnotated(v any) any {
	if _, val, ok := splitAnnotated(v); ok {
		var decoded any
		if err := json.Unmarshal(val, &decoded); err == nil {
			return decoded
		}
	}
	return v
}

// Overlap describes a field that exists in BOTH ServerSetting.json and a save's
// Subsystems.GameInfo.
//
// The panel must respect the frozen write policy for these: ServerSetting.json
// is the single source of write truth (the server applies it to the save on
// start), so the UI edits THAT file, and the save copy is shown read-only.
// Editing both produces the classic "I changed it and nothing happened" or
// "it reverted after restart" confusion (plan §6.3.1).
type Overlap struct {
	// ServerSettingKey is the JSON key in ServerSetting.json.
	ServerSettingKey string
	// GameInfoKey is the field name in the save's Subsystems.GameInfo.
	GameInfoKey string
	// Note records any format or semantic difference between the two.
	Note string
}

// OverlappingFields is the authoritative list the API layer must respect: every
// entry names the same logical setting in ServerSetting.json and in
// Subsystems.GameInfo.
//
// Derived from plan §6.3.1 (the overlap is listed there explicitly plus the
// field-by-field correspondence table).
func OverlappingFields() []Overlap {
	return []Overlap{
		{ServerSettingKey: "WorldName", GameInfoKey: "WorldName",
			Note: "display name only; the on-disk directory comes from WorldPath/WorldDirectoryName (plan §2.7)"},
		{ServerSettingKey: "GameMode", GameInfoKey: "GameMode",
			Note: "int in ServerSetting.json vs string enum name in the save; use GameModeName/GameModeValue"},
		{ServerSettingKey: "WorldPassword", GameInfoKey: "Password", Note: "same value, different key name"},
		{ServerSettingKey: "WorldDaySpeed", GameInfoKey: "DaySpeed", Note: "same value, different key name"},
		{ServerSettingKey: "WorldRecoverySpeed", GameInfoKey: "RecoverFator",
			Note: "same value, different key name; note the engine's spelling RecoverFator"},
		{ServerSettingKey: "WorldMaxPlayers", GameInfoKey: "MaxOnlinePlayerCount", Note: "same value, different key name"},
		{ServerSettingKey: "PVPEnabled", GameInfoKey: "IsFriendlyFireEnabled", Note: "same value, different key name"},
		{ServerSettingKey: "WorldDisableBlocks", GameInfoKey: "DisableBlocks", Note: "same value, different key name"},
		{ServerSettingKey: "SeasonChanging", GameInfoKey: "AreSeasonsChanging", Note: "same value, different key name"},
		{ServerSettingKey: "RandomSpawnPosition", GameInfoKey: "RandomSpawnPosition", Note: "same key name"},
		{ServerSettingKey: "WorldKeywordBlocking", GameInfoKey: "KeywordBlocking", Note: "same value, different key name"},
		{ServerSettingKey: "WorldSeed", GameInfoKey: "WorldSeedString",
			Note: "both string seeds; the save ALSO holds the derived int WorldSeed (read-only)"},
		{ServerSettingKey: "WorldPath", GameInfoKey: "WorldDirectoryName",
			Note: "must be equal; WorldPath is authoritative for the on-disk directory"},
	}
}
