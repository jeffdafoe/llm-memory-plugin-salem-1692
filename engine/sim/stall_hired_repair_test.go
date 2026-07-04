package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// stall_hired_repair_test.go — LLM-271. External (package sim_test) end-to-end test
// that a worker hired to labor at their employer's worn business can drive the real
// StartRepair command — spending their own nails and resetting the employer's stall
// wear at completion — not just the owner. Reuses the stall harness in
// stall_wear_test.go (buildStallTestWorld / placeAt / inventoryOf / forceComplete /
// stallWearOf). The stall "stall" is owned by ezekiel and worn to 450 (>= repair 400).

func TestStartRepair_HiredWorkerMendsEmployerStall(t *testing.T) {
	w, cancel := buildStallTestWorld(t)
	defer cancel()

	// Add a hired worker (Lewis) Working for ezekiel — the stall's owner — carrying
	// nails, then place him at the stall. This is the LLM-271 case: the owner-only
	// resolver would reject him ("no stall of yours"), the owner-or-hire resolver
	// admits him.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["lewis"] = &sim.Actor{
			ID: "lewis", DisplayName: "Lewis Walker", LLMAgent: "lewis", Kind: sim.KindNPCShared,
			Inventory: map[sim.ItemKind]int{"nail": 10},
		}
		until := time.Now().UTC().Add(time.Hour)
		world.LaborLedger[1] = &sim.LaborOffer{
			ID: 1, WorkerID: "lewis", EmployerID: "ezekiel",
			State: sim.LaborStateWorking, WorkingUntil: &until,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed hired worker: %v", err)
	}
	placeAt(t, w, "lewis", "stall")

	res, err := w.Send(sim.StartRepair("lewis"))
	if err != nil {
		t.Fatalf("StartRepair by hired worker: %v", err)
	}
	sr := res.(sim.SourceActivityStartResult)
	if !sr.Started || sr.Kind != sim.SourceActivityRepair || sr.ObjectID != "stall" {
		t.Fatalf("start result = %+v, want Started repair @ stall", sr)
	}
	// The hired worker's OWN nails are spent (10 - 5).
	if got := inventoryOf(t, w, "lewis", "nail"); got != 5 {
		t.Errorf("hired worker nails = %d, want 5 (5 consumed at start)", got)
	}
	// Wear resets at completion — the employer's stall is mended by the hired hand.
	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("completed = %d, want 1", n)
	}
	if got := stallWearOf(t, w, "stall"); got != 0 {
		t.Errorf("wear = %d, want 0 (reset by the hired worker's repair)", got)
	}
}
