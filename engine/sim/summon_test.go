package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// summon_test.go — ZBBS-HOME-311. Exercises the summon messenger-errand
// state machine end to end: the dispatch happy-path through every state to
// done, the refusal branch, each ActorArrived-driven and chat-pause-driven
// transition, messenger selection (free/busy/none), and the pre-check
// rejections. The bounded-membership invariant — the errand map is empty
// after EVERY terminal path — is asserted in every terminal case.
//
// The machine is driven synchronously: walk legs are advanced by synthesizing
// ActorArrived via sim.EmitForTest (the subscriber reads only ActorID +
// MovementAttemptID, never the actor's tile, so no real locomotion is
// needed), and the two chat-pause beats are fired via the
// RunSummon*ForTest export-test drivers (the AfterFunc bodies run inline).

const (
	stDispatched          = "dispatched"
	stSummonerAtPoint     = "summoner_at_point"
	stMessengerToTarget   = "messenger_to_target"
	stMessengerReturning  = "messenger_returning"
	stMessengerToSummoner = "messenger_to_summoner"
)

// buildSummonWorld seeds a running world for summon tests:
//
//   - all-grass terrain, walkable everywhere.
//   - "square": a summon_point-tagged object + backing structure at a
//     reachable spot.
//   - "summoner": a VA-backed NPC (LLMAgent set) parked at the pad origin.
//   - "target": a plain NPC near the square.
//   - "courier": a non-VA NPC carrying the messenger attribute (the only
//     free messenger by default).
//
// Returns the running world + cancel. The summon subscriber is registered.
func buildSummonWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	return buildSummonWorldOpt(t, true)
}

// buildSummonWorldOpt seeds the summon test world. pointBacksStructure controls
// whether the summon_point object ALSO gets a backing Structure row: true is the
// original anchor shape (structure-visit rendezvous); false exercises LLM-323
// gate 3 — a bare summon_point placement with no Structure shell, which the
// pre-LLM-323 DispatchSummon rejected as "cannot be reached" and which now walks
// via an object-visit instead.
func buildSummonWorldOpt(t *testing.T, pointBacksStructure bool) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"obelisk": {ID: "obelisk", Category: "structure"}, // doorless — visit only, fine for summon point
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"square": {
			ID:          "square",
			AssetID:     "obelisk",
			Pos:         sim.WorldPos{X: 320, Y: 320},
			DisplayName: "the town square",
			Tags:        []string{sim.SummonPointTag},
		},
	})
	if pointBacksStructure {
		handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
			"square": {ID: "square", DisplayName: "the town square"},
		})
	}
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"summoner": {ID: "summoner", DisplayName: "Goodwife Bishop", LLMAgent: "va-bishop", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
		"target":   {ID: "target", DisplayName: "John Proctor", Pos: sim.TilePos{X: sim.PadX + 3, Y: sim.PadY + 3}},
		"courier":  {ID: "courier", DisplayName: "the boy", Pos: sim.TilePos{X: sim.PadX + 1, Y: sim.PadY}, Attributes: map[string][]byte{sim.AttrMessenger: {}}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	sim.RegisterSummonSubscriber(w)
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// dispatchSummon runs DispatchSummon and returns the new errand id.
func dispatchSummon(t *testing.T, w *sim.World, summoner, target sim.ActorID, reason string) sim.ErrandID {
	t.Helper()
	res, err := w.Send(sim.DispatchSummon(summoner, string(target), reason, time.Now().UTC()))
	if err != nil {
		t.Fatalf("DispatchSummon(%q->%q): %v", summoner, target, err)
	}
	id, ok := res.(sim.ErrandID)
	if !ok {
		t.Fatalf("DispatchSummon returned %T, want sim.ErrandID", res)
	}
	return id
}

// arriveLeg synthesizes the ActorArrived for the errand's current leg —
// the actor it's waiting on, carrying the leg's tracked MovementAttemptID —
// so the machine advances. Runs the emit on the world goroutine.
func arriveLeg(t *testing.T, w *sim.World, id sim.ErrandID, actor sim.ActorID) {
	t.Helper()
	attempt, ok := legAttempt(t, w, id)
	if !ok {
		t.Fatalf("errand %d gone before arriveLeg(%q)", id, actor)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:           actor,
			MovementAttemptID: attempt,
			At:                time.Now().UTC(),
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emit ActorArrived(%q): %v", actor, err)
	}
}

func errandState(t *testing.T, w *sim.World, id sim.ErrandID) (string, bool) {
	t.Helper()
	var st string
	var ok bool
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		st, ok = sim.SummonErrandStateByID(world, id)
		return nil, nil
	}})
	return st, ok
}

