package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// town_rate_render_test.go — LLM-572. Direct coverage for the widened collector arm
// of the town-rate cue, on the LLM-557 golden fixture (townRateSnapshot).
//
// buildTownRateCollector used to return nil whenever no co-present keeper was in
// arrears, so the constable's scene said nothing about the levy for most of his
// rounds and never once stated which way it runs. On 2026-07-30 at 19:03:43 UTC
// Gideon Marsh paid John Ellis a coin "for" a `Town rate — Tavern day's fee`.
// Nothing settled — settleTownRate requires the PAYEE be a constable — so a coin
// simply ran backwards.

// renderTownRateFor builds and renders the cue from one actor's side.
func renderTownRateFor(t *testing.T, snap *sim.Snapshot, subject sim.ActorID) (*TownRateView, string) {
	t.Helper()
	v := buildTownRate(snap, subject, snap.Actors[subject])
	var b strings.Builder
	renderTownRate(&b, v)
	return v, b.String()
}

// The collector cue renders with NOTHING owing, naming the settled business. This is
// the branch whose absence produced the live backwards payment.
func TestTownRateCollector_RendersWhenNothingIsOwed(t *testing.T) {
	snap, _, constableID := townRateSnapshot(0, true)
	v, got := renderTownRateFor(t, snap, constableID)
	if v == nil {
		t.Fatal("collector view is nil with a paid-up keeper co-present — the levy is invisible again")
	}
	if !v.Collector {
		t.Errorf("Collector = false on the constable's own view")
	}
	if !strings.Contains(got, "The town rate keeps you, and it falls to you to collect it.") {
		t.Errorf("direction of the levy is not stated:\n%s", got)
	}
	if !strings.Contains(got, "The rate on the General Store is paid up — nothing is owing you here.") {
		t.Errorf("paid-up arm does not name the business:\n%s", got)
	}
	// The keeper-side instruction must never reach a constable: it tells the reader
	// to hand coin to the constable, which is the backwards payment itself. Before
	// this ticket renderTownRate dispatched on len(Debtors), so a collector view
	// with no debtors would have fallen straight into the keeper branch.
	if strings.Contains(got, "Settle it with pay") {
		t.Errorf("constable is told to settle the rate he collects:\n%s", got)
	}
}

// With arrears the collector still leads with the direction and then names who is
// behind — unchanged from LLM-557 except that the sentence is now unconditional.
func TestTownRateCollector_NamesDebtors(t *testing.T) {
	snap, _, constableID := townRateSnapshot(2, true)
	_, got := renderTownRateFor(t, snap, constableID)
	if !strings.Contains(got, "The town rate keeps you, and it falls to you to collect it.") {
		t.Errorf("direction of the levy is not stated:\n%s", got)
	}
	if !strings.Contains(got, "Josiah Thorne has let the rate on the General Store run on — 2 coins behind.") {
		t.Errorf("debtor sentence missing:\n%s", got)
	}
	if strings.Contains(got, "paid up") {
		t.Errorf("paid-up clause rendered alongside arrears:\n%s", got)
	}
}

// A constable with no rateable keeper in front of him gets no section at all — the
// levy is simply not part of that scene. The widened gate must not become a cue that
// rides every tick of his rounds.
func TestTownRateCollector_SilentWithNoKeeperPresent(t *testing.T) {
	snap, _, constableID := townRateSnapshot(0, true)
	// Strip the ownership so the co-present keeper is no longer rateable.
	snap.VillageObjects["general_store"].OwnerActorID = ""
	if v := buildTownRate(snap, constableID, snap.Actors[constableID]); v != nil {
		t.Errorf("collector view = %+v, want nil with no rateable keeper present", v)
	}
}

// The keeper's side is untouched: it still names the constable and the exact coin,
// which are the arguments the pay call needs.
func TestTownRateKeeper_StillAsksForTheCoin(t *testing.T) {
	snap, keeperID, _ := townRateSnapshot(1, true)
	v, got := renderTownRateFor(t, snap, keeperID)
	if v == nil || v.Collector {
		t.Fatalf("keeper view = %+v, want a non-collector view", v)
	}
	if !strings.Contains(got, "the day's rate on the General Store is owing — a coin") {
		t.Errorf("keeper line changed unexpectedly:\n%s", got)
	}
	if !strings.Contains(got, "Settle it with pay (recipient: Constable Gideon Marsh, amount: 1)") {
		t.Errorf("keeper is no longer told how to settle:\n%s", got)
	}
}

// A keeper who owes nothing is told he is square (LLM-607). This reverses the
// earlier rule that he got no cue at all, and the reversal is the fix: "nothing to
// pay" and "nothing to say" are not the same, and treating them as the same left the
// settled state — the state a daily payer is in nearly always — as the one state the
// scene never described. What filled the silence was a record of coin going one way,
// and Moses James read eight settled levies off it as eight debts owing him.
//
// The settled line carries NO pay instruction. There is nothing to settle, and a pay
// instruction here would ask a square keeper to pay the rate twice.
func TestTownRateKeeper_SettledWhenPaidUp(t *testing.T) {
	snap, keeperID, _ := townRateSnapshot(0, true)
	v, got := renderTownRateFor(t, snap, keeperID)
	if v == nil || v.Collector {
		t.Fatalf("keeper view = %+v, want a non-collector view with nothing owing", v)
	}
	if !strings.Contains(got, "The rate on the General Store stands settled — nothing is owing on it, and nothing is owed back.") {
		t.Errorf("settled keeper line missing:\n%s", got)
	}
	if strings.Contains(got, "Settle it with pay") {
		t.Errorf("a square keeper is told to pay the rate again:\n%s", got)
	}
}

// A keeper with no rateable business gets no cue at all. The widening above is from
// "he owes" to "he is rateable", NOT to "he is co-present with a constable" — a
// villager who keeps no shop owes no rate and has nothing to be square about.
func TestTownRateKeeper_SilentWithoutARateableBusiness(t *testing.T) {
	snap, keeperID, _ := townRateSnapshot(0, true)
	snap.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
	if v := buildTownRate(snap, keeperID, snap.Actors[keeperID]); v != nil {
		t.Errorf("keeper view = %+v, want nil for a villager who keeps no business", v)
	}
}
