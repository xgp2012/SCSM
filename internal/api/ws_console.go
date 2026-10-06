package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// This file implements the bidirectional console WebSocket:
//
//	GET /api/v1/ws/instances/:id/console   (also /ws/instances/:id/console)
//
// # Authentication over WebSocket
//
// The browser WebSocket constructor cannot set request headers, so the standard
// `Authorization: Bearer` channel is unavailable to the frontend. The handler
// therefore accepts the token from either place:
//
//   - `Authorization: Bearer <jwt>` — used by programmatic clients (a Go
//     dialer, a CLI), and by any browser polyfill that can set headers.
//   - `?token=<jwt>` — the browser path, validated with exactly the same code
//     (same signature check, same expiry check, same jti denylist lookup).
//
// The only difference is that the request is flagged ViaQueryToken, so the audit
// log records the weaker channel. An unauthenticated upgrade is rejected *before*
// the upgrade happens, with a JSON 401 envelope, so a client that ignores the
// HTTP status still sees a normal HTTP error rather than an immediately-closed
// socket.
//
// # Message protocol
//
// Server → client: {"type":"hello|log|status|error|pong", ...}
// Client → server: {"type":"command|ping|resize", ...}
//
// A bare (non-JSON) text frame is treated as a command, because a terminal UI
// naturally wants to send the raw line.

// upgrader is configured once. CheckOrigin is resolved per request by
// originAllowed, so the field here must not impose the library default (which
// would reject the same-origin case behind some proxies).
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Compression is left off: the console stream is mostly short lines, and
	// per-message deflate adds a memory cost per connection that is not worth
	// it for a handful of admin sessions.
	EnableCompression: false,
	CheckOrigin:       nil, // set in the handler via checkOrigin
}

// handleConsoleWS upgrades to the console socket.
func (s *Server) handleConsoleWS(c *gin.Context) {
	// --- authentication before the upgrade ---
	// allowQueryToken is true here: this is precisely the browser case the
	// query parameter exists for.
	p, apiErr := authenticate(c, s.authCfg, true)
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	id, idErr := instanceIDParam(c)
	if idErr != nil {
		Fail(c, idErr)
		return
	}

	inst, err := s.deps.Instances.GetByID(c.Request.Context(), id)
	if err != nil {
		Fail(c, Classify(err, "instance not found"))
		return
	}

	// Instance-level authorization (a no-op in single-user mode, but the grant
	// middleware is exercised on the same code path as HTTP routes).
	if aerr := s.deps.RBAC.AuthorizeInstance(p.Role, p.UserID, inst.OwnerID, inst.ID, permInstanceControl); aerr != nil {
		Fail(c, Forbidden("%s", aerr.Error()))
		return
	}

	// --- origin check ---
	if !s.originAllowed(c) {
		s.audit(c, "ws.console_rejected", "instance:"+itoa(id), gin.H{
			"reason": "origin not allowed",
			"origin": c.GetHeader("Origin"),
		})
		Fail(c, Forbidden("the WebSocket origin is not allowed"))
		return
	}

	// --- replay/raw options ---
	replay := queryInt(c, "replay", 0, 0, 5000)
	raw := c.Query("raw") == "true" || c.Query("raw") == "1"

	// Subscribe before upgrading so a failure is still a clean HTTP error.
	sub, serr := s.deps.Process.Subscribe(c.Request.Context(), id, replay, raw)
	if serr != nil {
		if IsNotImplemented(serr) {
			FailNotImplemented(c, "console stream (process supervisor)", serr)
			return
		}
		Fail(c, Classify(serr, "could not subscribe to the console stream"))
		return
	}

	conn, uerr := upgrader.Upgrade(c.Writer, c.Request, nil)
	if uerr != nil {
		// Upgrade already wrote its own error response.
		_ = sub.Close()
		loggerFrom(c).Warn("websocket upgrade failed",
			"instance_id", id, "err", uerr.Error(), "user_id", p.UserID)
		return
	}

	loggerFrom(c).Info("console websocket opened",
		"instance_id", id, "user_id", p.UserID, "via_query_token", p.ViaQueryToken, "replay", replay)

	s.audit(c, "ws.console_open", "instance:"+itoa(id), gin.H{
		"user":            p.Username,
		"via_query_token": p.ViaQueryToken,
		"replay":          replay,
		"raw":             raw,
	})

	session := &consoleSession{
		server:     s,
		conn:       conn,
		instanceID: id,
		principal:  p,
		sub:        sub,
		raw:        raw,
		writeMu:    &sync.Mutex{},
	}
	session.run()
}

