package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests cover the internal building blocks that the HTTP-level tests
// reach only indirectly: the log fan-out hub, the bounded file readers, and the
// directory accounting helpers. They are cheap, deterministic and they pin
// behaviour that is easy to break silently — particularly the "drop when the
// consumer is behind" policy, which must never block a producer.

func TestBroadcastLogHubFanOut(t *testing.T) {
	t.Parallel()

	hub := NewBroadcastLogHub(NewDiscardLogger())

	a := hub.Subscribe(1)
	b := hub.Subscribe(1)
	other := hub.Subscribe(2)
	defer a.Close()
	defer b.Close()
	defer other.Close()

	hub.PublishLog(1, LogLine{Text: "hello"})

	// Both subscribers of instance 1 receive the line.
	for i, f := range []*LogFanout{a, b} {
		select {
		case line := <-f.Lines():
			if line.Text != "hello" {
				t.Errorf("subscriber %d got %q, want hello", i, line.Text)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}

	// The subscriber of a different instance must NOT receive it: leaking logs
	// across instances would show one tenant's console to another.
	select {
	case line := <-other.Lines():
		t.Fatalf("SECURITY: instance 2 received instance 1's log line: %q", line.Text)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestBroadcastLogHubDropsWhenTheConsumerIsBehind pins the lossy policy: a slow
// subscriber must never block the producer.
func TestBroadcastLogHubDropsWhenTheConsumerIsBehind(t *testing.T) {
	t.Parallel()

	hub := NewBroadcastLogHub(NewDiscardLogger())

	slow := hub.Subscribe(1)
	defer slow.Close()

	// Publish far more than the buffer holds without ever reading.
	const publishes = 1000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < publishes; i++ {
			hub.PublishLog(1, LogLine{Text: "flood"})
		}
	}()

	select {
	case <-done:
		// The producer finished: it was never blocked by the unread buffer.
	case <-time.After(5 * time.Second):
		t.Fatal("PublishLog blocked on a subscriber that was not reading")
	}

	// The channel holds at most its capacity, and is never closed by a drop.
	got := 0
	for {
		select {
		case _, ok := <-slow.Lines():
			if !ok {
				t.Fatal("the fan-out channel was closed by a dropped line")
			}
			got++
			if got > 4096 {
				t.Fatal("the buffer grew without bound")
			}
			continue
		default:
		}
		break
	}
	if got == 0 {
		t.Error("no lines were buffered at all")
	}
	if got >= publishes {
		t.Errorf("buffered %d of %d lines: the buffer is unbounded", got, publishes)
	}
}

func TestLogFanoutCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	hub := NewBroadcastLogHub(NewDiscardLogger())

	f := hub.Subscribe(1)
	f.Close()
	// A second close must not panic on closing an already-closed channel; the
	// WebSocket cleanup path is re-entrant.
	f.Close()

	// Publishing to a closed fan-out must not panic.
	hub.PublishLog(1, LogLine{Text: "after close"})

	// Re-subscribing after a close works, and publishing reaches the new one.
	again := hub.Subscribe(1)
	defer again.Close()
	hub.PublishLog(1, LogLine{Text: "second life"})
	select {
	case line := <-again.Lines():
		if line.Text != "second life" {
			t.Errorf("got %q", line.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("the re-subscribed fan-out received nothing")
	}
}

func TestEventHubSubscriberLifecycle(t *testing.T) {
	t.Parallel()

	hub := NewEventHub(NewDiscardLogger())
	defer hub.Close()

	sub := hub.Subscribe()
	if got := hub.SubscriberCount(); got != 1 {
		t.Errorf("SubscriberCount = %d, want 1", got)
	}
	second := hub.Subscribe()
	defer second.Close()
	if got := hub.SubscriberCount(); got != 2 {
		t.Errorf("SubscriberCount = %d, want 2", got)
	}

	hub.Publish(Event{Type: "instance.state"})
	select {
	case ev := <-sub.Events():
		if ev.Type != "instance.state" {
			t.Errorf("event type = %q", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("no event was delivered")
	}

	sub.Close()
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("Done was not closed by Close")
	}
	if got := hub.SubscriberCount(); got != 1 {
		t.Errorf("SubscriberCount after close = %d, want 1 (the other subscriber)", got)
	}

	// Closing twice must be safe.
	sub.Close()
}

func TestReadFileLimited(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, truncated, err := readFileLimited(small, 1024)
	if err != nil {
		t.Fatalf("readFileLimited: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("data = %q", data)
	}
	if truncated {
		t.Error("a file smaller than the limit must not be reported as truncated")
	}

	// The limit truncates rather than erroring: a huge log tail must still be
	// servable.
	data, truncated, err = readFileLimited(small, 5)
	if err != nil {
		t.Fatalf("readFileLimited(limit=5): %v", err)
	}
	if len(data) > 5 {
		t.Errorf("read %d bytes with a limit of 5", len(data))
	}
	if !truncated {
		t.Error("a file larger than the limit must be reported as truncated")
	}

	// A missing file is an error, not empty data.
	if _, _, err := readFileLimited(filepath.Join(dir, "nope.txt"), 16); err == nil {
		t.Error("reading a missing file should fail")
	}
}

func TestCountDirSize(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), make([]byte, 250), 0o644); err != nil {
		t.Fatal(err)
	}

	total := dirSize(dir)
	if total < 350 {
		t.Errorf("dirSize = %d bytes, want at least 350", total)
	}

	if got := min64(3, 7); got != 3 {
		t.Errorf("min64(3,7) = %d", got)
	}
	if got := min64(7, 3); got != 3 {
		t.Errorf("min64(7,3) = %d", got)
	}
}

func TestNewestModTime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	old := filepath.Join(dir, "old.txt")
	newest := filepath.Join(dir, "newest.txt")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newest, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Skipf("cannot set file times: %v", err)
	}

	got := newestModTime(dir)
	if got.IsZero() {
		t.Fatal("newestModTime returned the zero time")
	}
	if got.Before(past.Add(30 * time.Minute)) {
		t.Errorf("newestModTime = %v, which looks like the older file", got)
	}
}

// TestRequireAuthQueryTokenRejectsQueryByDefault proves the default posture:
// a token in the URL is NOT accepted outside the WebSocket upgrade path, where
// it would leak into access logs, browser history and Referer headers.
func TestRequireAuthQueryTokenRejectsQueryByDefault(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	// The header is always accepted.
	if resp := ts.get(token, "/api/v1/auth/me"); resp.Status != 200 {
		t.Fatalf("with a header: %d", resp.Status)
	}

	// The query string is not, on a normal route.
	resp := ts.get("", "/api/v1/auth/me?token="+token)
	if resp.Status == 200 {
		t.Error("SECURITY: a query-string token was accepted on a regular route")
	}
}

func TestNewSlogLogger(t *testing.T) {
	t.Parallel()

	// A nil writer must fall back rather than panic: the logger is on the
	// critical path of every request.
	log := NewSlogLogger(nil)
	if log == nil {
		t.Fatal("NewSlogLogger(nil) returned nil")
	}
	log.Debug("debug")
	log.Info("info")
	log.Warn("warn")
	log.Error("error")

	child := log.With("key", "value")
	if child == nil {
		t.Fatal("With returned nil")
	}
	child.Info("child")

	// The discard logger must also be usable and must swallow everything.
	d := NewDiscardLogger()
	d.Debug("d")
	d.Info("i")
	d.Warn("w")
	d.Error("e")
	if d.With("k", "v") == nil {
		t.Fatal("discard With returned nil")
	}
}

func TestHumanSize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
	}
	for _, tc := range cases {
		if got := humanSize(tc.in); got != tc.want {
			t.Errorf("humanSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := writeFileAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" {
		t.Errorf("content = %q", data)
	}

	// A second write replaces the content atomically.
	if err := writeFileAtomic(path, []byte("second"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic (second): %v", err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "second" {
		t.Errorf("content = %q after the second write", data)
	}

	// No temporary file is left behind: a stray .tmp next to a config file
	// would be picked up by the directory listing and by backups.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "out.txt" {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}

	// A missing parent directory is created rather than refused: provisioning
	// writes ServerSetting.json before any directory exists.
	nested := filepath.Join(dir, "missing", "x.txt")
	if err := writeFileAtomic(nested, []byte("x"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic should create missing parents: %v", err)
	}
	if data, err := os.ReadFile(nested); err != nil || string(data) != "x" {
		t.Errorf("nested write: data=%q err=%v", data, err)
	}
}

func TestWriteJSONAtomic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "doc.json")

	if err := writeJSONAtomic(path, map[string]any{"b": 2, "a": 1}); err != nil {
		t.Fatalf("writeJSONAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The output must be valid JSON with sorted keys and a trailing newline, so
	// a config file is diff-stable and POSIX-text-clean after a rewrite.
	want := "{\n  \"a\": 1,\n  \"b\": 2\n}\n"
	if string(data) != want {
		t.Errorf("unexpected JSON formatting:\ngot:  %q\nwant: %q", data, want)
	}

	// A value that cannot be marshalled is an error, and must not leave a
	// partially written file behind.
	if err := writeJSONAtomic(path, func() {}); err == nil {
		t.Error("marshalling a function should fail")
	}
	data, _ = os.ReadFile(path)
	if string(data) != want {
		t.Errorf("a failed write corrupted the existing file:\ngot: %q", data)
	}
}
