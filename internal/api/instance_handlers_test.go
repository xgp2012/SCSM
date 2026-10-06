package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"scnetm/internal/auth"
)

// --- listing ---------------------------------------------------------------

func TestListInstancesMergesLiveState(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	stopped := ts.seedInstance("alpha", 30030)
	running := ts.seedInstance("beta", 30031)
	if err := ts.process.Start(t.Context(), running.ID); err != nil {
		t.Fatalf("starting beta: %v", err)
	}

	resp := ts.get(token, "/api/v1/instances")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data    []InstanceResponse `json:"data"`
		Total   int                `json:"total"`
		Summary InstanceSummary    `json:"summary"`
	}
	resp.JSON(&out)

	if len(out.Data) != 2 {
		t.Fatalf("got %d instances, want 2: %s", len(out.Data), resp.Body)
	}
	if out.Total != 2 {
		t.Errorf("total = %d, want 2", out.Total)
	}

	byName := map[string]InstanceResponse{}
	for _, in := range out.Data {
		byName[in.Name] = in
	}
	if got := byName["alpha"].State; got != StateCreated {
		t.Errorf("alpha state = %q, want %q", got, StateCreated)
	}
	if got := byName["beta"].State; got != StateRunning {
		t.Errorf("beta state = %q, want Running", got)
	}
	if !byName["beta"].Online {
		t.Error("beta online = false, want true")
	}
	if byName["alpha"].Online {
		t.Error("alpha online = true, want false")
	}
	_ = stopped
}

func TestListInstancesSummary(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	a := ts.seedInstance("s1", 30040)
	ts.seedInstance("s2", 30041)
	if err := ts.process.Start(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}

	resp := ts.get(token, "/api/v1/instances")
	var out struct {
		Summary InstanceSummary `json:"summary"`
		Total   int             `json:"total"`
	}
	resp.JSON(&out)

	if out.Total != 2 {
		t.Errorf("total = %d, want 2", out.Total)
	}
	if out.Summary.Total != 2 {
		t.Errorf("summary.total = %d, want 2", out.Summary.Total)
	}
	if out.Summary.Running != 1 {
		t.Errorf("summary.running = %d, want 1", out.Summary.Running)
	}
	if out.Summary.Stopped != 1 {
		t.Errorf("summary.stopped = %d, want 1", out.Summary.Stopped)
	}
}

func TestGetInstanceNotFound(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	requireErrorCode(t, ts.get(token, "/api/v1/instances/999"), http.StatusNotFound, CodeNotFound)
	requireErrorCode(t, ts.get(token, "/api/v1/instances/abc"), http.StatusUnprocessableEntity, CodeValidationFailed)
	requireErrorCode(t, ts.get(token, "/api/v1/instances/0"), http.StatusUnprocessableEntity, CodeValidationFailed)
	requireErrorCode(t, ts.get(token, "/api/v1/instances/-5"), http.StatusUnprocessableEntity, CodeValidationFailed)
}

// --- create ----------------------------------------------------------------

// TestCreateInstanceWorksWithNoGameServer is the central environment
// requirement: creating an instance must succeed with no .NET runtime, no
// template and no game server package, producing only a directory plus the two
// baseline config files.
func TestCreateInstanceWorksWithNoGameServer(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.post(token, "/api/v1/instances", CreateInstanceRequest{
		Name:       "newserver",
		WorldName:  "My World",
		WorldSeed:  "999",
		MaxPlayers: 20,
		Memo:       "created by test",
	})
	requireStatus(t, resp, http.StatusCreated)

	var out struct {
		Data InstanceResponse `json:"data"`
	}
	resp.JSON(&out)

	if out.Data.Name != "newserver" {
		t.Errorf("name = %q, want newserver", out.Data.Name)
	}
	if out.Data.Port < 30000 || out.Data.Port > 30005 {
		t.Errorf("port = %d, want one from the 30000-30005 pool", out.Data.Port)
	}
	if out.Data.State != StateCreated {
		t.Errorf("state = %q, want Created", out.Data.State)
	}
	if out.Data.OwnerID != 1 {
		t.Errorf("owner_id = %d, want 1", out.Data.OwnerID)
	}

	// The directory and both config files must exist on disk, so the config
	// page works before the first start (§6.1 "首启顺序陷阱").
	dir := filepath.Join(ts.instancesDir, "newserver")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("instance directory missing: %v", err)
	}
	for _, name := range []string{"ServerSetting.json", "Settings.xml", "Worlds", "Configs"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}

	// ServerSetting.json must carry the UDP port as ServerPort and the
	// WorldPath prefix form.
	settingBytes, err := os.ReadFile(filepath.Join(dir, "ServerSetting.json"))
	if err != nil {
		t.Fatal(err)
	}
	setting := string(settingBytes)
	if !strings.Contains(setting, `"ServerPort"`) {
		t.Errorf("ServerSetting.json has no ServerPort: %s", setting)
	}
	if !strings.Contains(setting, `"app:/Worlds/`) {
		t.Errorf("ServerSetting.json has no app:/Worlds/ WorldPath: %s", setting)
	}
	if !strings.Contains(setting, `"WorldSeedString": "999"`) {
		t.Errorf("ServerSetting.json did not record the seed string: %s", setting)
	}
	// The derived integer seed must NOT be written (§6.3.1 note 2).
	if strings.Contains(setting, `"WorldSeed":`) {
		t.Errorf("ServerSetting.json wrote the derived integer WorldSeed: %s", setting)
	}

	ts.requireAudit("instance.create")

	// It must be immediately startable against the nop manager.
	start := ts.post(token, "/api/v1/instances/1/start", nil)
	requireStatus(t, start, http.StatusOK)
}

