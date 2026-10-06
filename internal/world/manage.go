package world

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ActivationResult describes what [Activate] changed.
type ActivationResult struct {
	// DirName is the world directory now selected.
	DirName string `json:"dirName"`
	// WorldPath is the value written to ServerSetting.WorldPath, i.e.
	// "app:/Worlds/<dirName>".
	WorldPath string `json:"worldPath"`
	// PreviousWorldPath is the value that was there before, if any.
	PreviousWorldPath string `json:"previousWorldPath,omitempty"`
	// Changed is false when the world was already active.
	Changed bool `json:"changed"`
}

// ActivateGuard decides whether a state-changing world operation may proceed.
//
// It exists so that this package does not need to depend on the supervisor: the
// caller supplies `func() bool { return sup.State() != StateStopped }`, which
// keeps the dependency direction clean and makes the check trivially testable.
type ActivateGuard func() (running bool, reason string)

// RefuseWhileRunning is the standard guard body: it returns running=true
// whenever the caller's predicate says the instance is not stopped.
func RefuseWhileRunning(isRunning func() bool) ActivateGuard {
	return func() (bool, string) {
		if isRunning != nil && isRunning() {
			return true, "instance is running"
		}
		return false, ""
	}
}

// Activate switches the active save by rewriting ServerSetting.WorldPath to
// `app:/Worlds/<dirName>`.
//
// This is THE operation that selects a save. Plan §2.7 and the decisive
// experiment in appendix A.5.1 are explicit: the directory is chosen by
// WorldPath, and rewriting WorldName alone switches nothing. So this function
// writes WorldPath and nothing else; if a caller also wants the display name
// kept in step, that is a separate, explicitly-optional concern.
//
// The instance must be stopped. `running` is the caller's answer to "is the
// instance running right now?" — pass nil when it is known to be stopped. The
// check is refused with [ErrRunning] rather than silently skipped, because
// rewriting the config under a live server produces a world switch that appears
// to work and then reverts on shutdown.
//
// store may be nil, in which case the instance's own ServerSetting.json is read
// and rewritten. Passing a config-backed store is preferred once
// `internal/config` is wired in.
func Activate(serverSettingPath, dirName string, running bool, store SettingStore) (ActivationResult, error) {
	var res ActivationResult

	if running {
		return res, fmt.Errorf("%w: cannot switch saves while the instance is running", ErrRunning)
	}
	if err := ValidateDirName(dirName); err != nil {
		return res, err
	}

	if store == nil {
		if strings.TrimSpace(serverSettingPath) == "" {
			return res, fmt.Errorf("world: no ServerSetting path and no store supplied")
		}
		store = NewFileSettingStore(serverSettingPath)
	}

	// The world must actually exist: activating a directory the server would
	// then try to generate from scratch is almost always a typo, and the failure
	// mode (a silently created empty world) is very confusing.
	if serverSettingPath != "" {
		instanceDir := filepath.Dir(serverSettingPath)
		worldDir := filepath.Join(WorldsDir(instanceDir), dirName)
		if _, err := os.Lstat(worldDir); err != nil {
			if os.IsNotExist(err) {
				return res, fmt.Errorf("%w: %s", ErrNotFound, dirName)
			}
			return res, fmt.Errorf("world: stat %s: %w", worldDir, err)
		}
	}

	prev, hadPrev := store.WorldPath()
	res.PreviousWorldPath = prev

	want := PathForDir(dirName)
	res.DirName = dirName
	res.WorldPath = want

	if hadPrev {
		// Compare by resolved segment, not by raw string: "app:/Worlds/World"
		// and "World" name the same save.
		if seg, err := PathSegment(prev); err == nil && seg == dirName {
			res.Changed = false
			return res, nil
		}
	}

	if err := store.SetWorldPath(want); err != nil {
		return res, err
	}
	res.Changed = true
	return res, nil
}

// ActivateWithGuard is [Activate] taking a guard callback instead of a bool.
// The guard's reason is included in the returned error.
func ActivateWithGuard(serverSettingPath, dirName string, guard ActivateGuard, store SettingStore) (ActivationResult, error) {
	if guard != nil {
		if isRunning, reason := guard(); isRunning {
			if reason == "" {
				reason = "instance is running"
			}
			return ActivationResult{}, fmt.Errorf("%w: %s", ErrRunning, reason)
		}
	}
	return Activate(serverSettingPath, dirName, false, store)
}

// DeleteOptions configures [Delete].
type DeleteOptions struct {
	// BackupFirst produces a backup archive before removing the world (§6.3:
	// "先备份再删"). Strongly recommended; the HTTP layer should require it for
	// the normal delete path and treat a deletion without it as an
	// administrative action needing a second confirmation.
	BackupFirst bool
	// BackupPath is where the backup archive is written. When empty and
	// BackupFirst is set, a name is chosen under `<instance>/Backups/`.
	BackupPath string
	// BackupOptions tunes the backup archive (Regions included by default, and
	// the server's own .bak collected, per §6.5).
	BackupOptions ExportOptions
	// Force permits deleting the world that is currently active.
	//
	// Without it, deleting the active save is refused with [ErrActiveWorld],
	// because doing so leaves ServerSetting.WorldPath pointing at a directory
	// that no longer exists — the server would then silently generate a brand
	// new world on next start, which looks like data loss even though the old
	// world was deliberately deleted.
	Force bool
	// Store is the ServerSetting reader used to determine the active world.
	Store SettingStore
	// Running refuses the operation while the instance is running, matching
	// §6.3's "仅在实例停止时允许" rule for save-affecting operations.
	Running bool
}

