package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"scnetm/internal/auth"
)

// This file implements instance CRUD and the lifecycle endpoints.
//
// The design goal stated in the task is that every one of these must work with
// **zero** game-server dependency: creating an instance provisions a directory
// and the two configuration files the server needs (§6.1), and the lifecycle
// endpoints drive whatever ProcessManager is wired in — the NopProcessManager by
// default, which implements a real state machine with no process behind it.

// instanceNamePattern constrains an instance name.
//
// The name becomes a directory name under instances_dir, so it is validated as
// a filename *before* it is ever joined to a path: letters, digits, dot, dash
// and underscore only. This is the first line of defense; ResolveInside is the
// second.
var instanceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateInstanceName checks an instance name.
func ValidateInstanceName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: instance name is required", ErrInvalid)
	}
	if len(name) > 64 {
		return fmt.Errorf("%w: instance name must be at most 64 characters", ErrInvalid)
	}
	if !instanceNamePattern.MatchString(name) {
		return fmt.Errorf("%w: instance name must start with a letter or digit and contain only letters, digits, dot, dash and underscore", ErrInvalid)
	}
	// Reject the Windows reserved device names and the two relative-directory
	// names outright, so an instance directory can never be "." or "..".
	switch strings.ToLower(name) {
	case ".", "..", "con", "prn", "aux", "nul",
		"com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9",
		"lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
		return fmt.Errorf("%w: %q is a reserved name", ErrInvalid, name)
	}
	return nil
}

// handleListInstances returns every visible instance with live state merged in.
func (s *Server) handleListInstances(c *gin.Context) {
	p := MustPrincipal(c)

	filter := InstanceListFilter{}
	// In multi-user mode a non-admin sees only instances they own or hold a
	// grant on. In single-user mode the filter stays empty (see all).
	if !s.deps.RBAC.SingleUser() && p.Role != auth.RoleAdmin {
		filter.OwnerID = p.UserID
	}

	instances, err := s.deps.Instances.List(c.Request.Context(), filter)
	if err != nil {
		Fail(c, Classify(err, "no instances found"))
		return
	}

	// Fetch the used ports once for the whole listing rather than per row.
	portInUse := s.portUsage(instances)

	out := make([]InstanceResponse, 0, len(instances))
	summary := InstanceSummary{Total: len(instances)}
	for i := range instances {
		resp := s.instanceResponse(&instances[i], portInUse[instances[i].Port])
		out = append(out, resp)
		switch resp.State {
		case StateRunning, StateStarting, StateRestarting:
			summary.Running++
		case StateStopped, StateCreated:
			summary.Stopped++
		case StateCrashed:
			summary.Crashed++
		default:
			summary.Other++
		}
	}

	c.JSON(http.StatusOK, ListInstancesResponse{Data: out, Total: len(out), Summary: summary})
}

// portUsage probes the UDP ports of the given instances in parallel.
//
// UDP, not TCP: the game speaks LiteNetLib over UDP (§2.6, §6.6), so a UDP bind
// probe is the only meaningful "is the port listening" test.
func (s *Server) portUsage(instances []Instance) map[int]bool {
	out := make(map[int]bool, len(instances))
	type result struct {
		port int
		free bool
	}
	ch := make(chan result, len(instances))
	for i := range instances {
		port := instances[i].Port
		if port <= 0 {
			continue
		}
		go func(p int) {
			ch <- result{port: p, free: s.deps.Ports.IsFree(p)}
		}(port)
	}
	collected := 0
	for collected < len(instances) {
		select {
		case r := <-ch:
			out[r.port] = !r.free
			collected++
		case <-time.After(2 * time.Second):
			// A probe that hangs must not hang the listing.
			collected = len(instances)
		}
	}
	return out
}

// instanceResponse merges the stored row with live supervisor state.
func (s *Server) instanceResponse(inst *Instance, portInUse bool) InstanceResponse {
	return s.instanceResponseWithState(inst, portInUse, false)
}