func errandCount(t *testing.T, w *sim.World) int {
	t.Helper()
	var n int
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		n = sim.SummonErrandCount(world)
		return nil, nil
	}})
	return n
}

func messengerOf(t *testing.T, w *sim.World, id sim.ErrandID) sim.ActorID {
	t.Helper()
	var m sim.ActorID
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		m, _ = sim.SummonErrandMessengerByID(world, id)
		return nil, nil
	}})
	return m
}

func legAttempt(t *testing.T, w *sim.World, id sim.ErrandID) (sim.MovementAttemptID, bool) {
	t.Helper()
	var a sim.MovementAttemptID
	var ok bool
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok = sim.SummonErrandLegAttemptByID(world, id)
		return nil, nil
	}})
	return a, ok
}

func runCommission(t *testing.T, w *sim.World, id sim.ErrandID) {
	t.Helper()
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.RunSummonCommissionForTest(world, id, time.Now().UTC())
		return nil, nil
	}})
}

func runDeliver(t *testing.T, w *sim.World, id sim.ErrandID) {
	t.Helper()
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.RunSummonDeliverForTest(world, id, time.Now().UTC())
		return nil, nil
	}})
}

// pendingSummonOf / summonRefusalOf read the per-actor perception cues off
// the published snapshot (the surface perception build reads).
func pendingSummonOf(t *testing.T, w *sim.World, actor sim.ActorID) *sim.PendingSummon {
	t.Helper()
	snap := w.Published()
	a := snap.Actors[actor]
	if a == nil {
		return nil
	}
	return a.PendingSummon
}

func summonRefusalOf(t *testing.T, w *sim.World, actor sim.ActorID) *sim.SummonRefusal {
	t.Helper()
	snap := w.Published()
	a := snap.Actors[actor]
	if a == nil {
		return nil
	}
	return a.SummonRefusal
}

// TestSummonHappyPath drives an errand through every state to done and
// asserts the target-side perception cue lands and the map empties.
func TestSummonHappyPath(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	id := dispatchSummon(t, w, "summoner", "target", "There is news of the trial.")

	if st, _ := errandState(t, w, id); st != stDispatched {
		t.Fatalf("after dispatch: state %q, want %q", st, stDispatched)
	}
	if m := messengerOf(t, w, id); m != "courier" {
		t.Fatalf("selected messenger %q, want courier", m)
	}

	// Leg 1: summoner arrives at the summon point → messenger dispatched.
	arriveLeg(t, w, id, "summoner")
	if st, _ := errandState(t, w, id); st != stSummonerAtPoint {
		t.Fatalf("after summoner arrival: state %q, want %q", st, stSummonerAtPoint)
	}

	// Leg 2: messenger arrives at the point → messenger_at_point (awaiting beat).
	arriveLeg(t, w, id, "courier")
	if st, _ := errandState(t, w, id); st != "messenger_at_point" {
		t.Fatalf("after messenger arrival at point: state %q, want messenger_at_point", st)
	}

	// Commissioning beat → messenger dispatched to target.
	runCommission(t, w, id)
	if st, _ := errandState(t, w, id); st != stMessengerToTarget {
		t.Fatalf("after commission: state %q, want %q", st, stMessengerToTarget)
	}

	// Leg 3: messenger arrives at target → messenger_at_target (awaiting beat).
	arriveLeg(t, w, id, "courier")
	if st, _ := errandState(t, w, id); st != "messenger_at_target" {
		t.Fatalf("after messenger arrival at target: state %q, want messenger_at_target", st)
	}

	// Delivery beat → delivery cue stamped + messenger heads home.
	runDeliver(t, w, id)
	if st, _ := errandState(t, w, id); st != stMessengerReturning {
		t.Fatalf("after deliver: state %q, want %q", st, stMessengerReturning)
	}
	if cue := pendingSummonOf(t, w, "target"); cue == nil {
		t.Fatal("target has no PendingSummon cue after delivery")
	} else {
		if cue.SummonerName != "Goodwife Bishop" {
			t.Errorf("PendingSummon.SummonerName = %q, want Goodwife Bishop", cue.SummonerName)
		}
		if cue.Place != "the town square" {
			t.Errorf("PendingSummon.Place = %q, want the town square", cue.Place)
		}
	}

	// Leg 4: messenger arrives home → done; map empties.
	arriveLeg(t, w, id, "courier")
	if _, ok := errandState(t, w, id); ok {
		t.Fatal("errand still present after final arrival")
	}
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand map has %d entries after done, want 0 (bounded membership)", n)
	}
}

