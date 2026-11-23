package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// acceptOptions configure the upgrade of the GET /api/village/events request
// to a WebSocket.
//
// The authorization gate is the ?token= verify in handleEvents, which runs
// before the upgrade (browsers and Godot can't set WS handshake headers, so the
// token rides in a query param rather than the Bearer requireAuth middleware the
// REST routes use). The origin check is defense in depth on top of that.
//
// The zero value is the origin policy we want: coder/websocket's Accept allows
// a request with no Origin header and one whose Origin host matches the request
// Host (case-insensitive), and refuses any other Origin with 403. A browser sets
// Origin to the page's origin; the web-export Godot client connects to its own
// origin (wss from window.location.origin), so same-host passes. A cross-origin
// website's Origin does NOT match the request Host and is refused — without this,
// any site the user visits could open a WS to a locally running engine. Native
// Godot builds send no Origin header (Origin is a browser concept), so they pass.
// Do not set InsecureSkipVerify or OriginPatterns here. Compression stays off
// (the default).
var acceptOptions = &websocket.AcceptOptions{}

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
	conn, err := websocket.Accept(w, r, acceptOptions)
	if err != nil {
		// Accept has already written an error response to w.
		log.Printf("httpapi: WS upgrade: %v", err)
		return
	}
	// Reject a connection that arrives after the hub has shut down — no
	// goroutine would ever service it. Checked before the registration select
	// below so a closed hub is never raced into accepting a client (a select
	// with both register-ready and done-closed picks randomly).
	select {
	case <-s.hub.done:
		_ = conn.CloseNow()
		return
	default:
	}
	c := &client{
		hub:      s.hub,
		conn:     conn,
		send:     make(chan []byte, clientSendBuffer),
		readDone: make(chan struct{}),
		token:    token, // already verified above
		login:    login,
	}
	// Queue the hello frame before registering so it is the first data frame
	// the write pump sends (the channel is FIFO and has room). Any broadcast
	// that arrives after registration is queued behind it. (A pong or close
	// reply from the read pump can still go out before it.)
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
		go c.pingLoop()
		// Stamp presence immediately on connect (LLM-342). The heartbeat only
		// re-stamps on its next tick, so a freshly connected or reconnected PC —
		// whose LastPCSeenAt is nil (never attached this session) or already stale
		// — could be swept from its huddle in the gap before the first tick, the
		// very ejection this ticket removes. Synchronous like the old /pc/me stamp,
		// on the still-live request context; a no-op login or a caller with no PC
		// stamps nothing.
		// LLM-466: one command stamps both halves, so registration is a single
		// transition — a scan can't observe a socket-fresh, activity-nil PC in
		// between. Opening a client is itself an input, so the connect starts a
		// fresh eco-mode audience horizon (and dismisses any candle prompt this
		// PC was left holding). The ACTIVITY half is deliberately absent from the
		// heartbeat's path: a stamp that renewed itself every 15s for as long as
		// a tab existed is the bug this ticket fixes.
		if login != "" {
			logins := map[string]struct{}{login: {}}
			if _, err := s.world.SendContext(r.Context(), sim.StampPCConnected(logins, time.Now().UTC())); err != nil {
				log.Printf("httpapi: initial presence/activity stamp for %q failed: %v", login, err)
			}
		}
	case <-s.hub.done:
		_ = conn.CloseNow()
	case <-r.Context().Done():
		_ = conn.CloseNow()
	}
}
