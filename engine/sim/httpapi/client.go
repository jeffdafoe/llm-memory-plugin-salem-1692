package httpapi

import (
	"time"

	"github.com/gorilla/websocket"
)

const (
	// writeWait is the deadline for a single socket write (frame or ping).
	writeWait = 10 * time.Second
	// pongWait is how long we wait for a pong before treating the peer as dead.
	// The read pump extends the read deadline by this on every pong.
	pongWait = 60 * time.Second
	// pingPeriod is how often the write pump pings. Must be < pongWait so a
	// live peer always answers before the deadline lapses.
	pingPeriod = (pongWait * 9) / 10
	// maxMessageSize caps inbound frames. Clients are read-only this slice
	// (they send nothing), so anything inbound is unexpected — cap it small.
	maxMessageSize = 512
)

// client is one connected WebSocket. The hub goroutine is the only sender on
// send; the write pump is the only goroutine that writes to conn (gorilla
// requires a single writer). The read pump exists only to detect a closed
// connection and service pong deadlines — clients send no application messages
// this slice.
type client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	// token is the verified ?token= query value (handleEvents validates it
	// before the upgrade). Kept for re-verification / role checks when the
	// write routes land.
	token string
}

// readPump drains (and discards) inbound frames and services pong deadlines for
// keepalive. On any read error — including a normal close — it unregisters the
// client and closes the socket. Runs in its own goroutine. The unregister send
// selects on hub.done so a disconnect racing hub shutdown can't block forever.
func (c *client) readPump() {
	defer func() {
		select {
		case c.hub.unregister <- c:
		case <-c.hub.done:
		}
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump is the single writer to conn: it drains send, writing each frame as
// a text message, and pings on pingPeriod to keep the connection alive (and to
// detect a dead peer via the read pump's pong deadline). It exits when send is
// closed (the hub evicted or unregistered the client) or a write fails. Runs in
// its own goroutine.
func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel — send a clean close and exit.
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
