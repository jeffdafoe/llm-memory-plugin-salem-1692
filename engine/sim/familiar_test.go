package sim_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// familiar_test.go — LLM-800: Wear / Remove from the pack and the duck that
// follows a duck-pendant wearer.

const (
	familiarOwnerID  sim.ActorID  = "owner-0001"
	familiarSprite   sim.SpriteID = "579d0c01-0000-4000-8000-000000000001"
	familiarRoadY                 = 140
	familiarEdgeScan              = 30 // sim.VisitorEdgeScanMaxDepth
)

var familiarOwnerPos = sim.Position{X: 100, Y: familiarRoadY + 5}

// familiarTerrain is all grass with one dirt road across the map at
// familiarRoadY, so the only map-edge road tiles are at its two ends.
func familiarTerrain() *sim.Terrain {
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainLightGrass
	}
	for x := 0; x < sim.MapW; x++ {
		data[familiarRoadY*sim.MapW+x] = sim.TerrainDirt
	}
	return &sim.Terrain{Data: data}
}

// familiarWorld seeds a player holding a duck pendant and a loaf, wearing
// what `wearing` lists. The caller runs the world.
func familiarWorld(t *testing.T, wearing []sim.ItemKind) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(familiarTerrain())
	handles.Sprites.Seed(map[sim.SpriteID]*sim.Sprite{
		familiarSprite: {
			ID:        familiarSprite,
			Name:      "Duck (mallard)",
			Behaviors: []string{sim.BehaviorWaterfowl, sim.BehaviorAmbient},
		},
	})
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		sim.DuckPendantKind: {Name: sim.DuckPendantKind, Category: "charm", Capabilities: []string{sim.CapabilityWear}},
		"silver_locket":     {Name: "silver_locket", Category: "charm", Capabilities: []string{sim.CapabilityWear}},
		"bread":             {Name: "bread", Category: sim.ItemCategoryFood},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		familiarOwnerID: {
			ID: familiarOwnerID, DisplayName: "Jeff", Kind: sim.KindPC, LoginUsername: "jeff",
			Pos: familiarOwnerPos, Facing: "south", State: sim.StateIdle,
			Inventory: map[sim.ItemKind]int{sim.DuckPendantKind: 1, "bread": 1},
			Wearing:   wearing,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	return w
}

// familiarView is what the tests read back off the world goroutine.
type familiarView struct {
	Count     int
	ID        sim.ActorID
	Kind      sim.ActorKind
	Pos       sim.Position
	Moving    bool
	MoveTo    sim.Position
	Wearing   []sim.ItemKind
	OwnerPos  sim.Position
	PersistID bool
}

func viewFamiliar(world *sim.World) familiarView {
	var v familiarView
	owner := world.Actors[familiarOwnerID]
	v.Wearing = append([]sim.ItemKind(nil), owner.Wearing...)
	v.OwnerPos = owner.Pos
	for id, a := range world.Actors {
		if a.FamiliarOf != familiarOwnerID {
			continue
		}
		v.Count++
		v.ID, v.Kind, v.Pos = id, a.Kind, a.Pos
		v.PersistID = sim.IsPersistedActorID(id)
		if a.MoveIntent != nil && a.MoveIntent.Destination.Position != nil {
			v.Moving = true
			v.MoveTo = *a.MoveIntent.Destination.Position
		}
	}
	return v
}

func chebyshev(a, b sim.Position) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return max(dx, dy)
}

func nearMapEdge(p sim.Position) bool {
	return p.X < familiarEdgeScan || p.X >= sim.MapW-familiarEdgeScan
}

func useItem(t *testing.T, w *sim.World, kind sim.ItemKind, action string, now time.Time) error {
	t.Helper()
	_, err := w.Send(sim.UseItem(familiarOwnerID, kind, action, now))
	return err
}

func runWorld(t *testing.T, w *sim.World) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
}

func familiarState(t *testing.T, w *sim.World) familiarView {
	t.Helper()
	return runOn(t, w, func(world *sim.World) (any, error) { return viewFamiliar(world), nil }).(familiarView)
}

func familiarTick(t *testing.T, w *sim.World, now time.Time) familiarView {
	t.Helper()
	if _, err := w.Send(sim.EvaluateFamiliarTick(now)); err != nil {
		t.Fatalf("familiar tick: %v", err)
	}
	return familiarState(t, w)
}

