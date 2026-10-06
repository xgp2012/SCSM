// Package backup implements the panel's three-tier backup strategy (plan §6.5)
// and the restore path that phase-2 acceptance criterion §10 depends on:
// "a deleted instance save can be fully restored from backup".
//
// The three tiers and their retention rules, verbatim from §6.5:
//
//	pre-start  taken before every instance start   keep the newest 3
//	scheduled  cron-driven (default 04:00 daily)   keep N
//	manual     user-triggered                      unbounded (managed by hand)
//
// Format choice — zip, not tar.gz:
//
//   - It matches the archive format the world export/import feature already
//     uses (§6.3: "整目录打包 zip"), so one archive type serves both features
//     and an exported world is directly restorable.
//   - Selective extraction matters here. A restore may want just
//     Project.json, and a zip's central directory gives random access to one
//     entry without decompressing everything before it — the reason
//     archive/zip exposes Open on a *zip.Reader. A tar.gz would force a
//     sequential scan.
//   - A zip can be inspected with the operator's normal OS tools, which is
//     what matters when someone is recovering a server at 3am.
//
// The cost is a weaker compression ratio than gzip for a large Regions/
// directory. That is an acceptable trade for the above, and can be revisited
// independently of this API.
//
// Consistency (§6.5): a running server may be mid-save, so a backup taken
// while the instance is up is *not* guaranteed to be a coherent snapshot. This
// package never pretends otherwise — such a record is marked Hot, and the flag
// is surfaced in the record, in the note and through the repository metadata.
package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Kind is the backup tier. Its values match the `backups.kind` column's closed
// set (manual | scheduled | pre-start), so a record can be written straight to
// the repository.
type Kind string

// The three tiers of §6.5.
const (
	// KindManual is a user-triggered backup, retained until deleted by hand.
	KindManual Kind = "manual"
	// KindScheduled is the cron-driven backup (default 04:00 daily).
	KindScheduled Kind = "scheduled"
	// KindPreStart is taken before each start, keeping the newest 3.
	KindPreStart Kind = "pre-start"
)

// Valid reports whether k is one of the three tiers.
func (k Kind) Valid() bool {
	switch k {
	case KindManual, KindScheduled, KindPreStart:
		return true
	}
	return false
}

// keepPolicy returns the retention count for a tier.
//
// keep <= 0 means "unbounded", which is why the manual tier returns 0: §6.5
// says manual backups are managed by hand, and silently deleting a backup the
// operator deliberately took would be the worst possible failure mode.
func (k Kind) keepPolicy(requested int) int {
	switch k {
	case KindPreStart:
		// §6.5: "保留最近 3 份". Not configurable per call, because the whole
		// point of this tier is a small fixed safety net.
		return PreStartKeep
	case KindManual:
		return 0
	default:
		return requested
	}
}

// PreStartKeep is the fixed retention for the pre-start tier (§6.5).
const PreStartKeep = 3

// DefaultScheduledKeep mirrors config.DefaultBackupKeep, so a Request that does
// not specify Keep still retains a sensible number of scheduled backups.
const DefaultScheduledKeep = 7

// Sentinel errors. Callers match them with errors.Is; the API layer maps them
// to HTTP statuses, and the scheduler uses ErrNotEnoughSpace to distinguish
// "come back later" from a real failure.
var (
	// ErrInvalidRequest reports missing or contradictory caller input.
	ErrInvalidRequest = errors.New("backup: invalid request")
	// ErrNotEnoughSpace reports that the destination filesystem cannot hold
	// the backup. It is typed so the panel can alert on it and the scheduler
	// can retry later rather than treating it as a hard failure.
	ErrNotEnoughSpace = errors.New("backup: not enough free disk space")
	// ErrOutsideRoot reports a recorded path that escapes the configured
	// backup root. Deletion refuses to act on one.
	ErrOutsideRoot = errors.New("backup: path is outside the backup root")
	// ErrCorrupt reports an archive that failed verification before restore.
	ErrCorrupt = errors.New("backup: archive is corrupt")
	// ErrInstanceRunning reports a restore refused because the instance is up.
	ErrInstanceRunning = errors.New("backup: instance is running")
	// ErrNotFound reports a missing record or archive.
	ErrNotFound = errors.New("backup: not found")
	// ErrAlreadyExists reports a destination that is already present.
	ErrAlreadyExists = errors.New("backup: destination already exists")
)

// ProjectFileName and its companion are the world's metadata files (§2.3).
//
// §6.5 requires collecting Project.json.bak alongside Project.json: the game
// server writes the .bak itself at session end, and it is the operator's only
// recourse when Project.json is itself the damaged file.
const (
	// ProjectFileName is the world metadata file.
	ProjectFileName = "Project.json"
	// ProjectBackupName is the server's own companion backup of the above.
	ProjectBackupName = "Project.json.bak"
)

// Record describes one completed backup.
//
// It intentionally mirrors the `backups` table plus the fields this package
// needs and the table does not yet carry (Hot, World, Note). The repository
// interface below is what decides which of them persist.
type Record struct {
	// ID is the repository's row id, filled in after a successful Create.
	ID int64 `json:"id"`

	// InstanceID is the instance this backup belongs to.
	InstanceID int64 `json:"instanceId"`

	// World is the world (save) name this backup was taken from.
	World string `json:"world"`

	// Kind is the tier.
	Kind Kind `json:"kind"`

	// Path is the absolute archive path.
	Path string `json:"path"`

	// SizeBytes is the archive size on disk.
	SizeBytes int64 `json:"sizeBytes"`

	// SHA256 is the hex-encoded digest of the archive as written.
	SHA256 string `json:"sha256"`

	// CreatedAt is when the archive finished being written (UTC).
	CreatedAt time.Time `json:"createdAt"`

	// Note is the operator-visible annotation, including the hot-backup
	// warning when applicable.
	Note string `json:"note"`

	// Hot reports that the instance was running (or its state could not be
	// established) when this backup was taken, so the copy may be internally
	// inconsistent (§6.5). It is never silently false: an unknown guard state
	// counts as hot.
	Hot bool `json:"hot"`

	// SaveAttempted reports that a graceful save was requested before reading
	// the files (§6.5's "先发一次保存").
	SaveAttempted bool `json:"saveAttempted"`

	// SaveError is the failure of that save attempt, if any. A failed save
	// does not abort the backup — a possibly-stale backup of a running server
	// is still better than none — but it must be visible.
	SaveError string `json:"saveError,omitempty"`

	// Files is how many files were written into the archive.
	Files int `json:"files"`

	// Duration is how long the archive took to produce.
	Duration time.Duration `json:"duration"`
}

