package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_visitor_integration_test.go — real-pg coverage for LLM-573: a
// transient visitor's beats persist with actor_id NULL, and reach the co-present
// resident's day note.
//
// The substrate is the whole point here, so pgxmock cannot stand in: the
// question is whether the uuid column with its FK to actor(id) accepts a NULL
// author at all, and whether loadDayEventsSQL's cross-actor leg — which keys on
// action_type + huddle_id and never looks at actor_id — carries such a row into
// somebody else's day. Both are answers only a genuine server gives.

// TestActionLogRepo_Integration_VisitorRowPersistsWithNullActor: the sink writes
// a blanked ActorID as SQL NULL rather than failing the uuid cast the raw
// "vstr-" id used to fail (the LLM-379 error flood), and the row keeps the
// visitor's display name — the only identity a NULL-actor row carries.
func TestActionLogRepo_Integration_VisitorRowPersistsWithNullActor(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	r := NewActionLogRepo(f.Pool)
	const huddleID = "hud-00000000000000000000000000000573"
	r.writeOne(sim.DurableActionLogRow{
		ActorID:     "", // World.AppendActionLogDurable blanks a visitor's "vstr-" id
		OccurredAt:  time.Now().UTC(),
		ActionType:  sim.ActionTypeSpoke,
		Payload:     map[string]any{"text": "Nine will do, at three apiece."},
		SpeakerName: "Tobias Hewes the nail-buyer",
		HuddleID:    huddleID,
		Source:      "agent",
	})

	var (
		gotActor   *string
		gotSpeaker string
		gotHuddle  *string
	)
	if err := f.Pool.QueryRow(ctx,
		`SELECT actor_id, speaker_name, huddle_id FROM agent_action_log WHERE speaker_name = $1`,
		"Tobias Hewes the nail-buyer",
	).Scan(&gotActor, &gotSpeaker, &gotHuddle); err != nil {
		t.Fatalf("select back the visitor row: %v", err)
	}
	if gotActor != nil {
		t.Errorf("actor_id = %q, want NULL (a visitor is not in the actor aggregate)", *gotActor)
	}
	if gotSpeaker != "Tobias Hewes the nail-buyer" {
		t.Errorf("speaker_name = %q, want the visitor's display name", gotSpeaker)
	}
	// The huddle is what carries this row into a resident's day note, so a NULL
	// author must not cost it.
	if gotHuddle == nil || *gotHuddle != huddleID {
		t.Errorf("huddle_id = %v, want %q", gotHuddle, huddleID)
	}
}

