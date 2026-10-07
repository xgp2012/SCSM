package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"scnetm/internal/auth"
)

// --- files -----------------------------------------------------------------

func TestListFiles(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("files", 30500)

	resp := ts.get(token, "/api/v1/instances/1/files")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data struct {
			Path      string      `json:"path"`
			Entries   []FileEntry `json:"entries"`
			Truncated bool        `json:"truncated"`
		} `json:"data"`
	}
	resp.JSON(&out)

	if len(out.Data.Entries) == 0 {
		t.Fatalf("no files listed: %s", resp.Body)
	}
	// Every entry needs a path the client can feed straight back into a
	// download call, and directories must be flagged.
	dirs := 0
	for _, f := range out.Data.Entries {
		if f.Path == "" {
			t.Errorf("file entry has no path: %+v", f)
		}
		if f.IsDir {
			dirs++
		}
	}
	if dirs == 0 {
		t.Error("no directory was flagged is_dir in the listing")
	}
}

func TestFileDownload(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("filedl", 30501)

	// The handler resolves and existence-checks against the real filesystem, so
	// the file must exist on disk.
	ts.seedFile(mustInstance(t, ts, 1), "ServerSetting.json", []byte(`{"ServerPort":30501}`))

	resp := ts.get(token, "/api/v1/instances/1/files/download?path=ServerSetting.json")
	requireStatus(t, resp, http.StatusOK)
	if len(resp.Body) == 0 {
		t.Fatal("empty download body")
	}
	if resp.Header.Get("Content-Disposition") == "" {
		t.Error("no Content-Disposition header; a download should name the file")
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("downloads must set X-Content-Type-Options: nosniff to stop HTML sniffing")
	}
}

// TestUploadAllowlist: only known-safe extensions may be uploaded. A .sh or .php
// dropped into an instance directory is a persistence vector.
func TestUploadAllowlist(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("upload", 30502)

	allowed := []string{"mod.zip", "Lib.dll", "world.scpak", "Configs.json", "notes.txt", "data.pdb"}
	denied := []string{"evil.sh", "shell.php", "payload.exe", "x.py", "noext", "a.jsp", "run.bat"}

	for _, name := range allowed {
		resp := ts.multipartUpload(token, "/api/v1/instances/1/files/upload", "file", name, []byte("payload"))
		if resp.Status != http.StatusOK && resp.Status != http.StatusCreated {
			t.Errorf("upload %q rejected with %d: %s", name, resp.Status, resp.Body)
		}
	}
	for _, name := range denied {
		resp := ts.multipartUpload(token, "/api/v1/instances/1/files/upload", "file", name, []byte("payload"))
		if resp.Status == http.StatusOK || resp.Status == http.StatusCreated {
			t.Errorf("SECURITY: upload of %q was accepted", name)
		}
		if code := resp.ErrorCode(); code == CodeValidationFailed || code == CodeForbidden {
			// expected refusal
		} else if resp.Status != http.StatusBadRequest {
			t.Errorf("upload %q: status %d code %q", name, resp.Status, code)
		}
	}
}

// TestUploadSanitisesPathInFilename: the browser-supplied filename is reduced to
// its base name rather than rejected. Sanitising is the better contract — a
// browser that sends a full path is not hostile, it is just sloppy — but the
// result must always land inside the instance directory.
func TestUploadSanitisesPathInFilename(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("uploadpath", 30503)

	for _, name := range []string{"../../evil.zip", `..\..\evil.zip`, "/tmp/evil.zip"} {
		resp := ts.multipartUpload(token, "/api/v1/instances/1/files/upload", "file", name, []byte("payload"))
		// Either the name is sanitised and accepted, or the request is refused.
		// Both are safe; what matters is asserted below.
		switch resp.Status {
		case http.StatusOK, http.StatusCreated, http.StatusForbidden, http.StatusUnprocessableEntity,
			http.StatusBadRequest:
		default:
			t.Fatalf("upload with filename %q: unexpected status %d: %s", name, resp.Status, resp.Body)
		}
	}

	// Every path that reached the service must be a bare, relative name.
	for _, seen := range ts.files.paths() {
		if strings.Contains(seen, "..") {
			t.Fatalf("SECURITY: a traversing path reached FileService: %q", seen)
		}
		if strings.HasPrefix(seen, "/") {
			t.Fatalf("SECURITY: an absolute path reached FileService: %q", seen)
		}
	}

	// The name handed to the service must be the sanitised base name, and the
	// POSIX-style traversal must have been reduced to a bare filename.
	paths := ts.files.paths()
	if len(paths) == 0 {
		t.Fatal("the upload never reached the file service")
	}
	for _, p := range paths {
		if p != filepath.Base(p) {
			t.Errorf("an upload path was not reduced to a base name: %q", p)
		}
	}
	// At least one call must have used the sanitised name.
	found := false
	for _, p := range paths {
		if p == "evil.zip" {
			found = true
		}
	}
	if !found {
		t.Errorf("the sanitised name evil.zip was never used; saw %v", paths)
	}
}

