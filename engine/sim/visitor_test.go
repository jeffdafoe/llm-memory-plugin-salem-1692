package sim_test

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// makeAllDirtTerrain returns a Terrain blob of MapW*MapH dirt tiles —
// every tile is a road, so pickVisitorEdgeTile finds candidates at depth 0
// on every edge and FindPathToAdjacent always succeeds.
func makeAllDirtTerrain() *sim.Terrain {
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainDirt
	}
	return &sim.Terrain{Data: data}
}

// TestExtractSurname covers the surname-extraction helper used by the
// scrub.
func TestExtractSurname(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Master Whitcombe", "whitcombe"},
		{"Ezekiel Crane", "crane"},
		{"Tobias", "tobias"}, // single-token defensively treats first=surname
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sim.ExtractSurname(c.name)
			if got != c.want {
				t.Errorf("extractSurname(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// TestVisitorArchetypeSpriteExhaustive verifies every archetype in the
// pool has a sprite mapping. The init() in visitor.go panics on mismatch
// at load — this test is a clean regression signal if someone adds an
// archetype without a sprite and the panic message gets caught somewhere.
func TestVisitorArchetypeSpriteExhaustive(t *testing.T) {
	pool := sim.VisitorArchetypePoolForTest()
	for _, a := range pool {
		if _, ok := sim.VisitorArchetypeSprite[a]; !ok {
			t.Errorf("archetype %q has no entry in VisitorArchetypeSprite", a)
		}
	}
}

// visitorWorld builds an in-memory world seeded with an all-dirt terrain,
// a tavern structure + VillageObject placed in the village interior, and
// optionally a few extra actors so the surname scrub has something to
// hit. Returns a running World whose goroutine the caller cancels.
type visitorWorld struct {
	repo    sim.Repository
	handles *mem.Handles
}

func newVisitorWorld() *visitorWorld {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllDirtTerrain())
	return &visitorWorld{repo: repo, handles: handles}
}

func (vw *visitorWorld) seedTavern(t *testing.T) sim.StructureID {
	t.Helper()
	const id = "tavern"
	vw.handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"tavern-asset": {
			ID:          "tavern-asset",
			Category:    "structure",
			DoorOffsetX: intpV(1),
			DoorOffsetY: intpV(2),
		},
	})
	vw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		id: {
			ID:          id,
			AssetID:     "tavern-asset",
			Pos:         sim.WorldPos{X: 320, Y: 320},
			EntryPolicy: sim.EntryPolicyOpen,
			Tags:        []string{sim.VisitorTagTavern},
		},
	})
	vw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		id: {ID: id, DisplayName: "The Tavern"},
	})
	return id
}

func intpV(i int) *int { return &i }

func (vw *visitorWorld) load(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	w, err := sim.LoadWorld(context.Background(), vw.repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// TestPickVisitorDestination_TavernPreferred verifies a tagged tavern is
// chosen over an unrelated tagged structure.
func TestPickVisitorDestination_TavernPreferred(t *testing.T) {
	vw := newVisitorWorld()
	vw.handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"a": {ID: "a", Category: "structure"},
	})
	vw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"other":  {ID: "other", AssetID: "a", Pos: sim.WorldPos{X: 100, Y: 100}, Tags: []string{"smith"}},
		"tavern": {ID: "tavern", AssetID: "a", Pos: sim.WorldPos{X: 320, Y: 320}, Tags: []string{sim.VisitorTagTavern}},
	})
	vw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"other":  {ID: "other"},
		"tavern": {ID: "tavern"},
	})
	w, cancel := vw.load(t)
	defer cancel()

	res, err := w.Send(sim.PickVisitorDestinationForTest())
	if err != nil {
		t.Fatalf("PickVisitorDestinationForTest: %v", err)
	}
	got := res.(sim.PickVisitorDestinationResult)
	if !got.Ok {
		t.Fatal("expected ok=true")
	}
	if got.StructureID != "tavern" {
		t.Errorf("destination = %q, want %q", got.StructureID, "tavern")
	}
}

