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

// TestBeatNeedsAWake covers the gate on the beat wake (LLM-549). A beat dispatches
// no walk, so nothing else starts a round: on-shift AT his post he gets no shift
// duty (that switch has no at-work arm) and no idle backstop (that one only covers
// actors outdoors). This wake is the only thing that gets him going, and it must be
// a nudge rather than a nag — silent for a man already walking, and silent for a man
// mid-conversation.
func TestBeatNeedsAWake(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

	beatWorld := func(mutate func(*World, *Actor)) (*World, *Actor) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{
			NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseBeat,
			Stops:   []RouteStop{{ObjectID: "store"}, {ObjectID: "tavern"}},
			Visited: []bool{false, false},
		}
		w.Huddles = map[HuddleID]*Huddle{}
		if mutate != nil {
			mutate(w, a)
		}
		return w, a
	}

	// The live case: standing at his post with a round owed and nothing else to
	// wake him. Twelve minutes of this is what LLM-549 was filed on.
	t.Run("standing still with a round owed", func(t *testing.T) {
		w, a := beatWorld(nil)
		if !BeatNeedsAWake(w, a, now) {
			t.Error("no wake for a carrier stood still with a round owed — nothing else will start it")
		}
	})

	// He is already on his way. Waking him mid-leg is the nag this design exists to
	// avoid, and his arrival stamps a decision warrant anyway.
	t.Run("already walking", func(t *testing.T) {
		w, a := beatWorld(func(_ *World, a *Actor) {
			a.MoveIntent = &MoveIntent{}
		})
		if BeatNeedsAWake(w, a, now) {
			t.Error("woke a carrier who is already walking — that is a nag, not a nudge")
		}
	})

	// Interrupting a live conversation to say "you have rounds to walk" is exactly
	// the intrusion the dwell removal was meant to end.
	t.Run("mid-conversation", func(t *testing.T) {
		w, a := beatWorld(func(w *World, a *Actor) {
			a.CurrentHuddleID = "h1"
			w.Huddles["h1"] = &Huddle{
				ID:             "h1",
				Members:        map[ActorID]struct{}{a.ID: {}},
				StartedAt:      now.Add(-10 * time.Minute),
				LastActivityAt: now.Add(-30 * time.Second),
			}
		})
		if BeatNeedsAWake(w, a, now) {
			t.Error("woke a carrier mid-conversation")
		}
	})

	// LIVENESS, not lifecycle. A huddle stays open for the full silence timeout
	// after its last word, so a lifecycle test would gag the wake for the rest of
	// the afternoon over a conversation that ended before noon.
	t.Run("conversation long over but the huddle is still open", func(t *testing.T) {
		w, a := beatWorld(func(w *World, a *Actor) {
			a.CurrentHuddleID = "h1"
			w.Huddles["h1"] = &Huddle{
				ID:             "h1",
				Members:        map[ActorID]struct{}{a.ID: {}},
				StartedAt:      now.Add(-2 * time.Hour),
				LastActivityAt: now.Add(-90 * time.Minute),
			}
		})
		if !BeatNeedsAWake(w, a, now) {
			t.Error("an unconcluded but long-silent huddle suppressed the wake — that is the " +
				"lifecycle test, and it would hold him for the huddle's full 2h life")
		}
	})

	// A dispatched route walks its own carrier; it needs no nudge and must not get one.
	t.Run("a dispatched route is not nudged", func(t *testing.T) {
		w, a := beatWorld(func(w *World, a *Actor) {
			w.ActiveRoutes[a.ID].Phase = RoutePhaseActive
			w.ActiveRoutes[a.ID].Label = AttrTownCrier
		})
		if BeatNeedsAWake(w, a, now) {
			t.Error("nudged a dispatched route's carrier — the engine is already walking him")
		}
	})

	t.Run("no route at all owes nothing", func(t *testing.T) {
		w, a := beatWorld(func(w *World, a *Actor) {
			delete(w.ActiveRoutes, a.ID)
		})
		if BeatNeedsAWake(w, a, now) {
			t.Error("nudged a carrier with no round owed")
		}
	})
}