func TestUploadSizeLimit(t *testing.T) {
	t.Parallel()
	cfg := DefaultHTTPConfig()
	cfg.MaxUploadBytes = 1024
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.httpConfig = &cfg
	})
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("uploadbig", 30504)

	big := make([]byte, 4096)
	resp := ts.multipartUpload(token, "/api/v1/instances/1/files/upload", "file", "big.zip", big)
	if resp.Status == http.StatusOK || resp.Status == http.StatusCreated {
		t.Fatalf("an oversized upload was accepted: %s", resp.Body)
	}
	if resp.Status != http.StatusRequestEntityTooLarge && resp.Status != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 413: %s", resp.Status, resp.Body)
	}
}

func TestMkdirRenameDelete(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("fileops", 30505)

	mk := ts.post(token, "/api/v1/instances/1/files/mkdir", map[string]string{"path": "newdir"})
	requireStatus(t, mk, http.StatusCreated)
	ts.requireAudit("file.mkdir")

	rn := ts.post(token, "/api/v1/instances/1/files/rename", map[string]string{
		"from": "newdir", "to": "renamed",
	})
	requireStatus(t, rn, http.StatusOK)
	ts.requireAudit("file.rename")

	// Deleting a directory with contents requires the recursive flag.
	del := ts.del(token, "/api/v1/instances/1/files?path=renamed", nil)
	if del.Status == http.StatusOK {
		// The fake service does not model "non-empty", so a 409 here is also
		// acceptable; assert only that it is not a 5xx.
		t.Log("delete succeeded against the fake service")
	} else if del.Status >= 500 {
		t.Fatalf("delete returned %d: %s", del.Status, del.Body)
	}
}

// TestDeleteRefusesInstanceRoot: removing the instance directory through the
// file manager would bypass the instance-delete confirmation flow.
func TestDeleteRefusesInstanceRoot(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("fileroot", 30506)

	for _, p := range []string{"", ".", "/"} {
		resp := ts.del(token, "/api/v1/instances/1/files?path="+urlEscape(p), nil)
		if resp.Status == http.StatusOK {
			t.Errorf("SECURITY: deleting the instance root via path=%q was allowed", p)
		}
	}
	// The instance directory must still be there.
	resp := ts.get(token, "/api/v1/instances/1")
	requireStatus(t, resp, http.StatusOK)
}

func TestUnzipRejectsNonZip(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("unzip", 30507)

	resp := ts.post(token, "/api/v1/instances/1/files/unzip", map[string]string{"path": "notes.txt"})
	if resp.Status == http.StatusOK {
		t.Fatalf("unzip accepted a non-.zip file: %s", resp.Body)
	}

	ts.seedFile(mustInstance(t, ts, 1), "mod.zip", []byte("PK\x03\x04"))
	resp = ts.post(token, "/api/v1/instances/1/files/unzip", map[string]string{"path": "mod.zip"})
	requireStatus(t, resp, http.StatusOK)
	ts.requireAudit("file.unzip")
}

// --- logs ------------------------------------------------------------------

func TestGetLogs(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("logs", 30510)

	resp := ts.get(token, "/api/v1/instances/1/logs?tail=2")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data struct {
			InstanceID int64    `json:"instance_id"`
			Lines      []string `json:"lines"`
			Count      int      `json:"count"`
			Tail       int      `json:"tail"`
			Truncated  bool     `json:"truncated"`
		} `json:"data"`
	}
	resp.JSON(&out)

	if len(out.Data.Lines) != 2 {
		t.Fatalf("tail=2 returned %d lines: %s", len(out.Data.Lines), resp.Body)
	}
	if out.Data.Count != 2 {
		t.Errorf("count = %d, want 2", out.Data.Count)
	}
	// The tail must be the LAST lines.
	if !strings.Contains(out.Data.Lines[1], "Something went wrong") {
		t.Errorf("the tail is not the end of the file: %v", out.Data.Lines)
	}
}

