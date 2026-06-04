package simpush

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// dispatcher_test.go — the rollover/catch-up control flow, exercised with a
// fake clock + in-memory Store + recording Poster. No real PG or network: the
// pg Store and the HTTP Poster have their own integration / httptest coverage.

// fixedNow pins "today" at 2026-06-04 12:00 UTC, so yesterday is 2026-06-03.
func fixedNow() time.Time { return time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC) }

const yesterday = "2026-06-03"

type fakeStore struct {
	cursor    string
	setCalls  []string
	actors    []sim.AgentActor
	events    map[sim.ActorID][]sim.SimDayEvent
	cursorErr error
	setErr    error
	actorsErr error
	eventsErr map[sim.ActorID]error
}

func (f *fakeStore) LastPushedDay(context.Context) (string, error) {
	return f.cursor, f.cursorErr
}

func (f *fakeStore) SetLastPushedDay(_ context.Context, day string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.cursor = day
	f.setCalls = append(f.setCalls, day)
	return nil
}

func (f *fakeStore) AgentizedActors(context.Context) ([]sim.AgentActor, error) {
	return f.actors, f.actorsErr
}

func (f *fakeStore) DayEvents(_ context.Context, actorID sim.ActorID, _, _ time.Time) ([]sim.SimDayEvent, error) {
	if err := f.eventsErr[actorID]; err != nil {
		return nil, err
	}
	return f.events[actorID], nil
}

type postCall struct {
	agent  string
	day    string
	events []sim.SimDayEvent
}

type fakePoster struct {
	calls  []postCall
	errFor map[string]error // per-agent error, keyed by agent slug
}

func (f *fakePoster) PostDay(_ context.Context, agent, day string, events []sim.SimDayEvent) error {
	f.calls = append(f.calls, postCall{agent: agent, day: day, events: events})
	return f.errFor[agent]
}

func newDispatcher(store Store, poster Poster) *Dispatcher {
	return NewDispatcher(store, poster, WithClock(fixedNow))
}

// TestDispatch_FirstRunInit: an empty cursor anchors at yesterday and pushes
// nothing (no backfill of pre-existing rows on a fresh deploy).
func TestDispatch_FirstRunInit(t *testing.T) {
	store := &fakeStore{cursor: ""}
	poster := &fakePoster{}
	newDispatcher(store, poster).dispatchOnce(context.Background())

	if store.cursor != yesterday {
		t.Errorf("cursor = %q, want %q", store.cursor, yesterday)
	}
	if len(poster.calls) != 0 {
		t.Errorf("posted %d batches, want 0 on first-run init", len(poster.calls))
	}
}

// TestDispatch_CaughtUp: cursor already at yesterday → no push, no cursor write.
func TestDispatch_CaughtUp(t *testing.T) {
	store := &fakeStore{cursor: yesterday}
	poster := &fakePoster{}
	newDispatcher(store, poster).dispatchOnce(context.Background())

	if len(store.setCalls) != 0 {
		t.Errorf("cursor written %v, want none when caught up", store.setCalls)
	}
	if len(poster.calls) != 0 {
		t.Errorf("posted %d batches, want 0 when caught up", len(poster.calls))
	}
}

// TestDispatch_CatchUp: a 2-day gap pushes each missing day for each agentized
// actor, in order, stamping the cursor after each day, and hands the actor's
// events through to the poster.
func TestDispatch_CatchUp(t *testing.T) {
	a1 := sim.ActorID("act-1")
	a2 := sim.ActorID("act-2")
	ev := sim.SimDayEvent{At: fixedNow(), Kind: sim.ActionTypeSpoke, Payload: map[string]any{"text": "hi"}, Speaker: "Ezekiel"}
	store := &fakeStore{
		cursor: "2026-06-01",
		actors: []sim.AgentActor{{ID: a1, Agent: "salem-ezekiel"}, {ID: a2, Agent: "salem-john"}},
		events: map[sim.ActorID][]sim.SimDayEvent{a1: {ev}, a2: nil},
	}
	poster := &fakePoster{}
	newDispatcher(store, poster).dispatchOnce(context.Background())

	// Cursor stamped 06-02 then 06-03 (through yesterday), in order.
	wantSets := []string{"2026-06-02", "2026-06-03"}
	if len(store.setCalls) != len(wantSets) {
		t.Fatalf("cursor writes = %v, want %v", store.setCalls, wantSets)
	}
	for i, w := range wantSets {
		if store.setCalls[i] != w {
			t.Errorf("cursor write[%d] = %q, want %q", i, store.setCalls[i], w)
		}
	}
	if store.cursor != yesterday {
		t.Errorf("final cursor = %q, want %q", store.cursor, yesterday)
	}
	// 2 days × 2 actors = 4 posts.
	if len(poster.calls) != 4 {
		t.Fatalf("posted %d batches, want 4 (2 days × 2 actors)", len(poster.calls))
	}
	// Agent 1's events flow through to the poster.
	for _, c := range poster.calls {
		if c.agent == "salem-ezekiel" {
			if len(c.events) != 1 || c.events[0].Speaker != "Ezekiel" {
				t.Errorf("salem-ezekiel %s events = %+v, want the seeded event", c.day, c.events)
			}
		}
	}
}

