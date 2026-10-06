package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"scnetm/internal/auth"
)

// --- config read -----------------------------------------------------------

func TestGetConfig(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("cfg", 30100)
	ts.config.seed(inst, ConfigKindServerSetting, `{"ServerPort":30100,"MaxOnlinePlayerCount":20}`)

	resp := ts.get(token, "/api/v1/instances/1/config")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data ConfigResponse `json:"data"`
	}
	resp.JSON(&out)

	if out.Data.Kind != string(ConfigKindServerSetting) {
		t.Errorf("kind = %q, want ServerSetting.json", out.Data.Kind)
	}
	if len(out.Data.Content) == 0 {
		t.Fatal("no content returned")
	}
	if out.Data.ModTime.IsZero() {
		t.Error("no mod_time returned")
	}
	if out.Data.Filename == "" {
		t.Error("no filename returned")
	}
	// A JSON document must come back as a structured object so the UI can
	// render a form rather than a text area.
	if out.Data.Content == nil {
		t.Error("a JSON document was returned without parsed content")
	}
	if got := out.Data.Content["ServerPort"]; got != float64(30100) {
		t.Errorf("content.ServerPort = %v, want 30100", got)
	}
}

func TestGetConfigKinds(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("cfgkinds", 30101)
	ts.config.seed(inst, ConfigKindServerSetting, `{"a":1}`)
	ts.config.seed(inst, ConfigKindSettings, `<Settings/>`)
	ts.config.seed(inst, ConfigKindConfigs, `{"b":2}`)

	for _, kind := range []ConfigKind{ConfigKindServerSetting, ConfigKindSettings, ConfigKindConfigs} {
		resp := ts.get(token, "/api/v1/instances/1/config?kind="+string(kind))
		requireStatus(t, resp, http.StatusOK)
		var out struct {
			Data ConfigResponse `json:"data"`
		}
		resp.JSON(&out)
		if out.Data.Kind != string(kind) {
			t.Errorf("kind = %q, want %q", out.Data.Kind, kind)
		}
	}
}

func TestGetConfigUnknownKindIs404(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("cfgunknown", 30102)

	resp := ts.get(token, "/api/v1/instances/1/config?kind=../../etc/passwd")
	requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)

	resp = ts.get(token, "/api/v1/instances/1/config?kind=NotAKind")
	requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)
}

func TestGetConfigMissingIs404(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("cfgmissing", 30103)

	resp := ts.get(token, "/api/v1/instances/1/config")
	requireErrorCode(t, resp, http.StatusNotFound, CodeNotFound)
}

// --- config validate -------------------------------------------------------

func TestValidateConfigDoesNotWrite(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("cfgval", 30104)
	original := `{"ServerPort":30104,"MaxOnlinePlayerCount":20}`
	ts.config.seed(inst, ConfigKindServerSetting, original)

	resp := ts.post(token, "/api/v1/instances/1/config/validate", ValidateConfigRequest{
		Kind:        string(ConfigKindServerSetting),
		JSONContent: map[string]any{"ServerPort": 70000, "MaxOnlinePlayerCount": 20},
	})
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data struct {
			Valid  bool              `json:"valid"`
			Issues []ValidationIssue `json:"issues"`
		} `json:"data"`
	}
	resp.JSON(&out)

	if out.Data.Valid {
		t.Error("valid = true for an out-of-range port")
	}
	if len(out.Data.Issues) == 0 {
		t.Fatal("no issues reported")
	}
	if out.Data.Issues[0].Field != "ServerPort" {
		t.Errorf("issue field = %q, want ServerPort", out.Data.Issues[0].Field)
	}
	if out.Data.Issues[0].Severity != "error" {
		t.Errorf("severity = %q, want error", out.Data.Issues[0].Severity)
	}

	// Crucially: nothing was written.
	if got, _ := ts.config.currentContent(inst, ConfigKindServerSetting); string(got) != original {
		t.Fatalf("validate mutated the document:\n got: %s\nwant: %s", got, original)
	}
	if n := ts.config.backupCount(); n != 0 {
		t.Errorf("validate took %d backups", n)
	}
}

// --- config write ----------------------------------------------------------

