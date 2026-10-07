// Package config holds the panel's own configuration types.
//
// Two very different kinds of configuration live under this package:
//
//   - The panel's own settings file (this file): config.yaml, read once at
//     start-up. It describes the host-facing knobs — listen address, data
//     directories, dotnet path, port pool, backup defaults.
//   - The *game server's* configuration files (ServerSetting.json,
//     Settings.xml, ...): read and written per instance by the config
//     service. Those live in sibling files and are a different concern.
//
// Keeping Panel in its own file means the panel bootstrap depends on nothing
// but gopkg.in/yaml.v3.
package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Defaults for [LoadPanel]. They mirror configs/config.example.yaml.
const (
	DefaultListen       = "0.0.0.0:7000"
	DefaultDataDir      = "./data"
	DefaultTemplateDir  = "" // empty when unset
	DefaultDotnetPath   = "dotnet"
	DefaultTerm         = "xterm-256color"
	DefaultColorMode    = "enhanced"
	DefaultStopTimeout  = 30
	DefaultBackupCron   = "0 4 * * *"
	DefaultBackupKeep   = 7
	DefaultPortPoolFrom = 28887
	DefaultPortPoolTo   = 28900
)

// Color modes (plan §9.1). "enhanced" runs the server under a PTY so it emits
// ANSI colour; "basic" degrades to uncoloured output.
const (
	ColorModeEnhanced = "enhanced"
	ColorModeBasic    = "basic"
)

// AutoBackup configures the scheduled world backup job.
type AutoBackup struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Cron    string `yaml:"cron" json:"cron"`
	Keep    int    `yaml:"keep" json:"keep"`
}

// Defaults holds per-instance default values applied when a new instance is
// created without explicit overrides.
type Defaults struct {
	StopTimeoutSec int        `yaml:"stop_timeout_sec" json:"stop_timeout_sec"`
	AutoBackup     AutoBackup `yaml:"auto_backup" json:"auto_backup"`
}

// Panel is the panel's own configuration, loaded from config.yaml.
//
// Field order and YAML tags follow plan §9.1 verbatim so an operator can paste
// the documented example straight into configs/config.example.yaml.
type Panel struct {
	Listen       string   `yaml:"listen" json:"listen"`
	DataDir      string   `yaml:"data_dir" json:"data_dir"`
	InstancesDir string   `yaml:"instances_dir" json:"instances_dir"`
	TemplateDir  string   `yaml:"template_dir" json:"template_dir"`
	DotnetPath   string   `yaml:"dotnet_path" json:"dotnet_path"`
	PortPool     []int    `yaml:"port_pool" json:"port_pool"`
	Term         string   `yaml:"term" json:"term"`
	ColorMode    string   `yaml:"color_mode" json:"color_mode"`
	Defaults     Defaults `yaml:"defaults" json:"defaults"`

	// sourcePath records where this configuration was loaded from, for logging
	// and diagnostics. It is never serialised.
	sourcePath string `yaml:"-" json:"-"`
}

// SourcePath reports the file the configuration was loaded from, or "" when the
// built-in defaults were used.
func (p *Panel) SourcePath() string { return p.sourcePath }

// NewPanel returns a Panel populated with the built-in defaults, with
// [Panel.ApplyDefaults] already applied.
func NewPanel() *Panel {
	p := &Panel{}
	p.ApplyDefaults()
	return p
}

// LoadPanel reads the panel configuration from path.
//
// When path is empty it searches for "config.yaml" in the current directory and
// falls back to the built-in defaults when no file is found. A missing file at
// an *explicit* path is an error — silently ignoring a typo'd --config flag
// would start the panel somewhere unexpected.
//
// Defaults are applied before validation, so a partial file (for example only
// `listen:`) is valid.
func LoadPanel(path string) (*Panel, error) {
	p := &Panel{}

	switch {
	case path == "":
		const candidate = "config.yaml"
		f, err := os.Open(candidate)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("config: open %s: %w", candidate, err)
			}
			p.ApplyDefaults()
			return p, nil
		}
		defer f.Close()
		if err := decodeYAML(f, p); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", candidate, err)
		}
		p.sourcePath = candidate

	default:
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("config: resolve %s: %w", path, err)
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("config: open %s: %w", abs, err)
		}
		defer f.Close()
		if err := decodeYAML(f, p); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", abs, err)
		}
		p.sourcePath = abs
	}

	p.ApplyDefaults()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	// Resolve path fields against the config file's directory (or the CWD for
	// the defaults) so a relative data_dir behaves predictably regardless of
	// how the binary was invoked. Done after Validate so the raw operator input
	// is what gets reported on error.
	if err := p.resolvePaths(); err != nil {
		return nil, err
	}
	return p, nil
}

