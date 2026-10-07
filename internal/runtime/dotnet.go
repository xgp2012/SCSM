// Package runtime detects and describes the .NET runtime the Survivalcraft
// server needs, and validates a server template directory before an instance is
// created from it.
//
// Why this package exists at all (plan §2.1, §7, §9.1):
//
// The official server package ships `start.sh`, which prefers a self-contained
// native launcher (`./Survivalcraft`) and otherwise runs `dotnet
// ./Survivalcraft.dll`. The package released as X26.07.01.01 does NOT contain
// that native launcher, so the game server can only start if a `dotnet` host
// with a Microsoft.NETCore.App 10.x runtime is present. That makes runtime
// presence the panel's number one deployment prerequisite, and the panel must
// be able to *report* its absence rather than fail to boot.
//
// The package is therefore written to degrade gracefully: when `dotnet` is
// missing or misbehaving, Detect returns an Info with Available=false and a
// human-readable Error, together with a nil error. A nil error means "the check
// ran successfully and the answer is: not available". Errors are reserved for
// programming problems (a cancelled context), not for the absence of a
// dependency the operator may simply not have installed yet.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DesiredMajor is the minimum Microsoft.NETCore.App major version the server
// requires. Plan §2.1: `tfm = net10.0`, `framework: Microsoft.NETCore.App
// 10.0.0`.
const DesiredMajor = 10

// Runtime names that must NOT be mistaken for a usable server runtime. The plan
// is explicit that the server has no desktop dependency (§2.1, appendix A.2),
// so WindowsDesktop.App being present is not evidence, and AspNetCore.App is a
// different product entirely.
const (
	runtimeNetCoreApp     = "Microsoft.NETCore.App"
	runtimeWindowsDesktop = "Microsoft.WindowsDesktop.App"
	runtimeAspNetCore     = "Microsoft.AspNetCore.App"
)

// Runtime is one line of `dotnet --list-runtimes`.
type Runtime struct {
	Name    string `json:"name"`    // e.g. Microsoft.NETCore.App
	Version string `json:"version"` // e.g. 10.0.12 (or 10.0.0-rc.1.25451.1)
	Path    string `json:"path"`    // e.g. /usr/share/dotnet/shared/Microsoft.NETCore.App/10.0.12
}

// Info is the result of a runtime probe.
//
// Available is the only field a caller must branch on. When it is false, Error
// explains why in operator-readable terms and the panel should keep running,
// surfacing the message plus InstallHint() in the UI.
type Info struct {
	// Available reports whether a suitable Microsoft.NETCore.App >= 10 runtime
	// was found.
	Available bool `json:"available"`

	// Path is the `dotnet` executable that was probed.
	Path string `json:"path"`

	// Version is the highest Microsoft.NETCore.App version found, and the one
	// the server will actually load.
	Version string `json:"version"`

	// HasNetCoreApp10 reports whether any Microsoft.NETCore.App with major
	// version >= DesiredMajor exists. It is reported separately from Available
	// so the UI can distinguish "no dotnet at all" from "dotnet 8 installed,
	// upgrade needed" — different remediations for the operator.
	HasNetCoreApp10 bool `json:"hasNetCoreApp10"`

	// Runtimes is every runtime `dotnet --list-runtimes` reported, in the order
	// the CLI printed them (stable, since that is how operators compare with
	// their own terminal).
	Runtimes []Runtime `json:"runtimes,omitempty"`

	// Error is set instead of returning an error when the probe could not
	// establish availability.
	Error string `json:"error,omitempty"`
}

// ErrDotnetNotFound is returned by FindDotnet when no host is on PATH.
var ErrDotnetNotFound = errors.New("runtime: dotnet executable not found")

// candidatePaths are the well-known install locations checked when `dotnet` is
// not on PATH. The plan's §9.1 installs via dotnet-install.sh, which defaults
// to ~/.dotnet/dotnet — the single most common reason a working runtime looks
// "missing" when the panel runs under systemd with a minimal PATH.
//
// candidatePathsOverride exists only so tests can isolate themselves from a
// host that has .NET preinstalled in one of these locations (GitHub's
// ubuntu-latest images ship /usr/share/dotnet/dotnet, for example). It is nil in
// production, where the list below is always used.
var candidatePathsOverride []string

func candidatePaths() []string {
	if candidatePathsOverride != nil {
		return candidatePathsOverride
	}
	home, _ := os.UserHomeDir()
	out := []string{
		"/usr/share/dotnet/dotnet",
		"/usr/lib/dotnet/dotnet",
		"/usr/local/share/dotnet/dotnet",
		"/opt/dotnet/dotnet",
	}
	if home != "" {
		out = append([]string{filepath.Join(home, ".dotnet", "dotnet")}, out...)
	}
	return out
}

