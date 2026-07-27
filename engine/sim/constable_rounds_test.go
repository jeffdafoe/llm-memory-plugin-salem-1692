package sim

import (
	"testing"
	"time"
)

// constable_rounds_test.go — LLM-514. Unit coverage for the constable rounds
// interval decision (ConstableRoundsDue + per-carrier jitter) and the reusable
// enter-vs-loiter route-stop primitive (routeStopEntersStructure / RouteStopArrived
// / routeStopDestination). LLM-537 adds the dwell driver's defer-or-advance
// predicate (ConstableStopStillTalking) at the bottom.

// constableWorld builds a minimal world with a single constable actor settled at
// his post and on an all-day shift, plus the maps ConstableRoundsDue reads.
func constableWorld(a *Actor) *World {
	return &World{
		Actors:                map[ActorID]*Actor{a.ID: a},
		ActiveRoutes:          map[ActorID]*NPCRoute{},
		ConstableRoundsStamps: map[ActorID]time.Time{},
		Settings:              WorldSettings{Location: time.UTC},
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
		w.ConstableRoundsStamps[a.ID] = now
		if ConstableRoundsDue(w, a, interval, now) {
			t.Error("a stamp at now consumes the current beat — not due")
		}
	})

	t.Run("interval_elapsed_due", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		// Last rounds a full interval-plus ago: a fresh beat has passed.
		w.ConstableRoundsStamps[a.ID] = now.Add(-3 * time.Hour)
		if !ConstableRoundsDue(w, a, interval, now) {
			t.Error("a beat has elapsed since the stamp — should be due")
		}
	})
}

// TestConstableRoundsOffsetDesync proves the per-carrier phase offset keeps two
// constables from firing rounds on the same beat, AND that the PER-ACTOR stamp means
// neither suppresses the other: given identical last-rounds times, on carrier A's
// exact beat A is due and B is not; and stamping A leaves B's dueness untouched.
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
	// A's exact beat instant, and an (identical) per-actor stamp one ns behind it.
	instA := mostRecentRoundsInstant(base, interval, offA)
	now := instA
	stamp := instA.Add(-time.Nanosecond)

	a := constableAtPost("gideon")
	b := constableAtPost("silas")
	w := &World{
		Actors:                map[ActorID]*Actor{a.ID: a, b.ID: b},
		ActiveRoutes:          map[ActorID]*NPCRoute{},
		ConstableRoundsStamps: map[ActorID]time.Time{a.ID: stamp, b.ID: stamp},
		Settings:              WorldSettings{Location: time.UTC},
	}
	if !ConstableRoundsDue(w, a, interval, now) {
		t.Error("carrier A should be due on its own beat")
	}
	if ConstableRoundsDue(w, b, interval, now) {
		t.Error("carrier B should NOT be due on A's beat — the phase offset desyncs them")
	}
	// A fires and stamps itself. Because the stamp is per-actor, B's dueness must be
	// exactly what it was — A cannot suppress B.
	bDueBefore := ConstableRoundsDue(w, b, interval, now)
	StampConstableRounds(w, a.ID, now)
	if ConstableRoundsDue(w, b, interval, now) != bDueBefore {
		t.Error("stamping carrier A changed carrier B's dueness — per-actor stamps must not cross-suppress")
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
	actor := &Actor{ID: "gideon"}
	now := time.Now()

	if sid, enters := routeStopEntersStructure(w, actor, "tavern", now); !enters || sid != "tavern" {
		t.Errorf("door-backed open structure should enter: got (%q, %v), want (tavern, true)", sid, enters)
	}
	if _, enters := routeStopEntersStructure(w, actor, "farm", now); enters {
		t.Error("doorless structure should loiter, not enter")
	}
	if _, enters := routeStopEntersStructure(w, actor, "stall", now); enters {
		t.Error("bare non-structure prop should loiter, not enter")
	}
}

