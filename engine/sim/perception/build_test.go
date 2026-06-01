package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// --- test construction helpers -------------------------------------------

func basicWarrant(kind sim.WarrantKind, eventID sim.EventID, scene sim.SceneID, huddle sim.HuddleID, trigger sim.ActorID) sim.WarrantMeta {
	return sim.WarrantMeta{
		TriggerActorID: trigger,
		Reason:         sim.BasicWarrantReason{K: kind},
		SourceEventID:  eventID,
		SceneID:        scene,
		HuddleID:       huddle,
	}
}

func actorSnap(state sim.ActorState, structID sim.StructureID, x, y int, huddle sim.HuddleID, coins int) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		State:             state,
		InsideStructureID: structID,
		Pos:               sim.TilePos{X: x, Y: y},
		CurrentHuddleID:   huddle,
		Coins:             coins,
		Needs:             map[sim.NeedKey]int{},
	}
}

// --- nil / degraded inputs -----------------------------------------------

func TestBuild_NilSnapshot(t *testing.T) {
	w := []sim.WarrantMeta{basicWarrant(sim.WarrantKindArrived, 5, "", "", "alice")}
	p := Build(nil, "alice", w)

	if p.ActorID != "alice" {
		t.Errorf("ActorID = %q, want alice", p.ActorID)
	}
	if p.Baseline != BaselineMissingNoScene {
		t.Errorf("Baseline = %v, want BaselineMissingNoScene", p.Baseline)
	}
	if p.Primary != nil {
		t.Error("Primary should be nil for a nil snapshot")
	}
	// Warrants are still ordered/returned — they came from the caller, not
	// the snapshot.
	if len(p.Warrants) != 1 {
		t.Errorf("Warrants len = %d, want 1", len(p.Warrants))
	}
	if p.SelectionReason == "" {
		t.Error("SelectionReason should explain the degraded build")
	}
}

func TestBuild_ActorNotInSnapshot(t *testing.T) {
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{}}
	w := []sim.WarrantMeta{basicWarrant(sim.WarrantKindArrived, 5, "", "", "alice")}
	p := Build(snap, "alice", w)

	if p.Baseline != BaselineMissingNoScene {
		t.Errorf("Baseline = %v, want BaselineMissingNoScene", p.Baseline)
	}
	if p.Primary != nil {
		t.Error("Primary should be nil when the actor is absent")
	}
	if len(p.Warrants) != 1 {
		t.Errorf("Warrants len = %d, want 1", len(p.Warrants))
	}
}

// --- warrant ordering ----------------------------------------------------

func TestBuild_WarrantOrderingAscendingBySourceEventID(t *testing.T) {
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{
		"alice": actorSnap(sim.StateIdle, "", 0, 0, "", 0),
	}}
	in := []sim.WarrantMeta{
		basicWarrant(sim.WarrantKindNPCSpoke, 30, "", "", "c"),
		basicWarrant(sim.WarrantKindNPCSpoke, 10, "", "", "a"),
		basicWarrant(sim.WarrantKindNPCSpoke, 20, "", "", "b"),
	}
	p := Build(snap, "alice", in)

	wantOrder := []sim.EventID{10, 20, 30}
	for i, want := range wantOrder {
		if p.Warrants[i].SourceEventID != want {
			t.Errorf("Warrants[%d].SourceEventID = %d, want %d", i, p.Warrants[i].SourceEventID, want)
		}
	}
}

func TestBuild_WarrantOrderingZeroLineageSortsFirstStable(t *testing.T) {
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{
		"alice": actorSnap(sim.StateIdle, "", 0, 0, "", 0),
	}}
	// Two zero-lineage warrants (SourceEventID 0) plus one event-sourced —
	// the zero ones sort first, and stably (input order preserved).
	zeroA := basicWarrant(sim.WarrantKindHuddleJoined, 0, "", "", "first")
	zeroB := basicWarrant(sim.WarrantKindHuddleLeft, 0, "", "", "second")
	sourced := basicWarrant(sim.WarrantKindNPCSpoke, 99, "", "", "third")
	p := Build(snap, "alice", []sim.WarrantMeta{sourced, zeroA, zeroB})

	if p.Warrants[0].TriggerActorID != "first" || p.Warrants[1].TriggerActorID != "second" {
		t.Errorf("zero-lineage stable order broken: got %q, %q",
			p.Warrants[0].TriggerActorID, p.Warrants[1].TriggerActorID)
	}
	if p.Warrants[2].SourceEventID != 99 {
		t.Errorf("event-sourced warrant should sort last: got SourceEventID %d", p.Warrants[2].SourceEventID)
	}
}