// instanceResponseWithState is the variant used by single-instance handlers,
// where probing the port is worth the latency.
func (s *Server) instanceResponseWithState(inst *Instance, portInUse bool, probePort bool) InstanceResponse {
	if probePort && inst.Port > 0 {
		portInUse = !s.deps.Ports.IsFree(inst.Port)
	}

	resp := InstanceResponse{
		Instance:  *inst,
		State:     StateUnknown,
		Online:    false,
		PortInUse: portInUse,
	}

	live, ok := s.deps.Process.State(inst.ID)
	if ok {
		resp.State = live.State
		resp.Online = live.State == StateRunning
		resp.StateDetail = InstanceStateResponse{
			PID:           live.PID,
			Ready:         live.Ready,
			StartedAt:     live.StartedAt,
			StoppedAt:     live.StoppedAt,
			ExitCode:      live.ExitCode,
			Restarts:      live.Restarts,
			OnlinePlayers: live.OnlinePlayers,
			LastError:     live.LastError,
			StateSource:   "supervisor",
		}
		if live.StartedAt != nil && live.State == StateRunning {
			resp.StateDetail.UptimeSeconds = int64(s.now().Sub(*live.StartedAt).Seconds())
			if resp.StateDetail.UptimeSeconds < 0 {
				resp.StateDetail.UptimeSeconds = 0
			}
		}
		return resp
	}

	// Fall back to the persisted snapshot so a panel restart still shows
	// something truthful instead of "Unknown" for every instance.
	resp.State = StateUnknown
	resp.StateDetail.StateSource = "snapshot"
	if snap, err := s.deps.Instances.GetState(context.WithoutCancel(context.Background()), inst.ID); err == nil && snap != nil {
		resp.State = snap.State
		resp.Online = snap.State == StateRunning
		resp.StateDetail = InstanceStateResponse{
			PID:           snap.PID,
			Ready:         snap.State == StateRunning,
			StartedAt:     snap.StartedAt,
			StoppedAt:     snap.StoppedAt,
			ExitCode:      snap.ExitCode,
			OnlinePlayers: snap.OnlinePlayers,
			LastError:     snap.LastError,
			StateSource:   "snapshot",
		}
	}
	return resp
}

