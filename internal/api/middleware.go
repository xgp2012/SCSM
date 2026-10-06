package api

import (
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"scnetm/internal/auth"
)

// Context keys used to pass request-scoped values between middleware and
// handlers. They are unexported strings so nothing outside this package can
// accidentally collide with them.
const (
	ctxKeyRequestID   = "scnetm.request_id"
	ctxKeyPrincipal   = "scnetm.principal"
	ctxKeyLogger      = "scnetm.logger"
	ctxKeyInstance    = "scnetm.instance"
	ctxKeyInstanceID  = "scnetm.instance_id"
	ctxKeyAuthMethod  = "scnetm.auth_method"
	ctxKeyAccessToken = "scnetm.access_token"
)

// HeaderRequestID is the request-correlation header, inbound and outbound.
const HeaderRequestID = "X-Request-Id"

// Principal is the authenticated caller.
type Principal struct {
	// UserID is users.id (the JWT subject).
	UserID int64 `json:"id"`
	// Username is users.username.
	Username string `json:"username"`
	// Role is the RBAC role.
	Role auth.Role `json:"role"`
	// Permissions is the role's resolved permission set, precomputed so
	// handlers and the /auth/me payload agree.
	Permissions auth.PermissionSet `json:"-"`
	// TokenID is the JWT jti, needed by logout to revoke exactly this token.
	TokenID string `json:"-"`
	// ExpiresAt is the token expiry, surfaced to the UI so it can refresh
	// ahead of time.
	ExpiresAt time.Time `json:"expires_at"`
	// ViaQueryToken records that the caller authenticated with ?token= (the
	// WebSocket path, where browsers cannot set headers). It is surfaced so
	// audits can note the weaker channel.
	ViaQueryToken bool `json:"-"`
}

// Can reports whether the principal holds perm.
func (p *Principal) Can(perm auth.Permission) bool { return p.Role.Can(perm) }

// ---------------------------------------------------------------------------
// Context helpers
// ---------------------------------------------------------------------------

