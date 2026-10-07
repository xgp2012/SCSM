package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// This file implements the instance file manager (§6.4).
//
// # Security posture
//
// Every client-supplied path goes through ResolveInside exactly once, at the top
// of the handler, before the FileService sees it. ResolveInside performs the
// §5.7 check (Clean + EvalSymlinks + containment) and additionally rejects
// absolute paths, NUL bytes and Windows-style separators. A rejection is a 403
// with code "forbidden", because a traversal attempt is an attack rather than a
// missing file — the operator should see it in the logs and audit trail.
//
// When the FileService is the nop implementation the handler answers 501, so a
// build without internal/files still rejects traversal loudly rather than
// pretending the path was fine.

// uploadAllowlist is the type allowlist from §5.7.
//
// It is applied to uploads only: reading and downloading existing files in the
// instance directory is unrestricted, because those files arrived with the
// server package and blocking them would make the file manager useless.
var uploadAllowlist = map[string]bool{
	".zip":   true,
	".dll":   true,
	".scpak": true,
	".json":  true,
	".xml":   true,
	".txt":   true,
	// A few practical additions that are still inert data files and are
	// commonly dropped next to the server.
	".md":  true,
	".cfg": true,
	".ini": true,
	".log": true,
	// Plugins/NetMods/Mods packages are sometimes shipped as plain binaries.
	".pdb": true,
	".so":  true,
}

// handleListFiles lists a directory inside the instance.
func (s *Server) handleListFiles(c *gin.Context) {
	inst := MustInstance(c)

	rel := c.Query("path")
	if rel == "" {
		rel = "."
	}

	// --- the §5.7 path check, before anything else ---
	abs, apiErr := s.resolveInstancePath(c, inst, rel, true)
	if apiErr != nil {
		return
	}

	st, err := os.Stat(abs)
	if err != nil {
		Fail(c, Classify(err, "路径不存在"))
		return
	}
	if !st.IsDir() {
		Fail(c, ValidationFailed("%q 是文件，不是目录", rel))
		return
	}

	entries, lerr := s.deps.Files.List(c.Request.Context(), inst, rel)
	if lerr != nil {
		s.failFiles(c, lerr, inst, "list", rel)
		return
	}

	// Normalise the reply path so the UI breadcrumb is always safe.
	safeRel, _ := SafeRelPath(inst.Dir, rel, false)
	parent := ""
	if safeRel != "" {
		parent = path.Dir(safeRel)
		if parent == "." {
			parent = ""
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	c.JSON(http.StatusOK, DataResponse{Data: FileListResponse{
		Path:    safeRel,
		Parent:  parent,
		Entries: entries,
	}})
}

// handleDownloadFile streams a file with range support.
func (s *Server) handleDownloadFile(c *gin.Context) {
	inst := MustInstance(c)

	rel := c.Query("path")
	if rel == "" {
		Fail(c, BadRequest("必须提供 \"path\" 查询参数"))
		return
	}

	abs, apiErr := s.resolveInstancePath(c, inst, rel, true)
	if apiErr != nil {
		return
	}

	st, err := os.Stat(abs)
	if err != nil {
		Fail(c, Classify(err, "文件不存在"))
		return
	}
	if st.IsDir() {
		Fail(c, ValidationFailed("%q 是目录；请先打包为 zip，或改用列表接口", rel))
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		Fail(c, Classify(err, "文件不存在"))
		return
	}
	defer f.Close()

	s.audit(c, "file.download", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"path":       rel,
		"size_bytes": st.Size(),
	})

	name := sanitizeDownloadName(filepath.Base(abs))
	if ct := mime.TypeByExtension(filepath.Ext(abs)); ct != "" {
		c.Header("Content-Type", ct)
	} else {
		c.Header("Content-Type", "application/octet-stream")
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))

	// Never let the browser second-guess the type, and never let it render
	// panel-served content as HTML. The files here were uploaded by a user or
	// written by the game server, so treating them as trusted markup would turn
	// the panel's own origin into a stored-XSS vector.
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")

	// http.ServeContent gives range requests and conditional GETs for free
	// (§6.4 "下载走 http.ServeContent 支持断点").
	http.ServeContent(c.Writer, c.Request, name, st.ModTime(), f)
}

