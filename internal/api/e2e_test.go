package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"scnetm/internal/auth"
	"scnetm/internal/store"
)

// This file is the end-to-end acceptance proof for T6.
//
// Unlike the rest of the suite, it wires the API to the REAL SQLite store
// (internal/store) through the adapters in adapters.go, so the evidence below
// exercises actual persistence: real migrations, the real first-run seed, real
// SQLite rows for instances, state and audit.
//
// Only game-server-dependent pieces stay in-process (process supervision, .NET
// configuration parsing, world/file/backup services), because no game server
// package exists in this environment.
//
// Every step is written to a transcript on stderr, and also to $SCNETM_E2E_LOG
// when set, so the acceptance run can be inspected verbatim.

// --- transcript ------------------------------------------------------------

type e2eTranscript struct {
	file   *os.File
	stepNo int
}

func newE2ETranscript(t *testing.T) *e2eTranscript {
	t.Helper()
	tr := &e2eTranscript{}
	if path := os.Getenv("SCNETM_E2E_LOG"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("creating transcript %s: %v", path, err)
		}
		tr.file = f
		t.Cleanup(func() { _ = f.Close() })
	}
	return tr
}

func (tr *e2eTranscript) record(method, path string, status int, body []byte, note string) {
	tr.stepNo++

	// Pretty-print JSON so the transcript is readable; fall back to raw bytes
	// for non-JSON payloads (a config file, an error page).
	pretty := body
	var parsed any
	if err := json.Unmarshal(body, &parsed); err == nil {
		if formatted, err := json.MarshalIndent(parsed, "       ", "  "); err == nil {
			pretty = formatted
		}
	}

	line := fmt.Sprintf("\n[%02d] %s %s  ->  %d\n     %s\n     %s\n",
		tr.stepNo, method, path, status, note, pretty)

	fmt.Fprint(os.Stderr, line)
	if tr.file != nil {
		fmt.Fprint(tr.file, line)
	}
}

// --- client ----------------------------------------------------------------

type e2eClient struct {
	ts         *httptest.Server
	token      string
	transcript *e2eTranscript
	t          *testing.T
}