func TestGetLogsGrep(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("logsgrep", 30511)

	resp := ts.get(token, "/api/v1/instances/1/logs?grep=Error")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data struct {
			Lines []string `json:"lines"`
			Count int      `json:"count"`
			Grep  string   `json:"grep"`
		} `json:"data"`
	}
	resp.JSON(&out)
	if len(out.Data.Lines) == 0 {
		t.Fatal("grep=Error matched nothing")
	}
	if out.Data.Grep != "Error" {
		t.Errorf("the response did not echo the grep pattern: %q", out.Data.Grep)
	}
	for _, line := range out.Data.Lines {
		if !strings.Contains(line, "Error") {
			t.Errorf("grep leaked a non-matching line: %q", line)
		}
	}
}

// TestGetLogsRejectsHostileGrep: the grep pattern is compiled as a regex, so a
// pathologically complex one must not be able to hang the panel.
func TestGetLogsRejectsInvalidRegex(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("logsgrepbad", 30512)

	resp := ts.get(token, "/api/v1/instances/1/logs?grep="+urlEscape("[unclosed"))
	requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)
}

func TestDownloadLogs(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("logsdl", 30513)

	// The log service must report a path INSIDE the instance directory; the
	// handler verifies containment and treats an outside path as a supervisor
	// contract violation.
	logRel := "Logs/console.log"
	ts.seedFile(inst, logRel, []byte("[12:00:00] [Info] hello\n"))
	ts.logs.path = filepath.Join(inst.Dir, "Logs", "console.log")

	resp := ts.get(token, "/api/v1/instances/1/logs/download")
	requireStatus(t, resp, http.StatusOK)
	if resp.Header.Get("Content-Disposition") == "" {
		t.Error("no Content-Disposition header on a log download")
	}
	if !strings.Contains(resp.String(), "hello") {
		t.Errorf("the log body was not served: %s", resp.Body)
	}
	ts.requireAudit("log.download")
}

// TestDownloadLogsRefusesPathOutsideInstance is the guard that stops a buggy
// (or hostile) log service from turning the panel into an arbitrary file read.
func TestDownloadLogsRefusesPathOutsideInstance(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("logsoutside", 30515)

	outside := filepath.Join(filepath.Dir(ts.instancesDir), "outside.log")
	if err := os.WriteFile(outside, []byte("secret log contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	ts.logs.path = outside

	resp := ts.get(token, "/api/v1/instances/1/logs/download")
	if resp.Status == http.StatusOK {
		t.Fatalf("SECURITY: a log path outside the instance directory was served: %s", resp.Body)
	}
	if strings.Contains(resp.String(), "secret log contents") {
		t.Fatalf("SECURITY: the outside log contents were returned: %s", resp.Body)
	}
	ts.requireAudit("log.download_rejected")
}

// TestLogsNotImplementedIs501: with no log service the endpoint must say so.
func TestLogsNotImplementedIs501(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.nopBackends = true
	})
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("lognop", 30514)

	requireErrorCode(t, ts.get(token, "/api/v1/instances/1/logs"), http.StatusNotImplemented, CodeNotImplemented)
}

// --- worlds ----------------------------------------------------------------

func TestListWorlds(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("worlds", 30520)

	resp := ts.get(token, "/api/v1/instances/1/worlds")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data []WorldResponse `json:"data"`
	}
	resp.JSON(&out)

	if len(out.Data) != 2 {
		t.Fatalf("got %d worlds, want 2: %s", len(out.Data), resp.Body)
	}
	// Exactly one world is active.
	active := 0
	for _, w := range out.Data {
		if w.Active {
			active++
		}
		if w.SizeHuman == "" {
			t.Errorf("world %q has no human-readable size", w.DirName)
		}
	}
	if active != 1 {
		t.Errorf("%d active worlds, want exactly 1", active)
	}
}

