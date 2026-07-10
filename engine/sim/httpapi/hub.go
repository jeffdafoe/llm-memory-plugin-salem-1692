package httpapi

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// WireFrame is the envelope every WS message uses: a type discriminator plus
// an optional data payload. It matches the v1 client's {type, data} dispatch
// shape (event_client.gd _handle_message), so the client's existing frame
// router needs no envelope rewrite. The hello frame and every translated
// event are WireFrames.
type WireFrame struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

// EventTranslator maps an in-world sim.Event to a client-facing WireFrame.
// ok=false means the event has no client representation and is dropped — the
// common case, since most events are engine-internal (per-tile ActorMoved,
// warrant plumbing, cascade bookkeeping). It is invoked from Hub.Handle, which
// runs ON THE WORLD GOROUTINE, so a translator MUST be pure and non-blocking:
// build the frame from the event and return. The concrete event→frame
// vocabulary is filled in by a later slice; the hub is vocabulary-agnostic.
type EventTranslator func(evt sim.Event) (WireFrame, bool)

const (
	// broadcastBuffer bounds the world→hub frame queue. Hub.Handle (world
	// goroutine) does a non-blocking send onto it; if the hub goroutine falls
	// this far behind, frames are dropped (logged) rather than stalling the
	// world goroutine on client fan-out. Each queued frame is tiny and the hub
	// goroutine only forwards to per-client buffered channels, so this rarely
	// fills outside pathological slow-consumer storms.
	broadcastBuffer = 256
	// clientSendBuffer bounds one client's outbound queue. A client that can't
	// drain this fast is evicted (slow-consumer eviction in Hub.Run) so a
	// single stuck socket can't back-pressure the hub or the world.
	clientSendBuffer = 64
	// regBuffer sizes the register/unregister channels. Small — these are
	// connect/disconnect events, not a hot path.
	regBuffer = 16
)

// Hub fans world events out to all connected WebSocket clients.
//
// It implements sim.EventSubscriber. Handle runs synchronously on the world
// goroutine after each event's mutation lands; it translates the event to a
// WireFrame, marshals it ONCE, and hands the bytes to the hub goroutine via a
// non-blocking send — never touching a socket itself (the bus contract forbids
// blocking I/O on the world goroutine). Run, in its own goroutine, owns the
// client set (so no mutex guards it) and forwards each frame to every client's
// buffered send channel. Per-client write pumps (client.writePump) are the only
// goroutines that write to a socket, as gorilla requires.
type Hub struct {
	translate  EventTranslator
	broadcast  chan []byte
	register   chan *client
	unregister chan *client
	// done is closed when Run returns. Read pumps select on it so a client
	// disconnecting during shutdown doesn't block forever sending to
	// unregister after the hub goroutine has already exited.
	done chan struct{}

	// Delivery counters (WORK-434), surfaced via Stats() on /umbilical/state.
	// framesBroadcast / framesDropped are touched on the WORLD goroutine (Handle);
	// clientsEvicted / clientsConnected on the HUB goroutine (Run). Stats() reads
	// them from an HTTP-handler goroutine, so all four are atomic. Cumulative
	// since engine start (clientsConnected is the live count), reset on restart —
	// in-memory, no durability need, same posture as the telemetry/error rings.
	framesBroadcast  atomic.Uint64
	framesDropped    atomic.Uint64
	clientsEvicted   atomic.Uint64
	clientsConnected atomic.Int64

	// connectedLogins refcounts the login of every live WS client (LLM-342), so
	// the PC presence heartbeat (sim.RunPCPresenceHeartbeat) can tell which PCs
	// currently hold a socket. Refcounted because one player may hold several
	// sockets (multiple tabs); a login stays present until its last socket drops.
	// Mutated only under mu — written from the hub goroutine (register /
	// unregister / slow-consumer eviction), read from the heartbeat goroutine via
	// ConnectedLogins. Separate from the mutex-free clients map, which the hub
	// goroutine owns exclusively; only this cross-goroutine view needs the lock.
	mu              sync.Mutex
	connectedLogins map[string]int
}

// NewHub builds a hub that translates events via translate. Panics on a nil
// translator — a wiring bug that should fail loudly at startup.
func NewHub(translate EventTranslator) *Hub {
	if translate == nil {
		panic("httpapi: NewHub requires a non-nil translator")
	}
	return &Hub{
		translate:       translate,
		broadcast:       make(chan []byte, broadcastBuffer),
		register:        make(chan *client, regBuffer),
		unregister:      make(chan *client, regBuffer),
		done:            make(chan struct{}),
		connectedLogins: make(map[string]int),
	}
}

// trackLogin increments the refcount for a connecting client's login. An empty
// login (a token that resolved to no principal name) is never tracked.
func (h *Hub) trackLogin(login string) {
	if login == "" {
		return
	}
	h.mu.Lock()
	h.connectedLogins[login]++
	h.mu.Unlock()
}

// untrackLogin decrements the refcount for a disconnecting client's login,
// deleting the entry when its last socket drops. The inverse of trackLogin;
// empty logins were never tracked, so they no-op.
func (h *Hub) untrackLogin(login string) {
	if login == "" {
		return
	}
	h.mu.Lock()
	if h.connectedLogins[login] <= 1 {
		delete(h.connectedLogins, login)
	} else {
		h.connectedLogins[login]--
	}
	h.mu.Unlock()
}

// ConnectedLogins snapshots the set of logins that currently hold at least one
// live WS client. Safe to call from any goroutine; the PC presence heartbeat
// reads it each tick to refresh those PCs' LastPCSeenAt (LLM-342). Satisfies
// sim.ConnectedPCSource.
func (h *Hub) ConnectedLogins() map[string]struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]struct{}, len(h.connectedLogins))
	for login := range h.connectedLogins {
		out[login] = struct{}{}
	}
	return out
}

