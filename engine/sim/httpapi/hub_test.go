package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

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
	return newHubServerWith(t, translate, nil)
}

// newHubServerWith is newHubServer with a hook that adjusts the hub (its
// keepalive timings) before Run and the HTTP server start, so the goroutines
// that read those fields start after the write.
func newHubServerWith(t *testing.T, translate EventTranslator, adjust func(*Hub)) (*httptest.Server, *Hub) {
	t.Helper()
	w := seededWorld(t)
	hub := NewHub(translate)
	if adjust != nil {
		adjust(hub)
	}
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
	return dialEventsAs(t, ts, "test")
}

// readMessage reads one WS message within d. In coder/websocket a read whose
// context ends closes the connection, so a timeout here is final for conn: use
// it only where the message must come or the connection must end.
func readMessage(conn *websocket.Conn, d time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, data, err := conn.Read(ctx)
	return data, err
}

// readFrame reads one WS text frame and decodes it as a WireFrame.
func readFrame(t *testing.T, conn *websocket.Conn) WireFrame {
	t.Helper()
	data, err := readMessage(conn, 2*time.Second)
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
// being registered; retrying closes that window deterministically. One reader
// goroutine holds the whole deadline (a timed-out read would close conn) while
// this loop re-drives Handle every 100ms.
func broadcastUntilReceived(t *testing.T, conn *websocket.Conn, hub *Hub, wantType string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := make(chan error, 1)
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				got <- err
				return
			}
			var f WireFrame
			if err := json.Unmarshal(data, &f); err != nil {
				got <- fmt.Errorf("unmarshal frame %q: %w", data, err)
				return
			}
			if f.Type == wantType {
				got <- nil
				return
			}
		}
	}()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	hub.Handle(nil, nil)
	for {
		select {
		case err := <-got:
			if err != nil {
				t.Fatalf("did not receive a %q frame: %v", wantType, err)
			}
			return
		case <-tick.C:
			hub.Handle(nil, nil)
		}
	}
}

// The origin policy is coder/websocket's default (acceptOptions is the zero
// value): no Origin passes (native Godot), an Origin whose host is the request
// Host passes in any letter case (the web-export client), and any other Origin
// is refused with 403 before the upgrade.
func TestEventsOriginPolicy(t *testing.T) {
	ts, _ := newHubServer(t, fixedTranslator)
	host := strings.TrimPrefix(ts.URL, "http://")
	wsURL := "ws://" + host + "/api/village/events?token=test"
	cases := []struct {
		name   string
		origin string
		want   int
	}{
		{"no origin", "", http.StatusSwitchingProtocols},
		{"same origin", "http://" + host, http.StatusSwitchingProtocols},
		{"same origin, other case", "HTTP://" + strings.ToUpper(host), http.StatusSwitchingProtocols},
		{"cross origin", "https://evil.example", http.StatusForbidden},
		{"same host name, other port", "http://127.0.0.1:1", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
			if c.origin != "" {
				opts.HTTPHeader.Set("Origin", c.origin)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, resp, err := websocket.Dial(ctx, wsURL, opts)
			if conn != nil {
				_ = conn.CloseNow()
			}
			if resp == nil {
				t.Fatalf("no response: %v", err)
			}
			if resp.StatusCode != c.want {
				t.Fatalf("status %d, want %d (err %v)", resp.StatusCode, c.want, err)
			}
		})
	}
}

// fastKeepalive pings every 30ms and allows 200ms per ping.
func fastKeepalive(h *Hub) {
	h.pingPeriod = 30 * time.Millisecond
	h.writeWait = 200 * time.Millisecond
}

// A peer that never answers pings is closed by pingLoop and unregistered by
// the read pump. A coder client that never calls Read never answers a ping.
func TestUnansweredPingDropsClient(t *testing.T) {
	ts, hub := newHubServerWith(t, dropTranslator, fastKeepalive)
	_ = dialEvents(t, ts)
	waitConnected(t, hub, 1)
	waitConnected(t, hub, 0)
}

// A peer that keeps reading answers every ping (coder's reader replies
// automatically) and stays connected across many ping periods.
func TestAnsweredPingsKeepClient(t *testing.T) {
	ts, hub := newHubServerWith(t, dropTranslator, fastKeepalive)
	conn := dialEvents(t, ts)
	go func() {
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}()
	waitConnected(t, hub, 1)
	time.Sleep(600 * time.Millisecond) // about 20 ping periods
	if got := hub.Stats().ClientsConnected; got != 1 {
		t.Fatalf("clients_connected = %d after answering pings, want 1", got)
	}
}

// Hub shutdown while a ping waits for its pong: the hub closes send, the write
// pump sends its close frame and closes the socket, and the client's read sees
// the connection end. The peer never reads, so the ping is still waiting when
// the hub stops.
func TestHubShutdownDuringPing(t *testing.T) {
	w := seededWorld(t)
	hub := NewHub(dropTranslator)
	hub.pingPeriod = 30 * time.Millisecond
	hub.writeWait = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)
	srv := NewServer(w, okAuth{})
	srv.SetEventsHub(hub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	conn := dialEvents(t, ts)
	waitConnected(t, hub, 1)
	time.Sleep(100 * time.Millisecond) // a ping is now waiting for its pong
	cancel()
	<-hub.done
	// The write pump's close waits up to 5s for a close reply this peer never
	// sends, then closes the socket; the read below must end by then.
	if _, err := readMessage(conn, 8*time.Second); err == nil {
		// The hello may still be unread; the next read must fail.
		if _, err := readMessage(conn, 8*time.Second); err == nil {
			t.Fatal("connection still open after hub shutdown")
		}
	}
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
	if _, err := readMessage(conn, 300*time.Millisecond); err == nil {
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
	if _, err := readMessage(conn, 2*time.Second); err == nil {
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

	// Drop c1 abruptly (no close frame); the hub must keep serving c2.
	_ = c1.CloseNow()
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
	// Close sends a close frame and waits for the server's reply.
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Errorf("close handshake: %v", err)
	}
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

	_ = conn.CloseNow()
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
