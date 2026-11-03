package sim_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// lantern_test.go — LLM-846: Light and Put out on the horn lantern, the candle
// it burns, and the lantern going out when it leaves the pack.

const lanternPC sim.ActorID = "pc-lantern"

var lanternCatalog = map[sim.ItemKind]*sim.ItemKindDef{
	sim.HornLanternKind:  {Name: sim.HornLanternKind, Category: "gear", Capabilities: []string{"portable", sim.CapabilityLight}},
	sim.TallowCandleKind: {Name: sim.TallowCandleKind, Category: "gear", Capabilities: []string{"portable"}},
}

// lanternRig is a running world with one player and every LanternChanged it
// emits.
type lanternRig struct {
	w      *sim.World
	events []sim.LanternChanged
}

// newLanternRig seeds a player holding inv, with stub left over from a candle
// put out earlier. candleMinutes > 0 sets the live burn-time setting.
func newLanternRig(t *testing.T, inv map[sim.ItemKind]int, stub time.Duration, candleMinutes int) *lanternRig {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(lanternCatalog)
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		lanternPC: {
			ID: lanternPC, DisplayName: "Jeff", Kind: sim.KindPC, LoginUsername: "jeff",
			Pos: sim.Position{X: 10, Y: 10}, Facing: "south", State: sim.StateIdle,
			Inventory:         inv,
			LanternCandleLeft: stub,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if candleMinutes > 0 {
		w.Settings.LanternCandleMinutes = candleMinutes
	}
	rig := &lanternRig{w: w}
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		if e, ok := evt.(*sim.LanternChanged); ok {
			rig.events = append(rig.events, *e)
		}
	}))
	runWorld(t, w)
	return rig
}

func (r *lanternRig) use(t *testing.T, kind sim.ItemKind, action string, now time.Time) error {
	t.Helper()
	_, err := r.w.Send(sim.UseItem(lanternPC, kind, action, now))
	return err
}

// lanternView is the player's lantern state and the events so far, read on the
// world goroutine.
type lanternView struct {
	LitUntil   time.Time
	CandleLeft time.Duration
	Candles    int
	Events     []sim.LanternChanged
}

func (r *lanternRig) view(t *testing.T) lanternView {
	t.Helper()
	return runOn(t, r.w, func(world *sim.World) (any, error) {
		a := world.Actors[lanternPC]
		return lanternView{
			LitUntil:   a.LanternLitUntil,
			CandleLeft: a.LanternCandleLeft,
			Candles:    a.Inventory[sim.TallowCandleKind],
			Events:     append([]sim.LanternChanged(nil), r.events...),
		}, nil
	}).(lanternView)
}

var lanternNow = time.Date(2026, 11, 3, 21, 0, 0, 0, time.UTC)

// Light takes one candle and burns for the live setting's minutes.
func TestLanternLightTakesACandle(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 2}, 0, 60)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	v := rig.view(t)
	want := lanternNow.Add(60 * time.Minute)
	if !v.LitUntil.Equal(want) || v.Candles != 1 || v.CandleLeft != 0 {
		t.Errorf("after light: lit until %v, candles %d, stub %v; want %v, 1, 0", v.LitUntil, v.Candles, v.CandleLeft, want)
	}
	if len(v.Events) != 1 || v.Events[0].ActorID != lanternPC || !v.Events[0].LitUntil.Equal(want) {
		t.Errorf("events = %+v, want one LanternChanged lit until %v", v.Events, want)
	}
}

// A burn-time setting past the cap (one that would overflow the duration)
// burns the default: the candle always lights the lantern into the future.
func TestLanternOversizedSettingBurnsTheDefault(t *testing.T) {
	for _, minutes := range []int{sim.MaxLanternCandleMinutes + 1, 200000000} {
		rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 1}, 0, minutes)
		if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
			t.Fatalf("%d minutes: light: %v", minutes, err)
		}
		v := rig.view(t)
		if want := lanternNow.Add(sim.DefaultLanternCandleMinutes * time.Minute); !v.LitUntil.Equal(want) || v.Candles != 0 {
			t.Errorf("%d minutes: lit until %v, candles %d; want %v, 0", minutes, v.LitUntil, v.Candles, want)
		}
	}
}