// TestRouteStopEntersStructure_EntryGate is the LLM-514 fix #8 coverage: the
// enter-vs-loiter rule reuses the live entry gate (moveToCanEnter), so the
// constable stands OUTSIDE a business he can't enter right now — a lodge locked for
// the night because its keeper is abed (John Ellis's tavern before he's up). An
// open lodge admits him.
func TestRouteStopEntersStructure_EntryGate(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	constable := &Actor{ID: "gideon"} // not a member of the tavern

	t.Run("open_lodge_enters", func(t *testing.T) {
		k := closeupKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, k)
		placeInside(w, "tavern", "john") // keeper present & awake → not locked
		if sid, enters := routeStopEntersStructure(w, constable, "tavern", now); !enters || sid != "tavern" {
			t.Errorf("open lodge should admit the constable: got (%q, %v)", sid, enters)
		}
	})

	t.Run("locked_lodge_loiters", func(t *testing.T) {
		k := closeupKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, k)
		abedInStaffRoom(w, k) // keeper abed → lodgeLocked → owner-only
		if _, enters := routeStopEntersStructure(w, constable, "tavern", now); enters {
			t.Error("a lodge locked for the night must NOT admit a non-member constable — he loiters at the door")
		}
	})
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

// TestClearSuspendedRoundIfOffShift covers the bound on the duty exemption a
// SUSPENDED round carries (LLM-531). While a round sits part-walked, shiftDuty
// leaves the constable alone so he can choose to pick it up rather than be marched
// back to his post the moment he finishes his drink. That exemption must not follow
// him into the night: once his watch is over the part-walked round is dropped, so
// he goes home like anyone else and tomorrow starts a fresh circuit.
func TestClearSuspendedRoundIfOffShift(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) // noon

	t.Run("on shift keeps the paused round waiting", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseSuspended}
		if ClearSuspendedRoundIfOffShift(w, a, now) {
			t.Error("cleared a paused round while still on shift — he loses the chance to resume")
		}
		if w.ActiveRoutes[a.ID] == nil {
			t.Error("round dropped while on shift")
		}
	})

	t.Run("off shift drops the paused round", func(t *testing.T) {
		a := constableAtPost("gideon")
		// A morning-only watch, so noon is off shift.
		a.ScheduleStartMin, a.ScheduleEndMin = intptr(0), intptr(600)
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseSuspended}
		if !ClearSuspendedRoundIfOffShift(w, a, now) {
			t.Fatal("did not drop a paused round after the watch ended")
		}
		if w.ActiveRoutes[a.ID] != nil {
			t.Error("route still present after the off-shift drop — the duty exemption would persist overnight")
		}
	})

	t.Run("an in-flight round is left to the route machinery", func(t *testing.T) {
		a := constableAtPost("gideon")
		a.ScheduleStartMin, a.ScheduleEndMin = intptr(0), intptr(600) // off shift at noon
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseActive}
		if ClearSuspendedRoundIfOffShift(w, a, now) {
			t.Error("swept an ACTIVE route — in-flight rounds clear through their own paths")
		}
	})
}

// TestClearSuspendedRoundIfOffShift_FiresWithoutARoundsDueEvent is the invariant
// code_review asked to make explicit: the off-shift sweep is what bounds the duty
// exemption a SUSPENDED round carries, so it must not depend on a rounds beat
// happening to land. runConstableRounds calls the sweep BEFORE ConstableRoundsDue
// and runs off RouteScheduleTick, an unconditional per-minute ticker
// (RouteScheduleTickerInterval = time.Minute) — so crossing shift end drops the
// round on the next minute even though no round is due anywhere near that moment.
//
// This pins the two halves together: at the crossing instant rounds are NOT due
// (the stamp is fresh), and the sweep still clears.
func TestClearSuspendedRoundIfOffShift_FiresWithoutARoundsDueEvent(t *testing.T) {
	const interval = 2 * time.Hour
	a := constableAtPost("gideon")
	a.ScheduleStartMin, a.ScheduleEndMin = intptr(0), intptr(600) // watch ends at 10:00
	w := constableWorld(a)

	justAfterShiftEnd := time.Date(2026, 6, 12, 10, 1, 0, 0, time.UTC)
	w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseSuspended}
	// A fresh stamp, so no rounds beat is due at this instant.
	StampConstableRounds(w, a.ID, justAfterShiftEnd.Add(-time.Minute))

	if ConstableRoundsDue(w, a, interval, justAfterShiftEnd) {
		t.Fatal("fixture invalid: a round is due, so this would not prove the sweep runs independently")
	}
	if !ClearSuspendedRoundIfOffShift(w, a, justAfterShiftEnd) {
		t.Fatal("part-walked round survived the end of the watch with no rounds beat to clear it — " +
			"the duty exemption would persist overnight and leave him standing where he stopped")
	}
	if w.ActiveRoutes[a.ID] != nil {
		t.Error("route still present after the sweep")
	}
}

