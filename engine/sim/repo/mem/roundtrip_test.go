package mem_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestRoundTrip_ActorClonesBreakAliasing verifies that going through
// Seed → LoadAll → mutate → SaveSnapshot → LoadAll preserves values but
// produces fresh entities. The aliasing check is the load-bearing assertion
// — without per-aggregate clone helpers, mem aliased pointers and the
// pg-impl serialization boundary would surface shape bugs only at cutover.
func TestRoundTrip_ActorClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	now := time.Now().UTC()
	rem := 3
	expires := now.Add(2 * time.Hour)

	seed := map[sim.ActorID]*sim.Actor{
		"elizabeth": {
			ID:            "elizabeth",
			DisplayName:   "Elizabeth Ellis",
			Kind:          sim.KindNPCStateful,
			State:         sim.StateWalking,
			Needs:         map[sim.NeedKey]int{"hunger": 5, "tiredness": 3},
			Inventory:     map[sim.ItemKind]int{"bread": 2},
			Coins:         42,
			LastTickedAt:  &now,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
			DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
				{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}: {
					ObjectID:           "oak-1",
					Attribute:          "tiredness",
					Source:             sim.DwellSourceObject,
					LastCreditedAt:     now,
					DwellDelta:         -2,
					DwellPeriodMinutes: 15,
				},
				{ObjectID: "bread-1", Attribute: "hunger", Source: sim.DwellSourceItem}: {
					ObjectID:           "bread-1",
					Attribute:          "hunger",
					Source:             sim.DwellSourceItem,
					LastCreditedAt:     now,
					RemainingTicks:     &rem,
					DwellDelta:         -1,
					DwellPeriodMinutes: 5,
				},
			},
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 2, Source: sim.AccessSourceLedger}: {
					RoomID:    2,
					Source:    sim.AccessSourceLedger,
					LedgerID:  100,
					ExpiresAt: &expires,
					Active:    true,
					CreatedAt: now,
				},
			},
		},
	}
	h.Actors.Seed(seed)

	// Mutating the seed map after Seed must NOT bleed through — Seed clones.
	seed["elizabeth"].Needs["hunger"] = 999
	seed["elizabeth"].DwellCredits[sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}].DwellDelta = 999

	loaded1, err := h.Actors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := loaded1["elizabeth"].Needs["hunger"]; got != 5 {
		t.Fatalf("Seed didn't clone: post-Seed mutation of caller's map leaked to repo (Needs.hunger=%d, want 5)", got)
	}
	if got := loaded1["elizabeth"].DwellCredits[sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}].DwellDelta; got != -2 {
		t.Fatalf("Seed didn't clone DwellCredit: got DwellDelta=%d, want -2", got)
	}

	// Mutate the loaded entity, save, reload — value should be preserved.
	loaded1["elizabeth"].Needs["hunger"] = 7
	loaded1["elizabeth"].Inventory["ale"] = 1
	loaded1["elizabeth"].Coins = 50
	loaded1["elizabeth"].DwellCredits[sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}].DwellDelta = -5

	if err := h.Actors.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// After save, mutating the source again should not leak.
	loaded1["elizabeth"].Needs["hunger"] = 123

	loaded2, err := h.Actors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}

	// Values reflect the saved mutation.
	if got := loaded2["elizabeth"].Needs["hunger"]; got != 7 {
		t.Errorf("Needs.hunger after save+reload = %d, want 7", got)
	}
	if got := loaded2["elizabeth"].Inventory["ale"]; got != 1 {
		t.Errorf("Inventory.ale after save+reload = %d, want 1", got)
	}
	if got := loaded2["elizabeth"].Coins; got != 50 {
		t.Errorf("Coins after save+reload = %d, want 50", got)
	}
	if got := loaded2["elizabeth"].DwellCredits[sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}].DwellDelta; got != -5 {
		t.Errorf("DwellCredit DwellDelta after save+reload = %d, want -5", got)
	}

	// Pointer identity is broken across reloads — proves the clone is real
	// and not just a shallow copy of the outer struct.
	if loaded1["elizabeth"] == loaded2["elizabeth"] {
		t.Error("Actor pointer aliased between LoadAll calls")
	}
	k := sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}
	if loaded1["elizabeth"].DwellCredits[k] == loaded2["elizabeth"].DwellCredits[k] {
		t.Error("DwellCredit pointer aliased between LoadAll calls")
	}
	rk := sim.RoomAccessKey{RoomID: 2, Source: sim.AccessSourceLedger}
	if loaded1["elizabeth"].RoomAccess[rk] == loaded2["elizabeth"].RoomAccess[rk] {
		t.Error("RoomAccess pointer aliased between LoadAll calls")
	}
	if loaded1["elizabeth"].RoomAccess[rk].ExpiresAt == loaded2["elizabeth"].RoomAccess[rk].ExpiresAt {
		t.Error("RoomAccess.ExpiresAt *time.Time aliased between LoadAll calls")
	}
}

