//go:build linux

package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------- decode seam

// fakeDecoder is a deterministic lineDecoder used to prove the ring buffer,
// disk rotation and fan-out do not depend on internal/ansi's internals.
type fakeDecoder struct {
	emit  []string
	buf   []byte
	flush int
}

func (f *fakeDecoder) Write(p []byte) []ansiLine {
	f.buf = append(f.buf, p...)
	var out []ansiLine
	for {
		i := -1
		for j := range f.buf {
			if f.buf[j] == '\n' {
				i = j
				break
			}
		}
		if i < 0 {
			break
		}
		raw := string(f.buf[:i])
		f.buf = f.buf[i+1:]
		out = append(out, ansiLine{Raw: raw, Plain: stripForTest(raw)})
	}
	return out
}

func (f *fakeDecoder) Flush() []ansiLine {
	f.flush++
	if len(f.buf) == 0 {
		return nil
	}
	raw := string(f.buf)
	f.buf = f.buf[:0]
	return []ansiLine{{Raw: raw, Plain: stripForTest(raw), Partial: true}}
}

// stripForTest removes CSI sequences; enough for the fake's purposes.
func stripForTest(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func newTestPipe(t *testing.T, ring int, logDir string) *LogPipe {
	t.Helper()
	p, err := newLogPipe(ring, logDir, time.Second, true)
	if err != nil {
		t.Fatalf("newLogPipe: %v", err)
	}
	t.Cleanup(func() { _ = p.close() })
	return p
}

// ---------------------------------------------------------------- ring buffer

// TestRingBufferReplayOrder is the "last N lines in order" requirement.
func TestRingBufferReplayOrder(t *testing.T) {
	p := newTestPipe(t, 10, "")
	for i := 1; i <= 25; i++ {
		p.Ingest([]byte("line-" + itoa(i) + "\n"))
	}

	got := p.Replay(0, false)
	if len(got) != 10 {
		t.Fatalf("replay length = %d, want 10 (ring capacity)", len(got))
	}
	// Oldest first: the last 10 lines produced are 16..25.
	for i, rec := range got {
		want := "line-" + itoa(16+i)
		if rec.Plain != want {
			t.Errorf("replay[%d] = %q, want %q", i, rec.Plain, want)
		}
	}
	// Sequence numbers must be strictly increasing.
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Errorf("Seq not increasing at %d: %d then %d", i, got[i-1].Seq, got[i].Seq)
		}
	}
	// Partial replay returns the newest n.
	tail := p.Replay(3, false)
	if len(tail) != 3 || tail[0].Plain != "line-23" || tail[2].Plain != "line-25" {
		t.Errorf("Replay(3) = %+v, want line-23..line-25", tail)
	}
}

// TestRingBufferKeepsRawANSI proves the ring stores the colour-bearing text.
func TestRingBufferKeepsRawANSI(t *testing.T) {
	p := newTestPipe(t, 8, "")
	p.Ingest([]byte("\x1b[32mgreen line\x1b[0m\n"))

	raw := p.Replay(0, true)
	if len(raw) != 1 {
		t.Fatalf("replay length = %d, want 1", len(raw))
	}
	if !strings.Contains(raw[0].Raw, "\x1b[32m") {
		t.Errorf("Raw lost the ANSI sequence: %q", raw[0].Raw)
	}
	if raw[0].Plain != "green line" {
		t.Errorf("Plain = %q, want %q", raw[0].Plain, "green line")
	}
	// raw=false must blank the Raw field for the cheaper wire format.
	plain := p.Replay(0, false)
	if plain[0].Raw != "" {
		t.Errorf("raw=false still carried Raw: %q", plain[0].Raw)
	}
}

// ---------------------------------------------------------------- disk

// TestLogFilesRawVsPlain is the dual-write requirement: the .log file keeps ANSI
// bytes, the .plain.log file must not contain a single ESC.
func TestLogFilesRawVsPlain(t *testing.T) {
	dir := t.TempDir()
	p := newTestPipe(t, 16, dir)
	p.Ingest([]byte("\x1b[31mERROR: 红色错误\x1b[0m\n"))
	p.Ingest([]byte("普通中文行\n"))
	p.Flush()
	if err := p.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	day := dayKey(time.Now())
	rawB, err := os.ReadFile(filepath.Join(dir, day+logFileRawSuffix))
	if err != nil {
		t.Fatalf("read raw log: %v", err)
	}
	plainB, err := os.ReadFile(filepath.Join(dir, day+logFilePlainSuffix))
	if err != nil {
		t.Fatalf("read plain log: %v", err)
	}

	if !strings.Contains(string(rawB), "\x1b[31m") {
		t.Errorf("raw log has no ANSI bytes: %q", rawB)
	}
	if strings.Contains(string(plainB), "\x1b") {
		t.Errorf("plain log contains ANSI bytes: %q", plainB)
	}
	if !strings.Contains(string(plainB), "ERROR: 红色错误") {
		t.Errorf("plain log lost the text: %q", plainB)
	}
	if !strings.Contains(string(rawB), "普通中文行") {
		t.Errorf("raw log lost the UTF-8 line: %q", rawB)
	}
	// Both files must have one line per input line.
	if got := strings.Count(strings.TrimRight(string(rawB), "\n"), "\n") + 1; got != 2 {
		t.Errorf("raw log has %d lines, want 2", got)
	}
	if got := strings.Count(strings.TrimRight(string(plainB), "\n"), "\n") + 1; got != 2 {
		t.Errorf("plain log has %d lines, want 2", got)
	}
}

