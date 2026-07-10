package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// fixedTranslator maps every event to the same test frame. The hub is
// vocabulary-agnostic, so the transport tests don't need real events — they
// drive Hub.Handle directly with a nil event and assert the frame round-trips.
func fixedTranslator(_ sim.Event) (WireFrame, bool) {
	return WireFrame{Type: "test_event", Data: map[string]any{"n": 1}}, true
}

// dropTranslator maps nothing — every event is engine-internal. Mirrors the
// common real case where most bus events have no client representation.
func dropTranslator(_ sim.Event) (WireFrame, bool) {
	return WireFrame{}, false
}

// newHubServer stands up a hub (Run goroutine started) attached to a seeded
// world's read server, behind an httptest server. Returns the test server and
// the hub so a test can drive Hub.Handle directly.
func newHubServer(t *testing.T, translate EventTranslator) (*httptest.Server, *Hub) {
	t.Helper()
	w := seededWorld(t)
	hub := NewHub(translate)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)

	srv := NewServer(w, okAuth{})
	srv.SetEventsHub(hub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, hub
}

// dialEvents opens a WS connection to the /events endpoint.
func dialEvents(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/village/events?token=test"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial %s: %v (status %d)", wsURL, err, resp.StatusCode)
		}
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readFrame reads one WS text frame and decodes it as a WireFrame.
func readFrame(t *testing.T, conn *websocket.Conn) WireFrame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var f WireFrame
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal frame %q: %v", data, err)
	}
	return f
}

// broadcastUntilReceived repeatedly drives Hub.Handle until conn reads a frame
// of wantType, or fails after a deadline. Registration (handler goroutine →
// hub.register) and the broadcast (Hub.Handle → hub.broadcast) travel on
// separate channels, so a single early broadcast can race ahead of the client
// being registered; retrying closes that window deterministically.
func broadcastUntilReceived(t *testing.T, conn *websocket.Conn, hub *Hub, wantType string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.Handle(nil, nil)
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, data, err := conn.ReadMessage()
		if err != nil {
			continue // timeout — client not registered yet; retry
		}
		var f WireFrame
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatalf("unmarshal frame %q: %v", data, err)
		}
		if f.Type == wantType {
			return
		}
	}
	t.Fatalf("did not receive a %q frame within deadline", wantType)
}

func TestNewHubNilTranslatorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewHub(nil) should panic")
		}
	}()
	NewHub(nil)
}

func TestHubHelloOnConnect(t *testing.T) {
	ts, _ := newHubServer(t, fixedTranslator)
	conn := dialEvents(t, ts)

	f := readFrame(t, conn)
	if f.Type != "hello" {
		t.Fatalf("first frame type = %q, want %q", f.Type, "hello")
	}
	data, ok := f.Data.(map[string]any)
	if !ok {
		t.Fatalf("hello frame data = %#v, want object", f.Data)
	}
	cv, ok := data["contract_version"].(float64)
	if !ok || int(cv) != ContractVersion {
		t.Fatalf("hello contract_version = %#v, want %d", data["contract_version"], ContractVersion)
	}
}

func TestHubBroadcastReachesClient(t *testing.T) {
	ts, hub := newHubServer(t, fixedTranslator)
	conn := dialEvents(t, ts)

	if f := readFrame(t, conn); f.Type != "hello" {
		t.Fatalf("expected hello first, got %q", f.Type)
	}
	broadcastUntilReceived(t, conn, hub, "test_event")
}

func TestHubUntranslatedEventDropped(t *testing.T) {
	ts, hub := newHubServer(t, dropTranslator)
	conn := dialEvents(t, ts)

	if f := readFrame(t, conn); f.Type != "hello" {
		t.Fatalf("expected hello first, got %q", f.Type)
	}
	// A dropTranslator returns ok=false, so Handle never enqueues a frame — no
	// frame should follow the hello regardless of registration timing.
	for i := 0; i < 3; i++ {
		hub.Handle(nil, nil)
	}
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("received a frame after hello, but untranslated events must be dropped")
	}
}

func TestHubRejectsConnectAfterShutdown(t *testing.T) {
	w := seededWorld(t)
	hub := NewHub(fixedTranslator)
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)

	srv := NewServer(w, okAuth{})
	srv.SetEventsHub(hub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cancel()   // shut the hub down
	<-hub.done // wait until Run has exited and closed done

	conn := dialEvents(t, ts)
	// The handler sees the closed hub and closes the socket after the upgrade —
	// no hello frame should arrive; the read returns an error instead.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected the connection to be closed after hub shutdown, but a frame arrived")
	}
}

func TestHubClientDisconnectDoesNotWedge(t *testing.T) {
	ts, hub := newHubServer(t, fixedTranslator)

	c1 := dialEvents(t, ts)
	if f := readFrame(t, c1); f.Type != "hello" {
		t.Fatalf("c1 expected hello first, got %q", f.Type)
	}
	c2 := dialEvents(t, ts)
	if f := readFrame(t, c2); f.Type != "hello" {
		t.Fatalf("c2 expected hello first, got %q", f.Type)
	}

	// Drop c1 abruptly; the hub must keep serving c2.
	_ = c1.Close()
	broadcastUntilReceived(t, c2, hub, "test_event")
}

// waitConnected polls the hub's live connected count until it reaches want or a
// deadline lapses. Registration/unregistration travel on channels serviced by
// Run, so the count updates asynchronously after a dial/close.
func waitConnected(t *testing.T, hub *Hub, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.Stats().ClientsConnected == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("clients_connected did not reach %d within deadline (last=%d)", want, hub.Stats().ClientsConnected)
}