// TestBeatWakeIsAmbient pins the pacing knob (LLM-549). The repeat rate is NOT a
// bespoke interval and NOT the 4h rounds interval — it is the LLM-233 stale-wake
// ledger, which only paces AMBIENT kinds. Drop the kind out of that set and the
// wake fires at the driver's full once-a-minute cadence forever, against a man
// standing still, which is a real LLM turn every minute.
func TestBeatWakeIsAmbient(t *testing.T) {
	if !isAmbientWarrantKind(WarrantKindConstableRounds) {
		t.Error("the beat wake is not ambient — the stale-wake ledger will not pace it, " +
			"and the once-a-minute driver would then wake a stationary carrier every minute")
	}
	// The reason is condition-driven with nothing to discriminate: at most one round
	// is owed at a time, so a second stamp under the same conditions IS the same wake.
	if got := (ConstableRoundsWarrantReason{}).DedupDiscriminator(); got != 0 {
		t.Errorf("DedupDiscriminator = %d, want 0", got)
	}
	if got := (ConstableRoundsWarrantReason{}).Kind(); got != WarrantKindConstableRounds {
		t.Errorf("Kind = %q, want %q", got, WarrantKindConstableRounds)
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

	t.Run("dispatched_route_blocks_a_fresh_one", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrTownCrier, Phase: RoutePhaseActive}
		if ConstableRoundsDue(w, a, interval, now) {
			t.Error("must not re-trigger while a walk is in flight for an existing route")
		}
	})

	// A beat does NOT block, and that is deliberate (LLM-548): the next interval
	// supersedes a part-walked round outright, which is what bounds how long one can
	// sit part-walked to a single interval. He starts a fresh circuit rather than
	// carrying yesterday's around. In practice it cannot supersede him mid-round,
	// because being due also requires him settled back at his post — which a man
	// still walking his beat is not.
	t.Run("a_part_walked_beat_is_superseded_not_blocked", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseBeat}
		if !ConstableRoundsDue(w, a, interval, now) {
			t.Error("a part-walked beat blocked the next round — it would outlive its interval")
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

// TestClearBeatRouteIfOffShift covers the bound on the duty exemption a beat
// carries (LLM-531). While a round is owed, buildDutySteer leaves the constable
// alone so he can choose where to go next rather than be marched back to his post
// the moment he finishes his drink. That exemption must not follow him into the
// night: once his watch is over the part-walked round is dropped, so he goes home
// like anyone else and tomorrow starts a fresh circuit.
func TestClearBeatRouteIfOffShift(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) // noon

	t.Run("on shift keeps the part-walked round", func(t *testing.T) {
		a := constableAtPost("gideon")
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseBeat}
		if ClearBeatRouteIfOffShift(w, a, now) {
			t.Error("cleared a round while still on shift — he loses the chance to finish it")
		}
		if w.ActiveRoutes[a.ID] == nil {
			t.Error("round dropped while on shift")
		}
	})

	t.Run("off shift drops the part-walked round", func(t *testing.T) {
		a := constableAtPost("gideon")
		// A morning-only watch, so noon is off shift.
		a.ScheduleStartMin, a.ScheduleEndMin = intptr(0), intptr(600)
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseBeat}
		if !ClearBeatRouteIfOffShift(w, a, now) {
			t.Fatal("did not drop a part-walked round after the watch ended")
		}
		if w.ActiveRoutes[a.ID] != nil {
			t.Error("route still present after the off-shift drop — the duty exemption would persist overnight")
		}
	})

	// The sweep is scoped to beats. A decorative carrier's route is dispatched
	// machinery with its own completion and abandon paths; sweeping it out from
	// under a walk in flight would strand the actor mid-leg with no arrival handler
	// left to tidy up.
	t.Run("a decorative route is left to the route machinery", func(t *testing.T) {
		a := constableAtPost("grace")
		a.ScheduleStartMin, a.ScheduleEndMin = intptr(0), intptr(600) // off shift at noon
		w := constableWorld(a)
		w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrTownCrier, Phase: RoutePhaseActive}
		if ClearBeatRouteIfOffShift(w, a, now) {
			t.Error("swept a dispatched route — those clear through their own paths")
		}
		if w.ActiveRoutes[a.ID] == nil {
			t.Error("decorative route dropped by the beat sweep")
		}
	})
}

// TestClearBeatRouteIfOffShift_FiresWithoutARoundsDueEvent is the invariant
// code_review asked to make explicit: the off-shift sweep is what bounds the duty
// exemption a SUSPENDED round carries, so it must not depend on a rounds beat
// happening to land. runConstableRounds calls the sweep BEFORE ConstableRoundsDue
// and runs off RouteScheduleTick, an unconditional per-minute ticker
// (RouteScheduleTickerInterval = time.Minute) — so crossing shift end drops the
// round on the next minute even though no round is due anywhere near that moment.
//
// This pins the two halves together: at the crossing instant rounds are NOT due
// (the stamp is fresh), and the sweep still clears.
func TestClearBeatRouteIfOffShift_FiresWithoutARoundsDueEvent(t *testing.T) {
	const interval = 2 * time.Hour
	a := constableAtPost("gideon")
	a.ScheduleStartMin, a.ScheduleEndMin = intptr(0), intptr(600) // watch ends at 10:00
	w := constableWorld(a)

	justAfterShiftEnd := time.Date(2026, 6, 12, 10, 1, 0, 0, time.UTC)
	w.ActiveRoutes[a.ID] = &NPCRoute{NPCID: a.ID, Label: AttrConstable, Phase: RoutePhaseBeat}
	// A fresh stamp, so no rounds beat is due at this instant.
	StampConstableRounds(w, a.ID, justAfterShiftEnd.Add(-time.Minute))

	if ConstableRoundsDue(w, a, interval, justAfterShiftEnd) {
		t.Fatal("fixture invalid: a round is due, so this would not prove the sweep runs independently")
	}
	if !ClearBeatRouteIfOffShift(w, a, justAfterShiftEnd) {
		t.Fatal("part-walked round survived the end of the watch with no rounds beat to clear it — " +
			"the duty exemption would persist overnight and leave him standing where he stopped")
	}
	if w.ActiveRoutes[a.ID] != nil {
		t.Error("route still present after the sweep")
	}
}