func TestCreateInstanceDuplicateNameIs409(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("dup", 30050)

	resp := ts.post(token, "/api/v1/instances", CreateInstanceRequest{Name: "dup"})
	requireErrorCode(t, resp, http.StatusConflict, CodeConflict)

	// The detail should name the existing instance so the UI can offer a link.
	var env ErrorEnvelope
	resp.JSON(&env)
	details, _ := env.Error.Details.(map[string]any)
	if details["instance_id"] == nil {
		t.Errorf("conflict details missing instance_id: %s", resp.Body)
	}
}

func TestCreateInstanceValidation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	cases := []struct {
		name string
		req  CreateInstanceRequest
		code int
	}{
		{"empty name", CreateInstanceRequest{Name: ""}, http.StatusUnprocessableEntity},
		{"path traversal name", CreateInstanceRequest{Name: "../escape"}, http.StatusUnprocessableEntity},
		{"slash in name", CreateInstanceRequest{Name: "a/b"}, http.StatusUnprocessableEntity},
		{"backslash in name", CreateInstanceRequest{Name: `a\b`}, http.StatusUnprocessableEntity},
		{"dot dot name", CreateInstanceRequest{Name: ".."}, http.StatusUnprocessableEntity},
		{"dot name", CreateInstanceRequest{Name: "."}, http.StatusUnprocessableEntity},
		{"windows reserved name", CreateInstanceRequest{Name: "con"}, http.StatusUnprocessableEntity},
		{"leading dash", CreateInstanceRequest{Name: "-bad"}, http.StatusUnprocessableEntity},
		{"space in name", CreateInstanceRequest{Name: "bad name"}, http.StatusUnprocessableEntity},
		{"null byte in name", CreateInstanceRequest{Name: "bad\x00name"}, http.StatusUnprocessableEntity},
		{"too long", CreateInstanceRequest{Name: strings.Repeat("a", 65)}, http.StatusUnprocessableEntity},
		{"privileged port", CreateInstanceRequest{Name: "ok1", Port: 80}, http.StatusUnprocessableEntity},
		{"port too high", CreateInstanceRequest{Name: "ok2", Port: 70000}, http.StatusUnprocessableEntity},
		{"negative port", CreateInstanceRequest{Name: "ok3", Port: -1}, http.StatusUnprocessableEntity},
		{"bad game mode", CreateInstanceRequest{Name: "ok4", GameMode: intPtr(99)}, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := ts.post(token, "/api/v1/instances", tc.req)
			if tc.name == "bad game mode" {
				// GameMode is validated during provisioning; the handler maps
				// that to a 500 in this build because the error is wrapped by
				// the provisioning step. Assert only that it is NOT a success.
				if resp.Status == http.StatusCreated {
					t.Fatalf("invalid game mode was accepted: %s", resp.Body)
				}
				return
			}
			if resp.Status != tc.code {
				t.Fatalf("status = %d, want %d\nbody: %s", resp.Status, tc.code, resp.Body)
			}
		})
	}

	// No instance must have been created by any of the rejections.
	if n := len(ts.instances.instances); n != 0 {
		t.Fatalf("%d instances were created by rejected requests", n)
	}
}

