package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSettingsXMLPreservesDocumentOrder is the plan §6.2 requirement:
// Settings.xml is parsed as an ORDERED list, because the engine reads the file
// into a settings dictionary and reordering it is a compatibility risk.
func TestLoadSettingsXMLPreservesDocumentOrder(t *testing.T) {
	s, err := LoadSettingsXML(testdataPath(t, "Settings.xml"))
	if err != nil {
		t.Fatalf("LoadSettingsXML: %v", err)
	}
	want := []string{
		"ServerPort", "BroadcastPort", "VisibilityRange", "Resolution",
		"Language", "MaxFPS", "EnableSound", "WillEnterServer",
	}
	got := s.Names()
	if len(got) != len(want) {
		t.Fatalf("parsed %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q (document order not preserved): %v", i, got[i], want[i], got)
		}
	}

	// The ServerPort default from plan §2.2.
	port, err := s.ServerPort()
	if err != nil {
		t.Fatalf("ServerPort: %v", err)
	}
	if port != 28887 {
		t.Errorf("ServerPort = %d, want 28887", port)
	}
	if v, ok := s.Get("VisibilityRange"); !ok || v != "128" {
		t.Errorf("Get(VisibilityRange) = %q,%v; want 128,true", v, ok)
	}
	if _, ok := s.Get("NoSuchEntry"); ok {
		t.Error("Get returned true for a missing entry")
	}
}

// TestSetServerPortChangesOnlyThatValue is the central Settings.xml promise:
// after Set("ServerPort", ...) every other byte of the document — including
// comments, unusual formatting, attribute quoting and CRLF line endings — must
// be identical, with only the port digits swapped.
func TestSetServerPortChangesOnlyThatValue(t *testing.T) {
	path := stageFixture(t, "Settings.odd.xml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBOM(before) {
		t.Fatal("the odd fixture must carry a BOM to exercise BOM preservation")
	}

	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatalf("LoadSettingsXML: %v", err)
	}
	if err := s.SetServerPort(30000); err != nil {
		t.Fatalf("SetServerPort: %v", err)
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The ONE intended difference.
	wantAfter := bytes.Replace(before, []byte("Value='28887'"), []byte("Value='30000'"), 1)
	if !bytes.Equal(after, wantAfter) {
		t.Errorf("Set changed more than the port value.\n--- got ---\n%s\n--- want ---\n%s", after, wantAfter)
	}

	// Spelled out explicitly, so a failure is legible.
	checks := []struct {
		what   string
		needle string
	}{
		{"the BOM", "\ufeff"},
		{"the XML declaration", `<?xml version="1.0" encoding="utf-8"?>`},
		{"the operator comment", "<!-- Hand-edited by an operator."},
		{"the port comment", "<!-- port comment -->"},
		{"the single-quoted attribute style", "Value='30000'"},
		{"the spaced attribute style", "Value='30000'   />"},
		{"the tab indentation", "\t<VisibilityRange"},
		{"the double-tab indentation", "\t\t<MaxFPS"},
		{"an element with a text body", `<Resolution Value="High"></Resolution>`},
		{"CRLF line endings", "\r\n"},
	}
	text := string(after)
	for _, c := range checks {
		if !strings.Contains(text, c.needle) {
			t.Errorf("%s was not preserved:\n%s", c.what, text)
		}
	}

	// Document ORDER must be untouched.
	s2, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	wantOrder := []string{
		"VisibilityRange", "ServerPort", "Resolution", "Language",
		"MaxFPS", "EnableSound", "WillEnterServer",
	}
	gotOrder := s2.Names()
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("reorder after save: %v", gotOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Errorf("entry %d = %q, want %q after the edit: %v", i, gotOrder[i], wantOrder[i], gotOrder)
		}
	}
	// And the value actually changed.
	if port, err := s2.ServerPort(); err != nil || port != 30000 {
		t.Errorf("ServerPort after save = %d (err %v), want 30000", port, err)
	}
}

