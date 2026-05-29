package sim_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// eventRec is a mutex-guarded event sink for MoveActor tests that need to
// assert which events did (or did not) fire.
type eventRec struct {
	mu     sync.Mutex
	events []sim.Event
}

func (r *eventRec) handle(_ *sim.World, e sim.Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

// countEvents returns how many recorded events satisfy match. Safe to
// call from the test goroutine after a synchronous Send round-trip — the
// Send reply establishes happens-before over the subscriber appends.
func (r *eventRec) countEvents(match func(sim.Event) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if match(e) {
			n++
		}
	}
	return n
}

// snapshot returns a copy of the recorded events under the lock. Safe to call
// from the test goroutine after a synchronous Send round-trip.
func (r *eventRec) snapshot() []sim.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.Event(nil), r.events...)
}

// findMoveStarted asserts exactly one ActorMoveStarted was recorded and returns
// it.
func findMoveStarted(t *testing.T, rec *eventRec) *sim.ActorMoveStarted {
	t.Helper()
	var found *sim.ActorMoveStarted
	n := 0
	for _, e := range rec.snapshot() {
		if ms, ok := e.(*sim.ActorMoveStarted); ok {
			found = ms
			n++
		}
	}
	if n != 1 {
		t.Fatalf("got %d ActorMoveStarted events, want exactly 1", n)
	}
	return found
}

// buildMoveTestWorld seeds a running world for MoveActor tests:
//
//   - all-grass terrain
//   - "inn": a non-obstacle house structure at world (320,320); its
//     asset has a door offset, so structureEntryTile resolves to a
//     walkable door tile.
//   - "well": a closed-entry structure (StructureEnter must be rejected;
//     StructureVisit must still be allowed).
//   - "gazebo": an OPEN-entry but doorless structure — its asset has no
//     door offset, so StructureEnter must still be rejected (no entry
//     tile), distinct from "well"'s closed-policy rejection.
//   - "walker": an actor parked at the pad origin with a clear path to
//     everything.
//
// The returned eventRec captures every emitted event.
func buildMoveTestWorld(t *testing.T) (*sim.World, context.CancelFunc, *eventRec) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"house":  {ID: "house", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(2)},
		"well":   {ID: "well", Category: "prop"},        // no door offset
		"gazebo": {ID: "gazebo", Category: "structure"}, // no door offset
		"lamp":   {ID: "lamp", Category: "prop"},        // bare prop — no Structure shell
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"inn":    {ID: "inn", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 320}},
		"well":   {ID: "well", AssetID: "well", Pos: sim.WorldPos{X: 640, Y: 320}, EntryPolicy: sim.EntryPolicyClosed},
		"gazebo": {ID: "gazebo", AssetID: "gazebo", Pos: sim.WorldPos{X: 960, Y: 320}, EntryPolicy: sim.EntryPolicyOpen},
		// "lamp" is a bare prop placement with NO paired Structure row — the
		// has_interior=false case. Used by object_visit tests (ZBBS-WORK-351).
		"lamp": {ID: "lamp", AssetID: "lamp", Pos: sim.WorldPos{X: 1280, Y: 320}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"inn":    {ID: "inn", DisplayName: "Inn"},
		"well":   {ID: "well", DisplayName: "Well"},
		"gazebo": {ID: "gazebo", DisplayName: "Gazebo"},
		// NB: "lamp" deliberately absent — bare placement.
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"walker": {ID: "walker", DisplayName: "Walker", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	rec := &eventRec{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel, rec
}

// moveIntentOf returns a deep copy of an actor's current MoveIntent (nil
// when the actor isn't moving), read inside a command so the test
// goroutine never touches live world state.
func moveIntentOf(t *testing.T, w *sim.World, id sim.ActorID) *sim.MoveIntent {
	t.Helper()
	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors[id]
			if a == nil {
				return (*sim.MoveIntent)(nil), nil
			}
			return sim.CloneMoveIntent(a.MoveIntent), nil
		},
	})
	if err != nil {
		t.Fatalf("moveIntentOf: %v", err)
	}
	return res.(*sim.MoveIntent)
}

// huddleIDOf returns an actor's CurrentHuddleID.
func huddleIDOf(t *testing.T, w *sim.World, id sim.ActorID) sim.HuddleID {
	t.Helper()
	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors[id].CurrentHuddleID, nil
		},
	})
	if err != nil {
		t.Fatalf("huddleIDOf: %v", err)
	}
	return res.(sim.HuddleID)
}