func TestBuild_DoesNotMutateInputSlice(t *testing.T) {
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{
		"alice": actorSnap(sim.StateIdle, "", 0, 0, "", 0),
	}}
	in := []sim.WarrantMeta{
		basicWarrant(sim.WarrantKindNPCSpoke, 30, "", "", "c"),
		basicWarrant(sim.WarrantKindNPCSpoke, 10, "", "", "a"),
	}
	Build(snap, "alice", in)

	if in[0].SourceEventID != 30 || in[1].SourceEventID != 10 {
		t.Error("Build mutated the caller's warrant slice")
	}
}

// --- scene resolution: step 3 (no scene) ---------------------------------

func TestBuild_ScenelessWarrants_NoSceneResolved(t *testing.T) {
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{
		"alice": actorSnap(sim.StateIdle, "tavern", 1, 1, "", 0),
	}}
	w := []sim.WarrantMeta{basicWarrant(sim.WarrantKindNeedThreshold, 0, "", "", "")}
	p := Build(snap, "alice", w)

	if p.Baseline != BaselineMissingNoScene {
		t.Errorf("Baseline = %v, want BaselineMissingNoScene", p.Baseline)
	}
	if p.Primary != nil {
		t.Error("Primary should be nil with no scene-bearing warrant and no huddle")
	}
	if p.MultiSceneWarrantCount != 0 {
		t.Errorf("MultiSceneWarrantCount = %d, want 0", p.MultiSceneWarrantCount)
	}
}

// --- scene resolution: step 1 (warrant carries the scene) ----------------

func TestBuild_PrimaryFromWarrant_MaxSourceEventID(t *testing.T) {
	origin := actorSnap(sim.StateConversing, "tavern", 5, 5, "h1", 10)
	scene := &sim.Scene{
		ID:         "s-new",
		OriginKind: "pc_speak",
		ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{
			"alice": origin,
		},
	}
	sceneOld := &sim.Scene{
		ID:                       "s-old",
		OriginKind:               "idle_backstop",
		ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{"alice": origin},
	}
	snap := &sim.Snapshot{
		Actors:  map[sim.ActorID]*sim.ActorSnapshot{"alice": actorSnap(sim.StateConversing, "tavern", 5, 5, "h1", 10)},
		Scenes:  map[sim.SceneID]*sim.Scene{"s-new": scene, "s-old": sceneOld},
		Huddles: map[sim.HuddleID]*sim.Huddle{},
	}
	// Two warrants, different scenes — the one with the higher
	// SourceEventID wins the primary slot.
	w := []sim.WarrantMeta{
		basicWarrant(sim.WarrantKindNPCSpoke, 100, "s-old", "h1", "bob"),
		basicWarrant(sim.WarrantKindPCSpoke, 200, "s-new", "h1", "carol"),
	}
	p := Build(snap, "alice", w)

	if p.Primary == nil {
		t.Fatal("Primary should be set")
	}
	if p.Primary.SceneID != "s-new" {
		t.Errorf("Primary.SceneID = %q, want s-new (max SourceEventID)", p.Primary.SceneID)
	}
	if p.MultiSceneWarrantCount != 2 {
		t.Errorf("MultiSceneWarrantCount = %d, want 2", p.MultiSceneWarrantCount)
	}
	if len(p.Secondary) != 1 || p.Secondary[0].SceneID != "s-old" {
		t.Errorf("Secondary = %+v, want one entry for s-old", p.Secondary)
	}
	// The primary's baseline must not be applied to the secondary.
	if len(p.Secondary) == 1 && len(p.Secondary[0].Warrants) != 1 {
		t.Errorf("Secondary[0].Warrants len = %d, want 1", len(p.Secondary[0].Warrants))
	}
}

// --- scene resolution: step 2 (active huddle) ----------------------------

