package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// This file implements GET /system/info's backend, including the .NET runtime
// detection the plan calls the "number one deployment precondition" (§7):
//
//	面板启动时执行 dotnet --list-runtimes，校验存在 Microsoft.NETCore.App 10.x；
//	缺失则给出明确指引.
//
// # Failure policy
//
// A missing dotnet is an *expected* state, not an error. This environment has
// no dotnet, and neither will a fresh host before §9.1 step 1 is done. The
// service therefore reports Available=false with a human-readable Hint and a
// nil error, so the endpoint answers 200. It returns an error only for a
// genuinely broken panel state (unreadable instances dir, etc.).

// RequiredDotnetMajor is the Microsoft.NETCore.App major version the game
// server targets. §7 pins it at 10.
const RequiredDotnetMajor = 10

// DotnetProbeTimeout bounds the `dotnet --list-runtimes` call. A first run on a
// cold host can be slow (JIT, NuGet fallback), but it must never hang the
// system-info endpoint.
const DotnetProbeTimeout = 10 * time.Second

// HostSystemService is the SystemService implementation. It reports host facts,
// the .NET runtime status and disk usage.
type HostSystemService struct {
	// DotnetPath overrides PATH lookup for the dotnet executable. Empty means
	// "search PATH".
	DotnetPath string
	// TemplateDir is the server template package directory (§6.1). Empty means
	// "not configured".
	TemplateDir string
	// InstancesDir is the default instances root, used for storage reporting.
	InstancesDir string
	// CommandTimeout bounds each external probe.
	CommandTimeout time.Duration
	// ProbeDotnet allows tests to inject the runtime list without a real
	// dotnet binary. When nil, the real command is run.
	ProbeDotnet func(ctx context.Context) ([]string, error)
	// startedAt is the process start time, for panel uptime.
	startedAt time.Time
	// now is injectable for tests.
	now func() time.Time
}

// NewHostSystemService builds the service with production defaults.
func NewHostSystemService() *HostSystemService {
	return &HostSystemService{
		CommandTimeout: DotnetProbeTimeout,
		startedAt:      time.Now(),
		now:            time.Now,
	}
}

// Compile-time assertion.
var _ SystemService = (*HostSystemService)(nil)

// Info gathers the host report.
func (s *HostSystemService) Info(ctx context.Context) (*SystemInfo, error) {
	info := &SystemInfo{
		Version: s.version(),
		Host:    s.hostInfo(),
		Dotnet:  s.dotnetInfo(ctx),
		Storage: s.storageInfo(),
		Panel:   s.panelInfo(),
	}
	return info, nil
}

// SystemInfo is the aggregate returned by Info. It is defined here (rather than
// reusing SystemInfoResponse) so the service has no dependency on the HTTP DTO.
type SystemInfo struct {
	Version VersionInfo
	Host    HostInfo
	Dotnet  DotnetInfo
	Storage StorageInfo
	Panel   PanelInfo
}

func (s *HostSystemService) version() VersionInfo {
	v := VersionInfo{
		Version:   "0.1.0-dev",
		Commit:    "unknown",
		BuildTime: "unknown",
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	// Read the embedded build info so /system/info is accurate even before the
	// host wires internal/version in.
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range bi.Settings {
			switch setting.Key {
			case "vcs.revision":
				if len(setting.Value) >= 12 {
					v.Commit = setting.Value[:12]
				} else if setting.Value != "" {
					v.Commit = setting.Value
				}
			case "vcs.time":
				v.BuildTime = setting.Value
			}
		}
	}
	return v
}

func (s *HostSystemService) nowFn() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *HostSystemService) hostInfo() HostInfo {
	hostname, _ := os.Hostname()

	h := HostInfo{
		Hostname:  hostname,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CPUCores:  runtime.NumCPU(),
		GoVersion: runtime.Version(),
		Kernel:    readKernelVersion(),
	}

	// Load average is Linux/Unix only; on other platforms the helper returns
	// ok=false and the fields stay zero (omitted from JSON).
	if l1, l5, l15, ok := readLoadAvg(); ok {
		h.LoadAvg1, h.LoadAvg5, h.LoadAvg15 = l1, l5, l15
	}
	if total, avail, ok := readMemInfo(); ok {
		h.MemoryTotal = total
		h.MemoryAvail = avail
		h.MemoryUsed = total - avail
	}
	// Uptime: from /proc/uptime on Linux, else from the panel's own start.
	if up, ok := readHostUptime(); ok {
		h.UptimeSec = up
	}
	return h
}

