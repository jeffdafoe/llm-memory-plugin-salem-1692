package sim_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

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
		"prop":      {ID: "prop", Name: "Bench", Category: "prop", DefaultState: "default", States: []sim.AssetState{{ID: 1, State: "default"}}},
		"bldg":      {ID: "bldg", Name: "Tavern", Category: "structure", DefaultState: "default", States: []sim.AssetState{{ID: 2, State: "default"}}},
		"millhouse": {ID: "millhouse", Name: "Mill", Category: "structure", DefaultState: "default", States: []sim.AssetState{{ID: 3, State: "default"}}},
		// A structure asset with a BLANK name — used only to exercise the
		// display-name-clear guard (no asset-name fallback available).
		"blank": {ID: "blank", Name: "", Category: "structure", DefaultState: "default", States: []sim.AssetState{{ID: 4, State: "default"}}},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"prop-1":  {ID: "prop-1", AssetID: "prop", CurrentState: "default", Pos: sim.WorldPos{X: 100, Y: 100}},
		"tavern":  {ID: "tavern", AssetID: "bldg", CurrentState: "default", Pos: sim.WorldPos{X: 200, Y: 200}},
		"post":    {ID: "post", AssetID: "prop", CurrentState: "default", Pos: sim.WorldPos{X: 300, Y: 300}},
		"sign":    {ID: "sign", AssetID: "prop", CurrentState: "default", Pos: sim.WorldPos{X: 305, Y: 295}, AttachedTo: "post"},
		"lantern": {ID: "lantern", AssetID: "prop", CurrentState: "default", Pos: sim.WorldPos{X: 306, Y: 290}, AttachedTo: "sign"},
		// Root structure-category placements with NO backing Structure — the
		// promote-to-structure targets (LLM-249).
		"mill":  {ID: "mill", AssetID: "millhouse", CurrentState: "default", Pos: sim.WorldPos{X: 400, Y: 400}},
		"mill2": {ID: "mill2", AssetID: "millhouse", CurrentState: "default", Pos: sim.WorldPos{X: 420, Y: 400}},
		// A building whose asset has a blank name — the display-name-clear guard
		// fixture (clearing has no non-empty structure fallback).
		"corrupt-bld": {ID: "corrupt-bld", AssetID: "blank", CurrentState: "default", Pos: sim.WorldPos{X: 440, Y: 400}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern":      {ID: "tavern", DisplayName: "Tavern"},
		"corrupt-bld": {ID: "corrupt-bld", DisplayName: "Corrupt Hall"},
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
	if obj.Pos.X != 150.5 || obj.Pos.Y != 175.25 {
		t.Errorf("position = (%v, %v), want (150.5, 175.25)", obj.Pos.X, obj.Pos.Y)
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
	if obj.Pos.X != 100 || obj.Pos.Y != 100 {
		t.Errorf("position mutated to (%v,%v), want unchanged (100,100)", obj.Pos.X, obj.Pos.Y)
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

func TestSetVillageObjectOwner_SetAndClear(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	// Seed an actor to own the prop.
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"] = &sim.Actor{ID: "alice", DisplayName: "Alice", Kind: sim.KindNPCShared}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	res, err := w.Send(sim.SetVillageObjectOwner("prop-1", "alice"))
	if err != nil {
		t.Fatalf("set owner: %v", err)
	}
	if r := res.(sim.SetOwnerResult); r.ID != "prop-1" || r.OwnerActorID != "alice" {
		t.Errorf("result = %+v, want prop-1/alice", r)
	}
	if got := w.Published().VillageObjects["prop-1"].OwnerActorID; got != "alice" {
		t.Errorf("owner = %q, want alice", got)
	}

	// Clearing with an empty id is always allowed.
	if _, err := w.Send(sim.SetVillageObjectOwner("prop-1", "")); err != nil {
		t.Fatalf("clear owner: %v", err)
	}
	if got := w.Published().VillageObjects["prop-1"].OwnerActorID; got != "" {
		t.Errorf("owner = %q, want cleared", got)
	}

	// Metadata commands emit nothing — owner isn't in ObjectDTO.
	if got := len(cap.snapshot()); got != 0 {
		t.Errorf("emitted %d events, want 0 (owner change is client-invisible)", got)
	}
}

func TestSetVillageObjectOwner_OwnerNotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.SetVillageObjectOwner("prop-1", "ghost"))
	if !errors.Is(err, sim.ErrOwnerActorNotFound) {
		t.Errorf("err = %v, want ErrOwnerActorNotFound", err)
	}
	if got := w.Published().VillageObjects["prop-1"].OwnerActorID; got != "" {
		t.Errorf("owner = %q, want unchanged (empty) after rejected set", got)
	}
}

func TestSetVillageObjectOwner_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.SetVillageObjectOwner("ghost", ""))
	if !errors.Is(err, sim.ErrVillageObjectNotFound) {
		t.Errorf("err = %v, want ErrVillageObjectNotFound", err)
	}
}

