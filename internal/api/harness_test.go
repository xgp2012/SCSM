package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"scnetm/internal/auth"
)

// This file is the test harness: it assembles a Server with fakes, drives it
// through httptest, and provides the JSON/HTTP helpers the suites use.
//
// Everything runs in-process with no external dependency: no SQLite, no dotnet,
// no game server, no network listener beyond the httptest server.

func init() {
	// Silence Gin's route/mode chatter during tests.
	//
	// This is a package-level init, so it runs exactly once before any test
	// goroutine starts. It must NOT be repeated inside tests: gin.SetMode
	// writes a global, and parallel tests calling it race with each other and
	// with the router construction that reads it.
	gin.SetMode(gin.TestMode)
}

// testServer bundles a Server with its fakes and the httptest listener.
type testServer struct {
	t  *testing.T
	ts *httptest.Server

	server *Server
	engine *gin.Engine
	deps   Deps

	users     *fakeUserStore
	instances *fakeInstanceStore
	audit     *fakeAuditStore
	backups   *fakeBackupStore
	jobs      *fakeJobStore
	config    *fakeConfigService
	world     *fakeWorldService
	files     *fakeFileService
	logs      *fakeLogService
	backupSvc *fakeBackupService
	system    *fakeSystemService
	process   *NopProcessManager
	ports     *UDPPortAllocator
	sessions  *MemorySessionStore
	events    *EventHub

	instancesDir string
	clock        time.Time
}

// testOptions tweaks the harness before the server is built.
type testOptions struct {
	// seedAdmin installs the first-run admin row (default true).
	noAdmin bool
	// adminPassword pre-sets the admin password (skips the setup flow).
	adminPassword string
	// role seeds a second user with this role (for the RBAC matrix).
	roleUser     auth.Role
	rolePassword string
	// nopBackends leaves the feature services as nops, so the 501 paths are
	// exercised. Default false: the fakes are wired in.
	nopBackends bool
	// portsAllBusy makes every UDP probe fail, to test pool exhaustion.
	portsAllBusy bool
	// portPool overrides the allocation pool.
	portPool *PortPool
	// noDotnet leaves the real host system service in place (dotnet is absent
	// in this environment, which is exactly what the /system/info test wants).
	realSystemService bool
	// httpConfig overrides tunables (rate limits, lockout).
	httpConfig *HTTPConfig
	// instancesDir overrides the temp instances root.
	instancesDir string
}

