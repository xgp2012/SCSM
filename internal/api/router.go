package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Server owns the router and the HTTP-visible dependencies.
//
// It is the single place where the middleware chain and the route table are
// assembled, so adding a route cannot accidentally skip authentication: the
// authenticated group is created once and every handler is attached inside it.
type Server struct {
	deps Deps
	// engine is the Gin engine. Use Handler() to mount it in a host server.
	engine *gin.Engine
	// limiters are the three independent rate limiters from §5.7.
	limiters *limiterSet
	// lockouts tracks consecutive login failures per account.
	lockouts *LockoutTracker
	// reservations prevents two concurrent creates from claiming one port.
	reservations *PortReservation
	// startedAt is used by /healthz.
	startedAt time.Time
	// authCfg is the shared configuration for the auth middleware.
	authCfg authConfig
	// now is injectable for deterministic tests.
	now func() time.Time
}

// NewRouter builds the HTTP handler for the panel.
//
// It returns the *gin.Engine rather than an http.Handler so the host can mount
// the embedded frontend with NoRoute, but the engine implements http.Handler, so
// `http.Server{Handler: engine}` works unchanged.
func NewRouter(deps Deps) (*gin.Engine, *Server, error) {
	normalized, err := deps.Normalize()
	if err != nil {
		return nil, nil, err
	}

	// Release mode keeps Gin from printing a per-request debug banner, which
	// would interleave with the panel's own structured logs.
	//
	// gin.SetMode writes a package-global variable and is not safe to call
	// concurrently, so it is applied at most once per process. A production
	// host builds one router; a test binary builds hundreds in parallel, and
	// without the Once those builds race on Gin's mode flag.
	if normalized.Log != nil {
		setGinReleaseMode()
	}
	engine := gin.New()

	// Trust no proxy by default: X-Forwarded-For is spoofable and trusting it
	// would let anyone bypass the IP-keyed login limiter and forge audit IPs.
	// §5.7 expects a reverse proxy in production; the host can opt in with
	// engine.SetTrustedProxies(...) after calling NewRouter.
	_ = engine.SetTrustedProxies(nil)

	s := &Server{
		deps:         normalized,
		engine:       engine,
		limiters:     newLimiterSet(normalized.ConfigHTTP),
		lockouts:     NewLockoutTracker(normalized.ConfigHTTP.LoginMaxFailures, normalized.ConfigHTTP.LoginLockoutFor),
		reservations: NewPortReservation(2 * time.Minute),
		startedAt:    time.Now(),
		now:          time.Now,
		authCfg: authConfig{
			issuer: normalized.Tokens,
			users:  normalized.Users,
			rbac:   normalized.RBAC,
			sess:   normalized.Sessions,
			log:    normalized.Log,
		},
	}

	s.installMiddleware()
	s.registerRoutes()
	return engine, s, nil
}

// Handler returns the router as an http.Handler.
func (s *Server) Handler() http.Handler { return s.engine }

// Deps returns the normalized dependency bundle. Useful for tests asserting on
// the concrete fakes the caller passed in.
func (s *Server) Deps() Deps { return s.deps }

// Now returns the server's clock.
func (s *Server) Now() time.Time { return s.now() }

// setClockForTest swaps the clock and the sub-component clocks. Test helper.
func (s *Server) setClockForTest(now func() time.Time) {
	s.now = now
	s.startedAt = now()
	if ms, ok := s.deps.Sessions.(*MemorySessionStore); ok {
		ms.setClockForTest(now)
	}
	if pm, ok := s.deps.Process.(*NopProcessManager); ok {
		pm.setClockForTest(now)
	}
	// The token issuer must share the clock: a token's `iat` is compared
	// against the per-user revocation floor, and if the two clocks disagree the
	// comparison is meaningless.
	if s.deps.Tokens != nil {
		s.deps.Tokens.SetClockForTest(now)
	}
}

// installMiddleware builds the global chain.
//
// Order matters and is chosen so that:
//   - a panic anywhere downstream is still reported with a request id,
//   - the request id exists before anything logs,
//   - logging wraps the handlers (so it sees the final status) but sits inside
//     recovery (so a panic is still turned into a loggable 500),
//   - the body cap applies before a handler reads a body.
func (s *Server) installMiddleware() {
	cfg := s.deps.ConfigHTTP

	s.engine.Use(
		RecoveryMiddleware(s.deps.Log), // outermost: catches panics in everything below
		RequestIDMiddleware(),
		LoggingMiddleware(s.deps.Log),
		CORSMiddleware(cfg),
		MaxBodyMiddleware(cfg.MaxBodyBytes),
	)

	// Gin's default 404/405 handlers would emit a bare text body; replace them
	// so every response the API produces has the same envelope.
	s.engine.NoRoute(s.handleNoRoute)
	s.engine.HandleMethodNotAllowed = true
	s.engine.NoMethod(s.handleNoMethod)
}