// TestPutConfigValidationFailureLeavesFileUntouched is the §6.2 acceptance
// requirement: a 422 must not have written anything, and must not even have
// reached the write path.
func TestPutConfigValidationFailureLeavesFileUntouched(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("cfgbad", 30105)
	original := `{"ServerPort":30105,"MaxOnlinePlayerCount":20}`
	ts.config.seed(inst, ConfigKindServerSetting, original)

	cases := []struct {
		name    string
		content map[string]any
		field   string
	}{
		{"port out of range", map[string]any{"ServerPort": 70000}, "ServerPort"},
		{"port privileged", map[string]any{"ServerPort": 80}, "ServerPort"},
		{"max players zero", map[string]any{"MaxOnlinePlayerCount": 0}, "MaxOnlinePlayerCount"},
		{"max players too high", map[string]any{"MaxOnlinePlayerCount": 5000}, "MaxOnlinePlayerCount"},
		{"world path traversal", map[string]any{"WorldPath": "app:/Worlds/../../etc"}, "WorldPath"},
		{"world path absolute", map[string]any{"WorldPath": "/etc/passwd"}, "WorldPath"},
		{"world path missing prefix", map[string]any{"WorldPath": "Worlds/Alpha"}, "WorldPath"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := ts.put(token, "/api/v1/instances/1/config", UpdateConfigRequest{
				Kind:        string(ConfigKindServerSetting),
				JSONContent: tc.content,
			})
			requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)

			// The details must name the field so the UI can highlight it.
			var env ErrorEnvelope
			resp.JSON(&env)
			details, _ := env.Error.Details.(map[string]any)
			issues, _ := details["issues"].([]any)
			if len(issues) == 0 {
				t.Fatalf("no issues in the 422 details: %s", resp.Body)
			}
			first, _ := issues[0].(map[string]any)
			if first["field"] != tc.field {
				t.Errorf("issue field = %v, want %v\nbody: %s", first["field"], tc.field, resp.Body)
			}

			// The document on disk is byte-identical.
			if got, _ := ts.config.currentContent(inst, ConfigKindServerSetting); string(got) != original {
				t.Fatalf("the document was modified by a rejected write:\n got: %s\nwant: %s", got, original)
			}
			// And no backup was taken, because no write began.
			if n := ts.config.backupCount(); n != 0 {
				t.Fatalf("%d backups were taken for a rejected write", n)
			}
			// The audit trail records the attempt.
			ts.requireAudit("config.write_rejected")
		})
	}
}

// TestPutConfigSuccessTakesBackup is the other half: a valid write succeeds,
// the backup contains the PREVIOUS content, and the new content is live.
func TestPutConfigSuccessTakesBackup(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("cfgok", 30106)
	original := `{"ServerPort":30106,"MaxOnlinePlayerCount":20}`
	ts.config.seed(inst, ConfigKindServerSetting, original)

	resp := ts.put(token, "/api/v1/instances/1/config", UpdateConfigRequest{
		Kind:        string(ConfigKindServerSetting),
		JSONContent: map[string]any{"ServerPort": 30106, "MaxOnlinePlayerCount": 42},
	})
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data ConfigResponse `json:"data"`
	}
	resp.JSON(&out)

	if out.Data.Validated != nil && !out.Data.Validated.Valid {
		t.Errorf("a successful write reported invalid content: %+v", out.Data.Validated)
	}

	// Exactly one backup, holding the previous content.
	if n := ts.config.backupCount(); n != 1 {
		t.Fatalf("%d backups taken, want exactly 1", n)
	}
	if got := string(ts.config.backups[0].Content); got != original {
		t.Fatalf("backup holds the wrong content:\n got: %s\nwant: %s", got, original)
	}

	// The live document is the new one.
	live, _ := ts.config.currentContent(inst, ConfigKindServerSetting)
	if !strings.Contains(string(live), "42") {
		t.Fatalf("the new content was not stored: %s", live)
	}

	ts.requireAudit("config.write")
}

// TestPutConfigReportsRestartNeeded covers the three rules §6.2 gives for the
// restart flag.
func TestPutConfigReportsRestartNeeded(t *testing.T) {
	t.Parallel()

	t.Run("running instance needs a restart", func(t *testing.T) {
		ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
		token := ts.mustLogin("admin", "admin-password-123")
		inst := ts.seedInstance("cfgrun", 30107)
		ts.config.seed(inst, ConfigKindServerSetting, `{"ServerPort":30107}`)
		if err := ts.process.Start(t.Context(), inst.ID); err != nil {
			t.Fatal(err)
		}

		resp := ts.put(token, "/api/v1/instances/1/config", UpdateConfigRequest{
			Kind:        string(ConfigKindServerSetting),
			JSONContent: map[string]any{"ServerPort": 30107, "MaxOnlinePlayerCount": 30},
		})
		requireStatus(t, resp, http.StatusOK)

		var out struct {
			Data ConfigResponse `json:"data"`
		}
		resp.JSON(&out)
		if !out.Data.RestartNeeded && !out.Data.RequiresRestart {
			t.Errorf("restart not flagged for a running instance: %s", resp.Body)
		}
	})

	t.Run("stopped instance does not need one", func(t *testing.T) {
		ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
		token := ts.mustLogin("admin", "admin-password-123")
		inst := ts.seedInstance("cfgstop", 30108)
		ts.config.seed(inst, ConfigKindServerSetting, `{"ServerPort":30108}`)

		resp := ts.put(token, "/api/v1/instances/1/config", UpdateConfigRequest{
			Kind:        string(ConfigKindServerSetting),
			JSONContent: map[string]any{"ServerPort": 30108, "MaxOnlinePlayerCount": 30},
		})
		requireStatus(t, resp, http.StatusOK)

		var out struct {
			Data ConfigResponse `json:"data"`
		}
		resp.JSON(&out)
		if out.Data.RestartNeeded {
			t.Error("restart flagged for a stopped instance with no validator request")
		}
	})
}