func TestSetVillageObjectLoiterOffset_SetAndClear(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	x, y := 2, -3

	if _, err := w.Send(sim.SetVillageObjectLoiterOffset("prop-1", &x, &y)); err != nil {
		t.Fatalf("set offset: %v", err)
	}
	obj := w.Published().VillageObjects["prop-1"]
	if obj.LoiterOffsetX == nil || obj.LoiterOffsetY == nil || *obj.LoiterOffsetX != 2 || *obj.LoiterOffsetY != -3 {
		t.Errorf("offset = (%v,%v), want (2,-3)", obj.LoiterOffsetX, obj.LoiterOffsetY)
	}
	// Stored pointers must not alias the caller's — mutating the source after the
	// command must not change world state.
	x = 99
	if got := *w.Published().VillageObjects["prop-1"].LoiterOffsetX; got != 2 {
		t.Errorf("offset X = %d after mutating source, want 2 (must be copied)", got)
	}

	if _, err := w.Send(sim.SetVillageObjectLoiterOffset("prop-1", nil, nil)); err != nil {
		t.Fatalf("clear offset: %v", err)
	}
	obj = w.Published().VillageObjects["prop-1"]
	if obj.LoiterOffsetX != nil || obj.LoiterOffsetY != nil {
		t.Errorf("offset = (%v,%v), want cleared (nil,nil)", obj.LoiterOffsetX, obj.LoiterOffsetY)
	}
}

func TestSetVillageObjectLoiterOffset_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	x, y := 1, 1
	_, err := w.Send(sim.SetVillageObjectLoiterOffset("ghost", &x, &y))
	if !errors.Is(err, sim.ErrVillageObjectNotFound) {
		t.Errorf("err = %v, want ErrVillageObjectNotFound", err)
	}
}

// TestSetVillageObjectLoiterOffset_EmitsEvent: since ZBBS-HOME-289 put loiter in
// ObjectDTO, the command emits VillageObjectLoiterOffsetChanged carrying both the
// raw override and the server-resolved effective offset. prop-1's asset ("prop")
// has no door offset and footprint 0, so a cleared override resolves to the
// footprint default (0, 2); a set override resolves to itself.
func TestSetVillageObjectLoiterOffset_EmitsEvent(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)

	x, y := 2, -3
	if _, err := w.Send(sim.SetVillageObjectLoiterOffset("prop-1", &x, &y)); err != nil {
		t.Fatalf("set offset: %v", err)
	}
	evt := latestLoiterEvent(cap)
	if evt == nil {
		t.Fatal("no VillageObjectLoiterOffsetChanged emitted on set")
	}
	if evt.ObjectID != "prop-1" || evt.LoiterOffsetX == nil || *evt.LoiterOffsetX != 2 ||
		evt.EffectiveLoiterOffsetX != 2 || evt.EffectiveLoiterOffsetY != -3 {
		t.Errorf("set event = %+v, want prop-1 raw(2,-3) eff(2,-3)", evt)
	}

	if _, err := w.Send(sim.SetVillageObjectLoiterOffset("prop-1", nil, nil)); err != nil {
		t.Fatalf("clear offset: %v", err)
	}
	evt = latestLoiterEvent(cap)
	if evt == nil || evt.LoiterOffsetX != nil || evt.EffectiveLoiterOffsetX != 0 || evt.EffectiveLoiterOffsetY != 2 {
		t.Errorf("clear event = %+v, want raw nil + eff(0,2) footprint fallback", evt)
	}
}

// latestLoiterEvent returns the most recently captured loiter-offset event, or
// nil if none.
func latestLoiterEvent(cap *objEventCapture) *sim.VillageObjectLoiterOffsetChanged {
	var found *sim.VillageObjectLoiterOffsetChanged
	for _, evt := range cap.snapshot() {
		if e, ok := evt.(*sim.VillageObjectLoiterOffsetChanged); ok {
			found = e
		}
	}
	return found
}

