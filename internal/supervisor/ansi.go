package supervisor

import "scnetm/internal/ansi"

// ansiLine is an alias for ansi.Line. Keeping the alias means the whole package
// can be written in terms of one name, and tests can build lines without
// importing the sibling package.
//
// The production decoder always comes from ansi.NewDecoder, so the two types
// are identical by construction: no conversion cost, no possibility of drift.
type ansiLine = ansi.Line

// lineDecoder is the narrow seam between the supervisor and internal/ansi.
//
// internal/ansi is authored concurrently (see ANSI_CONTRACT.md). Depending on
// this interface — rather than on *ansi.Decoder directly — lets tests inject a
// deterministic fake while production keeps using ansi.NewDecoder, which is
// the only implementation referenced outside _test.go files.
type lineDecoder interface {
	Write(p []byte) []ansi.Line
	Flush() []ansi.Line
}

// newLineDecoder builds the production decoder.
func newLineDecoder(maxLineBytes, tabWidth int) lineDecoder {
	return ansi.NewDecoder(ansi.DecoderOptions{
		MaxLineBytes: maxLineBytes,
		KeepRaw:      true,
		TabWidth:     tabWidth,
	})
}