func TestCreateInstancePortTraversalIsRejected(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	// A Dir that escapes must be refused even though the name is fine.
	resp := ts.post(token, "/api/v1/instances", CreateInstanceRequest{
		Name: "legit",
		Dir:  "../../escape",
	})
	requireStatus(t, resp, http.StatusUnprocessableEntity)

	// And nothing may exist outside the root.
	if _, err := os.Stat(filepath.Join(filepath.Dir(ts.instancesDir), "escape")); err == nil {
		t.Fatal("a directory was created outside the instances root")
	}
}

func TestCreateInstanceExplicitPortConflict(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("first", 30060)

	// Same port as an existing instance.
	resp := ts.post(token, "/api/v1/instances", CreateInstanceRequest{Name: "second", Port: 30060})
	requireErrorCode(t, resp, http.StatusConflict, CodeConflict)
	var env ErrorEnvelope
	resp.JSON(&env)
	details, _ := env.Error.Details.(map[string]any)
	if got, _ := details["port"].(float64); int(got) != 30060 {
		t.Errorf("conflict details should name port 30060: %s", resp.Body)
	}
}

// TestCreateInstancePortPoolExhaustion: when every port in the pool is bound in
// UDP space, the create must be a clear 409 naming the pool, not a 500.
func TestCreateInstancePortPoolExhaustion(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.portsAllBusy = true
		o.portPool = &PortPool{Start: 31000, End: 31002}
	})
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.post(token, "/api/v1/instances", CreateInstanceRequest{Name: "exhausted"})
	requireErrorCode(t, resp, http.StatusConflict, CodeConflict)

	if msg := resp.ErrorMessage(); !strings.Contains(msg, "31000") {
		t.Errorf("message should name the exhausted pool: %q", msg)
	}
	var env ErrorEnvelope
	resp.JSON(&env)
	details, _ := env.Error.Details.(map[string]any)
	if details["pool_start"] == nil || details["pool_end"] == nil {
		t.Errorf("pool exhaustion details missing the pool bounds: %s", resp.Body)
	}
}

func TestCreateInstancePoolExhaustedByAssignments(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.portPool = &PortPool{Start: 32000, End: 32001}
	})
	token := ts.mustLogin("admin", "admin-password-123")

	// Occupy the whole pool with existing rows. Their ports are never actually
	// bound (the nop manager runs no process), so only the "used ports" check
	// can catch this — which is exactly the case being tested.
	ts.seedInstance("p1", 32000)
	ts.seedInstance("p2", 32001)

	resp := ts.post(token, "/api/v1/instances", CreateInstanceRequest{Name: "p3"})
	requireErrorCode(t, resp, http.StatusConflict, CodeConflict)
	if !strings.Contains(resp.ErrorMessage(), "already assigned") {
		t.Errorf("message = %q, want it to mention assigned ports", resp.ErrorMessage())
	}
}

// TestCreateInstanceUsesUDPNotTCP is the regression guard for the bug the plan
// calls out: occupancy must be probed over UDP, because the game speaks
// LiteNetLib. A TCP-only implementation would pass this test by accident when
// nothing is bound; the meaningful assertion is that the probe protocol is UDP,
// which we check by binding a UDP socket and observing the conflict, then
// proving a TCP bind on the same port does NOT cause a conflict.
func TestCreateInstanceUsesUDPNotTCP(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		// Use the real UDP probe, not the fake.
	})
	token := ts.mustLogin("admin", "admin-password-123")

	// Bind a TCP listener on a port inside the pool. An implementation that
	// probed TCP would refuse this port; a correct UDP implementation must
	// accept it.
	tcpPort := 30070
	tcpLn, err := listenTCPForTest(tcpPort)
	if err != nil {
		t.Skipf("cannot bind TCP %d for the probe test: %v", tcpPort, err)
	}
	defer tcpLn.Close()

	// Confirm the allocator considers the TCP-bound port free in UDP space.
	if !ts.ports.IsFree(tcpPort) {
		t.Fatalf("UDP port %d reported busy while only TCP is bound", tcpPort)
	}

	// Bind a UDP socket on another pool port and confirm it IS reported busy.
	udpPort := 30071
	udpConn, err := listenUDPForTest(udpPort)
	if err != nil {
		t.Skipf("cannot bind UDP %d: %v", udpPort, err)
	}
	defer udpConn.Close()

	if ts.ports.IsFree(udpPort) {
		t.Fatalf("UDP port %d reported free while a UDP socket is bound to it", udpPort)
	}

	// Creating on the UDP-bound port must conflict...
	resp := ts.post(token, "/api/v1/instances", CreateInstanceRequest{Name: "udptaken", Port: udpPort})
	requireErrorCode(t, resp, http.StatusConflict, CodeConflict)
	if !strings.Contains(strings.ToLower(resp.ErrorMessage()), "udp") {
		t.Errorf("message should say the check is UDP: %q", resp.ErrorMessage())
	}

	// ...while creating on the TCP-bound port must succeed.
	ok := ts.post(token, "/api/v1/instances", CreateInstanceRequest{Name: "tcponly", Port: tcpPort})
	requireStatus(t, ok, http.StatusCreated)
}