// TestMoveActor_EmitsMoveStarted asserts an accepted MoveActor emits an
// ActorMoveStarted carrying the resolved goal tile + destination metadata, so
// the client read surface can begin animating the walk.
func TestMoveActor_EmitsMoveStarted(t *testing.T) {
	now := time.Now().UTC()

	t.Run("position carries the exact resolved tile", func(t *testing.T) {
		w, cancel, rec := buildMoveTestWorld(t)
		defer cancel()
		dest := sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5})
		if _, err := w.Send(sim.MoveActor("walker", dest, false, now)); err != nil {
			t.Fatalf("MoveActor rejected: %v", err)
		}
		ev := findMoveStarted(t, rec)
		if ev.ActorID != "walker" {
			t.Errorf("ActorID = %q, want walker", ev.ActorID)
		}
		if ev.DestinationKind != sim.MoveDestinationPosition {
			t.Errorf("DestinationKind = %q, want position", ev.DestinationKind)
		}
		if want := (sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5}); ev.TargetPosition != want {
			t.Errorf("TargetPosition = %+v, want %+v", ev.TargetPosition, want)
		}
		if want := (sim.Position{X: sim.PadX, Y: sim.PadY}); ev.FromPosition != want {
			t.Errorf("FromPosition = %+v, want %+v (the walk start)", ev.FromPosition, want)
		}
		// Path is the full cost-weighted route, inclusive of both endpoints:
		// it starts at the actor's tile and ends at the resolved goal. The
		// contract is len >= 1 (a no-op move to the current tile yields a
		// single-point path); this route is non-trivial so both endpoint
		// checks below apply.
		if len(ev.Path) == 0 {
			t.Fatalf("Path is empty, want at least the start tile")
		}
		if got, want := ev.Path[0], (sim.GridPoint{X: ev.FromPosition.X, Y: ev.FromPosition.Y}); got != want {
			t.Errorf("Path[0] = %+v, want %+v (the walk start)", got, want)
		}
		if got, want := ev.Path[len(ev.Path)-1], (sim.GridPoint{X: ev.TargetPosition.X, Y: ev.TargetPosition.Y}); got != want {
			t.Errorf("Path[last] = %+v, want %+v (the resolved goal)", got, want)
		}
		if ev.StructureID != "" {
			t.Errorf("StructureID = %q, want empty for a position destination", ev.StructureID)
		}
		if ev.MovementAttemptID != 1 {
			t.Errorf("MovementAttemptID = %d, want 1 (matches the stamped intent)", ev.MovementAttemptID)
		}
	})

	t.Run("structure enter carries the structure id + kind", func(t *testing.T) {
		w, cancel, rec := buildMoveTestWorld(t)
		defer cancel()
		if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("inn"), false, now)); err != nil {
			t.Fatalf("MoveActor rejected: %v", err)
		}
		ev := findMoveStarted(t, rec)
		if ev.DestinationKind != sim.MoveDestinationStructureEnter {
			t.Errorf("DestinationKind = %q, want structure_enter", ev.DestinationKind)
		}
		if ev.StructureID != "inn" {
			t.Errorf("StructureID = %q, want inn", ev.StructureID)
		}
		// TargetPosition is the resolved door tile — not the actor's start.
		if want := (sim.Position{X: sim.PadX, Y: sim.PadY}); ev.TargetPosition == want {
			t.Errorf("TargetPosition unexpectedly equals the start origin %+v (no real goal resolved)", want)
		}
	})
}

// TestMoveActor_RejectionEmitsNoMoveStarted asserts a rejected MoveActor stamps
// no intent and emits no ActorMoveStarted.
func TestMoveActor_RejectionEmitsNoMoveStarted(t *testing.T) {
	now := time.Now().UTC()
	w, cancel, rec := buildMoveTestWorld(t)
	defer cancel()
	// Entering a closed structure ("well") is rejected at validation.
	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("well"), false, now)); err == nil {
		t.Fatal("expected MoveActor to reject entering a closed structure")
	}
	n := rec.countEvents(func(e sim.Event) bool {
		_, ok := e.(*sim.ActorMoveStarted)
		return ok
	})
	if n != 0 {
		t.Errorf("got %d ActorMoveStarted on a rejected move, want 0", n)
	}
}