func TestSetVillageObjectEntryPolicy_Applied(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	res, err := w.Send(sim.SetVillageObjectEntryPolicy("prop-1", sim.EntryPolicyOwner))
	if err != nil {
		t.Fatalf("set entry policy: %v", err)
	}
	if r := res.(sim.SetEntryPolicyResult); r.EntryPolicy != sim.EntryPolicyOwner {
		t.Errorf("result = %+v, want owner-only", r)
	}
	if got := w.Published().VillageObjects["prop-1"].EntryPolicy; got != sim.EntryPolicyOwner {
		t.Errorf("entry policy = %q, want owner-only", got)
	}
}

func TestSetVillageObjectEntryPolicy_Invalid(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.SetVillageObjectEntryPolicy("prop-1", sim.EntryPolicy("bogus")))
	if !errors.Is(err, sim.ErrInvalidEntryPolicy) {
		t.Errorf("err = %v, want ErrInvalidEntryPolicy", err)
	}
}

func TestSetVillageObjectEntryPolicy_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.SetVillageObjectEntryPolicy("ghost", sim.EntryPolicyOpen))
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

// countDisplayNameChanged / countTagsUpdated count the ZBBS-HOME-283 metadata
// events in the captured slice.
func countDisplayNameChanged(events []sim.Event) int {
	n := 0
	for _, evt := range events {
		if _, ok := evt.(*sim.VillageObjectDisplayNameChanged); ok {
			n++
		}
	}
	return n
}

func countTagsUpdated(events []sim.Event) int {
	n := 0
	for _, evt := range events {
		if _, ok := evt.(*sim.VillageObjectTagsUpdated); ok {
			n++
		}
	}
	return n
}

// --- SetVillageObjectDisplayName (ZBBS-HOME-283) ----------------------

func TestSetVillageObjectDisplayName_Applied(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	res, err := w.Send(sim.SetVillageObjectDisplayName("prop-1", "  Cozy Bench  "))
	if err != nil {
		t.Fatalf("set display name: %v", err)
	}
	// The command trims; the stored + echoed name is the trimmed value.
	if r := res.(sim.SetDisplayNameResult); r.DisplayName != "Cozy Bench" {
		t.Errorf("result = %+v, want display name 'Cozy Bench'", r)
	}
	if got := w.Published().VillageObjects["prop-1"].DisplayName; got != "Cozy Bench" {
		t.Errorf("display name = %q, want 'Cozy Bench'", got)
	}
	if n := countDisplayNameChanged(cap.snapshot()); n != 1 {
		t.Errorf("VillageObjectDisplayNameChanged count = %d, want 1", n)
	}
}

func TestSetVillageObjectDisplayName_ClearEmitsEmpty(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.SetVillageObjectDisplayName("prop-1", "Named")); err != nil {
		t.Fatalf("set display name: %v", err)
	}
	// Clearing with an empty string is valid and emits the empty name.
	res, err := w.Send(sim.SetVillageObjectDisplayName("prop-1", ""))
	if err != nil {
		t.Fatalf("clear display name: %v", err)
	}
	if r := res.(sim.SetDisplayNameResult); r.DisplayName != "" {
		t.Errorf("result = %+v, want cleared name", r)
	}
	if got := w.Published().VillageObjects["prop-1"].DisplayName; got != "" {
		t.Errorf("display name = %q, want cleared", got)
	}
	// Two real changes (set then clear) → two events.
	if n := countDisplayNameChanged(cap.snapshot()); n != 2 {
		t.Errorf("VillageObjectDisplayNameChanged count = %d, want 2", n)
	}
}

func TestSetVillageObjectDisplayName_NoOpNoEmit(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	// prop-1 starts with an empty DisplayName; setting it to "" is a no-op.
	if _, err := w.Send(sim.SetVillageObjectDisplayName("prop-1", "")); err != nil {
		t.Fatalf("no-op set display name: %v", err)
	}
	if n := countDisplayNameChanged(cap.snapshot()); n != 0 {
		t.Errorf("VillageObjectDisplayNameChanged count = %d, want 0 (no-op must not emit)", n)
	}
}

