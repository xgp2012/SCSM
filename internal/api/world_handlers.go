package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// This file implements the world (save game) endpoints (§6.3).
//
// Every handler here delegates to the WorldService interface. When that service
// is the nop implementation, the handler answers 501 with code
// "not_implemented" and names the feature — it never returns an empty world list,
// because an empty list reads as "this instance has no saves", which is a
// different and misleading statement.

// worldService delegates and centralises the 501 translation.
func (s *Server) worldService(c *gin.Context) (WorldService, bool) {
	if s.deps.World == nil {
		FailNotImplemented(c, "world management (§6.3)", nil)
		return nil, false
	}
	return s.deps.World, true
}

// handleListWorlds lists the save games under Worlds/.
func (s *Server) handleListWorlds(c *gin.Context) {
	inst := MustInstance(c)

	svc, ok := s.worldService(c)
	if !ok {
		return
	}

	worlds, err := svc.List(c.Request.Context(), inst)
	if err != nil {
		s.failWorld(c, err, inst, "list")
		return
	}

	active := ""
	out := make([]WorldResponse, 0, len(worlds))
	for _, w := range worlds {
		if w.Active {
			active = w.DirName
		}
		out = append(out, WorldResponse{WorldInfo: w, SizeHuman: humanSize(w.SizeBytes)})
	}

	c.JSON(http.StatusOK, WorldListResponse{Data: out, Total: len(out), ActiveDir: active})
}

// handleImportWorld imports a world from an uploaded zip.
func (s *Server) handleImportWorld(c *gin.Context) {
	inst := MustInstance(c)

	svc, ok := s.worldService(c)
	if !ok {
		return
	}

	// Accept either a multipart upload or a raw zip body.
	data, name, err := readZipUpload(c, s.deps.ConfigHTTP.MaxUploadBytes)
	if err != nil {
		Fail(c, Classify(err, "world archive not found"))
		return
	}

	world, err := svc.Import(c.Request.Context(), inst, name, data)
	if err != nil {
		s.failWorld(c, err, inst, "import")
		return
	}

	s.audit(c, "world.import", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"world": world.DirName,
		"bytes": len(data),
	})
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "world_import", "world": world.DirName},
	})

	c.JSON(http.StatusCreated, DataResponse{Data: WorldResponse{WorldInfo: *world, SizeHuman: humanSize(world.SizeBytes)}})
}

// handleExportWorld streams a world as a zip.
func (s *Server) handleExportWorld(c *gin.Context) {
	inst := MustInstance(c)

	svc, ok := s.worldService(c)
	if !ok {
		return
	}

	dirName, apiErr := s.worldDirParam(c)
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	// Regions/ is the bulk of a save; excluding it makes an export small
	// enough to move between hosts when only the world metadata is wanted.
	includeRegions := c.Query("include_regions") != "false"

	data, filename, err := svc.Export(c.Request.Context(), inst, dirName, includeRegions)
	if err != nil {
		s.failWorld(c, err, inst, "export")
		return
	}

	s.audit(c, "world.export", fmt.Sprintf("instance:%d/worlds/%s", inst.ID, dirName), gin.H{
		"include_regions": includeRegions,
		"bytes":           len(data),
	})

	if filename == "" {
		filename = dirName + ".zip"
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", sanitizeDownloadName(filename)))
	c.Data(http.StatusOK, "application/zip", data)
}

// handleBackupWorld creates a backup of one world.
func (s *Server) handleBackupWorld(c *gin.Context) {
	inst := MustInstance(c)

	svc, ok := s.worldService(c)
	if !ok {
		return
	}

	dirName, apiErr := s.worldDirParam(c)
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	var req WorldBackupRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, BadRequest("invalid backup request: %v", err))
			return
		}
	}

	backup, err := svc.Backup(c.Request.Context(), inst, dirName, req.Note)
	if err != nil {
		s.failWorld(c, err, inst, "backup")
		return
	}

	s.audit(c, "world.backup", fmt.Sprintf("instance:%d/worlds/%s", inst.ID, dirName), gin.H{
		"backup_id":  backup.ID,
		"size_bytes": backup.SizeBytes,
		"path":       backup.Path,
	})

	c.JSON(http.StatusCreated, DataResponse{Data: backup})
}

// handleRestoreWorld restores a world from a backup.
func (s *Server) handleRestoreWorld(c *gin.Context) {
	inst := MustInstance(c)

	svc, ok := s.worldService(c)
	if !ok {
		return
	}

	dirName, apiErr := s.worldDirParam(c)
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	var req WorldRestoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("invalid restore request: %v", err))
		return
	}

	// Destructive and irreversible: require an explicit confirmation, as §6.3
	// asks for the restore path.
	if !req.Confirm {
		Fail(c, Conflict("restore replaces the current world content; set \"confirm\": true to proceed").
			WithDetail(gin.H{"world": dirName, "backup_id": req.BackupID}))
		return
	}
	// A restore while the server is writing regions would corrupt the save.
	if live, ok := s.deps.Process.State(inst.ID); ok && live.State == StateRunning {
		Fail(c, Conflict("the instance is running; stop it before restoring a world").
			WithDetail(gin.H{"state": live.State, "world": dirName}))
		return
	}

	if err := svc.Restore(c.Request.Context(), inst, dirName, req.BackupID); err != nil {
		s.failWorld(c, err, inst, "restore")
		return
	}

	s.audit(c, "world.restore", fmt.Sprintf("instance:%d/worlds/%s", inst.ID, dirName), gin.H{
		"backup_id": req.BackupID,
	})
	s.deps.Events.Publish(Event{
		Type:       EventStateChange,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "world_restore", "world": dirName},
	})

	c.JSON(http.StatusOK, DataResponse{Data: OKResponse{OK: true}})
}

