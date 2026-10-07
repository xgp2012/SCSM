package store

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// hashFor produces a real bcrypt hash for the tests below.
//
// A genuine hash is used rather than a plausible-looking fake so the assertions
// exercise the same values the panel writes in production. bcrypt.MinCost keeps
// the test fast; the stored cost is not what these tests are checking.
func hashFor(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}
	return string(h)
}

// TestSeedAdminPasswordInstallsHash covers the configured-initial-password path:
// Migrate seeds the first-run marker, then SeedAdminPassword replaces it.
func TestSeedAdminPasswordInstallsHash(t *testing.T) {
	db, _ := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	hash := hashFor(t, "adfmin2026")
	n, err := SeedAdminPassword(db, DefaultAdminUsername, hash)
	if err != nil {
		t.Fatalf("SeedAdminPassword: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows affected = %d, want 1", n)
	}

	var stored string
	if err := db.QueryRow(
		`SELECT password_hash FROM users WHERE username = ?`, DefaultAdminUsername,
	).Scan(&stored); err != nil {
		t.Fatalf("read back admin row: %v", err)
	}

	if stored != hash {
		t.Errorf("stored hash does not match the one installed")
	}
	if IsFirstRunHash(stored) {
		t.Error("IsFirstRunHash(stored) = true; the panel would still demand first-run setup")
	}
	// The point of the exercise: the configured password actually authenticates.
	if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte("adfmin2026")); err != nil {
		t.Errorf("the seeded password does not verify: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte("wrong-password")); err == nil {
		t.Error("a wrong password verified against the seeded hash")
	}
}

// TestSeedAdminPasswordNeverOverwrites is the safety property that makes it
// acceptable to leave default_admin_password in config.yaml indefinitely: once a
// real password exists, the function must decline to touch it.
//
// Without this, every panel restart would silently reset the administrator's
// password back to the configured value, quietly undoing a password change made
// in the UI — and, worse, re-publishing a known credential.
func TestSeedAdminPasswordNeverOverwrites(t *testing.T) {
	db, _ := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Simulate an operator who changed their password in the panel.
	chosen := hashFor(t, "a-password-the-operator-chose")
	if _, err := db.Exec(
		`UPDATE users SET password_hash = ? WHERE username = ?`, chosen, DefaultAdminUsername,
	); err != nil {
		t.Fatalf("simulate operator password change: %v", err)
	}

	// Now a restart re-applies the config value.
	n, err := SeedAdminPassword(db, DefaultAdminUsername, hashFor(t, "adfmin2026"))
	if err != nil {
		t.Fatalf("SeedAdminPassword: %v", err)
	}
	if n != 0 {
		t.Errorf("rows affected = %d, want 0 (must not overwrite an existing password)", n)
	}

	var stored string
	if err := db.QueryRow(
		`SELECT password_hash FROM users WHERE username = ?`, DefaultAdminUsername,
	).Scan(&stored); err != nil {
		t.Fatalf("read back admin row: %v", err)
	}
	if stored != chosen {
		t.Error("the operator's password was overwritten by the configured default")
	}
}

// TestSeedAdminPasswordIsIdempotent confirms a second application is a no-op,
// so starting the panel repeatedly is harmless.
func TestSeedAdminPasswordIsIdempotent(t *testing.T) {
	db, _ := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	first := hashFor(t, "adfmin2026")
	if n, err := SeedAdminPassword(db, DefaultAdminUsername, first); err != nil || n != 1 {
		t.Fatalf("first call: n=%d err=%v, want n=1", n, err)
	}

	// A different password on the second call must still be refused, because the
	// account no longer holds a first-run marker.
	n, err := SeedAdminPassword(db, DefaultAdminUsername, hashFor(t, "something-else"))
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if n != 0 {
		t.Errorf("second call changed %d rows, want 0", n)
	}

	var stored string
	if err := db.QueryRow(
		`SELECT password_hash FROM users WHERE username = ?`, DefaultAdminUsername,
	).Scan(&stored); err != nil {
		t.Fatalf("read back admin row: %v", err)
	}
	if stored != first {
		t.Error("the second call altered the stored hash")
	}
}

// TestSeedAdminPasswordRejectsBadInput pins the guards. Each of these would
// otherwise either panic later or write an unusable row.
func TestSeedAdminPasswordRejectsBadInput(t *testing.T) {
	db, _ := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	t.Run("nil database", func(t *testing.T) {
		if _, err := SeedAdminPassword(nil, DefaultAdminUsername, hashFor(t, "x")); err == nil {
			t.Error("want an error for a nil database")
		}
	})

	t.Run("empty hash", func(t *testing.T) {
		if _, err := SeedAdminPassword(db, DefaultAdminUsername, ""); err == nil {
			t.Error("want an error for an empty hash")
		}
	})

	t.Run("a marker is not a password", func(t *testing.T) {
		// Passing the marker through would leave the account unusable while the
		// log claimed a password had been installed.
		if _, err := SeedAdminPassword(db, DefaultAdminUsername, FirstRunPasswordMarker); err == nil {
			t.Error("want an error when the hash is a first-run marker")
		}
	})

	t.Run("blank username defaults to admin", func(t *testing.T) {
		n, err := SeedAdminPassword(db, "", hashFor(t, "adfmin2026"))
		if err != nil {
			t.Fatalf("SeedAdminPassword with a blank username: %v", err)
		}
		if n != 1 {
			t.Errorf("rows affected = %d, want 1 (blank username should target the admin)", n)
		}
	})
}

// TestSeedAdminPasswordUnknownUserChangesNothing guards against a silent success
// when the admin row is absent (for example a hand-edited database).
func TestSeedAdminPasswordUnknownUserChangesNothing(t *testing.T) {
	db, _ := openTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	n, err := SeedAdminPassword(db, "someone-else", hashFor(t, "adfmin2026"))
	if err != nil {
		t.Fatalf("SeedAdminPassword: %v", err)
	}
	if n != 0 {
		t.Errorf("rows affected = %d, want 0 for an unknown username", n)
	}
}

// TestSeedAdminSQLUsesDefaultAdminUsername keeps the INSERT literal and the
// constant in step. They are written separately (SQL needs a literal, the API
// needs a Go string), so a rename applied to only one of them would leave
// SeedAdminPassword silently targeting a row that does not exist — and the
// failure would be n=0, which is not an error.
func TestSeedAdminSQLUsesDefaultAdminUsername(t *testing.T) {
	if !strings.Contains(seedAdminSQL, "'"+DefaultAdminUsername+"'") {
		t.Errorf("seedAdminSQL does not insert the username %q; it and DefaultAdminUsername have drifted",
			DefaultAdminUsername)
	}
}
