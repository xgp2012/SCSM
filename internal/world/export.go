package world

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ExportOptions configures [ExportZip].
type ExportOptions struct {
	// IncludeRegions includes the Regions/ directory.
	//
	// Setting it to false is the documented size optimisation from §6.3:
	// Regions/ holds terrain chunks and is the main driver of world size
	// (§2.3), so excluding it turns a multi-hundred-megabyte archive into a few
	// kilobytes of settings. The resulting zip is NOT a complete playable save
	// — the caller must label it as a settings-only export in the UI.
	IncludeRegions bool
	// IncludeBak includes Project.json.bak when present. Default false keeps
	// exports minimal; the backup feature sets it true so the server's own
	// backup travels with the archive (§6.5 "与 .bak 协同").
	IncludeBak bool
	// MaxSizeBytes, when positive, aborts the export if the source directory
	// exceeds this size. It protects the panel from trying to zip something
	// absurd.
	MaxSizeBytes int64
}

// ExportResult reports what an export wrote.
type ExportResult struct {
	// Files is the number of files written into the archive.
	Files int `json:"files"`
	// Bytes is the total uncompressed size of those files.
	Bytes int64 `json:"bytes"`
	// IncludedRegions mirrors the effective option.
	IncludedRegions bool `json:"includedRegions"`
}

// defaultMaxExportBytes bounds an export when the caller sets no limit.
const defaultMaxExportBytes = 32 << 30 // 32 GiB