// handleCreateInstance provisions a new instance.
//
// Steps (§6.1):
//  1. validate the name and check uniqueness (409 on a duplicate),
//  2. allocate or validate a UDP port from the pool,
//  3. create the instance directory *inside* instances_dir (verified),
//  4. write the initial ServerSetting.json and Settings.xml so the config page
//     works before the first start (the "首启顺序陷阱" from §6.1),
//  5. persist the row.
//
// No game server, template or .NET runtime is required: the directory and the
// two config files are all this needs, and the instance is immediately
// startable against the NopProcessManager.
func (s *Server) handleCreateInstance(c *gin.Context) {
	p := MustPrincipal(c)

	var req CreateInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("invalid instance request: %v", err))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if err := ValidateInstanceName(req.Name); err != nil {
		Fail(c, ValidationFailed("%v", err).WithDetail(validationDetails("name", "invalid_name", err)))
		return
	}

	ctx := c.Request.Context()

	// Uniqueness: check explicitly so we can return a precise 409 rather than
	// relying on the store's error text.
	if existing, err := s.deps.Instances.GetByName(ctx, req.Name); err == nil && existing != nil {
		Fail(c, Conflict("an instance named %q already exists", req.Name).WithDetail(gin.H{
			"instance_id": existing.ID,
			"name":        req.Name,
		}))
		return
	} else if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNotImplemented) {
		Fail(c, Classify(err, "could not check instance name"))
		return
	}

	// --- port allocation (UDP) ---
	usedPorts, err := s.deps.Instances.ListUsedPorts(ctx)
	if err != nil && !errors.Is(err, ErrNotImplemented) {
		Fail(c, Classify(err, "could not read instance ports"))
		return
	}
	usedPorts = append(usedPorts, s.reservations.Held()...)

	port := req.Port
	if port == 0 {
		allocated, err := s.deps.Ports.Allocate(ctx, usedPorts)
		if err != nil {
			// Pool exhaustion is a conflict, not a server error: the operator
			// must extend the pool or free a port.
			Fail(c, Conflict("could not allocate a UDP port: %v", err).WithDetail(gin.H{
				"pool_start": s.portPool().Start,
				"pool_end":   s.portPool().End,
			}))
			return
		}
		port = allocated
	} else {
		if err := ValidatePort(port); err != nil {
			Fail(c, ValidationFailed("%v", err).WithDetail(validationDetails("port", "invalid_port", err)))
			return
		}
		for _, u := range usedPorts {
			if u == port {
				Fail(c, Conflict("UDP port %d is already assigned to another instance", port).WithDetail(gin.H{"port": port}))
				return
			}
		}
		// The plan calls this out as a common bug: the occupancy check MUST be
		// UDP. CheckPortFree binds a UDP socket; a TCP check would give both
		// false positives and false negatives here.
		if err := CheckPortFree(port); err != nil {
			Fail(c, Conflict("UDP port %d is already in use on this host", port).WithDetail(gin.H{
				"port":     port,
				"protocol": "udp",
			}))
			return
		}
	}

	// Reserve the port so two concurrent creates cannot pick the same one
	// before either has committed its row.
	if !s.reservations.Reserve(port) {
		Fail(c, Conflict("UDP port %d was claimed by another request", port))
		return
	}
	committed := false
	defer func() {
		if !committed {
			s.reservations.Release(port)
		}
	}()

	// --- directory ---
	root := s.instancesDir()
	if root == "" {
		Fail(c, Unavailable("the instances directory is not configured"))
		return
	}
	dirName := req.Name
	if req.Dir != "" {
		dirName = strings.TrimSpace(req.Dir)
		if err := ValidateInstanceName(dirName); err != nil {
			Fail(c, ValidationFailed("invalid directory name: %v", err))
			return
		}
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		Fail(c, Internal(fmt.Errorf("creating instances root %s: %w", root, err)))
		return
	}

	// ResolveInside is the single guard for every client-influenced path: it
	// rejects traversal, absolute paths, NUL bytes, Windows separators and
	// symlink escapes.
	dir, err := ResolveInside(root, dirName, false)
	if err != nil {
		Fail(c, Forbidden("instance directory rejected: %v", err).WithDetail(gin.H{"dir": dirName}))
		return
	}

	if _, statErr := os.Stat(dir); statErr == nil {
		Fail(c, Conflict("the instance directory %q already exists on disk", dirName).WithDetail(gin.H{
			"dir": dir,
		}))
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		Fail(c, Internal(fmt.Errorf("creating instance directory: %w", err)))
		return
	}

	// --- initial configuration (§6.1 step 3-5) ---
	created := &Instance{
		Name:           req.Name,
		Dir:            dir,
		Port:           port,
		OwnerID:        p.UserID,
		AutoStart:      req.AutoStart,
		AutoRestart:    req.AutoRestart,
		MaxRestart:     req.MaxRestart,
		StopTimeoutSec: req.StopTimeoutSec,
		ServerJar:      strings.TrimSpace(req.ServerJar),
		DotnetPath:     strings.TrimSpace(req.DotnetPath),
		Term:           "xterm-256color",
		ColorMode:      "enhanced",
		CreatedAt:      s.now().UTC(),
		Memo:           req.Memo,
	}
	// The current official server package has no native launcher, so the run
	// line is `dotnet <ServerJar>`. Default to the assembly name from §2.1 when
	// the caller does not override it, otherwise the instance could never be
	// started and the failure would only surface at first start.
	if created.ServerJar == "" {
		created.ServerJar = "Survivalcraft.dll"
	}
	if created.OwnerID == 0 {
		created.OwnerID = 1
	}
	if created.MaxRestart <= 0 {
		created.MaxRestart = 5
	}
	if created.StopTimeoutSec <= 0 {
		created.StopTimeoutSec = 30
	}

	// Write the baseline config files. A failure here rolls the directory back
	// so the operator does not end up with an orphaned half-provisioned dir.
	if err := s.provisionInstanceDir(created, &req); err != nil {
		// Best-effort rollback; the safety check keeps this from ever touching
		// anything outside the instances root.
		if rmErr := SafeRemoveAll(root, dir); rmErr != nil {
			loggerFrom(c).Warn("could not roll back instance directory",
				"dir", dir, "err", rmErr.Error())
		}
		Fail(c, Internal(fmt.Errorf("provisioning instance directory: %w", err)))
		return
	}

	if err := s.deps.Instances.Create(ctx, created); err != nil {
		_ = SafeRemoveAll(root, dir)
		if errors.Is(err, ErrConflict) {
			Fail(c, Conflict("an instance named %q already exists", req.Name))
			return
		}
		if errors.Is(err, ErrInvalid) {
			Fail(c, ValidationFailed("%v", err))
			return
		}
		Fail(c, Classify(err, "could not create instance"))
		return
	}
	committed = true
	s.reservations.Release(port)

	// Seed the state snapshot so the row renders as Created rather than
	// Unknown before the first start.
	if err := s.deps.Instances.SaveState(ctx, &InstanceState{
		InstanceID: created.ID,
		State:      StateCreated,
	}); err != nil && !errors.Is(err, ErrNotImplemented) {
		loggerFrom(c).Warn("could not seed instance state", "instance_id", created.ID, "err", err.Error())
	}
	if nop, ok := s.deps.Process.(*NopProcessManager); ok {
		nop.EnsureInstance(created.ID)
	}

	s.audit(c, "instance.create", fmt.Sprintf("instance:%d", created.ID), gin.H{
		"name":     created.Name,
		"dir":      created.Dir,
		"port":     created.Port,
		"udp":      true,
		"port_src": portSource(req.Port),
	})
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: created.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "created", "name": created.Name},
	})

	resp := s.instanceResponseWithState(created, false, true)

	// Optionally start immediately.
	if req.AutoStart {
		if err := s.deps.Process.Start(ctx, created.ID); err != nil {
			// The instance exists; report the failed auto-start in the payload
			// rather than failing the whole request.
			loggerFrom(c).Warn("auto-start failed", "instance_id", created.ID, "err", err.Error())
			resp = s.instanceResponseWithState(created, false, true)
			body := DataResponse{Data: gin.H{
				"instance":    resp,
				"auto_start":  false,
				"start_error": err.Error(),
			}}
			c.JSON(http.StatusCreated, body)
			return
		}
		resp = s.instanceResponseWithState(created, false, true)
	}

	c.JSON(http.StatusCreated, DataResponse{Data: resp})
}

