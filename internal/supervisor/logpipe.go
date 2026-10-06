package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// LogRecord is one decoded log line as delivered to subscribers.
type LogRecord struct {
	// Raw is the original bytes, ANSI escape sequences intact. This is what
	// xterm.js renders (§5.2 link 3).
	Raw string `json:"raw,omitempty"`
	// Plain is the ANSI-stripped text used for matching and grep.
	Plain string `json:"plain"`
	// TS is when the line was decoded.
	TS time.Time `json:"ts"`
	// Seq is a per-Runner monotonic line counter, starting at 1.
	Seq uint64 `json:"seq"`
	// Truncated reports that the line exceeded the decoder's MaxLineBytes.
	Truncated bool `json:"truncated,omitempty"`
	// Partial reports a line emitted by Flush mid-sequence (no trailing
	// newline), i.e. the tail of a process that died mid-line.
	Partial bool `json:"partial,omitempty"`
}

// maxLineBytes bounds a single decoded line. The ring buffer keeps at most
// RingLines of these, so worst-case memory is bounded and predictable.
const maxLineBytes = 8 << 10

// tabWidth is used when expanding tabs for Plain output.
const tabWidth = 8

// Subscription tuning.
const (
	// subBuffer is the per-subscriber OUTBOUND queue depth, i.e. the number of
	// records a client may fall behind by. A subscriber that cannot keep up
	// loses lines (with an explicit marker) rather than stalling the reader.
	subBuffer = 512
	// subInBuffer is the hand-off queue between the reader goroutine and the
	// subscription's pump. It is intentionally shallow: it exists only to smooth
	// scheduling jitter, and a deep queue here would hide back-pressure from the
	// producer, making the drop accounting unpredictable.
	subInBuffer = 32
	// maxDroppedMarkers bounds consecutive "dropped N lines" markers.
	droppedMarker = "[scnetm] dropped %d log lines (subscriber too slow)"
)

// LogPipe owns everything downstream of the raw byte stream: ANSI decoding,
// the replay ring, disk writers and the subscriber fan-out (§5.2).
//
// Concurrency model: a single reader goroutine calls Ingest; subscribers each
// drain their own buffered channel. Subscriber channels are never written to
// from anywhere except Ingest, and the write is always non-blocking.
type LogPipe struct {
	decoder lineDecoder

	mu     sync.RWMutex
	closed bool

	// ring is a fixed-capacity circular buffer of records (oldest overwritten).
	ring    []LogRecord
	ringLen int
	ringPos int // next write index

	seq atomic.Uint64

	// onLine is invoked synchronously per line (Options.OnLine).
	onLine func(ansiLine)
	// onRecord is invoked synchronously per record, if set (used by Runner for
	// anchor matching + rule events).
	onRecord func(LogRecord)

	subs map[*Subscription]struct{}

	// disk writers
	rawW   *rotatingWriter
	plainW *rotatingWriter

	flushInterval time.Duration

	// stats
	linesTotal   atomic.Uint64
	droppedTotal atomic.Uint64
}

// newLogPipe builds the pipe. logDir may be empty to disable disk logging.
func newLogPipe(ringLines int, logDir string, flushInterval time.Duration, keepRaw bool) (*LogPipe, error) {
	if ringLines < 0 {
		ringLines = 0
	}
	p := &LogPipe{
		decoder:       newLineDecoder(maxLineBytes, tabWidth),
		ring:          make([]LogRecord, ringLines),
		subs:          make(map[*Subscription]struct{}),
		flushInterval: flushInterval,
	}
	if logDir != "" {
		rw, err := newRotatingWriter(logDir, ".log")
		if err != nil {
			return nil, err
		}
		pw, err := newRotatingWriter(logDir, ".plain.log")
		if err != nil {
			_ = rw.Close()
			return nil, err
		}
		p.rawW, p.plainW = rw, pw
	}
	return p, nil
}

// Ingest feeds raw bytes read from the PTY/pipe. It is called from exactly one
// goroutine and never blocks on subscribers.
func (p *LogPipe) Ingest(b []byte) {
	lines := p.decoder.Write(b)
	p.dispatch(lines)
}

// Flush drains the decoder (tail line without a newline) and flushes disk
// writers. Called once when the reader loop ends and by Stop.
func (p *LogPipe) Flush() {
	p.dispatch(p.decoder.Flush())
	p.mu.RLock()
	rw, pw := p.rawW, p.plainW
	p.mu.RUnlock()
	if rw != nil {
		_ = rw.Flush()
	}
	if pw != nil {
		_ = pw.Flush()
	}
}

