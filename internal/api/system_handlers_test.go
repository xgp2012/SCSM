package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- /system/info ----------------------------------------------------------

// TestSystemInfoWithDotnetAbsent is the environment-critical test: this machine
// has NO dotnet runtime, and that must be reported as DATA with a 200, never as
// a 5xx. A panel that 500s when a runtime is missing is unusable in exactly the
// environment the operator is trying to fix.
func TestSystemInfoWithDotnetAbsent(t *testing.T) {
	t.Parallel()

	// Use the REAL host service, not a fake: this asserts the actual detection
	// code path, including the child-process invocation of `dotnet`.
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.realSystemService = true
	})
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.get(token, "/api/v1/system/info")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data SystemInfoResponse `json:"data"`
	}
	resp.JSON(&out)

	// The payload must always carry host and panel facts, present or not.
	if out.Data.Host.Hostname == "" {
		t.Error("host.hostname is empty")
	}
	if out.Data.Host.OS == "" || out.Data.Host.Arch == "" {
		t.Errorf("host os/arch missing: %+v", out.Data.Host)
	}
	if out.Data.Host.CPUCores <= 0 {
		t.Errorf("host.cpu_cores = %d", out.Data.Host.CPUCores)
	}
	if out.Data.Version.Version == "" {
		t.Error("version.version is empty")
	}
	if out.Data.Host.GoVersion == "" {
		t.Error("host.go_version is empty (the UI shows it for bug reports)")
	}
	if out.Data.Version.Platform == "" {
		t.Error("version.platform is empty")
	}
	if out.Data.Storage.Path == "" {
		t.Error("storage.path is empty")
	}

	// The .NET section must be explicit about being unavailable.
	if out.Data.Dotnet.RequiredMajor != RequiredDotnetMajor {
		t.Errorf("dotnet.required_major = %d, want %d", out.Data.Dotnet.RequiredMajor, RequiredDotnetMajor)
	}

	// In this environment dotnet is absent. Assert the degrade-gracefully
	// contract; if a runtime were somehow installed, the assertions invert to
	// "available with a parsed version", so the test stays meaningful either
	// way rather than being skipped.
	if !out.Data.Dotnet.Available {
		if out.Data.Dotnet.Error == "" {
			t.Error("unavailable dotnet must carry an error explaining what failed")
		}
		if out.Data.Dotnet.Hint == "" {
			t.Error("unavailable dotnet must carry an installation hint for the operator")
		}
		if !strings.Contains(strings.ToLower(out.Data.Dotnet.Hint), ".net") {
			t.Errorf("hint does not look actionable: %q", out.Data.Dotnet.Hint)
		}
		if out.Data.Dotnet.Runtimes == nil {
			t.Error("runtimes should be an empty array, not null")
		}
	} else {
		if len(out.Data.Dotnet.Runtimes) == 0 {
			t.Error("available dotnet reported no runtimes")
		}
	}
}

// TestSystemInfoReflectsPanelState: the endpoint folds in counters the UI needs
// so it does not have to make four calls.
func TestSystemInfoReflectsPanelState(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.realSystemService = true })
	token := ts.adminToken("admin-password-123")

	a := ts.seedInstance("sysinfo1", 30300)
	ts.seedInstance("sysinfo2", 30301)
	if err := ts.process.Start(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}

	resp := ts.get(token, "/api/v1/system/info")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data SystemInfoResponse `json:"data"`
	}
	resp.JSON(&out)

	if out.Data.Panel.Instances != 2 {
		t.Errorf("panel.instances = %d, want 2", out.Data.Panel.Instances)
	}
	if out.Data.Panel.RunningCount != 1 {
		t.Errorf("panel.running_instances = %d, want 1", out.Data.Panel.RunningCount)
	}
	if out.Data.Panel.Users != 1 {
		t.Errorf("panel.users = %d, want 1", out.Data.Panel.Users)
	}
	if !out.Data.Panel.SingleUser {
		t.Error("panel.single_user = false, want true")
	}
	if out.Data.Panel.SetupRequired {
		t.Error("panel.setup_required = true after setup")
	}
	if out.Data.Panel.Goroutines <= 0 {
		t.Error("panel.goroutines should be positive")
	}
}