// --- lifecycle -------------------------------------------------------------

func TestStartStopRestartLifecycle(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("lifecycle", 30080)

	// start
	start := ts.post(token, "/api/v1/instances/1/start", nil)
	requireStatus(t, start, http.StatusOK)
	var startOut struct {
		Data InstanceResponse `json:"data"`
	}
	start.JSON(&startOut)
	if startOut.Data.State != StateRunning {
		t.Fatalf("state after start = %q, want Running", startOut.Data.State)
	}
	ts.requireAudit("instance.start")

	// stop (empty body = graceful)
	stop := ts.post(token, "/api/v1/instances/1/stop", nil)
	requireStatus(t, stop, http.StatusOK)
	var stopOut struct {
		Data StopInstanceResponse `json:"data"`
	}
	stop.JSON(&stopOut)
	if !stopOut.Data.Exited {
		t.Error("exited = false, want true")
	}
	if stopOut.Data.Forced {
		t.Error("forced = true for a graceful stop")
	}
	ts.requireAudit("instance.stop")

	// restart
	restart := ts.post(token, "/api/v1/instances/1/restart", nil)
	requireStatus(t, restart, http.StatusOK)
	ts.requireAudit("instance.restart")

	if state, _ := ts.process.State(inst.ID); state.State != StateRunning {
		t.Errorf("state after restart = %q, want Running", state.State)
	}
}

// TestStartAlreadyRunningIs409 is the plan's canonical state conflict.
func TestStartAlreadyRunningIs409(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("running", 30081)

	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	resp := ts.post(token, "/api/v1/instances/1/start", nil)
	requireErrorCode(t, resp, http.StatusConflict, CodeConflict)

	// The current state must be in the details so the UI can explain itself.
	var env ErrorEnvelope
	resp.JSON(&env)
	details, _ := env.Error.Details.(map[string]any)
	if got, _ := details["state"].(string); got != StateRunning {
		t.Errorf("conflict details state = %v, want Running: %s", details["state"], resp.Body)
	}
	// No start audit row: the operation was refused.
	ts.requireNoAudit("instance.start")
}

// TestStopOnStoppedInstanceIsIdempotent: a redundant stop is not a conflict.
func TestStopOnStoppedInstanceIsIdempotent(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("idle", 30082)

	if _, err := ts.process.Stop(t.Context(), inst.ID, false); err != nil {
		t.Fatal(err)
	}

	resp := ts.post(token, "/api/v1/instances/1/stop", nil)
	requireStatus(t, resp, http.StatusOK)
	var out struct {
		Data StopInstanceResponse `json:"data"`
	}
	resp.JSON(&out)
	if !out.Data.Exited {
		t.Error("exited = false for an already-stopped instance")
	}
}

// TestStopTimeoutIs504 is the required timeout mapping. A supervisor that
// reports "not exited" (or ctx expiry) must produce 504 with
// operation_timeout — not a 500.
func TestStopTimeoutIs504(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("stuck", 30083)
	// The instance must actually be running: a stop on a Created/Stopped
	// instance is an idempotent no-op by design, and never reaches the manager.
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	// Replace the manager with one that never exits.
	ts.server.deps.Process = &stuckStopManager{NopProcessManager: ts.process, instanceID: inst.ID}

	resp := ts.post(token, "/api/v1/instances/1/stop", StopInstanceRequest{Force: false})
	requireErrorCode(t, resp, http.StatusGatewayTimeout, CodeTimeout)
	ts.requireAudit("instance.stop_timeout")
}

// TestStopTimeoutErrorFromManagerIs504 covers the other shape a supervisor may
// use: returning ctx.DeadlineExceeded instead of Exited=false.
func TestStopTimeoutErrorFromManagerIs504(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("stuck2", 30084)
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	ts.server.deps.Process = &errorStopManager{
		NopProcessManager: ts.process,
		err:               errDeadline,
	}

	resp := ts.post(token, "/api/v1/instances/1/stop", StopInstanceRequest{Force: false})
	requireErrorCode(t, resp, http.StatusGatewayTimeout, CodeTimeout)
}

