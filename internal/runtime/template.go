package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Template validation — the other half of §7's "runtime detection" duty.
//
// The plan (§2.1, appendix A.2, A.3) establishes three facts about a usable
// server template package, and all three are machine-checkable before an
// instance is created from it:
//
//  1. The managed entry point must be present: Survivalcraft.dll, plus its
//     runtimeconfig.json and the 19MB Content.scpak resource pack.
//  2. The runtimeconfig must target net10.0 and must NOT require
//     Microsoft.WindowsDesktop.App. That absence is what makes the server
//     headless-capable on Linux, and it is the single most valuable check in
//     this file: a template that grew a desktop dependency would look perfectly
//     fine on Windows and fail mysteriously here.
//  3. The package currently has no native launcher, so start.sh falls through
//     to `dotnet Survivalcraft.dll`. That is a *warning*, not an error, because
//     the plan explicitly anticipates a future package that ships one.

// Template file names, from §2.1's unpacked layout.
const (
	// FileServerDLL is the game's managed entry point.
	FileServerDLL = "Survivalcraft.dll"
	// FileRuntimeConfig tells the .NET host which framework to load.
	FileRuntimeConfig = "Survivalcraft.runtimeconfig.json"
	// FileContentPack is the game's resource archive (~19MB).
	FileContentPack = "Content.scpak"

	// FileStartScript is the official launcher script.
	FileStartScript = "start.sh"
	// FileNativeLauncher is the self-contained apphost start.sh prefers.
	FileNativeLauncher = "Survivalcraft"
)

// requiredTemplateFiles must exist for a template to be usable at all.
var requiredTemplateFiles = []string{
	FileServerDLL,
	FileRuntimeConfig,
	FileContentPack,
}

// ErrTemplateInvalid is the sentinel every TemplateCheck failure wraps, so
// callers can match it with errors.Is while still showing the detailed,
// actionable message underneath.
var ErrTemplateInvalid = errors.New("runtime: server template is not usable")

// ErrTemplateMissingFile / ErrTemplateRuntimeConfig classify the two failure
// families. They exist so the API layer can pick an HTTP status or a UI hint
// without string matching.
var (
	// ErrTemplateMissingFile reports one or more required files are absent.
	ErrTemplateMissingFile = errors.New("runtime: template file missing")
	// ErrTemplateRuntimeConfig reports runtimeconfig.json is unusable or
	// declares the wrong framework.
	ErrTemplateRuntimeConfig = errors.New("runtime: template runtimeconfig is unusable")
)

// TemplateReport is the non-fatal outcome of a template inspection: the
// warnings that do not stop instance creation.
type TemplateReport struct {
	// Dir is the inspected directory.
	Dir string `json:"dir"`
	// TFM is the target framework from runtimeconfig.json (e.g. "net10.0").
	TFM string `json:"tfm,omitempty"`
	// Frameworks lists the declared framework names.
	Frameworks []string `json:"frameworks,omitempty"`
	// HasNativeLauncher reports whether the self-contained apphost is present.
	HasNativeLauncher bool `json:"hasNativeLauncher"`
	// HasStartScript reports whether the official start.sh is present.
	HasStartScript bool `json:"hasStartScript"`
	// Warnings holds non-fatal problems worth showing the operator.
	Warnings []string `json:"warnings,omitempty"`
}

// runtimeConfig mirrors the parts of a .NET runtimeconfig.json this package
// reads. The file has more keys (configProperties, includedFrameworks, …); only
// these decide whether the server can run here.
type runtimeConfig struct {
	RuntimeOptions struct {
		TFM        string         `json:"tfm"`
		Framework  *frameworkRef  `json:"framework"`
		Frameworks []frameworkRef `json:"frameworks"`
	} `json:"runtimeOptions"`
}

