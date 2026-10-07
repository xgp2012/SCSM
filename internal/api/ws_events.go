package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// This file implements the global event WebSocket:
//
//	GET /api/v1/ws/events   (also /ws/events)
//
// It carries panel-wide notifications — instance state changes, alerts, audit
// activity, backup completions — so the dashboard can stay live without polling.
// It is read-only: nothing a client sends changes server state, so the read loop
// exists purely to notice disconnection and to answer pings.
//
// Authentication follows the console socket: header or ?token=, validated
// identically before the upgrade.

// handleEventsWS upgrades to the global event stream.
func (s *Server) handleEventsWS(c *gin.Context) {
	p, apiErr := authenticate(c, s.authCfg, true)
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	// A viewer may watch events; the stream carries state and audit activity,
	// which every authenticated role is allowed to see. The per-event payload
	// is deliberately kept free of anything role-restricted.
	if !p.Can(permInstanceRead) {
		Fail(c, Forbidden("角色 %q 无权订阅面板事件", p.Role))
		return
	}

	if !s.originAllowed(c) {
		Fail(c, Forbidden("WebSocket 来源不在允许列表中"))
		return
	}

	sub := s.deps.Events.Subscribe()

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		sub.Close()
		loggerFrom(c).Warn("events websocket upgrade failed", "err", err.Error(), "user_id", p.UserID)
		return
	}

	loggerFrom(c).Info("events websocket opened", "user_id", p.UserID, "via_query_token", p.ViaQueryToken)

	cfg := s.deps.ConfigHTTP

	// Hello frame: confirms the socket is live and tells the client its
	// identity as the server sees it.
	_ = conn.SetWriteDeadline(time.Now().Add(cfg.WSWriteTimeout))
	_ = conn.WriteJSON(Event{
		Type:      EventReady,
		Timestamp: s.now().UTC(),
		Data: gin.H{
			"user_id":     p.UserID,
			"username":    p.Username,
			"role":        string(p.Role),
			"single_user": s.deps.RBAC.SingleUser(),
		},
	})

	done := make(chan struct{})
	var closeOnce func()
	{
		var once bool
		closeOnce = func() {
			if !once {
				once = true
				close(done)
			}
		}
	}

	// Reader: exists to detect a closed socket. The client may send "ping".
	go func() {
		defer closeOnce()
		conn.SetReadLimit(cfg.WSReadLimit)
		_ = conn.SetReadDeadline(time.Now().Add(3 * cfg.WSPingInterval))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(3 * cfg.WSPingInterval))
		})
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(3 * cfg.WSPingInterval))
			if mt == websocket.TextMessage && isPingFrame(data) {
				_ = conn.SetWriteDeadline(time.Now().Add(cfg.WSWriteTimeout))
				if err := conn.WriteJSON(Event{Type: EventPing, Timestamp: s.now().UTC()}); err != nil {
					return
				}
			}
		}
	}()

	// Writer: forwards hub events.
	go func() {
		defer closeOnce()
		for {
			select {
			case <-done:
				return
			case ev, ok := <-sub.Events():
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(cfg.WSWriteTimeout))
				if err := conn.WriteJSON(ev); err != nil {
					return
				}
			}
		}
	}()

	// Keepalive.
	go func() {
		defer closeOnce()
		ticker := time.NewTicker(cfg.WSPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(cfg.WSWriteTimeout))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	<-done

	sub.Close()
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	_ = conn.Close()
}

// isPingFrame recognises a client keepalive in either the raw or JSON form.
func isPingFrame(data []byte) bool {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 {
		return false
	}
	if string(trimmed) == "ping" {
		return true
	}
	return len(trimmed) > 2 && trimmed[0] == '{' &&
		(containsBytes(trimmed, []byte(`"ping"`)) || containsBytes(trimmed, []byte(`"type":"ping"`)))
}

func trimSpaceBytes(b []byte) []byte {
	start := 0
	for start < len(b) && isSpaceByte(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

var _ = http.StatusOK