// handleActivateWorld switches the active save.
//
// The only field that actually switches a save is ServerSetting.json's
// WorldPath; WorldName is a display name (§2.7, §6.3). Activation is refused
// while the instance runs, because the server reads WorldPath at startup.
func (s *Server) handleActivateWorld(c *gin.Context) {
	inst := MustInstance(c)

	svc, ok := s.worldService(c)
	if !ok {
		return
	}

	dirName, apiErr := s.worldDirParam(c)
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	if live, ok := s.deps.Process.State(inst.ID); ok {
		switch live.State {
		case StateRunning, StateStarting, StateRestarting:
			Fail(c, Conflict("the instance is %s; stop it before switching the active world", live.State).
				WithDetail(gin.H{"state": live.State, "world": dirName}))
			return
		}
	}

	if err := svc.Activate(c.Request.Context(), inst, dirName); err != nil {
		s.failWorld(c, err, inst, "activate")
		return
	}

	s.audit(c, "world.activate", fmt.Sprintf("instance:%d/worlds/%s", inst.ID, dirName), gin.H{
		"world_path": "app:/Worlds/" + dirName,
	})
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "world_activate", "world": dirName},
	})

	c.JSON(http.StatusOK, DataResponse{Data: gin.H{
		"active_dir":       dirName,
		"world_path":       "app:/Worlds/" + dirName,
		"restart_required": true,
	}})
}

// handleDeleteWorld deletes a save. The service backs it up first (§6.3).
func (s *Server) handleDeleteWorld(c *gin.Context) {
	inst := MustInstance(c)

	svc, ok := s.worldService(c)
	if !ok {
		return
	}

	dirName, apiErr := s.worldDirParam(c)
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	// Deleting the active world would leave ServerSetting.json pointing at
	// nothing. The service enforces this too; catching it here produces a much
	// clearer message.
	if live, ok := s.deps.Process.State(inst.ID); ok && live.State == StateRunning {
		Fail(c, Conflict("the instance is running; stop it before deleting a world").
			WithDetail(gin.H{"state": live.State, "world": dirName}))
		return
	}

	if err := svc.Delete(c.Request.Context(), inst, dirName); err != nil {
		s.failWorld(c, err, inst, "delete")
		return
	}

	s.audit(c, "world.delete", fmt.Sprintf("instance:%d/worlds/%s", inst.ID, dirName), gin.H{"world": dirName})
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "world_delete", "world": dirName},
	})

	c.JSON(http.StatusOK, DataResponse{Data: OKResponse{OK: true}})
}

// worldDirParam validates the :w path parameter as a bare directory name.
//
// The world identifier is the *directory* name, not the display name (§2.7).
// It is validated here before reaching any service, so a traversal attempt is
// rejected at the API boundary with a clear 422.
func (s *Server) worldDirParam(c *gin.Context) (string, *APIError) {
	raw := strings.TrimSpace(c.Param("w"))
	if raw == "" {
		return "", ValidationFailed("a world directory name is required")
	}
	if strings.ContainsAny(raw, `/\`) || raw == "." || raw == ".." || strings.Contains(raw, "..") {
		return "", ValidationFailed("world directory name must not contain path separators or \"..\"").
			WithDetail(gin.H{"world": raw})
	}
	if strings.ContainsRune(raw, 0) {
		return "", ValidationFailed("world directory name must not contain NUL bytes")
	}
	if len(raw) > 128 {
		return "", ValidationFailed("world directory name is too long")
	}
	return raw, nil
}

// failWorld maps a WorldService error onto a status, with the 501 translation
// for a nop backend.
func (s *Server) failWorld(c *gin.Context, err error, inst *Instance, op string) {
	if IsNotImplemented(err) {
		FailNotImplemented(c, "world management (§6.3)", err)
		return
	}
	if errors.Is(err, ErrNotImplemented) {
		FailNotImplemented(c, "world management (§6.3)", err)
		return
	}
	Fail(c, Classify(err, fmt.Sprintf("could not %s worlds for instance %d", op, inst.ID)))
}

// sanitizeDownloadName strips characters that would break a
// Content-Disposition header (header injection) or produce a nonsense filename.
func sanitizeDownloadName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '"' || r == '\\' || r == '\r' || r == '\n' || r == 0:
			continue
		case r < 0x20:
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = "download"
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// readZipUpload extracts the archive bytes from either a multipart form field
// ("file") or the raw request body.
//
// The size cap is enforced while reading, so an oversize upload cannot exhaust
// memory even if the client lies about Content-Length.
func readZipUpload(c *gin.Context, maxBytes int64) (data []byte, name string, err error) {
	fh, ferr := c.FormFile("file")
	if ferr == nil && fh != nil {
		if maxBytes > 0 && fh.Size > maxBytes {
			return nil, "", PayloadTooLarge("the archive exceeds the %s upload limit", humanSize(maxBytes))
		}
		f, oerr := fh.Open()
		if oerr != nil {
			return nil, "", BadRequest("could not read the uploaded archive: %v", oerr)
		}
		defer f.Close()

		buf, rerr := readAllLimited(f, maxBytes)
		if rerr != nil {
			return nil, "", rerr
		}
		return buf, fh.Filename, nil
	}

	if c.Request.Body == nil {
		return nil, "", BadRequest("an archive is required (multipart field \"file\" or a raw request body)")
	}
	buf, rerr := readAllLimited(c.Request.Body, maxBytes)
	if rerr != nil {
		return nil, "", rerr
	}
	if len(buf) == 0 {
		return nil, "", BadRequest("the request body is empty")
	}
	return buf, c.Query("name"), nil
}
