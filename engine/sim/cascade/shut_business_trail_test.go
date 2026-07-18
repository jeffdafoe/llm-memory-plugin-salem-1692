package cascade

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// shut_business_trail_test.go — LLM-463 regression guard for the shut-business
// SELF-TRAIL, end to end across the seam that broke: a real walk → ActorArrived →
// the walked action-log row (this package) → perception's "## What you've recently
// done" line.
//
// The live failure: NPCs re-walked to the shut Tavern indefinitely because their
// own trail read "You arrived at the Tavern" with no hint of what they found. The
// capture half (sim/closed_business.go) was recording ObservedClosed correctly the
// whole time; the trail's FoundShut flag keyed off the walked row's StructureID —
// the actor's CONVERSATIONAL SCOPE at action time — and conversationalScopeStructure
// deliberately returns "" for an actor loitering outside a SHUT structure
// (loiterScopeConversable, LLM-359). So the one field that identified the business
// went empty exactly for the trips the flag exists to name.
//
// It went unnoticed for months because the Tavern's loiter pin used to sit INSIDE
// its footprint, so arrivals parked on the walkable door tile and read as
// InsideStructureID == tavern — conversationalScopeStructure's first branch, which
// has no open/shut gate. Adding a per-instance loiter_offset that moved the pin one
// row below the footprint (a live data change, no code, no deploy) moved arrivals
// genuinely outdoors and silently killed the trail.
//
// Hence the geometry below is the LIVE Tavern's, not a convenient synthetic one:
// the bug is only reachable when the loiter pin lands OUTSIDE the footprint, so a
// test built on a 1x1 footprint-less asset would pass against the broken code.

// Live Tavern geometry (village_object 019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9 and
// its asset), reproduced exactly — a future loiter-pin move that re-breaks the
// trail must fail here rather than in the village.
const (
	tavernAnchorX = 106 // padded-grid tile of the live placement
	tavernAnchorY = 131
	// Per-instance loiter_offset applied live 2026-07-17 → pin (103,132), one row
	// BELOW the footprint. This is the change that exposed the bug.
	tavernLoiterOffsetX = -3
	tavernLoiterOffsetY = 1
	// door_offset (-2,-1) → door tile (104,130), inside the footprint.
	tavernDoorOffsetX = -2
	tavernDoorOffsetY = -1
	// footprint 3/4/6/0 → x in [103,110], y in [125,131].
	tavernFootprintLeft   = 3
	tavernFootprintRight  = 4
	tavernFootprintTop    = 6
	tavernFootprintBottom = 0
)

// worldPosForTile converts a padded-grid tile back to the WorldPos a placement
// carries, so the seeded object lands on exactly the tile named above.
func worldPosForTile(tx, ty int) sim.WorldPos {
	return sim.WorldPos{X: float64(tx-sim.PadX) * sim.TileSize, Y: float64(ty-sim.PadY) * sim.TileSize}
}

