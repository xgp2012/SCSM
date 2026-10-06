package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInstanceCreateAndGet(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := &Instance{
		Name:        "alpha",
		Dir:         "/srv/scnetm/alpha",
		Port:        19132,
		Dotnet:      "/usr/bin/dotnet",
		ServerDL:    "Survivalcraft.dll",
		AutoStart:   true,
		AutoRestart: true,
		Memo:        "main world",
	}
	if err := s.Instances().Create(ctx, inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inst.ID == 0 {
		t.Fatal("Create must fill in ID")
	}

	// Defaults from §5.5 must be applied by the repository, so a partially
	// filled struct from an API handler produces a complete row.
	if inst.OwnerID != DefaultOwnerID {
		t.Errorf("OwnerID = %d, want %d (D3 default)", inst.OwnerID, DefaultOwnerID)
	}
	if inst.MaxRestart != DefaultMaxRestart {
		t.Errorf("MaxRestart = %d, want %d", inst.MaxRestart, DefaultMaxRestart)
	}
	if inst.StopTimeoutSec != DefaultStopTimeoutSec {
		t.Errorf("StopTimeoutSec = %d, want %d", inst.StopTimeoutSec, DefaultStopTimeoutSec)
	}
	if inst.Term != DefaultTerm {
		t.Errorf("Term = %q, want %q (D1 default)", inst.Term, DefaultTerm)
	}
	if inst.ColorMode != DefaultColorMode {
		t.Errorf("ColorMode = %q, want %q", inst.ColorMode, DefaultColorMode)
	}

	got, err := s.Instances().Get(ctx, inst.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "alpha" || got.Dir != "/srv/scnetm/alpha" || got.Port != 19132 {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if !got.AutoStart || !got.AutoRestart {
		t.Errorf("bool flags lost: %+v", got)
	}
	if got.Term != DefaultTerm || got.ColorMode != DefaultColorMode {
		t.Errorf("D1 columns lost: term=%q color=%q", got.Term, got.ColorMode)
	}
	if got.OwnerID != DefaultOwnerID {
		t.Errorf("D3 owner_id lost: %d", got.OwnerID)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt must be set")
	}

	byName, err := s.Instances().GetByName(ctx, "alpha")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if byName.ID != inst.ID {
		t.Errorf("GetByName id = %d, want %d", byName.ID, inst.ID)
	}
}

func TestInstanceDuplicateNameIsTyped(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	mustCreateInstance(t, s, "dup-name")

	err := s.Instances().Create(ctx, &Instance{Name: "dup-name", Dir: "/srv/x", Port: 19140})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate name = %v, want ErrDuplicate", err)
	}
	if !errors.Is(err, ErrDuplicate) {
		t.Fatal("errors.Is must match ErrDuplicate")
	}

	// Update is the other path that can collide.
	other := mustCreateInstance(t, s, "other")
	other.Name = "dup-name"
	if err := s.Instances().Update(ctx, other); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Update to a duplicate name = %v, want ErrDuplicate", err)
	}
}

func TestInstanceCreateValidation(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	cases := map[string]*Instance{
		"nil":        nil,
		"empty name": {Dir: "/srv/x", Port: 19132},
		"empty dir":  {Name: "n", Port: 19132},
		"port zero":  {Name: "p0", Dir: "/srv/x"},
		"port high":  {Name: "p1", Dir: "/srv/x", Port: 70000},
	}
	for name, inst := range cases {
		if err := s.Instances().Create(ctx, inst); !errors.Is(err, ErrInvalid) {
			t.Errorf("Create(%s) = %v, want ErrInvalid", name, err)
		}
	}
}

func TestInstanceNotFoundIsTyped(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	if _, err := s.Instances().Get(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.Instances().GetByName(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByName(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Instances().Update(ctx, &Instance{ID: 9999, Name: "x", Dir: "/d", Port: 19132}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Instances().Delete(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Instances().SetOwner(ctx, 9999, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetOwner(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.Instances().GetState(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetState(missing) = %v, want ErrNotFound", err)
	}
}

func TestInstanceUpdate(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "upd")
	inst.Port = 20000
	inst.Memo = "changed"
	inst.AutoStart = true
	inst.MaxRestart = 9
	inst.Term = "xterm"
	inst.ColorMode = ColorBasic
	inst.Dotnet = "/opt/dotnet/dotnet"
	inst.ServerDL = "Other.dll"

	if err := s.Instances().Update(ctx, inst); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Instances().Get(ctx, inst.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Port != 20000 || got.Memo != "changed" || !got.AutoStart || got.MaxRestart != 9 {
		t.Errorf("scalar columns not updated: %+v", got)
	}
	if got.Term != "xterm" || got.ColorMode != ColorBasic {
		t.Errorf("D1 columns not updated: term=%q color=%q", got.Term, got.ColorMode)
	}
	if got.Dotnet != "/opt/dotnet/dotnet" || got.ServerDL != "Other.dll" {
		t.Errorf("paths not updated: %+v", got)
	}
	// created_at is immutable across Update.
	if !got.CreatedAt.Equal(inst.CreatedAt) {
		t.Errorf("created_at changed: %v -> %v", inst.CreatedAt, got.CreatedAt)
	}
}

func TestInstanceListVariants(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	a := mustCreateInstance(t, s, "list-a")
	b := mustCreateInstance(t, s, "list-b")
	mustCreateInstance(t, s, "list-c")

	if err := s.Instances().SetAutoStart(ctx, b.ID, true); err != nil {
		t.Fatalf("SetAutoStart: %v", err)
	}
	if err := s.Instances().SetOwner(ctx, a.ID, 7); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}

	all, err := s.Instances().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List = %d instances, want 3", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Errorf("List not ordered by id")
		}
	}

	auto, err := s.Instances().ListAutoStart(ctx)
	if err != nil {
		t.Fatalf("ListAutoStart: %v", err)
	}
	if len(auto) != 1 || auto[0].ID != b.ID {
		t.Fatalf("ListAutoStart = %+v, want only %q", auto, b.Name)
	}
	// ListAutoStart is what boots the panel, so the flag must be readable.
	if !auto[0].AutoStart {
		t.Error("ListAutoStart returned an instance with AutoStart false")
	}

	owned, err := s.Instances().ListByOwner(ctx, 7)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(owned) != 1 || owned[0].ID != a.ID {
		t.Fatalf("ListByOwner(7) = %+v, want only %q", owned, a.Name)
	}

	// Everything else belongs to the default owner (D3).
	def, err := s.Instances().ListByOwner(ctx, DefaultOwnerID)
	if err != nil {
		t.Fatalf("ListByOwner(default): %v", err)
	}
	if len(def) != 2 {
		t.Errorf("ListByOwner(%d) = %d, want 2", DefaultOwnerID, len(def))
	}
}

// TestInstanceDeleteCascade is the required cascade test: every dependent table
// must be emptied for that instance in one transaction, and rows belonging to
// other instances must be untouched.
func TestInstanceDeleteCascade(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	victim := mustCreateInstance(t, s, "victim")
	bystander := mustCreateInstance(t, s, "bystander")

	for _, id := range []int64{victim.ID, bystander.ID} {
		if err := s.Instances().SaveState(ctx, InstanceState{
			InstanceID: id, State: StateRunning, PID: 1000 + id,
		}); err != nil {
			t.Fatalf("SaveState(%d): %v", id, err)
		}
		if err := s.LogEvents().InsertBatch(ctx, seedEvents(id, 5)); err != nil {
			t.Fatalf("InsertBatch(%d): %v", id, err)
		}
		if err := s.Backups().Create(ctx, &Backup{
			InstanceID: id, Path: "/b/x.zip", Kind: BackupManual, SizeBytes: 10,
		}); err != nil {
			t.Fatalf("Create backup(%d): %v", id, err)
		}
		if err := s.Jobs().Create(ctx, &Job{
			InstanceID: id, Type: JobBackup, Cron: "0 4 * * *", Enabled: true,
		}); err != nil {
			t.Fatalf("Create job(%d): %v", id, err)
		}
		if err := s.Users().Grant(ctx, 1, id, PermControl); err != nil {
			t.Fatalf("Grant(%d): %v", id, err)
		}
	}

	if err := s.Instances().Delete(ctx, victim.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// The instance itself is gone.
	if _, err := s.Instances().Get(ctx, victim.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("instance still present: %v", err)
	}
	// Every dependent table is cleaned for the victim.
	if _, err := s.Instances().GetState(ctx, victim.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("instance_state not cascaded: %v", err)
	}
	if events, _ := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &victim.ID}); len(events) != 0 {
		t.Errorf("log_events not cascaded: %d rows remain", len(events))
	}
	if backups, _ := s.Backups().ListByInstance(ctx, victim.ID); len(backups) != 0 {
		t.Errorf("backups not cascaded: %d rows remain", len(backups))
	}
	if jobs, _ := s.Jobs().ListByInstance(ctx, victim.ID); len(jobs) != 0 {
		t.Errorf("jobs not cascaded: %d rows remain", len(jobs))
	}
	if grants, _ := s.Users().ListForInstance(ctx, victim.ID); len(grants) != 0 {
		t.Errorf("user_instance_grant not cascaded: %d rows remain", len(grants))
	}

	// The bystander is untouched.
	if _, err := s.Instances().Get(ctx, bystander.ID); err != nil {
		t.Fatalf("bystander instance was deleted: %v", err)
	}
	if _, err := s.Instances().GetState(ctx, bystander.ID); err != nil {
		t.Errorf("bystander state was deleted: %v", err)
	}
	if events, _ := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &bystander.ID}); len(events) != 5 {
		t.Errorf("bystander log_events = %d, want 5", len(events))
	}
	if backups, _ := s.Backups().ListByInstance(ctx, bystander.ID); len(backups) != 1 {
		t.Errorf("bystander backups = %d, want 1", len(backups))
	}
	if jobs, _ := s.Jobs().ListByInstance(ctx, bystander.ID); len(jobs) != 1 {
		t.Errorf("bystander jobs = %d, want 1", len(jobs))
	}
	if grants, _ := s.Users().ListForInstance(ctx, bystander.ID); len(grants) != 1 {
		t.Errorf("bystander grants = %d, want 1", len(grants))
	}
}

// TestInstanceDeleteCascadeCounts pins the report the API shows the operator.
func TestInstanceDeleteCascadeCounts(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "counted")
	if err := s.Instances().SaveState(ctx, InstanceState{InstanceID: inst.ID, State: StateStopped}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := s.LogEvents().InsertBatch(ctx, seedEvents(inst.ID, 7)); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.Backups().Create(ctx, &Backup{
			InstanceID: inst.ID, Path: "/b/" + string(rune('a'+i)) + ".zip", Kind: BackupManual,
		}); err != nil {
			t.Fatalf("Create backup: %v", err)
		}
	}
	if err := s.Jobs().Create(ctx, &Job{InstanceID: inst.ID, Type: JobBackup, Cron: "* * * * *"}); err != nil {
		t.Fatalf("Create job: %v", err)
	}
	if err := s.Users().Grant(ctx, 1, inst.ID, PermRead); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	var counts map[string]int64
	err := s.WithTx(ctx, func(tx *Store) error {
		var err error
		counts, err = tx.Instances().DeleteCascadeCounts(ctx, inst.ID)
		return err
	})
	if err != nil {
		t.Fatalf("DeleteCascadeCounts: %v", err)
	}

	want := map[string]int64{
		"instance_state":      1,
		"log_events":          7,
		"backups":             3,
		"jobs":                1,
		"user_instance_grant": 1,
		"instances":           1,
	}
	for table, n := range want {
		if counts[table] != n {
			t.Errorf("counts[%s] = %d, want %d", table, counts[table], n)
		}
	}
}

// TestInstanceDeleteKeepingData is the alternative delete path offered by the
// UI: unregister the instance but keep its history and archives.
func TestInstanceDeleteKeepingData(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "keep-data")
	if err := s.Instances().SaveState(ctx, InstanceState{InstanceID: inst.ID, State: StateStopped}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := s.LogEvents().InsertBatch(ctx, seedEvents(inst.ID, 4)); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	if err := s.Backups().Create(ctx, &Backup{InstanceID: inst.ID, Path: "/b/keep.zip", Kind: BackupManual}); err != nil {
		t.Fatalf("Create backup: %v", err)
	}

	if err := s.Instances().DeleteKeepingData(ctx, inst.ID); err != nil {
		t.Fatalf("DeleteKeepingData: %v", err)
	}

	if _, err := s.Instances().Get(ctx, inst.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("instance must be gone: %v", err)
	}
	if _, err := s.Instances().GetState(ctx, inst.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("instance_state must be gone: %v", err)
	}
	if events, _ := s.LogEvents().Query(ctx, LogEventFilter{InstanceID: &inst.ID}); len(events) != 4 {
		t.Errorf("log_events must be kept, got %d", len(events))
	}
	if backups, _ := s.Backups().ListByInstance(ctx, inst.ID); len(backups) != 1 {
		t.Errorf("backups must be kept, got %d", len(backups))
	}
}

func TestInstanceSetAutoStart(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "autostart")
	if err := s.Instances().SetAutoStart(ctx, inst.ID, true); err != nil {
		t.Fatalf("SetAutoStart: %v", err)
	}
	got, _ := s.Instances().Get(ctx, inst.ID)
	if !got.AutoStart {
		t.Error("auto_start must be true")
	}
	if err := s.Instances().SetAutoStart(ctx, inst.ID, false); err != nil {
		t.Fatalf("SetAutoStart(false): %v", err)
	}
	got, _ = s.Instances().Get(ctx, inst.ID)
	if got.AutoStart {
		t.Error("auto_start must be false")
	}
}