func TestItemActions(t *testing.T) {
	pendant := &sim.ItemKindDef{Name: "silver_locket", Capabilities: []string{sim.CapabilityWear}}
	bread := &sim.ItemKindDef{Name: "bread", Capabilities: []string{"portable"}}
	if got := sim.ItemActions(pendant, false); !slices.Equal(got, []string{sim.ItemActionWear}) {
		t.Errorf("pendant, not worn: %v, want [wear]", got)
	}
	if got := sim.ItemActions(pendant, true); !slices.Equal(got, []string{sim.ItemActionRemove}) {
		t.Errorf("pendant, worn: %v, want [remove]", got)
	}
	if got := sim.ItemActions(bread, false); got == nil || len(got) != 0 {
		t.Errorf("bread: %v, want empty non-nil", got)
	}
}

func TestUseItemRefusals(t *testing.T) {
	w := familiarWorld(t, nil)
	runWorld(t, w)
	now := time.Now()
	cases := []struct {
		name   string
		kind   sim.ItemKind
		action string
	}{
		{"bread cannot be worn", "bread", sim.ItemActionWear},
		{"a pendant not carried", "silver_locket", sim.ItemActionWear},
		{"remove what is not worn", sim.DuckPendantKind, sim.ItemActionRemove},
		{"unknown action", sim.DuckPendantKind, "juggle"},
	}
	for _, c := range cases {
		if err := useItem(t, w, c.kind, c.action, now); !errors.Is(err, sim.ErrItemAction) {
			t.Errorf("%s: err = %v, want ErrItemAction", c.name, err)
		}
	}
	if v := familiarState(t, w); v.Count != 0 || len(v.Wearing) != 0 {
		t.Errorf("refusals changed state: %+v", v)
	}
	if err := useItem(t, w, sim.DuckPendantKind, sim.ItemActionWear, now); err != nil {
		t.Fatalf("wear: %v", err)
	}
	if err := useItem(t, w, sim.DuckPendantKind, sim.ItemActionWear, now); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("wearing it twice: err = %v, want ErrItemAction", err)
	}
}

// Wear: the duck walks in from a map-edge road tile toward its owner.
func TestWearBringsFamiliarFromEdge(t *testing.T) {
	w := familiarWorld(t, nil)
	runWorld(t, w)
	if err := useItem(t, w, sim.DuckPendantKind, sim.ItemActionWear, time.Now()); err != nil {
		t.Fatalf("wear: %v", err)
	}
	v := familiarState(t, w)
	if !slices.Equal(v.Wearing, []sim.ItemKind{sim.DuckPendantKind}) {
		t.Fatalf("wearing = %v, want [duck_pendant]", v.Wearing)
	}
	if v.Count != 1 {
		t.Fatalf("familiars = %d, want 1", v.Count)
	}
	if v.Kind != sim.KindDecorative || v.PersistID {
		t.Errorf("familiar kind %v persisted-id %v, want decorative and a non-uuid id", v.Kind, v.PersistID)
	}
	if v.Pos.Y != familiarRoadY || !nearMapEdge(v.Pos) {
		t.Errorf("familiar starts at %+v, want the road at a map edge", v.Pos)
	}
	if !v.Moving || chebyshev(v.MoveTo, familiarOwnerPos) > sim.FamiliarFollowRadius || v.MoveTo == familiarOwnerPos {
		t.Errorf("familiar moving=%v to %+v, want a walk to beside its owner at %+v", v.Moving, v.MoveTo, familiarOwnerPos)
	}
}

// Remove: the duck walks off to a map edge and is gone once it stops there.
func TestRemoveSendsFamiliarOff(t *testing.T) {
	w := familiarWorld(t, nil)
	runWorld(t, w)
	now := time.Now()
	if err := useItem(t, w, sim.DuckPendantKind, sim.ItemActionWear, now); err != nil {
		t.Fatalf("wear: %v", err)
	}
	if err := useItem(t, w, sim.DuckPendantKind, sim.ItemActionRemove, now); err != nil {
		t.Fatalf("remove: %v", err)
	}
	v := familiarState(t, w)
	if len(v.Wearing) != 0 {
		t.Errorf("wearing = %v after remove, want none", v.Wearing)
	}
	if v.Count != 1 || !v.Moving || !nearMapEdge(v.MoveTo) {
		t.Fatalf("after remove: %+v, want the duck walking to a map edge", v)
	}
	// Still walking: it stays.
	if v = familiarTick(t, w, now.Add(2*time.Second)); v.Count != 1 {
		t.Fatalf("duck removed mid-walk")
	}
	// Arrived: the walk ends and the next pass removes it.
	runOn(t, w, func(world *sim.World) (any, error) {
		d := world.Actors[v.ID]
		d.Pos, d.MoveIntent = v.MoveTo, nil
		return nil, nil
	})
	if v = familiarTick(t, w, now.Add(4*time.Second)); v.Count != 0 {
		t.Errorf("familiars = %d after reaching the edge, want 0", v.Count)
	}
}