// buildShutTavernTrailWorld stands up a running world holding the live Tavern
// placement, its sole keeper asleep inside (so the business reads shut — LLM-126),
// and a stateful NPC parked on open ground with somewhere else to work.
func buildShutTavernTrailWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(allGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"tavern-asset": {
			ID: "tavern-asset", Category: "structure",
			DoorOffsetX:     intp(tavernDoorOffsetX),
			DoorOffsetY:     intp(tavernDoorOffsetY),
			FootprintLeft:   tavernFootprintLeft,
			FootprintRight:  tavernFootprintRight,
			FootprintTop:    tavernFootprintTop,
			FootprintBottom: tavernFootprintBottom,
		},
		"farm-asset": {ID: "farm-asset", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(0)},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {
			ID: "tavern", AssetID: "tavern-asset", DisplayName: "Tavern",
			Pos:           worldPosForTile(tavernAnchorX, tavernAnchorY),
			LoiterOffsetX: intp(tavernLoiterOffsetX), LoiterOffsetY: intp(tavernLoiterOffsetY),
			EntryPolicy: sim.EntryPolicyOpen,
		},
		// The walker's own workplace, far enough away that it never contests the
		// Tavern's loiter attribution.
		"farm": {ID: "farm", AssetID: "farm-asset", DisplayName: "James Farm", Pos: worldPosForTile(140, 160)},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
		"farm":   {ID: "farm", DisplayName: "James Farm"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"moses": {
			ID: "moses", DisplayName: "Moses James", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, WorkStructureID: "farm",
			Pos:           sim.TilePos{X: tavernAnchorX - 8, Y: tavernAnchorY + 8},
			RecentActions: sim.NewRingBuffer[sim.Action](8),
		},
		// Sole keeper, abed inside — an asleep keeper is not tending (LLM-126), so
		// the Tavern reads shut.
		"john": {
			ID: "john", DisplayName: "John Ellis", Kind: sim.KindNPCStateful,
			WorkStructureID: "tavern", HomeStructureID: "tavern",
			InsideStructureID: "tavern", State: sim.StateSleeping,
			Pos:           sim.TilePos{X: tavernAnchorX - 3, Y: tavernAnchorY - 1},
			RecentActions: sim.NewRingBuffer[sim.Action](8),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	sim.RegisterClosedBusinessSubscriber(w) // capture half: stamps ObservedClosed
	RegisterActionLog(ctx, w)               // surface half: the walked row the trail reads
	go w.Run(ctx)
	return w, cancel
}

// TestShutBusinessTrail_OutdoorArrivalAtShutTavernReadsAsDeadEnd is the LLM-463
// guard. Moses walks to the Tavern, whose keeper is abed. He comes to rest at a
// visitor slot genuinely OUTSIDE the building — the geometry the live loiter_offset
// produces — and his own trail must say he found it shut, not merely that he
// arrived. Without that the trip is invisible to him and he walks it again.
func TestShutBusinessTrail_OutdoorArrivalAtShutTavernReadsAsDeadEnd(t *testing.T) {
	w, cancel := buildShutTavernTrailWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveActor("moses", sim.NewStructureVisitDestination("tavern"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	for i := 0; i < 60; i++ {
		if _, err := w.Send(sim.EvaluateLocomotion(now)); err != nil {
			t.Fatalf("EvaluateLocomotion: %v", err)
		}
		moving, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			return world.Actors["moses"].MoveIntent != nil, nil
		}})
		if err != nil {
			t.Fatalf("read MoveIntent: %v", err)
		}
		if !moving.(bool) {
			break
		}
	}

	// Preconditions — assert the geometry the bug depends on, so this test can
	// never silently degrade into the easy (indoors) case that always worked.
	type arrivalState struct {
		pos    sim.TilePos
		inside sim.StructureID
		moving bool
		walked sim.ActionLogEntry
		found  bool
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["moses"]
		st := arrivalState{pos: a.Pos, inside: a.InsideStructureID, moving: a.MoveIntent != nil}
		for i := len(world.ActionLog) - 1; i >= 0; i-- {
			if e := world.ActionLog[i]; e.ActorID == "moses" && e.ActionType == sim.ActionTypeWalked {
				st.walked, st.found = e, true
				break
			}
		}
		return st, nil
	}})
	if err != nil {
		t.Fatalf("read arrival state: %v", err)
	}
	got := res.(arrivalState)
	if got.moving {
		t.Fatal("moses never finished his walk to the Tavern")
	}
	if got.inside != "" {
		t.Fatalf("precondition failed: moses ended INSIDE %q — the live loiter pin parks visitors outdoors, "+
			"and the indoors case was never broken", got.inside)
	}
	pin := sim.TilePos{X: tavernAnchorX + tavernLoiterOffsetX, Y: tavernAnchorY + tavernLoiterOffsetY}
	if d := pin.Chebyshev(got.pos); d > sim.LoiterAttributionTiles {
		t.Fatalf("precondition failed: moses rested at %v, Chebyshev %d from the Tavern pin %v — not an arrival at the pin", got.pos, d, pin)
	}
	if !got.found {
		t.Fatal("no walked action-log row for moses — the arrival never reached the action-log cascade")
	}
	// The precondition that makes LLM-463 reachable: the row's conversational-scope
	// stamp is EMPTY, because loiterScopeConversable refuses the outdoor scope of a
	// shut structure. A non-empty value here means the scenario has drifted back to
	// the case that always worked, and the test below would prove nothing.
	if got.walked.StructureID != "" {
		t.Fatalf("precondition failed: walked row stamped StructureID %q; LLM-463 exists BECAUSE a shut business "+
			"blanks that scope, so this test no longer covers the real case", got.walked.StructureID)
	}
	if got.walked.DestStructureID != "tavern" {
		t.Fatalf("walked row lost its destination: DestStructureID = %q, want \"tavern\" — the trail has nothing "+
			"left to identify the business by", got.walked.DestStructureID)
	}

	// The capture half must have fired — if it hasn't, the trail failure below is a
	// different bug and this message says so rather than blaming the surface.
	//
	// The no-op Send is a publish barrier, not a sleep: Run republishes after every
	// command and holds the Send reply until it has, so a returned Send guarantees
	// Published() reflects the arrival. Same barrier the LLM-270 realwalk test uses.
	if _, err := w.Send(sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("publish barrier: %v", err)
	}
	snap := w.Published()
	subject := snap.Actors["moses"]
	if subject == nil {
		t.Fatal("moses missing from published snapshot")
	}
	key := sim.ObservedStateKey{StructureID: "tavern", Condition: sim.ObservedClosed}
	if !subject.Observed.Active(key, snap.PublishedAt) {
		t.Fatal("capture half regressed: no active ObservedClosed(tavern) after arriving at the keeperless Tavern")
	}

	// The trail line itself — what the model actually reads.
	rendered := perception.Render(
		perception.Build(snap, "moses", nil),
		perception.RenderConfig{},
	)
	// Both halves — the trail's section can sit in either, and which one is a
	// history/ephemeral split decision this test has no stake in.
	prompt := rendered.Text + "\n" + rendered.EphemeralText
	const want = "You went to the Tavern but found it shut, no one tending it"
	if !strings.Contains(prompt, want) {
		t.Errorf("moses' self-trail does not name the dead end (LLM-463).\nwant a line containing: %q\ngot prompt:\n%s",
			want, prompt)
	}
	if strings.Contains(prompt, "You arrived at the Tavern") {
		t.Errorf("the shut trip still renders as a neutral arrival — the churn stays invisible to the model:\n%s",
			prompt)
	}
}