// dotnetInfo is the runtime detection. It NEVER returns an error: every failure
// mode is described in the returned struct.
func (s *HostSystemService) dotnetInfo(ctx context.Context) DotnetInfo {
	info := DotnetInfo{
		// Always an array, never null: the UI iterates it unconditionally.
		Runtimes:      []string{},
		RequiredMajor: RequiredDotnetMajor,
	}

	if s.TemplateDir != "" {
		info.TemplateDir = s.TemplateDir
		if st, err := os.Stat(s.TemplateDir); err == nil && st.IsDir() {
			info.TemplateAvailable = true
		}
	}

	// Locate the executable so the operator knows whether we even looked in the
	// right place. LookPath failing is the common "no .NET installed" case.
	exe := s.DotnetPath
	if exe == "" {
		found, err := exec.LookPath("dotnet")
		if err != nil {
			info.Error = "dotnet executable not found in PATH"
			info.Hint = dotnetInstallHint(s.TemplateDir)
			return info
		}
		exe = found
	} else if _, err := os.Stat(exe); err != nil {
		info.Error = fmt.Sprintf("configured dotnet path %q does not exist", exe)
		info.Hint = dotnetInstallHint(s.TemplateDir)
		return info
	}
	info.DotnetPath = exe

	runtimes, exitCode, err := s.listRuntimesWithExit(ctx, exe)
	info.ExitCode = exitCode
	if err != nil {
		info.Error = err.Error()
		info.Hint = dotnetInstallHint(s.TemplateDir)
		// Still expose the path: dotnet exists but could not enumerate.
		return info
	}
	// Guard against a nil slice: encoding/json renders nil as `null`, which
	// would force every UI to null-check a list it should be able to iterate.
	if runtimes == nil {
		runtimes = []string{}
	}
	info.Runtimes = runtimes

	best := ""
	for _, line := range runtimes {
		// Format: "Microsoft.NETCore.App 10.0.0 [/usr/share/dotnet/shared/...]"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if !strings.EqualFold(fields[0], "Microsoft.NETCore.App") {
			continue
		}
		major, ok := majorOf(fields[1])
		if !ok || major != RequiredDotnetMajor {
			continue
		}
		if versionLess(best, fields[1]) {
			best = fields[1]
		}
	}

	if best == "" {
		info.Error = fmt.Sprintf("Microsoft.NETCore.App %d.x is not installed (found %d runtime(s))",
			RequiredDotnetMajor, len(runtimes))
		info.Hint = dotnetInstallHint(s.TemplateDir)
		return info
	}

	info.Available = true
	info.Version = best
	return info
}

// listRuntimes runs `dotnet --list-runtimes` (or the injected probe).
func (s *HostSystemService) listRuntimes(ctx context.Context, exe string) ([]string, error) {
	runtimes, _, err := s.listRuntimesWithExit(ctx, exe)
	return runtimes, err
}