func portSource(requested int) string {
	if requested == 0 {
		return "pool"
	}
	return "explicit"
}

// provisionInstanceDir writes the minimal file set an instance needs before its
// first start (§6.1: the server only generates its config on first run, so the
// panel must seed a baseline or the config page is empty).
func (s *Server) provisionInstanceDir(inst *Instance, req *CreateInstanceRequest) error {
	// Subdirectories the server expects to find.
	for _, sub := range []string{"Worlds", "Configs"} {
		dir, err := ResolveInside(inst.Dir, sub, false)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", sub, err)
		}
	}

	// ServerSetting.json — the panel's single write entry point for world
	// settings (§6.3.1). Only WorldPath (the directory-determining field) and
	// WorldName are seeded here; everything else is left to the server's
	// defaults so a future server version's new fields are not pre-empted.
	worldName := strings.TrimSpace(req.WorldName)
	if worldName == "" {
		worldName = inst.Name + "World"
	}
	// The directory component of WorldPath must be a legal directory name
	// (§6.2 validator): no separators, no "..".
	worldDir := sanitizeWorldDir(worldName)
	worldPath := "app:/Worlds/" + worldDir

	maxPlayers := req.MaxPlayers
	if maxPlayers <= 0 {
		maxPlayers = 20
	}
	if maxPlayers > 255 {
		// MaxOnlinePlayerCount is a ushort in the save, but a sane server
		// cannot exceed this in practice.
		maxPlayers = 255
	}

	setting := map[string]any{
		"WorldPath":            worldPath,
		"WorldName":            worldName,
		"MaxOnlinePlayerCount": maxPlayers,
		"ServerPort":           inst.Port,
	}
	if req.WorldSeed != "" {
		// Only the string form is written: WorldSeed (int) is derived by the
		// server (§6.3.1 note 2).
		setting["WorldSeedString"] = req.WorldSeed
	}
	if req.GameMode != nil {
		if err := ValidateGameMode(*req.GameMode); err != nil {
			return err
		}
		setting["GameMode"] = *req.GameMode
	}
	if req.WorldPassword != "" {
		setting["WorldPassword"] = req.WorldPassword
	}

	path, err := ResolveInside(inst.Dir, "ServerSetting.json", false)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(path, setting); err != nil {
		return err
	}

	// Settings.xml — only the keys the panel owns are written, matching the
	// "只改目标键" rule of §6.2.
	xmlPath, err := ResolveInside(inst.Dir, "Settings.xml", false)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(xmlPath, []byte(settingsXMLTemplate(inst.Port)), 0o644); err != nil {
		return err
	}

	return nil
}

