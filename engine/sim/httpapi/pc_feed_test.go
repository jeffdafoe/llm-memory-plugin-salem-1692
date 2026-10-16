package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_feed_test.go — LLM-797: the player's walk to a cow and the feed.

func seedCow(t *testing.T, w *sim.World, id string, x, y, milk int) {
	t.Helper()
	max, period := 4, 12
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[sim.ActorID(id)] = &sim.Actor{
			ID: sim.ActorID(id), DisplayName: "Cow 2", Kind: sim.KindDecorative, State: sim.StateIdle,
			Pos: sim.Position{X: x, Y: y},
			Yield: &sim.LivestockYield{Row: sim.ObjectRefresh{
				GatherItem: "milk", AvailableQuantity: &milk, MaxQuantity: &max,
				RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: &period,
			}},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedCow: %v", err)
	}
}

func TestHandlePCMove_LivestockWalksBesideAndHolds(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	seedCow(t, w, "cow2", 16, 10, 4)
	srv := NewServer(w, okAuth{})

	rec := post(t, srv, "/api/village/pc/move", `{"destination":{"kind":"livestock","actor_id":"cow2"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	r, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.CloneActor(world.Actors["pc-tester"]), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	pc := r.(*sim.Actor)
	if pc.HerdTargetID != "cow2" {
		t.Errorf("herd target = %q, want cow2 (the cow waits for the player)", pc.HerdTargetID)
	}
	if pc.MoveIntent == nil || pc.MoveIntent.Destination.Position == nil {
		t.Fatalf("intent = %+v, want a walk to a tile", pc.MoveIntent)
	}
	if got := *pc.MoveIntent.Destination.Position; got != (sim.Position{X: 15, Y: 10}) {
		t.Errorf("destination = %v, want (15,10): beside the cow, level, on the near side", got)
	}

	for _, tc := range []struct{ body, why string }{
		{`{"destination":{"kind":"livestock"}}`, "no actor_id"},
		{`{"destination":{"kind":"livestock","actor_id":"cow2","object_id":"x"}}`, "a second target"},
	} {
		if rec := post(t, srv, "/api/village/pc/move", tc.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.why, rec.Code)
		}
	}
	if rec := post(t, srv, "/api/village/pc/move", `{"destination":{"kind":"livestock","actor_id":"pc-tester"}}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("walk to a non-animal: status = %d, want 422", rec.Code)
	}
}

func TestHandlePCFeed(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	seedCow(t, w, "cow2", 11, 10, 0)
	srv := NewServer(w, okAuth{})

	if rec := post(t, srv, "/api/village/pc/feed", `{"actor_id":"cow2"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("feed with no sage: status = %d, want 422", rec.Code)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["pc-tester"].Inventory = map[sim.ItemKind]int{"sage": 1}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if rec := post(t, srv, "/api/village/pc/feed", `{"actor_id":"cow2"}`); rec.Code != http.StatusOK {
		t.Fatalf("feed: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// A second feed: the player is told, in the scene's words, that she is fed.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["pc-tester"].Inventory = map[sim.ItemKind]int{"sage": 1}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	rec := post(t, srv, "/api/village/pc/feed", `{"actor_id":"cow2"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Cow 2 doesn't seem hungry") {
		t.Errorf("second feed: status %d body %s, want 422 \"Cow 2 doesn't seem hungry\"", rec.Code, rec.Body.String())
	}
	if rec := post(t, srv, "/api/village/pc/feed", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no actor_id: status = %d, want 400", rec.Code)
	}
}

// code_review round 2 asked whether a player in a conversation can walk to a cow:
// the livestock walk leaves the huddle like every other PC move (MoveActor with
// leaveHuddleFirst true, inside moveToLivestockLabeled).
func TestHandlePCMove_LivestockLeavesTheHuddle(t *testing.T) {
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	seedCow(t, w, "cow2", 16, 10, 4)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles["h1"] = &sim.Huddle{ID: "h1", Members: map[sim.ActorID]struct{}{"pc-tester": {}, "bram": {}}}
		world.Actors["pc-tester"].CurrentHuddleID = "h1"
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/pc/move", `{"destination":{"kind":"livestock","actor_id":"cow2"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	r, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return string(world.Actors["pc-tester"].CurrentHuddleID), nil
	}})
	if r.(string) != "" {
		t.Errorf("still in huddle %q after walking to the cow", r)
	}
}
