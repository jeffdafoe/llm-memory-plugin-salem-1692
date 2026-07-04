package perception

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_offpost_golden_test.go — LLM-268 fixtures for the off-post laboring
// surface. Three situations, each pinning the worker's rendered self-state cue,
// which is the same LaboringView predicate that re-grants her move_to at the tool
// surface (the move_to advertising itself is asserted in
// handlers/labor_gating_test.go, since gateTools lives there):
//
//   - off-post: the worker has wandered off the post and the employer still holds
//     it → the "head back" return cue + the return-to-post felt-impulse warrant
//     line that woke her.
//   - at-post, employer present: neither flag holds → the plain "stay with it"
//     line, unchanged (the LLM-230 regression guard).
//   - employer away: the employer has left the post → the "gone to X, follow if
//     they want you along" accompany cue.
//
// All three use an INTERIOR post (the Tavern), so "at post" is InsideStructureID
// membership and no VillageObject/Asset geometry is needed in the fixture; the
// doorless-stall loiter branch of sim.ActorAtWorkpost is covered by a focused unit
// test in the sim package.

// laborOffPostBase builds the shared John-Ellis-employs-Silence-Walker fixture:
// John (tavernkeeper) holds a Working labor contract over Silence (laborer) with
// ~90 minutes left. Callers place the two actors and add the away structure.
func laborOffPostBase() (john, silence *sim.ActorSnapshot, snap *sim.Snapshot, workerID sim.ActorID, published time.Time) {
	const (
		johnID    = sim.ActorID("john")
		silenceID = sim.ActorID("silence")
		tavern    = sim.StructureID("tavern")
	)
	published = time.Date(2026, 7, 4, 16, 0, 0, 0, time.UTC)
	workingUntil := published.Add(90 * time.Minute)
	acceptedAt := published.Add(-30 * time.Minute)
	john = &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		Coins:             50,
		Needs:             map[sim.NeedKey]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Silence Walker": {}},
	}
	silence = &sim.ActorSnapshot{
		Kind:          sim.KindNPCShared,
		DisplayName:   "Silence Walker",
		Role:          "laborer",
		State:         sim.StateLaboring,
		Coins:         0,
		Needs:         map[sim.NeedKey]int{},
		Acquaintances: map[string]sim.Acquaintance{"John Ellis": {}},
	}
	snap = &sim.Snapshot{
		PublishedAt:    published,
		NeedThresholds: sim.NeedThresholds{},
		Actors:         map[sim.ActorID]*sim.ActorSnapshot{johnID: john, silenceID: silence},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: plainStructure(tavern, "Tavern"),
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID:           1,
				WorkerID:     silenceID,
				EmployerID:   johnID,
				Reward:       2,
				DurationMin:  120,
				State:        sim.LaborStateWorking,
				AcceptedAt:   &acceptedAt,
				WorkingUntil: &workingUntil,
			},
		},
		ItemKinds: foodDrinkCatalog(),
	}
	return john, silence, snap, silenceID, published
}

// laboringWorkerOffPost — Silence has wandered off the Tavern (outdoors,
// InsideStructureID == "") while John still keeps it. The return-to-post backstop
// has stamped her felt impulse; the golden pins that line PLUS her self-state cue
// "head back to the Tavern with move_to" (LLM-268 symptom 1, the marooning fix).
func laboringWorkerOffPost() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	john, silence, snap, workerID, published := laborOffPostBase()
	_ = john                                // John stays at the post (InsideStructureID already the tavern).
	silence.InsideStructureID = ""          // wandered outdoors, off the post
	silence.Pos = sim.TilePos{X: 40, Y: 40} // somewhere out in the open
	warrants := []sim.WarrantMeta{{
		TriggerActorID: workerID,
		Reason:         sim.ReturnToPostWarrantReason{},
		OccurredAt:     published,
	}}
	return snap, workerID, warrants
}

// laboringWorkerAtPostEmployerPresent — the normal committed case: Silence is
// inside the Tavern with John present, green needs. Neither off-post flag holds,
// so the golden pins the unchanged "stay with it" self-state line and no
// directional cue. The LLM-230 regression guard: the off-post widening must not
// leak the return/accompany cue (or move_to) into the at-post case.
func laboringWorkerAtPostEmployerPresent() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	_, silence, snap, workerID, _ := laborOffPostBase()
	silence.InsideStructureID = "tavern" // at the post with the owner present
	return snap, workerID, nil
}

// laboringWorkerEmployerAway — Silence is at the Tavern but John has left it for
// the General Store mid-contract (the Hannah/Lewis accompany case, LLM-268 symptom
// 2). The golden pins the accompany cue naming where he went, so a "come with me"
// errand can be followed.
func laboringWorkerEmployerAway() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const generalStore = sim.StructureID("general_store")
	john, silence, snap, workerID, _ := laborOffPostBase()
	silence.InsideStructureID = "tavern"  // at the post
	john.InsideStructureID = generalStore // employer has walked off to the store
	snap.Structures[generalStore] = plainStructure(generalStore, "General Store")
	return snap, workerID, nil
}