func TestHubStatsCountsBroadcastAndDrops(t *testing.T) {
	// No Run goroutine: nothing drains the broadcast channel, so it fills at
	// exactly broadcastBuffer and every Handle past that is a counted drop.
	hub := NewHub(fixedTranslator)
	const extra = 10
	for i := 0; i < broadcastBuffer+extra; i++ {
		hub.Handle(nil, nil)
	}
	st := hub.Stats()
	if st.FramesBroadcast != broadcastBuffer {
		t.Errorf("frames_broadcast = %d, want %d", st.FramesBroadcast, broadcastBuffer)
	}
	if st.FramesDropped != extra {
		t.Errorf("frames_dropped = %d, want %d", st.FramesDropped, extra)
	}
}

func TestHubStatsIgnoresUntranslatedEvents(t *testing.T) {
	// A dropTranslator returns ok=false, so Handle never enqueues a frame —
	// neither the broadcast nor the drop counter should move.
	hub := NewHub(dropTranslator)
	for i := 0; i < 5; i++ {
		hub.Handle(nil, nil)
	}
	st := hub.Stats()
	if st.FramesBroadcast != 0 || st.FramesDropped != 0 {
		t.Errorf("untranslated events counted: broadcast=%d dropped=%d, want 0/0", st.FramesBroadcast, st.FramesDropped)
	}
}

func TestHubStatsConnectedCount(t *testing.T) {
	ts, hub := newHubServer(t, fixedTranslator)
	conn := dialEvents(t, ts)
	if f := readFrame(t, conn); f.Type != "hello" {
		t.Fatalf("expected hello first, got %q", f.Type)
	}
	// Run records the registration asynchronously; poll until it lands.
	waitConnected(t, hub, 1)
	// A clean disconnect unregisters via the read pump → connected returns to 0.
	_ = conn.Close()
	waitConnected(t, hub, 0)
}

// ConnectedLogins refcounts a login across its sockets (LLM-342): present after
// either of two sockets registers, still present when one closes, gone only when
// the last closes. An empty login (a token with no principal name) is never
// tracked. White-box (direct track/untrack) so the refcount transitions are
// deterministic, without racing the async register/unregister channel.
func TestHubConnectedLogins_Refcount(t *testing.T) {
	hub := NewHub(dropTranslator)

	if got := len(hub.ConnectedLogins()); got != 0 {
		t.Fatalf("fresh hub ConnectedLogins len = %d, want 0", got)
	}

	hub.trackLogin("alice")
	hub.trackLogin("alice")
	if _, ok := hub.ConnectedLogins()["alice"]; !ok {
		t.Fatal("alice not connected after tracking two sockets")
	}
	hub.untrackLogin("alice")
	if _, ok := hub.ConnectedLogins()["alice"]; !ok {
		t.Fatal("alice dropped after one of two sockets closed (refcount bug)")
	}
	hub.untrackLogin("alice")
	if _, ok := hub.ConnectedLogins()["alice"]; ok {
		t.Fatal("alice still connected after both sockets closed")
	}

	hub.trackLogin("")
	if got := len(hub.ConnectedLogins()); got != 0 {
		t.Fatalf("empty login tracked; ConnectedLogins len = %d, want 0", got)
	}
}

// End to end: a live WS client's login appears in ConnectedLogins on connect and
// clears on disconnect — the signal the PC presence heartbeat reads. okAuth
// resolves every token to login "tester".
func TestHubConnectedLogins_WSLifecycle(t *testing.T) {
	ts, hub := newHubServer(t, dropTranslator)

	conn := dialEvents(t, ts)
	waitForConnectedLogin(t, hub, "tester", true)

	_ = conn.Close()
	waitForConnectedLogin(t, hub, "tester", false)
}

// waitForConnectedLogin polls hub.ConnectedLogins until login's presence matches
// want, or fails after a short deadline. Register/unregister are asynchronous
// (handler → hub goroutine, read pump → hub goroutine), so a direct assert would
// race the hub goroutine.
func waitForConnectedLogin(t *testing.T, hub *Hub, login string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hub.ConnectedLogins()[login]; ok == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("login %q connected=%v not reached within deadline", login, want)
}

// A WS connection stamps its PC's presence immediately on register (LLM-342), not
// only on the next heartbeat tick — so a freshly connected or reconnected PC
// whose LastPCSeenAt is nil/stale can't be swept in the gap before the first tick
// (code_review finding). No heartbeat runs in this test, so a non-nil stamp
// proves the on-connect path fired, not a background ticker.
func TestHandleEvents_StampsPresenceOnConnect(t *testing.T) {
	w := seededWorld(t)
	// Give the seeded PC (bram) the login okAuth resolves every token to, so the
	// connecting socket maps to it.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["bram"].LoginUsername = "tester"
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed bram login: %v", err)
	}
	if seen := w.Published().Actors["bram"].LastPCSeenAt; seen != nil {
		t.Fatalf("precondition: bram already stamped (%v), want nil", seen)
	}

	hub := NewHub(dropTranslator)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	srv := NewServer(w, okAuth{})
	srv.SetEventsHub(hub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	_ = dialEvents(t, ts)

	// The stamp is a world command sent from the register path; poll the published
	// snapshot until it lands (Dial can return before the command executes).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Published().Actors["bram"].LastPCSeenAt != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("bram not stamped present after WS connect (on-connect stamp missing)")
}