func (s *Server) handleNoRoute(c *gin.Context) {
	Fail(c, NotFound("no route matches %s %s", c.Request.Method, c.Request.URL.Path))
}

func (s *Server) handleNoMethod(c *gin.Context) {
	Fail(c, &APIError{
		Status:  http.StatusMethodNotAllowed,
		Code:    "method_not_allowed",
		Message: "method " + c.Request.Method + " is not allowed for " + c.FullPath(),
	})
}

// registerRoutes builds the §5.6 route table under /api/v1.
// HydrateGrants loads persisted instance-level grants into the authorizer.
//
// The store is the source of truth and the authorizer is an in-memory cache of
// it, so this must run once at startup — otherwise grants written in a previous
// process are on disk but unenforced, and multi-user deployments appear to
// forget their own configuration on every restart.
//
// It is safe to call when either backend is absent (single-user mode stores no
// grants), and it is idempotent.
func (s *Server) HydrateGrants(ctx context.Context) error {
	if s == nil || s.deps.Grants == nil || s.deps.RBAC == nil || s.deps.Users == nil {
		return nil
	}
	if s.deps.RBAC.SingleUser() {
		// Single-user mode implies every user owns every instance, so a grant
		// table would change nothing. Skip the query entirely.
		return nil
	}

	users, err := s.deps.Users.List(ctx)
	if err != nil {
		return fmt.Errorf("loading grants: %w", err)
	}
	for _, u := range users {
		grants, err := s.deps.Grants.ListForUser(ctx, u.ID)
		if err != nil {
			return fmt.Errorf("loading grants for user %d: %w", u.ID, err)
		}
		for _, g := range grants {
			s.deps.RBAC.SetGrant(g.UserID, g.InstanceID, g.Perm)
		}
	}
	return nil
}

// ginModeOnce guards the process-global Gin mode flag.
var ginModeOnce sync.Once

// setGinReleaseMode switches Gin to release mode exactly once per process.
func setGinReleaseMode() {
	ginModeOnce.Do(func() { gin.SetMode(gin.ReleaseMode) })
}

