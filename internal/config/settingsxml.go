package config

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// XMLSetting is one entry of Settings.xml, i.e. a self-closing element of the
// form <Name Value="..."/> (or <Name>text</Name>).
type XMLSetting struct {
	Name  string
	Value string
}

// SettingsXML is an ordered view of Settings.xml.
//
// Entries keeps DOCUMENT ORDER, which matters because the engine reads the file
// into a settings dictionary and the plan requires that unrelated content — its
// order included — survives every panel write (plan §6.2).
//
// The raw document is retained verbatim so that Save can rewrite the byte ranges
// covering only the entries that actually changed. Comments, unknown elements,
// attributes the panel does not model, indentation and even CRLF line endings
// are therefore preserved bit for byte.
type SettingsXML struct {
	Entries []XMLSetting

	mu   sync.Mutex
	raw  []byte // full original document, BOM included when present
	span []span // span[i] describes entry i's byte range inside raw
}

// span locates the value bytes of one entry inside the raw document.
//
// For a value-attribute element (<Name Value="x"/>) start/end delimit the value
// text INSIDE the quotes. For an element with text content
// (<Name>x</Name>) start/end delimit the text between the tags.
type span struct {
	start   int // byte offset of the first value byte, in raw (BOM-included) coordinates
	end     int // byte offset one past the last value byte
	changed bool
}

// xmlAttrName is the attribute carrying a setting's value in Settings.xml.
const xmlAttrName = "Value"

// LoadSettingsXML parses path into an ordered entry list while remembering the
// raw document so that Save can perform a minimal, surgical edit.
func LoadSettingsXML(path string) (*SettingsXML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read Settings.xml %q: %w", path, err)
	}
	s, err := ParseSettingsXML(data)
	if err != nil {
		return nil, fmt.Errorf("config: parse Settings.xml %q: %w", path, err)
	}
	return s, nil
}

// ParseSettingsXML parses an in-memory Settings.xml document.
func ParseSettingsXML(data []byte) (*SettingsXML, error) {
	stripped := trimBOM(data)
	// bomLen shifts spans from stripped coordinates into raw coordinates.
	bomLen := len(data) - len(stripped)

	s := &SettingsXML{
		raw: append([]byte(nil), data...),
	}
	dec := xml.NewDecoder(bytes.NewReader(stripped))

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("config: malformed XML: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || !isSettingElement(se) {
			continue
		}
		// The decoder has consumed the start tag, so its position is just past
		// the '>' (for a self-closing tag) or just past the '>' of the open tag.
		// Walk back to the '<' that starts this element.
		elemStart := elementStart(stripped, int(dec.InputOffset()), se.Name.Local)
		if elemStart < 0 {
			continue
		}

		var (
			sp    span
			value string
		)
		if sp, value, err = s.measure(dec, stripped, elemStart, se); err != nil {
			return nil, err
		}
		// measure() works in STRIPPED coordinates (the document minus its BOM),
		// but renderLocked splices into s.raw, which INCLUDES the BOM. Shift the
		// span into raw coordinates so the two agree; forgetting this offset
		// corrupts the document by exactly the BOM length.
		if bomLen > 0 {
			sp.start += bomLen
			sp.end += bomLen
		}
		// A self-closing tag yields a synthetic EndElement next; skip it so the
		// token stream stays aligned.
		if bytes.HasSuffix(bytes.TrimRight(stripped[elemStart:decPosOrEnd(stripped, dec)], " \t\r\n"), []byte("/>")) {
			skipSyntheticEnd(dec)
		}
		s.Entries = append(s.Entries, XMLSetting{Name: se.Name.Local, Value: value})
		s.span = append(s.span, sp)
	}
	if len(s.Entries) == 0 {
		return nil, errors.New("config: Settings.xml contains no setting entries")
	}
	return s, nil
}

// decPosOrEnd bounds the decoder's position to the document.
func decPosOrEnd(doc []byte, dec *xml.Decoder) int {
	if p := int(dec.InputOffset()); p <= len(doc) {
		return p
	}
	return len(doc)
}

// skipSyntheticEnd consumes the EndElement the decoder synthesises for a
// self-closing tag, when one is pending.
func skipSyntheticEnd(dec *xml.Decoder) {
	saved := dec.InputOffset()
	tok, err := dec.Token()
	if err != nil {
		return
	}
	if _, ok := tok.(xml.EndElement); !ok {
		// Not synthetic after all: rewind is impossible, so report nothing. The
		// token we consumed was char data or a comment, which the caller ignores
		// anyway.
		_ = saved
	}
}