// TestSummonRefusalBranch: the target vanishes before the commissioning
// beat, so the messenger turns around and delivers the refusal to the
// summoner. Asserts the refusal cue + empty map.
func TestSummonRefusalBranch(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	id := dispatchSummon(t, w, "summoner", "target", "")

	arriveLeg(t, w, id, "summoner")
	arriveLeg(t, w, id, "courier")

	// Remove the target before the commissioning beat resolves its location.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors, "target")
		return nil, nil
	}})

	runCommission(t, w, id)
	if st, _ := errandState(t, w, id); st != stMessengerToSummoner {
		t.Fatalf("after commission with missing target: state %q, want %q", st, stMessengerToSummoner)
	}

	// Messenger returns to summoner → refusal delivered; map empties.
	arriveLeg(t, w, id, "courier")
	if _, ok := errandState(t, w, id); ok {
		t.Fatal("errand still present after refusal return")
	}
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand map has %d entries after refusal, want 0 (bounded membership)", n)
	}
	// The target was deleted, so the display-name lookup falls back to the
	// raw id (defensive — a deleted actor has no DisplayName to resolve).
	if cue := summonRefusalOf(t, w, "summoner"); cue == nil {
		t.Fatal("summoner has no SummonRefusal cue after refusal")
	} else if cue.TargetName != "target" {
		t.Errorf("SummonRefusal.TargetName = %q, want target (id fallback for deleted actor)", cue.TargetName)
	}
}

// TestSummonRejectSelf: summoning yourself is rejected, no errand created.
func TestSummonRejectSelf(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()
	if _, err := w.Send(sim.DispatchSummon("summoner", "summoner", "", time.Now().UTC())); err == nil {
		t.Fatal("DispatchSummon(self) did not error")
	}
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand created on self-summon: %d", n)
	}
}

// TestSummonRejectUnknownTarget: summoning a nonexistent actor is rejected.
func TestSummonRejectUnknownTarget(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()
	if _, err := w.Send(sim.DispatchSummon("summoner", "ghost", "", time.Now().UTC())); err == nil {
		t.Fatal("DispatchSummon(unknown target) did not error")
	}
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand created for unknown target: %d", n)
	}
}

// TestSummonRejectNoSummonPoint: with no summon_point object, dispatch is
// rejected.
func TestSummonRejectNoSummonPoint(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"summoner": {ID: "summoner", DisplayName: "S", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
		"target":   {ID: "target", DisplayName: "T", Pos: sim.TilePos{X: sim.PadX + 2, Y: sim.PadY}},
		"courier":  {ID: "courier", DisplayName: "C", Pos: sim.TilePos{X: sim.PadX + 1, Y: sim.PadY}, Attributes: map[string][]byte{sim.AttrMessenger: {}}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	sim.RegisterSummonSubscriber(w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", time.Now().UTC())); err == nil {
		t.Fatal("DispatchSummon with no summon_point did not error")
	}
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand created with no summon point: %d", n)
	}
}

// TestSummonMessengerSelection_NoneFree: when the only messenger is a VA
// (LLMAgent set), no free messenger exists → rejection.
func TestSummonMessengerSelection_NoneFree(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"obelisk": {ID: "obelisk", Category: "structure"}})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"square": {ID: "square", AssetID: "obelisk", Pos: sim.WorldPos{X: 320, Y: 320}, Tags: []string{sim.SummonPointTag}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{"square": {ID: "square", DisplayName: "square"}})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"summoner": {ID: "summoner", DisplayName: "S", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
		"target":   {ID: "target", DisplayName: "T", Pos: sim.TilePos{X: sim.PadX + 2, Y: sim.PadY}},
		// VA-backed messenger — ineligible (we don't burn LLM ticks on errands).
		"vacourier": {ID: "vacourier", DisplayName: "VA", LLMAgent: "va-x", Pos: sim.TilePos{X: sim.PadX + 1, Y: sim.PadY}, Attributes: map[string][]byte{sim.AttrMessenger: {}}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	sim.RegisterSummonSubscriber(w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", time.Now().UTC())); err == nil {
		t.Fatal("DispatchSummon with no free messenger did not error")
	}
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand created with no free messenger: %d", n)
	}
}

