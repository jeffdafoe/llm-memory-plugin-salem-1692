package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// TestGateTools_RepairOnlyWithStallCue — the repair tool is advertised in EXACTLY
// the situation that renders the "## Your business" cue (payload.StallRepair
// non-nil: the owner stands at their own worn business), and nowhere else. Same
// discussion-109 "advertise a tool only with its triggering perception" invariant
// as craft/gather.
func TestGateTools_RepairOnlyWithStallCue(t *testing.T) {
	r := NewRegistry()
	if err := RegisterRepair(r); err != nil {
		t.Fatalf("RegisterRepair: %v", err)
	}

	// No StallRepair cue → repair is not advertised.
	none := specNameSet(gateTools(r, perception.Payload{ActorID: "ezekiel"}, nil))
	if none["repair"] != 0 {
		t.Errorf("repair advertised with no '## Your business' cue (count %d)", none["repair"])
	}

	// At the owner's own worn business (StallRepair present) → repair is advertised once.
	at := specNameSet(gateTools(r, perception.Payload{
		ActorID:     "ezekiel",
		StallRepair: &perception.StallRepairView{NailsNeeded: 5, NailsHeld: 2},
	}, nil))
	if at["repair"] != 1 {
		t.Errorf("repair not advertised at the owner's worn business (count %d)", at["repair"])
	}
}
