package cascade

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// messenger_news_test.go — LLM-700: a messenger's spawn starts one news call to the
// salem-news VA, and its reply becomes the word he carries. No other spawn calls.

func buildMessengerNewsWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	seed := map[sim.ActorID]*sim.Actor{
		"vstr-roger": {ID: "vstr-roger", DisplayName: "Roger Standish the messenger", Kind: sim.KindNPCShared,
			VisitorState: &sim.VisitorState{Archetype: sim.MessengerArchetype, Phase: sim.VisitorPhaseArriving}},
		"vstr-ephraim": {ID: "vstr-ephraim", DisplayName: "Ephraim Pollard the itinerant musician", Kind: sim.KindNPCShared,
			VisitorState: &sim.VisitorState{Archetype: "itinerant musician", Phase: sim.VisitorPhaseArriving}},
		"john": {ID: "john", DisplayName: "John Ellis", Kind: sim.KindNPCShared},
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for id, a := range seed {
			world.Actors[id] = a
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return w, func() {
		cancel()
		<-done
	}
}

// spawnEvent runs the subscriber body on the world goroutine for a spawn of id, the
// way World.emit would, and reports whether it started a news call.
func spawnEvent(t *testing.T, w *sim.World, client llm.Client, evt sim.Event) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return startMessengerNews(context.Background(), world, client, evt), nil
	}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	return res.(bool)
}

func payloadOf(t *testing.T, w *sim.World, id sim.ActorID) string {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id].VisitorState.Payload, nil
	}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	return res.(string)
}

func TestMessengerNews_SpawnInstallsNews(t *testing.T) {
	w, stop := buildMessengerNewsWorld(t)
	defer stop()
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		Content: "that Port Royal in Jamaica was swallowed by a great earthquake in June.",
	}})

	at := time.Date(2026, 10, 2, 18, 0, 0, 0, time.UTC)
	if !spawnEvent(t, w, client, &sim.NPCCreated{ActorID: "vstr-roger", DisplayName: "Roger Standish the messenger", At: at}) {
		t.Fatal("a messenger's spawn did not start a news call")
	}

	want := "Port Royal in Jamaica was swallowed by a great earthquake in June"
	deadline := time.Now().Add(2 * time.Second)
	for payloadOf(t, w, "vstr-roger") != want {
		if time.Now().After(deadline) {
			t.Fatalf("payload = %q, want %q", payloadOf(t, w, "vstr-roger"), want)
		}
		time.Sleep(10 * time.Millisecond)
	}

	reqs := client.Requests()
	if len(reqs) != 1 {
		t.Fatalf("calls = %d, want 1", len(reqs))
	}
	if reqs[0].Model != messengerNewsLLMModel {
		t.Errorf("model = %q, want %q", reqs[0].Model, messengerNewsLLMModel)
	}
	prompt := reqs[0].Messages[0].Content
	if !strings.Contains(prompt, "October 2, 1692") {
		t.Errorf("prompt does not carry the 1692 date for the spawn day:\n%s", prompt)
	}
}

func TestMessengerNews_NoCallForOthers(t *testing.T) {
	w, stop := buildMessengerNewsWorld(t)
	defer stop()
	client := llm.NewFakeClient()
	at := time.Now()

	if spawnEvent(t, w, client, &sim.NPCCreated{ActorID: "vstr-ephraim", At: at}) {
		t.Error("a musician's spawn started a news call")
	}
	if spawnEvent(t, w, client, &sim.NPCCreated{ActorID: "john", At: at}) {
		t.Error("a resident's creation started a news call")
	}
	if spawnEvent(t, w, client, &sim.ActorDeparted{ActorID: "vstr-roger", At: at}) {
		t.Error("an event other than NPCCreated started a news call")
	}
	if _, err := w.Send(sim.SetMessengerNews("vstr-roger", "a fort rose at Pemaquid")); err != nil {
		t.Fatalf("pre-install: %v", err)
	}
	if spawnEvent(t, w, client, &sim.NPCCreated{ActorID: "vstr-roger", At: at}) {
		t.Error("a messenger already carrying news started another call")
	}
}