// --- LLM-537: the dwell driver's defer-or-advance predicate ---
//
// These drive `now` and `quiet` as arguments instead of reading the wall clock, so
// the window edges are exact. The cascade-side tests
// (cascade/constable_rounds_test.go) cover the same decision end-to-end through the
// real route + dwell machinery; these pin the boundaries an integration test cannot
// hit deterministically.

// constableStopWorld builds the minimal world the predicate reads: one constable in
// one huddle, silent since `lastActivity`. mutate customizes the huddle first.
func constableStopWorld(lastActivity time.Time, mutate func(*Huddle)) (*World, *Actor) {
	h := &Huddle{
		ID:             "h1",
		Members:        map[ActorID]struct{}{"gideon": {}, "keeper": {}},
		StartedAt:      lastActivity.Add(-time.Hour),
		LastActivityAt: lastActivity,
	}
	if mutate != nil {
		mutate(h)
	}
	actor := &Actor{ID: "gideon", CurrentHuddleID: "h1"}
	w := &World{
		Actors:  map[ActorID]*Actor{"gideon": actor},
		Huddles: map[HuddleID]*Huddle{"h1": h},
	}
	return w, actor
}

// TestConstableStopStillTalkingQuietBoundary pins the exact edge of the quiet
// window. HuddleIsLive is inclusive (now.Sub(last) <= window), so silence of exactly
// one window still defers and a nanosecond more advances. Worth pinning: the whole
// defect was a predicate that never stopped deferring, and an off-by-one here is
// that bug in miniature.
func TestConstableStopStillTalkingQuietBoundary(t *testing.T) {
	const quiet = 90 * time.Second
	now := time.Date(2026, 7, 27, 13, 30, 0, 0, time.UTC)

	cases := []struct {
		name    string
		silence time.Duration
		want    bool
	}{
		{"one_second_inside", quiet - time.Second, true},
		{"exactly_at_window", quiet, true},
		{"one_nanosecond_past", quiet + time.Nanosecond, false},
		{"one_second_past", quiet + time.Second, false},
		{"long_gone", 5 * time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, actor := constableStopWorld(now.Add(-tc.silence), nil)
			if got := ConstableStopStillTalking(w, actor, now, quiet); got != tc.want {
				t.Errorf("silence %v against a %v window: still talking = %v, want %v", tc.silence, quiet, got, tc.want)
			}
		})
	}
}

// TestConstableStopStillTalkingPlayerArm pins the player grace period. It keys on a
// player's last UTTERANCE, not on PC membership, because a parked and silent player
// at a hub would otherwise hold the constable at that stop indefinitely. So this is
// a recent-speech grace period, NOT evidence that the player is at this moment
// reading or composing: a player quiet past huddlePCAttentionWindow does lose the
// arm, deliberately, and the constable walks on.
func TestConstableStopStillTalkingPlayerArm(t *testing.T) {
	const quiet = 90 * time.Second
	now := time.Date(2026, 7, 27, 13, 30, 0, 0, time.UTC)
	// Silent well past the NPC-sized quiet window, so only the player arm can defer.
	silentSince := now.Add(-10 * time.Minute)

	cases := []struct {
		name    string
		pcAgo   time.Duration
		want    bool
		because string
	}{
		{"just_spoke", time.Second, true, "a player is mid-conversation"},
		{"inside_grace", huddlePCAttentionWindow - time.Second, true, "still inside the player grace period"},
		{"at_grace_boundary", huddlePCAttentionWindow, false, "the grace period is exclusive (age < window)"},
		{"wandered_off", 10 * time.Minute, false, "a long-silent player must not park him at the stop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, actor := constableStopWorld(silentSince, func(h *Huddle) {
				h.LastPCUtteranceAt = now.Add(-tc.pcAgo)
			})
			if got := ConstableStopStillTalking(w, actor, now, quiet); got != tc.want {
				t.Errorf("player spoke %v ago: still talking = %v, want %v (%s)", tc.pcAgo, got, tc.want, tc.because)
			}
		})
	}
}