// TestPickVisitorDestination_FallbackAnyTagged verifies an untagged-tavern
// world falls back to any structure with a VillageObject.
func TestPickVisitorDestination_FallbackAnyTagged(t *testing.T) {
	vw := newVisitorWorld()
	vw.handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"a": {ID: "a", Category: "structure"},
	})
	vw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"smith": {ID: "smith", AssetID: "a", Pos: sim.WorldPos{X: 100, Y: 100}, Tags: []string{"smith"}},
	})
	vw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"smith": {ID: "smith"},
	})
	w, cancel := vw.load(t)
	defer cancel()
	res, err := w.Send(sim.PickVisitorDestinationForTest())
	if err != nil {
		t.Fatalf("PickVisitorDestinationForTest: %v", err)
	}
	got := res.(sim.PickVisitorDestinationResult)
	if !got.Ok {
		t.Fatal("expected ok=true")
	}
	if got.StructureID != "smith" {
		t.Errorf("destination = %q, want %q", got.StructureID, "smith")
	}
}

// TestPickVisitorDestination_None verifies an empty village returns false.
func TestPickVisitorDestination_None(t *testing.T) {
	vw := newVisitorWorld()
	w, cancel := vw.load(t)
	defer cancel()
	res, err := w.Send(sim.PickVisitorDestinationForTest())
	if err != nil {
		t.Fatalf("PickVisitorDestinationForTest: %v", err)
	}
	got := res.(sim.PickVisitorDestinationResult)
	if got.Ok {
		t.Errorf("expected ok=false, got destination=%q", got.StructureID)
	}
}

// TestTickVisitorCascade_SpawnDisabled verifies chance=0 produces no
// spawn and reports the skip reason in telemetry.
func TestTickVisitorCascade_SpawnDisabled(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	r := rand.New(rand.NewSource(1))
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{
		Now: time.Now(), Rand: r,
	}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	tm := res.(sim.VisitorCascadeTelemetry)
	if tm.Spawned != 0 {
		t.Errorf("spawned = %d, want 0", tm.Spawned)
	}
	if tm.SpawnSkipChance != 1 {
		t.Errorf("SpawnSkipChance = %d, want 1", tm.SpawnSkipChance)
	}
}

// TestTickVisitorCascade_Spawns verifies a guaranteed roll produces a
// visitor with VisitorState populated and a tavern walk dispatched.
func TestTickVisitorCascade_Spawns(t *testing.T) {
	vw := newVisitorWorld()
	tavernID := vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	// Force settings: chance=1000 guarantees the spawn roll fires.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorSpawnChancePermille = 1000
		world.Settings.VisitorMaxConcurrent = 2
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	r := rand.New(rand.NewSource(42))
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{
		Now: time.Now(), Rand: r,
	}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	tm := res.(sim.VisitorCascadeTelemetry)
	if tm.Spawned != 1 {
		t.Errorf("spawned = %d, want 1", tm.Spawned)
	}

	snap := w.Published()
	var got *sim.ActorSnapshot
	for _, a := range snap.Actors {
		if a.VisitorState != nil {
			got = a
			break
		}
	}
	if got == nil {
		t.Fatal("no visitor in snapshot after spawn")
	}
	if got.Kind != sim.KindNPCShared {
		t.Errorf("visitor Kind = %v, want KindNPCShared", got.Kind)
	}
	if got.VisitorState.Archetype == "" {
		t.Error("visitor Archetype is empty")
	}
	if got.VisitorState.Origin == "" {
		t.Error("visitor Origin is empty")
	}
	if got.VisitorState.Disposition == "" {
		t.Error("visitor Disposition is empty")
	}
	if got.VisitorState.ExpiresAt.IsZero() {
		t.Error("visitor ExpiresAt not stamped")
	}

	// Walk-target verification: the visitor's MoveIntent should reference
	// the tavern (either via StructureEnter or StructureVisit fallback).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, a := range world.Actors {
			if a.VisitorState == nil {
				continue
			}
			if a.MoveIntent == nil {
				t.Errorf("spawned visitor has no MoveIntent")
				return nil, nil
			}
			if a.MoveIntent.Destination.StructureID == nil ||
				*a.MoveIntent.Destination.StructureID != tavernID {
				t.Errorf("visitor MoveIntent dest = %+v, want StructureID=%q", a.MoveIntent.Destination, tavernID)
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("MoveIntent check: %v", err)
	}
}