// newTestServer builds the harness.
func newTestServer(t *testing.T, opts ...func(*testOptions)) *testServer {
	t.Helper()

	o := &testOptions{}
	for _, fn := range opts {
		fn(o)
	}

	dir := o.instancesDir
	if dir == "" {
		dir = t.TempDir()
	}
	// Ensure the instances root exists so create/delete work without a setup
	// step in each test.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating instances dir: %v", err)
	}

	issuer, err := auth.NewTokenIssuer("test-secret-value-that-is-long-enough-1234567890")
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}

	cfg := DefaultHTTPConfig()
	if o.httpConfig != nil {
		cfg = *o.httpConfig
	}

	process := NewNopProcessManager(NewDiscardLogger())
	ports := NewUDPPortAllocator(PortPool{Start: 30000, End: 30005})
	if o.portPool != nil {
		ports = NewUDPPortAllocator(*o.portPool)
	}
	if o.portsAllBusy {
		ports.SetProbeForTest(func(int) error { return errPortBusy })
	}

	deps := Deps{
		Users:        nil, // set below
		Tokens:       issuer,
		Sessions:     NewMemorySessionStore(),
		RBAC:         auth.NewAuthorizer(true),
		Instances:    nil,
		Process:      process,
		Ports:        ports,
		Audit:        nil,
		InstancesDir: dir,
		Log:          NewDiscardLogger(),
		ConfigHTTP:   cfg,
		Version: VersionInfo{
			Version:   "test",
			Commit:    "deadbeef",
			GoVersion: "go1.27.1",
			Platform:  "linux/amd64",
		},
	}

	ts := &testServer{
		t:            t,
		users:        newFakeUserStore(),
		instances:    newFakeInstanceStore(),
		audit:        newFakeAuditStore(),
		backups:      newFakeBackupStore(),
		jobs:         newFakeJobStore(),
		config:       newFakeConfigService(),
		world:        newFakeWorldService(),
		files:        newFakeFileService(),
		logs:         newFakeLogService(),
		backupSvc:    &fakeBackupService{},
		process:      process,
		ports:        ports,
		sessions:     deps.Sessions.(*MemorySessionStore),
		events:       NewEventHub(NewDiscardLogger()),
		instancesDir: dir,
		clock:        time.Now().UTC(),
	}

	deps.Users = ts.users
	deps.Instances = ts.instances
	deps.Audit = ts.audit
	deps.Backups = ts.backups
	deps.Jobs = ts.jobs
	deps.Events = ts.events

	if !o.nopBackends {
		deps.Config = ts.config
		deps.World = ts.world
		deps.Files = ts.files
		deps.Logs = ts.logs
		deps.Backup = ts.backupSvc
	}

	if o.realSystemService {
		host := NewHostSystemService()
		host.InstancesDir = dir
		ts.system = &fakeSystemService{} // unused, kept for assertions
		deps.System = host
	} else {
		ts.system = &fakeSystemService{info: &SystemInfo{
			Version: deps.Version,
			Host:    HostInfo{Hostname: "test-host", OS: "linux", Arch: "amd64", CPUCores: 4},
			Dotnet: DotnetInfo{
				Available:     false,
				RequiredMajor: RequiredDotnetMajor,
				Error:         "dotnet executable not found in PATH",
				Hint:          "Install the .NET 10 runtime",
			},
			Storage: StorageInfo{Path: dir, TotalBytes: 1 << 40, FreeBytes: 1 << 39, Writable: true},
			Panel:   PanelInfo{UptimeSec: 5, Goroutines: 12},
		}}
		deps.System = ts.system
	}

	engine, server, err := NewRouter(deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	ts.engine = engine
	ts.server = server
	ts.deps = server.Deps()

	// Freeze the clock so timestamps in assertions are deterministic.
	ts.server.setClockForTest(func() time.Time { return ts.clock })

	// Seed users.
	if !o.noAdmin {
		admin := ts.users.seedAdmin()
		if o.adminPassword != "" {
			hash, herr := auth.HashPassword(o.adminPassword)
			if herr != nil {
				t.Fatalf("hashing admin password: %v", herr)
			}
			admin.PasswordHash = hash
			admin.HasPassword = true
			_ = ts.users.Update(t.Context(), admin)
		}
	}
	if o.roleUser != "" {
		pw := o.rolePassword
		if pw == "" {
			pw = "role-password-123"
		}
		hash, herr := auth.HashPassword(pw)
		if herr != nil {
			t.Fatalf("hashing role password: %v", herr)
		}
		ts.users.add(&User{
			Username:     string(o.roleUser),
			PasswordHash: hash,
			Role:         o.roleUser,
			HasPassword:  true,
		})
	}

	ts.ts = httptest.NewServer(engine)
	t.Cleanup(ts.ts.Close)
	t.Cleanup(ts.events.Close)
	return ts
}

var errPortBusy = errPortBusyType{}

// contextT2 is the context type used by the ProcessManager interface; aliased so
// the test doubles read clearly.
type contextT2 = context.Context

// contextDeadlineExceeded returns context.DeadlineExceeded without importing
// context at every call site in a test file.
func contextDeadlineExceeded() error { return context.DeadlineExceeded }

// listenTCPForTest binds a TCP listener on the given port, for the UDP-vs-TCP
// probe test.
func listenTCPForTest(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// listenUDPForTest binds a UDP socket on the given port.
func listenUDPForTest(port int) (net.PacketConn, error) {
	return net.ListenPacket("udp", fmt.Sprintf("0.0.0.0:%d", port))
}

type errPortBusyType struct{}

func (errPortBusyType) Error() string { return "address already in use" }

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// response captures a decoded HTTP reply.
type response struct {
	t      *testing.T
	Status int
	Body   []byte
	Header http.Header
}

// JSON decodes the body into v.
func (r *response) JSON(v any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.t.Fatalf("decoding JSON response (status %d): %v\nbody: %s", r.Status, err, r.Body)
	}
}

// Envelope decodes the standard data envelope.
func (r *response) Envelope() map[string]any {
	r.t.Helper()
	var m map[string]any
	r.JSON(&m)
	return m
}

// Data decodes {"data": ...} and returns the inner object.
func (r *response) Data() map[string]any {
	r.t.Helper()
	env := r.Envelope()
	d, ok := env["data"].(map[string]any)
	if !ok {
		r.t.Fatalf("response has no object \"data\" field (status %d): %s", r.Status, r.Body)
	}
	return d
}

// ErrorCode returns the machine-readable error code, or "" on success.
func (r *response) ErrorCode() string {
	r.t.Helper()
	var env ErrorEnvelope
	if err := json.Unmarshal(r.Body, &env); err != nil {
		return ""
	}
	return env.Error.Code
}

// ErrorMessage returns the human-readable error message.
func (r *response) ErrorMessage() string {
	r.t.Helper()
	var env ErrorEnvelope
	_ = json.Unmarshal(r.Body, &env)
	return env.Error.Message
}