// TestMoveActor_HappyPathPerKind covers acceptance for each destination
// kind: the command returns a fresh attempt ID and stamps a matching
// MoveIntent on the actor.
func TestMoveActor_HappyPathPerKind(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name     string
		dest     sim.MoveDestination
		wantKind sim.MoveDestinationKind
	}{
		{"structure enter", sim.NewStructureEnterDestination("inn"), sim.MoveDestinationStructureEnter},
		{"structure visit", sim.NewStructureVisitDestination("inn"), sim.MoveDestinationStructureVisit},
		{"object visit (bare prop)", sim.NewObjectVisitDestination("lamp"), sim.MoveDestinationObjectVisit},
		{"position", sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5}), sim.MoveDestinationPosition},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, cancel, _ := buildMoveTestWorld(t)
			defer cancel()

			res, err := w.Send(sim.MoveActor("walker", c.dest, false, now))
			if err != nil {
				t.Fatalf("MoveActor rejected: %v", err)
			}
			r := res.(sim.MoveActorResult)
			if r.MovementAttemptID != 1 {
				t.Errorf("MovementAttemptID = %d, want 1", r.MovementAttemptID)
			}
			if r.SupersededAttemptID != 0 {
				t.Errorf("SupersededAttemptID = %d, want 0", r.SupersededAttemptID)
			}

			mi := moveIntentOf(t, w, "walker")
			if mi == nil {
				t.Fatal("walker has no MoveIntent after accepted MoveActor")
			}
			if mi.Destination.Kind != c.wantKind {
				t.Errorf("MoveIntent kind = %q, want %q", mi.Destination.Kind, c.wantKind)
			}
			if mi.AttemptID != 1 {
				t.Errorf("MoveIntent.AttemptID = %d, want 1", mi.AttemptID)
			}
		})
	}
}

// TestMoveActor_Rejections covers the validation rejections — each
// leaves the actor with no MoveIntent.
func TestMoveActor_Rejections(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name  string
		actor sim.ActorID
		dest  sim.MoveDestination
	}{
		{"actor not found", "ghost", sim.NewPositionDestination(sim.Position{X: sim.PadX + 1, Y: sim.PadY + 1})},
		{"structure not found", "walker", sim.NewStructureEnterDestination("nowhere")},
		{"entry policy closed", "walker", sim.NewStructureEnterDestination("well")},
		{"doorless structure (open policy)", "walker", sim.NewStructureEnterDestination("gazebo")},
		// Regression guard for ZBBS-WORK-351: a bare VillageObject without a
		// Structure shell must NOT silently fall through structure_enter to
		// object_visit — the dispatch is the client's responsibility. The
		// engine rejects structure_enter on "lamp" because no Structure row
		// shares its id.
		{"structure_enter on bare object 404s (no silent fallthrough)", "walker", sim.NewStructureEnterDestination("lamp")},
		{"object_visit object not found", "walker", sim.NewObjectVisitDestination("nowhere-object")},
		{"untraversable position", "walker", sim.NewPositionDestination(sim.Position{X: -5, Y: -5})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, cancel, _ := buildMoveTestWorld(t)
			defer cancel()

			if _, err := w.Send(sim.MoveActor(c.actor, c.dest, false, now)); err == nil {
				t.Fatal("expected MoveActor to be rejected, got nil error")
			}
			if c.actor == "walker" {
				if mi := moveIntentOf(t, w, "walker"); mi != nil {
					t.Errorf("rejected MoveActor left a MoveIntent: %+v", mi)
				}
			}
		})
	}
}

// TestMoveActor_DoorlessStructureRejectedAtValidation covers the step-2
// contract: a StructureEnter to a non-closed but doorless structure is
// rejected at destination validation with the specific "no door" error
// — not later by resolvePathTarget with a generic "cannot be resolved"
// message. "gazebo" is EntryPolicyOpen with no door offset, so it gets
// past the closed/membership checks and must be caught by the explicit
// entry-tile check.
func TestMoveActor_DoorlessStructureRejectedAtValidation(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	_, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("gazebo"), false, now))
	if err == nil {
		t.Fatal("expected StructureEnter to a doorless structure to be rejected")
	}
	if !strings.Contains(err.Error(), "no door") {
		t.Errorf("expected the step-2 'no door' rejection, got: %v", err)
	}
}

