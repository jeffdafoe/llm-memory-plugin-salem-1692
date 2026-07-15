package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// summon_test.go — ZBBS-HOME-311, reworked LLM-414. Exercises the summon
// messenger-errand state machine end to end: the dispatch happy-path through
// every state to the meeting, the refusal branch, each ActorArrived-driven
// and chat-pause-driven transition, messenger selection (free/busy/none),
// and the pre-check rejections. The bounded-membership invariant — the
// errand map is empty after EVERY terminal path — is asserted in every
// terminal case.
//
// The machine is driven synchronously: walk legs are advanced by synthesizing
// ActorArrived via sim.EmitForTest (the subscriber reads only ActorID +
// MovementAttemptID / DestStructureID, never the actor's tile, so no real
// locomotion is needed), and the two chat-pause beats are fired via the
// RunSummon*ForTest export-test drivers (the AfterFunc bodies run inline).

const (
	stDispatched          = "dispatched"
	stSummonerAtPoint     = "summoner_at_point"
	stMessengerToTarget   = "messenger_to_target"
	stAwaitingTarget      = "awaiting_target"
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

// dispatchSummon runs DispatchSummon (no say beat) and returns the new
// errand id.
func dispatchSummon(t *testing.T, w *sim.World, summoner, target sim.ActorID, reason string) sim.ErrandID {
	t.Helper()
	res, err := w.Send(sim.DispatchSummon(summoner, string(target), reason, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("DispatchSummon(%q->%q): %v", summoner, target, err)
	}
	id, ok := res.(sim.ErrandID)
	if !ok {
		t.Fatalf("DispatchSummon returned %T, want sim.ErrandID", res)
	}
	return id
}

// arriveTargetAt synthesizes the TARGET's model-driven arrival at a
// destination structure — the awaiting_target leg matches on destination,
// not on a tracked MovementAttemptID (the errand never sees the target's own
// move_to attempt).
func arriveTargetAt(t *testing.T, w *sim.World, target sim.ActorID, dest sim.StructureID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:           target,
			DestStructureID:   dest,
			MovementAttemptID: 424242,
			At:                time.Now().UTC(),
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emit target ActorArrived at %q: %v", dest, err)
	}
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

	// Delivery beat → delivery cue stamped, messenger freed (walks home
	// untracked), errand parks awaiting the target's answer (LLM-414).
	runDeliver(t, w, id)
	if st, _ := errandState(t, w, id); st != stAwaitingTarget {
		t.Fatalf("after deliver: state %q, want %q", st, stAwaitingTarget)
	}
	if m := messengerOf(t, w, id); m != "" {
		t.Fatalf("messenger %q still bound to the errand after delivery — must be freed for the next summons", m)
	}
	if cue := pendingSummonOf(t, w, "target"); cue == nil {
		t.Fatal("target has no PendingSummon cue after delivery")
	} else {
		if cue.SummonerName != "Goodwife Bishop" {
			t.Errorf("PendingSummon.SummonerName = %q, want Goodwife Bishop", cue.SummonerName)
		}
		// The meet place resolves off the summoner's in-flight bell walk
		// (MoveIntent → the structure-backed square) — the summoner's own
		// place, never a separate rendezvous.
		if cue.Place != "the town square" {
			t.Errorf("PendingSummon.Place = %q, want the town square", cue.Place)
		}
		if cue.PlaceStructureID != "square" {
			t.Errorf("PendingSummon.PlaceStructureID = %q, want square", cue.PlaceStructureID)
		}
	}

	// The target's arrival ELSEWHERE is a choice, not an answer: nothing
	// advances and the cue stands.
	arriveTargetAt(t, w, "target", "somewhere-else")
	if st, _ := errandState(t, w, id); st != stAwaitingTarget {
		t.Fatalf("target arrival elsewhere advanced the errand: state %q, want %q", st, stAwaitingTarget)
	}
	if cue := pendingSummonOf(t, w, "target"); cue == nil {
		t.Fatal("cue cleared by the target's arrival elsewhere")
	}

	// The target's arrival AT the meet place answers the summons: cue
	// cleared, errand done, map empties.
	arriveTargetAt(t, w, "target", "square")
	if _, ok := errandState(t, w, id); ok {
		t.Fatal("errand still present after the target reached the meet place")
	}
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand map has %d entries after done, want 0 (bounded membership)", n)
	}
	if cue := pendingSummonOf(t, w, "target"); cue != nil {
		t.Fatal("PendingSummon cue not cleared by the meeting")
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
	if _, err := w.Send(sim.DispatchSummon("summoner", "summoner", "", "", time.Now().UTC())); err == nil {
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
	if _, err := w.Send(sim.DispatchSummon("summoner", "ghost", "", "", time.Now().UTC())); err == nil {
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

	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", "", time.Now().UTC())); err == nil {
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

	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", "", time.Now().UTC())); err == nil {
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
	if _, err := w.Send(sim.DispatchSummon("summoner2", "target", "", "", time.Now().UTC())); err == nil {
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
	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", "", time.Now().UTC())); err == nil {
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

// TestSummonCuesFadeOnResponse — the LLM-414 fade rules. PendingSummon is
// STICKY: it survives the holder's own speech ("Aye, I'm coming" is terminal
// and used to erase the summons) and its own walk (the walk toward the meet
// place IS the answer in progress, and the arrival tick needs the cue as
// scene context). It clears on take_break (an explicit settling-in), on the
// meeting, and on the errand's terminal paths. SummonRefusal keeps the v1
// posture: any own move/speak/break fades it.
func TestSummonCuesFadeOnResponse(t *testing.T) {
	// Unit: the errand-scoped clear drops only a matching errand's cue.
	a := &sim.Actor{
		ID:            "x",
		PendingSummon: &sim.PendingSummon{ErrandID: 7, SummonerName: "S", Place: "p"},
	}
	sim.ClearSummonCueForErrandForTest(a, 3)
	if a.PendingSummon == nil {
		t.Error("clearSummonCueForErrand wiped a DIFFERENT errand's cue")
	}
	sim.ClearSummonCueForErrandForTest(a, 7)
	if a.PendingSummon != nil {
		t.Error("clearSummonCueForErrand did not clear its own errand's cue")
	}
	sim.ClearSummonCueForErrandForTest(nil, 7) // nil-safe

	// Integration: the response-fade subscriber.
	w, cancel := buildSummonWorld(t)
	defer cancel()

	setCue := func(actor sim.ActorID) {
		w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors[actor].PendingSummon = &sim.PendingSummon{SummonerName: "Goodwife Bishop", Place: "the town square"}
			return nil, nil
		}})
	}
	setRefusal := func(actor sim.ActorID) {
		w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors[actor].SummonRefusal = &sim.SummonRefusal{TargetName: "T"}
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
	hasRefusal := func(actor sim.ActorID) bool {
		var has bool
		w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			has = world.Actors[actor].SummonRefusal != nil
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
	// The holder's own move does NOT clear it — the answer-walk carries it.
	emit(&sim.ActorMoveStarted{ActorID: "target", At: time.Now().UTC()})
	if !hasCue("target") {
		t.Error("target's cue cleared on its own ActorMoveStarted — the arrival tick loses its scene context")
	}
	// The holder's own speech does NOT clear it — a spoken acknowledgement
	// must not erase the summons it acknowledges.
	emit(&sim.Spoke{SpeakerID: "target", At: time.Now().UTC()})
	if !hasCue("target") {
		t.Error("target's cue cleared on its own Spoke — 'Aye, I'm coming' erased the summons")
	}
	// take_break DOES clear it — an explicit settling-in is a "not going".
	emit(&sim.TookBreak{ActorID: "target", At: time.Now().UTC()})
	if hasCue("target") {
		t.Error("target's cue not cleared on its own TookBreak")
	}

	// SummonRefusal keeps the v1 fade: any own act clears it.
	for _, tc := range []struct {
		name string
		evt  sim.Event
	}{
		{"move", &sim.ActorMoveStarted{ActorID: "summoner", At: time.Now().UTC()}},
		{"speak", &sim.Spoke{SpeakerID: "summoner", At: time.Now().UTC()}},
		{"break", &sim.TookBreak{ActorID: "summoner", At: time.Now().UTC()}},
	} {
		setRefusal("summoner")
		emit(tc.evt)
		if hasRefusal("summoner") {
			t.Errorf("SummonRefusal not cleared on the holder's own %s", tc.name)
		}
	}
}

// TestSummonArrivalWarrantSuppression: the work-domain seam, LLM-414 shape.
// The summoner is suppressed only through the bell ritual (dispatch →
// commission) — after the commission his role is over and his arrival ticks
// must run so his own steers walk him back to his business. The messenger is
// suppressed for the legs it walks and freed at delivery. The target is
// NEVER suppressed — its arrival tick at the meet place IS the greeting.
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
		t.Error("summoner NOT suppressed during the bell ritual — would LLM-tick and wander off")
	}
	if !suppressed("courier") {
		t.Error("messenger NOT suppressed during active errand")
	}
	if suppressed("target") {
		t.Error("uninvolved target suppressed during errand")
	}

	// Through the ritual: still suppressed at the point.
	arriveLeg(t, w, id, "summoner")
	arriveLeg(t, w, id, "courier")
	if !suppressed("summoner") {
		t.Error("summoner not suppressed awaiting the commission beat")
	}

	// The commission releases the summoner; the messenger walks on.
	runCommission(t, w, id)
	if suppressed("summoner") {
		t.Error("summoner still suppressed after the commission — his walk back to his business needs its arrival tick")
	}
	if !suppressed("courier") {
		t.Error("messenger not suppressed on the delivery leg")
	}

	// Delivery frees the messenger; the errand still stands (awaiting_target).
	arriveLeg(t, w, id, "courier")
	runDeliver(t, w, id)
	if st, _ := errandState(t, w, id); st != stAwaitingTarget {
		t.Fatalf("state %q, want %q", st, stAwaitingTarget)
	}
	if suppressed("courier") {
		t.Error("messenger still suppressed after delivery — must be freed for the next summons")
	}
	if suppressed("target") {
		t.Error("target suppressed while awaited — its meet-place arrival tick is the greeting")
	}

	// The meeting ends the errand; nobody is suppressed after.
	arriveTargetAt(t, w, "target", "square")
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

// TestSummonErrand_TTLClearsCueAndRecordsOutcome — LLM-414. An errand swept
// by the TTL while awaiting its target must (a) clear the target's
// PendingSummon (the steer suppression keys on it — a dangling cue would
// suppress the target's go-home steers past the errand's life) and (b) land
// in the recent-errands observability ring with outcome "expired" and its
// full state history.
func TestSummonErrand_TTLClearsCueAndRecordsOutcome(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	id := dispatchSummon(t, w, "summoner", "target", "News.")
	arriveLeg(t, w, id, "summoner")
	arriveLeg(t, w, id, "courier")
	runCommission(t, w, id)
	arriveLeg(t, w, id, "courier")
	runDeliver(t, w, id)
	if cue := pendingSummonOf(t, w, "target"); cue == nil {
		t.Fatal("target has no cue after delivery")
	}

	// The target never answers; the TTL sweeps the awaiting errand.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.RunSummonErrandTTLForTest(world, id)
		return nil, nil
	}})
	if n := errandCount(t, w); n != 0 {
		t.Fatalf("errand count after TTL = %d, want 0", n)
	}
	if cue := pendingSummonOf(t, w, "target"); cue != nil {
		t.Fatal("PendingSummon must be cleared when its errand expires — a dangling cue suppresses the target's steers forever")
	}

	// The ring records the post-mortem.
	res, err := w.Send(sim.SummonErrandsReport())
	if err != nil {
		t.Fatalf("SummonErrandsReport: %v", err)
	}
	report := res.(sim.SummonErrandsReportResult)
	if len(report.Active) != 0 {
		t.Errorf("report.Active has %d entries, want 0", len(report.Active))
	}
	if len(report.Recent) != 1 {
		t.Fatalf("report.Recent has %d entries, want 1", len(report.Recent))
	}
	rec := report.Recent[0]
	if rec.ID != id || rec.Outcome != "expired" {
		t.Errorf("recent record = id %d outcome %q, want id %d outcome expired", rec.ID, rec.Outcome, id)
	}
	if rec.SummonerName != "Goodwife Bishop" || rec.TargetName != "John Proctor" {
		t.Errorf("recent record names = %q/%q, want Goodwife Bishop/John Proctor", rec.SummonerName, rec.TargetName)
	}
	if len(rec.History) < 5 {
		t.Errorf("recent record history has %d stamps, want the full transition trail (>=5): %+v", len(rec.History), rec.History)
	}
	if last := rec.History[len(rec.History)-1]; last.State != "awaiting_target" {
		t.Errorf("last history state = %q, want awaiting_target (where it died)", last.State)
	}
}

