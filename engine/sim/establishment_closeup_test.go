package sim

import (
	"testing"
	"time"
)

// establishment_closeup_test.go — LLM-129. The close-up decision logic:
// who counts as a non-tenant, whether the house is still attended, the closing
// announcement, and the eviction-time re-open guard. The actual walk-out
// (MoveActor StructureVisit from inside the structure) is exercised end-to-end
// against the walkable world in establishment_closeup_integration_test.go.
//
// Reuses keeperTavernWorld / liveInKeeper from npc_keeper_sleep_test.go (same
// package): a tavern with a common floor (room 2), a private bedroom (room 3),
// and a staff room (room 1).

// placeInside puts the actors inside structureID — both their InsideStructureID
// and the actorsByStructure secondary index the close-up helpers scan. The bare
// keeperTavernWorld leaves the index empty (its sleep-room tests don't read it).
func placeInside(w *World, structureID StructureID, ids ...ActorID) {
	if w.actorsByStructure == nil {
		w.actorsByStructure = map[StructureID]map[ActorID]struct{}{}
	}
	m := w.actorsByStructure[structureID]
	if m == nil {
		m = map[ActorID]struct{}{}
		w.actorsByStructure[structureID] = m
	}
	for _, id := range ids {
		m[id] = struct{}{}
		if a := w.Actors[id]; a != nil {
			a.InsideStructureID = structureID
		}
	}
}

// lodgerActor is a boarder holding an active ledger grant on the tavern's
// private room 3 — a tenant who stays at close (npc_keeper_sleep_test.go uses
// the same shape).
func lodgerActor(id ActorID, now time.Time) *Actor {
	future := now.Add(72 * time.Hour)
	return &Actor{
		ID:                id,
		Kind:              KindNPCStateful,
		InsideStructureID: "tavern",
		WorkStructureID:   "smithy", // works elsewhere — a boarder, not the keeper
		RoomAccess: map[RoomAccessKey]*RoomAccess{
			{RoomID: 3, Source: AccessSourceLedger}: {RoomID: 3, Source: AccessSourceLedger, Active: true, ExpiresAt: &future},
		},
	}
}

// closeupKeeper is a live-in tavernkeeper that the close-up recognizes as the
// establishment's keeper: liveInKeeper plus the BusinessownerState marker
// (actorIsEstablishmentKeeper requires it, matching the cascade-wide keeper
// predicate). The flavor value is irrelevant to the close-up — only non-nil
// matters.
func closeupKeeper(id ActorID) *Actor {
	k := liveInKeeper(id)
	k.BusinessownerState = &BusinessownerState{Flavor: "reserved"}
	return k
}

// TestNonTenantOccupants: of everyone inside the tavern, only the agent NPC and
// the PC with no membership are turned out. The keeper (staff), a lodger (active
// private grant), and a resident (HomeStructureID == here) all stay; a
// decorative is scenery and is never a body to evict.
func TestNonTenantOccupants(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)

	keeper := closeupKeeper("john") // staff: WorkStructureID == tavern
	lodger := lodgerActor("ezekiel", now)
	resident := &Actor{ID: "child", Kind: KindNPCStateful, HomeStructureID: "tavern", InsideStructureID: "tavern"}
	customer := &Actor{ID: "buyer", Kind: KindNPCStateful, InsideStructureID: "tavern"} // no membership
	player := &Actor{ID: "wendy", Kind: KindPC, InsideStructureID: "tavern"}            // no membership
	deco := &Actor{ID: "cat", Kind: KindDecorative, InsideStructureID: "tavern"}        // scenery

	w := keeperTavernWorld(true, keeper, lodger, resident, customer, player, deco)
	placeInside(w, "tavern", "john", "ezekiel", "child", "buyer", "wendy", "cat")

	npcIDs, pcIDs := nonTenantOccupants(w, "tavern", now)

	if len(npcIDs) != 1 || npcIDs[0] != "buyer" {
		t.Errorf("npcIDs = %v, want [buyer] (only the unaffiliated agent NPC)", npcIDs)
	}
	if len(pcIDs) != 1 || pcIDs[0] != "wendy" {
		t.Errorf("pcIDs = %v, want [wendy] (the unaffiliated player)", pcIDs)
	}
}