// TestDispatch_InvalidCursor: a malformed cursor re-anchors at yesterday and
// pushes nothing (defends the catch-up loop from spinning).
func TestDispatch_InvalidCursor(t *testing.T) {
	store := &fakeStore{cursor: "not-a-date"}
	poster := &fakePoster{}
	newDispatcher(store, poster).dispatchOnce(context.Background())

	if store.cursor != yesterday {
		t.Errorf("cursor = %q, want re-anchored to %q", store.cursor, yesterday)
	}
	if len(poster.calls) != 0 {
		t.Errorf("posted %d batches, want 0 on invalid cursor", len(poster.calls))
	}
}

// TestDispatch_PosterFailureHaltsCursor: when an actor's POST fails, the day is
// not counted (cursor stays put) so the next tick retries — but the other
// actors on that day are still attempted.
func TestDispatch_PosterFailureHaltsCursor(t *testing.T) {
	a1 := sim.ActorID("act-1")
	a2 := sim.ActorID("act-2")
	store := &fakeStore{
		cursor: "2026-06-02", // one day behind → pushes 06-03 only
		actors: []sim.AgentActor{{ID: a1, Agent: "salem-ezekiel"}, {ID: a2, Agent: "salem-john"}},
		events: map[sim.ActorID][]sim.SimDayEvent{},
	}
	poster := &fakePoster{errFor: map[string]error{"salem-ezekiel": errors.New("boom")}}
	newDispatcher(store, poster).dispatchOnce(context.Background())

	if len(store.setCalls) != 0 {
		t.Errorf("cursor written %v, want none when a push failed", store.setCalls)
	}
	if store.cursor != "2026-06-02" {
		t.Errorf("cursor = %q, want unchanged 2026-06-02", store.cursor)
	}
	// Both actors attempted despite one failing.
	if len(poster.calls) != 2 {
		t.Errorf("posted %d batches, want 2 (both actors attempted)", len(poster.calls))
	}
}

// TestDispatch_DayEventsErrorStillAttemptsOthers: a per-actor DayEvents failure
// is logged and skipped, the other actors still post, and the day is not
// counted (so it retries).
func TestDispatch_DayEventsErrorStillAttemptsOthers(t *testing.T) {
	a1 := sim.ActorID("act-1")
	a2 := sim.ActorID("act-2")
	store := &fakeStore{
		cursor:    "2026-06-02",
		actors:    []sim.AgentActor{{ID: a1, Agent: "salem-ezekiel"}, {ID: a2, Agent: "salem-john"}},
		events:    map[sim.ActorID][]sim.SimDayEvent{a2: nil},
		eventsErr: map[sim.ActorID]error{a1: errors.New("query failed")},
	}
	poster := &fakePoster{}
	newDispatcher(store, poster).dispatchOnce(context.Background())

	if len(store.setCalls) != 0 {
		t.Errorf("cursor written %v, want none when an actor's events failed", store.setCalls)
	}
	// a1's events errored (no post); a2 still posted.
	if len(poster.calls) != 1 || poster.calls[0].agent != "salem-john" {
		t.Errorf("posts = %+v, want only salem-john", poster.calls)
	}
}

// TestNextDay covers the date arithmetic incl. a month boundary and a bad input.
func TestNextDay(t *testing.T) {
	got, err := nextDay("2026-06-30")
	if err != nil {
		t.Fatalf("nextDay: %v", err)
	}
	if got != "2026-07-01" {
		t.Errorf("nextDay(2026-06-30) = %q, want 2026-07-01", got)
	}
	if _, err := nextDay("garbage"); err == nil {
		t.Error("nextDay(garbage) = nil error, want a parse error")
	}
}