// The last candle leaves the inventory map, not a zero entry.
func TestLanternLightLastCandle(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 1}, 0, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	held := runOn(t, rig.w, func(world *sim.World) (any, error) {
		_, ok := world.Actors[lanternPC].Inventory[sim.TallowCandleKind]
		return ok, nil
	}).(bool)
	if held {
		t.Error("tallow_candle still in the inventory map after the last one was burned")
	}
	if v := rig.view(t); !v.LitUntil.Equal(lanternNow.Add(sim.DefaultLanternCandleMinutes * time.Minute)) {
		t.Errorf("lit until %v, want the default %d minutes", v.LitUntil, sim.DefaultLanternCandleMinutes)
	}
}

// A stub kept by Put out is burned first, and no candle is taken.
func TestLanternLightUsesStubFirst(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 1}, 40*time.Minute, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	v := rig.view(t)
	if !v.LitUntil.Equal(lanternNow.Add(40*time.Minute)) || v.Candles != 1 || v.CandleLeft != 0 {
		t.Errorf("after light: lit until %v, candles %d, stub %v; want +40m, 1, 0", v.LitUntil, v.Candles, v.CandleLeft)
	}
}

// With a stub and no candle the lantern still lights.
func TestLanternLightStubWithoutCandle(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1}, 10*time.Minute, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	if v := rig.view(t); !v.LitUntil.Equal(lanternNow.Add(10 * time.Minute)) {
		t.Errorf("lit until %v, want +10m", v.LitUntil)
	}
}

// No candle and no stub, already lit, or no lantern: refused, nothing changes.
func TestLanternLightRefused(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1}, 0, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("no fuel: err = %v, want ErrItemAction", err)
	}
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionPutOut, lanternNow); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("put out while out: err = %v, want ErrItemAction", err)
	}

	rig = newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 2}, 0, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow.Add(time.Minute)); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("already lit: err = %v, want ErrItemAction", err)
	}
	if v := rig.view(t); v.Candles != 1 || len(v.Events) != 1 {
		t.Errorf("after refused relight: candles %d, events %d; want 1, 1", v.Candles, len(v.Events))
	}

	rig = newLanternRig(t, map[sim.ItemKind]int{sim.TallowCandleKind: 2}, 0, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("no lantern: err = %v, want ErrItemAction", err)
	}
	if err := rig.use(t, sim.TallowCandleKind, sim.ItemActionLight, lanternNow); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("light a candle: err = %v, want ErrItemAction", err)
	}
}

// Put out keeps the rest of the candle; the next Light burns exactly that.
func TestLanternPutOutKeepsStub(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 1}, 0, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	out := lanternNow.Add(30 * time.Minute)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionPutOut, out); err != nil {
		t.Fatalf("put out: %v", err)
	}
	v := rig.view(t)
	if !v.LitUntil.IsZero() || v.CandleLeft != 150*time.Minute {
		t.Errorf("after put out: lit until %v, stub %v; want zero, 150m", v.LitUntil, v.CandleLeft)
	}
	if len(v.Events) != 2 || !v.Events[1].LitUntil.IsZero() {
		t.Errorf("events = %+v, want lit then out", v.Events)
	}

	relit := out.Add(time.Hour)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, relit); err != nil {
		t.Fatalf("relight: %v", err)
	}
	if v := rig.view(t); !v.LitUntil.Equal(relit.Add(150*time.Minute)) || v.CandleLeft != 0 || v.Candles != 0 {
		t.Errorf("after relight: lit until %v, stub %v, candles %d; want +150m, 0, 0", v.LitUntil, v.CandleLeft, v.Candles)
	}
}