func TestSetVillageObjectDisplayName_Invalid(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	cases := map[string]string{
		"control char": "bad\x07name",
		"over cap":     strings.Repeat("z", sim.MaxVillageObjectDisplayNameLen+1),
	}
	for name, val := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := w.Send(sim.SetVillageObjectDisplayName("prop-1", val)); !errors.Is(err, sim.ErrInvalidDisplayName) {
				t.Errorf("err = %v, want ErrInvalidDisplayName", err)
			}
		})
	}
	if n := countDisplayNameChanged(cap.snapshot()); n != 0 {
		t.Errorf("rejected display names emitted %d events; want 0", n)
	}
}

func TestSetVillageObjectDisplayName_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.SetVillageObjectDisplayName("ghost", "X"))
	if !errors.Is(err, sim.ErrVillageObjectNotFound) {
		t.Errorf("err = %v, want ErrVillageObjectNotFound", err)
	}
}

// --- Add/RemoveVillageObjectTag (ZBBS-HOME-283) -----------------------

func TestAddVillageObjectTag_Applied(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	res, err := w.Send(sim.AddVillageObjectTag("prop-1", " vendor "))
	if err != nil {
		t.Fatalf("add tag: %v", err)
	}
	r := res.(sim.SetTagsResult)
	if len(r.Tags) != 1 || r.Tags[0] != "vendor" {
		t.Errorf("result tags = %v, want [vendor] (trimmed)", r.Tags)
	}
	if got := w.Published().VillageObjects["prop-1"].Tags; len(got) != 1 || got[0] != "vendor" {
		t.Errorf("stored tags = %v, want [vendor]", got)
	}
	if n := countTagsUpdated(cap.snapshot()); n != 1 {
		t.Errorf("VillageObjectTagsUpdated count = %d, want 1", n)
	}
}

func TestAddVillageObjectTag_DuplicateNoOp(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.AddVillageObjectTag("prop-1", "vendor")); err != nil {
		t.Fatalf("first add: %v", err)
	}
	res, err := w.Send(sim.AddVillageObjectTag("prop-1", "vendor"))
	if err != nil {
		t.Fatalf("duplicate add: %v", err)
	}
	// Set stays deduplicated; the duplicate add emits nothing.
	if r := res.(sim.SetTagsResult); len(r.Tags) != 1 {
		t.Errorf("tags = %v, want a single 'vendor' (no duplicate)", r.Tags)
	}
	if n := countTagsUpdated(cap.snapshot()); n != 1 {
		t.Errorf("VillageObjectTagsUpdated count = %d, want 1 (duplicate add must not emit)", n)
	}
}

func TestAddVillageObjectTag_Invalid(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	for _, val := range []string{"   ", "bad\x00tag", strings.Repeat("z", sim.MaxVillageObjectTagLen+1)} {
		if _, err := w.Send(sim.AddVillageObjectTag("prop-1", val)); !errors.Is(err, sim.ErrInvalidTag) {
			t.Errorf("add %q: err = %v, want ErrInvalidTag", val, err)
		}
	}
}

func TestAddVillageObjectTag_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.AddVillageObjectTag("ghost", "vendor"))
	if !errors.Is(err, sim.ErrVillageObjectNotFound) {
		t.Errorf("err = %v, want ErrVillageObjectNotFound", err)
	}
}

