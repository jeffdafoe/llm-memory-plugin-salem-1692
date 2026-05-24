package sim_test

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// rotationFixture returns a world seeded with three assets exercising the
// three rotation algorithms, plus one phase-only asset (no rotation
// participation) for confirming the rotation pass skips it.
//
//	"laundry-line"    — random_per_object, three rotatable states (dirty /
//	                    wet / clean); 3 placed instances.
//	"notice-board"    — random_per_asset, five rotatable states; 2 placed
//	                    instances.
//	"deterministic-x" — deterministic, three rotatable states; 1 placement.
//	"lamp-iron"       — no rotation_algo, only day/night-active states; 1
//	                    placement.
func rotationFixture(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()

	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"laundry-line": {
			ID:           "laundry-line",
			Name:         "Laundry Line",
			Category:     "prop",
			DefaultState: "dirty",
			RotationAlgo: sim.RotationAlgoRandomPerObject,
			States: []sim.AssetState{
				{ID: 100, State: "dirty", Tags: []string{sim.TagRotatable, "laundry"}},
				{ID: 101, State: "wet", Tags: []string{sim.TagRotatable, "laundry"}},
				{ID: 102, State: "clean", Tags: []string{sim.TagRotatable, "laundry"}},
			},
		},
		"notice-board": {
			ID:           "notice-board",
			Name:         "Notice Board",
			Category:     "prop",
			DefaultState: "variant-1",
			RotationAlgo: sim.RotationAlgoRandomPerAsset,
			States: []sim.AssetState{
				{ID: 200, State: "variant-1", Tags: []string{sim.TagRotatable, "notice-board"}},
				{ID: 201, State: "variant-2", Tags: []string{sim.TagRotatable, "notice-board"}},
				{ID: 202, State: "variant-3", Tags: []string{sim.TagRotatable, "notice-board"}},
				{ID: 203, State: "variant-4", Tags: []string{sim.TagRotatable, "notice-board"}},
				{ID: 204, State: "variant-5", Tags: []string{sim.TagRotatable, "notice-board"}},
			},
		},
		"deterministic-x": {
			ID:           "deterministic-x",
			Name:         "Deterministic Cycler",
			Category:     "prop",
			DefaultState: "a",
			RotationAlgo: sim.RotationAlgoDeterministic,
			States: []sim.AssetState{
				{ID: 300, State: "a", Tags: []string{sim.TagRotatable}},
				{ID: 301, State: "b", Tags: []string{sim.TagRotatable}},
				{ID: 302, State: "c", Tags: []string{sim.TagRotatable}},
			},
		},
		"lamp-iron": {
			ID:           "lamp-iron",
			Name:         "Iron Lamp",
			Category:     "structure",
			DefaultState: "unlit",
			States: []sim.AssetState{
				{ID: 10, State: "unlit", Tags: []string{"day-active"}},
				{ID: 11, State: "lit", Tags: []string{"night-active"}},
			},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"laundry-A": {ID: "laundry-A", AssetID: "laundry-line", CurrentState: "dirty", Pos: sim.WorldPos{X: 10, Y: 10}},
		"laundry-B": {ID: "laundry-B", AssetID: "laundry-line", CurrentState: "wet", Pos: sim.WorldPos{X: 20, Y: 20}},
		"laundry-C": {ID: "laundry-C", AssetID: "laundry-line", CurrentState: "clean", Pos: sim.WorldPos{X: 30, Y: 30}},
		"notice-A":  {ID: "notice-A", AssetID: "notice-board", CurrentState: "variant-1", Pos: sim.WorldPos{X: 40, Y: 40}},
		"notice-B":  {ID: "notice-B", AssetID: "notice-board", CurrentState: "variant-1", Pos: sim.WorldPos{X: 50, Y: 50}},
		"det-X":     {ID: "det-X", AssetID: "deterministic-x", CurrentState: "a", Pos: sim.WorldPos{X: 60, Y: 60}},
		"lamp":      {ID: "lamp", AssetID: "lamp-iron", CurrentState: "lit", Pos: sim.WorldPos{X: 70, Y: 70}},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// TestPickRandomExcluding covers the helper's three branches.
func TestPickRandomExcluding(t *testing.T) {
	pool := []*sim.AssetState{
		{ID: 1, State: "a"},
		{ID: 2, State: "b"},
		{ID: 3, State: "c"},
	}
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 30; i++ {
		got := sim.PickRandomExcluding(pool, "b", r)
		if got == "b" {
			t.Fatalf("got %q, want anything except b", got)
		}
		if got != "a" && got != "c" {
			t.Fatalf("got %q, want a or c", got)
		}
	}

	t.Run("single-state pool returns the only state", func(t *testing.T) {
		single := []*sim.AssetState{{ID: 1, State: "only"}}
		if got := sim.PickRandomExcluding(single, "only", r); got != "only" {
			t.Errorf("got %q, want only", got)
		}
	})

	t.Run("empty pool returns current as fallback", func(t *testing.T) {
		if got := sim.PickRandomExcluding(nil, "x", r); got != "x" {
			t.Errorf("got %q, want x", got)
		}
	})
}

// TestPickDeterministicNext covers the cycle + fallback paths.
func TestPickDeterministicNext(t *testing.T) {
	pool := []*sim.AssetState{
		{ID: 1, State: "a"}, {ID: 2, State: "b"}, {ID: 3, State: "c"},
	}
	cases := []struct {
		from string
		want string
	}{
		{"a", "b"},
		{"b", "c"},
		{"c", "a"}, // wraps
	}
	for _, c := range cases {
		got := sim.PickDeterministicNext(pool, c.from)
		if got != c.want {
			t.Errorf("from=%q got=%q want=%q", c.from, got, c.want)
		}
	}

	t.Run("current not in pool falls back to first", func(t *testing.T) {
		if got := sim.PickDeterministicNext(pool, "missing"); got != "a" {
			t.Errorf("got %q, want a", got)
		}
	})

	t.Run("empty pool returns current", func(t *testing.T) {
		if got := sim.PickDeterministicNext(nil, "x"); got != "x" {
			t.Errorf("got %q, want x", got)
		}
	})
}

// TestExcludedByScope covers the membership branches.
func TestExcludedByScope(t *testing.T) {
	state := &sim.AssetState{Tags: []string{"laundry", "rotatable"}}
	if sim.ExcludedByScope(state, nil) {
		t.Errorf("nil excludes returned true")
	}
	if sim.ExcludedByScope(state, []string{}) {
		t.Errorf("empty excludes returned true")
	}
	if !sim.ExcludedByScope(state, []string{"laundry"}) {
		t.Errorf("matching tag returned false")
	}
	if sim.ExcludedByScope(state, []string{"notice-board"}) {
		t.Errorf("non-matching tag returned true")
	}
}

// TestAssetRotatablePool covers ordering + empty pool semantics.
func TestAssetRotatablePool(t *testing.T) {
	a := &sim.Asset{
		States: []sim.AssetState{
			{ID: 50, State: "fifty", Tags: []string{sim.TagRotatable}},
			{ID: 10, State: "ten"},
			{ID: 30, State: "thirty", Tags: []string{sim.TagRotatable}},
			{ID: 20, State: "twenty", Tags: []string{sim.TagRotatable}},
		},
	}
	pool := a.RotatablePool()
	if len(pool) != 3 {
		t.Fatalf("pool len = %d, want 3", len(pool))
	}
	want := []string{"twenty", "thirty", "fifty"} // by ID asc: 20, 30, 50
	for i, s := range pool {
		if s.State != want[i] {
			t.Errorf("pool[%d] = %q, want %q", i, s.State, want[i])
		}
	}
	t.Run("no rotatable states returns nil", func(t *testing.T) {
		b := &sim.Asset{States: []sim.AssetState{{ID: 1, State: "x"}}}
		if got := b.RotatablePool(); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}

// TestDetermineRotationFlips_FullScope covers the unscoped pass: every
// rotatable instance generates a flip. Lamp (no rotation_algo) is left
// alone.
func TestDetermineRotationFlips_FullScope(t *testing.T) {
	w, cancel := rotationFixture(t)
	defer cancel()

	r := rand.New(rand.NewSource(1))
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.DetermineRotationFlipsForTest(world, sim.RotationScope{}, r), nil
	}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	flips := res.([]sim.PendingFlip)

	// 3 laundry + 2 notice + 1 det = 6 candidates. Each must produce a
	// flip (the rotation algos avoid current=new where possible).
	if len(flips) != 6 {
		t.Fatalf("flip count = %d, want 6 (got %+v)", len(flips), flips)
	}
	byID := map[sim.VillageObjectID]sim.PendingFlip{}
	for _, f := range flips {
		byID[f.ObjectID] = f
	}
	// Lamp not included (no rotation_algo).
	if _, ok := byID["lamp"]; ok {
		t.Errorf("lamp included in rotation flips")
	}
	// Notice boards (random_per_asset) — both flip to the same target.
	noticeA, hasA := byID["notice-A"]
	noticeB, hasB := byID["notice-B"]
	if !hasA || !hasB {
		t.Fatalf("notice boards missing from flips")
	}
	if noticeA.NewState != noticeB.NewState {
		t.Errorf("random_per_asset diverged: notice-A=%q notice-B=%q",
			noticeA.NewState, noticeB.NewState)
	}
	// Deterministic: a → b.
	detX := byID["det-X"]
	if detX.NewState != "b" {
		t.Errorf("det-X next = %q, want b", detX.NewState)
	}
}

// TestDetermineRotationFlips_ScopeNarrowsToTag confirms scope.Tag
// filters to instances whose current state carries the tag.
func TestDetermineRotationFlips_ScopeNarrowsToTag(t *testing.T) {
	w, cancel := rotationFixture(t)
	defer cancel()

	r := rand.New(rand.NewSource(1))
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.DetermineRotationFlipsForTest(
			world,
			sim.RotationScope{Tag: "laundry"},
			r,
		), nil
	}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	flips := res.([]sim.PendingFlip)
	if len(flips) != 3 {
		t.Errorf("laundry scope flips = %d, want 3 (got %+v)", len(flips), flips)
	}
	for _, f := range flips {
		if string(f.ObjectID) != "laundry-A" && string(f.ObjectID) != "laundry-B" && string(f.ObjectID) != "laundry-C" {
			t.Errorf("non-laundry flip: %s", f.ObjectID)
		}
	}
}

// TestDetermineRotationFlips_ScopeExcludesTags filters OUT tags that
// belong to other dispatchers (the NPC-route hand-off path).
func TestDetermineRotationFlips_ScopeExcludesTags(t *testing.T) {
	w, cancel := rotationFixture(t)
	defer cancel()

	r := rand.New(rand.NewSource(1))
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.DetermineRotationFlipsForTest(
			world,
			sim.RotationScope{ExcludeTags: []string{"laundry", "notice-board"}},
			r,
		), nil
	}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	flips := res.([]sim.PendingFlip)
	// Only det-X remains.
	if len(flips) != 1 {
		t.Errorf("exclude scope flips = %d, want 1 (got %+v)", len(flips), flips)
	}
	if len(flips) == 1 && flips[0].ObjectID != "det-X" {
		t.Errorf("expected det-X only, got %s", flips[0].ObjectID)
	}
}

// TestDetermineRotationFlips_NilRandReturnsNil — defensive.
func TestDetermineRotationFlips_NilRandReturnsNil(t *testing.T) {
	w, cancel := rotationFixture(t)
	defer cancel()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.DetermineRotationFlipsForTest(world, sim.RotationScope{}, nil), nil
	}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	flips := res.([]sim.PendingFlip)
	if len(flips) != 0 {
		t.Errorf("nil rand returned %d flips, want 0", len(flips))
	}
}

