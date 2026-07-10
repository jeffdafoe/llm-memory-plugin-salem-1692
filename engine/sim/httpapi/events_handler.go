package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// upgrader promotes the GET /api/village/events request to a WebSocket.
//
// The authorization gate is the ?token= verify in handleEvents, which runs
// before the upgrade (browsers and Godot can't set WS handshake headers, so the
// token rides in a query param rather than the Bearer requireAuth middleware the
// REST routes use). CheckOrigin is defense in depth on top of that.
//
// CheckOrigin enforces same-origin for browser clients while permitting native
// clients. A browser sets the Origin header to the page's origin; the web-export
// Godot client connects to its own origin (wss from window.location.origin), so
// same-host passes. A cross-origin website's Origin would NOT match the request
// Host and is rejected — without this, any site the user visits could open a WS
// to a locally running engine. Native Godot builds send no Origin header (Origin
// is a browser concept), so an empty Origin is allowed.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // native client — no Origin header
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	},
}

// handleEvents authenticates, upgrades the connection to a WebSocket, and
// registers it with the hub. The token rides in the ?token= query param
// (browsers and Godot can't set WS handshake headers) and is verified BEFORE
// the upgrade — an unauthenticated client gets a plain HTTP auth error and is
// never upgraded, registered, or sent the hello frame. On connect the client is
// queued a hello frame (contract version), then receives the live event stream
// until it disconnects.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	res := s.auth.Verify(token)
	if !res.Valid {
		writeAuthError(w, res.Reason)
		return
	}
	// The verified principal's login, refcounted by the hub for the PC presence
	// heartbeat (LLM-342). A valid token always carries a User, but guard the
	// deref — an empty login is simply never tracked.
	login := ""
	if res.User != nil {
		login = res.User.Username
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written an error response to w.
		log.Printf("httpapi: WS upgrade: %v", err)
		return
	}
	// Reject a connection that arrives after the hub has shut down — no
	// goroutine would ever service it. Checked before the registration select
	// below so a closed hub is never raced into accepting a client (a select
	// with both register-ready and done-closed picks randomly).
	select {
	case <-s.hub.done:
		_ = conn.Close()
		return
	default:
	}
	c := &client{
		hub:   s.hub,
		conn:  conn,
		send:  make(chan []byte, clientSendBuffer),
		token: token, // already verified above
		login: login,
	}
	// Queue the hello frame before registering so it is the first thing the
	// write pump sends (the channel is FIFO and has room). Any broadcast that
	// arrives after registration is queued behind it.
	if hello, err := json.Marshal(helloFrame()); err == nil {
		c.send <- hello
	}
	// Registration is a blocking channel send. Guard it so a full register
	// buffer, a hub whose Run hasn't started, or a hub that shut down between
	// the check above and here can't wedge the handler after the upgrade. The
	// pumps start only on a successful register; every failure path closes the
	// socket itself, since no pump exists to do it.
	select {
	case s.hub.register <- c:
		go c.writePump()
		go c.readPump()
		// Stamp presence immediately on connect (LLM-342). The heartbeat only
		// re-stamps on its next tick, so a freshly connected or reconnected PC —
		// whose LastPCSeenAt is nil (never attached this session) or already stale
		// — could be swept from its huddle in the gap before the first tick, the
		// very ejection this ticket removes. Synchronous like the old /pc/me stamp,
		// on the still-live request context; a no-op login or a caller with no PC
		// stamps nothing.
		if login != "" {
			if _, err := s.world.SendContext(r.Context(), sim.StampConnectedPCsSeen(map[string]struct{}{login: {}})); err != nil {
				log.Printf("httpapi: initial presence stamp for %q failed: %v", login, err)
			}
		}
	case <-s.hub.done:
		_ = conn.Close()
	case <-r.Context().Done():
		_ = conn.Close()
	}
}
