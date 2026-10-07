// Package store owns the panel's SQLite database: opening it, migrating it and
// (in sibling files) the repositories that read and write the tables declared
// here.
//
// The schema in this file is the single source of truth for the panel's
// metadata. It is the DDL from plan §5.5, including the D3 forward-compatibility
// extension points (users.role, instances.owner_id and user_instance_grant) so
// that turning on multi-user later needs no table rewrite and no data
// migration.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// DriverName is the database/sql driver registered by modernc.org/sqlite.
const DriverName = "sqlite"

// DefaultBusyTimeout is how long a writer waits for a competing lock before
// giving up. The panel is a single-process, multi-goroutine writer, so this
// only needs to cover short scheduling hiccups.
const DefaultBusyTimeout = 5 * time.Second

// FirstRunPasswordMarker is stored in users.password_hash for the auto-created
// admin account. It is deliberately NOT a valid bcrypt hash, so it can never be
// matched by a password check: any login attempt against it must fail. The API
// layer looks for exactly this value to decide that first-run password setup is
// still pending and to gate the panel behind it.
const FirstRunPasswordMarker = "!first-run-password-not-set"

// DefaultAdminUsername is the username of the seeded administrator. It is the
// default target of SeedAdminPassword and the account the first-run setup flow
// looks for.
const DefaultAdminUsername = "admin"

// IsFirstRunHash reports whether hash is the first-run marker, i.e. the admin
// account exists but no password has been chosen yet.
func IsFirstRunHash(hash string) bool {
	return hash == FirstRunPasswordMarker || hash == ""
}

// SeedAdminPassword sets a password hash on the built-in administrator during
// bootstrap, instead of leaving the first-run marker in place.
//
// It exists so an operator can opt into a known initial password via
// `default_admin_password` in config.yaml. The default path — calling Migrate
// and nothing else — still seeds FirstRunPasswordMarker and forces the operator
// through the first-run setup flow, which is the safer behaviour and remains
// the one documented in docs/README.md.
//
// The update is deliberately conditional on the current hash still being a
// first-run marker. An operator who has already set a real password must never
// have it silently overwritten by a config file that still carries the initial
// value, and re-running the panel after a password change must be a no-op.
//
// It returns the number of rows changed: 0 means the admin already had a real
// password (or no admin row exists), which callers should report rather than
// treat as a failure.
func SeedAdminPassword(db *sql.DB, username, passwordHash string) (int, error) {
	if db == nil {
		return 0, errors.New("store: SeedAdminPassword called with a nil database")
	}
	if username == "" {
		username = DefaultAdminUsername
	}
	if passwordHash == "" {
		return 0, errors.New("store: SeedAdminPassword called with an empty hash")
	}
	if IsFirstRunHash(passwordHash) {
		return 0, errors.New("store: SeedAdminPassword needs a real hash, not a first-run marker")
	}

	// The WHERE clause is the safety property: only a row that is still in the
	// first-run state can be changed. This makes the call idempotent in the
	// sense that matters — it can never clobber a password someone chose.
	res, err := db.Exec(`
		UPDATE users SET password_hash = ?
		WHERE username = ? AND (password_hash = ? OR password_hash = '')`,
		passwordHash, username, FirstRunPasswordMarker,
	)
	if err != nil {
		return 0, fmt.Errorf("store: seed admin password: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: seed admin password: %w", err)
	}
	return int(n), nil
}

// Open opens (creating if needed) the panel database at path and applies the
// connection settings the panel relies on.
//
// The DSN enables three pragmas:
//
//   - busy_timeout(5000): wait rather than fail on a locked database.
//   - journal_mode(WAL): readers never block the writer, which matters because
//     the log/event tables are written from supervisor goroutines while the API
//     serves reads.
//   - foreign_keys(1): off by default in SQLite; the grant table depends on it.
//
// The pool is pinned to a single connection. SQLite allows exactly one writer,
// and a larger pool turns concurrent writes into SQLITE_BUSY errors instead of
// letting them queue. Everything in the panel is fast local I/O, so the
// serialisation is not a bottleneck.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("store: database path must not be empty")
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
		path, DefaultBusyTimeout.Milliseconds(),
	)

	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// One writer, and a bounded wait for the initial connectivity check.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	return db, nil
}