// consoleSession owns one console connection for its whole lifetime.
type consoleSession struct {
	server     *Server
	conn       *websocket.Conn
	instanceID int64
	principal  *Principal
	sub        LogSubscription
	raw        bool

	// writeMu serialises writes: the log pump and the pong/status writers run
	// on different goroutines, and gorilla/websocket permits only one
	// concurrent writer.
	writeMu *sync.Mutex

	// closeOnce makes cleanup re-entrant: the read loop, the write loop and
	// the ping ticker can all observe the end first.
	closeOnce sync.Once

	// signal is closed by close() so every auxiliary goroutine stops. It is
	// created lazily by closedSignal.
	signalOnce sync.Once
	signal     chan struct{}
}

func (cs *consoleSession) run() {
	cfg := cs.server.deps.ConfigHTTP

	// Start from a known state: tell the client what it connected to.
	state := StateUnknown
	if live, ok := cs.server.deps.Process.State(cs.instanceID); ok {
		state = live.State
	}
	cs.writeJSON(WSMessage{
		Type:       WSMsgHello,
		InstanceID: cs.instanceID,
		TS:         cs.server.now().UTC(),
		State:      state,
		Text:       "console connected",
	})

	// Replay any buffered history so a reconnecting terminal is not blank.
	// The read limit is a byte budget, not a line count, so a fixed sensible
	// cap is used for the replay depth.
	//
	// A nil result is legitimate (see LogSubscription.History): a supervisor
	// that seeds Lines() with its own backlog has no separate replay buffer, and
	// ranging over a nil slice simply does nothing. Live output below is
	// unaffected either way.
	const maxReplayLines = 2000
	for _, line := range cs.sub.History(maxReplayLines) {
		if !cs.writeLog(line) {
			cs.close()
			return
		}
	}

	done := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }

	// Reader: client → server commands.
	go func() {
		defer finish()
		cs.readLoop(cfg)
	}()

	// Writer: server → client log lines.
	go func() {
		defer finish()
		cs.writeLoop()
	}()

	// Keepalive: detect a dead peer without waiting for a TCP timeout.
	go func() {
		defer finish()
		cs.pingLoop(cfg)
	}()

	// Status pump: push state changes so the console header stays current.
	statusSub := cs.server.deps.Events.Subscribe()
	go func() {
		defer finish()
		cs.statusLoop(statusSub)
	}()

	<-done

	statusSub.Close()
	cs.close()
}

func (cs *consoleSession) readLoop(cfg HTTPConfig) {
	cs.conn.SetReadLimit(cfg.WSReadLimit)
	// A generous read deadline, refreshed by every inbound frame including
	// pongs: an idle console is normal, a silent half-open socket is not.
	_ = cs.conn.SetReadDeadline(time.Now().Add(3 * cfg.WSPingInterval))
	cs.conn.SetPongHandler(func(string) error {
		return cs.conn.SetReadDeadline(time.Now().Add(3 * cfg.WSPingInterval))
	})

	for {
		mt, data, err := cs.conn.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage && mt != websocket.BinaryMessage {
			continue
		}
		_ = cs.conn.SetReadDeadline(time.Now().Add(3 * cfg.WSPingInterval))

		cs.handleClientMessage(data)
	}
}

// handleClientMessage dispatches one inbound frame.
func (cs *consoleSession) handleClientMessage(data []byte) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return
	}

	var msg WSCommand
	// A frame that is not JSON is treated as a raw command line, which is what
	// a terminal UI sends naturally.
	if !strings.HasPrefix(text, "{") || jsonUnmarshal(data, &msg) != nil {
		cs.dispatchCommand(text)
		return
	}

	switch msg.Type {
	case WSMsgCommand, "":
		cs.dispatchCommand(msg.Line)
	case WSMsgPing:
		cs.writeJSON(WSMessage{Type: WSMsgPong, TS: cs.server.now().UTC()})
	case WSMsgResize:
		// PTY resize is owned by the supervisor (D1); the API layer forwards
		// it when the manager exposes a resizer and otherwise acknowledges so
		// the client does not retry forever.
		if r, ok := cs.server.deps.Process.(Resizable); ok {
			if err := r.Resize(cs.instanceID, msg.Cols, msg.Rows); err != nil {
				cs.writeError("resize_failed", err.Error())
				return
			}
			return
		}
		cs.writeError("resize_unsupported", "terminal resize is not supported by the current process manager")
	default:
		cs.writeError("unknown_type", "unsupported message type "+msg.Type)
	}
}