// sanitizeWorldDir reduces a display name to a legal world directory name.
//
// §6.2 requires the WorldPath suffix to contain no path separators and no "..".
// Chinese characters are explicitly allowed (world display names commonly are),
// so this does not restrict to ASCII — it removes the dangerous characters and
// collapses whitespace.
func sanitizeWorldDir(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == 0:
			continue
		case r < 0x20:
			continue
		case r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ".")
	if out == "" || out == "." || out == ".." {
		out = "World"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// ValidateGameMode checks the numeric ServerSetting.json game mode range.
//
// §6.2.1 documents 0-6 as the inferred mapping. The value is an int in
// ServerSetting.json but a *string* inside the save's Project.json, so the two
// must never be confused; this validator only covers the int form.
func ValidateGameMode(mode int) error {
	if mode < 0 || mode > 6 {
		return fmt.Errorf("%w: game_mode must be between 0 (Creative) and 6, got %d", ErrInvalid, mode)
	}
	return nil
}

// settingsXMLTemplate is a minimal Settings.xml carrying just ServerPort.
//
// The real file is generated by the server on first run; this baseline exists so
// that the config editor has something to show and so the port is set before the
// first start.
func settingsXMLTemplate(port int) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<Settings>
  <Setting Name="ServerPort" Value="` + itoa(int64(port)) + `" />
</Settings>
`
}

// handleGetInstance returns one instance with live state and the port probe.
func (s *Server) handleGetInstance(c *gin.Context) {
	inst := MustInstance(c)
	c.JSON(http.StatusOK, DataResponse{Data: s.instanceResponseWithState(inst, false, true)})
}

// handleUpdateInstance updates the mutable instance metadata (memo, timeouts,
// auto-restart policy). It deliberately does not touch the port or directory:
// changing those behind a running process would be a footgun.
func (s *Server) handleUpdateInstance(c *gin.Context) {
	inst := MustInstance(c)

	var req struct {
		Memo           *string `json:"memo,omitempty"`
		AutoStart      *bool   `json:"auto_start,omitempty"`
		AutoRestart    *bool   `json:"auto_restart,omitempty"`
		MaxRestart     *int    `json:"max_restart,omitempty"`
		StopTimeoutSec *int    `json:"stop_timeout_sec,omitempty"`
		ColorMode      *string `json:"color_mode,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("invalid update request: %v", err))
		return
	}

	before := *inst
	if req.Memo != nil {
		inst.Memo = *req.Memo
	}
	if req.AutoStart != nil {
		inst.AutoStart = *req.AutoStart
	}
	if req.AutoRestart != nil {
		inst.AutoRestart = *req.AutoRestart
	}
	if req.MaxRestart != nil {
		if *req.MaxRestart < 0 || *req.MaxRestart > 100 {
			Fail(c, ValidationFailed("max_restart must be between 0 and 100"))
			return
		}
		inst.MaxRestart = *req.MaxRestart
	}
	if req.StopTimeoutSec != nil {
		if *req.StopTimeoutSec < 0 || *req.StopTimeoutSec > 600 {
			Fail(c, ValidationFailed("stop_timeout_sec must be between 0 and 600"))
			return
		}
		inst.StopTimeoutSec = *req.StopTimeoutSec
	}
	if req.ColorMode != nil {
		switch *req.ColorMode {
		case "enhanced", "basic":
			inst.ColorMode = *req.ColorMode
		default:
			Fail(c, ValidationFailed("color_mode must be \"enhanced\" or \"basic\""))
			return
		}
	}

	if err := s.deps.Instances.Update(c.Request.Context(), inst); err != nil {
		Fail(c, Classify(err, "instance not found"))
		return
	}

	s.audit(c, "instance.update", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"before_memo":  before.Memo,
		"after_memo":   inst.Memo,
		"auto_start":   inst.AutoStart,
		"auto_restart": inst.AutoRestart,
		"max_restart":  inst.MaxRestart,
		"stop_timeout": inst.StopTimeoutSec,
		"color_mode":   inst.ColorMode,
	})
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "updated", "name": inst.Name},
	})

	c.JSON(http.StatusOK, DataResponse{Data: s.instanceResponseWithState(inst, false, false)})
}

