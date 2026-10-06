package ansi

import (
	"testing"
)

// benchLine is a realistic ~120-byte game-server log line, matching the shape
// and length of the real output described in §5.2.1. The colored variant wraps
// the dynamic parts in exactly the kind of sequences the PTY emits.
const (
	// ~120 bytes, the realistic length of a game-server log line.
	benchLinePlain = "Loaded world, WorldName=PlanTest, Seed=1234567890, GameMode=Survival, Port=28887, Players=8, Version=X26.07\r\n"
	benchLineANSI  = esc + "[36m[StartServer]" + esc + "[0m" +
		"开启服务器成功，端口 " + esc + "[1;35m28887" + esc + "[0m" +
		", WorldName=" + esc + "[33mPlanTest" + esc + "[0m" +
		", Players=" + esc + "[38;5;208m8" + esc + "[0m" + "\r\n"
)

// benchChunk is the slice handed to Write on each call. The decoder must
// reassemble sequences across writes, so a chunk boundary that lands inside a
// sequence is the realistic (and interesting) case.
var benchChunk = []byte(benchLineANSI)

func BenchmarkWrite_NoANSI(b *testing.B) {
	line := []byte(benchLinePlain)
	if len(line) < 100 || len(line) > 160 {
		b.Fatalf("benchmark line is %d bytes, want a realistic ~120", len(line))
	}

	b.SetBytes(int64(len(line)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// A fresh decoder per iteration keeps the measurement honest: in
		// production one decoder lives for the whole process, but the per-line
		// cost (line building, Strip, UTF-8 validation) is what we care about.
		d := NewDecoder(DecoderOptions{KeepRaw: true, TabWidth: 8})
		if got := d.Write(line); len(got) != 1 {
			b.Fatalf("Write returned %d lines, want 1", len(got))
		}
	}
}

func BenchmarkWrite_WithANSI(b *testing.B) {
	line := []byte(benchLineANSI)
	if len(line) < 100 || len(line) > 200 {
		b.Fatalf("benchmark line is %d bytes, want a realistic ~120-200", len(line))
	}

	b.SetBytes(int64(len(line)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		d := NewDecoder(DecoderOptions{KeepRaw: true, TabWidth: 8})
		if got := d.Write(line); len(got) != 1 {
			b.Fatalf("Write returned %d lines, want 1", len(got))
		}
	}
}

// BenchmarkWrite_SplitSequence measures the chunk-boundary path, which is the
// reason this package exists: the same line delivered in two reads, with the
// cut inside a color sequence.
func BenchmarkWrite_SplitSequence(b *testing.B) {
	full := []byte(benchLineANSI)
	// Cut inside the 256-color sequence.
	cut := 0
	for i := 0; i+2 < len(full); i++ {
		if full[i] == '\x1b' && full[i+1] == '[' {
			cut = i + 3
			break
		}
	}
	if cut == 0 || cut >= len(full) {
		b.Fatal("could not find a split point")
	}
	head, tail := full[:cut], full[cut:]

	b.SetBytes(int64(len(full)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		d := NewDecoder(DecoderOptions{KeepRaw: true, TabWidth: 8})
		d.Write(head)
		if got := d.Write(tail); len(got) != 1 {
			b.Fatalf("Write returned %d lines, want 1", len(got))
		}
	}
}

// BenchmarkStrip isolates the pure stripping path used for on-demand plain
// conversion.
func BenchmarkStrip(b *testing.B) {
	s := benchLineANSI
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = Strip(s)
	}
}

// BenchmarkWrite_SharedDecoder is the production shape: one decoder for the
// whole stream, many lines. It also exercises the carry/steady-state paths.
func BenchmarkWrite_SharedDecoder(b *testing.B) {
	line := []byte(benchLineANSI)
	b.SetBytes(int64(len(line)))
	b.ReportAllocs()
	b.ResetTimer()

	d := NewDecoder(DecoderOptions{KeepRaw: true, TabWidth: 8})
	for i := 0; i < b.N; i++ {
		d.Write(line)
	}
}