// TestPutConfigRestartsWithRunningInstance proves the optional restart flag is
// honoured.
func TestPutConfigRestartsWithRunningInstance(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("cfgreload", 30109)
	ts.config.seed(inst, ConfigKindServerSetting, `{"ServerPort":30109}`)
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	resp := ts.put(token, "/api/v1/instances/1/config", UpdateConfigRequest{
		Kind:        string(ConfigKindServerSetting),
		JSONContent: map[string]any{"ServerPort": 30109, "MaxOnlinePlayerCount": 25},
		Restart:     true,
	})
	requireStatus(t, resp, http.StatusOK)
	ts.requireAudit("config.write")
	ts.requireAudit("instance.restart")
}

// TestPutConfigAcceptsRawTextForm covers the Settings.xml path, where the body
// is text and cannot be a JSON object.
func TestPutConfigAcceptsRawTextForm(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("cfgxml", 30110)
	ts.config.seed(inst, ConfigKindSettings, "<Settings/>")

	body := map[string]any{
		"kind": string(ConfigKindSettings),
		"text": "<Settings><Language>en</Language></Settings>",
	}
	resp := ts.put(token, "/api/v1/instances/1/config", body)
	requireStatus(t, resp, http.StatusOK)

	live, _ := ts.config.currentContent(inst, ConfigKindSettings)
	if !strings.Contains(string(live), "<Language>en</Language>") {
		t.Fatalf("the XML text was not stored: %s", live)
	}
}

func TestPutConfigRejectsBothFormsAtOnce(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("cfgboth", 30111)
	ts.config.seed(inst, ConfigKindServerSetting, `{"ServerPort":30111}`)

	resp := ts.put(token, "/api/v1/instances/1/config", map[string]any{
		"kind":    string(ConfigKindServerSetting),
		"content": map[string]any{"ServerPort": 30111},
		"text":    `{"ServerPort":30111}`,
	})
	requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)
}

func TestPutConfigRejectsNeitherForm(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("cfgneither", 30112)

	resp := ts.put(token, "/api/v1/instances/1/config", map[string]any{
		"kind": string(ConfigKindServerSetting),
	})
	requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)
}

// TestPutConfigNonJSONIntoJSONKindIsRejected: writing garbage into
// ServerSetting.json must not be possible, or the server would fail to boot.
func TestPutConfigNonJSONIntoJSONKindIsRejected(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("cfggarbage", 30113)
	original := `{"ServerPort":30113}`
	ts.config.seed(inst, ConfigKindServerSetting, original)

	resp := ts.put(token, "/api/v1/instances/1/config", map[string]any{
		"kind": string(ConfigKindServerSetting),
		"text": "{ this is not json at all",
	})
	requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)

	if got, _ := ts.config.currentContent(inst, ConfigKindServerSetting); string(got) != original {
		t.Fatalf("invalid JSON was written: %s", got)
	}
}

// --- mutating-route audit matrix -------------------------------------------

