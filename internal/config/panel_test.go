package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewPanelAppliesDefaults(t *testing.T) {
	p := NewPanel()

	if p.Listen != DefaultListen {
		t.Errorf("Listen = %q, want %q", p.Listen, DefaultListen)
	}
	if p.DataDir != DefaultDataDir {
		t.Errorf("DataDir = %q, want %q", p.DataDir, DefaultDataDir)
	}
	if want := filepath.Join(DefaultDataDir, "instances"); p.InstancesDir != want {
		t.Errorf("InstancesDir = %q, want %q", p.InstancesDir, want)
	}
	if p.DotnetPath != DefaultDotnetPath {
		t.Errorf("DotnetPath = %q, want %q", p.DotnetPath, DefaultDotnetPath)
	}
	if p.Term != DefaultTerm {
		t.Errorf("Term = %q, want %q", p.Term, DefaultTerm)
	}
	if p.ColorMode != ColorModeEnhanced {
		t.Errorf("ColorMode = %q, want %q", p.ColorMode, ColorModeEnhanced)
	}
	if p.Defaults.StopTimeoutSec != DefaultStopTimeout {
		t.Errorf("StopTimeoutSec = %d, want %d", p.Defaults.StopTimeoutSec, DefaultStopTimeout)
	}
	if p.Defaults.AutoBackup.Cron != DefaultBackupCron {
		t.Errorf("AutoBackup.Cron = %q, want %q", p.Defaults.AutoBackup.Cron, DefaultBackupCron)
	}
	if p.Defaults.AutoBackup.Keep != DefaultBackupKeep {
		t.Errorf("AutoBackup.Keep = %d, want %d", p.Defaults.AutoBackup.Keep, DefaultBackupKeep)
	}

	// The default pool is the inclusive range 28887..28900 from plan §9.1.
	if len(p.PortPool) != DefaultPortPoolTo-DefaultPortPoolFrom+1 {
		t.Fatalf("len(PortPool) = %d, want %d",
			len(p.PortPool), DefaultPortPoolTo-DefaultPortPoolFrom+1)
	}
	if p.PortPool[0] != DefaultPortPoolFrom {
		t.Errorf("PortPool[0] = %d, want %d", p.PortPool[0], DefaultPortPoolFrom)
	}
	if last := p.PortPool[len(p.PortPool)-1]; last != DefaultPortPoolTo {
		t.Errorf("PortPool[last] = %d, want %d", last, DefaultPortPoolTo)
	}

	if err := p.Validate(); err != nil {
		t.Errorf("default panel failed validation: %v", err)
	}
}

// TestApplyDefaultsIsIdempotent guards the --listen/--data-dir override path in
// main, which calls ApplyDefaults a second time after mutating fields.
func TestApplyDefaultsIsIdempotent(t *testing.T) {
	p := NewPanel()
	first := *p

	p.ApplyDefaults()

	if p.Listen != first.Listen || p.DataDir != first.DataDir ||
		p.InstancesDir != first.InstancesDir || len(p.PortPool) != len(first.PortPool) {
		t.Errorf("ApplyDefaults is not idempotent:\n first=%+v\nsecond=%+v", first, *p)
	}
}

// TestApplyDefaultsDerivesInstancesDir covers the --data-dir override: clearing
// instances_dir must re-derive it from the new data_dir.
func TestApplyDefaultsDerivesInstancesDir(t *testing.T) {
	p := NewPanel()
	p.DataDir = "/srv/custom"
	p.InstancesDir = ""
	p.ApplyDefaults()

	if want := "/srv/custom/instances"; p.InstancesDir != want {
		t.Errorf("InstancesDir = %q, want %q", p.InstancesDir, want)
	}
}

func TestValidateRejectsBadListen(t *testing.T) {
	tests := []struct {
		name   string
		listen string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"missing port", "127.0.0.1"},
		{"port out of range", "127.0.0.1:70000"},
		{"port zero", "127.0.0.1:0"},
		{"negative port", "127.0.0.1:-1"},
		{"non-numeric port", "127.0.0.1:http"},
		{"path in host", "127.0.0.1/x:8080"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPanel()
			p.Listen = tc.listen
			if err := p.Validate(); err == nil {
				t.Fatalf("Validate() = nil for listen %q, want error", tc.listen)
			}
		})
	}
}