// TestApplyDailyRotation_StampsAndEmits covers the end-to-end Command:
// stamps LastRotationAt, bumps Gen, returns RotationResult, emits
// RotationApplied event.
func TestApplyDailyRotation_StampsAndEmits(t *testing.T) {
	w, cancel := rotationFixture(t)
	defer cancel()

	// Subscribe before invoking so we can observe the event.
	var got []*sim.RotationApplied
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if e, ok := evt.(*sim.RotationApplied); ok {
				got = append(got, e)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	stamp := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)
	res, err := w.Send(sim.ApplyDailyRotation(
		sim.RotationTickInputs{Now: stamp, Rand: rand.New(rand.NewSource(1))},
		sim.RotationScope{},
	))
	if err != nil {
		t.Fatalf("ApplyDailyRotation: %v", err)
	}
	result, ok := res.(sim.RotationResult)
	if !ok {
		t.Fatalf("returned %T, want RotationResult", res)
	}
	if !result.At.Equal(stamp) {
		t.Errorf("result.At = %v, want %v", result.At, stamp)
	}
	if result.Gen == 0 {
		t.Errorf("result.Gen = 0, want >0 (Gen should be bumped)")
	}
	if result.ObjectsAffected != 6 {
		t.Errorf("result.ObjectsAffected = %d, want 6", result.ObjectsAffected)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	evt := got[0]
	if evt.Gen != result.Gen {
		t.Errorf("event Gen = %d, result Gen = %d", evt.Gen, result.Gen)
	}
	if evt.ObjectsAffected != 6 {
		t.Errorf("event ObjectsAffected = %d, want 6", evt.ObjectsAffected)
	}

	// LastRotationAt mutated.
	envRes, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Environment.LastRotationAt, nil
	}})
	if err != nil {
		t.Fatalf("read LastRotationAt: %v", err)
	}
	last := envRes.(time.Time)
	if !last.Equal(stamp) {
		t.Errorf("LastRotationAt = %v, want %v", last, stamp)
	}
}

