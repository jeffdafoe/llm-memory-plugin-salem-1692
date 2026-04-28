package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Default idle timeout if the setting row is missing/unparseable. Matches
// the value seeded by ZBBS-082; the constant is the floor when the DB
// can't be reached at upgrade time.
const defaultPCIdleTimeoutSeconds = 60

// minPCIdleTimeoutSeconds floors any admin-configured value. A timeout
// short enough that idleTimeout/2 produces a sub-5s ping cadence is
// almost certainly a misconfiguration — the keepalive overhead
// outweighs the value of detecting drops faster, and we don't want a
// hot loop on every WS connection. Logged as a warning when clamped.
const minPCIdleTimeoutSeconds = 10

// settingLookupTimeout caps the DB call inside the WS upgrade path.
// loadPCIdleTimeout has a safe default to fall back to, so blocking
// the upgrade indefinitely on a stalled DB would defeat the purpose of
// this PR (false-live clients in Hub.clients).
const settingLookupTimeout = 500 * time.Millisecond

// loadPCIdleTimeout reads pc_idle_timeout_seconds from the setting table.
// Returns the parsed duration or the default if the row is missing,
// NULL, non-numeric, or non-positive. Read on every WebSocket upgrade
// (cheap single-row lookup) so an admin can re-tune without a restart.
//
// Bounded by settingLookupTimeout so a stalled DB can't park the
// upgrade — if we can't get the setting in 500ms, fall back to the
// default rather than holding up the handler.
func (app *App) loadPCIdleTimeout(ctx context.Context) time.Duration {
	ctx, cancel := context.WithTimeout(ctx, settingLookupTimeout)
	defer cancel()

	var v sql.NullString
	err := app.DB.QueryRow(ctx,
		`SELECT value FROM setting WHERE key = $1`, "pc_idle_timeout_seconds").Scan(&v)
	if err != nil || !v.Valid {
		return time.Duration(defaultPCIdleTimeoutSeconds) * time.Second
	}
	n, err := strconv.Atoi(v.String)
	if err != nil || n <= 0 {
		log.Printf("pc_idle_timeout_seconds: bad value %q, falling back to %ds", v.String, defaultPCIdleTimeoutSeconds)
		return time.Duration(defaultPCIdleTimeoutSeconds) * time.Second
	}
	if n < minPCIdleTimeoutSeconds {
		log.Printf("pc_idle_timeout_seconds: value %d below floor, clamping to %ds", n, minPCIdleTimeoutSeconds)
		n = minPCIdleTimeoutSeconds
	}
	return time.Duration(n) * time.Second
}

// WorldEvent is a change broadcast to all connected viewers.
// Type identifies what happened; Data carries the payload.
type WorldEvent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// EventHub maintains connected WebSocket clients and broadcasts events.
type EventHub struct {
	mu      sync.RWMutex
	clients map[*wsClient]bool
}

type wsClient struct {
	conn *websocket.Conn
	send chan []byte

	// writeMu serializes writes to conn. gorilla/websocket permits one
	// concurrent reader and one concurrent writer; this Hub has the
	// writer goroutine emitting broadcast events and the ping ticker
	// goroutine emitting keepalive pings — both call WriteMessage. The
	// previous code had no synchronization; the configurable ping
	// cadence introduced in this commit can shrink the window where a
	// race goes unnoticed, so all writes now hold this mutex.
	writeMu sync.Mutex

	// sessionToken is the llm-memory token presented at the WS upgrade.
	// Re-verified inside the ping loop so an idle client whose session
	// expires (or is revoked by a deploy) gets pushed back to the login
	// screen instead of silently losing edits.
	sessionToken string
}

// safeWrite serializes WriteMessage calls against any other writer on
// the same connection. Returns the underlying WriteMessage error so
// callers can decide whether to bail.
func (c *wsClient) safeWrite(messageType int, data []byte, deadline time.Duration) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(deadline))
	return c.conn.WriteMessage(messageType, data)
}

func NewEventHub() *EventHub {
	return &EventHub{
		clients: make(map[*wsClient]bool),
	}
}

// Broadcast sends an event to all connected clients.
func (h *EventHub) Broadcast(event WorldEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("Failed to marshal event: %v", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	log.Printf("Broadcasting %s to %d clients", event.Type, len(h.clients))

	for client := range h.clients {
		select {
		case client.send <- data:
		default:
			// Client buffer full — drop it
			go h.removeClient(client)
		}
	}
}

func (h *EventHub) addClient(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = true
	log.Printf("WebSocket client connected (%d total)", len(h.clients))
}

func (h *EventHub) removeClient(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
		c.conn.Close()
		log.Printf("WebSocket client disconnected (%d remaining)", len(h.clients))
	}
}