// Wearing it again while the duck walks off turns the same duck around.
func TestRewearTurnsFamiliarAround(t *testing.T) {
	w := familiarWorld(t, nil)
	runWorld(t, w)
	now := time.Now()
	for _, action := range []string{sim.ItemActionWear, sim.ItemActionRemove, sim.ItemActionWear} {
		if err := useItem(t, w, sim.DuckPendantKind, action, now); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	v := familiarState(t, w)
	if v.Count != 1 {
		t.Fatalf("familiars = %d, want the one duck", v.Count)
	}
	if !v.Moving || chebyshev(v.MoveTo, familiarOwnerPos) > sim.FamiliarFollowRadius {
		t.Errorf("duck heading to %+v, want back beside its owner", v.MoveTo)
	}
	// It is not removed when that walk ends.
	runOn(t, w, func(world *sim.World) (any, error) {
		d := world.Actors[v.ID]
		d.Pos, d.MoveIntent = v.MoveTo, nil
		return nil, nil
	})
	if v = familiarTick(t, w, now.Add(2*time.Second)); v.Count != 1 {
		t.Errorf("turned-around duck removed")
	}
}

// After a restart the wearer has no duck: the first pass puts one beside them,
// and it follows when they move.
func TestFamiliarRebuiltBesideOwnerAndFollows(t *testing.T) {
	w := familiarWorld(t, []sim.ItemKind{sim.DuckPendantKind})
	runWorld(t, w)
	now := time.Now()
	v := familiarTick(t, w, now)
	if v.Count != 1 {
		t.Fatalf("familiars = %d after the first pass, want 1", v.Count)
	}
	if d := chebyshev(v.Pos, familiarOwnerPos); d == 0 || d > sim.FamiliarFollowRadius || v.Moving {
		t.Errorf("rebuilt duck at %+v moving=%v, want resting beside its owner", v.Pos, v.Moving)
	}
	// Idle and near: the pond wander must not take it.
	runOn(t, w, func(world *sim.World) (any, error) {
		sim.EvaluateWaterfowl(world, now)
		sim.EvaluateWaterfowl(world, now.Add(13*time.Second))
		return nil, nil
	})
	if v = familiarState(t, w); v.Moving {
		t.Errorf("pond wander moved the familiar to %+v", v.MoveTo)
	}
	// The owner walks off: the duck follows.
	moved := sim.Position{X: familiarOwnerPos.X + 10, Y: familiarOwnerPos.Y + 3}
	runOn(t, w, func(world *sim.World) (any, error) {
		world.Actors[familiarOwnerID].Pos = moved
		return nil, nil
	})
	v = familiarTick(t, w, now.Add(2*time.Second))
	if !v.Moving || chebyshev(v.MoveTo, moved) > sim.FamiliarFollowRadius {
		t.Errorf("duck moving=%v to %+v, want beside its owner at %+v", v.Moving, v.MoveTo, moved)
	}
}

// Selling the last pendant takes it off, and the duck leaves.
func TestPendantGoneTakesItOff(t *testing.T) {
	w := familiarWorld(t, []sim.ItemKind{sim.DuckPendantKind})
	runWorld(t, w)
	now := time.Now()
	if v := familiarTick(t, w, now); v.Count != 1 {
		t.Fatalf("familiars = %d, want 1", v.Count)
	}
	runOn(t, w, func(world *sim.World) (any, error) {
		delete(world.Actors[familiarOwnerID].Inventory, sim.DuckPendantKind)
		return nil, nil
	})
	v := familiarTick(t, w, now.Add(2*time.Second))
	if len(v.Wearing) != 0 {
		t.Errorf("wearing = %v with no pendant held, want none", v.Wearing)
	}
	if v.Count != 1 || !v.Moving || !nearMapEdge(v.MoveTo) {
		t.Errorf("after the sale: %+v, want the duck walking to a map edge", v)
	}
}

// No free tile beside the owner: no duck yet (never one on the owner's own
// tile); it comes on the first pass after a tile frees up.
func TestFamiliarWaitsForAFreeTile(t *testing.T) {
	w := familiarWorld(t, []sim.ItemKind{sim.DuckPendantKind})
	runWorld(t, w)
	var blockers []sim.ActorID
	runOn(t, w, func(world *sim.World) (any, error) {
		r := sim.FamiliarFollowRadius
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				id := sim.ActorID(fmt.Sprintf("blocker-%d-%d", dx, dy))
				world.Actors[id] = &sim.Actor{
					ID: id, DisplayName: "Post", Kind: sim.KindDecorative, State: sim.StateIdle,
					Pos: sim.Position{X: familiarOwnerPos.X + dx, Y: familiarOwnerPos.Y + dy},
				}
				blockers = append(blockers, id)
			}
		}
		return nil, nil
	})
	now := time.Now()
	if v := familiarTick(t, w, now); v.Count != 0 {
		t.Fatalf("familiars = %d with every tile beside the owner taken, want 0 (at %+v)", v.Count, v.Pos)
	}
	runOn(t, w, func(world *sim.World) (any, error) {
		delete(world.Actors, blockers[0])
		return nil, nil
	})
	v := familiarTick(t, w, now.Add(2*time.Second))
	if v.Count != 1 || v.Pos == familiarOwnerPos || chebyshev(v.Pos, familiarOwnerPos) > sim.FamiliarFollowRadius {
		t.Errorf("after a tile frees: %+v, want one duck beside its owner", v)
	}
}

