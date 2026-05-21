package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

// upgrader promotes the GET /api/village/events request to a WebSocket.
//
// CheckOrigin enforces same-origin for browser clients while permitting native
// clients. A browser sets the Origin header to the page's origin; the web-export
// Godot client connects to its own origin (wss from window.location.origin), so
// same-host passes. A cross-origin website's Origin would NOT match the request
// Host and is rejected — without this, any site the user visits could open a WS
// to a locally running engine and read the (unauthenticated) village stream.
// Native Godot builds send no Origin header (Origin is a browser concept), so an
// empty Origin is allowed. When the auth middleware lands with the write routes,
// the token becomes the real gate and this stays as defense in depth.
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
	if res := s.auth.Verify(token); !res.Valid {
		writeAuthError(w, res.Reason)
		return
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
	case <-s.hub.done:
		_ = conn.Close()
	case <-r.Context().Done():
		_ = conn.Close()
	}
}