func TestBuild_PrimaryFromActiveHuddle(t *testing.T) {
	origin := actorSnap(sim.StateIdle, "tavern", 2, 2, "h1", 0)
	scene := &sim.Scene{
		ID:                       "s1",
		OriginKind:               "pc_speak",
		Huddles:                  map[sim.HuddleID]struct{}{"h1": {}},
		ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{"alice": origin},
	}
	snap := &sim.Snapshot{
		Actors:  map[sim.ActorID]*sim.ActorSnapshot{"alice": actorSnap(sim.StateIdle, "tavern", 2, 2, "h1", 0)},
		Scenes:  map[sim.SceneID]*sim.Scene{"s1": scene},
		Huddles: map[sim.HuddleID]*sim.Huddle{"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"alice": {}}}},
	}
	// No scene-bearing warrant — resolution falls to the actor's huddle.
	w := []sim.WarrantMeta{basicWarrant(sim.WarrantKindIdleBackstop, 0, "", "", "")}
	p := Build(snap, "alice", w)

	if p.Primary == nil || p.Primary.SceneID != "s1" {
		t.Fatalf("Primary = %+v, want scene s1 from active huddle", p.Primary)
	}
	if p.Baseline != BaselinePresent {
		t.Errorf("Baseline = %v, want BaselinePresent", p.Baseline)
	}
	if p.MultiSceneWarrantCount != 0 {
		t.Errorf("MultiSceneWarrantCount = %d, want 0 (no scene-bearing warrant)", p.MultiSceneWarrantCount)
	}
}

// --- BaselineStatus cases ------------------------------------------------

func TestBuild_BaselinePresent_WithDiff(t *testing.T) {
	origin := actorSnap(sim.StateIdle, "tavern", 1, 1, "h1", 5)
	current := actorSnap(sim.StateConversing, "tavern", 1, 1, "h1", 5) // state changed only
	scene := &sim.Scene{
		ID:                       "s1",
		ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{"alice": origin},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"alice": current},
		Scenes: map[sim.SceneID]*sim.Scene{"s1": scene},
	}
	w := []sim.WarrantMeta{basicWarrant(sim.WarrantKindPCSpoke, 50, "s1", "h1", "bob")}
	p := Build(snap, "alice", w)

	if p.Baseline != BaselinePresent {
		t.Fatalf("Baseline = %v, want BaselinePresent", p.Baseline)
	}
	if p.Primary.Diff == nil {
		t.Fatal("Diff should be set for BaselinePresent")
	}
	if !p.Primary.Diff.StateChanged {
		t.Error("Diff.StateChanged should be true")
	}
	if p.Primary.Diff.PositionChanged || p.Primary.Diff.CoinsChanged {
		t.Error("only state changed; position/coins should be unchanged")
	}
	if !p.Primary.Diff.AnyChange {
		t.Error("Diff.AnyChange should be true")
	}
}

func TestBuild_BaselinePresent_NoChange(t *testing.T) {
	origin := actorSnap(sim.StateIdle, "tavern", 1, 1, "h1", 5)
	current := actorSnap(sim.StateIdle, "tavern", 1, 1, "h1", 5) // identical
	scene := &sim.Scene{
		ID:                       "s1",
		ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{"alice": origin},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"alice": current},
		Scenes: map[sim.SceneID]*sim.Scene{"s1": scene},
	}
	w := []sim.WarrantMeta{basicWarrant(sim.WarrantKindPCSpoke, 50, "s1", "h1", "bob")}
	p := Build(snap, "alice", w)

	if p.Baseline != BaselinePresent {
		t.Fatalf("Baseline = %v, want BaselinePresent", p.Baseline)
	}
	if p.Primary.Diff.AnyChange {
		t.Error("Diff.AnyChange should be false when nothing changed")
	}
}

func TestBuild_BaselineMissingJoinedAfterOrigin(t *testing.T) {
	// Scene captured a baseline for bob, but not alice — alice joined after.
	scene := &sim.Scene{
		ID: "s1",
		ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{
			"bob": actorSnap(sim.StateIdle, "tavern", 0, 0, "h1", 0),
		},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"alice": actorSnap(sim.StateConversing, "tavern", 1, 1, "h1", 0)},
		Scenes: map[sim.SceneID]*sim.Scene{"s1": scene},
	}
	w := []sim.WarrantMeta{basicWarrant(sim.WarrantKindPCSpoke, 50, "s1", "h1", "bob")}
	p := Build(snap, "alice", w)

	if p.Baseline != BaselineMissingJoinedAfterOrigin {
		t.Errorf("Baseline = %v, want BaselineMissingJoinedAfterOrigin", p.Baseline)
	}
	if p.Primary == nil {
		t.Fatal("Primary should still be set — the scene resolved")
	}
	if p.Primary.Diff != nil {
		t.Error("Diff must be nil for a Missing* baseline (unknown, never no-change)")
	}
}

func TestBuild_BaselineMissingNoOriginSnapshot(t *testing.T) {
	// Scene captured no participant baseline at all.
	scene := &sim.Scene{ID: "s1", ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{}}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"alice": actorSnap(sim.StateIdle, "", 0, 0, "h1", 0)},
		Scenes: map[sim.SceneID]*sim.Scene{"s1": scene},
	}
	w := []sim.WarrantMeta{basicWarrant(sim.WarrantKindPCSpoke, 50, "s1", "h1", "bob")}
	p := Build(snap, "alice", w)

	if p.Baseline != BaselineMissingNoOriginSnapshot {
		t.Errorf("Baseline = %v, want BaselineMissingNoOriginSnapshot", p.Baseline)
	}
	if p.Primary == nil || p.Primary.Diff != nil {
		t.Error("Primary set, Diff nil expected for BaselineMissingNoOriginSnapshot")
	}
}