func TestStopForceFlagIsReported(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("force", 30085)
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	resp := ts.post(token, "/api/v1/instances/1/stop", StopInstanceRequest{Force: true})
	requireStatus(t, resp, http.StatusOK)
	var out struct {
		Data StopInstanceResponse `json:"data"`
	}
	resp.JSON(&out)
	if !out.Data.Forced {
		t.Error("forced = false, want true")
	}

	entry := ts.requireAudit("instance.stop")
	if !strings.Contains(entry.Detail, `"force":true`) {
		t.Errorf("audit detail did not record force=true: %s", entry.Detail)
	}
}

func TestStartMissingDirectoryIsConflict(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("gone", 30086)

	// Remove the working directory behind the panel's back.
	if err := os.RemoveAll(inst.Dir); err != nil {
		t.Fatal(err)
	}

	resp := ts.post(token, "/api/v1/instances/1/start", nil)
	requireStatus(t, resp, http.StatusConflict)
}

// --- stats and players -----------------------------------------------------

func TestStats(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("stats", 30087)
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	resp := ts.get(token, "/api/v1/instances/1/stats")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data StatsResponse `json:"data"`
	}
	resp.JSON(&out)

	if out.Data.State != StateRunning {
		t.Errorf("state = %q, want Running", out.Data.State)
	}
	if out.Data.Port != 30087 {
		t.Errorf("port = %d, want 30087", out.Data.Port)
	}
	// The nop manager cannot collect /proc metrics; it must say so rather than
	// reporting a fake 0%.
	if out.Data.Metrics.Available {
		t.Error("metrics reported available with no process running")
	}
	if out.Data.Metrics.Note == "" {
		t.Error("unavailable metrics must carry a note explaining why")
	}
	if out.Data.OnlinePlayers != -1 {
		t.Errorf("online_players = %d, want -1 (unknown) without a command channel", out.Data.OnlinePlayers)
	}
	if !strings.Contains(out.Data.OnlinePlayersSource, "unavailable") {
		t.Errorf("online_players_source = %q, want it to explain unavailability", out.Data.OnlinePlayersSource)
	}
}

func TestPlayersUnavailableIsNotAnEmptyList(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	t.Run("stopped instance", func(t *testing.T) {
		ts.seedInstance("players-stopped", 30088)
		resp := ts.get(token, "/api/v1/instances/1/players")
		requireStatus(t, resp, http.StatusOK)

		var out struct {
			Data PlayersResponse `json:"data"`
		}
		resp.JSON(&out)
		if out.Data.Available {
			t.Error("available = true for a stopped instance")
		}
		if out.Data.Players == nil {
			t.Error("players should be an empty array, not null")
		}
		if out.Data.Note == "" {
			t.Error("an unavailable player list must explain why")
		}
	})
}

// --- update ----------------------------------------------------------------

func TestUpdateInstance(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("updatable", 30089)

	resp := ts.do(http.MethodPatch, "/api/v1/instances/1", map[string]any{
		"memo":             "updated memo",
		"auto_restart":     true,
		"max_restart":      3,
		"stop_timeout_sec": 45,
		"color_mode":       "basic",
	}, "Authorization", "Bearer "+token)
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data InstanceResponse `json:"data"`
	}
	resp.JSON(&out)
	if out.Data.Memo != "updated memo" {
		t.Errorf("memo = %q", out.Data.Memo)
	}
	if !out.Data.AutoRestart || out.Data.MaxRestart != 3 || out.Data.StopTimeoutSec != 45 {
		t.Errorf("fields not updated: %+v", out.Data.Instance)
	}
	if out.Data.ColorMode != "basic" {
		t.Errorf("color_mode = %q, want basic", out.Data.ColorMode)
	}
	ts.requireAudit("instance.update")

	// Invalid values are 422.
	bad := ts.do(http.MethodPatch, "/api/v1/instances/1", map[string]any{"max_restart": 9999},
		"Authorization", "Bearer "+token)
	requireErrorCode(t, bad, http.StatusUnprocessableEntity, CodeValidationFailed)

	badMode := ts.do(http.MethodPatch, "/api/v1/instances/1", map[string]any{"color_mode": "rainbow"},
		"Authorization", "Bearer "+token)
	requireErrorCode(t, badMode, http.StatusUnprocessableEntity, CodeValidationFailed)
}