// A candle that burns down leaves no stub, and the lantern reads out without a
// sweep.
func TestLanternBurnsOut(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 1}, 0, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	after := lanternNow.Add((sim.DefaultLanternCandleMinutes + 1) * time.Minute)
	lit := runOn(t, rig.w, func(world *sim.World) (any, error) {
		return sim.LanternLit(world.Actors[lanternPC], after), nil
	}).(bool)
	if lit {
		t.Error("lantern still lit after its candle burned down")
	}
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionPutOut, after); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("put out a burned-out lantern: err = %v, want ErrItemAction", err)
	}
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, after); !errors.Is(err, sim.ErrItemAction) {
		t.Errorf("relight with no candle and no stub: err = %v, want ErrItemAction", err)
	}
	if v := rig.view(t); v.CandleLeft != 0 {
		t.Errorf("stub after burn-out = %v, want 0", v.CandleLeft)
	}
}

// A lit lantern that leaves the pack goes out on the next familiar pass, and
// the rest of the candle is kept.
func TestLanternLeavingThePackPutsItOut(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 1}, 0, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	gone := lanternNow.Add(time.Hour)
	runOn(t, rig.w, func(world *sim.World) (any, error) {
		delete(world.Actors[lanternPC].Inventory, sim.HornLanternKind)
		return nil, nil
	})
	if _, err := rig.w.Send(sim.EvaluateFamiliarTick(gone)); err != nil {
		t.Fatalf("familiar tick: %v", err)
	}
	v := rig.view(t)
	if !v.LitUntil.IsZero() || v.CandleLeft != 2*time.Hour {
		t.Errorf("after the lantern left: lit until %v, stub %v; want zero, 2h", v.LitUntil, v.CandleLeft)
	}
	if len(v.Events) != 2 || !v.Events[1].LitUntil.IsZero() {
		t.Errorf("events = %+v, want lit then out", v.Events)
	}

	// A held lantern stays lit through the pass.
	rig = newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 1}, 0, 0)
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	if _, err := rig.w.Send(sim.EvaluateFamiliarTick(gone)); err != nil {
		t.Fatalf("familiar tick: %v", err)
	}
	if v := rig.view(t); v.LitUntil.IsZero() || len(v.Events) != 1 {
		t.Errorf("held lantern: lit until %v, events %d; want still lit, 1", v.LitUntil, len(v.Events))
	}
}

// The pack offers Light only with fuel, Put out only while lit, and nothing on
// the candle row.
func TestLanternItemActions(t *testing.T) {
	lantern := lanternCatalog[sim.HornLanternKind]
	candle := lanternCatalog[sim.TallowCandleKind]
	withCandle := map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 1}
	cases := []struct {
		name string
		def  *sim.ItemKindDef
		st   sim.PackState
		want []string
	}{
		{"no fuel", lantern, sim.PackState{Inventory: map[sim.ItemKind]int{sim.HornLanternKind: 1}}, []string{}},
		{"a candle", lantern, sim.PackState{Inventory: withCandle}, []string{sim.ItemActionLight}},
		{"a stub", lantern, sim.PackState{LanternCandleLeft: time.Minute}, []string{sim.ItemActionLight}},
		{"lit", lantern, sim.PackState{Inventory: withCandle, LanternLitUntil: lanternNow.Add(time.Minute)}, []string{sim.ItemActionPutOut}},
		{"burned out", lantern, sim.PackState{Inventory: withCandle, LanternLitUntil: lanternNow}, []string{sim.ItemActionLight}},
		{"candle row", candle, sim.PackState{Inventory: withCandle}, []string{}},
		{"another light good", &sim.ItemKindDef{Name: "rushlight", Capabilities: []string{sim.CapabilityLight}}, sim.PackState{Inventory: withCandle}, []string{}},
	}
	for _, c := range cases {
		if got := sim.ItemActions(c.def, c.st, lanternNow); !slices.Equal(got, c.want) {
			t.Errorf("%s: ItemActions = %v, want %v", c.name, got, c.want)
		}
	}
}
