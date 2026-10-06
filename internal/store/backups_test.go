package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBackupCreateAndGet(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "bk-create")
	created := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)

	b := &Backup{
		InstanceID: inst.ID,
		World:      "MyWorld",
		Path:       "/var/backups/scnetm/w1.zip",
		SizeBytes:  123456,
		SHA256:     "abc123",
		Kind:       BackupPreStart,
		CreatedAt:  created,
		Note:       "before upgrade",
	}
	if err := s.Backups().Create(ctx, b); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if b.ID == 0 {
		t.Fatal("Create must fill in ID")
	}

	got, err := s.Backups().Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.World != "MyWorld" || got.SizeBytes != 123456 || got.SHA256 != "abc123" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Kind != BackupPreStart {
		t.Errorf("kind = %q, want %q", got.Kind, BackupPreStart)
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}
	if got.Note != "before upgrade" {
		t.Errorf("Note = %q", got.Note)
	}

	byPath, err := s.Backups().GetByPath(ctx, "/var/backups/scnetm/w1.zip")
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if byPath.ID != b.ID {
		t.Errorf("GetByPath id = %d, want %d", byPath.ID, b.ID)
	}

	bySHA, err := s.Backups().GetBySHA256(ctx, inst.ID, "abc123")
	if err != nil {
		t.Fatalf("GetBySHA256: %v", err)
	}
	if bySHA.ID != b.ID {
		t.Errorf("GetBySHA256 id = %d, want %d", bySHA.ID, b.ID)
	}
}

func TestBackupDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "bk-val")

	// kind defaults to manual; created_at defaults to now.
	b := &Backup{InstanceID: inst.ID, Path: "/b/default.zip", SizeBytes: 1}
	if err := s.Backups().Create(ctx, b); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if b.Kind != BackupManual {
		t.Errorf("kind = %q, want %q", b.Kind, BackupManual)
	}
	if b.CreatedAt.IsZero() {
		t.Error("CreatedAt must default to now")
	}

	cases := map[string]*Backup{
		"nil":          nil,
		"no instance":  {Path: "/b/x.zip", Kind: BackupManual},
		"no path":      {InstanceID: inst.ID, Kind: BackupManual},
		"unknown kind": {InstanceID: inst.ID, Path: "/b/x.zip", Kind: "hourly"},
	}
	for name, bad := range cases {
		if err := s.Backups().Create(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Create(%s) = %v, want ErrInvalid", name, err)
		}
	}
}