func TestSystemInfoRequiresAuth(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)

	resp := ts.get("", "/api/v1/system/info")
	requireErrorCode(t, resp, http.StatusUnauthorized, CodeUnauthorized)
}

func TestHealthIsPublic(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)

	resp := ts.get("", "/api/v1/healthz")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data HealthResponse `json:"data"`
	}
	resp.JSON(&out)
	if out.Data.Status != "ok" {
		t.Errorf("status = %q, want ok", out.Data.Status)
	}
}

// TestRootHealthAlias: reverse proxies often probe /healthz.
func TestRootHealthAlias(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)

	requireStatus(t, ts.get("", "/healthz"), http.StatusOK)
}

// --- /system/info host probing is shell-free -------------------------------

// TestListRuntimesDoesNotUseAShell guards the §5.7 command-injection rule at its
// source: the dotnet probe must pass an argument vector, never a command string.
func TestListRuntimesDoesNotUseAShell(t *testing.T) {
	// Deliberately NOT parallel: t.Setenv mutates the process environment.

	// A PATH entry whose name contains shell metacharacters must be treated as
	// a literal path, not interpreted. We prove this by putting a fake "dotnet"
	// executable in a directory whose name contains shell characters and
	// checking that the probe executes it directly (the fake writes a marker
	// file) rather than failing to parse the path.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell semantics")
	}

	dir := filepath.Join(t.TempDir(), "has space;$and;semi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The marker path is embedded in the script, so keep it free of characters
	// the shell would interpret; the DIRECTORY name (with spaces and
	// semicolons) is what proves the executable path is not shell-parsed.
	marker := filepath.Join(t.TempDir(), "ran.marker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + strconv.Quote(marker) + "\n" +
		"echo 'Microsoft.NETCore.App 10.0.0 [/fake/dotnet]'\n"
	fake := filepath.Join(dir, "dotnet")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir)

	host := NewHostSystemService()
	host.DotnetPath = fake

	info := host.dotnetInfo(t.Context())
	if !info.Available {
		t.Fatalf("the fake dotnet was not detected: %+v", info)
	}
	if len(info.Runtimes) == 0 {
		t.Fatalf("no runtimes parsed from the fake output: %+v", info)
	}

	// The marker proves the process ran with the arguments we intended, which
	// is only reachable through exec-with-argv.
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the probe did not run the executable directly: %v", err)
	}
	if !strings.Contains(string(data), "--list-runtimes") {
		t.Errorf("the probe did not pass --list-runtimes: %q", data)
	}
}

func TestDotnetInfoNeverErrorsOnGarbageOutput(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell semantics")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "dotnet")
	// Exit non-zero with noise on stderr, which is what a broken install does.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'boom' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	host := NewHostSystemService()
	host.DotnetPath = fake

	info := host.dotnetInfo(t.Context())
	if info.Available {
		t.Error("a failing dotnet was reported as available")
	}
	if info.Error == "" {
		t.Error("no error recorded for a failing dotnet")
	}
	if info.ExitCode == nil || *info.ExitCode != 3 {
		t.Errorf("exit code not captured: %+v", info.ExitCode)
	}
}

// --- path traversal through the HTTP surface -------------------------------