// TestConstableStopStillTalkingNotInAConversation covers every way the predicate
// must answer "nothing is holding him" — including the one a lifecycle test gets
// wrong in the other direction: a CONCLUDED huddle never defers, not even with a
// player utterance seconds old. Once a huddle has concluded there is nothing left
// to be dragged out of.
func TestConstableStopStillTalkingNotInAConversation(t *testing.T) {
	const quiet = 90 * time.Second
	now := time.Date(2026, 7, 27, 13, 30, 0, 0, time.UTC)
	concluded := now.Add(-time.Minute)

	t.Run("nil_actor", func(t *testing.T) {
		w, _ := constableStopWorld(now, nil)
		if ConstableStopStillTalking(w, nil, now, quiet) {
			t.Error("a nil actor is not in a conversation")
		}
	})

	t.Run("no_huddle_id", func(t *testing.T) {
		w, actor := constableStopWorld(now, nil)
		actor.CurrentHuddleID = ""
		if ConstableStopStillTalking(w, actor, now, quiet) {
			t.Error("an actor with no huddle is not in a conversation")
		}
	})

	t.Run("huddle_id_dangles", func(t *testing.T) {
		// A back-reference to a huddle no longer in the map (boot clear, conclusion
		// sweep). Must read "not talking" rather than panic.
		w, actor := constableStopWorld(now, nil)
		delete(w.Huddles, "h1")
		if ConstableStopStillTalking(w, actor, now, quiet) {
			t.Error("a dangling CurrentHuddleID is not a conversation")
		}
	})

	t.Run("concluded_though_recent", func(t *testing.T) {
		w, actor := constableStopWorld(now, func(h *Huddle) { h.ConcludedAt = &concluded })
		if ConstableStopStillTalking(w, actor, now, quiet) {
			t.Error("a concluded huddle must not defer, however recent its last word")
		}
	})

	t.Run("concluded_though_player_attended", func(t *testing.T) {
		w, actor := constableStopWorld(now, func(h *Huddle) {
			h.ConcludedAt = &concluded
			h.LastPCUtteranceAt = now.Add(-time.Second)
		})
		if ConstableStopStillTalking(w, actor, now, quiet) {
			t.Error("the player arm must not resurrect a concluded huddle")
		}
	})
}

// TestConstableStopStillTalkingUnstampedHuddle documents the deliberate fallback for
// a huddle carrying NEITHER stamp (a hand-built snapshot, or a creation site that
// forgot to stamp): HuddleIsLive reads it live, so the constable defers. That is the
// safe error — a false "live" costs one more dwell re-check, while a false "quiet"
// would walk him out of a real conversation. Asserted directly rather than left
// riding on an incidental integration-test fixture.
func TestConstableStopStillTalkingUnstampedHuddle(t *testing.T) {
	now := time.Date(2026, 7, 27, 13, 30, 0, 0, time.UTC)
	w, actor := constableStopWorld(now, func(h *Huddle) {
		h.StartedAt = time.Time{}
		h.LastActivityAt = time.Time{}
	})
	if !ConstableStopStillTalking(w, actor, now, 90*time.Second) {
		t.Error("an unstamped huddle must read as still talking — deferring is the safe direction")
	}
}

// TestEffectiveConstableRoundsQuiet pins the lazy-default posture: a non-positive
// stored value resolves to the default rather than acting as an off-switch. Zero
// must NOT mean "no quiet window" — that would advance him the instant a line
// landed, the mid-exchange yank the deferral exists to prevent. The feature's
// off-switch is ConstableRoundsInterval <= 0.
func TestEffectiveConstableRoundsQuiet(t *testing.T) {
	cases := []struct {
		name   string
		stored time.Duration
		want   time.Duration
	}{
		{"unset_defaults", 0, DefaultConstableRoundsQuiet},
		{"negative_defaults", -time.Second, DefaultConstableRoundsQuiet},
		{"set_wins", 3 * time.Minute, 3 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &World{Settings: WorldSettings{ConstableRoundsQuiet: tc.stored}}
			if got := EffectiveConstableRoundsQuiet(w); got != tc.want {
				t.Errorf("stored %v: effective = %v, want %v", tc.stored, got, tc.want)
			}
		})
	}
}