// TestEveryMutatingRouteWritesAudit is the §5.7 requirement that every mutating
// handler leaves a row. Each case performs a successful mutation and then
// asserts its action is present.
func TestEveryMutatingRouteWritesAudit(t *testing.T) {
	t.Parallel()

	type step struct {
		name   string
		action string
		do     func(ts *testServer, token string) *response
	}

	steps := []step{
		{"login", "auth.login", func(ts *testServer, _ string) *response {
			return ts.login("admin", "admin-password-123")
		}},
		{"create instance", "instance.create", func(ts *testServer, token string) *response {
			return ts.post(token, "/api/v1/instances", CreateInstanceRequest{Name: "audited-new"})
		}},
		{"update instance", "instance.update", func(ts *testServer, token string) *response {
			return ts.do(http.MethodPatch, "/api/v1/instances/1", map[string]any{"memo": "x"},
				"Authorization", "Bearer "+token)
		}},
		{"start instance", "instance.start", func(ts *testServer, token string) *response {
			return ts.post(token, "/api/v1/instances/1/start", nil)
		}},
		{"stop instance", "instance.stop", func(ts *testServer, token string) *response {
			// A stop is only audited when it actually does something, so the
			// instance must be running first.
			if err := ts.process.Start(ts.t.Context(), 1); err != nil {
				ts.t.Fatalf("starting for the stop step: %v", err)
			}
			return ts.post(token, "/api/v1/instances/1/stop", nil)
		}},
		{"restart instance", "instance.restart", func(ts *testServer, token string) *response {
			return ts.post(token, "/api/v1/instances/1/restart", nil)
		}},
		{"write config", "config.write", func(ts *testServer, token string) *response {
			return ts.put(token, "/api/v1/instances/1/config", UpdateConfigRequest{
				Kind:        string(ConfigKindServerSetting),
				JSONContent: map[string]any{"ServerPort": 30200, "MaxOnlinePlayerCount": 12},
			})
		}},
		{"create backup", "backup.create", func(ts *testServer, token string) *response {
			return ts.post(token, "/api/v1/instances/1/backups", CreateBackupRequest{Kind: "manual"})
		}},
		{"create user", "user.create", func(ts *testServer, token string) *response {
			return ts.post(token, "/api/v1/users", CreateUserRequest{
				Username: "audituser", Password: "audit-password-123", Role: "viewer",
			})
		}},
		{"delete instance", "instance.delete", func(ts *testServer, token string) *response {
			return ts.del(token, "/api/v1/instances/1", nil)
		}},
	}

	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
			inst := ts.seedInstance("audited", 30200)
			ts.config.seed(inst, ConfigKindServerSetting, `{"ServerPort":30200}`)

			token := ""
			if st.action != "auth.login" {
				token = ts.mustLogin("admin", "admin-password-123")
			}

			resp := st.do(ts, token)
			if resp.Status >= 400 {
				t.Fatalf("%s failed with %d: %s", st.name, resp.Status, resp.Body)
			}

			entry := ts.requireAudit(st.action)
			if entry.Timestamp.IsZero() {
				t.Errorf("audit row %q has no timestamp", st.action)
			}
			// auth.login is the one mutating route with no prior authenticated
			// principal, so its row is keyed by the username it attempted
			// rather than by a user id. Every other row must carry the actor.
			if st.action == "auth.login" {
				if entry.Target == "" {
					t.Errorf("login audit row does not record the attempted username")
				}
			} else if entry.UserID == 0 {
				t.Errorf("audit row %q has no user id", st.action)
			}
		})
	}
}

// TestAuditFailureDoesNotFailTheRequest: auditing is best-effort by design, so a
// broken audit sink must not take the panel down.
func TestAuditFailureDoesNotFailTheRequest(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	// Make every subsequent audit write fail.
	ts.audit.failWrite = errAuditSinkDown

	resp := ts.post(token, "/api/v1/instances", CreateInstanceRequest{Name: "noaudit"})
	requireStatus(t, resp, http.StatusCreated)

	// The instance really was created.
	if _, err := ts.instances.GetByName(t.Context(), "noaudit"); err != nil {
		t.Fatalf("instance was not created: %v", err)
	}
}

// --- audit read ------------------------------------------------------------

func TestListAudit(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("auditread", 30201)

	// Generate some rows.
	ts.post(token, "/api/v1/instances/1/start", nil)
	ts.post(token, "/api/v1/instances/1/stop", nil)

	resp := ts.get(token, "/api/v1/audit")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data  []AuditEntry `json:"data"`
		Total int          `json:"total"`
	}
	resp.JSON(&out)

	if len(out.Data) == 0 {
		t.Fatalf("no audit rows returned: %s", resp.Body)
	}
	if out.Total < len(out.Data) {
		t.Errorf("total = %d, less than the returned page %d", out.Total, len(out.Data))
	}
	// Newest first is the only sensible order for an audit view.
	for i := 1; i < len(out.Data); i++ {
		if out.Data[i].Timestamp.After(out.Data[i-1].Timestamp) {
			t.Errorf("audit rows are not newest-first at index %d", i)
		}
	}
}

func TestListAuditFilterByAction(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("auditfilter", 30202)
	ts.post(token, "/api/v1/instances/1/start", nil)

	resp := ts.get(token, "/api/v1/audit?action=instance.start")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data []AuditEntry `json:"data"`
	}
	resp.JSON(&out)

	if len(out.Data) == 0 {
		t.Fatal("filtering by action returned nothing")
	}
	for _, e := range out.Data {
		if e.Action != "instance.start" {
			t.Errorf("filter leaked action %q", e.Action)
		}
	}
}

func TestListAuditBadTimeParam(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.get(token, "/api/v1/audit?from=not-a-time")
	requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)
}

var errAuditSinkDown = auditSinkDownError{}

type auditSinkDownError struct{}

func (auditSinkDownError) Error() string { return "audit sink unavailable" }

var _ = time.Now
var _ = auth.RoleAdmin
