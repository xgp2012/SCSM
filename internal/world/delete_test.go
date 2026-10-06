package world

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedTime is the frozen clock used to make backup names deterministic.
var fixedTime = time.Date(2026, 10, 6, 12, 34, 56, 0, time.UTC)

// TestDeleteRefusesActiveWorld is the §6.3 protection test: the save the server
// is configured to load must not be deletable by accident.
func TestDeleteRefusesActiveWorld(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "Active", "A", "g1", 10, "Harmless", 1, 10)
	makeWorld(t, inst, "Idle", "I", "g2", 10, "Survival", 1, 10)
	writeServerSetting(t, inst, "app:/Worlds/Active", "A")

	if _, err := Delete(inst, "Active", DeleteOptions{}); !errors.Is(err, ErrActiveWorld) {
		t.Fatalf("Delete(active) = %v; want ErrActiveWorld", err)
	}
	// The directory must be completely untouched.
	if _, err := Get(inst, "Active"); err != nil {
		t.Fatalf("a refused delete removed the world: %v", err)
	}

	// With Force it goes through.
	if _, err := Delete(inst, "Active", DeleteOptions{Force: true}); err != nil {
		t.Fatalf("Delete(active, Force) = %v", err)
	}
	if _, err := Get(inst, "Active"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the world still exists after a forced delete: %v", err)
	}

	// A non-active world needs no force.
	if _, err := Delete(inst, "Idle", DeleteOptions{}); err != nil {
		t.Fatalf("Delete(inactive) = %v", err)
	}
}

// TestDeleteRefusesWhileRunning covers the stopped-instance requirement.
func TestDeleteRefusesWhileRunning(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 1, 10)

	if _, err := Delete(inst, "World", DeleteOptions{Running: true}); !errors.Is(err, ErrRunning) {
		t.Fatalf("Delete while running = %v; want ErrRunning", err)
	}
	if _, err := Get(inst, "World"); err != nil {
		t.Fatalf("a refused delete removed the world: %v", err)
	}
	if _, err := DeleteWithBackup(inst, "World", false, false, true); !errors.Is(err, ErrRunning) {
		t.Fatalf("DeleteWithBackup while running = %v; want ErrRunning", err)
	}
}

// TestDeleteWithBackup is the §6.3 "先备份再删" path.
func TestDeleteWithBackup(t *testing.T) {
	inst := makeInstance(t)
	srcDir := makeWorld(t, inst, "World", "W", "g-backup", 10, "Harmless", 3, 500)
	original, err := os.ReadFile(filepath.Join(srcDir, ProjectFileName))
	if err != nil {
		t.Fatal(err)
	}

	backupPath, err := Delete(inst, "World", DeleteOptions{BackupFirst: true})
	if err != nil {
		t.Fatalf("Delete with backup: %v", err)
	}
	if backupPath == "" {
		t.Fatal("no backup path was reported")
	}
	// The world is gone...
	if _, err := Get(inst, "World"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the world was not deleted: %v", err)
	}
	// ...but the backup exists, under Backups/, and holds the full content.
	if !strings.Contains(backupPath, "Backups") {
		t.Errorf("backup path %q is not under Backups/", backupPath)
	}
	entries := readZipEntries(t, backupPath)
	if _, ok := entries[ProjectFileName]; !ok {
		t.Fatal("the backup lost Project.json")
	}
	regionCount := 0
	for name := range entries {
		if strings.HasPrefix(name, RegionsDirName+"/") {
			regionCount++
		}
	}
	if regionCount != 3 {
		t.Errorf("the backup has %d regions; want 3 (a complete backup must include terrain)", regionCount)
	}

	// The backup must be importable back into a working world.
	dst := makeInstance(t)
	if _, err := ImportZip(dst, backupPath, "Restored", ImportOptions{}); err != nil {
		t.Fatalf("the backup could not be restored: %v", err)
	}
	restored, err := Get(dst, "Restored")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Guid != "g-backup" {
		t.Errorf("restored Guid = %q; want g-backup", restored.Guid)
	}
	if restored.RegionsCount != 3 {
		t.Errorf("restored RegionsCount = %d; want 3", restored.RegionsCount)
	}
	got, err := os.ReadFile(filepath.Join(dst, WorldsDirName, "Restored", ProjectFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Error("the restored Project.json differs from the original")
	}
}

// TestDeleteBackupFailureAborts proves nothing is removed when the backup fails.
func TestDeleteBackupFailureAborts(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 1, 10)

	// Point the backup at a directory that cannot be created (a path under an
	// existing regular FILE), so ExportZip fails.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Delete(inst, "World", DeleteOptions{
		BackupFirst: true,
		BackupPath:  filepath.Join(blocker, "backup.zip"),
	})
	if err == nil {
		t.Fatal("Delete succeeded despite an impossible backup path")
	}
	if !strings.Contains(err.Error(), "nothing was removed") {
		t.Errorf("error = %v; want it to state that nothing was removed", err)
	}
	// The world must still be intact and loadable.
	w, gerr := Get(inst, "World")
	if gerr != nil {
		t.Fatalf("the world was removed even though the backup failed: %v", gerr)
	}
	if w.Guid != "g" || w.RegionsCount != 1 {
		t.Fatalf("the world was partially removed: %+v", w)
	}
}

