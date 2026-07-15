package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// summon_steer_test.go — LLM-414. A live summons must be the single
// actionable movement voice: buildDutySteer (both arms), the evening leisure
// invitation, and the walk-away errand cues all yield while PendingSummon
// stands, and the golden matrix carries the cross-scenario invariant. The
// live 2026-07-14 incident: the target was summoned just past dusk, the
// go-home steer argued for home, and home won — the meeting never happened.

// summonedSnap adds a fresh (non-aged) PendingSummon to an actor snapshot.
func stampSummons(a *sim.ActorSnapshot, at time.Time) {
	a.PendingSummon = &sim.PendingSummon{
		SummonerName:     "John Ellis",
		Place:            "the Tavern",
		PlaceStructureID: "tavern",
		At:               at,
	}
}

// TestBuildDutySteer_SummonsSuppressesBothArms: with a live summons, the duty
// steer yields entirely — the to-work yank, the at-post stabilizer, and the
// go-home wind-down all fall silent so the summons cue is the one voice. An
// AGED-OUT summons restores the steer (the TTL is the decline path's bound).
func TestBuildDutySteer_SummonsSuppressesBothArms(t *testing.T) {
	now := time.Date(2026, 7, 14, 23, 20, 0, 0, time.UTC)
	freshSnap := func(nowMin int) *sim.Snapshot {
		s := dutySnap(nowMin, 420, 1140)
		s.PublishedAt = now
		return s
	}
	agent := func(inside sim.StructureID) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{
			Kind:              sim.KindNPCStateful,
			ScheduleStartMin:  dutyMinPtr(420),  // 07:00
			ScheduleEndMin:    dutyMinPtr(1140), // 19:00
			InsideStructureID: inside,
		}
	}

	t.Run("on shift, away from work: summons silences the to-work yank", func(t *testing.T) {
		a := agent("general_store")
		if v := buildDutySteer(freshSnap(600), "", a, dutyAnchors, false, false, false); v == nil {
			t.Fatal("control: steer must render without a summons")
		}
		stampSummons(a, now)
		if v := buildDutySteer(freshSnap(600), "", a, dutyAnchors, false, false, false); v != nil {
			t.Fatalf("summoned: to-work steer must yield, got %+v", v)
		}
	})
	t.Run("on shift, at post: summons silences the at-post stabilizer", func(t *testing.T) {
		a := agent("tavern")
		stampSummons(a, now)
		if v := buildDutySteer(freshSnap(600), "", a, dutyAnchors, false, false, false); v != nil {
			t.Fatalf("summoned: at-post stabilizer must yield, got %+v", v)
		}
	})
	t.Run("off shift, away from home: summons silences the go-home steer (the incident)", func(t *testing.T) {
		a := agent("general_store")
		if v := buildDutySteer(freshSnap(1170), "", a, dutyAnchors, false, false, false); v == nil || v.ToWork {
			// 19:30 — past shift end; without evening-leisure fixtures the
			// go-home arm renders (this bare fixture has no night-place venue).
			t.Fatalf("control: go-home steer must render without a summons, got %+v", v)
		}
		stampSummons(a, now)
		if v := buildDutySteer(freshSnap(1170), "", a, dutyAnchors, false, false, false); v != nil {
			t.Fatalf("summoned: go-home steer must yield, got %+v", v)
		}
	})
	t.Run("aged-out summons restores the steer", func(t *testing.T) {
		a := agent("general_store")
		a.PendingSummon = &sim.PendingSummon{
			SummonerName: "John Ellis", Place: "the Tavern",
			At: now.Add(-summonCueRenderTTL - time.Minute),
		}
		if v := buildDutySteer(freshSnap(600), "", a, dutyAnchors, false, false, false); v == nil {
			t.Fatal("aged-out summons must not suppress the steer forever")
		}
	})
}

// TestBuildEveningLeisure_SummonsSuppressesInvitation: the tavern invitation
// yields to a live summons — the second competing voice of the incident.
func TestBuildEveningLeisure_SummonsSuppressesInvitation(t *testing.T) {
	snap, actorID, _ := homedWorkerEveningTavernOpen()
	if v := Build(snap, actorID, nil).EveningLeisure; !v.Invitation() {
		t.Fatal("control: the evening invitation must render without a summons")
	}
	stampSummons(snap.Actors[actorID], snap.PublishedAt)
	if v := Build(snap, actorID, nil).EveningLeisure; v != nil {
		t.Fatalf("summoned: the evening invitation must yield, got %+v", v)
	}
}

// TestBuildSuppressesErrandCuesWhileSummoned: the walk-away errand cues (farm
// upkeep, restock, repair-buy, forage) yield to a live summons at the Build
// level — each names a different destination and renders under the triage
// coda's obligations-first ranking, so a summoned keeper would lose the
// meeting to a shovel. Control: the same actor outdoors on the evening WITHOUT
// a summons keeps its upkeep errand (the LLM-345 leisure gate doesn't apply
// outdoors).
func TestBuildSuppressesErrandCuesWhileSummoned(t *testing.T) {
	snap, actorID, warrants := farmOwnerSettledInTavernEvening()
	snap.Actors[actorID].InsideStructureID = "" // outdoors: leisure venue gate off
	if v := Build(snap, actorID, warrants).FarmUpkeep; v == nil {
		t.Fatal("control: outdoors on the evening the upkeep errand must render")
	}
	stampSummons(snap.Actors[actorID], snap.PublishedAt)
	p := Build(snap, actorID, warrants)
	if p.FarmUpkeep != nil {
		t.Errorf("farm-upkeep errand must yield to a live summons, got %+v", p.FarmUpkeep)
	}
	if p.Restocking != nil {
		t.Errorf("restock errand must yield to a live summons, got %+v", p.Restocking)
	}
	if p.StallRepairBuy != nil {
		t.Errorf("repair-buy errand must yield to a live summons, got %+v", p.StallRepairBuy)
	}
	if p.Forage != nil {
		t.Errorf("forage errand must yield to a live summons, got %+v", p.Forage)
	}
	if p.SummonsForYou == nil {
		t.Fatal("the summons section itself must render in the errands' place")
	}
}

// TestSummonsReplacesMovementSteers is the cross-scenario invariant (the
// GUIDELINES growth-loop): wherever the golden matrix shows '## You have been
// summoned', NO competing movement steer may render — not the go-home
// wind-down, not the at-post stay-put line, not the evening invitation. The
// summons is the single actionable movement voice while it stands.
func TestSummonsReplacesMovementSteers(t *testing.T) {
	seen := false
	for _, sc := range perceptionScenarios {
		out := renderScenario(sc)
		if !strings.Contains(out, "## You have been summoned") {
			continue
		}
		seen = true
		for _, competing := range []string{
			"Your working hours are over",             // go-home wind-down
			"stay and look after your work",           // at-post stabilizer
			"the tavern is open of an evening",        // evening invitation
			"It is your working hours, yet you are a", // to-work yank
		} {
			if strings.Contains(out, competing) {
				t.Errorf("scenario %q shows the summons AND a competing steer (%q); the summons must be the single movement voice", sc.name, competing)
			}
		}
	}
	if !seen {
		t.Fatal("no golden scenario carries the summons section — the invariant is vacuous (add one back)")
	}
}