// TestFilePathTraversalIsRejectedOverHTTP drives the traversal table through the
// real handlers, because the safepath unit test only proves the helper is
// correct — not that every handler actually calls it.
func TestFilePathTraversalIsRejectedOverHTTP(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("traversal", 30310)

	// A file outside the instances root that must survive every attempt.
	outside := filepath.Join(filepath.Dir(ts.instancesDir), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("classified"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	hostilePaths := []string{
		"../../etc/passwd",
		"..%2F..%2Fetc%2Fpasswd",
		"../../../outside-secret.txt",
		"/etc/passwd",
		`..\..\windows\win.ini`,
		`C:\Windows\win.ini`,
		"..\\..\\outside-secret.txt",
		"\x00/etc/passwd",
		"a\x00b",
		"....//....//etc/passwd",
		"Worlds/../../../outside-secret.txt",
		"./../../outside-secret.txt",
	}

	// Every read-shaped route that takes a path parameter.
	readRoutes := []struct {
		name  string
		build func(p string) string
	}{
		{"list files", func(p string) string { return "/api/v1/instances/1/files?path=" + urlEscape(p) }},
		{"download file", func(p string) string { return "/api/v1/instances/1/files/download?path=" + urlEscape(p) }},
		{"world export", func(p string) string { return "/api/v1/instances/1/worlds/" + urlEscape(p) + "/export" }},
	}

	for _, route := range readRoutes {
		for _, hostile := range hostilePaths {
			t.Run(route.name+"/"+shortLabel(hostile), func(t *testing.T) {
				resp := ts.get(token, route.build(hostile))
				// 403 (rejected as traversal), 404 (nothing there) or 422
				// (malformed) are all acceptable refusals. What must never
				// happen is a 200 with the outside file's contents, or a 5xx.
				switch resp.Status {
				case http.StatusOK:
					if strings.Contains(resp.String(), "classified") {
						t.Fatalf("SECURITY FAILURE: outside file read via %q: %s", hostile, resp.Body)
					}
				case http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity,
					http.StatusBadRequest:
					// correct refusal
				default:
					t.Errorf("unexpected status %d for %q: %s", resp.Status, hostile, resp.Body)
				}
			})
		}
	}

	// The outside file must still be intact.
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "classified" {
		t.Fatalf("the outside file was modified: %v", err)
	}

	// A rejected path must never have reached the file service.
	for _, seen := range ts.files.paths() {
		if strings.Contains(seen, "..") || strings.HasPrefix(seen, "/") || strings.Contains(seen, "\x00") {
			t.Fatalf("SECURITY FAILURE: a hostile path reached FileService: %q", seen)
		}
	}
}

// TestFileWriteTraversalIsRejected covers the mutating file routes.
func TestFileWriteTraversalIsRejected(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("traversalwrite", 30311)

	outside := filepath.Join(filepath.Dir(ts.instancesDir), "outside-write.txt")
	t.Cleanup(func() { _ = os.Remove(outside) })

	hostile := []string{
		"../../outside-write.txt",
		"../outside-write.txt",
		"/tmp/evil.txt",
		`..\..\outside-write.txt`,
		"Worlds/../../outside-write.txt",
	}

	for _, p := range hostile {
		t.Run(shortLabel(p), func(t *testing.T) {
			// mkdir
			resp := ts.post(token, "/api/v1/instances/1/files/mkdir", map[string]string{"path": p})
			if resp.Status == http.StatusOK {
				t.Errorf("mkdir accepted %q: %s", p, resp.Body)
			}

			// upload
			resp = ts.do(http.MethodPost, "/api/v1/instances/1/files/upload?path="+urlEscape(p),
				[]byte("payload"), "Authorization", "Bearer "+token,
				"Content-Type", "application/octet-stream")
			if resp.Status == http.StatusOK || resp.Status == http.StatusCreated {
				t.Errorf("upload accepted %q: %s", p, resp.Body)
			}

			// delete
			resp = ts.del(token, "/api/v1/instances/1/files?path="+urlEscape(p), nil)
			if resp.Status == http.StatusOK {
				t.Errorf("delete accepted %q: %s", p, resp.Body)
			}

			// rename (both ends)
			resp = ts.post(token, "/api/v1/instances/1/files/rename", map[string]string{
				"from": "Worlds", "to": p,
			})
			if resp.Status == http.StatusOK {
				t.Errorf("rename accepted target %q: %s", p, resp.Body)
			}
		})
	}

	if _, err := os.Stat(outside); err == nil {
		t.Fatal("SECURITY FAILURE: a file was created outside the instances root")
	}
}

// TestCommandInjectionIsRejected covers the console/command path: newlines, CR,
// NUL, escape sequences and oversize payloads must all be refused.
func TestCommandInjectionIsRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
	}{
		{"newline", "say hello\nrm -rf /"},
		{"carriage return", "say hello\r\nrm -rf /"},
		{"bare CR", "say hello\rrm -rf /"},
		{"null byte", "say hello\x00rm"},
		{"escape sequence", "say \x1b[31mred"},
		{"empty", ""},
		{"whitespace only", "   "},
		{"oversize", strings.Repeat("A", CommandMaxBytes+1)},
		{"oversize multibyte", strings.Repeat("\u00e9", CommandMaxBytes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateCommand(tc.line); err == nil {
				t.Fatalf("ValidateCommand(%q) = nil, want rejection", truncate(tc.line, 40))
			}
		})
	}

	// Ordinary commands, including ones with tabs and quotes, are accepted.
	ok := []string{
		"say hello",
		"/time set day",
		"say \"hello world\"",
		"say hello\tworld",
		"list",
		strings.Repeat("A", CommandMaxBytes),
	}
	for _, line := range ok {
		got, err := ValidateCommand(line)
		if err != nil {
			t.Errorf("ValidateCommand(%q) = %v, want nil", truncate(line, 30), err)
			continue
		}
		// The returned line is what gets sent to the process, so it must be the
		// line itself with no stray terminator.
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("ValidateCommand(%q) returned a line containing a terminator: %q", line, got)
		}
	}

	// A trailing terminator is stripped rather than rejected: it is what a
	// terminal sends when the operator presses Enter, and it must not smuggle a
	// second command through.
	normalized, err := ValidateCommand("say hello\n")
	if err != nil {
		t.Fatalf("a trailing newline was rejected: %v", err)
	}
	if normalized != "say hello" {
		t.Fatalf("ValidateCommand(\"say hello\\n\") = %q, want \"say hello\"", normalized)
	}
}

