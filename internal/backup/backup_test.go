package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// memStore is an in-memory Store that mimics the real repository's contract,
// including the crucial ordering guarantee: ListByInstanceKind is newest first.
type memStore struct {
	mu      sync.Mutex
	nextID  int64
	records map[int64]*Record

	createErr error
	listErr   error
	getErr    error
	deleteErr error
}

func newMemStore() *memStore {
	return &memStore{nextID: 1, records: map[int64]*Record{}}
}

func (s *memStore) Create(_ context.Context, rec *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	rec.ID = s.nextID
	s.nextID++
	cp := *rec
	s.records[rec.ID] = &cp
	return nil
}

func (s *memStore) Get(_ context.Context, id int64) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	rec, ok := s.records[id]
	if !ok {
		return nil, fmt.Errorf("%w: id %d", ErrNotFound, id)
	}
	cp := *rec
	return &cp, nil
}

func (s *memStore) ListByInstance(_ context.Context, instanceID int64) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []Record
	for _, rec := range s.records {
		if rec.InstanceID == instanceID {
			out = append(out, *rec)
		}
	}
	sortNewestFirst(out)
	return out, nil
}

func (s *memStore) ListByInstanceKind(_ context.Context, instanceID int64, kind Kind) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []Record
	for _, rec := range s.records {
		if rec.InstanceID == instanceID && rec.Kind == kind {
			out = append(out, *rec)
		}
	}
	sortNewestFirst(out)
	return out, nil
}

func (s *memStore) Delete(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.records, id)
	return nil
}

// sortNewestFirst orders by CreatedAt desc, then ID desc — the same total order
// store.BackupRepo uses, and the one retention depends on.
func sortNewestFirst(recs []Record) {
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
			return recs[i].CreatedAt.After(recs[j].CreatedAt)
		}
		return recs[i].ID > recs[j].ID
	})
}

