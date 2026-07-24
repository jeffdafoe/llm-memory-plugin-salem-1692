package sim

import (
	"testing"
	"time"
)

// constable_rounds_test.go — LLM-514. Unit coverage for the constable rounds
// interval decision (ConstableRoundsDue + per-carrier jitter) and the reusable
// enter-vs-loiter route-stop primitive (routeStopEntersStructure / RouteStopArrived
// / routeStopDestination).

// constableWorld builds a minimal world with a single constable actor settled at
// his post and on an all-day shift, plus the maps ConstableRoundsDue reads.
func constableWorld(a *Actor) *World {
	return &World{
		Actors:              map[ActorID]*Actor{a.ID: a},
		ActiveRoutes:        map[ActorID]*NPCRoute{},
		RouteBoundaryStamps: map[string]time.Time{},
		Settings:            WorldSettings{Location: time.UTC},
	}
}

// constableAtPost builds a constable NPC settled at his post (InsideStructureID ==
// WorkStructureID) with an all-day shift window so on-shift is trivially true.
func constableAtPost(id ActorID) *Actor {
	return &Actor{
		ID:                id,
		Kind:              KindNPCStateful,
		WorkStructureID:   "meeting_house",
		InsideStructureID: "meeting_house",
		ScheduleStartMin:  intptr(0),
		ScheduleEndMin:    intptr(1440), // all-day: minuteInShiftWindow(0,1440,*) is always on shift
	}
}

func TestConstableRoundsDue_Gates(t *testing.T) {
	const interval = 2 * time.Hour
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

	// Baseline: at post, on shift, not routing, no stamp — the boot catch-up fires.
	t.Run("at_post_on_shift_no_stamp_due", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		if !ConstableRoundsDue(w, a, interval, now) {
			t.Error("expected due (at post, on shift, not routing, no stamp)")
		}
	})

	t.Run("interval_disabled_not_due", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		if ConstableRoundsDue(w, a, 0, now) {
			t.Error("interval <= 0 must disable rounds")
		}
	})

	t.Run("not_at_post_not_due", func(t *testing.T) {
		a := constableAtPost("gideon")
		a.InsideStructureID = "tavern" // out on the town, not settled at his post
		w := constableWorld(a)
		if ConstableRoundsDue(w, a, interval, now) {
			t.Error("rounds must start from the post, not mid-walk")
		}
	})

	t.Run("no_work_structure_not_due", func(t *testing.T) {
		a := constableAtPost("gideon")
		a.WorkStructureID = ""
		a.InsideStructureID = "" // matches empty work id, but no post means no rounds
		w := constableWorld(a)
		if ConstableRoundsDue(w, a, interval, now) {
			t.Error("a workless constable has no post to leave")
		}
	})

	t.Run("off_shift_not_due", func(t *testing.T) {
		a := constableAtPost("gideon")
		// A morning-only shift; noon is off shift.
		a.ScheduleStartMin = intptr(0)
		a.ScheduleEndMin = intptr(600) // 00:00–10:00
		w := constableWorld(a)
		if ConstableRoundsDue(w, a, interval, now) {
			t.Error("off-shift constable must not walk rounds")
		}
	})

	t.Run("already_routing_not_due", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseActive}
		if ConstableRoundsDue(w, a, interval, now) {
			t.Error("must not re-trigger while a route is already in flight")
		}
	})

	t.Run("interval_not_elapsed_not_due", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		// Just fired this instant: the current beat is consumed, so not due again.
		w.RouteBoundaryStamps[AttrConstable] = now
		if ConstableRoundsDue(w, a, interval, now) {
			t.Error("a stamp at now consumes the current beat — not due")
		}
	})

	t.Run("interval_elapsed_due", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		// Last rounds a full interval-plus ago: a fresh beat has passed.
		w.RouteBoundaryStamps[AttrConstable] = now.Add(-3 * time.Hour)
		if !ConstableRoundsDue(w, a, interval, now) {
			t.Error("a beat has elapsed since the stamp — should be due")
		}
	})
}