// TestTickVisitorCascade_EcoPausesSpawn verifies eco mode (LLM-313) withholds
// the spawn roll while no player presence is fresh — visitors exist to be
// seen — and that a fresh presence stamp restores spawning on the next tick.
func TestTickVisitorCascade_EcoPausesSpawn(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	// Guaranteed roll, eco armed, no PC → the eco gate is the only thing
	// standing between this tick and a spawn.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorSpawnChancePermille = 1000
		world.Settings.VisitorMaxConcurrent = 2
		world.Settings.EcoEnabled = true
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	now := time.Now().UTC()
	r := rand.New(rand.NewSource(42))
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: r}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	if tm := res.(sim.VisitorCascadeTelemetry); tm.Spawned != 0 {
		t.Errorf("spawned = %d while unwatched, want 0", tm.Spawned)
	}

	// A PC with a fresh stamp lifts the pause on the very next tick.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		stamp := now
		world.Actors["pc"] = &sim.Actor{ID: "pc", Kind: sim.KindPC, LastPCSeenAt: &stamp}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed PC: %v", err)
	}
	res, err = w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: r}))
	if err != nil {
		t.Fatalf("TickVisitorCascade (watched): %v", err)
	}
	if tm := res.(sim.VisitorCascadeTelemetry); tm.Spawned != 1 {
		t.Errorf("spawned = %d with a fresh presence stamp, want 1", tm.Spawned)
	}
}

// TestTickVisitorCascade_RespectsCap verifies VisitorMaxConcurrent caps
// the spawn even when chance fires.
func TestTickVisitorCascade_RespectsCap(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()
	// Settings: chance=1000, cap=1.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorSpawnChancePermille = 1000
		world.Settings.VisitorMaxConcurrent = 1
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	r := rand.New(rand.NewSource(99))
	for i := 0; i < 3; i++ {
		if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{
			Now: time.Now(), Rand: r,
		})); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}

	snap := w.Published()
	count := 0
	for _, a := range snap.Actors {
		if a.VisitorState != nil {
			count++
		}
	}
	if count != 1 {
		t.Errorf("visitor count = %d, want 1 (cap)", count)
	}
}

// TestTickVisitorCascade_DespawnDispatches verifies a visitor past
// ExpiresAt gets a despawn walk and LeaveDispatched set.
func TestTickVisitorCascade_DespawnDispatches(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := time.Now()
	// Seed a visitor whose stay window has already passed.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		v := &sim.Actor{
			ID:          "test-visitor",
			DisplayName: "Test Visitor",
			Kind:        sim.KindNPCShared,
			LLMAgent:    sim.VisitorAgentName,
			Pos:         sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			Needs:       sim.SeedVisitorNeedsForTest(),
			Inventory:   map[sim.ItemKind]int{},
			VisitorState: &sim.VisitorState{
				Archetype:   "peddler",
				Origin:      "Boston",
				Disposition: "weary",
				ExpiresAt:   now.Add(-1 * time.Minute),
			},
			State: sim.StateIdle,
		}
		world.Actors["test-visitor"] = v
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed visitor: %v", err)
	}

	r := rand.New(rand.NewSource(7))
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: r}))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	tm := res.(sim.VisitorCascadeTelemetry)
	if tm.DespawnsStarted != 1 {
		t.Errorf("DespawnsStarted = %d, want 1", tm.DespawnsStarted)
	}

	// LeaveDispatched should be set.
	snap := w.Published()
	got, ok := snap.Actors["test-visitor"]
	if !ok {
		t.Fatal("visitor missing post-despawn (should not be removed before grace)")
	}
	if !got.VisitorState.LeaveDispatched {
		t.Error("LeaveDispatched not set")
	}
}

