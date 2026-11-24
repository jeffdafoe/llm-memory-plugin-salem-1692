package pg

import (
	"context"
	"testing"
	"time"
)

// action_log_settled_integration_test.go — LLM-1018, real pg. An engine-written
// row with no huddle (order_lapsed, farm_tools_worn, refunded, settled) says
// nothing about where the actor stood, so loadDayEventsSQL must not let it start
// or end a co-presence interval. And the coin-record seed must not read a
// `settled` row: the active party's row already counts the coin.

func insertActionRowWithSource(t *testing.T, ctx context.Context, pool Pool, actorID, source, actionType, speaker, huddle, payloadJSON string, occurredAt time.Time) {
	t.Helper()
	var h any
	if huddle != "" {
		h = huddle
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_action_log
		    (actor_id, occurred_at, source, action_type, payload, result, speaker_name, huddle_id)
		 VALUES ($1, $2, $3, $4, $5::jsonb, 'ok', $6, $7)`,
		actorID, occurredAt, source, actionType, payloadJSON, speaker, h,
	); err != nil {
		t.Fatalf("insert %s/%s row for %s: %v", source, actionType, speaker, err)
	}
}

func TestLoadDayEvents_EngineRowWithNoHuddleKeepsTheInterval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		a = "11111111-1111-1111-1111-111111111111" // the keeper
		b = "22222222-2222-2222-2222-222222222222" // a guest in her huddle
	)
	seedActorForLog(t, ctx, f.Pool, a, "Hannah")
	seedActorForLog(t, ctx, f.Pool, b, "Bob")

	at := func(h, m int) time.Time {
		return loadDayStart.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}

	// Before the day: she is in hud-1 at 23:00, and a sweep writes an engine row
	// with no huddle at 23:30. The seed must still be hud-1.
	insertActionRowWithSource(t, ctx, f.Pool, a, "agent", "spoke", "Hannah", "hud-1", `{"text":"a-late"}`, loadDayStart.Add(-1*time.Hour))
	insertActionRowWithSource(t, ctx, f.Pool, a, "engine", "order_lapsed", "Hannah", "", `{"text":"lapse"}`, loadDayStart.Add(-30*time.Minute))
	insertActionRowWithSource(t, ctx, f.Pool, b, "agent", "spoke", "Bob", "hud-1", `{"text":"b-after-midnight"}`, at(0, 10))

	// In the day: she speaks in hud-1, a settlement lands with no huddle, the
	// guest keeps talking, then she walks away (an agent row, NULL huddle).
	insertActionRowWithSource(t, ctx, f.Pool, a, "agent", "spoke", "Hannah", "hud-1", `{"text":"a-morning"}`, at(9, 0))
	insertActionRowWithSource(t, ctx, f.Pool, b, "agent", "spoke", "Bob", "hud-1", `{"text":"b-before-settle"}`, at(9, 10))
	insertActionRowWithSource(t, ctx, f.Pool, a, "engine", "settled", "Hannah", "", `{"settlement":"rent","text":"rent"}`, at(9, 20))
	insertActionRowWithSource(t, ctx, f.Pool, b, "agent", "spoke", "Bob", "hud-1", `{"text":"b-after-settle"}`, at(9, 30))
	insertActionRowWithSource(t, ctx, f.Pool, a, "agent", "walked", "Hannah", "", `{"destination":"the Square"}`, at(10, 0))
	insertActionRowWithSource(t, ctx, f.Pool, b, "agent", "spoke", "Bob", "hud-1", `{"text":"b-after-walk"}`, at(10, 10))

	got, err := NewActionLogRepo(f.Pool).LoadDayEvents(ctx, a, loadDayStart, loadDayEnd)
	if err != nil {
		t.Fatalf("LoadDayEvents: %v", err)
	}
	heard := map[string]bool{}
	ownSettled := false
	for _, e := range got {
		if e.Speaker == "Bob" {
			heard[e.Payload["text"].(string)] = true
		}
		if e.Speaker == "Hannah" && e.Kind == "settled" {
			ownSettled = true
		}
	}
	for _, want := range []string{"b-after-midnight", "b-before-settle", "b-after-settle"} {
		if !heard[want] {
			t.Errorf("missing overheard %q: %+v", want, got)
		}
	}
	if heard["b-after-walk"] {
		t.Errorf("included speech after she walked away: %+v", got)
	}
	if !ownSettled {
		t.Errorf("her own settled row is missing from the day: %+v", got)
	}
}

func TestCoinRecordsRepo_Integration_IgnoresSettledRows(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		keeper = "11111111-1111-1111-1111-111111111111"
		lodger = "22222222-2222-2222-2222-222222222222"
	)
	seedActorForLog(t, ctx, f.Pool, keeper, "Hannah")
	seedActorForLog(t, ctx, f.Pool, lodger, "Thomas")

	base := time.Now().UTC().Truncate(time.Microsecond)
	insertActionRowWithSource(t, ctx, f.Pool, lodger, "engine", "paid", "Thomas", "",
		`{"recipient":"Hannah","recipient_actor_id":"`+keeper+`","amount":4,"for":"a night's lodging","lodging_grant":"inn"}`, base.Add(-time.Hour))
	insertActionRowWithSource(t, ctx, f.Pool, keeper, "engine", "settled", "Hannah", "",
		`{"settlement":"rent","counterparty":"Thomas","counterparty_actor_id":"`+lodger+`","amount":4,"text":"Thomas paid you 4 coins for another night's lodging."}`, base.Add(-time.Hour))

	rows, err := NewCoinRecordsRepo(f.Pool).LoadPaymentsSince(ctx, base.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("LoadPaymentsSince: %v", err)
	}
	if len(rows) != 1 || string(rows[0].ActorID) != lodger {
		t.Fatalf("seed rows = %+v, want only the lodger's paid row", rows)
	}
}

// Other engine rows (the estate rate, the court) stamp CurrentHuddleID. A NULL
// there means the actor really was in no huddle, so it must still end the
// interval — during the day and as the last row before midnight.
func TestLoadDayEvents_EngineRowFromAnEmptyHuddleStillEndsTheInterval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		a = "11111111-1111-1111-1111-111111111111"
		b = "22222222-2222-2222-2222-222222222222"
	)
	seedActorForLog(t, ctx, f.Pool, a, "Hannah")
	seedActorForLog(t, ctx, f.Pool, b, "Bob")

	at := func(h, m int) time.Time {
		return loadDayStart.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	estate := `{"recipient":"the town","amount":2,"estate_rate":true}`

	// Before midnight: in hud-1, then an estate-rate row with no huddle (she had
	// left). The seed must be that boundary, not hud-1.
	insertActionRowWithSource(t, ctx, f.Pool, a, "agent", "spoke", "Hannah", "hud-1", `{"text":"a-late"}`, loadDayStart.Add(-1*time.Hour))
	insertActionRowWithSource(t, ctx, f.Pool, a, "engine", "paid", "Hannah", "", estate, loadDayStart.Add(-30*time.Minute))
	insertActionRowWithSource(t, ctx, f.Pool, b, "agent", "spoke", "Bob", "hud-1", `{"text":"b-after-midnight"}`, at(0, 10))

	// In the day: in hud-1, then an estate-rate row with no huddle.
	insertActionRowWithSource(t, ctx, f.Pool, a, "agent", "spoke", "Hannah", "hud-1", `{"text":"a-morning"}`, at(9, 0))
	insertActionRowWithSource(t, ctx, f.Pool, b, "agent", "spoke", "Bob", "hud-1", `{"text":"b-before"}`, at(9, 10))
	insertActionRowWithSource(t, ctx, f.Pool, a, "engine", "paid", "Hannah", "", estate, at(9, 20))
	insertActionRowWithSource(t, ctx, f.Pool, b, "agent", "spoke", "Bob", "hud-1", `{"text":"b-after-boundary"}`, at(9, 30))

	got, err := NewActionLogRepo(f.Pool).LoadDayEvents(ctx, a, loadDayStart, loadDayEnd)
	if err != nil {
		t.Fatalf("LoadDayEvents: %v", err)
	}
	heard := map[string]bool{}
	for _, e := range got {
		if e.Speaker == "Bob" {
			heard[e.Payload["text"].(string)] = true
		}
	}
	if !heard["b-before"] {
		t.Errorf("missing speech heard while present: %+v", got)
	}
	for _, not := range []string{"b-after-midnight", "b-after-boundary"} {
		if heard[not] {
			t.Errorf("included %q after an engine row from an empty huddle: %+v", not, got)
		}
	}
}
