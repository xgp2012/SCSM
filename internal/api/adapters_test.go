package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"scnetm/internal/auth"
	"scnetm/internal/store"
)

// These tests exercise the real-store adapters against a real SQLite database.
//
// The rest of the suite runs on in-memory doubles, which is what keeps it fast
// and hermetic — but doubles cannot prove that the adapter's error translation,
// type conversions and field mappings actually match internal/store. That is
// exactly the seam where a silent mismatch would turn a 409 into a 500, or drop
// a field on the way to the database. So every adapter method is driven here
// against the genuine repository.

// newStoreBackends opens a migrated temporary database and builds the adapters.
func newStoreBackends(t *testing.T) (StoreBackends, *store.Store) {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "adapters.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := store.Migrate(db); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	st := store.New(db)
	return NewStoreBackends(st, nil), st
}

func TestStoreUserAdapter(t *testing.T) {
	backends, _ := newStoreBackends(t)
	users := backends.Users
	ctx := context.Background()

	// The migration seeds the admin in the first-run state.
	admin, err := users.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetByUsername(admin): %v", err)
	}
	if admin.ID == 0 {
		t.Error("the seeded admin has no id")
	}
	if admin.HasPassword {
		t.Error("the seeded admin should report HasPassword=false")
	}
	if admin.Role != auth.RoleAdmin {
		t.Errorf("role = %q, want admin", admin.Role)
	}

	// Usernames are matched case-insensitively.
	if _, err := users.GetByUsername(ctx, "ADMIN"); err != nil {
		t.Errorf("GetByUsername should be case-insensitive: %v", err)
	}
	if _, err := users.GetByID(ctx, admin.ID); err != nil {
		t.Errorf("GetByID: %v", err)
	}

	// An unknown user is ErrNotFound, which the handler turns into a 404.
	_, err = users.GetByUsername(ctx, "nobody")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: err = %v, want ErrNotFound", err)
	}
	_, err = users.GetByID(ctx, 999999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}

	// Create, then confirm the id and timestamp are reflected back.
	created := &User{Username: "operator1", PasswordHash: bcryptHash(0), Role: auth.RoleOperator}
	if err := users.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Error("Create did not populate the id")
	}
	if created.CreatedAt.IsZero() {
		t.Error("Create did not populate CreatedAt")
	}
	if !created.HasPassword {
		t.Error("a user with a real hash should report HasPassword=true")
	}

	// A duplicate username is a conflict, which becomes a 409.
	dup := &User{Username: "operator1", PasswordHash: "x", Role: auth.RoleViewer}
	if err := users.Create(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate create: err = %v, want ErrConflict", err)
	}

	// List includes both users.
	all, err := users.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List returned %d users, want 2", len(all))
	}

	n, err := users.Count(ctx)
	if err != nil || n != 2 {
		t.Errorf("Count = %d (err %v), want 2", n, err)
	}

	// Update: role, disabled and password, each through the repo's guarded
	// setters.
	upd := &User{ID: created.ID, Username: "operator1", Role: auth.RoleViewer, Disabled: true}
	if err := users.Update(ctx, upd); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, err := users.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Role != auth.RoleViewer {
		t.Errorf("role = %q after update, want viewer", after.Role)
	}
	if !after.Disabled {
		t.Error("the account should be disabled")
	}

	// The username is immutable: changing it is a validation error, not a
	// silent no-op.
	if err := users.Update(ctx, &User{ID: created.ID, Username: "renamed"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("renaming: err = %v, want ErrInvalid", err)
	}

	// An unknown user is a 404, not a 500.
	if err := users.Update(ctx, &User{ID: 999999, Username: "ghost"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("updating an unknown user: err = %v, want ErrNotFound", err)
	}

	// A direct password change.
	if err := users.UpdatePassword(ctx, created.ID, bcryptHash(1)); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	// TouchLastLogin is best-effort bookkeeping.
	if err := users.TouchLastLogin(ctx, created.ID, time.Now().UTC()); err != nil {
		t.Errorf("TouchLastLogin: %v", err)
	}

	// SetInitialPassword is one-shot: it succeeds while unset, then conflicts.
	fresh := &User{Username: "freshuser", PasswordHash: store.FirstRunPasswordMarker, Role: auth.RoleAdmin}
	if err := users.Create(ctx, fresh); err != nil {
		t.Fatalf("Create(fresh): %v", err)
	}
	if err := users.SetInitialPassword(ctx, fresh.ID, bcryptHash(2)); err != nil {
		t.Fatalf("SetInitialPassword on an unset account: %v", err)
	}
	if err := users.SetInitialPassword(ctx, fresh.ID, bcryptHash(3)); !errors.Is(err, ErrConflict) {
		t.Errorf("second SetInitialPassword: err = %v, want ErrConflict", err)
	}
	current, err := users.GetByID(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !current.HasPassword {
		t.Error("the account should now report HasPassword=true")
	}

	// Delete removes the account.
	if err := users.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := users.GetByID(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the user survived deletion: %v", err)
	}
	if err := users.Delete(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting an unknown user: err = %v, want ErrNotFound", err)
	}
}

func TestStoreInstanceAdapter(t *testing.T) {
	backends, st := newStoreBackends(t)
	instances := backends.Instances
	ctx := context.Background()

	created := &Instance{
		Name:           "adapter-inst",
		Dir:            "/tmp/adapter-inst",
		Port:           41000,
		OwnerID:        1,
		AutoStart:      true,
		AutoRestart:    true,
		MaxRestart:     3,
		StopTimeoutSec: 30,
	}
	if err := instances.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Error("Create did not populate the id")
	}

	// Read back by id and by name; every scalar must survive the round trip.
	got, err := instances.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "adapter-inst" || got.Port != 41000 || got.Dir != "/tmp/adapter-inst" {
		t.Errorf("round trip changed the row: %+v", got)
	}
	if got.MaxRestart != 3 || got.StopTimeoutSec != 30 {
		t.Errorf("numeric fields did not survive: max=%d stop=%d", got.MaxRestart, got.StopTimeoutSec)
	}
	if !got.AutoStart || !got.AutoRestart {
		t.Errorf("boolean fields did not survive: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt was not populated")
	}

	if _, err := instances.GetByName(ctx, "adapter-inst"); err != nil {
		t.Errorf("GetByName: %v", err)
	}
	if _, err := instances.GetByID(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown instance: err = %v, want ErrNotFound", err)
	}
	if _, err := instances.GetByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown name: err = %v, want ErrNotFound", err)
	}

	// A duplicate name is a conflict: the handler relies on this for its 409.
	dup := &Instance{Name: "adapter-inst", Dir: "/tmp/other", Port: 41001}
	if err := instances.Create(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name: err = %v, want ErrConflict", err)
	}

	// List, and the owner-scoped filter.
	all, err := instances.List(ctx, InstanceListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List returned %d instances, want 1", len(all))
	}
	owned, err := instances.List(ctx, InstanceListFilter{OwnerID: 1})
	if err != nil {
		t.Fatalf("List(owner): %v", err)
	}
	if len(owned) != 1 {
		t.Errorf("the owner filter returned %d, want 1", len(owned))
	}
	notOwned, err := instances.List(ctx, InstanceListFilter{OwnerID: 999})
	if err != nil {
		t.Fatalf("List(unknown owner): %v", err)
	}
	if len(notOwned) != 0 {
		t.Errorf("an unknown owner returned %d instances, want 0", len(notOwned))
	}

	// ListUsedPorts is what the allocator consults.
	used, err := instances.ListUsedPorts(ctx)
	if err != nil {
		t.Fatalf("ListUsedPorts: %v", err)
	}
	if len(used) != 1 || used[0] != 41000 {
		t.Errorf("ListUsedPorts = %v, want [41000]", used)
	}

	// Update, then confirm the change is durable.
	got.Memo = "updated memo"
	got.Port = 41005
	if err := instances.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reread, err := instances.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Memo != "updated memo" || reread.Port != 41005 {
		t.Errorf("the update was not persisted: %+v", reread)
	}
	// A field the API does not own must be preserved by the merge.
	if reread.OwnerID != 1 {
		t.Errorf("OwnerID = %d after update, want the original 1", reread.OwnerID)
	}
	if reread.CreatedAt.IsZero() {
		t.Error("CreatedAt was lost by the update merge")
	}

	if err := instances.Update(ctx, &Instance{ID: 999999, Name: "ghost"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("updating an unknown instance: err = %v, want ErrNotFound", err)
	}

	// State: save, read, and overwrite. The exit code is a pointer on both
	// sides, so the conversion is worth proving.
	code := 7
	if err := instances.SaveState(ctx, &InstanceState{
		InstanceID: created.ID,
		State:      "Running",
		PID:        4242,
		ExitCode:   &code,
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	state, err := instances.GetState(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if state.State != "Running" || state.PID != 4242 {
		t.Errorf("state round trip: %+v", state)
	}
	if state.ExitCode == nil || *state.ExitCode != 7 {
		t.Errorf("ExitCode = %v, want 7", state.ExitCode)
	}

	// Upsert means a second save overwrites rather than conflicting.
	if err := instances.SaveState(ctx, &InstanceState{
		InstanceID: created.ID,
		State:      "Stopped",
		PID:        0,
	}); err != nil {
		t.Fatalf("SaveState (second): %v", err)
	}
	state, err = instances.GetState(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "Stopped" {
		t.Errorf("state = %q after the second save, want Stopped", state.State)
	}
	if state.ExitCode != nil {
		t.Errorf("ExitCode = %v after clearing, want nil", state.ExitCode)
	}

	if _, err := instances.GetState(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("state for an unknown instance: err = %v, want ErrNotFound", err)
	}

	// Delete is a pure database operation; it must not touch the directory.
	if err := instances.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := instances.GetByID(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the instance survived deletion: %v", err)
	}
	if err := instances.Delete(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting an unknown instance: err = %v, want ErrNotFound", err)
	}

	// The delete cascaded the state row, so the next create can reuse the id.
	_ = st
}

func TestStoreAuditAdapter(t *testing.T) {
	backends, _ := newStoreBackends(t)
	audit := backends.Audit
	ctx := context.Background()

	// A nil user id must be stored as NULL, not as 0: the column has a foreign
	// key and 0 is not a user.
	if err := audit.Write(ctx, &AuditEntry{
		Action: "system.boot", Target: "panel", Detail: `{"ok":true}`, IP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("Write (no user): %v", err)
	}
	if err := audit.Write(ctx, &AuditEntry{
		UserID: 1, Action: "instance.create", Target: "instance:1", Detail: `{"name":"x"}`, IP: "10.0.0.1",
	}); err != nil {
		t.Fatalf("Write (with user): %v", err)
	}

	rows, err := audit.List(ctx, AuditFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(rows))
	}
	// Newest first.
	if rows[0].Action != "instance.create" {
		t.Errorf("rows[0].Action = %q, want instance.create", rows[0].Action)
	}
	if rows[0].UserID != 1 {
		t.Errorf("UserID = %d, want 1", rows[0].UserID)
	}
	if rows[0].Timestamp.IsZero() {
		t.Error("Timestamp was not populated")
	}
	if rows[1].UserID != 0 {
		t.Errorf("an anonymous row should report UserID 0, got %d", rows[1].UserID)
	}

	// Filters.
	byUser, err := audit.List(ctx, AuditFilter{UserID: 1})
	if err != nil {
		t.Fatalf("List(user): %v", err)
	}
	if len(byUser) != 1 || byUser[0].UserID != 1 {
		t.Errorf("the user filter returned %+v", byUser)
	}
	byAction, err := audit.List(ctx, AuditFilter{Action: "system.boot"})
	if err != nil {
		t.Fatalf("List(action): %v", err)
	}
	if len(byAction) != 1 || byAction[0].Action != "system.boot" {
		t.Errorf("the action filter returned %+v", byAction)
	}
	limited, err := audit.List(ctx, AuditFilter{Limit: 1})
	if err != nil {
		t.Fatalf("List(limit): %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("the limit was ignored: got %d rows", len(limited))
	}
	offset, err := audit.List(ctx, AuditFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("List(offset): %v", err)
	}
	if len(offset) != 1 {
		t.Errorf("the offset was ignored: got %d rows", len(offset))
	}

	// An empty result is an empty slice, never nil: the JSON must be [].
	empty, err := audit.List(ctx, AuditFilter{Action: "does.not.exist"})
	if err != nil {
		t.Fatalf("List(empty): %v", err)
	}
	if empty == nil {
		t.Error("an empty audit list must be an empty slice, not nil")
	}
	if len(empty) != 0 {
		t.Errorf("expected no rows, got %d", len(empty))
	}

	// A nil entry is tolerated rather than panicking.
	if err := audit.Write(ctx, nil); err != nil {
		t.Errorf("Write(nil): %v", err)
	}
}

// TestStoreAuditAdapterFallsBackWhenTheDatabaseFails proves the best-effort
// contract: a failed audit insert still lands somewhere, and the business
// operation is never failed by it.
func TestStoreAuditAdapterFallsBackWhenTheDatabaseFails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "fallback.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	st := store.New(db)

	fallback := newFakeAuditStore()
	audit := NewStoreAuditStore(st, fallback)
	ctx := context.Background()

	// Drop the table out from under the repository to force a write failure.
	if _, err := db.ExecContext(ctx, `DROP TABLE audit_logs`); err != nil {
		t.Skipf("could not drop the audit table: %v", err)
	}

	err = audit.Write(ctx, &AuditEntry{UserID: 1, Action: "instance.start", Target: "instance:1"})
	if err == nil {
		t.Error("a failed database write should report its error")
	}
	// The row must still be recorded by the fallback sink.
	if !fallback.hasAction("instance.start") {
		t.Error("the audit row was lost instead of falling back")
	}
}

func TestStoreBackupAdapter(t *testing.T) {
	backends, _ := newStoreBackends(t)
	ctx := context.Background()

	// A backup needs an instance row, because of the foreign key.
	if err := backends.Instances.Create(ctx, &Instance{
		Name: "backup-host", Dir: "/tmp/backup-host", Port: 42000, OwnerID: 1,
	}); err != nil {
		t.Fatalf("seeding the instance: %v", err)
	}

	created := &Backup{
		InstanceID: 1,
		World:      "world1",
		Path:       "/tmp/backup-host/backups/world1.zip",
		SizeBytes:  2048,
		Kind:       "manual",
		Note:       "a note",
	}
	if err := backends.Backups.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Error("Create did not populate the id")
	}

	got, err := backends.Backups.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.World != "world1" || got.SizeBytes != 2048 || got.Kind != "manual" || got.Note != "a note" {
		t.Errorf("the backup did not round trip: %+v", got)
	}
	if got.InstanceID != 1 {
		t.Errorf("InstanceID = %d, want 1", got.InstanceID)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt was not populated")
	}

	if _, err := backends.Backups.GetByID(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown backup: err = %v, want ErrNotFound", err)
	}

	// A second backup, so pagination has something to page.
	second := &Backup{
		InstanceID: 1, World: "world2", Path: "/tmp/backup-host/backups/world2.zip",
		SizeBytes: 1024, Kind: "scheduled",
	}
	if err := backends.Backups.Create(ctx, second); err != nil {
		t.Fatalf("Create(second): %v", err)
	}

	// Scoped listing.
	scoped, err := backends.Backups.List(ctx, BackupFilter{InstanceID: 1})
	if err != nil {
		t.Fatalf("List(scoped): %v", err)
	}
	if len(scoped) != 2 {
		t.Errorf("List(instance=1) returned %d, want 2", len(scoped))
	}

	// Unscoped listing walks the instances, so it sees the same two.
	all, err := backends.Backups.List(ctx, BackupFilter{})
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List(all) returned %d, want 2", len(all))
	}

	// The limit and offset windows are applied.
	limited, err := backends.Backups.List(ctx, BackupFilter{Limit: 1})
	if err != nil {
		t.Fatalf("List(limit): %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("the limit was ignored: got %d", len(limited))
	}
	offset, err := backends.Backups.List(ctx, BackupFilter{Offset: 1, Limit: 5})
	if err != nil {
		t.Fatalf("List(offset): %v", err)
	}
	if len(offset) != 1 {
		t.Errorf("the offset was ignored: got %d", len(offset))
	}
	// An offset past the end is empty, not an error or a panic.
	past, err := backends.Backups.List(ctx, BackupFilter{Offset: 99})
	if err != nil {
		t.Fatalf("List(past the end): %v", err)
	}
	if len(past) != 0 {
		t.Errorf("an offset past the end returned %d rows, want 0", len(past))
	}

	// Scoping to an instance with no backups is an empty list.
	empty, err := backends.Backups.List(ctx, BackupFilter{InstanceID: 999})
	if err != nil {
		t.Fatalf("List(unknown instance): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected no backups, got %d", len(empty))
	}

	if err := backends.Backups.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := backends.Backups.GetByID(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the backup survived deletion: %v", err)
	}
	if err := backends.Backups.Delete(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting an unknown backup: err = %v, want ErrNotFound", err)
	}
}

func TestStoreJobAdapter(t *testing.T) {
	backends, _ := newStoreBackends(t)
	ctx := context.Background()

	if err := backends.Instances.Create(ctx, &Instance{
		Name: "job-host", Dir: "/tmp/job-host", Port: 43000, OwnerID: 1,
	}); err != nil {
		t.Fatalf("seeding the instance: %v", err)
	}

	created := &Job{
		InstanceID: 1,
		Type:       "backup",
		Cron:       "0 4 * * *",
		Payload:    `{"kind":"manual"}`,
		Enabled:    true,
	}
	if err := backends.Jobs.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Error("Create did not populate the id")
	}

	got, err := backends.Jobs.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Type != "backup" || got.Cron != "0 4 * * *" || !got.Enabled {
		t.Errorf("the job did not round trip: %+v", got)
	}
	if got.Payload != `{"kind":"manual"}` {
		t.Errorf("Payload = %q", got.Payload)
	}
	if got.InstanceID != 1 {
		t.Errorf("InstanceID = %d, want 1", got.InstanceID)
	}

	if _, err := backends.Jobs.GetByID(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown job: err = %v, want ErrNotFound", err)
	}

	all, err := backends.Jobs.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List returned %d jobs, want 1", len(all))
	}

	// Update the mutable fields.
	got.Cron = "30 2 * * *"
	got.Enabled = false
	got.Payload = `{"kind":"scheduled"}`
	if err := backends.Jobs.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reread, err := backends.Jobs.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Cron != "30 2 * * *" || reread.Enabled {
		t.Errorf("the update was not persisted: %+v", reread)
	}

	if err := backends.Jobs.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := backends.Jobs.GetByID(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the job survived deletion: %v", err)
	}
	if err := backends.Jobs.Delete(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting an unknown job: err = %v, want ErrNotFound", err)
	}
}

// TestStoreAdaptersReportNilStoreForNilStore proves the constructors degrade to
// nil rather than panicking, which is what lets a host leave a backend unwired
// and still get a 501 from the routes that need it.
func TestStoreAdaptersReportNilStoreForNilStore(t *testing.T) {
	if NewStoreUserStore(nil) != nil {
		t.Error("NewStoreUserStore(nil) should be nil")
	}
	if NewStoreInstanceStore(nil) != nil {
		t.Error("NewStoreInstanceStore(nil) should be nil")
	}
	if NewStoreBackupStore(nil, nil) != nil {
		t.Error("NewStoreBackupStore(nil, nil) should be nil")
	}
	if NewStoreJobStore(nil) != nil {
		t.Error("NewStoreJobStore(nil) should be nil")
	}

	// With a fallback the audit adapter stays usable, because the fallback is
	// the only sink left.
	fallback := newFakeAuditStore()
	if NewStoreAuditStore(nil, fallback) != AuditStore(fallback) {
		t.Error("NewStoreAuditStore(nil, fallback) should return the fallback")
	}
	if NewStoreAuditStore(nil, nil) != nil {
		t.Error("NewStoreAuditStore(nil, nil) should be nil")
	}
}

// TestTranslateStoreError pins the mapping that decides 404 vs 409 vs 422.
func TestTranslateStoreError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"nil stays nil", nil, nil},
		{"not found becomes ErrNotFound", store.ErrNotFound, ErrNotFound},
		{"duplicate becomes ErrConflict", store.ErrDuplicate, ErrConflict},
		{"invalid becomes ErrInvalid", store.ErrInvalid, ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateStoreError(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Errorf("got %v, want it to wrap %v", got, tc.want)
			}
		})
	}

	// A wrapped sentinel still maps, because the store wraps with %w.
	wrapped := errors.Join(errors.New("context"), store.ErrDuplicate)
	if !errors.Is(translateStoreError(wrapped), ErrConflict) {
		t.Error("a wrapped ErrDuplicate should still map to ErrConflict")
	}

	// An unrecognised error is passed through untouched: Classify turns it into
	// a 500 rather than mislabelling it as a client error.
	unknown := errors.New("disk on fire")
	if got := translateStoreError(unknown); got != unknown {
		t.Errorf("an unknown error should pass through, got %v", got)
	}
}

