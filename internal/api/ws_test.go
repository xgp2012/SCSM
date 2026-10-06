package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"scnetm/internal/auth"
)

// dialWS opens a real WebSocket connection against the httptest server, using
// the genuine gorilla dialer — no fake transport. This is what makes these tests
// meaningful: they exercise Gin's upgrade, the auth middleware, and the
// read/write pumps exactly as a browser would.
func dialWS(t *testing.T, ts *testServer, path string, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u, err := url.Parse(ts.ts.URL)
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}
	u.Scheme = "ws"
	u.Path = path

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	return dialer.Dial(u.String(), header)
}

// wsURL builds a ws:// URL with a token query parameter, the browser path.
func wsURL(ts *testServer, path, token string) string {
	u := strings.Replace(ts.ts.URL, "http://", "ws://", 1) + path
	if token != "" {
		u += "?token=" + url.QueryEscape(token)
	}
	return u
}

// readWSMessage reads one message with a deadline.
func readWSMessage(t *testing.T, conn *websocket.Conn) WSMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting read deadline: %v", err)
	}
	var msg WSMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("reading WS message: %v", err)
	}
	return msg
}

// readUntilType reads messages, discarding any that are not of the wanted type,
// and returns the first match.
//
// Use it only when the preceding messages are genuinely irrelevant. When a
// wanted message may be preceded by one whose content matters, accumulate with
// readUntilText instead.
func readUntilType(t *testing.T, conn *websocket.Conn, want string) WSMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := readWSMessage(t, conn)
		if msg.Type == want {
			return msg
		}
	}
	t.Fatalf("no %q message within the deadline", want)
	return WSMessage{}
}

// readUntilText scans every frame — log, status and error alike — until one
// carries the given text. This is the right helper for asserting on console
// output, because the frame type that delivers a command's echo is an
// implementation detail while the text is the contract.
func readUntilText(t *testing.T, conn *websocket.Conn, want string) WSMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := readWSMessage(t, conn)
		if strings.Contains(msg.Text, want) {
			return msg
		}
	}
	t.Fatalf("no frame carrying %q within the deadline", want)
	return WSMessage{}
}

// readUntilEvent reads Event frames until one of the given type arrives.
func readUntilEvent(t *testing.T, conn *websocket.Conn, want string) Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		assertDeadline(t, conn)
		var ev Event
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatalf("reading event: %v", err)
		}
		if ev.Type == want {
			return ev
		}
	}
	t.Fatalf("no %q event within the deadline", want)
	return Event{}
}

func assertDeadline(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting read deadline: %v", err)
	}
}

// --- console ---------------------------------------------------------------

// TestConsoleWSRejectsUnauthenticated is the security baseline: the upgrade must
// be refused before a socket is established.
func TestConsoleWSRejectsUnauthenticated(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	ts.seedInstance("wsnoauth", 30400)

	conn, resp, err := dialWS(t, ts, "/api/v1/ws/instances/1/console", nil)
	if err == nil {
		conn.Close()
		t.Fatal("SECURITY FAILURE: an unauthenticated client completed the WebSocket upgrade")
	}
	if resp == nil {
		t.Fatalf("no HTTP response for a rejected handshake: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("handshake status = %d, want 401", resp.StatusCode)
	}
}

func TestConsoleWSRejectsBadToken(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	ts.seedInstance("wsbadtoken", 30401)

	header := http.Header{}
	header.Set("Authorization", "Bearer not-a-real-token")
	conn, resp, err := dialWS(t, ts, "/api/v1/ws/instances/1/console", header)
	if err == nil {
		conn.Close()
		t.Fatal("SECURITY FAILURE: a forged token completed the upgrade")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("handshake status = %d, want 401", resp.StatusCode)
	}
}

// TestConsoleWSQueryTokenWorks is the browser path: a browser cannot set an
// Authorization header on a WebSocket handshake, so ?token= must work.
func TestConsoleWSQueryTokenWorks(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("wstoken", 30402)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), nil)
	if err != nil {
		t.Fatalf("query-token dial failed: %v", err)
	}
	defer conn.Close()

	// The server greets with a hello frame.
	msg := readUntilType(t, conn, WSMsgHello)
	if msg.Type != WSMsgHello {
		t.Fatalf("first message type = %q, want %q", msg.Type, WSMsgHello)
	}
	if msg.InstanceID != 1 {
		t.Errorf("hello.instance_id = %d, want 1", msg.InstanceID)
	}

	// The query-token path must be recorded in the audit trail, because a token
	// in a URL is more exposed than one in a header (it lands in access logs).
	if !ts.audit.hasAction("ws.console_open") {
		t.Fatalf("no ws.console_open audit row; actions: %v", ts.audit.actions())
	}
	entry, _ := ts.audit.find("ws.console_open")
	if !strings.Contains(entry.Detail, "query_token") {
		t.Errorf("audit row does not record the query-token path: %s", entry.Detail)
	}
}