// TestDeleteUsesExplicitBackupPath covers the caller-supplied path.
func TestDeleteUsesExplicitBackupPath(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 1, 10)
	dest := filepath.Join(t.TempDir(), "custom-backup.zip")

	got, err := Delete(inst, "World", DeleteOptions{BackupFirst: true, BackupPath: dest})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got != dest {
		t.Errorf("backup path = %q; want %q", got, dest)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("the explicit backup was not written: %v", err)
	}
}

// TestDeleteErrors covers the naming and existence guards.
func TestDeleteErrors(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 0, 0)

	for _, bad := range []string{"", ".", "..", "../World", "a/b", `a\b`, "nul\x00x"} {
		if _, err := Delete(inst, bad, DeleteOptions{}); !errors.Is(err, ErrInvalidDirName) {
			t.Errorf("Delete(%q) = %v; want ErrInvalidDirName", bad, err)
		}
	}
	if _, err := Delete(inst, "Missing", DeleteOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) = %v; want ErrNotFound", err)
	}
	// The traversal attempt must not have reached a sibling directory.
	sibling := filepath.Join(inst, "Sibling")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Delete(inst, "../Sibling", DeleteOptions{}); err == nil {
		t.Fatal("Delete with a traversal name succeeded")
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatal("a traversal delete removed a directory outside Worlds/")
	}
	// A file where a world directory is expected.
	if err := os.WriteFile(filepath.Join(inst, WorldsDirName, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Delete(inst, "afile", DeleteOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(file) = %v; want ErrNotFound", err)
	}
}

// TestDeleteDeterministicBackupName proves the timestamped name is well formed
// and survives the clock seam.
func TestDeleteDeterministicBackupName(t *testing.T) {
	orig := stampNow
	t.Cleanup(func() { stampNow = orig })
	stampNow = func() time.Time { return fixedTime }

	inst := makeInstance(t)
	makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 0, 0)
	p, err := Delete(inst, "World", DeleteOptions{BackupFirst: true})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if filepath.Base(p) != "world-World-20261006-123456.zip" {
		t.Fatalf("backup name = %q; want world-World-20261006-123456.zip", filepath.Base(p))
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the deterministically named backup is missing: %v", err)
	}
}

// TestDeleteLeavesNoStagingBehind proves the rename-then-remove sequence cleans
// up after itself.
func TestDeleteLeavesNoStagingBehind(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "World", "W", "g", 10, "Harmless", 2, 10)
	if _, err := Delete(inst, "World", DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(inst, WorldsDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("delete left %q behind", e.Name())
	}
}

// TestDeleteDoesNotFollowSymlinkedWorld proves a symlinked world directory is
// removed as a link, not as its target.
func TestDeleteDoesNotFollowSymlinkedWorld(t *testing.T) {
	inst := makeInstance(t)
	worldsRoot := filepath.Join(inst, WorldsDirName)
	if err := os.MkdirAll(worldsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(t.TempDir(), "real-world")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(real, "canary.txt")
	if err := os.WriteFile(canary, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(worldsRoot, "Linked")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	if _, err := Delete(inst, "Linked", DeleteOptions{}); err != nil {
		t.Fatalf("Delete(symlinked world) = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(worldsRoot, "Linked")); !os.IsNotExist(err) {
		t.Error("the symlink itself was not removed")
	}
	// The target must survive untouched.
	if b, err := os.ReadFile(canary); err != nil || string(b) != "safe" {
		t.Fatalf("deleting a symlinked world destroyed the target: %v", err)
	}
}

// TestDeleteScanConsistency proves the scanner agrees the world is gone.
func TestDeleteScanConsistency(t *testing.T) {
	inst := makeInstance(t)
	makeWorld(t, inst, "A", "A", "g1", 5, "Harmless", 0, 0)
	makeWorld(t, inst, "B", "B", "g2", 5, "Survival", 0, 0)

	if _, err := Delete(inst, "A", DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	worlds, err := Scan(inst)
	if err != nil {
		t.Fatal(err)
	}
	if len(worlds) != 1 || worlds[0].DirName != "B" {
		t.Fatalf("Scan after delete = %+v; want only B", worlds)
	}
}