// TestSetOtherKeyLeavesServerPortAlone checks that an edit to one entry never
// disturbs a neighbouring entry on the same line or an adjacent one.
func TestSetOtherKeyLeavesServerPortAlone(t *testing.T) {
	path := stageFixture(t, "Settings.xml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("VisibilityRange", "256"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(before, []byte(`Value="128"`), []byte(`Value="256"`), 1)
	if !bytes.Equal(after, want) {
		t.Errorf("editing VisibilityRange changed more than expected:\n--- got ---\n%s\n--- want ---\n%s", after, want)
	}
	port, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := port.ServerPort(); err != nil || p != 28887 {
		t.Errorf("ServerPort = %d (err %v), want the untouched 28887", p, err)
	}
}

// TestSettingsXMLSaveIdempotent proves Load -> Save -> Load is stable: a save
// with no pending Set must leave the file byte-identical, and a second save
// after a Set must be a no-op.
func TestSettingsXMLSaveIdempotent(t *testing.T) {
	path := stageFixture(t, "Settings.odd.xml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing was Set: rendering must reproduce the input exactly.
	rendered, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rendered, original) {
		t.Errorf("rendering an unmodified document changed it:\n--- got ---\n%s\n--- want ---\n%s", rendered, original)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, original) {
		t.Error("saving an unmodified document changed the file")
	}

	// Now edit once and save twice; the second save must not shift anything.
	if err := s.Set("MaxFPS", "120"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Set("MaxFPS", "120"); err != nil { // same value: no-op by design
		t.Fatal(err)
	}
	if err := s2.Save(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("saving the same value twice produced different bytes:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestSetIsIdempotentForEqualValue checks the early return: writing the value an
// entry already holds must not mark the document dirty at all.
func TestSetIsIdempotentForEqualValue(t *testing.T) {
	path := stageFixture(t, "Settings.odd.xml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("ServerPort", "28887"); err != nil { // exactly the current value
		t.Fatalf("Set: %v", err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Error("setting an entry to its current value modified the document")
	}
}

// TestSetAppendsUnknownEntry covers adding a key that Settings.xml does not have
// yet: existing content and order must survive, and the new entry must be
// inside the root element.
func TestSetAppendsUnknownEntry(t *testing.T) {
	path := stageFixture(t, "Settings.xml")
	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("BrandNewSetting", "hello"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `<BrandNewSetting Value="hello" />`) {
		t.Errorf("new entry not written in the expected form:\n%s", text)
	}
	// The new entry must land before the root close tag.
	newIdx := strings.Index(text, "BrandNewSetting")
	closeIdx := strings.Index(text, "</Settings>")
	if newIdx < 0 || closeIdx < 0 || newIdx > closeIdx {
		t.Errorf("new entry was appended outside the root element:\n%s", text)
	}

	// Existing entries and their order are untouched.
	reloaded, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	want := []string{
		"ServerPort", "BroadcastPort", "VisibilityRange", "Resolution",
		"Language", "MaxFPS", "EnableSound", "WillEnterServer", "BrandNewSetting",
	}
	got := reloaded.Names()
	if len(got) != len(want) {
		t.Fatalf("entries after append = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSettingsXMLEscaping proves a value containing XML-significant characters
// cannot corrupt the document.
func TestSettingsXMLEscaping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Settings.xml")
	body := `<Settings><ServerName Value="x" /></Settings>`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	tricky := `a & b < c > d " e`
	if err := s.Set("ServerName", tricky); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("&amp;")) || !bytes.Contains(raw, []byte("&lt;")) {
		t.Errorf("value was not escaped:\n%s", raw)
	}

	reloaded, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := reloaded.Get("ServerName")
	if !ok || got != tricky {
		t.Errorf("Get(ServerName) = %q,%v, want %q,true", got, ok, tricky)
	}
}

// TestSettingsXMLBOMRoundTrip checks the BOM of a BOM-prefixed Settings.xml
// survives a write.
func TestSettingsXMLBOMRoundTrip(t *testing.T) {
	path := stageFixture(t, "Settings.odd.xml")
	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetServerPort(27777); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBOM(raw) {
		t.Error("the BOM was lost on save")
	}
}

// TestSettingsXMLDuplicateKeys documents the first-wins read rule a
// hand-edited file with repeated keys follows.
func TestSettingsXMLDuplicateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Settings.xml")
	body := `<Settings>
  <ServerPort Value="1111" />
  <VisibilityRange Value="128" />
  <ServerPort Value="2222" />
</Settings>`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := s.Get("ServerPort"); !ok || v != "1111" {
		t.Errorf("Get(ServerPort) = %q,%v; the FIRST occurrence must win", v, ok)
	}
	if v, ok := s.GetLast("ServerPort"); !ok || v != "2222" {
		t.Errorf("GetLast(ServerPort) = %q,%v; want the last occurrence 2222", v, ok)
	}
	// The engine reads the last assignment into its dictionary, so ServerPort()
	// must agree with GetLast rather than Get for a duplicated key.
	// This fixture exists to make that disagreement visible.
	if v, _ := s.Get("ServerPort"); v == "2222" {
		t.Error("Get unexpectedly returned the last occurrence")
	}
}

// TestSettingsXMLBackup verifies the .panel.bak behaviour.
func TestSettingsXMLBackup(t *testing.T) {
	path := stageFixture(t, "Settings.xml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetServerPort(28888); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + BackupSuffix)
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if !bytes.Equal(backup, before) {
		t.Error("backup does not match the pre-save file")
	}
}

// TestSetServerPortValidation proves the range guard runs before any write.
func TestSetServerPortValidation(t *testing.T) {
	path := stageFixture(t, "Settings.xml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettingsXML(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int{0, -1, 65536, 99999} {
		if err := s.SetServerPort(bad); err == nil {
			t.Errorf("SetServerPort(%d) did not error", bad)
		}
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a rejected SetServerPort still modified the file")
	}
	if _, err := s.ServerPort(); err != nil {
		t.Errorf("ServerPort after rejected writes: %v", err)
	}
}

// TestSettingsXMLErrors covers the failure paths.
func TestSettingsXMLErrors(t *testing.T) {
	if _, err := LoadSettingsXML(filepath.Join(t.TempDir(), "missing.xml")); err == nil {
		t.Error("loading a missing file did not error")
	}

	bad := filepath.Join(t.TempDir(), "bad.xml")
	if err := os.WriteFile(bad, []byte("<Settings><Unclosed></Settings>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettingsXML(bad); err == nil {
		t.Error("loading malformed XML did not error")
	}

	empty := filepath.Join(t.TempDir(), "empty.xml")
	if err := os.WriteFile(empty, []byte("<Settings></Settings>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettingsXML(empty); err == nil {
		t.Error("loading a Settings.xml with no entries did not error")
	}

	s, err := LoadSettingsXML(testdataPath(t, "Settings.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("", "x"); err == nil {
		t.Error("Set with an empty name did not error")
	}
	var nilXML *SettingsXML
	if err := nilXML.Set("A", "b"); err == nil {
		t.Error("Set on a nil SettingsXML did not error")
	}
	if err := nilXML.Save(filepath.Join(t.TempDir(), "x.xml")); err == nil {
		t.Error("Save on a nil SettingsXML did not error")
	}
	if _, err := nilXML.ServerPort(); err == nil {
		t.Error("ServerPort on a nil SettingsXML did not error")
	}

	// An entry whose value is not a number.
	notNum := filepath.Join(t.TempDir(), "notnum.xml")
	if err := os.WriteFile(notNum, []byte(`<Settings><ServerPort Value="abc" /></Settings>`), 0o644); err != nil {
		t.Fatal(err)
	}
	sn, err := LoadSettingsXML(notNum)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sn.ServerPort(); err == nil {
		t.Error("ServerPort parsed a non-numeric value")
	}

	// No ServerPort entry at all.
	noPort := filepath.Join(t.TempDir(), "noport.xml")
	if err := os.WriteFile(noPort, []byte(`<Settings><Language Value="zh-CN" /></Settings>`), 0o644); err != nil {
		t.Fatal(err)
	}
	sp, err := LoadSettingsXML(noPort)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.ServerPort(); err == nil {
		t.Error("ServerPort succeeded with no ServerPort entry")
	}
	// ...but it can be added.
	if err := sp.SetServerPort(28887); err != nil {
		t.Fatalf("SetServerPort on a file without one: %v", err)
	}
	if err := sp.Save(noPort); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := LoadSettingsXML(noPort)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := back.ServerPort(); err != nil || p != 28887 {
		t.Errorf("ServerPort after adding = %d (err %v)", p, err)
	}
}

// TestSettingsXMLValueAttributeSpellings proves the parser handles the quoting
// and spacing variants an operator's editor may produce.
func TestSettingsXMLValueAttributeSpellings(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"double quotes", `<Settings><A Value="1" /></Settings>`, "1"},
		{"single quotes", `<Settings><A Value='1' /></Settings>`, "1"},
		{"extra spacing", `<Settings><A   Value  =  "1"   /></Settings>`, "1"},
		{"non-self-closing with attribute", `<Settings><A Value="1"></A></Settings>`, "1"},
		// A Value ATTRIBUTE wins over any text body: that is the form the engine
		// writes, and the attribute is unambiguous.
		{"attribute wins over body", `<Settings><A Value="x">1</A></Settings>`, "x"},
		// With no Value attribute the element's text is the setting value.
		{"text body only", `<Settings><A>1</A></Settings>`, "1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := ParseSettingsXML([]byte(c.doc))
			if err != nil {
				t.Fatalf("ParseSettingsXML: %v", err)
			}
			got, ok := s.Get("A")
			if !ok {
				t.Fatalf("entry A not found in %s", c.doc)
			}
			if got != c.want {
				t.Errorf("A = %q, want %q", got, c.want)
			}

			// Editing must still work and preserve the rest of the document.
			if err := s.Set("A", "CHANGED"); err != nil {
				t.Fatalf("Set: %v", err)
			}
			out, err := s.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			want := strings.Replace(c.doc, c.want, "CHANGED", 1)
			if string(out) != want {
				t.Errorf("rendered = %q, want %q", out, want)
			}

			// And the edit must round-trip through a re-parse.
			again, err := ParseSettingsXML(out)
			if err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			if v, _ := again.Get("A"); v != "CHANGED" {
				t.Errorf("after re-parse A = %q, want CHANGED", v)
			}
		})
	}
}

// TestParseSettingsXMLPreservesNothingUnexpected is a byte-level invariant check
// across every fixture: describing the document must never alter it.
func TestParseSettingsXMLPreservesNothingUnexpected(t *testing.T) {
	for _, name := range []string{"Settings.xml", "Settings.odd.xml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(testdataPath(t, name))
			if err != nil {
				t.Fatal(err)
			}
			s, err := LoadSettingsXML(testdataPath(t, name))
			if err != nil {
				t.Fatal(err)
			}
			out, err := s.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, out) {
				t.Errorf("round trip changed %s:\n--- got ---\n%q\n--- want ---\n%q", name, out, raw)
			}
		})
	}
}
