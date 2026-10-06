package files

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// UploadRequest describes an incoming file upload for [Root.SaveUpload].
type UploadRequest struct {
	// DirRel is the destination directory, relative to the root. It must
	// already exist (call Mkdir first); an empty value means the root itself.
	DirRel string
	// Name is the client-supplied file name. It is validated by
	// [SanitizeUploadName] and by the extension allow-list; the result is a bare
	// base name.
	Name string
	// Size is the declared size, when known (Content-Length). A value <= 0
	// means "unknown"; the actual bytes read are always capped anyway.
	Size int64
	// Policy overrides the limits. Nil means [DefaultPolicy].
	Policy *Policy
	// Overwrite permits replacing an existing file.
	Overwrite bool
}

// UploadResult reports where an upload landed and how much was written.
type UploadResult struct {
	// Rel is the root-relative path of the stored file, slash-separated.
	Rel string `json:"rel"`
	// Name is the validated base name.
	Name string `json:"name"`
	// Size is the number of bytes written.
	Size int64 `json:"size"`
}

// ValidateUploadName applies the §5.7 policy to a client-supplied name,
// returning the sanitised base name or an error.
//
// This is exposed separately so the HTTP layer can reject a bad upload with a
// 4xx BEFORE the body is streamed, rather than after.
func ValidateUploadName(name string, p *Policy) (string, error) {
	pol := DefaultPolicy()
	if p != nil {
		pol = p.normalize()
	}
	clean, err := SanitizeUploadName(name)
	if err != nil {
		return "", err
	}
	if !ExtensionAllowed(clean, pol.UploadExtensions) {
		return "", policyErr(fmt.Sprintf("extension %q is not allowed for uploads (allowed: %s)",
			filepath.Ext(clean), strings.Join(pol.UploadExtensions, ", ")))
	}
	return clean, nil
}