// TestMoveActor_StructureVisitClosedStructureAllowed covers that
// StructureVisit ignores entry policy — an actor can always walk to a
// visitor slot outside a closed structure (a well).
func TestMoveActor_StructureVisitClosedStructureAllowed(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureVisitDestination("well"), false, now)); err != nil {
		t.Fatalf("StructureVisit to a closed structure should be allowed: %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi == nil || mi.Destination.Kind != sim.MoveDestinationStructureVisit {
		t.Errorf("walker MoveIntent = %+v, want a structure_visit intent", mi)
	}
}

// TestMoveActor_NoPath covers the path-existence rejection: a walkable
// target tile that is completely ringed by impassable water is
// resolvable but unreachable.
func TestMoveActor_NoPath(t *testing.T) {
	terrain := makeAllGrassTerrain()
	tx, ty := sim.PadX+20, sim.PadY+20
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			terrain.Data[(ty+dy)*sim.MapW+(tx+dx)] = sim.TerrainDeepWater
		}
	}
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(terrain)
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"walker": {ID: "walker", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now().UTC()
	_, err = w.Send(sim.MoveActor("walker", sim.NewPositionDestination(sim.Position{X: tx, Y: ty}), false, now))
	if err == nil {
		t.Fatal("expected no-path rejection for a water-ringed target")
	}
}

// TestMoveActor_InHuddleRequiresLeaveFirst covers that an actor in an
// active huddle cannot move unless LeaveHuddleFirst is set — and that the
// rejected command leaves both the huddle and the (absent) MoveIntent
// untouched.
func TestMoveActor_InHuddleRequiresLeaveFirst(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.JoinHuddle("walker", "inn", "", now)); err != nil {
		t.Fatalf("JoinHuddle: %v", err)
	}
	huddleBefore := huddleIDOf(t, w, "walker")
	if huddleBefore == "" {
		t.Fatal("walker not in a huddle after JoinHuddle")
	}

	_, err := w.Send(sim.MoveActor("walker", sim.NewPositionDestination(sim.Position{X: sim.PadX + 3, Y: sim.PadY + 3}), false, now))
	if err == nil {
		t.Fatal("expected MoveActor to reject an in-huddle actor without LeaveHuddleFirst")
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Errorf("rejected in-huddle MoveActor left a MoveIntent: %+v", mi)
	}
	if got := huddleIDOf(t, w, "walker"); got != huddleBefore {
		t.Errorf("rejected MoveActor changed huddle membership: %q -> %q", huddleBefore, got)
	}
}

// TestMoveActor_InHuddleRejectionPrecedesPathCheck covers the ordering
// fix: the active-huddle gate is evaluated BEFORE path validation, so an
// in-huddle actor without LeaveHuddleFirst gets the huddle-specific
// rejection even when the destination is also unreachable. Were the
// order reversed, the actor would see a "destination cannot be resolved"
// error and the LeaveHuddleFirst contract would depend on the
// destination being valid.
func TestMoveActor_InHuddleRejectionPrecedesPathCheck(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.JoinHuddle("walker", "inn", "", now)); err != nil {
		t.Fatalf("JoinHuddle: %v", err)
	}

	// An untraversable destination — fails path resolution — issued by an
	// in-huddle actor without LeaveHuddleFirst.
	_, err := w.Send(sim.MoveActor("walker",
		sim.NewPositionDestination(sim.Position{X: -5, Y: -5}), false, now))
	if err == nil {
		t.Fatal("expected MoveActor to be rejected")
	}
	if !strings.Contains(err.Error(), "LeaveHuddleFirst") {
		t.Errorf("expected the active-huddle rejection (mentioning LeaveHuddleFirst), got: %v", err)
	}
}

// TestMoveActor_LeaveHuddleFirst covers the LeaveHuddleFirst path: the
// actor leaves its huddle (emitting HuddleLeft) before the MoveIntent is
// stamped, and the result reports the huddle that was left.
func TestMoveActor_LeaveHuddleFirst(t *testing.T) {
	w, cancel, rec := buildMoveTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.JoinHuddle("walker", "inn", "", now)); err != nil {
		t.Fatalf("JoinHuddle: %v", err)
	}
	huddleBefore := huddleIDOf(t, w, "walker")

	res, err := w.Send(sim.MoveActor("walker",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 3, Y: sim.PadY + 3}), true, now))
	if err != nil {
		t.Fatalf("MoveActor with LeaveHuddleFirst rejected: %v", err)
	}
	r := res.(sim.MoveActorResult)
	if r.LeftHuddleID != huddleBefore {
		t.Errorf("LeftHuddleID = %q, want %q", r.LeftHuddleID, huddleBefore)
	}
	if got := huddleIDOf(t, w, "walker"); got != "" {
		t.Errorf("walker still in huddle %q after LeaveHuddleFirst move", got)
	}
	if mi := moveIntentOf(t, w, "walker"); mi == nil {
		t.Error("walker has no MoveIntent after accepted LeaveHuddleFirst move")
	}
	left := rec.countEvents(func(e sim.Event) bool {
		hl, ok := e.(*sim.HuddleLeft)
		return ok && hl.ActorID == "walker"
	})
	if left != 1 {
		t.Errorf("HuddleLeft{walker} count = %d, want 1", left)
	}
}