// decodeYAML decodes YAML into p, rejecting unknown keys.
//
// Strict decoding is deliberate: a typo such as `instance_dir` (singular) would
// otherwise be silently ignored and the panel would quietly use the default.
func decodeYAML(r io.Reader, p *Panel) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil {
		if errors.Is(err, io.EOF) {
			// Completely empty file: keep the defaults.
			return nil
		}
		return err
	}
	return nil
}

// ApplyDefaults fills in every zero-valued field with its documented default.
// It is idempotent and safe to call on a freshly constructed Panel.
func (p *Panel) ApplyDefaults() {
	if p.Listen == "" {
		p.Listen = DefaultListen
	}
	if p.DataDir == "" {
		p.DataDir = DefaultDataDir
	}
	if p.InstancesDir == "" {
		p.InstancesDir = filepath.Join(p.DataDir, "instances")
	}
	if p.DotnetPath == "" {
		p.DotnetPath = DefaultDotnetPath
	}
	if len(p.PortPool) == 0 {
		p.PortPool = defaultPortPool()
	}
	if p.Term == "" {
		p.Term = DefaultTerm
	}
	if p.ColorMode == "" {
		p.ColorMode = DefaultColorMode
	}
	if p.Defaults.StopTimeoutSec == 0 {
		p.Defaults.StopTimeoutSec = DefaultStopTimeout
	}
	if p.Defaults.AutoBackup.Cron == "" {
		p.Defaults.AutoBackup.Cron = DefaultBackupCron
	}
	if p.Defaults.AutoBackup.Keep == 0 {
		p.Defaults.AutoBackup.Keep = DefaultBackupKeep
	}
}

// defaultPortPool returns [28887, 28888, ..., 28900] (plan §9.1 uses an
// inclusive range written as `[28887, 28900]`; we expand it because the rest of
// the panel treats port_pool as a concrete list of usable ports).
func defaultPortPool() []int {
	pool := make([]int, 0, DefaultPortPoolTo-DefaultPortPoolFrom+1)
	for port := DefaultPortPoolFrom; port <= DefaultPortPoolTo; port++ {
		pool = append(pool, port)
	}
	return pool
}

// resolvePaths turns the path-ish fields into absolute paths.
func (p *Panel) resolvePaths() error {
	base := "."
	if p.sourcePath != "" {
		base = filepath.Dir(p.sourcePath)
	}
	for _, field := range []struct {
		name string
		ptr  *string
	}{
		{"data_dir", &p.DataDir},
		{"instances_dir", &p.InstancesDir},
	} {
		abs, err := absFrom(base, *field.ptr)
		if err != nil {
			return fmt.Errorf("config: %s: %w", field.name, err)
		}
		*field.ptr = abs
	}
	// template_dir is optional; only resolve it when set.
	if p.TemplateDir != "" {
		abs, err := absFrom(base, p.TemplateDir)
		if err != nil {
			return fmt.Errorf("config: template_dir: %w", err)
		}
		p.TemplateDir = abs
	}
	// dotnet_path may legitimately be a bare command name resolved via PATH
	// ("dotnet"). Only resolve it when it looks like a path.
	if strings.ContainsRune(p.DotnetPath, os.PathSeparator) {
		abs, err := absFrom(base, p.DotnetPath)
		if err != nil {
			return fmt.Errorf("config: dotnet_path: %w", err)
		}
		p.DotnetPath = abs
	}
	return nil
}

func absFrom(base, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Abs(filepath.Clean(path))
}