// TestRoundTrip_ActorNarrativeAndRelationshipsClonesBreakAliasing
// verifies the per-actor knowledge state (Acquaintances, Relationships,
// Narrative) round-trips with values preserved and pointer identity
// broken — including the *time.Time pointers on Relationship /
// NarrativeState and the SalientFacts slice on each Relationship.
//
// Acquaintances is a value-typed map so pointer-identity isn't an issue
// for the values themselves; the test still asserts post-Seed mutation
// of the caller's map doesn't leak (the map itself is cloned).
func TestRoundTrip_ActorNarrativeAndRelationshipsClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	now := time.Now().UTC()
	earlier := now.Add(-2 * time.Hour)
	consolidated := now.Add(-1 * time.Hour)

	seed := map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:          "hannah",
			DisplayName: "Hannah",
			Kind:        sim.KindNPCShared,
			Acquaintances: map[string]sim.Acquaintance{
				"Ezekiel Crane": {FirstInteractedAt: earlier},
				"John Ellis":    {FirstInteractedAt: now},
			},
			Relationships: map[sim.ActorID]*sim.Relationship{
				"ezekiel": {
					SummaryText:        "Talks about iron a lot.",
					SalientFacts:       []sim.SalientFact{{At: earlier, Kind: sim.InteractionHeard, Text: "Said he needs charcoal."}},
					InteractionCount:   3,
					LastInteractionAt:  &now,
					LastConsolidatedAt: &consolidated,
					CreatedAt:          earlier,
					UpdatedAt:          now,
					DroppedFactCount:   7,
				},
			},
			Narrative: &sim.NarrativeState{
				SeedText:           "You are Hannah, daughter of the innkeeper.",
				EvolvingSummary:    "Has been worried about the harvest.",
				LastConsolidatedAt: &consolidated,
				CreatedAt:          earlier,
				UpdatedAt:          now,
			},
		},
	}
	h.Actors.Seed(seed)

	// Post-Seed mutation of the caller's structures must not leak. The
	// *time.Time mutations dereference and overwrite the local `now` /
	// `consolidated` variables, so assertions below use the leak-marker
	// value rather than re-comparing to those vars.
	leakMarker := time.Unix(0, 0)
	seed["hannah"].Acquaintances["Ezekiel Crane"] = sim.Acquaintance{FirstInteractedAt: leakMarker}
	seed["hannah"].Relationships["ezekiel"].SummaryText = "MUTATED"
	seed["hannah"].Relationships["ezekiel"].SalientFacts[0].Text = "MUTATED"
	*seed["hannah"].Relationships["ezekiel"].LastInteractionAt = leakMarker
	seed["hannah"].Narrative.EvolvingSummary = "MUTATED"
	*seed["hannah"].Narrative.LastConsolidatedAt = leakMarker

	loaded1, err := h.Actors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := loaded1["hannah"].Acquaintances["Ezekiel Crane"].FirstInteractedAt; got.Equal(leakMarker) {
		t.Error("Seed didn't clone Acquaintances: post-Seed mutation leaked")
	}
	if got := loaded1["hannah"].Relationships["ezekiel"].SummaryText; got != "Talks about iron a lot." {
		t.Errorf("Seed didn't clone Relationship: SummaryText = %q", got)
	}
	if got := loaded1["hannah"].Relationships["ezekiel"].SalientFacts[0].Text; got != "Said he needs charcoal." {
		t.Errorf("Seed didn't clone SalientFacts slice element: Text = %q", got)
	}
	if got := *loaded1["hannah"].Relationships["ezekiel"].LastInteractionAt; got.Equal(leakMarker) {
		t.Error("Seed didn't clone LastInteractionAt pointer: post-Seed mutation leaked")
	}
	if got := loaded1["hannah"].Narrative.EvolvingSummary; got != "Has been worried about the harvest." {
		t.Errorf("Seed didn't clone Narrative: EvolvingSummary = %q", got)
	}
	if got := *loaded1["hannah"].Narrative.LastConsolidatedAt; got.Equal(leakMarker) {
		t.Error("Seed didn't clone Narrative.LastConsolidatedAt pointer: post-Seed mutation leaked")
	}

	// Mutate loaded, save, reload — values preserved across the round-trip.
	loaded1["hannah"].Relationships["ezekiel"].SummaryText = "Stopped buying charcoal — switched suppliers."
	loaded1["hannah"].Relationships["ezekiel"].SalientFacts = append(loaded1["hannah"].Relationships["ezekiel"].SalientFacts,
		sim.SalientFact{At: now, Kind: sim.InteractionPaidBy, Text: "Paid 4 coins for ale."})
	loaded1["hannah"].Relationships["ezekiel"].InteractionCount = 4
	loaded1["hannah"].Narrative.EvolvingSummary = "Less worried — Ezekiel's order came through."

	if err := h.Actors.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Post-save mutation of the source must not leak.
	loaded1["hannah"].Relationships["ezekiel"].SummaryText = "MUTATED-POST-SAVE"

	loaded2, err := h.Actors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}
	if got := loaded2["hannah"].Relationships["ezekiel"].SummaryText; got != "Stopped buying charcoal — switched suppliers." {
		t.Errorf("Relationship.SummaryText after save+reload = %q", got)
	}
	if got := len(loaded2["hannah"].Relationships["ezekiel"].SalientFacts); got != 2 {
		t.Errorf("SalientFacts after save+reload: len = %d, want 2", got)
	}
	if got := loaded2["hannah"].Relationships["ezekiel"].InteractionCount; got != 4 {
		t.Errorf("Relationship.InteractionCount after save+reload = %d, want 4", got)
	}
	if got := loaded2["hannah"].Relationships["ezekiel"].DroppedFactCount; got != 7 {
		t.Errorf("Relationship.DroppedFactCount after save+reload = %d, want 7", got)
	}
	if got := loaded2["hannah"].Narrative.EvolvingSummary; got != "Less worried — Ezekiel's order came through." {
		t.Errorf("Narrative.EvolvingSummary after save+reload = %q", got)
	}

	// Pointer identity broken across reloads for every clonable boundary.
	if loaded1["hannah"].Relationships["ezekiel"] == loaded2["hannah"].Relationships["ezekiel"] {
		t.Error("Relationship pointer aliased between LoadAll calls")
	}
	if loaded1["hannah"].Relationships["ezekiel"].LastInteractionAt == loaded2["hannah"].Relationships["ezekiel"].LastInteractionAt {
		t.Error("Relationship.LastInteractionAt *time.Time aliased between LoadAll calls")
	}
	if loaded1["hannah"].Narrative == loaded2["hannah"].Narrative {
		t.Error("Narrative pointer aliased between LoadAll calls")
	}
	if loaded1["hannah"].Narrative.LastConsolidatedAt == loaded2["hannah"].Narrative.LastConsolidatedAt {
		t.Error("Narrative.LastConsolidatedAt *time.Time aliased between LoadAll calls")
	}
}