// TestAddVillageObjectTag_WellProvisionsDefaults covers the LLM-269 fix: tagging a
// bare placement `well` auto-provisions the canonical two-row water model (an
// infinite thirst drink row + a finite yield-only water row) and a "Well" name,
// so a dropped Well asset works without hand-editing the DB. prop-1 ships bare
// (no refresh rows, no display name).
func TestAddVillageObjectTag_WellProvisionsDefaults(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.AddVillageObjectTag("prop-1", "well")); err != nil {
		t.Fatalf("add well tag: %v", err)
	}

	rows := readRefreshes(t, w)
	if len(rows) != 2 {
		t.Fatalf("provisioned %d rows, want 2 (drink + water yield): %+v", len(rows), rows)
	}
	// Row 0: infinite thirst drink, not gatherable.
	drink := rows[0]
	if drink.Attribute != "thirst" || drink.Amount != -8 {
		t.Errorf("drink row = {attr %q amount %d}, want {thirst -8}", drink.Attribute, drink.Amount)
	}
	if drink.IsFinite() {
		t.Error("drink row should be infinite (nil supply), got finite")
	}
	if drink.IsGatherable() {
		t.Error("drink row must not be gatherable (no gather_item)")
	}
	// Row 1: finite yield-only water, gather_item=water, 20/20, periodic 6h.
	water := rows[1]
	if water.Attribute != "" || water.Amount != 0 {
		t.Errorf(`water row = {attr %q amount %d}, want yield-only {"" 0}`, water.Attribute, water.Amount)
	}
	if !water.IsYieldOnly() || string(water.GatherItem) != "water" {
		t.Errorf("water row should be yield-only gather_item=water, got gather_item %q", water.GatherItem)
	}
	if !water.IsFinite() || water.AvailableQuantity == nil || *water.AvailableQuantity != 20 ||
		water.MaxQuantity == nil || *water.MaxQuantity != 20 {
		t.Errorf("water row supply = %v/%v, want 20/20", water.AvailableQuantity, water.MaxQuantity)
	}
	if water.RefreshMode != sim.RefreshModePeriodic || water.RefreshPeriodHours == nil || *water.RefreshPeriodHours != 6 {
		t.Errorf("water row regen = {mode %q period %v}, want {periodic 6}", water.RefreshMode, water.RefreshPeriodHours)
	}
	// The set must satisfy the same validation the editor's set-refresh route uses.
	if err := sim.ValidateObjectRefreshes(rows); err != nil {
		t.Errorf("provisioned rows fail ValidateObjectRefreshes: %v", err)
	}
	// Named "Well" so move_to and the gather resolver can reach it.
	if got := w.Published().VillageObjects["prop-1"].DisplayName; got != "Well" {
		t.Errorf("display_name = %q, want Well", got)
	}
	// The name change is client-visible → exactly one display-name event.
	if n := countDisplayNameChanged(cap.snapshot()); n != 1 {
		t.Errorf("VillageObjectDisplayNameChanged count = %d, want 1", n)
	}
}

// TestAddVillageObjectTag_WellNonDestructive proves provisioning never clobbers an
// operator's existing setup: a `well` tag added to an object that already has
// refresh rows and a name leaves both untouched.
func TestAddVillageObjectTag_WellNonDestructive(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	seedRefreshes(t, w, []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -5}})
	if _, err := w.Send(sim.SetVillageObjectDisplayName("prop-1", "Old Cistern")); err != nil {
		t.Fatalf("seed name: %v", err)
	}
	nameEventsBefore := countDisplayNameChanged(cap.snapshot())

	if _, err := w.Send(sim.AddVillageObjectTag("prop-1", "well")); err != nil {
		t.Fatalf("add well tag: %v", err)
	}

	rows := readRefreshes(t, w)
	if len(rows) != 1 || rows[0].Amount != -5 {
		t.Errorf("rows = %+v, want the pre-seeded single {thirst -5} untouched", rows)
	}
	if got := w.Published().VillageObjects["prop-1"].DisplayName; got != "Old Cistern" {
		t.Errorf("display_name = %q, want pre-existing 'Old Cistern' (not clobbered)", got)
	}
	if n := countDisplayNameChanged(cap.snapshot()); n != nameEventsBefore {
		t.Errorf("display-name events = %d, want unchanged %d (non-destructive name skip)", n, nameEventsBefore)
	}
}

// TestAddVillageObjectTag_WellNamesRowedButNamelessObject exercises the two
// non-destructive guards independently: an object that already has rows but no
// name keeps its rows AND still gets the "Well" name + display-name event. (The
// _WellNonDestructive test above seeds both rows and a name, so it never covers
// this rows-present/name-empty path.)
func TestAddVillageObjectTag_WellNamesRowedButNamelessObject(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	seedRefreshes(t, w, []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -5}})

	if _, err := w.Send(sim.AddVillageObjectTag("prop-1", "well")); err != nil {
		t.Fatalf("add well tag: %v", err)
	}

	rows := readRefreshes(t, w)
	if len(rows) != 1 || rows[0].Amount != -5 {
		t.Errorf("rows = %+v, want the pre-seeded {thirst -5} untouched", rows)
	}
	if got := w.Published().VillageObjects["prop-1"].DisplayName; got != "Well" {
		t.Errorf("display_name = %q, want Well (nameless object gets named even with rows)", got)
	}
	if n := countDisplayNameChanged(cap.snapshot()); n != 1 {
		t.Errorf("VillageObjectDisplayNameChanged count = %d, want 1", n)
	}
}