// TestNonTenantOccupants_SortedDeterministically: the actorsByStructure index is
// a map, so the returned slices must be sorted by ActorID for a stable announce
// recipient set and eviction order.
func TestNonTenantOccupants_SortedDeterministically(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	c1 := &Actor{ID: "zelda", Kind: KindNPCStateful, InsideStructureID: "tavern"}
	c2 := &Actor{ID: "amos", Kind: KindNPCStateful, InsideStructureID: "tavern"}
	c3 := &Actor{ID: "mary", Kind: KindNPCShared, InsideStructureID: "tavern"}
	w := keeperTavernWorld(true, c1, c2, c3)
	placeInside(w, "tavern", "zelda", "amos", "mary")

	npcIDs, _ := nonTenantOccupants(w, "tavern", now)
	want := []ActorID{"amos", "mary", "zelda"}
	if len(npcIDs) != len(want) {
		t.Fatalf("npcIDs = %v, want %v", npcIDs, want)
	}
	for i := range want {
		if npcIDs[i] != want[i] {
			t.Fatalf("npcIDs = %v, want %v (ActorID-sorted)", npcIDs, want)
		}
	}
}

// TestEstablishmentHasAwakeKeeperPresent: a worker inside and awake means the
// house is still attended; a resting (asleep) keeper does not, the exclude id
// is skipped, and a worker of a different structure never counts.
func TestEstablishmentHasAwakeKeeperPresent(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)

	t.Run("awake keeper inside -> attended", func(t *testing.T) {
		k := closeupKeeper("john")
		w := keeperTavernWorld(true, k)
		placeInside(w, "tavern", "john")
		if !establishmentHasAwakeKeeperPresent(w, "tavern", "", now) {
			t.Error("want attended — an awake keeper is on the floor")
		}
	})

	t.Run("resting keeper -> not attended", func(t *testing.T) {
		k := closeupKeeper("john")
		future := now.Add(8 * time.Hour)
		k.SleepingUntil = &future
		w := keeperTavernWorld(true, k)
		placeInside(w, "tavern", "john")
		if establishmentHasAwakeKeeperPresent(w, "tavern", "", now) {
			t.Error("want unattended — the only keeper is asleep")
		}
	})

	t.Run("excluded keeper is skipped", func(t *testing.T) {
		k := closeupKeeper("john") // awake, but the one bedding down
		w := keeperTavernWorld(true, k)
		placeInside(w, "tavern", "john")
		if establishmentHasAwakeKeeperPresent(w, "tavern", "john", now) {
			t.Error("want unattended — the sole keeper is the one being excluded")
		}
	})

	t.Run("co-keeper still awake -> attended", func(t *testing.T) {
		bedding := closeupKeeper("john")
		cokeeper := closeupKeeper("martha") // also works the tavern, still awake
		w := keeperTavernWorld(true, bedding, cokeeper)
		placeInside(w, "tavern", "john", "martha")
		if !establishmentHasAwakeKeeperPresent(w, "tavern", "john", now) {
			t.Error("want attended — a co-keeper is still awake on the floor")
		}
	})

	t.Run("works elsewhere -> not a keeper here", func(t *testing.T) {
		k := closeupKeeper("john")
		future := now.Add(8 * time.Hour)
		k.SleepingUntil = &future
		passerby := &Actor{ID: "smith", Kind: KindNPCStateful, WorkStructureID: "smithy", InsideStructureID: "tavern"}
		w := keeperTavernWorld(true, k, passerby)
		placeInside(w, "tavern", "john", "smith")
		if establishmentHasAwakeKeeperPresent(w, "tavern", "", now) {
			t.Error("want unattended — the awake person inside works at the smithy, not here")
		}
	})

	t.Run("non-keeper employee inside -> not attended", func(t *testing.T) {
		k := closeupKeeper("john")
		future := now.Add(8 * time.Hour)
		k.SleepingUntil = &future // the keeper is asleep
		// A hired hand works here (WorkStructureID == tavern) but has no
		// BusinessownerState, so it is NOT the establishment's keeper and must not
		// keep the house attended — the LLM-129 keeper-predicate tightening.
		hand := &Actor{ID: "hired", Kind: KindNPCStateful, WorkStructureID: "tavern", InsideStructureID: "tavern"}
		w := keeperTavernWorld(true, k, hand)
		placeInside(w, "tavern", "john", "hired")
		if establishmentHasAwakeKeeperPresent(w, "tavern", "", now) {
			t.Error("want unattended — an awake hired hand is not the keeper")
		}
	})
}