// TestSummonMessengerSelection_Busy: a second summoner can't reuse a
// messenger already running an errand (one active errand per messenger).
// With only one messenger, the second dispatch is rejected.
func TestSummonMessengerSelection_Busy(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	// First errand claims the only courier.
	id1 := dispatchSummon(t, w, "summoner", "target", "")
	if messengerOf(t, w, id1) != "courier" {
		t.Fatal("first errand did not claim courier")
	}

	// A second summoner with the courier busy → no free messenger.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["summoner2"] = &sim.Actor{ID: "summoner2", DisplayName: "S2", LLMAgent: "va-2", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY + 1}}
		return nil, nil
	}})
	if _, err := w.Send(sim.DispatchSummon("summoner2", "target", "", time.Now().UTC())); err == nil {
		t.Fatal("second DispatchSummon succeeded while the only messenger was busy")
	}
	// First errand still present, unaffected.
	if n := errandCount(t, w); n != 1 {
		t.Fatalf("errand count %d after busy-messenger rejection, want 1", n)
	}
}

// TestSummonRejectDoubleDispatch: a summoner with an in-flight errand can't
// start a second.
func TestSummonRejectDoubleDispatch(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	dispatchSummon(t, w, "summoner", "target", "")
	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", time.Now().UTC())); err == nil {
		t.Fatal("second DispatchSummon by the same summoner did not error")
	}
	if n := errandCount(t, w); n != 1 {
		t.Fatalf("errand count %d after double-dispatch attempt, want 1", n)
	}
}

// TestSummonStaleArrivalIgnored: an ActorArrived carrying a mismatched
// MovementAttemptID does not advance the machine.
func TestSummonStaleArrivalIgnored(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	id := dispatchSummon(t, w, "summoner", "target", "")

	// Synthesize an arrival for the summoner with a bogus attempt id.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:           "summoner",
			MovementAttemptID: 999999,
			At:                time.Now().UTC(),
		})
		return nil, nil
	}})
	if st, _ := errandState(t, w, id); st != stDispatched {
		t.Fatalf("stale arrival advanced the machine: state %q, want %q", st, stDispatched)
	}
}

// TestSummonCuesFadeOnResponse: a summon cue persists across ticks/events and
// fades only when its HOLDER responds — commits a move_to / speak / take_break
// (v1's drop-on-response semantics, NOT drop-on-any-tick, so a summoned NPC
// that ticks for an unrelated reason doesn't forget the summons). An unrelated
// actor's event must leave the cue alone.
func TestSummonCuesFadeOnResponse(t *testing.T) {
	// Unit: the clear helper nils both fields and is nil-safe.
	a := &sim.Actor{
		ID:            "x",
		PendingSummon: &sim.PendingSummon{SummonerName: "S", Place: "p"},
		SummonRefusal: &sim.SummonRefusal{TargetName: "T"},
	}
	sim.ClearSummonCuesForTest(a)
	if a.PendingSummon != nil || a.SummonRefusal != nil {
		t.Error("clearSummonCues did not nil both cues")
	}
	sim.ClearSummonCuesForTest(nil) // nil-safe

	// Integration: the response-fade subscriber clears a holder's cue on its
	// OWN move/speak/break event, and leaves it for another actor's event.
	w, cancel := buildSummonWorld(t)
	defer cancel()

	setCue := func(actor sim.ActorID) {
		w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors[actor].PendingSummon = &sim.PendingSummon{SummonerName: "Goodwife Bishop", Place: "the town square"}
			return nil, nil
		}})
	}
	hasCue := func(actor sim.ActorID) bool {
		var has bool
		w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			has = world.Actors[actor].PendingSummon != nil
			return nil, nil
		}})
		return has
	}
	emit := func(evt sim.Event) {
		w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			sim.EmitForTest(world, evt)
			return nil, nil
		}})
	}

	// Another actor's Spoke must NOT clear the target's cue.
	setCue("target")
	emit(&sim.Spoke{SpeakerID: "summoner", At: time.Now().UTC()})
	if !hasCue("target") {
		t.Error("target's cue cleared by an unrelated actor's Spoke")
	}
	// The holder's own move_to (ActorMoveStarted) clears it — the answer-walk.
	emit(&sim.ActorMoveStarted{ActorID: "target", At: time.Now().UTC()})
	if hasCue("target") {
		t.Error("target's cue not cleared on its own ActorMoveStarted")
	}
	// Speak clears it.
	setCue("target")
	emit(&sim.Spoke{SpeakerID: "target", At: time.Now().UTC()})
	if hasCue("target") {
		t.Error("target's cue not cleared on its own Spoke")
	}
	// take_break clears it.
	setCue("target")
	emit(&sim.TookBreak{ActorID: "target", At: time.Now().UTC()})
	if hasCue("target") {
		t.Error("target's cue not cleared on its own TookBreak")
	}
}