// TestMoveActor_Supersede covers re-issuing MoveActor against an actor
// that already has an in-flight intent: the new attempt ID increments
// monotonically, the result reports the superseded attempt, and NO
// ActorMoveStopped event fires for the dead attempt.
func TestMoveActor_Supersede(t *testing.T) {
	w, cancel, rec := buildMoveTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	first, err := w.Send(sim.MoveActor("walker", sim.NewStructureVisitDestination("inn"), false, now))
	if err != nil {
		t.Fatalf("first MoveActor: %v", err)
	}
	if first.(sim.MoveActorResult).MovementAttemptID != 1 {
		t.Fatalf("first attempt ID = %d, want 1", first.(sim.MoveActorResult).MovementAttemptID)
	}

	second, err := w.Send(sim.MoveActor("walker",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 4, Y: sim.PadY + 4}), false, now))
	if err != nil {
		t.Fatalf("second MoveActor: %v", err)
	}
	r := second.(sim.MoveActorResult)
	if r.MovementAttemptID != 2 {
		t.Errorf("second attempt ID = %d, want 2 (monotonic)", r.MovementAttemptID)
	}
	if r.SupersededAttemptID != 1 {
		t.Errorf("SupersededAttemptID = %d, want 1", r.SupersededAttemptID)
	}

	mi := moveIntentOf(t, w, "walker")
	if mi == nil || mi.AttemptID != 2 || mi.Destination.Kind != sim.MoveDestinationPosition {
		t.Errorf("MoveIntent after supersede = %+v, want attempt 2 / position", mi)
	}

	stopped := rec.countEvents(func(e sim.Event) bool {
		_, ok := e.(*sim.ActorMoveStopped)
		return ok
	})
	if stopped != 0 {
		t.Errorf("ActorMoveStopped count = %d, want 0 (supersede dies silently)", stopped)
	}
}