// TestApplyDailyRotation_ExcludedTagsRoundTrip — event carries the
// ExcludedTags scope, defensively copied.
func TestApplyDailyRotation_ExcludedTagsRoundTrip(t *testing.T) {
	w, cancel := rotationFixture(t)
	defer cancel()

	var got *sim.RotationApplied
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if e, ok := evt.(*sim.RotationApplied); ok {
				got = e
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	excludes := []string{"laundry", "notice-board"}
	if _, err := w.Send(sim.ApplyDailyRotation(
		sim.RotationTickInputs{Now: time.Now().UTC(), Rand: rand.New(rand.NewSource(1))},
		sim.RotationScope{ExcludeTags: excludes},
	)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got == nil {
		t.Fatalf("event not received")
	}
	if len(got.ExcludedTags) != 2 {
		t.Fatalf("ExcludedTags len = %d, want 2 (got %v)", len(got.ExcludedTags), got.ExcludedTags)
	}
	if got.ExcludedTags[0] != "laundry" || got.ExcludedTags[1] != "notice-board" {
		t.Errorf("ExcludedTags = %v, want [laundry notice-board]", got.ExcludedTags)
	}

	// Mutate the caller's slice and confirm the event's copy is unaffected.
	excludes[0] = "MUTATED"
	if got.ExcludedTags[0] == "MUTATED" {
		t.Errorf("event ExcludedTags aliased caller slice")
	}
}

// TestApplyDailyRotation_NilRandErrors covers the caller-bug path.
func TestApplyDailyRotation_NilRandErrors(t *testing.T) {
	w, cancel := rotationFixture(t)
	defer cancel()

	_, err := w.Send(sim.ApplyDailyRotation(
		sim.RotationTickInputs{Now: time.Now().UTC(), Rand: nil},
		sim.RotationScope{},
	))
	if err == nil {
		t.Errorf("nil Rand: expected error")
	}
}

// TestApplyDailyRotation_IdempotentAfterConverge covers the design call:
// a redundant invocation against a world that cannot produce flips
// (single-state rotatable pools — nowhere to rotate TO) returns
// ObjectsAffected=0 BUT still stamps LastRotationAt + bumps Gen, mirroring
// ApplyPhaseTransition's "force-rotate always stamps" semantics.
//
// Setup uses a fixture where every rotatable asset has a single-state
// pool, so the substrate genuinely cannot flip anything regardless of
// RNG choice — exercises the actual no-op path, not the "happened to
// pick the same state" path.
func TestApplyDailyRotation_IdempotentAfterConverge(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"single-state-prop": {
			ID:           "single-state-prop",
			Name:         "Single-State Prop",
			Category:     "prop",
			DefaultState: "only",
			RotationAlgo: sim.RotationAlgoRandomPerObject,
			States: []sim.AssetState{
				{ID: 1, State: "only", Tags: []string{sim.TagRotatable}},
			},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"prop-A": {ID: "prop-A", AssetID: "single-state-prop", CurrentState: "only", Pos: sim.WorldPos{X: 1, Y: 1}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	defer cancel()

	r := rand.New(rand.NewSource(1))
	stamp := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	res1, err := w.Send(sim.ApplyDailyRotation(
		sim.RotationTickInputs{Now: stamp, Rand: r},
		sim.RotationScope{},
	))
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	r1 := res1.(sim.RotationResult)
	if r1.ObjectsAffected != 0 {
		t.Errorf("first pass ObjectsAffected = %d, want 0 (single-state pool)", r1.ObjectsAffected)
	}
	if r1.Gen == 0 {
		t.Errorf("first pass Gen = 0, want >0 (stamp advances even on no-op)")
	}

	second := stamp.Add(24 * time.Hour)
	res2, err := w.Send(sim.ApplyDailyRotation(
		sim.RotationTickInputs{Now: second, Rand: r},
		sim.RotationScope{},
	))
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	r2 := res2.(sim.RotationResult)
	if r2.ObjectsAffected != 0 {
		t.Errorf("second pass ObjectsAffected = %d, want 0", r2.ObjectsAffected)
	}
	if r2.Gen <= r1.Gen {
		t.Errorf("Gen did not advance across no-op passes: r1.Gen=%d r2.Gen=%d", r1.Gen, r2.Gen)
	}
	envRes, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Environment.LastRotationAt, nil
	}})
	if err != nil {
		t.Fatalf("read LastRotationAt: %v", err)
	}
	if got := envRes.(time.Time); !got.Equal(second) {
		t.Errorf("LastRotationAt = %v, want %v", got, second)
	}
}

// TestDetermineRotationFlips_RandomPerAssetConverges covers code_review
// R1 finding 1: when multiple instances of a random_per_asset asset start
// in DIFFERENT current states, the per-asset target is picked over the
// FULL candidate set (excluding all currents when possible) so that "all
// instances flip to one target" actually holds — no instance is silently
// skipped because the memo'd target happened to equal its current state.
//
// Setup: two noticeboards, one at variant-1 and one at variant-2. Pool
// has 5 states. After rotation, both must end up at the SAME state which
// is neither variant-1 nor variant-2. ObjectsAffected must be 2.
func TestDetermineRotationFlips_RandomPerAssetConverges(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"notice-board": {
			ID:           "notice-board",
			Name:         "Notice Board",
			Category:     "prop",
			DefaultState: "variant-1",
			RotationAlgo: sim.RotationAlgoRandomPerAsset,
			States: []sim.AssetState{
				{ID: 200, State: "variant-1", Tags: []string{sim.TagRotatable, "notice-board"}},
				{ID: 201, State: "variant-2", Tags: []string{sim.TagRotatable, "notice-board"}},
				{ID: 202, State: "variant-3", Tags: []string{sim.TagRotatable, "notice-board"}},
				{ID: 203, State: "variant-4", Tags: []string{sim.TagRotatable, "notice-board"}},
				{ID: 204, State: "variant-5", Tags: []string{sim.TagRotatable, "notice-board"}},
			},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"notice-A": {ID: "notice-A", AssetID: "notice-board", CurrentState: "variant-1", Pos: sim.WorldPos{X: 10, Y: 10}},
		"notice-B": {ID: "notice-B", AssetID: "notice-board", CurrentState: "variant-2", Pos: sim.WorldPos{X: 20, Y: 20}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	defer cancel()

	// Drive determineRotationFlips through a Command. Use multiple RNG seeds
	// to assert the convergence property holds regardless of which
	// non-current state the picker happens to land on.
	for _, seed := range []int64{1, 7, 42, 100, 999} {
		r := rand.New(rand.NewSource(seed))
		res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			return sim.DetermineRotationFlipsForTest(world, sim.RotationScope{}, r), nil
		}})
		if err != nil {
			t.Fatalf("seed=%d: send: %v", seed, err)
		}
		flips := res.([]sim.PendingFlip)
		if len(flips) != 2 {
			t.Errorf("seed=%d: flip count = %d, want 2 (both notice boards must flip)",
				seed, len(flips))
			continue
		}
		// Both flips must target the SAME state — convergence.
		if flips[0].NewState != flips[1].NewState {
			t.Errorf("seed=%d: targets diverged: %q vs %q",
				seed, flips[0].NewState, flips[1].NewState)
		}
		// Neither target may match either instance's current state.
		target := flips[0].NewState
		if target == "variant-1" || target == "variant-2" {
			t.Errorf("seed=%d: target = %q must not equal either current state",
				seed, target)
		}
	}
}