// --- instance_state ------------------------------------------------------

// TestInstanceStateUpsertOnFirstWrite is the "no row yet" case: the supervisor
// calls SaveState for an instance that has never run, and a plain UPDATE would
// silently do nothing.
func TestInstanceStateUpsertOnFirstWrite(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "first-write")

	if _, err := s.Instances().GetState(ctx, inst.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no snapshot should exist yet, got %v", err)
	}

	started := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	exit := int64(137)
	if err := s.Instances().Upsert(ctx, InstanceState{
		InstanceID:    inst.ID,
		State:         StateRunning,
		PID:           4321,
		StartedAt:     &started,
		ExitCode:      &exit,
		LastError:     "",
		OnlinePlayers: 3,
	}); err != nil {
		t.Fatalf("Upsert (first write): %v", err)
	}

	st, err := s.Instances().GetState(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if st.State != StateRunning || st.PID != 4321 || st.OnlinePlayers != 3 {
		t.Errorf("first write mismatch: %+v", st)
	}
	if st.StartedAt == nil || !st.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", st.StartedAt, started)
	}
	if st.ExitCode == nil || *st.ExitCode != 137 {
		t.Errorf("ExitCode = %v, want 137", st.ExitCode)
	}
}

func TestInstanceStateUpsertOverwrites(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "upsert")

	started := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	stopped := started.Add(time.Hour)
	exit := int64(0)

	// Run 1.
	if err := s.Instances().SaveState(ctx, InstanceState{
		InstanceID: inst.ID, State: StateRunning, PID: 111, StartedAt: &started, OnlinePlayers: 2,
	}); err != nil {
		t.Fatalf("SaveState 1: %v", err)
	}
	// Run 2 overwrites in place; the primary key means there is still one row.
	if err := s.Instances().SaveState(ctx, InstanceState{
		InstanceID: inst.ID, State: StateStopped, PID: 0,
		StoppedAt: &stopped, ExitCode: &exit, LastError: "clean shutdown", OnlinePlayers: 0,
	}); err != nil {
		t.Fatalf("SaveState 2: %v", err)
	}

	st, err := s.Instances().GetState(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if st.State != StateStopped || st.PID != 0 {
		t.Errorf("state not overwritten: %+v", st)
	}
	if st.StartedAt != nil {
		t.Errorf("StartedAt must be overwritten to NULL, got %v", st.StartedAt)
	}
	if st.StoppedAt == nil || !st.StoppedAt.Equal(stopped) {
		t.Errorf("StoppedAt = %v, want %v", st.StoppedAt, stopped)
	}
	if st.LastError != "clean shutdown" {
		t.Errorf("LastError = %q", st.LastError)
	}

	all, err := s.Instances().ListAllStates(ctx)
	if err != nil {
		t.Fatalf("ListAllStates: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("upsert must not create a second row, got %d", len(all))
	}
}

func TestInstanceStateListAllAndDelete(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	a := mustCreateInstance(t, s, "st-a")
	b := mustCreateInstance(t, s, "st-b")

	if err := s.Instances().SaveState(ctx, InstanceState{InstanceID: a.ID, State: StateRunning}); err != nil {
		t.Fatalf("SaveState a: %v", err)
	}
	if err := s.Instances().SaveState(ctx, InstanceState{InstanceID: b.ID, State: StateCrashed}); err != nil {
		t.Fatalf("SaveState b: %v", err)
	}

	all, err := s.Instances().ListAllStates(ctx)
	if err != nil {
		t.Fatalf("ListAllStates: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAllStates = %d, want 2", len(all))
	}
	if all[0].InstanceID != a.ID || all[1].InstanceID != b.ID {
		t.Errorf("ListAllStates not ordered by instance id: %+v", all)
	}

	if err := s.Instances().DeleteState(ctx, a.ID); err != nil {
		t.Fatalf("DeleteState: %v", err)
	}
	if _, err := s.Instances().GetState(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("state must be gone: %v", err)
	}
	// Deleting a state that is not there is a no-op, not an error.
	if err := s.Instances().DeleteState(ctx, a.ID); err != nil {
		t.Errorf("DeleteState twice = %v, want nil", err)
	}
}

func TestInstanceStateValidation(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	if err := s.Instances().SaveState(ctx, InstanceState{State: StateRunning}); !errors.Is(err, ErrInvalid) {
		t.Errorf("SaveState without instance id = %v, want ErrInvalid", err)
	}
	// An empty state defaults rather than erroring: the supervisor may
	// legitimately write a snapshot before the state machine has decided.
	inst := mustCreateInstance(t, s, "default-state")
	if err := s.Instances().SaveState(ctx, InstanceState{InstanceID: inst.ID}); err != nil {
		t.Fatalf("SaveState with empty state: %v", err)
	}
	st, _ := s.Instances().GetState(ctx, inst.ID)
	if st.State != StateUnknown {
		t.Errorf("empty state = %q, want %q", st.State, StateUnknown)
	}
}
