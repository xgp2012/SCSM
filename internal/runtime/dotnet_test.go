package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readTestdata loads a captured `dotnet --list-runtimes` sample.
func readTestdata(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(data)
}

// isolateFromHostDotnet makes FindDotnet deterministic on machines that already
// have .NET installed in one of the well-known locations.
//
// Setting PATH and DOTNET_ROOT is not enough on its own: FindDotnet also probes
// candidatePaths(), and the GitHub Actions ubuntu-latest image ships
// /usr/share/dotnet/dotnet. Without this, tests asserting "not found" pass on a
// bare host and fail in CI. Tests that need the real candidate list must not
// call it.
func isolateFromHostDotnet(t *testing.T) {
	t.Helper()
	prev := candidatePathsOverride
	candidatePathsOverride = []string{}
	t.Cleanup(func() { candidatePathsOverride = prev })
}

func TestParseListRuntimesRealSamples(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file     string
		wantLen  int
		wantName string
		wantVer  string
		wantPath string
	}{
		{
			file:     "list-runtimes-real-ubuntu-2404.txt",
			wantLen:  4,
			wantName: "Microsoft.AspNetCore.App",
			wantVer:  "8.0.10",
			wantPath: "/usr/share/dotnet/shared/Microsoft.AspNetCore.App/8.0.10",
		},
		{
			file:     "list-runtimes-net10-ga.txt",
			wantLen:  3,
			wantName: "Microsoft.AspNetCore.App",
			wantVer:  "10.0.12",
			wantPath: "/usr/share/dotnet/shared/Microsoft.AspNetCore.App/10.0.12",
		},
		{
			file:     "list-runtimes-net10-preview.txt",
			wantLen:  3,
			wantName: "Microsoft.AspNetCore.App",
			wantVer:  "10.0.0-rc.1.25451.1",
			wantPath: "/root/.dotnet/shared/Microsoft.AspNetCore.App/10.0.0-rc.1.25451.1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			got := ParseListRuntimes(readTestdata(t, tc.file))
			if len(got) != tc.wantLen {
				t.Fatalf("parsed %d runtimes, want %d: %+v", len(got), tc.wantLen, got)
			}
			first := got[0]
			if first.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", first.Name, tc.wantName)
			}
			if first.Version != tc.wantVer {
				t.Errorf("Version = %q, want %q", first.Version, tc.wantVer)
			}
			if first.Path != tc.wantPath {
				t.Errorf("Path = %q, want %q", first.Path, tc.wantPath)
			}
		})
	}
}

// TestParseListRuntimesToleratesNoise pins the parser's behaviour on
// `dotnet --info`-style banners and blank lines: a stray banner must not hide
// the runtimes, and must not itself become a fake runtime.
func TestParseListRuntimesToleratesNoise(t *testing.T) {
	t.Parallel()

	got := ParseListRuntimes(readTestdata(t, "list-runtimes-with-banner.txt"))
	if len(got) != 2 {
		t.Fatalf("parsed %d runtimes, want 2: %+v", len(got), got)
	}
	for _, r := range got {
		if strings.Contains(r.Name, ":") || strings.Contains(r.Name, " ") {
			t.Errorf("banner text leaked into a runtime name: %+v", r)
		}
		if r.Version != "10.0.12" {
			t.Errorf("Version = %q, want 10.0.12", r.Version)
		}
	}
}

func TestParseListRuntimesEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"blank lines", "\n\n   \n", 0},
		{"one field only", "Microsoft.NETCore.App\n", 0},
		{"non numeric version", "Microsoft.NETCore.App latest [/x]\n", 0},
		{"undotted name", "netcore 10.0.0 [/x]\n", 0},
		{"no brackets", "Microsoft.NETCore.App 10.0.0\n", 1},
		{"unterminated bracket", "Microsoft.NETCore.App 10.0.0 [/x\n", 1},
		{"leading whitespace", "   Microsoft.NETCore.App 10.0.0 [/x]  \n", 1},
		{"crlf", "Microsoft.NETCore.App 10.0.0 [/x]\r\n", 1},
		{"path with spaces", "Microsoft.NETCore.App 10.0.0 [/opt/my dotnet/shared/x]\n", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseListRuntimes(tc.in)
			if len(got) != tc.want {
				t.Fatalf("parsed %d runtimes, want %d: %+v", len(got), tc.want, got)
			}
		})
	}
}