// migration is one ordered, versioned schema change.
type migration struct {
	version int
	name    string
	sql     string
}

// migrations is the ordered list of schema changes. Append only: never edit or
// renumber an already-released entry, because databases in the field record the
// version they have applied.
var migrations = []migration{
	{
		version: 1,
		name:    "initial schema",
		sql:     schemaV1,
	},
}

// schemaV1 is the initial DDL, transcribed from plan §5.5.
//
// Types are the SQLite-appropriate ones the plan specifies: INTEGER PRIMARY KEY
// for surrogate keys, DATETIME for timestamps (SQLite has no date type; values
// are stored as RFC3339 text or unix seconds by the repositories) and INTEGER
// for booleans.
const schemaV1 = `
-- 面板用户与权限
-- 【D3 扩展位】单用户模式下仅一行（id=1, role='admin'），首次启动自动创建并要求设置密码。
-- 表结构、中间件、owner_id 现在就留好，将来开启多用户无需改表、无需数据迁移。
CREATE TABLE users (
  id INTEGER PRIMARY KEY, username TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL, role TEXT NOT NULL,      -- admin/operator/viewer
  created_at DATETIME, last_login_at DATETIME, disabled INTEGER DEFAULT 0
);
CREATE TABLE user_instance_grant (                      -- 实例级授权（单用户下恒为空，逻辑已就位）
  user_id INTEGER, instance_id INTEGER, perm TEXT,       -- read/control/config
  PRIMARY KEY (user_id, instance_id, perm)
);

-- 实例
CREATE TABLE instances (
  id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
  dir TEXT NOT NULL,                                     -- 工作目录绝对路径
  port INTEGER NOT NULL,                                 -- UDP
  dotnet_path TEXT, server_jar TEXT,                     -- Survivalcraft.dll
  owner_id INTEGER DEFAULT 1,                            -- 【D3 扩展位】实例归属
  auto_start INTEGER DEFAULT 0, auto_restart INTEGER DEFAULT 0,
  max_restart INTEGER DEFAULT 5,
  stop_timeout_sec INTEGER DEFAULT 30,
  term TEXT DEFAULT 'xterm-256color',                    -- 【D1】PTY TERM
  color_mode TEXT DEFAULT 'enhanced',                    -- enhanced|basic（降级时记录）
  created_at DATETIME, memo TEXT
);

-- 运行态快照（重启后可恢复展示）
CREATE TABLE instance_state (
  instance_id INTEGER PRIMARY KEY, state TEXT, pid INTEGER,
  started_at DATETIME, stopped_at DATETIME, exit_code INTEGER,
  last_error TEXT, online_players INTEGER DEFAULT 0
);

-- 日志索引（正文在文件，库中只存索引与检索关键词）
CREATE TABLE log_events (
  id INTEGER PRIMARY KEY, instance_id INTEGER, ts DATETIME,
  level TEXT, event TEXT, payload TEXT                     -- JSON
);
CREATE INDEX idx_log_events ON log_events(instance_id, ts);

-- 备份
CREATE TABLE backups (
  id INTEGER PRIMARY KEY, instance_id INTEGER, world TEXT,
  path TEXT, size_bytes INTEGER, sha256 TEXT,
  kind TEXT,                                               -- manual/scheduled/pre-start
  created_at DATETIME, note TEXT
);

-- 任务与审计
CREATE TABLE jobs (
  id INTEGER PRIMARY KEY, instance_id INTEGER, type TEXT,  -- backup/restart/command
  cron TEXT, payload TEXT, enabled INTEGER, last_run DATETIME, last_result TEXT
);
CREATE TABLE audit_logs (
  id INTEGER PRIMARY KEY, user_id INTEGER, action TEXT,
  target TEXT, detail TEXT, ip TEXT, ts DATETIME
);
`

