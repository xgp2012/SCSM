package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// This file implements the panel-level endpoints: health, system information and
// the small shared parsing helpers the other handler files use.

// handleHealth is the liveness probe. It is unauthenticated and must answer
// before setup is complete, so a systemd unit or container healthcheck can run
// against a panel whose admin password has not been set yet.
func (s *Server) handleHealth(c *gin.Context) {
	uptime := int64(s.now().Sub(s.startedAt).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	c.JSON(http.StatusOK, DataResponse{Data: HealthResponse{
		Status:  "ok",
		Uptime:  uptime,
		Version: s.deps.Version.Version,
	}})
}

// handleSystemInfo reports host facts, the .NET runtime status and storage.
//
// Failure policy (§7): a missing .NET runtime is the single most likely reason a
// deployment fails, and it is an *expected* state on a fresh host. It is
// therefore reported as data — available=false with an actionable hint — and the
// endpoint still answers 200. Returning 5xx here would make a monitoring system
// page an operator for a condition the panel is already explaining clearly.
func (s *Server) handleSystemInfo(c *gin.Context) {
	info, err := s.deps.System.Info(c.Request.Context())
	if err != nil {
		if IsNotImplemented(err) {
			FailNotImplemented(c, "system service", err)
			return
		}
		Fail(c, Classify(err, "system information is unavailable"))
		return
	}
	if info == nil {
		Fail(c, Internal(errors.New("system service returned no information")))
		return
	}

	// The response contract is "runtimes is always an array". A service that
	// leaves it nil would serialise as null and break every client that
	// iterates it unconditionally, so the guarantee is enforced here rather
	// than trusted to each backend.
	if info.Dotnet.Runtimes == nil {
		info.Dotnet.Runtimes = []string{}
	}

	// Fold in the panel-side counters, which the system service does not own.
	panel := info.Panel
	instances, ierr := s.deps.Instances.List(c.Request.Context(), InstanceListFilter{})
	if ierr == nil {
		panel.Instances = len(instances)
		for i := range instances {
			if live, ok := s.deps.Process.State(instances[i].ID); ok {
				switch live.State {
				case StateRunning, StateStarting, StateRestarting:
					panel.RunningCount++
				}
			}
		}
	}
	if users, uerr := s.deps.Users.Count(c.Request.Context()); uerr == nil {
		panel.Users = users
	}
	panel.SingleUser = s.deps.RBAC.SingleUser()
	panel.SetupRequired = panel.Users == 0 || s.setupRequired(c)

	version := info.Version
	if s.deps.Version.Version != "" {
		version = s.deps.Version
	}

	c.JSON(http.StatusOK, DataResponse{Data: SystemInfoResponse{
		Version: version,
		Host:    info.Host,
		Dotnet:  info.Dotnet,
		Storage: info.Storage,
		Panel:   panel,
	}})
}

// setupRequired is a cheap best-effort check used only to decorate
// /system/info. A failure here must never fail the endpoint, so errors are
// reported as "not required".
func (s *Server) setupRequired(c *gin.Context) bool {
	users, err := s.deps.Users.List(c.Request.Context())
	if err != nil {
		return false
	}
	if len(users) == 0 {
		return true
	}
	for i := range users {
		u := &users[i]
		if u.Role == "admin" && u.HasPassword && strings.TrimSpace(u.PasswordHash) != "" {
			if !isFirstRunHash(u.PasswordHash) {
				return false
			}
		}
	}
	for i := range users {
		u := &users[i]
		if u.HasPassword && strings.TrimSpace(u.PasswordHash) != "" && !isFirstRunHash(u.PasswordHash) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Shared parsing helpers
// ---------------------------------------------------------------------------

// int64Param parses a positive int64 path parameter.
func int64Param(c *gin.Context, name, label string) (int64, *APIError) {
	raw := c.Param(name)
	v, err := parseInt64(raw)
	if err != nil || v <= 0 {
		return 0, ValidationFailed("%s 必须为正整数，实际为 %q", label, raw)
	}
	return v, nil
}

// parseInt64 parses a base-10 signed integer.
func parseInt64(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("empty")
	}
	var out int64
	neg := false
	if raw[0] == '-' {
		neg = true
		raw = raw[1:]
	} else if raw[0] == '+' {
		raw = raw[1:]
	}
	if raw == "" {
		return 0, errors.New("no digits")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid digit %q", r)
		}
		out = out*10 + int64(r-'0')
		if out < 0 {
			return 0, errors.New("overflow")
		}
	}
	if neg {
		out = -out
	}
	return out, nil
}

// queryInt reads an integer query parameter, clamping it into [min, max].
func queryInt(c *gin.Context, name string, def, min, max int) int {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return def
	}
	v, err := parseInt64(raw)
	if err != nil {
		return def
	}
	out := int(v)
	if out < min {
		return min
	}
	if out > max {
		return max
	}
	return out
}

// filepathRel is a thin wrapper so log_handlers.go can compute a relative path
// without importing path/filepath directly in two files.
func filepathRel(base, target string) (string, error) {
	return filepath.Rel(base, target)
}

var (
	_ = time.Now
	_ = http.StatusOK
)