// dispatchCommand validates and forwards a console command.
//
// Validation happens here as well as in the supervisor (defense in depth, §5.7):
// a newline in the command line would let a client smuggle a second command into
// the server's stdin.
func (cs *consoleSession) dispatchCommand(line string) {
	clean, err := ValidateCommand(line)
	if err != nil {
		cs.server.auditCommand(cs.principal, cs.instanceID, line, false, err.Error())
		cs.writeError("invalid_command", err.Error())
		return
	}

	// Rate limit command dispatch per user (§5.7 keeps this limiter separate
	// from the login and upload limiters).
	if limiter := cs.server.limiters.command; limiter != nil {
		key := "user:" + itoa(cs.principal.UserID)
		if ok, retry := limiter.Allow(key); !ok {
			cs.server.auditCommand(cs.principal, cs.instanceID, clean, false, "rate limited")
			cs.writeError("rate_limited", "too many commands; retry in "+itoa(int64(retry))+"s")
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := cs.server.deps.Process.SendCommand(ctx, cs.instanceID, clean); err != nil {
		code := "command_failed"
		switch {
		case errors.Is(err, ErrConflict):
			code = "instance_not_running"
		case errors.Is(err, ErrNotImplemented):
			code = CodeNotImplemented
		case errors.Is(err, context.DeadlineExceeded):
			code = CodeTimeout
		case errors.Is(err, ErrCommandRejected):
			code = "invalid_command"
		}
		cs.server.auditCommand(cs.principal, cs.instanceID, clean, false, err.Error())
		cs.writeError(code, err.Error())
		return
	}

	cs.server.auditCommand(cs.principal, cs.instanceID, clean, true, "")
}

func (cs *consoleSession) writeLoop() {
	lines := cs.sub.Lines()
	for {
		select {
		case <-cs.doneChan():
			return
		case line, ok := <-lines:
			if !ok {
				// The supervisor ended the feed (instance stopped).
				cs.writeJSON(WSMessage{
					Type:       WSMsgStatus,
					InstanceID: cs.instanceID,
					TS:         cs.server.now().UTC(),
					State:      StateStopped,
					Text:       "the console stream ended",
				})
				cs.close()
				return
			}
			if !cs.writeLog(line) {
				return
			}
		}
	}
}

func (cs *consoleSession) statusLoop(sub *EventSubscription) {
	events := sub.Events()
	for {
		select {
		case <-cs.doneChan():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.InstanceID != cs.instanceID || ev.Type != EventStateChange {
				continue
			}
			state := ""
			if m, ok := ev.Data.(gin.H); ok {
				if s, ok := m["state"].(string); ok {
					state = s
				}
			}
			if !cs.writeJSON(WSMessage{
				Type:       WSMsgStatus,
				InstanceID: cs.instanceID,
				TS:         ev.Timestamp,
				State:      state,
			}) {
				return
			}
		}
	}
}

func (cs *consoleSession) pingLoop(cfg HTTPConfig) {
	ticker := time.NewTicker(cfg.WSPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cs.doneChan():
			return
		case <-ticker.C:
			cs.writeMu.Lock()
			_ = cs.conn.SetWriteDeadline(time.Now().Add(cfg.WSWriteTimeout))
			err := cs.conn.WriteMessage(websocket.PingMessage, nil)
			cs.writeMu.Unlock()
			if err != nil {
				cs.close()
				return
			}
		}
	}
}

func (cs *consoleSession) writeLog(line LogLine) bool {
	return cs.writeJSON(WSMessage{
		Type:       WSMsgLog,
		InstanceID: cs.instanceID,
		Seq:        line.Seq,
		TS:         line.Timestamp,
		Text:       line.Text,
		Stream:     line.Stream,
	})
}

func (cs *consoleSession) writeError(code, message string) bool {
	return cs.writeJSON(WSMessage{
		Type:       WSMsgError,
		InstanceID: cs.instanceID,
		TS:         cs.server.now().UTC(),
		Code:       code,
		Message:    message,
	})
}

func (cs *consoleSession) writeJSON(v any) bool {
	cs.writeMu.Lock()
	defer cs.writeMu.Unlock()
	_ = cs.conn.SetWriteDeadline(time.Now().Add(cs.server.deps.ConfigHTTP.WSWriteTimeout))
	if err := cs.conn.WriteJSON(v); err != nil {
		return false
	}
	return true
}

// close tears the session down exactly once.
func (cs *consoleSession) close() {
	cs.closeOnce.Do(func() {
		cs.signalOnce.Do(func() { cs.signal = make(chan struct{}) })
		close(cs.signal)
		// Closing the subscription first stops new lines arriving while we
		// shut the socket down.
		_ = cs.sub.Close()
		cs.writeMu.Lock()
		_ = cs.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(time.Second))
		cs.writeMu.Unlock()
		_ = cs.conn.Close()

		cs.server.auditWSClose(cs.principal, cs.instanceID)
	})
}