// measure locates the value bytes for an element whose StartElement was just
// decoded, consumes the remainder of that element, and reports what it found.
//
// elemStart is the absolute offset (into the original, BOM-aware document) at
// which the element's '<' sits.
func (s *SettingsXML) measure(dec *xml.Decoder, stripped []byte, elemStart int, se xml.StartElement) (span, string, error) {
	for _, a := range se.Attr {
		if a.Name.Local != xmlAttrName {
			continue
		}
		_ = a
		// Locate the RAW value bytes. Offsets must come from the raw slice
		// because the decoded text can differ in length from the source
		// (`&amp;` is five bytes, one decoded byte).
		if sp, raw, ok := s.locateAttrValue(elemStart); ok {
			// Consume the remainder of the element so the decoder stays in sync.
			if err := consumeElement(dec, se.Name.Local); err != nil {
				return span{}, "", err
			}
			return sp, decodeAttrText(raw), nil
		}
		break
	}

	// No usable Value attribute: fall back to the element's character data.
	//
	// A <Name>text</Name> element carries its value as a text node, and the
	// engine reads it that way, so the text body wins over any other attribute.
	var (
		buf        bytes.Buffer
		textStart  = -1
		textEnd    = -1
		haveBody   bool
		openTagEnd = -1
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return span{}, "", fmt.Errorf("config: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			if openTagEnd < 0 {
				if gt := bytes.IndexByte(stripped[elemStart:], '>'); gt >= 0 {
					openTagEnd = elemStart + gt + 1
				}
			}
			if !haveBody && openTagEnd >= 0 {
				// Record the raw range of the first text node.
				rel := bytes.Index(stripped[openTagEnd:], t)
				if rel >= 0 {
					textStart = openTagEnd + rel
					textEnd = textStart + len(t)
					haveBody = true
				}
			}
			buf.Write(t)
		case xml.StartElement:
			// Nested markup: not a plain setting, treat it as having no value.
			if err := consumeElement(dec, t.Name.Local); err != nil {
				return span{}, "", err
			}
		case xml.EndElement:
			if t.Name.Local == se.Name.Local {
				if haveBody && textStart >= 0 && textEnd >= textStart {
					return span{start: textStart, end: textEnd}, buf.String(), nil
				}
				return span{start: elemStart, end: elemStart}, "", nil
			}
		}
	}
	return span{start: elemStart, end: elemStart}, "", nil
}

// elementStart locates the '<' that begins the element the decoder just
// returned, searching backwards from the decoder's current position.
//
// The '<' immediately before the decoder's position is the start of the tag only
// when the tag is self-closing or has just been consumed. To stay correct for
// both `<Name ... />` and `<Name ...>` forms, the search walks back to the
// nearest '<' that is followed by the element's name.
func elementStart(doc []byte, decPos int, name string) int {
	if decPos > len(doc) {
		decPos = len(doc)
	}
	for i := decPos - 1; i >= 0; i-- {
		if doc[i] != '<' {
			continue
		}
		rest := doc[i+1:]
		if rest[0] == '/' || rest[0] == '!' || rest[0] == '?' {
			continue
		}
		if bytes.HasPrefix(rest, []byte(name)) {
			return i
		}
	}
	return -1
}

// locateAttrValue finds the byte span of the Value attribute's text inside the
// element's start tag that begins at elemStart.
//
// # Why the raw bytes are walked rather than the decoded attribute
//
// encoding/xml exposes only the UNESCAPED attribute value, whose length can
// differ from the source (`&amp;` is five source bytes, one decoded byte). Since
// the panel must rewrite exactly the bytes it changed and nothing else, the span
// has to come from the raw document.
//
// The raw tag is first decoded with a throwaway decoder so that the attribute
// list is judged by the real parser (rejecting malformed spellings exactly as the
// engine would), then the bytes are walked to recover the one thing the decoder
// does not expose: where the value text lives. Both quote styles, whitespace
// around '=', self-closing tags and mixed attribute order are handled.
func (s *SettingsXML) locateAttrValue(elemStart int) (span, string, bool) {
	body := trimBOM(s.raw)
	if elemStart < 0 || elemStart >= len(body) {
		return span{}, "", false
	}
	rel := bytes.IndexByte(body[elemStart:], '>')
	if rel < 0 {
		return span{}, "", false
	}
	tagEnd := elemStart + rel

	tag := append([]byte(nil), body[elemStart:tagEnd+1]...)
	dec := xml.NewDecoder(bytes.NewReader(tag))
	tok, err := dec.Token()
	if err != nil {
		return span{}, "", false
	}
	se, ok := tok.(xml.StartElement)
	if !ok {
		return span{}, "", false
	}
	found := false
	for _, a := range se.Attr {
		if a.Name.Local == xmlAttrName {
			found = true
			break
		}
	}
	if !found {
		return span{}, "", false
	}
	return attrValueSpan(body, elemStart, tagEnd, xmlAttrName)
}