// ExportZip packs `Worlds/<dirName>` into a zip at destZip.
//
// The archive is written so that extracting it into `Worlds/<dirName>`
// reproduces a valid world:
//
//   - entry names are relative to the WORLD DIRECTORY (no "Worlds/" prefix, no
//     wrapper directory), which is what "extracted into Worlds/<dir>" means;
//   - Project.json is always included, and is always written first so that a
//     truncated archive still carries the manifest;
//   - Project.json.bak is included when [ExportOptions.IncludeBak] is set;
//   - Regions/ is included unless [ExportOptions.IncludeRegions] is false;
//   - symbolic links are skipped, both because they are not part of a save and
//     because an archive containing them is a delivery vehicle for zip-slip;
//   - the filesystem is walked with a depth bound, so a hostile world tree
//     cannot pin the exporter.
//
// destZip may be inside the instance (the caller is responsible for not
// exporting a world into itself in a way that recurses — the zip is written to a
// temporary file and renamed at the end, so a partially written archive is never
// visible).
func ExportZip(instanceDir, dirName, destZip string, opts ExportOptions) error {
	if err := ValidateDirName(dirName); err != nil {
		return err
	}
	if strings.TrimSpace(destZip) == "" {
		return fmt.Errorf("world: empty export destination")
	}
	srcDir := filepath.Join(WorldsDir(instanceDir), dirName)
	fi, err := os.Lstat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, dirName)
		}
		return fmt.Errorf("world: stat %s: %w", srcDir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrNotFound, dirName)
	}
	// Refuse to export a world that does not even have a manifest: the archive
	// would be useless and the user almost certainly picked the wrong thing.
	if _, perr := os.Lstat(filepath.Join(srcDir, ProjectFileName)); perr != nil {
		return fmt.Errorf("%w: %s", ErrNoProjectFile, dirName)
	}

	maxBytes := opts.MaxSizeBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxExportBytes
	}

	files, err := collectWorldFiles(srcDir, opts, maxBytes)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(destZip), 0o755); err != nil {
		return fmt.Errorf("world: create export directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destZip), ".scnetm-export-*.zip")
	if err != nil {
		return fmt.Errorf("world: create temp archive: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if err := writeWorldZip(tmp, srcDir, files); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("world: close archive: %w", err)
	}
	if err := os.Rename(tmpName, destZip); err != nil {
		return fmt.Errorf("world: commit archive %s: %w", destZip, err)
	}
	committed = true
	return nil
}

// ExportToZip is [ExportZip] returning the summary.
func ExportToZip(instanceDir, dirName, destZip string, opts ExportOptions) (ExportResult, error) {
	var res ExportResult
	// Validate up front so the reported counts always come from a world that
	// could actually be exported: a missing or wrongly-typed target must be
	// ErrNotFound here, not a raw walk error.
	if err := ValidateDirName(dirName); err != nil {
		return res, err
	}
	srcDir := filepath.Join(WorldsDir(instanceDir), dirName)
	if fi, serr := os.Lstat(srcDir); serr != nil {
		if os.IsNotExist(serr) {
			return res, fmt.Errorf("%w: %s", ErrNotFound, dirName)
		}
		return res, fmt.Errorf("world: stat %s: %w", srcDir, serr)
	} else if !fi.IsDir() {
		return res, fmt.Errorf("%w: %s is not a directory", ErrNotFound, dirName)
	}
	files, err := collectWorldFiles(srcDir, opts, defaultMaxExportBytes)
	if err != nil {
		return res, err
	}
	for _, f := range files {
		if f.name == ProjectFileName {
			continue
		}
		res.Files++
		res.Bytes += f.size
	}
	if err := ExportZip(instanceDir, dirName, destZip, opts); err != nil {
		return res, err
	}
	// Re-count including the manifest so the numbers describe the archive.
	res.Files++
	if fi, serr := os.Lstat(filepath.Join(srcDir, ProjectFileName)); serr == nil {
		res.Bytes += fi.Size()
	}
	res.IncludedRegions = opts.IncludeRegions
	return res, nil
}

// worldFile is one file selected for archiving.
type worldFile struct {
	// name is the archive-relative, slash-separated entry name.
	name string
	// abs is the source path.
	abs string
	// size is the source size in bytes.
	size int64
}

// collectWorldFiles walks a world directory and returns the files to archive,
// with Project.json forced to the front.
func collectWorldFiles(srcDir string, opts ExportOptions, maxBytes int64) ([]worldFile, error) {
	var files []worldFile
	var total int64

	err := filepath.WalkDir(srcDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("world: walk %s: %w", p, err)
		}
		rel, rerr := filepath.Rel(srcDir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if depthOf(srcDir, p) > maxScanDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Never archive symlinks: they are not save data, and an archive that
		// carries them is an attack vector for the import side.
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if !opts.IncludeRegions && rel == RegionsDirName {
				return filepath.SkipDir
			}
			return nil
		}
		if !opts.IncludeBak && rel == ProjectBakName {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("world: stat %s: %w", p, ierr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if total+info.Size() > maxBytes {
			return fmt.Errorf("world: %s exceeds the %d byte export limit", srcDir, maxBytes)
		}
		total += info.Size()
		files = append(files, worldFile{name: rel, abs: p, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Project.json first: a truncated archive then still carries the manifest,
	// and every extractor sees the world's identity before its bulk.
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].name == ProjectFileName {
			return true
		}
		if files[j].name == ProjectFileName {
			return false
		}
		return files[i].name < files[j].name
	})
	return files, nil
}

// writeWorldZip writes the selected files into an open zip writer.
func writeWorldZip(w io.Writer, srcDir string, files []worldFile) error {
	zw := zip.NewWriter(w)
	for _, f := range files {
		// The entry name was produced by filepath.Rel against the world
		// directory, so it cannot be absolute or contain "..". Re-check anyway:
		// an export that produces a traversing entry would be a zip-slip bomb
		// handed to whoever later imports it back.
		if err := assertSafeEntryName(f.name); err != nil {
			zw.Close()
			return err
		}
		hdr := &zip.FileHeader{
			Name:   f.name,
			Method: zip.Deflate,
		}
		hdr.SetMode(0o644)
		hdr.Modified = mtimeOf(f.abs)
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			zw.Close()
			return fmt.Errorf("world: create zip entry %q: %w", f.name, err)
		}
		in, err := os.Open(f.abs)
		if err != nil {
			zw.Close()
			return fmt.Errorf("world: open %s: %w", f.abs, err)
		}
		_, cerr := io.Copy(out, in)
		in.Close()
		if cerr != nil {
			zw.Close()
			return fmt.Errorf("world: write zip entry %q: %w", f.name, cerr)
		}
	}
	return zw.Close()
}

// mtimeOf returns a file's modification time, or the zero time if it cannot be
// stat'ed (the entry is then written with the zero timestamp, which is harmless).
func mtimeOf(p string) time.Time {
	if fi, err := os.Lstat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// assertSafeEntryName is the export-side counterpart of the import-side checks.
// Producing a safe archive is as important as consuming one safely: a panel that
// hands out a traversing zip has shipped a weapon.
func assertSafeEntryName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty entry name", ErrZipSlip)
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("%w: NUL byte in entry name", ErrZipSlip)
	}
	if strings.ContainsRune(name, '\\') {
		return fmt.Errorf("%w: backslash in entry name %q", ErrZipSlip, name)
	}
	if path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return fmt.Errorf("%w: absolute entry name %q", ErrZipSlip, name)
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%w: traversing entry name %q", ErrZipSlip, name)
	}
	if len(name) >= 2 && name[1] == ':' {
		return fmt.Errorf("%w: drive-letter entry name %q", ErrZipSlip, name)
	}
	return nil
}
