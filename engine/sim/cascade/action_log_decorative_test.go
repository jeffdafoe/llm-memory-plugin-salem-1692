package cascade

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_decorative_test.go — LLM-593 regression coverage. A decorative
// actor is walked by the engine but never ticked, so its locomotion must not
// reach the action log. The pond ducks wander every few seconds; before the
// fix that was 15,672 "walked" rows a day into a log the rest of the village
// fills at ~1,000/day, drowning the admin Village tab and the atmosphere
// prompt's activity digest.
//
// The assertions run against BOTH sinks in one test on purpose: the
// subscriber calls the in-memory funnel and the durable sink independently,
// so a gate on only one of them still ships half the defect.

// seedDecorative adds a sprite-only actor to a running test world.
func seedDecorative(t *testing.T, w *sim.World, id sim.ActorID, name string) {
	t.Helper()
	invokeOnWorld(t, w, func(world *sim.World) {
		world.Actors[id] = &sim.Actor{
			ID:          id,
			DisplayName: name,
			Kind:        sim.KindDecorative,
			State:       sim.StateIdle,
		}
	})
}

// --- TestHandleActorArrivedActionLog_DecorativeDropped ---------------
// A duck's arrival writes neither an in-memory row nor a durable one, while
// the very next arrival by a real NPC still lands in both. The paired
// assertion is what distinguishes "decoratives are gated" from "the log
// broke".
func TestHandleActorArrivedActionLog_DecorativeDropped(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })
	seedDecorative(t, w, "duck", "Duck")

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{ActorID: "duck", At: at})
	})

	if got := readActionLog(t, w); len(got) != 0 {
		t.Fatalf("len(ActionLog) = %d, want 0 — a decorative arrival must not be logged (got %+v)", len(got), got)
	}
	if rows := rec.snapshot(); len(rows) != 0 {
		t.Fatalf("durable rows = %d, want 0 — a decorative arrival must not reach agent_action_log (got %+v)", len(rows), rows)
	}

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{ActorID: "hannah", At: at.Add(time.Second)})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1 — the gate must be specific to decoratives", len(got))
	}
	// The dropped duck must not have burned a sequence number. Seq is the
	// Village tab's paging cursor, so a gap is harmless to the client, but a
	// counter that advances on a row nobody wrote makes the log's own
	// telemetry lie about how much was recorded.
	if got[0].Seq != 1 {
		t.Errorf("Seq = %d, want 1 — the dropped decorative row consumed a sequence number", got[0].Seq)
	}
	if rows := rec.snapshot(); len(rows) != 1 {
		t.Errorf("durable rows = %d, want 1 for the NPC arrival", len(rows))
	}
}

// --- TestHandleActorLeftStructureActionLog_DecorativeDropped ---------
// The departure twin. A decorative that walks out of a structure footprint
// is the other locomotion path into the log, and the central funnel gate
// covers it without the subscriber knowing anything about Kind.
func TestHandleActorLeftStructureActionLog_DecorativeDropped(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	seedDecorative(t, w, "duck", "Duck")

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorLeftStructureActionLog(world, &sim.ActorLeftStructure{
			ActorID:     "duck",
			StructureID: "tavern",
			At:          time.Now().UTC(),
		})
	})

	if got := readActionLog(t, w); len(got) != 0 {
		t.Fatalf("len(ActionLog) = %d, want 0 — a decorative departure must not be logged (got %+v)", len(got), got)
	}
}

// --- TestAppendActionLogEntry_UnresolvableActorStillAppends ----------
// The gate must key on a RESOLVED decorative actor, never on "the actor
// isn't in the map". A visitor's row is deliberately kept and its id blanked
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
		t.Fatalf("len(ActionLog) = %d, want 1 — an unresolvable actor id is not a decorative", len(got))
	}
}
