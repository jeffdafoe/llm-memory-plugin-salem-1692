package httpapi

import (
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seedActorInStructure adds an actor parked inside a structure (InsideStructureID
// set) at (x,y). Used to recreate the live ZBBS-HOME-358 scene: a PC and NPCs
// co-located inside the Tavern with no huddle between them.
func seedActorInStructure(t *testing.T, w *sim.World, a *sim.Actor) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[a.ID] = a
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedActorInStructure %q: %v", a.ID, err)
	}
}

// TestHandlePCSpeak_CoLocatedNoHuddle reproduces the live ZBBS-HOME-358 bug: a
// PC standing inside a structure with NPCs, but with no huddle, cannot address
// them by name. sim.Speak builds its audience from CurrentHuddleID only, and a
// plain walk-in (StructureEnter) never forms a huddle — only a knock or an
// outdoor encounter does. So the co-located PC has CurrentHuddleID == "", the
// vocative-absentee gate sees the named NPC as a non-peer (empty peer set), and
// the speak is rejected 422 — the player-visible "I can't talk to them".
//
// After the fix, the speak path first forms a REAL co-located structure huddle
// (EnsureColocatedHuddle, before sim.Speak), so the named NPC is a huddle peer
// and the address succeeds (200) — and the two end up genuinely co-huddled,
// which is what the transaction/reaction paths also need.
func TestHandlePCSpeak_CoLocatedNoHuddle(t *testing.T) {
	w := seededWorld(t)
	// The Tavern as a real structure + placement — JoinHuddle requires the
	// structure to exist (it is the indoor huddle anchor). Mirrors the live
	// world where the Tavern is a structure-backed village_object.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Structures["tavern"] = &sim.Structure{ID: "tavern", DisplayName: "The Tavern"}
		world.VillageObjects["tavern"] = &sim.VillageObject{
			ID: "tavern", AssetID: "tavern-asset", DisplayName: "The Tavern",
			Pos: sim.WorldPos{X: 100, Y: 100},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed tavern: %v", err)
	}
	// okAuth resolves the test token to username "tester"; bind the PC to it.
	seedActorInStructure(t, w, &sim.Actor{
		ID: "pc-tester", DisplayName: "Tester", Kind: sim.KindPC,
		State: sim.StateIdle, Pos: sim.TilePos{X: 3, Y: 4},
		LoginUsername: "tester", InsideStructureID: "tavern",
	})
	seedActorInStructure(t, w, &sim.Actor{
		ID: "npc-john", DisplayName: "John Ellis", Kind: sim.KindNPCStateful,
		State: sim.StateIdle, Pos: sim.TilePos{X: 3, Y: 4},
		InsideStructureID: "tavern",
	})
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/speak", `{"text":"John, good evening."}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("addressing a co-located NPC by name: want 200, got %d (body %s)",
			rec.Code, rec.Body.String())
	}

	// The substance of the fix: a REAL huddle formed, PC and NPC co-huddled —
	// not merely a 200 from some other path (code_review #5).
	var pcH, npcH sim.HuddleID
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		pcH = world.Actors["pc-tester"].CurrentHuddleID
		npcH = world.Actors["npc-john"].CurrentHuddleID
		return nil, nil
	}}); err != nil {
		t.Fatalf("read huddles: %v", err)
	}
	if pcH == "" || pcH != npcH {
		t.Fatalf("not co-huddled after speak: pc=%q npc=%q", pcH, npcH)
	}
}