// TestTickVisitorCascade_CleanupRemovesAndEmits verifies a visitor past
// ExpiresAt + grace is removed from World.Actors and ActorDeparted fires.
func TestTickVisitorCascade_CleanupRemovesAndEmits(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := time.Now()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		v := &sim.Actor{
			ID:          "expired-visitor",
			DisplayName: "Elias Drum the peddler",
			Kind:        sim.KindNPCShared,
			LLMAgent:    sim.VisitorAgentName,
			Pos:         sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5},
			Needs:       sim.SeedVisitorNeedsForTest(),
			VisitorState: &sim.VisitorState{
				Archetype:       "peddler",
				ExpiresAt:       now.Add(-time.Duration(sim.VisitorCleanupGraceMinutes+1) * time.Minute),
				LeaveDispatched: true,
			},
		}
		world.Actors["expired-visitor"] = v
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed expired visitor: %v", err)
	}
	// Rebuild secondary indices so the outdoor-actors set includes the
	// freshly-seeded row before cleanup runs (verifies the delete path
	// actually removes from the index rather than no-opping on a
	// never-present key).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.RebuildIndicesForTest(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("rebuild indices: %v", err)
	}

	var captured *sim.ActorDeparted
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		if d, ok := evt.(*sim.ActorDeparted); ok {
			captured = d
		}
	}))

	r := rand.New(rand.NewSource(0))
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: r}))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	tm := res.(sim.VisitorCascadeTelemetry)
	if tm.CleanedUp != 1 {
		t.Errorf("CleanedUp = %d, want 1", tm.CleanedUp)
	}
	snap := w.Published()
	if _, ok := snap.Actors["expired-visitor"]; ok {
		t.Error("expired visitor still in snapshot after cleanup")
	}
	if captured == nil {
		t.Fatal("ActorDeparted not emitted")
	}
	if captured.ActorID != "expired-visitor" {
		t.Errorf("event ActorID = %q, want expired-visitor", captured.ActorID)
	}
	if captured.VisitorContext == nil {
		t.Error("event VisitorContext is nil")
	}
}

// --- Cross-cascade skips ---------------------------------------------------

// TestRecordInteraction_SkipsVisitor verifies the visitor skip in
// RecordInteraction — neither side accumulates relationship rows when one
// is a transient visitor.
func TestRecordInteraction_SkipsVisitor(t *testing.T) {
	vw := newVisitorWorld()
	w, cancel := vw.load(t)
	defer cancel()
	at := time.Now()
	// Seed Hannah (persistent shared-VA) + a visitor.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"] = &sim.Actor{
			ID: "hannah", DisplayName: "Hannah", Kind: sim.KindNPCShared,
		}
		world.Actors["visitor"] = &sim.Actor{
			ID: "visitor", DisplayName: "Elias the peddler", Kind: sim.KindNPCShared,
			VisitorState: &sim.VisitorState{Archetype: "peddler", ExpiresAt: at.Add(time.Hour)},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed actors: %v", err)
	}

	// Hannah heard the visitor — should be a no-op.
	if _, err := w.Send(sim.RecordInteraction("hannah", "visitor", sim.InteractionHeard, "hello", at)); err != nil {
		t.Fatalf("RecordInteraction hannah→visitor: %v", err)
	}
	// Visitor heard Hannah — should also be a no-op.
	if _, err := w.Send(sim.RecordInteraction("visitor", "hannah", sim.InteractionHeard, "back", at)); err != nil {
		t.Fatalf("RecordInteraction visitor→hannah: %v", err)
	}
	snap := w.Published()
	if h := snap.Actors["hannah"]; h != nil && len(h.Relationships) != 0 {
		t.Errorf("Hannah accumulated %d Relationships, want 0", len(h.Relationships))
	}
	if v := snap.Actors["visitor"]; v != nil && len(v.Relationships) != 0 {
		t.Errorf("Visitor accumulated %d Relationships, want 0", len(v.Relationships))
	}
}