// FlushDisk flushes the buffered writers without touching the decoder.
func (p *LogPipe) FlushDisk() {
	p.mu.RLock()
	rw, pw := p.rawW, p.plainW
	p.mu.RUnlock()
	if rw != nil {
		_ = rw.Flush()
	}
	if pw != nil {
		_ = pw.Flush()
	}
}

func (p *LogPipe) dispatch(lines []ansiLine) {
	for i := range lines {
		l := lines[i]
		now := time.Now()
		rec := LogRecord{
			Raw:       l.Raw,
			Plain:     l.Plain,
			TS:        now,
			Seq:       p.seq.Add(1),
			Truncated: l.Truncated,
			Partial:   l.Partial,
		}
		p.record(rec, l)
	}
}

// record handles one decoded record: ring, disk, callbacks, fan-out.
func (p *LogPipe) record(rec LogRecord, l ansiLine) {
	p.linesTotal.Add(1)

	// 1. ring buffer (raw, ANSI intact) — replay for new console clients.
	p.mu.Lock()
	if len(p.ring) > 0 {
		p.ring[p.ringPos] = rec
		p.ringPos = (p.ringPos + 1) % len(p.ring)
		if p.ringLen < len(p.ring) {
			p.ringLen++
		}
	}
	rw, pw := p.rawW, p.plainW
	closed := p.closed
	p.mu.Unlock()

	if closed {
		return
	}

	// 2. disk, both flavours (§5.2 link 2). Errors are non-fatal by design: a
	// full disk must not kill a running game server, but the caller can see
	// them via DiskErrors().
	if rw != nil {
		_, _ = rw.WriteLine(rec.Raw)
	}
	if pw != nil {
		_, _ = pw.WriteLine(rec.Plain)
	}

	// 3. synchronous callbacks.
	if p.onLine != nil {
		p.onLine(l)
	}
	if p.onRecord != nil {
		p.onRecord(rec)
	}

	// 4. fan-out. Each subscriber write is non-blocking: a slow consumer is
	// dropped (with an explicit marker) rather than stalling the reader.
	p.fanout(rec)
}

// fanout delivers rec to every subscriber without blocking.
func (p *LogPipe) fanout(rec LogRecord) {
	p.mu.RLock()
	if len(p.subs) == 0 {
		p.mu.RUnlock()
		return
	}
	subs := make([]*Subscription, 0, len(p.subs))
	for s := range p.subs {
		subs = append(subs, s)
	}
	p.mu.RUnlock()

	for _, s := range subs {
		s.deliver(rec)
	}
}

// Replay returns up to n recent records, oldest first. raw selects whether the
// Raw field is populated (Raw is always stored; the flag only clears it on
// output so the WebSocket layer can send the cheaper plain form).
func (p *LogPipe) Replay(n int, raw bool) []LogRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.replayLocked(n, raw)
}

// RingLen reports how many records the ring currently holds.
func (p *LogPipe) RingLen() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ringLen
}

// SubscriberBufferDepth reports the total number of records a subscriber may
// hold before it starts losing lines (the hand-off queue plus the outbound
// queue). Exposed for tests and for capacity planning in the API layer.
func SubscriberBufferDepth() int { return subBuffer + subInBuffer }

// Stats returns cumulative counters for the metrics/UI layer.
func (p *LogPipe) Stats() (lines, dropped uint64, subs int) {
	p.mu.RLock()
	n := len(p.subs)
	p.mu.RUnlock()
	return p.linesTotal.Load(), p.droppedTotal.Load(), n
}

// subscribe registers a new subscriber and optionally replays the last
// replayLines records into it.
func (p *LogPipe) subscribe(replayLines int, raw bool) (*Subscription, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	s := &Subscription{
		pipe:   p,
		in:     make(chan LogRecord, subInBuffer),
		ch:     make(chan LogRecord, subBuffer),
		raw:    raw,
		closed: make(chan struct{}),
	}
	p.subs[s] = struct{}{}
	replay := p.replayLocked(replayLines, raw)
	p.mu.Unlock()

	// Seed the outbound channel with the backlog BEFORE starting the pump. Doing
	// it here, synchronously and before the pump exists, guarantees the client
	// sees its backlog first, in order, with no interleaving against live records.
	// Later replay lines win if the backlog exceeds the queue (newest-first is
	// what a console wants), and nothing is reported as "dropped" -- this is a
	// backlog, not data loss.
	if len(replay) > cap(s.ch) {
		replay = replay[len(replay)-cap(s.ch):]
	}
	for _, rec := range replay {
		s.ch <- rec
	}

	go s.pump()
	return s, nil
}

