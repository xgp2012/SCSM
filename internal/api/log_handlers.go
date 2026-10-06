package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// This file implements the log endpoints (§5.2, §5.2.1).
//
// Historical log access delegates to the LogService; when it is a nop the
// handler answers 501 rather than an empty result, because "no log lines" and
// "no log backend" are very different statements to an operator debugging a
// server that will not start.

// Log tail defaults and caps.
const (
	// DefaultLogTail is the number of trailing lines returned when the client
	// does not ask for a specific count.
	DefaultLogTail = 200
	// MaxLogTail caps a single request, protecting the panel from a client
	// asking for a million lines of a multi-gigabyte log.
	MaxLogTail = 20000
	// MaxLogDownloadBytes caps a log download.
	MaxLogDownloadBytes = 64 << 20
)

// handleGetLogs returns historical log lines with optional tail and grep.
func (s *Server) handleGetLogs(c *gin.Context) {
	inst := MustInstance(c)

	var q LogsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		Fail(c, BadRequest("invalid log query: %v", err))
		return
	}

	tail := q.Tail
	if tail <= 0 {
		tail = DefaultLogTail
	}
	if tail > MaxLogTail {
		Fail(c, ValidationFailed("tail must be at most %d", MaxLogTail).
			WithDetail(gin.H{"max_tail": MaxLogTail}))
		return
	}

	// Validate the pattern here so a bad regex is a 422 with a clear message
	// rather than an opaque failure deep in the log reader.
	if q.Grep != "" {
		if _, err := regexp.Compile(q.Grep); err != nil {
			Fail(c, ValidationFailed("invalid grep pattern: %v", err).WithDetail(gin.H{
				"issues": []ValidationIssue{{
					Field:    "grep",
					Code:     "invalid_regex",
					Message:  err.Error(),
					Severity: "error",
				}},
			}))
			return
		}
	}

	switch q.Stream {
	case "", "stdout", "stderr", "both":
	default:
		Fail(c, ValidationFailed("stream must be one of stdout, stderr, both"))
		return
	}

	lines, err := s.deps.Logs.Tail(c.Request.Context(), inst, tail, q.Grep)
	if err != nil {
		if IsNotImplemented(err) {
			FailNotImplemented(c, "log service (§5.2)", err)
			return
		}
		if errors.Is(err, ErrInvalid) {
			Fail(c, ValidationFailed("%v", err))
			return
		}
		Fail(c, Classify(err, fmt.Sprintf("no logs found for instance %d", inst.ID)))
		return
	}
	if lines == nil {
		lines = []string{}
	}

	if q.Format == "text" {
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.String(http.StatusOK, strings.Join(lines, "\n"))
		return
	}

	c.JSON(http.StatusOK, DataResponse{Data: LogsResponse{
		InstanceID: inst.ID,
		Lines:      lines,
		Count:      len(lines),
		Tail:       tail,
		Grep:       q.Grep,
	}})
}

// handleDownloadLogs serves the raw log file.
func (s *Server) handleDownloadLogs(c *gin.Context) {
	inst := MustInstance(c)

	logPath, err := s.deps.Logs.LogsPath(c.Request.Context(), inst)
	if err != nil {
		if IsNotImplemented(err) {
			FailNotImplemented(c, "log service (§5.2)", err)
			return
		}
		Fail(c, Classify(err, fmt.Sprintf("no log file for instance %d", inst.ID)))
		return
	}

	// The log path comes from the log service, not from the client, but it is
	// still verified against the instance directory: a supervisor bug that
	// reported "/etc/passwd" must not turn into an arbitrary file read through
	// the panel.
	if inst.Dir != "" {
		if _, perr := ResolveInside(inst.Dir, relOrSelf(inst.Dir, logPath), true); perr != nil {
			s.audit(c, "log.download_rejected", fmt.Sprintf("instance:%d", inst.ID), gin.H{
				"log_path": logPath,
				"reason":   perr.Error(),
			})
			Fail(c, Internal(fmt.Errorf("the log path is outside the instance directory")))
			return
		}
	}

	f, err := os.Open(logPath)
	if err != nil {
		Fail(c, Classify(err, "the log file does not exist yet"))
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		Fail(c, Classify(err, "could not stat the log file"))
		return
	}

	s.audit(c, "log.download", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"file":       logPath,
		"size_bytes": st.Size(),
	})

	name := sanitizeDownloadName(inst.Name + "-console.log")
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeContent(c.Writer, c.Request, name, st.ModTime(), f)
}

// relOrSelf returns the path of target relative to root when it is inside root,
// otherwise target itself so ResolveInside reports the escape.
func relOrSelf(root, target string) string {
	rel, err := filepathRel(root, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		// Return something that cannot resolve inside, so the caller rejects it.
		return target
	}
	return rel
}

// ---------------------------------------------------------------------------
// Backups (§6.5)
// ---------------------------------------------------------------------------