// handleUploadFile stores an uploaded file.
func (s *Server) handleUploadFile(c *gin.Context) {
	inst := MustInstance(c)

	// The destination directory (not the full path): the filename comes from
	// the multipart header and is sanitised below.
	destDir := c.Query("path")
	if destDir == "" {
		destDir = "."
	}

	absDir, apiErr := s.resolveInstancePath(c, inst, destDir, true)
	if apiErr != nil {
		return
	}
	st, err := os.Stat(absDir)
	if err != nil || !st.IsDir() {
		Fail(c, ValidationFailed("上传目标 %q 不是目录", destDir))
		return
	}

	fh, err := c.FormFile("file")
	if err != nil {
		Fail(c, BadRequest("必须提供名为 \"file\" 的 multipart 字段：%v", err))
		return
	}

	maxBytes := s.deps.ConfigHTTP.MaxUploadBytes
	if fh.Size > maxBytes {
		Fail(c, PayloadTooLarge("文件大小 %s 超过 %s 的上限", humanSize(fh.Size), humanSize(maxBytes)))
		return
	}

	// The filename arrives from the client and may contain a path. Take only
	// the base name and re-validate through ResolveInside.
	safeName := filepath.Base(filepath.FromSlash(fh.Filename))
	if safeName == "." || safeName == ".." || safeName == "/" || safeName == "" {
		Fail(c, ValidationFailed("上传的文件没有可用的文件名"))
		return
	}
	rel := safeName
	if destDir != "." && destDir != "" {
		rel = path.Join(destDir, safeName)
	}
	absFile, apiErr := s.resolveInstancePath(c, inst, rel, false)
	if apiErr != nil {
		return
	}

	// Type allowlist (§5.7). Extension-based, which is the right granularity
	// here: the panel never executes an upload, it only stores it for the game
	// server to load.
	ext := strings.ToLower(filepath.Ext(safeName))
	warnings := []string{}
	if !uploadAllowlist[ext] {
		Fail(c, ValidationFailed("不允许扩展名 %q；允许的类型为 %s",
			ext, allowlistString()).WithDetail(gin.H{
			"issues": []ValidationIssue{{
				Field:    "file",
				Code:     "extension_not_allowed",
				Message:  fmt.Sprintf("%q is not in the upload allowlist", ext),
				Severity: "error",
			}},
			"allowlist": allowlist(),
		}))
		return
	}

	src, err := fh.Open()
	if err != nil {
		Fail(c, BadRequest("无法读取上传内容：%v", err))
		return
	}
	defer src.Close()

	data, rerr := readAllLimited(src, maxBytes)
	if rerr != nil {
		Fail(c, Classify(rerr, "upload failed"))
		return
	}

	entry, werr := s.deps.Files.SaveUpload(c.Request.Context(), inst, rel, data)
	if werr != nil {
		s.failFiles(c, werr, inst, "upload", rel)
		return
	}

	s.audit(c, "file.upload", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"path":       rel,
		"bytes":      len(data),
		"sha_prefix": shortHash(data),
		"ext":        ext,
	})

	resp := FileUploadResponse{Bytes: int64(len(data))}
	if entry != nil {
		resp.Entry = *entry
	} else {
		resp.Entry = FileEntry{
			Name:  safeName,
			Path:  filepath.ToSlash(rel),
			Size:  int64(len(data)),
			IsDir: false,
		}
	}
	resp.AllowlistWarnings = warnings

	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "file_upload", "path": rel, "bytes": len(data)},
	})

	_ = absFile
	c.JSON(http.StatusCreated, DataResponse{Data: resp})
}

// handleMkdir creates a directory.
func (s *Server) handleMkdir(c *gin.Context) {
	inst := MustInstance(c)

	var req FileMkdirRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("新建目录请求格式无效：%v", err))
		return
	}

	abs, apiErr := s.resolveInstancePath(c, inst, req.Path, false)
	if apiErr != nil {
		return
	}

	perm := os.FileMode(0o755)
	if req.Mode != "" {
		var parsed uint32
		if _, err := fmt.Sscanf(req.Mode, "%o", &parsed); err != nil || parsed > 0o777 {
			Fail(c, ValidationFailed("mode 必须形如 \"0755\" 的八进制权限字符串"))
			return
		}
		perm = os.FileMode(parsed)
	}

	if err := s.deps.Files.Mkdir(c.Request.Context(), inst, req.Path); err != nil {
		s.failFiles(c, err, inst, "mkdir", req.Path)
		return
	}
	// Best-effort: also ensure it exists locally, so a nop-less build and the
	// real service agree on the outcome.
	_ = os.MkdirAll(abs, perm)

	s.audit(c, "file.mkdir", fmt.Sprintf("instance:%d", inst.ID), gin.H{"path": req.Path})
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "file_mkdir", "path": req.Path},
	})

	c.JSON(http.StatusCreated, DataResponse{Data: gin.H{"path": req.Path, "created": true}})
}

