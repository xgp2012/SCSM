package store

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestSchemaMatchesTestDDL guards the one real risk of having a test-local copy
// of the schema (testutil_test.go's testDDL, used only if the sibling
// migrate.go is ever unusable): drift between migrate.go's schemaV1 and
// testDDL.
//
// It applies each schema to its own database and compares the resulting column
// sets, so adding a column to one and not the other fails loudly here instead
// of producing a mysterious "no such column" in an unrelated repository test.
func TestSchemaMatchesTestDDL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fromMigrate := openFreshForSchema(t, true)
	fromTestDDL := openFreshForSchema(t, false)

	want := tableColumns(t, fromMigrate)
	got := tableColumns(t, fromTestDDL)

	if len(want) == 0 {
		t.Fatal("migrate.go produced no tables; the schema did not apply")
	}
	for _, table := range Tables() {
		wantCols, ok := want[table]
		if !ok {
			t.Errorf("migrate.go is missing table %q listed in Tables()", table)
			continue
		}
		gotCols, ok := got[table]
		if !ok {
			t.Errorf("testDDL is missing table %q", table)
			continue
		}
		if !reflect.DeepEqual(wantCols, gotCols) {
			t.Errorf("column drift in %s:\n  migrate.go : %v\n  testDDL    : %v",
				table, wantCols, gotCols)
		}
	}

	// The index that makes log_events queries usable must exist in both.
	for name, db := range map[string]*sql.DB{"migrate.go": fromMigrate, "testDDL": fromTestDDL} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_log_events'`).Scan(&n); err != nil {
			t.Fatalf("%s: query index: %v", name, err)
		}
		if n != 1 {
			t.Errorf("%s: index idx_log_events missing", name)
		}
	}
}

// TestSchemaV1SeedsAdmin pins the first-run contract the API depends on, in
// terms of the repository layer: the built-in admin exists, is enabled, holds
// the admin role and has no usable password yet.
//
// migrate_test.go pins the same seed at the SQL level; this one asserts the
// scan path (nullable DATETIME -> *time.Time, INTEGER -> bool) reads it back
// correctly, which is what the API actually consumes.
func TestSchemaV1SeedsAdmin(t *testing.T) {
	t.Parallel()
	s := openRepoTestDB(t)
	ctx := context.Background()

	u, err := s.Users().GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID(1): %v", err)
	}
	if u.Username != "admin" {
		t.Errorf("username = %q, want admin", u.Username)
	}
	if u.Role != RoleAdmin {
		t.Errorf("role = %q, want %q", u.Role, RoleAdmin)
	}
	if u.Disabled {
		t.Error("seeded admin must be enabled")
	}
	if !u.NeedsPasswordSetup() {
		t.Errorf("seeded admin must require password setup, got hash %q", u.PasswordHash)
	}
	if u.CreatedAt.IsZero() {
		t.Error("seeded admin must carry created_at")
	}
	if u.LastLoginAt != nil {
		t.Errorf("seeded admin must have no last_login_at, got %v", u.LastLoginAt)
	}
}

// --- helpers -------------------------------------------------------------

func openFreshForSchema(t *testing.T, viaMigrate bool) *sql.DB {
	t.Helper()
	path := t.TempDir() + "/schema.db"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if viaMigrate {
		if err := Migrate(db); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		return db
	}
	if _, err := db.Exec(testDDL); err != nil {
		t.Fatalf("exec testDDL: %v", err)
	}
	return db
}

// tableColumns maps table name -> sorted column names.
func tableColumns(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	out := make(map[string][]string)
	for _, table := range Tables() {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", table, err)
		}
		var cols []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan column name: %v", err)
			}
			cols = append(cols, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate columns of %s: %v", table, err)
		}
		_ = rows.Close()
		sort.Strings(cols)
		out[table] = cols
	}
	return out
}

// TestTestDDLIsComplete is a cheap sanity check that the fallback DDL is valid
// SQL rather than a truncated string.
func TestTestDDLIsComplete(t *testing.T) {
	t.Parallel()
	if got := strings.Count(testDDL, "CREATE TABLE"); got != len(Tables()) {
		t.Errorf("testDDL declares %d tables, Tables() lists %d", got, len(Tables()))
	}
}
