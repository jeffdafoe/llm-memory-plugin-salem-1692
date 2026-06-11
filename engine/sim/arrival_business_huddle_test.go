package sim_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// arrival_business_huddle_test.go — ZBBS-HOME-425 indoor-arrival
// hospitality bootstrap. Package sim_test in engine/sim/ for the same
// reason as arrival_encounter_subscriber_test.go: sim.EmitForTest.
//
// Tests register the REAL businessowner cascade alongside the arrival
// subscriber and assert on the keeper's greet Spoke — the property the
// bootstrap exists to produce — plus huddle topology.

// businessArrivalActor describes one seeded actor.
type businessArrivalActor struct {
	id           sim.ActorID
	inside       sim.StructureID
	kind         sim.ActorKind
	huddleID     sim.HuddleID
	keeperOf     sim.StructureID // non-empty: BusinessownerState + WorkStructureID
	state        sim.ActorState
	lastPCSeenAt *time.Time
}

// greetSpokeRecorder captures Spoke events emitted on the world goroutine.
type greetSpokeRecorder struct {
	mu     sync.Mutex
	spokes []*sim.Spoke
}

func (r *greetSpokeRecorder) record(_ *sim.World, evt sim.Event) {
	if s, ok := evt.(*sim.Spoke); ok {
		r.mu.Lock()
		r.spokes = append(r.spokes, s)
		r.mu.Unlock()
	}
}

func (r *greetSpokeRecorder) bySpeaker(id sim.ActorID) []*sim.Spoke {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*sim.Spoke
	for _, s := range r.spokes {
		if s.SpeakerID == id {
			out = append(out, s)
		}
	}
	return out
}

// buildBusinessArrivalWorld seeds a running world with the arrival
// bootstrap + businessowner cascade registered and a Spoke recorder
// subscribed.
func buildBusinessArrivalWorld(t *testing.T, actors []businessArrivalActor) (*sim.World, *greetSpokeRecorder, context.CancelFunc) {
	t.Helper()
	repo, h := mem.NewRepository()

	structures := map[sim.StructureID]*sim.Structure{}
	huddles := map[sim.HuddleID]*sim.Huddle{}
	actorMap := make(map[sim.ActorID]*sim.Actor, len(actors))
	for _, a := range actors {
		actor := &sim.Actor{
			ID:                a.id,
			DisplayName:       string(a.id),
			InsideStructureID: a.inside,
			CurrentHuddleID:   a.huddleID,
			Kind:              a.kind,
			State:             a.state,
			LastPCSeenAt:      a.lastPCSeenAt,
		}
		if a.keeperOf != "" {
			actor.BusinessownerState = &sim.BusinessownerState{Flavor: "flamboyant"}
			actor.WorkStructureID = a.keeperOf
		}
		actorMap[a.id] = actor
		for _, sid := range []sim.StructureID{a.inside, a.keeperOf} {
			if sid != "" {
				if _, exists := structures[sid]; !exists {
					structures[sid] = &sim.Structure{ID: sid, DisplayName: string(sid)}
				}
			}
		}
		if a.huddleID != "" {
			hud, exists := huddles[a.huddleID]
			if !exists {
				hud = &sim.Huddle{
					ID:          a.huddleID,
					Members:     map[sim.ActorID]struct{}{},
					StructureID: a.inside,
					StartedAt:   time.Now().UTC().Add(-time.Minute),
				}
				huddles[a.huddleID] = hud
			}
			hud.Members[a.id] = struct{}{}
		}
	}
	if len(structures) > 0 {
		h.Structures.Seed(structures)
		// Shared-Identity Bridge: every Structure.ID needs a backing
		// VillageObject (+ Asset) for scene minting — the structure scene's
		// origin comes from vobj.Pos (ZBBS-WORK-342). One shared position is
		// fine: no test here compares cross-structure geometry.
		h.Assets.Seed(map[sim.AssetID]*sim.Asset{
			"bldg-asset": {ID: "bldg-asset", Category: "structure"},
		})
		objects := make(map[sim.VillageObjectID]*sim.VillageObject, len(structures))
		for sid := range structures {
			objects[sim.VillageObjectID(sid)] = &sim.VillageObject{
				ID:      sim.VillageObjectID(sid),
				AssetID: "bldg-asset",
				Pos:     sim.WorldPos{X: 160, Y: 160},
			}
		}
		h.VillageObjects.Seed(objects)
	}
	if len(huddles) > 0 {
		h.Huddles.Seed(huddles)
	}
	h.Actors.Seed(actorMap)

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)

	rec := &greetSpokeRecorder{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cascade.RegisterBusinessArrival(world)
		cascade.RegisterBusinessowner(ctx, world)
		world.Subscribe(sim.SubscriberFunc(rec.record))
		return nil, nil
	}}); err != nil {
		cancel()
		t.Fatalf("register cascades: %v", err)
	}
	return w, rec, cancel
}