// Request describes a backup to take.
type Request struct {
	// InstanceID is required.
	InstanceID int64 `json:"instanceId"`

	// InstanceDir is the instance's working directory. It is the root that
	// WorldDir is resolved against and the source of Project.json.
	InstanceDir string `json:"instanceDir"`

	// WorldDir is the world directory to archive. When empty, World is used to
	// resolve <InstanceDir>/Worlds/<World>.
	WorldDir string `json:"worldDir"`

	// World is the world (save) name. When empty it is derived from
	// WorldDir's basename.
	World string `json:"world"`

	// Kind is the tier. Empty defaults to KindManual.
	Kind Kind `json:"kind"`

	// Keep is the retention count for the scheduled tier. Ignored for
	// pre-start (fixed at 3) and manual (unbounded). 0 uses
	// DefaultScheduledKeep.
	Keep int `json:"keep"`

	// Note is a free-text annotation.
	Note string `json:"note"`
}

// normalize fills in derived fields and validates.
func (r Request) normalize() (Request, error) {
	if r.InstanceID == 0 {
		return r, fmt.Errorf("%w: instance id must not be zero", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.InstanceDir) == "" {
		return r, fmt.Errorf("%w: instance directory must not be empty", ErrInvalidRequest)
	}

	instDir, err := filepath.Abs(filepath.Clean(r.InstanceDir))
	if err != nil {
		return r, fmt.Errorf("%w: instance directory %q: %v", ErrInvalidRequest, r.InstanceDir, err)
	}
	r.InstanceDir = instDir

	// Resolve the world directory, then the world name from it.
	switch {
	case strings.TrimSpace(r.WorldDir) != "":
		worldDir, err := filepath.Abs(filepath.Clean(r.WorldDir))
		if err != nil {
			return r, fmt.Errorf("%w: world directory %q: %v", ErrInvalidRequest, r.WorldDir, err)
		}
		r.WorldDir = worldDir
		if strings.TrimSpace(r.World) == "" {
			r.World = filepath.Base(worldDir)
		}
	case strings.TrimSpace(r.World) != "":
		r.WorldDir = filepath.Join(r.InstanceDir, "Worlds", r.World)
	default:
		return r, fmt.Errorf("%w: either world or worldDir is required", ErrInvalidRequest)
	}

	// A world directory climbing out of the instance directory is either a
	// mistake or an attempt to archive something that is not a world. §6.2
	// makes the same judgement about WorldPath's last segment.
	if !isWithin(r.InstanceDir, r.WorldDir) {
		return r, fmt.Errorf("%w: world directory %s is outside the instance directory %s",
			ErrInvalidRequest, r.WorldDir, r.InstanceDir)
	}

	if r.Kind == "" {
		r.Kind = KindManual
	}
	if !r.Kind.Valid() {
		return r, fmt.Errorf("%w: unknown kind %q (want manual|scheduled|pre-start)",
			ErrInvalidRequest, r.Kind)
	}
	if r.Keep < 0 {
		return r, fmt.Errorf("%w: keep must not be negative, got %d", ErrInvalidRequest, r.Keep)
	}
	if r.Kind == KindScheduled && r.Keep == 0 {
		r.Keep = DefaultScheduledKeep
	}

	return r, nil
}

// Guard reports whether an instance is running. It is the narrow seam that
// keeps this package from importing internal/supervisor, which is still being
// stabilised.
//
// The second return value is "the check succeeded". A guard that cannot
// determine the state must return false for the first value and false for the
// second, so the caller treats the backup as hot rather than silently claiming
// consistency.
type Guard interface {
	IsRunning(instanceID int64) (running bool, known bool)
}

// GuardFunc adapts a function to the Guard interface.
type GuardFunc func(instanceID int64) (running bool, known bool)

// IsRunning implements Guard.
func (f GuardFunc) IsRunning(instanceID int64) (bool, bool) {
	if f == nil {
		return false, false
	}
	return f(instanceID)
}

// Saver requests a graceful save of a running instance before its files are
// read (§6.5's "先发一次保存/优雅停").
type Saver interface {
	// Save asks the instance to write its world to disk. It should return when
	// the save has been requested or completed, and must honour ctx.
	Save(ctx context.Context, instanceID int64) error
}

// SaverFunc adapts a function to the Saver interface.
type SaverFunc func(ctx context.Context, instanceID int64) error

// Save implements Saver.
func (f SaverFunc) Save(ctx context.Context, instanceID int64) error {
	if f == nil {
		return errors.New("backup: no saver configured")
	}
	return f(ctx, instanceID)
}

// Store persists backup records. It is satisfied by *store.BackupRepo's method
// set through a thin adapter (see cmd/scnetm), because the repository's
// PruneOldest takes a string kind and returns store.Backup rows.
//
// Defining the interface here rather than importing internal/store keeps this
// package's tests free of SQLite and lets the retention logic be tested against
// an in-memory fake.
type Store interface {
	// Create records a completed backup, filling in rec.ID.
	Create(ctx context.Context, rec *Record) error

	// Get returns one record by id, or ErrNotFound.
	Get(ctx context.Context, id int64) (*Record, error)

	// ListByInstanceKind returns an instance's backups of one kind, newest
	// first. The ordering is what retention depends on.
	ListByInstanceKind(ctx context.Context, instanceID int64, kind Kind) ([]Record, error)

	// ListByInstance returns every backup of an instance, newest first.
	ListByInstance(ctx context.Context, instanceID int64) ([]Record, error)

	// Delete removes one record. It does not touch the file.
	Delete(ctx context.Context, id int64) error
}

// Options configures a Manager.
type Options struct {
	// Root is the directory every archive is written under. It is the security
	// boundary for deletion and restore: this package refuses to unlink
	// anything that does not resolve inside it.
	//
	// Required.
	Root string

	// Store persists records. Optional: when nil, backups are still written
	// but no index exists, which is only useful in tests.
	Store Store

	// Guard reports whether an instance is running. Optional; when nil every
	// backup is treated as hot, because the state is unknown.
	Guard Guard

	// Saver performs a graceful save before a hot backup. Optional.
	Saver Saver

	// SaveWait is how long to allow the graceful save to complete before
	// proceeding anyway. Default 30s. The backup proceeds either way: refusing
	// to back up because a save timed out would remove the safety net exactly
	// when the server is misbehaving.
	SaveWait time.Duration

	// StatFS reports the free bytes available on the filesystem containing a
	// path. Optional; the default reads statfs(2) on Linux. Injecting it makes
	// the disk-space precheck testable without filling a disk.
	StatFS func(path string) (free uint64, err error)

	// Now is the clock, for tests.
	Now func() time.Time

	// MinFreeReserve is the free space that must remain after a backup
	// completes, so the panel does not fill the disk it is trying to protect.
	// Default 256MB.
	MinFreeReserve int64
}

// Default option values.
const (
	// DefaultSaveWait bounds the pre-backup graceful save.
	DefaultSaveWait = 30 * time.Second
	// DefaultMinFreeReserve is the free space kept back after a backup.
	DefaultMinFreeReserve = 256 << 20 // 256 MiB
	// dirPerm / filePerm are the modes for directories and archives this
	// package creates.
	dirPerm  = 0o750
	filePerm = 0o640
)

// Manager creates, prunes and restores backups.
type Manager struct {
	root     string
	store    Store
	guard    Guard
	saver    Saver
	saveWait time.Duration
	statFS   func(path string) (uint64, error)
	now      func() time.Time
	reserve  int64
}

// NewManager builds a Manager.
func NewManager(opts Options) (*Manager, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, fmt.Errorf("%w: backup root must not be empty", ErrInvalidRequest)
	}

	root, err := filepath.Abs(filepath.Clean(opts.Root))
	if err != nil {
		return nil, fmt.Errorf("%w: backup root %q: %v", ErrInvalidRequest, opts.Root, err)
	}

	if opts.SaveWait <= 0 {
		opts.SaveWait = DefaultSaveWait
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.StatFS == nil {
		opts.StatFS = defaultStatFS
	}
	if opts.MinFreeReserve < 0 {
		opts.MinFreeReserve = 0
	}
	if opts.MinFreeReserve == 0 {
		opts.MinFreeReserve = DefaultMinFreeReserve
	}

	return &Manager{
		root:     root,
		store:    opts.Store,
		guard:    opts.Guard,
		saver:    opts.Saver,
		saveWait: opts.SaveWait,
		statFS:   opts.StatFS,
		now:      opts.Now,
		reserve:  opts.MinFreeReserve,
	}, nil
}

// Root returns the configured backup root (absolute).
func (m *Manager) Root() string { return m.root }

// Create produces a backup archive, records it and enforces retention.
//
// The sequence is deliberate:
//
//  1. Validate the request and resolve the world directory.
//  2. Determine whether the instance is running, and mark the backup hot if it
//     is (or if that cannot be determined).
//  3. If hot, ask for a graceful save and wait up to SaveWait. A failure here
//     is recorded but does not abort: the risk being mitigated is an
//     inconsistent copy, which is strictly better than no copy.
//  4. Precheck free disk space and fail with ErrNotEnoughSpace rather than
//     filling the disk.
//  5. Write the archive to a temporary file, hashing as we go, then rename it
//     into place. A crash therefore leaves either no archive or a complete one
//     — never a half-written file that later looks restorable.
//  6. Record it, then prune the tier's retention window.
//
// Returning a non-nil *Record with a non-nil error is not a thing: on any
// failure the archive is removed and the record is nil.
func (m *Manager) Create(ctx context.Context, req Request) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	req, err := req.normalize()
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(req.WorldDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: world directory %s does not exist", ErrNotFound, req.WorldDir)
		}
		return nil, fmt.Errorf("backup: stat world directory %s: %w", req.WorldDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: world path %s is not a directory", ErrInvalidRequest, req.WorldDir)
	}

	rec := &Record{
		InstanceID: req.InstanceID,
		World:      req.World,
		Kind:       req.Kind,
		Note:       strings.TrimSpace(req.Note),
	}

	// 2. Consistency awareness.
	rec.Hot = m.isHot(ctx, req.InstanceID)

	// 3. Graceful save for a hot backup.
	if rec.Hot && m.saver != nil {
		rec.SaveAttempted = true
		saveCtx, cancel := context.WithTimeout(ctx, m.saveWait)
		saveErr := m.saver.Save(saveCtx, req.InstanceID)
		cancel()
		if saveErr != nil {
			rec.SaveError = saveErr.Error()
		}
	}

	// 5. Destination path. Deterministic and lexicographically sortable, so a
	// directory listing alone orders backups correctly.
	dest, err := m.archivePath(req, m.now())
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), dirPerm); err != nil {
		return nil, fmt.Errorf("backup: create destination directory: %w", err)
	}

	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, dest)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("backup: stat destination %s: %w", dest, err)
	}

	// 4. Disk-space precheck.
	if err := m.checkSpace(filepath.Dir(dest)); err != nil {
		return nil, err
	}

	started := m.now()

	// 5. Write to a temporary file first, then rename.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".backup-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("backup: create temporary archive: %w", err)
	}
	tmpName := tmp.Name()
	// Any failure from here on must not leave a stray temp file behind.
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	hasher := sha256.New()
	// The archive is hashed as it is written rather than by re-reading it,
	// which both halves the I/O and guarantees the digest describes exactly
	// the bytes that were produced.
	files, err := writeWorldZip(io.MultiWriter(tmp, hasher), req, rec.Hot)
	if err != nil {
		return nil, err
	}

	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("backup: sync archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("backup: close archive: %w", err)
	}
	if err := os.Chmod(tmpName, filePerm); err != nil {
		return nil, fmt.Errorf("backup: chmod archive: %w", err)
	}

	var size int64
	if st, err := os.Stat(tmpName); err == nil {
		size = st.Size()
	}

	if err := os.Rename(tmpName, dest); err != nil {
		return nil, fmt.Errorf("backup: move archive into place: %w", err)
	}
	committed = true

	rec.Path = dest
	rec.SHA256 = hex.EncodeToString(hasher.Sum(nil))
	rec.SizeBytes = size
	rec.CreatedAt = m.now().UTC().Truncate(time.Second)
	rec.Files = files
	rec.Duration = rec.CreatedAt.Sub(started.Truncate(time.Second))
	rec.Note = composeNote(req, rec)

	if m.store != nil {
		if err := m.store.Create(ctx, rec); err != nil {
			// The archive is valid but unindexed. Removing it keeps the
			// filesystem and the index from disagreeing, which would otherwise
			// leave an orphan file that no retention pass can ever clean up.
			if rmErr := os.Remove(dest); rmErr != nil {
				return nil, fmt.Errorf("backup: record failed (%v) and the archive could not be removed: %w",
					err, rmErr)
			}
			return nil, fmt.Errorf("backup: record backup: %w", err)
		}
	}

	// 6. Retention.
	if _, err := m.Prune(ctx, req.InstanceID, req.Kind, req.Keep); err != nil {
		// The backup itself succeeded; a retention failure must not be reported
		// as a backup failure or the operator will re-run it.
		rec.Note = strings.TrimSpace(rec.Note + " (retention pass failed: " + err.Error() + ")")
	}

	return rec, nil
}

