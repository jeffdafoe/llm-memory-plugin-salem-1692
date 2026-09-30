package sim_test

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// visitor_persona_test.go — LLM-686 end to end: a visitor name is one fixed man
// with one trade and one road, and one returner row.

// spawnedVisitor returns the one visitor in the published snapshot.
func spawnedVisitor(t *testing.T, w *sim.World) *sim.ActorSnapshot {
	t.Helper()
	for _, a := range w.Published().Actors {
		if a.VisitorState != nil {
			return a
		}
	}
	t.Fatal("no visitor in snapshot after spawn")
	return nil
}

// personaOf resolves a spawned visitor's display name back to its table persona.
func personaOf(t *testing.T, displayName string) sim.VisitorPersona {
	t.Helper()
	i := strings.LastIndex(displayName, " the ")
	if i < 0 {
		t.Fatalf("display name %q has no \" the \" suffix", displayName)
	}
	p, ok := sim.VisitorPersonaByName(displayName[:i])
	if !ok {
		t.Fatalf("spawned %q is not a table persona", displayName)
	}
	return p
}

// TestSpawn_FactorIsAFactorName: a sell errand brings a factor-class name from
// Boston, whatever the seed.
func TestSpawn_FactorIsAFactorName(t *testing.T) {
	for seed := int64(1); seed <= 8; seed++ {
		vw := newVisitorWorld()
		vw.seedTavern(t)
		vw.seedDistributor(t)
		w, cancel := vw.load(t)
		withWorld(t, w, func(world *sim.World) {
			world.Settings.VisitorMerchantTrickleChancePermille = 1000
			world.Settings.VisitorMaxConcurrent = 2
			world.Settings.VisitorSellWeightPermille = 1000
		})
		if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: visitorSpawnDaytime, Rand: rand.New(rand.NewSource(seed))})); err != nil {
			cancel()
			t.Fatalf("seed %d: TickVisitorCascade: %v", seed, err)
		}
		got := spawnedVisitor(t, w)
		p := personaOf(t, got.DisplayName)
		if p.Class != sim.VisitorClassFactor || got.VisitorState.Origin != p.Origin || got.VisitorState.Origin != sim.FactorOrigin {
			t.Errorf("seed %d: factor spawn = %q (class %s) from %q, want a factor name from %s",
				seed, got.DisplayName, p.Class, got.VisitorState.Origin, sim.FactorOrigin)
		}
		cancel()
	}
}

// TestSpawn_PasserCarriesHisCalling: a passer-through spawn is a passer name with
// the table's calling and hometown.
func TestSpawn_PasserCarriesHisCalling(t *testing.T) {
	for seed := int64(1); seed <= 8; seed++ {
		vw := newVisitorWorld()
		vw.seedTavern(t)
		w, cancel := vw.load(t)
		withWorld(t, w, func(world *sim.World) {
			world.Settings.VisitorPasserSpawnChancePermille = 1000
			world.Settings.VisitorMaxConcurrent = 2
		})
		if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: visitorSpawnDaytime, Rand: rand.New(rand.NewSource(seed))})); err != nil {
			cancel()
			t.Fatalf("seed %d: TickVisitorCascade: %v", seed, err)
		}
		got := spawnedVisitor(t, w)
		p := personaOf(t, got.DisplayName)
		if p.Class != sim.VisitorClassPasser || got.VisitorState.Archetype != p.Archetype || got.VisitorState.Origin != p.Origin {
			t.Errorf("seed %d: passer spawn = %q the %s from %q, want %s the %s from %s",
				seed, got.DisplayName, got.VisitorState.Archetype, got.VisitorState.Origin, p.Name, p.Archetype, p.Origin)
		}
		cancel()
	}
}

