package cascade

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_decorative_test.go — LLM-593 regression coverage. A duck's
// wander is ambient scenery motion, so it must not reach the action log. The
// pond ducks move every few seconds; before the fix that was 15,672 "walked"
// rows a day into a log the rest of the village fills at ~1,000/day, drowning
// the admin Village tab and the atmosphere prompt's activity digest.
//
// The boundary these tests defend is waterfowl vs. the wider KindDecorative.
// The lamplighter, washerwoman and town crier are ALSO decorative — the
// engine walks them because they have no LLM volition — but they tour and
// they speak, and agent_action_log is the sole input to the day note behind
// the nightly dream pipeline. A Kind-shaped gate would silently amputate
// their history, so TestActionLog_DecorativeCarrierStillLogged is the half of
// this file that matters most.
//
// Assertions run against BOTH sinks on purpose: the subscriber calls the
// in-memory funnel and the durable sink independently, so a gate on only one
// of them still ships half the defect.

const (
	duckSpriteID   sim.SpriteID = "sprite-duck"
	villagerSprite sim.SpriteID = "sprite-villager"
)

// seedWaterfowl adds a duck — a decorative actor whose sprite carries the
// waterfowl behavior — to a running test world.
func seedWaterfowl(t *testing.T, w *sim.World, id sim.ActorID, name string) {
	t.Helper()
	invokeOnWorld(t, w, func(world *sim.World) {
		if world.Sprites == nil {
			world.Sprites = make(map[sim.SpriteID]*sim.Sprite)
		}
		world.Sprites[duckSpriteID] = &sim.Sprite{
			ID:        duckSpriteID,
			Name:      "Duck (mallard)",
			Behaviors: []string{sim.BehaviorWaterfowl},
		}
		world.Actors[id] = &sim.Actor{
			ID:          id,
			DisplayName: name,
			Kind:        sim.KindDecorative,
			SpriteID:    duckSpriteID,
			State:       sim.StateIdle,
		}
	})
}

// seedDecorativeCarrier adds a route carrier — decorative like a duck, but
// with an ordinary sprite and real work to do (town crier / washerwoman /
// lamplighter).
//
// The sprite is assigned EXPLICITLY with an empty behavior list rather than
// left nil. A nil sprite would pass the gate for the uninteresting reason
// that HasBehavior nil-guards to false, so the test would keep passing even
// if sprites started defaulting to a waterfowl behavior. What must be
// asserted is that an ordinary sprite is not ambient.
func seedDecorativeCarrier(t *testing.T, w *sim.World, id sim.ActorID, name string) {
	t.Helper()
	invokeOnWorld(t, w, func(world *sim.World) {
		if world.Sprites == nil {
			world.Sprites = make(map[sim.SpriteID]*sim.Sprite)
		}
		world.Sprites[villagerSprite] = &sim.Sprite{
			ID:        villagerSprite,
			Name:      "Villager",
			Behaviors: []string{},
		}
		world.Actors[id] = &sim.Actor{
			ID:          id,
			DisplayName: name,
			Kind:        sim.KindDecorative,
			SpriteID:    villagerSprite,
			State:       sim.StateIdle,
		}
	})
}

// --- TestHandleActorArrivedActionLog_WaterfowlDropped ----------------
// A duck's arrival writes neither an in-memory row nor a durable one, while
// the very next arrival by a real NPC still lands in both. The paired
// assertion is what distinguishes "waterfowl are gated" from "the log broke".
func TestHandleActorArrivedActionLog_WaterfowlDropped(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })
	seedWaterfowl(t, w, "duck", "Duck")

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{ActorID: "duck", At: at})
	})

	if got := readActionLog(t, w); len(got) != 0 {
		t.Fatalf("len(ActionLog) = %d, want 0 — a duck's wander must not be logged (got %+v)", len(got), got)
	}
	if rows := rec.snapshot(); len(rows) != 0 {
		t.Fatalf("durable rows = %d, want 0 — a duck's wander must not reach agent_action_log (got %+v)", len(rows), rows)
	}

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{ActorID: "hannah", At: at.Add(time.Second)})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1 — the gate must be specific to waterfowl", len(got))
	}
	// The dropped duck must not have burned a sequence number. Seq is the
	// Village tab's paging cursor, so a gap is harmless to the client, but a
	// counter that advances on a row nobody wrote makes the log's own
	// telemetry lie about how much was recorded.
	if got[0].Seq != 1 {
		t.Errorf("Seq = %d, want 1 — the dropped waterfowl row consumed a sequence number", got[0].Seq)
	}
	if rows := rec.snapshot(); len(rows) != 1 {
		t.Errorf("durable rows = %d, want 1 for the NPC arrival", len(rows))
	}
}

// --- TestActionLog_DecorativeCarrierStillLogged ----------------------
// The gate must NOT widen to KindDecorative. The town crier, washerwoman and
// lamplighter are decorative carriers with no LLM volition, walked by the
// engine — and their tours and announcements are real village history that
// feeds the durable day note. Only the sprite behavior separates them from a
// duck, so this is the assertion that catches a gate written against Kind.
func TestActionLog_DecorativeCarrierStillLogged(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })
	seedDecorativeCarrier(t, w, "crier", "Grace Edwards")

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{
			ActorID:          "crier",
			FinalStructureID: "tavern",
			At:               time.Now().UTC(),
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1 — a decorative route carrier is not scenery", len(got))
	}
	if got[0].ActorID != "crier" {
		t.Errorf("ActorID = %q, want crier", got[0].ActorID)
	}
	if rows := rec.snapshot(); len(rows) != 1 {
		t.Fatalf("durable rows = %d, want 1 — the crier's history feeds the day note", len(rows))
	}
}

// --- TestHandleActorLeftStructureActionLog_WaterfowlDropped ----------
// The departure twin. A duck that walks out of a structure footprint is the
// other locomotion path into the log, and the central funnel gate covers it
// without the subscriber knowing anything about waterfowl.
func TestHandleActorLeftStructureActionLog_WaterfowlDropped(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	seedWaterfowl(t, w, "duck", "Duck")

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorLeftStructureActionLog(world, &sim.ActorLeftStructure{
			ActorID:     "duck",
			StructureID: "tavern",
			At:          time.Now().UTC(),
		})
	})

	if got := readActionLog(t, w); len(got) != 0 {
		t.Fatalf("len(ActionLog) = %d, want 0 — a duck's departure must not be logged (got %+v)", len(got), got)
	}
}

// --- TestAppendActionLogEntry_UnresolvableActorStillAppends ----------
// The gate must key on a RESOLVED waterfowl actor, never on "the actor isn't
// in the map". A visitor's row is deliberately kept and its id blanked
// downstream (LLM-573), and an emit site that runs after visitor cleanup
// hands the funnel an id that no longer resolves. Treating that as scenery
// would silently drop the very rows LLM-573 restored.
func TestAppendActionLogEntry_UnresolvableActorStillAppends(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{
			ActorID: "vstr-0a1b2c3d",
			At:      time.Now().UTC(),
		})
	})

	if got := readActionLog(t, w); len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1 — an unresolvable actor id is not a waterfowl", len(got))
	}
}