// TestNewSalientFact_TruncatesText verifies the rune-aware Text cap.
// MaxSalientFactTextLen is in runes, not bytes — multibyte text mustn't
// be split mid-rune.
func TestNewSalientFact_TruncatesText(t *testing.T) {
	now := time.Now().UTC()
	// ASCII: each rune is 1 byte; 250-char input gets capped to 220.
	long := ""
	for i := 0; i < 250; i++ {
		long += "x"
	}
	got := sim.NewSalientFact(now, sim.InteractionSpoke, long)
	if len([]rune(got.Text)) != sim.MaxSalientFactTextLen {
		t.Errorf("ASCII truncation: got %d runes, want %d", len([]rune(got.Text)), sim.MaxSalientFactTextLen)
	}
	// Multibyte: 250 'é' (2 bytes each in UTF-8). Truncating by bytes
	// would split a rune; this test catches that.
	multi := ""
	for i := 0; i < 250; i++ {
		multi += "é"
	}
	got2 := sim.NewSalientFact(now, sim.InteractionSpoke, multi)
	if len([]rune(got2.Text)) != sim.MaxSalientFactTextLen {
		t.Errorf("Multibyte truncation: got %d runes, want %d", len([]rune(got2.Text)), sim.MaxSalientFactTextLen)
	}
	// The cut marks itself now (LLM-405), so the last rune is the elision marker
	// and everything BEFORE it must still be a whole 'é' — a byte-wise cut would
	// leave a split rune in there, which is what this check exists to catch.
	if !strings.HasSuffix(got2.Text, sim.ElisionMarker) {
		t.Errorf("Multibyte truncation: cut was not marked with %q", sim.ElisionMarker)
	}
	for _, r := range strings.TrimSuffix(got2.Text, sim.ElisionMarker) {
		if r != 'é' {
			t.Errorf("Multibyte truncation split a rune: found %q", r)
			break
		}
	}
}