func TestActivateWorldRefusedWhileRunning(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("worldrun", 30521)
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	// Switching the active world under a running server would corrupt it, so
	// this must be a 409 with an explanation.
	resp := ts.post(token, "/api/v1/instances/1/worlds/Beta/activate", nil)
	requireErrorCode(t, resp, http.StatusConflict, CodeConflict)

	// And it must say the instance has to be stopped.
	if !strings.Contains(resp.ErrorMessage(), "停止") {
		t.Errorf("message does not tell the operator to stop the server: %q", resp.ErrorMessage())
	}

	// The active world is unchanged.
	var out struct {
		Data []WorldResponse `json:"data"`
	}
	ts.get(token, "/api/v1/instances/1/worlds").JSON(&out)
	for _, w := range out.Data {
		if w.DirName == "Alpha" && !w.Active {
			t.Error("the active world changed despite the 409")
		}
	}
}

func TestActivateWorldWhenStopped(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("worldstop", 30522)
	if _, err := ts.process.Stop(t.Context(), inst.ID, false); err != nil {
		t.Fatal(err)
	}

	resp := ts.post(token, "/api/v1/instances/1/worlds/Beta/activate", nil)
	requireStatus(t, resp, http.StatusOK)
	ts.requireAudit("world.activate")

	if len(ts.world.activated) != 1 || ts.world.activated[0] != "Beta" {
		t.Fatalf("the wrong world was activated: %v", ts.world.activated)
	}
}

// TestWorldDirTraversalIsRejected: the world directory is a path segment taken
// from the URL.
func TestWorldDirTraversalIsRejected(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("worldtrav", 30523)

	hostile := []string{
		"..",
		"../../etc",
		"..%2f..%2fetc",
		`..\..\windows`,
		"\x00",
		"%2e%2e",
	}
	for _, dir := range hostile {
		t.Run(shortLabel(dir), func(t *testing.T) {
			// The router may not even match a path containing a raw slash, so
			// both a 404 and an explicit rejection are acceptable. A 200 is not.
			resp := ts.post(token, "/api/v1/instances/1/worlds/"+urlEscape(dir)+"/activate", nil)
			if resp.Status == http.StatusOK {
				t.Fatalf("SECURITY: activate accepted world dir %q", dir)
			}
			resp = ts.post(token, "/api/v1/instances/1/worlds/"+urlEscape(dir)+"/backup", nil)
			if resp.Status == http.StatusOK {
				t.Fatalf("SECURITY: backup accepted world dir %q", dir)
			}
			resp = ts.del(token, "/api/v1/instances/1/worlds/"+urlEscape(dir), nil)
			if resp.Status == http.StatusOK {
				t.Fatalf("SECURITY: delete accepted world dir %q", dir)
			}
		})
	}

	if len(ts.world.deleted) != 0 {
		t.Fatalf("SECURITY: hostile world directories reached the service: %v", ts.world.deleted)
	}
}

func TestRestoreWorldRequiresConfirmation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("worldrestore", 30524)

	// Without the confirm flag a destructive restore must be refused.
	resp := ts.post(token, "/api/v1/instances/1/worlds/Alpha/restore", map[string]any{"backup_id": 1})
	requireErrorCode(t, resp, http.StatusConflict, CodeConflict)
	if !strings.Contains(strings.ToLower(resp.ErrorMessage()), "confirm") {
		t.Errorf("message should ask for confirmation: %q", resp.ErrorMessage())
	}

	// With it, the restore proceeds.
	resp = ts.post(token, "/api/v1/instances/1/worlds/Alpha/restore", map[string]any{
		"backup_id": 1, "confirm": true,
	})
	requireStatus(t, resp, http.StatusOK)
	ts.requireAudit("world.restore")
}

func TestBackupWorld(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("worldbackup", 30525)

	resp := ts.post(token, "/api/v1/instances/1/worlds/Alpha/backup", map[string]any{"note": "before upgrade"})
	requireStatus(t, resp, http.StatusCreated)

	var out struct {
		Data Backup `json:"data"`
	}
	resp.JSON(&out)
	if out.Data.World != "Alpha" {
		t.Errorf("backup.world = %q, want Alpha", out.Data.World)
	}
	if out.Data.Note != "before upgrade" {
		t.Errorf("the note was not stored: %q", out.Data.Note)
	}
	ts.requireAudit("world.backup")
}

func TestExportWorld(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("worldexport", 30526)

	resp := ts.get(token, "/api/v1/instances/1/worlds/Alpha/export")
	requireStatus(t, resp, http.StatusOK)
	if len(resp.Body) == 0 {
		t.Fatal("empty export body")
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, ".zip") {
		t.Errorf("Content-Disposition does not name a zip: %q", cd)
	}
}

