package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// arrival_encounter_stall_shadow_test.go — LLM-375. Companion to
// handlers/stall_loiter_pay_shadow_test.go. It pins the FIX at the source: the
// arrival-encounter cascade must NOT grab customers waiting at an OPEN structure's
// loiter pin into an open-ground peer huddle, because that huddle shadows the
// structure (it excludes the inside keeper) and then nobody in it can pay, quote,
// or be greeted. An open-stall walk-up is a knock without the ceremony, and the
// cascade already skips knocks; outdoorEncounterExcludesActor now also skips
// open-structure loiterers (sim.InOpenLoiterStructureScope).
//
// The field-encounter path (two actors meeting on open ground, no structure) is
// unchanged — covered by TestArrivalEncounter_NearbyOutdoorActor — and the
// SHUT-structure path is asserted below so the fix can't silently over-suppress.

// buildStallEncounterWorld seeds a Blacksmith with a resolvable loiter pin (named
// vobj at the anchor tile, zero loiter offsets), a keeper working INSIDE it in the
// given state, and a buyer already loitering OUTSIDE at the pin — then wires the
// arrival-encounter subscriber. A second buyer's arrival at the pin is what the
// tests drive. keeperState selects an OPEN stall (StateIdle) vs a SHUT one
// (StateSleeping ⇒ keeperPresentAt false).
func buildStallEncounterWorld(t *testing.T, keeperState sim.ActorState) (*sim.World, sim.TilePos, func()) {
	t.Helper()
	repo, h := mem.NewRepository()
	h.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"smithy": {ID: "smithy", DisplayName: "Blacksmith"},
	})
	h.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bldg-asset": {ID: "bldg-asset", Category: "structure"},
	})
	z := 0
	h.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"smithy": {
			ID: "smithy", AssetID: "bldg-asset", DisplayName: "Blacksmith",
			Pos:           sim.WorldPos{X: 160, Y: 160},
			LoiterOffsetX: &z, LoiterOffsetY: &z,
		},
	})
	pin := sim.WorldPos{X: 160, Y: 160}.Tile()
	h.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"keeper": {
			ID: "keeper", DisplayName: "Ezekiel", Kind: sim.KindNPCStateful,
			State:             keeperState,
			InsideStructureID: "smithy",
			WorkStructureID:   "smithy",
		},
		"buyer1": {
			ID: "buyer1", DisplayName: "Prudence", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, Pos: pin, // already loitering at the pin
		},
		"buyer2": {
			ID: "buyer2", DisplayName: "Josiah", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, Pos: pin, // the arriver, walking up to the same pin
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cascade.RegisterEncounter(world)
		return nil, nil
	}}); err != nil {
		cancel()
		<-done
		t.Fatalf("RegisterEncounter: %v", err)
	}
	return w, pin, func() { cancel(); <-done }
}

// TestArrivalEncounter_OpenStallArrivalDoesNotShadow is the fix: buyer2 walks up
// to the OPEN Blacksmith's pin where buyer1 already loiters. The arrival must form
// NO open-ground peer huddle — both are in the structure's (open) loiter scope, so
// the encounter skips them. The keeper stays reachable: buyer2's own next act
// (here EnsureColocatedHuddle, exactly what pay/speak run) forms the structure
// huddle WITH the inside keeper.
func TestArrivalEncounter_OpenStallArrivalDoesNotShadow(t *testing.T) {
	w, _, stop := buildStallEncounterWorld(t, sim.StateIdle)
	defer stop()

	emitArrivalFor(t, w, "buyer2", time.Now().UTC())

	// No shadowing peer huddle: the arrival grabbed no one.
	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Fatalf("open-stall arrival minted %d huddle(s); should mint none (no open-ground shadow)", st.activeHuddleCount)
	}
	for _, id := range []sim.ActorID{"buyer1", "buyer2", "keeper"} {
		if h := st.memberToHuddleIDs[id]; h != "" {
			t.Errorf("%s should not be huddled after the open-stall arrival, got %q", id, h)
		}
	}

	// The keeper is still reachable: the arriver's own action forms/joins the
	// structure huddle across the threshold and pulls the keeper in.
	if _, err := w.Send(sim.EnsureColocatedHuddle("buyer2", time.Now().UTC())); err != nil {
		t.Fatalf("EnsureColocatedHuddle(buyer2): %v", err)
	}
	after := readEncounterHuddleState(t, w)
	b2, kh := after.memberToHuddleIDs["buyer2"], after.memberToHuddleIDs["keeper"]
	if b2 == "" || b2 != kh {
		t.Errorf("arriver could not reach the inside keeper: buyer2=%q keeper=%q", b2, kh)
	}
}

// TestArrivalEncounter_ShutStallLoiterersStillMeetOutdoors is the composition
// guard with LLM-359: when the structure is SHUT (keeper abed ⇒ keeperPresentAt
// false ⇒ conversationalScopeStructure resolves to ""), the two loiterers are NOT
// in a stall scope, so the fix must NOT suppress their ordinary open-ground
// encounter. buyer2 arriving forms the outdoor peer huddle with buyer1, and the
// sleeping keeper (inside, unreachable across the shut wall) stays out of it.
func TestArrivalEncounter_ShutStallLoiterersStillMeetOutdoors(t *testing.T) {
	w, _, stop := buildStallEncounterWorld(t, sim.StateSleeping)
	defer stop()

	emitArrivalFor(t, w, "buyer2", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 1 {
		t.Fatalf("shut-stall arrival should form the ordinary outdoor huddle; got %d huddle(s)", st.activeHuddleCount)
	}
	b1, b2 := st.memberToHuddleIDs["buyer1"], st.memberToHuddleIDs["buyer2"]
	if b1 == "" || b1 != b2 {
		t.Errorf("loiterers at a SHUT stall should meet on open ground, got buyer1=%q buyer2=%q", b1, b2)
	}
	if kh := st.memberToHuddleIDs["keeper"]; kh != "" {
		t.Errorf("the abed keeper behind a shut wall should not join the outdoor huddle, got %q", kh)
	}
}