// doneChan returns the session's termination signal.
//
// The real termination signal is the socket itself: when close() runs, the read
// loop and the write loop unwind because their I/O fails. This channel exists so
// a goroutine blocked in a channel select (rather than on I/O) also notices.
func (cs *consoleSession) doneChan() <-chan struct{} {
	cs.signalOnce.Do(func() { cs.signal = make(chan struct{}) })
	return cs.signal
}

// auditCommand records a console command attempt.
func (s *Server) auditCommand(p *Principal, instanceID int64, line string, ok bool, reason string) {
	action := "console.command"
	if !ok {
		action = "console.command_rejected"
	}
	detail := gin.H{
		"line":     truncate(line, 256),
		"accepted": ok,
	}
	if reason != "" {
		detail["reason"] = reason
	}
	if p != nil {
		detail["user"] = p.Username
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entry := &AuditEntry{
		Action:    action,
		Target:    "instance:" + itoa(instanceID),
		Detail:    renderDetail(detail),
		Timestamp: s.now().UTC(),
	}
	if p != nil {
		entry.UserID = p.UserID
	}
	if err := s.deps.Audit.Write(ctx, entry); err != nil {
		s.deps.Log.Error("audit write failed for console command", "err", err.Error())
	}
}

func (s *Server) auditWSClose(p *Principal, instanceID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entry := &AuditEntry{
		Action:    "ws.console_close",
		Target:    "instance:" + itoa(instanceID),
		Timestamp: s.now().UTC(),
	}
	if p != nil {
		entry.UserID = p.UserID
		entry.Detail = renderDetail(gin.H{"user": p.Username})
	}
	if err := s.deps.Audit.Write(ctx, entry); err != nil {
		s.deps.Log.Error("audit write failed for console close", "err", err.Error())
	}
}

// Resizable is implemented by a ProcessManager that can forward a terminal
// resize to the PTY. It is optional: a manager that does not implement it makes
// the console answer "resize_unsupported" rather than failing the socket.
type Resizable interface {
	Resize(instanceID int64, cols, rows int) error
}

// originAllowed implements the WebSocket Origin policy.
//
// Default (no configured origins): the request must be same-origin, established
// by comparing the Origin header's host with the request's Host. An absent
// Origin header is allowed, because non-browser clients (a Go dialer, curl) do
// not send one and have already passed token authentication.
//
// This matters because a WebSocket handshake is not subject to the same-origin
// policy; without the check a malicious page could open a console socket to a
// panel on localhost using the victim's browser as a proxy.
func (s *Server) originAllowed(c *gin.Context) bool {
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin == "" {
		return true
	}

	allowed := s.deps.ConfigHTTP.AllowedOrigins
	if len(allowed) > 0 {
		for _, a := range allowed {
			if strings.EqualFold(strings.TrimSpace(a), origin) {
				return true
			}
		}
		// Fall through to same-origin so a configured list does not break the
		// common case of the bundled frontend.
	}

	host := originHost(origin)
	if host == "" {
		return false
	}
	return strings.EqualFold(host, c.Request.Host)
}

// originHost extracts the host:port from an Origin header value.
func originHost(origin string) string {
	if i := strings.Index(origin, "://"); i >= 0 {
		rest := origin[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[:j]
		}
		return rest
	}
	return origin
}

// jsonUnmarshal decodes a WebSocket frame payload.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

var _ = http.StatusSwitchingProtocols
