package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// fakeClientEventStore is an in-test ClientEventStore: it records what the
// handlers asked for and returns canned rows (or an error), so the routes run
// without a pg pool.
type fakeClientEventStore struct {
	inserted  []sim.ClientEvent
	insertErr error
	rows      []sim.ClientEvent
	counts    []sim.ClientEventDayCount
	gotFilter sim.ClientEventFilter
	gotLimit  int
}

func (f *fakeClientEventStore) InsertClientEvent(_ context.Context, ev sim.ClientEvent) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, ev)
	return nil
}

func (f *fakeClientEventStore) LoadClientEvents(_ context.Context, filter sim.ClientEventFilter, limit int) ([]sim.ClientEvent, error) {
	f.gotFilter = filter
	f.gotLimit = limit
	return f.rows, nil
}

func (f *fakeClientEventStore) CountClientEvents(_ context.Context, filter sim.ClientEventFilter) ([]sim.ClientEventDayCount, error) {
	f.gotFilter = filter
	return f.counts, nil
}

// clientEventServer builds an umbilical-enabled Server whose client-event routes
// are backed by store. A nil store is left unwired so the 503 path is reachable.
func clientEventServer(t *testing.T, perms map[string][]string, store ClientEventStore) http.Handler {
	t.Helper()
	srv := NewServer(seededWorld(t), permAuth{perms})
	srv.SetTelemetry(telemetry.New(8))
	if store != nil {
		srv.SetClientEventStore(store)
	}
	return srv.Handler()
}

// pcWorldServer is clientEventServer over a world holding a PC bound to the
// test login "op", so the route can stamp the caller's actor id.
func pcWorldServer(t *testing.T, store ClientEventStore) http.Handler {
	t.Helper()
	w := seededWorld(t)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["pc-op"] = &sim.Actor{ID: "pc-op", DisplayName: "Op", Kind: sim.KindPC, LoginUsername: "op"}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed PC: %v", err)
	}
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	srv.SetClientEventStore(store)
	return srv.Handler()
}

