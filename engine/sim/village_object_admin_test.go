package sim_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// objEventCapture records emitted events for assertion. Handle runs on the
// world goroutine; the mutex guards the slice against the test goroutine's
// reads after Send returns.
type objEventCapture struct {
	mu     sync.Mutex
	events []sim.Event
}

func (c *objEventCapture) Handle(_ *sim.World, evt sim.Event) {
	c.mu.Lock()
	c.events = append(c.events, evt)
	c.mu.Unlock()
}

func (c *objEventCapture) snapshot() []sim.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]sim.Event, len(c.events))
	copy(out, c.events)
	return out
}

// buildObjectAdminWorld seeds a world for the admin object commands: a plain
// prop, a structure-bridge object whose id matches a Structure (shared
// identity), and a parent prop with a child overlay and a grandchild overlay
// (post ← sign ← lantern). The capture subscriber is registered BEFORE Run so
// it sees every emitted event.
func buildObjectAdminWorld(t *testing.T) (*sim.World, *objEventCapture) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"prop": {ID: "prop", Name: "Bench", Category: "prop", DefaultState: "default", States: []sim.AssetState{{ID: 1, State: "default"}}},
		"bldg": {ID: "bldg", Name: "Tavern", Category: "structure", DefaultState: "default", States: []sim.AssetState{{ID: 2, State: "default"}}},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"prop-1":  {ID: "prop-1", AssetID: "prop", CurrentState: "default", X: 100, Y: 100},
		"tavern":  {ID: "tavern", AssetID: "bldg", CurrentState: "default", X: 200, Y: 200},
		"post":    {ID: "post", AssetID: "prop", CurrentState: "default", X: 300, Y: 300},
		"sign":    {ID: "sign", AssetID: "prop", CurrentState: "default", X: 305, Y: 295, AttachedTo: "post"},
		"lantern": {ID: "lantern", AssetID: "prop", CurrentState: "default", X: 306, Y: 290, AttachedTo: "sign"},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	cap := &objEventCapture{}
	w.Subscribe(cap)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	return w, cap
}

func TestMoveVillageObject_Applied(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)

	res, err := w.Send(sim.MoveVillageObject("prop-1", 150.5, 175.25))
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	mr := res.(sim.MoveObjectResult)
	if mr.ID != "prop-1" || mr.X != 150.5 || mr.Y != 175.25 {
		t.Errorf("result = %+v, want prop-1 (150.5, 175.25)", mr)
	}
	obj := w.Published().VillageObjects["prop-1"]
	if obj.X != 150.5 || obj.Y != 175.25 {
		t.Errorf("position = (%v, %v), want (150.5, 175.25)", obj.X, obj.Y)
	}

	var moved *sim.VillageObjectMoved
	for _, evt := range cap.snapshot() {
		if m, ok := evt.(*sim.VillageObjectMoved); ok {
			moved = m
		}
	}
	if moved == nil {
		t.Fatal("no VillageObjectMoved emitted")
	}
	if moved.ObjectID != "prop-1" || moved.X != 150.5 || moved.Y != 175.25 {
		t.Errorf("event = %+v, want prop-1 (150.5, 175.25)", moved)
	}
}

func TestMoveVillageObject_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.MoveVillageObject("ghost", 1, 2))
	if !errors.Is(err, sim.ErrVillageObjectNotFound) {
		t.Errorf("err = %v, want ErrVillageObjectNotFound", err)
	}
}

// TestMoveVillageObject_RejectsNonFinite proves the sim-level invariant: a
// direct call with a NaN/Inf coordinate is refused and does NOT mutate world
// state (a corrupt coordinate must never reach a checkpoint).
func TestMoveVillageObject_RejectsNonFinite(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	for _, bad := range []struct{ x, y float64 }{
		{math.NaN(), 0},
		{0, math.Inf(1)},
		{math.Inf(-1), 0},
	} {
		_, err := w.Send(sim.MoveVillageObject("prop-1", bad.x, bad.y))
		if !errors.Is(err, sim.ErrInvalidObjectPosition) {
			t.Errorf("MoveVillageObject(%v,%v) err = %v, want ErrInvalidObjectPosition", bad.x, bad.y, err)
		}
	}
	obj := w.Published().VillageObjects["prop-1"]
	if obj.X != 100 || obj.Y != 100 {
		t.Errorf("position mutated to (%v,%v), want unchanged (100,100)", obj.X, obj.Y)
	}
}