// listRuntimesWithExit is listRuntimes plus the child's exit status, so the
// /system/info payload can distinguish "dotnet is not installed" (no process
// ran, exit is nil) from "dotnet is installed but misconfigured" (a non-zero
// exit), which need different remediation from the operator.
func (s *HostSystemService) listRuntimesWithExit(ctx context.Context, exe string) ([]string, *int, error) {
	if s.ProbeDotnet != nil {
		out, err := s.ProbeDotnet(ctx)
		return out, nil, err
	}

	timeout := s.CommandTimeout
	if timeout <= 0 {
		timeout = DotnetProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Arguments are passed as an array, never through a shell (§5.7).
	cmd := exec.CommandContext(ctx, exe, "--list-runtimes")
	cmd.Env = append(os.Environ(),
		// Keep dotnet from printing a first-run welcome banner, and from
		// generating a telemetry payload.
		"DOTNET_NOLOGO=1",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, nil, fmt.Errorf("`dotnet --list-runtimes` timed out after %s", timeout)
		}
		var exitErr *exec.ExitError
		var code *int
		if errors.As(err, &exitErr) {
			c := exitErr.ExitCode()
			code = &c
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, code, fmt.Errorf("`dotnet --list-runtimes` failed: %s", truncate(msg, 300))
	}

	var out []string
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	zero := 0
	return out, &zero, nil
}

// majorOf extracts the leading integer of a version string.
func majorOf(v string) (int, bool) {
	i := strings.IndexByte(v, '.')
	if i <= 0 {
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	n, err := strconv.Atoi(v[:i])
	return n, err == nil
}

// versionLess reports whether a is "less than" b, treating "" as least.
func versionLess(a, b string) bool {
	if a == "" {
		return true
	}
	return compareVersions(a, b) < 0
}

// compareVersions compares dotted numeric versions.
func compareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv int
		if i < len(as) {
			av, _ = strconv.Atoi(strings.TrimSpace(as[i]))
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(strings.TrimSpace(bs[i]))
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// dotnetInstallHint builds the operator-facing remediation text from §9.1.
func dotnetInstallHint(templateDir string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Install the .NET %d runtime, then restart the panel: ", RequiredDotnetMajor))
	b.WriteString("https://learn.microsoft.com/dotnet/core/install/linux (or run the bundled dotnet-install.sh). ")
	b.WriteString(fmt.Sprintf("Verify with `dotnet --list-runtimes`; it must list Microsoft.NETCore.App %d.x.", RequiredDotnetMajor))
	if templateDir == "" {
		b.WriteString(" The server template directory is not configured, so instances cannot be provisioned from a template yet (see §6.1).")
	}
	return b.String()
}

func (s *HostSystemService) storageInfo() StorageInfo {
	dir := s.InstancesDir
	info := StorageInfo{Path: dir}
	if dir == "" {
		info.Error = "instances directory is not configured"
		return info
	}

	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		info.Error = fmt.Sprintf("statfs %s: %v", dir, err)
	} else {
		info.TotalBytes = st.Blocks * uint64(st.Bsize)
		info.FreeBytes = st.Bavail * uint64(st.Bsize)
		info.UsedBytes = info.TotalBytes - st.Bfree*uint64(st.Bsize)
	}

	// Writability is the fact that actually matters for creating instances.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		info.Error = fmt.Sprintf("instances directory is not creatable: %v", err)
		return info
	}
	f, err := os.CreateTemp(dir, ".scnetm-write-probe-*")
	if err != nil {
		if info.Error == "" {
			info.Error = fmt.Sprintf("instances directory is not writable: %v", err)
		}
		return info
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	info.Writable = true
	return info
}

func (s *HostSystemService) panelInfo() PanelInfo {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	uptime := int64(s.nowFn().Sub(s.startedAt).Seconds())
	if uptime < 0 {
		uptime = 0
	}
	return PanelInfo{
		UptimeSec:  uptime,
		Goroutines: runtime.NumGoroutine(),
		HeapAlloc:  ms.HeapAlloc,
		Sys:        ms.Sys,
	}
}

// ---------------------------------------------------------------------------
// /proc helpers (Linux). Each returns ok=false where unavailable, so the
// endpoint stays portable and degrades to "field omitted".
// ---------------------------------------------------------------------------

func readKernelVersion() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readLoadAvg() (l1, l5, l15 float64, ok bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return 0, 0, 0, false
	}
	var err1, err2, err3 error
	l1, err1 = strconv.ParseFloat(fields[0], 64)
	l5, err2 = strconv.ParseFloat(fields[1], 64)
	l15, err3 = strconv.ParseFloat(fields[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return l1, l5, l15, true
}

func readMemInfo() (total, available uint64, ok bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	var gotTotal, gotAvail bool
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			total, gotTotal = parseKBLine(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			available, gotAvail = parseKBLine(line)
		}
	}
	return total, available, gotTotal && gotAvail
}

func parseKBLine(line string) (uint64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, false
	}
	kb, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return kb * 1024, true
}

func readHostUptime() (int64, bool) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return int64(secs), true
}

// FirstExistingDir returns the first of the candidates that exists and is a
// directory, or "". It is a small helper for locating optional directories
// (template packs, dotnet installs) without hardcoding a single layout.
func FirstExistingDir(candidates ...string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(filepath.Clean(c)); err == nil && st.IsDir() {
			return filepath.Clean(c)
		}
	}
	return ""
}
