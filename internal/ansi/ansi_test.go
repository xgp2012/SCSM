package ansi

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// esc is a short alias so the tables below stay readable.
const esc = "\x1b"

// lines is a convenience for the many "feed everything at once" cases.
func decodeAll(t *testing.T, opts DecoderOptions, input string) []Line {
	t.Helper()
	d := NewDecoder(opts)
	got := d.Write([]byte(input))
	return append(got, d.Flush()...)
}

// plainOf decodes input as a single stream and returns the Plain texts.
func plainOf(t *testing.T, opts DecoderOptions, input string) []string {
	t.Helper()
	var out []string
	for _, l := range decodeAll(t, opts, input) {
		out = append(out, l.Plain)
	}
	return out
}

// keepRaw is the option set the supervisor uses.
func keepRaw() DecoderOptions { return DecoderOptions{KeepRaw: true} }

// ---------------------------------------------------------------------------
// Strip / HasANSI
// ---------------------------------------------------------------------------

func TestStrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "hello world", "hello world"},
		{"cjk", "开启服务器成功，端口 28887", "开启服务器成功，端口 28887"},
		{"single CSI", esc + "[31mred", "red"},
		{"reset", esc + "[0m", ""},
		{"bold fg", esc + "[1;31mbold", "bold"},
		{"256 color", esc + "[38;5;196mred" + esc + "[0m", "red"},
		{"truecolor", esc + "[38;2;255;0;0mred" + esc + "[0m", "red"},
		{"bg truecolor", esc + "[48;2;255;0;0mX", "X"},
		{"question params", esc + "[?25lX", "X"},
		// A space is an intermediate byte (0x20-0x2f) and must follow the
		// params, so this is the well-formed ordering.
		{"space intermediates", esc + "[1 mX", "X"},
		{"multiple", esc + "[31mA" + esc + "[0mB" + esc + "[32mC", "ABC"},
		{"nested-ish", esc + "[1m" + esc + "[31m" + esc + "[4mU", "U"},
		{"OSC BEL", esc + "]0;title" + "\x07" + "visible", "visible"},
		{"OSC ST", esc + "]0;title" + esc + `\` + "visible", "visible"},
		{"OSC only", esc + "]0;t" + "\x07", ""},
		{"charset", esc + "(Bok", "ok"},
		{"save restore cursor", esc + "7x" + esc + "8", "x"},
		{"bare CR dropped", "a\rb", "ab"},
		{"invalid utf8", "a\xffb", "a" + replacement + "b"},
		{"two-byte ESC X", esc + "X", ""},
		{"CRLF", "a\r\nb", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Strip(tt.in); got != tt.want {
				t.Fatalf("Strip(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestStripAlwaysPlain is the invariant the supervisor relies on: after Strip,
// no ESC survives and the result is valid UTF-8.
func TestStripAlwaysPlain(t *testing.T) {
	inputs := []string{
		esc + "[38;2;255;0;0mred",
		"开启服务器成功" + esc + "[0m",
		esc + "]0;t" + "\x07" + esc + "[1mX",
		"\xff\xfe" + esc + "[31m",
		esc, esc + "[", esc + "]", esc + "]0;t",
		strings.Repeat(esc+"[31m", 50),
	}
	for _, in := range inputs {
		got := Strip(in)
		if HasANSI(got) {
			t.Errorf("Strip(%q) = %q still contains ANSI", in, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Strip(%q) = %q is not valid UTF-8", in, got)
		}
	}
}

func TestHasANSI(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"plain text", false},
		{"中文日志行", false},
		{esc + "[31mred", true},
		{"text " + esc + "[0m", true},
		{esc, true},
		// A sequence truncated by a chunk boundary still counts as ANSI.
		{esc + "[3", true},
		{esc + "]0;title", true},
		{"\x07", false},
	}
	for _, tt := range tests {
		if got := HasANSI(tt.in); got != tt.want {
			t.Errorf("HasANSI(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Decoder: basic framing
// ---------------------------------------------------------------------------

func TestWritePlainPassthrough(t *testing.T) {
	got := plainOf(t, keepRaw(), "hello\nworld\n")
	want := []string{"hello", "world"}
	if !equalStrings(got, want) {
		t.Fatalf("Plain lines = %q, want %q", got, want)
	}
}

// TestRawFidelity is requirement 3: Raw must equal the input line bytes minus
// the terminator, byte for byte, ANSI included.
func TestRawFidelity(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantRaw   []string
		wantPlain []string
	}{
		{
			name:      "single colored line",
			input:     esc + "[31mred" + esc + "[0m\n",
			wantRaw:   []string{esc + "[31mred" + esc + "[0m"},
			wantPlain: []string{"red"},
		},
		{
			name:      "crlf terminator excluded",
			input:     esc + "[32mgreen" + esc + "[0m\r\n",
			wantRaw:   []string{esc + "[32mgreen" + esc + "[0m"},
			wantPlain: []string{"green"},
		},
		{
			name:      "multiple lines",
			input:     "a\n" + esc + "[31mb\n" + "c\n",
			wantRaw:   []string{"a", esc + "[31mb", "c"},
			wantPlain: []string{"a", "b", "c"},
		},
		{
			name:      "no trailing newline before Flush",
			input:     "tail " + esc + "[36mcyan",
			wantRaw:   []string{"tail " + esc + "[36mcyan"},
			wantPlain: []string{"tail cyan"},
		},
		{
			name:      "bare CR keeps all bytes in Raw",
			input:     "1%\r50%\r100%\n",
			wantRaw:   []string{"1%\r50%\r100%"},
			wantPlain: []string{"100%"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeAll(t, keepRaw(), tt.input)
			var raws, plains []string
			for _, l := range got {
				raws = append(raws, l.Raw)
				plains = append(plains, l.Plain)
			}
			if !equalStrings(raws, tt.wantRaw) {
				t.Errorf("Raw = %q, want %q", raws, tt.wantRaw)
			}
			if !equalStrings(plains, tt.wantPlain) {
				t.Errorf("Plain = %q, want %q", plains, tt.wantPlain)
			}
		})
	}
}

// TestKeepRawFalse checks the opt-out.
func TestKeepRawFalse(t *testing.T) {
	got := decodeAll(t, DecoderOptions{}, esc+"[31mred"+esc+"[0m\n")
	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1", len(got))
	}
	if got[0].Raw != "" {
		t.Errorf("Raw = %q, want empty when KeepRaw is false", got[0].Raw)
	}
	if got[0].Plain != "red" {
		t.Errorf("Plain = %q, want %q", got[0].Plain, "red")
	}
}

// ---------------------------------------------------------------------------
// Decoder: line terminators and CR handling (requirement 4)
// ---------------------------------------------------------------------------

func TestLineTerminators(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantRaw   []string
		wantPlain []string
	}{
		{
			name:      "LF only",
			input:     "a\nb\n",
			wantRaw:   []string{"a", "b"},
			wantPlain: []string{"a", "b"},
		},
		{
			name:      "CRLF counts once",
			input:     "a\r\nb\r\n",
			wantRaw:   []string{"a", "b"},
			wantPlain: []string{"a", "b"},
		},
		{
			name:      "mixed CRLF and LF",
			input:     "a\r\nb\nc\r\n",
			wantRaw:   []string{"a", "b", "c"},
			wantPlain: []string{"a", "b", "c"},
		},
		{
			name:      "bare CR overwrite last wins",
			input:     "progress 10%\rprogress 90%\rdone\n",
			wantRaw:   []string{"progress 10%\rprogress 90%\rdone"},
			wantPlain: []string{"done"},
		},
		{
			name:      "bare CR does not split a line",
			input:     "aaa\rbbb\nccc\n",
			wantRaw:   []string{"aaa\rbbb", "ccc"},
			wantPlain: []string{"bbb", "ccc"},
		},
		{
			name:      "CR overwrite with ANSI",
			input:     esc + "[33m50%" + esc + "[0m\r" + esc + "[32m100%" + esc + "[0m\n",
			wantRaw:   []string{esc + "[33m50%" + esc + "[0m\r" + esc + "[32m100%" + esc + "[0m"},
			wantPlain: []string{"100%"},
		},
		{
			name:      "empty lines preserved",
			input:     "a\n\nb\n",
			wantRaw:   []string{"a", "", "b"},
			wantPlain: []string{"a", "", "b"},
		},
		{
			name:      "only a newline",
			input:     "\n",
			wantRaw:   []string{""},
			wantPlain: []string{""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeAll(t, keepRaw(), tt.input)
			var raws, plains []string
			for _, l := range got {
				raws = append(raws, l.Raw)
				plains = append(plains, l.Plain)
			}
			if !equalStrings(raws, tt.wantRaw) {
				t.Errorf("Raw = %q, want %q", raws, tt.wantRaw)
			}
			if !equalStrings(plains, tt.wantPlain) {
				t.Errorf("Plain = %q, want %q", plains, tt.wantPlain)
			}
		})
	}
}

// TestFlushPartial covers requirement: Flush emits a partial trailing line.
func TestFlushPartial(t *testing.T) {
	d := NewDecoder(keepRaw())
	if got := d.Write([]byte("no newline here")); len(got) != 0 {
		t.Fatalf("Write returned %d lines before Flush, want 0", len(got))
	}
	got := d.Flush()
	if len(got) != 1 {
		t.Fatalf("Flush returned %d lines, want 1", len(got))
	}
	if !got[0].Partial {
		t.Error("Partial = false, want true for a Flush-emitted line")
	}
	if got[0].Raw != "no newline here" {
		t.Errorf("Raw = %q", got[0].Raw)
	}
	// A second Flush must not invent another line.
	if again := d.Flush(); len(again) != 0 {
		t.Errorf("second Flush returned %d lines, want 0", len(again))
	}
}

// TestFlushAfterCompleteLine: Flush must not synthesise an empty trailing line.
func TestFlushAfterCompleteLine(t *testing.T) {
	d := NewDecoder(keepRaw())
	got := d.Write([]byte("done\n"))
	if len(got) != 1 || got[0].Partial {
		t.Fatalf("Write = %+v, want one non-partial line", got)
	}
	if extra := d.Flush(); len(extra) != 0 {
		t.Errorf("Flush = %+v, want nothing", extra)
	}
}

// ---------------------------------------------------------------------------
// Decoder: chunk safety (requirement 1) — the core of this package
// ---------------------------------------------------------------------------

// TestSplitSequences feeds one stream in two pieces at many split points. Every
// split point must produce the same result as feeding it whole.
func TestSplitSequences(t *testing.T) {
	// A realistic colored line. Splitting at EVERY byte offset is the strongest
	// form of this test: it covers splitting inside CSI params, between ESC and
	// '[', inside an OSC payload, and between the two bytes of a CRLF.
	input := "before " + esc + "[38;5;196mred" + esc + "[0m mid " +
		esc + "]0;window title" + "\x07" + "after" + esc + "[0m\r\n"

	want := decodeAll(t, keepRaw(), input)
	if len(want) != 1 {
		t.Fatalf("whole-stream decode gave %d lines, want 1", len(want))
	}

	for split := 0; split <= len(input); split++ {
		split := split
		t.Run(fmt.Sprintf("split_at_%d", split), func(t *testing.T) {
			d := NewDecoder(keepRaw())
			got := d.Write([]byte(input[:split]))
			got = append(got, d.Write([]byte(input[split:]))...)
			got = append(got, d.Flush()...)

			if len(got) != len(want) {
				t.Fatalf("split %d: got %d lines (%+v), want %d", split, len(got), got, len(want))
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("split %d: line %d = %+v, want %+v", split, i, got[i], want[i])
				}
			}
		})
	}
}

// TestSplitSequencesNamed keeps the explicit, human-readable cases the brief
// calls out, so a failure names the actual scenario.
func TestSplitSequencesNamed(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		wantP []string
		wantR []string
	}{
		{
			// The exact case required by the brief.
			name:  "inside CSI params",
			parts: []string{esc + "[3", "1mred" + esc + "[0m\n"},
			wantP: []string{"red"},
			wantR: []string{esc + "[31mred" + esc + "[0m"},
		},
		{
			name:  "between ESC and bracket",
			parts: []string{"a" + esc, "[31mb\n"},
			wantP: []string{"ab"},
			wantR: []string{"a" + esc + "[31mb"},
		},
		{
			name:  "inside OSC payload",
			parts: []string{"x" + esc + "]0;ti", "tle" + "\x07" + "y\n"},
			wantP: []string{"xy"},
			wantR: []string{"x" + esc + "]0;title" + "\x07" + "y"},
		},
		{
			name:  "inside OSC ST terminator",
			parts: []string{"x" + esc + "]0;t" + esc, `\` + "y\n"},
			wantP: []string{"xy"},
			wantR: []string{"x" + esc + "]0;t" + esc + `\` + "y"},
		},
		{
			name:  "inside truecolor params",
			parts: []string{esc + "[38;2;255;", "0;0mZ\n"},
			wantP: []string{"Z"},
			wantR: []string{esc + "[38;2;255;0;0mZ"},
		},
		{
			name:  "CRLF split across writes",
			parts: []string{"a\r", "\nb\n"},
			wantP: []string{"a", "b"},
			wantR: []string{"a", "b"},
		},
		{
			name:  "charset sequence split",
			parts: []string{esc + "(", "Bok\n"},
			wantP: []string{"ok"},
			wantR: []string{esc + "(Bok"},
		},
		{
			name:  "three-way split inside one sequence",
			parts: []string{esc, "[3", "1mX\n"},
			wantP: []string{"X"},
			wantR: []string{esc + "[31mX"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDecoder(keepRaw())
			var got []Line
			for _, p := range tt.parts {
				got = append(got, d.Write([]byte(p))...)
			}
			got = append(got, d.Flush()...)

			var plains, raws []string
			for _, l := range got {
				plains = append(plains, l.Plain)
				raws = append(raws, l.Raw)
			}
			if !equalStrings(plains, tt.wantP) {
				t.Errorf("Plain = %q, want %q", plains, tt.wantP)
			}
			if !equalStrings(raws, tt.wantR) {
				t.Errorf("Raw = %q, want %q", raws, tt.wantR)
			}
		})
	}
}

// TestByteAtATime feeds a stream one byte per Write: the most hostile framing.
func TestByteAtATime(t *testing.T) {
	input := esc + "[1;38;5;208m警告" + esc + "[0m " +
		esc + "]0;title" + "\x07" + "ok\r\n" + "next\n"
	want := decodeAll(t, keepRaw(), input)

	d := NewDecoder(keepRaw())
	var got []Line
	for i := 0; i < len(input); i++ {
		got = append(got, d.Write([]byte{input[i]})...)
	}
	got = append(got, d.Flush()...)

	if len(got) != len(want) {
		t.Fatalf("got %d lines %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestWriteRandomChunks splits the stream at pseudo-random widths.
func TestWriteRandomChunks(t *testing.T) {
	input := esc + "[36m[StartServer]" + esc + "[0m开启服务器成功，端口 " +
		esc + "[1;33m28887" + esc + "[0m\r\n" +
		esc + "]0;Survivalcraft" + "\x07" + "Entered screen \"Game\"\n" +
		"progress\rprogress 2\rprogress 3\n"
	want := decodeAll(t, keepRaw(), input)

	widths := []int{1, 2, 3, 5, 7, 13, 64, 100}
	for _, w := range widths {
		w := w
		t.Run(fmt.Sprintf("width_%d", w), func(t *testing.T) {
			d := NewDecoder(keepRaw())
			var got []Line
			for i := 0; i < len(input); i += w {
				end := i + w
				if end > len(input) {
					end = len(input)
				}
				got = append(got, d.Write([]byte(input[i:end]))...)
			}
			got = append(got, d.Flush()...)

			if len(got) != len(want) {
				t.Fatalf("got %d lines, want %d\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Decoder: input must never be mutated (requirement 2)
// ---------------------------------------------------------------------------

func TestWriteDoesNotMutateInput(t *testing.T) {
	inputs := []string{
		esc + "[31mred" + esc + "[0m\n",
		"a\r\nb\rprogress\n",
		esc + "]0;title" + "\x07" + "x\n",
		"invalid \xff\xfe bytes\n",
	}
	for _, in := range inputs {
		buf := []byte(in)
		before := append([]byte(nil), buf...)

		d := NewDecoder(keepRaw())
		d.Write(buf)
		d.Flush()

		if !bytes.Equal(buf, before) {
			t.Errorf("Write mutated its input:\n got %q\nwant %q", buf, before)
		}
	}
}

// TestWriteRetainsNoAlias checks the decoder copies rather than retaining the
// caller's slice: mutating the caller's buffer afterwards must not change the
// emitted Raw.
func TestWriteRetainsNoAlias(t *testing.T) {
	d := NewDecoder(keepRaw())
	buf := []byte("hello\npartial")
	got := d.Write(buf)
	if len(got) != 1 || got[0].Raw != "hello" {
		t.Fatalf("unexpected first Write result: %+v", got)
	}

	// Scribble over the caller's buffer.
	for i := range buf {
		buf[i] = 'X'
	}

	tail := d.Flush()
	if len(tail) != 1 {
		t.Fatalf("Flush = %+v, want 1 line", tail)
	}
	if tail[0].Raw != "partial" {
		t.Errorf("Raw = %q, want %q (decoder aliased the caller's buffer)", tail[0].Raw, "partial")
	}
}

// ---------------------------------------------------------------------------
// Decoder: escape sequence stripping (requirement 5)
// ---------------------------------------------------------------------------

func TestStripForms(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"CSI reset", esc + "[0mX", "X"},
		{"CSI bold", esc + "[1mX", "X"},
		{"CSI 256 color", esc + "[38;5;196mX", "X"},
		{"CSI truecolor", esc + "[38;2;255;0;0mX", "X"},
		{"CSI bg truecolor", esc + "[48;2;0;255;0mX", "X"},
		{"CSI private mode", esc + "[?25lX", "X"},
		{"CSI private mode set", esc + "[?1049hX", "X"},
		{"CSI with intermediates", esc + "[1$pX", "X"},
		{"CSI cursor move", esc + "[2;3HX", "X"},
		{"CSI erase line", esc + "[KX", "X"},
		{"OSC BEL", esc + "]0;title" + "\x07" + "X", "X"},
		{"OSC ST", esc + "]0;title" + esc + `\X`, "X"},
		{"OSC 8 hyperlink BEL", esc + "]8;;http://e.com" + "\x07" + "link" + esc + "]8;;" + "\x07", "link"},
		{"charset G0", esc + "(BX", "X"},
		{"charset G1", esc + ")0X", "X"},
		{"DEC alignment", esc + "#8X", "X"},
		{"save cursor", esc + "7X", "X"},
		{"restore cursor", esc + "8X", "X"},
		{"keypad mode", esc + "=X", "X"},
		{"reset terminal", esc + "cX", "X"},
		{"combined", esc + "(B" + esc + "[1m" + esc + "]0;t" + "\x07" + "X", "X"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plainOf(t, keepRaw(), tt.in+"\n")
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("Plain = %q, want [%q]", got, tt.want)
			}
		})
	}
}

// TestPlainNeverHasANSI is the invariant the supervisor asserts on .plain.log.
func TestPlainNeverHasANSI(t *testing.T) {
	input := esc + "[38;2;1;2;3mred" + esc + "[0m " +
		esc + "]0;t" + "\x07" + " + " + esc + "[1;38;5;9mY" + esc + "[0m\r\n" +
		esc + "[?25l" + "hidden" + esc + "[?25h\n" +
		"\xff\xfe" + esc + "[31mbad\n"
	for _, l := range decodeAll(t, keepRaw(), input) {
		if HasANSI(l.Plain) {
			t.Errorf("Plain %q contains ANSI", l.Plain)
		}
		if !utf8.ValidString(l.Plain) {
			t.Errorf("Plain %q is not valid UTF-8", l.Plain)
		}
	}
}

// ---------------------------------------------------------------------------
// Decoder: invalid UTF-8 (requirement 7)
// ---------------------------------------------------------------------------

func TestInvalidUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lone continuation", "a\x80b", "a" + replacement + "b"},
		{"truncated 2-byte", "a\xc3", "a" + replacement},
		{"truncated 3-byte", "a\xe4\xb8", "a" + replacement},
		{"overlong", "a\xc0\xafb", "a" + replacement + "b"},
		{"0xff", "a\xffb", "a" + replacement + "b"},
		{"valid cjk survives", "开启服务器", "开启服务器"},
		{"valid emoji survives", "🎮", "🎮"},
		{"mixed", "\xff开\xfe启", replacement + "开" + replacement + "启"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plainOf(t, keepRaw(), tt.in+"\n")
			if len(got) != 1 {
				t.Fatalf("got %d lines, want 1", len(got))
			}
			if got[0] != tt.want {
				t.Errorf("Plain = %q, want %q", got[0], tt.want)
			}
			if !utf8.ValidString(got[0]) {
				t.Errorf("Plain %q is not valid UTF-8", got[0])
			}
		})
	}
}

// TestInvalidUTF8RawIsByteFaithful: Raw must keep the original bytes untouched.
func TestInvalidUTF8RawIsByteFaithful(t *testing.T) {
	raw := "a\xff\xfeb"
	got := decodeAll(t, keepRaw(), raw+"\n")
	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1", len(got))
	}
	if got[0].Raw != raw {
		t.Errorf("Raw = %q, want %q", got[0].Raw, raw)
	}
}

// TestSplitUTF8AcrossWrites: a multi-byte rune split between chunks must still
// decode to valid UTF-8.
func TestSplitUTF8AcrossWrites(t *testing.T) {
	r := "开"
	if len(r) != 3 {
		t.Fatalf("test assumption broken: %q is %d bytes", r, len(r))
	}
	for split := 1; split < len(r); split++ {
		d := NewDecoder(keepRaw())
		var got []Line
		got = append(got, d.Write([]byte("a"+r[:split]))...)
		got = append(got, d.Write([]byte(r[split:]+"b\n"))...)
		got = append(got, d.Flush()...)

		if len(got) != 1 {
			t.Fatalf("split %d: got %d lines, want 1", split, len(got))
		}
		if got[0].Plain != "a开b" {
			t.Errorf("split %d: Plain = %q, want %q", split, got[0].Plain, "a开b")
		}
		if got[0].Raw != "a开b" {
			t.Errorf("split %d: Raw = %q, want %q", split, got[0].Raw, "a开b")
		}
	}
}

// ---------------------------------------------------------------------------
// Decoder: truncation (requirement 8)
// ---------------------------------------------------------------------------

func TestMaxLineBytes(t *testing.T) {
	const max = 8
	d := NewDecoder(DecoderOptions{KeepRaw: true, MaxLineBytes: max})

	input := strings.Repeat("a", 20) + "\n"
	got := d.Write([]byte(input))
	got = append(got, d.Flush()...)

	// The line is cut into max-sized pieces; the total content is preserved and
	// every piece except possibly the last is marked Truncated.
	var joined strings.Builder
	for i, l := range got {
		if len(l.Raw) > max {
			t.Errorf("line %d Raw is %d bytes, exceeds MaxLineBytes %d", i, len(l.Raw), max)
		}
		if i < len(got)-1 && !l.Truncated {
			t.Errorf("line %d: Truncated = false, want true for a mid-line cut", i)
		}
		joined.WriteString(l.Raw)
	}
	if joined.String() != strings.Repeat("a", 20) {
		t.Errorf("truncated content = %q, want 20 a's", joined.String())
	}
}

// TestMaxLineBytesMarksAtLeastOnce covers the common "keep only the first N
// bytes" usage: the first Line is flagged Truncated so callers can tell.
func TestMaxLineBytesMarksAtLeastOnce(t *testing.T) {
	const max = 16
	long := strings.Repeat("x", max*3)
	got := decodeAll(t, DecoderOptions{KeepRaw: true, MaxLineBytes: max}, long)

	if len(got) == 0 {
		t.Fatal("no lines emitted")
	}
	if !got[0].Truncated {
		t.Error("first line Truncated = false, want true")
	}
	for i, l := range got {
		if len(l.Raw) > max {
			t.Errorf("line %d: %d bytes exceeds cap %d", i, len(l.Raw), max)
		}
	}
}

// TestMaxLineBytesBoundedMemory: a pathological stream with no newline must not
// grow without limit.
func TestMaxLineBytesBoundedMemory(t *testing.T) {
	const max = 128
	d := NewDecoder(DecoderOptions{KeepRaw: true, MaxLineBytes: max})

	total := 0
	collect := func(ls []Line) {
		for _, l := range ls {
			if len(l.Raw) > max {
				t.Fatalf("emitted %d bytes, exceeds cap %d", len(l.Raw), max)
			}
			total += len(l.Raw)
		}
	}
	for i := 0; i < 100; i++ {
		collect(d.Write([]byte(strings.Repeat("z", 100))))
	}
	// The tail below the cap is legitimately still buffered; Flush releases it.
	collect(d.Flush())
	if total != 100*100 {
		t.Errorf("emitted %d bytes total, want %d", total, 100*100)
	}
}

// TestDefaultMaxLineBytes documents the default cap.
func TestDefaultMaxLineBytes(t *testing.T) {
	d := NewDecoder(keepRaw())
	if d.opts.MaxLineBytes != DefaultMaxLineBytes {
		t.Errorf("default MaxLineBytes = %d, want %d", d.opts.MaxLineBytes, DefaultMaxLineBytes)
	}
}

// ---------------------------------------------------------------------------
// Decoder: incomplete sequences at end of stream (requirement 6)
// ---------------------------------------------------------------------------

// TestUnterminatedAtFlush: bytes of an unterminated sequence must be emitted
// literally rather than dropped.
func TestUnterminatedAtFlush(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"bare ESC", "hi" + esc},
		{"CSI start", "hi" + esc + "["},
		{"CSI params only", "hi" + esc + "[31"},
		{"CSI with intermediates", "hi" + esc + "[1$"},
		{"OSC start", "hi" + esc + "]"},
		{"OSC payload", "hi" + esc + "]0;title"},
		{"OSC ST partial", "hi" + esc + "]0;title" + esc},
		{"charset partial", "hi" + esc + "("},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDecoder(keepRaw())
			got := d.Write([]byte(tt.in))
			got = append(got, d.Flush()...)

			if len(got) != 1 {
				t.Fatalf("got %d lines %+v, want 1", len(got), got)
			}
			// Raw fidelity: every input byte must survive.
			if got[0].Raw != tt.in {
				t.Errorf("Raw = %q, want %q (bytes were lost)", got[0].Raw, tt.in)
			}
			if !strings.HasPrefix(got[0].Plain, "hi") {
				t.Errorf("Plain = %q, want it to start with %q", got[0].Plain, "hi")
			}
		})
	}
}

// TestUnterminatedDoesNotSwallowStream is the heart of requirement 6: an
// unterminated sequence must not hold subsequent real text hostage forever.
func TestUnterminatedDoesNotSwallowStream(t *testing.T) {
	// A CSI that never terminates: params long past the pending bound, with no
	// final byte. Real lines follow.
	runaway := esc + "[" + strings.Repeat("1", 400)

	t.Run("text is recovered, not swallowed", func(t *testing.T) {
		d := NewDecoder(keepRaw())
		d.Write([]byte(runaway))
		// The follow-up traffic carries the text. It must come back out of
		// Write (promptly) or, failing that, out of Flush -- but it must never
		// be lost, and it must not stay trapped in the carry buffer.
		got := d.Write([]byte("\nreal text\n"))
		got = append(got, d.Flush()...)

		var plains []string
		for _, l := range got {
			plains = append(plains, l.Plain)
		}
		joined := strings.Join(plains, "\n")
		if !strings.Contains(joined, "real text") {
			t.Fatalf("the text after an unterminated sequence was lost: %q", joined)
		}
		if len(d.scan) > maxPendingSequence {
			t.Errorf("carried %d bytes after recovery, want <= %d", len(d.scan), maxPendingSequence)
		}
	})

	t.Run("bounded carry", func(t *testing.T) {
		d := NewDecoder(keepRaw())
		d.Write([]byte(runaway))

		// Whatever happens, the decoder must not be holding an unbounded buffer.
		if len(d.scan) > maxPendingSequence {
			t.Errorf("carried %d bytes, want <= %d", len(d.scan), maxPendingSequence)
		}
	})

	t.Run("recovers within a bounded number of reads", func(t *testing.T) {
		// Feed a truncated sequence then ordinary traffic; the traffic must
		// reappear promptly (not only at Flush of the whole session).
		d := NewDecoder(keepRaw())
		d.Write([]byte(esc + "[38;2;1;2;3")) // never terminated

		found := false
		for i := 0; i < 10 && !found; i++ {
			for _, l := range d.Write([]byte("padding padding padding\n")) {
				if strings.Contains(l.Plain, "padding") {
					found = true
				}
			}
		}
		if !found {
			t.Error("text following a truncated sequence never resurfaced")
		}
	})
}

// ---------------------------------------------------------------------------
// Decoder: tabs
// ---------------------------------------------------------------------------

func TestTabExpansion(t *testing.T) {
	tests := []struct {
		width int
		in    string
		want  string
	}{
		{0, "a\tb", "a\tb"},
		{4, "a\tb", "a   b"},
		{8, "a\tb", "a       b"},
		{4, "\tx", "    x"},
		{4, "abcd\te", "abcd    e"},
		{4, "ab\tcd\te", "ab  cd  e"},
		// Tabs are measured after ANSI removal.
		{4, esc + "[31mab" + esc + "[0m\tc", "ab  c"},
	}
	for _, tt := range tests {
		name := fmt.Sprintf("width_%d_%q", tt.width, tt.in)
		t.Run(name, func(t *testing.T) {
			got := plainOf(t, DecoderOptions{KeepRaw: true, TabWidth: tt.width}, tt.in+"\n")
			if len(got) != 1 {
				t.Fatalf("got %d lines, want 1", len(got))
			}
			if got[0] != tt.want {
				t.Errorf("Plain = %q, want %q", got[0], tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Realistic end-to-end sample (§5.2.1 anchors, with color)
// ---------------------------------------------------------------------------

// TestRealisticGameServerLog decodes a sample resembling the game server's
// Chinese log lines under the PTY (enhanced, colored) terminal mode.
func TestRealisticGameServerLog(t *testing.T) {
	// Assembled from the anchors documented in §5.2.1 and appendix A.5.
	const sample = esc + "[36m[自动检测]" + esc + "[0m 输出被重定向，使用基础终端模式\r\n" +
		esc + "[32mEnhancedTerminalLogSink initialized With ANSI Support:" + esc + "[0m " +
		esc + "[1;33m增强终端安全退出" + esc + "[0m\r\n" +
		esc + "[90m已自动生成存档目录 app:/Worlds/PlanTest" + esc + "[0m\r\n" +
		esc + "[36m[StartServer]" + esc + "[0m开启服务器成功，端口 " + esc + "[1;35m28887" + esc + "[0m\r\n" +
		"Loaded world, WorldName=" + esc + "[33mPlanTest" + esc + "[0m\r\n" +
		"Entered screen " + esc + "[1m\"Game\"" + esc + "[0m\r\n" +
		esc + "[31mERROR:" + esc + "[0m 插件加载失败\r\n" +
		"正在加载 " + esc + "[36m10" + esc + "[0m%\r正在加载 " + esc + "[36m100" + esc + "[0m%\r" +
		esc + "[32m加载完成" + esc + "[0m\r\n"

	wantPlain := []string{
		"[自动检测] 输出被重定向，使用基础终端模式",
		"EnhancedTerminalLogSink initialized With ANSI Support: 增强终端安全退出",
		"已自动生成存档目录 app:/Worlds/PlanTest",
		"[StartServer]开启服务器成功，端口 28887",
		"Loaded world, WorldName=PlanTest",
		`Entered screen "Game"`,
		"ERROR: 插件加载失败",
		"加载完成", // the bare-CR progress overwrite collapses to the last write
	}

	got := decodeAll(t, keepRaw(), sample)
	var plains []string
	for _, l := range got {
		plains = append(plains, l.Plain)
	}
	if !equalStrings(plains, wantPlain) {
		t.Fatalf("Plain lines mismatch:\n got %q\nwant %q", plains, wantPlain)
	}

	// Every raw line except the progress one must still carry color bytes, which
	// is the whole point of the package.
	colored := 0
	for _, l := range got {
		if HasANSI(l.Raw) {
			colored++
		}
		if HasANSI(l.Plain) {
			t.Errorf("Plain line %q contains ANSI", l.Plain)
		}
	}
	if colored < len(got)-1 {
		t.Errorf("only %d of %d raw lines kept ANSI; color was lost", colored, len(got))
	}
}

// TestGameServerAnchorsMatchPlain checks that the §5.2.1 rule anchors are
// findable in Plain even when the source line is heavily colored.
func TestGameServerAnchorsMatchPlain(t *testing.T) {
	anchors := []struct {
		name  string
		raw   string
		plain string
	}{
		{
			name:  "server listening",
			raw:   esc + "[36m[StartServer]" + esc + "[0m开启服务器成功，端口 " + esc + "[1m28887" + esc + "[0m",
			plain: "[StartServer]开启服务器成功，端口 28887",
		},
		{
			name:  "world loaded",
			raw:   "Loaded world," + esc + "[33mWorldName=PlanTest" + esc + "[0m",
			plain: "Loaded world,WorldName=PlanTest",
		},
		{
			name:  "screen game",
			raw:   "Entered screen " + esc + "[1m\"Game\"" + esc + "[0m",
			plain: `Entered screen "Game"`,
		},
		{
			name:  "error",
			raw:   esc + "[31mERROR:" + esc + "[0m boom",
			plain: "ERROR: boom",
		},
	}
	for _, a := range anchors {
		t.Run(a.name, func(t *testing.T) {
			got := plainOf(t, keepRaw(), a.raw+"\n")
			if len(got) != 1 || got[0] != a.plain {
				t.Fatalf("Plain = %q, want %q", got, a.plain)
			}
			// The anchor patterns in §5.2.1 must match the Plain text.
			if !strings.Contains(got[0], "ERROR:") && !strings.Contains(got[0], "port") &&
				!strings.Contains(got[0], "WorldName=") && !strings.Contains(got[0], `"Game"`) &&
				!strings.Contains(got[0], "开启服务器成功") {
				t.Errorf("no anchor found in %q", got[0])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrency documentation / purity
// ---------------------------------------------------------------------------

// TestStripConcurrent documents that Strip/HasANSI are pure and safe for
// concurrent use. Run with -race to make this meaningful.
func TestStripConcurrent(t *testing.T) {
	const workers = 8
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				if got := Strip(esc + "[31mX" + esc + "[0m"); got != "X" {
					t.Errorf("Strip = %q, want X", got)
					return
				}
				if !HasANSI(esc + "[31m") {
					t.Error("HasANSI = false, want true")
					return
				}
			}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}
}

// TestSeparateDecodersIndependent: one Decoder per stream is the contract.
func TestSeparateDecodersIndependent(t *testing.T) {
	a := NewDecoder(keepRaw())
	b := NewDecoder(keepRaw())

	if got := a.Write([]byte("from-a\n")); len(got) != 1 || got[0].Plain != "from-a" {
		t.Errorf("a: %+v", got)
	}
	if got := b.Write([]byte("from-b\n")); len(got) != 1 || got[0].Plain != "from-b" {
		t.Errorf("b: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestWriteEmptyAndNil: empty writes are no-ops and must not panic.
func TestWriteEmptyAndNil(t *testing.T) {
	d := NewDecoder(keepRaw())
	if got := d.Write(nil); got != nil {
		t.Errorf("Write(nil) = %+v, want nil", got)
	}
	if got := d.Write([]byte{}); got != nil {
		t.Errorf("Write(empty) = %+v, want nil", got)
	}
	if got := d.Flush(); got != nil {
		t.Errorf("Flush on empty = %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// Carriage-return edge cases (rewind only erases what is written over it)
// ---------------------------------------------------------------------------

func TestCarriageReturnEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantPlain []string
		wantRaw   []string
	}{
		{
			name:      "trailing CR with no newline erases nothing",
			input:     "abc\r",
			wantPlain: []string{"abc"},
			wantRaw:   []string{"abc"},
		},
		{
			name:      "CR overwrite then trailing CR",
			input:     "10%\r50%\r",
			wantPlain: []string{"50%"},
			wantRaw:   []string{"10%\r50%"},
		},
		{
			name:      "CRLF after a progress overwrite",
			input:     "1%\r99%\r\n",
			wantPlain: []string{"99%"},
			wantRaw:   []string{"1%\r99%"},
		},
		{
			name:      "empty overwrite",
			input:     "\r\rX\n",
			wantPlain: []string{"X"},
			wantRaw:   []string{"\r\rX"},
		},
		{
			name:      "CR at start of line",
			input:     "\rabc\n",
			wantPlain: []string{"abc"},
			wantRaw:   []string{"\rabc"},
		},
		{
			name:      "several overwrites keep only the last",
			input:     "a\rb\rc\rd\n",
			wantPlain: []string{"d"},
			wantRaw:   []string{"a\rb\rc\rd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeAll(t, keepRaw(), tt.input)
			var plains, raws []string
			for _, l := range got {
				plains = append(plains, l.Plain)
				raws = append(raws, l.Raw)
			}
			if !equalStrings(plains, tt.wantPlain) {
				t.Errorf("Plain = %q, want %q", plains, tt.wantPlain)
			}
			if !equalStrings(raws, tt.wantRaw) {
				t.Errorf("Raw = %q, want %q", raws, tt.wantRaw)
			}
		})
	}
}

// TestTrailingCRAtEOFOnly: a bare CR as the very last byte of the stream.
func TestTrailingCRAtEOFOnly(t *testing.T) {
	d := NewDecoder(keepRaw())
	if got := d.Write([]byte("\r")); len(got) != 0 {
		t.Fatalf("Write(CR) = %+v, want nothing before Flush", got)
	}
	got := d.Flush()
	// A lone CR is whitespace with no visible text: no line should be invented.
	if len(got) != 0 {
		t.Errorf("Flush = %+v, want no line for a lone CR", got)
	}
}

// TestBareCRSplitAcrossWrites: the CR and its following text arrive separately.
func TestBareCRSplitAcrossWrites(t *testing.T) {
	d := NewDecoder(keepRaw())
	d.Write([]byte("progress 10%\r"))
	got := d.Write([]byte("progress 90%\n"))
	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1: %+v", len(got), got)
	}
	if got[0].Plain != "progress 90%" {
		t.Errorf("Plain = %q, want %q", got[0].Plain, "progress 90%")
	}
	if got[0].Raw != "progress 10%\rprogress 90%" {
		t.Errorf("Raw = %q", got[0].Raw)
	}
}

// ---------------------------------------------------------------------------
// Option normalisation
// ---------------------------------------------------------------------------

func TestOptionNormalisation(t *testing.T) {
	t.Run("zero MaxLineBytes becomes the default", func(t *testing.T) {
		d := NewDecoder(DecoderOptions{KeepRaw: true})
		if d.opts.MaxLineBytes != DefaultMaxLineBytes {
			t.Errorf("MaxLineBytes = %d, want %d", d.opts.MaxLineBytes, DefaultMaxLineBytes)
		}
	})
	t.Run("negative MaxLineBytes becomes the default", func(t *testing.T) {
		d := NewDecoder(DecoderOptions{KeepRaw: true, MaxLineBytes: -5})
		if d.opts.MaxLineBytes != DefaultMaxLineBytes {
			t.Errorf("MaxLineBytes = %d, want %d", d.opts.MaxLineBytes, DefaultMaxLineBytes)
		}
	})
	t.Run("negative TabWidth is clamped to zero", func(t *testing.T) {
		d := NewDecoder(DecoderOptions{KeepRaw: true, TabWidth: -5})
		if d.opts.TabWidth != 0 {
			t.Errorf("TabWidth = %d, want 0", d.opts.TabWidth)
		}
		if got := plainOf(t, DecoderOptions{KeepRaw: true, TabWidth: -5}, "a\tb\n"); got[0] != "a\tb" {
			t.Errorf("Plain = %q, want the tab untouched", got[0])
		}
	})
	t.Run("MaxLineBytes of one still makes progress", func(t *testing.T) {
		d := NewDecoder(DecoderOptions{KeepRaw: true, MaxLineBytes: 1})
		var all []Line
		all = append(all, d.Write([]byte("abc\n"))...)
		all = append(all, d.Flush()...)

		var joined strings.Builder
		for _, l := range all {
			if len(l.Raw) > 1 {
				t.Errorf("Raw %q exceeds cap 1", l.Raw)
			}
			joined.WriteString(l.Raw)
		}
		if joined.String() != "abc" {
			t.Errorf("joined = %q, want %q", joined.String(), "abc")
		}
	})
}

// TestCapClampsEscapeSequences: a long escape sequence must not overshoot the
// cap either.
func TestCapClampsEscapeSequences(t *testing.T) {
	const max = 8
	d := NewDecoder(DecoderOptions{KeepRaw: true, MaxLineBytes: max})

	long := esc + "[38;2;255;0;0m" + strings.Repeat("x", 40) + "\n"
	var all []Line
	all = append(all, d.Write([]byte(long))...)
	all = append(all, d.Flush()...)

	for i, l := range all {
		if len(l.Raw) > max {
			t.Errorf("line %d: Raw is %d bytes, exceeds cap %d: %q", i, len(l.Raw), max, l.Raw)
		}
	}
}

// ---------------------------------------------------------------------------
// Malformed and exotic sequences
// ---------------------------------------------------------------------------

func TestMalformedSequences(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"CSI then bare ESC", esc + "[1" + esc},
		{"CSI with NUL", esc + "[31m\x00x"},
		{"OSC containing ESC not ST", esc + "]0;t" + esc + "x" + "\x07" + "y"},
		{"truncated CSI at end", "x" + esc + "[9999999"},
		{"ESC followed by invalid", esc + "\x01x"},
		{"repeated ESC", esc + esc + esc + "[31mx"},
		{"CSI final byte only", esc + "[m"},
		{"CSI empty params", esc + "[]m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Must not panic, must produce valid UTF-8, and must not lose Raw bytes.
			got := decodeAll(t, keepRaw(), tt.in+"\n")
			var raws strings.Builder
			for _, l := range got {
				if !utf8.ValidString(l.Plain) {
					t.Errorf("Plain %q is not valid UTF-8", l.Plain)
				}
				if HasANSI(l.Plain) {
					t.Errorf("Plain %q contains ANSI", l.Plain)
				}
				raws.WriteString(l.Raw)
			}
			if !strings.HasPrefix(raws.String(), tt.in) {
				t.Errorf("Raw %q lost bytes, want it to start with %q", raws.String(), tt.in)
			}
		})
	}
}

// TestLongOSCIsBounded: an OSC that never terminates must not grow the carry.
func TestLongOSCIsBounded(t *testing.T) {
	d := NewDecoder(keepRaw())
	d.Write([]byte(esc + "]0;" + strings.Repeat("t", 500)))
	if len(d.scan) > maxPendingSequence {
		t.Errorf("carried %d bytes, want <= %d", len(d.scan), maxPendingSequence)
	}
	// Real text afterwards must still surface.
	got := d.Write([]byte("real\n"))
	got = append(got, d.Flush()...)
	var plains []string
	for _, l := range got {
		plains = append(plains, l.Plain)
	}
	if !strings.Contains(strings.Join(plains, "\n"), "real") {
		t.Errorf("text after a long OSC was lost: %q", plains)
	}
}
