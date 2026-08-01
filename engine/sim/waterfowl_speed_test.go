package sim_test

// waterfowl_speed_test.go — LLM-580: the waterfowl slow-walk (advance every
// WaterfowlStepDivisor-th locomotion tick) and display-name uniqueness (a
// duplicate breaks the checkpoint's UNIQUE upsert, so it is refused at the
// door).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestWaterfowlHalfSpeedLocomotion — over 2*N locomotion ticks a waterfowl
// advances N tiles while a villager walking the same distance advances 2*N
// (capped at arrival). Exercises the WaterfowlStepDivisor beat end-to-end
// through EvaluateLocomotion.
func TestWaterfowlHalfSpeedLocomotion(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makePondTerrain())
	seedDuckSprite(handles)
	handles.Sprites.Seed(map[sim.SpriteID]*sim.Sprite{
		"sprite-villager": {ID: "sprite-villager", Name: "Villager A"},
	})
	// Duck swims east along the pond's middle row; villager walks east on
	// open grass well away from the pond. Both routes are straight lines
	// with no contention, so tile-per-step behavior is exact.
	duckStart := sim.Position{X: pondMinX, Y: pondMinY + 2}
	villagerStart := sim.Position{X: sim.PadX + 40, Y: sim.PadY + 40}
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"duck-speed": {
			ID: "duck-speed", DisplayName: "Duck", Kind: sim.KindDecorative,
			SpriteID: duckSpriteID, Pos: duckStart,
		},
		"villager-speed": {
			ID: "villager-speed", DisplayName: "Goody Walker", Kind: sim.KindDecorative,
			SpriteID: "sprite-villager", Pos: villagerStart,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now()
	duckDest := sim.Position{X: pondMinX + 6, Y: pondMinY + 2}
	villagerDest := sim.Position{X: villagerStart.X + 6, Y: villagerStart.Y}
	if _, err := w.Send(sim.MoveActor("duck-speed",
		sim.MoveDestination{Kind: sim.MoveDestinationPosition, Position: &duckDest}, false, now)); err != nil {
		t.Fatalf("duck MoveActor: %v", err)
	}
	if _, err := w.Send(sim.MoveActor("villager-speed",
		sim.MoveDestination{Kind: sim.MoveDestinationPosition, Position: &villagerDest}, false, now)); err != nil {
		t.Fatalf("villager MoveActor: %v", err)
	}

	// 4 locomotion ticks: villager steps 4 tiles, duck steps 2.
	for i := 0; i < 4; i++ {
		if _, err := w.Send(sim.EvaluateLocomotion(now.Add(time.Duration(i) * time.Second))); err != nil {
			t.Fatalf("EvaluateLocomotion #%d: %v", i, err)
		}
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return [2]int{
			world.Actors["duck-speed"].Pos.X - duckStart.X,
			world.Actors["villager-speed"].Pos.X - villagerStart.X,
		}, nil
	}})
	if err != nil {
		t.Fatalf("read positions: %v", err)
	}
	steps := res.([2]int)
	if steps[0] != 2 {
		t.Errorf("duck advanced %d tiles over 4 ticks, want 2 (every %d-th tick)", steps[0], sim.WaterfowlStepDivisor)
	}
	if steps[1] != 4 {
		t.Errorf("villager advanced %d tiles over 4 ticks, want 4", steps[1])
	}

	// Supersede mid-walk: the beat re-arms on every accepted MoveActor, so
	// the FIRST tick after a new destination always steps — deterministic
	// cadence regardless of where the previous walk's beat ended.
	posBefore, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["duck-speed"].Pos, nil
	}})
	if err != nil {
		t.Fatalf("read pos: %v", err)
	}
	westDest := sim.Position{X: pondMinX, Y: pondMinY + 2}
	if _, err := w.Send(sim.MoveActor("duck-speed",
		sim.MoveDestination{Kind: sim.MoveDestinationPosition, Position: &westDest}, false, now)); err != nil {
		t.Fatalf("supersede MoveActor: %v", err)
	}
	if _, err := w.Send(sim.EvaluateLocomotion(now.Add(10 * time.Second))); err != nil {
		t.Fatalf("post-supersede tick: %v", err)
	}
	res, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["duck-speed"].Pos, nil
	}})
	if err != nil {
		t.Fatalf("read pos: %v", err)
	}
	from := posBefore.(sim.Position)
	to := res.(sim.Position)
	moved := absInt(to.X-from.X) + absInt(to.Y-from.Y)
	if moved != 1 {
		t.Errorf("first tick after supersede moved %d tiles, want exactly 1 (beat re-armed)", moved)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// TestDisplayNameUniqueness — the checkpoint-killing duplicate is refused at
// creation and rename, and the blank-name default self-dedupes.
func TestDisplayNameUniqueness(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	seedDuckSprite(handles)
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now()
	mkNPC := func(name string, x float64) (sim.ActorID, error) {
		res, err := w.Send(sim.CreateNPC(name, string(duckSpriteID), sim.WorldPos{X: x, Y: 100}, now))
		if err != nil {
			return "", err
		}
		return res.(sim.CreateNPCResult).ActorID, nil
	}
	nameOf := func(id sim.ActorID) string {
		res, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			return world.Actors[id].DisplayName, nil
		}})
		return res.(string)
	}

	// Blank names self-dedupe: Villager, Villager 2, Villager 3.
	var ids []sim.ActorID
	for i := 0; i < 3; i++ {
		id, err := mkNPC("", float64(100+32*i))
		if err != nil {
			t.Fatalf("blank-name create #%d: %v", i, err)
		}
		ids = append(ids, id)
	}
	for i, want := range []string{"Villager", "Villager 2", "Villager 3"} {
		if got := nameOf(ids[i]); got != want {
			t.Errorf("defaulted name #%d = %q, want %q", i, got, want)
		}
	}

	// An explicit duplicate is refused outright.
	if _, err := mkNPC("Villager 2", 300); !errors.Is(err, sim.ErrDisplayNameTaken) {
		t.Errorf("explicit duplicate create err = %v, want ErrDisplayNameTaken", err)
	}

	// Rename onto an in-use name is refused; rename to a fresh name works;
	// renaming an actor to its own current name stays a no-op success.
	if _, err := w.Send(sim.SetActorDisplayName(ids[1], "Villager")); !errors.Is(err, sim.ErrDisplayNameTaken) {
		t.Errorf("rename onto in-use name err = %v, want ErrDisplayNameTaken", err)
	}
	if _, err := w.Send(sim.SetActorDisplayName(ids[1], "Drake")); err != nil {
		t.Errorf("rename to fresh name: %v", err)
	}
	if _, err := w.Send(sim.SetActorDisplayName(ids[1], "Drake")); err != nil {
		t.Errorf("self-rename no-op: %v", err)
	}

	// A cap-length explicit name still creates once and refuses its
	// duplicate cleanly. (The dedupe suffix only ever applies to the short
	// "Villager" default, so it cannot push a name past the cap.)
	long := ""
	for i := 0; i < sim.MaxActorDisplayNameLen-2; i++ {
		long += "x"
	}
	if _, err := mkNPC(long, 400); err != nil {
		t.Fatalf("cap-adjacent create: %v", err)
	}
	if _, err := mkNPC(long, 432); !errors.Is(err, sim.ErrDisplayNameTaken) {
		t.Errorf("cap-adjacent duplicate err = %v, want ErrDisplayNameTaken", err)
	}
}
