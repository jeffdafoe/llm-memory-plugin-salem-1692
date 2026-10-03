package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// court_golden_test.go — golden scenarios + a cross-scenario invariant for
// LLM-695, the magistrates: the constable's "## The magistrates" section with a
// matter before the court, the same section at the day's limit (no tool line),
// and a party's ruling beat.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "constable_with_a_matter_before_the_court",
			summary: "LLM-695: Gideon has brought one matter today — the ledger case — and it is heard today at " +
				"noon. '## The magistrates' says how the court works, that he may bring up to 2 matters a day " +
				"with bring_before_magistrates, and lists the matter before the court with its parties.",
			build: constableWithMatterBeforeCourtScenario,
		},
		perceptionScenario{
			name: "constable_at_the_daily_limit",
			summary: "LLM-695: Gideon has brought two matters today; the noon sitting has passed and the first has " +
				"no ruling yet. The section says the court hears no more from him today (the tool is withdrawn) " +
				"and that the court has sat with its word not yet come.",
			build: constableAtDailyLimitScenario,
		},
		perceptionScenario{
			name: "party_hears_the_magistrates_ruling",
			summary: "LLM-695: Joseph is a party to a matter the magistrates ruled on. His Since-your-last-turn " +
				"carries the ruling: which matter, the magistrate's words as given, and 'The matter is closed.' " +
				"No section — he is not the constable.",
			build: partyHearsRulingScenario,
		},
	)
}

func courtSnapshot() *sim.Snapshot {
	snap := publicWorksSnapshot(0)
	snap.VillageObjects["mill_well"].DamagedAt = time.Time{}
	snap.CourtDailyCaseLimit = 2
	snap.CourtSittingTime = "12:00"
	return snap
}

var ledgerCase = &sim.CourtCase{
	ID:          "case-0000ab01",
	FiledAt:     time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC),
	FiledByID:   pwGideon,
	FiledByName: "Constable Gideon Marsh",
	Parties: []sim.CourtParty{
		{ActorID: pwJoseph, Name: "Joseph Scott"},
		{ActorID: pwGideon, Name: "Constable Gideon Marsh"},
	},
	Complaint: "Joseph Scott's account book was taken from the Mill some days past; no one will say who took it.",
	Status:    sim.CourtCaseStatusPending,
}

func constableWithMatterBeforeCourtScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := courtSnapshot()
	snap.CourtFiledToday = map[sim.ActorID]int{pwGideon: 1}
	snap.CourtDocket = []sim.CourtDocketEntry{{Case: ledgerCase.Clone(), Hearing: sim.CourtHearingToday}}
	return snap, pwGideon, nil
}

func constableAtDailyLimitScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := courtSnapshot()
	snap.CourtFiledToday = map[sim.ActorID]int{pwGideon: 2}
	snap.CourtDocket = []sim.CourtDocketEntry{{Case: ledgerCase.Clone(), Hearing: sim.CourtHearingAwaiting}}
	return snap, pwGideon, nil
}

func partyHearsRulingScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := courtSnapshot()
	ruled := ledgerCase.Clone()
	ruled.Status = sim.CourtCaseStatusRuled
	ruled.Result = sim.CourtResultNoCase
	ruled.Words = "No account book was ever kept at the Mill, and none was taken; no coin or goods passed that week that the record does not show. There is no case."
	warrant := sim.WarrantMeta{
		TriggerActorID: pwGideon,
		Reason: sim.CourtRuledWarrantReason{
			CaseID:        ruled.ID,
			FiledBy:       pwGideon,
			NarrationText: sim.CourtRulingNarration(ruled, pwJoseph),
		},
	}
	return snap, pwJoseph, []sim.WarrantMeta{warrant}
}

// TestGoldensOnlyAConstableHearsTheMagistrates — across the whole matrix,
// "## The magistrates" renders only for a constable, and the tool line ("with
// bring_before_magistrates") only when the section offers filing. Vacuity-guarded.
func TestGoldensOnlyAConstableHearsTheMagistrates(t *testing.T) {
	sawSection, sawToolLine, sawLimit := false, false, false
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			a := snap.Actors[actorID]
			if a == nil {
				return
			}
			p := Build(snap, actorID, warrants)
			out := combinedPrompt(Render(p, DefaultRenderConfig()))
			has := strings.Contains(out, "## The magistrates\n")
			if has {
				sawSection = true
				if !isConstableSnapshot(a) {
					t.Fatalf("%s is not a constable but hears ## The magistrates", a.DisplayName)
				}
			}
			toolLine := strings.Contains(out, "with bring_before_magistrates")
			if toolLine {
				sawToolLine = true
			}
			if toolLine != p.Court.OffersFiling() {
				t.Fatalf("tool line rendered=%v but OffersFiling=%v — cue and gate drifted", toolLine, p.Court.OffersFiling())
			}
			if strings.Contains(out, "the court hears no more from you until tomorrow") {
				sawLimit = true
			}
		})
	}
	if !sawSection || !sawToolLine || !sawLimit {
		t.Fatalf("vacuous: section=%v toolLine=%v limit=%v", sawSection, sawToolLine, sawLimit)
	}
}

func init() {
	perceptionScenarios = append(perceptionScenarios, perceptionScenario{
		name: "party_wakes_to_a_closed_matter",
		summary: "LLM-695: Joseph slept through the ruling, so its beat is gone. The matter still stands in his " +
			"prompt under '## The magistrates' word': on 3 October the magistrates ruled on the matter Constable " +
			"Gideon Marsh brought, their words, and 'The matter is closed.'",
		build: partyWakesToAClosedMatterScenario,
	})
}

func partyWakesToAClosedMatterScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := courtSnapshot()
	ruled := ledgerCase.Clone()
	ruled.Status = sim.CourtCaseStatusRuled
	ruled.Result = sim.CourtResultNoCase
	ruled.RuledAt = time.Date(2026, 10, 3, 12, 14, 0, 0, time.UTC)
	ruled.Words = "No account book was ever kept at the Mill, and none was taken. There is no case."
	snap.CourtRecentRulings = []sim.CourtRecentRuling{{Case: ruled, Day: "3 October"}}
	return snap, pwJoseph, nil
}

// TestGoldensOnlyThoseAMatterConcernsHearTheMagistratesWord — across the
// matrix, "## The magistrates' word" renders only for a party or the filer of
// one of the snapshot's recent rulings. Vacuity-guarded.
func TestGoldensOnlyThoseAMatterConcernsHearTheMagistratesWord(t *testing.T) {
	saw := false
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			out := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
			if !strings.Contains(out, "## The magistrates' word") {
				return
			}
			saw = true
			for _, r := range snap.CourtRecentRulings {
				if r.Case != nil && r.Case.Involves(actorID) {
					return
				}
			}
			t.Fatalf("%s hears ## The magistrates' word but no recent ruling concerns them", actorID)
		})
	}
	if !saw {
		t.Fatal("vacuous: no scenario rendered ## The magistrates' word")
	}
}