// buildMembershipTestWorld seeds a running world with one owner-only
// structure ("cottage") and one actor per membership basis, so the
// owner-only entry gate can be exercised through every leg:
//
//   - "homeowner" — owner (OwnerActorID), but NOT a resident, so the
//     owner leg is tested in isolation.
//   - "spouse"    — resident (HomeStructureID), not the owner.
//   - "servant"   — staff (WorkStructureID).
//   - "boarder"   — lodger (active RoomAccess for cottage's bedroom).
//   - "stranger"  — no membership of any kind.
//
// The cottage asset carries a door offset so structureEntryTile resolves.
func buildMembershipTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"cottage-asset": {ID: "cottage-asset", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(2)},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"cottage": {
			ID: "cottage", AssetID: "cottage-asset", Pos: sim.WorldPos{X: 320, Y: 320},
			EntryPolicy: sim.EntryPolicyOwner, OwnerActorID: "homeowner",
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"cottage": {
			ID: "cottage", DisplayName: "Cottage",
			Rooms: []*sim.Room{
				{ID: 1, StructureID: "cottage", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"homeowner": {ID: "homeowner", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
		"spouse":    {ID: "spouse", Pos: sim.TilePos{X: sim.PadX + 1, Y: sim.PadY}, HomeStructureID: "cottage"},
		"servant":   {ID: "servant", Pos: sim.TilePos{X: sim.PadX + 2, Y: sim.PadY}, WorkStructureID: "cottage"},
		"boarder": {
			ID: "boarder", Pos: sim.TilePos{X: sim.PadX + 3, Y: sim.PadY},
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				// A real lodger grant always carries a future ExpiresAt
				// (AssignBedroomForLodger sets it = lodger_until); the lodger
				// leg gates on IsActiveLedgerGrant, which fails closed on a
				// nil/past expiry.
				{RoomID: 1, Source: sim.AccessSourceLedger}: {
					RoomID: 1, Source: sim.AccessSourceLedger, Active: true,
					// Fixed far-future instant — always after the test's
					// time.Now() `now`, with no wall-clock dependence.
					ExpiresAt: func() *time.Time { t := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC); return &t }(),
				},
			},
		},
		"stranger": {ID: "stranger", Pos: sim.TilePos{X: sim.PadX + 4, Y: sim.PadY}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// TestStructureMembershipAllows covers the membership predicate directly:
// each of the four legs admits, a non-member is rejected, and an expired
// lodger grant does not count.
func TestStructureMembershipAllows(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	allows := func(actorID sim.ActorID, when time.Time) bool {
		res, err := w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				return sim.StructureMembershipAllows(world, world.Actors[actorID], "cottage", when), nil
			},
		})
		if err != nil {
			t.Fatalf("StructureMembershipAllows(%s): %v", actorID, err)
		}
		return res.(bool)
	}

	for _, actorID := range []sim.ActorID{"homeowner", "spouse", "servant", "boarder"} {
		if !allows(actorID, now) {
			t.Errorf("%s should be a member of cottage", actorID)
		}
	}
	if allows("stranger", now) {
		t.Error("stranger should not be a member of cottage")
	}

	// Expire the boarder's RoomAccess and confirm the lodger leg drops.
	expireRes, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			ra := world.Actors["boarder"].RoomAccess[sim.RoomAccessKey{RoomID: 1, Source: sim.AccessSourceLedger}]
			past := now.Add(-time.Hour)
			ra.ExpiresAt = &past
			return nil, nil
		},
	})
	_ = expireRes
	if err != nil {
		t.Fatalf("expire RoomAccess: %v", err)
	}
	if allows("boarder", now) {
		t.Error("boarder with an expired RoomAccess grant should not be a member")
	}
}

// TestMoveActor_OwnerOnlyEntry covers the gate end-to-end through
// MoveActor: every membership leg is admitted into an owner-only
// structure, and a non-member is rejected.
func TestMoveActor_OwnerOnlyEntry(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	for _, actorID := range []sim.ActorID{"homeowner", "spouse", "servant", "boarder"} {
		if _, err := w.Send(sim.MoveActor(actorID, sim.NewStructureEnterDestination("cottage"), false, now)); err != nil {
			t.Errorf("%s should be admitted into owner-only cottage: %v", actorID, err)
		}
	}
	if _, err := w.Send(sim.MoveActor("stranger", sim.NewStructureEnterDestination("cottage"), false, now)); err == nil {
		t.Error("stranger should be rejected from owner-only cottage")
	}

	// StructureVisit ignores membership — a stranger can still walk to a
	// visitor slot outside an owner-only structure.
	if _, err := w.Send(sim.MoveActor("stranger", sim.NewStructureVisitDestination("cottage"), false, now)); err != nil {
		t.Errorf("StructureVisit to an owner-only structure should be allowed for a non-member: %v", err)
	}
}

// buildOutdoorTestWorld seeds a running world for StartOutdoorHuddle
// tests: all-grass terrain, three actors clustered outdoors near tile
// (PadX+10, PadY+10), one actor far away, and one actor seeded inside a
// structure (fails the outdoor area-bound check).
func buildOutdoorTestWorld(t *testing.T) (*sim.World, context.CancelFunc, *eventRec) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"hut": {ID: "hut", DisplayName: "Hut"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"ann": {ID: "ann", Pos: sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10}},
		"ben": {ID: "ben", Pos: sim.TilePos{X: sim.PadX + 11, Y: sim.PadY + 10}},
		"cal": {ID: "cal", Pos: sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 11}},
		"far": {ID: "far", Pos: sim.TilePos{X: sim.PadX + 50, Y: sim.PadY + 50}},
		"indoorsy": {
			ID: "indoorsy", Pos: sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			InsideStructureID: "hut",
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	rec := &eventRec{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel, rec
}

// worldCounts reads the live Scene / Huddle map sizes inside a command —
// used to assert StartOutdoorHuddle's all-or-nothing atomicity.
func worldCounts(t *testing.T, w *sim.World) (scenes, huddles int) {
	t.Helper()
	type counts struct{ s, h int }
	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return counts{s: len(world.Scenes), h: len(world.Huddles)}, nil
		},
	})
	if err != nil {
		t.Fatalf("worldCounts: %v", err)
	}
	c := res.(counts)
	return c.s, c.h
}