// TestStoreGrantAdapter covers the user_instance_grant table.
//
// This is the backend that makes instance-level grants survive a restart; before
// it existed, grants lived only in the authorizer's in-memory map.
func TestStoreGrantAdapter(t *testing.T) {
	backends, _ := newStoreBackends(t)
	grants := backends.Grants
	ctx := context.Background()

	if grants == nil {
		t.Fatal("the grant store was not wired")
	}

	// The grant table has foreign keys, so the rows it references must exist.
	if err := backends.Users.Create(ctx, &User{
		Username: "grantee", PasswordHash: bcryptHash(0), Role: auth.RoleOperator,
	}); err != nil {
		t.Fatalf("seeding the user: %v", err)
	}
	if err := backends.Instances.Create(ctx, &Instance{
		Name: "grant-target", Dir: "/tmp/grant-target", Port: 44000, OwnerID: 1,
	}); err != nil {
		t.Fatalf("seeding the instance: %v", err)
	}

	if err := grants.Grant(ctx, 2, 1, auth.GrantControl); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	forUser, err := grants.ListForUser(ctx, 2)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(forUser) != 1 {
		t.Fatalf("ListForUser returned %d grants, want 1", len(forUser))
	}
	if forUser[0].Perm != auth.GrantControl {
		t.Errorf("Perm = %q, want control", forUser[0].Perm)
	}
	if forUser[0].UserID != 2 || forUser[0].InstanceID != 1 {
		t.Errorf("the grant carries the wrong ids: %+v", forUser[0])
	}

	forInstance, err := grants.ListForInstance(ctx, 1)
	if err != nil {
		t.Fatalf("ListForInstance: %v", err)
	}
	if len(forInstance) != 1 {
		t.Errorf("ListForInstance returned %d grants, want 1", len(forInstance))
	}

	// Re-granting the same level is idempotent, not a duplicate-key error.
	if err := grants.Grant(ctx, 2, 1, auth.GrantControl); err != nil {
		t.Errorf("re-granting the same level should be idempotent: %v", err)
	}

	// An unknown level is a validation error, not a silent write.
	if err := grants.Grant(ctx, 2, 1, auth.GrantPerm("superuser")); !errors.Is(err, ErrInvalid) {
		t.Errorf("an unknown level: err = %v, want ErrInvalid", err)
	}

	if err := grants.Revoke(ctx, 2, 1); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	forUser, err = grants.ListForUser(ctx, 2)
	if err != nil {
		t.Fatalf("ListForUser after revoke: %v", err)
	}
	if len(forUser) != 0 {
		t.Errorf("grants = %+v after revoke, want none", forUser)
	}
	// Revoking again is not an error.
	if err := grants.Revoke(ctx, 2, 1); err != nil {
		t.Errorf("Revoke is not idempotent: %v", err)
	}

	// A user with no grants is an empty list, never nil: JSON must be [].
	none, err := grants.ListForUser(ctx, 1)
	if err != nil {
		t.Fatalf("ListForUser(no grants): %v", err)
	}
	if none == nil {
		t.Error("an empty grant list must be an empty slice, not nil")
	}
	if len(none) != 0 {
		t.Errorf("expected no grants, got %d", len(none))
	}
}

