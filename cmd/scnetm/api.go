// Wiring of the panel's HTTP surface: the embedded frontend, /healthz, and the
// REST + WebSocket API (internal/api).
//
// This file exists so cmd/scnetm/main.go stays thin. internal/api already ships
// adapters for the store, the UDP port allocator, the session store, the event
// hub and the host system service (see internal/api/adapters.go and friends).
// The one seam it cannot provide for itself is the process supervisor, because
// internal/api deliberately does not import internal/supervisor; that adapter
// lives here.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"scnetm/internal/api"
	"scnetm/internal/auth"
	"scnetm/internal/config"
	"scnetm/internal/store"
	"scnetm/internal/supervisor"
	"scnetm/internal/version"
)

// apiPrefixes are the path prefixes owned by internal/api. They must be
// registered before the "/" SPA fallback, or an unmatched /api/... request
// would be answered with index.html instead of a JSON error — which is exactly
// what happened before this wiring existed.
var apiPrefixes = []string{"/api/v1/", "/ws/"}

// buildAPI assembles the API router and returns the handler plus a shutdown
// function that stops every supervised instance.
//
// A nil handler with a nil error means "run without the API": the panel still
// serves /healthz and the frontend. A degraded-but-running panel is preferable
// to a dead one, so callers should log and continue rather than exit.
func buildAPI(
	panel *config.Panel,
	db *sql.DB,
	logger *slog.Logger,
	build version.Info,
) (http.Handler, func(context.Context) error, error) {
	st := store.New(db)

	// The JWT signing secret is generated once and persisted beside the
	// database, so a restart does not invalidate every session.
	//
	// LoadOrCreateSecret takes the *path* (NewTokenIssuer takes the raw secret
	// bytes and rejects anything under 32 bytes), so the two must not be
	// confused: passing the path straight to NewTokenIssuer fails with
	// "jwt secret must be at least 32 bytes".
	secret, created, err := auth.LoadOrCreateSecret(filepath.Join(panel.DataDir, "jwt.secret"))
	if err != nil {
		return nil, nil, err
	}
	if created {
		logger.Info("已生成新的 JWT 签名密钥", "path", "jwt.secret")
	}

	issuer, err := auth.NewTokenIssuer(secret)
	if err != nil {
		return nil, nil, err
	}

	manager := supervisor.NewManager()

	deps := api.Deps{
		// Fully backed in this build.
		Users:     api.NewStoreUserStore(st),
		Tokens:    issuer,
		Sessions:  api.NewMemorySessionStore(),
		RBAC:      auth.NewAuthorizer(false), // single-user mode: grants are a no-op
		Instances: api.NewStoreInstanceStore(st),
		Process:   newProcessManagerAdapter(manager, st, logger),
		Ports:     api.NewUDPPortAllocator(api.PortPool{Start: poolStart(panel), End: poolEnd(panel)}),
		Audit:     api.NewStoreAuditStore(st, nil),
		Jobs:      api.NewStoreJobStore(st),
		Backups:   api.NewStoreBackupStore(st, api.NewStoreInstanceStore(st)),

		System: api.NewHostSystemService(),

		// Left nil on purpose. Each nil field makes internal/api substitute a
		// nop backend that answers 501 with code "not_implemented", so a
		// half-wired feature says so instead of silently returning empty data.
		//   Config  ConfigService   — config editing
		//   World   WorldService    — save management
		//   Files   FileService     — file browser
		//   Logs    LogService      — historical log search
		//   Backup  BackupService   — backup create/restore

		InstancesDir: panel.InstancesDir,
		Log:          api.NewSlogLogger(logger),
		ConfigHTTP:   api.DefaultHTTPConfig(),
		Version: api.VersionInfo{
			Version:   build.Version,
			Commit:    build.Commit,
			BuildTime: build.BuildTime,
			GoVersion: build.GoVersion,
			Platform:  build.Platform,
		},
	}

	engine, _, err := api.NewRouter(deps)
	if err != nil {
		return nil, nil, err
	}

	logger.Info("API 已挂载", "prefixes", apiPrefixes, "instances_dir", panel.InstancesDir)

	shutdown := func(ctx context.Context) error {
		return manager.Close(ctx)
	}
	return engine, shutdown, nil
}

func poolStart(p *config.Panel) int {
	if len(p.PortPool) > 0 {
		return p.PortPool[0]
	}
	return 28887
}