// An allowlisted event is stored with an engine-stamped time, user and PC; the
// client cannot set those, and the detail is stored compacted.
func TestClientEvent_StoresWithEngineStamps(t *testing.T) {
	store := &fakeClientEventStore{}
	h := pcWorldServer(t, store)
	before := time.Now().UTC()

	rec := postBody(t, h, "/api/village/client-event", "tok",
		`{"kind":"treeline_figure_gone","detail":{ "reason": "walked", "seconds_shown": 7 }}`, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("client-event POST = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(store.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(store.inserted))
	}
	ev := store.inserted[0]
	if ev.Kind != "treeline_figure_gone" {
		t.Errorf("kind = %q", ev.Kind)
	}
	if ev.Username != "op" || ev.ActorID != "pc-op" {
		t.Errorf("user/actor = %q/%q, want op/pc-op (engine-stamped)", ev.Username, ev.ActorID)
	}
	if ev.At.Before(before) {
		t.Errorf("at = %v, want engine time at or after %v", ev.At, before)
	}
	if string(ev.Detail) != `{"reason":"walked","seconds_shown":7}` {
		t.Errorf("detail = %s, want compacted object", ev.Detail)
	}
}

// A user with no PC is stored with an empty actor id; an absent or null detail
// is stored as an empty object.
func TestClientEvent_NoPCAndEmptyDetail(t *testing.T) {
	store := &fakeClientEventStore{}
	h := clientEventServer(t, operatorPerms, store)
	for _, body := range []string{
		`{"kind":"treeline_figure_shown"}`,
		`{"kind":"treeline_figure_shown","detail":null}`,
	} {
		if rec := postBody(t, h, "/api/village/client-event", "tok", body, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("%s = %d, want 204; body=%s", body, rec.Code, rec.Body.String())
		}
	}
	for i, ev := range store.inserted {
		if ev.ActorID != "" {
			t.Errorf("row %d actor = %q, want empty (no PC for op)", i, ev.ActorID)
		}
		if string(ev.Detail) != "{}" {
			t.Errorf("row %d detail = %s, want {}", i, ev.Detail)
		}
	}
}

// Bad input is refused and nothing is stored.
func TestClientEvent_Refusals(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown kind", `{"kind":"made_up"}`},
		{"no kind", `{"detail":{}}`},
		{"array detail", `{"kind":"treeline_figure_shown","detail":[1,2]}`},
		{"string detail", `{"kind":"treeline_figure_shown","detail":"x"}`},
		{"oversize detail", `{"kind":"treeline_figure_shown","detail":{"x":"` + strings.Repeat("a", clientEventMaxDetail) + `"}}`},
		{"unknown field", `{"kind":"treeline_figure_shown","username":"someone"}`},
		{"not json", `kind=treeline_figure_shown`},
		// One Decode reads only the first value; the rest of the body must be empty.
		{"second object", `{"kind":"treeline_figure_shown"}{"kind":"treeline_figure_gone"}`},
		{"trailing garbage", `{"kind":"treeline_figure_shown"} junk`},
		{"over the body cap after a valid object", `{"kind":"treeline_figure_shown"}` + strings.Repeat(" ", clientEventMaxBody)},
		// The detail is a flat object of plain values that Postgres jsonb accepts.
		{"nested object", `{"kind":"treeline_figure_shown","detail":{"x":{"y":1}}}`},
		{"array value", `{"kind":"treeline_figure_shown","detail":{"x":[1]}}`},
		{"NUL in a string", `{"kind":"treeline_figure_shown","detail":{"x":"a\u0000b"}}`},
		{"NUL in a key", `{"kind":"treeline_figure_shown","detail":{"a\u0000":1}}`},
		{"number past the bound", `{"kind":"treeline_figure_shown","detail":{"x":1e16}}`},
		{"number past float range", `{"kind":"treeline_figure_shown","detail":{"x":1e1000000}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeClientEventStore{}
			h := clientEventServer(t, operatorPerms, store)
			if rec := postBody(t, h, "/api/village/client-event", "tok", tc.body, nil); rec.Code != http.StatusBadRequest {
				t.Errorf("= %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if len(store.inserted) != 0 {
				t.Errorf("stored %d rows, want 0", len(store.inserted))
			}
		})
	}
}

// The engine stores its own encoding, never the client's bytes: an unpaired
// surrogate becomes U+FFFD and a tiny number becomes its parsed value, so
// Postgres jsonb accepts what the route accepts.
func TestClientEvent_DetailReencoded(t *testing.T) {
	store := &fakeClientEventStore{}
	h := clientEventServer(t, operatorPerms, store)
	rec := postBody(t, h, "/api/village/client-event", "tok",
		`{"kind":"treeline_figure_shown","detail":{"s":"\ud800","tiny":1e-1000000,"n":12,"ok":true,"none":null}}`, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("= %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if len(store.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(store.inserted))
	}
	want := `{"n":12,"none":null,"ok":true,"s":"` + "�" + `","tiny":0}`
	if got := string(store.inserted[0].Detail); got != want {
		t.Errorf("detail = %s, want %s", got, want)
	}
}

// The write needs a session, and answers 503 with no store and 500 when the
// store fails.
func TestClientEvent_AuthStoreAndFailure(t *testing.T) {
	body := `{"kind":"treeline_figure_shown"}`
	if rec := postBody(t, clientEventServer(t, operatorPerms, &fakeClientEventStore{}), "/api/village/client-event", "", body, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
	if rec := postBody(t, clientEventServer(t, operatorPerms, nil), "/api/village/client-event", "tok", body, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no store = %d, want 503", rec.Code)
	}
	failing := &fakeClientEventStore{insertErr: errors.New("boom")}
	if rec := postBody(t, clientEventServer(t, operatorPerms, failing), "/api/village/client-event", "tok", body, nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("store error = %d, want 500", rec.Code)
	}
}

// Any authed player may write — the route is not operator-gated — but the
// per-user rate cap returns 429 past clientEventRateMax in one window.
func TestClientEvent_PlayerWriteAndRateCap(t *testing.T) {
	store := &fakeClientEventStore{}
	h := clientEventServer(t, nil, store) // authed, no plugins/administer
	body := `{"kind":"treeline_figure_shown"}`
	for i := 0; i < clientEventRateMax; i++ {
		if rec := postBody(t, h, "/api/village/client-event", "tok", body, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("report %d = %d, want 204", i, rec.Code)
		}
	}
	if rec := postBody(t, h, "/api/village/client-event", "tok", body, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("report past the cap = %d, want 429", rec.Code)
	}
	if len(store.inserted) != clientEventRateMax {
		t.Errorf("stored %d, want %d", len(store.inserted), clientEventRateMax)
	}
}

// The read returns the day counts and the newest rows, passes the filter
// through, over-fetches one row to set has_more, and is operator-gated.
func TestUmbilicalClientEvents_Read(t *testing.T) {
	day := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	store := &fakeClientEventStore{
		counts: []sim.ClientEventDayCount{{Day: day, Kind: "treeline_figure_shown", Count: 3}},
		rows: []sim.ClientEvent{
			{At: day.Add(20 * time.Hour), Username: "jeff", ActorID: "pc-jeff", Kind: "treeline_figure_shown", Detail: []byte(`{"distance_tiles":12}`)},
			{At: day.Add(19 * time.Hour), Username: "wendy", Kind: "treeline_figure_shown", Detail: []byte(`{}`)},
			{At: day.Add(18 * time.Hour), Username: "jeff", Kind: "treeline_figure_shown"},
		},
	}
	h := clientEventServer(t, operatorPerms, store)
	rec := req(t, h, "/api/village/umbilical/client-events?kind=treeline_figure_shown&since=2026-10-01T00:00:00Z&until=2026-11-01T00:00:00Z&limit=2", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("client-events = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if store.gotFilter.Kind != "treeline_figure_shown" || !store.gotFilter.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) ||
		!store.gotFilter.Until.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("filter = %+v", store.gotFilter)
	}
	if store.gotLimit != 3 {
		t.Errorf("limit asked = %d, want 3 (cap + 1)", store.gotLimit)
	}
	var out UmbilicalClientEventsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Counts) != 1 || out.Counts[0].Day != "2026-10-29" || out.Counts[0].Count != 3 {
		t.Errorf("counts = %+v", out.Counts)
	}
	if out.Returned != 2 || !out.HasMore || len(out.Events) != 2 {
		t.Fatalf("returned/has_more/events = %d/%v/%d, want 2/true/2", out.Returned, out.HasMore, len(out.Events))
	}
	if out.Events[0].ActorID != "pc-jeff" || string(out.Events[0].Detail) != `{"distance_tiles":12}` {
		t.Errorf("first event = %+v", out.Events[0])
	}

	if rec := req(t, clientEventServer(t, nil, store), "/api/village/umbilical/client-events", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator read = %d, want 403", rec.Code)
	}
	if rec := req(t, h, "/api/village/umbilical/client-events?since=yesterday", "tok"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad since = %d, want 400", rec.Code)
	}
	if rec := req(t, clientEventServer(t, operatorPerms, nil), "/api/village/umbilical/client-events", "tok"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no store read = %d, want 503", rec.Code)
	}
}