// TestSummonArrivalWarrantSuppression: the work-domain seam. While an errand
// is active, both participants (summoner + messenger) are suppressed from the
// arrival-warrant stamp; an uninvolved actor is not. After the errand
// terminates, suppression lifts for everyone.
func TestSummonArrivalWarrantSuppression(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	// No errand yet → nobody suppressed.
	suppressed := func(actor sim.ActorID) bool {
		var b bool
		w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			b = sim.SuppressArrivalWarrantForTest(world, actor)
			return nil, nil
		}})
		return b
	}
	if suppressed("summoner") {
		t.Fatal("summoner suppressed with no active errand")
	}

	id := dispatchSummon(t, w, "summoner", "target", "")
	if !suppressed("summoner") {
		t.Error("summoner NOT suppressed during active errand — would LLM-tick and wander off")
	}
	if !suppressed("courier") {
		t.Error("messenger NOT suppressed during active errand")
	}
	if suppressed("target") {
		t.Error("uninvolved target suppressed during errand")
	}

	// Drive the errand to done and confirm suppression lifts.
	arriveLeg(t, w, id, "summoner")
	arriveLeg(t, w, id, "courier")
	runCommission(t, w, id)
	arriveLeg(t, w, id, "courier")
	runDeliver(t, w, id)
	arriveLeg(t, w, id, "courier")
	if errandCount(t, w) != 0 {
		t.Fatal("errand did not terminate")
	}
	if suppressed("summoner") {
		t.Error("summoner still suppressed after errand finished — leaked errand would dead-lock the NPC")
	}
}

// TestSummonAbandonOnMessengerGone: the messenger is removed after dispatch
// but before the summoner arrives, so the second-leg dispatch (MoveActor on a
// missing courier) fails. The errand abandons cleanly (map empties) rather
// than dangling — exercising the abandon terminal path.
func TestSummonAbandonOnMessengerGone(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	id := dispatchSummon(t, w, "summoner", "target", "")
	// Delete the courier, then arrive the summoner: the second-leg dispatch
	// (MoveActor on a missing courier) fails → finishErrand abandon path.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors, "courier")
		return nil, nil
	}})
	arriveLeg(t, w, id, "summoner")
	if _, ok := errandState(t, w, id); ok {
		t.Fatal("errand still present after messenger-unreachable abandon")
	}
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand map has %d entries after abandon, want 0 (bounded membership)", n)
	}
}

// TestSummonErrand_TTLRemovesStuckErrand: the load-bearing leak guard. An
// errand whose in-flight leg is superseded (or otherwise stalls) never gets a
// matching ActorArrived, so it would sit in the map forever — and because the
// arrival-warrant suppression hook keys off membership with no time bound, the
// summoner's warrants would be suppressed forever (a dead NPC). The per-errand
// TTL sweeps any errand still in flight at the cap, lifting suppression. This
// drives the TTL body directly on a never-advanced errand.
func TestSummonErrand_TTLRemovesStuckErrand(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	suppressed := func(actor sim.ActorID) bool {
		var b bool
		w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			b = sim.SuppressArrivalWarrantForTest(world, actor)
			return nil, nil
		}})
		return b
	}

	id := dispatchSummon(t, w, "summoner", "target", "")
	// Never advance any leg — simulate a superseded/stalled first leg.
	if st, ok := errandState(t, w, id); !ok || st != stDispatched {
		t.Fatalf("errand state = %q ok=%v, want %q (in flight)", st, ok, stDispatched)
	}
	if !suppressed("summoner") {
		t.Fatal("summoner should be suppressed while the errand is in flight")
	}

	// Fire the TTL: the stuck errand must be removed, lifting suppression.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.RunSummonErrandTTLForTest(world, id)
		return nil, nil
	}})
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand count after TTL = %d, want 0 (stuck errand must be swept)", n)
	}
	if suppressed("summoner") {
		t.Fatal("suppression must lift once the stuck errand is swept — else the summoner is dead forever")
	}

	// TTL on an already-gone errand is a harmless no-op.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.RunSummonErrandTTLForTest(world, id)
		return nil, nil
	}})
}

