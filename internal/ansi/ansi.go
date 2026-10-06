// Package ansi implements color-preserving terminal byte-stream handling for
// the scnetm panel (§5.2 / §5.2.1 of the implementation plan).
//
// The game server is launched under a PTY so that it emits ANSI-colored output
// ("enhanced terminal mode"). The panel must keep those raw bytes for replay and
// display while also producing a clean plain-text copy for searching and rule
// matching. This package is the single place that reconciles the two.
//
// # Design guarantees
//
//   - Chunk safety. A PTY read returns arbitrary byte counts, so an escape
//     sequence can be split across two reads. The Decoder reassembles sequences
//     across Write calls; it never assumes a sequence is intact within one
//     buffer. TestSplitSequences covers this explicitly.
//   - Raw fidelity. The raw stream is never mutated. Line.Raw holds exactly the
//     bytes that arrived, minus the line terminator, when KeepRaw is set. This is
//     what guarantees "don't lose color for the sake of searching".
//   - Plain safety. Line.Plain is always valid UTF-8 and never contains an escape
//     sequence.
//
// Strip and HasANSI are pure functions over a complete string and are safe for
// concurrent use. A Decoder is stateful and is NOT safe for concurrent use: it
// must be driven by a single goroutine (one reader per PTY). See Decoder.
package ansi

import (
	"strings"
	"unicode/utf8"
)

const (
	// DefaultMaxLineBytes bounds a single decoded line by default (1 MiB).
	DefaultMaxLineBytes = 1 << 20

	// escByte is the ANSI escape introducer.
	escByte = 0x1b

	// belByte terminates an OSC string in the BEL form.
	belByte = 0x07

	// maxPendingSequence bounds how many bytes may be buffered while waiting for
	// an escape sequence to terminate. A real CSI parameter string is a few dozen
	// bytes and OSC payloads (window titles) are short. The bound stops a
	// truncated or malicious sequence from swallowing the rest of the stream.
	maxPendingSequence = 256

	// replacement is the U+FFFD substitution used for invalid UTF-8.
	replacement = "\uFFFD"
)

// Line is one logical output line.
type Line struct {
	// Raw is the original bytes including ANSI sequences, with the line
	// terminator removed. When KeepRaw is false it is empty. Raw is a snapshot
	// of the input bytes and is never rewritten by the decoder.
	Raw string
	// Plain is the ANSI-stripped, CR-resolved, valid-UTF-8 text used for
	// searching, rule matching and the .plain.log file. It never contains ESC.
	Plain string
	// Truncated reports that MaxLineBytes forced an early flush.
	Truncated bool
	// Partial reports that the line was emitted by Flush without a terminating
	// newline.
	Partial bool
}

// DecoderOptions configures a Decoder.
type DecoderOptions struct {
	// MaxLineBytes is a hard cap on one logical line. Longer lines are flushed
	// early and marked Truncated; the excess continues on the next Line. Zero
	// means DefaultMaxLineBytes.
	MaxLineBytes int
	// KeepRaw retains ANSI bytes in Line.Raw.
	//
	// Note the asymmetry with the zero value: false means "do not keep raw",
	// which is the opposite of what a bare DecoderOptions{} produces. Callers
	// that want raw fidelity must say so explicitly, e.g.
	// DecoderOptions{KeepRaw: true}. internal/supervisor does.
	KeepRaw bool
	// TabWidth expands tabs in Plain to this many columns. Zero leaves tabs
	// alone.
	TabWidth int
}

// Decoder is a stateful, streaming, chunk-safe ANSI and line decoder. Feed it
// raw bytes from a PTY in arbitrary chunks; it emits complete logical lines.
//
// A Decoder is NOT safe for concurrent use. It is designed to be owned by the
// single goroutine that reads the PTY. Use one Decoder per stream.
//
// The zero value is not usable; construct one with NewDecoder.
type Decoder struct {
	opts DecoderOptions

	// scan is the carry buffer: the tail of the previous chunk that could not be
	// resolved yet because an escape sequence (or a trailing CR that might begin
	// a CRLF) was still incomplete. It is prepended to the next chunk, which is
	// what makes split sequences work. Its length is bounded by maxPendingScan.
	scan []byte

	// seqLen counts how many bytes the currently carried, still-unterminated
	// escape sequence spans across all chunks. It is the bound that stops a
	// never-terminating sequence from swallowing the stream: once it exceeds
	// maxPendingSequence the bytes are emitted literally and scanning resumes.
	// It is reset whenever scan is drained.
	seqLen int

	// line holds the bytes of the line being accumulated, terminator excluded.
	// It may legitimately contain ESC (and even a split sequence) until the line
	// is handed to emit, because Raw is byte-faithful.
	line []byte

	// rewind is the offset in line at which the currently visible tail begins:
	// everything before it was erased by a later column-0 CR write. 0 means the
	// whole line is visible. It is only advanced when content actually follows a
	// CR, because a CR with nothing written after it erases nothing.
	rewind int

	// crPending is the offset just past the most recent CR, waiting to see
	// whether any content follows it. If content does, rewind moves here.
	crPending int
}

