package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// Test-only schema helper.
//
// The production schema lives in migrate.go (Open + Migrate), which a sibling
// agent owns. openTestDB prefers those entry points so every test exercises the
// real connection setup and the real v1 migration; TestSchemaMatchesTestDDL
// (schema_contract_test.go) asserts that the fallback DDL below and migrate.go's
// schemaV1 stay in agreement, so a drift is a test failure rather than a
// silently-passing suite.
//
// testDDL is the §5.5 DDL, copied so the repositories can still be tested if
// migrate.go is ever missing or fails to compile. It is a test fixture only and
// is never referenced by production code.

// testDDL is the v1 schema, copied from plan §5.5.
const testDDL = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY, username TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL, role TEXT NOT NULL,
  created_at DATETIME, last_login_at DATETIME, disabled INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS user_instance_grant (
  user_id INTEGER, instance_id INTEGER, perm TEXT,
  PRIMARY KEY (user_id, instance_id, perm)
);
CREATE TABLE IF NOT EXISTS instances (
  id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
  dir TEXT NOT NULL,
  port INTEGER NOT NULL,
  dotnet_path TEXT, server_jar TEXT,
  owner_id INTEGER DEFAULT 1,
  auto_start INTEGER DEFAULT 0, auto_restart INTEGER DEFAULT 0,
  max_restart INTEGER DEFAULT 5,
  stop_timeout_sec INTEGER DEFAULT 30,
  term TEXT DEFAULT 'xterm-256color',
  color_mode TEXT DEFAULT 'enhanced',
  created_at DATETIME, memo TEXT
);
CREATE TABLE IF NOT EXISTS instance_state (
  instance_id INTEGER PRIMARY KEY, state TEXT, pid INTEGER,
  started_at DATETIME, stopped_at DATETIME, exit_code INTEGER,
  last_error TEXT, online_players INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS log_events (
  id INTEGER PRIMARY KEY, instance_id INTEGER, ts DATETIME,
  level TEXT, event TEXT, payload TEXT
);
CREATE INDEX IF NOT EXISTS idx_log_events ON log_events(instance_id, ts);
CREATE TABLE IF NOT EXISTS backups (
  id INTEGER PRIMARY KEY, instance_id INTEGER, world TEXT,
  path TEXT, size_bytes INTEGER, sha256 TEXT,
  kind TEXT,
  created_at DATETIME, note TEXT
);
CREATE TABLE IF NOT EXISTS jobs (
  id INTEGER PRIMARY KEY, instance_id INTEGER, type TEXT,
  cron TEXT, payload TEXT, enabled INTEGER, last_run DATETIME, last_result TEXT
);
CREATE TABLE IF NOT EXISTS audit_logs (
  id INTEGER PRIMARY KEY, user_id INTEGER, action TEXT,
  target TEXT, detail TEXT, ip TEXT, ts DATETIME
);
`

// testSeedAdmin mirrors the seeded admin row required by §5.5/D3. It is only
// referenced by TestSchemaMatchesTestDDL as a documentation anchor; the live
// seed comes from store.Migrate (FirstRunPasswordMarker) on both paths.
const testSeedAdmin = `INSERT INTO users (id, username, password_hash, role, created_at, disabled)
VALUES (1, 'admin', '!first-run-password-not-set', 'admin', '2026-01-01T00:00:00Z', 0)`

// openTestDB returns a Store backed by its own freshly-created on-disk SQLite
// file inside t.TempDir().
//
// A file (not ":memory:") is used deliberately: t.TempDir() gives every test
// its own database, so tests stay parallel-safe, and a file exercises the same
// WAL/locking path as production.
func openRepoTestDB(t *testing.T) *Store {
	t.Helper()

	path := filepath.Join(t.TempDir(), "scnetm-test.db")
	db, err := ensureSchema(path)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

// ensureSchema opens path with store.Open and runs store.Migrate, the same
// pair cmd/scnetm uses. If either is missing or fails, the test fails rather
// than silently running against a different schema.
func ensureSchema(path string) (*sql.DB, error) {
	db, err := Open(path)
	if err != nil {
		return nil, fmt.Errorf("store.Open: %w", err)
	}
	if err := Migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store.Migrate: %w", err)
	}
	return db, nil
}

// mustCreateInstance is the shared fixture: an instance with sane defaults.
func mustCreateInstance(t *testing.T, s *Store, name string) *Instance {
	t.Helper()
	inst := &Instance{
		Name:           name,
		Dir:            "/srv/scnetm/" + name,
		Port:           19132,
		Dotnet:         "/usr/bin/dotnet",
		ServerDL:       "Survivalcraft.dll",
		AutoStart:      false,
		AutoRestart:    true,
		MaxRestart:     DefaultMaxRestart,
		StopTimeoutSec: DefaultStopTimeoutSec,
		Term:           DefaultTerm,
		ColorMode:      DefaultColorMode,
		Memo:           "fixture",
	}
	if err := s.Instances().Create(context.Background(), inst); err != nil {
		t.Fatalf("create instance %q: %v", name, err)
	}
	return inst
}