// LLM-812: while the owner stands still outdoors and awake, the resting duck
// potters — after a dwell it walks to a free tile on the FamiliarFollowRadius
// ring round them. An owner indoors, asleep or on the move gets no pottering.
func TestFamiliarPottersRoundAStandingOwner(t *testing.T) {
	w := familiarWorld(t, []sim.ItemKind{sim.DuckPendantKind})
	runWorld(t, w)
	now := time.Now()
	if v := familiarTick(t, w, now); v.Count != 1 || v.Moving {
		t.Fatalf("setup: %+v, want one duck resting beside its owner", v)
	}
	// First pass at rest: the dwell starts, no walk yet.
	if v := familiarTick(t, w, now.Add(time.Second)); v.Moving {
		t.Fatalf("walked before the dwell: to %+v", v.MoveTo)
	}
	// Past the longest dwell: it potters to the ring.
	late := now.Add(time.Second + sim.FamiliarPotterDwell + sim.FamiliarPotterJitter + time.Second)
	v := familiarTick(t, w, late)
	if !v.Moving || chebyshev(v.MoveTo, familiarOwnerPos) != sim.FamiliarFollowRadius || v.MoveTo == v.Pos {
		t.Fatalf("after the dwell: moving=%v to %+v, want a walk to the ring %d out from %+v", v.Moving, v.MoveTo, sim.FamiliarFollowRadius, familiarOwnerPos)
	}

	for _, c := range []struct {
		name string
		set  func(o *sim.Actor)
	}{
		{"owner indoors", func(o *sim.Actor) { o.InsideStructureID = "inn" }},
		{"owner asleep", func(o *sim.Actor) { until := time.Now().Add(time.Hour); o.SleepingUntil = &until }},
		{"owner walking", func(o *sim.Actor) {
			o.MoveIntent = &sim.MoveIntent{Destination: sim.NewPositionDestination(familiarOwnerPos)}
		}},
	} {
		runOn(t, w, func(world *sim.World) (any, error) {
			for _, a := range world.Actors {
				if a.FamiliarOf == familiarOwnerID {
					a.Pos, a.MoveIntent = v.MoveTo, nil
				}
			}
			o := world.Actors[familiarOwnerID]
			o.InsideStructureID, o.SleepingUntil, o.MoveIntent = "", nil, nil
			c.set(o)
			return nil, nil
		})
		familiarTick(t, w, late.Add(time.Minute))
		if got := familiarTick(t, w, late.Add(time.Hour)); got.Moving {
			t.Errorf("%s: the duck pottered to %+v", c.name, got.MoveTo)
		}
	}
}