// TestParseListRuntimesPathWithSpaces guards the re-join logic: a path
// containing a space would be split by strings.Fields, so the parser must
// rebuild it from the brackets rather than take the third field.
func TestParseListRuntimesPathWithSpaces(t *testing.T) {
	t.Parallel()

	got := ParseListRuntimes("Microsoft.NETCore.App 10.0.0 [/opt/my dotnet/shared/Microsoft.NETCore.App/10.0.0]\n")
	if len(got) != 1 {
		t.Fatalf("parsed %d runtimes, want 1", len(got))
	}
	if want := "/opt/my dotnet/shared/Microsoft.NETCore.App/10.0.0"; got[0].Path != want {
		t.Errorf("Path = %q, want %q", got[0].Path, want)
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want int
	}{
		// Numeric, not lexical: this is the bug the helper exists to prevent.
		{"10.0.12", "10.0.9", 1},
		{"10.0.9", "10.0.12", -1},
		{"9.0.0", "10.0.0", -1},
		{"10.0.0", "9.0.0", 1},
		{"10.0.0", "10.0.0", 0},
		{"10", "10.0.0", 0},
		{"10.0", "10.0.0", 0},
		{"v10.0.0", "10.0.0", 0},
		{"10.0.1", "10.0.0", 1},
		{"10.1.0", "10.0.99", 1},
		{"11.0.0", "10.99.99", 1},

		// Preview suffixes (SemVer rule 11): a preview sorts below its release.
		{"10.0.0-rc.1", "10.0.0", -1},
		{"10.0.0", "10.0.0-rc.1", 1},
		{"10.0.0-rc.1", "10.0.0-rc.1", 0},
		{"10.0.0-preview.7", "10.0.0-rc.1", -1},
		{"10.0.0-rc.1", "10.0.0-preview.7", 1},
		{"10.0.0-rc.1.25451.1", "10.0.0", -1},

		// Build metadata is ignored.
		{"10.0.0+abc", "10.0.0+def", 0},

		// Garbage degrades to zero rather than panicking: the input comes from
		// another process.
		{"", "", 0},
		{"", "1.0.0", -1},
		{"abc", "0.0.0", 0},
	}

	for _, tc := range tests {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestMajorVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"10.0.12", 10, true},
		{"10.0.0-rc.1", 10, true},
		{"8.0.10", 8, true},
		{"v9.0.0", 9, true},
		{"", 0, false},
	}
	for _, tc := range tests {
		got, ok := MajorVersion(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("MajorVersion(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBestNetCoreApp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		file  string
		want  string
		found bool
	}{
		{"ubuntu 24.04 (8 and 9)", "list-runtimes-real-ubuntu-2404.txt", "9.0.0", true},
		{"net10 ga picks highest patch", "list-runtimes-net10-ga.txt", "10.0.12", true},
		{"net10 preview", "list-runtimes-net10-preview.txt", "10.0.0-rc.1.25451.1", true},
		{"too old", "list-runtimes-too-old.txt", "9.0.0", true},
		{"desktop only", "list-runtimes-desktop-only.txt", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			best, ok := BestNetCoreApp(ParseListRuntimes(readTestdata(t, tc.file)))
			if ok != tc.found {
				t.Fatalf("found = %v, want %v", ok, tc.found)
			}
			if ok && best.Version != tc.want {
				t.Errorf("best version = %q, want %q", best.Version, tc.want)
			}
		})
	}
}

// TestBestNetCoreAppIgnoresOtherProducts is §2.1's requirement stated as a
// test: only Microsoft.NETCore.App counts.
func TestBestNetCoreAppIgnoresOtherProducts(t *testing.T) {
	t.Parallel()

	rts := ParseListRuntimes(readTestdata(t, "list-runtimes-desktop-only.txt"))
	if _, ok := BestNetCoreApp(rts); ok {
		t.Fatal("WindowsDesktop.App must not satisfy the NETCore.App requirement")
	}
	if !HasWindowsDesktop(rts) {
		t.Error("HasWindowsDesktop should report true for the desktop-only sample")
	}

	aspnet := []Runtime{{Name: runtimeAspNetCore, Version: "10.0.12"}}
	if _, ok := BestNetCoreApp(aspnet); ok {
		t.Fatal("AspNetCore.App must not satisfy the NETCore.App requirement")
	}
}