func (c *e2eClient) do(method, path string, body any) (int, []byte) {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshalling request: %v", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.ts.URL+path, reader)
	if err != nil {
		c.t.Fatalf("building request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.ts.Client().Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("reading response: %v", err)
	}
	return resp.StatusCode, data
}

// --- the acceptance run ----------------------------------------------------

// TestEndToEndAgainstRealSQLite runs the full acceptance sequence against a real
// database:
//
//	setup the admin password -> login -> create an instance -> list -> read the
//	config -> write the config -> start -> stop -> verify the persisted state
//	and audit trail -> logout.
func TestEndToEndAgainstRealSQLite(t *testing.T) {
	// Not parallel: it writes to a shared transcript.
	if testing.Short() {
		t.Skip("skipping the end-to-end run in short mode")
	}

	transcript := newE2ETranscript(t)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "scnetm.db")
	instancesDir := filepath.Join(dir, "instances")
	if err := os.MkdirAll(instancesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// --- real database: open + migrate ---
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	if err := store.Migrate(db); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	st := store.New(db)

	// --- real-store adapters ---
	backends := NewStoreBackends(st, nil)
	if backends.Users == nil || backends.Instances == nil || backends.Audit == nil {
		t.Fatal("the store adapters were not wired")
	}

	// --- in-process pieces for the game-server-dependent features ---
	issuer, err := auth.NewTokenIssuer("an-end-to-end-test-secret-that-is-long-enough")
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}

	process := NewNopProcessManager(NewDiscardLogger())
	events := NewEventHub(NewDiscardLogger())
	defer events.Close()

	configSvc := newFakeConfigService()

	deps := Deps{
		Users:        backends.Users,
		Instances:    backends.Instances,
		Tokens:       issuer,
		Sessions:     NewMemorySessionStore(),
		RBAC:         auth.NewAuthorizer(true),
		Process:      process,
		Ports:        NewUDPPortAllocator(PortPool{Start: 40000, End: 40010}),
		Audit:        backends.Audit,
		Backups:      backends.Backups,
		Jobs:         backends.Jobs,
		Events:       events,
		InstancesDir: instancesDir,
		Log:          NewDiscardLogger(),
		ConfigHTTP:   DefaultHTTPConfig(),
		Version: VersionInfo{
			Version:   "t6-e2e",
			Commit:    "e2e",
			GoVersion: "go1.27.1",
			Platform:  "linux/amd64",
		},
		Config: configSvc,
		World:  newFakeWorldService(),
		Files:  newFakeFileService(),
		Logs:   newFakeLogService(),
		Backup: &fakeBackupService{},
	}

	engine, _, err := NewRouter(deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	hs := httptest.NewServer(engine)
	defer hs.Close()

	client := &e2eClient{ts: hs, transcript: transcript, t: t}
	bg := context.Background()

	// --- step 1: the migration seeded a first-run admin ---
	admin, err := st.Users().GetByUsername(bg, "admin")
	if err != nil {
		t.Fatalf("the migration did not seed an admin: %v", err)
	}
	if !store.IsFirstRunHash(admin.PasswordHash) {
		t.Fatalf("the seeded admin is not in the first-run state: %q", admin.PasswordHash)
	}
	transcript.record("SQLITE", "users.GetByUsername(admin)", 200,
		[]byte(fmt.Sprintf(`{"id":%d,"username":%q,"role":%q,"password_hash":%q}`,
			admin.ID, admin.Username, admin.Role, admin.PasswordHash)),
		"the real migration seeded the first-run admin row")

	// --- step 2: GET /auth/setup-required ---
	status, body := client.do(http.MethodGet, "/api/v1/auth/setup-required", nil)
	transcript.record("GET", "/api/v1/auth/setup-required", status, body,
		"the panel reports that an admin password must be set")
	if status != http.StatusOK {
		t.Fatalf("setup-required = %d: %s", status, body)
	}

	// --- step 3: login before setup is a distinct 409 ---
	status, body = client.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": "anything"})
	transcript.record("POST", "/api/v1/auth/login", status, body,
		"409 setup_required, not a generic 401")
	if status != http.StatusConflict {
		t.Fatalf("pre-setup login = %d, want 409", status)
	}

	// --- step 4: POST /auth/setup ---
	status, body = client.do(http.MethodPost, "/api/v1/auth/setup",
		map[string]string{"password": "e2e-admin-password", "confirm_password": "e2e-admin-password"})
	transcript.record("POST", "/api/v1/auth/setup", status, body,
		"first-run bootstrap; returns a usable token")
	if status != http.StatusOK {
		t.Fatalf("setup = %d: %s", status, body)
	}

	// The new hash must be in SQLite.
	admin, err = st.Users().GetByUsername(bg, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if store.IsFirstRunHash(admin.PasswordHash) {
		t.Fatal("the password was not persisted")
	}

	// --- step 5: POST /auth/login ---
	status, body = client.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": "e2e-admin-password"})
	transcript.record("POST", "/api/v1/auth/login", status, body, "normal login")
	if status != http.StatusOK {
		t.Fatalf("login = %d: %s", status, body)
	}
	var loginOut struct {
		Data struct {
			Token     string `json:"token"`
			ExpiresIn int64  `json:"expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &loginOut); err != nil {
		t.Fatalf("decoding login: %v", err)
	}
	client.token = loginOut.Data.Token
	if client.token == "" {
		t.Fatal("login returned no token")
	}

	// --- step 6: the password is not stored in plaintext ---
	admin, _ = st.Users().GetByUsername(bg, "admin")
	if admin.PasswordHash == "e2e-admin-password" {
		t.Fatal("SECURITY: the password was stored in plaintext")
	}
	transcript.record("SQLITE", "users.GetByUsername(admin)", 200,
		[]byte(fmt.Sprintf(`{"password_hash":%q,"has_plaintext":false}`, admin.PasswordHash)),
		"the stored hash is bcrypt; the plaintext is nowhere in the database")

	// --- step 7: GET /auth/me ---
	status, body = client.do(http.MethodGet, "/api/v1/auth/me", nil)
	transcript.record("GET", "/api/v1/auth/me", status, body, "the session identifies the admin")

	// --- step 8: GET /system/info with no .NET runtime installed ---
	status, body = client.do(http.MethodGet, "/api/v1/system/info", nil)
	transcript.record("GET", "/api/v1/system/info", status, body,
		"200 even though no .NET runtime is installed (reported as data)")
	if status != http.StatusOK {
		t.Fatalf("system/info = %d, want 200 without dotnet: %s", status, body)
	}

	// --- step 9: POST /instances ---
	status, body = client.do(http.MethodPost, "/api/v1/instances", map[string]any{
		"name":        "e2e-server",
		"world_name":  "E2E World",
		"world_seed":  "20240501",
		"max_players": 16,
		"memo":        "created by the end-to-end acceptance run",
	})
	transcript.record("POST", "/api/v1/instances", status, body,
		"instance created with no game server present")
	if status != http.StatusCreated {
		t.Fatalf("create instance = %d: %s", status, body)
	}
	var createOut struct {
		Data InstanceResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &createOut); err != nil {
		t.Fatalf("decoding create: %v", err)
	}
	instanceID := createOut.Data.ID
	if instanceID == 0 {
		t.Fatal("no instance id was returned")
	}

	// The row must be in SQLite and the directory provisioned on disk.
	row, err := st.Instances().Get(bg, instanceID)
	if err != nil {
		t.Fatalf("the instance was not persisted: %v", err)
	}
	if row.Name != "e2e-server" {
		t.Fatalf("persisted name = %q", row.Name)
	}
	if row.Port < 40000 || row.Port > 40010 {
		t.Fatalf("persisted port = %d, outside the configured pool", row.Port)
	}
	transcript.record("SQLITE", fmt.Sprintf("instances.Get(%d)", instanceID), 200,
		[]byte(fmt.Sprintf(`{"id":%d,"name":%q,"dir":%q,"port":%d,"owner_id":%d}`,
			row.ID, row.Name, row.Dir, row.Port, row.OwnerID)),
		"the instance row is in the real database")

	for _, name := range []string{"ServerSetting.json", "Settings.xml", "Worlds", "Configs"} {
		if _, err := os.Stat(filepath.Join(instancesDir, "e2e-server", name)); err != nil {
			t.Errorf("%s was not provisioned: %v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(instancesDir, "e2e-server", "ServerSetting.json")); err == nil {
		transcript.record("FILE", "instances/e2e-server/ServerSetting.json", 200, data,
			"baseline configuration written at create time")
	}

	// --- step 10: GET /instances ---
	status, body = client.do(http.MethodGet, "/api/v1/instances", nil)
	transcript.record("GET", "/api/v1/instances", status, body,
		"the listing merges live supervisor state onto the database rows")
	if status != http.StatusOK {
		t.Fatalf("list = %d", status)
	}

	// --- step 11: duplicate name -> 409 ---
	status, body = client.do(http.MethodPost, "/api/v1/instances", map[string]any{"name": "e2e-server"})
	transcript.record("POST", "/api/v1/instances", status, body, "duplicate name is a 409")
	if status != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409: %s", status, body)
	}

	// --- step 12: privileged port -> 422 ---
	status, body = client.do(http.MethodPost, "/api/v1/instances",
		map[string]any{"name": "bad-port", "port": 80})
	transcript.record("POST", "/api/v1/instances", status, body,
		"a privileged port is a 422 validation failure")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid port = %d, want 422: %s", status, body)
	}

	// --- step 13: path traversal in the name -> 422 ---
	status, body = client.do(http.MethodPost, "/api/v1/instances",
		map[string]any{"name": "../../escape"})
	transcript.record("POST", "/api/v1/instances", status, body,
		"a traversing instance name is refused")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("traversal name = %d, want 422: %s", status, body)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(instancesDir), "escape")); err == nil {
		t.Fatal("SECURITY: a directory was created outside the instances root")
	}

	// --- step 14: GET the config document ---
	cfgPath := fmt.Sprintf("/api/v1/instances/%d/config", instanceID)
	if data, err := os.ReadFile(filepath.Join(instancesDir, "e2e-server", "ServerSetting.json")); err == nil {
		configSvc.seed(apiInstanceRow(row), ConfigKindServerSetting, string(data))
	}
	status, body = client.do(http.MethodGet, cfgPath, nil)
	transcript.record("GET", cfgPath, status, body, "the configuration document")
	if status != http.StatusOK {
		t.Fatalf("get config = %d: %s", status, body)
	}

	// --- step 15: PUT an invalid config -> 422 with the file untouched ---
	cfgFile := filepath.Join(instancesDir, "e2e-server", "ServerSetting.json")
	before, _ := os.ReadFile(cfgFile)
	status, body = client.do(http.MethodPut, cfgPath, map[string]any{
		"kind":    "ServerSetting.json",
		"content": map[string]any{"ServerPort": 99999, "MaxOnlinePlayerCount": 9999},
	})
	transcript.record("PUT", cfgPath, status, body,
		"422: the invalid document is NOT written")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid config write = %d, want 422: %s", status, body)
	}
	after, _ := os.ReadFile(cfgFile)
	if !bytes.Equal(before, after) {
		t.Fatal("the rejected write modified the file on disk")
	}
	transcript.record("FILE", "instances/e2e-server/ServerSetting.json", 200, after,
		"byte-identical to the pre-write content, proving nothing was written")

	// --- step 16: PUT a valid config -> 200 ---
	status, body = client.do(http.MethodPut, cfgPath, map[string]any{
		"kind":    "ServerSetting.json",
		"content": map[string]any{"ServerPort": row.Port, "MaxOnlinePlayerCount": 24},
	})
	transcript.record("PUT", cfgPath, status, body, "a valid document is written")
	if status != http.StatusOK {
		t.Fatalf("valid config write = %d: %s", status, body)
	}

	// --- step 17: start ---
	startPath := fmt.Sprintf("/api/v1/instances/%d/start", instanceID)
	status, body = client.do(http.MethodPost, startPath, nil)
	transcript.record("POST", startPath, status, body, "the instance starts")
	if status != http.StatusOK {
		t.Fatalf("start = %d: %s", status, body)
	}

	// --- step 18: start again -> 409 state conflict ---
	status, body = client.do(http.MethodPost, startPath, nil)
	transcript.record("POST", startPath, status, body,
		"409: starting an already-running instance is a state conflict")
	if status != http.StatusConflict {
		t.Fatalf("double start = %d, want 409: %s", status, body)
	}

	// --- step 19: stats ---
	statsPath := fmt.Sprintf("/api/v1/instances/%d/stats", instanceID)
	status, body = client.do(http.MethodGet, statsPath, nil)
	transcript.record("GET", statsPath, status, body, "runtime statistics")

	// --- step 20: stop ---
	stopPath := fmt.Sprintf("/api/v1/instances/%d/stop", instanceID)
	status, body = client.do(http.MethodPost, stopPath, nil)
	transcript.record("POST", stopPath, status, body, "the instance stops")
	if status != http.StatusOK {
		t.Fatalf("stop = %d: %s", status, body)
	}

	// --- step 21: the state is persisted in SQLite ---
	state, err := st.Instances().GetState(bg, instanceID)
	if err != nil {
		t.Fatalf("reading persisted state: %v", err)
	}
	transcript.record("SQLITE", fmt.Sprintf("instances.GetState(%d)", instanceID), 200,
		[]byte(fmt.Sprintf(`{"instance_id":%d,"state":%q}`, state.InstanceID, state.State)),
		"the final lifecycle state is persisted in the real database")
	if state.State != StateStopped {
		t.Fatalf("persisted state = %q, want %q", state.State, StateStopped)
	}

	// --- step 22: the audit trail is in the database ---
	logs, err := st.Audit().List(bg, store.AuditFilter{Limit: 200})
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	actions := make([]string, 0, len(logs))
	for _, l := range logs {
		actions = append(actions, l.Action)
	}
	encoded, _ := json.Marshal(actions)
	transcript.record("SQLITE", "audit.List(limit=200)", 200, encoded,
		"every mutating request left a row in the real audit table")

	for _, want := range []string{
		"auth.setup", "auth.login", "instance.create", "instance.start", "instance.stop",
	} {
		found := false
		for _, a := range actions {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no %q row in the audit table; got %v", want, actions)
		}
	}

	// --- step 23: a cookie-less client with no token is rejected ---
	anon := &e2eClient{ts: hs, transcript: transcript, t: t}
	status, body = anon.do(http.MethodGet, "/api/v1/instances", nil)
	transcript.record("GET", "/api/v1/instances", status, body, "401 without a token")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d, want 401", status)
	}

	// --- step 24: logout revokes the token ---
	status, body = client.do(http.MethodPost, "/api/v1/auth/logout", nil)
	transcript.record("POST", "/api/v1/auth/logout", status, body, "the session is revoked")
	if status != http.StatusOK {
		t.Fatalf("logout = %d: %s", status, body)
	}

	status, body = client.do(http.MethodGet, "/api/v1/auth/me", nil)
	transcript.record("GET", "/api/v1/auth/me", status, body,
		"401: the revoked token stops working immediately")
	if status != http.StatusUnauthorized {
		t.Fatalf("post-logout /api/v1/auth/me = %d, want 401: %s", status, body)
	}

	t.Logf("end-to-end acceptance run completed with %d recorded steps", transcript.stepNo)
}

// apiInstanceRow converts a store row into the API's Instance for seeding the
// in-process config service, so the config read returns what provisioning wrote.
func apiInstanceRow(r *store.Instance) *Instance {
	return &Instance{
		ID:        r.ID,
		Name:      r.Name,
		Dir:       r.Dir,
		Port:      int(r.Port),
		OwnerID:   r.OwnerID,
		CreatedAt: r.CreatedAt,
	}
}

var _ = time.Now