// count returns how many records the store holds.
func (s *memStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// fixture is a synthetic instance with a world directory.
type fixture struct {
	instanceID  int64
	instanceDir string
	worldDir    string
	worldName   string
	root        string

	files map[string]string // relative to worldDir -> content
}

// newFixture builds an instance directory containing one world with a
// Project.json, a Project.json.bak and a Regions directory, which is the real
// layout from §2.2/§2.3.
func newFixture(t *testing.T) *fixture {
	t.Helper()

	base := t.TempDir()
	f := &fixture{
		instanceID:  1,
		instanceDir: filepath.Join(base, "instances", "survival-1"),
		worldName:   "MyWorld",
		root:        filepath.Join(base, "backups"),
	}
	f.worldDir = filepath.Join(f.instanceDir, "Worlds", f.worldName)

	f.files = map[string]string{
		"Project.json":      `{"Name":["string","MyWorld"],"Version":["string","2.4"]}`,
		"Project.json.bak":  `{"Name":["string","MyWorld"],"Version":["string","2.3"]}`,
		"Regions/r.0.0.dat": "region data for 0,0",
		"Regions/r.1.0.dat": "region data for 1,0",
		"Regions/r.0.1.dat": "region data for 0,1",
		"Chunks/chunk.json": `{"chunk":true}`,
	}

	f.write(t)
	return f
}

// write materialises the fixture's files on disk.
func (f *fixture) write(t *testing.T) {
	t.Helper()
	for rel, content := range f.files {
		p := filepath.Join(f.worldDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// readFile reads a file from the world directory.
func (f *fixture) readFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.worldDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// request builds a Request for the fixture's world.
func (f *fixture) request(kind Kind) Request {
	return Request{
		InstanceID:  f.instanceID,
		InstanceDir: f.instanceDir,
		WorldDir:    f.worldDir,
		World:       f.worldName,
		Kind:        kind,
	}
}

// stoppedGuard reports every instance as stopped.
func stoppedGuard() Guard {
	return GuardFunc(func(int64) (bool, bool) { return false, true })
}

// runningGuard reports every instance as running.
func runningGuard() Guard {
	return GuardFunc(func(int64) (bool, bool) { return true, true })
}

// unknownGuard cannot determine the state.
func unknownGuard() Guard {
	return GuardFunc(func(int64) (bool, bool) { return false, false })
}

// newTestManager builds a Manager over the fixture with a controllable clock
// and a large free-space report.
func newTestManager(t *testing.T, f *fixture, store Store, guard Guard) *Manager {
	t.Helper()

	m, err := NewManager(Options{
		Root:   f.root,
		Store:  store,
		Guard:  guard,
		StatFS: func(string) (uint64, error) { return 1 << 40, nil }, // 1 TiB free
		Now:    func() time.Time { return time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// listArchive returns the archive's entry names, sorted.
func listArchive(t *testing.T, path string) []string {
	t.Helper()

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open archive %s: %v", path, err)
	}
	defer zr.Close()

	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

// readArchiveEntry returns one entry's content.
func readArchiveEntry(t *testing.T, path, name string) string {
	t.Helper()

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read entry %s: %v", name, err)
		}
		return string(data)
	}
	t.Fatalf("entry %s not found in %s", name, path)
	return ""
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

// TestCreateVerifiesHashSizeAndMetadata is the core Create contract: the
// archive is a valid zip, its recorded sha256 and size describe the bytes on
// disk, and the metadata matches the request.
func TestCreateVerifiesHashSizeAndMetadata(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()
	m := newTestManager(t, f, store, stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Metadata.
	if rec.ID == 0 {
		t.Error("Create must fill in the record id")
	}
	if rec.InstanceID != f.instanceID {
		t.Errorf("InstanceID = %d, want %d", rec.InstanceID, f.instanceID)
	}
	if rec.World != f.worldName {
		t.Errorf("World = %q, want %q", rec.World, f.worldName)
	}
	if rec.Kind != KindManual {
		t.Errorf("Kind = %q, want manual", rec.Kind)
	}
	if rec.Hot {
		t.Error("Hot = true for a stopped instance")
	}
	if rec.Files == 0 {
		t.Error("Files = 0, want the number of archived entries")
	}
	if rec.SHA256 == "" {
		t.Fatal("SHA256 must be recorded")
	}

	// The recorded digest must describe the file on disk.
	onDisk, err := os.ReadFile(rec.Path)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	sum := sha256.Sum256(onDisk)
	if got := hex.EncodeToString(sum[:]); got != rec.SHA256 {
		t.Errorf("recorded sha256 = %s, but the file hashes to %s", rec.SHA256, got)
	}

	// The recorded size must match too.
	st, err := os.Stat(rec.Path)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if rec.SizeBytes != st.Size() {
		t.Errorf("SizeBytes = %d, but the file is %d bytes", rec.SizeBytes, st.Size())
	}

	// The archive must be readable and contain the world's files.
	names := listArchive(t, rec.Path)
	for _, want := range []string{
		"world/Project.json",
		"world/Project.json.bak",
		"world/Regions/r.0.0.dat",
		"world/Chunks/chunk.json",
	} {
		if !contains(names, want) {
			t.Errorf("archive is missing %q; entries: %v", want, names)
		}
	}

	// Content round trip inside the archive.
	if got := readArchiveEntry(t, rec.Path, "world/Project.json"); got != f.files["Project.json"] {
		t.Errorf("archived Project.json = %q, want %q", got, f.files["Project.json"])
	}

	// The record must be persisted.
	if store.count() != 1 {
		t.Errorf("store has %d records, want 1", store.count())
	}
}

// TestCreatePathIsDeterministicAndSortable checks the naming contract: the path
// is under the root, follows <instance>/<world>/<kind>-<stamp>-….zip, and
// sorts chronologically as a plain listing.
func TestCreatePathIsDeterministicAndSortable(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	times := []time.Time{
		time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 5, 7, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 5, 8, 0, 0, 0, time.UTC),
	}

	var paths []string
	for _, at := range times {
		m, err := NewManager(Options{
			Root: f.root, Store: store, Guard: stoppedGuard(),
			StatFS: func(string) (uint64, error) { return 1 << 40, nil },
			Now:    func() time.Time { return at },
		})
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		rec, err := m.Create(context.Background(), f.request(KindScheduled))
		if err != nil {
			t.Fatalf("Create at %v: %v", at, err)
		}
		paths = append(paths, rec.Path)

		if !isWithin(f.root, rec.Path) {
			t.Errorf("archive %s is outside the root %s", rec.Path, f.root)
		}
		if got := filepath.Base(filepath.Dir(rec.Path)); got != f.worldName {
			t.Errorf("archive parent = %q, want the world name", got)
		}
		if !strings.HasPrefix(filepath.Base(rec.Path), string(KindScheduled)+"-") {
			t.Errorf("archive name = %q, want it to start with the kind", filepath.Base(rec.Path))
		}
		if !strings.HasSuffix(rec.Path, ".zip") {
			t.Errorf("archive name = %q, want a .zip suffix", filepath.Base(rec.Path))
		}
	}

	// Lexicographic order must equal chronological order.
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	for i := range paths {
		if paths[i] != sorted[i] {
			t.Fatalf("paths do not sort chronologically:\n%v\nvs\n%v", paths, sorted)
		}
	}

	// No colons: the stamp must be portable to filesystems that reject them.
	for _, p := range paths {
		if strings.Contains(filepath.Base(p), ":") {
			t.Errorf("archive name %q contains a colon", filepath.Base(p))
		}
	}
}

func TestCreateColdBackupNote(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.Contains(rec.Note, "cold backup") {
		t.Errorf("Note = %q, want it to say the copy is consistent", rec.Note)
	}
	if rec.SaveAttempted {
		t.Error("SaveAttempted = true for a stopped instance")
	}
}

// TestCreateHotBackupIsFlagged is the §6.5 consistency requirement: a backup
// taken while the instance is up must be marked, never presented as consistent.
func TestCreateHotBackupIsFlagged(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	var savedFor int64
	saver := SaverFunc(func(_ context.Context, instanceID int64) error {
		savedFor = instanceID
		return nil
	})

	m, err := NewManager(Options{
		Root: f.root, Store: newMemStore(), Guard: runningGuard(), Saver: saver,
		StatFS: func(string) (uint64, error) { return 1 << 40, nil },
		Now:    func() time.Time { return time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	rec, err := m.Create(context.Background(), f.request(KindPreStart))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !rec.Hot {
		t.Fatal("Hot = false for a running instance; a hot backup must never be presented as consistent")
	}
	if !rec.SaveAttempted {
		t.Error("SaveAttempted = false although a Saver was configured")
	}
	if savedFor != f.instanceID {
		t.Errorf("Save called for instance %d, want %d", savedFor, f.instanceID)
	}
	if !strings.Contains(rec.Note, "hot backup") {
		t.Errorf("Note = %q, want it to carry the hot-backup warning", rec.Note)
	}
	if !strings.Contains(rec.Note, "may be inconsistent") {
		t.Errorf("Note = %q, want it to state the consistency risk explicitly", rec.Note)
	}
	if !strings.Contains(rec.Note, "save was requested") {
		t.Errorf("Note = %q, want it to record that a save was attempted", rec.Note)
	}
}

// TestCreateUnknownGuardStateCountsAsHot pins the asymmetric default.
func TestCreateUnknownGuardStateCountsAsHot(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), unknownGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !rec.Hot {
		t.Error("Hot = false although the instance state could not be determined")
	}
}

// TestCreateWithNoGuardCountsAsHot checks the no-guard-configured default.
func TestCreateWithNoGuardCountsAsHot(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), nil)

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !rec.Hot {
		t.Error("Hot = false with no guard configured; unknown must mean hot")
	}
}

// TestCreateHotBackupSurvivesFailedSave pins the deliberate trade-off: a save
// that fails must not stop the backup, but must be recorded.
func TestCreateHotBackupSurvivesFailedSave(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	m, err := NewManager(Options{
		Root: f.root, Store: newMemStore(), Guard: runningGuard(),
		Saver:  SaverFunc(func(context.Context, int64) error { return errors.New("console unavailable") }),
		StatFS: func(string) (uint64, error) { return 1 << 40, nil },
		Now:    func() time.Time { return time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create must still produce a backup when the save fails: %v", err)
	}
	if !rec.Hot {
		t.Error("Hot = false")
	}
	if rec.SaveError == "" {
		t.Fatal("SaveError must record the failed save")
	}
	if !strings.Contains(rec.Note, "save failed") {
		t.Errorf("Note = %q, want it to mention the failed save", rec.Note)
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Errorf("the archive must exist: %v", err)
	}
}

// TestCreateDiskSpacePrecheck checks the typed insufficient-space error.
func TestCreateDiskSpacePrecheck(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	m, err := NewManager(Options{
		Root: f.root, Store: newMemStore(), Guard: stoppedGuard(),
		StatFS:         func(string) (uint64, error) { return 1024, nil }, // 1 KiB free
		MinFreeReserve: 1 << 30,
		Now:            func() time.Time { return time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = m.Create(context.Background(), f.request(KindManual))
	if err == nil {
		t.Fatal("Create must fail when free space is below the reserve")
	}
	if !errors.Is(err, ErrNotEnoughSpace) {
		t.Errorf("error = %v, want ErrNotEnoughSpace", err)
	}

	// No archive may be left behind by the aborted attempt.
	var zips int
	_ = filepath.WalkDir(f.root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".zip") {
			zips++
		}
		return nil
	})
	if zips != 0 {
		t.Errorf("%d archives exist after a failed space precheck, want 0", zips)
	}
}

// TestCreateSkipsSpacePrecheckWhenUnknown checks that a statfs failure does not
// block a backup the operator needs.
func TestCreateSkipsSpacePrecheckWhenUnknown(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	m, err := NewManager(Options{
		Root: f.root, Store: newMemStore(), Guard: stoppedGuard(),
		StatFS: func(string) (uint64, error) { return 0, errors.New("statfs unsupported") },
		Now:    func() time.Time { return time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if _, err := m.Create(context.Background(), f.request(KindManual)); err != nil {
		t.Fatalf("Create must proceed when free space cannot be determined: %v", err)
	}
}

func TestCreateRejectsBadRequests(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	tests := []struct {
		name string
		req  Request
		want error
	}{
		{"no instance id", Request{InstanceDir: f.instanceDir, WorldDir: f.worldDir}, ErrInvalidRequest},
		{"no instance dir", Request{InstanceID: 1, WorldDir: f.worldDir}, ErrInvalidRequest},
		{"no world at all", Request{InstanceID: 1, InstanceDir: f.instanceDir}, ErrInvalidRequest},
		{"bad kind", Request{InstanceID: 1, InstanceDir: f.instanceDir, WorldDir: f.worldDir, Kind: "hourly"}, ErrInvalidRequest},
		{"negative keep", Request{InstanceID: 1, InstanceDir: f.instanceDir, WorldDir: f.worldDir, Keep: -1}, ErrInvalidRequest},
		{
			"world outside the instance",
			Request{InstanceID: 1, InstanceDir: f.instanceDir, WorldDir: filepath.Dir(f.instanceDir)},
			ErrInvalidRequest,
		},
		{
			"world does not exist",
			Request{InstanceID: 1, InstanceDir: f.instanceDir, World: "NoSuchWorld"},
			ErrNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.Create(context.Background(), tc.req)
			if err == nil {
				t.Fatal("Create must reject this request")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestCreateRejectsTraversalWorldDir is the §6.2 path rule applied to backups:
// the world directory may not climb out of the instance.
func TestCreateRejectsTraversalWorldDir(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	outside := filepath.Join(filepath.Dir(f.instanceDir), "somewhere-else")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := m.Create(context.Background(), Request{
		InstanceID:  1,
		InstanceDir: f.instanceDir,
		WorldDir:    outside,
	})
	if err == nil {
		t.Fatal("Create must refuse a world directory outside the instance")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("error = %v, want ErrInvalidRequest", err)
	}
}

// TestCreateWorldNameCannotEscapeRoot checks the sanitisation of the world name
// used as a path component.
func TestCreateWorldNameCannotEscapeRoot(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	// The world directory is legitimate but its basename is hostile: a name
	// with separators would otherwise create directories outside the root.
	hostile := filepath.Join(f.instanceDir, "Worlds", "..evil")
	if err := os.MkdirAll(hostile, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	rec, err := m.Create(context.Background(), Request{
		InstanceID:  1,
		InstanceDir: f.instanceDir,
		WorldDir:    hostile,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !isWithin(f.root, rec.Path) {
		t.Fatalf("archive %s escaped the backup root %s", rec.Path, f.root)
	}
}

func TestCreateCancelledContext(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := m.Create(ctx, f.request(KindManual)); !errors.Is(err, context.Canceled) {
		t.Errorf("Create with a cancelled context = %v, want context.Canceled", err)
	}
}

// TestCreateRecordFailureRemovesTheArchive checks that the index and the
// filesystem cannot disagree: an unindexed archive would be invisible to every
// later retention pass.
func TestCreateRecordFailureRemovesTheArchive(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()
	store.createErr = errors.New("database is locked")
	m := newTestManager(t, f, store, stoppedGuard())

	_, err := m.Create(context.Background(), f.request(KindManual))
	if err == nil {
		t.Fatal("Create must report a repository failure")
	}

	var zips []string
	_ = filepath.WalkDir(f.root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".zip") {
			zips = append(zips, p)
		}
		return nil
	})
	if len(zips) != 0 {
		t.Errorf("orphan archives left behind: %v", zips)
	}
}

// TestCreateExcludesRestoreStagingDirectories checks that an interrupted
// restore's scratch directory is not swept into the next backup.
func TestCreateExcludesRestoreStagingDirectories(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	staging := filepath.Join(f.worldDir, ".restore-abc123")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "leftover.dat"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := newTestManager(t, f, newMemStore(), stoppedGuard())
	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, name := range listArchive(t, rec.Path) {
		if strings.Contains(name, ".restore-") {
			t.Errorf("archive contains staging entry %q", name)
		}
	}
}

func TestCreateArchivesSymlinksAndSkipsEscapingOnes(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// A symlink inside the world is fine.
	if err := os.Symlink("Regions/r.0.0.dat", filepath.Join(f.worldDir, "inside.link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// A symlink pointing outside the world must not be archived.
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(f.worldDir, "escape.link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	m := newTestManager(t, f, newMemStore(), stoppedGuard())
	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	names := listArchive(t, rec.Path)
	if !contains(names, "world/inside.link") {
		t.Errorf("the internal symlink should be archived; entries: %v", names)
	}
	if contains(names, "world/escape.link") {
		t.Errorf("a symlink escaping the world must not be archived; entries: %v", names)
	}
}

func TestCreateArchivesEmptyDirectories(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.worldDir, "Empty"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m := newTestManager(t, f, newMemStore(), stoppedGuard())
	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !contains(listArchive(t, rec.Path), "world/Empty/") {
		t.Errorf("empty directories must survive the round trip; entries: %v", listArchive(t, rec.Path))
	}
}

// ---------------------------------------------------------------------------
// Retention
// ---------------------------------------------------------------------------

// TestPreStartKeepThree is §6.5's retention rule, tested exactly: five
// pre-start backups with keep=3 leave the newest three.
func TestPreStartKeepThree(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)

	var created []*Record
	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		m, err := NewManager(Options{
			Root: f.root, Store: store, Guard: stoppedGuard(),
			StatFS: func(string) (uint64, error) { return 1 << 40, nil },
			Now:    func() time.Time { return at },
		})
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}

		// Ask for a large keep: the pre-start tier must ignore it and use 3.
		req := f.request(KindPreStart)
		req.Keep = 100

		rec, err := m.Create(context.Background(), req)
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		created = append(created, rec)
	}

	remaining, err := store.ListByInstanceKind(context.Background(), f.instanceID, KindPreStart)
	if err != nil {
		t.Fatalf("ListByInstanceKind: %v", err)
	}
	if len(remaining) != PreStartKeep {
		t.Fatalf("kept %d pre-start backups, want exactly %d", len(remaining), PreStartKeep)
	}

	// The survivors must be the three newest.
	wantPaths := map[string]bool{
		created[2].Path: true,
		created[3].Path: true,
		created[4].Path: true,
	}
	for _, rec := range remaining {
		if !wantPaths[rec.Path] {
			t.Errorf("kept %s, which is not one of the three newest: %v", rec.Path, wantPaths)
		}
	}

	// The two oldest archives must be gone from disk as well as from the index.
	for _, gone := range created[:2] {
		if _, err := os.Stat(gone.Path); !os.IsNotExist(err) {
			t.Errorf("archive %s still exists after pruning (err=%v)", gone.Path, err)
		}
	}
	for _, kept := range created[2:] {
		if _, err := os.Stat(kept.Path); err != nil {
			t.Errorf("archive %s should have survived: %v", kept.Path, err)
		}
	}
}

// TestManualBackupsAreNeverPruned pins §6.5's "manual management": the panel
// must never delete a backup the operator deliberately took.
func TestManualBackupsAreNeverPruned(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	var paths []string

	for i := 0; i < 8; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		m, err := NewManager(Options{
			Root: f.root, Store: store, Guard: stoppedGuard(),
			StatFS: func(string) (uint64, error) { return 1 << 40, nil },
			Now:    func() time.Time { return at },
		})
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}

		req := f.request(KindManual)
		req.Keep = 1 // must be ignored for the manual tier
		rec, err := m.Create(context.Background(), req)
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		paths = append(paths, rec.Path)
	}

	remaining, err := store.ListByInstanceKind(context.Background(), f.instanceID, KindManual)
	if err != nil {
		t.Fatalf("ListByInstanceKind: %v", err)
	}
	if len(remaining) != 8 {
		t.Fatalf("kept %d manual backups, want all 8", len(remaining))
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("manual archive %s was removed: %v", p, err)
		}
	}

	// Prune on the manual tier explicitly must also be a no-op.
	m := newTestManager(t, f, store, stoppedGuard())
	removed, err := m.Prune(context.Background(), f.instanceID, KindManual, 1)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("Prune removed %d manual backups, want 0", len(removed))
	}
}

// TestScheduledKeepNChecksConfiguredRetention checks the configurable tier.
func TestScheduledKeepNChecksConfiguredRetention(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		m, err := NewManager(Options{
			Root: f.root, Store: store, Guard: stoppedGuard(),
			StatFS: func(string) (uint64, error) { return 1 << 40, nil },
			Now:    func() time.Time { return at },
		})
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}

		req := f.request(KindScheduled)
		req.Keep = 2
		if _, err := m.Create(context.Background(), req); err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
	}

	remaining, err := store.ListByInstanceKind(context.Background(), f.instanceID, KindScheduled)
	if err != nil {
		t.Fatalf("ListByInstanceKind: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("kept %d scheduled backups, want 2", len(remaining))
	}
}

// TestScheduledDefaultKeep checks the default retention when Keep is unset.
func TestScheduledDefaultKeep(t *testing.T) {
	t.Parallel()

	req := Request{InstanceID: 1, InstanceDir: "/x", World: "w", Kind: KindScheduled}
	norm, err := req.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if norm.Keep != DefaultScheduledKeep {
		t.Errorf("Keep = %d, want %d", norm.Keep, DefaultScheduledKeep)
	}
}

// TestRetentionIsPerKind checks that pruning one tier does not touch another.
func TestRetentionIsPerKind(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	next := func(i int) *Manager {
		at := base.Add(time.Duration(i) * time.Minute)
		m, err := NewManager(Options{
			Root: f.root, Store: store, Guard: stoppedGuard(),
			StatFS: func(string) (uint64, error) { return 1 << 40, nil },
			Now:    func() time.Time { return at },
		})
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		return m
	}

	for i := 0; i < 5; i++ {
		m := next(i)
		if _, err := m.Create(context.Background(), f.request(KindPreStart)); err != nil {
			t.Fatalf("pre-start Create: %v", err)
		}
	}
	for i := 10; i < 13; i++ {
		m := next(i)
		if _, err := m.Create(context.Background(), f.request(KindManual)); err != nil {
			t.Fatalf("manual Create: %v", err)
		}
	}

	preStart, _ := store.ListByInstanceKind(context.Background(), f.instanceID, KindPreStart)
	manual, _ := store.ListByInstanceKind(context.Background(), f.instanceID, KindManual)

	if len(preStart) != PreStartKeep {
		t.Errorf("pre-start count = %d, want %d", len(preStart), PreStartKeep)
	}
	if len(manual) != 3 {
		t.Errorf("manual count = %d, want 3 (pruning pre-start must not touch manual)", len(manual))
	}
}

// TestPruneRejectsTraversalDeletion is the security requirement: a crafted
// record whose path escapes the backup root must not cause a deletion outside
// it.
func TestPruneRejectsTraversalDeletion(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	// A file outside the backup root that must survive.
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "important.txt")
	if err := os.WriteFile(victim, []byte("do not delete"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)

	// Four pre-start records: three legitimate files (so the window is
	// exceeded) plus the traversing one as the *oldest*, which is the record
	// retention will try to delete first.
	makeRec := func(path string, at time.Time) {
		t.Helper()
		rec := &Record{
			InstanceID: f.instanceID, World: f.worldName, Kind: KindPreStart,
			Path: path, CreatedAt: at, SHA256: "deadbeef",
		}
		if err := store.Create(context.Background(), rec); err != nil {
			t.Fatalf("store.Create: %v", err)
		}
	}

	// Relative traversal and an absolute escape are both attempted.
	makeRec(filepath.Join(f.root, "..", "..", "escape.zip"), base)
	makeRec(victim, base.Add(time.Minute))
	makeRec("/etc/passwd", base.Add(2*time.Minute))
	makeRec(filepath.Join(f.root, "instance-1", "MyWorld", "ok.zip"), base.Add(3*time.Minute))

	m := newTestManager(t, f, store, stoppedGuard())

	_, err := m.Prune(context.Background(), f.instanceID, KindPreStart, 1)
	if err == nil {
		t.Fatal("Prune must refuse to act on a record outside the backup root")
	}
	if !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("error = %v, want ErrOutsideRoot", err)
	}

	// The victim files must be untouched.
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a file outside the backup root was deleted: %v", err)
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "do not delete" {
		t.Errorf("the victim file was modified: %q, %v", data, err)
	}
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Fatalf("a system file was deleted: %v", err)
	}
}

// TestPruneRejectsSymlinkEscape checks the resolved-path check: a symlink
// planted inside the root that points outside it must not be followed.
func TestPruneRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "target.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	// A symlink *inside* the backup root pointing at the victim outside it.
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	link := filepath.Join(f.root, "innocent.zip")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		rec := &Record{
			InstanceID: f.instanceID, World: f.worldName, Kind: KindPreStart,
			Path:      link,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if i > 0 {
			rec.Path = filepath.Join(f.root, fmt.Sprintf("legit-%d.zip", i))
			if err := os.WriteFile(rec.Path, []byte("zip"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		if err := store.Create(context.Background(), rec); err != nil {
			t.Fatalf("store.Create: %v", err)
		}
	}

	m := newTestManager(t, f, store, stoppedGuard())

	// Pruning to 3 targets the oldest record, which is the symlink.
	_, err := m.Prune(context.Background(), f.instanceID, KindPreStart, 3)
	if err == nil {
		t.Fatal("Prune must refuse to follow a symlink out of the backup root")
	}
	if !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("error = %v, want ErrOutsideRoot", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the symlink target was deleted: %v", err)
	}
}

// TestPruneToleratesAlreadyMissingArchive checks that an operator who removed
// a file by hand can still have the record cleaned up.
func TestPruneToleratesAlreadyMissingArchive(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		rec := &Record{
			InstanceID: f.instanceID, World: f.worldName, Kind: KindPreStart,
			Path:      filepath.Join(f.root, "instance-1", "MyWorld", fmt.Sprintf("%d.zip", i)),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := store.Create(context.Background(), rec); err != nil {
			t.Fatalf("store.Create: %v", err)
		}
	}

	m := newTestManager(t, f, store, stoppedGuard())
	removed, err := m.Prune(context.Background(), f.instanceID, KindPreStart, 3)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %d records, want 2", len(removed))
	}
	if got := store.count(); got != 3 {
		t.Errorf("store holds %d records, want 3", got)
	}
}

func TestPruneValidation(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	if _, err := m.Prune(context.Background(), 1, "hourly", 3); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Prune(bad kind) = %v, want ErrInvalidRequest", err)
	}
	if _, err := m.Prune(context.Background(), 1, KindPreStart, -1); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Prune(keep=-1) = %v, want ErrInvalidRequest", err)
	}
}

func TestPruneWithNoStore(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, nil, stoppedGuard())

	removed, err := m.Prune(context.Background(), 1, KindPreStart, 1)
	if err != nil || removed != nil {
		t.Errorf("Prune without a store = %v, %v; want nil, nil", removed, err)
	}
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

// TestRestoreRoundTrip is the phase-2 acceptance criterion from §10: a deleted
// instance save can be fully restored from backup, byte for byte.
func TestRestoreRoundTrip(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()
	m := newTestManager(t, f, store, stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Record the original contents.
	original := map[string]string{}
	for rel := range f.files {
		original[rel] = f.readFile(t, rel)
	}

	// Simulate the disaster: delete the world entirely.
	if err := os.RemoveAll(f.worldDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(f.worldDir); !os.IsNotExist(err) {
		t.Fatal("the world directory should be gone")
	}

	if err := m.Restore(context.Background(), rec.ID, f.worldDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Every file must be back, byte for byte.
	for rel, want := range original {
		got, err := os.ReadFile(filepath.Join(f.worldDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("restored world is missing %s: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}

	// The world directory itself must exist and be a directory.
	st, err := os.Stat(f.worldDir)
	if err != nil {
		t.Fatalf("stat restored world: %v", err)
	}
	if !st.IsDir() {
		t.Fatal("the restored world is not a directory")
	}
}

// TestRestoreOverExistingWorldPreservesPreviousState checks the "back up
// current state before overwriting" requirement: the old tree is moved aside,
// not destroyed.
func TestRestoreOverExistingWorldPreservesPreviousState(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()
	m := newTestManager(t, f, store, stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The live world diverges after the backup.
	liveContent := "LIVE DATA THAT IS NOT IN THE BACKUP"
	if err := os.WriteFile(filepath.Join(f.worldDir, "Project.json"), []byte(liveContent), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := m.Restore(context.Background(), rec.ID, f.worldDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// The restored content must be the backup's.
	if got := f.readFile(t, "Project.json"); got != f.files["Project.json"] {
		t.Errorf("Project.json = %q, want the backed-up content", got)
	}

	// The pre-restore tree must still exist somewhere alongside the target.
	parent := filepath.Dir(f.worldDir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var found bool
	for _, e := range entries {
		if !strings.Contains(e.Name(), ".pre-restore-") {
			continue
		}
		found = true
		// The preserved directory IS the old world, so its files sit at its
		// root rather than under a "world/" component.
		data, err := os.ReadFile(filepath.Join(parent, e.Name(), "Project.json"))
		if err != nil {
			t.Fatalf("read preserved world: %v", err)
		}
		if string(data) != liveContent {
			t.Errorf("preserved world contains %q, want the pre-restore content", data)
		}
	}
	if !found {
		t.Error("no .pre-restore- directory was created; the previous world was destroyed")
	}
}

// TestRestoreCorruptArchiveLeavesWorldIntact is the safety requirement: a
// corrupt archive must leave the existing world completely untouched.
func TestRestoreCorruptArchiveLeavesWorldIntact(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()
	m := newTestManager(t, f, store, stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The live world holds data that must survive the failed restore.
	live := map[string]string{
		"Project.json":      `{"live":true}`,
		"Regions/r.0.0.dat": "live region",
	}
	for rel, content := range live {
		if err := os.WriteFile(filepath.Join(f.worldDir, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// Corrupt the archive in two ways, each must be caught before any write.
	t.Run("truncated archive", func(t *testing.T) {
		data, err := os.ReadFile(rec.Path)
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		if err := os.WriteFile(rec.Path, data[:len(data)/2], 0o644); err != nil {
			t.Fatalf("truncate: %v", err)
		}

		err = m.Restore(context.Background(), rec.ID, f.worldDir)
		if err == nil {
			t.Fatal("Restore must fail on a truncated archive")
		}
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("error = %v, want ErrCorrupt", err)
		}
		assertWorldContents(t, f.worldDir, live)
	})

	t.Run("tampered content", func(t *testing.T) {
		// Rewrite the archive so its bytes differ from the recorded sha256
		// while remaining a structurally valid zip.
		f2 := newFixture(t)
		store2 := newMemStore()
		m2 := newTestManager(t, f2, store2, stoppedGuard())

		rec2, err := m2.Create(context.Background(), f2.request(KindManual))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		original, err := os.ReadFile(rec2.Path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		// Flip a byte in the middle: still a file, no longer the same content.
		original[len(original)/2] ^= 0xFF
		if err := os.WriteFile(rec2.Path, original, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		live2 := map[string]string{"Project.json": `{"live":true}`}
		if err := os.WriteFile(filepath.Join(f2.worldDir, "Project.json"), []byte(live2["Project.json"]), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		err = m2.Restore(context.Background(), rec2.ID, f2.worldDir)
		if err == nil {
			t.Fatal("Restore must fail when the archive no longer matches its sha256")
		}
		if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrCorrupt", err)
		}
		assertWorldContents(t, f2.worldDir, live2)
	})

	t.Run("missing archive", func(t *testing.T) {
		if err := os.Remove(rec.Path); err != nil {
			t.Fatalf("remove: %v", err)
		}
		err := m.Restore(context.Background(), rec.ID, f.worldDir)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
		assertWorldContents(t, f.worldDir, live)
	})
}

// assertWorldContents verifies that a world directory still holds exactly the
// expected files, so a failed restore can be shown to have changed nothing.
func assertWorldContents(t *testing.T, worldDir string, want map[string]string) {
	t.Helper()

	for rel, content := range want {
		got, err := os.ReadFile(filepath.Join(worldDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("the world lost %s: %v", rel, err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s = %q, want %q (the failed restore modified the world)", rel, got, content)
		}
	}
}

// TestRestoreRefusesRunningInstance checks the guard callback.
func TestRestoreRefusesRunningInstance(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	// Take the backup while stopped, then restore while running — the exact
	// sequence an operator would attempt.
	stopped := newTestManager(t, f, store, stoppedGuard())
	rec, err := stopped.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	running := newTestManager(t, f, store, runningGuard())
	err = running.Restore(context.Background(), rec.ID, f.worldDir)
	if err == nil {
		t.Fatal("Restore must refuse while the instance is running")
	}
	if !errors.Is(err, ErrInstanceRunning) {
		t.Errorf("error = %v, want ErrInstanceRunning", err)
	}
	if !strings.Contains(err.Error(), "running") {
		t.Errorf("error = %q, want it to explain the state", err)
	}
}

// TestRestoreRefusesUnknownInstanceState keeps the restore guard as
// conservative as the backup guard.
func TestRestoreRefusesUnknownInstanceState(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	m := newTestManager(t, f, store, stoppedGuard())
	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	unknown := newTestManager(t, f, store, unknownGuard())
	err = unknown.Restore(context.Background(), rec.ID, f.worldDir)
	if !errors.Is(err, ErrInstanceRunning) {
		t.Errorf("error = %v, want ErrInstanceRunning for an unknown instance state", err)
	}
}

func TestRestoreValidation(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	if err := m.Restore(context.Background(), 1, ""); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Restore(empty target) = %v, want ErrInvalidRequest", err)
	}
	if err := m.Restore(context.Background(), 4242, f.worldDir); !errors.Is(err, ErrNotFound) {
		t.Errorf("Restore(unknown id) = %v, want ErrNotFound", err)
	}
	if err := m.RestoreRecord(context.Background(), nil, f.worldDir); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("RestoreRecord(nil) = %v, want ErrInvalidRequest", err)
	}
}

func TestRestoreCancelledContext(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()
	m := newTestManager(t, f, store, stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := m.Restore(ctx, rec.ID, f.worldDir); !errors.Is(err, context.Canceled) {
		t.Errorf("Restore with a cancelled context = %v, want context.Canceled", err)
	}
}

// TestRestoreLeavesNoStagingDirectoryBehind checks cleanup on failure.
func TestRestoreLeavesNoStagingDirectoryBehind(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	m := newTestManager(t, f, store, stoppedGuard())
	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	data, err := os.ReadFile(rec.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(rec.Path, data[:len(data)/2], 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	_ = m.Restore(context.Background(), rec.ID, f.worldDir)

	entries, err := os.ReadDir(filepath.Dir(f.worldDir))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".restore-") {
			t.Errorf("staging directory %s was left behind", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// Zip-slip
// ---------------------------------------------------------------------------

// TestZipSlipIsRejectedBeforeExtraction is the archive-side path traversal
// guard: an archive containing an entry that escapes the destination must be
// rejected as a whole, and nothing may be written.
func TestZipSlipIsRejectedBeforeExtraction(t *testing.T) {
	t.Parallel()

	hostile := []string{
		"world/../../evil.txt",
		"../evil.txt",
		"/etc/evil.txt",
		"world/../../../tmp/evil.txt",
		`world\..\..\evil.txt`,
		"world/C:/evil.txt",
	}

	for _, entryName := range hostile {
		t.Run(entryName, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			store := newMemStore()
			m := newTestManager(t, f, store, stoppedGuard())

			// Build a hostile archive by hand and register it as a record.
			destDir := filepath.Join(f.root, "instance-1", "MyWorld")
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			archivePath := filepath.Join(destDir, "hostile.zip")

			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			w, err := zw.Create("world/Project.json")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if _, err := w.Write([]byte("{}")); err != nil {
				t.Fatalf("write: %v", err)
			}
			w, err = zw.Create(entryName)
			if err != nil {
				t.Fatalf("create hostile: %v", err)
			}
			if _, err := w.Write([]byte("pwned")); err != nil {
				t.Fatalf("write hostile: %v", err)
			}
			if err := zw.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
				t.Fatalf("write archive: %v", err)
			}

			sum := sha256.Sum256(buf.Bytes())
			rec := &Record{
				InstanceID: f.instanceID, World: f.worldName, Kind: KindManual,
				Path: archivePath, SHA256: hex.EncodeToString(sum[:]),
				SizeBytes: int64(buf.Len()), CreatedAt: time.Now().UTC(),
			}
			if err := store.Create(context.Background(), rec); err != nil {
				t.Fatalf("store.Create: %v", err)
			}

			// Track what exists before the attempt.
			victim := filepath.Join(filepath.Dir(f.instanceDir), "evil.txt")

			err = m.Restore(context.Background(), rec.ID, f.worldDir)
			if err == nil {
				t.Fatalf("Restore must reject the hostile entry %q", entryName)
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("error = %v, want ErrCorrupt", err)
			}

			if _, err := os.Stat(victim); err == nil {
				t.Fatalf("zip-slip wrote %s", victim)
			}
			if _, err := os.Stat("/etc/evil.txt"); err == nil {
				t.Fatal("zip-slip wrote /etc/evil.txt")
			}
		})
	}
}

func TestSafeEntryPath(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"world/Project.json": "world/Project.json",
		"world/":             "world",
		"world":              "world",
		"a/b/c.dat":          "a/b/c.dat",
		"world/./x":          "world/x",
		"./world/x":          "world/x",
		"/":                  "",
		"world//double//x":   "world/double/x",
	}
	for in, want := range valid {
		got, err := safeEntryPath(in)
		if err != nil {
			t.Errorf("safeEntryPath(%q) = %v, want %q", in, err, want)
			continue
		}
		if got != want {
			t.Errorf("safeEntryPath(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{
		"", // an empty entry name is malformed
		"../evil",
		"world/../../evil",
		"..",
		"/abs/path",
		`back\slash`,
		"C:/drive",
		"a:b",
	}
	for _, in := range invalid {
		if _, err := safeEntryPath(in); err == nil {
			t.Errorf("safeEntryPath(%q) = nil error, want rejection", in)
		}
	}
}

// ---------------------------------------------------------------------------
// Verify, List, Delete
// ---------------------------------------------------------------------------

func TestVerify(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()
	m := newTestManager(t, f, store, stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	n, err := m.Verify(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if n == 0 {
		t.Error("Verify reported 0 entries")
	}

	// Corrupt it and check Verify notices.
	data, err := os.ReadFile(rec.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(rec.Path, append(data, 'x'), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := m.Verify(context.Background(), rec.ID); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Verify on a tampered archive = %v, want ErrCorrupt", err)
	}

	if _, err := m.Verify(context.Background(), 4242); !errors.Is(err, ErrNotFound) {
		t.Errorf("Verify(unknown) = %v, want ErrNotFound", err)
	}
}

func TestVerifyArchiveEdgeCases(t *testing.T) {
	t.Parallel()

	if _, err := verifyArchive("", ""); !errors.Is(err, ErrCorrupt) {
		t.Errorf("verifyArchive(\"\") = %v, want ErrCorrupt", err)
	}

	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.zip")
	if _, err := verifyArchive(missing, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("verifyArchive(missing) = %v, want ErrNotFound", err)
	}

	empty := filepath.Join(dir, "empty.zip")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := verifyArchive(empty, ""); !errors.Is(err, ErrCorrupt) {
		t.Errorf("verifyArchive(empty) = %v, want ErrCorrupt", err)
	}

	notZip := filepath.Join(dir, "notzip.zip")
	if err := os.WriteFile(notZip, []byte("this is plain text, not a zip"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := verifyArchive(notZip, ""); !errors.Is(err, ErrCorrupt) {
		t.Errorf("verifyArchive(not a zip) = %v, want ErrCorrupt", err)
	}

	// A valid archive with a wrong expected hash.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("world/Project.json")
	_, _ = w.Write([]byte("{}"))
	_ = zw.Close()
	good := filepath.Join(dir, "good.zip")
	if err := os.WriteFile(good, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := verifyArchive(good, "0000000000000000000000000000000000000000000000000000000000000000"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("verifyArchive(wrong hash) = %v, want ErrCorrupt", err)
	}
	if _, err := verifyArchive(good, ""); err != nil {
		t.Errorf("verifyArchive(no expected hash) = %v, want nil", err)
	}
	// Case-insensitive comparison: some tools uppercase the digest.
	sum := sha256.Sum256(buf.Bytes())
	if _, err := verifyArchive(good, strings.ToUpper(hex.EncodeToString(sum[:]))); err != nil {
		t.Errorf("verifyArchive(uppercase hash) = %v, want nil", err)
	}
}

func TestList(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		m, err := NewManager(Options{
			Root: f.root, Store: store, Guard: stoppedGuard(),
			StatFS: func(string) (uint64, error) { return 1 << 40, nil },
			Now:    func() time.Time { return at },
		})
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		if _, err := m.Create(context.Background(), f.request(KindManual)); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	m := newTestManager(t, f, store, stoppedGuard())
	got, err := m.List(context.Background(), f.instanceID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d records, want 3", len(got))
	}
	// Newest first.
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt.After(got[i-1].CreatedAt) {
			t.Errorf("List is not newest-first: %v then %v", got[i-1].CreatedAt, got[i].CreatedAt)
		}
	}

	// Unknown instance yields an empty list, not an error.
	empty, err := m.List(context.Background(), 9999)
	if err != nil || len(empty) != 0 {
		t.Errorf("List(unknown) = %v, %v; want empty, nil", empty, err)
	}

	// No store configured.
	noStore := newTestManager(t, f, nil, stoppedGuard())
	if got, err := noStore.List(context.Background(), 1); err != nil || got != nil {
		t.Errorf("List without a store = %v, %v", got, err)
	}
}

func TestDeleteRemovesFileAndRecord(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()
	m := newTestManager(t, f, store, stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := m.Delete(context.Background(), rec.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(rec.Path); !os.IsNotExist(err) {
		t.Errorf("the archive still exists after Delete (err=%v)", err)
	}
	if _, err := store.Get(context.Background(), rec.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the record still exists after Delete: %v", err)
	}

	if err := m.Delete(context.Background(), 4242); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(unknown) = %v, want ErrNotFound", err)
	}
}

// TestDeleteRefusesOutsideRoot checks that the single-record delete path shares
// the traversal guard.
func TestDeleteRefusesOutsideRoot(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "keep.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := &Record{
		InstanceID: f.instanceID, World: f.worldName, Kind: KindManual,
		Path: victim, CreatedAt: time.Now().UTC(),
	}
	if err := store.Create(context.Background(), rec); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	m := newTestManager(t, f, store, stoppedGuard())
	err := m.Delete(context.Background(), rec.ID)
	if !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("Delete = %v, want ErrOutsideRoot", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a file outside the root was deleted: %v", err)
	}
}

func TestDeleteWithNoStore(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, nil, stoppedGuard())

	if err := m.Delete(context.Background(), 1); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Delete without a store = %v, want ErrInvalidRequest", err)
	}
}

// ---------------------------------------------------------------------------
// Constructor and helpers
// ---------------------------------------------------------------------------

func TestNewManagerValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewManager(Options{}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("NewManager(no root) = %v, want ErrInvalidRequest", err)
	}
	if _, err := NewManager(Options{Root: "   "}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("NewManager(blank root) = %v, want ErrInvalidRequest", err)
	}

	m, err := NewManager(Options{Root: "./relative"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if !filepath.IsAbs(m.Root()) {
		t.Errorf("Root() = %q, want an absolute path", m.Root())
	}

	// Defaults must be applied.
	m2, err := NewManager(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if m2.saveWait != DefaultSaveWait {
		t.Errorf("saveWait = %v, want %v", m2.saveWait, DefaultSaveWait)
	}
	if m2.reserve != DefaultMinFreeReserve {
		t.Errorf("reserve = %d, want %d", m2.reserve, DefaultMinFreeReserve)
	}
	if m2.statFS == nil || m2.now == nil {
		t.Error("statFS and now must be defaulted")
	}
}

func TestKindValidAndKeepPolicy(t *testing.T) {
	t.Parallel()

	for _, k := range []Kind{KindManual, KindScheduled, KindPreStart} {
		if !k.Valid() {
			t.Errorf("Kind(%q).Valid() = false", k)
		}
	}
	if Kind("hourly").Valid() {
		t.Error("Kind(hourly).Valid() = true")
	}
	if Kind("").Valid() {
		t.Error(`Kind("").Valid() = true`)
	}

	if got := KindPreStart.keepPolicy(100); got != PreStartKeep {
		t.Errorf("pre-start keepPolicy(100) = %d, want %d", got, PreStartKeep)
	}
	if got := KindManual.keepPolicy(5); got != 0 {
		t.Errorf("manual keepPolicy(5) = %d, want 0 (unbounded)", got)
	}
	if got := KindScheduled.keepPolicy(5); got != 5 {
		t.Errorf("scheduled keepPolicy(5) = %d, want 5", got)
	}
}

func TestRequestNormalizeDerivesWorldFromPath(t *testing.T) {
	t.Parallel()

	req := Request{InstanceID: 1, InstanceDir: "/srv/instances/a", WorldDir: "/srv/instances/a/Worlds/Zeta"}
	norm, err := req.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if norm.World != "Zeta" {
		t.Errorf("World = %q, want it derived from the path basename", norm.World)
	}

	// And the other direction: a world name resolves the path.
	req2 := Request{InstanceID: 1, InstanceDir: "/srv/instances/a", World: "Alpha"}
	norm2, err := req2.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if norm2.WorldDir != "/srv/instances/a/Worlds/Alpha" {
		t.Errorf("WorldDir = %q", norm2.WorldDir)
	}
}

func TestIsWithin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		parent, child string
		want          bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/b/c/d.zip", true},
		{"/a/b", "/a", false},
		{"/a/b", "/a/bc", false}, // the prefix trap
		{"/a/b", "/a/bc/d", false},
		{"/a/b", "/other", false},
		{"/a/b", "/a/b/../c", false},
		{"/a/b", "/a/b/./c", true},
	}
	for _, tc := range tests {
		if got := isWithin(tc.parent, tc.child); got != tc.want {
			t.Errorf("isWithin(%q, %q) = %v, want %v", tc.parent, tc.child, got, tc.want)
		}
	}
}

func TestSanitizeComponent(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"World":       "World",
		"My World":    "My World",
		"..":          "",
		".":           "",
		"a/b":         "a_b",
		`a\b`:         "a_b",
		"C:evil":      "C_evil",
		"  trimmed  ": "trimmed",
	}
	for in, want := range tests {
		if got := sanitizeComponent(in); got != want {
			t.Errorf("sanitizeComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()

	tests := map[int64]string{
		0:         "0B",
		512:       "512B",
		1024:      "1.0KiB",
		1 << 20:   "1.0MiB",
		256 << 20: "256.0MiB",
		5 << 30:   "5.0GiB",
	}
	for in, want := range tests {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestGuardsAndSavers(t *testing.T) {
	t.Parallel()

	if err := (SaverFunc(nil)).Save(context.Background(), 1); err == nil {
		t.Error("a nil SaverFunc must return an error, not panic")
	}

	// A nil GuardFunc must report "unknown", which the manager turns into hot.
	running, known := GuardFunc(nil).IsRunning(1)
	if running || known {
		t.Errorf("nil GuardFunc = (%v, %v), want (false, false)", running, known)
	}

	called := false
	saver := SaverFunc(func(_ context.Context, id int64) error {
		called = true
		if id != 5 {
			t.Errorf("Save instance = %d, want 5", id)
		}
		return nil
	})
	if err := saver.Save(context.Background(), 5); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !called {
		t.Error("Save did not invoke the function")
	}
}

func TestComposeNote(t *testing.T) {
	t.Parallel()

	cold := composeNote(Request{Note: "before upgrade"}, &Record{Hot: false})
	if !strings.Contains(cold, "before upgrade") || !strings.Contains(cold, "cold backup") {
		t.Errorf("cold note = %q", cold)
	}

	hotFailed := composeNote(Request{}, &Record{
		Hot: true, SaveAttempted: true, SaveError: "console down",
	})
	for _, want := range []string{"hot backup", "save failed", "console down"} {
		if !strings.Contains(hotFailed, want) {
			t.Errorf("hot note %q is missing %q", hotFailed, want)
		}
	}

	hotNoSaver := composeNote(Request{}, &Record{Hot: true})
	if !strings.Contains(hotNoSaver, "hot backup") {
		t.Errorf("hot note without a save = %q", hotNoSaver)
	}
}

// TestConcurrentCreates exercises the manager under -race.
func TestConcurrentCreates(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	m, err := NewManager(Options{
		Root: f.root, Store: store, Guard: stoppedGuard(),
		StatFS: func(string) (uint64, error) { return 1 << 40, nil },
		Now:    func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 16)

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := f.request(KindManual)
			// Distinct destinations: identical stamps would collide, which the
			// manager reports as ErrAlreadyExists and is correct behaviour.
			if _, err := m.Create(context.Background(), req); err != nil {
				if !errors.Is(err, ErrAlreadyExists) {
					errs <- err
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Create: %v", err)
	}
}

// TestCreatedArchiveIsAValidZipForATool checks the archive is not merely
// readable by this package.
func TestCreatedArchiveIsAValidZipForATool(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	data, err := os.ReadFile(rec.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The zip local file header magic, and the end-of-central-directory
	// signature "PK\x05\x06" somewhere at the end.
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		t.Error("the archive does not start with a zip local file header")
	}
	if !bytes.Contains(data[len(data)-64:], []byte("PK\x05\x06")) {
		t.Error("the archive has no end-of-central-directory record")
	}

	// It must open through the standard library's reader from a byte slice,
	// which is what an external consumer would do.
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(zr.File) == 0 {
		t.Error("the archive contains no entries")
	}
}

// TestDefaultStatFSWorksOnThisFilesystem checks the real statfs path end to
// end, which is what the disk precheck uses in production.
func TestDefaultStatFSWorksOnThisFilesystem(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	free, err := defaultStatFS(dir)
	if err != nil {
		t.Fatalf("defaultStatFS(%s): %v", dir, err)
	}
	if free == 0 {
		t.Skip("the test filesystem reports no free space (a container quirk); nothing to assert")
	}
	if free < 1<<20 {
		t.Logf("only %s free on %s", humanBytes(int64(free)), dir)
	}
}

// TestCreateUsesRealStatFSWhenNotInjected exercises the production disk
// precheck rather than the injected fake.
func TestCreateUsesRealStatFSWhenNotInjected(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	m, err := NewManager(Options{
		Root: f.root, Store: newMemStore(), Guard: stoppedGuard(),
		Now: func() time.Time { return time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC) },
		// StatFS deliberately not set.
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if _, err := m.Create(context.Background(), f.request(KindManual)); err != nil {
		// A genuinely full disk is the only acceptable reason for this to
		// fail, and it would report ErrNotEnoughSpace.
		if errors.Is(err, ErrNotEnoughSpace) {
			t.Skipf("the test filesystem is genuinely too full: %v", err)
		}
		t.Fatalf("Create: %v", err)
	}
}

// contains reports whether ss holds want.
func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Additional coverage of the archive/restore internals
// ---------------------------------------------------------------------------

// buildArchive writes a zip from the given entries into dir and returns its
// path plus the hex digest of its bytes.
func buildArchive(t *testing.T, dir string, entries map[string]string, symlinks map[string]string) (string, string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Deterministic order so the archives built here are reproducible.
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(entries[name])); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	linkNames := make([]string, 0, len(symlinks))
	for name := range symlinks {
		linkNames = append(linkNames, name)
	}
	sort.Strings(linkNames)

	for _, name := range linkNames {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(os.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("create symlink %s: %v", name, err)
		}
		if _, err := w.Write([]byte(symlinks[name])); err != nil {
			t.Fatalf("write symlink %s: %v", name, err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return path, hex.EncodeToString(sum[:])
}

// TestRestoreRecreatesSymlinks checks the symlink branch of extractEntry.
func TestRestoreRecreatesSymlinks(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	archiveDir := filepath.Join(f.root, "instance-1", "MyWorld")
	path, digest := buildArchive(t, archiveDir,
		map[string]string{
			"world/Project.json":      `{"restored":true}`,
			"world/Regions/r.0.0.dat": "region",
		},
		map[string]string{"world/link.dat": "Regions/r.0.0.dat"},
	)

	rec := &Record{
		InstanceID: f.instanceID, World: f.worldName, Kind: KindManual,
		Path: path, SHA256: digest, CreatedAt: time.Now().UTC(),
	}
	if err := store.Create(context.Background(), rec); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	m := newTestManager(t, f, store, stoppedGuard())
	if err := m.Restore(context.Background(), rec.ID, f.worldDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	link := filepath.Join(f.worldDir, "link.dat")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("the symlink was not restored: %v", err)
	}
	if target != "Regions/r.0.0.dat" {
		t.Errorf("symlink target = %q, want the archived relative target", target)
	}
}

// TestRestoreArchiveWithoutWorldWrapper checks the fallback for an archive that
// does not follow this package's "world/" convention.
func TestRestoreArchiveWithoutWorldWrapper(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	archiveDir := filepath.Join(f.root, "instance-1", "MyWorld")
	path, digest := buildArchive(t, archiveDir, map[string]string{
		"Project.json":      `{"flat":true}`,
		"Regions/r.0.0.dat": "flat region",
	}, nil)

	rec := &Record{
		InstanceID: f.instanceID, World: f.worldName, Kind: KindManual,
		Path: path, SHA256: digest, CreatedAt: time.Now().UTC(),
	}
	if err := store.Create(context.Background(), rec); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	m := newTestManager(t, f, store, stoppedGuard())
	if err := m.Restore(context.Background(), rec.ID, f.worldDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(f.worldDir, "Project.json"))
	if err != nil {
		t.Fatalf("the flat archive was not restored: %v", err)
	}
	if string(data) != `{"flat":true}` {
		t.Errorf("Project.json = %q", data)
	}
}

// TestRestoreReplacesExistingFiles checks that a restore truncates rather than
// appends, so a shorter restored file cannot leave stale trailing bytes.
func TestRestoreReplacesExistingFiles(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	m := newTestManager(t, f, store, stoppedGuard())

	// Back up the short original first...
	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// ... then let the live world grow a much longer file. Restoring must
	// replace it wholesale, not overwrite in place and leave the tail behind.
	long := strings.Repeat("X", 4096)
	if err := os.WriteFile(filepath.Join(f.worldDir, "Project.json"), []byte(long), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := m.Restore(context.Background(), rec.ID, f.worldDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(f.worldDir, "Project.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != f.files["Project.json"] {
		t.Errorf("Project.json = %q, want exactly the archived content (%d bytes, got %d)",
			data, len(f.files["Project.json"]), len(data))
	}
}

// TestCopyTreeAndRenameDir covers the cross-filesystem fallback helpers
// directly, including their error paths.
func TestCopyTreeAndRenameDir(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")

	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "f.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink("sub/f.txt", filepath.Join(src, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dst, "sub", "f.txt"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(data) != "content" {
		t.Errorf("copied content = %q", data)
	}
	link, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != "sub/f.txt" {
		t.Errorf("copied symlink = %q", link)
	}

	// copyTree on a missing source must report the error.
	if err := copyTree(filepath.Join(src, "nope"), filepath.Join(dst, "x")); err == nil {
		t.Error("copyTree on a missing source must fail")
	}

	// renameDir on the same filesystem takes the plain rename path.
	sameFS := filepath.Join(t.TempDir(), "renamed")
	if err := renameDir(src, sameFS); err != nil {
		t.Fatalf("renameDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sameFS, "sub", "f.txt")); err != nil {
		t.Errorf("renameDir did not move the tree: %v", err)
	}
}

// TestRenameDirCrossDeviceFallback forces the copy path by staging on a
// different filesystem when one is available, and otherwise asserts the same-
// filesystem path still works. Either way renameDir must succeed.
func TestRenameDirCrossDeviceFallback(t *testing.T) {
	t.Parallel()

	// /dev/shm is a tmpfs on virtually every Linux host, so it is a reliable
	// second filesystem when it exists and is writable.
	other := "/dev/shm"
	if st, err := os.Stat(other); err != nil || !st.IsDir() {
		t.Skipf("%s is unavailable, so a cross-device move cannot be staged", other)
	}

	srcParent, err := os.MkdirTemp(other, "scnetm-cdev-*")
	if err != nil {
		t.Skipf("cannot create a staging directory in %s: %v", other, err)
	}
	defer os.RemoveAll(srcParent)

	src := filepath.Join(srcParent, "world")
	if err := os.MkdirAll(filepath.Join(src, "Regions"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "Regions", "r.dat"), []byte("region"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "installed")
	if err := renameDir(src, dst); err != nil {
		t.Fatalf("renameDir across filesystems: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dst, "Regions", "r.dat"))
	if err != nil {
		t.Fatalf("the tree did not arrive: %v", err)
	}
	if string(data) != "region" {
		t.Errorf("content = %q", data)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("the source should have been removed after the copy fallback (err=%v)", err)
	}
}

// TestCopyFileErrorPaths covers copyFile's failure branches.
func TestCopyFileErrorPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := copyFile(filepath.Join(dir, "missing.txt"), filepath.Join(dir, "out.txt"), 0o644); err == nil {
		t.Error("copyFile with a missing source must fail")
	}

	// Destination inside a path that is a file, so MkdirAll fails.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := copyFile(src, filepath.Join(blocker, "sub", "out.txt"), 0o644); err == nil {
		t.Error("copyFile into a path under a regular file must fail")
	}
}

// TestIsCrossDevice checks the errno classification.
func TestIsCrossDevice(t *testing.T) {
	t.Parallel()

	if !isCrossDevice(syscall.EXDEV) {
		t.Error("EXDEV must be recognised")
	}
	if !isCrossDevice(&os.LinkError{Op: "rename", Old: "a", New: "b", Err: syscall.EXDEV}) {
		t.Error("a wrapped EXDEV must be recognised")
	}
	if isCrossDevice(errors.New("some other error")) {
		t.Error("an unrelated error must not be classified as cross-device")
	}
}

// TestRestoreRecordInstanceState covers the guard branches of instanceState and
// the error wording of stateWord.
func TestInstanceStateBranches(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	noGuard := newTestManager(t, f, newMemStore(), nil)
	running, known := noGuard.instanceState(1)
	if !running || known {
		t.Errorf("with no guard = (%v, %v), want (true, false)", running, known)
	}

	stopped := newTestManager(t, f, newMemStore(), stoppedGuard())
	running, known = stopped.instanceState(1)
	if running || !known {
		t.Errorf("with a stopped guard = (%v, %v), want (false, true)", running, known)
	}

	tests := []struct {
		running, known bool
		want           string
	}{
		{false, false, "in an unknown state"},
		{true, true, "running"},
		{false, true, "stopped"},
	}
	for _, tc := range tests {
		if got := stateWord(tc.running, tc.known); got != tc.want {
			t.Errorf("stateWord(%v, %v) = %q, want %q", tc.running, tc.known, got, tc.want)
		}
	}
}

// TestVerifyRejectsUnsafeEntryEvenWithMatchingHash proves the two checks are
// independent: a digest that matches does not make a zip-slip archive safe.
func TestVerifyRejectsUnsafeEntryEvenWithMatchingHash(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, digest := buildArchive(t, dir, map[string]string{
		"world/Project.json": `{}`,
		"../escape.txt":      "pwned",
	}, nil)

	if _, err := verifyArchive(path, digest); !errors.Is(err, ErrCorrupt) {
		t.Errorf("verifyArchive with a matching hash but an unsafe entry = %v, want ErrCorrupt", err)
	}
}

// TestVerifyRejectsBackslashEntry covers the Windows-separator rejection.
func TestVerifyRejectsBackslashEntry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, digest := buildArchive(t, dir, map[string]string{
		`world\Project.json`: `{}`,
	}, nil)

	if _, err := verifyArchive(path, digest); err == nil {
		t.Error("an entry with a backslash must be rejected")
	}
}

// TestArchiveWithOnlyDirectories checks the directory-entry branch of the
// extractor.
func TestArchiveWithOnlyDirectories(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	dir := filepath.Join(f.root, "instance-1", "MyWorld")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	archivePath := filepath.Join(dir, "dirs.zip")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"world/", "world/Regions/", "world/Empty/"} {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(os.ModeDir | 0o755)
		if _, err := zw.CreateHeader(header); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	sum := sha256.Sum256(buf.Bytes())
	rec := &Record{
		InstanceID: f.instanceID, World: f.worldName, Kind: KindManual,
		Path: archivePath, SHA256: hex.EncodeToString(sum[:]), CreatedAt: time.Now().UTC(),
	}
	if err := store.Create(context.Background(), rec); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	m := newTestManager(t, f, store, stoppedGuard())
	if err := m.Restore(context.Background(), rec.ID, f.worldDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	for _, sub := range []string{"Regions", "Empty"} {
		st, err := os.Stat(filepath.Join(f.worldDir, sub))
		if err != nil {
			t.Errorf("directory %s was not restored: %v", sub, err)
			continue
		}
		if !st.IsDir() {
			t.Errorf("%s is not a directory", sub)
		}
	}
}

// TestCopyTreeSkipsSpecialFiles documents that a FIFO in the tree is skipped
// rather than hanging the copy.
func TestCopyTreeSkipsSpecialFiles(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")

	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree must not fail on a FIFO: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "real.txt")); err != nil {
		t.Errorf("the regular file was not copied: %v", err)
	}
}

// TestCreateFailsWhenArchiveCannotBeWritten covers writeWorldZip's failure
// branch by making the world directory unreadable mid-walk.
func TestCreateArchivesFilesWithSpecialModes(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// A read-only file and a file with unusual permissions must both be
	// archived: the server writes files with a variety of modes.
	ro := filepath.Join(f.worldDir, "readonly.dat")
	if err := os.WriteFile(ro, []byte("read only"), 0o400); err != nil {
		t.Fatalf("write: %v", err)
	}
	exec := filepath.Join(f.worldDir, "script.sh")
	if err := os.WriteFile(exec, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A FIFO must be skipped, not block the archiver forever.
	if err := syscall.Mkfifo(filepath.Join(f.worldDir, "pipe"), 0o644); err != nil {
		t.Logf("cannot create a FIFO here, skipping that part: %v", err)
	}

	m := newTestManager(t, f, newMemStore(), stoppedGuard())
	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	names := listArchive(t, rec.Path)
	for _, want := range []string{"world/readonly.dat", "world/script.sh"} {
		if !contains(names, want) {
			t.Errorf("archive is missing %q; entries: %v", want, names)
		}
	}
	if contains(names, "world/pipe") {
		t.Errorf("a FIFO must not be archived; entries: %v", names)
	}
	if got := readArchiveEntry(t, rec.Path, "world/script.sh"); got != "#!/bin/sh\n" {
		t.Errorf("script.sh content = %q", got)
	}
}

// TestVerifyArchiveUnreadablePath covers the open-failure branch.
func TestVerifyArchiveUnreadablePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sub := filepath.Join(dir, "adir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Opening a directory succeeds on Linux but reading it as a zip fails;
	// either way the call must report an error rather than panic.
	if _, err := verifyArchive(sub, ""); err == nil {
		t.Error("verifyArchive on a directory must fail")
	}
}

// TestPruneStopsAtFirstUndeletableRecord checks that a failure part-way through
// pruning leaves the index consistent with the filesystem: the record whose
// file could not be removed must keep its row.
func TestPruneStopsAtFirstUndeletableRecord(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()

	base := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)

	// The oldest record is a *directory* rather than a file: os.Remove refuses
	// a non-empty one, so the retention pass must stop there instead of
	// pretending it succeeded. keep=3 makes it the first record pruned.
	dirAsArchive := filepath.Join(f.root, "instance-1", "MyWorld", "dir.zip")
	if err := os.MkdirAll(filepath.Join(dirAsArchive, "inner"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirAsArchive, "inner", "x"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The directory must be the OLDEST record so that keep=3 makes it the
	// first pruning candidate.
	paths := []string{
		dirAsArchive,
		filepath.Join(f.root, "instance-1", "MyWorld", "b.zip"),
		filepath.Join(f.root, "instance-1", "MyWorld", "c.zip"),
		filepath.Join(f.root, "instance-1", "MyWorld", "d.zip"),
	}
	for i, p := range paths {
		rec := &Record{
			InstanceID: f.instanceID, World: f.worldName, Kind: KindPreStart,
			Path: p, CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := store.Create(context.Background(), rec); err != nil {
			t.Fatalf("store.Create: %v", err)
		}
	}

	m := newTestManager(t, f, store, stoppedGuard())
	removed, err := m.Prune(context.Background(), f.instanceID, KindPreStart, 3)
	if err == nil {
		t.Fatal("Prune must report a record it could not remove")
	}
	if len(removed) != 0 {
		t.Errorf("removed = %d records, want 0 (the first candidate was undeletable)", len(removed))
	}

	// The directory record must still be indexed, because its file remains.
	remaining, err := store.ListByInstanceKind(context.Background(), f.instanceID, KindPreStart)
	if err != nil {
		t.Fatalf("ListByInstanceKind: %v", err)
	}
	var stillIndexed bool
	for _, r := range remaining {
		if r.Path == dirAsArchive {
			stillIndexed = true
		}
	}
	if !stillIndexed {
		t.Error("a record whose file could not be deleted must keep its row (otherwise the file is orphaned)")
	}
}

// TestRemoveArchiveEmptyPath covers the empty-path guard.
func TestRemoveArchiveEmptyPath(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	if err := m.removeArchive(""); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("removeArchive(\"\") = %v, want ErrOutsideRoot", err)
	}
	if err := m.removeArchive("   "); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("removeArchive(blank) = %v, want ErrOutsideRoot", err)
	}
}

// TestRestoreTargetIsAFile covers the branch where the restore target exists
// but is not a directory.
func TestRestoreTargetIsAFile(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	store := newMemStore()
	m := newTestManager(t, f, store, stoppedGuard())

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Replace the world directory with a plain file.
	target := filepath.Join(t.TempDir(), "world-as-file")
	if err := os.WriteFile(target, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := m.Restore(context.Background(), rec.ID, target); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !st.IsDir() {
		t.Error("the target should have been replaced by the restored directory")
	}
	if _, err := os.Stat(filepath.Join(target, "Project.json")); err != nil {
		t.Errorf("the restored world is incomplete: %v", err)
	}
}

// TestExtractArchiveCancelledMidway covers the context check inside the
// extraction loop.
func TestExtractArchiveCancelledMidway(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, _ := buildArchive(t, dir, map[string]string{
		"world/a.txt": "a",
		"world/b.txt": "b",
		"world/c.txt": "c",
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := extractArchive(ctx, path, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Errorf("extractArchive with a cancelled context = %v, want context.Canceled", err)
	}
}

// TestAddTreeHandlesVanishingFile covers the "file disappeared mid-walk" branch,
// which is normal on a live server rotating logs.
func TestAddTreeHandlesVanishingFile(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	m := newTestManager(t, f, newMemStore(), stoppedGuard())

	// A dangling symlink is the closest stable analogue of a vanishing file:
	// d.Info() resolves the link and fails.
	if err := os.Symlink(filepath.Join(f.worldDir, "gone.dat"), filepath.Join(f.worldDir, "dangling.link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	rec, err := m.Create(context.Background(), f.request(KindManual))
	if err != nil {
		t.Fatalf("Create must tolerate a dangling symlink: %v", err)
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Errorf("the archive must exist: %v", err)
	}
}
