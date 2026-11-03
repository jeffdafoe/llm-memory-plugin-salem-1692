package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// omen_pc_test.go — LLM-845: a player's omens happen at the player's own
// moments through the real command paths — lighting the camp fire, lighting the
// lantern, eating at a bush, drinking at the well — and the player gets the
// scene as a popup and a talk-box line.

// armPCOmen sets an omen of kind against a player, live for a day, with player
// omens switched on.
func armPCOmen(t *testing.T, w *sim.World, kind sim.OmenKind, subject sim.ActorID) {
	t.Helper()
	mustSend(t, w, func(world *sim.World) {
		world.Settings.OmenPCChancePermille = 1000
		now := time.Now().UTC()
		world.Environment.Omens.Armed = append(world.Environment.Omens.Armed, sim.ArmedOmen{
			Kind: kind, SubjectID: subject, ArmedAt: now.Add(-time.Minute), LapsesAt: now.Add(24 * time.Hour),
		})
	})
}

// pcOmenSeen captures the player's talk-box lines and OmenNoticed from here on.
type pcOmenSeen struct {
	lines  []*sim.PCOmenNarrated
	notice []*sim.OmenNoticed
}

func recordPCOmens(t *testing.T, w *sim.World) *pcOmenSeen {
	t.Helper()
	rec := &pcOmenSeen{}
	mustSend(t, w, func(world *sim.World) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) {
			switch ev := e.(type) {
			case *sim.PCOmenNarrated:
				rec.lines = append(rec.lines, ev)
			case *sim.OmenNoticed:
				rec.notice = append(rec.notice, ev)
			}
		}))
	})
	return rec
}

// assertPCOmenSeen checks what the player gets: one talk-box line, one popup
// with the same text, one OmenNoticed about themselves, and the armed omen
// spent.
func assertPCOmenSeen(t *testing.T, w *sim.World, rec *pcOmenSeen, kind sim.OmenKind, pc sim.ActorID) {
	t.Helper()
	if len(rec.notice) != 1 || rec.notice[0].Kind != kind || rec.notice[0].WitnessID != pc || rec.notice[0].SubjectID != pc {
		t.Fatalf("OmenNoticed = %+v, want one %q about %s", rec.notice, kind, pc)
	}
	if len(rec.lines) != 1 || rec.lines[0].ActorID != pc || rec.lines[0].Kind != kind || rec.lines[0].Text == "" {
		t.Fatalf("talk-box lines = %+v, want one for %s", rec.lines, pc)
	}
	record, _ := omenState(t, w, pc)
	if len(record.Armed) != 0 {
		t.Errorf("armed omens left = %+v, want none", record.Armed)
	}
	popups := record.Popups[pc]
	if len(popups) != 1 || popups[0].Kind != kind || popups[0].Text != rec.lines[0].Text {
		t.Errorf("popups = %+v, want one %q carrying the talk-box line", popups, kind)
	}
}

func TestPCOmen_CampFireGutters(t *testing.T) {
	w := buildCampWorld(t, nil, nil)
	now := pitchAll(t, w)
	mustSend(t, w, func(world *sim.World) {
		world.Actors["pc"].Pos = sim.TilePos{X: campStand.X + 2, Y: campStand.Y + 2}
	})
	rec := recordPCOmens(t, w)
	armPCOmen(t, w, sim.OmenCampFireGutters, "pc")

	res, err := w.Send(sim.LightCampFire("pc", now))
	if err != nil {
		t.Fatalf("LightCampFire: %v", err)
	}
	lit := res.(sim.LightCampFireResult)
	if got := lit.OutAt.Sub(now); got != 15*time.Minute {
		t.Errorf("out after %v, want 15m (the fire sinks)", got)
	}
	if !lit.BurnsUntil.Equal(now) {
		t.Errorf("burns until %v, want now: it dies down from the start", lit.BurnsUntil)
	}
	if s := readCamp(t, w); s.Fire != sim.CampFireStateDying {
		t.Errorf("fire = %q, want dying", s.Fire)
	}
	assertPCOmenSeen(t, w, rec, sim.OmenCampFireGutters, "pc")

	// Out on the clock, it lights again as usual.
	if _, err := w.Send(sim.LightCampFire("pc", now.Add(20*time.Minute))); err != nil {
		t.Fatalf("relight: %v", err)
	}
	if s := readCamp(t, w); s.Fire != sim.CampFireStateBurning {
		t.Errorf("relit fire = %q, want burning", s.Fire)
	}
}