// --- stale scene reference -----------------------------------------------

func TestBuild_StaleSceneReference_FallsThrough(t *testing.T) {
	// The highest-SourceEventID warrant points at a scene no longer in the
	// snapshot; the next one points at a live scene and wins primary.
	live := &sim.Scene{
		ID:                       "s-live",
		ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{"alice": actorSnap(sim.StateIdle, "", 0, 0, "", 0)},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"alice": actorSnap(sim.StateIdle, "", 0, 0, "", 0)},
		Scenes: map[sim.SceneID]*sim.Scene{"s-live": live},
	}
	w := []sim.WarrantMeta{
		basicWarrant(sim.WarrantKindPCSpoke, 100, "s-live", "h1", "bob"),
		basicWarrant(sim.WarrantKindNPCSpoke, 200, "s-gone", "h1", "carol"), // stale, highest event
	}
	p := Build(snap, "alice", w)

	if p.Primary == nil || p.Primary.SceneID != "s-live" {
		t.Fatalf("Primary = %+v, want s-live (fell through past the stale reference)", p.Primary)
	}
	// The stale scene group must not appear as a secondary signal.
	for _, s := range p.Secondary {
		if s.SceneID == "s-gone" {
			t.Error("stale scene s-gone should not appear in Secondary")
		}
	}
}

func TestBuild_AllSceneReferencesStale_NoScene(t *testing.T) {
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"alice": actorSnap(sim.StateIdle, "", 0, 0, "", 0)},
		Scenes: map[sim.SceneID]*sim.Scene{},
	}
	w := []sim.WarrantMeta{basicWarrant(sim.WarrantKindPCSpoke, 100, "s-gone", "h1", "bob")}
	p := Build(snap, "alice", w)

	if p.Baseline != BaselineMissingNoScene {
		t.Errorf("Baseline = %v, want BaselineMissingNoScene", p.Baseline)
	}
	if p.Primary != nil {
		t.Error("Primary should be nil when every scene reference is stale")
	}
}

// --- surroundings --------------------------------------------------------

func TestBuild_SurroundingsHuddleMembersSortedAndSelfExcluded(t *testing.T) {
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"alice": actorSnap(sim.StateIdle, "tavern", 0, 0, "h1", 0)},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": {ID: "tavern", DisplayName: "The Prancing Pony"},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"alice": {}, "carol": {}, "bob": {}}},
		},
	}
	p := Build(snap, "alice", nil)

	if p.Surroundings.StructureName != "The Prancing Pony" {
		t.Errorf("StructureName = %q", p.Surroundings.StructureName)
	}
	want := []sim.ActorID{"bob", "carol"}
	if len(p.Surroundings.HuddleMembers) != 2 ||
		p.Surroundings.HuddleMembers[0].ID != want[0] ||
		p.Surroundings.HuddleMembers[1].ID != want[1] {
		t.Errorf("HuddleMembers = %v, want %v (sorted by ID, self excluded)", p.Surroundings.HuddleMembers, want)
	}
}

// --- arrival-warrant place names (ZBBS-WORK-358) --------------------------

func TestBuild_WarrantPlaceNames(t *testing.T) {
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"alice": actorSnap(sim.StateIdle, "", 0, 0, "", 0)},
		Structures: map[sim.StructureID]*sim.Structure{
			"tavern": {ID: "tavern", DisplayName: "The Prancing Pony"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"well1": {DisplayName: "the Village Well"},
		},
	}
	warrants := []sim.WarrantMeta{
		{TriggerActorID: "alice", Reason: sim.ArrivalWarrantReason{AttemptID: 1, AtStructureID: "tavern"}},
		{TriggerActorID: "alice", Reason: sim.ArrivalWarrantReason{AttemptID: 2, AtObjectID: "well1"}},
	}
	p := Build(snap, "alice", warrants)

	if got := p.WarrantPlaceNames["tavern"]; got != "The Prancing Pony" {
		t.Errorf("place name for tavern = %q, want The Prancing Pony", got)
	}
	if got := p.WarrantPlaceNames["well1"]; got != "the Village Well" {
		t.Errorf("place name for well1 = %q, want the Village Well", got)
	}
}
