package sim_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// speak_mentions_test.go — ZBBS-WORK-400: the structured sale hints riding
// a Spoke. Covers filterSpeakMentions' sellability posture (catalog
// resolution incl. display labels, stock gate, service exemption, dedup,
// price clamp) through the public SpeakTo surface, plus the nil paths.

func seedMentionCatalog(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["stew"] = &sim.ItemKindDef{Name: "stew", DisplayLabel: "a bowl of stew"}
		world.ItemKinds["ale"] = &sim.ItemKindDef{Name: "ale", DisplayLabel: "ale"}
		world.ItemKinds["bread"] = &sim.ItemKindDef{Name: "bread", DisplayLabel: "bread"}
		world.ItemKinds["nights_stay"] = &sim.ItemKindDef{
			Name: "nights_stay", DisplayLabel: "a night's stay",
			Capabilities: []string{"service", "lodging"},
		}
		john := world.Actors["john"]
		john.Inventory = map[sim.ItemKind]int{"stew": 2, "bread": 1} // ale deliberately zero-stock
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
}

func TestSpeakTo_MentionsFilteredToSellable(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "john", displayName: "John", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "pc", displayName: "Jeff", kind: sim.KindPC, huddleID: "h1"},
	)
	defer stop()
	seedMentionCatalog(t, w)

	captured := captureSpoke(t, w)
	mentions := []sim.SpeakMention{
		{Item: "Stew", Price: 3},           // case-insensitive kind match → kept with price
		{Item: "a night's stay", Price: 4}, // display-label resolution; service kind passes with zero stock
		{Item: "ale", Price: 2},            // catalog-known, zero stock, non-service → dropped
		{Item: "dragon scales", Price: 9},  // not in catalog → dropped
		{Item: "stew", Price: 9},           // duplicate kind → first occurrence wins
		{Item: "bread", Price: -1},         // negative price clamps to 0 ("no price named")
	}
	if _, err := w.Send(sim.SpeakTo(
		"john", "Stew and bread tonight, and a room if you need one.", "",
		mentions, true, time.Now().UTC(),
	)); err != nil {
		t.Fatalf("SpeakTo: %v", err)
	}

	if len(*captured) != 1 {
		t.Fatalf("captured %d Spoke events, want 1", len(*captured))
	}
	got := (*captured)[0].Mentions
	want := []sim.SpeakMention{
		{Item: "stew", Price: 3},
		{Item: "nights_stay", Price: 4},
		{Item: "bread", Price: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Mentions = %+v, want %+v", got, want)
	}
}

func TestSpeakTo_NoMentionsStaysNil(t *testing.T) {
	w, stop := buildSpeakTestWorld(t,
		actorSpec{id: "john", displayName: "John", kind: sim.KindNPCShared, huddleID: "h1"},
		actorSpec{id: "pc", displayName: "Jeff", kind: sim.KindPC, huddleID: "h1"},
	)
	defer stop()
	seedMentionCatalog(t, w)

	captured := captureSpoke(t, w)
	// The to-less Speak wrapper (the PC path) carries no mentions.
	if _, err := w.Send(sim.Speak("pc", "What do you have?", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	// All-filtered mentions normalize to nil, not an empty slice.
	if _, err := w.Send(sim.SpeakTo(
		"john", "Finest dragon scales in Salem.", "",
		[]sim.SpeakMention{{Item: "dragon scales", Price: 9}}, true, time.Now().UTC(),
	)); err != nil {
		t.Fatalf("SpeakTo: %v", err)
	}

	if len(*captured) != 2 {
		t.Fatalf("captured %d Spoke events, want 2", len(*captured))
	}
	for i, s := range *captured {
		if s.Mentions != nil {
			t.Errorf("event %d Mentions = %+v, want nil", i, s.Mentions)
		}
	}
}