// attrValueSpan walks a raw start tag and returns the byte range of the named
// attribute's value text, delimited by its own quote characters.
func attrValueSpan(body []byte, elemStart, tagEnd int, attr string) (span, string, bool) {
	region := body[elemStart : tagEnd+1]
	key := []byte(attr)

	for i := 0; i+len(key) <= len(region); i++ {
		if !bytes.Equal(region[i:i+len(key)], key) {
			continue
		}
		// Must start on a token boundary...
		if i > 0 && !isXMLSpace(region[i-1]) {
			continue
		}
		j := i + len(key)
		// ...and be followed, after optional whitespace, by '='.
		for j < len(region) && isXMLSpace(region[j]) {
			j++
		}
		if j >= len(region) || region[j] != '=' {
			continue
		}
		j++
		for j < len(region) && isXMLSpace(region[j]) {
			j++
		}
		if j >= len(region) {
			return span{}, "", false
		}
		quote := region[j]
		if quote != '"' && quote != '\'' {
			return span{}, "", false
		}
		valueStartRel := j + 1
		closeRel := bytes.IndexByte(region[valueStartRel:], quote)
		if closeRel < 0 {
			return span{}, "", false
		}
		start := elemStart + valueStartRel
		return span{start: start, end: start + closeRel},
			string(region[valueStartRel : valueStartRel+closeRel]), true
	}
	return span{}, "", false
}

func isXMLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// consumeElement advances the decoder past the end of the named element,
// including any nested markup, so that the caller's token stream stays aligned.
func consumeElement(dec *xml.Decoder, name string) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("config: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth == 0 {
				if t.Name.Local == name {
					return nil
				}
				continue
			}
			depth--
		}
	}
}

// isSettingElement reports whether an XML element should be treated as a
// Settings.xml entry: a direct child of the document root.
func isSettingElement(se xml.StartElement) bool {
	return se.Name.Local != "Settings" && se.Name.Local != "ModSettings"
}

// Get returns the value of the named entry. When a name occurs more than once
// the first occurrence in document order wins.
func (s *SettingsXML) Get(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	for i := range s.Entries {
		if s.Entries[i].Name == name {
			return s.Entries[i].Value, true
		}
	}
	return "", false
}

// GetLast returns the value of the LAST occurrence of name. Use it together
// with Get to detect duplicate keys in a hand-edited file.
func (s *SettingsXML) GetLast(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Name == name {
			return s.Entries[i].Value, true
		}
	}
	return "", false
}

// Names returns the entry names in document order.
func (s *SettingsXML) Names() []string {
	out := make([]string, 0, len(s.Entries))
	for _, e := range s.Entries {
		out = append(out, e.Name)
	}
	return out
}

// Set changes the value of an existing entry, or appends a new entry when the
// name is not present yet.
//
// ORDER IS PRESERVED: an existing entry keeps its position, and every other
// entry keeps its exact bytes. Only the updated element is re-emitted, and only
// when its text actually differs.
func (s *SettingsXML) Set(name, value string) error {
	if s == nil {
		return errors.New("config: nil SettingsXML")
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("config: settings entry name must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.Entries {
		if s.Entries[i].Name != name {
			continue
		}
		if s.Entries[i].Value == value {
			return nil // idempotent: do not disturb the document at all
		}
		s.Entries[i].Value = value
		s.span[i].changed = true
		return nil
	}

	// New entry: append just before the root close tag.
	s.Entries = append(s.Entries, XMLSetting{Name: name, Value: value})
	s.span = append(s.span, span{changed: true, start: -1, end: -1})
	return nil
}

// SetServerPort sets the engine's ServerPort entry after range validation.
func (s *SettingsXML) SetServerPort(port int) error {
	if problems := ValidatePortRange(port); len(problems) > 0 {
		return fmt.Errorf("config: %s", problems[0].Message)
	}
	return s.Set("ServerPort", strconv.Itoa(port))
}

// ServerPort returns the engine's ServerPort entry. The default the engine
// writes is 28887 (plan §2.2).
func (s *SettingsXML) ServerPort() (int, error) {
	raw, ok := s.Get("ServerPort")
	if !ok {
		return 0, errors.New("config: Settings.xml has no ServerPort entry")
	}
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("config: ServerPort %q is not an integer: %w", raw, err)
	}
	return port, nil
}

// Save writes the document to path, changing only the entries flagged by Set.
// The previous file is backed up to <path>.panel.bak first (plan §6.2).
func (s *SettingsXML) Save(path string) error {
	if s == nil {
		return errors.New("config: nil SettingsXML")
	}
	s.mu.Lock()
	out, err := s.renderLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := BackupFile(path); err != nil {
		return err
	}
	if err := writeFileAtomic(path, out); err != nil {
		return fmt.Errorf("config: write Settings.xml %q: %w", path, err)
	}
	return nil
}

