package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// seedEvents builds n deterministic events for one instance. Timestamps step by
// one second so ordering is unambiguous.
func seedEvents(instanceID int64, n int) []LogEvent {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	out := make([]LogEvent, 0, n)
	for i := 0; i < n; i++ {
		level := LevelInfo
		switch i % 3 {
		case 1:
			level = LevelWarn
		case 2:
			level = LevelError
		}
		out = append(out, LogEvent{
			InstanceID: instanceID,
			TS:         base.Add(time.Duration(i) * time.Second),
			Level:      level,
			Event:      fmt.Sprintf("event-%d", i),
			Payload:    fmt.Sprintf(`{"i":%d}`, i),
		})
	}
	return out
}

// TestLogEventsInsertBatchLarge is the hot-path test: a 1000-event flush must
// land in one transaction with every row intact and its returned ID filled in.
func TestLogEventsInsertBatchLarge(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "batch")

	const n = 1000
	events := seedEvents(inst.ID, n)

	start := time.Now()
	if err := s.LogEvents().InsertBatch(ctx, events); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	elapsed := time.Since(start)

	count, err := s.LogEvents().Count(ctx, LogEventFilter{InstanceID: &inst.ID})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != n {
		t.Fatalf("count = %d, want %d", count, n)
	}

	// The batch fills in IDs, which lets the caller correlate rows without a
	// second query.
	if events[0].ID == 0 || events[n-1].ID == 0 {
		t.Errorf("batch must fill in IDs, got first=%d last=%d", events[0].ID, events[n-1].ID)
	}
	if events[n-1].ID != events[0].ID+int64(n)-1 {
		t.Errorf("IDs are not contiguous: %d .. %d", events[0].ID, events[n-1].ID)
	}

	// Spot-check a row end to end.
	got, err := s.LogEvents().Query(ctx, LogEventFilter{
		InstanceID: &inst.ID, Event: "event-500",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Query by event returned %d rows, want 1", len(got))
	}
	if !got[0].TS.Equal(events[500].TS) {
		t.Errorf("ts = %v, want %v", got[0].TS, events[500].TS)
	}
	if got[0].Payload != `{"i":500}` {
		t.Errorf("payload = %q", got[0].Payload)
	}
	if got[0].Level != LevelError {
		t.Errorf("level = %q, want %q", got[0].Level, LevelError)
	}

	t.Logf("InsertBatch of %d events took %v", n, elapsed)
}

func TestLogEventsInsertBatchEdgeCases(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "batch-edge")

	// Empty and nil batches are no-ops, not errors: the supervisor flushes a
	// buffer that may legitimately be empty.
	if err := s.LogEvents().InsertBatch(ctx, nil); err != nil {
		t.Errorf("InsertBatch(nil) = %v, want nil", err)
	}
	if err := s.LogEvents().InsertBatch(ctx, []LogEvent{}); err != nil {
		t.Errorf("InsertBatch(empty) = %v, want nil", err)
	}

	// Defaults are applied per element.
	batch := []LogEvent{
		{InstanceID: inst.ID, Event: "defaulted"},
		{InstanceID: inst.ID, TS: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Level: "", Event: "also"},
	}
	if err := s.LogEvents().InsertBatch(ctx, batch); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	if batch[0].TS.IsZero() || batch[0].Level != LevelInfo {
		t.Errorf("defaults not applied: %+v", batch[0])
	}
	if batch[1].Level != LevelInfo {
		t.Errorf("empty level not defaulted: %+v", batch[1])
	}

	// A bad element fails the whole batch atomically: nothing is written.
	bad := []LogEvent{
		{InstanceID: inst.ID, Event: "good"},
		{InstanceID: 0, Event: "bad"},
	}
	if err := s.LogEvents().InsertBatch(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("InsertBatch with a bad element = %v, want ErrInvalid", err)
	}
	got, err := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &inst.ID, Event: "good"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a failed batch must write nothing, found %d rows", len(got))
	}
}

// TestLogEventsInsertBatchInsideTx proves the batch participates in an outer
// transaction instead of opening (and committing) its own.
func TestLogEventsInsertBatchInsideTx(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "batch-tx")

	err := s.WithTx(ctx, func(tx *Store) error {
		if err := tx.LogEvents().InsertBatch(ctx, seedEvents(inst.ID, 10)); err != nil {
			return err
		}
		return errors.New("abort")
	})
	if err == nil {
		t.Fatal("expected the transaction to fail")
	}

	count, cerr := s.LogEvents().Count(ctx, LogEventFilter{InstanceID: &inst.ID})
	if cerr != nil {
		t.Fatalf("Count: %v", cerr)
	}
	if count != 0 {
		t.Errorf("events from a rolled-back batch survived: %d", count)
	}
}

