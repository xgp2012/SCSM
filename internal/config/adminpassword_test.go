package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultAdminPasswordIsEmptyByDefault pins the safe default.
//
// The panel binds 0.0.0.0 by default, so an initial password that ships pre-set
// would be a published credential for every fresh install. Opting in must be an
// explicit act by the operator.
func TestDefaultAdminPasswordIsEmptyByDefault(t *testing.T) {
	if got := NewPanel().DefaultAdminPassword; got != "" {
		t.Errorf("DefaultAdminPassword = %q, want empty (the first-run setup flow is the default)", got)
	}
}

// TestDefaultAdminPasswordLengthsMatchAuthPolicy keeps the duplicated bounds in
// step with internal/auth, which owns the real password policy.
//
// internal/config does not import internal/auth (that would invert the
// dependency direction), so the constants are copied. This test is the tripwire
// that prevents the copies from drifting: if auth.MinPasswordLength rises to 12
// and this file still says 8, a config the panel accepts at start-up would be
// rejected at hash time — or, worse, a value accepted here would be stored
// before any check.
func TestDefaultAdminPasswordLengthsMatchAuthPolicy(t *testing.T) {
	// Values mirrored from internal/auth/password.go. If this test fails, the
	// mismatch is the finding: update MinAdminPasswordLength /
	// MaxAdminPasswordLength in panel.go to match auth, not the other way round.
	//
	// authMinPasswordLength was lowered from 8 to 6 when adfmin (6 characters)
	// became the configured initial administrator password.
	const (
		authMinPasswordLength = 6
		authMaxPasswordLength = 72
	)
	if MinAdminPasswordLength != authMinPasswordLength {
		t.Errorf("MinAdminPasswordLength = %d, but auth.MinPasswordLength = %d",
			MinAdminPasswordLength, authMinPasswordLength)
	}
	if MaxAdminPasswordLength != authMaxPasswordLength {
		t.Errorf("MaxAdminPasswordLength = %d, but auth.MaxPasswordLength = %d",
			MaxAdminPasswordLength, authMaxPasswordLength)
	}
}

// TestValidateDefaultAdminPassword covers the start-up validation of the
// optional initial password.
func TestValidateDefaultAdminPassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"unset is fine", "", false},
		{"a normal 10-character password", "adfmin2026", false},
		{"the configured default, 6 characters", "adfmin", false},
		{"exactly at the minimum", "abcdef", false},
		{"exactly at the maximum", strings.Repeat("a", 72), false},
		{"common forms are accepted here", "Passw0rd!X", false},

		{"too short", "abcde", true},
		{"one below the minimum", "abcde", true},
		{"blank", "   ", true},
		{"leading whitespace", " adfmin", true},
		{"trailing whitespace", "adfmin ", true},
		{"tabs only", "\t\t\t\t", true},
		{"over the bcrypt limit", strings.Repeat("a", 73), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPanel()
			p.DefaultAdminPassword = tc.password
			err := p.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil for default_admin_password %q, want an error", tc.password)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v for default_admin_password %q, want nil", err, tc.password)
			}
			// Whatever else Validate reports, the message must name the field so
			// an operator can find the offending key.
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "default_admin_password") {
				t.Errorf("error %q does not name the default_admin_password key", err)
			}
		})
	}
}

// TestValidateDefaultAdminPasswordDoesNotBlockOtherErrors confirms an unset
// password contributes nothing to the aggregate error.
func TestValidateDefaultAdminPasswordDoesNotBlockOtherErrors(t *testing.T) {
	p := NewPanel()
	p.DefaultAdminPassword = ""
	if err := p.Validate(); err != nil {
		t.Fatalf("a default panel with no initial password must validate, got: %v", err)
	}
}

// TestDefaultAdminPasswordNeverSerialised is the leak guard.
//
// The field carries a secret, so it must be absent from any marshalled view of
// the configuration. The YAML side matters as much as JSON: it would be easy for
// a future "write the effective config back out" feature to persist the password
// to disk in plain text.
func TestDefaultAdminPasswordNeverSerialised(t *testing.T) {
	const secret = "adfmin2026"
	p := NewPanel()
	p.DefaultAdminPassword = secret

	if out, err := json.Marshal(p); err != nil {
		t.Fatalf("json.Marshal: %v", err)
	} else if strings.Contains(string(out), secret) {
		t.Errorf("JSON encoding leaked the password: %s", out)
	} else if strings.Contains(string(out), "default_admin_password") {
		t.Errorf("JSON encoding includes the default_admin_password key at all: %s", out)
	}

	// Redacted() is what the log path uses; it must blank the value.
	if got := p.Redacted().DefaultAdminPassword; got != "" {
		t.Errorf("Redacted().DefaultAdminPassword = %q, want empty", got)
	}
}

// TestStringReportsPresenceWithoutValue verifies the human-readable summary
// mentions the setting but never its value.
func TestStringReportsPresenceWithoutValue(t *testing.T) {
	const secret = "adfmin2026"

	p := NewPanel()
	p.DefaultAdminPassword = secret
	s := p.String()
	if strings.Contains(s, secret) {
		t.Errorf("String() leaked the password:\n%s", s)
	}
	if !strings.Contains(s, "default_admin_password: (set)") {
		t.Errorf("String() does not report the password as set:\n%s", s)
	}

	unset := NewPanel()
	if s := unset.String(); !strings.Contains(s, "default_admin_password: (unset)") {
		t.Errorf("String() does not report an unset password:\n%s", s)
	}
}

// TestLoadPanelReadsDefaultAdminPassword confirms the YAML key is wired up and
// that strict decoding accepts it (an unknown key is a start-up error).
func TestLoadPanelReadsDefaultAdminPassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "default_admin_password: \"adfmin2026\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	p, err := LoadPanel(path)
	if err != nil {
		t.Fatalf("LoadPanel: %v", err)
	}
	if p.DefaultAdminPassword != "adfmin2026" {
		t.Errorf("DefaultAdminPassword = %q, want %q", p.DefaultAdminPassword, "adfmin2026")
	}
}

// TestLoadPanelRejectsShortDefaultAdminPassword proves the policy is enforced at
// load time, before the database is opened — so a bad value fails the start-up
// rather than producing a panel nobody can log into.
//
// The value below is one character under the current floor (6). It was "adfmin"
// while the floor was 8; that is now the accepted default, so the test uses a
// value that is still invalid.
func TestLoadPanelRejectsShortDefaultAdminPassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "default_admin_password: \"abcde\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := LoadPanel(path); err == nil {
		t.Fatal("LoadPanel accepted a 5-character default_admin_password, want an error")
	}
}

// TestLoadPanelAcceptsTheConfiguredDefault pins the value actually shipped in
// configs/config.example.yaml, so lowering it further (or raising the floor
// again) breaks a test rather than silently shipping a config the panel refuses
// to start with.
func TestLoadPanelAcceptsTheConfiguredDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "default_admin_password: \"adfmin\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	p, err := LoadPanel(path)
	if err != nil {
		t.Fatalf("LoadPanel rejected the shipped default_admin_password: %v", err)
	}
	if p.DefaultAdminPassword != "adfmin" {
		t.Errorf("DefaultAdminPassword = %q, want %q", p.DefaultAdminPassword, "adfmin")
	}
}