// TestSpawn_LinksTheNamesReturnerRow: a fresh spawn of a name that has a returner
// row is that man's visit — linked, visit counted, schedule cleared, disposition
// kept — and no second row appears.
func TestSpawn_LinksTheNamesReturnerRow(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	vw.seedDistributor(t)
	w, cancel := vw.load(t)
	defer cancel()

	// A row for every factor name, so whichever the seed picks has one.
	var rows int
	withWorld(t, w, func(world *sim.World) {
		world.RecurringVisitors = map[sim.RecurringVisitorID]*sim.RecurringVisitor{}
		for i, p := range sim.VisitorPersonas() {
			if p.Class != sim.VisitorClassFactor {
				continue
			}
			id := sim.RecurringVisitorID("rvis-0000000" + string(rune('a'+i)))
			world.RecurringVisitors[id] = &sim.RecurringVisitor{
				ID: id, Name: p.Name, Archetype: "provisioner", Origin: "Wenham", Disposition: "wry",
				VisitCount: 1, NextReturnAt: visitorSpawnDaytime.Add(30 * 24 * time.Hour),
				Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{},
			}
		}
		rows = len(world.RecurringVisitors)
		world.Settings.VisitorMerchantTrickleChancePermille = 1000
		world.Settings.VisitorMaxConcurrent = 2
		world.Settings.VisitorSellWeightPermille = 1000
	})
	if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: visitorSpawnDaytime, Rand: rand.New(rand.NewSource(5))})); err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	got := spawnedVisitor(t, w)
	if got.VisitorState.RecurringID == "" {
		t.Fatalf("spawned %q not linked to his returner row", got.DisplayName)
	}
	if got.VisitorState.Trade == nil {
		t.Fatalf("a linked merchant must still bind his errand; %q has none", got.DisplayName)
	}
	if got.VisitorState.Disposition != "wry" {
		t.Errorf("disposition = %q, want the row's %q", got.VisitorState.Disposition, "wry")
	}
	withWorld(t, w, func(world *sim.World) {
		rv := world.RecurringVisitors[sim.RecurringVisitorID(got.VisitorState.RecurringID)]
		if rv == nil {
			t.Fatalf("linked row %s missing", got.VisitorState.RecurringID)
		}
		if !strings.HasPrefix(got.DisplayName, rv.Name+" the ") {
			t.Errorf("linked row %s is %q, visitor is %q", rv.ID, rv.Name, got.DisplayName)
		}
		if rv.VisitCount != 2 || !rv.NextReturnAt.IsZero() {
			t.Errorf("row after arrival: visit_count %d next_return %v, want 2 and cleared", rv.VisitCount, rv.NextReturnAt)
		}
		if rv.Origin != sim.FactorOrigin {
			t.Errorf("row origin = %q, want aligned to %s", rv.Origin, sim.FactorOrigin)
		}
		if len(world.RecurringVisitors) != rows {
			t.Errorf("row count = %d, want %d (no new row)", len(world.RecurringVisitors), rows)
		}
	})
}

// TestReturner_PromotionReusesTheNamesRow: a visitor who meets a PC while his name
// already has a row (he spawned before the link existed) joins that row instead of
// minting a second one.
func TestReturner_PromotionReusesTheNamesRow(t *testing.T) {
	rid := sim.RecurringVisitorID("rvis-0000abcd")
	past := time.Now().UTC().Add(-20 * 24 * time.Hour)
	seed := map[sim.RecurringVisitorID]*sim.RecurringVisitor{
		rid: {
			ID: rid, Name: "Elias Drum", Archetype: "factor", Origin: "Boston", Disposition: "weary",
			VisitCount: 2, FirstSeenAt: past, LastSeenAt: past,
			Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{
				"pc-wendy": {PCActorID: "pc-wendy", PCDisplayName: "Wendy", FirstMetAt: past, LastMetAt: past},
			},
		},
	}
	w, stop := buildReturnerTestWorld(t, inWindowVisitor("vstr-0000aaaa", ""), seed)
	defer stop()

	emitInCommand(t, w, &sim.ActorMet{A: "vstr-0000aaaa", B: "pc-jeff", At: time.Now().UTC()})

	if got := w.Published().Actors["vstr-0000aaaa"].VisitorState.RecurringID; got != string(rid) {
		t.Fatalf("RecurringID = %q, want the existing row %s", got, rid)
	}
	withWorld(t, w, func(world *sim.World) {
		if n := len(world.RecurringVisitors); n != 1 {
			t.Errorf("row count = %d, want 1", n)
		}
		rv := world.RecurringVisitors[rid]
		if rv.Acquaintances["pc-jeff"] == nil || rv.Acquaintances["pc-wendy"] == nil {
			t.Errorf("acquaintances = %v, want both Wendy (kept) and Jeff (new)", rv.Acquaintances)
		}
	})
}