// driveToAwaiting runs an errand from dispatched through delivery so it parks
// in awaiting_target: summoner to the point, messenger to the point,
// commission, messenger to the target, deliver.
func driveToAwaiting(t *testing.T, w *sim.World, id sim.ErrandID, summoner, courier sim.ActorID) {
	t.Helper()
	arriveLeg(t, w, id, summoner)
	arriveLeg(t, w, id, courier)
	runCommission(t, w, id)
	arriveLeg(t, w, id, courier)
	runDeliver(t, w, id)
	if st, _ := errandState(t, w, id); st != stAwaitingTarget {
		t.Fatalf("errand %d state %q, want %q", id, st, stAwaitingTarget)
	}
}

// addActor seeds an extra actor into the running world.
func addActor(t *testing.T, w *sim.World, a *sim.Actor) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[a.ID] = a
		return nil, nil
	}}); err != nil {
		t.Fatalf("add actor %q: %v", a.ID, err)
	}
}

// TestSummonTargetArrival_MultipleErrands — the code_review blocker. Two
// active summons for the same target: an OLDER one still stalled at
// `dispatched` and a NEWER one in `awaiting_target`. First-match errand
// resolution could return the stalled one and drop the valid arrival (map
// iteration order is unstable, so it failed nondeterministically). The
// target-role scan must examine every errand: the arrival completes exactly
// the awaiting errand and leaves the stalled one untouched.
func TestSummonTargetArrival_MultipleErrands(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	// Older errand: claims the courier, never advances past dispatched.
	idOld := dispatchSummon(t, w, "summoner", "target", "")

	// Newer errand from a second summoner with its own courier.
	addActor(t, w, &sim.Actor{ID: "summoner2", DisplayName: "Reverend Hale", LLMAgent: "va-hale", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY + 2}})
	addActor(t, w, &sim.Actor{ID: "courier2", DisplayName: "the girl", Pos: sim.TilePos{X: sim.PadX + 2, Y: sim.PadY}, Attributes: map[string][]byte{sim.AttrMessenger: {}}})
	idNew := dispatchSummon(t, w, "summoner2", "target", "")
	driveToAwaiting(t, w, idNew, "summoner2", "courier2")

	// The target answers the newer summons.
	arriveTargetAt(t, w, "target", "square")

	if _, ok := errandState(t, w, idNew); ok {
		t.Error("awaiting errand not completed by the target's arrival (first-match resolution dropped it)")
	}
	// The old errand is untouched because it is not AWAITING (still stalled at
	// dispatched) — not because only one errand may complete per arrival.
	// Several errands already awaiting the same target at the same structure
	// would all complete, by design.
	if st, ok := errandState(t, w, idOld); !ok || st != stDispatched {
		t.Errorf("stalled (non-awaiting) errand = (%q, ok=%v), want untouched at %q", st, ok, stDispatched)
	}
	if n := errandCount(t, w); n != 1 {
		t.Errorf("errand count = %d, want 1 (the stalled errand remains)", n)
	}
}