// TestAddVillageObjectTag_NonWellNoProvision confirms only `well` triggers
// provisioning: a different tag leaves a bare object bare.
func TestAddVillageObjectTag_NonWellNoProvision(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.AddVillageObjectTag("prop-1", "vendor")); err != nil {
		t.Fatalf("add vendor tag: %v", err)
	}
	if rows := readRefreshes(t, w); len(rows) != 0 {
		t.Errorf("vendor tag provisioned %d refresh rows, want 0", len(rows))
	}
	if got := w.Published().VillageObjects["prop-1"].DisplayName; got != "" {
		t.Errorf("vendor tag set display_name %q, want empty", got)
	}
}

func TestRemoveVillageObjectTag_Applied(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.AddVillageObjectTag("prop-1", "vendor")); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	if _, err := w.Send(sim.AddVillageObjectTag("prop-1", "innkeeper")); err != nil {
		t.Fatalf("seed add 2: %v", err)
	}
	res, err := w.Send(sim.RemoveVillageObjectTag("prop-1", "vendor"))
	if err != nil {
		t.Fatalf("remove tag: %v", err)
	}
	if r := res.(sim.SetTagsResult); len(r.Tags) != 1 || r.Tags[0] != "innkeeper" {
		t.Errorf("result tags = %v, want [innkeeper]", r.Tags)
	}
	if got := w.Published().VillageObjects["prop-1"].Tags; len(got) != 1 || got[0] != "innkeeper" {
		t.Errorf("stored tags = %v, want [innkeeper]", got)
	}
	// Two adds + one remove = three tag-set changes.
	if n := countTagsUpdated(cap.snapshot()); n != 3 {
		t.Errorf("VillageObjectTagsUpdated count = %d, want 3", n)
	}
}

func TestRemoveVillageObjectTag_AbsentNoOp(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)
	res, err := w.Send(sim.RemoveVillageObjectTag("prop-1", "never-had-it"))
	if err != nil {
		t.Fatalf("remove absent tag: %v", err)
	}
	if r := res.(sim.SetTagsResult); len(r.Tags) != 0 {
		t.Errorf("tags = %v, want empty", r.Tags)
	}
	if n := countTagsUpdated(cap.snapshot()); n != 0 {
		t.Errorf("VillageObjectTagsUpdated count = %d, want 0 (removing an absent tag must not emit)", n)
	}
}

func TestRemoveVillageObjectTag_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.RemoveVillageObjectTag("ghost", "vendor"))
	if !errors.Is(err, sim.ErrVillageObjectNotFound) {
		t.Errorf("err = %v, want ErrVillageObjectNotFound", err)
	}
}

// seedRefreshes sets prop-1's refresh set via an inline command — used to plant
// a known starting set (including a non-nil LastRefreshAt anchor) before testing
// SetVillageObjectRefreshes' anchor-preservation behavior.
func seedRefreshes(t *testing.T, w *sim.World, rows []*sim.ObjectRefresh) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.VillageObjects["prop-1"].Refreshes = rows
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed refreshes: %v", err)
	}
}

// readRefreshes returns a snapshot of prop-1's live refresh rows, read on the
// world goroutine.
func readRefreshes(t *testing.T, w *sim.World) []*sim.ObjectRefresh {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects["prop-1"].Refreshes, nil
	}})
	if err != nil {
		t.Fatalf("read refreshes: %v", err)
	}
	return res.([]*sim.ObjectRefresh)
}