// TestDetectMissingDotnet is the live path in this environment: dotnet is not
// installed, and the panel must still be able to boot and report that.
func TestDetectMissingDotnet(t *testing.T) {
	t.Parallel()

	info, err := Detect(context.Background(), filepath.Join(t.TempDir(), "no-such-dotnet"))
	if err != nil {
		t.Fatalf("Detect must not error when dotnet is absent, got %v", err)
	}
	if info == nil {
		t.Fatal("Detect returned a nil Info")
	}
	if info.Available {
		t.Error("Available = true for a nonexistent dotnet")
	}
	if info.Error == "" {
		t.Error("Error must explain why the runtime is unavailable")
	}
	if info.HasNetCoreApp10 {
		t.Error("HasNetCoreApp10 = true for a nonexistent dotnet")
	}
	if !strings.Contains(info.Error, "no-such-dotnet") {
		t.Errorf("Error should name the probed path, got %q", info.Error)
	}
	if len(info.Runtimes) != 0 {
		t.Errorf("Runtimes = %+v, want empty", info.Runtimes)
	}
}

// TestDetectNoDotnetAnywhere exercises the empty-path branch, which falls back
// to FindDotnet(). It is only meaningful when the test host genuinely has no
// dotnet on PATH; if one is present the test asserts the opposite contract
// (FindDotnet succeeds) so it is meaningful either way.
func TestDetectNoDotnetAnywhere(t *testing.T) {
	t.Parallel()

	found, findErr := FindDotnet()

	info, err := Detect(context.Background(), "")
	if err != nil {
		t.Fatalf("Detect must not error, got %v", err)
	}
	if info == nil {
		t.Fatal("Detect returned a nil Info")
	}

	if findErr != nil {
		if !errors.Is(findErr, ErrDotnetNotFound) {
			t.Fatalf("FindDotnet error = %v, want ErrDotnetNotFound", findErr)
		}
		if info.Available {
			t.Error("Available = true with no dotnet found")
		}
		if info.Error == "" {
			t.Error("Error must be set when no dotnet was found")
		}
		return
	}

	if info.Path != found {
		t.Errorf("Info.Path = %q, want the found host %q", info.Path, found)
	}
}

// TestDetectCommandFails covers a `dotnet` that exists, is executable, and
// fails — a broken install. Detect must still return a nil error.
func TestDetectCommandFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fake := filepath.Join(dir, "dotnet")
	script := "#!/bin/sh\necho 'The framework is broken' >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake dotnet: %v", err)
	}

	info, err := Detect(context.Background(), fake)
	if err != nil {
		t.Fatalf("Detect must not error on a failing host, got %v", err)
	}
	if info.Available {
		t.Error("Available = true for a failing host")
	}
	if !strings.Contains(info.Error, "The framework is broken") {
		t.Errorf("Error should surface the host's stderr, got %q", info.Error)
	}
}

// TestDetectParsesFakeHost runs the full happy path end to end against a
// scripted `dotnet`, so the parsing and the version decision are exercised
// together rather than only through ParseListRuntimes.
func TestDetectParsesFakeHost(t *testing.T) {
	t.Parallel()

	sample := readTestdata(t, "list-runtimes-net10-ga.txt")
	fake := writeFakeDotnet(t, "#!/bin/sh\ncat <<'EOF'\n"+sample+"EOF\n")

	info, err := Detect(context.Background(), fake)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !info.Available {
		t.Fatalf("Available = false, Error = %q", info.Error)
	}
	if !info.HasNetCoreApp10 {
		t.Error("HasNetCoreApp10 = false")
	}
	if info.Version != "10.0.12" {
		t.Errorf("Version = %q, want 10.0.12 (highest patch)", info.Version)
	}
	if len(info.Runtimes) != 3 {
		t.Errorf("Runtimes = %d entries, want 3", len(info.Runtimes))
	}
	if info.Error != "" {
		t.Errorf("Error = %q, want empty on success", info.Error)
	}
}

// TestDetectTooOldRuntime checks the "installed but wrong version" case, which
// needs a different remediation than "not installed at all".
func TestDetectTooOldRuntime(t *testing.T) {
	t.Parallel()

	sample := readTestdata(t, "list-runtimes-too-old.txt")
	fake := writeFakeDotnet(t, "#!/bin/sh\ncat <<'EOF'\n"+sample+"EOF\n")

	info, err := Detect(context.Background(), fake)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Available {
		t.Fatal("Available = true for a net9-only host")
	}
	if info.HasNetCoreApp10 {
		t.Error("HasNetCoreApp10 = true for a net9-only host")
	}
	if !strings.Contains(info.Error, "too old") {
		t.Errorf("Error = %q, want it to say the runtime is too old", info.Error)
	}
	if info.Version != "9.0.0" {
		t.Errorf("Version = %q, want the best available (9.0.0) so the UI can show it", info.Version)
	}
}

