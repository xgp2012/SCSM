package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestEndToEndOnDiskDatabase is the "does this actually work against a real
// database file" test required by the T5 definition of done.
//
// Unlike the per-repository tests, it drives the exact sequence cmd/scnetm
// performs — store.Open -> store.Migrate -> store.New — against a real file on
// disk, then exercises every repository through the public API, including the
// cross-cutting behaviours (transaction rollback, instance cascade, retention)
// that individual repository tests only cover in isolation.
//
// It deliberately does not use openRepoTestDB: the point is to run the
// production entry points, including migrate.go's connection setup and v1
// migration, and to observe the stored bytes.
func TestEndToEndOnDiskDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "panel.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(db); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	version, err := SchemaVersion(db)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != LatestSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", version, LatestSchemaVersion())
	}

	st := New(db)
	defer st.Close()

	// --- users -----------------------------------------------------------
	admin, err := st.Users().GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID(1): %v", err)
	}
	if admin.Username != "admin" || admin.Role != RoleAdmin || !admin.NeedsPasswordSetup() {
		t.Fatalf("seeded admin wrong: %+v", admin)
	}

	if err := st.Users().UpdatePassword(ctx, 1, "$2a$10$endtoend"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	admin, err = st.Users().GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID after password: %v", err)
	}
	if admin.NeedsPasswordSetup() {
		t.Error("admin still needs password setup after UpdatePassword")
	}

	// --- instances -------------------------------------------------------
	inst := &Instance{
		Name: "e2e-world", Dir: filepath.Join(t.TempDir(), "world"), Port: 19132,
		Dotnet: "/usr/bin/dotnet", ServerDL: "Survivalcraft.dll",
		AutoStart: true, Memo: "end to end",
	}
	if err := st.Instances().Create(ctx, inst); err != nil {
		t.Fatalf("Create instance: %v", err)
	}
	if inst.OwnerID != DefaultOwnerID || inst.Term != DefaultTerm || inst.ColorMode != DefaultColorMode {
		t.Fatalf("D1/D3 defaults not applied: %+v", inst)
	}

	auto, err := st.Instances().ListAutoStart(ctx)
	if err != nil {
		t.Fatalf("ListAutoStart: %v", err)
	}
	if len(auto) != 1 || auto[0].ID != inst.ID {
		t.Fatalf("ListAutoStart = %+v, want just the new instance", auto)
	}

	// --- instance_state --------------------------------------------------
	started := time.Date(2026, 10, 6, 3, 23, 53, 0, time.UTC)
	if err := st.Instances().SaveState(ctx, InstanceState{
		InstanceID: inst.ID, State: StateRunning, PID: 4242,
		StartedAt: &started, OnlinePlayers: 3,
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	snapshot, err := st.Instances().GetState(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if snapshot.State != StateRunning || snapshot.PID != 4242 || snapshot.OnlinePlayers != 3 {
		t.Fatalf("state snapshot wrong: %+v", snapshot)
	}

	// --- log events ------------------------------------------------------
	batch := make([]LogEvent, 250)
	for i := range batch {
		batch[i] = LogEvent{
			InstanceID: inst.ID,
			TS:         started.Add(time.Duration(i) * time.Second),
			Level:      []string{LevelInfo, LevelWarn, LevelError}[i%3],
			Event:      fmt.Sprintf("e2e-%d", i),
			Payload:    fmt.Sprintf(`{"i":%d}`, i),
		}
	}
	if err := st.LogEvents().InsertBatch(ctx, batch); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	indexed, err := st.LogEvents().Count(ctx, LogEventFilter{InstanceID: &inst.ID})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if indexed != 250 {
		t.Fatalf("indexed %d events, want 250", indexed)
	}

	// --- audit -----------------------------------------------------------
	if err := st.Audit().Log(ctx, &admin.ID, "instance.create",
		fmt.Sprintf("instance:%d", inst.ID), "end to end", "127.0.0.1"); err != nil {
		t.Fatalf("Audit.Log: %v", err)
	}
	audit, err := st.Audit().ListByInstance(ctx, inst.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	if len(audit) != 1 || audit[0].Action != "instance.create" {
		t.Fatalf("audit entries wrong: %+v", audit)
	}
	if audit[0].TS.Location() != time.UTC {
		t.Errorf("audit timestamp not UTC: %v", audit[0].TS.Location())
	}

	// --- backups + retention ---------------------------------------------
	for i := 0; i < 5; i++ {
		if err := st.Backups().Create(ctx, &Backup{
			InstanceID: inst.ID,
			Path:       filepath.Join(t.TempDir(), fmt.Sprintf("pre-%d.zip", i)),
			Kind:       BackupPreStart,
			SizeBytes:  int64(1000 * (i + 1)),
			CreatedAt:  started.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("Create backup %d: %v", i, err)
		}
	}
	total, err := st.Backups().TotalSize(ctx, inst.ID)
	if err != nil {
		t.Fatalf("TotalSize: %v", err)
	}
	if total != 15000 {
		t.Errorf("TotalSize = %d, want 15000", total)
	}

	pruned, err := st.Backups().PruneOldest(ctx, inst.ID, BackupPreStart, 3)
	if err != nil {
		t.Fatalf("PruneOldest: %v", err)
	}
	if len(pruned) != 2 {
		t.Fatalf("PruneOldest deleted %d, want 2", len(pruned))
	}
	kept, err := st.Backups().ListOldestFirst(ctx, inst.ID)
	if err != nil {
		t.Fatalf("ListOldestFirst: %v", err)
	}
	if len(kept) != 3 {
		t.Fatalf("%d backups remain, want 3", len(kept))
	}
	if kept[0].SizeBytes != 3000 {
		t.Errorf("the newest 3 should be sizes 3000/4000/5000, oldest kept = %d", kept[0].SizeBytes)
	}

	// --- jobs ------------------------------------------------------------
	job := &Job{InstanceID: inst.ID, Type: JobBackup, Cron: "0 4 * * *", Enabled: true}
	if err := st.Jobs().Create(ctx, job); err != nil {
		t.Fatalf("Create job: %v", err)
	}
	if err := st.Jobs().RecordRun(ctx, job.ID, "ok: 12 files", started); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	storedJob, err := st.Jobs().Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}
	if storedJob.LastRun == nil || !storedJob.LastRun.Equal(started) {
		t.Fatalf("LastRun = %v, want %v", storedJob.LastRun, started)
	}
	if storedJob.LastResult != "ok: 12 files" {
		t.Errorf("LastResult = %q", storedJob.LastResult)
	}

	// --- timestamp fidelity, observed as raw bytes -----------------------
	quirkTS := time.Date(2026, 10, 6, 3, 23, 53, 0, time.UTC)
	if err := st.LogEvents().Insert(ctx, &LogEvent{
		InstanceID: 99, TS: quirkTS, Event: "quirk-check",
	}); err != nil {
		t.Fatalf("Insert quirk event: %v", err)
	}
	var raw string
	if err := db.QueryRowContext(ctx,
		`SELECT ts FROM log_events WHERE event = 'quirk-check'`).Scan(&raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if raw != "2026-10-06T03:23:53Z" {
		t.Errorf("stored timestamp = %q, want RFC3339 UTC text %q", raw, "2026-10-06T03:23:53Z")
	}
	other := int64(99)
	scanned, err := st.LogEvents().Query(ctx, LogEventFilter{InstanceID: &other})
	if err != nil {
		t.Fatalf("Query quirk event: %v", err)
	}
	if len(scanned) != 1 || !scanned[0].TS.Equal(quirkTS) || scanned[0].TS.Location() != time.UTC {
		t.Fatalf("timestamp round trip failed: %+v", scanned)
	}

	// --- transactional rollback ------------------------------------------
	sentinel := errors.New("deliberate rollback")
	err = st.WithTx(ctx, func(tx *Store) error {
		if e := tx.Instances().Create(ctx, &Instance{
			Name: "should-vanish", Dir: "/tmp/vanished", Port: 19133,
		}); e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx error = %v, want the sentinel unchanged", err)
	}
	if _, err := st.Instances().GetByName(ctx, "should-vanish"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rolled-back instance survived on disk: %v", err)
	}

	// --- cascade delete ---------------------------------------------------
	if err := st.Instances().Delete(ctx, inst.ID); err != nil {
		t.Fatalf("Delete instance: %v", err)
	}
	if _, err := st.Instances().Get(ctx, inst.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("instance survived delete: %v", err)
	}
	if _, err := st.Instances().GetState(ctx, inst.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("instance_state survived delete: %v", err)
	}
	if n, _ := st.LogEvents().Count(ctx, LogEventFilter{InstanceID: &inst.ID}); n != 0 {
		t.Errorf("%d log_events survived delete", n)
	}
	if rows, _ := st.Backups().ListByInstance(ctx, inst.ID); len(rows) != 0 {
		t.Errorf("%d backups survived delete", len(rows))
	}
	if rows, _ := st.Jobs().ListByInstance(ctx, inst.ID); len(rows) != 0 {
		t.Errorf("%d jobs survived delete", len(rows))
	}

	// --- the database is still usable after all of that -------------------
	if err := st.Ping(ctx); err != nil {
		t.Fatalf("Ping after the full exercise: %v", err)
	}
	// ...and survives a reopen, i.e. it was really written to disk.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	reopenedStore := New(reopened)
	reopenedAdmin, err := reopenedStore.Users().GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID after reopen: %v", err)
	}
	if reopenedAdmin.PasswordHash != "$2a$10$endtoend" {
		t.Errorf("password change did not persist: %q", reopenedAdmin.PasswordHash)
	}
}