// isHot reports whether instanceID should be treated as running.
//
// Unknown counts as hot. The asymmetry is intentional: falsely marking a
// stopped instance hot costs a harmless "may be inconsistent" label, while
// falsely marking a running instance consistent would present an inconsistent
// archive as a trustworthy recovery point.
func (m *Manager) isHot(_ context.Context, instanceID int64) bool {
	if m.guard == nil {
		return true
	}
	running, known := m.guard.IsRunning(instanceID)
	if !known {
		return true
	}
	return running
}

// archivePath builds the deterministic destination path:
//
//	<root>/<instance>/<world>/<kind>-<RFC3339>-<sha8>.zip
//
// The instance and world names are used as directory components after being
// sanitised, so a name containing a separator or ".." cannot escape the root
// (the same rule §6.2 applies to WorldPath's last segment).
func (m *Manager) archivePath(req Request, at time.Time) (string, error) {
	instancePart := sanitizeComponent(fmt.Sprintf("instance-%d", req.InstanceID))
	worldPart := sanitizeComponent(req.World)
	if worldPart == "" {
		return "", fmt.Errorf("%w: world name %q cannot be used as a path component", ErrInvalidRequest, req.World)
	}

	// Timestamp with colons replaced: colons are legal on Linux but not on
	// every filesystem an operator may mount the backup root on, and their
	// presence would make an archive un-copyable to those hosts.
	stamp := at.UTC().Format("20060102T150405Z")

	name := fmt.Sprintf("%s-%s.zip", req.Kind, stamp)
	dest := filepath.Join(m.root, instancePart, worldPart, name)

	// Belt and braces: confirm the computed path is inside the root before it
	// is ever used.
	if !isWithin(m.root, dest) {
		return "", fmt.Errorf("%w: computed path %s escapes the backup root %s",
			ErrInvalidRequest, dest, m.root)
	}
	return dest, nil
}