func TestDeleteVillageObject_Plain(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)

	res, err := w.Send(sim.DeleteVillageObject("prop-1"))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	dr := res.(sim.DeleteObjectResult)
	if len(dr.DeletedIDs) != 1 || dr.DeletedIDs[0] != "prop-1" {
		t.Errorf("deleted = %v, want [prop-1]", dr.DeletedIDs)
	}
	if _, ok := w.Published().VillageObjects["prop-1"]; ok {
		t.Error("prop-1 still present after delete")
	}
	if got := countDeleted(cap.snapshot()); got != 1 {
		t.Errorf("emitted %d VillageObjectDeleted, want 1", got)
	}
}

// TestDeleteVillageObject_CascadesAttachedChildren proves a parent delete
// transitively removes its attached overlays (post ← sign ← lantern), emits one
// VillageObjectDeleted per removed id, and reports children before the parent.
func TestDeleteVillageObject_CascadesAttachedChildren(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)

	res, err := w.Send(sim.DeleteVillageObject("post"))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	dr := res.(sim.DeleteObjectResult)
	if len(dr.DeletedIDs) != 3 {
		t.Fatalf("deleted = %v, want 3 (post, sign, lantern)", dr.DeletedIDs)
	}
	// Post-order contract: every overlay is reported before the object it is
	// attached to (lantern → sign → post). Assert each edge directly, not just
	// "parent last".
	idx := indexOf(dr.DeletedIDs)
	if !(idx["lantern"] < idx["sign"] && idx["sign"] < idx["post"]) {
		t.Errorf("order = %v, want child-before-parent (lantern < sign < post)", dr.DeletedIDs)
	}
	snap := w.Published()
	for _, id := range []sim.VillageObjectID{"post", "sign", "lantern"} {
		if _, ok := snap.VillageObjects[id]; ok {
			t.Errorf("%q still present after cascade delete", id)
		}
	}
	if got := countDeleted(cap.snapshot()); got != 3 {
		t.Errorf("emitted %d VillageObjectDeleted, want 3", got)
	}
}

// TestDeleteVillageObject_RefusesStructure proves the shared-identity guard: an
// object whose id matches a Structure is refused (deleting it would orphan the
// live aggregate) and left in place.
func TestDeleteVillageObject_RefusesStructure(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)

	_, err := w.Send(sim.DeleteVillageObject("tavern"))
	if !errors.Is(err, sim.ErrVillageObjectIsStructure) {
		t.Errorf("err = %v, want ErrVillageObjectIsStructure", err)
	}
	if _, ok := w.Published().VillageObjects["tavern"]; !ok {
		t.Error("tavern removed despite structure-bridge refusal")
	}
}

func TestDeleteVillageObject_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.DeleteVillageObject("ghost"))
	if !errors.Is(err, sim.ErrVillageObjectNotFound) {
		t.Errorf("err = %v, want ErrVillageObjectNotFound", err)
	}
}

// indexOf maps each id to its position in the slice (for ordering assertions).
func indexOf(ids []sim.VillageObjectID) map[sim.VillageObjectID]int {
	out := make(map[sim.VillageObjectID]int, len(ids))
	for i, id := range ids {
		out[id] = i
	}
	return out
}

// countDeleted counts VillageObjectDeleted events in the captured slice.
func countDeleted(events []sim.Event) int {
	n := 0
	for _, evt := range events {
		if _, ok := evt.(*sim.VillageObjectDeleted); ok {
			n++
		}
	}
	return n
}