func poolEnd(p *config.Panel) int {
	if len(p.PortPool) > 0 {
		return p.PortPool[len(p.PortPool)-1]
	}
	return 28887
}

// ---------------------------------------------------------------------------
// Supervisor adapter
// ---------------------------------------------------------------------------

// processManagerAdapter implements api.ProcessManager over supervisor.Manager.
//
// supervisor.Manager only starts runners that were already registered via Add,
// so this adapter is responsible for building the child-process Options from
// the instance's persisted row and registering it on first use. Without that
// step a start request fails with "instance not found" even though the instance
// exists in the database.
type processManagerAdapter struct {
	m   *supervisor.Manager
	st  *store.Store
	log *slog.Logger
}

func newProcessManagerAdapter(m *supervisor.Manager, st *store.Store, log *slog.Logger) api.ProcessManager {
	return &processManagerAdapter{m: m, st: st, log: log}
}

// EnsureRegistered registers a runner for id if it is not already known.
// Registration is idempotent, so a stop/start cycle reuses the same runner and
// keeps its state history.
func (a *processManagerAdapter) EnsureRegistered(ctx context.Context, id int64) error {
	if _, ok := a.m.Get(id); ok {
		return nil
	}
	in, err := a.st.Instances().Get(ctx, id)
	if err != nil {
		return err
	}
	opts, err := runnerOptions(in)
	if err != nil {
		return err
	}
	// The factory is not overridden, so Add constructs a default Runner.
	_, err = a.m.Add(ctx, opts, false)
	if err != nil && !strings.Contains(err.Error(), "already registered") {
		return err
	}
	return nil
}

func (a *processManagerAdapter) Start(ctx context.Context, id int64) error {
	if err := a.EnsureRegistered(ctx, id); err != nil {
		return err
	}
	if r, ok := a.m.Get(id); ok {
		if st := r.State(); st == supervisor.StateRunning || st == supervisor.StateStarting {
			return api.ErrConflict
		}
	}
	return a.m.Start(ctx, id)
}

// runnerOptions builds the child-process spec from a persisted instance row.
//
// The instance directory is the isolation boundary (§4.3): SurvivalcraftNet
// derives every generated file from its working directory, so Dir is mandatory
// and the child is never launched through a shell.
func runnerOptions(in *store.Instance) (supervisor.Options, error) {
	if in == nil {
		return supervisor.Options{}, errors.New("scnetm: nil instance")
	}
	if in.Dir == "" {
		return supervisor.Options{}, fmt.Errorf("scnetm: instance %d has no directory", in.ID)
	}
	if in.ServerDL == "" {
		return supervisor.Options{}, fmt.Errorf("scnetm: instance %d has no server dll configured", in.ID)
	}
	dotnet := in.Dotnet
	if dotnet == "" {
		dotnet = "dotnet"
	}
	colorMode := in.ColorMode
	if colorMode == "" {
		colorMode = supervisor.ColorModeEnhanced
	}
	stopTimeout := time.Duration(in.StopTimeoutSec) * time.Second
	if stopTimeout <= 0 {
		stopTimeout = 30 * time.Second
	}
	return supervisor.Options{
		ID:          in.ID,
		Dir:         in.Dir,
		Executable:  dotnet,
		Args:        []string{in.ServerDL},
		Term:        in.Term,
		ColorMode:   colorMode,
		StopTimeout: stopTimeout,
		// Logs are dual-written (colour-preserving <date>.log plus a stripped
		// <date>.plain.log) into the instance directory, per §5.2.
		LogDir: filepath.Join(in.Dir, "logs"),
	}, nil
}

func (a *processManagerAdapter) Stop(ctx context.Context, id int64, force bool) (*api.StopResult, error) {
	started := time.Now()
	res, err := a.m.Stop(ctx, id)
	out := &api.StopResult{
		InstanceID: id,
		DurationMS: time.Since(started).Milliseconds(),
	}
	if err != nil {
		// ErrAlreadyStopped still carries a usable result; surface the error so
		// the handler can answer 409 while the UI keeps its data.
		if errors.Is(err, supervisor.ErrAlreadyStopped) {
			out.Exited = true
			return out, api.ErrConflict
		}
		return out, err
	}
	out.Exited = true
	out.Forced = !res.Graceful
	if res.ExitCode != 0 {
		code := res.ExitCode
		out.ExitCode = &code
	}
	return out, nil
}