// TestSummonTargetArrival_PhysicalPresenceIsTheMeeting pins the PRODUCT
// DECISION behind destination-only matching (code_review round 2): an
// awaiting errand completes on ANY arrival of its target at the meet place —
// even one the summons did not cause, even after the target's pending cue
// was replaced or faded. The summons asked them to come and they are
// physically there; what motivated the walk does not change that the meeting
// is happening at that spot. (The alternative — correlating on the cue —
// would strand an older errand whose cue was overwritten by a newer summons
// despite a physically-formed meeting.)
func TestSummonTargetArrival_PhysicalPresenceIsTheMeeting(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	id := dispatchSummon(t, w, "summoner", "target", "")
	driveToAwaiting(t, w, id, "summoner", "courier")

	// The cue is gone (faded, replaced, or cleared by a take_break) — the
	// arrival still completes the errand.
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["target"].PendingSummon = nil
		return nil, nil
	}})
	arriveTargetAt(t, w, "target", "square")
	if _, ok := errandState(t, w, id); ok {
		t.Error("errand not completed by a cue-less arrival at the meet place — physical presence IS the meeting (supported rule)")
	}
	if n := errandCount(t, w); n != 0 {
		t.Errorf("errand count = %d, want 0", n)
	}
}

// TestSummonMessengerFreedAtDelivery_Reusable — the messenger is released at
// delivery, while the first errand still awaits its target: a second summon
// dispatched right then must be able to select the same courier.
func TestSummonMessengerFreedAtDelivery_Reusable(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	id1 := dispatchSummon(t, w, "summoner", "target", "")
	driveToAwaiting(t, w, id1, "summoner", "courier")

	addActor(t, w, &sim.Actor{ID: "summoner2", DisplayName: "Reverend Hale", LLMAgent: "va-hale", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY + 2}})
	id2 := dispatchSummon(t, w, "summoner2", "target", "")
	if m := messengerOf(t, w, id2); m != "courier" {
		t.Fatalf("second errand's messenger = %q, want the courier freed at the first delivery", m)
	}
	if n := errandCount(t, w); n != 2 {
		t.Fatalf("errand count = %d, want 2 (awaiting + fresh)", n)
	}
}

