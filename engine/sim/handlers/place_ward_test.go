package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// LLM-897: place_ward is advertised only with the ward place choice
// (payload.WardPlace — the view the "## A ward for the house" cue renders from),
// its arguments decode strictly, and its result says the ward is up.
func TestGateTools_PlaceWardOnlyWithThePlaceChoice(t *testing.T) {
	r := laborGatingRegistry(t)
	if err := RegisterPlaceWard(r); err != nil {
		t.Fatalf("RegisterPlaceWard: %v", err)
	}
	off := perception.Payload{ActorID: "elizabeth"}
	if specNameSet(gateTools(r, off, nil))[placeWardToolName] != 0 {
		t.Error("place_ward advertised with no place choice")
	}
	off.WardWant = &perception.WardWantView{Item: sim.HorseshoeKind}
	if specNameSet(gateTools(r, off, nil))[placeWardToolName] != 0 {
		t.Error("place_ward advertised with only the buy errand")
	}
	on := perception.Payload{ActorID: "elizabeth", WardPlace: &perception.WardPlaceView{Items: []sim.ItemKind{sim.HorseshoeKind}}}
	if specNameSet(gateTools(r, on, nil))[placeWardToolName] != 1 {
		t.Error("place_ward not advertised with the place choice")
	}
}

func TestDecodePlaceWardArgs(t *testing.T) {
	if got, err := DecodePlaceWardArgs(json.RawMessage(`{"item": " horseshoe "}`)); err != nil || got.(PlaceWardArgs).Item != "horseshoe" {
		t.Errorf("good args: %v %v", got, err)
	}
	for _, bad := range []string{`{}`, `{"item": ""}`, `{"item": "witch_bottle"}`, `{"item": "bread"}`,
		`{"item": "horseshoe", "extra": 1}`, `[]`, `{"item": "horseshoe"} {}`} {
		if _, err := DecodePlaceWardArgs(json.RawMessage(bad)); err == nil {
			t.Errorf("%s decoded without error", bad)
		}
	}
}

func TestPlaceWardSurfaceNeverSaysWitch(t *testing.T) {
	_, err := DecodePlaceWardArgs(json.RawMessage(`{"item": "witch_bottle"}`))
	if err == nil {
		t.Fatal("witch_bottle decoded without error")
	}
	for _, s := range []string{placeWardDescription, string(placeWardSchema), err.Error()} {
		if strings.Contains(strings.ToLower(s), "witch") {
			t.Errorf("place_ward surface names witchcraft: %q", s)
		}
	}
}

func TestCommitResultContent_PlaceWardSaysItIsUp(t *testing.T) {
	got := commitResultContent(&ValidatedCall{Name: placeWardToolName},
		sim.HomeWardPlacedResult{Item: sim.HorseshoeKind, Text: "You nail the horseshoe over the door, points up."})
	if got != "[ok] You nail the horseshoe over the door, points up." {
		t.Errorf("result = %q", got)
	}
}
