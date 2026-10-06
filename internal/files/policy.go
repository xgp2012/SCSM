package files

import (
	"path/filepath"
	"strings"
)

// Policy carries the configurable limits and allow-lists for the upload,
// unzip, and text-editor surfaces. The plan (§5.7, §6.4) requires all three to
// be constrained; making them configurable means the panel can tighten them per
// deployment without a code change, while the zero value still yields safe
// defaults via [DefaultPolicy].
type Policy struct {
	// MaxUploadSize caps a single uploaded file, in bytes.
	MaxUploadSize int64
	// MaxUnzipSize caps the TOTAL decompressed size of an archive, in bytes.
	// This is the zip-bomb defence.
	MaxUnzipSize int64
	// MaxUnzipEntries caps the number of entries in an archive, so that a
	// huge-but-tiny entry list cannot exhaust inodes or time.
	MaxUnzipEntries int
	// MaxUnzipDepth caps the directory depth an archive may create.
	MaxUnzipDepth int
	// MaxUnzipEntrySize caps a single decompressed entry, in bytes.
	MaxUnzipEntrySize int64
	// MaxCompressionRatio rejects an entry whose decompressed size exceeds the
	// compressed size by more than this factor. Zero disables the check.
	// 0 is a deliberate default because small text files legitimately compress
	// extremely well (a 3 KB JSON of zeros is ~200 bytes compressed).
	MaxCompressionRatio int64
	// UploadExtensions is the §5.7 allow-list, lower-case and dot-prefixed.
	UploadExtensions []string
	// TextExtensions is the §6.4 built-in text editor allow-list.
	TextExtensions []string
	// MaxTextFileSize caps a file the text editor may open or save.
	MaxTextFileSize int64
	// AllowSymlinkUpload rejects a multipart upload whose name implies a path.
	// Kept for clarity: uploads are validated by [SanitizeUploadName] anyway.
	AllowSymlinkUpload bool
}

// Default values. These are the plan's numbers where the plan gives one.
const (
	DefaultMaxUploadSize     int64 = 512 << 20 // 512 MiB
	DefaultMaxUnzipSize      int64 = 2 << 30   // 2 GiB total decompressed
	DefaultMaxUnzipEntries         = 100_000
	DefaultMaxUnzipDepth           = 32
	DefaultMaxUnzipEntrySize int64 = 1 << 30 // 1 GiB per entry
	DefaultMaxTextFileSize   int64 = 2 << 20 // 2 MiB (§6.4 "建议 <2MB")
)

// DefaultPolicy returns the plan's defaults.
//
// The upload allow-list is copied verbatim from §5.7:
// `.zip/.dll/.scpak/.json/.xml/.txt`.
func DefaultPolicy() Policy {
	return Policy{
		MaxUploadSize:       DefaultMaxUploadSize,
		MaxUnzipSize:        DefaultMaxUnzipSize,
		MaxUnzipEntries:     DefaultMaxUnzipEntries,
		MaxUnzipDepth:       DefaultMaxUnzipDepth,
		MaxUnzipEntrySize:   DefaultMaxUnzipEntrySize,
		MaxCompressionRatio: 0,
		UploadExtensions:    []string{".zip", ".dll", ".scpak", ".json", ".xml", ".txt"},
		TextExtensions:      []string{".json", ".xml", ".txt", ".properties"},
		MaxTextFileSize:     DefaultMaxTextFileSize,
	}
}