// outdoorAnchor is the encounter anchor used by the StartOutdoorHuddle
// tests — ann sits exactly on it; ben and cal are one tile away.
var outdoorAnchor = sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}

// TestStartOutdoorHuddle_HappyPath covers the atomic create-and-join: two
// outdoor actors are minted into one area-bound scene + huddle, both get
// CurrentHuddleID set, and the expected SceneMinted / HuddleJoined /
// ActorMet events fire.
func TestStartOutdoorHuddle_HappyPath(t *testing.T) {
	w, cancel, rec := buildOutdoorTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res, err := w.Send(sim.StartOutdoorHuddle([]sim.ActorID{"ann", "ben"}, outdoorAnchor, 3, nil, now))
	if err != nil {
		t.Fatalf("StartOutdoorHuddle: %v", err)
	}
	r := res.(sim.StartOutdoorHuddleResult)
	if r.SceneID == "" || r.HuddleID == "" {
		t.Fatalf("result missing IDs: %+v", r)
	}

	annHuddle := huddleIDOf(t, w, "ann")
	benHuddle := huddleIDOf(t, w, "ben")
	if annHuddle != r.HuddleID || benHuddle != r.HuddleID {
		t.Errorf("participants not in the new huddle: ann=%q ben=%q want %q", annHuddle, benHuddle, r.HuddleID)
	}

	// Scene must be area-bound and observe exactly the new huddle.
	type sceneSt struct {
		kind        sim.SceneBoundKind
		huddleCount int
	}
	sres, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			s := world.Scenes[r.SceneID]
			if s == nil {
				return sceneSt{}, nil
			}
			return sceneSt{kind: s.Bound.Kind, huddleCount: len(s.Huddles)}, nil
		},
	})
	ss := sres.(sceneSt)
	if ss.kind != sim.SceneBoundArea {
		t.Errorf("scene bound kind = %q, want area", ss.kind)
	}
	if ss.huddleCount != 1 {
		t.Errorf("scene observes %d huddles, want 1", ss.huddleCount)
	}

	joined := rec.countEvents(func(e sim.Event) bool {
		hj, ok := e.(*sim.HuddleJoined)
		return ok && hj.HuddleID == r.HuddleID
	})
	if joined != 2 {
		t.Errorf("HuddleJoined count = %d, want 2", joined)
	}
	met := rec.countEvents(func(e sim.Event) bool {
		_, ok := e.(*sim.ActorMet)
		return ok
	})
	if met != 1 {
		t.Errorf("ActorMet count = %d, want 1 (one pair)", met)
	}
	minted := rec.countEvents(func(e sim.Event) bool {
		sm, ok := e.(*sim.SceneMinted)
		return ok && sm.SceneID == r.SceneID
	})
	if minted != 1 {
		t.Errorf("SceneMinted count = %d, want 1", minted)
	}
}