// emitIndoorArrival synthesizes the ActorArrived for an indoor arrival
// matching the actor's seeded state.
func emitIndoorArrival(t *testing.T, w *sim.World, actorID sim.ActorID, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor, ok := world.Actors[actorID]
		if !ok {
			t.Fatalf("emitIndoorArrival: actor %q not found", actorID)
		}
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:          actorID,
			FinalPosition:    sim.Position{X: actor.Pos.X, Y: actor.Pos.Y},
			FinalStructureID: actor.InsideStructureID,
			At:               now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emitIndoorArrival: %v", err)
	}
}

// readHuddleOf reads an actor's CurrentHuddleID on the world goroutine.
func readHuddleOf(t *testing.T, w *sim.World, actorID sim.ActorID) sim.HuddleID {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor, ok := world.Actors[actorID]
		if !ok {
			return sim.HuddleID(""), nil
		}
		return actor.CurrentHuddleID, nil
	}})
	if err != nil {
		t.Fatalf("readHuddleOf: %v", err)
	}
	return res.(sim.HuddleID)
}

// freshStampAt pins the PC presence stamp to the same clock the arrival
// event uses, so the presence gate can't race a slow test run.
// (code_review)
func freshStampAt(now time.Time) *time.Time {
	t := now
	return &t
}

const tavern = sim.StructureID("tavern")

// A fresh PC walking into a staffed business gets huddled with the
// keeper AND greeted — keeper joins first, so the greet subscriber sees
// it in OtherMembers on the arriver's join.
func TestArrivalBusinessHuddle_PCGreetedByAtPostKeeper(t *testing.T) {
	now := time.Now().UTC()
	w, rec, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCShared, keeperOf: tavern},
		{id: "pc", inside: tavern, kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()

	emitIndoorArrival(t, w, "pc", now)

	pcHuddle := readHuddleOf(t, w, "pc")
	keeperHuddle := readHuddleOf(t, w, "keeper")
	if pcHuddle == "" || pcHuddle != keeperHuddle {
		t.Fatalf("huddles: pc=%q keeper=%q, want same non-empty", pcHuddle, keeperHuddle)
	}
	greets := rec.bySpeaker("keeper")
	if len(greets) != 1 {
		t.Fatalf("keeper greets = %d, want 1", len(greets))
	}
}

// A conversational NPC walk-in is greeted the same way (ZBBS-HOME-363
// parity — NPC customers transact too).
func TestArrivalBusinessHuddle_NPCCustomerGreeted(t *testing.T) {
	now := time.Now().UTC()
	w, rec, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCShared, keeperOf: tavern},
		{id: "customer", inside: tavern, kind: sim.KindNPCStateful},
	})
	defer cancel()

	emitIndoorArrival(t, w, "customer", now)

	if h := readHuddleOf(t, w, "customer"); h == "" || h != readHuddleOf(t, w, "keeper") {
		t.Fatalf("customer/keeper not huddled together")
	}
	if got := len(rec.bySpeaker("keeper")); got != 1 {
		t.Fatalf("keeper greets = %d, want 1", got)
	}
}

// No keeper in the structure → no huddle, no greet. Homes stay quiet and
// passers-through are never auto-joined.
func TestArrivalBusinessHuddle_NoKeeperNoHuddle(t *testing.T) {
	now := time.Now().UTC()
	w, rec, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "villager", inside: "house", kind: sim.KindNPCShared},
		{id: "pc", inside: "house", kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()

	emitIndoorArrival(t, w, "pc", now)

	if h := readHuddleOf(t, w, "pc"); h != "" {
		t.Fatalf("pc huddle = %q, want none", h)
	}
	if got := len(rec.spokes); got != 0 {
		t.Fatalf("spokes = %d, want 0", got)
	}
}