// FindDotnet locates a usable `dotnet` host.
//
// Order of preference:
//
//  1. $DOTNET_ROOT/dotnet — the conventional override, and what the operator
//     sets when dotnet lives outside PATH.
//  2. `dotnet` resolved through PATH (exec.LookPath).
//  3. The well-known install locations in candidatePaths.
//
// It returns ErrDotnetNotFound (wrapped, so errors.Is works) when nothing
// suitable is found.
func FindDotnet() (string, error) {
	if root := strings.TrimSpace(os.Getenv("DOTNET_ROOT")); root != "" {
		p := filepath.Join(root, "dotnet")
		if isExecutable(p) {
			return p, nil
		}
	}

	if p, err := exec.LookPath("dotnet"); err == nil {
		return p, nil
	}

	for _, p := range candidatePaths() {
		if isExecutable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w (checked PATH, $DOTNET_ROOT and %s)",
		ErrDotnetNotFound, strings.Join(candidatePaths(), ", "))
}

// isExecutable reports whether p is a regular executable file.
func isExecutable(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode().Perm()&0o111 != 0
}

// Detect probes dotnetPath with `dotnet --list-runtimes` and reports what is
// installed.
//
// A missing executable, a non-zero exit status, or unparsable output all
// produce Info{Available:false, Error:"..."} with a nil error — see the package
// comment. The only error returned is a context cancellation, because that is a
// programming/ lifecycle condition rather than a deployment state.
//
// An empty dotnetPath means "find one" (FindDotnet).
func Detect(ctx context.Context, dotnetPath string) (*Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	path := strings.TrimSpace(dotnetPath)
	if path == "" {
		found, err := FindDotnet()
		if err != nil {
			return &Info{Error: err.Error()}, nil
		}
		path = found
	}

	info := &Info{Path: path}

	out, err := runListRuntimes(ctx, path)
	if err != nil {
		// A cancelled context is a real error; everything else is a
		// deployment condition the panel reports and survives.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		info.Error = err.Error()
		return info, nil
	}

	info.Runtimes = ParseListRuntimes(out)
	if len(info.Runtimes) == 0 {
		info.Error = fmt.Sprintf("%s --list-runtimes reported no runtimes; "+
			"the host is installed but no runtime pack is available", path)
		return info, nil
	}

	best, ok := BestNetCoreApp(info.Runtimes)
	if !ok {
		info.Error = fmt.Sprintf("%s has no %s runtime (found: %s)",
			path, runtimeNetCoreApp, summarize(info.Runtimes))
		return info, nil
	}
	info.Version = best.Version

	major, ok := MajorVersion(best.Version)
	if !ok {
		info.Error = fmt.Sprintf("cannot interpret %s version %q", runtimeNetCoreApp, best.Version)
		return info, nil
	}

	info.HasNetCoreApp10 = major >= DesiredMajor
	if !info.HasNetCoreApp10 {
		info.Error = fmt.Sprintf("%s %s is too old: the server package targets "+
			"net%d.0 and needs %s %d.x", runtimeNetCoreApp, best.Version,
			DesiredMajor, runtimeNetCoreApp, DesiredMajor)
		return info, nil
	}

	info.Available = true
	return info, nil
}

// runListRuntimes executes `dotnet --list-runtimes` and returns stdout.
//
// .NET writes the runtime list to stdout; a failure (bad host, corrupted
// install) writes to stderr and exits non-zero, and both streams are included
// in the error so the operator sees the host's own diagnosis.
func runListRuntimes(ctx context.Context, dotnetPath string) (string, error) {
	cmd := exec.CommandContext(ctx, dotnetPath, "--list-runtimes")

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s --list-runtimes failed: %s", dotnetPath, firstLine(msg))
	}
	return stdout.String(), nil
}

// ParseListRuntimes parses `dotnet --list-runtimes` output.
//
// The documented format is one runtime per line:
//
//	Microsoft.NETCore.App 10.0.12 [/usr/share/dotnet/shared/Microsoft.NETCore.App/10.0.12]
//
// The bracketed path is optional in the parser because older hosts omitted it
// in some builds; lines that do not match the shape at all are skipped rather
// than aborting the parse, since a single unexpected line (a warning on stdout)
// must not hide the runtimes that are present. See testdata/ for captured
// samples.
func ParseListRuntimes(out string) []Runtime {
	var runtimes []Runtime
	for _, line := range strings.Split(out, "\n") {
		if r, ok := parseRuntimeLine(line); ok {
			runtimes = append(runtimes, r)
		}
	}
	return runtimes
}

// parseRuntimeLine parses a single `--list-runtimes` line.
func parseRuntimeLine(line string) (Runtime, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Runtime{}, false
	}

	fields := strings.Fields(line)
	if len(fields) < 2 {
		return Runtime{}, false
	}

	name := fields[0]
	version := fields[1]

	// Dotnet runtime names are dotted assembly-style identifiers. Requiring a
	// dot plus a digit-leading version keeps stray banners ("Welcome to .NET")
	// and progress output out of the result.
	if !strings.Contains(name, ".") || version == "" || (version[0] < '0' || version[0] > '9') {
		return Runtime{}, false
	}

	r := Runtime{Name: name, Version: version}

	// The path is bracketed and the last field, but a path containing a space
	// would be split, so re-join everything from the first '[' onwards.
	if i := strings.Index(line, "["); i >= 0 {
		end := strings.LastIndex(line, "]")
		if end <= i {
			end = len(line)
		}
		r.Path = strings.TrimSpace(line[i+1 : end])
	}
	return r, true
}

