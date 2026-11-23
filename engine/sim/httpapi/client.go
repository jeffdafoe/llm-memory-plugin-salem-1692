package httpapi

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// The keepalive defaults NewHub copies into Hub.writeWait and Hub.pingPeriod
// (tests shorten the Hub fields).
const (
	// writeWait bounds a single data-frame write, and one whole ping: sending
	// the ping frame and waiting for its pong.
	writeWait = 10 * time.Second
	// pingPeriod is how often pingLoop pings. A peer that stops answering is
	// closed when a ping does not complete within writeWait, so a dead peer is
	// found at most pingPeriod + writeWait (about 64s) after its last pong.
	pingPeriod = 54 * time.Second
)

// maxMessageSize caps inbound frames. Clients are read-only this slice (they
// send nothing), so anything inbound is unexpected — cap it small. A larger
// message closes the connection with 1009.
const maxMessageSize = 512

// client is one connected WebSocket. The hub goroutine is the only sender on
// send; the write pump is the only goroutine that writes data frames to conn.
// The read pump exists only to detect a closed connection and to handle control
// frames — clients send no application messages this slice. The ping loop is a
// third goroutine (coder/websocket allows a ping beside a write), so its ticks
// and pong waits run apart from the write pump: waiting for a pong never holds
// up data. The ping frame itself still shares coder's frame-write lock with data
// writes, so a slow data write can delay sending it; that delay counts against
// the same writeWait bound.
type client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	// readDone is closed when the read pump exits, which happens on every way
	// the connection ends; it stops the ping loop.
	readDone chan struct{}
	// token is the verified ?token= query value (handleEvents validates it
	// before the upgrade). Kept for re-verification / role checks when the
	// write routes land.
	token string
	// login is the verified principal's username (from the token's AuthUser).
	// The hub refcounts it in connectedLogins so the PC presence heartbeat
	// (LLM-342) can tell which PCs currently hold a live socket. Empty when the
	// token resolved to no principal name — such a client is never tracked.
	login string
}

// readPump drains (and discards) inbound frames. Reading is also what handles
// control frames: coder/websocket answers a ping, matches a pong to the waiting
// Ping call, and answers a close frame, all inside Read (its own control
// replies give up and close the connection after 5s). On any read error —
// including a normal close or a close from the ping goroutine — it unregisters
// the client and closes the socket. Runs in its own goroutine. The unregister
// send selects on hub.done so a disconnect racing hub shutdown can't block
// forever.
func (c *client) readPump() {
	defer func() {
		close(c.readDone)
		select {
		case c.hub.unregister <- c:
		case <-c.hub.done:
		}
		c.conn.CloseNow()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	for {
		// No context deadline: in coder/websocket a read whose context ends
		// closes the connection. Liveness comes from the pings instead.
		if _, _, err := c.conn.Read(context.Background()); err != nil {
			return
		}
	}
}

// writePump is the single data-frame writer to conn: it drains send, writing
// each frame as a text message. It exits when send is closed (the hub evicted
// or unregistered the client) or a write fails. Runs in its own goroutine.
func (c *client) writePump() {
	defer c.conn.CloseNow()
	for msg := range c.send {
		// A write that misses its deadline closes the connection, which ends
		// the read pump too.
		ctx, cancel := context.WithTimeout(context.Background(), c.hub.writeWait)
		err := c.conn.Write(ctx, websocket.MessageText, msg)
		cancel()
		if err != nil {
			return
		}
	}
	// Hub closed the channel — send a close frame with an empty payload
	// (StatusNoStatusRcvd is written as no payload). Close then waits up to 5s
	// for the peer's close reply before it closes the socket.
	_ = c.conn.Close(websocket.StatusNoStatusRcvd, "")
}

// pingLoop pings every pingPeriod. Each ping (sending the frame and waiting for
// the pong) must complete within writeWait; if it does not, the peer is treated
// as dead and the connection is closed (the read pump then unregisters the
// client). It returns when the read pump exits; a ping in flight at that moment
// ends when the closed connection or its writeWait context ends it. Runs in its
// own goroutine.
func (c *client) pingLoop() {
	ticker := time.NewTicker(c.hub.pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-c.readDone:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), c.hub.writeWait)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				c.conn.CloseNow()
				return
			}
		}
	}
}