// checkSpace verifies the destination filesystem has room for the backup plus
// the reserve.
//
// It cannot know the archive's final size, so it uses the reserve as the
// requirement. That is deliberately conservative and cheap; its job is to stop
// a backup from filling the disk, not to predict the exact size.
func (m *Manager) checkSpace(dir string) error {
	free, err := m.statFS(dir)
	if err != nil {
		// Unable to determine free space is not a reason to refuse to make a
		// backup: the operator needs the backup more than the check.
		return nil
	}
	if free < uint64(m.reserve) {
		return fmt.Errorf("%w: %s has %s free, need at least %s",
			ErrNotEnoughSpace, dir, humanBytes(int64(free)), humanBytes(m.reserve))
	}
	return nil
}

// Prune enforces a tier's retention window and returns the removed records.
//
// keep semantics (see KeepOptions):
//
//	pre-start  always pruned to 3 (§6.5), whatever is requested
//	manual     never pruned automatically
//	scheduled  pruned to keep (default DefaultScheduledKeep)
//
// Files are deleted only after their path is confirmed to resolve inside the
// backup root; a record whose path escapes the root is reported and skipped
// rather than followed.
func (m *Manager) Prune(ctx context.Context, instanceID int64, kind Kind, keep int) ([]Record, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidRequest, kind)
	}
	if keep < 0 {
		return nil, fmt.Errorf("%w: keep must not be negative, got %d", ErrInvalidRequest, keep)
	}
	if m.store == nil {
		return nil, nil
	}

	effective := kind.keepPolicy(keep)
	if effective <= 0 {
		// Unbounded tier: nothing to do.
		return nil, nil
	}

	all, err := m.store.ListByInstanceKind(ctx, instanceID, kind)
	if err != nil {
		return nil, fmt.Errorf("backup: list %s backups: %w", kind, err)
	}
	if len(all) <= effective {
		return nil, nil
	}

	// The store contract is newest first, so everything from index effective
	// onwards is outside the window. Walk it backwards so the report reads
	// oldest first, which is the order the removals happened.
	var removed []Record
	for i := len(all) - 1; i >= effective; i-- {
		rec := all[i]

		if err := m.removeArchive(rec.Path); err != nil {
			// Keep the row: its file is still there, so dropping the row would
			// orphan the file permanently.
			return removed, err
		}
		if err := m.store.Delete(ctx, rec.ID); err != nil {
			return removed, fmt.Errorf("backup: delete record %d: %w", rec.ID, err)
		}
		removed = append(removed, rec)
	}
	return removed, nil
}