// TestConsoleWSBidirectionalCommand is the core requirement: a command sent by
// the client round-trips to the process manager, and its output comes back.
func TestConsoleWSBidirectionalCommand(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("wscmd", 30403)
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// The hello frame arrives first.
	readUntilType(t, conn, WSMsgHello)

	command := "say hello from the test"
	if err := conn.WriteJSON(WSCommand{Type: WSMsgCommand, Line: command}); err != nil {
		t.Fatalf("writing command: %v", err)
	}

	// The nop manager echoes commands into the log feed, so the text must come
	// back. This proves the full path: client → handler → ValidateCommand →
	// ProcessManager.SendCommand → log feed → client.
	msg := readUntilText(t, conn, command)
	if msg.Type != WSMsgLog {
		t.Errorf("the command echo arrived as %q, want %q", msg.Type, WSMsgLog)
	}

	// The command must be audited with its text.
	entry := ts.requireAudit("console.command")
	if !strings.Contains(entry.Detail, "hello from the test") {
		t.Errorf("audit row does not record the command text: %s", entry.Detail)
	}
}

// TestConsoleWSRejectsInjectedCommand proves ValidateCommand is enforced on the
// socket, not just in the helper.
func TestConsoleWSRejectsInjectedCommand(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("wsinject", 30404)
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	readUntilType(t, conn, WSMsgHello)

	hostile := []string{
		"say hi\nrm -rf /",
		"say hi\r\nrm -rf /",
		strings.Repeat("A", CommandMaxBytes+1),
	}
	for _, cmd := range hostile {
		if err := conn.WriteJSON(WSCommand{Type: WSMsgCommand, Line: cmd}); err != nil {
			t.Fatalf("writing hostile command: %v", err)
		}
		msg := readUntilType(t, conn, WSMsgError)
		if msg.Type != WSMsgError {
			t.Fatalf("hostile command %q did not produce an error frame", truncate(cmd, 30))
		}
	}

	// None of the rejected commands may have reached the process manager, so no
	// command audit rows exist.
	if ts.audit.hasAction("instance.command") {
		t.Error("a rejected command was still audited as executed")
	}
}

// TestConsoleWSViewerIsForbidden: the console is a control surface.
func TestConsoleWSViewerIsForbidden(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	viewer := ts.seedUserWithRole("viewer", auth.RoleViewer)
	token := ts.issueToken(viewer.ID, viewer.Username, auth.RoleViewer, time.Hour)
	ts.seedInstance("wsviewer", 30405)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), nil)
	if err == nil {
		conn.Close()
		t.Fatal("SECURITY FAILURE: a viewer completed a console upgrade")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("handshake status = %d, want 403", resp.StatusCode)
	}
}

// TestConsoleWSUnknownInstanceIs404.
func TestConsoleWSUnknownInstanceIs404(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/999/console", token), nil)
	if err == nil {
		conn.Close()
		t.Fatal("the upgrade succeeded for a missing instance")
	}
	if resp != nil && resp.StatusCode != http.StatusNotFound {
		t.Errorf("handshake status = %d, want 404", resp.StatusCode)
	}
}

// TestConsoleWSRejectsCrossOrigin: a hostile page must not be able to open a
// console to the operator's panel using the browser's ambient credentials.
func TestConsoleWSRejectsCrossOrigin(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("wscors", 30406)

	header := http.Header{}
	header.Set("Origin", "https://evil.example.com")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), header)
	if err == nil {
		conn.Close()
		t.Fatal("SECURITY FAILURE: a cross-origin handshake was accepted")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("handshake status = %d, want 403", resp.StatusCode)
	}

	// A same-origin Origin is accepted.
	sameOrigin := http.Header{}
	sameOrigin.Set("Origin", ts.ts.URL)
	okConn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), sameOrigin)
	if err != nil {
		t.Fatalf("same-origin dial failed: %v", err)
	}
	okConn.Close()
}