// TestAnnounceEstablishmentClosing: a single engine-authored Spoke — speaker is
// the keeper, no huddle, the non-tenant NPCs on RecipientIDs, the PCs on
// PCBystanderIDs, text drawn from the closing pool.
func TestAnnounceEstablishmentClosing(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	keeper := closeupKeeper("john")
	w := keeperTavernWorld(true, keeper)
	rec := &spokeRecorder{}
	w.Subscribe(rec)

	announceEstablishmentClosing(w, keeper, []ActorID{"buyer"}, []ActorID{"wendy"}, now)

	if len(rec.spokes) != 1 {
		t.Fatalf("emitted %d Spoke events, want 1 (the closing call)", len(rec.spokes))
	}
	got := rec.spokes[0]
	if got.SpeakerID != "john" {
		t.Errorf("SpeakerID = %q, want john", got.SpeakerID)
	}
	if got.HuddleID != "" {
		t.Errorf("HuddleID = %q, want empty (a room-wide announcement, not a huddle line)", got.HuddleID)
	}
	if len(got.RecipientIDs) != 1 || got.RecipientIDs[0] != "buyer" {
		t.Errorf("RecipientIDs = %v, want [buyer] (the non-tenant NPC)", got.RecipientIDs)
	}
	if len(got.PCBystanderIDs) != 1 || got.PCBystanderIDs[0] != "wendy" {
		t.Errorf("PCBystanderIDs = %v, want [wendy] (the co-present player overhears)", got.PCBystanderIDs)
	}
	if got.Text == "" {
		t.Error("Text is empty, want a closing line drawn from the pool")
	}
}

// TestAnnounceEstablishmentClosing_SilentWithoutPool: with no narration pool
// (a literal-built world), the announcement degrades to silence — no Spoke. The
// eviction still fires; the courtesy is optional, the mechanism is not.
func TestAnnounceEstablishmentClosing_SilentWithoutPool(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	keeper := closeupKeeper("john")
	w := keeperTavernWorld(true, keeper)
	w.NarrationPools = nil // no registry — narrationDraw returns nil
	rec := &spokeRecorder{}
	w.Subscribe(rec)

	announceEstablishmentClosing(w, keeper, []ActorID{"buyer"}, nil, now)

	if len(rec.spokes) != 0 {
		t.Errorf("emitted %d Spoke events, want 0 (no pool -> silent announce)", len(rec.spokes))
	}
}

// TestMaybeBeginEstablishmentCloseup: the trigger fires (announces) only when an
// establishment's own keeper beds down and leaves a non-tenant behind in an
// otherwise-unattended house. The three no-op gates each suppress the announce.
func TestMaybeBeginEstablishmentCloseup(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)

	t.Run("keeper beds with a non-tenant inside -> announces", func(t *testing.T) {
		keeper := closeupKeeper("john")
		customer := &Actor{ID: "buyer", Kind: KindNPCStateful, InsideStructureID: "tavern"}
		w := keeperTavernWorld(true, keeper, customer)
		placeInside(w, "tavern", "john", "buyer")
		rec := &spokeRecorder{}
		w.Subscribe(rec)

		maybeBeginEstablishmentCloseup(w, keeper, now) // also arms a 5-min timer (never fires in-test)

		if len(rec.spokes) != 1 || rec.spokes[0].SpeakerID != "john" {
			t.Errorf("spokes = %+v, want one closing call from john", rec.spokes)
		}
	})

	t.Run("a lodger bedding down is not a close-up", func(t *testing.T) {
		keeper := closeupKeeper("john")
		lodger := lodgerActor("ezekiel", now) // WorkStructureID == smithy, not tavern
		customer := &Actor{ID: "buyer", Kind: KindNPCStateful, InsideStructureID: "tavern"}
		w := keeperTavernWorld(true, keeper, lodger, customer)
		placeInside(w, "tavern", "john", "ezekiel", "buyer")
		rec := &spokeRecorder{}
		w.Subscribe(rec)

		maybeBeginEstablishmentCloseup(w, lodger, now)

		if len(rec.spokes) != 0 {
			t.Errorf("spokes = %+v, want none — a boarder turning in does not close the house", rec.spokes)
		}
	})

	t.Run("co-keeper still awake -> no close-up", func(t *testing.T) {
		bedding := closeupKeeper("john")
		cokeeper := closeupKeeper("martha")
		customer := &Actor{ID: "buyer", Kind: KindNPCStateful, InsideStructureID: "tavern"}
		w := keeperTavernWorld(true, bedding, cokeeper, customer)
		placeInside(w, "tavern", "john", "martha", "buyer")
		rec := &spokeRecorder{}
		w.Subscribe(rec)

		maybeBeginEstablishmentCloseup(w, bedding, now)

		if len(rec.spokes) != 0 {
			t.Errorf("spokes = %+v, want none — a co-keeper keeps the house open", rec.spokes)
		}
	})

	t.Run("empty house -> no announce", func(t *testing.T) {
		keeper := closeupKeeper("john")
		w := keeperTavernWorld(true, keeper)
		placeInside(w, "tavern", "john")
		rec := &spokeRecorder{}
		w.Subscribe(rec)

		maybeBeginEstablishmentCloseup(w, keeper, now)

		if len(rec.spokes) != 0 {
			t.Errorf("spokes = %+v, want none — no one to turn out", rec.spokes)
		}
	})
}

