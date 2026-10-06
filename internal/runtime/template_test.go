package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syntheticTemplate builds a template directory in a temp dir.
//
// runtimeConfig is the raw text of Survivalcraft.runtimeconfig.json; pass ""
// to omit the file entirely. extraFiles maps a name to contents; the required
// files are created with dummy contents unless overridden.
func syntheticTemplate(t *testing.T, runtimeConfig string, extraFiles map[string]string) string {
	t.Helper()

	dir := t.TempDir()

	files := map[string]string{
		FileServerDLL:     "MZ fake managed assembly",
		FileContentPack:   "fake scpak payload",
		FileStartScript:   "#!/bin/sh\nexec dotnet ./Survivalcraft.dll\n",
		FileRuntimeConfig: runtimeConfig,
	}
	for name, content := range extraFiles {
		files[name] = content
	}

	for name, content := range files {
		if content == "" && name == FileRuntimeConfig {
			continue
		}
		if content == "<DELETE>" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// goodRuntimeConfig is the shape the real package ships (§2.1): net10.0 on
// Microsoft.NETCore.App, with no desktop framework.
const goodRuntimeConfig = `{
  "runtimeOptions": {
    "tfm": "net10.0",
    "framework": {
      "name": "Microsoft.NETCore.App",
      "version": "10.0.0"
    },
    "configProperties": {
      "System.Reflection.Metadata.MetadataUpdater.IsSupported": false
    }
  }
}`

func TestTemplateCheckGoodTemplate(t *testing.T) {
	t.Parallel()

	dir := syntheticTemplate(t, goodRuntimeConfig, nil)

	report, err := TemplateCheck(dir)
	if err != nil {
		t.Fatalf("TemplateCheck: %v", err)
	}
	if report.Dir != dir {
		t.Errorf("Dir = %q, want %q", report.Dir, dir)
	}
	if report.TFM != "net10.0" {
		t.Errorf("TFM = %q, want net10.0", report.TFM)
	}
	if !report.HasStartScript {
		t.Error("HasStartScript = false, want true")
	}
	if report.HasNativeLauncher {
		t.Error("HasNativeLauncher = true for a template without the apphost")
	}
	if len(report.Warnings) == 0 {
		t.Error("expected a warning about the missing native launcher (§2.1)")
	}
}

func TestTemplateCheckNativeLauncherPresent(t *testing.T) {
	t.Parallel()

	dir := syntheticTemplate(t, goodRuntimeConfig, map[string]string{
		FileNativeLauncher: "#!/bin/sh ELF-ish apphost",
	})

	report, err := TemplateCheck(dir)
	if err != nil {
		t.Fatalf("TemplateCheck: %v", err)
	}
	if !report.HasNativeLauncher {
		t.Error("HasNativeLauncher = false with a Survivalcraft apphost present")
	}
	if len(report.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none when a native launcher exists", report.Warnings)
	}
}

// TestTemplateCheckRejectsWindowsDesktop is the decisive check from §2.1 and
// appendix A.2: a template that requires the Windows desktop runtime cannot run
// headless on Linux, and the panel must say so rather than fail at first start.
func TestTemplateCheckRejectsWindowsDesktop(t *testing.T) {
	t.Parallel()

	cfg := `{
  "runtimeOptions": {
    "tfm": "net10.0",
    "framework": {
      "name": "Microsoft.WindowsDesktop.App",
      "version": "10.0.0"
    }
  }
}`
	dir := syntheticTemplate(t, cfg, nil)

	_, err := TemplateCheck(dir)
	if err == nil {
		t.Fatal("TemplateCheck must reject a WindowsDesktop-dependent template")
	}
	if !errors.Is(err, ErrTemplateInvalid) {
		t.Errorf("error = %v, want ErrTemplateInvalid", err)
	}
	if !errors.Is(err, ErrTemplateRuntimeConfig) {
		t.Errorf("error = %v, want ErrTemplateRuntimeConfig", err)
	}
	if !strings.Contains(err.Error(), "WindowsDesktop") {
		t.Errorf("error should name the offending framework, got %v", err)
	}
	if !strings.Contains(err.Error(), "headless") {
		t.Errorf("error should explain the consequence, got %v", err)
	}
}

// TestTemplateCheckRejectsWindowsDesktopInFrameworksArray covers the other
// spelling of a framework dependency: runtimeOptions.frameworks. A pack that
// listed the desktop runtime only there would otherwise slip through.
func TestTemplateCheckRejectsWindowsDesktopInFrameworksArray(t *testing.T) {
	t.Parallel()

	cfg := `{
  "runtimeOptions": {
    "tfm": "net10.0",
    "frameworks": [
      { "name": "Microsoft.NETCore.App", "version": "10.0.0" },
      { "name": "Microsoft.WindowsDesktop.App", "version": "10.0.0" }
    ]
  }
}`
	dir := syntheticTemplate(t, cfg, nil)

	_, err := TemplateCheck(dir)
	if err == nil {
		t.Fatal("TemplateCheck must reject a desktop dependency declared in the frameworks array")
	}
	if !errors.Is(err, ErrTemplateRuntimeConfig) {
		t.Errorf("error = %v, want ErrTemplateRuntimeConfig", err)
	}
}

func TestTemplateCheckTFM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tfm     string
		wantErr bool
	}{
		{"net10.0", "net10.0", false},
		{"net10.0-windows", "net10.0-windows", false},
		{"net10.0 lowercase NET", "NET10.0", false},
		{"net8.0 is too old", "net8.0", true},
		{"net9.0 is too old", "net9.0", true},
		{"net11.0 is too new", "net11.0", true},
		{"netcoreapp3.1 is too old", "netcoreapp3.1", true},
		{"garbage moniker", "banana", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := `{"runtimeOptions":{"tfm":"` + tc.tfm + `","framework":{"name":"Microsoft.NETCore.App","version":"10.0.0"}}}`
			dir := syntheticTemplate(t, cfg, nil)

			report, err := TemplateCheck(dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("TemplateCheck(tfm=%q) = %+v, want an error", tc.tfm, report)
				}
				if !errors.Is(err, ErrTemplateInvalid) {
					t.Errorf("error = %v, want ErrTemplateInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("TemplateCheck(tfm=%q): %v", tc.tfm, err)
			}
			if report.TFM != tc.tfm {
				t.Errorf("TFM = %q, want %q", report.TFM, tc.tfm)
			}
		})
	}
}