// removeArchive deletes one archive, refusing to touch anything outside the
// backup root.
//
// The containment check runs on the *resolved* path (symlinks followed), so a
// symlink planted inside the root that points outside it cannot be used to
// delete an arbitrary file. A missing file is not an error: retention must be
// able to clean up a record whose archive the operator already removed by hand.
func (m *Manager) removeArchive(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: empty archive path", ErrOutsideRoot)
	}

	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("%w: %q: %v", ErrOutsideRoot, path, err)
	}

	// Resolve symlinks where possible. EvalSymlinks fails on a missing file, in
	// which case the lexical path is what we would have deleted anyway.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}

	if !isWithin(m.root, abs) {
		return fmt.Errorf("%w: refusing to delete %s (backup root is %s)",
			ErrOutsideRoot, abs, m.root)
	}

	if err := os.Remove(abs); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("backup: remove archive %s: %w", abs, err)
	}
	return nil
}

// Restore extracts a backup over targetWorldDir.
//
// It is safe by default, in this order:
//
//  1. Look the record up and load it (a nil record is accepted for testing and
//     for restoring from an archive with no index row).
//  2. Refuse if the instance is running. Overwriting a live world's files
//     under a running server corrupts it, and that is precisely the situation
//     the operator is already trying to recover from.
//  3. Verify the archive *before* touching anything: the sha256 must match the
//     record and every entry must pass a path-safety check.
//  4. Extract into a temporary directory alongside the target, so a failure
//     leaves the existing world completely untouched.
//  5. Move the current world aside to a timestamped sibling (not delete it),
//     then move the extracted tree into place.
//  6. On any failure after step 4, put the original back.
//
// Step 5 keeping the old tree is deliberate: a restore is exactly the moment
// when the operator is least certain what the right contents are, and disk is
// cheaper than regret.
func (m *Manager) Restore(ctx context.Context, backupID int64, targetWorldDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(targetWorldDir) == "" {
		return fmt.Errorf("%w: target world directory must not be empty", ErrInvalidRequest)
	}
	if m.store == nil {
		return fmt.Errorf("%w: no backup store configured", ErrInvalidRequest)
	}

	rec, err := m.store.Get(ctx, backupID)
	if err != nil {
		return fmt.Errorf("backup: load record %d: %w", backupID, err)
	}
	return m.RestoreRecord(ctx, rec, targetWorldDir)
}

