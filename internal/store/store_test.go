package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreAccessorsReturnRepositories(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)

	if s.Users() == nil || s.Instances() == nil || s.Backups() == nil ||
		s.Jobs() == nil || s.Audit() == nil || s.LogEvents() == nil {
		t.Fatal("every accessor must return a non-nil repository")
	}
	// Accessors are stable, so repeated calls inside a request do not
	// allocate a new repository each time.
	if s.Users() != s.Users() {
		t.Error("Users() must be cached")
	}
	if s.DB() == nil {
		t.Error("DB() must return the handle")
	}
	if s.InTx() || s.Tx() != nil {
		t.Error("a non-transactional Store must report InTx() == false")
	}
}

func TestStoreNilSafety(t *testing.T) {
	t.Parallel()
	var s *Store

	if s.DB() != nil {
		t.Error("nil Store DB() must be nil")
	}
	if s.Close() != nil {
		t.Error("nil Store Close() must be a no-op")
	}
	if err := s.Ping(context.Background()); !errors.Is(err, ErrNoStore) {
		t.Errorf("nil Store Ping = %v, want ErrNoStore", err)
	}
	err := s.WithTx(context.Background(), func(*Store) error { return nil })
	if !errors.Is(err, ErrNoStore) {
		t.Errorf("nil Store WithTx = %v, want ErrNoStore", err)
	}
}

func TestWithTxCommits(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	var id int64
	err := s.WithTx(ctx, func(tx *Store) error {
		if !tx.InTx() || tx.Tx() == nil {
			return errors.New("callback must receive a transactional view")
		}
		inst := &Instance{Name: "tx-commit", Dir: "/srv/tx", Port: 19133}
		if err := tx.Instances().Create(ctx, inst); err != nil {
			return err
		}
		id = inst.ID
		return tx.Instances().SaveState(ctx, InstanceState{
			InstanceID: inst.ID, State: StateRunning, PID: 4242,
		})
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	inst, err := s.Instances().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after commit: %v", err)
	}
	if inst.Name != "tx-commit" {
		t.Errorf("name = %q", inst.Name)
	}
	st, err := s.Instances().GetState(ctx, id)
	if err != nil {
		t.Fatalf("GetState after commit: %v", err)
	}
	if st.PID != 4242 {
		t.Errorf("pid = %d, want 4242", st.PID)
	}
}

// TestWithTxRollsBackOnError is the transactionality guarantee the instance
// cascade and the last-admin guard both rely on.
func TestWithTxRollsBackOnError(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	sentinel := errors.New("deliberate failure")
	var createdID int64

	err := s.WithTx(ctx, func(tx *Store) error {
		inst := &Instance{Name: "tx-rollback", Dir: "/srv/rb", Port: 19134}
		if err := tx.Instances().Create(ctx, inst); err != nil {
			return err
		}
		createdID = inst.ID

		// Visible inside the transaction...
		if _, err := tx.Instances().Get(ctx, inst.ID); err != nil {
			return fmt.Errorf("row must be visible inside the tx: %w", err)
		}
		// ...then fail, which must undo everything.
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx error = %v, want the sentinel unchanged", err)
	}
	if createdID == 0 {
		t.Fatal("the callback should have created a row")
	}

	if _, err := s.Instances().Get(ctx, createdID); !errors.Is(err, ErrNotFound) {
		t.Errorf("rolled-back instance is still readable: %v", err)
	}
	list, err := s.Instances().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, inst := range list {
		if inst.Name == "tx-rollback" {
			t.Error("rolled-back instance appears in List")
		}
	}
}

// TestWithTxSurvivesSentinelErrors checks that a sentinel raised inside a
// transaction is still matchable by the API layer after the rollback.
//
// It also pins the join semantics: SetRole opens a transaction of its own, but
// inside WithTx it must join the caller's rather than fail as a nested one
// ("WithTx cannot be nested" would break every handler that wraps a
// repository call in a bigger unit of work).
func TestWithTxSurvivesSentinelErrors(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx *Store) error {
		return tx.Users().SetRole(ctx, 1, RoleViewer)
	})
	if !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("SetRole inside WithTx = %v, want ErrLastAdmin", err)
	}
	u, gerr := s.Users().GetByID(ctx, 1)
	if gerr != nil {
		t.Fatalf("GetByID: %v", gerr)
	}
	if u.Role != RoleAdmin {
		t.Errorf("inner rollback must also undo the outer transaction's work; role = %q", u.Role)
	}
}