func TestBackupNotFoundIsTyped(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	if _, err := s.Backups().Get(ctx, 4242); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.Backups().GetByPath(ctx, "/nope.zip"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByPath(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.Backups().GetBySHA256(ctx, 1, "deadbeef"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBySHA256(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Backups().Delete(ctx, 4242); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.Backups().GetBySHA256(ctx, 1, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("GetBySHA256(\"\") = %v, want ErrInvalid", err)
	}
}

func TestBackupListByInstanceAndKind(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	a := mustCreateInstance(t, s, "bk-a")
	b := mustCreateInstance(t, s, "bk-b")

	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	mk := func(instID int64, kind string, i int) {
		t.Helper()
		if err := s.Backups().Create(ctx, &Backup{
			InstanceID: instID,
			Path:       fmt.Sprintf("/b/%d-%s-%d.zip", instID, kind, i),
			Kind:       kind,
			SizeBytes:  int64(100 * (i + 1)),
			CreatedAt:  base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	mk(a.ID, BackupManual, 0)
	mk(a.ID, BackupManual, 1)
	mk(a.ID, BackupScheduled, 0)
	mk(a.ID, BackupPreStart, 0)
	mk(b.ID, BackupManual, 0)

	all, err := s.Backups().ListByInstance(ctx, a.ID)
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("ListByInstance = %d, want 4", len(all))
	}
	// Newest first.
	for i := 1; i < len(all); i++ {
		if all[i].CreatedAt.After(all[i-1].CreatedAt) {
			t.Errorf("ListByInstance must be newest-first: %v then %v", all[i-1].CreatedAt, all[i].CreatedAt)
		}
	}

	manuals, err := s.Backups().ListByInstanceKind(ctx, a.ID, BackupManual)
	if err != nil {
		t.Fatalf("ListByInstanceKind: %v", err)
	}
	if len(manuals) != 2 {
		t.Errorf("manual backups = %d, want 2", len(manuals))
	}

	oldestFirst, err := s.Backups().ListOldestFirst(ctx, a.ID)
	if err != nil {
		t.Fatalf("ListOldestFirst: %v", err)
	}
	if len(oldestFirst) != 4 {
		t.Fatalf("ListOldestFirst = %d, want 4", len(oldestFirst))
	}
	for i := 1; i < len(oldestFirst); i++ {
		if oldestFirst[i].CreatedAt.Before(oldestFirst[i-1].CreatedAt) {
			t.Errorf("ListOldestFirst is not oldest-first")
		}
	}

	if _, err := s.Backups().ListByInstanceKind(ctx, a.ID, "hourly"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ListByInstanceKind(bogus) = %v, want ErrInvalid", err)
	}
}

func TestBackupTotalSizeAndCount(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "bk-size")

	// No rows: SUM is NULL and must read as 0, not as an error.
	total, err := s.Backups().TotalSize(ctx, inst.ID)
	if err != nil {
		t.Fatalf("TotalSize on empty: %v", err)
	}
	if total != 0 {
		t.Errorf("TotalSize on empty = %d, want 0", total)
	}

	for _, size := range []int64{100, 250, 1000} {
		if err := s.Backups().Create(ctx, &Backup{
			InstanceID: inst.ID, Path: fmt.Sprintf("/b/%d.zip", size),
			Kind: BackupManual, SizeBytes: size,
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	total, err = s.Backups().TotalSize(ctx, inst.ID)
	if err != nil {
		t.Fatalf("TotalSize: %v", err)
	}
	if total != 1350 {
		t.Errorf("TotalSize = %d, want 1350", total)
	}

	n, err := s.Backups().CountByKind(ctx, inst.ID, BackupManual)
	if err != nil {
		t.Fatalf("CountByKind: %v", err)
	}
	if n != 3 {
		t.Errorf("CountByKind = %d, want 3", n)
	}
	if _, err := s.Backups().CountByKind(ctx, inst.ID, "bogus"); !errors.Is(err, ErrInvalid) {
		t.Errorf("CountByKind(bogus) = %v, want ErrInvalid", err)
	}
}

// TestPruneOldestRetentionMath is the §6.5 requirement spelled out: create 5,
// keep 3, and assert exactly the 2 oldest are gone while the newest 3 remain.
func TestPruneOldestRetentionMath(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "prune-math")

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var created []Backup
	for i := 0; i < 5; i++ {
		b := &Backup{
			InstanceID: inst.ID,
			Path:       fmt.Sprintf("/backups/pre-start-%d.zip", i),
			Kind:       BackupPreStart,
			SizeBytes:  int64(i + 1),
			CreatedAt:  base.Add(time.Duration(i) * time.Hour),
			Note:       fmt.Sprintf("run %d", i),
		}
		if err := s.Backups().Create(ctx, b); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		created = append(created, *b)
	}

	deleted, err := s.Backups().PruneOldest(ctx, inst.ID, BackupPreStart, 3)
	if err != nil {
		t.Fatalf("PruneOldest: %v", err)
	}

	// Exactly the 2 oldest are reported as deleted, oldest first.
	if len(deleted) != 2 {
		t.Fatalf("PruneOldest deleted %d, want exactly 2: %+v", len(deleted), deleted)
	}
	if deleted[0].Path != "/backups/pre-start-0.zip" {
		t.Errorf("first deleted = %q, want the oldest (run 0)", deleted[0].Path)
	}
	if deleted[1].Path != "/backups/pre-start-1.zip" {
		t.Errorf("second deleted = %q, want run 1", deleted[1].Path)
	}
	if !deleted[0].CreatedAt.Before(deleted[1].CreatedAt) {
		t.Errorf("deleted list must be oldest-first: %v then %v", deleted[0].CreatedAt, deleted[1].CreatedAt)
	}

	// The newest 3 remain, exactly.
	remaining, err := s.Backups().ListOldestFirst(ctx, inst.ID)
	if err != nil {
		t.Fatalf("ListOldestFirst: %v", err)
	}
	if len(remaining) != 3 {
		t.Fatalf("%d backups remain, want 3: %+v", len(remaining), remaining)
	}
	wantRemaining := []string{
		"/backups/pre-start-2.zip",
		"/backups/pre-start-3.zip",
		"/backups/pre-start-4.zip",
	}
	for i, want := range wantRemaining {
		if remaining[i].Path != want {
			t.Errorf("remaining[%d] = %q, want %q", i, remaining[i].Path, want)
		}
	}

	// The rows that were returned really are gone.
	for _, d := range deleted {
		if _, err := s.Backups().Get(ctx, d.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("deleted backup %d is still readable: %v", d.ID, err)
		}
	}

	// Idempotent: pruning again to the same bound removes nothing.
	deleted, err = s.Backups().PruneOldest(ctx, inst.ID, BackupPreStart, 3)
	if err != nil {
		t.Fatalf("second PruneOldest: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("second PruneOldest deleted %d, want 0", len(deleted))
	}
}

// TestPruneOldestIsPerKind pins that retention is scoped to one kind: the
// pre-start rule must not delete scheduled or manual archives.
func TestPruneOldestIsPerKind(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "prune-kind")
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		if err := s.Backups().Create(ctx, &Backup{
			InstanceID: inst.ID, Path: fmt.Sprintf("/b/pre-%d.zip", i),
			Kind: BackupPreStart, CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("Create pre-start: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := s.Backups().Create(ctx, &Backup{
			InstanceID: inst.ID, Path: fmt.Sprintf("/b/manual-%d.zip", i),
			Kind: BackupManual, CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("Create manual: %v", err)
		}
	}

	deleted, err := s.Backups().PruneOldest(ctx, inst.ID, BackupPreStart, 3)
	if err != nil {
		t.Fatalf("PruneOldest: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted %d pre-start backups, want 2", len(deleted))
	}

	manuals, err := s.Backups().ListByInstanceKind(ctx, inst.ID, BackupManual)
	if err != nil {
		t.Fatalf("ListByInstanceKind: %v", err)
	}
	if len(manuals) != 5 {
		t.Errorf("manual backups = %d, want all 5 untouched", len(manuals))
	}

	// keep=0 clears the kind entirely.
	deleted, err = s.Backups().PruneOldest(ctx, inst.ID, BackupPreStart, 0)
	if err != nil {
		t.Fatalf("PruneOldest(0): %v", err)
	}
	if len(deleted) != 3 {
		t.Errorf("keep=0 deleted %d, want 3", len(deleted))
	}
	if n, _ := s.Backups().CountByKind(ctx, inst.ID, BackupPreStart); n != 0 {
		t.Errorf("%d pre-start backups remain, want 0", n)
	}
}

// TestPruneOldestIsPerInstance makes sure one instance's retention never
// touches another's backups.
func TestPruneOldestIsPerInstance(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	a := mustCreateInstance(t, s, "pi-inst-a")
	b := mustCreateInstance(t, s, "pi-inst-b")
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	for _, id := range []int64{a.ID, b.ID} {
		for i := 0; i < 5; i++ {
			if err := s.Backups().Create(ctx, &Backup{
				InstanceID: id, Path: fmt.Sprintf("/b/%d-%d.zip", id, i),
				Kind: BackupPreStart, CreatedAt: base.Add(time.Duration(i) * time.Hour),
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
	}

	deleted, err := s.Backups().PruneOldest(ctx, a.ID, BackupPreStart, 3)
	if err != nil {
		t.Fatalf("PruneOldest: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted %d, want 2", len(deleted))
	}
	for _, d := range deleted {
		if d.InstanceID != a.ID {
			t.Errorf("pruned a backup belonging to instance %d", d.InstanceID)
		}
	}
	if n, _ := s.Backups().CountByKind(ctx, b.ID, BackupPreStart); n != 5 {
		t.Errorf("instance b has %d backups, want all 5 untouched", n)
	}
}

// TestPruneOldestSameSecondTieBreak covers the RFC3339 one-second resolution:
// five backups created "at once" must still be pruned deterministically by the
// id tie-break, never leaving an arbitrary three.
func TestPruneOldestSameSecondTieBreak(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "prune-ties")
	sameSecond := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	var ids []int64
	for i := 0; i < 5; i++ {
		b := &Backup{
			InstanceID: inst.ID,
			Path:       fmt.Sprintf("/b/tie-%d.zip", i),
			Kind:       BackupPreStart,
			CreatedAt:  sameSecond, // identical timestamps
		}
		if err := s.Backups().Create(ctx, b); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		ids = append(ids, b.ID)
	}

	deleted, err := s.Backups().PruneOldest(ctx, inst.ID, BackupPreStart, 3)
	if err != nil {
		t.Fatalf("PruneOldest: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted %d, want 2", len(deleted))
	}
	// With equal timestamps the id ordering decides: the two lowest ids go.
	if deleted[0].ID != ids[0] || deleted[1].ID != ids[1] {
		t.Errorf("tie-break deleted ids %d,%d; want the two lowest (%d,%d)",
			deleted[0].ID, deleted[1].ID, ids[0], ids[1])
	}

	remaining, _ := s.Backups().ListOldestFirst(ctx, inst.ID)
	if len(remaining) != 3 {
		t.Fatalf("%d remain, want 3", len(remaining))
	}
	for i, want := range ids[2:] {
		if remaining[i].ID != want {
			t.Errorf("remaining[%d] id = %d, want %d", i, remaining[i].ID, want)
		}
	}
}

func TestPruneOldestValidation(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "prune-val")

	if _, err := s.Backups().PruneOldest(ctx, inst.ID, "hourly", 3); !errors.Is(err, ErrInvalid) {
		t.Errorf("PruneOldest(bogus kind) = %v, want ErrInvalid", err)
	}
	if _, err := s.Backups().PruneOldest(ctx, inst.ID, BackupManual, -1); !errors.Is(err, ErrInvalid) {
		t.Errorf("PruneOldest(keep=-1) = %v, want ErrInvalid", err)
	}
	// An instance with no backups is not an error.
	deleted, err := s.Backups().PruneOldest(ctx, 9999, BackupManual, 3)
	if err != nil {
		t.Fatalf("PruneOldest on a missing instance: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted %d rows for a missing instance", len(deleted))
	}
}

func TestPruneBefore(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "prune-before")
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 6; i++ {
		if err := s.Backups().Create(ctx, &Backup{
			InstanceID: inst.ID, Path: fmt.Sprintf("/b/d%d.zip", i),
			Kind: BackupScheduled, CreatedAt: base.AddDate(0, 0, i),
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	// Keep the last three days.
	cutoff := base.AddDate(0, 0, 3)
	deleted, err := s.Backups().PruneBefore(ctx, inst.ID, cutoff)
	if err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if len(deleted) != 3 {
		t.Fatalf("PruneBefore deleted %d, want 3", len(deleted))
	}
	for _, d := range deleted {
		if !d.CreatedAt.Before(cutoff) {
			t.Errorf("PruneBefore deleted %v, which is not before %v", d.CreatedAt, cutoff)
		}
	}
	remaining, _ := s.Backups().ListOldestFirst(ctx, inst.ID)
	if len(remaining) != 3 {
		t.Fatalf("%d remain, want 3", len(remaining))
	}
	if !remaining[0].CreatedAt.Equal(cutoff) {
		t.Errorf("PruneBefore must be exclusive of the cutoff; oldest kept = %v", remaining[0].CreatedAt)
	}
}

func TestBackupDeleteByInstance(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	a := mustCreateInstance(t, s, "del-inst-a")
	b := mustCreateInstance(t, s, "del-inst-b")

	for i := 0; i < 3; i++ {
		if err := s.Backups().Create(ctx, &Backup{
			InstanceID: a.ID, Path: fmt.Sprintf("/b/a%d.zip", i), Kind: BackupManual,
		}); err != nil {
			t.Fatalf("Create a: %v", err)
		}
	}
	if err := s.Backups().Create(ctx, &Backup{InstanceID: b.ID, Path: "/b/b0.zip", Kind: BackupManual}); err != nil {
		t.Fatalf("Create b: %v", err)
	}

	deleted, err := s.Backups().DeleteByInstance(ctx, a.ID)
	if err != nil {
		t.Fatalf("DeleteByInstance: %v", err)
	}
	if len(deleted) != 3 {
		t.Errorf("DeleteByInstance reported %d, want 3", len(deleted))
	}
	if rows, _ := s.Backups().ListByInstance(ctx, a.ID); len(rows) != 0 {
		t.Errorf("%d rows remain for a", len(rows))
	}
	if rows, _ := s.Backups().ListByInstance(ctx, b.ID); len(rows) != 1 {
		t.Errorf("instance b lost its backups: %d remain", len(rows))
	}
}

func TestBackupDeleteSingle(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	inst := mustCreateInstance(t, s, "del-single")
	b := &Backup{InstanceID: inst.ID, Path: "/b/one.zip", Kind: BackupManual}
	if err := s.Backups().Create(ctx, b); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Backups().Delete(ctx, b.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Backups().Get(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
}