// BestNetCoreApp returns the highest-versioned Microsoft.NETCore.App runtime.
//
// "Highest" uses numeric component comparison, not string comparison: as text,
// "10.0.9" > "10.0.12", which would pick the older patch and could reject a
// perfectly good install.
func BestNetCoreApp(runtimes []Runtime) (Runtime, bool) {
	var best Runtime
	found := false
	for _, r := range runtimes {
		if r.Name != runtimeNetCoreApp {
			continue
		}
		if !found || CompareVersions(r.Version, best.Version) > 0 {
			best, found = r, true
		}
	}
	return best, found
}

// HasWindowsDesktop reports whether a WindowsDesktop.App runtime is present.
// It is used by TemplateCheck's diagnostics to explain a desktop-dependent
// template, never to grant availability.
func HasWindowsDesktop(runtimes []Runtime) bool {
	for _, r := range runtimes {
		if r.Name == runtimeWindowsDesktop {
			return true
		}
	}
	return false
}

// CompareVersions compares two .NET version strings by numeric component.
//
// It returns -1, 0 or 1 for a < b, a == b, a > b. Semantics:
//
//   - Components are compared numerically, so 10.0.12 > 10.0.9.
//   - A missing component is treated as 0, so 10 == 10.0.0.
//   - A pre-release suffix makes a version *lower* than the same numeric
//     version without one (SemVer rule 11), so 10.0.0-rc.1 < 10.0.0. Two
//     pre-releases of equal numbers compare by their suffixes lexically, which
//     is not full SemVer precedence (10.0.0-rc.10 > 10.0.0-rc.9 is wrong
//     lexically) but is sufficient — and safe — for the only decision this
//     package makes: whether a release-grade 10.x runtime is present.
//   - Unparsable components compare as 0 rather than panicking, because a
//     version string comes from an external process.
func CompareVersions(a, b string) int {
	an, apre := splitVersion(a)
	bn, bpre := splitVersion(b)

	for i := 0; i < len(an) || i < len(bn); i++ {
		var av, bv int
		if i < len(an) {
			av = an[i]
		}
		if i < len(bn) {
			bv = bn[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}

	switch {
	case apre == "" && bpre == "":
		return 0
	case apre == "":
		return 1 // a is the release, b is a preview
	case bpre == "":
		return -1
	case apre == bpre:
		return 0
	case apre < bpre:
		return -1
	default:
		return 1
	}
}

// MajorVersion extracts the major component of a version string.
func MajorVersion(v string) (int, bool) {
	nums, _ := splitVersion(v)
	if len(nums) == 0 {
		return 0, false
	}
	return nums[0], true
}

// splitVersion splits "10.0.12-preview.1" into [10 0 12] and "preview.1".
//
// Build metadata ("+abc", which SemVer says to ignore when comparing) is
// dropped entirely; a leading 'v' is tolerated because some installers print
// it.
func splitVersion(v string) ([]int, string) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return nil, ""
	}

	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}

	var pre string
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre = v[i+1:]
		v = v[:i]
	}

	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			// Tolerate an unparsable component by treating it as 0; the
			// string came from another process.
			n = 0
		}
		nums = append(nums, n)
	}
	return nums, pre
}

// summarize renders a runtime list compactly for an error message, e.g.
// "Microsoft.NETCore.App 8.0.10, Microsoft.AspNetCore.App 8.0.10".
func summarize(runtimes []Runtime) string {
	seen := make(map[string]string, len(runtimes))
	names := make([]string, 0, len(runtimes))
	for _, r := range runtimes {
		if _, ok := seen[r.Name]; !ok {
			names = append(names, r.Name)
		}
		if CompareVersions(r.Version, seen[r.Name]) > 0 {
			seen[r.Name] = r.Version
		}
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+" "+seen[n])
	}
	return strings.Join(parts, ", ")
}

// firstLine returns the first non-empty line of s, for compact error messages.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// InstallHint returns the operator-facing remediation shown when no suitable
// runtime is found. The command is the one in plan §9.1.
//
// It is a plain string (not an error) so the UI can render it in a code block
// without unwrapping an error chain.
func InstallHint() string {
	return "未找到合适的 .NET 运行时。服务端程序包目标框架为 net10.0，" +
		"需要 Microsoft.NETCore.App " + strconv.Itoa(DesiredMajor) +
		".x 运行时（是普通运行时，不是 ASP.NET Core，也不是 Windows Desktop）。\n" +
		"安装命令：\n" +
		"  curl -sSL https://dot.net/v1/dotnet-install.sh | bash -s -- --channel " +
		strconv.Itoa(DesiredMajor) + ".0 --runtime dotnet\n" +
		"之后请将 dotnet 加入 PATH、设置 DOTNET_ROOT，或把面板的 " +
		"`dotnet_path` 指向它（默认：~/.dotnet/dotnet），然后重启面板。"
}