// TestLogRotationAppendsAcrossDays proves the writer appends to an existing
// file and rotates on the local date.
func TestLogRotationAppendsAcrossDays(t *testing.T) {
	dir := t.TempDir()
	w, err := newRotatingWriter(dir, ".log")
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	if _, err := w.WriteLine("first"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen: the same day must append, not truncate.
	w2, err := newRotatingWriter(dir, ".log")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := w2.WriteLine("second"); err != nil {
		t.Fatalf("write: %v", err)
	}
	day := dayKey(time.Now())
	if err := w2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, day+".log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "first\nsecond\n" {
		t.Errorf("content = %q, want append semantics", b)
	}

	// Simulate a date rollover with the clock seam: the writer must close the
	// old file and start a new dated one on the next write.
	w3, err := newRotatingWriter(dir, ".log")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	nextDay := time.Date(2030, 3, 4, 1, 2, 3, 0, time.Local)
	w3.mu.Lock()
	w3.now = func() time.Time { return nextDay }
	w3.mu.Unlock()
	if _, err := w3.WriteLine("next day"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w3.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	newPath := filepath.Join(dir, dayKey(nextDay)+".log")
	b2, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("rotation did not create %s: %v", newPath, err)
	}
	if string(b2) != "next day"+"\n" {
		t.Errorf("rotated file = %q, want %q", b2, "next day\n")
	}
	// The previous day's file must be untouched.
	oldB, err := os.ReadFile(filepath.Join(dir, day+".log"))
	if err != nil {
		t.Fatalf("read old log: %v", err)
	}
	if string(oldB) != "first\nsecond\n" {
		t.Errorf("old log = %q, want it untouched", oldB)
	}
}

// ---------------------------------------------------------------- subscribers

// TestSubscriptionReceivesLiveLines covers the basic fan-out contract.
func TestSubscriptionReceivesLiveLines(t *testing.T) {
	p := newTestPipe(t, 32, "")
	sub, err := p.subscribe(0, true)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	p.Ingest([]byte("alpha\n"))
	p.Ingest([]byte("beta\n"))

	var got []string
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case rec := <-sub.Ch():
			got = append(got, rec.Plain)
		case <-deadline:
			t.Fatalf("timed out; got %v", got)
		}
	}
	if got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("got %v, want [alpha beta]", got)
	}
}