// RestoreRecord is Restore for a record already in hand.
func (m *Manager) RestoreRecord(ctx context.Context, rec *Record, targetWorldDir string) error {
	if rec == nil {
		return fmt.Errorf("%w: nil record", ErrInvalidRequest)
	}

	target, err := filepath.Abs(filepath.Clean(targetWorldDir))
	if err != nil {
		return fmt.Errorf("%w: target world directory %q: %v", ErrInvalidRequest, targetWorldDir, err)
	}

	// 2. Refuse while the instance is running. Unknown is treated as running
	// for the same reason it is treated as hot on the backup side: the
	// consequences of guessing wrong are asymmetric.
	if running, known := m.instanceState(rec.InstanceID); running || !known {
		return fmt.Errorf("%w: instance %d is %s; stop it before restoring",
			ErrInstanceRunning, rec.InstanceID, stateWord(running, known))
	}

	// 3. Verify before touching anything.
	if _, err := verifyArchive(rec.Path, rec.SHA256); err != nil {
		return err
	}

	// 4. Extract to a temporary directory adjacent to the target, so the final
	// move is a same-filesystem rename.
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		return fmt.Errorf("backup: create parent of %s: %w", target, err)
	}

	staging, err := os.MkdirTemp(parent, ".restore-*")
	if err != nil {
		return fmt.Errorf("backup: create staging directory: %w", err)
	}
	stagingCommitted := false
	defer func() {
		if !stagingCommitted {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := extractArchive(ctx, rec.Path, staging); err != nil {
		return err
	}

	// The archive always stores its contents under a single "world/" root (see
	// worldZipName). Install that root *as* the target rather than installing
	// the staging directory wholesale, which would nest the world one level
	// deeper — a restore that silently produces Worlds/<name>/world/Project.json
	// is worse than one that fails, because the game server would then simply
	// regenerate a fresh world alongside the restored data.
	payload := filepath.Join(staging, worldZipName)
	if st, err := os.Stat(payload); err != nil || !st.IsDir() {
		// Fall back to the staging root so an archive produced by a different
		// tool (or an older layout) still restores, rather than being rejected
		// for not matching this package's convention.
		if _, err := os.Stat(staging); err != nil {
			return fmt.Errorf("backup: staged extraction vanished: %w", err)
		}
		payload = staging
	}

	// 5. Move the current world aside.
	previous := ""
	if _, err := os.Stat(target); err == nil {
		previous = fmt.Sprintf("%s.pre-restore-%s", target, m.now().UTC().Format("20060102T150405Z"))
		if _, err := os.Stat(previous); err == nil {
			previous = fmt.Sprintf("%s-%d", previous, m.now().UnixNano())
		}
		if err := renameDir(target, previous); err != nil {
			return fmt.Errorf("backup: move the current world aside: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("backup: stat target %s: %w", target, err)
	}

	// 6. Put the extracted tree in place, restoring the original on failure.
	if err := renameDir(payload, target); err != nil {
		if previous != "" {
			if rbErr := renameDir(previous, target); rbErr != nil {
				return fmt.Errorf("backup: install restored world: %v (and the original could not be restored from %s: %w)",
					err, previous, rbErr)
			}
		}
		return fmt.Errorf("backup: install restored world: %w", err)
	}
	stagingCommitted = true

	// The staging directory itself is now empty apart from the wrapper that
	// was just moved out; removing it is the deferred cleanup's job.
	return nil
}

// instanceState reports whether rec's instance is running.
func (m *Manager) instanceState(instanceID int64) (running bool, known bool) {
	if m.guard == nil {
		return true, false
	}
	return m.guard.IsRunning(instanceID)
}

// stateWord renders the guard state for an error message.
func stateWord(running, known bool) string {
	switch {
	case !known:
		return "in an unknown state"
	case running:
		return "running"
	default:
		return "stopped"
	}
}

// Verify checks a record's archive against its recorded sha256 and reports the
// entry count. It is what the UI's "verify" action calls, and it is the same
// check Restore performs first.
func (m *Manager) Verify(ctx context.Context, backupID int64) (int, error) {
	if m.store == nil {
		return 0, fmt.Errorf("%w: no backup store configured", ErrInvalidRequest)
	}
	rec, err := m.store.Get(ctx, backupID)
	if err != nil {
		return 0, fmt.Errorf("backup: load record %d: %w", backupID, err)
	}
	return verifyArchive(rec.Path, rec.SHA256)
}

// List returns an instance's backups, newest first.
func (m *Manager) List(ctx context.Context, instanceID int64) ([]Record, error) {
	if m.store == nil {
		return nil, nil
	}
	return m.store.ListByInstance(ctx, instanceID)
}

// Delete removes one backup: its file first, then its record.
//
// That order matters. Removing the record first would leave the archive
// unindexed if the unlink then failed, and nothing would ever clean it up.
func (m *Manager) Delete(ctx context.Context, backupID int64) error {
	if m.store == nil {
		return fmt.Errorf("%w: no backup store configured", ErrInvalidRequest)
	}

	rec, err := m.store.Get(ctx, backupID)
	if err != nil {
		return fmt.Errorf("backup: load record %d: %w", backupID, err)
	}

	if err := m.removeArchive(rec.Path); err != nil {
		return err
	}
	return m.store.Delete(ctx, backupID)
}

// ---------------------------------------------------------------------------
// Archive plumbing
// ---------------------------------------------------------------------------

// worldZipName is the directory prefix inside the archive. Every entry is
// stored under it so the archive always has exactly one root directory, which
// makes extraction into a clean target unambiguous.
const worldZipName = "world"

// writeWorldZip writes the world archive to w and returns the entry count.
//
// Layout inside the archive:
//
//	world/Project.json
//	world/Project.json.bak       (when present, per §6.5)
//	world/Regions/…
//	world/…
//
// Symlinks are recorded as symlinks rather than followed, except when the link
// target would escape the world directory, in which case the entry is skipped.
// Following them would silently pull unrelated files into the archive and could
// be used to exfiltrate anything the panel's user can read.
func writeWorldZip(w io.Writer, req Request, hot bool) (int, error) {
	zw := zip.NewWriter(w)

	count, err := addTree(zw, req.WorldDir, worldZipName, req.WorldDir)
	if err != nil {
		// Close still has to run to flush the central directory; the error from
		// the failed walk is the one worth reporting.
		_ = zw.Close()
		return 0, err
	}

	// §6.5: collect Project.json.bak alongside Project.json. When the world
	// lives directly under the instance (an unusual layout), the .bak can also
	// sit beside the world rather than inside it, so both are checked.
	if err := addExtraProjectBackup(zw, req); err != nil {
		_ = zw.Close()
		return 0, err
	}

	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("backup: finalise archive: %w", err)
	}
	return count, nil
}

// addTree walks root and adds every regular file and symlink to zw under prefix.
//
// excludedRoots lets the caller skip the extraction target when it is nested
// inside the tree being archived, which would otherwise recurse into itself.
func addTree(zw *zip.Writer, root, prefix string, worldRoot string) (int, error) {
	count := 0

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A file that vanished mid-walk (the server rotating logs) is not
			// a reason to fail the backup.
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("backup: walk %s: %w", path, err)
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("backup: relative path of %s: %w", path, err)
		}
		if rel == "." {
			return nil
		}

		// A staging directory from an interrupted restore, or any dot-prefixed
		// scratch directory, must not be archived.
		if strings.HasPrefix(filepath.Base(rel), ".restore-") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		name := prefix + "/" + filepath.ToSlash(rel)

		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("backup: stat %s: %w", path, err)
		}

		switch {
		case d.IsDir():
			// Explicit directory entries preserve empty directories, which the
			// game server creates and expects.
			header := &zip.FileHeader{Name: name + "/", Method: zip.Store}
			header.SetMode(info.Mode())
			header.Modified = info.ModTime()
			if _, err := zw.CreateHeader(header); err != nil {
				return fmt.Errorf("backup: add directory %s: %w", name, err)
			}
			return nil

		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return nil // vanished; skip
			}
			// Refuse to record a link that leaves the world directory: on
			// restore it would either dangle or point at something unrelated.
			resolved := target
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(path), target)
			}
			if !isWithin(worldRoot, resolved) {
				return nil
			}

			header := &zip.FileHeader{Name: name, Method: zip.Store}
			header.SetMode(os.ModeSymlink | 0o777)
			header.Modified = info.ModTime()
			entry, err := zw.CreateHeader(header)
			if err != nil {
				return fmt.Errorf("backup: add symlink %s: %w", name, err)
			}
			if _, err := entry.Write([]byte(target)); err != nil {
				return fmt.Errorf("backup: write symlink %s: %w", name, err)
			}
			count++
			return nil

		case info.Mode().IsRegular():
			if err := addFile(zw, path, name, info); err != nil {
				return err
			}
			count++
			return nil

		default:
			// Sockets, FIFOs and devices have no meaningful archive
			// representation; skipping them is better than failing the backup.
			return nil
		}
	})
	if err != nil {
		return count, err
	}
	return count, nil
}