// handleDeleteInstance removes an instance row and, optionally, its directory.
//
// Deleting files requires an explicit flag *and* the instance name as
// confirmation (§6.3 "二次确认" applied to the most destructive operation), and
// the directory is removed through SafeRemoveAll, which verifies the resolved
// target is strictly inside instances_dir — so a corrupted or hostile `dir`
// column cannot turn this into an arbitrary recursive delete.
func (s *Server) handleDeleteInstance(c *gin.Context) {
	inst := MustInstance(c)
	ctx := c.Request.Context()

	// A body is optional; an empty body means "delete the row only".
	var req DeleteInstanceRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, BadRequest("invalid delete request: %v", err))
			return
		}
	}

	if req.DeleteDir {
		if req.ConfirmName != inst.Name {
			Fail(c, ValidationFailed("confirm_name must exactly match the instance name %q to delete its directory", inst.Name).WithDetail(gin.H{
				"expected": inst.Name,
				"got":      req.ConfirmName,
			}))
			return
		}
		// Refuse while the process is live: removing a running instance's
		// working directory is never what the operator wants.
		if live, ok := s.deps.Process.State(inst.ID); ok {
			switch live.State {
			case StateRunning, StateStarting, StateStopping, StateRestarting:
				Fail(c, Conflict("instance %q is %s; stop it before deleting its directory", inst.Name, live.State).
					WithDetail(gin.H{"state": live.State}))
				return
			}
		}
	}

	if err := s.deps.Instances.Delete(ctx, inst.ID); err != nil {
		Fail(c, Classify(err, "instance not found"))
		return
	}

	result := gin.H{
		"id":          inst.ID,
		"name":        inst.Name,
		"dir_removed": false,
	}
	if req.DeleteDir {
		root := s.instancesDir()
		if root == "" {
			root = filepath.Dir(inst.Dir)
		}
		if err := SafeRemoveAll(root, inst.Dir); err != nil {
			// The row is gone; report the file-level failure explicitly rather
			// than pretending the delete was clean.
			s.audit(c, "instance.delete", fmt.Sprintf("instance:%d", inst.ID), gin.H{
				"name":       inst.Name,
				"delete_dir": true,
				"dir_error":  err.Error(),
			})
			Fail(c, Internal(fmt.Errorf("instance deleted but its directory could not be removed: %w", err)))
			return
		}
		result["dir_removed"] = true
		result["dir"] = inst.Dir
	}

	s.audit(c, "instance.delete", fmt.Sprintf("instance:%d", inst.ID), result)
	s.deps.Events.Publish(Event{
		Type:       EventInstance,
		InstanceID: inst.ID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"action": "deleted", "name": inst.Name, "dir_removed": req.DeleteDir},
	})

	c.JSON(http.StatusOK, DataResponse{Data: result})
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// handleStartInstance starts an instance.
//
// Starting an already-running instance is the plan's canonical state conflict
// and must be a 409, not a 500 and not a silent no-op.
func (s *Server) handleStartInstance(c *gin.Context) {
	inst := MustInstance(c)
	ctx := c.Request.Context()

	// Pre-check so the conflict is reported with the current state attached,
	// even if the supervisor's Start would also catch it.
	if live, ok := s.deps.Process.State(inst.ID); ok {
		switch live.State {
		case StateRunning:
			Fail(c, Conflict("instance %q is already running", inst.Name).WithDetail(gin.H{
				"instance_id": inst.ID,
				"state":       live.State,
				"pid":         live.PID,
			}))
			return
		case StateStarting, StateRestarting:
			Fail(c, Conflict("instance %q is already starting", inst.Name).WithDetail(gin.H{
				"instance_id": inst.ID,
				"state":       live.State,
			}))
			return
		}
	}

	// The instance directory must exist: starting a server whose working
	// directory vanished would fail deep inside the process launcher.
	root := s.instancesDir()
	if root != "" && inst.Dir != "" {
		if _, err := ResolveInside(root, filepath.Base(inst.Dir), true); err != nil {
			Fail(c, Conflict("the instance directory is missing or unsafe: %v", err).WithDetail(gin.H{
				"dir": inst.Dir,
			}))
			return
		}
	}

	if err := s.deps.Process.Start(ctx, inst.ID); err != nil {
		if errors.Is(err, ErrConflict) {
			Fail(c, Conflict("%v", err).WithDetail(gin.H{"instance_id": inst.ID}))
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			Fail(c, Timeout("starting instance %q timed out", inst.Name))
			return
		}
		Fail(c, Classify(err, "could not start the instance"))
		return
	}

	if err := s.saveState(ctx, inst.ID, StateRunning, nil); err != nil {
		loggerFrom(c).Warn("could not persist instance state", "instance_id", inst.ID, "err", err.Error())
	}

	s.audit(c, "instance.start", fmt.Sprintf("instance:%d", inst.ID), gin.H{"name": inst.Name})
	s.publishStateChange(inst.ID, StateRunning)

	c.JSON(http.StatusOK, DataResponse{Data: s.instanceResponseWithState(inst, false, true)})
}