// Delete removes a save directory.
//
// Order of operations, all of which matter:
//
//  1. the directory name is validated (no traversal);
//  2. a running instance refuses the operation;
//  3. the active world is protected unless Force is set;
//  4. when BackupFirst is set, a complete archive is produced and fsync'ed
//     before anything is unlinked — if the backup fails, NOTHING is deleted;
//  5. the directory is renamed to a temporary name inside Worlds/ and then
//     removed, so a crash mid-delete cannot leave a half-deleted world that the
//     scanner would report as valid and the server might load.
func Delete(instanceDir, dirName string, opts DeleteOptions) (string, error) {
	if err := ValidateDirName(dirName); err != nil {
		return "", err
	}
	if opts.Running {
		return "", fmt.Errorf("%w: cannot delete a save while the instance is running", ErrRunning)
	}

	worldDir := filepath.Join(WorldsDir(instanceDir), dirName)
	fi, err := os.Lstat(worldDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrNotFound, dirName)
		}
		return "", fmt.Errorf("world: stat %s: %w", worldDir, err)
	}
	// A symlinked "world directory" is removed as the LINK. Following it and
	// deleting the target would be a straightforward way to destroy data
	// outside the instance with a single API call, so isDir is only satisfied
	// by a real directory.
	isSymlink := fi.Mode()&os.ModeSymlink != 0
	if !fi.IsDir() && !isSymlink {
		return "", fmt.Errorf("%w: %s is not a directory", ErrNotFound, dirName)
	}
	if isSymlink {
		// Never follow: rename/unlink the link itself.
		staging := filepath.Join(WorldsDir(instanceDir), ".scnetm-deleting-"+dirName)
		_ = os.RemoveAll(staging)
		if rerr := os.Rename(worldDir, staging); rerr != nil {
			return "", fmt.Errorf("world: stage deletion of %s: %w", dirName, rerr)
		}
		if rerr := os.RemoveAll(staging); rerr != nil {
			return "", fmt.Errorf("world: removed %s but could not clean %s: %w", dirName, staging, rerr)
		}
		return "", nil
	}

	// --- active-world protection ---
	store := opts.Store
	if store == nil {
		store = NewFileSettingStore(filepath.Join(instanceDir, "ServerSetting.json"))
	}
	if active, aerr := ActiveDirName(instanceDir, store); aerr == nil && active != "" && active == dirName && !opts.Force {
		return "", fmt.Errorf("%w: %s is selected by ServerSetting.WorldPath; pass Force to delete it anyway",
			ErrActiveWorld, dirName)
	}

	// --- backup before touching anything ---
	backupPath := opts.BackupPath
	if opts.BackupFirst {
		if backupPath == "" {
			backupDir := filepath.Join(instanceDir, "Backups")
			if err := os.MkdirAll(backupDir, 0o755); err != nil {
				return "", fmt.Errorf("world: create backup directory: %w", err)
			}
			backupPath = filepath.Join(backupDir, fmt.Sprintf("world-%s-%s.zip", dirName, nowStamp()))
		}
		// Default to a COMPLETE archive: a backup that quietly omitted the
		// terrain would be worse than no backup at all.
		bopts := opts.BackupOptions
		if !bopts.IncludeRegions && !bopts.IncludeBak && opts.BackupOptions == (ExportOptions{}) {
			bopts.IncludeRegions = true
			bopts.IncludeBak = true
		}
		if err := ExportZip(instanceDir, dirName, backupPath, bopts); err != nil {
			return "", fmt.Errorf("world: backup before delete failed, nothing was removed: %w", err)
		}
	}

	// --- remove via a rename, so the world disappears atomically ---
	staging := filepath.Join(WorldsDir(instanceDir), ".scnetm-deleting-"+dirName)
	_ = os.RemoveAll(staging)
	if err := os.Rename(worldDir, staging); err != nil {
		return backupPath, fmt.Errorf("world: stage deletion of %s: %w", dirName, err)
	}
	if err := os.RemoveAll(staging); err != nil {
		// The world is already gone from the scanner's point of view (it has a
		// reserved name); report the leftover so an operator can clean it.
		return backupPath, fmt.Errorf("world: world removed but its staging directory %s could not be cleaned: %w", staging, err)
	}
	return backupPath, nil
}

// nowStamp returns a filesystem-safe UTC timestamp for backup names.
func nowStamp() string {
	return stampNow().UTC().Format("20060102-150405")
}

// DeleteWithBackup is a convenience wrapper for the common case.
func DeleteWithBackup(instanceDir, dirName string, backupFirst, force, running bool) (string, error) {
	return Delete(instanceDir, dirName, DeleteOptions{
		BackupFirst: backupFirst,
		Force:       force,
		Running:     running,
	})
}

// IsActive reports whether dirName is the world selected by ServerSetting.
func IsActive(instanceDir, dirName string, store SettingStore) (bool, error) {
	if err := ValidateDirName(dirName); err != nil {
		return false, err
	}
	active, err := ActiveDirName(instanceDir, store)
	if err != nil {
		if errors.Is(err, ErrInvalidDirName) {
			return false, nil // a malformed WorldPath selects nothing
		}
		return false, err
	}
	return active != "" && active == dirName, nil
}