// TestStartOutdoorHuddle_Rejections covers every validation rejection,
// and asserts the all-or-nothing guarantee: a rejected command mints no
// scene and no huddle.
func TestStartOutdoorHuddle_Rejections(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name         string
		participants []sim.ActorID
		setup        func(t *testing.T, w *sim.World)
	}{
		{"empty", nil, nil},
		{"duplicate", []sim.ActorID{"ann", "ann"}, nil},
		{"actor not found", []sim.ActorID{"ann", "ghost"}, nil},
		{"actor indoors", []sim.ActorID{"ann", "indoorsy"}, nil},
		{"actor outside radius", []sim.ActorID{"ann", "far"}, nil},
		{
			"actor already in a huddle",
			[]sim.ActorID{"ann", "ben"},
			func(t *testing.T, w *sim.World) {
				if _, err := w.Send(sim.JoinHuddle("ben", "hut", "", now)); err != nil {
					t.Fatalf("pre-join ben: %v", err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, cancel, _ := buildOutdoorTestWorld(t)
			defer cancel()
			if c.setup != nil {
				c.setup(t, w)
			}
			scenesBefore, huddlesBefore := worldCounts(t, w)

			if _, err := w.Send(sim.StartOutdoorHuddle(c.participants, outdoorAnchor, 3, nil, now)); err == nil {
				t.Fatal("expected StartOutdoorHuddle to be rejected")
			}
			scenesAfter, huddlesAfter := worldCounts(t, w)
			if scenesAfter != scenesBefore {
				t.Errorf("rejected command changed scene count: %d -> %d", scenesBefore, scenesAfter)
			}
			if huddlesAfter != huddlesBefore {
				t.Errorf("rejected command changed huddle count: %d -> %d", huddlesBefore, huddlesAfter)
			}
		})
	}
}

// TestStartOutdoorHuddle_RadiusDefault covers radius <= 0 falling back to
// the world's DefaultOutdoorSceneRadius.
func TestStartOutdoorHuddle_RadiusDefault(t *testing.T) {
	w, cancel, _ := buildOutdoorTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res, err := w.Send(sim.StartOutdoorHuddle([]sim.ActorID{"ann", "ben"}, outdoorAnchor, 0, nil, now))
	if err != nil {
		t.Fatalf("StartOutdoorHuddle: %v", err)
	}
	sceneID := res.(sim.StartOutdoorHuddleResult).SceneID

	rres, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			s := world.Scenes[sceneID]
			if s == nil || s.Bound.Radius == nil {
				return -1, nil
			}
			return *s.Bound.Radius, nil
		},
	})
	if got := rres.(int); got != sim.DefaultOutdoorSceneRadiusValue {
		t.Errorf("scene bound radius = %d, want default %d", got, sim.DefaultOutdoorSceneRadiusValue)
	}
}

// TestStartOutdoorHuddle_MoverLeavesHuddleAndKeepsWalking covers the
// post-ZBBS-HOME-340 invariant: an actor mid-walk that is somehow in an
// outdoor huddle is NOT paused (the old bilateral pause is gone) — on the
// very next tick the ticker leaves the huddle and advances it. The leave
// is unconditional (not dependent on the step crossing the scene bound),
// so this holds regardless of the huddle's radius.
func TestStartOutdoorHuddle_MoverLeavesHuddleAndKeepsWalking(t *testing.T) {
	w, cancel, _ := buildOutdoorTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// ann is walking somewhere when the encounter fires.
	if _, err := w.Send(sim.MoveActor("ann",
		sim.NewPositionDestination(sim.Position{X: sim.PadX + 10, Y: sim.PadY + 20}), false, now)); err != nil {
		t.Fatalf("MoveActor ann: %v", err)
	}
	if _, err := w.Send(sim.StartOutdoorHuddle([]sim.ActorID{"ann", "ben"}, outdoorAnchor, 3, nil, now)); err != nil {
		t.Fatalf("StartOutdoorHuddle: %v", err)
	}

	before, _ := actorSpatial(t, w, "ann")
	tickLoco(t, w, now)

	if huddleID := huddleIDOf(t, w, "ann"); huddleID != "" {
		t.Errorf("ann should have left the huddle on the first tick, still in %q", huddleID)
	}
	after, _ := actorSpatial(t, w, "ann")
	if after == before {
		t.Errorf("ann did not advance after leaving the huddle: still at %+v (pause should be gone)", before)
	}
}

// TestStartOutdoorHuddle_TeardownConcludesScene covers PR 4a's
// area-scene-1:1 teardown: when the last participant leaves the outdoor
// huddle, the orphaned area scene auto-concludes.
func TestStartOutdoorHuddle_TeardownConcludesScene(t *testing.T) {
	w, cancel, _ := buildOutdoorTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res, err := w.Send(sim.StartOutdoorHuddle([]sim.ActorID{"ann", "ben"}, outdoorAnchor, 3, nil, now))
	if err != nil {
		t.Fatalf("StartOutdoorHuddle: %v", err)
	}
	sceneID := res.(sim.StartOutdoorHuddleResult).SceneID

	if _, err := w.Send(sim.LeaveHuddle("ann", now)); err != nil {
		t.Fatalf("LeaveHuddle ann: %v", err)
	}
	sceneAlive := func() bool {
		out, _ := w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				_, ok := world.Scenes[sceneID]
				return ok, nil
			},
		})
		return out.(bool)
	}
	if !sceneAlive() {
		t.Fatal("scene concluded after only one of two participants left")
	}
	if _, err := w.Send(sim.LeaveHuddle("ben", now)); err != nil {
		t.Fatalf("LeaveHuddle ben: %v", err)
	}
	if sceneAlive() {
		t.Error("orphaned area scene was not auto-concluded after the last participant left")
	}
}