// handleStopInstance stops an instance, gracefully unless force is set.
//
// A stop that does not actually terminate the process is reported as 504: the
// operation was accepted but exceeded its deadline (§5.6 status convention).
func (s *Server) handleStopInstance(c *gin.Context) {
	inst := MustInstance(c)

	var req StopInstanceRequest
	// An empty body is legal and means "graceful stop".
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, BadRequest("invalid stop request: %v", err))
			return
		}
	}

	timeout := time.Duration(inst.StopTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if req.TimeoutSec > 0 {
		if req.TimeoutSec > 600 {
			Fail(c, ValidationFailed("timeout_sec must be at most 600"))
			return
		}
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}

	// A stop on a stopped instance is not a conflict: it is idempotent and the
	// operator's intent is satisfied. Report it as such rather than 409.
	if live, ok := s.deps.Process.State(inst.ID); ok {
		if live.State == StateStopped || live.State == StateCreated {
			c.JSON(http.StatusOK, DataResponse{Data: StopInstanceResponse{
				InstanceID: inst.ID,
				Forced:     req.Force,
				Exited:     true,
				State:      live.State,
			}})
			return
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout+5*time.Second)
	defer cancel()

	result, err := s.deps.Process.Stop(ctx, inst.ID, req.Force)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			s.audit(c, "instance.stop_timeout", fmt.Sprintf("instance:%d", inst.ID), gin.H{
				"force":      req.Force,
				"timeout_ms": timeout.Milliseconds(),
			})
			Fail(c, Timeout("stopping instance %q exceeded the %s timeout", inst.Name, timeout))
			return
		case errors.Is(err, ErrConflict):
			Fail(c, Conflict("%v", err))
			return
		}
		Fail(c, Classify(err, "could not stop the instance"))
		return
	}

	// A supervisor that returns "not exited, no error" is telling us it gave
	// up waiting. That is a timeout by another name.
	if result != nil && !result.Exited {
		s.audit(c, "instance.stop_timeout", fmt.Sprintf("instance:%d", inst.ID), gin.H{
			"force":       req.Force,
			"duration_ms": result.DurationMS,
			"reason":      "process did not exit within the timeout",
		})
		Fail(c, Timeout("instance %q did not exit within the %s timeout", inst.Name, timeout).
			WithDetail(gin.H{"forced": req.Force, "duration_ms": result.DurationMS}))
		return
	}

	newState := StateStopped
	if err := s.saveState(ctx, inst.ID, newState, exitCodeOf(result)); err != nil {
		loggerFrom(c).Warn("could not persist instance state", "instance_id", inst.ID, "err", err.Error())
	}

	resp := StopInstanceResponse{
		InstanceID: inst.ID,
		Forced:     req.Force,
		Exited:     true,
		State:      newState,
	}
	if result != nil {
		resp.ExitCode = result.ExitCode
		resp.DurationMS = result.DurationMS
		resp.Forced = result.Forced || req.Force
	}

	s.audit(c, "instance.stop", fmt.Sprintf("instance:%d", inst.ID), gin.H{
		"name":        inst.Name,
		"force":       req.Force,
		"duration_ms": resp.DurationMS,
	})
	s.publishStateChange(inst.ID, newState)

	c.JSON(http.StatusOK, DataResponse{Data: resp})
}

// handleRestartInstance restarts an instance.
func (s *Server) handleRestartInstance(c *gin.Context) {
	inst := MustInstance(c)
	ctx := c.Request.Context()

	if err := s.deps.Process.Restart(ctx, inst.ID); err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			s.audit(c, "instance.restart_timeout", fmt.Sprintf("instance:%d", inst.ID), nil)
			Fail(c, Timeout("restarting instance %q timed out", inst.Name))
			return
		case errors.Is(err, ErrConflict):
			Fail(c, Conflict("%v", err))
			return
		}
		Fail(c, Classify(err, "could not restart the instance"))
		return
	}

	if err := s.saveState(ctx, inst.ID, StateRunning, nil); err != nil {
		loggerFrom(c).Warn("could not persist instance state", "instance_id", inst.ID, "err", err.Error())
	}

	s.audit(c, "instance.restart", fmt.Sprintf("instance:%d", inst.ID), gin.H{"name": inst.Name})
	s.publishStateChange(inst.ID, StateRunning)

	c.JSON(http.StatusOK, DataResponse{Data: s.instanceResponseWithState(inst, false, true)})
}

func exitCodeOf(r *StopResult) *int {
	if r == nil {
		return nil
	}
	return r.ExitCode
}