// Handle satisfies sim.EventSubscriber. It runs on the world goroutine after
// the emitting command's mutation lands. It translates + marshals the frame,
// then NON-BLOCKING sends the bytes to the hub goroutine: if the broadcast
// queue is full the frame is dropped (logged), because the world goroutine must
// never block on client delivery. A dropped frame is recovered by the client's
// REST resync on reconnect; live drops under extreme load are cosmetic for a
// read-only viewer.
func (h *Hub) Handle(_ *sim.World, evt sim.Event) {
	frame, ok := h.translate(evt)
	if !ok {
		return
	}
	data, err := json.Marshal(frame)
	if err != nil {
		log.Printf("httpapi: marshal WS frame (type=%q): %v", frame.Type, err)
		return
	}
	// Stop enqueueing once Run has shut down — there is no consumer, so frames
	// would only accumulate as dead drops. Checked first so a closed hub is
	// preferred over a not-yet-full broadcast buffer.
	select {
	case <-h.done:
		return
	default:
	}
	select {
	case h.broadcast <- data:
		h.framesBroadcast.Add(1)
	default:
		h.framesDropped.Add(1)
		log.Printf("httpapi: WS broadcast buffer full, dropping frame (type=%q)", frame.Type)
	}
}

// Run owns the client set and fans broadcasts out to it. Start it in a goroutine
// BEFORE world.Run (the hub must also be Subscribed before Run). It returns when
// ctx is cancelled, signalling its done channel and closing every client's send
// channel so the write pumps emit a close frame and exit.
//
// Run MUST be called at most once per Hub: it closes h.done on return, so a
// second call would close an already-closed channel and panic. A stopped hub is
// permanently dead (build a fresh one) — same single-run posture as the engine's
// other lifecycle types (TickWorkerPool, the tickers).
//
// Every mutation of the clients map happens here, in this one goroutine, so the
// map needs no mutex. The world goroutine only ever does a channel send onto
// broadcast; HTTP-handler goroutines only ever send onto register/unregister.
func (h *Hub) Run(ctx context.Context) {
	clients := make(map[*client]struct{})
	defer func() {
		// Signal read pumps first (so a concurrent unregister send unblocks via
		// done), then close sends so write pumps exit cleanly.
		close(h.done)
		for c := range clients {
			close(c.send)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			clients[c] = struct{}{}
			h.trackLogin(c.login)
			h.clientsConnected.Store(int64(len(clients)))
		case c := <-h.unregister:
			// Guarded so an already-evicted client (slow-consumer path below)
			// isn't closed twice when its read pump later unregisters it — and so
			// its login refcount is decremented exactly once.
			if _, ok := clients[c]; ok {
				delete(clients, c)
				close(c.send)
				h.untrackLogin(c.login)
				h.clientsConnected.Store(int64(len(clients)))
			}
		case data := <-h.broadcast:
			evicted := false
			for c := range clients {
				select {
				case c.send <- data:
				default:
					// Slow consumer: evict. Closing send makes the write pump
					// emit a close frame and exit; the read pump then errors and
					// tries to unregister, which is a no-op (already deleted).
					delete(clients, c)
					close(c.send)
					h.untrackLogin(c.login)
					h.clientsEvicted.Add(1)
					evicted = true
				}
			}
			if evicted {
				h.clientsConnected.Store(int64(len(clients)))
			}
		}
	}
}

// WSDeliveryStatsDTO is the WS event-hub delivery accounting, surfaced on
// /umbilical/state (UmbilicalStateDTO.ws). It gives an operator remote
// visibility into frame-drop / slow-consumer health without SSH to the box.
//
//   - frames_broadcast: client-facing frames the hub accepted onto the broadcast
//     queue (cumulative).
//   - frames_dropped: client-facing frames dropped because the broadcast queue
//     was full. The world goroutine never blocks on delivery, so an overrun is a
//     silent drop recovered only by the client's REST resync on reconnect. A
//     nonzero value after a stale-client report (e.g. a noticeboard not updating
//     live) is the confirming signal — low-frequency frames (a board flips ~twice
//     daily) don't self-heal from a drop the way frequent ones (lamp phase flips)
//     do.
//   - clients_evicted: slow-consumer evictions — a client whose own send buffer
//     filled and was dropped (cumulative).
//   - clients_connected: WS clients connected right now.
type WSDeliveryStatsDTO struct {
	FramesBroadcast  uint64 `json:"frames_broadcast"`
	FramesDropped    uint64 `json:"frames_dropped"`
	ClientsEvicted   uint64 `json:"clients_evicted"`
	ClientsConnected int64  `json:"clients_connected"`
}

// Stats snapshots the hub's delivery counters. Safe to call from any goroutine
// (all fields atomic). The four loads are independent, so the snapshot is
// near-consistent rather than a single atomic instant — fine for an operator
// health read.
func (h *Hub) Stats() WSDeliveryStatsDTO {
	return WSDeliveryStatsDTO{
		FramesBroadcast:  h.framesBroadcast.Load(),
		FramesDropped:    h.framesDropped.Load(),
		ClientsEvicted:   h.clientsEvicted.Load(),
		ClientsConnected: h.clientsConnected.Load(),
	}
}

// helloFrame is the first frame sent to every client on connect. It carries the
// contract version so the client can fail loudly on a mismatch (same rule as the
// REST surface). Sent per-connection, not broadcast.
func helloFrame() WireFrame {
	return WireFrame{
		Type: "hello",
		Data: map[string]any{"contract_version": ContractVersion},
	}
}