// NewDecoder builds a Decoder.
func NewDecoder(opts DecoderOptions) *Decoder {
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = DefaultMaxLineBytes
	}
	if opts.TabWidth < 0 {
		opts.TabWidth = 0
	}
	return &Decoder{
		opts: opts,
		scan: make([]byte, 0, maxPendingSequence),
		line: make([]byte, 0, 512),
	}
}

// Strip removes CSI and OSC sequences from a single already-complete string, and
// additionally removes the two-byte ESC-prefixed forms (charset selection and
// saved/restored cursor). Bare CR bytes are dropped and the result is forced to
// valid UTF-8.
//
// Strip is lossy by design and is intended for strings that are already complete.
// For a streaming source use a Decoder, which reassembles sequences split across
// reads.
//
// Strip is pure and safe for concurrent use.
func Strip(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	stripInto(&b, s)
	return validUTF8(b.String())
}

// HasANSI reports whether s contains any escape sequence.
//
// It is a byte scan for ESC (0x1b): that is exactly the condition the rest of
// the panel cares about, because a line is "colored" iff it carries an ESC
// introducer. Detecting ESC rather than a well-formed sequence also means a
// sequence truncated by a chunk boundary still reports true.
//
// HasANSI is pure and safe for concurrent use.
func HasANSI(s string) bool {
	return strings.IndexByte(s, escByte) >= 0
}

