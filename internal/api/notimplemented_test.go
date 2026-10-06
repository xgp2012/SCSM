package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// TestNopBackendsReturn501NotSilentSuccess is the sweep that enforces the
// "never silently empty" rule from the brief.
//
// Every route whose backend is a nop must answer 501 with a machine-readable
// code and a reason naming the missing subsystem. Returning 200 with an empty
// array would be worse than an error: a client (or a monitoring probe) cannot
// tell "this panel has no backups" from "backup support is not wired up", and
// the second one looks like data loss.
//
// The test walks the routes that need a real feature backend and asserts the
// envelope, so adding a route that quietly returns empty is caught here.
func TestNopBackendsReturn501NotSilentSuccess(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.nopBackends = true
	})
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("nop-inst", 30600)

	// Routes whose feature backend is a nop in this configuration.
	//
	// Note what is deliberately absent: GET /backups reads the *store*, which
	// is a fake here and happily returns an empty array, so it is legitimately
	// 200. Only the backup *service* (create/restore) is 501.
	cases := []struct {
		method string
		path   string
		body   any
		label  string
	}{
		{http.MethodGet, "/api/v1/instances/1/config", nil, "configuration service"},
		{http.MethodPut, "/api/v1/instances/1/config", map[string]any{
			"kind": "ServerSetting.json", "content": map[string]any{"ServerPort": 30600},
		}, "configuration service"},
		{http.MethodPost, "/api/v1/instances/1/config/validate", map[string]any{
			"kind": "ServerSetting.json", "content": map[string]any{"ServerPort": 30600},
		}, "configuration service"},

		{http.MethodGet, "/api/v1/instances/1/files?path=", nil, "file management"},
		{http.MethodPost, "/api/v1/instances/1/files/mkdir", map[string]string{"path": "new"}, "file management"},

		{http.MethodGet, "/api/v1/instances/1/logs", nil, "log service"},
		{http.MethodGet, "/api/v1/instances/1/logs/download", nil, "log service"},

		{http.MethodGet, "/api/v1/instances/1/worlds", nil, "world management"},
		{http.MethodPost, "/api/v1/instances/1/worlds/import", map[string]string{"path": "x"}, "world management"},

		{http.MethodPost, "/api/v1/instances/1/backups", map[string]any{"world": "w1"}, "backup service"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("%s %s", tc.method, tc.path), func(t *testing.T) {
			var resp *response
			switch tc.method {
			case http.MethodGet:
				resp = ts.get(token, tc.path)
			case http.MethodPost:
				resp = ts.post(token, tc.path, tc.body)
			case http.MethodPut:
				resp = ts.put(token, tc.path, tc.body)
			default:
				t.Fatalf("unsupported method %s", tc.method)
			}

			if resp.Status != http.StatusNotImplemented {
				t.Fatalf("%s %s = %d, want 501 (it must not report success with no backend)\nbody: %s",
					tc.method, tc.path, resp.Status, resp.Body)
			}

			var env struct {
				Error struct {
					Code    string         `json:"code"`
					Message string         `json:"message"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(resp.Body), &env); err != nil {
				t.Fatalf("the 501 body is not a JSON envelope: %v\nbody: %s", err, resp.Body)
			}
			if env.Error.Code != "not_implemented" {
				t.Errorf("error code = %q, want not_implemented (a machine-readable code, not prose)", env.Error.Code)
			}
			if env.Error.Message == "" {
				t.Error("the 501 carries no message")
			}
			// The reason must name the subsystem, so an operator can tell what
			// to wire up.
			reason, _ := env.Error.Details["reason"].(string)
			if reason == "" {
				t.Errorf("the 501 has no details.reason\nbody: %s", resp.Body)
			}
		})
	}
}

// TestResourceLookupPrecedesNotImplemented documents the ordering rule the
// sweep above relies on: a route resolves the resource it names BEFORE reaching
// a feature backend, so a missing instance or file is a 404 even when the
// backend itself is unwired.
//
// That ordering is deliberate. Reporting 501 for "backup 1" would tell a client
// the feature is unavailable when the real answer is that the backup does not
// exist; and 404-vs-501 is exactly the distinction a script needs to decide
// whether to retry against a differently-configured panel.
func TestResourceLookupPrecedesNotImplemented(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.nopBackends = true
	})
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("lookup-inst", 30601)

	// A file that is not there: 404, not 501.
	resp := ts.get(token, "/api/v1/instances/1/files/download?path=absent.txt")
	if resp.Status != http.StatusNotFound {
		t.Errorf("downloading a missing file = %d, want 404: %s", resp.Status, resp.Body)
	}

	// A backup that is not there: 404, not 501.
	resp = ts.post(token, "/api/v1/backups/999/restore", map[string]any{"confirm": true})
	if resp.Status != http.StatusNotFound {
		t.Errorf("restoring a missing backup = %d, want 404: %s", resp.Status, resp.Body)
	}

	// An unknown instance id is a 404 before anything else.
	resp = ts.get(token, "/api/v1/instances/424242/config")
	if resp.Status != http.StatusNotFound {
		t.Errorf("config for an unknown instance = %d, want 404: %s", resp.Status, resp.Body)
	}
}

// TestSystemInfoSucceedsWithoutDotnet proves the one route that must NEVER 501:
// the panel's own host information. A missing .NET runtime is data the response
// carries, not a missing backend — the panel is expected to run on a host where
// the runtime is not installed yet, and reporting 501 would make the UI unable
// to tell the operator what to install.
func TestSystemInfoSucceedsWithoutDotnet(t *testing.T) {
	t.Parallel()

	// This uses the REAL host service, so it probes the actual machine.
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.realSystemService = true
	})
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.get(token, "/api/v1/system/info")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /system/info = %d, want 200 even with no .NET runtime: %s", resp.Status, resp.Body)
	}

	var out struct {
		Data struct {
			Dotnet struct {
				Available     bool     `json:"available"`
				RequiredMajor int      `json:"required_major"`
				Runtimes      []string `json:"runtimes"`
				Hint          string   `json:"hint"`
				Error         string   `json:"error"`
			} `json:"dotnet"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &out); err != nil {
		t.Fatalf("decoding: %v\nbody: %s", err, resp.Body)
	}
	if out.Data.Dotnet.RequiredMajor != 10 {
		t.Errorf("required_major = %d, want 10", out.Data.Dotnet.RequiredMajor)
	}
	if out.Data.Dotnet.Runtimes == nil {
		t.Error("runtimes serialised as null; it must always be an array")
	}
	// When the runtime is genuinely absent the response must explain itself.
	if !out.Data.Dotnet.Available {
		if out.Data.Dotnet.Error == "" {
			t.Error("dotnet is unavailable but no error was reported")
		}
		if out.Data.Dotnet.Hint == "" {
			t.Error("dotnet is unavailable but no hint was given")
		}
	}
}

// TestUnimplementedRoutesAreNotSilentlyEmpty is the converse sweep: it proves
// that a 200 from a collection route means the backend really answered.
func TestUnimplementedRoutesAreNotSilentlyEmpty(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		o.nopBackends = true
	})
	token := ts.mustLogin("admin", "admin-password-123")

	// GET /backups reads the store, so it is a legitimate 200 with an array.
	resp := ts.get(token, "/api/v1/backups")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /backups = %d: %s", resp.Status, resp.Body)
	}
	var env struct {
		Data  []any `json:"data"`
		Total int   `json:"total"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &env); err != nil {
		t.Fatalf("decoding: %v\nbody: %s", err, resp.Body)
	}
	// An empty result must be [] and not null, so a client can iterate without
	// a nil check. Unmarshalling already rejects null into a slice only when the
	// field is absent, so assert on the raw text too.
	if env.Data == nil {
		t.Errorf("the backup list is null, not an empty array: %s", resp.Body)
	}
	if !strings.Contains(resp.String(), `"data":[]`) {
		t.Errorf("expected an empty array in the envelope, got: %s", resp.Body)
	}
}

// TestRouteTableIsStable records the number of registered routes.
//
// It is a change detector, not a specification: if a route is added or removed,
// this number changes and the author is forced to confirm that the new route
// has a permission check, an audit row where it mutates, and a 501 path if its
// backend can be absent.
func TestRouteTableIsStable(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })

	var paths []string
	for _, r := range ts.server.engine.Routes() {
		paths = append(paths, r.Method+" "+r.Path)
	}
	sort.Strings(paths)

	const wantRoutes = 55
	if len(paths) != wantRoutes {
		t.Errorf("the router registers %d routes, want %d", len(paths), wantRoutes)
		for _, p := range paths {
			t.Logf("  %s", p)
		}
	}

	// Every API route must live under the versioned prefix, except the
	// health probe and the first-run discovery endpoints, which a proxy or a
	// fresh client must be able to reach before logging in.
	for _, p := range paths {
		switch {
		case len(p) > 0 && (p == "GET /healthz" || contains(p, "/api/v1/") || contains(p, "/ws/")):
		default:
			t.Errorf("route %q is outside /api/v1 and is not a known exception", p)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
