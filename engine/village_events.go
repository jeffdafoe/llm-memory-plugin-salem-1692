package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

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

	// sessionToken is the llm-memory token presented at the WS upgrade.
	// Re-verified inside the ping loop so an idle client whose session
	// expires (or is revoked by a deploy) gets pushed back to the login
	// screen instead of silently losing edits.
	sessionToken string
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

	client := &wsClient{
		conn:         conn,
		send:         make(chan []byte, 64),
		sessionToken: token,
	}
	app.Hub.addClient(client)

	// Writer goroutine — sends events to the client
	go func() {
		defer app.Hub.removeClient(client)
		for msg := range client.send {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	// Reader goroutine — handles pong and detects disconnect.
	// We don't expect messages from the client, but need to read
	// to process control frames (ping/pong/close).
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Ping + session check ticker. Each tick: re-verify the session, then
	// send a keepalive ping. If the session went bad, push a session_expired
	// event through the send channel so the client can react before we close.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
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
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}()

	// Block on read — exits when client disconnects
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}