func TestTemplateCheckMissingFiles(t *testing.T) {
	t.Parallel()

	for _, missing := range []string{FileServerDLL, FileRuntimeConfig, FileContentPack} {
		t.Run(missing, func(t *testing.T) {
			t.Parallel()

			dir := syntheticTemplate(t, goodRuntimeConfig, nil)
			if err := os.Remove(filepath.Join(dir, missing)); err != nil {
				t.Fatalf("remove %s: %v", missing, err)
			}

			_, err := TemplateCheck(dir)
			if err == nil {
				t.Fatalf("TemplateCheck must fail without %s", missing)
			}
			if !errors.Is(err, ErrTemplateMissingFile) {
				t.Errorf("error = %v, want ErrTemplateMissingFile", err)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error should name %q, got %v", missing, err)
			}
		})
	}
}

func TestTemplateCheckEmptyRequiredFile(t *testing.T) {
	t.Parallel()

	dir := syntheticTemplate(t, goodRuntimeConfig, nil)
	if err := os.WriteFile(filepath.Join(dir, FileContentPack), nil, 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	_, err := TemplateCheck(dir)
	if err == nil {
		t.Fatal("an empty Content.scpak must be reported")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want it to mention the file is empty", err)
	}
}

func TestTemplateCheckRequiredPathIsDirectory(t *testing.T) {
	t.Parallel()

	dir := syntheticTemplate(t, goodRuntimeConfig, nil)
	pack := filepath.Join(dir, FileContentPack)
	if err := os.Remove(pack); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Mkdir(pack, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := TemplateCheck(dir)
	if err == nil {
		t.Fatal("a directory where a required file belongs must be reported")
	}
	if !errors.Is(err, ErrTemplateMissingFile) {
		t.Errorf("error = %v, want ErrTemplateMissingFile", err)
	}
}

func TestTemplateCheckMalformedRuntimeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{"not json", "{ this is not json", "not valid JSON"},
		{"no framework", `{"runtimeOptions":{"tfm":"net10.0"}}`, "declares no framework"},
		{"aspnet only", `{"runtimeOptions":{"tfm":"net10.0","framework":{"name":"Microsoft.AspNetCore.App","version":"10.0.0"}}}`, "no shared .NET runtime"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := syntheticTemplate(t, tc.cfg, nil)

			_, err := TemplateCheck(dir)
			if err == nil {
				t.Fatal("TemplateCheck must reject this runtimeconfig")
			}
			if !errors.Is(err, ErrTemplateRuntimeConfig) {
				t.Errorf("error = %v, want ErrTemplateRuntimeConfig", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestTemplateCheckNoFrameworkVersion covers a runtimeconfig with a framework
// name but no tfm and no version: nothing tells the host what to load.
func TestTemplateCheckNoFrameworkVersion(t *testing.T) {
	t.Parallel()

	cfg := `{"runtimeOptions":{"framework":{"name":"Microsoft.NETCore.App"}}}`
	dir := syntheticTemplate(t, cfg, nil)

	_, err := TemplateCheck(dir)
	if err == nil {
		t.Fatal("TemplateCheck must reject a runtimeconfig with no tfm and no version")
	}
	if !errors.Is(err, ErrTemplateRuntimeConfig) {
		t.Errorf("error = %v, want ErrTemplateRuntimeConfig", err)
	}
}

// TestTemplateCheckVersionOnlyAccepted documents the deliberate tolerance: a
// runtimeconfig with a framework version but no tfm is accepted, because the
// host can resolve it and rejecting it would break third-party repacks.
func TestTemplateCheckVersionOnlyAccepted(t *testing.T) {
	t.Parallel()

	cfg := `{"runtimeOptions":{"framework":{"name":"Microsoft.NETCore.App","version":"10.0.0"}}}`
	dir := syntheticTemplate(t, cfg, nil)

	if _, err := TemplateCheck(dir); err != nil {
		t.Fatalf("TemplateCheck: %v", err)
	}
}

func TestTemplateCheckBadDirectory(t *testing.T) {
	t.Parallel()

	t.Run("empty string", func(t *testing.T) {
		t.Parallel()
		if _, err := TemplateCheck("   "); err == nil {
			t.Fatal("an unset template directory must be an error")
		}
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		_, err := TemplateCheck(filepath.Join(t.TempDir(), "nope"))
		if err == nil {
			t.Fatal("a missing template directory must be an error")
		}
		if !strings.Contains(err.Error(), "template_dir") {
			t.Errorf("error should point at the setting to fix, got %v", err)
		}
	})

	t.Run("is a file", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "afile")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := TemplateCheck(p)
		if err == nil {
			t.Fatal("a file where the template directory belongs must be an error")
		}
		if !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("error = %v, want it to say the path is not a directory", err)
		}
	})
}

func TestTemplateCheckNoLaunchers(t *testing.T) {
	t.Parallel()

	dir := syntheticTemplate(t, goodRuntimeConfig, nil)
	if err := os.Remove(filepath.Join(dir, FileStartScript)); err != nil {
		t.Fatalf("remove start.sh: %v", err)
	}

	report, err := TemplateCheck(dir)
	if err != nil {
		t.Fatalf("TemplateCheck: %v", err)
	}
	if report.HasStartScript || report.HasNativeLauncher {
		t.Error("both launcher flags should be false")
	}
	if len(report.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one", report.Warnings)
	}
	if !strings.Contains(report.Warnings[0], "dotnet Survivalcraft.dll") {
		t.Errorf("warning should say how the panel will launch the server, got %q", report.Warnings[0])
	}
}

// TestTemplateReportJSON pins the wire shape the API layer will expose.
func TestTemplateReportJSON(t *testing.T) {
	t.Parallel()

	dir := syntheticTemplate(t, goodRuntimeConfig, nil)
	report, err := TemplateCheck(dir)
	if err != nil {
		t.Fatalf("TemplateCheck: %v", err)
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{`"dir"`, `"tfm"`, `"hasNativeLauncher"`, `"hasStartScript"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("JSON %s is missing %s", data, key)
		}
	}
}

func TestParseTFMMajor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"net10.0", 10, true},
		{"net10.0-windows", 10, true},
		{"net8.0", 8, true},
		{"net48", 48, true},
		{"netcoreapp3.1", 3, true},
		{"netstandard2.1", 2, true},
		{"", 0, false},
		{"net", 0, false},
		{"banana", 0, false},
	}
	for _, tc := range tests {
		got, ok := parseTFMMajor(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseTFMMajor(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