func TestPCOmen_LanternGutters(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 2}, 0, 0)
	rec := recordPCOmens(t, rig.w)
	armPCOmen(t, rig.w, sim.OmenLanternGutters, lanternPC)

	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	v := rig.view(t)
	if want := lanternNow.Add(5 * time.Minute); !v.LitUntil.Equal(want) {
		t.Errorf("lit until %v, want %v (the candle gutters)", v.LitUntil, want)
	}
	if v.Candles != 1 {
		t.Errorf("candles = %d, want 1: the candle is spent all the same", v.Candles)
	}
	assertPCOmenSeen(t, rig.w, rec, sim.OmenLanternGutters, lanternPC)
}

func TestPCOmen_WitheredBushWhenEating(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC)
	mustSend(t, w, func(world *sim.World) {
		world.Actors["hannah"].LoginUsername = "hannah"
		world.Actors["hannah"].Needs = map[sim.NeedKey]int{"hunger": 20}
	})
	placeAt(t, w, "hannah", "bush")
	rec := recordPCOmens(t, w)
	armPCOmen(t, w, sim.OmenWitheredBush, "hannah")

	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	forceComplete(t, w)
	if got := availOf(t, w, "bush"); got != 0 {
		t.Errorf("bush supply = %d, want 0 (stripped, to regrow)", got)
	}
	hunger := runOn(t, w, func(world *sim.World) (any, error) {
		return world.Actors["hannah"].Needs["hunger"], nil
	}).(int)
	if hunger != 20 {
		t.Errorf("hunger = %d, want 20: nothing was eaten", hunger)
	}
	busy := runOn(t, w, func(world *sim.World) (any, error) {
		return world.Actors["hannah"].SourceActivity != nil, nil
	}).(bool)
	if busy {
		t.Error("the bite was re-armed at a withered bush")
	}
	assertPCOmenSeen(t, w, rec, sim.OmenWitheredBush, "hannah")
}

func TestPCOmen_FoulWellWhenDrinking(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setActorKind(t, w, "hannah", sim.KindPC)
	mustSend(t, w, func(world *sim.World) {
		world.Actors["hannah"].LoginUsername = "hannah"
		world.Actors["hannah"].Needs = map[sim.NeedKey]int{"thirst": 20}
		world.VillageObjects["well"].Tags = append(world.VillageObjects["well"].Tags, sim.TagWell)
	})
	placeAt(t, w, "hannah", "well")
	rec := recordPCOmens(t, w)
	armPCOmen(t, w, sim.OmenFoulWell, "hannah")

	if _, err := w.Send(sim.StartRefreshAtArrival("hannah")); err != nil {
		t.Fatalf("StartRefreshAtArrival: %v", err)
	}
	forceComplete(t, w)
	thirst := runOn(t, w, func(world *sim.World) (any, error) {
		return world.Actors["hannah"].Needs["thirst"], nil
	}).(int)
	if thirst >= 20 {
		t.Errorf("thirst = %d, want eased: the foul well takes nothing away", thirst)
	}
	assertPCOmenSeen(t, w, rec, sim.OmenFoulWell, "hannah")
}

// An omen armed against a player does not happen while player omens are
// switched off, whatever the villagers' setting; it lapses.
func TestPCOmen_SwitchedOff(t *testing.T) {
	rig := newLanternRig(t, map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 1}, 0, 0)
	rec := recordPCOmens(t, rig.w)
	armPCOmen(t, rig.w, sim.OmenLanternGutters, lanternPC)
	mustSend(t, rig.w, func(world *sim.World) {
		world.Settings.OmenPCChancePermille = 0
		world.Settings.OmenChancePermille = 1000
	})
	if err := rig.use(t, sim.HornLanternKind, sim.ItemActionLight, lanternNow); err != nil {
		t.Fatalf("light: %v", err)
	}
	if v := rig.view(t); !v.LitUntil.Equal(lanternNow.Add(sim.DefaultLanternCandleMinutes * time.Minute)) {
		t.Errorf("lit until %v, want a full candle", v.LitUntil)
	}
	if len(rec.notice) != 0 || len(rec.lines) != 0 {
		t.Errorf("an omen happened while player omens are off: %+v %+v", rec.notice, rec.lines)
	}
}