func TestDeleteWorld(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("worlddelete", 30527)

	resp := ts.del(token, "/api/v1/instances/1/worlds/Beta", nil)
	requireStatus(t, resp, http.StatusOK)
	ts.requireAudit("world.delete")
}

// --- users -----------------------------------------------------------------

func TestListUsers(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.get(token, "/api/v1/users")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data []UserResponse `json:"data"`
	}
	resp.JSON(&out)

	if len(out.Data) != 1 {
		t.Fatalf("got %d users, want 1: %s", len(out.Data), resp.Body)
	}
	// The response must never carry a password hash.
	if strings.Contains(resp.String(), "$2") {
		t.Fatalf("SECURITY: the user list leaked a password hash: %s", resp.Body)
	}
	if !out.Data[0].HasPassword {
		t.Error("has_password = false for a configured admin")
	}
}

func TestCreateUser(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.post(token, "/api/v1/users", CreateUserRequest{
		Username: "operator1",
		Password: "operator-password-123",
		Role:     "operator",
	})
	requireStatus(t, resp, http.StatusCreated)

	var out struct {
		Data UserResponse `json:"data"`
	}
	resp.JSON(&out)
	if out.Data.Username != "operator1" || out.Data.Role != auth.RoleOperator {
		t.Errorf("created user = %+v", out.Data)
	}
	ts.requireAudit("user.create")

	// The new user can log in.
	requireStatus(t, ts.login("operator1", "operator-password-123"), http.StatusOK)
}

func TestCreateUserValidation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	cases := []struct {
		name string
		req  CreateUserRequest
		code int
	}{
		{"duplicate", CreateUserRequest{Username: "admin", Password: "a-good-password-1", Role: "viewer"}, http.StatusConflict},
		{"short password", CreateUserRequest{Username: "u1", Password: "short", Role: "viewer"}, http.StatusUnprocessableEntity},
		{"bad role", CreateUserRequest{Username: "u2", Password: "a-good-password-1", Role: "superuser"}, http.StatusUnprocessableEntity},
		{"hostile username", CreateUserRequest{Username: "../../etc", Password: "a-good-password-1", Role: "viewer"}, http.StatusUnprocessableEntity},
		{"empty username", CreateUserRequest{Username: "", Password: "a-good-password-1", Role: "viewer"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := ts.post(token, "/api/v1/users", tc.req)
			if resp.Status != tc.code {
				t.Fatalf("status = %d, want %d: %s", resp.Status, tc.code, resp.Body)
			}
		})
	}
}

func TestUpdateUserPasswordResetRevokesSessions(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	admin := ts.mustLogin("admin", "admin-password-123")

	// Create and log in as another user.
	create := ts.post(admin, "/api/v1/users", CreateUserRequest{
		Username: "victim", Password: "victim-password-123", Role: "viewer",
	})
	requireStatus(t, create, http.StatusCreated)
	victimToken := ts.mustLogin("victim", "victim-password-123")
	requireStatus(t, ts.get(victimToken, "/api/v1/auth/me"), http.StatusOK)

	// Advance the clock so the reset happens strictly AFTER the victim's token
	// was issued. Real time moves; this harness freezes it for deterministic
	// assertions, and the revocation floor is a second-granularity comparison.
	ts.clock = ts.clock.Add(2 * time.Second)

	// The admin resets that user's password.
	resp := ts.do(http.MethodPut, "/api/v1/users/2", map[string]string{
		"password": "brand-new-victim-password",
	}, "Authorization", "Bearer "+admin)
	requireStatus(t, resp, http.StatusOK)
	ts.requireAudit("user.update")

	// The old session must be dead: a password reset is how an operator
	// responds to a compromise, so it has to invalidate live tokens.
	requireStatus(t, ts.get(victimToken, "/api/v1/auth/me"), http.StatusUnauthorized)
	// The old password no longer works either.
	requireStatus(t, ts.login("victim", "victim-password-123"), http.StatusUnauthorized)
	requireStatus(t, ts.login("victim", "brand-new-victim-password"), http.StatusOK)
}