func (a *processManagerAdapter) Restart(ctx context.Context, id int64) error {
	return a.m.Restart(ctx, id)
}

func (a *processManagerAdapter) State(id int64) (api.StateInfo, bool) {
	r, ok := a.m.Get(id)
	if !ok {
		return api.StateInfo{}, false
	}
	snap := r.Snapshot()
	info := api.StateInfo{
		State:     string(snap.State),
		PID:       snap.PID,
		Ready:     r.Ready(),
		Restarts:  snap.Restarts,
		LastError: snap.LastError,
		ExitCode:  snap.ExitCode,
	}
	if !snap.Since.IsZero() {
		t := snap.Since
		info.StartedAt = &t
	}
	if res := r.LastStop(); res.Duration > 0 && !snap.Since.IsZero() {
		t := snap.Since
		info.StoppedAt = &t
	}
	// -1 means "unknown, no command channel to ask" per §6.6.
	info.OnlinePlayers = -1
	return info, true
}

func (a *processManagerAdapter) Metrics(id int64) (api.MetricsInfo, error) {
	r, ok := a.m.Get(id)
	if !ok {
		return api.MetricsInfo{}, api.ErrNotFound
	}
	m, err := r.Sample()
	if err != nil {
		// A stopped instance is not an error worth a 5xx: report unavailable so
		// the UI renders "unavailable" rather than "0%".
		return api.MetricsInfo{
			InstanceID:  id,
			Available:   false,
			Note:        err.Error(),
			CollectedAt: time.Now().UTC(),
		}, nil
	}
	return api.MetricsInfo{
		InstanceID:    id,
		PID:           m.PID,
		CPUPercent:    m.CPUPercent,
		MemoryBytes:   int64(m.RSSBytes),
		MemoryRSS:     int64(m.RSSBytes),
		Threads:       m.Threads,
		OpenFDs:       m.FDs,
		ReadBytes:     int64(m.ReadBytes),
		WrittenBytes:  int64(m.WriteBytes),
		UptimeSeconds: int64(m.UptimeSeconds),
		Restarts:      m.Restarts,
		LastExitCode:  m.LastExitCode,
		Available:     true,
		CollectedAt:   time.Now().UTC(),
	}, nil
}

func (a *processManagerAdapter) SendCommand(ctx context.Context, id int64, line string) error {
	return a.m.SendCommand(id, line)
}

func (a *processManagerAdapter) Subscribe(
	ctx context.Context, id int64, replay int, raw bool,
) (api.LogSubscription, error) {
	sub, err := a.m.Subscribe(ctx, id, replay, raw)
	if err != nil {
		return nil, err
	}
	return &logSubscriptionAdapter{sub: sub}, nil
}

// logSubscriptionAdapter bridges supervisor.Subscription to api.LogSubscription.
type logSubscriptionAdapter struct {
	sub *supervisor.Subscription
}

func (a *logSubscriptionAdapter) Lines() <-chan api.LogLine {
	out := make(chan api.LogLine, 256)
	go func() {
		defer close(out)
		for rec := range a.sub.Ch() {
			out <- a.toAPILine(rec)
		}
	}()
	return out
}

// toAPILine maps a supervisor LogRecord onto the API's LogLine. The API asks
// for one representation at a time (raw=false for search, raw=true to keep
// ANSI colour), so the record's populated field is selected by the
// subscription's own raw flag rather than guessed per line.
func (a *logSubscriptionAdapter) toAPILine(rec supervisor.LogRecord) api.LogLine {
	text := rec.Plain
	if a.sub.Raw() {
		text = rec.Raw
	}
	return api.LogLine{
		Seq:       rec.Seq,
		Timestamp: rec.TS,
		Stream:    "stdout",
		Text:      text,
		Raw:       a.sub.Raw(),
	}
}

// History returns nil: supervisor.Subscription seeds the replay backlog
// directly into Lines() (the channel is filled with the ring buffer contents
// before live output), and exposes no separate replay accessor. Calling Lines()
// therefore already delivers history in order. Draining the channel here to
// populate a separate buffer would race the WebSocket reader and silently
// swallow live lines, so this deliberately returns nothing and the read path
// stays single-owner.
func (a *logSubscriptionAdapter) History(int) []api.LogLine { return nil }

func (a *logSubscriptionAdapter) Close() error {
	a.sub.Close()
	return nil
}