// TestSubscriptionReplay proves a late subscriber gets backlog first.
func TestSubscriptionReplay(t *testing.T) {
	p := newTestPipe(t, 32, "")
	for i := 0; i < 5; i++ {
		p.Ingest([]byte("line-" + itoa(i) + "\n"))
	}
	sub, err := p.subscribe(3, true)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	var got []string
	for i := 0; i < 3; i++ {
		select {
		case rec := <-sub.Ch():
			got = append(got, rec.Plain)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d replayed lines", i)
		}
	}
	want := []string{"line-2", "line-3", "line-4"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("replay[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSlowSubscriberDoesNotBlock is the critical back-pressure guarantee: a
// subscriber that never reads must not stall the reader goroutine, and must be
// told explicitly how many lines it lost.
func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	p := newTestPipe(t, 4096, "")
	sub, err := p.subscribe(0, true)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	// Never read from sub.Ch(). Push far more than the subscriber can hold. The
	// total is derived from the real buffering rather than a magic constant, so
	// the test stays deterministic even when the pump goroutine is slowed by
	// -race instrumentation.
	total := SubscriberBufferDepth() * 8
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < total; i++ {
			p.Ingest([]byte("flood-" + itoa(i) + "\n"))
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Ingest blocked on a slow subscriber: the reader goroutine would stall")
	}

	// The producer must report losses. Allow a brief settle: the counter is
	// incremented by the reader goroutine that also did the flooding.
	waitFor(t, 2*time.Second, "dropped lines to be accounted", func() bool {
		return sub.Dropped() > 0
	})

	// Now drain: the first lines delivered should include an explicit marker
	// rather than a silent gap.
	deadline := time.After(3 * time.Second)
	sawMarker := false
	for !sawMarker {
		select {
		case rec := <-sub.Ch():
			if strings.Contains(rec.Plain, "dropped") && strings.Contains(rec.Plain, "log lines") {
				sawMarker = true
			}
		case <-deadline:
			t.Fatal("no 'dropped N lines' marker was ever delivered")
		}
	}

	// The pipe itself must remain healthy for a well-behaved subscriber. Note
	// this subscriber attaches *after* a slow one overflowed, so it receives the
	// full replay backlog (subBuffer lines) plus one drop marker before the new
	// line. What matters is that the real line still arrives, promptly and in
	// order, rather than the exact interleaving.
	fast, err := p.subscribe(0, true)
	if err != nil {
		t.Fatalf("subscribe (fast): %v", err)
	}
	defer fast.Close()
	p.Ingest([]byte("after-flood\n"))

	found := false
	drainDeadline := time.After(5 * time.Second)
	for !found {
		select {
		case rec := <-fast.Ch():
			if rec.Plain == "after-flood" {
				found = true
			}
		case <-drainDeadline:
			t.Fatal("fast subscriber starved after a slow subscriber overflowed")
		}
	}
}

// TestConcurrentSubscribersUnderRace exercises fan-out under -race.
func TestConcurrentSubscribersUnderRace(t *testing.T) {
	p := newTestPipe(t, 512, "")
	const subs = 8
	var wg sync.WaitGroup
	for i := 0; i < subs; i++ {
		s, err := p.subscribe(0, true)
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		wg.Add(1)
		go func(s *Subscription) {
			defer wg.Done()
			defer s.Close()
			count := 0
			timeout := time.After(3 * time.Second)
			for count < 50 {
				select {
				case _, ok := <-s.Ch():
					if !ok {
						return
					}
					count++
				case <-timeout:
					return
				}
			}
		}(s)
	}
	for i := 0; i < 50; i++ {
		p.Ingest([]byte("msg-" + itoa(i) + "\n"))
	}
	wg.Wait()
}

// TestSubscriptionCloseIsIdempotent covers double-close and post-close writes.
func TestSubscriptionCloseIsIdempotent(t *testing.T) {
	p := newTestPipe(t, 8, "")
	sub, err := p.subscribe(0, true)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sub.Close()
	sub.Close() // must not panic
	p.Ingest([]byte("after close\n"))
	select {
	case _, ok := <-sub.Ch():
		if ok {
			t.Error("received a line after Close")
		}
	case <-time.After(200 * time.Millisecond):
		// Nothing to read is also acceptable; the important part is no panic.
	}
}

// TestPipeCloseClosesSubscribers proves the shutdown path is clean.
func TestPipeCloseClosesSubscribers(t *testing.T) {
	p := newTestPipe(t, 8, "")
	sub, err := p.subscribe(0, true)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := p.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := p.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	select {
	case _, ok := <-sub.Ch():
		if ok {
			t.Error("subscription still delivered after pipe close")
		}
	case <-time.After(time.Second):
		t.Error("subscription channel was not closed with the pipe")
	}
	if _, err := p.subscribe(0, true); !isErr(err, ErrClosed) {
		t.Errorf("subscribe after close = %v, want ErrClosed", err)
	}
}

// ---------------------------------------------------------------- runner glue

// TestRunnerSubscribeRejectsStoppedInstance covers the typed error the API's
// WebSocket handler has to handle.
func TestRunnerSubscribeRejectsStoppedInstance(t *testing.T) {
	r, err := NewRunner(helperOpts(t, "ready"))
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := r.Subscribe(context.Background(), 0, true); err == nil {
		t.Error("Subscribe on a stopped instance returned nil error")
	}
	if got := r.Replay(context.Background(), 10, true); got != nil {
		t.Errorf("Replay on a stopped instance = %v, want nil", got)
	}
}

// TestRunnerSubscribeCancelledContext proves ctx cancellation detaches.
func TestRunnerSubscribeCancelledContext(t *testing.T) {
	r := startRunner(t, helperOpts(t, "ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.WaitState(ctx, func(s State) bool { return s == StateRunning }); err != nil {
		t.Fatalf("wait for Running: %v", err)
	}

	sctx, scancel := context.WithCancel(context.Background())
	sub, err := r.Subscribe(sctx, 0, true)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	scancel()
	waitFor(t, 2*time.Second, "subscription detached on ctx cancel", func() bool {
		_, dropped, subs := r.pipeStats()
		return subs == 0 || dropped >= 0 && subscriptionClosed(sub)
	})
}

// subscriptionClosed reports whether the subscription's channel is done.
func subscriptionClosed(s *Subscription) bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

// pipeStats reaches the runner's pipe for assertions.
func (r *Runner) pipeStats() (lines, dropped uint64, subs int) {
	r.mu.RLock()
	p := r.pipe
	r.mu.RUnlock()
	if p == nil {
		return 0, 0, 0
	}
	return p.Stats()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