// TestRoundTrip_ActorMoveIntentClonesBreakAliasing verifies an actor's
// MoveIntent (Phase 2 PR 4) survives the repo boundary with values
// preserved and pointer identity broken — including the nested
// MoveDestination.StructureID pointer. MoveAttemptCounter is a scalar, so
// it just needs to be preserved.
func TestRoundTrip_ActorMoveIntentClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	seed := map[sim.ActorID]*sim.Actor{
		"walker": {
			ID:                 "walker",
			DisplayName:        "Walker",
			MoveIntent:         &sim.MoveIntent{Destination: sim.NewStructureEnterDestination("inn"), AttemptID: 7},
			MoveAttemptCounter: 7,
		},
	}
	h.Actors.Seed(seed)

	// Post-Seed mutation of the caller's MoveIntent must not leak.
	*seed["walker"].MoveIntent.Destination.StructureID = "MUTATED"
	seed["walker"].MoveIntent.AttemptID = 999

	loaded1, err := h.Actors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	mi1 := loaded1["walker"].MoveIntent
	if mi1 == nil {
		t.Fatal("Seed dropped MoveIntent")
	}
	if mi1.AttemptID != 7 {
		t.Errorf("Seed didn't clone MoveIntent: AttemptID=%d, want 7", mi1.AttemptID)
	}
	if *mi1.Destination.StructureID != "inn" {
		t.Errorf("Seed didn't clone MoveDestination.StructureID: got %q, want inn", *mi1.Destination.StructureID)
	}
	if loaded1["walker"].MoveAttemptCounter != 7 {
		t.Errorf("MoveAttemptCounter not preserved through Seed: got %d, want 7", loaded1["walker"].MoveAttemptCounter)
	}

	// Mutate the loaded MoveIntent, save, reload — values preserved.
	*loaded1["walker"].MoveIntent.Destination.StructureID = "tavern"
	loaded1["walker"].MoveIntent.AttemptID = 9
	loaded1["walker"].MoveAttemptCounter = 9
	if err := h.Actors.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	// Post-save mutation of the source must not leak.
	loaded1["walker"].MoveIntent.AttemptID = 123

	loaded2, err := h.Actors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}
	mi2 := loaded2["walker"].MoveIntent
	if mi2.AttemptID != 9 {
		t.Errorf("MoveIntent.AttemptID after save+reload = %d, want 9", mi2.AttemptID)
	}
	if *mi2.Destination.StructureID != "tavern" {
		t.Errorf("MoveDestination.StructureID after save+reload = %q, want tavern", *mi2.Destination.StructureID)
	}
	if loaded2["walker"].MoveAttemptCounter != 9 {
		t.Errorf("MoveAttemptCounter after save+reload = %d, want 9", loaded2["walker"].MoveAttemptCounter)
	}

	// Pointer identity broken across reloads — the MoveIntent itself AND
	// its nested MoveDestination.StructureID pointer.
	if loaded1["walker"].MoveIntent == loaded2["walker"].MoveIntent {
		t.Error("MoveIntent pointer aliased between LoadAll calls")
	}
	if loaded1["walker"].MoveIntent.Destination.StructureID == loaded2["walker"].MoveIntent.Destination.StructureID {
		t.Error("MoveDestination.StructureID pointer aliased between LoadAll calls")
	}
}

