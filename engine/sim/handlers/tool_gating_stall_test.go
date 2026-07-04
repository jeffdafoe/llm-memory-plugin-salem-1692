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

// TestGateTools_RepairAdvertisedToLaboringHiredWorker (LLM-271) — a hired worker
// mid-job (payload.Laboring set) at their employer's worn business (StallRepair set,
// Hired) is STILL advertised the repair tool. repair is not in laborAbandonTools, so
// the laboring speak-only strip must not remove it — otherwise the surfaced "## The
// business you're working at" cue would have no tool behind it, and the wake would
// be wasted. The buildStallRepair cue is what sets StallRepair (hired or owner); the
// gate keys only on its presence, so this pins that the laboring gate leaves it be.
func TestGateTools_RepairAdvertisedToLaboringHiredWorker(t *testing.T) {
	r := NewRegistry()
	if err := RegisterRepair(r); err != nil {
		t.Fatalf("RegisterRepair: %v", err)
	}
	got := specNameSet(gateTools(r, perception.Payload{
		ActorID:     "lewis",
		Laboring:    &perception.LaboringView{},
		StallRepair: &perception.StallRepairView{Hired: true, NailsNeeded: 5, NailsHeld: 5},
	}, nil))
	if got["repair"] != 1 {
		t.Errorf("repair not advertised to a laboring hired worker at the employer's worn business (count %d)", got["repair"])
	}
}
