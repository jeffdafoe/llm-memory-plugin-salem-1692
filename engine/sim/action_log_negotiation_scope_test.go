package sim

import (
	"testing"
	"time"
)

// action_log_negotiation_scope_test.go — LLM-283: the pay-ledger negotiation
// beats (offered/declined/countered) are FEED-ONLY. These tests lock the shared
// isNegotiationActionType guard at every NPC-facing action-log consumer — the
// atmosphere activity digest, the per-actor narrative event window, and the
// reflection activity gate — so a haggle changes the Village debugging window and
// nothing an NPC perceives. If a fourth negotiation type is added to the
// vocabulary without wiring the guard, one of these leaks and a test here fails.

// TestNarrativeConsumersExcludeNegotiationBeats covers the two
// narrative_consolidation readers directly: snapshotEventsForActor (the event
// window fed into the reflection prompt) and actorHasEventSince (the activity
// gate that decides whether to spend an LLM re-reflection call).
func TestNarrativeConsumersExcludeNegotiationBeats(t *testing.T) {
	cutoff := time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC)
	mixed := &World{ActionLog: []ActionLogEntry{
		{ActorID: "hannah", OccurredAt: cutoff.Add(10 * time.Minute), ActionType: ActionTypeSpoke, Text: "hello"},
		{ActorID: "hannah", OccurredAt: cutoff.Add(11 * time.Minute), ActionType: ActionTypeOffered, CounterpartyName: "Bob", Amount: 3, Text: "3x milk"},
		{ActorID: "hannah", OccurredAt: cutoff.Add(12 * time.Minute), ActionType: ActionTypeDeclined, CounterpartyName: "Bob"},
		{ActorID: "hannah", OccurredAt: cutoff.Add(13 * time.Minute), ActionType: ActionTypeCountered, CounterpartyName: "Bob", Amount: 5},
	}}

	events := snapshotEventsForActor(mixed, "hannah", cutoff)
	if len(events) != 1 {
		t.Fatalf("snapshotEventsForActor returned %d events, want 1 (spoke only)", len(events))
	}
	if events[0].ActionType != ActionTypeSpoke {
		t.Errorf("kept event type = %q, want spoke", events[0].ActionType)
	}

	// A window with a real beat present still reports activity...
	if !actorHasEventSince(mixed, "hannah", cutoff) {
		t.Error("actorHasEventSince = false, want true (the spoke beat is real activity)")
	}
	// ...but a haggle-ONLY window must read as no activity, or the reflection
	// gate burns an LLM call to re-chew an unchanged summary.
	haggleOnly := &World{ActionLog: []ActionLogEntry{
		{ActorID: "hannah", OccurredAt: cutoff.Add(11 * time.Minute), ActionType: ActionTypeOffered, CounterpartyName: "Bob", Amount: 3},
		{ActorID: "hannah", OccurredAt: cutoff.Add(12 * time.Minute), ActionType: ActionTypeDeclined, CounterpartyName: "Bob"},
		{ActorID: "hannah", OccurredAt: cutoff.Add(13 * time.Minute), ActionType: ActionTypeCountered, CounterpartyName: "Bob", Amount: 5},
	}}
	if actorHasEventSince(haggleOnly, "hannah", cutoff) {
		t.Error("actorHasEventSince = true for a haggle-only window, want false (negotiation excluded)")
	}
}

// TestAtmosphereDigestExcludesNegotiationBeats covers the third consumer, the
// atmosphere activity digest, through its real FetchAtmosphereContext path: a
// spoke beat is counted, the three negotiation beats are not.
func TestAtmosphereDigestExcludesNegotiationBeats(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	priorAt := time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC)
	w.Environment.LastAtmosphereRefreshAt = priorAt
	w.Actors["hannah"] = &Actor{ID: "hannah", DisplayName: "Hannah", Kind: KindNPCShared}
	w.ActionLog = []ActionLogEntry{
		{ActorID: "hannah", OccurredAt: priorAt.Add(10 * time.Minute), ActionType: ActionTypeSpoke},
		{ActorID: "hannah", OccurredAt: priorAt.Add(11 * time.Minute), ActionType: ActionTypeOffered, CounterpartyName: "Bob", Amount: 3},
		{ActorID: "hannah", OccurredAt: priorAt.Add(12 * time.Minute), ActionType: ActionTypeDeclined, CounterpartyName: "Bob"},
		{ActorID: "hannah", OccurredAt: priorAt.Add(13 * time.Minute), ActionType: ActionTypeCountered, CounterpartyName: "Bob", Amount: 5},
	}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(priorAt.Add(4*time.Hour)))
	ctx := v.(AtmosphereContext)
	if len(ctx.ActivityDigest) != 1 {
		t.Fatalf("ActivityDigest len = %d, want 1 (hannah, spoke only)", len(ctx.ActivityDigest))
	}
	counts := ctx.ActivityDigest[0].Counts
	if counts[ActionTypeSpoke] != 1 {
		t.Errorf("Counts[spoke] = %d, want 1", counts[ActionTypeSpoke])
	}
	for _, nt := range []ActionType{ActionTypeOffered, ActionTypeDeclined, ActionTypeCountered} {
		if c, ok := counts[nt]; ok {
			t.Errorf("Counts[%s] = %d present, want absent (feed-only, excluded from digest)", nt, c)
		}
	}
	if len(counts) != 1 {
		t.Errorf("Counts has %d keys, want 1 (spoke only)", len(counts))
	}
}