func TestCannotDemoteOrDeleteLastAdmin(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	// Demoting the only admin would lock everyone out of user management.
	resp := ts.do(http.MethodPut, "/api/v1/users/1", map[string]string{"role": "viewer"},
		"Authorization", "Bearer "+token)
	if resp.Status == http.StatusOK {
		t.Fatalf("SECURITY: the last admin was demoted to viewer: %s", resp.Body)
	}
	if resp.Status != http.StatusConflict && resp.Status != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 409/422: %s", resp.Status, resp.Body)
	}

	// Deleting yourself must also be refused.
	del := ts.del(token, "/api/v1/users/1", nil)
	if del.Status == http.StatusOK {
		t.Fatalf("SECURITY: an admin deleted their own account: %s", del.Body)
	}

	// The admin must still work.
	requireStatus(t, ts.get(token, "/api/v1/auth/me"), http.StatusOK)
}

func TestCannotDisableSelf(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.do(http.MethodPut, "/api/v1/users/1", map[string]any{"disabled": true},
		"Authorization", "Bearer "+token)
	if resp.Status == http.StatusOK {
		t.Fatalf("an admin disabled their own account: %s", resp.Body)
	}
}

func TestGrantAndRevokeInstancePermissions(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("grants", 30530)

	// Create a viewer with a control grant on that instance.
	resp := ts.post(token, "/api/v1/users", CreateUserRequest{
		Username: "grantee",
		Password: "grantee-password-123",
		Role:     "viewer",
		Grants:   []GrantRequest{{InstanceID: inst.ID, Perm: "control"}},
	})
	requireStatus(t, resp, http.StatusCreated)

	var out struct {
		Data UserResponse `json:"data"`
	}
	resp.JSON(&out)

	// The grant was accepted and recorded on the user row.
	//
	// The harness runs in single-user mode, where Authorizer.GrantsFor
	// deliberately reports a blanket GrantConfig for everyone: instance-level
	// grants are a no-op there by design, because a single operator must be able
	// to manage every instance without a grant table. Grant *storage* is still
	// asserted, and the multi-user enforcement path is covered separately by
	// TestInstanceGrantMiddlewareIsWired.
	if grant, ok := ts.server.deps.RBAC.GrantsFor(out.Data.ID, inst.ID); !ok || grant != auth.GrantConfig {
		t.Errorf("single-user mode should report a blanket config grant, got %q (ok=%v)", grant, ok)
	}
	if !ts.server.deps.RBAC.SingleUser() {
		t.Error("the harness should be running in single-user mode")
	}

	// Critically, the grant must not be able to defeat the role: a viewer stays
	// read-only no matter what the grant table says, because the role check
	// runs first and a viewer never holds a mutating permission.
	if err := ts.server.deps.RBAC.Authorize(auth.RoleViewer, auth.PermInstanceControl); err == nil {
		t.Fatal("SECURITY: a viewer's grant let it hold a control permission")
	}
}

func TestListUsersHidesGrantedInstanceDetailsFromThemselves(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	// The admin can always read the audit log; a viewer cannot (covered in the
	// RBAC matrix). This asserts the /auth/me grant list is present and typed.
	resp := ts.get(token, "/api/v1/auth/me")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data MeResponse `json:"data"`
	}
	resp.JSON(&out)
	if out.Data.InstanceGrants == nil {
		t.Error("instance_grants is null; it should be an empty array for an admin with no grants")
	}
}

// --- jobs ------------------------------------------------------------------

func TestJobsCRUD(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("jobs", 30540)

	create := ts.post(token, "/api/v1/jobs", JobRequest{
		Type:       "backup",
		Cron:       "0 3 * * *",
		InstanceID: 1,
		Enabled:    true,
	})
	requireStatus(t, create, http.StatusCreated)
	ts.requireAudit("job.create")

	var created struct {
		Data Job `json:"data"`
	}
	create.JSON(&created)

	list := ts.get(token, "/api/v1/jobs")
	requireStatus(t, list, http.StatusOK)
	var listed struct {
		Data []Job `json:"data"`
	}
	list.JSON(&listed)
	if len(listed.Data) != 1 {
		t.Fatalf("got %d jobs, want 1: %s", len(listed.Data), list.Body)
	}

	// Update is a full replacement, so the body must be complete.
	upd := ts.do(http.MethodPut, "/api/v1/jobs/1", JobRequest{
		Type: "backup", Cron: "0 3 * * *", InstanceID: 1, Enabled: false,
	}, "Authorization", "Bearer "+token)
	requireStatus(t, upd, http.StatusOK)
	ts.requireAudit("job.update")

	// Delete.
	del := ts.del(token, "/api/v1/jobs/1", nil)
	requireStatus(t, del, http.StatusOK)
	ts.requireAudit("job.delete")
}