// saveState persists a run-state snapshot so the UI has something to render
// after a panel restart.
func (s *Server) saveState(ctx context.Context, instanceID int64, state string, exitCode *int) error {
	now := s.now().UTC()
	snap := &InstanceState{
		InstanceID: instanceID,
		State:      state,
		ExitCode:   exitCode,
	}
	switch state {
	case StateRunning, StateStarting, StateRestarting:
		snap.StartedAt = &now
	case StateStopped, StateCrashed:
		snap.StoppedAt = &now
	}
	return s.deps.Instances.SaveState(context.WithoutCancel(ctx), snap)
}

func (s *Server) publishStateChange(instanceID int64, state string) {
	s.deps.Events.Publish(Event{
		Type:       EventStateChange,
		InstanceID: instanceID,
		Timestamp:  s.now().UTC(),
		Data:       gin.H{"state": state},
	})
}

// handleInstanceStats returns CPU/memory/uptime/player metrics (§6.6).
func (s *Server) handleInstanceStats(c *gin.Context) {
	inst := MustInstance(c)

	metrics, err := s.deps.Process.Metrics(inst.ID)
	if err != nil && !errors.Is(err, ErrNotImplemented) {
		Fail(c, Classify(err, "could not collect metrics"))
		return
	}
	if err != nil {
		metrics = MetricsInfo{
			InstanceID:  inst.ID,
			Available:   false,
			Note:        err.Error(),
			CollectedAt: s.now().UTC(),
		}
	}

	state := StateUnknown
	online := -1
	if live, ok := s.deps.Process.State(inst.ID); ok {
		state = live.State
		online = live.OnlinePlayers
	}

	source := "supervisor"
	if online < 0 {
		source = "unavailable: requires the command channel (V0-1)"
	}

	c.JSON(http.StatusOK, DataResponse{Data: StatsResponse{
		InstanceID:          inst.ID,
		State:               state,
		Metrics:             metrics,
		PortListening:       inst.Port > 0 && !s.deps.Ports.IsFree(inst.Port),
		Port:                inst.Port,
		OnlinePlayers:       online,
		OnlinePlayersSource: source,
	}})
}

// handleInstancePlayers returns the online player list.
//
// The count depends on parsing the server's `player list` output (V0-1), which
// needs a live command channel. Rather than fabricating an empty list — which
// reads as "nobody is online" — this reports Available=false with the reason.
func (s *Server) handleInstancePlayers(c *gin.Context) {
	inst := MustInstance(c)

	resp := PlayersResponse{
		InstanceID: inst.ID,
		Players:    []PlayerInfo{},
		Available:  false,
	}

	live, ok := s.deps.Process.State(inst.ID)
	if !ok {
		resp.Note = "the process supervisor has no record of this instance"
		c.JSON(http.StatusOK, DataResponse{Data: resp})
		return
	}
	if live.State != StateRunning {
		resp.Note = fmt.Sprintf("the instance is %s; the player list is only available while it is running", live.State)
		c.JSON(http.StatusOK, DataResponse{Data: resp})
		return
	}
	if live.OnlinePlayers >= 0 {
		resp.Available = true
		resp.Count = live.OnlinePlayers
		resp.Note = "count parsed from the server's player list; per-player detail requires the command channel"
		c.JSON(http.StatusOK, DataResponse{Data: resp})
		return
	}
	resp.Note = "the online player list requires the command channel (V0-1), which is not available in this build"
	c.JSON(http.StatusOK, DataResponse{Data: resp})
}

// instancesDir returns the configured instances root.
func (s *Server) instancesDir() string {
	if s.deps.InstancesDir != "" {
		return s.deps.InstancesDir
	}
	if h, ok := s.deps.System.(*HostSystemService); ok {
		return h.InstancesDir
	}
	return ""
}

// portPool returns the configured UDP port pool (for error details).
func (s *Server) portPool() PortPool {
	if a, ok := s.deps.Ports.(*UDPPortAllocator); ok {
		return a.Pool()
	}
	return PortPool{Start: DefaultPortPoolStart, End: DefaultPortPoolEnd}
}

// validationDetails builds the per-field detail block used by 422 responses.
func validationDetails(field, code string, err error) gin.H {
	return gin.H{
		"issues": []ValidationIssue{{
			Field:    field,
			Code:     code,
			Message:  err.Error(),
			Severity: "error",
		}},
	}
}