// Write feeds raw bytes and returns any complete lines.
//
// Write never retains and never mutates the caller's slice: the bytes are copied
// into the decoder's own buffers before returning.
func (d *Decoder) Write(p []byte) []Line {
	if len(p) == 0 {
		return nil
	}

	// Copy so we never retain or mutate the caller's buffer. Joining the carry
	// with the new chunk also makes a split escape sequence contiguous, which is
	// what makes chunk safety work.
	chunk := make([]byte, 0, len(d.scan)+len(p))
	chunk = append(chunk, d.scan...)
	chunk = append(chunk, p...)
	d.scan = d.scan[:0]

	// If the carry is a still-unterminated "sequence" that has already outgrown
	// the bound, it is not a real sequence: flush those bytes into the line
	// literally so they cannot hold the stream hostage. This is the hard stop
	// required for a truncated or malicious escape sequence.
	if d.seqLen >= maxPendingSequence {
		n := len(chunk)
		if n > maxPendingSequence {
			n = maxPendingSequence
		}
		d.appendLine(chunk[:n])
		chunk = chunk[n:]
		d.seqLen = 0
	}

	var out []Line
	size := len(chunk)
	i := 0

	for i < size {
		b := chunk[i]

		// Fast path: a run of ordinary text.
		if b != escByte && b != '\n' && b != '\r' {
			n := plainRun(chunk[i:])
			// Take only as much as still fits, so the line is cut at exactly
			// MaxLineBytes and the remainder continues on the next Line.
			if room := d.opts.MaxLineBytes - len(d.line); n > room {
				n = room
			}
			d.appendLine(chunk[i : i+n])
			i += n
			if d.overLimit() {
				out = append(out, d.emit(true, false))
			}
		} else {
			switch b {
			case escByte:
				// Try to parse the sequence in place. Success means its
				// terminator is already here, so it is consumed immediately:
				// deferring a complete sequence would delay every line that ends
				// near a chunk boundary by a whole read.
				if n, _, ok := consumeSequence(chunk[i:]); ok {
					d.appendLine(chunk[i : i+n])
					i += n
					break
				}
				// Unterminated, or malformed. It may still be completed by the
				// next chunk, so carry it over — but only while the sequence has
				// not already grown to the bound. Once it reaches
				// maxPendingSequence it cannot be a genuine sequence, so the
				// bytes are emitted literally and normal scanning resumes: an
				// unterminated sequence must never swallow the rest of the
				// stream. (Write also flushes an overgrown carry at entry.)
				if d.seqLen+size-i < maxPendingSequence {
					d.scan = append(d.scan, chunk[i:]...)
					d.seqLen += size - i
					i = size
					continue
				}
				n := maxPendingSequence - d.seqLen
				if n <= 0 {
					n = 1
				}
				if n > size-i {
					n = size - i
				}
				d.appendLine(chunk[i : i+n])
				d.seqLen = 0
				i += n

			case '\r':
				if i+1 < size && chunk[i+1] == '\n' {
					// CRLF is a single terminator. Neither byte is stored, so Raw
					// excludes the terminator; any rewind already recorded by an
					// earlier bare CR still applies to Plain.
					out = append(out, d.emit(false, false))
					i += 2
					continue
				}
				if i+1 >= size {
					// Trailing CR: it may be the first half of a split CRLF, so
					// defer the decision to the next Write or to Flush instead of
					// guessing that it is a rewind.
					d.scan = append(d.scan, chunk[i])
					i++
					continue
				}
				// Bare CR mid-line: return to column 0. Raw keeps the byte.
				d.appendLine(chunk[i : i+1])
				i++

			case '\n':
				out = append(out, d.emit(false, false))
				i++
			}
		}

		// Memory bound: a stream with no newline must not grow without limit.
		if d.overLimit() {
			out = append(out, d.emit(true, false))
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// Flush emits any buffered partial line. Call it at process exit.
//
// An unterminated escape sequence still buffered at Flush time is emitted
// literally rather than dropped, so no bytes are ever lost.
func (d *Decoder) Flush() []Line {
	// Fold any deferred carry back into the line. A trailing CR that never got
	// its LF, or an escape sequence that never terminated, is resolved here:
	// resolveScan turns the CR into a rewind and keeps unmatched escape bytes
	// literally, so nothing is ever dropped.
	d.resolveScan()

	if len(d.line) == 0 {
		return nil
	}
	return []Line{d.emit(false, true)}
}

// resolveScan folds any deferred carry bytes back into the current line. An
// unterminated escape sequence is kept literally: Raw fidelity beats tidiness,
// and dropping bytes would silently corrupt the log.
func (d *Decoder) resolveScan() {
	if len(d.scan) == 0 {
		return
	}
	chunk := d.scan
	d.scan = d.scan[:0]
	d.seqLen = 0

	i := 0
	for i < len(chunk) {
		if chunk[i] != escByte {
			n := plainRun(chunk[i:])
			if n == 0 {
				// A lingering CR/LF that reached Flush: it is a rewind, not text.
				if chunk[i] == '\r' {
					d.markCR()
				}
				i++
				continue
			}
			d.appendLine(chunk[i : i+n])
			i += n
			continue
		}
		n, _, ok := consumeSequence(chunk[i:])
		if !ok {
			// Really unterminated at end of stream: emit the bytes literally.
			d.appendLine(chunk[i:])
			break
		}
		d.appendLine(chunk[i : i+n])
		i += n
	}
}

// overLimit reports whether the current line has reached MaxLineBytes.
func (d *Decoder) overLimit() bool {
	return len(d.line) >= d.opts.MaxLineBytes
}

// markCR records a column-0 rewind at the current end of line. Whether it
// erases the preceding text depends on whether anything is written after it,
// which crPending defers until that is known.
func (d *Decoder) markCR() {
	d.crPending = len(d.line)
}

// appendLine records literal line bytes. Bytes are stored verbatim, including an
// interior CR, so Raw stays faithful; ANSI removal happens later, when Plain is
// built. Line terminators are never passed here.
//
// At most MaxLineBytes bytes are accepted, so no caller can overshoot the cap.
func (d *Decoder) appendLine(b []byte) {
	if len(b) == 0 {
		return
	}
	if room := d.opts.MaxLineBytes - len(d.line); len(b) > room {
		b = b[:room]
	}
	if len(b) == 0 {
		return
	}
	// Walk the segment so a CR is only treated as erasing when content follows
	// it. This keeps "abc\r" (nothing overwritten) distinct from "abc\rXY"
	// (abc erased).
	for i := 0; i < len(b); {
		if b[i] == '\r' {
			d.crPending = len(d.line)
			d.line = append(d.line, '\r')
			i++
			continue
		}
		j := i
		for j < len(b) && b[j] != '\r' {
			j++
		}
		if d.crPending > 0 {
			d.rewind = d.crPending
			d.crPending = 0
		}
		d.line = append(d.line, b[i:j]...)
		i = j
	}
}

// emit builds a Line from the accumulated bytes and resets per-line state.
func (d *Decoder) emit(truncated, partial bool) Line {
	l := Line{Truncated: truncated, Partial: partial}

	// A CR rewinds to column 0: text before the rewind was painted over by
	// whatever followed. Plain keeps only the final segment (last write wins)
	// while Raw keeps every byte. If the rewind sits exactly at the end of the
	// line nothing was painted after it, so the text up to the rewind is still
	// what the terminal shows.
	segment := d.line
	if d.rewind > 0 && d.rewind < len(d.line) {
		segment = d.line[d.rewind:]
	}

	if d.opts.KeepRaw {
		l.Raw = string(d.line)
	}
	l.Plain = d.plain(segment)

	d.line = d.line[:0]
	d.rewind = 0
	d.crPending = 0
	return l
}

// plain renders segment to ANSI-free, valid-UTF-8 text and expands tabs.
func (d *Decoder) plain(segment []byte) string {
	var b strings.Builder
	b.Grow(len(segment))
	stripInto(&b, string(segment))
	out := validUTF8(b.String())
	if d.opts.TabWidth > 0 {
		out = expandTabs(out, d.opts.TabWidth)
	}
	return out
}

// -- escape sequence parsing -------------------------------------------------

// sequence kinds.
const (
	seqCSI = iota + 1
	seqOSC
	seqTwoByte
)

// consumeSequence parses one escape sequence at the start of b, which must begin
// with ESC, and reports its total length. ok is false when the sequence is not
// terminated within b (or is malformed), in which case the caller decides
// whether to wait for more bytes or to emit the bytes literally.
//
// Handled forms:
//
//	CSI       ESC [ params* intermediates* final
//	          params 0x30-0x3f, intermediates 0x20-0x2f, final 0x40-0x7e
//	OSC       ESC ] ... (BEL | ESC \)
//	charset   ESC ( X, ESC ) X, ESC * X, ESC + X, ESC - X, ESC . X, ESC / X
//	DEC       ESC # X
//	two-byte  ESC 7, ESC 8, ESC =, ESC >, ESC c, ...
func consumeSequence(b []byte) (n int, kind int, ok bool) {
	if len(b) == 0 || b[0] != escByte {
		return 0, 0, false
	}
	if len(b) == 1 {
		return 0, 0, false // bare ESC: wait for more
	}

	switch b[1] {
	case '[': // CSI
		i := 2
		for i < len(b) && b[i] >= 0x30 && b[i] <= 0x3f {
			i++
		}
		for i < len(b) && b[i] >= 0x20 && b[i] <= 0x2f {
			i++
		}
		if i >= len(b) {
			return 0, 0, false // final byte not seen yet
		}
		if b[i] >= 0x40 && b[i] <= 0x7e {
			return i + 1, seqCSI, true
		}
		return 0, 0, false // malformed

	case ']': // OSC, terminated by BEL or ST (ESC \)
		i := 2
		for i < len(b) {
			switch b[i] {
			case belByte:
				return i + 1, seqOSC, true
			case escByte:
				if i+1 >= len(b) {
					return 0, 0, false // maybe an ESC \ split across chunks
				}
				if b[i+1] == '\\' {
					return i + 2, seqOSC, true
				}
				return 0, 0, false // a non-ST ESC inside an OSC: malformed
			}
			i++
		}
		return 0, 0, false // no terminator yet

	case '(', ')', '*', '+', '-', '.', '/', '#': // three-byte forms
		if len(b) >= 3 {
			return 3, seqTwoByte, true
		}
		return 0, 0, false

	default:
		if b[1] >= 0x30 && b[1] <= 0x7e { // ESC 7, ESC 8, ESC =, ESC >, ESC c
			return 2, seqTwoByte, true
		}
		return 0, 0, false
	}
}

// stripInto writes s to b with every escape sequence removed.
func stripInto(b *strings.Builder, s string) {
	i := 0
	for i < len(s) {
		c := s[i]
		if c != escByte {
			if c == '\r' || c == '\n' {
				// Terminators and rewinds carry no printable content.
				i++
				continue
			}
			n := plainRunString(s[i:])
			if n == 0 {
				// Defensive: never fail to advance.
				n = 1
			}
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		n, _, ok := consumeSequence([]byte(s[i:]))
		if !ok {
			// Not a sequence we recognise. Drop the ESC but keep what follows, so
			// ordinary text is never swallowed by a stray control byte.
			i++
			continue
		}
		i += n
	}
}

// -- byte helpers ------------------------------------------------------------

// plainRun returns the length of the leading run of bytes that are neither ESC
// nor CR/LF.
func plainRun(b []byte) int {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == escByte || c == '\n' || c == '\r' {
			return i
		}
	}
	return len(b)
}

// plainRunString is plainRun for strings.
func plainRunString(s string) int {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == escByte || c == '\n' || c == '\r' {
			return i
		}
	}
	return len(s)
}

// validUTF8 replaces invalid UTF-8 sequences with U+FFFD.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, replacement)
}

// expandTabs replaces each tab with spaces so the next character lands on a
// multiple of width. Columns advance by rune, measured after ANSI removal, which
// is what callers want for searchable text.
func expandTabs(s string, width int) string {
	if width <= 0 || strings.IndexByte(s, '\t') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := width - col%width
			for i := 0; i < n; i++ {
				b.WriteByte(' ')
			}
			col += n
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}