// TestRepositoryMethodJoinsOuterTx is the positive half of the same rule: work
// a nested repository method performs must be rolled back with the outer
// transaction, not committed early by its own.
func TestRepositoryMethodJoinsOuterTx(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	// A second admin lets SetRole succeed rather than tripping ErrLastAdmin,
	// so this exercises a *successful* nested write inside a failing caller.
	if err := s.Users().Create(ctx, &User{Username: "admin2", PasswordHash: "h", Role: RoleAdmin}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := s.WithTx(ctx, func(tx *Store) error {
		if err := tx.Users().SetRole(ctx, 1, RoleViewer); err != nil {
			return err
		}
		// Observably applied inside the transaction.
		u, err := tx.Users().GetByID(ctx, 1)
		if err != nil {
			return err
		}
		if u.Role != RoleViewer {
			t.Fatalf("inner write not visible inside its own transaction: %+v", u)
		}
		return errors.New("abort the outer transaction")
	})
	if err == nil {
		t.Fatal("expected the outer transaction to fail")
	}

	u, err := s.Users().GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if u.Role != RoleAdmin {
		t.Errorf("a nested repository method committed independently; role = %q, want %q", u.Role, RoleAdmin)
	}
}

// TestWithTxNestedRejected pins the deliberate refusal to nest, rather than a
// silent join that would commit early.
func TestWithTxNestedRejected(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx *Store) error {
		return tx.WithTx(ctx, func(*Store) error { return nil })
	})
	if err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("nested WithTx = %v, want a nesting error", err)
	}
}

func TestWithTxNilCallback(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	if err := s.WithTx(context.Background(), nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("WithTx(nil) = %v, want ErrInvalid", err)
	}
}

func TestWithTxPanicRollsBack(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("panic must propagate out of WithTx")
			}
		}()
		_ = s.WithTx(ctx, func(tx *Store) error {
			if err := tx.Instances().Create(ctx, &Instance{Name: "tx-panic", Dir: "/srv/p", Port: 19135}); err != nil {
				return err
			}
			panic("boom")
		})
	}()

	if _, err := s.Instances().GetByName(ctx, "tx-panic"); !errors.Is(err, ErrNotFound) {
		t.Errorf("panicking transaction left a row behind: %v", err)
	}
}

// TestWithTxRollsBackInstanceCascade forces the cascade to fail halfway and
// asserts the instance survives, i.e. the delete is all-or-nothing.
func TestWithTxRollsBackInstanceCascade(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "cascade-rb")
	if err := s.Instances().SaveState(ctx, InstanceState{InstanceID: inst.ID, State: StateStopped}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := s.Backups().Create(ctx, &Backup{InstanceID: inst.ID, Path: "/b/1.zip", Kind: BackupManual}); err != nil {
		t.Fatalf("Create backup: %v", err)
	}

	// Delete some dependent rows, then abort: everything must come back.
	err := s.WithTx(ctx, func(tx *Store) error {
		if _, err := tx.Instances().DeleteCascadeCounts(ctx, inst.ID); err != nil {
			return err
		}
		if _, err := tx.Instances().Get(ctx, inst.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("instance should be gone inside the tx, got %v", err)
		}
		return errors.New("abort")
	})
	if err == nil {
		t.Fatal("expected the transaction to fail")
	}

	if _, err := s.Instances().Get(ctx, inst.ID); err != nil {
		t.Fatalf("instance must survive a rolled-back cascade: %v", err)
	}
	if _, err := s.Instances().GetState(ctx, inst.ID); err != nil {
		t.Errorf("instance_state must survive a rolled-back cascade: %v", err)
	}
	if _, err := s.Backups().GetByPath(ctx, "/b/1.zip"); err != nil {
		t.Errorf("backup must survive a rolled-back cascade: %v", err)
	}
}