func TestSetVillageObjectRefreshes_Applied(t *testing.T) {
	w, cap := buildObjectAdminWorld(t)

	rows := []*sim.ObjectRefresh{
		// Finite continuous supply.
		{Attribute: "thirst", Amount: -12, AvailableQuantity: ip(3), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: ip(2)},
		// Infinite + dwell (no supply, no regen config).
		{Attribute: "tiredness", Amount: -4, DwellDelta: ip(-2), DwellPeriodMinutes: ip(30)},
	}
	res, err := w.Send(sim.SetVillageObjectRefreshes("prop-1", rows))
	if err != nil {
		t.Fatalf("set refreshes: %v", err)
	}
	out := res.(sim.SetRefreshesResult)
	if out.ID != "prop-1" || len(out.Refreshes) != 2 {
		t.Fatalf("result = %+v, want prop-1 with 2 rows", out)
	}

	live := readRefreshes(t, w)
	if len(live) != 2 {
		t.Fatalf("world has %d refresh rows, want 2", len(live))
	}
	if live[0].Attribute != "thirst" || live[0].Amount != -12 || !live[0].IsFinite() ||
		*live[0].AvailableQuantity != 3 || *live[0].MaxQuantity != 10 ||
		live[0].RefreshMode != sim.RefreshModeContinuous || *live[0].RefreshPeriodHours != 2 {
		t.Errorf("row 0 = %+v, want finite thirst", live[0])
	}
	if live[1].Attribute != "tiredness" || live[1].IsFinite() || !live[1].HasDwell() ||
		*live[1].DwellDelta != -2 || *live[1].DwellPeriodMinutes != 30 {
		t.Errorf("row 1 = %+v, want infinite tiredness+dwell", live[1])
	}

	// Stored rows must not alias the caller's pointers — mutating the source
	// after the command must not change world state.
	*rows[0].AvailableQuantity = 99
	if got := *readRefreshes(t, w)[0].AvailableQuantity; got != 3 {
		t.Errorf("available_quantity = %d after mutating source, want 3 (must be copied)", got)
	}

	// The refresh rows themselves are not in ObjectDTO, so they broadcast nothing.
	// The rows DO emit a display-name frame here (LLM-398): they turned a nameless
	// prop into an eat-on-arrival source, and a nameless source is unreachable
	// (resolveLoiteringObject skips it, so arrival-eat can never resolve it), so it
	// takes its asset's catalog name — and the name IS client-rendered.
	var named *sim.VillageObjectDisplayNameChanged
	for _, evt := range cap.snapshot() {
		switch e := evt.(type) {
		case *sim.VillageObjectDisplayNameChanged:
			named = e
		case *sim.VillageObjectTagsUpdated, *sim.VillageObjectStateChanged:
			t.Errorf("unexpected client-visible event emitted on set-refresh: %T", evt)
		}
	}
	if named == nil {
		t.Error("no display-name event: a nameless prop that gains refresh rows must take its asset name")
	} else if named.ObjectID != "prop-1" || named.DisplayName != "Bench" {
		t.Errorf("display-name event = %+v, want prop-1 → \"Bench\"", named)
	}
}

func TestSetVillageObjectRefreshes_PreservesAnchorOnUnchangedSchedule(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	anchor := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	seedRefreshes(t, w, []*sim.ObjectRefresh{
		{Attribute: "thirst", Amount: -5, AvailableQuantity: ip(3), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: ip(2), LastRefreshAt: &anchor},
	})

	// Same mode + period, changed amount/supply → anchor preserved (an unrelated
	// edit shouldn't restart the regen schedule).
	if _, err := w.Send(sim.SetVillageObjectRefreshes("prop-1", []*sim.ObjectRefresh{
		{Attribute: "thirst", Amount: -8, AvailableQuantity: ip(10), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: ip(2)},
	})); err != nil {
		t.Fatalf("set refreshes (unchanged schedule): %v", err)
	}
	live := readRefreshes(t, w)
	if live[0].LastRefreshAt == nil || !live[0].LastRefreshAt.Equal(anchor) {
		t.Errorf("anchor = %v, want preserved %v", live[0].LastRefreshAt, anchor)
	}

	// Changed period → anchor cleared so the regen tick re-anchors.
	if _, err := w.Send(sim.SetVillageObjectRefreshes("prop-1", []*sim.ObjectRefresh{
		{Attribute: "thirst", Amount: -8, AvailableQuantity: ip(10), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: ip(5)},
	})); err != nil {
		t.Fatalf("set refreshes (changed period): %v", err)
	}
	if got := readRefreshes(t, w)[0].LastRefreshAt; got != nil {
		t.Errorf("anchor = %v after period change, want nil", got)
	}
}

// TestSetVillageObjectRefreshes_NoPreserveForNonRefilling: a finite row with no
// regen period (depletes and never refills) doesn't carry its anchor forward —
// the regen tick ignores it, so a preserved anchor would just be dead state.
func TestSetVillageObjectRefreshes_NoPreserveForNonRefilling(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	anchor := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	seedRefreshes(t, w, []*sim.ObjectRefresh{
		{Attribute: "thirst", Amount: -5, AvailableQuantity: ip(3), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous, LastRefreshAt: &anchor},
	})
	if _, err := w.Send(sim.SetVillageObjectRefreshes("prop-1", []*sim.ObjectRefresh{
		{Attribute: "thirst", Amount: -8, AvailableQuantity: ip(10), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous},
	})); err != nil {
		t.Fatalf("set refreshes: %v", err)
	}
	if got := readRefreshes(t, w)[0].LastRefreshAt; got != nil {
		t.Errorf("anchor = %v for a non-refilling (nil-period) row, want nil", got)
	}
}