// frameworkRef is one entry of framework/frameworks.
type frameworkRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// TemplateCheck inspects the template directory templateDir and reports whether
// an instance can be created from it.
//
// It returns:
//
//   - a non-nil error wrapping ErrTemplateInvalid when the template cannot be
//     used (a required file is missing, runtimeconfig.json is malformed, the
//     target framework is not net10.0, or it requires a desktop runtime);
//   - a *TemplateReport plus a nil error when the template is usable, with
//     Warnings populated for anything suboptimal (for example: no start.sh or
//     native launcher, so the panel must invoke `dotnet Survivalcraft.dll`
//     directly — the situation described in §2.1).
//
// The directory must exist; a missing templateDir is an error rather than a
// warning because it is almost always a misconfigured `template_dir`.
func TemplateCheck(templateDir string) (*TemplateReport, error) {
	if strings.TrimSpace(templateDir) == "" {
		return nil, fmt.Errorf("%w: template directory is not configured", ErrTemplateInvalid)
	}

	st, err := os.Stat(templateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: template directory %s does not exist "+
				"(check the panel's `template_dir` setting)", ErrTemplateInvalid, templateDir)
		}
		return nil, fmt.Errorf("%w: cannot inspect template directory %s: %v",
			ErrTemplateInvalid, templateDir, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%w: template path %s is not a directory",
			ErrTemplateInvalid, templateDir)
	}

	report := &TemplateReport{Dir: templateDir}

	// 1. Required files.
	var missing []string
	for _, name := range requiredTemplateFiles {
		fi, err := os.Stat(filepath.Join(templateDir, name))
		switch {
		case os.IsNotExist(err):
			missing = append(missing, name)
		case err != nil:
			return nil, fmt.Errorf("%w: cannot stat %s in %s: %v",
				ErrTemplateInvalid, name, templateDir, err)
		case fi.IsDir():
			missing = append(missing, name+" (is a directory, expected a file)")
		case fi.Size() == 0:
			missing = append(missing, name+" (present but empty)")
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %w: %s is missing %s; unpack the official "+
			"server package into it (see plan §2.1)", ErrTemplateInvalid,
			ErrTemplateMissingFile, templateDir, strings.Join(missing, ", "))
	}

	// 2. runtimeconfig.json.
	rc, err := readRuntimeConfig(filepath.Join(templateDir, FileRuntimeConfig))
	if err != nil {
		return nil, err
	}
	report.TFM = rc.RuntimeOptions.TFM

	// 3. Optional launchers. Their absence is the normal case today, so these
	// are reported rather than rejected.
	report.HasStartScript = fileExists(filepath.Join(templateDir, FileStartScript))
	report.HasNativeLauncher = fileExists(filepath.Join(templateDir, FileNativeLauncher))
	if !report.HasStartScript && !report.HasNativeLauncher {
		report.Warnings = append(report.Warnings,
			"template has neither start.sh nor a native Survivalcraft launcher; "+
				"the panel will run the server as `dotnet Survivalcraft.dll`")
	} else if !report.HasNativeLauncher {
		report.Warnings = append(report.Warnings,
			"template has no self-contained Survivalcraft launcher, so start.sh "+
				"falls through to `dotnet Survivalcraft.dll`; a dotnet runtime is required")
	}

	return report, nil
}

