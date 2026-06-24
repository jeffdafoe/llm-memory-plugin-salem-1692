package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// emptyAssetSet lets these fixtures' objects (no AssetID, explicit loiter offsets)
// resolve through ResolveLoiteringObject, which requires a known asset entry — the
// "" key matches their unset AssetID. LLM-93 made the gather cue asset-aware (it now
// shares sim.ResolveGatherSource with the command).
var emptyAssetSet = map[sim.AssetID]*sim.Asset{"": {}}

// gatherCueSnapshot builds a snapshot with one gatherable well at world (100,100)
// and the actor placed at actorTile. The well has zero loiter offset, so its
// pin is the well's anchor tile.
func gatherCueSnapshot(actorTile sim.TilePos) *sim.Snapshot {
	zero := 0
	return &sim.Snapshot{
		Assets: emptyAssetSet,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": {Pos: actorTile},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"well": {
				ID:            "well",
				DisplayName:   "Old Well",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{
					{Attribute: "thirst", Amount: -12, GatherItem: "water"},
				},
			},
			// A non-gatherable refresh object far away — out of attribution range,
			// so it neither cues nor (being distant) suppresses the well.
			"oak": {
				ID:          "oak",
				DisplayName: "Oak",
				Pos:         sim.WorldPos{X: 5000, Y: 5000},
				Refreshes:   []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -4}},
			},
		},
	}
}

func TestBuild_GatherableCue_AtSource(t *testing.T) {
	wellPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	p := Build(gatherCueSnapshot(wellPin), "hannah", nil)

	if p.Surroundings.GatherableItem != "water" {
		t.Errorf("GatherableItem=%q, want water", p.Surroundings.GatherableItem)
	}
	if p.Surroundings.GatherableSource != "Old Well" {
		t.Errorf("GatherableSource=%q, want Old Well", p.Surroundings.GatherableSource)
	}
}

func TestBuild_GatherableCue_TooFar_NoCue(t *testing.T) {
	wellPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	far := sim.TilePos{X: wellPin.X + 2, Y: wellPin.Y} // Chebyshev 2 > LoiterAttributionTiles (1)
	p := Build(gatherCueSnapshot(far), "hannah", nil)

	if p.Surroundings.GatherableItem != "" {
		t.Errorf("GatherableItem=%q, want empty (out of attribution range)", p.Surroundings.GatherableItem)
	}
}

// TestBuild_GatherableCue_CloserNonGatherableSuppresses — resolve-then-check:
// a non-gatherable refresh object NEARER than the well owns the tile, so no cue
// is offered (matches the sim.Gather Command, which would reject because that
// closer object is what it resolves).
func TestBuild_GatherableCue_CloserNonGatherableSuppresses(t *testing.T) {
	zero := 0
	wellPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	// Actor stands on the oak (cheb 0); the well is one tile away (cheb 1).
	snap := &sim.Snapshot{
		Assets: emptyAssetSet,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": {Pos: wellPin},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"oak": {
				ID: "oak", DisplayName: "Oak",
				Pos:           sim.WorldPos{X: 100, Y: 100}, // same tile as the actor
				LoiterOffsetX: &zero, LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -4}}, // not gatherable
			},
			"well": {
				ID: "well", DisplayName: "Old Well",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				LoiterOffsetX: intp(1), LoiterOffsetY: &zero, // pin one tile east → cheb 1
				Refreshes: []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -12, GatherItem: "water"}},
			},
		},
	}
	p := Build(snap, "hannah", nil)
	if p.Surroundings.GatherableItem != "" {
		t.Errorf("GatherableItem=%q, want empty (closer non-gatherable object owns the tile)", p.Surroundings.GatherableItem)
	}
}

// intp returns a pointer to v (loiter-offset fields are *int).
func intp(v int) *int { return &v }

// TestRenderSurroundings_GatherableLine — the cue renders a "gatherable" line;
// absent when no cue.
func TestRenderSurroundings_GatherableLine(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{GatherableItem: "water", GatherableSource: "Old Well"})
	out := b.String()
	if !strings.Contains(out, "You're at Old Well — you can gather water here.") {
		t.Errorf("render missing gatherable line:\n%s", out)
	}

	var b2 strings.Builder
	renderSurroundings(&b2, SurroundingsView{})
	if strings.Contains(b2.String(), "you can gather") {
		t.Errorf("render emitted a gatherable line with no cue:\n%s", b2.String())
	}
}

// TestBuild_GatherableCue_OwnedByOther_Suppressed — LLM-50 D2: the gatherable
// cue (and thus the gather tool advertisement, which reads the same
// SurroundingsView field) is suppressed for a non-owner at an owned source.
func TestBuild_GatherableCue_OwnedByOther_Suppressed(t *testing.T) {
	wellPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	snap := gatherCueSnapshot(wellPin)
	snap.VillageObjects["well"].OwnerActorID = "prudence" // owned by someone other than hannah
	p := Build(snap, "hannah", nil)

	if p.Surroundings.GatherableItem != "" {
		t.Errorf("GatherableItem=%q, want empty (owned by another actor)", p.Surroundings.GatherableItem)
	}
}

// TestBuild_GatherableCue_OwnedBySelf_Shows — the owner still sees their own
// source cued.
func TestBuild_GatherableCue_OwnedBySelf_Shows(t *testing.T) {
	wellPin := sim.WorldPos{X: 100, Y: 100}.Tile()
	snap := gatherCueSnapshot(wellPin)
	snap.VillageObjects["well"].OwnerActorID = "hannah" // the subject owns it
	p := Build(snap, "hannah", nil)

	if p.Surroundings.GatherableItem != "water" {
		t.Errorf("GatherableItem=%q, want water (owner sees own source)", p.Surroundings.GatherableItem)
	}
}

// TestBuild_GatherableCue_NearestOwnedSuppresses_NoFallthrough locks cue/command
// parity: when the NEAREST gatherable is owned-by-other, the cue is suppressed
// even though a farther UNOWNED gatherable is also in range. Falling through to
// the farther source would advertise a gather the command refuses — Gather
// resolves the nearer owned object and returns ErrNotYourSource. Pairs with the
// sim-side TestGather_NearestOwned_RejectsDespiteFartherCommons.
func TestBuild_GatherableCue_NearestOwnedSuppresses_NoFallthrough(t *testing.T) {
	zero := 0
	actorTile := sim.WorldPos{X: 100, Y: 100}.Tile()
	snap := &sim.Snapshot{
		Assets: emptyAssetSet,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"hannah": {Pos: actorTile}},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"owned": { // nearest (actor's tile, cheb 0), owned by another actor
				ID: "owned", DisplayName: "Prudence's Bush",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				LoiterOffsetX: &zero, LoiterOffsetY: &zero,
				OwnerActorID: "prudence",
				Refreshes:    []*sim.ObjectRefresh{{Attribute: "hunger", Amount: 0, GatherItem: "berries"}},
			},
			"commons": { // farther (cheb 1, still in range), unowned, also gatherable
				ID: "commons", DisplayName: "Wild Bush",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				LoiterOffsetX: intp(1), LoiterOffsetY: &zero, // pin one tile east → cheb 1
				Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: 0, GatherItem: "berries"}},
			},
		},
	}
	p := Build(snap, "hannah", nil)
	if p.Surroundings.GatherableItem != "" {
		t.Errorf("GatherableItem=%q, want empty (nearest is owned — must NOT fall through to the farther commons)", p.Surroundings.GatherableItem)
	}
}
