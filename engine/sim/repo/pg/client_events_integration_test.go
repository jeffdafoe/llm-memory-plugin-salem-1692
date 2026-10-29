package pg

// Real-pg integration tests for client events (LLM-880): rows round-trip through
// the real client_event columns and CHECK, the filter and limit narrow the read
// newest first, and the day counts group by UTC day and kind.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestIntegration_ClientEvents_InsertLoadCount(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewClientEventsRepo(f.Pool)
	day1 := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)

	events := []sim.ClientEvent{
		{At: day1.Add(23 * time.Hour), Username: "jeff", ActorID: "pc-jeff", Kind: "treeline_figure_shown", Detail: []byte(`{"distance_tiles":12}`)},
		{At: day2.Add(1 * time.Hour), Username: "jeff", ActorID: "pc-jeff", Kind: "treeline_figure_gone", Detail: []byte(`{"reason":"walked"}`)},
		{At: day2.Add(2 * time.Hour), Username: "wendy", Kind: "treeline_figure_shown"},
	}
	for _, ev := range events {
		if err := repo.InsertClientEvent(ctx, ev); err != nil {
			t.Fatalf("InsertClientEvent %s: %v", ev.Kind, err)
		}
	}

	all, err := repo.LoadClientEvents(ctx, sim.ClientEventFilter{}, 10)
	if err != nil {
		t.Fatalf("LoadClientEvents: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("rows = %d, want 3", len(all))
	}
	if all[0].Username != "wendy" || all[0].ActorID != "" {
		t.Errorf("newest = %+v, want wendy with no actor (NULL round-trips to empty)", all[0])
	}
	if string(all[0].Detail) != "{}" {
		t.Errorf("empty detail stored as %s, want {}", all[0].Detail)
	}
	var detail map[string]any
	if err := json.Unmarshal(all[2].Detail, &detail); err != nil || detail["distance_tiles"] != float64(12) {
		t.Errorf("oldest detail = %s (%v)", all[2].Detail, err)
	}

	shown, err := repo.LoadClientEvents(ctx, sim.ClientEventFilter{Kind: "treeline_figure_shown", Since: day2}, 10)
	if err != nil {
		t.Fatalf("LoadClientEvents filtered: %v", err)
	}
	if len(shown) != 1 || shown[0].Username != "wendy" {
		t.Errorf("kind+since filter = %+v, want only wendy's", shown)
	}
	capped, err := repo.LoadClientEvents(ctx, sim.ClientEventFilter{}, 2)
	if err != nil || len(capped) != 2 {
		t.Errorf("limit 2 = %d rows (%v), want 2", len(capped), err)
	}

	counts, err := repo.CountClientEvents(ctx, sim.ClientEventFilter{})
	if err != nil {
		t.Fatalf("CountClientEvents: %v", err)
	}
	want := []sim.ClientEventDayCount{
		{Day: day2, Kind: "treeline_figure_gone", Count: 1},
		{Day: day2, Kind: "treeline_figure_shown", Count: 1},
		{Day: day1, Kind: "treeline_figure_shown", Count: 1},
	}
	if len(counts) != len(want) {
		t.Fatalf("counts = %+v, want %+v", counts, want)
	}
	for i := range want {
		if !counts[i].Day.Equal(want[i].Day) || counts[i].Kind != want[i].Kind || counts[i].Count != want[i].Count {
			t.Errorf("count %d = %+v, want %+v", i, counts[i], want[i])
		}
	}
}

// Valid JSON that Postgres jsonb refuses (the reason the route re-encodes the
// detail as a flat object of plain values): the raw client forms fail at insert,
// and the forms the route produces from them insert cleanly.
func TestIntegration_ClientEvents_JSONBCompatibility(t *testing.T) {
	f := newFixture(t)
	repo := NewClientEventsRepo(f.Pool)
	insert := func(detail string) error {
		return repo.InsertClientEvent(t.Context(), sim.ClientEvent{
			At: time.Now().UTC(), Username: "jeff", Kind: "treeline_figure_shown", Detail: []byte(detail),
		})
	}
	for _, raw := range []string{`{"x":"\u0000"}`, `{"x":1e1000000}`, `{"x":1e-1000000}`} {
		if err := insert(raw); err == nil {
			t.Errorf("raw %s stored, want Postgres to refuse it", raw)
		}
	}
	for _, encoded := range []string{`{"x":"` + "�" + `"}`, `{"x":0}`, `{"x":1000000000000000}`} {
		if err := insert(encoded); err != nil {
			t.Errorf("route-encoded %s refused: %v", encoded, err)
		}
	}
}

// The detail CHECK refuses anything that is not a JSON object, so a row the
// route would never write cannot land by another path either.
func TestIntegration_ClientEvents_DetailMustBeObject(t *testing.T) {
	f := newFixture(t)
	repo := NewClientEventsRepo(f.Pool)
	err := repo.InsertClientEvent(t.Context(), sim.ClientEvent{
		At: time.Now().UTC(), Username: "jeff", Kind: "treeline_figure_shown", Detail: []byte(`[1,2]`),
	})
	if err == nil {
		t.Fatal("array detail stored, want the CHECK to refuse it")
	}
}