// handleListBackups lists backups, optionally filtered by instance.
func (s *Server) handleListBackups(c *gin.Context) {
	var filter BackupFilter
	if v := c.Query("instance_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id < 0 {
			Fail(c, ValidationFailed("instance_id must be a non-negative integer"))
			return
		}
		filter.InstanceID = id
	}
	filter.Limit = queryInt(c, "limit", 100, 1, 1000)
	filter.Offset = queryInt(c, "offset", 0, 0, 1<<30)

	backups, err := s.deps.Backups.List(c.Request.Context(), filter)
	if err != nil {
		if IsNotImplemented(err) {
			FailNotImplemented(c, "backup store (§5.5 backups)", err)
			return
		}
		Fail(c, Classify(err, "no backups found"))
		return
	}
	if backups == nil {
		backups = []Backup{}
	}

	c.JSON(http.StatusOK, BackupListResponse{Data: backups, Total: len(backups)})
}

// handleCreateBackup creates a manual backup of an instance.
func (s *Server) handleCreateBackup(c *gin.Context) {
	inst := MustInstance(c)

	var req CreateBackupRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, BadRequest("invalid backup request: %v", err))
			return
		}
	}

	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "manual"
	}
	switch kind {
	case "manual", "scheduled", "pre-start":
	default:
		Fail(c, ValidationFailed("kind must be one of manual, scheduled, pre-start"))
		return
	}

	backup, err := s.deps.Backup.Create(c.Request.Context(), inst, kind, req.Note)
	if err != nil {
		if IsNotImplemented(err) {
			FailNotImplemented(c, "backup service (§6.5)", err)
			return
		}
		Fail(c, Classify(err, "could not create the backup"))
		return
	}

	s.audit(c, "backup.create", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"backup_id":  backup.ID,
		"kind":       kind,
		"size_bytes": backup.SizeBytes,
		"note":       req.Note,
	})
	s.deps.Events.Publish(Event{
		Type:       EventBackup,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "created", "backup_id": backup.ID, "kind": kind},
	})

	c.JSON(http.StatusCreated, DataResponse{Data: backup})
}

// handleRestoreBackup restores a backup by id.
func (s *Server) handleRestoreBackup(c *gin.Context) {
	backupID, apiErr := int64Param(c, "id", "backup id")
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	backup, err := s.deps.Backups.GetByID(c.Request.Context(), backupID)
	if err != nil {
		Fail(c, Classify(err, fmt.Sprintf("backup %d not found", backupID)))
		return
	}

	inst, err := s.deps.Instances.GetByID(c.Request.Context(), backup.InstanceID)
	if err != nil {
		Fail(c, Classify(err, fmt.Sprintf("instance %d for backup %d not found", backup.InstanceID, backupID)))
		return
	}

	// Instance-level authorization applies even though the route is keyed on
	// the backup, not the instance.
	p := MustPrincipal(c)
	if err := s.deps.RBAC.AuthorizeInstance(p.Role, p.UserID, inst.OwnerID, inst.ID, permInstanceBackup); err != nil {
		Fail(c, Forbidden("%s", err.Error()))
		return
	}

	if live, ok := s.deps.Process.State(inst.ID); ok && live.State == StateRunning {
		Fail(c, Conflict("the instance is running; stop it before restoring a backup").
			WithDetail(gin.H{"state": live.State, "instance_id": inst.ID}))
		return
	}

	if err := s.deps.Backup.Restore(c.Request.Context(), inst, backupID); err != nil {
		if IsNotImplemented(err) {
			FailNotImplemented(c, "backup service (§6.5)", err)
			return
		}
		Fail(c, Classify(err, "could not restore the backup"))
		return
	}

	s.audit(c, "backup.restore", fmt.Sprintf("backup:%d", backupID), gin.H{
		"instance_id": inst.ID,
		"path":        backup.Path,
	})
	s.deps.Events.Publish(Event{
		Type:       EventBackup,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "restored", "backup_id": backupID},
	})

	c.JSON(http.StatusOK, DataResponse{Data: OKResponse{OK: true}})
}

// handleDeleteBackup deletes a backup row.
func (s *Server) handleDeleteBackup(c *gin.Context) {
	backupID, apiErr := int64Param(c, "id", "backup id")
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	backup, err := s.deps.Backups.GetByID(c.Request.Context(), backupID)
	if err != nil {
		Fail(c, Classify(err, fmt.Sprintf("backup %d not found", backupID)))
		return
	}

	p := MustPrincipal(c)
	if inst, ierr := s.deps.Instances.GetByID(c.Request.Context(), backup.InstanceID); ierr == nil && inst != nil {
		if aerr := s.deps.RBAC.AuthorizeInstance(p.Role, p.UserID, inst.OwnerID, inst.ID, permInstanceBackup); aerr != nil {
			Fail(c, Forbidden("%s", aerr.Error()))
			return
		}
	}

	if err := s.deps.Backups.Delete(c.Request.Context(), backupID); err != nil {
		Fail(c, Classify(err, fmt.Sprintf("backup %d not found", backupID)))
		return
	}

	s.audit(c, "backup.delete", fmt.Sprintf("backup:%d", backupID), gin.H{
		"instance_id": backup.InstanceID,
		"path":        backup.Path,
		"size_bytes":  backup.SizeBytes,
	})

	c.JSON(http.StatusOK, DataResponse{Data: OKResponse{OK: true}})
}