// TestConcurrentReads is a smoke test for the single-connection pool: parallel
// readers must not deadlock or see "database is locked".
func TestConcurrentReads(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "concurrent")
	if err := s.LogEvents().InsertBatch(ctx, seedEvents(inst.ID, 50)); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Instances().List(ctx); err != nil {
				errCh <- err
				return
			}
			id := inst.ID
			if _, err := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &id, Limit: 10}); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent access failed: %v", err)
	}
}

// --- timestamp handling --------------------------------------------------

// TestTimestampRoundTripUTC pins the storage convention: every timestamp is
// written as RFC3339 UTC text and read back as the same instant in UTC.
//
// This is the test that would fail if the repository layer ever started handing
// time.Time straight to the driver (which stores a different layout) or
// compared a text bound against an integer column.
func TestTimestampRoundTripUTC(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "ts-roundtrip")

	// A non-UTC input must come back as the equivalent UTC instant, not as
	// the original offset.
	zone := time.FixedZone("UTC+8", 8*3600)
	local := time.Date(2026, 7, 8, 9, 10, 11, 0, zone)
	wantUTC := local.UTC()

	if err := s.LogEvents().Insert(ctx, &LogEvent{
		InstanceID: inst.ID, TS: local, Level: LevelInfo, Event: "roundtrip",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Read the raw stored text to prove the convention, not just the scan.
	var raw string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT ts FROM log_events WHERE instance_id = ?`, inst.ID).Scan(&raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if raw != wantUTC.Format(time.RFC3339) {
		t.Errorf("stored text = %q, want %q", raw, wantUTC.Format(time.RFC3339))
	}
	if !strings.HasSuffix(raw, "Z") {
		t.Errorf("stored timestamp %q must end in Z (UTC)", raw)
	}

	events, err := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &inst.ID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Query returned %d events, want 1", len(events))
	}
	got := events[0].TS
	if !got.Equal(wantUTC) {
		t.Errorf("round trip = %v, want %v", got, wantUTC)
	}
	if got.Location() != time.UTC {
		t.Errorf("returned time must be UTC, got %v", got.Location())
	}
}

func TestTimestampZeroAndNilHandling(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "ts-zero")

	// A zero TS is defaulted rather than stored as NULL, because a log event
	// with no time is useless and a NULL would break ordering.
	if err := s.LogEvents().Insert(ctx, &LogEvent{InstanceID: inst.ID, Event: "no-ts"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	events, err := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &inst.ID})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 || events[0].TS.IsZero() {
		t.Fatalf("zero TS must be defaulted, got %+v", events)
	}
	if events[0].Level != LevelInfo {
		t.Errorf("default level = %q, want %q", events[0].Level, LevelInfo)
	}

	// A NULL nullable timestamp scans to nil, not to the zero time value.
	if err := s.Jobs().Create(ctx, &Job{
		InstanceID: inst.ID, Type: JobBackup, Cron: "0 4 * * *", Enabled: true,
	}); err != nil {
		t.Fatalf("Create job: %v", err)
	}
	jobs, err := s.Jobs().List(ctx)
	if err != nil {
		t.Fatalf("List jobs: %v", err)
	}
	if jobs[0].LastRun != nil {
		t.Errorf("never-run job must have nil LastRun, got %v", jobs[0].LastRun)
	}

	// instance_state with no started_at/stopped_at/exit_code.
	if err := s.Instances().SaveState(ctx, InstanceState{InstanceID: inst.ID, State: StateStopped}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	st, err := s.Instances().GetState(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if st.StartedAt != nil || st.StoppedAt != nil || st.ExitCode != nil {
		t.Errorf("NULL columns must scan to nil: %+v", st)
	}
	if st.PID != 0 || st.OnlinePlayers != 0 {
		t.Errorf("absent integers must be zero: %+v", st)
	}
}

// TestTimestampOrderingAcrossReaders proves the RFC3339 text ordering matches
// chronological ordering, which is what PruneOldest depends on.
func TestTimestampOrdering(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "ts-order")

	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var want []time.Time
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Hour)
		want = append(want, ts)
		if err := s.LogEvents().Insert(ctx, &LogEvent{
			InstanceID: inst.ID, TS: ts, Event: fmt.Sprintf("e%d", i),
		}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	// Newest first.
	events, err := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &inst.ID, NewestFirst: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("got %d events, want 5", len(events))
	}
	for i, e := range events {
		wantTS := want[len(want)-1-i]
		if !e.TS.Equal(wantTS) {
			t.Errorf("event %d TS = %v, want %v (descending order broken)", i, e.TS, wantTS)
		}
	}

	// A time-range filter is inclusive at both ends and uses text comparison.
	from, to := want[1], want[3]
	filtered, err := s.LogEvents().Query(ctx, LogEventFilter{
		InstanceID: &inst.ID, From: &from, To: &to,
	})
	if err != nil {
		t.Fatalf("Query range: %v", err)
	}
	if len(filtered) != 3 {
		t.Fatalf("range query returned %d events, want 3: %+v", len(filtered), filtered)
	}
}

func TestNullTimeScanner(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	// NULL -> invalid.
	var nt NullTime
	if err := s.DB().QueryRowContext(ctx, `SELECT NULL`).Scan(&nt); err != nil {
		t.Fatalf("scan NULL: %v", err)
	}
	if nt.Valid {
		t.Error("NULL must scan as invalid NullTime")
	}

	// RFC3339 text -> valid UTC.
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := s.DB().QueryRowContext(ctx, `SELECT ?`, at.Format(time.RFC3339)).Scan(&nt); err != nil {
		t.Fatalf("scan text: %v", err)
	}
	if !nt.Valid || !nt.Time.Equal(at) {
		t.Errorf("NullTime = %+v, want %v", nt, at)
	}
}

// TestScanTimeToleratesLegacyFormats documents the tolerance the helper
// deliberately has: rows written by an older build or by hand must still scan.
func TestScanTimeToleratesLegacyFormats(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	legacy := []string{
		at.Format(time.RFC3339),          // canonical
		"2026-05-06 07:08:09",            // SQLite CURRENT_TIMESTAMP
		"2026-05-06T07:08:09",            // no zone
		"2026-05-06T07:08:09.123456789Z", // sub-second
		"2026-05-06 07:08:09+00:00",
	}
	for _, raw := range legacy {
		var st ScanTime
		if err := s.DB().QueryRowContext(ctx, `SELECT ?`, raw).Scan(&st); err != nil {
			t.Errorf("scan %q: %v", raw, err)
			continue
		}
		if !st.Time.Equal(at) {
			t.Errorf("scan %q = %v, want %v", raw, st.Time, at)
		}
	}

	// Garbage is an error, never a silent zero: retention logic depends on it.
	var st ScanTime
	if err := s.DB().QueryRowContext(ctx, `SELECT 'not a timestamp'`).Scan(&st); err == nil {
		t.Error("unparsable timestamp must be an error")
	}
}

// TestBooleansAreIntegers proves booleans survive the 0/1 round trip in both
// directions, including the DEFAULT-0 case where the column was never written.
func TestBooleansAreIntegers(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := &Instance{
		Name: "bool-inst", Dir: "/srv/bool", Port: 19136,
		AutoStart: true, AutoRestart: false,
	}
	if err := s.Instances().Create(ctx, inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var autoStart, autoRestart int64
	if err := s.DB().QueryRowContext(ctx,
		`SELECT auto_start, auto_restart FROM instances WHERE id = ?`, inst.ID).
		Scan(&autoStart, &autoRestart); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if autoStart != 1 {
		t.Errorf("auto_start stored as %d, want 1", autoStart)
	}
	if autoRestart != 0 {
		t.Errorf("auto_restart stored as %d, want 0", autoRestart)
	}

	got, err := s.Instances().Get(ctx, inst.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.AutoStart || got.AutoRestart {
		t.Errorf("bools round-tripped wrong: %+v", got)
	}
}