func TestValidateAcceptsGoodListen(t *testing.T) {
	for _, listen := range []string{
		"127.0.0.1:8080",
		":8080",
		"8080",
		"0.0.0.0:80",
		"[::1]:8080",
		"localhost:8080",
	} {
		t.Run(listen, func(t *testing.T) {
			p := NewPanel()
			p.Listen = listen
			if err := p.Validate(); err != nil {
				t.Fatalf("Validate() = %v for listen %q, want nil", err, listen)
			}
		})
	}
}

func TestValidatePortPool(t *testing.T) {
	tests := []struct {
		name    string
		pool    []int
		wantErr bool
	}{
		{"empty", []int{}, true},
		{"zero", []int{0}, true},
		{"too large", []int{65536}, true},
		{"negative", []int{-1}, true},
		{"duplicate", []int{28887, 28887}, true},
		{"valid single", []int{28887}, false},
		{"valid range", []int{28887, 28888, 28900}, false},
		{"boundaries", []int{1, 65535}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPanel()
			p.PortPool = tc.pool
			err := p.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil for pool %v, want error", tc.pool)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v for pool %v, want nil", err, tc.pool)
			}
		})
	}
}

func TestValidateColorMode(t *testing.T) {
	if err := func() error {
		p := NewPanel()
		p.ColorMode = ColorModeBasic
		return p.Validate()
	}(); err != nil {
		t.Errorf("color_mode=basic rejected: %v", err)
	}

	p := NewPanel()
	p.ColorMode = "rainbow"
	if err := p.Validate(); err == nil {
		t.Error("Validate() = nil for color_mode=rainbow, want error")
	}
}

