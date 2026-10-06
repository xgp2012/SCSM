package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openTestDB opens a database in a temp directory. Using a real file (rather
// than ":memory:") is deliberate: it exercises WAL mode and the on-disk
// migration path that production uses.
func openTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "scnetm.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\") = nil, want error")
	}
}

func TestMigrateCreatesAllTables(t *testing.T) {
	db, _ := openTestDB(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, table := range Tables() {
		t.Run(table, func(t *testing.T) {
			var name string
			err := db.QueryRow(
				`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
			).Scan(&name)
			if err != nil {
				t.Fatalf("table %s missing: %v", table, err)
			}
		})
	}

	// The log index from §5.5 must exist too.
	var idx string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_log_events'`,
	).Scan(&idx)
	if err != nil {
		t.Fatalf("index idx_log_events missing: %v", err)
	}
}

func TestMigrateRecordsVersion(t *testing.T) {
	db, _ := openTestDB(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	got, err := SchemaVersion(db)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if want := LatestSchemaVersion(); got != want {
		t.Errorf("SchemaVersion() = %d, want %d", got, want)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if want := len(migrations); count != want {
		t.Errorf("schema_migrations rows = %d, want %d", count, want)
	}
}

// TestMigrateIsIdempotent is the core requirement: running Migrate repeatedly
// must not error, must not duplicate rows and must not disturb data.
func TestMigrateIsIdempotent(t *testing.T) {
	db, _ := openTestDB(t)

	for i := 0; i < 3; i++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("Migrate (run %d): %v", i+1, err)
		}
	}

	var ledgerRows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&ledgerRows); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if want := len(migrations); ledgerRows != want {
		t.Errorf("schema_migrations rows = %d after 3 runs, want %d", ledgerRows, want)
	}

	var userRows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&userRows); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userRows != 1 {
		t.Errorf("users rows = %d after 3 runs, want exactly 1 (seed must not duplicate)", userRows)
	}
}

// TestMigratePreservesExistingData proves a second call is a true no-op.
func TestMigratePreservesExistingData(t *testing.T) {
	db, _ := openTestDB(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Simulate an operator who has set a real password and created an instance.
	if _, err := db.Exec(
		`UPDATE users SET password_hash = '$2a$10$realbcrypthash', last_login_at = '2026-01-01T00:00:00Z' WHERE id = 1`,
	); err != nil {
		t.Fatalf("update user: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO instances (id, name, dir, port) VALUES (1, 'survival', '/srv/scnetm/instances/1', 28887)`,
	); err != nil {
		t.Fatalf("insert instance: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate (second run): %v", err)
	}

	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = 1`).Scan(&hash); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if hash != "$2a$10$realbcrypthash" {
		t.Errorf("password_hash = %q, want it untouched by re-migration", hash)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM instances WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("read instance: %v", err)
	}
	if name != "survival" {
		t.Errorf("instance name = %q, want %q", name, "survival")
	}
}

// TestMigrateSeedsFirstRunAdmin checks the first-run contract the API relies on.
func TestMigrateSeedsFirstRunAdmin(t *testing.T) {
	db, _ := openTestDB(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var (
		id       int
		username string
		hash     string
		role     string
		disabled int
	)
	err := db.QueryRow(
		`SELECT id, username, password_hash, role, disabled FROM users WHERE id = 1`,
	).Scan(&id, &username, &hash, &role, &disabled)
	if err != nil {
		t.Fatalf("seeded admin row missing: %v", err)
	}

	if id != 1 {
		t.Errorf("id = %d, want 1", id)
	}
	if username != "admin" {
		t.Errorf("username = %q, want %q", username, "admin")
	}
	if role != "admin" {
		t.Errorf("role = %q, want %q", role, "admin")
	}
	if disabled != 0 {
		t.Errorf("disabled = %d, want 0", disabled)
	}
	if hash != FirstRunPasswordMarker {
		t.Errorf("password_hash = %q, want the first-run marker %q", hash, FirstRunPasswordMarker)
	}

	// The marker must be recognisable, and must never look like a usable hash.
	if !IsFirstRunHash(hash) {
		t.Error("IsFirstRunHash(seeded hash) = false, want true")
	}
	if IsFirstRunHash("$2a$10$realbcrypthash") {
		t.Error("IsFirstRunHash(real bcrypt hash) = true, want false")
	}
	if IsFirstRunHash("") != true {
		t.Error("IsFirstRunHash(\"\") = false, want true (empty means unset)")
	}
}

// TestSeedIsConditionalOnEmptyUsersTable verifies Migrate never resurrects or
// overwrites the admin account once users exist.
func TestSeedIsConditionalOnEmptyUsersTable(t *testing.T) {
	db, _ := openTestDB(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM users`); err != nil {
		t.Fatalf("delete users: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO users (id, username, password_hash, role) VALUES (7, 'operator', 'x', 'operator')`,
	); err != nil {
		t.Fatalf("insert operator: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("users rows = %d, want 1 (seed must not run on a non-empty table)", count)
	}

	var username string
	if err := db.QueryRow(`SELECT username FROM users`).Scan(&username); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if username != "operator" {
		t.Errorf("username = %q, want %q", username, "operator")
	}
}

// TestMigratePersistsAcrossReopen migrates, closes, reopens and re-migrates —
// the panel's upgrade path.
func TestMigratePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scnetm.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(db1); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("Open (reopen): %v", err)
	}
	defer db2.Close()

	if err := Migrate(db2); err != nil {
		t.Fatalf("Migrate (reopen): %v", err)
	}

	got, err := SchemaVersion(db2)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if want := LatestSchemaVersion(); got != want {
		t.Errorf("SchemaVersion() after reopen = %d, want %d", got, want)
	}
}

// TestOpenEnablesPragmas asserts the connection settings the panel depends on.
func TestOpenEnablesPragmas(t *testing.T) {
	db, _ := openTestDB(t)

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1 (single SQLite writer)", got)
	}

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestMigrateNilDB(t *testing.T) {
	if err := Migrate(nil); err == nil {
		t.Fatal("Migrate(nil) = nil, want error")
	}
}

// TestSchemaColumnsMatchPlan spot-checks the D3 extension columns, which exist
// now so multi-user needs no migration later.
func TestSchemaColumnsMatchPlan(t *testing.T) {
	db, _ := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	required := map[string][]string{
		"users":               {"id", "username", "password_hash", "role", "created_at", "last_login_at", "disabled"},
		"user_instance_grant": {"user_id", "instance_id", "perm"},
		"instances":           {"id", "name", "dir", "port", "dotnet_path", "server_jar", "owner_id", "auto_start", "auto_restart", "max_restart", "stop_timeout_sec", "term", "color_mode", "created_at", "memo"},
		"instance_state":      {"instance_id", "state", "pid", "started_at", "stopped_at", "exit_code", "last_error", "online_players"},
		"log_events":          {"id", "instance_id", "ts", "level", "event", "payload"},
		"backups":             {"id", "instance_id", "world", "path", "size_bytes", "sha256", "kind", "created_at", "note"},
		"jobs":                {"id", "instance_id", "type", "cron", "payload", "enabled", "last_run", "last_result"},
		"audit_logs":          {"id", "user_id", "action", "target", "detail", "ip", "ts"},
	}

	for table, columns := range required {
		t.Run(table, func(t *testing.T) {
			rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
			if err != nil {
				t.Fatalf("pragma_table_info(%s): %v", table, err)
			}
			defer rows.Close()

			have := map[string]bool{}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatalf("scan: %v", err)
				}
				have[name] = true
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("rows: %v", err)
			}

			for _, col := range columns {
				if !have[col] {
					t.Errorf("table %s is missing column %s (have %v)", table, col, have)
				}
			}
		})
	}
}