func TestLogEventsInsertSingle(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "single")

	if err := s.LogEvents().Insert(ctx, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("Insert(nil) = %v, want ErrInvalid", err)
	}
	if err := s.LogEvents().Insert(ctx, &LogEvent{Event: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Insert without instance = %v, want ErrInvalid", err)
	}

	e := &LogEvent{
		InstanceID: inst.ID,
		TS:         time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Level:      LevelWarn,
		Event:      "player_join",
		Payload:    `{"name":"steve"}`,
	}
	if err := s.LogEvents().Insert(ctx, e); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if e.ID == 0 {
		t.Error("Insert must fill in ID")
	}

	got, err := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &inst.ID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].Event != "player_join" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestLogEventsQueryFilters(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	a := mustCreateInstance(t, s, "q-a")
	b := mustCreateInstance(t, s, "q-b")

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// 12 events on a, 4 on b, one per hour on a.
	for i := 0; i < 12; i++ {
		if err := s.LogEvents().Insert(ctx, &LogEvent{
			InstanceID: a.ID,
			TS:         base.Add(time.Duration(i) * time.Hour),
			Level:      []string{LevelInfo, LevelWarn, LevelError}[i%3],
			Event:      []string{"tick", "join", "leave"}[i%3],
		}); err != nil {
			t.Fatalf("Insert a[%d]: %v", i, err)
		}
	}
	if err := s.LogEvents().InsertBatch(ctx, seedEvents(b.ID, 4)); err != nil {
		t.Fatalf("InsertBatch b: %v", err)
	}

	cases := []struct {
		name   string
		filter LogEventFilter
		want   int
	}{
		{"by instance", LogEventFilter{InstanceID: &a.ID}, 12},
		{"by other instance", LogEventFilter{InstanceID: &b.ID}, 4},
		{"by level", LogEventFilter{InstanceID: &a.ID, Level: LevelWarn}, 4},
		{"by event", LogEventFilter{InstanceID: &a.ID, Event: "join"}, 4},
		{
			"by level set",
			LogEventFilter{InstanceID: &a.ID, Levels: []string{LevelWarn, LevelError}},
			8,
		},
		{
			"by event set",
			LogEventFilter{InstanceID: &a.ID, Events: []string{"join", "leave"}},
			8,
		},
		{
			"from only",
			LogEventFilter{InstanceID: &a.ID, From: timePtr(base.Add(6 * time.Hour))},
			6,
		},
		{
			"to only",
			LogEventFilter{InstanceID: &a.ID, To: timePtr(base.Add(5 * time.Hour))},
			6,
		},
		{
			"range inclusive",
			LogEventFilter{
				InstanceID: &a.ID,
				From:       timePtr(base.Add(2 * time.Hour)),
				To:         timePtr(base.Add(4 * time.Hour)),
			},
			3,
		},
		{
			"range plus level",
			LogEventFilter{
				InstanceID: &a.ID,
				From:       timePtr(base),
				To:         timePtr(base.Add(5 * time.Hour)),
				Level:      LevelInfo,
			},
			2,
		},
		{"no match", LogEventFilter{InstanceID: &a.ID, Event: "nope"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.LogEvents().Query(ctx, tc.filter)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("Query = %d rows, want %d", len(got), tc.want)
			}
			for _, e := range got {
				if tc.filter.InstanceID != nil && e.InstanceID != *tc.filter.InstanceID {
					t.Errorf("row from instance %d leaked into a filter for %d", e.InstanceID, *tc.filter.InstanceID)
				}
				if tc.filter.Level != "" && e.Level != tc.filter.Level {
					t.Errorf("row with level %q leaked into a filter for %q", e.Level, tc.filter.Level)
				}
			}
		})
	}

	// Count ignores Limit/Offset, which is what the API uses for paging.
	n, err := s.LogEvents().Count(ctx, LogEventFilter{InstanceID: &a.ID, Limit: 2})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 12 {
		t.Errorf("Count = %d, want 12 (Limit must not affect it)", n)
	}
}