// handleRenameFile renames or moves a path inside the instance.
func (s *Server) handleRenameFile(c *gin.Context) {
	inst := MustInstance(c)

	var req FileRenameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("重命名请求格式无效：%v", err))
		return
	}

	// Both ends are validated: a safe source with an escaping destination is
	// still an escape.
	if _, apiErr := s.resolveInstancePath(c, inst, req.From, true); apiErr != nil {
		return
	}
	if _, apiErr := s.resolveInstancePath(c, inst, req.To, false); apiErr != nil {
		return
	}

	if err := s.deps.Files.Rename(c.Request.Context(), inst, req.From, req.To); err != nil {
		s.failFiles(c, err, inst, "rename", req.From)
		return
	}

	s.audit(c, "file.rename", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"from": req.From,
		"to":   req.To,
	})
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "file_rename", "from": req.From, "to": req.To},
	})

	c.JSON(http.StatusOK, DataResponse{Data: gin.H{"from": req.From, "to": req.To}})
}

// handleDeleteFile removes a file or directory.
func (s *Server) handleDeleteFile(c *gin.Context) {
	inst := MustInstance(c)

	var req FileDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("删除请求格式无效：%v", err))
		return
	}

	abs, apiErr := s.resolveInstancePath(c, inst, req.Path, true)
	if apiErr != nil {
		return
	}

	// Refuse to delete the instance root itself, even though ResolveInside
	// permits the path "" (which resolves to the root).
	rootAbs, _ := filepath.Abs(inst.Dir)
	if filepath.Clean(abs) == filepath.Clean(rootAbs) {
		Fail(c, Forbidden("拒绝删除实例根目录"))
		return
	}

	st, err := os.Lstat(abs)
	if err == nil && st.IsDir() && !req.Recursive {
		entries, _ := os.ReadDir(abs)
		if len(entries) > 0 {
			Fail(c, Conflict("目录非空；如需连同内容一并删除，请设置 \"recursive\": true").
				WithDetail(gin.H{"path": req.Path, "entries": len(entries)}))
			return
		}
	}

	if err := s.deps.Files.Delete(c.Request.Context(), inst, req.Path); err != nil {
		s.failFiles(c, err, inst, "delete", req.Path)
		return
	}

	s.audit(c, "file.delete", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"path":      req.Path,
		"recursive": req.Recursive,
	})
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "file_delete", "path": req.Path},
	})

	c.JSON(http.StatusOK, DataResponse{Data: OKResponse{OK: true}})
}

// handleUnzipFile extracts an archive.
func (s *Server) handleUnzipFile(c *gin.Context) {
	inst := MustInstance(c)

	var req FileUnzipRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("解压请求格式无效：%v", err))
		return
	}

	abs, apiErr := s.resolveInstancePath(c, inst, req.Path, true)
	if apiErr != nil {
		return
	}
	if !strings.EqualFold(filepath.Ext(abs), ".zip") {
		Fail(c, ValidationFailed("仅支持解压 .zip 归档"))
		return
	}

	dest := req.Dest
	if dest == "" {
		rel, _ := SafeRelPath(inst.Dir, req.Path, true)
		dest = path.Dir(rel)
	}
	if _, apiErr := s.resolveInstancePath(c, inst, dest, false); apiErr != nil {
		return
	}

	if err := s.deps.Files.Unzip(c.Request.Context(), inst, req.Path, dest); err != nil {
		s.failFiles(c, err, inst, "unzip", req.Path)
		return
	}

	s.audit(c, "file.unzip", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"path": req.Path,
		"dest": dest,
	})
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "file_unzip", "path": req.Path, "dest": dest},
	})

	c.JSON(http.StatusOK, DataResponse{Data: gin.H{"path": req.Path, "dest": dest, "extracted": true}})
}

