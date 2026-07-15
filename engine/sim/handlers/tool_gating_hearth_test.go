package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// tool_gating_hearth_test.go — LLM-412. The stoke tool's advertising gate:
// present in EXACTLY the situation that renders the hearth cue
// (payload.Hearth non-nil), and it survives the laboring speak-only strip —
// tending the fire is work, not leaving (the work-vs-leaving principle).

// TestGateTools_StokeOnlyWithHearthCue — stoke rides the same signal the
// "## Your hearth" cue renders from, and nowhere else (discussion-109).
func TestGateTools_StokeOnlyWithHearthCue(t *testing.T) {
	r := NewRegistry()
	if err := RegisterStoke(r); err != nil {
		t.Fatalf("RegisterStoke: %v", err)
	}

	// No hearth cue → stoke is not advertised.
	none := specNameSet(gateTools(r, perception.Payload{ActorID: "hannah"}, nil))
	if none["stoke"] != 0 {
		t.Errorf("stoke advertised with no hearth cue (count %d)", none["stoke"])
	}

	// Inside the low-fired hearth (Hearth present) → stoke is advertised once,
	// even short of wood (the cue steers buy-then-stoke; StartStoke errors
	// helpfully — the repair-tool posture).
	at := specNameSet(gateTools(r, perception.Payload{
		ActorID: "hannah",
		Hearth:  &perception.HearthView{Out: true, WoodNeeded: 1, WoodHeld: 0},
	}, nil))
	if at["stoke"] != 1 {
		t.Errorf("stoke not advertised with the hearth cue (count %d)", at["stoke"])
	}
}

// TestGateTools_StokeAdvertisedToLaboringHiredWorker — a hired worker mid-job
// (payload.Laboring set) at an employer whose hearth wants wood (Hearth set,
// Hired) is STILL advertised the stoke tool. stoke is deliberately not in
// laborAbandonTools: stoking the employer's fire IS the job — stripping it
// would leave the "## The hearth where you're working" cue with no tool
// behind it and waste the hired hearth wake (the LLM-271 lesson).
func TestGateTools_StokeAdvertisedToLaboringHiredWorker(t *testing.T) {
	r := NewRegistry()
	if err := RegisterStoke(r); err != nil {
		t.Fatalf("RegisterStoke: %v", err)
	}
	got := specNameSet(gateTools(r, perception.Payload{
		ActorID:  "anne",
		Laboring: &perception.LaboringView{},
		Hearth:   &perception.HearthView{Hired: true, WoodNeeded: 1, WoodHeld: 1, HasEnoughWood: true},
	}, nil))
	if got["stoke"] != 1 {
		t.Errorf("stoke not advertised to a laboring hired worker at the employer's low hearth (count %d)", got["stoke"])
	}
}