// TestSummonMessengerSelection_ExcludesSummonerAndTarget: the summoner and the
// target must never be chosen as the messenger. A self-messenger can't be
// observed in the messenger role (errandForArrival resolves the summoner role
// first) and would strand the machine; a target-messenger would be sent to
// fetch itself. With the courier stripped, the only messenger-eligible actor is
// first the target, then the summoner — dispatch must reject in both cases.
func TestSummonMessengerSelection_ExcludesSummonerAndTarget(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	// Only the TARGET carries the messenger attribute now → must be excluded.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors["courier"].Attributes, sim.AttrMessenger)
		world.Actors["target"].Attributes = map[string][]byte{sim.AttrMessenger: {}}
		return nil, nil
	}})
	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", time.Now().UTC())); err == nil {
		t.Fatal("dispatch should reject: the only messenger candidate is the target (self-fetch)")
	}

	// Only the SUMMONER carries it (and is made non-VA so the VA filter doesn't
	// mask the exclusion) → must be excluded too.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors["target"].Attributes, sim.AttrMessenger)
		world.Actors["summoner"].LLMAgent = ""
		world.Actors["summoner"].Attributes = map[string][]byte{sim.AttrMessenger: {}}
		return nil, nil
	}})
	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", time.Now().UTC())); err == nil {
		t.Fatal("dispatch should reject: the only messenger candidate is the summoner itself")
	}
}

// buildResolutionWorld seeds a running world with a roster tailored to exercise
// target-name resolution (LLM-323 gate 1): a normal villager, one whose display
// name legitimately begins with an article, and a duplicate-name pair for the
// ambiguity branch. No summon point / messenger — resolution never dispatches.
func buildResolutionWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"a1":       {ID: "a1", DisplayName: "Ezekiel Crane", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
		"a2":       {ID: "a2", DisplayName: "the boy", Pos: sim.TilePos{X: sim.PadX + 1, Y: sim.PadY}},
		"dup1":     {ID: "dup1", DisplayName: "John Smith", Pos: sim.TilePos{X: sim.PadX + 2, Y: sim.PadY}},
		"dup2":     {ID: "dup2", DisplayName: "John Smith", Pos: sim.TilePos{X: sim.PadX + 3, Y: sim.PadY}},
		"nameless": {ID: "nameless", DisplayName: "", Pos: sim.TilePos{X: sim.PadX + 4, Y: sim.PadY}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// resolveTarget runs resolveSummonTarget on the world goroutine (it reads
// w.Actors) and returns its tri-state result.
func resolveTarget(t *testing.T, w *sim.World, raw string) (sim.ActorID, bool, bool) {
	t.Helper()
	var id sim.ActorID
	var ok, ambiguous bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		id, ok, ambiguous = sim.ResolveSummonTargetForTest(world, raw)
		return nil, nil
	}}); err != nil {
		t.Fatalf("resolve %q: %v", raw, err)
	}
	return id, ok, ambiguous
}