// readRuntimeConfig loads and validates Survivalcraft.runtimeconfig.json.
func readRuntimeConfig(path string) (*runtimeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w: cannot read %s: %v",
			ErrTemplateInvalid, ErrTemplateRuntimeConfig, path, err)
	}

	var rc runtimeConfig
	if err := json.Unmarshal(data, &rc); err != nil {
		return nil, fmt.Errorf("%w: %w: %s is not valid JSON: %v",
			ErrTemplateInvalid, ErrTemplateRuntimeConfig, path, err)
	}

	opts := rc.RuntimeOptions

	// Collect every declared framework: the single `framework` object (the
	// common case) and the `frameworks` array (used when a runtimeconfig
	// declares several). Checking both means a desktop dependency hidden in
	// either spelling is caught.
	var declared []string
	var declaredVersions []string
	if opts.Framework != nil && opts.Framework.Name != "" {
		declared = append(declared, opts.Framework.Name)
		declaredVersions = append(declaredVersions, opts.Framework.Version)
	}
	for _, f := range opts.Frameworks {
		if f.Name != "" {
			declared = append(declared, f.Name)
			declaredVersions = append(declaredVersions, f.Version)
		}
	}

	if len(declared) == 0 {
		return nil, fmt.Errorf("%w: %w: %s declares no framework "+
			"(runtimeOptions.framework), so the .NET host has nothing to load",
			ErrTemplateInvalid, ErrTemplateRuntimeConfig, path)
	}

	// The decisive check from §2.1 / A.2: no desktop runtime.
	for _, name := range declared {
		if name == runtimeWindowsDesktop {
			return nil, fmt.Errorf("%w: %w: %s requires %s, which is a "+
				"Windows-only desktop runtime — this template cannot run headless "+
				"on Linux (plan §2.1 requires a package without a desktop "+
				"dependency)", ErrTemplateInvalid, ErrTemplateRuntimeConfig,
				filepath.Base(path), runtimeWindowsDesktop)
		}
	}

	// The managed entry point must be loadable by the shared runtime.
	hasNetCore := false
	for _, name := range declared {
		if name == runtimeNetCoreApp {
			hasNetCore = true
			break
		}
	}
	if !hasNetCore {
		return nil, fmt.Errorf("%w: %w: %s declares %s but not %s, so no "+
			"shared .NET runtime can load it", ErrTemplateInvalid,
			ErrTemplateRuntimeConfig, filepath.Base(path),
			strings.Join(declared, ", "), runtimeNetCoreApp)
	}

	// The target framework must be net<DesiredMajor>.0. This is what ties a
	// template to the runtime Detect looks for: a net8.0 template and a 10.x
	// runtime do not go together, and the mismatch is better reported at
	// template-validation time than as a cryptic host error at first start.
	if err := checkTFM(filepath.Base(path), opts.TFM, declaredVersions); err != nil {
		return nil, err
	}

	return &rc, nil
}

// checkTFM validates runtimeOptions.tfm against DesiredMajor.
func checkTFM(label, tfm string, versions []string) error {
	tfm = strings.TrimSpace(tfm)

	if tfm == "" {
		// A missing tfm is tolerated only when a version was declared that we
		// can interpret; otherwise the host would guess.
		if len(versions) > 0 && versions[0] != "" {
			return nil
		}
		return fmt.Errorf("%w: %w: %s has no runtimeOptions.tfm and no framework "+
			"version, so the target runtime cannot be determined",
			ErrTemplateInvalid, ErrTemplateRuntimeConfig, label)
	}

	major, ok := parseTFMMajor(tfm)
	if !ok {
		return fmt.Errorf("%w: %w: %s targets %q, which is not a recognisable "+
			"framework moniker (expected the form net%d.0)",
			ErrTemplateInvalid, ErrTemplateRuntimeConfig, label, tfm, DesiredMajor)
	}

	if major != DesiredMajor {
		return fmt.Errorf("%w: %w: %s targets %s but the server package must "+
			"target net%d.0 — a net%d.x runtime will not load it (and the panel "+
			"only checks for Microsoft.NETCore.App %d.x)",
			ErrTemplateInvalid, ErrTemplateRuntimeConfig, label, tfm,
			DesiredMajor, major, DesiredMajor)
	}
	return nil
}

// parseTFMMajor extracts the major version from a framework moniker like
// "net10.0", "net10.0-windows" or the legacy "netcoreapp3.1".
func parseTFMMajor(tfm string) (int, bool) {
	s := strings.ToLower(strings.TrimSpace(tfm))
	if s == "" {
		return 0, false
	}

	switch {
	case strings.HasPrefix(s, "netcoreapp"), strings.HasPrefix(s, "netstandard"):
		// Not a target this server can use, but report it as a *version*
		// mismatch rather than an unparsable moniker.
		rest := s[strings.IndexAny(s, "0123456789"):]
		return parseLeadingInt(rest)
	case strings.HasPrefix(s, "net"):
		rest := s[len("net"):]
		// "net10.0-windows" -> "10.0"
		if i := strings.IndexAny(rest, "-"); i >= 0 {
			rest = rest[:i]
		}
		return parseLeadingInt(rest)
	default:
		return 0, false
	}
}

// parseLeadingInt reads the digits at the start of s.
func parseLeadingInt(s string) (int, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n := 0
	for _, c := range s[:i] {
		n = n*10 + int(c-'0')
	}
	return n, n > 0
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
