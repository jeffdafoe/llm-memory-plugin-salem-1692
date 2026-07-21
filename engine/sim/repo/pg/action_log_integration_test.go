package pg

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_integration_test.go — real-pg round-trip for the durable
// ActionLogRepo sink (ZBBS-WORK-376). Validates what pgxmock can't: the
// INSERT against the migrated schema — actor_id uuid FK, the now-TEXT
// huddle_id (ZBBS-WORK-239 §4b dropped its scene_huddle FK + retyped it),
// the jsonb payload cast, and the source/result CHECK constraints — and
// exercises the async Append → writer-goroutine → drain-on-cancel lifecycle.

func TestActionLogRepo_Integration_RoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Seed the referenced actor (actor_id FK → actor(id)). id +
	// display_name + current_x/current_y are the only NOT-NULL columns
	// without a default.
	const actorID = "11111111-1111-1111-1111-111111111111"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
		actorID, "Ezekiel",
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	r := NewActionLogRepo(f.Pool)

	// Run the writer goroutine, append a row, then cancel to drain — the
	// production lifecycle in miniature. <-done guarantees the buffered row
	// has been written (via the loop or the drain) before we read back.
	runCtx, cancelRun := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(runCtx)
		close(done)
	}()

	const huddleID = "hud-00000000000000000000000000000001"
	row := sim.DurableActionLogRow{
		ActorID:     actorID,
		OccurredAt:  time.Now().UTC(),
		ActionType:  sim.ActionTypeSpoke,
		Payload:     map[string]any{"text": "Good morrow."},
		SpeakerName: "Ezekiel",
		HuddleID:    huddleID,
		Source:      "agent",
	}
	if err := r.Append(ctx, row); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cancelRun()
	<-done

	// Read it back and assert every column the sink writes.
	var (
		gotType    string
		payloadRaw []byte
		gotResult  string
		gotSource  string
		gotSpeaker string
		gotHuddle  *string
		gotLedger  *int64
	)
	if err := f.Pool.QueryRow(ctx,
		`SELECT action_type, payload, result, source, speaker_name, huddle_id, ledger_id
		   FROM agent_action_log WHERE actor_id = $1`, actorID,
	).Scan(&gotType, &payloadRaw, &gotResult, &gotSource, &gotSpeaker, &gotHuddle, &gotLedger); err != nil {
		t.Fatalf("select back: %v", err)
	}

	if gotType != string(sim.ActionTypeSpoke) {
		t.Errorf("action_type = %q, want %q", gotType, sim.ActionTypeSpoke)
	}
	if gotResult != "ok" {
		t.Errorf("result = %q, want ok", gotResult)
	}
	if gotSource != "agent" {
		t.Errorf("source = %q, want agent", gotSource)
	}
	if gotSpeaker != "Ezekiel" {
		t.Errorf("speaker_name = %q, want Ezekiel", gotSpeaker)
	}
	if gotHuddle == nil || *gotHuddle != huddleID {
		t.Errorf("huddle_id = %v, want %q", gotHuddle, huddleID)
	}
	// A spoke row carries no ledger_id, so the universal-mirror column stays NULL
	// (LLM-494).
	if gotLedger != nil {
		t.Errorf("ledger_id = %d, want NULL (a spoke row carries no ledger_id)", *gotLedger)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		t.Fatalf("payload not valid json: %v (raw=%s)", err, payloadRaw)
	}
	if payload["text"] != "Good morrow." {
		t.Errorf("payload.text = %v, want %q", payload["text"], "Good morrow.")
	}
}

// TestActionLogRepo_Integration_NullHuddle confirms an empty HuddleID
// lands as SQL NULL (not the literal ""), matching the writer's nil-arg
// path. Outdoor / pre-huddle actions (walked, took_break) rely on this.
func TestActionLogRepo_Integration_NullHuddle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const actorID = "22222222-2222-2222-2222-222222222222"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
		actorID, "Prudence",
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	r := NewActionLogRepo(f.Pool)
	r.writeOne(sim.DurableActionLogRow{
		ActorID:     actorID,
		OccurredAt:  time.Now().UTC(),
		ActionType:  sim.ActionTypeWalked,
		Payload:     map[string]any{"destination": "the Bakery"},
		SpeakerName: "Prudence",
		HuddleID:    "", // outdoor arrival → NULL
		Source:      "agent",
	})

	var huddle *string
	if err := f.Pool.QueryRow(ctx,
		`SELECT huddle_id FROM agent_action_log WHERE actor_id = $1`, actorID,
	).Scan(&huddle); err != nil {
		t.Fatalf("select back: %v", err)
	}
	if huddle != nil {
		t.Errorf("huddle_id = %q, want NULL", *huddle)
	}
}

// TestActionLogRepo_Integration_LedgerIDColumn confirms the write path mirrors a
// payload's ledger_id into the typed column (LLM-494). The column is derived in
// SQL from the same payload the row stores (insertActionLogSQL), so a paid row
// built with a sim.LedgerID — the production type the cascade writes — lands that
// integer in the column, and a value beyond bigint's range lands NULL rather than
// wrapping to a different (negative) number.
func TestActionLogRepo_Integration_LedgerIDColumn(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	r := NewActionLogRepo(f.Pool)
	i64 := func(v int64) *int64 { return &v }

	cases := []struct {
		name    string
		actorID string
		ledger  sim.LedgerID
		want    *int64 // nil = NULL column
	}{
		{"normal id", "33333333-3333-3333-3333-333333333333", sim.LedgerID(477), i64(477)},
		// A uint64 past bigint's range: the SQL guard NULLs it rather than wrapping
		// to a negative column value — the write path cannot store what bigint can't
		// hold, and never mis-mirrors the payload as a different number.
		{"beyond bigint range", "44444444-4444-4444-4444-444444444444", sim.LedgerID(18446744073709551615), nil},
	}

	for _, c := range cases {
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
			c.actorID, "Ezekiel ("+c.name+")", // display_name is unique
		); err != nil {
			t.Fatalf("%s: seed actor: %v", c.name, err)
		}
		r.writeOne(sim.DurableActionLogRow{
			ActorID:     sim.ActorID(c.actorID),
			OccurredAt:  time.Now().UTC(),
			ActionType:  sim.ActionTypePaid,
			Payload:     map[string]any{"recipient": "John Ellis", "amount": 3, "ledger_id": c.ledger},
			SpeakerName: "Ezekiel",
			Source:      "agent",
		})

		var got *int64
		if err := f.Pool.QueryRow(ctx,
			`SELECT ledger_id FROM agent_action_log WHERE actor_id = $1`, c.actorID,
		).Scan(&got); err != nil {
			t.Fatalf("%s: select back: %v", c.name, err)
		}
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: ledger_id = %d, want NULL", c.name, *got)
		case c.want != nil && (got == nil || *got != *c.want):
			t.Errorf("%s: ledger_id = %v, want %d", c.name, got, *c.want)
		}
	}
}