// TestReturner_MerchantDepartureNotScheduled: a merchant name leaves with his
// last-seen stamped and no return date — he comes back with his class.
func TestReturner_MerchantDepartureNotScheduled(t *testing.T) {
	rid := sim.RecurringVisitorID("rvis-0000abce")
	seed := map[sim.RecurringVisitorID]*sim.RecurringVisitor{
		rid: {
			ID: rid, Name: "Elias Drum", Archetype: "factor", Origin: "Boston", Disposition: "weary",
			VisitCount: 1, FirstSeenAt: time.Now().UTC().Add(-time.Hour), LastSeenAt: time.Now().UTC().Add(-time.Hour),
			Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{},
		},
	}
	w, stop := buildReturnerTestWorld(t, inWindowVisitor("vstr-0000aaaa", string(rid)), seed)
	defer stop()

	future := time.Now().UTC().Add(6 * time.Hour)
	if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: future, Rand: rand.New(rand.NewSource(1))})); err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	withWorld(t, w, func(world *sim.World) {
		rv := world.RecurringVisitors[rid]
		if !rv.NextReturnAt.IsZero() {
			t.Errorf("merchant NextReturnAt = %v, want unscheduled", rv.NextReturnAt)
		}
		if !rv.LastSeenAt.Equal(future) {
			t.Errorf("LastSeenAt = %v, want departure %v", rv.LastSeenAt, future)
		}
	})
}

// TestReturner_LoadAlignsRowsToTable: at boot a merchant-name row takes its
// table hometown and loses any return date; a passer row takes its calling; a
// name outside the table is left as stored.
func TestReturner_LoadAlignsRowsToTable(t *testing.T) {
	due := time.Now().UTC().Add(-24 * time.Hour)
	seed := map[sim.RecurringVisitorID]*sim.RecurringVisitor{
		"rvis-00000001": {ID: "rvis-00000001", Name: "Caleb Wendell", Archetype: "provisioner", Origin: "Wenham",
			Disposition: "wry", VisitCount: 1, NextReturnAt: due, Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{}},
		"rvis-00000002": {ID: "rvis-00000002", Name: "Ephraim Pollard", Archetype: "shovel-buyer", Origin: "Ipswich",
			Disposition: "warm", VisitCount: 1, NextReturnAt: due, Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{}},
		"rvis-00000003": {ID: "rvis-00000003", Name: "Obadiah Pratt", Archetype: "circuit preacher", Origin: "Lynn",
			Disposition: "wry", VisitCount: 1, NextReturnAt: due, Acquaintances: map[sim.ActorID]*sim.RecurringAcquaintance{}},
	}
	w, stop := buildReturnerTestWorld(t, nil, seed)
	defer stop()
	withWorld(t, w, func(world *sim.World) {
		caleb := world.RecurringVisitors["rvis-00000001"]
		if caleb.Origin != sim.FactorOrigin || !caleb.NextReturnAt.IsZero() {
			t.Errorf("Caleb row = from %q next %v, want from %s, unscheduled", caleb.Origin, caleb.NextReturnAt, sim.FactorOrigin)
		}
		ephraim := world.RecurringVisitors["rvis-00000002"]
		if ephraim.Archetype != "itinerant musician" || ephraim.Origin != "the coast road" || !ephraim.NextReturnAt.Equal(due) {
			t.Errorf("Ephraim row = the %s from %q next %v, want the itinerant musician from the coast road, still due",
				ephraim.Archetype, ephraim.Origin, ephraim.NextReturnAt)
		}
		other := world.RecurringVisitors["rvis-00000003"]
		if other.Archetype != "circuit preacher" || other.Origin != "Lynn" {
			t.Errorf("unknown-name row changed: the %s from %q", other.Archetype, other.Origin)
		}
	})
}