// A sleeping keeper receives no one.
func TestArrivalBusinessHuddle_SleepingKeeperNoHuddle(t *testing.T) {
	now := time.Now().UTC()
	w, _, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCShared, keeperOf: tavern, state: sim.StateSleeping},
		{id: "pc", inside: tavern, kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()

	emitIndoorArrival(t, w, "pc", now)

	if h := readHuddleOf(t, w, "pc"); h != "" {
		t.Fatalf("pc huddle = %q, want none (keeper asleep)", h)
	}
}

// The structure's own keeper returning to post bootstraps nothing.
func TestArrivalBusinessHuddle_KeeperReturningToPostNoOp(t *testing.T) {
	now := time.Now().UTC()
	w, rec, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCShared, keeperOf: tavern},
		{id: "pc", inside: tavern, kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()

	emitIndoorArrival(t, w, "keeper", now)

	if h := readHuddleOf(t, w, "keeper"); h != "" {
		t.Fatalf("keeper huddle = %q, want none", h)
	}
	if got := len(rec.spokes); got != 0 {
		t.Fatalf("spokes = %d, want 0", got)
	}
}

// A ghost PC (nil presence stamp — closed tab / never polled) is not
// welcomed (ZBBS-WORK-326 posture).
func TestArrivalBusinessHuddle_StalePCNotGreeted(t *testing.T) {
	now := time.Now().UTC()
	w, rec, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCShared, keeperOf: tavern},
		{id: "pc", inside: tavern, kind: sim.KindPC, lastPCSeenAt: nil},
	})
	defer cancel()

	emitIndoorArrival(t, w, "pc", now)

	if h := readHuddleOf(t, w, "pc"); h != "" {
		t.Fatalf("stale pc huddle = %q, want none", h)
	}
	if got := len(rec.spokes); got != 0 {
		t.Fatalf("spokes = %d, want 0", got)
	}
}

// A stale ActorArrived — the event names a structure the actor has since
// left — must not bootstrap a huddle at the actor's CURRENT (also staffed)
// structure. (code_review: the command acts only when the event's final
// structure still matches live state.)
func TestArrivalBusinessHuddle_StaleEventForOtherStructureNoOp(t *testing.T) {
	now := time.Now().UTC()
	w, rec, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: "smithy", kind: sim.KindNPCShared, keeperOf: "smithy"},
		{id: "pc", inside: "smithy", kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()

	// Event claims the PC arrived at the tavern; the PC is actually inside
	// the smithy (with an at-post keeper that must NOT be bootstrapped off
	// this unrelated event).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:          "pc",
			FinalStructureID: tavern,
			At:               now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emit stale arrival: %v", err)
	}

	if h := readHuddleOf(t, w, "pc"); h != "" {
		t.Fatalf("pc huddle = %q, want none (stale event)", h)
	}
	if got := len(rec.spokes); got != 0 {
		t.Fatalf("spokes = %d, want 0", got)
	}
}

// A keeper already conversing: the arriver joins THAT huddle and the
// greet still fires (keeper is among OtherMembers for the join).
func TestArrivalBusinessHuddle_JoinsExistingKeeperHuddle(t *testing.T) {
	now := time.Now().UTC()
	w, rec, cancel := buildBusinessArrivalWorld(t, []businessArrivalActor{
		{id: "keeper", inside: tavern, kind: sim.KindNPCShared, keeperOf: tavern, huddleID: "hud-existing"},
		{id: "regular", inside: tavern, kind: sim.KindNPCStateful, huddleID: "hud-existing"},
		{id: "pc", inside: tavern, kind: sim.KindPC, lastPCSeenAt: freshStampAt(now)},
	})
	defer cancel()

	emitIndoorArrival(t, w, "pc", now)

	pcHuddle := readHuddleOf(t, w, "pc")
	if pcHuddle != "hud-existing" {
		t.Fatalf("pc huddle = %q, want hud-existing", pcHuddle)
	}
	if got := len(rec.bySpeaker("keeper")); got != 1 {
		t.Fatalf("keeper greets = %d, want 1", got)
	}
}
