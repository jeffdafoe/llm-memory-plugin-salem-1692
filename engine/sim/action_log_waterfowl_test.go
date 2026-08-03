package sim

import (
	"testing"
	"time"
)

// action_log_waterfowl_test.go — LLM-593 substrate coverage for the two
// append funnels, exercised directly rather than through cascade wiring.
// The cascade-level tests live in cascade/action_log_decorative_test.go.

// waterfowlLogWorld builds a minimal world holding a duck (decorative +
// waterfowl sprite), a decorative route carrier with an ordinary sprite, and
// an ordinary NPC.
func waterfowlLogWorld() *World {
	w := &World{
		Actors:            make(map[ActorID]*Actor),
		Structures:        make(map[StructureID]*Structure),
		Huddles:           make(map[HuddleID]*Huddle),
		actorsByStructure: make(map[StructureID]map[ActorID]struct{}),
		actorsByHuddle:    make(map[HuddleID]map[ActorID]struct{}),
		outdoorActors:     make(map[ActorID]struct{}),
		Sprites: map[SpriteID]*Sprite{
			"sprite-duck": {ID: "sprite-duck", Behaviors: []string{BehaviorWaterfowl}},
		},
	}
	w.Actors["duck"] = &Actor{ID: "duck", DisplayName: "Duck", Kind: KindDecorative, SpriteID: "sprite-duck"}
	w.Actors["crier"] = &Actor{ID: "crier", DisplayName: "Grace Edwards", Kind: KindDecorative}
	w.Actors["hannah"] = &Actor{ID: "hannah", DisplayName: "Hannah", Kind: KindNPCShared}
	return w
}

func durableRow(id ActorID) DurableActionLogRow {
	return DurableActionLogRow{
		ActorID:    id,
		OccurredAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
		ActionType: ActionTypeWalked,
	}
}

// TestAppendActionLogDurable_WaterfowlDropped exercises the durable funnel on
// its own. The cascade tests reach it through a subscriber; this one proves
// the gate lives in AppendActionLogDurable itself, so a future caller that
// writes a durable row without touching the in-memory log is still covered.
func TestAppendActionLogDurable_WaterfowlDropped(t *testing.T) {
	w := waterfowlLogWorld()
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)

	w.AppendActionLogDurable(durableRow("duck"))
	if len(sink.rows) != 0 {
		t.Fatalf("durable rows = %d, want 0 for a duck (got %+v)", len(sink.rows), sink.rows)
	}

	w.AppendActionLogDurable(durableRow("crier"))
	w.AppendActionLogDurable(durableRow("hannah"))
	if len(sink.rows) != 2 {
		t.Fatalf("durable rows = %d, want 2 — the crier and the NPC both belong in the day note", len(sink.rows))
	}
}

// TestAppendActionLogDurable_VisitorRowKeptAndBlanked pins the interaction
// with LLM-573. A visitor's durable row is deliberately KEPT after the
// visitor is gone, with ActorID blanked to NULL so it has no FK to violate —
// that row is what stops a stateful NPC's day note becoming a one-sided
// transcript. The waterfowl gate must not resurrect the drop LLM-573 undid,
// which is why it keys on a resolved duck rather than on a failed lookup.
func TestAppendActionLogDurable_VisitorRowKeptAndBlanked(t *testing.T) {
	w := waterfowlLogWorld()
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)

	row := durableRow("vstr-0a1b2c3d")
	row.SpeakerName = "Master Babbage the provisioner"
	w.AppendActionLogDurable(row)

	if len(sink.rows) != 1 {
		t.Fatalf("durable rows = %d, want 1 — a departed visitor's row is kept (LLM-573)", len(sink.rows))
	}
	if got := sink.rows[0].ActorID; got != "" {
		t.Errorf("ActorID = %q, want blank — the id must be NULLed, not persisted", got)
	}
	if got := sink.rows[0].SpeakerName; got != "Master Babbage the provisioner" {
		t.Errorf("SpeakerName = %q, want it preserved — the name is what survives the blanking", got)
	}
}

// TestAppendActionLogEntry_RemovedWaterfowlIsAResolvedMiss documents the
// ordering invariant that makes a registry of departed decorative ids
// unnecessary. World.emit dispatches subscribers SYNCHRONOUSLY and inline on
// the world goroutine, so the append for a duck's ActorArrived completes
// inside the same command that emitted it — no removal can interleave. This
// test covers only the residual case an operator can force: appending for an
// id already deleted from w.Actors. That row survives, deliberately, because
// the alternative (treating an unresolvable id as scenery) would drop the
// visitor rows above. The cost is bounded at one row per deleted duck.
func TestAppendActionLogEntry_RemovedWaterfowlIsAResolvedMiss(t *testing.T) {
	w := waterfowlLogWorld()
	delete(w.Actors, "duck")

	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:    "duck",
		OccurredAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
		ActionType: ActionTypeWalked,
	}).Fn(w); err != nil {
		t.Fatalf("AppendActionLogEntry: %v", err)
	}

	if len(w.ActionLog) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1 — an unresolvable id is not gated as scenery", len(w.ActionLog))
	}
}
