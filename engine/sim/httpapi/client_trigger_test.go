package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// LLM-881 — the reverse channel: a ClientTrigger reaches only its player's
// sockets, and the umbilical route is a thin caller of the same event.

// loginAuth resolves each token to a login of the same name, so a test can
// connect sockets as different players.
type loginAuth struct{}

func (loginAuth) Verify(token string) VerifyResult {
	if token == "" {
		return VerifyResult{Reason: "missing"}
	}
	return VerifyResult{Valid: true, User: &AuthUser{Username: token, Realms: []string{"salem"}, Permissions: operatorPerms}}
}

func TestTranslateEvent_ClientTrigger(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ClientTrigger{
		ActorID: "bram", Login: "alice", Kind: "treeline_figure", Detail: json.RawMessage(`{"a":1}`),
	})
	if !ok {
		t.Fatal("ClientTrigger should translate")
	}
	if frame.Type != "client_trigger" || frame.Target != "alice" {
		t.Fatalf("frame type=%q target=%q, want client_trigger / alice", frame.Type, frame.Target)
	}
	b, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The target is for the hub only; the client sees {type, data:{kind, detail}}.
	want := `{"type":"client_trigger","data":{"kind":"treeline_figure","detail":{"a":1}}}`
	if string(b) != want {
		t.Errorf("wire = %s, want %s", b, want)
	}
}

func TestTranslateEvent_ClientTriggerNoLoginDropped(t *testing.T) {
	// An empty Target would broadcast a private effect to everyone.
	if _, ok := TranslateEvent(&sim.ClientTrigger{ActorID: "bram", Kind: "treeline_figure"}); ok {
		t.Error("ClientTrigger with no login translated; want dropped")
	}
}

func TestTranslateEvent_ClientTriggerNilDetailIsObject(t *testing.T) {
	frame, _ := TranslateEvent(&sim.ClientTrigger{ActorID: "bram", Login: "alice", Kind: "treeline_figure"})
	b, _ := json.Marshal(frame)
	if !strings.Contains(string(b), `"detail":{}`) {
		t.Errorf("wire = %s, want detail {}", b)
	}
}

// sentinelTranslator translates real events with TranslateEvent and maps a nil
// event to a broadcast "sentinel" frame. Frames reach Run in order, so a client
// that reads the sentinel has already been sent every earlier frame meant for it.
func sentinelTranslator(evt sim.Event) (WireFrame, bool) {
	if evt == nil {
		return WireFrame{Type: "sentinel"}, true
	}
	return TranslateEvent(evt)
}

func dialEventsAs(t *testing.T, ts *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/village/events?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial as %s: %v", token, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// framesUntilSentinel reads frames until the sentinel and returns the types
// read before it.
func framesUntilSentinel(t *testing.T, conn *websocket.Conn) []string {
	t.Helper()
	var types []string
	for {
		f := readFrame(t, conn)
		if f.Type == "sentinel" {
			return types
		}
		types = append(types, f.Type)
	}
}

func countType(types []string, want string) int {
	n := 0
	for _, ty := range types {
		if ty == want {
			n++
		}
	}
	return n
}

// triggerWorld is the seeded world with two PCs: bram (alice) and cara (bob).
func triggerWorld(t *testing.T) *sim.World {
	t.Helper()
	w := seededWorld(t)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["bram"].LoginUsername = "alice"
		world.Actors["cara"] = &sim.Actor{ID: "cara", DisplayName: "Cara", Kind: sim.KindPC, LoginUsername: "bob", Pos: sim.TilePos{X: 2, Y: 2}}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed PCs: %v", err)
	}
	return w
}