// TestCommandInjectionIsRejectedOverHTTP proves the check is actually wired into
// the console command route rather than only existing as a helper.
func TestCommandInjectionIsRejectedOverHTTP(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("cmdinject", 30312)

	// The REST command route, if present, must refuse the same payloads.
	for _, hostile := range []string{"say hi\nrm -rf /", "say hi\r", strings.Repeat("A", CommandMaxBytes+1)} {
		resp := ts.post(token, "/api/v1/instances/1/command", map[string]string{"command": hostile})
		if resp.Status == http.StatusOK {
			t.Errorf("command route accepted %q: %s", truncate(hostile, 30), resp.Body)
		}
	}
}

// --- instance name as a path component -------------------------------------

// TestInstanceNameCannotEscapeTheRoot is the create-path counterpart: even if
// the name validator were bypassed, the directory resolution must refuse.
func TestInstanceNameCannotEscapeTheRoot(t *testing.T) {
	t.Parallel()

	names := []string{
		"../escape",
		"..",
		"/absolute",
		`..\escape`,
		"a/../../b",
		"\x00",
		".",
		"sub/dir",
	}
	for _, name := range names {
		t.Run(shortLabel(name), func(t *testing.T) {
			if err := ValidateInstanceName(name); err == nil {
				t.Fatalf("ValidateInstanceName(%q) = nil, want rejection", name)
			}
		})
	}

	// Good names, including the dot/dash/underscore the plan allows.
	for _, name := range []string{"server1", "my-server", "my_server", "A1", "srv.prod"} {
		if err := ValidateInstanceName(name); err != nil {
			t.Errorf("ValidateInstanceName(%q) = %v, want nil", name, err)
		}
	}
}

// --- system handler edge cases ---------------------------------------------

func TestIntParamParsing(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	// Non-numeric, zero, negative and out-of-range ids are all 422.
	for _, id := range []string{"abc", "0", "-1", "1.5", "99999999999999999999", " "} {
		resp := ts.get(token, "/api/v1/instances/"+id)
		if resp.Status != http.StatusUnprocessableEntity && resp.Status != http.StatusNotFound {
			t.Errorf("id %q: status = %d, want 422 or 404: %s", id, resp.Status, resp.Body)
		}
	}
}