// WebSocket upgrader — allow any origin (CORS handled at HTTP level)
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleVillageEvents upgrades to WebSocket and streams world events.
// The token is supplied as a query param (?token=...) since browsers can't
// set custom headers on WebSocket connections. The session is re-verified
// inside the ping ticker so idle clients learn about expiry without having
// to make a doomed authed request first.
func (app *App) handleVillageEvents(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if res := app.verifyLLMMemoryToken(token); !res.Valid {
		// Reject before the upgrade so the client sees a real HTTP status.
		// 401 covers both missing and invalid tokens — the client treats
		// either as session-expired and bounces to the login screen.
		status := http.StatusUnauthorized
		if res.Reason == "realm" {
			status = http.StatusForbidden
		} else if res.Reason == "service" {
			status = http.StatusServiceUnavailable
		}
		log.Printf("WS auth reject: reason=%q tokenLen=%d", res.Reason, len(token))
		http.Error(w, "Auth required", status)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	// Load the idle-timeout setting BEFORE registering with the Hub.
	// loadPCIdleTimeout has a bounded context; if the DB stalled and we
	// were already in Hub.clients, the false-live entry would be exactly
	// what this PR is meant to eliminate. Done before addClient so any
	// upstream stall fails out without ever joining the Hub.
	idleTimeout := app.loadPCIdleTimeout(r.Context())
	pingInterval := idleTimeout / 2

	client := &wsClient{
		conn:         conn,
		send:         make(chan []byte, 64),
		sessionToken: token,
	}
	app.Hub.addClient(client)
	// Belt-and-suspenders cleanup. The writer goroutine has its own
	// defer-removeClient that fires when the send channel closes or a
	// write fails, but the read loop in this main goroutine is what
	// exits first on idle timeout / abrupt disconnect. Without an
	// explicit removeClient here the Hub.clients map keeps the entry
	// until a later broadcast attempts a write and the writer notices
	// the dead conn — that latency is what made the chronicler's
	// "is a PC observing right now?" check unreliable. removeClient is
	// idempotent so the writer's later call is a no-op.
	defer app.Hub.removeClient(client)

	// Writer goroutine — sends events to the client. Goes through
	// safeWrite so it doesn't race the ping ticker on the same conn.
	go func() {
		defer app.Hub.removeClient(client)
		for msg := range client.send {
			if err := client.safeWrite(websocket.TextMessage, msg, 10*time.Second); err != nil {
				return
			}
		}
	}()

	// Reader goroutine — handles pong and detects disconnect.
	// We don't expect messages from the client, but need to read
	// to process control frames (ping/pong/close).
	conn.SetReadDeadline(time.Now().Add(idleTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(idleTimeout))
		return nil
	})

	// Ping + session check ticker. Each tick: re-verify the session, then
	// send a keepalive ping. If the session went bad, push a session_expired
	// event through the send channel so the client can react before we close.
	// All writes go through safeWrite to serialize with the writer goroutine.
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for range ticker.C {
			if res := app.verifyLLMMemoryToken(client.sessionToken); !res.Valid && res.Reason != "service" {
				// Don't tear down on transient llm-memory unavailability — only
				// on a definitive "your token is no good" verdict.
				evt, _ := json.Marshal(WorldEvent{Type: "session_expired", Data: nil})
				select {
				case client.send <- evt:
				default:
				}
				// Brief delay so the writer can flush the event before close.
				time.Sleep(100 * time.Millisecond)
				app.Hub.removeClient(client)
				return
			}
			if err := client.safeWrite(websocket.PingMessage, nil, 10*time.Second); err != nil {
				return
			}
		}
	}()

	// Block on read — exits when client disconnects, the read deadline
	// expires (idle timeout), or any other read error. The deferred
	// removeClient at the top guarantees Hub.clients gets the entry
	// removed regardless of which exit path we took.
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			if isIdleTimeout(err) {
				log.Printf("pc-ws: dropping idle client (no pong within %s)", idleTimeout)
			}
			break
		}
	}
}

// isIdleTimeout reports whether the read error came from the deadline
// expiring (no pong within idleTimeout) versus a normal close, abrupt
// transport drop, or anything else. Used purely for log labelling so the
// admin can distinguish "client crashed" from "client closed cleanly."
func isIdleTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