func TestLogEventsPagination(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "paging")
	events := seedEvents(inst.ID, 25)
	if err := s.LogEvents().InsertBatch(ctx, events); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	// Ascending pages must partition the set without overlap or gaps.
	var seen []string
	for offset := 0; offset < 25; offset += 10 {
		page, err := s.LogEvents().Query(ctx, LogEventFilter{
			InstanceID: &inst.ID, Limit: 10, Offset: offset,
		})
		if err != nil {
			t.Fatalf("Query offset %d: %v", offset, err)
		}
		for _, e := range page {
			seen = append(seen, e.Event)
		}
	}
	if len(seen) != 25 {
		t.Fatalf("paging returned %d events, want 25", len(seen))
	}
	for i, name := range seen {
		if want := fmt.Sprintf("event-%d", i); name != want {
			t.Fatalf("page %d = %q, want %q (pagination order broken)", i, name, want)
		}
	}

	// NewestFirst gives the most recent events first, which is the "tail the
	// log" query.
	latest, err := s.LogEvents().Query(ctx, LogEventFilter{
		InstanceID: &inst.ID, NewestFirst: true, Limit: 3,
	})
	if err != nil {
		t.Fatalf("Query newest: %v", err)
	}
	if len(latest) != 3 {
		t.Fatalf("newest query returned %d, want 3", len(latest))
	}
	for i, want := range []string{"event-24", "event-23", "event-22"} {
		if latest[i].Event != want {
			t.Errorf("newest[%d] = %q, want %q", i, latest[i].Event, want)
		}
	}

	// RecentByInstance is the same query spelled out.
	recent, err := s.LogEvents().RecentByInstance(ctx, inst.ID, 2)
	if err != nil {
		t.Fatalf("RecentByInstance: %v", err)
	}
	if len(recent) != 2 || recent[0].Event != "event-24" {
		t.Errorf("RecentByInstance = %+v", recent)
	}

	// A huge limit is capped rather than rejected.
	if _, err := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &inst.ID, Limit: 1 << 30}); err != nil {
		t.Errorf("oversized limit: %v", err)
	}
}

func TestLogEventsPrune(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	a := mustCreateInstance(t, s, "prune-a")
	b := mustCreateInstance(t, s, "prune-b")

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if err := s.LogEvents().Insert(ctx, &LogEvent{
			InstanceID: a.ID, TS: base.Add(time.Duration(i) * time.Hour), Event: fmt.Sprintf("e%d", i),
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	// b's events are all newer than the cutoff, so the prune must leave them
	// alone: Prune is age-based and applies to every instance at once.
	if err := s.LogEvents().InsertBatch(ctx, []LogEvent{
		{InstanceID: b.ID, TS: base.AddDate(0, 0, 30), Event: "b-new"},
		{InstanceID: b.ID, TS: base.AddDate(0, 0, 31), Event: "b-newer"},
		{InstanceID: b.ID, TS: base.AddDate(0, 0, 32), Event: "b-newest"},
	}); err != nil {
		t.Fatalf("InsertBatch b: %v", err)
	}

	// Keep everything from hour 5 onwards: 5 of a's 10 rows go.
	cutoff := base.Add(5 * time.Hour)
	n, err := s.LogEvents().Prune(ctx, cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 5 {
		t.Errorf("Prune removed %d rows, want 5", n)
	}

	remaining, err := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &a.ID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(remaining) != 5 {
		t.Fatalf("%d rows remain for a, want 5", len(remaining))
	}
	if !remaining[0].TS.Equal(cutoff) {
		t.Errorf("prune must be exclusive of the cutoff: oldest kept is %v", remaining[0].TS)
	}

	// b's newer rows survive untouched.
	if n, _ = s.LogEvents().Count(ctx, LogEventFilter{InstanceID: &b.ID}); n != 3 {
		t.Errorf("b has %d rows, want 3 (all newer than the cutoff)", n)
	}

	// Pruning again removes nothing.
	if n, err = s.LogEvents().Prune(ctx, cutoff); err != nil || n != 0 {
		t.Errorf("second Prune = (%d, %v), want (0, nil)", n, err)
	}
}

func TestLogEventsPruneInstanceBefore(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	a := mustCreateInstance(t, s, "pi-a")
	b := mustCreateInstance(t, s, "pi-b")

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []int64{a.ID, b.ID} {
		if err := s.LogEvents().InsertBatch(ctx, []LogEvent{
			{InstanceID: id, TS: old, Event: "old"},
			{InstanceID: id, TS: old.AddDate(1, 0, 0), Event: "new"},
		}); err != nil {
			t.Fatalf("InsertBatch(%d): %v", id, err)
		}
	}

	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	n, err := s.LogEvents().PruneInstanceBefore(ctx, a.ID, cutoff)
	if err != nil {
		t.Fatalf("PruneInstanceBefore: %v", err)
	}
	if n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	if n, _ = s.LogEvents().Count(ctx, LogEventFilter{InstanceID: &a.ID}); n != 1 {
		t.Errorf("a has %d rows, want 1", n)
	}
	// b is untouched.
	if n, _ = s.LogEvents().Count(ctx, LogEventFilter{InstanceID: &b.ID}); n != 2 {
		t.Errorf("b has %d rows, want 2 (must not be pruned)", n)
	}
}

// --- helpers -------------------------------------------------------------

func timePtr(t time.Time) *time.Time { return &t }
