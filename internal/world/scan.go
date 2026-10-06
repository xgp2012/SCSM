package world

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorldsDirName is the subdirectory of an instance that holds saves.
const WorldsDirName = "Worlds"

// ProjectFileName is the save's manifest.
const ProjectFileName = "Project.json"

// ProjectBakName is the server's own companion backup (§2.3, §A.5.3).
const ProjectBakName = "Project.json.bak"

// RegionsDirName holds terrain chunks — the main driver of disk growth (§2.3).
const RegionsDirName = "Regions"

// WorldPathPrefix is the logical prefix the server uses for instance-relative
// paths. §2.7: `app:` means the instance working directory.
const WorldPathPrefix = "app:"

// maxScanDepth bounds the size walk inside one world so a hostile tree cannot
// pin the scanner. Worlds are shallow (Project.json plus Regions/), so 32 is
// generous.
const maxScanDepth = 32

// WorldsDir returns the absolute `Worlds/` path for an instance.
func WorldsDir(instanceDir string) string {
	return filepath.Join(instanceDir, WorldsDirName)
}

// PathForDir builds the ServerSetting WorldPath value for a directory name:
// `app:/Worlds/<dirName>`. This is THE value that selects a save (§2.7).
func PathForDir(dirName string) string {
	return WorldPathPrefix + "/" + WorldsDirName + "/" + dirName
}

// Scan lists every save under `<instanceDir>/Worlds/`.
//
// Behaviour required by the plan and implemented here:
//
//   - a missing `Worlds/` directory yields an empty list and no error, because
//     a freshly created instance legitimately has no saves yet;
//   - a world directory with no Project.json is still returned, with an issue
//     recorded, so the user can see and delete it — one broken save must never
//     abort the whole scan;
//   - a corrupt Project.json likewise yields an entry plus an issue;
//   - directory names come from the filesystem entries, never from a display
//     name (§2.7);
//   - the active world is determined by ServerSetting.WorldPath's last segment,
//     compared against directory names;
//   - sizes are computed with Lstat/ReadDir only — file contents are never read
//     to measure them.
//
// The returned slice is sorted by directory name for a stable UI ordering.
func Scan(instanceDir string) ([]World, error) {
	return scanWithStore(instanceDir, nil)
}

// ScanWithStore is [Scan] with an explicit ServerSetting reader. Pass the
// config-backed store here once `internal/config` is wired in; passing nil makes
// the scan read `ServerSetting.json` itself, and if that file is absent the
// active flag is simply left false.
func ScanWithStore(instanceDir string, store SettingStore) ([]World, error) {
	return scanWithStore(instanceDir, store)
}

func scanWithStore(instanceDir string, store SettingStore) ([]World, error) {
	if strings.TrimSpace(instanceDir) == "" {
		return nil, fmt.Errorf("world: empty instance directory")
	}
	dir := WorldsDir(instanceDir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// A fresh instance has no Worlds/ until the server first runs.
			// This is not an error (§6.1 step 5).
			return []World{}, nil
		}
		return nil, fmt.Errorf("world: read %s: %w", dir, err)
	}

	active := activeDirName(instanceDir, store)

	worlds := make([]World, 0, len(entries))
	for _, de := range entries {
		// Only directories can be saves. A stray file directly under Worlds/ is
		// skipped rather than reported: it is not a save and cannot become one.
		if !de.IsDir() {
			continue
		}
		name := de.Name()
		if err := ValidateDirName(name); err != nil {
			// A directory the server could never have created (e.g. one with a
			// newline) is skipped: it cannot be addressed by WorldPath anyway.
			continue
		}
		w := scanOne(filepath.Join(dir, name), name)
		w.Active = active != "" && active == name
		worlds = append(worlds, w)
	}

	sort.Slice(worlds, func(i, j int) bool { return worlds[i].DirName < worlds[j].DirName })
	return worlds, nil
}

// Get returns a single world by directory name.
//
// dirName is validated first, so a caller cannot use Get to probe outside
// `Worlds/`; the name is then joined and the result is the on-disk directory —
// the display name is never consulted (§2.7).
func Get(instanceDir, dirName string) (*World, error) {
	return getWithStore(instanceDir, dirName, nil)
}

// GetWithStore is [Get] with an explicit ServerSetting reader.
func GetWithStore(instanceDir, dirName string, store SettingStore) (*World, error) {
	return getWithStore(instanceDir, dirName, store)
}

func getWithStore(instanceDir, dirName string, store SettingStore) (*World, error) {
	if err := ValidateDirName(dirName); err != nil {
		return nil, err
	}
	dir := filepath.Join(WorldsDir(instanceDir), dirName)
	fi, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, dirName)
		}
		return nil, fmt.Errorf("world: stat %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrNotFound, dirName)
	}
	w := scanOne(dir, dirName)
	if active := activeDirName(instanceDir, store); active != "" {
		w.Active = active == dirName
	}
	return &w, nil
}