// TestEvictNonTenantsAtClose_ReopenedNoOp: if a keeper is back on the floor and
// awake when the grace timer fires (it woke and re-opened during the window),
// the eviction is a no-op — no one is moved.
func TestEvictNonTenantsAtClose_ReopenedNoOp(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	keeper := closeupKeeper("john") // awake again
	customer := &Actor{ID: "buyer", Kind: KindNPCStateful, InsideStructureID: "tavern"}
	w := keeperTavernWorld(true, keeper, customer)
	placeInside(w, "tavern", "john", "buyer")

	res, err := evictNonTenantsAtClose("tavern", now).Fn(w)
	if err != nil {
		t.Fatalf("evictNonTenantsAtClose: %v", err)
	}
	if n, _ := res.(int); n != 0 {
		t.Errorf("evicted = %d, want 0 (the keeper re-opened)", n)
	}
	if customer.MoveIntent != nil {
		t.Error("customer was sent walking though the house re-opened")
	}
}

// TestRenderClosingLine: a draw returns a line from the pool, is stable for a
// given (keeper, minute), and degrades to "" with no pool.
func TestRenderClosingLine(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	w := keeperTavernWorld(true)

	got := w.renderClosingLine("john", now)
	if got == "" {
		t.Fatal("renderClosingLine returned empty with a seeded pool")
	}
	found := false
	for _, line := range closingLines {
		if line == got {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("renderClosingLine = %q, not a seed line", got)
	}
	if again := w.renderClosingLine("john", now); again != got {
		t.Errorf("renderClosingLine not stable for (john, minute): %q != %q", again, got)
	}

	w.NarrationPools = nil
	if empty := w.renderClosingLine("john", now); empty != "" {
		t.Errorf("renderClosingLine = %q with no pool, want empty", empty)
	}
}

// TestFireEstablishmentCloseup_StaleTimerNoOp: the generation guard. A keeper
// that woke and re-bedded inside the grace window leaves two timers armed for the
// same structure; only the one matching the recorded deadline may fire. A stale
// timer (an older deadline) is a no-op and must not consume the current entry —
// otherwise it would evict on a shortened second grace window.
func TestFireEstablishmentCloseup_StaleTimerNoOp(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	keeper := closeupKeeper("john") // awake on the floor — so the eviction body no-ops cleanly
	customer := &Actor{ID: "buyer", Kind: KindNPCStateful, InsideStructureID: "tavern"}
	w := keeperTavernWorld(true, keeper, customer)
	placeInside(w, "tavern", "john", "buyer")

	d1 := now.Add(establishmentCloseupGrace)                 // the first (now superseded) close-up
	d2 := now.Add(2*time.Minute + establishmentCloseupGrace) // the active close-up after a re-bed
	w.establishmentCloseupDeadline = map[StructureID]time.Time{"tavern": d2}

	// Stale timer fires (deadline d1): superseded -> no eviction, and the current
	// entry (d2) is left intact for the live timer.
	res, err := fireEstablishmentCloseup("tavern", d1).Fn(w)
	if err != nil {
		t.Fatalf("fireEstablishmentCloseup(stale): %v", err)
	}
	if n, _ := res.(int); n != 0 {
		t.Errorf("stale timer evicted %d, want 0", n)
	}
	if customer.MoveIntent != nil {
		t.Error("stale timer sent the customer walking")
	}
	if got, ok := w.establishmentCloseupDeadline["tavern"]; !ok || !got.Equal(d2) {
		t.Errorf("stale timer disturbed the current deadline entry: got %v ok=%v, want %v", got, ok, d2)
	}

	// The live timer fires (deadline d2): it matches, so it consumes the entry.
	if _, err := fireEstablishmentCloseup("tavern", d2).Fn(w); err != nil {
		t.Fatalf("fireEstablishmentCloseup(current): %v", err)
	}
	if _, ok := w.establishmentCloseupDeadline["tavern"]; ok {
		t.Error("the live timer did not consume the deadline entry")
	}
}