// SaveUpload stores an uploaded file under DirRel.
//
// The reader is capped at the policy's MaxUploadSize with a LimitReader, so a
// client that lies about Content-Length cannot exceed the limit: the extra bytes
// are simply never read, and the partial file is removed. Writing goes to a
// temporary file that is renamed into place only on success, so a failed upload
// never leaves a half-written artifact that the game server might load.
func (r *Root) SaveUpload(dst UploadRequest, src io.Reader) (UploadResult, error) {
	var res UploadResult

	pol := r.withPolicy(dst.Policy)
	if dst.Size > pol.MaxUploadSize {
		return res, tooLargeErr(fmt.Sprintf("declared size %d exceeds the upload cap of %d", dst.Size, pol.MaxUploadSize))
	}
	name, err := ValidateUploadName(dst.Name, dst.Policy)
	if err != nil {
		return res, err
	}
	rel := name
	if dst.DirRel != "" {
		rel = strings.TrimSuffix(cleanRel(dst.DirRel), "/") + "/" + name
	}

	// Resolve by NAME, not by target: uploads replace the directory entry.
	// Using Resolve here would reject an existing symlink that points outside
	// the root (so the user could never overwrite or repair it) and, for a link
	// pointing inside, would write THROUGH it. resolveForDelete returns the
	// link's own path so the rename below replaces the link atomically.
	abs, err := r.resolveForDelete(rel)
	if err != nil {
		return res, err
	}
	// The parent must resolve normally (no escaping directory component).
	parentResolved, perr := r.Resolve(cleanRel(dst.DirRel))
	if perr != nil {
		return res, perr
	}
	abs = filepath.Join(parentResolved, filepath.Base(abs))
	dirAbs := parentResolved
	if fi, serr := os.Stat(dirAbs); serr != nil {
		if os.IsNotExist(serr) {
			return res, fmt.Errorf("%w: upload directory %q", ErrNotFound, cleanRel(dst.DirRel))
		}
		return res, serr
	} else if !fi.IsDir() {
		return res, fmt.Errorf("%w: upload directory %q", ErrNotDir, cleanRel(dst.DirRel))
	}

	if fi, serr := os.Lstat(abs); serr == nil {
		if fi.IsDir() {
			return res, fmt.Errorf("%w: %s", ErrIsDir, rel)
		}
		if !dst.Overwrite {
			return res, fmt.Errorf("%w: %s", ErrExists, rel)
		}
		// Never write *through* an existing symlink.
		if fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(abs); err != nil {
				return res, fmt.Errorf("files: remove symlink at %q: %w", rel, err)
			}
		}
	}

	tmp, err := os.CreateTemp(dirAbs, ".scnetm-upload-*")
	if err != nil {
		return res, fmt.Errorf("files: create temp for upload %q: %w", rel, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	// Read at most cap+1 bytes so an over-long body is detectable rather than
	// silently truncated.
	limit := pol.MaxUploadSize + 1
	n, cerr := io.Copy(tmp, io.LimitReader(src, limit))
	if cerr != nil {
		tmp.Close()
		return res, fmt.Errorf("files: write upload %q: %w", rel, cerr)
	}
	if n > pol.MaxUploadSize {
		tmp.Close()
		return res, tooLargeErr(fmt.Sprintf("upload exceeded the cap of %d bytes", pol.MaxUploadSize))
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return res, fmt.Errorf("files: chmod upload %q: %w", rel, err)
	}
	if err := tmp.Close(); err != nil {
		return res, fmt.Errorf("files: close upload %q: %w", rel, err)
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return res, fmt.Errorf("files: commit upload %q: %w", rel, err)
	}
	committed = true

	res = UploadResult{Rel: rel, Name: name, Size: n}
	return res, nil
}

// ---------------------------------------------------------------------------
// Built-in text editor (§6.4)
// ---------------------------------------------------------------------------

// ReadTextFile reads a file for the built-in text editor.
//
// Only the extensions §6.4 allows (`.json/.xml/.txt/.properties`) may be read,
// the size is capped (2 MiB by default), and the content must be valid UTF-8.
// The UTF-8 requirement is a deliberate refusal to round-trip binary data
// through a text widget: doing so would corrupt `.scpak`/`.dll` files, and a
// panel that silently corrupts an instance is worse than one that says no.
func (r *Root) ReadTextFile(rel string, p *Policy) (string, error) {
	pol := r.withPolicy(p)
	clean, err := SanitizeUploadName(filepath.Base(cleanRel(rel)))
	if err != nil {
		return "", err
	}
	if !ExtensionAllowed(clean, pol.TextExtensions) {
		return "", policyErr(fmt.Sprintf("extension %q is not editable (allowed: %s)",
			filepath.Ext(clean), strings.Join(pol.TextExtensions, ", ")))
	}
	data, err := r.ReadFile(rel, pol.MaxTextFileSize)
	if err != nil {
		return "", err
	}
	if int64(len(data)) > pol.MaxTextFileSize {
		return "", tooLargeErr(fmt.Sprintf("%s exceeds the %d byte text cap", cleanRel(rel), pol.MaxTextFileSize))
	}
	if !utf8.Valid(data) {
		return "", policyErr(fmt.Sprintf("%s is not valid UTF-8 and cannot be edited as text", cleanRel(rel)))
	}
	return string(data), nil
}

// WriteTextFile saves content from the built-in text editor.
//
// The same extension allow-list and size cap apply as for reading, and the
// content is UTF-8 validated before anything is written, so the editor cannot
// be used to inject invalid bytes or to turn a config file into a binary blob.
func (r *Root) WriteTextFile(rel, content string, p *Policy) error {
	pol := r.withPolicy(p)
	clean, err := SanitizeUploadName(filepath.Base(cleanRel(rel)))
	if err != nil {
		return err
	}
	if !ExtensionAllowed(clean, pol.TextExtensions) {
		return policyErr(fmt.Sprintf("extension %q is not editable (allowed: %s)",
			filepath.Ext(clean), strings.Join(pol.TextExtensions, ", ")))
	}
	if int64(len(content)) > pol.MaxTextFileSize {
		return tooLargeErr(fmt.Sprintf("%s exceeds the %d byte text cap", cleanRel(rel), pol.MaxTextFileSize))
	}
	if !utf8.ValidString(content) {
		return policyErr("content is not valid UTF-8")
	}
	return r.WriteFile(rel, []byte(content))
}

// IsTextExt reports whether rel is editable by the built-in editor.
func IsTextExt(rel string, p *Policy) bool {
	pol := DefaultPolicy()
	if p != nil {
		pol = p.normalize()
	}
	return ExtensionAllowed(filepath.Base(rel), pol.TextExtensions)
}

// IsUploadExt reports whether rel may be uploaded.
func IsUploadExt(rel string, p *Policy) bool {
	pol := DefaultPolicy()
	if p != nil {
		pol = p.normalize()
	}
	return ExtensionAllowed(filepath.Base(rel), pol.UploadExtensions)
}