// String returns the raw body.
func (r *response) String() string { return string(r.Body) }

// do performs an HTTP request against the test server.
func (ts *testServer) do(method, path string, body any, headers ...string) *response {
	ts.t.Helper()

	var reader io.Reader
	switch v := body.(type) {
	case nil:
		reader = nil
	case []byte:
		reader = bytes.NewReader(v)
	case string:
		reader = strings.NewReader(v)
	case io.Reader:
		reader = v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			ts.t.Fatalf("marshalling request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, ts.ts.URL+path, reader)
	if err != nil {
		ts.t.Fatalf("building request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	resp, err := ts.ts.Client().Do(req)
	if err != nil {
		ts.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		ts.t.Fatalf("reading response body: %v", err)
	}
	return &response{t: ts.t, Status: resp.StatusCode, Body: data, Header: resp.Header.Clone()}
}

// get performs an authenticated GET.
func (ts *testServer) get(token, path string) *response {
	ts.t.Helper()
	return ts.auth(token, http.MethodGet, path, nil)
}

// post performs an authenticated POST.
func (ts *testServer) post(token, path string, body any) *response {
	ts.t.Helper()
	return ts.auth(token, http.MethodPost, path, body)
}

// put performs an authenticated PUT.
func (ts *testServer) put(token, path string, body any) *response {
	ts.t.Helper()
	return ts.auth(token, http.MethodPut, path, body)
}

// del performs an authenticated DELETE.
func (ts *testServer) del(token, path string, body any) *response {
	ts.t.Helper()
	return ts.auth(token, http.MethodDelete, path, body)
}

func (ts *testServer) auth(token, method, path string, body any) *response {
	ts.t.Helper()
	if token == "" {
		return ts.do(method, path, body)
	}
	return ts.do(method, path, body, "Authorization", "Bearer "+token)
}

// ---------------------------------------------------------------------------
// Auth shortcuts
// ---------------------------------------------------------------------------

// login authenticates and returns the token.
func (ts *testServer) login(username, password string) *response {
	ts.t.Helper()
	return ts.do(http.MethodPost, "/api/v1/auth/login", LoginRequest{
		Username: username,
		Password: password,
	})
}

// mustLogin logs in and fails the test on error.
func (ts *testServer) mustLogin(username, password string) string {
	ts.t.Helper()
	resp := ts.login(username, password)
	if resp.Status != http.StatusOK {
		ts.t.Fatalf("login failed: status %d body %s", resp.Status, resp.Body)
	}
	var out struct {
		Data LoginResponse `json:"data"`
	}
	resp.JSON(&out)
	if out.Data.Token == "" {
		ts.t.Fatalf("login returned an empty token: %s", resp.Body)
	}
	return out.Data.Token
}

// adminToken runs the full first-run bootstrap and returns the resulting token.
func (ts *testServer) adminToken(password string) string {
	ts.t.Helper()
	if password == "" {
		password = "admin-password-123"
	}
	resp := ts.do(http.MethodPost, "/api/v1/auth/setup", SetupRequest{
		Password:        password,
		ConfirmPassword: password,
	})
	if resp.Status != http.StatusOK {
		ts.t.Fatalf("setup failed: status %d body %s", resp.Status, resp.Body)
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	resp.JSON(&out)
	if out.Data.Token == "" {
		ts.t.Fatalf("setup did not return a token: %s", resp.Body)
	}
	return out.Data.Token
}

// issueToken mints a token directly, bypassing login. Used by the RBAC matrix
// and the token-validation tests, where the roles and lifetimes are known.
//
// A NEGATIVE ttl means "already expired": auth.WithAccessTTL deliberately
// ignores non-positive values (they would be a footgun in production), so the
// expiry is instead produced by minting with a positive TTL from a token issuer
// whose clock is offset backwards by |ttl|. The resulting token has an exp in
// the real past, which the server — validating against the real clock — rejects.
func (ts *testServer) issueToken(userID int64, username string, role auth.Role, ttl time.Duration) string {
	ts.t.Helper()

	if ttl <= 0 {
		// Mint as if "now" were |ttl| in the past, with a 1-minute lifetime:
		// exp = now - |ttl| + 1min, which is in the past for |ttl| > 1min.
		backdated := time.Now().UTC().Add(ttl - time.Minute)
		issuer, err := auth.NewTokenIssuerFromIssuer(ts.deps.Tokens,
			auth.WithAccessTTL(time.Minute),
			auth.WithClock(func() time.Time { return backdated }),
		)
		if err != nil {
			ts.t.Fatalf("deriving backdated issuer: %v", err)
		}
		token, _, err := issuer.Issue(userID, username, role)
		if err != nil {
			ts.t.Fatalf("issuing expired token: %v", err)
		}
		return token
	}

	issuer, err := auth.NewTokenIssuerFromIssuer(ts.deps.Tokens, auth.WithAccessTTL(ttl))
	if err != nil {
		ts.t.Fatalf("deriving issuer: %v", err)
	}
	token, _, err := issuer.Issue(userID, username, role)
	if err != nil {
		ts.t.Fatalf("issuing token: %v", err)
	}
	return token
}

// seedUserWithRole inserts a user with the given role and returns it, so an
// RBAC token refers to a user that actually exists in the store. This matters
// because the auth middleware enriches every token from the user row and
// rejects a token whose account is gone.
func (ts *testServer) seedUserWithRole(username string, role auth.Role) *User {
	ts.t.Helper()
	hash, err := auth.HashPassword("role-password-123")
	if err != nil {
		ts.t.Fatalf("hashing: %v", err)
	}
	return ts.users.add(&User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		HasPassword:  true,
		CreatedAt:    ts.clock,
	})
}

// multipartUpload posts a multipart/form-data body containing one file.
func (ts *testServer) multipartUpload(token, path, field, filename string, content []byte) *response {
	ts.t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		ts.t.Fatalf("building multipart form: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		ts.t.Fatalf("writing multipart content: %v", err)
	}
	if err := mw.Close(); err != nil {
		ts.t.Fatalf("closing multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.ts.URL+path, &buf)
	if err != nil {
		ts.t.Fatalf("building upload request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := ts.ts.Client().Do(req)
	if err != nil {
		ts.t.Fatalf("upload request: %v", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		ts.t.Fatalf("reading upload response: %v", err)
	}
	return &response{t: ts.t, Status: resp.StatusCode, Body: data, Header: resp.Header.Clone()}
}

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// seedInstance inserts an instance row plus its directory on disk.
func (ts *testServer) seedInstance(name string, port int) *Instance {
	ts.t.Helper()
	dir := filepath.Join(ts.instancesDir, name)
	if err := os.MkdirAll(filepath.Join(dir, "Worlds"), 0o755); err != nil {
		ts.t.Fatalf("creating instance dir: %v", err)
	}
	inst := &Instance{
		Name:           name,
		Dir:            dir,
		Port:           port,
		OwnerID:        1,
		MaxRestart:     5,
		StopTimeoutSec: 30,
		Term:           "xterm-256color",
		ColorMode:      "enhanced",
		CreatedAt:      ts.clock,
	}
	if err := ts.instances.Create(ts.t.Context(), inst); err != nil {
		ts.t.Fatalf("seeding instance: %v", err)
	}
	ts.process.EnsureInstance(inst.ID)
	return inst
}

// seedFile writes a real file inside the instance's directory on disk, which is
// what the handlers validate against (the fake FileService is only consulted
// after the path has been resolved for real).
func (ts *testServer) seedFile(inst *Instance, rel string, content []byte) {
	ts.t.Helper()
	abs := filepath.Join(inst.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		ts.t.Fatalf("creating parent for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		ts.t.Fatalf("seeding %s: %v", rel, err)
	}
}

// mustInstance fetches an instance row or fails the test.
func mustInstance(t *testing.T, ts *testServer, id int64) *Instance {
	t.Helper()
	inst, err := ts.deps.Instances.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("fetching instance %d: %v", id, err)
	}
	return inst
}

// requireStatus asserts the status code and prints the body on mismatch.
func requireStatus(t *testing.T, r *response, want int) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status = %d, want %d\nbody: %s", r.Status, want, r.Body)
	}
}

// requireErrorCode asserts both the status and the machine-readable code.
func requireErrorCode(t *testing.T, r *response, wantStatus int, wantCode string) {
	t.Helper()
	requireStatus(t, r, wantStatus)
	if got := r.ErrorCode(); got != wantCode {
		t.Fatalf("error code = %q, want %q\nbody: %s", got, wantCode, r.Body)
	}
}

// requireAudit asserts that an audit row with the given action exists.
func (ts *testServer) requireAudit(action string) AuditEntry {
	ts.t.Helper()
	entry, ok := ts.audit.find(action)
	if !ok {
		ts.t.Fatalf("no audit row with action %q; recorded actions: %v", action, ts.audit.actions())
	}
	return entry
}

// requireNoAudit asserts no row with the action exists.
func (ts *testServer) requireNoAudit(action string) {
	ts.t.Helper()
	if _, ok := ts.audit.find(action); ok {
		ts.t.Fatalf("unexpected audit row with action %q; recorded actions: %v", action, ts.audit.actions())
	}
}
