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
func (app *App) handleVillageEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	client := &wsClient{
		conn: conn,
		send: make(chan []byte, 64),
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

	// Ping ticker to keep connection alive
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
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