// TestLoadDayEvents_VisitorSpeechReachesCoPresentResident replays the LLM-573
// trigger scene at the Blacksmith. Before the fix Ezekiel's durable day held
// only his own lines — he answered questions nobody asked and agreed to a price
// nobody proposed. His day must now read as a conversation.
//
// It also pins the two deliberate non-inclusions, both of which are the
// pre-existing treatment of any buyer rather than anything visitor-specific: the
// visitor's `paid` row does not enter Ezekiel's day (the cross-actor leg is
// `spoke`-only, and his own `delivered` row already carries the amount), and the
// visitor has no day of its own.
func TestLoadDayEvents_VisitorSpeechReachesCoPresentResident(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const ezekiel = "33333333-3333-3333-3333-333333333333"
	seedActorForLog(t, ctx, f.Pool, ezekiel, "Ezekiel Crane")

	at := func(h, m int) time.Time {
		return loadDayStart.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	const forge = "hud-blacksmith"
	const tobias = "Tobias Hewes the nail-buyer"

	insertAgentActionRow(t, ctx, f.Pool, ezekiel, "spoke", "Ezekiel Crane", forge,
		`{"text":"Afternoon. What nails do you need?"}`, at(19, 3), "ok")
	// The visitor's half — NULL actor_id, same huddle.
	insertAgentActionRow(t, ctx, f.Pool, "", "spoke", tobias, forge,
		`{"text":"Nine, for framing. Lynn has none to spare."}`, at(19, 4), "ok")
	insertAgentActionRow(t, ctx, f.Pool, ezekiel, "delivered", "Ezekiel Crane", forge,
		`{"qty":9,"item":"nail","amount":27,"recipient":"Tobias Hewes the nail-buyer"}`, at(19, 5), "ok")
	insertAgentActionRow(t, ctx, f.Pool, "", "paid", tobias, forge,
		`{"recipient":"Ezekiel Crane","amount":27,"for":"9x nail"}`, at(19, 5), "ok")
	// A NULL-actor speech row from a huddle Ezekiel was never in must stay out —
	// the interval scoping is what bounds inclusion, and a NULL author must not
	// slip past it.
	insertAgentActionRow(t, ctx, f.Pool, "", "spoke", "Elias Drum the peddler", "hud-tavern",
		`{"text":"Wares from Boston!"}`, at(19, 4), "ok")

	got, err := NewActionLogRepo(f.Pool).LoadDayEvents(ctx, ezekiel, loadDayStart, loadDayEnd)
	if err != nil {
		t.Fatalf("LoadDayEvents: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (Ezekiel's two rows + the visitor's speech): %+v", len(got), got)
	}
	wantSpeakers := []string{"Ezekiel Crane", tobias, "Ezekiel Crane"}
	wantKinds := []sim.ActionType{sim.ActionTypeSpoke, sim.ActionTypeSpoke, sim.ActionTypeDelivered}
	for i := range wantSpeakers {
		if got[i].Speaker != wantSpeakers[i] {
			t.Errorf("got[%d].Speaker = %q, want %q", i, got[i].Speaker, wantSpeakers[i])
		}
		if got[i].Kind != wantKinds[i] {
			t.Errorf("got[%d].Kind = %q, want %q", i, got[i].Kind, wantKinds[i])
		}
	}
	if got[1].Payload["text"] != "Nine, for framing. Lynn has none to spare." {
		t.Errorf("the visitor's line did not survive: %v", got[1].Payload["text"])
	}
	// The money is in the scene through Ezekiel's own delivered row, which is why
	// the `spoke`-only cross-actor leg needs no widening.
	if got[2].Payload["amount"] != float64(27) {
		t.Errorf("delivered payload.amount = %v, want 27", got[2].Payload["amount"])
	}
}

// TestLoadSettlements_Integration_NullActorBuyer is the audit's one real
// NULL-safety break: LoadSettlements SELECTs actor_id and used to scan it into a
// plain string, so the first visitor settlement would have failed the whole read
// with "cannot scan NULL into *string" — taking every resident settlement in the
// window down with it, not just the visitor's row.
func TestLoadSettlements_Integration_NullActorBuyer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const hannah = "44444444-4444-4444-4444-444444444444"
	seedActorForLog(t, ctx, f.Pool, hannah, "Hannah Boggs")

	at := loadDayStart.Add(19 * time.Hour)
	insertAgentActionRow(t, ctx, f.Pool, "", "paid", "Tobias Hewes the nail-buyer", "hud-blacksmith",
		`{"recipient":"Ezekiel Crane","amount":27,"for":"9x nail","ledger_id":4001}`, at, "ok")
	insertAgentActionRow(t, ctx, f.Pool, hannah, "paid", "Hannah Boggs", "hud-inn",
		`{"recipient":"Josiah Thorne","amount":2,"for":"1 firewood","ledger_id":4002}`, at.Add(time.Minute), "ok")

	got, err := NewActionLogRepo(f.Pool).LoadSettlements(ctx, sim.SettlementFilter{}, 10)
	if err != nil {
		t.Fatalf("LoadSettlements: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (the resident's row and the visitor's): %+v", len(got), got)
	}

	// Most-recent first: Hannah, then the visitor.
	if got[0].BuyerID != hannah || got[0].BuyerName != "Hannah Boggs" {
		t.Errorf("resident settlement = %+v; want Hannah with her actor id", got[0])
	}
	if got[1].BuyerID != "" {
		t.Errorf("visitor settlement BuyerID = %q, want \"\" (actor_id is NULL)", got[1].BuyerID)
	}
	if got[1].BuyerName != "Tobias Hewes the nail-buyer" {
		t.Errorf("visitor settlement BuyerName = %q; the name is the only identity the row carries", got[1].BuyerName)
	}
	if got[1].SellerName != "Ezekiel Crane" || got[1].Amount != 27 {
		t.Errorf("visitor settlement terms = %+v, want Ezekiel / 27", got[1])
	}

	// The actor filter still scopes to a real actor — a NULL author matches
	// nothing, so a visitor's settlement never leaks into a resident's filtered view.
	filtered, err := NewActionLogRepo(f.Pool).LoadSettlements(ctx, sim.SettlementFilter{ActorID: hannah}, 10)
	if err != nil {
		t.Fatalf("LoadSettlements(actor=hannah): %v", err)
	}
	if len(filtered) != 1 || filtered[0].BuyerID != hannah {
		t.Errorf("filtered = %+v, want only Hannah's settlement", filtered)
	}
}