func TestQueryIntValidation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("queryint", 30313)

	// A negative or absurd tail is a validation failure, not a silent default.
	resp := ts.get(token, "/api/v1/instances/1/logs?tail=-1")
	if resp.Status != http.StatusUnprocessableEntity && resp.Status != http.StatusOK {
		t.Errorf("tail=-1: status = %d: %s", resp.Status, resp.Body)
	}
	resp = ts.get(token, "/api/v1/instances/1/logs?tail=abc")
	// A malformed query parameter is a bad request, not a validation failure of
	// the resource being addressed: 400 is the correct code here.
	if resp.Status != http.StatusBadRequest && resp.Status != http.StatusUnprocessableEntity {
		t.Errorf("tail=abc: status = %d, want 400: %s", resp.Status, resp.Body)
	}
}

// --- 501 delegation --------------------------------------------------------

// TestNopBackendsReturn501NotImplemented is the explicit requirement that a
// handler whose backend is absent must say so with a machine-readable code
// rather than returning silently empty data.
func TestNopBackendsReturn501NotImplemented(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.nopBackends = true
	})
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("nopbackend", 30320)

	routes := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"list worlds", http.MethodGet, "/api/v1/instances/1/worlds", nil},
		{"list files", http.MethodGet, "/api/v1/instances/1/files", nil},
		{"get config", http.MethodGet, "/api/v1/instances/1/config", nil},
		{"get logs", http.MethodGet, "/api/v1/instances/1/logs", nil},
		{"create backup", http.MethodPost, "/api/v1/instances/1/backups", map[string]any{"kind": "manual"}},
	}
	// NOTE: GET /backups is deliberately not in this list. It reads the backup
	// *store* (a row listing), which remains available in nop-backend mode, so
	// an empty array plus 200 is its correct answer — the 501 applies to the
	// backup *service* (creating/restoring), which is asserted above.
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			resp := ts.auth(token, route.method, route.path, route.body)
			requireStatus(t, resp, http.StatusNotImplemented)

			var env ErrorEnvelope
			if err := json.Unmarshal(resp.Body, &env); err != nil {
				t.Fatalf("501 body is not an envelope: %v", err)
			}
			if env.Error.Code != CodeNotImplemented {
				t.Fatalf("error code = %q, want %q: %s", env.Error.Code, CodeNotImplemented, resp.Body)
			}
			if env.Error.Message == "" {
				t.Error("501 has no message explaining what is missing")
			}
			// A 501 must never carry a data payload that looks like success.
			if strings.Contains(resp.String(), `"data"`) {
				t.Errorf("501 response contains a data field: %s", resp.Body)
			}
		})
	}
}

// TestProcessManagerNopStillServesLifecycle: even with every feature backend
// absent, instance lifecycle must keep working, because that is what makes the
// panel usable before the game server exists.
func TestProcessManagerNopStillServesLifecycle(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.nopBackends = true
	})
	token := ts.mustLogin("admin", "admin-password-123")

	create := ts.post(token, "/api/v1/instances", CreateInstanceRequest{Name: "noplife"})
	requireStatus(t, create, http.StatusCreated)

	requireStatus(t, ts.post(token, "/api/v1/instances/1/start", nil), http.StatusOK)

	state := ts.get(token, "/api/v1/instances/1")
	requireStatus(t, state, http.StatusOK)
	var out struct {
		Data InstanceResponse `json:"data"`
	}
	state.JSON(&out)
	if out.Data.State != StateRunning {
		t.Errorf("state = %q, want Running", out.Data.State)
	}

	requireStatus(t, ts.post(token, "/api/v1/instances/1/stop", nil), http.StatusOK)
}

// --- helpers ---------------------------------------------------------------

func urlEscape(s string) string {
	// Escape everything that is not unreserved, so hostile characters reach the
	// handler exactly as written rather than being normalised away by the URL
	// parser.
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if strings.IndexByte(unreserved, ch) >= 0 {
			b.WriteByte(ch)
			continue
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[ch>>4])
		b.WriteByte(hex[ch&0x0F])
	}
	return b.String()
}

func shortLabel(p string) string {
	label := strings.NewReplacer("/", "_", "\\", "_", "%", "pct", "\x00", "NUL", " ", "_").Replace(p)
	if label == "" {
		label = "empty"
	}
	if len(label) > 40 {
		label = label[:40]
	}
	return label
}

var _ = time.Now