// normalize fills zero fields from the defaults so that a partially populated
// Policy (e.g. built from config with only one field set) is still safe.
func (p Policy) normalize() Policy {
	d := DefaultPolicy()
	if p.MaxUploadSize <= 0 {
		p.MaxUploadSize = d.MaxUploadSize
	}
	if p.MaxUnzipSize <= 0 {
		p.MaxUnzipSize = d.MaxUnzipSize
	}
	if p.MaxUnzipEntries <= 0 {
		p.MaxUnzipEntries = d.MaxUnzipEntries
	}
	if p.MaxUnzipDepth <= 0 {
		p.MaxUnzipDepth = d.MaxUnzipDepth
	}
	if p.MaxUnzipEntrySize <= 0 {
		p.MaxUnzipEntrySize = d.MaxUnzipEntrySize
	}
	if p.MaxTextFileSize <= 0 {
		p.MaxTextFileSize = d.MaxTextFileSize
	}
	if p.UploadExtensions == nil {
		p.UploadExtensions = d.UploadExtensions
	}
	if p.TextExtensions == nil {
		p.TextExtensions = d.TextExtensions
	}
	return p
}

// withPolicy installs a Policy on the Root. A nil policy means defaults.
func (r *Root) withPolicy(p *Policy) Policy {
	if p == nil {
		return DefaultPolicy()
	}
	return p.normalize()
}

// ExtensionAllowed reports whether name has one of the allowed extensions.
//
// The comparison is on the *lower-cased final extension only*, so "evil.JSON"
// is allowed (that is fine — the server does not execute by extension), while
// "evil.json.php" and "evil.php" are both rejected. An empty allow-list means
// "allow everything", which is only ever correct for an explicitly configured
// trusted caller.
func ExtensionAllowed(name string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}
	for _, a := range allowed {
		if !strings.HasPrefix(a, ".") {
			a = "." + a
		}
		if strings.EqualFold(ext, a) {
			return true
		}
	}
	return false
}

// SanitizeUploadName validates a client-supplied file name for an upload.
//
// It rejects everything that could express a path rather than a name: empty
// names, separators (POSIX or Windows), "." and "..", drive letters, NUL bytes,
// control characters, and names longer than 255 bytes (the ext4 limit). The
// returned name is the bare base name, guaranteed free of separators.
//
// This is intentionally stricter than "take filepath.Base": silently stripping
// a path from an untrusted name hides an attack instead of reporting it.
func SanitizeUploadName(name string) (string, error) {
	if name == "" {
		return "", &PolicyError{Reason: "empty file name"}
	}
	if len(name) > 255 {
		return "", &PolicyError{Reason: "file name longer than 255 bytes"}
	}
	if strings.ContainsRune(name, 0) {
		return "", &PolicyError{Reason: "NUL byte in file name"}
	}
	if strings.ContainsAny(name, `/\`) {
		return "", &PolicyError{Reason: "path separator in file name"}
	}
	if name == "." || name == ".." {
		return "", &PolicyError{Reason: "reserved file name " + name}
	}
	if len(name) >= 2 && name[1] == ':' && isASCIILetter(name[0]) {
		return "", &PolicyError{Reason: "drive-letter file name"}
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", &PolicyError{Reason: "control character in file name"}
		}
	}
	if filepath.Base(name) != name {
		return "", &PolicyError{Reason: "file name is not a base name"}
	}
	return name, nil
}

// PolicyError is returned when an operation is refused by a Policy limit or
// allow-list. Is reports true for [ErrNotAllowed] and [ErrTooLarge] so callers
// can use errors.Is on the coarse sentinels.
type PolicyError struct {
	// Reason is a human-readable, non-sensitive explanation.
	Reason string
	// TooLarge marks a size-cap violation so errors.Is(err, ErrTooLarge) works.
	TooLarge bool
}

func (e *PolicyError) Error() string { return "files: " + e.Reason }

// Is lets errors.Is match the coarse sentinels.
func (e *PolicyError) Is(target error) bool {
	if target == ErrNotAllowed {
		return !e.TooLarge
	}
	if target == ErrTooLarge {
		return e.TooLarge
	}
	return false
}

func policyErr(reason string) error { return &PolicyError{Reason: reason} }

func tooLargeErr(reason string) error { return &PolicyError{Reason: reason, TooLarge: true} }