// TestRoundTrip_VillageObjectClonesBreakAliasing verifies the same
// invariants for VillageObject — Tags slice, Refreshes slice, and each
// ObjectRefresh pointer must be fresh across the repo boundary.
func TestRoundTrip_VillageObjectClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	avail := 5
	max := 10
	hours := 6

	seed := map[sim.VillageObjectID]*sim.VillageObject{
		"well-1": {
			ID:           "well-1",
			AssetID:      "well",
			CurrentState: "default",
			Tags:         []string{"refresh", "public"},
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "thirst",
					Amount:             -3,
					AvailableQuantity:  &avail,
					MaxQuantity:        &max,
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: &hours,
				},
			},
		},
	}
	h.VillageObjects.Seed(seed)

	// Post-Seed mutation must not leak.
	seed["well-1"].Tags[0] = "MUTATED"
	seed["well-1"].Refreshes[0] = nil

	loaded1, err := h.VillageObjects.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := loaded1["well-1"].Tags[0]; got != "refresh" {
		t.Fatalf("Seed didn't clone Tags: got %q, want refresh", got)
	}
	if loaded1["well-1"].Refreshes[0] == nil {
		t.Fatal("Seed didn't clone Refreshes slice element")
	}

	// Mutate + save + reload.
	loaded1["well-1"].CurrentState = "lit"
	next := 8
	loaded1["well-1"].Refreshes[0].AvailableQuantity = &next

	if err := h.VillageObjects.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	loaded2, err := h.VillageObjects.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}

	if got := loaded2["well-1"].CurrentState; got != "lit" {
		t.Errorf("CurrentState after save+reload = %q, want lit", got)
	}
	if got := *loaded2["well-1"].Refreshes[0].AvailableQuantity; got != 8 {
		t.Errorf("AvailableQuantity after save+reload = %d, want 8", got)
	}

	if loaded1["well-1"] == loaded2["well-1"] {
		t.Error("VillageObject pointer aliased between LoadAll calls")
	}
	if loaded1["well-1"].Refreshes[0] == loaded2["well-1"].Refreshes[0] {
		t.Error("ObjectRefresh pointer aliased between LoadAll calls")
	}
}

// TestRoundTrip_StructureClonesBreakAliasing verifies Structure (and its
// Rooms slice) round-trips with fresh pointers.
func TestRoundTrip_StructureClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	seed := map[sim.StructureID]*sim.Structure{
		"tavern": {
			ID:          "tavern",
			DisplayName: "The Crow's Foot",
			Tags:        []string{"tavern", "lodging"},
			Rooms: []*sim.Room{
				{ID: 1, StructureID: "tavern", Kind: sim.RoomKindCommon, Name: "common"},
				{ID: 2, StructureID: "tavern", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			},
		},
	}
	h.Structures.Seed(seed)

	seed["tavern"].Tags[0] = "MUTATED"
	seed["tavern"].Rooms[0].Name = "MUTATED"

	loaded1, err := h.Structures.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := loaded1["tavern"].Tags[0]; got != "tavern" {
		t.Fatalf("Seed didn't clone Tags: got %q", got)
	}
	if got := loaded1["tavern"].Rooms[0].Name; got != "common" {
		t.Fatalf("Seed didn't clone Rooms: got %q", got)
	}

	loaded1["tavern"].DisplayName = "Renamed"
	if err := h.Structures.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	loaded2, err := h.Structures.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}

	if got := loaded2["tavern"].DisplayName; got != "Renamed" {
		t.Errorf("DisplayName after save+reload = %q, want Renamed", got)
	}
	if loaded1["tavern"] == loaded2["tavern"] {
		t.Error("Structure pointer aliased between LoadAll calls")
	}
	if loaded1["tavern"].Rooms[0] == loaded2["tavern"].Rooms[0] {
		t.Error("Room pointer aliased between LoadAll calls")
	}
}