// TestDispatchSummon_SayBeatBestEffort — LLM-414. A dispatch carrying a say
// beat still succeeds when the speak is rejected (the summoner is alone —
// no audience), and the say never blocks the errand. The beat itself riding
// the real Speak pipeline is asserted at the handler layer; here we pin the
// best-effort contract.
func TestDispatchSummon_SayBeatBestEffort(t *testing.T) {
	w, cancel := buildSummonWorld(t)
	defer cancel()

	res, err := w.Send(sim.DispatchSummon("summoner", "John Proctor", "News.",
		"Aye, I'll have a messenger fetch him over for you.", time.Now().UTC()))
	if err != nil {
		t.Fatalf("DispatchSummon with say beat: %v", err)
	}
	if _, ok := res.(sim.ErrandID); !ok {
		t.Fatalf("DispatchSummon returned %T, want sim.ErrandID", res)
	}
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
	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", "", time.Now().UTC())); err == nil {
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
	if _, err := w.Send(sim.DispatchSummon("summoner", "target", "", "", time.Now().UTC())); err == nil {
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

	res, err := w.Send(sim.DispatchSummon("summoner", "John Proctor", "", "", time.Now().UTC()))
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

	res, err := w.Send(sim.DispatchSummon("summoner", "target", "", "", time.Now().UTC()))
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