// TestSetVillageObjectRefreshes_NilExistingRowSkipped: a nil row in persisted
// world state must not panic the existing-row index pass on an admin edit.
func TestSetVillageObjectRefreshes_NilExistingRowSkipped(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	seedRefreshes(t, w, []*sim.ObjectRefresh{
		nil,
		{Attribute: "thirst", Amount: -5, AvailableQuantity: ip(3), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: ip(2)},
	})
	if _, err := w.Send(sim.SetVillageObjectRefreshes("prop-1", []*sim.ObjectRefresh{
		{Attribute: "thirst", Amount: -8, AvailableQuantity: ip(10), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: ip(2)},
	})); err != nil {
		t.Fatalf("set refreshes with nil existing row: %v", err)
	}
	if got := readRefreshes(t, w); len(got) != 1 || got[0].Attribute != "thirst" {
		t.Errorf("result = %+v, want single thirst row", got)
	}
}

func TestSetVillageObjectRefreshes_ClearsAll(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	if _, err := w.Send(sim.SetVillageObjectRefreshes("prop-1", []*sim.ObjectRefresh{
		{Attribute: "thirst", Amount: -5, AvailableQuantity: ip(3), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous},
	})); err != nil {
		t.Fatalf("set refreshes: %v", err)
	}
	if _, err := w.Send(sim.SetVillageObjectRefreshes("prop-1", nil)); err != nil {
		t.Fatalf("clear refreshes: %v", err)
	}
	if got := readRefreshes(t, w); len(got) != 0 {
		t.Errorf("refresh rows = %d after clear, want 0", len(got))
	}
}

func TestSetVillageObjectRefreshes_NotFound(t *testing.T) {
	w, _ := buildObjectAdminWorld(t)
	_, err := w.Send(sim.SetVillageObjectRefreshes("ghost", []*sim.ObjectRefresh{
		{Attribute: "thirst", Amount: -1, AvailableQuantity: ip(1), MaxQuantity: ip(1),
			RefreshMode: sim.RefreshModeContinuous},
	}))
	if !errors.Is(err, sim.ErrVillageObjectNotFound) {
		t.Errorf("err = %v, want ErrVillageObjectNotFound", err)
	}
}

func TestSetVillageObjectRefreshes_InvalidRejected(t *testing.T) {
	cases := []struct {
		name string
		rows []*sim.ObjectRefresh
	}{
		{"positive amount", []*sim.ObjectRefresh{{Attribute: "thirst", Amount: 5}}},
		{"unknown attribute", []*sim.ObjectRefresh{{Attribute: "vibes", Amount: -1}}},
		{"empty attribute", []*sim.ObjectRefresh{{Attribute: "", Amount: -1}}},
		{"duplicate attribute", []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -1}, {Attribute: "thirst", Amount: -2}}},
		{"finite pair half-set", []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -1, AvailableQuantity: ip(3)}}},
		{"available exceeds max", []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -1, AvailableQuantity: ip(11), MaxQuantity: ip(10),
				RefreshMode: sim.RefreshModeContinuous}}},
		{"infinite with mode", []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -1, RefreshMode: sim.RefreshModeContinuous}}},
		{"infinite with period", []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -1, RefreshPeriodHours: ip(2)}}},
		{"finite bad mode", []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -1, AvailableQuantity: ip(3), MaxQuantity: ip(10),
				RefreshMode: "hourly"}}},
		{"dwell half-set", []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -1, DwellDelta: ip(-2)}}},
		{"dwell positive delta", []*sim.ObjectRefresh{
			{Attribute: "thirst", Amount: -1, DwellDelta: ip(2), DwellPeriodMinutes: ip(30)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := buildObjectAdminWorld(t)
			// Plant a valid set first so we can prove an invalid call leaves it intact.
			seedRefreshes(t, w, []*sim.ObjectRefresh{
				{Attribute: "hunger", Amount: -3, RefreshMode: ""},
			})
			_, err := w.Send(sim.SetVillageObjectRefreshes("prop-1", tc.rows))
			if !errors.Is(err, sim.ErrInvalidRefresh) {
				t.Fatalf("err = %v, want ErrInvalidRefresh", err)
			}
			live := readRefreshes(t, w)
			if len(live) != 1 || live[0].Attribute != "hunger" {
				t.Errorf("refresh set mutated on invalid input: %+v", live)
			}
		})
	}
}