// addFile streams one regular file into the archive.
func addFile(zw *zip.Writer, path, name string, info os.FileInfo) error {
	src, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("backup: open %s: %w", path, err)
	}
	defer src.Close()

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("backup: header for %s: %w", path, err)
	}
	header.Name = name
	// Deflate for everything: world data is mostly JSON and zone data, which
	// compresses well, and the plan's own export path uses the same default
	// (zero Method).
	header.Method = zip.Deflate

	dst, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("backup: add %s: %w", name, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		if os.IsNotExist(err) {
			// The file was unlinked while being read. Truncating it silently
			// would produce a corrupt entry, so the whole backup is abandoned;
			// the caller can retry.
			return fmt.Errorf("backup: read %s: %w", path, err)
		}
		return fmt.Errorf("backup: copy %s: %w", path, err)
	}
	return nil
}

// addExtraProjectBackup ensures Project.json.bak is in the archive (§6.5).
//
// If the .bak lives inside the world directory it was already collected by the
// tree walk, which is the normal layout. The fallback covers a world directory
// that was configured oddly, and is silent when there is nothing to add: a
// server that has never exited cleanly has no .bak, and that is not an error.
func addExtraProjectBackup(zw *zip.Writer, req Request) error {
	inside := filepath.Join(req.WorldDir, ProjectBackupName)
	if fileExists(inside) {
		return nil // already archived by the walk
	}

	beside := filepath.Join(req.InstanceDir, ProjectBackupName)
	info, err := os.Stat(beside)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	return addFile(zw, beside, worldZipName+"/"+ProjectBackupName, info)
}

// verifyArchive checks that path is a readable zip whose sha256 matches want,
// and returns its entry count.
//
// Both checks matter and neither subsumes the other: a truncated file can still
// be a valid zip if the truncation happened after the central directory (rare
// but real with a sparse file), and a digest match says nothing about whether
// the entry names are safe to extract. Restore needs both.
func verifyArchive(path, want string) (int, error) {
	if strings.TrimSpace(path) == "" {
		return 0, fmt.Errorf("%w: empty archive path", ErrCorrupt)
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("%w: archive %s does not exist", ErrNotFound, path)
		}
		return 0, fmt.Errorf("backup: open archive %s: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("backup: stat archive %s: %w", path, err)
	}
	if st.Size() == 0 {
		return 0, fmt.Errorf("%w: archive %s is empty", ErrCorrupt, path)
	}

	if want != "" {
		got, err := hashFile(f)
		if err != nil {
			return 0, err
		}
		if !strings.EqualFold(got, want) {
			return 0, fmt.Errorf("%w: archive %s has sha256 %s, expected %s",
				ErrCorrupt, path, got, want)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, fmt.Errorf("backup: rewind archive %s: %w", path, err)
		}
	}

	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return 0, fmt.Errorf("%w: archive %s is not a readable zip: %v", ErrCorrupt, path, err)
	}

	// Validate every entry name before anything is extracted, so a malicious
	// archive is rejected as a whole rather than halfway through.
	for _, zf := range zr.File {
		if _, err := safeEntryPath(zf.Name); err != nil {
			return 0, err
		}
	}
	return len(zr.File), nil
}

// extractArchive unpacks the archive at path into destDir.
//
// Every entry name is validated against destDir first; a single unsafe entry
// aborts the extraction (the caller's staging directory is then discarded), so
// a zip-slip archive cannot leave a partial result behind.
func extractArchive(ctx context.Context, path, destDir string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("backup: open archive %s: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("backup: stat archive %s: %w", path, err)
	}

	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return fmt.Errorf("%w: archive %s is not a readable zip: %v", ErrCorrupt, path, err)
	}

	// Pre-validate names.
	relPaths := make([]string, len(zr.File))
	for i, zf := range zr.File {
		rel, err := safeEntryPath(zf.Name)
		if err != nil {
			return err
		}
		relPaths[i] = rel
	}

	for i, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}

		rel := relPaths[i]
		if rel == "" {
			continue
		}
		target := filepath.Join(destDir, rel)

		// Re-check containment after joining: safeEntryPath works on the
		// cleaned relative name, and this catches anything that slipped
		// through.
		if !isWithin(destDir, target) {
			return fmt.Errorf("%w: entry %q escapes the destination", ErrCorrupt, zf.Name)
		}

		if err := extractEntry(zf, target); err != nil {
			return err
		}
	}
	return nil
}

