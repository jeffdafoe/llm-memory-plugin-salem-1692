package handlers

import (
	"encoding/json"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// LLM-20: let_in is advertised only while perception names someone at the
// door (payload.AtYourDoor — the slice the door cue reads), and its arguments
// decode strictly.
func TestGateTools_LetInOnlyWithSomeoneAtTheDoor(t *testing.T) {
	r := laborGatingRegistry(t)
	if err := RegisterLetIn(r); err != nil {
		t.Fatalf("RegisterLetIn: %v", err)
	}
	off := perception.Payload{ActorID: "silence", Surroundings: speakAudience()}
	if specNameSet(gateTools(r, off, nil))["let_in"] != 0 {
		t.Error("let_in advertised with nobody at the door")
	}
	on := perception.Payload{ActorID: "silence", Surroundings: speakAudience(), AtYourDoor: []sim.ActorID{"jefferey"}}
	if specNameSet(gateTools(r, on, nil))["let_in"] != 1 {
		t.Error("let_in not advertised with someone at the door")
	}
}

func TestDecodeLetInArgs(t *testing.T) {
	if got, err := DecodeLetInArgs(json.RawMessage(`{"name": "Jefferey"}`)); err != nil || got.(LetInArgs).Name != "Jefferey" {
		t.Errorf("good args: %v %v", got, err)
	}
	for _, bad := range []string{`{}`, `{"name": "  "}`, `{"name": "x", "extra": 1}`, `[]`, `{"name": "x"} {}`} {
		if _, err := DecodeLetInArgs(json.RawMessage(bad)); err == nil {
			t.Errorf("%s decoded without error", bad)
		}
	}
}