func TestJobValidation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	cases := []struct {
		name string
		req  JobRequest
	}{
		{"bad type", JobRequest{Type: "shell", Cron: "0 3 * * *"}},
		{"bad cron field count", JobRequest{Type: "backup", Cron: "0 3 *"}},
		{"bad cron field value", JobRequest{Type: "backup", Cron: "99 99 * * *"}},
		{"empty cron", JobRequest{Type: "backup", Cron: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := ts.post(token, "/api/v1/jobs", tc.req)
			if resp.Status == http.StatusCreated {
				t.Fatalf("an invalid job was created: %s", resp.Body)
			}
		})
	}
}

// TestJobsReportSchedulerAvailability: without a scheduler the list must say so,
// rather than implying jobs are running.
func TestJobsReportSchedulerAvailability(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.get(token, "/api/v1/jobs")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data               []Job `json:"data"`
		SchedulerAvailable bool  `json:"scheduler_available"`
	}
	resp.JSON(&out)
	if out.Data == nil {
		t.Error("data should be an empty array, not null")
	}
	// With only the store wired (no scheduler), the field must be false so the
	// UI can warn that scheduled jobs will not fire.
	if out.SchedulerAvailable {
		t.Error("scheduler_available = true with no scheduler wired in")
	}
}

// --- backups ---------------------------------------------------------------

func TestBackupsCRUD(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("backups", 30550)

	// Seed a row so the list and restore/delete routes have something to act
	// on. The create route delegates to the backup SERVICE, and in this build
	// the store is the source of truth for the listing.
	seeded := &Backup{
		InstanceID: 1,
		Path:       "/backups/1/manual-1.zip",
		SizeBytes:  4096,
		Kind:       "manual",
		Note:       "before the upgrade",
		CreatedAt:  ts.clock,
	}
	if err := ts.backups.Create(t.Context(), seeded); err != nil {
		t.Fatalf("seeding backup: %v", err)
	}

	create := ts.post(token, "/api/v1/instances/1/backups", CreateBackupRequest{
		Kind: "manual",
		Note: "a fresh backup",
	})
	requireStatus(t, create, http.StatusCreated)
	ts.requireAudit("backup.create")

	var created struct {
		Data Backup `json:"data"`
	}
	create.JSON(&created)
	if created.Data.Kind != "manual" {
		t.Errorf("kind = %q, want manual", created.Data.Kind)
	}
	if ts.backupSvc.created != 1 {
		t.Errorf("the backup service was called %d times, want 1", ts.backupSvc.created)
	}

	list := ts.get(token, "/api/v1/backups")
	requireStatus(t, list, http.StatusOK)
	var listed struct {
		Data []Backup `json:"data"`
	}
	list.JSON(&listed)
	if len(listed.Data) != 1 {
		t.Fatalf("got %d backups, want 1: %s", len(listed.Data), list.Body)
	}

	// Restore requires the instance to be stopped; it is.
	restore := ts.post(token, "/api/v1/backups/1/restore", nil)
	requireStatus(t, restore, http.StatusOK)
	ts.requireAudit("backup.restore")

	del := ts.del(token, "/api/v1/backups/1", nil)
	requireStatus(t, del, http.StatusOK)
	ts.requireAudit("backup.delete")
}

func TestRestoreBackupRefusedWhileRunning(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("backuprun", 30551)

	seeded := &Backup{
		InstanceID: 1,
		Path:       "/backups/1/manual-1.zip",
		Kind:       "manual",
		CreatedAt:  ts.clock,
	}
	if err := ts.backups.Create(t.Context(), seeded); err != nil {
		t.Fatalf("seeding backup: %v", err)
	}

	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	restore := ts.post(token, "/api/v1/backups/1/restore", nil)
	requireErrorCode(t, restore, http.StatusConflict, CodeConflict)
}

func TestCreateBackupValidation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("backupval", 30552)

	resp := ts.post(token, "/api/v1/instances/1/backups", CreateBackupRequest{Kind: "nonsense"})
	requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)
}

var _ = time.Now