// Validate checks the configuration for values that cannot work.
//
// It intentionally does NOT touch the filesystem: a panel configured for a
// data_dir that does not exist yet must still validate, because the bootstrap
// creates it.
func (p *Panel) Validate() error {
	var errs []error

	if err := validateListen(p.Listen); err != nil {
		errs = append(errs, fmt.Errorf("listen: %w", err))
	}

	if strings.TrimSpace(p.DataDir) == "" {
		errs = append(errs, errors.New("data_dir: must not be empty"))
	}
	if strings.TrimSpace(p.InstancesDir) == "" {
		errs = append(errs, errors.New("instances_dir: must not be empty"))
	}
	// A dotnet_path that is just whitespace would produce a confusing
	// exec error much later, at instance start time.
	if strings.TrimSpace(p.DotnetPath) == "" {
		errs = append(errs, errors.New("dotnet_path: must not be empty"))
	}

	if err := validatePortPool(p.PortPool); err != nil {
		errs = append(errs, err)
	}

	switch p.ColorMode {
	case ColorModeEnhanced, ColorModeBasic:
	default:
		errs = append(errs, fmt.Errorf("color_mode: %q is not one of %q or %q",
			p.ColorMode, ColorModeEnhanced, ColorModeBasic))
	}

	if strings.TrimSpace(p.Term) == "" {
		errs = append(errs, errors.New("term: must not be empty"))
	}

	if p.Defaults.StopTimeoutSec < 0 {
		errs = append(errs, fmt.Errorf("defaults.stop_timeout_sec: %d must not be negative",
			p.Defaults.StopTimeoutSec))
	}

	ab := p.Defaults.AutoBackup
	if ab.Enabled {
		if err := validateCron(ab.Cron); err != nil {
			errs = append(errs, fmt.Errorf("defaults.auto_backup.cron: %w", err))
		}
		if ab.Keep <= 0 {
			errs = append(errs, fmt.Errorf("defaults.auto_backup.keep: %d must be > 0 when auto backup is enabled",
				ab.Keep))
		}
	} else if ab.Keep < 0 {
		errs = append(errs, fmt.Errorf("defaults.auto_backup.keep: %d must not be negative", ab.Keep))
	}

	return errors.Join(errs...)
}

// validateListen accepts "host:port", ":port" and "port".
func validateListen(listen string) error {
	if strings.TrimSpace(listen) == "" {
		return errors.New("must not be empty")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		// Allow the bare ":8080" / "8080" shorthand.
		if n, convErr := strconv.Atoi(strings.TrimPrefix(listen, ":")); convErr == nil {
			return validatePort(n)
		}
		var addrErr *net.AddrError
		if errors.As(err, &addrErr) {
			return fmt.Errorf("missing port in address %q", listen)
		}
		return err
	}
	if host != "" {
		if ip := net.ParseIP(host); ip == nil {
			// A hostname is acceptable, but it must at least be syntactically sane.
			if strings.ContainsAny(host, " \t/\\") {
				return fmt.Errorf("invalid host %q", host)
			}
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("invalid port %q", port)
	}
	return validatePort(n)
}

func validatePort(n int) error {
	if n < 1 || n > 65535 {
		return fmt.Errorf("port %d out of range 1..65535", n)
	}
	return nil
}

func validatePortPool(pool []int) error {
	if len(pool) == 0 {
		return errors.New("port_pool: must not be empty")
	}
	seen := make(map[int]struct{}, len(pool))
	for _, port := range pool {
		if err := validatePort(port); err != nil {
			return fmt.Errorf("port_pool: %w", err)
		}
		if _, dup := seen[port]; dup {
			return fmt.Errorf("port_pool: duplicate port %d", port)
		}
		seen[port] = struct{}{}
	}
	return nil
}

// validateCron performs a light structural check on a 5-field cron expression.
// Full schedule parsing belongs to the scheduler package; this catches the
// common operator mistakes (wrong field count, stray text) at start-up.
func validateCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("%q must have exactly 5 fields (minute hour dom month dow), got %d",
			expr, len(fields))
	}
	lo := []int{0, 0, 1, 1, 0}
	hi := []int{59, 23, 31, 12, 6}
	for i, f := range fields {
		if err := validateCronField(f, lo[i], hi[i]); err != nil {
			return fmt.Errorf("field %d (%q): %w", i+1, f, err)
		}
	}
	return nil
}