// TestFindConsolidationCandidates_SkipsVisitor verifies the C1 cascade
// gate skips a visitor even if a Relationship row somehow exists.
func TestFindConsolidationCandidates_SkipsVisitor(t *testing.T) {
	vw := newVisitorWorld()
	w, cancel := vw.load(t)
	defer cancel()
	at := time.Now()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		v := &sim.Actor{
			ID: "visitor", DisplayName: "Elias", Kind: sim.KindNPCShared,
			VisitorState: &sim.VisitorState{ExpiresAt: at.Add(time.Hour)},
		}
		// Inject a stray relationship to make sure the skip kicks even
		// when one exists.
		v.Relationships = map[sim.ActorID]*sim.Relationship{
			"peer": {
				SalientFacts: []sim.SalientFact{sim.NewSalientFact(at, sim.InteractionHeard, "x")},
				CreatedAt:    at,
			},
		}
		world.Actors["visitor"] = v
		world.Actors["peer"] = &sim.Actor{ID: "peer", DisplayName: "Peer", Kind: sim.KindNPCShared}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := w.Send(sim.FindConsolidationCandidates(at.Add(48*time.Hour), 10))
	if err != nil {
		t.Fatalf("FindConsolidationCandidates: %v", err)
	}
	got := res.([]sim.ConsolidationCandidate)
	if len(got) != 0 {
		t.Errorf("got %d candidates, want 0 (visitor skipped)", len(got))
	}
}

// TestFindNarrativeConsolidationCandidates_SkipsVisitor verifies the C2
// cascade gate skips visitors.
func TestFindNarrativeConsolidationCandidates_SkipsVisitor(t *testing.T) {
	vw := newVisitorWorld()
	w, cancel := vw.load(t)
	defer cancel()
	at := time.Now()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["visitor"] = &sim.Actor{
			ID: "visitor", DisplayName: "Elias", Kind: sim.KindNPCShared,
			VisitorState: &sim.VisitorState{ExpiresAt: at.Add(time.Hour)},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := w.Send(sim.FindNarrativeConsolidationCandidates(at, 10))
	if err != nil {
		t.Fatalf("FindNarrativeConsolidationCandidates: %v", err)
	}
	got := res.([]sim.NarrativeCandidate)
	if len(got) != 0 {
		t.Errorf("got %d candidates, want 0 (visitor skipped)", len(got))
	}
}

// TestEvaluateIdleBackstop_SkipsVisitor verifies idle-backstop scope
// excludes visitors.
func TestEvaluateIdleBackstop_SkipsVisitor(t *testing.T) {
	vw := newVisitorWorld()
	w, cancel := vw.load(t)
	defer cancel()
	now := time.Now()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		// Anchor LoadedAt far in the past so quiet > threshold.
		world.LoadedAt = now.Add(-24 * time.Hour)
		world.Actors["visitor"] = &sim.Actor{
			ID: "visitor", DisplayName: "Elias", Kind: sim.KindNPCShared,
			VisitorState: &sim.VisitorState{ExpiresAt: now.Add(time.Hour)},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := w.Send(sim.EvaluateIdleBackstop(now))
	if err != nil {
		t.Fatalf("EvaluateIdleBackstop: %v", err)
	}
	tm := res.(sim.IdleBackstopTelemetry)
	if tm.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0 (visitor must not get idle backstop)", tm.Stamped)
	}
	// Visitor should be counted in SkippedScope.
	if tm.SkippedScope < 1 {
		t.Errorf("SkippedScope = %d, want >= 1", tm.SkippedScope)
	}
}