func (s *Server) registerRoutes() {
	// A bare /healthz at the server root, outside the versioned API: reverse
	// proxies, container orchestrators and uptime checks conventionally probe
	// the root, and they should not have to know the API version. It exposes
	// exactly the same payload as /api/v1/healthz and no more.
	s.engine.GET("/healthz", s.handleHealth)

	root := s.engine.Group("/api/v1")

	// ---- public: liveness and the first-run bootstrap ----
	// These are intentionally unauthenticated:
	//   - /healthz must answer before setup so a supervisor can probe it.
	//   - /auth/setup-required tells the login screen whether to show the
	//     "set the admin password" form.
	//   - /auth/setup performs that one-time bootstrap, and is refused the
	//     moment a password exists, so it can never be a backdoor.
	//   - /auth/login is the login endpoint itself.
	root.GET("/healthz", s.handleHealth)
	root.GET("/auth/setup-required", s.handleSetupRequired)
	root.POST("/auth/setup",
		RateLimitMiddleware(s.limiters.login, ipKey, "auth_setup"),
		s.handleSetup,
	)
	root.POST("/auth/login",
		RateLimitMiddleware(s.limiters.login, ipKey, "auth_login"),
		s.handleLogin,
	)

	// ---- authenticated ----
	auth := root.Group("", RequireAuth(s.authCfg))

	auth.POST("/auth/logout", s.handleLogout)
	auth.GET("/auth/me", s.handleMe)
	auth.POST("/auth/password", s.handleChangePassword)

	// ---- system ----
	auth.GET("/system/info", RequirePermission(permSystemRead), s.handleSystemInfo)

	// ---- instances ----
	auth.GET("/instances", RequirePermission(permInstanceRead), s.handleListInstances)
	auth.POST("/instances", RequirePermission(permInstanceWrite), s.handleCreateInstance)

	// Routes scoped to one instance go through requireInstance, which resolves
	// the row and enforces instance-level permissions in one place.
	inst := func(perm permKey) gin.HandlerFunc { return s.requireInstance(perm) }

	auth.GET("/instances/:id", inst(permInstanceRead), s.handleGetInstance)
	auth.DELETE("/instances/:id", inst(permInstanceWrite), s.handleDeleteInstance)
	auth.PATCH("/instances/:id", inst(permInstanceWrite), s.handleUpdateInstance)

	auth.POST("/instances/:id/start", inst(permInstanceControl), s.handleStartInstance)
	auth.POST("/instances/:id/stop", inst(permInstanceControl), s.handleStopInstance)
	auth.POST("/instances/:id/restart", inst(permInstanceControl), s.handleRestartInstance)
	auth.GET("/instances/:id/stats", inst(permInstanceRead), s.handleInstanceStats)
	auth.GET("/instances/:id/players", inst(permInstanceRead), s.handleInstancePlayers)

	// ---- config ----
	auth.GET("/instances/:id/config", inst(permInstanceConfigRead), s.handleGetConfig)
	auth.PUT("/instances/:id/config", inst(permInstanceConfig), s.handlePutConfig)
	auth.POST("/instances/:id/config/validate", inst(permInstanceConfigRead), s.handleValidateConfig)

	// ---- worlds ----
	auth.GET("/instances/:id/worlds", inst(permInstanceRead), s.handleListWorlds)
	auth.POST("/instances/:id/worlds/import", inst(permInstanceConfig), s.handleImportWorld)
	auth.GET("/instances/:id/worlds/:w/export", inst(permInstanceRead), s.handleExportWorld)
	auth.POST("/instances/:id/worlds/:w/backup", inst(permInstanceBackup), s.handleBackupWorld)
	auth.POST("/instances/:id/worlds/:w/restore", inst(permInstanceConfig), s.handleRestoreWorld)
	auth.POST("/instances/:id/worlds/:w/activate", inst(permInstanceConfig), s.handleActivateWorld)
	auth.DELETE("/instances/:id/worlds/:w", inst(permInstanceWrite), s.handleDeleteWorld)

	// ---- files ----
	//
	// Downloads accept ?token= because the browser drives them with a plain
	// navigation (an <a href>), which cannot set an Authorization header. The
	// token is still fully validated, and the route is flagged ViaQueryToken.
	auth.GET("/instances/:id/files", inst(permInstanceFileRead), s.handleListFiles)
	auth.GET("/instances/:id/files/download", inst(permInstanceFileRead), s.handleDownloadFile)
	auth.POST("/instances/:id/files/upload",
		inst(permInstanceFile),
		RateLimitMiddleware(s.limiters.upload, userKey, "file_upload"),
		s.handleUploadFile,
	)
	auth.POST("/instances/:id/files/mkdir", inst(permInstanceFile), s.handleMkdir)
	auth.POST("/instances/:id/files/rename", inst(permInstanceFile), s.handleRenameFile)
	auth.POST("/instances/:id/files/delete", inst(permInstanceFile), s.handleDeleteFile)
	auth.POST("/instances/:id/files/unzip", inst(permInstanceFile), s.handleUnzipFile)

	// ---- logs ----
	auth.GET("/instances/:id/logs", inst(permInstanceLogRead), s.handleGetLogs)
	auth.GET("/instances/:id/logs/download", inst(permInstanceLogRead), s.handleDownloadLogs)

	// ---- backups ----
	auth.GET("/backups", RequirePermission(permInstanceBackupRead), s.handleListBackups)
	auth.POST("/instances/:id/backups", inst(permInstanceBackup), s.handleCreateBackup)
	auth.POST("/backups/:id/restore", RequirePermission(permInstanceBackup), s.handleRestoreBackup)
	auth.DELETE("/backups/:id", RequirePermission(permInstanceBackup), s.handleDeleteBackup)

	// ---- jobs ----
	auth.GET("/jobs", RequirePermission(permJobManage), s.handleListJobs)
	auth.POST("/jobs", RequirePermission(permJobManage), s.handleCreateJob)
	auth.PUT("/jobs/:id", RequirePermission(permJobManage), s.handleUpdateJob)
	auth.DELETE("/jobs/:id", RequirePermission(permJobManage), s.handleDeleteJob)

	// ---- users ----
	auth.GET("/users", RequirePermission(permUserManage), s.handleListUsers)
	auth.POST("/users", RequirePermission(permUserManage), s.handleCreateUser)
	auth.PUT("/users/:id", RequirePermission(permUserManage), s.handleUpdateUser)
	auth.DELETE("/users/:id", RequirePermission(permUserManage), s.handleDeleteUser)

	// ---- audit ----
	auth.GET("/audit", RequirePermission(permAuditRead), s.handleListAudit)

	// ---- WebSocket ----
	//
	// The upgrade handlers perform their own authentication from a header OR
	// ?token=, because a browser WebSocket constructor cannot set headers.
	// They are therefore registered OUTSIDE the RequireAuth group: putting them
	// inside would make the header-less browser case fail with a JSON 401
	// before the upgrade could even be attempted.
	s.engine.GET("/api/v1/ws/instances/:id/console", s.handleConsoleWS)
	s.engine.GET("/api/v1/ws/events", s.handleEventsWS)

	// A second, unversioned mount of the sockets: many WebSocket clients
	// normalise paths and some operators expect /ws/... . Both are identical.
	s.engine.GET("/ws/instances/:id/console", s.handleConsoleWS)
	s.engine.GET("/ws/events", s.handleEventsWS)
}

// permKey is an alias so the route table's `inst(permX)` calls read cleanly
// while still typing the argument.
type permKey = permissionAlias

// ipKey rates limits by client IP; userKey by authenticated user, falling back
// to IP on an unauthenticated path.
func ipKey(c *gin.Context) string { return "ip:" + ClientIP(c) }

func userKey(c *gin.Context) string {
	if p, ok := PrincipalFrom(c); ok {
		return "user:" + itoa(p.UserID)
	}
	return "ip:" + ClientIP(c)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
