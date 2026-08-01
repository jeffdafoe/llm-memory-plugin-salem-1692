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

	// LLM-586: an editor placement is born decorative, and decoratives are
	// exempt from the uniqueness rule, so an explicit duplicate is ACCEPTED.
	dupID, err := mkNPC("Villager 2", 300)
	if err != nil {
		t.Fatalf("explicit duplicate create for a decorative should be allowed: %v", err)
	}
	if got := nameOf(dupID); got != "Villager 2" {
		t.Errorf("explicit duplicate name = %q, want %q — it must not be silently deduped", got, "Villager 2")
	}

	// The headline case: several ducks all called "Duck".
	var duckIDs []sim.ActorID
	for i := 0; i < 3; i++ {
		id, err := mkNPC("Duck", float64(600+32*i))
		if err != nil {
			t.Fatalf("duck #%d named Duck: %v", i, err)
		}
		duckIDs = append(duckIDs, id)
	}
	for i, id := range duckIDs {
		if got := nameOf(id); got != "Duck" {
			t.Errorf("duck #%d = %q, want %q", i, got, "Duck")
		}
	}

	// Renaming a decorative onto an in-use name is likewise allowed, and
	// renaming to its own current name stays a no-op success.
	if _, err := w.Send(sim.SetActorDisplayName(ids[1], "Villager")); err != nil {
		t.Errorf("renaming a decorative onto an in-use name should be allowed: %v", err)
	}
	if _, err := w.Send(sim.SetActorDisplayName(ids[1], "Villager")); err != nil {
		t.Errorf("self-rename no-op: %v", err)
	}

	// A cap-length explicit name creates twice without complaint now.
	long := ""
	for i := 0; i < sim.MaxActorDisplayNameLen-2; i++ {
		long += "x"
	}
	if _, err := mkNPC(long, 400); err != nil {
		t.Fatalf("cap-adjacent create: %v", err)
	}
	if _, err := mkNPC(long, 432); err != nil {
		t.Errorf("cap-adjacent duplicate for a decorative should be allowed: %v", err)
	}
}

// TestDisplayNameUniquenessDrivenActors — the exemption is decorative-ONLY.
// An actor with an agent behind it can still speak, be paid and be remembered
// (npc_acquaintance is keyed by NAME), so a duplicate there is the original
// checkpoint-killing bug and must still be refused. This is the control that
// stops the LLM-586 exemption from having quietly disabled the rule outright.
func TestDisplayNameUniquenessDrivenActors(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	seedDuckSprite(handles)
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	defer func() { cancel(); <-done }()

	now := time.Now()
	mkDriven := func(name, agent string, x float64) sim.ActorID {
		t.Helper()
		res, err := w.Send(sim.CreateNPC(name, string(duckSpriteID), sim.WorldPos{X: x, Y: 100}, now))
		if err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
		id := res.(sim.CreateNPCResult).ActorID
		// Linking an agent reclassifies decorative -> stateful live, which is
		// what takes the actor OUT of the exemption.
		if _, err := w.Send(sim.SetActorAgentLink(id, agent)); err != nil {
			t.Fatalf("link %q: %v", name, err)
		}
		return id
	}

	josiah := mkDriven("Josiah Thorne", "zbbs-josiah-thorne", 100)
	ezekiel := mkDriven("Ezekiel Crane", "zbbs-ezekiel-crane", 200)

	kindOf := func(id sim.ActorID) sim.ActorKind {
		res, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			return world.Actors[id].Kind, nil
		}})
		return res.(sim.ActorKind)
	}
	if got := kindOf(josiah); got == sim.KindDecorative {
		t.Fatalf("setup: linked actor is still decorative (%v) — the refusals below would prove nothing", got)
	}

	// Renaming one driven actor onto another's name is still refused.
	if _, err := w.Send(sim.SetActorDisplayName(ezekiel, "Josiah Thorne")); !errors.Is(err, sim.ErrDisplayNameTaken) {
		t.Errorf("driven rename onto an in-use driven name err = %v, want ErrDisplayNameTaken", err)
	}

	// A DECORATIVE may take a driven actor's name: the constraint's predicate
	// covers only driven rows, so the decorative row is outside the index.
	// Asserted so the in-memory gate and the database predicate cannot drift —
	// if this ever needs to be forbidden, the constraint must change too.
	res, err := w.Send(sim.CreateNPC("Josiah Thorne", string(duckSpriteID), sim.WorldPos{X: 300, Y: 100}, now))
	if err != nil {
		t.Fatalf("decorative taking a driven actor's name should be allowed (mirrors the DB predicate): %v", err)
	}
	impostor := res.(sim.CreateNPCResult).ActorID

	// ...but PROMOTING that decorative would create the forbidden state, so
	// the link must be refused rather than silently producing two driven
	// "Josiah Thorne"s — the exact row pair the constraint rejects at COMMIT.
	if _, err := w.Send(sim.SetActorAgentLink(impostor, "zbbs-impostor")); !errors.Is(err, sim.ErrDisplayNameTaken) {
		t.Errorf("promoting a decorative onto an in-use driven name err = %v, want ErrDisplayNameTaken", err)
	}

	// The gate reads the persisted columns, not Kind, so it holds for a PC too
	// — a login is the other half of the constraint's OR.
	if _, err := w.Send(sim.CreatePC("player-1", "Wendy", string(duckSpriteID), now)); err != nil {
		t.Fatalf("create PC: %v", err)
	}
	if !sim.ActorIsDriven(actorOf(t, w, ezekiel)) {
		t.Error("an agent-linked actor must read as driven")
	}
	pcs := actorsNamed(t, w, "Wendy")
	if len(pcs) != 1 || !sim.ActorIsDriven(pcs[0]) {
		t.Fatalf("expected exactly one driven PC named Wendy, got %d", len(pcs))
	}
	// A decorative may still take the PC's name (outside the predicate)...
	res2, err := w.Send(sim.CreateNPC("Wendy", string(duckSpriteID), sim.WorldPos{X: 400, Y: 100}, now))
	if err != nil {
		t.Fatalf("decorative taking a PC's name should be allowed: %v", err)
	}
	// ...but promoting THAT one is refused, proving the gate covers the login
	// arm of the predicate and not just the agent arm.
	if _, err := w.Send(sim.SetActorAgentLink(res2.(sim.CreateNPCResult).ActorID, "zbbs-wendy-impostor")); !errors.Is(err, sim.ErrDisplayNameTaken) {
		t.Errorf("promoting a decorative onto an in-use PC name err = %v, want ErrDisplayNameTaken", err)
	}
}

// actorOf reads one actor off the world goroutine.
func actorOf(t *testing.T, w *sim.World, id sim.ActorID) *sim.Actor {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id], nil
	}})
	if err != nil {
		t.Fatalf("read actor %s: %v", id, err)
	}
	return res.(*sim.Actor)
}

// actorsNamed collects every actor carrying the given display name.
func actorsNamed(t *testing.T, w *sim.World, name string) []*sim.Actor {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		var out []*sim.Actor
		for _, a := range world.Actors {
			if a != nil && a.DisplayName == name {
				out = append(out, a)
			}
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("scan actors named %q: %v", name, err)
	}
	return res.([]*sim.Actor)
}
