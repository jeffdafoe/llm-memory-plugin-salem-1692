package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestSceneBound_StructureBound_Contains covers the structure-bound
// containment rule: an actor is contained iff their InsideStructureID
// matches the bound's structure ID. An actor outdoors (empty
// InsideStructureID) or inside a different structure is rejected.
func TestSceneBound_StructureBound_Contains(t *testing.T) {
	bound := sim.NewStructureBound("tavern")

	cases := []struct {
		name  string
		actor *sim.Actor
		want  bool
	}{
		{"inside target", &sim.Actor{InsideStructureID: "tavern"}, true},
		{"outdoors", &sim.Actor{InsideStructureID: ""}, false},
		{"inside other", &sim.Actor{InsideStructureID: "blacksmith"}, false},
		{"nil actor", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bound.Contains(nil, tc.actor)
			if got != tc.want {
				t.Errorf("Contains(%+v) = %v, want %v", tc.actor, got, tc.want)
			}
		})
	}
}

// TestSceneBound_AreaBound_Contains covers the area-bound containment
// rule: actor must be outdoors AND within Chebyshev radius of the
// anchor. An indoor actor at a tile within radius is rejected (the
// "actor inside nearby building" case).
func TestSceneBound_AreaBound_Contains(t *testing.T) {
	anchor := sim.Position{X: 10, Y: 10}
	bound := sim.NewAreaBound(anchor, 3)

	cases := []struct {
		name  string
		actor *sim.Actor
		want  bool
	}{
		{"outdoor at anchor", &sim.Actor{Pos: sim.TilePos{X: 10, Y: 10}}, true},
		{"outdoor within radius (king's move)", &sim.Actor{Pos: sim.TilePos{X: 12, Y: 13}}, true},
		{"outdoor at exact radius", &sim.Actor{Pos: sim.TilePos{X: 13, Y: 10}}, true},
		{"outdoor just past radius", &sim.Actor{Pos: sim.TilePos{X: 14, Y: 10}}, false},
		{"outdoor diagonal past radius", &sim.Actor{Pos: sim.TilePos{X: 14, Y: 14}}, false},
		{"indoor actor within radius", &sim.Actor{InsideStructureID: "tavern", Pos: sim.TilePos{X: 10, Y: 10}}, false},
		{"nil actor", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bound.Contains(nil, tc.actor)
			if got != tc.want {
				t.Errorf("Contains(%+v) = %v, want %v", tc.actor, got, tc.want)
			}
		})
	}
}

// TestSceneBound_UnboundedBound_Contains covers the unbounded
// containment rule: every actor is contained, regardless of position
// or structure. Used for chronicler atmosphere refresh and admin-
// triggered scenes that observe village-wide activity.
func TestSceneBound_UnboundedBound_Contains(t *testing.T) {
	bound := sim.NewUnboundedBound()

	cases := []struct {
		name  string
		actor *sim.Actor
		want  bool
	}{
		{"indoor actor", &sim.Actor{InsideStructureID: "tavern"}, true},
		{"outdoor actor far away", &sim.Actor{Pos: sim.TilePos{X: 999, Y: 999}}, true},
		{"zero-state actor", &sim.Actor{}, true},
		{"nil actor", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bound.Contains(nil, tc.actor)
			if got != tc.want {
				t.Errorf("Contains(%+v) = %v, want %v", tc.actor, got, tc.want)
			}
		})
	}
}

// TestSceneBound_AreaBound_RadiusClamping covers the radius validation:
// negative radius clamps to 0 (a 1-tile bound on the anchor itself).
// This matches the "0 means no margin" semantic of Chebyshev distance.
func TestSceneBound_AreaBound_RadiusClamping(t *testing.T) {
	anchor := sim.Position{X: 5, Y: 5}
	bound := sim.NewAreaBound(anchor, -7)

	atAnchor := &sim.Actor{Pos: sim.TilePos{X: 5, Y: 5}}
	oneAway := &sim.Actor{Pos: sim.TilePos{X: 6, Y: 5}}

	if !bound.Contains(nil, atAnchor) {
		t.Error("radius-clamped-to-0 bound should contain actor exactly at anchor")
	}
	if bound.Contains(nil, oneAway) {
		t.Error("radius-clamped-to-0 bound should not contain actor 1 tile away")
	}
}