// resolveInstancePath is the single entry point for every client-supplied path
// in the file API.
//
// It applies ResolveInside (the §5.7 check), records the attempt in the audit
// log when it is rejected, and writes the error response itself so handlers
// cannot forget to. It returns (abs, nil) on success; on failure it has already
// written the response and returns a nil APIError sentinel via the second
// value being non-nil.
func (s *Server) resolveInstancePath(c *gin.Context, inst *Instance, rel string, requireExists bool) (string, *APIError) {
	root := inst.Dir
	if root == "" {
		apiErr := Internal(errors.New("instance has no working directory recorded"))
		Fail(c, apiErr)
		return "", apiErr
	}

	abs, err := ResolveInside(root, rel, requireExists)
	if err != nil {
		// A traversal attempt is an attack: 403, audited, and greppable.
		if errors.Is(err, ErrPathTraversal) || errors.Is(err, ErrPathSymlinkEscape) ||
			errors.Is(err, ErrPathAbsolute) || errors.Is(err, ErrPathNUL) {
			s.audit(c, "file.path_rejected", fmt.Sprintf("instance:%d", inst.ID), gin.H{
				"path":   rel,
				"reason": err.Error(),
				"ip":     ClientIP(c),
			})
			apiErr := Forbidden("路径被拒绝：%v", err).WithDetail(gin.H{
				"path":   rel,
				"reason": classifyPathError(err),
			})
			Fail(c, apiErr)
			return "", apiErr
		}
		if errors.Is(err, ErrPathNotExist) {
			apiErr := NotFound("%v", err).WithDetail(gin.H{"path": rel})
			Fail(c, apiErr)
			return "", apiErr
		}
		if errors.Is(err, ErrPathEmpty) {
			apiErr := ValidationFailed("必须提供 path 参数")
			Fail(c, apiErr)
			return "", apiErr
		}
		apiErr := Internal(err)
		Fail(c, apiErr)
		return "", apiErr
	}
	return abs, nil
}

// classifyPathError turns a traversal error into a stable machine-readable
// reason string for the response details.
func classifyPathError(err error) string {
	switch {
	case errors.Is(err, ErrPathAbsolute):
		return "absolute_path"
	case errors.Is(err, ErrPathNUL):
		return "null_byte"
	case errors.Is(err, ErrPathSymlinkEscape):
		return "symlink_escape"
	case errors.Is(err, ErrPathTraversal):
		return "path_traversal"
	default:
		return "rejected"
	}
}

// failFiles maps a FileService error onto a status.
func (s *Server) failFiles(c *gin.Context, err error, inst *Instance, op, rel string) {
	if IsNotImplemented(err) {
		FailNotImplemented(c, "file management (§6.4)", err)
		return
	}
	if errors.Is(err, ErrInvalid) {
		Fail(c, ValidationFailed("%v", err).WithDetail(gin.H{"path": rel, "operation": op}))
		return
	}
	if errors.Is(err, ErrPathTraversal) || errors.Is(err, ErrPathSymlinkEscape) {
		s.audit(c, "file.path_rejected", fmt.Sprintf("instance:%d", inst.ID), gin.H{
			"path": rel, "operation": op, "reason": err.Error(),
		})
		Fail(c, Forbidden("路径被拒绝：%v", err).WithDetail(gin.H{"path": rel, "reason": classifyPathError(err)}))
		return
	}
	Fail(c, Classify(err, fmt.Sprintf("无法对 %q 执行 %s", rel, op)))
}

// allowlist returns the sorted upload extension allowlist.
func allowlist() []string {
	out := make([]string, 0, len(uploadAllowlist))
	for ext := range uploadAllowlist {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

func allowlistString() string { return strings.Join(allowlist(), ", ") }

// readAllLimited reads everything from r, refusing to exceed maxBytes.
func readAllLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 512 << 20
	}
	limited := io.LimitReader(r, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, Internal(fmt.Errorf("reading request body: %w", err))
	}
	if int64(len(data)) > maxBytes {
		return nil, PayloadTooLarge("请求体大小超过 %s 的上限", humanSize(maxBytes))
	}
	return data, nil
}