// scanOne gathers everything the panel shows for one world directory.
//
// Failures are collected into World.Issues rather than returned, with the single
// exception of a directory that cannot be stat'ed at all.
func scanOne(dir, dirName string) World {
	w := World{
		DirName: dirName,
		Path:    dir,
	}

	if fi, err := os.Lstat(dir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			w.Issues = append(w.Issues, "world directory is a symbolic link")
		}
	} else {
		w.Issues = append(w.Issues, fmt.Sprintf("cannot stat world directory: %v", err))
		return w
	}

	// --- Project.json ---
	projectPath := filepath.Join(dir, ProjectFileName)
	if pfi, err := os.Lstat(projectPath); err != nil {
		if os.IsNotExist(err) {
			w.Issues = append(w.Issues, "Project.json is missing; this directory is not a usable save")
		} else if os.IsPermission(err) {
			w.Issues = append(w.Issues, "Project.json is not readable (permission denied)")
		} else {
			w.Issues = append(w.Issues, fmt.Sprintf("cannot stat Project.json: %v", err))
		}
	} else {
		w.ProjectMTime = pfi.ModTime().Unix()
		p, perr := ReadProject(projectPath)
		if perr != nil {
			switch {
			case errors.Is(perr, ErrNoProjectFile):
				w.Issues = append(w.Issues, "Project.json is missing; this directory is not a usable save")
			case errors.Is(perr, ErrCorruptProject):
				w.Issues = append(w.Issues, "Project.json is corrupt and cannot be parsed")
			default:
				w.Issues = append(w.Issues, fmt.Sprintf("cannot read Project.json: %v", perr))
			}
		} else {
			w.project = p
			populateFromProject(&w, p)
		}
	}

	// --- Project.json.bak ---
	bakPath := filepath.Join(dir, ProjectBakName)
	if bfi, err := os.Lstat(bakPath); err == nil {
		w.HasBak = true
		w.LastBakMTime = bfi.ModTime().Unix()
	}

	// --- sizes, without reading any file contents ---
	size, regionsSize, regionsCount := measureWorld(dir)
	w.SizeBytes = size
	w.RegionsBytes = regionsSize
	w.RegionsCount = regionsCount

	return w
}

// populateFromProject fills the display fields from a parsed Project.json.
func populateFromProject(w *World, p *Project) {
	// The display name. NOTE: this is display only and is never turned into a
	// path (plan §2.7). A save whose GameInfo.WorldName differs from its
	// directory name is completely normal — that divergence is exactly what
	// appendix A.5.1 demonstrated.
	if name, ok := p.GameInfoString("WorldName"); ok {
		w.DisplayName = name
	}
	if guid, ok := p.TopLevelString("Guid"); ok {
		w.Guid = guid
	}
	if mode, ok := p.GameInfoString("GameMode"); ok {
		// GameMode inside a save is a STRING such as "Harmless"; in
		// ServerSetting.json the same concept is an INTEGER. They must never be
		// mixed (§6.3.1 note 1).
		w.Mode = mode
	} else {
		w.ModeUnknown = true
	}
	if n, ok := p.IntAt(p.GameInfo(), "MaxOnlinePlayerCount"); ok {
		w.MaxPlayers = n
	}
}

// measureWorld computes the directory size, the Regions/ size, and the region
// file count.
//
// It never opens a file: os.ReadDir exposes sizes via DirEntry.Info(). Symbolic
// links are not followed, so a link to / cannot make this traverse the host.
func measureWorld(dir string) (total, regions int64, regionsCount int) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Unreadable subdirectory: skip it rather than failing the scan.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if depthOf(dir, path) > maxScanDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// WalkDir does not follow symlinks; they are skipped entirely so a link
		// to a huge tree cannot distort the reported size.
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || !info.Mode().IsRegular() {
			return nil
		}
		sz := info.Size()
		total += sz
		if rel, rerr := filepath.Rel(dir, path); rerr == nil {
			if isInRegions(rel) {
				regions += sz
				regionsCount++
			}
		}
		return nil
	})
	return total, regions, regionsCount
}

// isInRegions reports whether a world-relative path lives under Regions/.
func isInRegions(rel string) bool {
	rel = filepath.ToSlash(rel)
	return strings.HasPrefix(rel, RegionsDirName+"/")
}

// depthOf counts the path components between base and path.
func depthOf(base, path string) int {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(filepath.ToSlash(rel), "/") + 1
}

// activeDirName resolves which world directory ServerSetting.WorldPath selects.
//
// This is the §2.7 rule: read WorldPath, take its last segment. WorldName is
// never consulted. An empty or unreadable ServerSetting leaves the active world
// unknown (""), and no world is marked active.
func activeDirName(instanceDir string, store SettingStore) string {
	if store == nil {
		store = NewFileSettingStore(filepath.Join(instanceDir, "ServerSetting.json"))
	}
	raw, ok := store.WorldPath()
	if !ok || strings.TrimSpace(raw) == "" {
		return ""
	}
	seg, err := PathSegment(raw)
	if err != nil {
		return ""
	}
	return seg
}

// ActiveDirName exposes the active-world resolution for callers that need it
// without a full scan (used by Delete and Activate).
func ActiveDirName(instanceDir string, store SettingStore) (string, error) {
	if store == nil {
		store = NewFileSettingStore(filepath.Join(instanceDir, "ServerSetting.json"))
	}
	raw, ok := store.WorldPath()
	if !ok || strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return PathSegment(raw)
}
