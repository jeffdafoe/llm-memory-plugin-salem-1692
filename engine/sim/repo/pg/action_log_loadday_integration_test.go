package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_loadday_integration_test.go — real-pg coverage for the cross-actor
// "heard" day pull (ZBBS-WORK-376 piece 2, ActionLogRepo.LoadDayEvents). The
// presence-interval CTEs (LEAD windowing, seed carryover, NULL-huddle
// boundaries) are exactly the substrate pgxmock can't validate, so these run
// against the migrated schema via the embedded-postgres fixture.

// dayWindow is the fixed UTC day every LoadDayEvents test scopes to.
var (
	loadDayStart = time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	loadDayEnd   = loadDayStart.Add(24 * time.Hour)
)

func seedActorForLog(t *testing.T, ctx context.Context, pool Pool, id, name string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
		id, name,
	); err != nil {
		t.Fatalf("seed actor %s: %v", name, err)
	}
}

// insertAgentActionRow writes one agent_action_log row directly, so a test can
// control occurred_at / huddle_id / result precisely (the async sink always
// stamps result='ok' and a live timestamp). Empty huddle → SQL NULL.
func insertAgentActionRow(t *testing.T, ctx context.Context, pool Pool, actorID, actionType, speaker, huddle, payloadJSON string, occurredAt time.Time, result string) {
	t.Helper()
	var h any
	if huddle != "" {
		h = huddle
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_action_log
		    (actor_id, occurred_at, source, action_type, payload, result, speaker_name, huddle_id)
		 VALUES ($1, $2, 'agent', $3, $4::jsonb, $5, $6, $7)`,
		actorID, occurredAt, actionType, payloadJSON, result, speaker, h,
	); err != nil {
		t.Fatalf("insert %s row for %s: %v", actionType, speaker, err)
	}
}

// TestLoadDayEvents_OwnRows: an actor's own committed rows come back in
// occurred_at order with payloads decoded, and the window + result='ok' filters
// exclude before-window, at-or-after-dayEnd, and non-ok rows.
func TestLoadDayEvents_OwnRows(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const a = "11111111-1111-1111-1111-111111111111"
	seedActorForLog(t, ctx, f.Pool, a, "Ezekiel")

	at := func(h, m int) time.Time {
		return loadDayStart.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}

	insertAgentActionRow(t, ctx, f.Pool, a, "spoke", "Ezekiel", "hud-1", `{"text":"morning"}`, at(9, 0), "ok")
	insertAgentActionRow(t, ctx, f.Pool, a, "paid", "Ezekiel", "hud-1", `{"recipient":"Bob","amount":3,"for":"bread"}`, at(9, 5), "ok")
	insertAgentActionRow(t, ctx, f.Pool, a, "walked", "Ezekiel", "", `{"destination":"the Bakery"}`, at(10, 0), "ok")
	// Excluded: before the window, at dayEnd (half-open), and a rejected row.
	insertAgentActionRow(t, ctx, f.Pool, a, "spoke", "Ezekiel", "hud-1", `{"text":"last-night"}`, loadDayStart.Add(-1*time.Hour), "ok")
	insertAgentActionRow(t, ctx, f.Pool, a, "spoke", "Ezekiel", "hud-1", `{"text":"next-day"}`, loadDayEnd, "ok")
	insertAgentActionRow(t, ctx, f.Pool, a, "spoke", "Ezekiel", "hud-1", `{"text":"rejected"}`, at(9, 30), "rejected")

	got, err := NewActionLogRepo(f.Pool).LoadDayEvents(ctx, a, loadDayStart, loadDayEnd)
	if err != nil {
		t.Fatalf("LoadDayEvents: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (before-window / dayEnd / rejected must be excluded): %+v", len(got), got)
	}
	wantKinds := []sim.ActionType{sim.ActionTypeSpoke, sim.ActionTypePaid, sim.ActionTypeWalked}
	for i, w := range wantKinds {
		if got[i].Kind != w {
			t.Errorf("got[%d].Kind = %q, want %q", i, got[i].Kind, w)
		}
		if got[i].Speaker != "Ezekiel" {
			t.Errorf("got[%d].Speaker = %q, want Ezekiel", i, got[i].Speaker)
		}
	}
	if got[0].Payload["text"] != "morning" {
		t.Errorf("spoke payload.text = %v, want morning", got[0].Payload["text"])
	}
	// amount round-trips through jsonb as a float64.
	if got[1].Payload["recipient"] != "Bob" || got[1].Payload["amount"] != float64(3) {
		t.Errorf("paid payload = %v, want recipient=Bob amount=3", got[1].Payload)
	}
	if got[2].Payload["destination"] != "the Bakery" {
		t.Errorf("walked payload.destination = %v, want the Bakery", got[2].Payload["destination"])
	}
}

// TestLoadDayEvents_CrossActorHeardSpeech: a huddle-mate's `spoke` row is
// included only while the target is co-present in that huddle. Excludes speech
// after the target walked away (NULL-huddle boundary ends the interval), speech
// in a different huddle, and a non-spoke action by a huddle-mate.
func TestLoadDayEvents_CrossActorHeardSpeech(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		a = "11111111-1111-1111-1111-111111111111" // target
		b = "22222222-2222-2222-2222-222222222222" // huddle-mate
		c = "33333333-3333-3333-3333-333333333333" // elsewhere
	)
	seedActorForLog(t, ctx, f.Pool, a, "Ezekiel")
	seedActorForLog(t, ctx, f.Pool, b, "Bob")
	seedActorForLog(t, ctx, f.Pool, c, "Cyrus")

	at := func(h, m int) time.Time {
		return loadDayStart.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}

	// Target's presence in hud-1: [09:00 spoke, 09:30 walked-away). The walk
	// stamps NULL huddle and bounds the interval.
	insertAgentActionRow(t, ctx, f.Pool, a, "spoke", "Ezekiel", "hud-1", `{"text":"a-greets"}`, at(9, 0), "ok")
	insertAgentActionRow(t, ctx, f.Pool, a, "walked", "Ezekiel", "", `{"destination":"the Square"}`, at(9, 30), "ok")
	// Heard: B speaks in hud-1 inside the interval.
	insertAgentActionRow(t, ctx, f.Pool, b, "spoke", "Bob", "hud-1", `{"text":"b-in-interval"}`, at(9, 10), "ok")
	// Not heard: B speaks in hud-1 after the target left; B pays (not speech);
	// C speaks in a different huddle.
	insertAgentActionRow(t, ctx, f.Pool, b, "spoke", "Bob", "hud-1", `{"text":"b-after-left"}`, at(9, 45), "ok")
	insertAgentActionRow(t, ctx, f.Pool, b, "paid", "Bob", "hud-1", `{"recipient":"Ezekiel","amount":2}`, at(9, 10), "ok")
	insertAgentActionRow(t, ctx, f.Pool, c, "spoke", "Cyrus", "hud-2", `{"text":"c-other-huddle"}`, at(9, 10), "ok")

	got, err := NewActionLogRepo(f.Pool).LoadDayEvents(ctx, a, loadDayStart, loadDayEnd)
	if err != nil {
		t.Fatalf("LoadDayEvents: %v", err)
	}

	// Expect, in order: A spoke 09:00, B spoke 09:10, A walked 09:30.
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3: %+v", len(got), got)
	}
	if got[1].Speaker != "Bob" || got[1].Payload["text"] != "b-in-interval" {
		t.Errorf("got[1] = {speaker:%q text:%v}, want Bob/b-in-interval", got[1].Speaker, got[1].Payload["text"])
	}
	for _, e := range got {
		switch e.Payload["text"] {
		case "b-after-left":
			t.Errorf("included speech after target walked away: %+v", e)
		case "c-other-huddle":
			t.Errorf("included speech from a different huddle: %+v", e)
		}
		if e.Speaker == "Bob" && e.Kind == sim.ActionTypePaid {
			t.Errorf("included a huddle-mate's non-spoke action: %+v", e)
		}
	}
}

// TestLoadDayEvents_SeedCarryover: when the target sits silently in a huddle
// across midnight, its last pre-window row seeds the huddle so cross-actor
// speech from day-start onward is still attributed.
func TestLoadDayEvents_SeedCarryover(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		a = "11111111-1111-1111-1111-111111111111" // target — silent all window
		b = "22222222-2222-2222-2222-222222222222" // speaks in the seeded huddle
		c = "33333333-3333-3333-3333-333333333333" // speaks elsewhere
	)
	seedActorForLog(t, ctx, f.Pool, a, "Ezekiel")
	seedActorForLog(t, ctx, f.Pool, b, "Bob")
	seedActorForLog(t, ctx, f.Pool, c, "Cyrus")

	// Target's last action before the window is in hud-1 → seed huddle.
	insertAgentActionRow(t, ctx, f.Pool, a, "spoke", "Ezekiel", "hud-1", `{"text":"a-last-night"}`, loadDayStart.Add(-1*time.Hour), "ok")
	// In-window cross-actor speech: B in the seeded huddle (heard), C elsewhere.
	insertAgentActionRow(t, ctx, f.Pool, b, "spoke", "Bob", "hud-1", `{"text":"b-morning"}`, loadDayStart.Add(8*time.Hour), "ok")
	insertAgentActionRow(t, ctx, f.Pool, c, "spoke", "Cyrus", "hud-2", `{"text":"c-elsewhere"}`, loadDayStart.Add(8*time.Hour), "ok")

	got, err := NewActionLogRepo(f.Pool).LoadDayEvents(ctx, a, loadDayStart, loadDayEnd)
	if err != nil {
		t.Fatalf("LoadDayEvents: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (only B's seeded-huddle speech): %+v", len(got), got)
	}
	if got[0].Speaker != "Bob" || got[0].Payload["text"] != "b-morning" {
		t.Errorf("got[0] = {speaker:%q text:%v}, want Bob/b-morning", got[0].Speaker, got[0].Payload["text"])
	}
}

// TestLoadDayEvents_Empty: an actor with no rows returns a non-nil empty slice
// (so a JSON-marshaling caller emits "[]", which the API endpoint requires).
func TestLoadDayEvents_Empty(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const a = "11111111-1111-1111-1111-111111111111"
	seedActorForLog(t, ctx, f.Pool, a, "Ezekiel")

	got, err := NewActionLogRepo(f.Pool).LoadDayEvents(ctx, a, loadDayStart, loadDayEnd)
	if err != nil {
		t.Fatalf("LoadDayEvents: %v", err)
	}
	if got == nil {
		t.Fatal("got = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0: %+v", len(got), got)
	}
}