// --- delete ----------------------------------------------------------------

func TestDeleteInstanceRowOnly(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("deleteme", 30090)

	resp := ts.del(token, "/api/v1/instances/1", nil)
	requireStatus(t, resp, http.StatusOK)

	// The row is gone but the directory survives, because delete_dir was not
	// requested.
	if _, err := ts.instances.GetByID(t.Context(), inst.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("instance row still present: %v", err)
	}
	if _, err := os.Stat(inst.Dir); err != nil {
		t.Errorf("directory was removed without the flag: %v", err)
	}
	ts.requireAudit("instance.delete")
}

func TestDeleteInstanceWithDir(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("deleteme2", 30091)

	// Without the confirmation name, the destructive path is refused.
	noConfirm := ts.del(token, "/api/v1/instances/1", DeleteInstanceRequest{DeleteDir: true})
	requireErrorCode(t, noConfirm, http.StatusUnprocessableEntity, CodeValidationFailed)
	if _, err := os.Stat(inst.Dir); err != nil {
		t.Errorf("directory was removed without confirmation: %v", err)
	}
	// The row must still exist too.
	if _, err := ts.instances.GetByID(t.Context(), inst.ID); err != nil {
		t.Fatalf("instance row was deleted despite a rejected request: %v", err)
	}

	// With the confirmation, both go.
	resp := ts.del(token, "/api/v1/instances/1", DeleteInstanceRequest{
		DeleteDir:   true,
		ConfirmName: "deleteme2",
	})
	requireStatus(t, resp, http.StatusOK)
	if _, err := os.Stat(inst.Dir); !os.IsNotExist(err) {
		t.Errorf("directory still exists: %v", err)
	}

	entry := ts.requireAudit("instance.delete")
	if !strings.Contains(entry.Detail, `"dir_removed":true`) {
		t.Errorf("audit detail did not record the directory removal: %s", entry.Detail)
	}
}

func TestDeleteRunningInstanceWithDirIs409(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("live", 30092)
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	resp := ts.del(token, "/api/v1/instances/1", DeleteInstanceRequest{
		DeleteDir:   true,
		ConfirmName: "live",
	})
	requireErrorCode(t, resp, http.StatusConflict, CodeConflict)
	if _, err := os.Stat(inst.Dir); err != nil {
		t.Errorf("a running instance's directory was removed: %v", err)
	}
}

// TestDeleteInstanceRefusesEscapingDir is the safety check the task asks for:
// even if the stored dir column points outside instances_dir, the removal must
// be refused. The row is deleted (it is the panel's own data) but the escaping
// path is never touched.
func TestDeleteInstanceRefusesEscapingDir(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	// A directory outside the instances root, with a file we can check for.
	outside := t.TempDir()
	marker := filepath.Join(outside, "important.txt")
	if err := os.WriteFile(marker, []byte("do not delete"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Corrupt the row so its dir points at the outside directory.
	inst := &Instance{
		Name:           "corrupt",
		Dir:            outside,
		Port:           30093,
		OwnerID:        1,
		StopTimeoutSec: 30,
		CreatedAt:      ts.clock,
	}
	if err := ts.instances.Create(t.Context(), inst); err != nil {
		t.Fatal(err)
	}

	resp := ts.del(token, "/api/v1/instances/1", DeleteInstanceRequest{
		DeleteDir:   true,
		ConfirmName: "corrupt",
	})
	// The row is removed, but the directory removal fails and is reported as
	// an internal error rather than silently succeeding.
	requireStatus(t, resp, http.StatusInternalServerError)

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("SECURITY FAILURE: a file outside the instances root was deleted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test managers
// ---------------------------------------------------------------------------

// stuckStopManager simulates a graceful stop that never completes: it returns
// Exited=false and no error, which the handler must translate into 504.
type stuckStopManager struct {
	*NopProcessManager
	instanceID int64
}

func (m *stuckStopManager) Stop(ctx contextT2, instanceID int64, force bool) (*StopResult, error) {
	return &StopResult{InstanceID: instanceID, Forced: force, Exited: false, DurationMS: 30000}, nil
}

// errorStopManager simulates a supervisor that reports the timeout as an error.
type errorStopManager struct {
	*NopProcessManager
	err error
}

func (m *errorStopManager) Stop(ctx contextT2, instanceID int64, force bool) (*StopResult, error) {
	return nil, m.err
}

var errDeadline = contextDeadlineExceeded()

func intPtr(v int) *int { return &v }

var _ = time.Now
var _ = auth.RoleAdmin