// TestDetectDesktopOnlyRuntime is the §2.1 negative: a WindowsDesktop.App
// runtime is not a runtime for this server.
func TestDetectDesktopOnlyRuntime(t *testing.T) {
	t.Parallel()

	sample := readTestdata(t, "list-runtimes-desktop-only.txt")
	fake := writeFakeDotnet(t, "#!/bin/sh\ncat <<'EOF'\n"+sample+"EOF\n")

	info, err := Detect(context.Background(), fake)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Available {
		t.Fatal("WindowsDesktop.App must not make the runtime Available")
	}
	if !strings.Contains(info.Error, runtimeNetCoreApp) {
		t.Errorf("Error = %q, want it to name %s", info.Error, runtimeNetCoreApp)
	}
}

func TestDetectEmptyOutput(t *testing.T) {
	t.Parallel()

	fake := writeFakeDotnet(t, "#!/bin/sh\nexit 0\n")

	info, err := Detect(context.Background(), fake)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Available {
		t.Error("Available = true with no runtimes reported")
	}
	if !strings.Contains(info.Error, "no runtimes") {
		t.Errorf("Error = %q, want it to mention that no runtimes were reported", info.Error)
	}
}

func TestDetectHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	// A script that would block forever if the context were ignored.
	fake := writeFakeDotnet(t, "#!/bin/sh\nsleep 30\n")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := Detect(ctx, fake)
	if err == nil {
		t.Fatal("Detect must return an error when the context is cancelled")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want a context error", err)
	}
}

// TestFindDotnetPrefersDotnetRoot pins the lookup order: an explicit
// DOTNET_ROOT wins over PATH, which is the documented contract for hosts
// installed outside the system prefix (plan §9.1's dotnet-install.sh puts it in
// ~/.dotnet).
func TestFindDotnetPrefersDotnetRoot(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeDotnetIn(t, dir, "#!/bin/sh\nexit 0\n")

	t.Setenv("DOTNET_ROOT", dir)
	t.Setenv("PATH", t.TempDir()) // no dotnet on PATH

	got, err := FindDotnet()
	if err != nil {
		t.Fatalf("FindDotnet: %v", err)
	}
	if got != fake {
		t.Errorf("FindDotnet = %q, want %q", got, fake)
	}
}

func TestFindDotnetIgnoresNonExecutableDotnetRoot(t *testing.T) {
	isolateFromHostDotnet(t)

	dir := t.TempDir()
	// Present but not executable: must fall through, not be selected.
	if err := os.WriteFile(filepath.Join(dir, "dotnet"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("DOTNET_ROOT", dir)
	t.Setenv("PATH", t.TempDir())

	if _, err := FindDotnet(); !errors.Is(err, ErrDotnetNotFound) {
		t.Fatalf("FindDotnet = %v, want ErrDotnetNotFound", err)
	}
}

func TestFindDotnetIgnoresDOTNET_ROOTDirectory(t *testing.T) {
	isolateFromHostDotnet(t)

	dir := t.TempDir()
	// A *directory* named dotnet must not be returned as the host.
	if err := os.Mkdir(filepath.Join(dir, "dotnet"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	t.Setenv("DOTNET_ROOT", dir)
	t.Setenv("PATH", t.TempDir())

	if _, err := FindDotnet(); !errors.Is(err, ErrDotnetNotFound) {
		t.Fatalf("FindDotnet = %v, want ErrDotnetNotFound", err)
	}
}

func TestInstallHintMentionsTheRequiredRuntime(t *testing.T) {
	t.Parallel()

	hint := InstallHint()
	for _, want := range []string{"Microsoft.NETCore.App 10", "dotnet-install.sh", "--runtime dotnet"} {
		if !strings.Contains(hint, want) {
			t.Errorf("InstallHint() is missing %q:\n%s", want, hint)
		}
	}
}

// writeFakeDotnet writes an executable script into a fresh temp dir and returns
// its path.
func writeFakeDotnet(t *testing.T, script string) string {
	t.Helper()
	return writeFakeDotnetIn(t, t.TempDir(), script)
}

func writeFakeDotnetIn(t *testing.T, dir, script string) string {
	t.Helper()
	path := filepath.Join(dir, "dotnet")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake dotnet: %v", err)
	}
	return path
}