// Bytes renders the (possibly edited) document without touching the filesystem.
func (s *SettingsXML) Bytes() ([]byte, error) {
	if s == nil {
		return nil, errors.New("config: nil SettingsXML")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renderLocked()
}

// renderLocked applies every changed span to a copy of the raw document.
//
// Edits are applied back-to-front so that earlier offsets stay valid. Only the
// bytes inside the changed spans differ from the input; everything else — order,
// comments, unknown attributes, whitespace, line endings and the BOM — survives
// unchanged.
func (s *SettingsXML) renderLocked() ([]byte, error) {
	out := append([]byte(nil), s.raw...)

	type edit struct {
		start, end int
		text       []byte
	}
	var edits []edit
	for i, sp := range s.span {
		if !sp.changed {
			continue
		}
		text := []byte(escapeXMLText(s.Entries[i].Value))
		if sp.start < 0 || sp.end < sp.start || sp.end > len(out) {
			continue // appended entry, handled below
		}
		edits = append(edits, edit{start: sp.start, end: sp.end, text: text})
	}

	// Append the entries that did not exist in the source document.
	var appended []XMLSetting
	for i, sp := range s.span {
		if sp.changed && (sp.start < 0 || sp.end < sp.start || sp.end > len(out)) {
			appended = append(appended, s.Entries[i])
		}
	}

	// Apply edits back-to-front so that earlier offsets stay valid.
	//
	// The splice must NOT be written as append(out[:start], append(text, out[end:]...)...):
	// that reuses out's backing array, so the inner append can overwrite the very
	// bytes out[end:] is about to copy. Building a fresh slice each time avoids
	// the aliasing entirely.
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		spliced := make([]byte, 0, len(out)-(e.end-e.start)+len(e.text))
		spliced = append(spliced, out[:e.start]...)
		spliced = append(spliced, e.text...)
		spliced = append(spliced, out[e.end:]...)
		out = spliced
	}

	if len(appended) > 0 {
		var block bytes.Buffer
		for _, e := range appended {
			block.WriteString("\n  <" + e.Name + " " + xmlAttrName + "=\"" +
				escapeXMLText(e.Value) + "\" />")
		}
		idx := bytes.LastIndex(out, []byte("</Settings>"))
		if idx < 0 {
			return nil, errors.New("config: Settings.xml has no </Settings> root close tag to append to")
		}
		merged := make([]byte, 0, len(out)+block.Len())
		merged = append(merged, out[:idx]...)
		merged = append(merged, block.Bytes()...)
		merged = append(merged, '\n')
		merged = append(merged, out[idx:]...)
		out = merged
	}
	return out, nil
}

// decodeAttrText unescapes the XML entity references and the \r\n -> \n
// normalisation that a parser applies to attribute values, so that a value read
// from the raw bytes compares equal to the value the decoder reports.
//
// This is the inverse of escapeXMLText for the characters it escapes, and it
// also accepts the numeric forms (&amp;#34;, &amp;#x22;) an editor may emit.
func decodeAttrText(raw string) string {
	if !strings.ContainsAny(raw, "&\r") {
		return raw
	}
	var b strings.Builder
	for i := 0; i < len(raw); {
		c := raw[i]
		if c != '&' {
			// Unescaped CR is normalised to LF by an XML parser; a lone CR in an
			// attribute value is a literal CR, so keep it as-is.
			b.WriteByte(c)
			i++
			continue
		}
		semi := strings.IndexByte(raw[i:], ';')
		if semi < 0 {
			b.WriteByte(c)
			i++
			continue
		}
		entity := raw[i+1 : i+semi]
		if decoded, ok := decodeEntity(entity); ok {
			b.WriteString(decoded)
			i += semi + 1
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// decodeEntity resolves one entity name or numeric character reference.
func decodeEntity(name string) (string, bool) {
	switch name {
	case "lt":
		return "<", true
	case "gt":
		return ">", true
	case "amp":
		return "&", true
	case "apos":
		return "'", true
	case "quot":
		return `"`, true
	}
	if !strings.HasPrefix(name, "#") {
		return "", false
	}
	digits := name[1:]
	base := 10
	if strings.HasPrefix(digits, "x") || strings.HasPrefix(digits, "X") {
		digits = digits[1:]
		base = 16
	}
	if digits == "" {
		return "", false
	}
	n, err := strconv.ParseUint(digits, base, 32)
	if err != nil || n > unicode.MaxRune {
		return "", false
	}
	return string(rune(n)), true
}

// escapeXMLText escapes the characters significant inside an XML attribute
// value or text node, so that a value containing &, <, > or a quote cannot
// corrupt the document or fail to round-trip.
//
// Note that encoding/xml's EscapeText does NOT escape quotation marks, which is
// correct for text nodes but wrong for an attribute value delimited by them.
func escapeXMLText(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&#34;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