func TestValidateAutoBackup(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Panel)
		wantErr bool
	}{
		{"disabled ignores cron", func(p *Panel) { p.Defaults.AutoBackup.Enabled = false }, false},
		{"enabled valid cron", func(p *Panel) {
			p.Defaults.AutoBackup.Enabled = true
		}, false},
		{"enabled bad cron arity", func(p *Panel) {
			p.Defaults.AutoBackup.Enabled = true
			p.Defaults.AutoBackup.Cron = "0 4 * *"
		}, true},
		{"enabled bad cron value", func(p *Panel) {
			p.Defaults.AutoBackup.Enabled = true
			p.Defaults.AutoBackup.Cron = "99 4 * * *"
		}, true},
		{"enabled keep zero", func(p *Panel) {
			p.Defaults.AutoBackup.Enabled = true
			p.Defaults.AutoBackup.Keep = 0
		}, true},
		{"enabled stepped cron", func(p *Panel) {
			p.Defaults.AutoBackup.Enabled = true
			p.Defaults.AutoBackup.Cron = "*/15 2-6 * * 1,3,5"
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPanel()
			tc.mutate(p)
			err := p.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestValidateAggregatesAllErrors(t *testing.T) {
	p := NewPanel()
	p.Listen = "nope"
	p.ColorMode = "rainbow"
	p.PortPool = []int{0}

	err := p.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}
	msg := err.Error()
	for _, want := range []string{"listen", "color_mode", "port_pool"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestLoadPanelFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
listen: "0.0.0.0:9000"
data_dir: "data"
instances_dir: "instances"
template_dir: "templates/server"
dotnet_path: "/opt/dotnet/dotnet"
port_pool: [30000, 30001]
term: "screen-256color"
color_mode: "basic"
defaults:
  stop_timeout_sec: 45
  auto_backup: { enabled: true, cron: "0 3 * * *", keep: 14 }
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := LoadPanel(path)
	if err != nil {
		t.Fatalf("LoadPanel: %v", err)
	}

	if p.Listen != "0.0.0.0:9000" {
		t.Errorf("Listen = %q", p.Listen)
	}
	if p.ColorMode != ColorModeBasic {
		t.Errorf("ColorMode = %q", p.ColorMode)
	}
	if p.Term != "screen-256color" {
		t.Errorf("Term = %q", p.Term)
	}
	if p.Defaults.StopTimeoutSec != 45 {
		t.Errorf("StopTimeoutSec = %d", p.Defaults.StopTimeoutSec)
	}
	if p.Defaults.AutoBackup.Keep != 14 || p.Defaults.AutoBackup.Cron != "0 3 * * *" {
		t.Errorf("AutoBackup = %+v", p.Defaults.AutoBackup)
	}
	if len(p.PortPool) != 2 || p.PortPool[0] != 30000 {
		t.Errorf("PortPool = %v", p.PortPool)
	}

	// Paths are resolved relative to the config file's directory.
	if !filepath.IsAbs(p.DataDir) {
		t.Errorf("DataDir = %q, want absolute", p.DataDir)
	}
	if want := filepath.Join(dir, "data"); p.DataDir != want {
		t.Errorf("DataDir = %q, want %q", p.DataDir, want)
	}
	if want := filepath.Join(dir, "instances"); p.InstancesDir != want {
		t.Errorf("InstancesDir = %q, want %q", p.InstancesDir, want)
	}
	// An absolute dotnet_path is preserved.
	if p.DotnetPath != "/opt/dotnet/dotnet" {
		t.Errorf("DotnetPath = %q", p.DotnetPath)
	}
	if p.SourcePath() != path {
		t.Errorf("SourcePath() = %q, want %q", p.SourcePath(), path)
	}
}

// TestLoadPanelPartialFileUsesDefaults is the important robustness case: a file
// that sets one field must still get every other default.
func TestLoadPanelPartialFileUsesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("listen: \":9999\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := LoadPanel(path)
	if err != nil {
		t.Fatalf("LoadPanel: %v", err)
	}
	if p.Listen != ":9999" {
		t.Errorf("Listen = %q", p.Listen)
	}
	if p.ColorMode != DefaultColorMode {
		t.Errorf("ColorMode = %q, want default", p.ColorMode)
	}
	if p.Defaults.AutoBackup.Cron != DefaultBackupCron {
		t.Errorf("AutoBackup.Cron = %q, want default", p.Defaults.AutoBackup.Cron)
	}
	if len(p.PortPool) == 0 {
		t.Error("PortPool is empty, want default pool")
	}
}

func TestLoadPanelMissingExplicitPathIsError(t *testing.T) {
	_, err := LoadPanel(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("LoadPanel() = nil for a missing explicit path, want error")
	}
}

func TestLoadPanelRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// A typo that would otherwise silently fall back to the default.
	if err := os.WriteFile(path, []byte("instance_dir: /tmp/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPanel(path); err == nil {
		t.Fatal("LoadPanel() = nil for a misspelled key, want error")
	}
}

func TestLoadPanelInvalidFileFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("color_mode: rainbow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPanel(path); err == nil {
		t.Fatal("LoadPanel() = nil for color_mode=rainbow, want error")
	}
}

func TestRedactedDoesNotSharePortPool(t *testing.T) {
	p := NewPanel()
	r := p.Redacted()

	r.PortPool[0] = 1
	if p.PortPool[0] == 1 {
		t.Error("Redacted() shares the PortPool backing array with the original")
	}
}

func TestStringMentionsKeyFields(t *testing.T) {
	p := NewPanel()
	s := p.String()
	for _, want := range []string{"listen", "data_dir", "instances_dir", "port_pool", "color_mode"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() does not mention %q:\n%s", want, s)
		}
	}
}

func TestEnsureDirsCreatesMissingOnly(t *testing.T) {
	base := t.TempDir()
	p := NewPanel()
	p.DataDir = filepath.Join(base, "data")
	p.InstancesDir = filepath.Join(base, "data", "instances")
	p.TemplateDir = "" // optional: must be skipped without error

	created, err := p.EnsureDirs()
	if err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %v, want 2 entries", created)
	}
	for _, dir := range []string{p.DataDir, p.InstancesDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("directory %s was not created (err=%v)", dir, err)
		}
	}

	// Second call must be a no-op.
	created, err = p.EnsureDirs()
	if err != nil {
		t.Fatalf("EnsureDirs (second): %v", err)
	}
	if len(created) != 0 {
		t.Errorf("created = %v on second call, want none", created)
	}
}

func TestFormatPortPool(t *testing.T) {
	tests := []struct {
		pool []int
		want string
	}{
		{nil, "(none)"},
		{[]int{28887}, "28887"},
		{[]int{28887, 28888, 28889}, "28887-28889"},
		{[]int{28887, 28889}, "28887,28889"},
		{[]int{3, 1, 2, 9}, "1-3,9"},
	}
	for _, tc := range tests {
		if got := formatPortPool(tc.pool); got != tc.want {
			t.Errorf("formatPortPool(%v) = %q, want %q", tc.pool, got, tc.want)
		}
	}
}