// TestConstableRoundsOffsetDesync proves the per-carrier phase offset keeps two
// constables from firing rounds on the same beat: on one carrier's exact beat,
// with the shared stamp one nanosecond behind it, that carrier is due and the
// other is not.
func TestConstableRoundsOffsetDesync(t *testing.T) {
	const interval = 2 * time.Hour
	offA := constableRoundsOffset("gideon", interval)
	offB := constableRoundsOffset("silas", interval)
	if offA < 0 || offA >= interval || offB < 0 || offB >= interval {
		t.Fatalf("offsets out of [0,interval): offA=%v offB=%v", offA, offB)
	}
	if offA == offB {
		t.Fatalf("offsets collided (offA==offB==%v) — pick different test ids", offA)
	}

	base := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	// A's exact beat instant, and a shared stamp one ns behind it.
	instA := mostRecentRoundsInstant(base, interval, offA)
	now := instA
	stamp := instA.Add(-time.Nanosecond)

	a := constableAtPost("gideon")
	b := constableAtPost("silas")
	w := &World{
		Actors:              map[ActorID]*Actor{a.ID: a, b.ID: b},
		ActiveRoutes:        map[ActorID]*NPCRoute{},
		RouteBoundaryStamps: map[string]time.Time{AttrConstable: stamp},
		Settings:            WorldSettings{Location: time.UTC},
	}
	if !ConstableRoundsDue(w, a, interval, now) {
		t.Error("carrier A should be due on its own beat")
	}
	if ConstableRoundsDue(w, b, interval, now) {
		t.Error("carrier B should NOT be due on A's beat — the phase offset desyncs them")
	}
}

// enterLoiterWorld builds a world with a door-backed business structure (enter), a
// doorless business structure (loiter), and a bare non-structure prop (loiter).
func enterLoiterWorld() *World {
	return &World{
		Structures: map[StructureID]*Structure{
			"tavern": {ID: "tavern"},
			"farm":   {ID: "farm"},
		},
		VillageObjects: map[VillageObjectID]*VillageObject{
			"tavern": {ID: "tavern", AssetID: "tavern_asset"},
			"farm":   {ID: "farm", AssetID: "farm_asset"},
			"stall":  {ID: "stall", AssetID: "stall_asset"}, // no Structure entry — a bare prop
		},
		Assets: map[AssetID]*Asset{
			"tavern_asset": {ID: "tavern_asset", DoorOffsetX: intptr(0), DoorOffsetY: intptr(2)},
			"farm_asset":   {ID: "farm_asset"}, // doorless structure (an open farm placement)
			"stall_asset":  {ID: "stall_asset"},
		},
	}
}

func TestRouteStopEntersStructure(t *testing.T) {
	w := enterLoiterWorld()

	if sid, enters := routeStopEntersStructure(w, "tavern"); !enters || sid != "tavern" {
		t.Errorf("door-backed structure should enter: got (%q, %v), want (tavern, true)", sid, enters)
	}
	if _, enters := routeStopEntersStructure(w, "farm"); enters {
		t.Error("doorless structure should loiter, not enter")
	}
	if _, enters := routeStopEntersStructure(w, "stall"); enters {
		t.Error("bare non-structure prop should loiter, not enter")
	}
}

func TestRouteStopArrivedAndDestination(t *testing.T) {
	enterStop := RouteStop{ObjectID: "tavern", WalkTo: Position{X: 5, Y: 5}, EnterStructureID: "tavern"}
	loiterStop := RouteStop{ObjectID: "stall", WalkTo: Position{X: 7, Y: 7}}

	// Arrival detection branches on stop kind.
	inside := &Actor{InsideStructureID: "tavern", Pos: TilePos{X: 99, Y: 99}}
	if !RouteStopArrived(inside, enterStop) {
		t.Error("enter stop: inside the structure counts as arrived regardless of Pos")
	}
	if RouteStopArrived(inside, loiterStop) {
		t.Error("loiter stop: Pos != WalkTo must not count as arrived")
	}
	atTile := &Actor{Pos: TilePos{X: 7, Y: 7}}
	if !RouteStopArrived(atTile, loiterStop) {
		t.Error("loiter stop: standing on WalkTo is arrived")
	}
	if RouteStopArrived(atTile, enterStop) {
		t.Error("enter stop: not inside the structure is not arrived")
	}

	// Dispatch destination branches on stop kind.
	ed := routeStopDestination(enterStop)
	if ed.Kind != MoveDestinationStructureEnter || ed.StructureID == nil || *ed.StructureID != "tavern" {
		t.Errorf("enter stop destination = %+v, want StructureEnter(tavern)", ed)
	}
	ld := routeStopDestination(loiterStop)
	if ld.Kind != MoveDestinationPosition || ld.Position == nil || *ld.Position != (Position{X: 7, Y: 7}) {
		t.Errorf("loiter stop destination = %+v, want Position(7,7)", ld)
	}
}