// schemaMigrationsDDL creates the ledger. It is applied outside the versioned
// migration list because it is the thing that makes the list work.
const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  applied_at DATETIME
);
`

// seedAdminSQL inserts the single administrator row used by single-user mode.
//
// The password_hash is the first-run marker rather than a real bcrypt hash, so
// the account is unusable until the operator sets a password through the
// forced first-run flow.
const seedAdminSQL = `
INSERT INTO users (id, username, password_hash, role, created_at, disabled)
SELECT 1, '` + DefaultAdminUsername + `', ?, 'admin', ?, 0
WHERE NOT EXISTS (SELECT 1 FROM users)
`

// Migrate brings db up to the current schema version.
//
// It is idempotent: each migration runs at most once, is recorded in
// schema_migrations, and is wrapped in a transaction together with its ledger
// insert so a crash mid-migration can never record a version that was not
// applied. Calling Migrate on an already-current database is a no-op.
func Migrate(db *sql.DB) error {
	if db == nil {
		return errors.New("store: Migrate called with a nil database")
	}

	if _, err := db.Exec(schemaMigrationsDDL); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	current, hasCurrent, err := currentVersion(db)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if hasCurrent && m.version <= current {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return err
		}
	}

	return seedAdmin(db)
}

// currentVersion reports the highest applied migration version. hasCurrent is
// false when no migration has ever been applied.
func currentVersion(db *sql.DB) (version int, hasCurrent bool, err error) {
	row := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`)
	var maxVersion sql.NullInt64
	if err := row.Scan(&maxVersion); err != nil {
		return 0, false, fmt.Errorf("store: read schema version: %w", err)
	}
	if !maxVersion.Valid {
		return 0, false, nil
	}
	return int(maxVersion.Int64), true, nil
}

// applyMigration runs one migration and records it, atomically.
func applyMigration(db *sql.DB, m migration) error {
	if m.version <= 0 {
		return fmt.Errorf("store: migration %q has invalid version %d", m.name, m.version)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin migration %d (%s): %w", m.version, m.name, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit has succeeded

	// Re-check inside the transaction: another process may have applied it
	// between our read and this write.
	var exists int
	err = tx.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, m.version).Scan(&exists)
	if err != nil {
		return fmt.Errorf("store: check migration %d: %w", m.version, err)
	}
	if exists > 0 {
		return nil
	}

	if _, err := tx.Exec(m.sql); err != nil {
		return fmt.Errorf("store: apply migration %d (%s): %w", m.version, m.name, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("store: record migration %d (%s): %w", m.version, m.name, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %d (%s): %w", m.version, m.name, err)
	}
	return nil
}

// seedAdmin creates the built-in administrator when the users table is empty.
//
// It is conditional on an empty table so that a database which already has
// users (including a real admin with a real password) is never touched, and so
// repeated Migrate calls are harmless.
func seedAdmin(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&count); err != nil {
		return fmt.Errorf("store: count users: %w", err)
	}
	if count > 0 {
		return nil
	}

	if _, err := db.Exec(seedAdminSQL, FirstRunPasswordMarker, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("store: seed admin user: %w", err)
	}
	return nil
}

// SchemaVersion reports the currently applied schema version of db.
func SchemaVersion(db *sql.DB) (int, error) {
	v, ok, err := currentVersion(db)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return v, nil
}

// LatestSchemaVersion reports the version this binary migrates to.
func LatestSchemaVersion() int {
	latest := 0
	for _, m := range migrations {
		if m.version > latest {
			latest = m.version
		}
	}
	return latest
}

// Tables lists the application tables in dependency-friendly order. Useful for
// diagnostics and for tests that assert the schema was applied.
func Tables() []string {
	return []string{
		"users",
		"user_instance_grant",
		"instances",
		"instance_state",
		"log_events",
		"backups",
		"jobs",
		"audit_logs",
	}
}