// TestSceneBound_StructureBound_RoundTripPreserves covers the
// requirement that SceneBound deep-clones across the Scene round-trip.
// CloneScene must produce a Bound whose pointer fields don't alias the
// original — a downstream mutation of the cloned scene's Bound must not
// leak back into the source.
func TestSceneBound_StructureBound_RoundTripPreserves(t *testing.T) {
	now := time.Now().UTC()
	orig := &sim.Scene{
		ID:         "sc1",
		OriginAt:   now,
		OriginKind: "pc_speak",
		Bound:      sim.NewStructureBound("tavern"),
		Huddles:    map[sim.HuddleID]struct{}{},
	}

	cp := sim.CloneScene(orig)
	if cp == nil {
		t.Fatal("CloneScene returned nil")
	}
	if cp.Bound.Kind != sim.SceneBoundStructure {
		t.Fatalf("clone Bound.Kind = %q, want %q", cp.Bound.Kind, sim.SceneBoundStructure)
	}
	if cp.OriginStructureID() != "tavern" {
		t.Errorf("clone OriginStructureID = %q, want tavern", cp.OriginStructureID())
	}

	// Mutate clone's bound — original must not observe it. The pointer
	// field is the alias risk; if cloneSceneBound failed to deep-copy,
	// reassigning *cp.Bound.StructureID would flow back to orig.
	newID := sim.StructureID("blacksmith")
	cp.Bound.StructureID = &newID

	if orig.OriginStructureID() != "tavern" {
		t.Errorf("original OriginStructureID drifted after clone mutation: got %q", orig.OriginStructureID())
	}
}

// TestSceneBound_AreaBound_RoundTripPreserves covers area-bound deep-
// clone: both Anchor and Radius pointer fields must be independent
// copies.
func TestSceneBound_AreaBound_RoundTripPreserves(t *testing.T) {
	orig := &sim.Scene{
		ID:         "sc1",
		OriginAt:   time.Now().UTC(),
		OriginKind: "encounter",
		Bound:      sim.NewAreaBound(sim.Position{X: 10, Y: 10}, 3),
		Huddles:    map[sim.HuddleID]struct{}{},
	}

	cp := sim.CloneScene(orig)
	if cp.Bound.Kind != sim.SceneBoundArea {
		t.Fatalf("clone Bound.Kind = %q, want %q", cp.Bound.Kind, sim.SceneBoundArea)
	}

	// Mutate the clone's pointer-field contents.
	*cp.Bound.Anchor = sim.Position{X: 99, Y: 99}
	*cp.Bound.Radius = 99

	if got := *orig.Bound.Anchor; got != (sim.Position{X: 10, Y: 10}) {
		t.Errorf("original Anchor drifted: got %+v", got)
	}
	if got := *orig.Bound.Radius; got != 3 {
		t.Errorf("original Radius drifted: got %d", got)
	}
}

// TestSceneBound_UnboundedBound_RoundTrip covers the trivial unbounded
// clone case: no pointer fields to deep-copy, but Kind must round-trip.
func TestSceneBound_UnboundedBound_RoundTrip(t *testing.T) {
	orig := &sim.Scene{
		ID:         "sc1",
		OriginAt:   time.Now().UTC(),
		OriginKind: "atmosphere_refresh",
		Bound:      sim.NewUnboundedBound(),
		Huddles:    map[sim.HuddleID]struct{}{},
	}

	cp := sim.CloneScene(orig)
	if cp.Bound.Kind != sim.SceneBoundUnbounded {
		t.Fatalf("clone Bound.Kind = %q, want %q", cp.Bound.Kind, sim.SceneBoundUnbounded)
	}
	if cp.OriginStructureID() != "" {
		t.Errorf("unbounded scene OriginStructureID() = %q, want empty", cp.OriginStructureID())
	}
}