// TestConsoleWSMultipleClients: two operators watching the same instance both
// receive output.
func TestConsoleWSMultipleClients(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	inst := ts.seedInstance("wsmulti", 30407)
	if err := ts.process.Start(t.Context(), inst.ID); err != nil {
		t.Fatal(err)
	}

	connA, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), nil)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer connA.Close()
	connB, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), nil)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer connB.Close()

	// Drain each socket's greeting before issuing a command, so the assertion
	// below cannot be satisfied by a frame that predates it.
	readUntilType(t, connA, WSMsgHello)
	readUntilType(t, connB, WSMsgHello)

	command := "say broadcast to both"
	if err := connA.WriteJSON(WSCommand{Type: WSMsgCommand, Line: command}); err != nil {
		t.Fatalf("writing: %v", err)
	}

	// Both sockets must see the echoed output.
	for name, conn := range map[string]*websocket.Conn{"A": connA, "B": connB} {
		msg := readUntilText(t, conn, command)
		if !strings.Contains(msg.Text, command) {
			t.Errorf("client %s did not receive the broadcast output", name)
		}
	}
}

// --- events ----------------------------------------------------------------

func TestEventsWSRequiresAuth(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })

	conn, resp, err := dialWS(t, ts, "/api/v1/ws/events", nil)
	if err == nil {
		conn.Close()
		t.Fatal("SECURITY FAILURE: an unauthenticated client opened the event stream")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("handshake status = %d, want 401", resp.StatusCode)
	}
}

// TestEventsWSDeliversStateChanges proves the event bus is actually wired to the
// lifecycle handlers.
func TestEventsWSDeliversStateChanges(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("wsevents", 30408)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/events", token), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// A ready event greets the client.
	ready := readUntilEvent(t, conn, EventReady)
	if ready.Type != EventReady {
		t.Fatalf("first event = %q, want %q", ready.Type, EventReady)
	}

	// Change state through the HTTP API, not by calling the manager directly:
	// publishing the event is the *handler's* job, and going through the route
	// is what proves the wiring exists.
	resp := ts.post(token, "/api/v1/instances/1/start", nil)
	requireStatus(t, resp, http.StatusOK)

	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		assertDeadline(t, conn)
		var ev Event
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatalf("reading event: %v", err)
		}
		if ev.Type != EventStateChange && ev.Type != EventInstance {
			continue
		}
		// Data is decoded as a generic object, so look for the state key
		// wherever the payload nests it.
		raw, err := json.Marshal(ev.Data)
		if err != nil {
			continue
		}
		if strings.Contains(string(raw), StateRunning) {
			found = true
		}
	}
	if !found {
		t.Fatal("no state_change event mentioning Running arrived")
	}
}

// TestEventsWSViewerCanSubscribe: a viewer may watch, since events are read-only
// and the payload contains no secrets.
func TestEventsWSViewerCanSubscribe(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	viewer := ts.seedUserWithRole("viewer", auth.RoleViewer)
	token := ts.issueToken(viewer.ID, viewer.Username, auth.RoleViewer, time.Hour)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/events", token), nil)
	if err != nil {
		t.Fatalf("a viewer could not subscribe to events: %v", err)
	}
	defer conn.Close()

	readUntilType(t, conn, EventReady)
}

// --- resilience ------------------------------------------------------------

// TestConsoleWSClosesWhenTokenIsRevoked: logging out must not leave a live
// console behind. The next client frame is rejected.
func TestConsoleWSRejectsRevokedToken(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("wsrevoke", 30409)

	// Revoke before dialling: the handshake must fail.
	resp := ts.post(token, "/api/v1/auth/logout", nil)
	requireStatus(t, resp, http.StatusOK)

	conn, httpResp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), nil)
	if err == nil {
		conn.Close()
		t.Fatal("SECURITY FAILURE: a revoked token opened a console")
	}
	if httpResp != nil && httpResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("handshake status = %d, want 401", httpResp.StatusCode)
	}
}

// TestWSPingKeepsConnectionAlive: the server's ping loop must not tear down a
// healthy connection.
func TestWSConnectionSurvivesIdlePeriod(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")
	ts.seedInstance("wsidle", 30410)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/v1/ws/instances/1/console", token), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	readUntilType(t, conn, WSMsgHello)

	// Read for a while, answering control frames automatically.
	conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				t.Fatalf("the server closed an idle connection: %v", err)
			}
			// A read deadline is the expected way out.
			break
		}
	}

	// The connection must still be usable afterwards.
	if err := conn.WriteJSON(WSCommand{Type: WSMsgCommand, Line: "list"}); err != nil {
		t.Fatalf("the connection was not usable after idling: %v", err)
	}
}
