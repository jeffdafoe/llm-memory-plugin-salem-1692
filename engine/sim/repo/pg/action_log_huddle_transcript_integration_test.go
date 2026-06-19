package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_huddle_transcript_integration_test.go — real-pg round-trip for
// LoadHuddleTranscript (LLM-35). Validates what pgxmock can't: the WHERE
// huddle_id = $1 match against the now-TEXT huddle_id (ZBBS-WORK-239 §4b),
// oldest-first ordering, the result='ok' filter, cross-source inclusion
// (agent + player), and payload.text extraction — all against the migrated
// schema.

func TestActionLogRepo_Integration_LoadHuddleTranscript(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Two participants in the conversation: an agent NPC and the human PC. The
	// actor_id FK → actor(id) requires both to exist.
	const prudence = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const jefferey = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	for _, a := range []struct{ id, name string }{{prudence, "Prudence"}, {jefferey, "Jefferey"}} {
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
			a.id, a.name,
		); err != nil {
			t.Fatalf("seed actor %s: %v", a.name, err)
		}
	}

	r := NewActionLogRepo(f.Pool)
	const huddle = "hud-00000000000000000000000000000035"
	const otherHuddle = "hud-0000000000000000000000000000ffff"
	base := time.Date(2026, 6, 18, 21, 21, 0, 0, time.UTC)

	// Rows written out of chronological order, so passing them back oldest-first
	// proves the ORDER BY rather than insertion order.
	rows := []sim.DurableActionLogRow{
		{ActorID: prudence, OccurredAt: base.Add(2 * time.Minute), ActionType: sim.ActionTypeSpoke, Payload: map[string]any{"text": "And to you, good sir."}, SpeakerName: "Prudence", HuddleID: huddle, Source: "agent"},
		{ActorID: jefferey, OccurredAt: base, ActionType: sim.ActionTypeSpoke, Payload: map[string]any{"text": "Good evening."}, SpeakerName: "Jefferey", HuddleID: huddle, Source: "player"},
		// A textless committed action (a payment): action_type carries the
		// meaning, Text comes back empty.
		{ActorID: jefferey, OccurredAt: base.Add(4 * time.Minute), ActionType: sim.ActionType("paid"), Payload: map[string]any{"amount": 3, "item": "ale"}, SpeakerName: "Jefferey", HuddleID: huddle, Source: "player"},
		// A row in a DIFFERENT huddle — must be excluded by the huddle filter.
		{ActorID: prudence, OccurredAt: base.Add(time.Minute), ActionType: sim.ActionTypeSpoke, Payload: map[string]any{"text": "elsewhere"}, SpeakerName: "Prudence", HuddleID: otherHuddle, Source: "agent"},
		// A NULL-huddle (outdoor) row — must be excluded (huddle_id IS NULL).
		{ActorID: prudence, OccurredAt: base.Add(time.Minute), ActionType: sim.ActionType("walked"), Payload: map[string]any{"destination": "well"}, SpeakerName: "Prudence", HuddleID: "", Source: "agent"},
	}
	for _, row := range rows {
		r.writeOne(row)
	}

	// A non-'ok' row in the target huddle (direct insert — the sink only ever
	// writes 'ok'). Must be excluded by the result='ok' filter.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, occurred_at, source, action_type, payload, result, speaker_name, huddle_id)
		 VALUES ($1, $2, 'agent', 'spoke', '{"text":"rejected line"}'::jsonb, 'rejected', 'Prudence', $3)`,
		prudence, base.Add(3*time.Minute), huddle,
	); err != nil {
		t.Fatalf("seed rejected row: %v", err)
	}

	got, err := r.LoadHuddleTranscript(ctx, huddle, 100)
	if err != nil {
		t.Fatalf("LoadHuddleTranscript: %v", err)
	}

	// Exactly the three committed rows of this huddle, oldest-first; the other
	// huddle, the NULL-huddle row, and the rejected row are all excluded.
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3 (other-huddle / null-huddle / rejected excluded): %+v", len(got), got)
	}
	if got[0].SpeakerName != "Jefferey" || got[0].Source != "player" || got[0].Text != "Good evening." {
		t.Errorf("row[0] = %+v, want the player's opening line", got[0])
	}
	if got[1].SpeakerName != "Prudence" || got[1].Source != "agent" || got[1].Text != "And to you, good sir." {
		t.Errorf("row[1] = %+v, want Prudence's reply", got[1])
	}
	// The payment: action_type preserved, text empty (no payload.text).
	if got[2].ActionType != sim.ActionType("paid") || got[2].Text != "" {
		t.Errorf("row[2] = %+v, want action_type=paid with empty text", got[2])
	}
	// Oldest-first ordering holds across the set.
	if !got[0].OccurredAt.Before(got[1].OccurredAt) || !got[1].OccurredAt.Before(got[2].OccurredAt) {
		t.Errorf("rows not strictly oldest-first: %v, %v, %v", got[0].OccurredAt, got[1].OccurredAt, got[2].OccurredAt)
	}
}

// TestActionLogRepo_Integration_LoadHuddleTranscript_Empty: an unknown huddle
// yields a non-nil empty slice (clean "no transcript", not an error).
func TestActionLogRepo_Integration_LoadHuddleTranscript_Empty(t *testing.T) {
	f := newFixture(t)
	r := NewActionLogRepo(f.Pool)
	got, err := r.LoadHuddleTranscript(context.Background(), "hud-does-not-exist", 100)
	if err != nil {
		t.Fatalf("LoadHuddleTranscript: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %v, want non-nil empty slice", got)
	}
}