// RequestIDFrom returns the request id, or "" outside a request.
func RequestIDFrom(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyRequestID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// PrincipalFrom returns the authenticated principal. The second return is false
// on an unauthenticated route.
func PrincipalFrom(c *gin.Context) (*Principal, bool) {
	v, ok := c.Get(ctxKeyPrincipal)
	if !ok {
		return nil, false
	}
	p, ok := v.(*Principal)
	return p, ok && p != nil
}

// MustPrincipal returns the principal or panics. It is only safe inside
// handlers mounted behind the auth middleware.
func MustPrincipal(c *gin.Context) *Principal {
	p, ok := PrincipalFrom(c)
	if !ok {
		panic("api: MustPrincipal called on an unauthenticated route")
	}
	return p
}

// InstanceFrom returns the instance resolved by requireInstance.
func InstanceFrom(c *gin.Context) (*Instance, bool) {
	v, ok := c.Get(ctxKeyInstance)
	if !ok {
		return nil, false
	}
	in, ok := v.(*Instance)
	return in, ok && in != nil
}

// MustInstance returns the instance resolved by requireInstance or panics.
func MustInstance(c *gin.Context) *Instance {
	in, ok := InstanceFrom(c)
	if !ok {
		panic("api: MustInstance called on a route without requireInstance")
	}
	return in
}

// loggerFrom returns the request-scoped logger, falling back to a discard
// logger so middleware never nil-panics.
func loggerFrom(c *gin.Context) Logger {
	if v, ok := c.Get(ctxKeyLogger); ok {
		if l, ok := v.(Logger); ok && l != nil {
			return l
		}
	}
	return NewDiscardLogger()
}

// ---------------------------------------------------------------------------
// Request ID
// ---------------------------------------------------------------------------

// RequestIDMiddleware assigns (or adopts) a request id and echoes it back.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(HeaderRequestID))
		// Bound the length and the alphabet: the value ends up in logs.
		if id == "" || len(id) > 128 || !isSafeID(id) {
			var err error
			id, err = auth.RandomToken(12)
			if err != nil {
				id = strconv.FormatInt(time.Now().UnixNano(), 36)
			}
		}
		c.Set(ctxKeyRequestID, id)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

func isSafeID(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Recovery
// ---------------------------------------------------------------------------

// RecoveryMiddleware converts a panic into a 500 with the standard envelope,
// logging the stack. Gin's own Recovery does not use our envelope, hence this
// replacement.
func RecoveryMiddleware(log Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				// A broken connection during a hijacked WebSocket write is not
				// a bug; gin/net-http surface it as a panic with
				// http.ErrAbortHandler.
				if r == http.ErrAbortHandler {
					panic(r)
				}
				log.Error("panic recovered",
					"panic", fmt.Sprint(r),
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
					"request_id", RequestIDFrom(c),
					"stack", string(debug.Stack()),
				)
				Fail(c, Internal(fmt.Errorf("panic: %v", r)))
			}
		}()
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Structured logging
// ---------------------------------------------------------------------------

// LoggingMiddleware logs one structured line per request, binding a
// request-scoped logger for handlers to reuse.
func LoggingMiddleware(log Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		reqLog := log.With(
			"request_id", RequestIDFrom(c),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
		)
		c.Set(ctxKeyLogger, reqLog)

		c.Next()

		status := c.Writer.Status()
		args := []any{
			"status", status,
			"duration_ms", float64(time.Since(start).Microseconds()) / 1000.0,
			"ip", ClientIP(c),
			"size", c.Writer.Size(),
		}
		if ua := c.Request.UserAgent(); ua != "" {
			args = append(args, "user_agent", truncate(ua, 200))
		}
		if p, ok := PrincipalFrom(c); ok {
			args = append(args, "user_id", p.UserID, "role", string(p.Role))
		}
		if errs := c.Errors.ByType(gin.ErrorTypePrivate); len(errs) > 0 {
			args = append(args, "handler_error", errs.String())
		}

		switch {
		case status >= 500:
			reqLog.Error("request", args...)
		case status >= 400:
			reqLog.Warn("request", args...)
		default:
			reqLog.Info("request", args...)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ClientIP resolves the caller's address.
//
// Only the direct peer is trusted by default: X-Forwarded-For is trivially
// spoofable, and trusting it would let anyone bypass IP-keyed rate limits and
// poison the audit log. §5.7 recommends a reverse proxy, so when one is in
// front the operator can enable forwarded-header trust explicitly via
// SetTrustedProxies on the engine (documented in router.go).
func ClientIP(c *gin.Context) string {
	ip := c.ClientIP()
	if ip == "" {
		if host, _, err := net.SplitHostPort(c.Request.RemoteAddr); err == nil {
			ip = host
		} else {
			ip = c.Request.RemoteAddr
		}
	}
	return ip
}

// ---------------------------------------------------------------------------
// CORS
// ---------------------------------------------------------------------------

// CORSMiddleware is a no-op unless enabled.
//
// The panel serves its own frontend from the same origin, so CORS is off by
// default (§5.7). When enabled, only the configured origins are echoed — never
// "*", because credentials are in play.
func CORSMiddleware(cfg HTTPConfig) gin.HandlerFunc {
	if !cfg.CORSEnabled || len(cfg.AllowedOrigins) == 0 {
		return func(c *gin.Context) { c.Next() }
	}
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		allowed[strings.TrimSpace(o)] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			if _, ok := allowed[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Access-Control-Allow-Credentials", "true")
				c.Header("Vary", "Origin")
				c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, "+HeaderRequestID)
				c.Header("Access-Control-Expose-Headers", HeaderRequestID)
				c.Header("Access-Control-Max-Age", "600")
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Body size limits
// ---------------------------------------------------------------------------

// MaxBodyMiddleware caps the request body using http.MaxBytesReader so an
// oversize body is rejected while streaming rather than after buffering it all.
func MaxBodyMiddleware(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limit <= 0 {
			c.Next()
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// RateLimiter is a fixed-window counter keyed by an arbitrary string.
//
// A fixed window is chosen over a token bucket because it is trivially
// testable with an injected clock, and the panel's limits are coarse
// anti-abuse guards rather than precise traffic shaping. Windows are swept
// lazily; memory is bounded by the number of distinct keys seen in a window,
// which for a self-hosted panel is tiny.
type RateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]*rateEntry
	now     func() time.Time
	// lastSweep bounds map growth for keys that are never seen again.
	lastSweep time.Time
}

type rateEntry struct {
	count     int
	windowEnd time.Time
}

// NewRateLimiter builds a limiter allowing limit events per window.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		limit:     limit,
		window:    window,
		entries:   make(map[string]*rateEntry),
		now:       time.Now,
		lastSweep: time.Now(),
	}
}

// Allow records an event for key and reports whether it is within the limit.
// The second return is the number of seconds until the window resets, suitable
// for a Retry-After header.
func (r *RateLimiter) Allow(key string) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	r.sweepLocked(now)

	e, ok := r.entries[key]
	if !ok || now.After(e.windowEnd) {
		r.entries[key] = &rateEntry{count: 1, windowEnd: now.Add(r.window)}
		return true, 0
	}
	e.count++
	if e.count > r.limit {
		return false, retryAfter(now, e.windowEnd)
	}
	return true, 0
}

// Peek reports the current count for key without recording an event.
func (r *RateLimiter) Peek(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[key]
	if !ok || r.now().After(e.windowEnd) {
		return 0
	}
	return e.count
}

// Reset clears the counter for key.
func (r *RateLimiter) Reset(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, key)
}

// sweepLocked drops expired entries at most once per window.
func (r *RateLimiter) sweepLocked(now time.Time) {
	if now.Sub(r.lastSweep) < r.window {
		return
	}
	r.lastSweep = now
	for k, e := range r.entries {
		if now.After(e.windowEnd) {
			delete(r.entries, k)
		}
	}
}

func retryAfter(now, end time.Time) int {
	d := int(end.Sub(now).Seconds())
	if d < 1 {
		d = 1
	}
	return d
}

// setClockForTest swaps the clock. Only used by tests in this package.
func (r *RateLimiter) setClockForTest(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// RateLimitMiddleware rejects requests over the limit for a given key function.
func RateLimitMiddleware(limiter *RateLimiter, keyFn func(*gin.Context) string, what string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := keyFn(c)
		ok, retry := limiter.Allow(key)
		if !ok {
			c.Header("Retry-After", strconv.Itoa(retry))
			Fail(c, RateLimited(retry).WithDetail(gin.H{
				"limit_for":           what,
				"retry_after_seconds": retry,
			}))
			return
		}
		c.Next()
	}
}

// limiterSet groups the three independently-tuned limiters §5.7 requires:
// login, command dispatch and file upload. They are separate instances so a
// burst of one kind cannot exhaust another.
type limiterSet struct {
	login   *RateLimiter
	command *RateLimiter
	upload  *RateLimiter
}

func newLimiterSet(cfg HTTPConfig) *limiterSet {
	return &limiterSet{
		login:   NewRateLimiter(cfg.LoginRateLimit, cfg.LoginRateWindow),
		command: NewRateLimiter(cfg.CommandRateLimit, cfg.CommandRateWindow),
		upload:  NewRateLimiter(cfg.UploadRateLimit, cfg.UploadRateWindow),
	}
}

// ---------------------------------------------------------------------------
// Login failure tracking / lockout
// ---------------------------------------------------------------------------

// LockoutTracker counts consecutive failed logins per account and locks the
// account for a cooling-off period once the threshold is crossed.
//
// It is deliberately account-keyed (not IP-keyed): the IP limiter in front
// stops a single source hammering the endpoint, while this stops a distributed
// guessing campaign against one account. A successful login clears the
// counter, so a legitimate user who mistypes a few times is not punished.
type LockoutTracker struct {
	mu       sync.Mutex
	max      int
	lockFor  time.Duration
	failures map[string]*lockoutEntry
	now      func() time.Time
}

type lockoutEntry struct {
	failures  int
	lockedTil time.Time
}

// NewLockoutTracker builds a tracker locking an account for lockFor after max
// consecutive failures.
func NewLockoutTracker(max int, lockFor time.Duration) *LockoutTracker {
	if max <= 0 {
		max = 5
	}
	if lockFor <= 0 {
		lockFor = 15 * time.Minute
	}
	return &LockoutTracker{
		max:      max,
		lockFor:  lockFor,
		failures: make(map[string]*lockoutEntry),
		now:      time.Now,
	}
}

// Locked reports whether key is locked and, if so, for how many more seconds.
func (t *LockoutTracker) Locked(key string) (bool, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.failures[key]
	if !ok {
		return false, 0
	}
	now := t.now()
	if now.Before(e.lockedTil) {
		return true, retryAfter(now, e.lockedTil)
	}
	// Lock expired: reset so the user gets a fresh budget.
	if !e.lockedTil.IsZero() && !now.Before(e.lockedTil) {
		delete(t.failures, key)
	}
	return false, 0
}

// Fail records a failed attempt and returns the resulting failure count and
// whether the account is now locked.
func (t *LockoutTracker) Fail(key string) (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.failures[key]
	if !ok {
		e = &lockoutEntry{}
		t.failures[key] = e
	}
	e.failures++
	if e.failures >= t.max {
		e.lockedTil = t.now().Add(t.lockFor)
		return e.failures, true
	}
	return e.failures, false
}

// Reset clears the failure counter, called after a successful login.
func (t *LockoutTracker) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.failures, key)
}

// Failures returns the current consecutive failure count.
func (t *LockoutTracker) Failures(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.failures[key]; ok {
		return e.failures
	}
	return 0
}

func (t *LockoutTracker) setClockForTest(now func() time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now
}