func validateCronField(field string, min, max int) error {
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return errors.New("empty list element")
		}
		// Strip a step suffix: "*/5", "1-10/2", "*/2".
		if base, step, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(step)
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid step %q", step)
			}
			part = base
		}
		if part == "*" {
			continue
		}
		// Range "a-b" or single value "a".
		from, to, isRange := strings.Cut(part, "-")
		start, err := strconv.Atoi(from)
		if err != nil {
			return fmt.Errorf("invalid value %q", from)
		}
		end := start
		if isRange {
			end, err = strconv.Atoi(to)
			if err != nil {
				return fmt.Errorf("invalid range end %q", to)
			}
			if end < start {
				return fmt.Errorf("range %q is inverted", part)
			}
		}
		if start < min || start > max || end < min || end > max {
			return fmt.Errorf("value %q out of range %d..%d", part, min, max)
		}
	}
	return nil
}

// EnsureDirs creates the data, instances and template directories, returning
// the ones it created. Existing directories are left untouched.
func (p *Panel) EnsureDirs() (created []string, err error) {
	for _, dir := range []string{p.DataDir, p.InstancesDir, p.TemplateDir} {
		if dir == "" {
			continue
		}
		if _, statErr := os.Stat(dir); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return created, fmt.Errorf("config: stat %s: %w", dir, statErr)
		}
		if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
			return created, fmt.Errorf("config: create %s: %w", dir, mkErr)
		}
		created = append(created, dir)
	}
	sort.Strings(created)
	return created, nil
}

// Redacted returns a copy safe to write to logs.
//
// The panel configuration holds no secrets today (credentials live in SQLite),
// so nothing is masked; the method exists so that any secret added later has an
// obvious, single place to be redacted, and so log call sites already use the
// safe accessor.
func (p *Panel) Redacted() Panel {
	c := *p
	c.PortPool = append([]int(nil), p.PortPool...)
	return c
}

// String renders the effective configuration as a multi-line summary.
//
// It is built by hand rather than with %+v so the output is stable and readable
// in slog JSON logs, and so the source path is always visible.
func (p *Panel) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "config (source=%s):\n", orNone(p.sourcePath))
	fmt.Fprintf(&b, "  listen:        %s\n", p.Listen)
	fmt.Fprintf(&b, "  data_dir:      %s\n", p.DataDir)
	fmt.Fprintf(&b, "  instances_dir: %s\n", p.InstancesDir)
	fmt.Fprintf(&b, "  template_dir:  %s\n", orNone(p.TemplateDir))
	fmt.Fprintf(&b, "  dotnet_path:   %s\n", p.DotnetPath)
	fmt.Fprintf(&b, "  port_pool:     %s (%d ports)\n", formatPortPool(p.PortPool), len(p.PortPool))
	fmt.Fprintf(&b, "  term:          %s\n", p.Term)
	fmt.Fprintf(&b, "  color_mode:    %s\n", p.ColorMode)
	fmt.Fprintf(&b, "  defaults.stop_timeout_sec: %d\n", p.Defaults.StopTimeoutSec)
	fmt.Fprintf(&b, "  defaults.auto_backup: enabled=%t cron=%q keep=%d\n",
		p.Defaults.AutoBackup.Enabled, p.Defaults.AutoBackup.Cron, p.Defaults.AutoBackup.Keep)
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// formatPortPool compresses a contiguous run into "a-b" and otherwise lists the
// ports, so a 14-port default pool stays on one line.
func formatPortPool(pool []int) string {
	if len(pool) == 0 {
		return "(none)"
	}
	sorted := append([]int(nil), pool...)
	sort.Ints(sorted)

	var parts []string
	start, prev := sorted[0], sorted[0]
	flush := func() {
		if start == prev {
			parts = append(parts, strconv.Itoa(start))
			return
		}
		parts = append(parts, strconv.Itoa(start)+"-"+strconv.Itoa(prev))
	}
	for _, port := range sorted[1:] {
		if port == prev+1 {
			prev = port
			continue
		}
		flush()
		start, prev = port, port
	}
	flush()
	return strings.Join(parts, ",")
}