// Engine code emitting a ClientTrigger reaches every socket of that player's
// login and no other player's socket.
func TestClientTrigger_ReachesOnlyTargetLogin(t *testing.T) {
	w := triggerWorld(t)
	hub := NewHub(sentinelTranslator)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(hub)
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe hub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	srv := NewServer(w, loginAuth{})
	srv.SetEventsHub(hub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	alice1 := dialEventsAs(t, ts, "alice")
	alice2 := dialEventsAs(t, ts, "alice")
	bob := dialEventsAs(t, ts, "bob")
	waitConnected(t, hub, 3)

	if _, err := w.Send(sim.SendClientTrigger("bram", "treeline_figure", nil, time.Now())); err != nil {
		t.Fatalf("SendClientTrigger: %v", err)
	}
	hub.Handle(nil, nil) // sentinel, broadcast to all

	for name, conn := range map[string]*websocket.Conn{"alice socket 1": alice1, "alice socket 2": alice2} {
		if n := countType(framesUntilSentinel(t, conn), "client_trigger"); n != 1 {
			t.Errorf("%s got %d client_trigger frames, want 1", name, n)
		}
	}
	if n := countType(framesUntilSentinel(t, bob), "client_trigger"); n != 0 {
		t.Errorf("bob got %d client_trigger frames, want 0", n)
	}
}

func clientTriggerServer(t *testing.T) (*Server, http.Handler, *sim.World) {
	t.Helper()
	w := triggerWorld(t)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	srv.SetControlEnabled(true)
	return srv, srv.Handler(), w
}

// The route emits the same ClientTrigger event (no separate frame path),
// audits, and reports whether the login holds a socket.
func TestUmbilicalClientTrigger_EmitsEvent(t *testing.T) {
	srv, h, w := clientTriggerServer(t)
	rec := &triggerRecorder{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(rec)
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	hub := NewHub(TranslateEvent)
	hub.trackLogin("alice") // a live socket for alice, without dialing one
	srv.SetEventsHub(hub)
	h = srv.Handler()

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	resp := postReq(t, h, "/api/village/umbilical/client-trigger", "tok", `{"actor_id":"bram","kind":"treeline_figure","detail":{"x":2}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("client-trigger = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var out umbilicalClientTriggerResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := umbilicalClientTriggerResponse{ActorID: "bram", Login: "alice", Kind: "treeline_figure", Connected: true}
	if out != want {
		t.Errorf("response = %+v, want %+v", out, want)
	}
	if len(rec.events) != 1 || rec.events[0].Login != "alice" || string(rec.events[0].Detail) != `{"x":2}` {
		t.Errorf("emitted = %+v, want one ClientTrigger for alice with detail {\"x\":2}", rec.events)
	}
	if !strings.Contains(logs.String(), `umbilical AUDIT: operator="op" action="client-trigger" actor=bram kind=treeline_figure`) {
		t.Errorf("no audit line; log = %q", logs.String())
	}

	// cara (bob) holds no socket.
	resp = postReq(t, h, "/api/village/umbilical/client-trigger", "tok", `{"actor_id":"cara","kind":"treeline_figure"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("client-trigger cara = %d; body=%s", resp.Code, resp.Body.String())
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &out)
	if out.Connected {
		t.Error("cara connected = true, want false (bob holds no socket)")
	}
}

type triggerRecorder struct{ events []*sim.ClientTrigger }

func (r *triggerRecorder) Handle(_ *sim.World, evt sim.Event) {
	if e, ok := evt.(*sim.ClientTrigger); ok {
		r.events = append(r.events, e)
	}
}

// The route passes detail numbers through unchanged: a float64 round-trip would
// turn 9007199254740993 into ...992 (code_review).
func TestUmbilicalClientTrigger_DetailNumbersUnchanged(t *testing.T) {
	_, h, w := clientTriggerServer(t)
	rec := &triggerRecorder{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(rec)
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	resp := postReq(t, h, "/api/village/umbilical/client-trigger", "tok", `{"actor_id":"bram","kind":"treeline_figure","detail":{"id":9007199254740993,"f":0.5}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("client-trigger = %d; body=%s", resp.Code, resp.Body.String())
	}
	if len(rec.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(rec.events))
	}
	if got := string(rec.events[0].Detail); got != `{"f":0.5,"id":9007199254740993}` {
		t.Errorf("detail = %s, want {\"f\":0.5,\"id\":9007199254740993}", got)
	}
}

func TestUmbilicalClientTrigger_Statuses(t *testing.T) {
	_, h, _ := clientTriggerServer(t)
	big := `{"s":"` + strings.Repeat("x", sim.ClientTriggerMaxDetail) + `"}`
	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing actor_id", `{"kind":"treeline_figure"}`, http.StatusBadRequest},
		{"unknown kind", `{"actor_id":"bram","kind":"fireworks"}`, http.StatusBadRequest},
		{"missing kind", `{"actor_id":"bram"}`, http.StatusBadRequest},
		{"detail not an object", `{"actor_id":"bram","kind":"treeline_figure","detail":[1]}`, http.StatusBadRequest},
		{"oversize detail", `{"actor_id":"bram","kind":"treeline_figure","detail":` + big + `}`, http.StatusBadRequest},
		{"unknown actor", `{"actor_id":"ghost","kind":"treeline_figure"}`, http.StatusNotFound},
		{"not a PC", `{"actor_id":"hannah","kind":"treeline_figure"}`, http.StatusUnprocessableEntity},
		{"null detail", `{"actor_id":"bram","kind":"treeline_figure","detail":null}`, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := postReq(t, h, "/api/village/umbilical/client-trigger", "tok", tc.body); rec.Code != tc.want {
				t.Errorf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// A PC with no login has no client to send to.
func TestUmbilicalClientTrigger_PCWithoutLogin(t *testing.T) {
	_, h, w := clientTriggerServer(t)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["cara"].LoginUsername = ""
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear login: %v", err)
	}
	if rec := postReq(t, h, "/api/village/umbilical/client-trigger", "tok", `{"actor_id":"cara","kind":"treeline_figure"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

// Control off: the route is not registered, and the manifest does not list it.
func TestUmbilicalClientTrigger_AbsentWithoutControl(t *testing.T) {
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()
	if rec := postReq(t, h, "/api/village/umbilical/client-trigger", "tok", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("control off = %d, want 404", rec.Code)
	}
	if rec := req(t, h, "/api/village/umbilical", "tok"); strings.Contains(rec.Body.String(), "client-trigger") {
		t.Error("manifest lists client-trigger with control off")
	}

	_, on, _ := clientTriggerServer(t)
	if rec := req(t, on, "/api/village/umbilical", "tok"); !strings.Contains(rec.Body.String(), "/client-trigger") {
		t.Errorf("manifest with control on does not list client-trigger; body=%s", rec.Body.String())
	}
}