// replayLocked is Replay with p.mu already held.
//
// Semantics: "the last n lines, oldest first". The ring holds up to ringLen
// records; the newest one sits just before ringPos. So the window starts at
// ringPos-n (NOT ringPos-ringLen), and n is clamped to what is actually stored.
func (p *LogPipe) replayLocked(n int, raw bool) []LogRecord {
	if len(p.ring) == 0 || p.ringLen == 0 {
		return nil
	}
	if n <= 0 || n > p.ringLen {
		n = p.ringLen
	}
	if n <= 0 {
		return nil
	}
	out := make([]LogRecord, 0, n)
	start := p.ringPos - n
	if start < 0 {
		start += len(p.ring)
	}
	for i := 0; i < n; i++ {
		rec := p.ring[(start+i)%len(p.ring)]
		if !raw {
			rec.Raw = ""
		}
		out = append(out, rec)
	}
	return out
}

func (p *LogPipe) unsubscribe(s *Subscription) {
	p.mu.Lock()
	delete(p.subs, s)
	p.mu.Unlock()
}

// close shuts the pipe: no more fan-out, subscribers are closed, writers
// flushed and closed. Idempotent.
func (p *LogPipe) close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	subs := make([]*Subscription, 0, len(p.subs))
	for s := range p.subs {
		subs = append(subs, s)
	}
	p.subs = map[*Subscription]struct{}{}
	rw, pw := p.rawW, p.plainW
	p.rawW, p.plainW = nil, nil
	p.mu.Unlock()

	for _, s := range subs {
		s.close()
	}
	var firstErr error
	if rw != nil {
		if err := rw.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if pw != nil {
		if err := pw.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Subscription is a live log stream handed to the WebSocket layer.
//
// Records arrive on an internal channel and are forwarded to the public one by a
// pump goroutine. That indirection exists for one reason: the "dropped N lines"
// marker must be injected when the consumer has room, which is a condition only
// observable on the receiving side. The producer stays strictly non-blocking.
type Subscription struct {
	pipe *LogPipe
	in   chan LogRecord // producer side; writes are non-blocking
	ch   chan LogRecord // consumer side; closed on shutdown
	raw  bool

	closeOnce sync.Once
	closed    chan struct{}

	dropped    atomic.Uint64
	lastMarker atomic.Int64 // unix nanos of last dropped marker
}

// Ch is the record stream. It is closed when the subscription is closed or the
// pipe shuts down, so `for range sub.Ch()` terminates cleanly.
func (s *Subscription) Ch() <-chan LogRecord { return s.ch }

// pump relays records from the internal queue to the consumer channel, injecting
// the coalescing "dropped N lines" marker whenever the consumer has room.
//
// Losslessness is structural: the pump never RECEIVES from s.in unless it can
// simultaneously SEND to s.ch. A record that has been accepted from the producer
// therefore cannot be discarded here. The only losses are the ones the producer
// explicitly counted because s.in was full, and those are always announced.
func (s *Subscription) pump() {
	defer close(s.ch)
	for {
		// A pending marker takes priority and does not consume a record.
		if n := s.dropped.Load(); n > 0 {
			marker := LogRecord{
				Plain: fmt.Sprintf(droppedMarker, n),
				TS:    time.Now(),
			}
			if s.raw {
				marker.Raw = marker.Plain
			}
			select {
			case <-s.closed:
				return
			case s.ch <- marker:
				s.dropped.Add(^uint64(n - 1)) // subtract exactly n
				continue
			case rec := <-s.in:
				// Room freed for a real record first; serve it, then the marker.
				select {
				case s.ch <- rec:
				case <-s.closed:
					return
				}
				continue
			}
		}

		select {
		case <-s.closed:
			return
		case rec := <-s.in:
			select {
			case s.ch <- rec:
			case <-s.closed:
				return
			}
		}
	}
}

// Raw reports whether Raw fields are populated for this subscription.
func (s *Subscription) Raw() bool { return s.raw }

// Dropped reports how many lines this subscriber lost to back-pressure.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Close unregisters the subscriber. Safe to call multiple times and from any
// goroutine; it never blocks.
//
// Ordering matters: the subscriber is removed from the fan-out map (under the
// pipe lock) BEFORE its channel is closed, which guarantees the reader
// goroutine can no longer hold a reference to send on it.
func (s *Subscription) Close() {
	s.closeOnce.Do(func() {
		// Detach under the pipe lock FIRST so the reader can no longer reach
		// this subscription, then signal the pump to drain and exit. The pump
		// closes s.ch on its way out; closing it here would double-close.
		s.pipe.unsubscribe(s)
		close(s.closed)
	})
}

// close is the pipe-initiated shutdown. The pump goroutine closes s.ch when it
// observes this, so a consumer's `for range sub.Ch()` terminates cleanly.
func (s *Subscription) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
	})
}