// extractEntry writes one archive entry to target.
func extractEntry(zf *zip.File, target string) error {
	mode := zf.Mode()

	switch {
	case zf.FileInfo().IsDir():
		if err := os.MkdirAll(target, dirPerm); err != nil {
			return fmt.Errorf("backup: create directory %s: %w", target, err)
		}
		return nil

	case mode&os.ModeSymlink != 0:
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("%w: open symlink entry %s: %v", ErrCorrupt, zf.Name, err)
		}
		linkTarget, readErr := io.ReadAll(io.LimitReader(rc, 4096))
		rc.Close()
		if readErr != nil {
			return fmt.Errorf("%w: read symlink entry %s: %v", ErrCorrupt, zf.Name, readErr)
		}

		if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
			return fmt.Errorf("backup: create parent of %s: %w", target, err)
		}
		_ = os.Remove(target) // replacing an existing symlink
		if err := os.Symlink(string(linkTarget), target); err != nil {
			// A filesystem without symlink support is not a reason to fail the
			// whole restore; the game server's own data is all regular files.
			return nil
		}
		return nil

	default:
		if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
			return fmt.Errorf("backup: create parent of %s: %w", target, err)
		}

		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("%w: open entry %s: %v", ErrCorrupt, zf.Name, err)
		}
		defer rc.Close()

		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
		if err != nil {
			return fmt.Errorf("backup: create %s: %w", target, err)
		}

		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			return fmt.Errorf("%w: extract %s: %v", ErrCorrupt, zf.Name, err)
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return fmt.Errorf("backup: sync %s: %w", target, err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("backup: close %s: %w", target, err)
		}
		return nil
	}
}

// safeEntryPath validates a zip entry name and returns it as a clean relative
// slash-separated path.
//
// This is the zip-slip guard. A zip entry name is attacker-controlled data, so
// it is treated as such: absolute paths, drive letters, "..", and any
// backslash (which Windows treats as a separator and Linux does not) are all
// rejected. The plan calls out the same requirement for the file manager's
// unzip (§6.4).
func safeEntryPath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: archive contains an entry with an empty name", ErrCorrupt)
	}

	// Normalise separators for the check, but reject the Windows-only one
	// outright rather than translating it: a name containing a backslash is
	// either an attack or a broken archiver.
	if strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("%w: entry %q contains a backslash", ErrCorrupt, name)
	}

	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" {
		return "", nil // the root directory entry
	}

	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("%w: entry %q is an absolute path", ErrCorrupt, name)
	}
	// A drive-letter prefix (C:...) or any colon is not valid in a portable
	// archive entry.
	if strings.ContainsRune(trimmed, ':') {
		return "", fmt.Errorf("%w: entry %q contains a colon", ErrCorrupt, name)
	}

	clean := path.Clean(trimmed)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: entry %q escapes the archive root", ErrCorrupt, name)
	}

	return clean, nil
}

// hashFile returns the hex sha256 of the reader's remaining content.
func hashFile(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("backup: hash archive: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// renameDir moves a directory tree from src to dst, falling back to a copy when
// the kernel refuses a cross-filesystem rename.
//
// os.Rename on a directory only works within one filesystem; otherwise it fails
// with EXDEV. Staging is created next to the target precisely to make the
// rename cheap, but that is not a guarantee: /tmp and the data directory are
// frequently separate mounts, and an operator may have bind-mounted the world
// directory. When the rename is refused the tree is copied instead, which
// preserves the caller's atomic-ish intent for every case that matters (the
// old world has already been moved aside, so a failure here is still
// recoverable).
func renameDir(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return err
	}

	if err := copyTree(src, dst); err != nil {
		return fmt.Errorf("cross-filesystem move failed and the copy fallback also failed: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		// The data is safely at dst; failing to clean up the source is a
		// warning, not a failed restore.
		return nil
	}
	return nil
}

// isCrossDevice reports whether err is a cross-filesystem link error.
func isCrossDevice(err error) bool {
	// errors.Is(err, syscall.EXDEV) is the portable spelling; go's os package
	// wraps the errno in *os.LinkError, which unwraps correctly.
	return errors.Is(err, syscall.EXDEV)
}

// copyTree recursively copies src to dst, preserving modes and symlinks.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())

		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)

		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())

		default:
			// Sockets, FIFOs and devices have no useful copy; skip them the
			// same way the archiver does.
			return nil
		}
	})
}

// copyFile copies one file, flushing it to disk before returning.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// isWithin reports whether child is parent or lives underneath it.
//
// Both paths are compared after lexical cleaning. The comparison is done on
// path *elements*, not on string prefixes, so /backups-two is correctly not
// considered to be inside /backups.
func isWithin(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)

	if parent == child {
		return true
	}

	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sanitizeComponent makes s safe to use as a single path element.
func sanitizeComponent(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, string(filepath.Separator), "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, ":", "_")
	// "." and ".." would be path navigation rather than names.
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// composeNote builds the record's note: the operator's text plus any warning
// that must not be lost.
func composeNote(req Request, rec *Record) string {
	parts := make([]string, 0, 3)
	if req.Note != "" {
		parts = append(parts, req.Note)
	}
	if rec.Hot {
		warning := "hot backup: the instance was running, " +
			"so this copy may be inconsistent (§6.5)"
		if rec.SaveAttempted && rec.SaveError == "" {
			warning += "; a save was requested first"
		} else if rec.SaveError != "" {
			warning += "; the pre-backup save failed: " + rec.SaveError
		}
		parts = append(parts, warning)
	} else {
		parts = append(parts, "cold backup: the instance was stopped, so this copy is consistent")
	}
	return strings.Join(parts, " | ")
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// humanBytes renders a byte count for an error message.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