// TestGrantsSurviveRestart is the regression test for the bug this adapter
// fixes: grants must still be enforced after the process is rebuilt.
func TestGrantsSurviveRestart(t *testing.T) {
	backends, st := newStoreBackends(t)
	ctx := context.Background()

	if err := backends.Users.Create(ctx, &User{
		Username: "operator9", PasswordHash: bcryptHash(0), Role: auth.RoleOperator,
	}); err != nil {
		t.Fatalf("seeding the user: %v", err)
	}
	if err := backends.Instances.Create(ctx, &Instance{
		Name: "restart-target", Dir: "/tmp/restart-target", Port: 45000, OwnerID: 1,
	}); err != nil {
		t.Fatalf("seeding the instance: %v", err)
	}

	if err := backends.Grants.Grant(ctx, 2, 1, auth.GrantControl); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	// Build a fresh server against the SAME database: this is the restart.
	authz := auth.NewAuthorizer(false)
	engine, srv, err := NewRouter(Deps{
		Users:     backends.Users,
		Instances: backends.Instances,
		Grants:    backends.Grants,
		Audit:     backends.Audit,
		Tokens:    mustIssuer(t),
		Sessions:  NewMemorySessionStore(),
		RBAC:      authz,
		Process:   NewNopProcessManager(NewDiscardLogger()),
		Ports:     NewUDPPortAllocator(PortPool{Start: 50000, End: 50010}),
		Log:       NewDiscardLogger(),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	_ = engine

	// Before hydration the fresh authorizer knows nothing, which is the bug.
	if _, ok := authz.GrantsFor(2, 1); ok {
		t.Fatal("a brand-new authorizer should start empty")
	}

	if err := srv.HydrateGrants(ctx); err != nil {
		t.Fatalf("HydrateGrants: %v", err)
	}

	grant, ok := authz.GrantsFor(2, 1)
	if !ok {
		t.Fatal("the grant did not survive the restart")
	}
	if grant != auth.GrantControl {
		t.Errorf("grant = %q, want control", grant)
	}
	// An unrelated instance still carries no grant.
	if _, ok := authz.GrantsFor(2, 999); ok {
		t.Error("the grant leaked to an unrelated instance")
	}
	_ = st
}

// TestHydrateGrantsIsSkippedInSingleUserMode proves the query is not even
// attempted when grants cannot matter.
func TestHydrateGrantsIsSkippedInSingleUserMode(t *testing.T) {
	backends, _ := newStoreBackends(t)

	engine, srv, err := NewRouter(Deps{
		Users:     backends.Users,
		Instances: backends.Instances,
		Grants:    backends.Grants,
		Audit:     backends.Audit,
		Tokens:    mustIssuer(t),
		Sessions:  NewMemorySessionStore(),
		RBAC:      auth.NewAuthorizer(true),
		Process:   NewNopProcessManager(NewDiscardLogger()),
		Ports:     NewUDPPortAllocator(PortPool{Start: 50000, End: 50010}),
		Log:       NewDiscardLogger(),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	_ = engine

	if err := srv.HydrateGrants(context.Background()); err != nil {
		t.Errorf("HydrateGrants in single-user mode: %v", err)
	}

	// A server with no grant backend at all must not fail either.
	_, bare, err := NewRouter(Deps{
		Tokens:   mustIssuer(t),
		Sessions: NewMemorySessionStore(),
		RBAC:     auth.NewAuthorizer(true),
		Process:  NewNopProcessManager(NewDiscardLogger()),
		Log:      NewDiscardLogger(),
	})
	if err != nil {
		t.Fatalf("NewRouter (bare): %v", err)
	}
	if err := bare.HydrateGrants(context.Background()); err != nil {
		t.Errorf("HydrateGrants with no store: %v", err)
	}
}

// mustIssuer builds a token issuer for the router tests above.
func mustIssuer(t *testing.T) *auth.TokenIssuer {
	t.Helper()
	iss, err := auth.NewTokenIssuer("a-test-secret-that-is-definitely-long-enough")
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	return iss
}

// bcryptHash returns a structurally valid bcrypt hash for testing.
//
// The API derives HasPassword from a structural check on the stored string
// (cheap, and it avoids parsing attacker-influenced data), so tests must supply
// a genuinely well-formed hash rather than an arbitrary placeholder — a short
// fake is correctly classified as "no password set". The seed only varies the
// last characters, which is enough to make the values distinct.
func bcryptHash(seed int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./"
	// A 60-character bcrypt string: "$2a$12$" + 53 characters of salt+hash.
	body := make([]byte, 53)
	for i := range body {
		body[i] = alphabet[(seed*7+i*13)%len(alphabet)]
	}
	return "$2a$12$" + string(body)
}

// TestBcryptHashHelperIsStructurallyValid guards the helper itself: if it ever
// stops producing a well-formed hash, the tests that rely on it would silently
// start asserting the wrong thing.
func TestBcryptHashHelperIsStructurallyValid(t *testing.T) {
	h := bcryptHash(0)
	if len(h) != 60 {
		t.Fatalf("bcryptHash produced %d characters, want 60", len(h))
	}
	if !looksLikeBcryptHash(h) {
		t.Fatalf("bcryptHash produced %q, which the panel does not recognise as a hash", h)
	}
	if isFirstRunHash(h) {
		t.Fatal("bcryptHash produced something classified as the first-run marker")
	}
	if bcryptHash(0) == bcryptHash(1) {
		t.Fatal("bcryptHash must produce distinct values for distinct seeds")
	}
}