// TestMostRecentRotationBoundary covers the same-day-before / same-day-
// after / midnight-boundary cases.
func TestMostRecentRotationBoundary(t *testing.T) {
	loc := time.FixedZone("test", 0)
	cases := []struct {
		name string
		now  time.Time
		h, m int
		want time.Time
	}{
		{
			name: "now before today's boundary returns yesterday's",
			now:  time.Date(2026, 5, 18, 6, 30, 0, 0, loc),
			h:    8, m: 0,
			want: time.Date(2026, 5, 17, 8, 0, 0, 0, loc),
		},
		{
			name: "now exactly at today's boundary returns today's",
			now:  time.Date(2026, 5, 18, 8, 0, 0, 0, loc),
			h:    8, m: 0,
			want: time.Date(2026, 5, 18, 8, 0, 0, 0, loc),
		},
		{
			name: "now after today's boundary returns today's",
			now:  time.Date(2026, 5, 18, 14, 30, 0, 0, loc),
			h:    8, m: 0,
			want: time.Date(2026, 5, 18, 8, 0, 0, 0, loc),
		},
		{
			name: "midnight boundary, now before it returns yesterday",
			now:  time.Date(2026, 5, 18, 0, 0, 0, 0, loc).Add(-time.Second),
			h:    0, m: 0,
			want: time.Date(2026, 5, 17, 0, 0, 0, 0, loc),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sim.MostRecentRotationBoundary(c.now, c.h, c.m)
			if !got.Equal(c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