// TestResolveSummonTarget — LLM-323 gate 1. The summon tool invites a display
// name, so DispatchSummon must resolve name → actor id. Covers the exact-id fast
// path, case/punctuation/quote tolerance, the leading-article-kept case (a proper
// name never carries one; a display name that does must still match verbatim),
// the unknown name, the empty query, and the ambiguous duplicate.
func TestResolveSummonTarget(t *testing.T) {
	w, cancel := buildResolutionWorld(t)
	defer cancel()

	cases := []struct {
		name          string
		raw           string
		wantID        sim.ActorID
		wantOK        bool
		wantAmbiguous bool
	}{
		{"display name", "Ezekiel Crane", "a1", true, false},
		{"case-insensitive", "ezekiel crane", "a1", true, false},
		{"trailing period", "Ezekiel Crane.", "a1", true, false},
		{"surrounding quotes + comma", `"Ezekiel Crane,"`, "a1", true, false},
		{"leading article kept", "the boy", "a2", true, false},
		{"exact id fast path", "a1", "a1", true, false},
		{"unknown name", "Nobody Here", "", false, false},
		{"empty query", "   ", "", false, false},
		{"punctuation-only query never matches a nameless actor", ".", "", false, false},
		{"ambiguous duplicate", "John Smith", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok, ambiguous := resolveTarget(t, w, tc.raw)
			if ok != tc.wantOK || ambiguous != tc.wantAmbiguous || (tc.wantOK && id != tc.wantID) {
				t.Fatalf("resolveSummonTarget(%q) = (%q, ok=%v, ambiguous=%v); want (%q, ok=%v, ambiguous=%v)",
					tc.raw, id, ok, ambiguous, tc.wantID, tc.wantOK, tc.wantAmbiguous)
			}
		})
	}
}

// TestDispatchSummon_ByDisplayName — LLM-323 gate 1 end to end: a dispatch that
// names the target by DISPLAY NAME (not the UUID key) resolves and starts an
// errand, where before LLM-323 it died at the exact-id lookup.
func TestDispatchSummon_ByDisplayName(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	res, err := w.Send(sim.DispatchSummon("summoner", "John Proctor", "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("DispatchSummon by display name: %v", err)
	}
	if _, ok := res.(sim.ErrandID); !ok {
		t.Fatalf("DispatchSummon by display name returned %T, want sim.ErrandID", res)
	}
}

// TestSummonPointDestination — LLM-323 gate 3. The rendezvous resolves to a
// structure-visit when the summon_point object backs a Structure, and to an
// object-visit when it is a bare placement (the live village's case).
func TestSummonPointDestination(t *testing.T) {
	t.Run("structure-backed → structure-visit", func(t *testing.T) {
		w, cancel := buildSummonWorldOpt(t, true)
		defer cancel()
		dest := pointDestination(t, w, "square")
		if dest.Kind != sim.MoveDestinationStructureVisit {
			t.Fatalf("kind = %q, want structure_visit", dest.Kind)
		}
	})
	t.Run("bare object → object-visit", func(t *testing.T) {
		w, cancel := buildSummonWorldOpt(t, false)
		defer cancel()
		dest := pointDestination(t, w, "square")
		if dest.Kind != sim.MoveDestinationObjectVisit {
			t.Fatalf("kind = %q, want object_visit", dest.Kind)
		}
	})
}

// pointDestination runs summonPointDestination on the world goroutine and fails
// if the point can't be resolved at all.
func pointDestination(t *testing.T, w *sim.World, pointID sim.VillageObjectID) sim.MoveDestination {
	t.Helper()
	var dest sim.MoveDestination
	var ok bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		dest, ok = sim.SummonPointDestinationForTest(world, pointID)
		return nil, nil
	}}); err != nil {
		t.Fatalf("resolve point %q: %v", pointID, err)
	}
	if !ok {
		t.Fatalf("summonPointDestination(%q) = !ok", pointID)
	}
	return dest
}

// TestDispatchSummon_BarePointStillDispatches — LLM-323 gate 3 end to end: with a
// summon_point that has no backing Structure (the live village's state), dispatch
// no longer rejects with "the summoning place cannot be reached" — it walks the
// summoner to the object via an object-visit and starts the errand.
func TestDispatchSummon_BarePointStillDispatches(t *testing.T) {
	w, cancel := buildSummonWorldOpt(t, false)
	defer cancel()

	res, err := w.Send(sim.DispatchSummon("summoner", "target", "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("DispatchSummon with a bare (structure-less) summon point: %v", err)
	}
	id, ok := res.(sim.ErrandID)
	if !ok {
		t.Fatalf("DispatchSummon returned %T, want sim.ErrandID", res)
	}
	if st, ok := errandState(t, w, id); !ok || st != stDispatched {
		t.Fatalf("errand state = (%q, ok=%v), want %q", st, ok, stDispatched)
	}
}