// TestRoundTrip_HuddleClonesBreakAliasing covers Huddle with its Members
// map.
func TestRoundTrip_HuddleClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	seed := map[sim.HuddleID]*sim.Huddle{
		"h1": {
			ID:        "h1",
			Members:   map[sim.ActorID]struct{}{"alice": {}, "bob": {}},
			StartedAt: time.Now().UTC(),
		},
	}
	h.Huddles.Seed(seed)

	// Mutate seed after — must not leak into repo.
	delete(seed["h1"].Members, "alice")

	loaded1, err := h.Huddles.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := loaded1["h1"].Members["alice"]; !ok {
		t.Fatal("Seed didn't clone Members: post-Seed delete leaked")
	}

	delete(loaded1["h1"].Members, "alice")
	if err := h.Huddles.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	loaded2, err := h.Huddles.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}
	if _, ok := loaded2["h1"].Members["alice"]; ok {
		t.Error("alice should be gone after save+reload")
	}
	if loaded1["h1"] == loaded2["h1"] {
		t.Error("Huddle pointer aliased between LoadAll calls")
	}
}

// TestRoundTrip_SceneClonesBreakAliasing covers Scene including the
// nested ParticipantStateAtOrigin map of *ActorSnapshot. The participant-
// snapshot capture is the seam Phase 2 PR 3 perception build will read for
// diff-against-scene-start, so the round-trip clone has to deep-copy each
// snapshot AND each snapshot's Needs map — otherwise a checkpoint+reload
// would produce ghost-aliased state observable through subsequent
// mutations.
func TestRoundTrip_SceneClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	now := time.Now().UTC()
	seed := map[sim.SceneID]*sim.Scene{
		"sc1": {
			ID:         "sc1",
			OriginAt:   now,
			OriginKind: "pc_speak",
			Bound:      sim.NewStructureBound("tavern"),
			Huddles: map[sim.HuddleID]struct{}{
				"h1": {},
			},
			ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{
				"alice": {
					AtTick:            42,
					State:             sim.StateConversing,
					InsideStructureID: "tavern",
					CurrentHuddleID:   "h1",
					Needs:             map[sim.NeedKey]int{"hunger": 4, "thirst": 1},
					Coins:             7,
				},
			},
		},
	}
	h.Scenes.Seed(seed)

	// Mutate seed AFTER Seed — must not leak into the repo. Tests both
	// the Huddles set and the inner Needs map of the captured snapshot.
	delete(seed["sc1"].Huddles, "h1")
	seed["sc1"].ParticipantStateAtOrigin["alice"].Needs["hunger"] = 999

	loaded1, err := h.Scenes.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := loaded1["sc1"].Huddles["h1"]; !ok {
		t.Fatal("Seed didn't clone Huddles set: post-Seed delete leaked")
	}
	if got := loaded1["sc1"].ParticipantStateAtOrigin["alice"].Needs["hunger"]; got != 4 {
		t.Fatalf("Seed didn't deep-clone ActorSnapshot.Needs: got hunger=%d, want 4", got)
	}

	// Mutate the loaded scene, save, reload — values preserved.
	loaded1["sc1"].Huddles["h2"] = struct{}{}
	loaded1["sc1"].ParticipantStateAtOrigin["alice"].Needs["thirst"] = 5
	loaded1["sc1"].ParticipantStateAtOrigin["bob"] = &sim.ActorSnapshot{
		AtTick:          42,
		State:           sim.StateIdle,
		CurrentHuddleID: "h1",
		Needs:           map[sim.NeedKey]int{"hunger": 0},
		Coins:           3,
	}

	if err := h.Scenes.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	loaded2, err := h.Scenes.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}
	if _, ok := loaded2["sc1"].Huddles["h2"]; !ok {
		t.Error("Huddles set lost h2 after save+reload")
	}
	if got := loaded2["sc1"].ParticipantStateAtOrigin["alice"].Needs["thirst"]; got != 5 {
		t.Errorf("alice thirst after save+reload = %d, want 5", got)
	}
	if got := loaded2["sc1"].ParticipantStateAtOrigin["bob"].Coins; got != 3 {
		t.Errorf("bob coins after save+reload = %d, want 3", got)
	}

	if loaded1["sc1"] == loaded2["sc1"] {
		t.Error("Scene pointer aliased between LoadAll calls")
	}
	if loaded1["sc1"].ParticipantStateAtOrigin["alice"] == loaded2["sc1"].ParticipantStateAtOrigin["alice"] {
		t.Error("ActorSnapshot pointer aliased between LoadAll calls")
	}
}