// deliver performs the non-blocking hand-off. On overflow the record is
// discarded and a single "dropped N lines" marker is injected once the channel
// drains, so the console shows an honest gap instead of silently missing data.
func (s *Subscription) deliver(rec LogRecord) {
	if !s.raw {
		rec.Raw = ""
	}
	select {
	case s.in <- rec:
		return
	case <-s.closed:
		return
	default:
		// Queue full: drop and account. The pump will report the loss as soon
		// as the consumer drains, even if no further records ever arrive.
		s.dropped.Add(1)
		s.pipe.droppedTotal.Add(1)
	}
}

func (s *Subscription) markDropped(n int) {
	s.dropped.Add(uint64(n))
	s.pipe.droppedTotal.Add(uint64(n))
}

// ---------------------------------------------------------------- disk writer

// rotatingWriter appends to <dir>/<YYYY-MM-DD><suffix> and rotates when the
// local date changes. Output is buffered and flushed periodically.
type rotatingWriter struct {
	dir    string
	suffix string
	mu     sync.Mutex
	file   *os.File
	buf    *bufio.Writer
	day    string
	err    error

	// now is the clock seam. Production leaves it nil (wall clock); tests set it
	// to simulate a date rollover without waiting for midnight.
	now func() time.Time
}

// clock returns the writer's current time source.
func (w *rotatingWriter) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func newRotatingWriter(dir, suffix string) (*rotatingWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("supervisor: create log dir %s: %w", dir, err)
	}
	w := &rotatingWriter{dir: dir, suffix: suffix}
	if err := w.rotateLocked(w.clock()); err != nil {
		return nil, err
	}
	return w, nil
}

// dayKey formats the local calendar date used for rotation (§5.2).
func dayKey(t time.Time) string { return t.Format("2006-01-02") }

// Path returns the file currently being written.
func (w *rotatingWriter) Path() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return filepath.Join(w.dir, w.day+w.suffix)
}

func (w *rotatingWriter) rotateLocked(now time.Time) error {
	key := dayKey(now)
	if w.file != nil {
		_ = w.buf.Flush()
		_ = w.file.Close()
		w.file, w.buf = nil, nil
	}
	path := filepath.Join(w.dir, key+w.suffix)
	// O_APPEND: appending if the file already exists (§5.2).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		w.err = err
		return fmt.Errorf("supervisor: open log %s: %w", path, err)
	}
	w.file = f
	w.buf = bufio.NewWriterSize(f, 32<<10)
	w.day = key
	return nil
}

// WriteLine writes one line plus a newline, rotating across local midnight.
func (w *rotatingWriter) WriteLine(s string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf == nil {
		return 0, ErrClosed
	}
	if now := w.clock(); dayKey(now) != w.day {
		if err := w.rotateLocked(now); err != nil {
			return 0, err
		}
	}
	n, err := w.buf.WriteString(s)
	if err != nil {
		w.err = err
		return n, err
	}
	if err := w.buf.WriteByte('\n'); err != nil {
		w.err = err
		return n, err
	}
	return n + 1, nil
}

// Flush pushes buffered bytes to the OS. Safe to call concurrently.
func (w *rotatingWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf == nil {
		return nil
	}
	return w.buf.Flush()
}

// Close flushes and closes. Idempotent.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf == nil {
		return nil
	}
	ferr := w.buf.Flush()
	cerr := w.file.Close()
	w.buf, w.file = nil, nil
	if ferr != nil {
		return ferr
	}
	return cerr
}

// runFlusher periodically flushes both writers until ctx is done, so a tail of
// log lines survives a panel crash (§5.2 "flushed periodically and on close").
func (p *LogPipe) runFlusher(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.FlushDisk()
		}
	}
}
