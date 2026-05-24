package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildRestFloorWorld seeds a world with grass terrain and one free
// tiredness-easing oak (named, asset-backed, infinite supply) ten tiles east
// of where the test actors sit, plus the supplied actor. The oak's slot is far
// from the actor so an eligible actor is never already standing on it.
func buildRestFloorWorld(t *testing.T, actor *sim.Actor) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"oak-tree": {ID: "oak-tree", Category: "prop"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"oak": {ID: "oak", DisplayName: "Oak", AssetID: "oak-tree",
			Pos: sim.TileToWorld(sim.GridPoint{X: sim.PadX + 20, Y: sim.PadY + 10}),
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "tiredness", Amount: -8},
			}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{actor.ID: actor})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// eligibleVagrant returns the canonical rest-fallback candidate: an agent NPC,
// homeless, exhausted (tiredness at the red ceiling), no lodging, and on an
// always-off shift window ([720,720) is empty → always off-shift). Tests flip
// exactly one field to exercise each skip arm.
func eligibleVagrant() *sim.Actor {
	return &sim.Actor{
		ID:               "vagrant",
		Kind:             sim.KindNPCShared,
		Pos:              sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
		Needs:            map[sim.NeedKey]int{"tiredness": sim.NeedMax},
		ScheduleStartMin: intp(720),
		ScheduleEndMin:   intp(720),
	}
}

func routeRest(t *testing.T, w *sim.World, now time.Time) int {
	t.Helper()
	res, err := w.Send(sim.RouteHomelessToRest(now))
	if err != nil {
		t.Fatalf("RouteHomelessToRest: %v", err)
	}
	return res.(int)
}

func hasMoveIntent(t *testing.T, w *sim.World, id sim.ActorID) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id].MoveIntent != nil, nil
	}})
	if err != nil {
		t.Fatalf("hasMoveIntent: %v", err)
	}
	return res.(bool)
}

// TestRouteHomelessToRest_RoutesEligible: the canonical candidate is walked
// toward the oak — one route issued and a position move-intent installed.
func TestRouteHomelessToRest_RoutesEligible(t *testing.T) {
	w, cancel := buildRestFloorWorld(t, eligibleVagrant())
	defer cancel()

	if n := routeRest(t, w, time.Now().UTC()); n != 1 {
		t.Fatalf("routed = %d, want 1", n)
	}
	if !hasMoveIntent(t, w, "vagrant") {
		t.Error("eligible vagrant has no MoveIntent after routing")
	}
}

// TestRouteHomelessToRest_Skips covers every gate that should suppress the
// floor, one field flipped per case.
func TestRouteHomelessToRest_Skips(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(24 * time.Hour)

	cases := []struct {
		name   string
		mutate func(a *sim.Actor)
	}{
		{"homed", func(a *sim.Actor) { a.HomeStructureID = "some-house" }},
		{"not-exhausted", func(a *sim.Actor) { a.Needs["tiredness"] = 10 }}, // below red threshold (20)
		{"on-shift", func(a *sim.Actor) { a.ScheduleStartMin = intp(0); a.ScheduleEndMin = intp(1440) }},
		{"resting", func(a *sim.Actor) { a.SleepingUntil = &future }},
		{"not-agent", func(a *sim.Actor) { a.Kind = sim.KindDecorative }},
		{"lodger", func(a *sim.Actor) {
			a.RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 1, Source: sim.AccessSourceLedger}: {
					RoomID: 1, Source: sim.AccessSourceLedger, Active: true, ExpiresAt: &future,
				},
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := eligibleVagrant()
			c.mutate(a)
			w, cancel := buildRestFloorWorld(t, a)
			defer cancel()

			if n := routeRest(t, w, now); n != 0 {
				t.Errorf("routed = %d, want 0 (%s must be skipped)", n, c.name)
			}
			if hasMoveIntent(t, w, "vagrant") {
				t.Errorf("%s actor got a MoveIntent — should not be routed", c.name)
			}
		})
	}
}

// TestRouteHomelessToRest_SkipsHuddled: an actor in an active huddle is left
// alone (don't yank someone out of a conversation to nap).
func TestRouteHomelessToRest_SkipsHuddled(t *testing.T) {
	a := eligibleVagrant()
	a.CurrentHuddleID = "h1"
	w, cancel := buildRestFloorWorld(t, a)
	defer cancel()

	// Install an active (un-concluded) huddle the actor belongs to.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles["h1"] = &sim.Huddle{
			ID:        "h1",
			Members:   map[sim.ActorID]struct{}{"vagrant": {}},
			StartedAt: time.Now().UTC(),
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("install huddle: %v", err)
	}

	if n := routeRest(t, w, time.Now().UTC()); n != 0 {
		t.Errorf("routed = %d, want 0 (huddled actor must be skipped)", n)
	}
}

// TestRouteHomelessToRest_Idempotent: a second pass after the actor is already
// en route re-issues nothing (the alreadyHeadedToRest guard).
func TestRouteHomelessToRest_Idempotent(t *testing.T) {
	w, cancel := buildRestFloorWorld(t, eligibleVagrant())
	defer cancel()

	now := time.Now().UTC()
	if n := routeRest(t, w, now); n != 1 {
		t.Fatalf("first pass routed = %d, want 1", n)
	}
	if n := routeRest(t, w, now); n != 0 {
		t.Errorf("second pass routed = %d, want 0 (already en route)", n)
	}
}
