package sim

import (
	"context"
	"testing"
	"time"
)

// farm_tools_worn_test.go — LLM-1005. The daily assessment records what it wore
// out of the owner's pack (FarmToolsWorn + the farm_tools_worn ring entry and
// durable row), replaces or clears that record at the next assessment, and the
// upkeep obligation reserves held shovels from barter (FarmUpkeepReserve /
// SpokenFor).

// wornSink captures durable rows. assessFarmUpkeep runs inline in these tests,
// on the test goroutine, so no lock is needed.
type wornSink struct{ rows []DurableActionLogRow }

func (s *wornSink) Append(_ context.Context, row DurableActionLogRow) error {
	s.rows = append(s.rows, row)
	return nil
}

func (s *wornSink) worn(id ActorID) []DurableActionLogRow {
	var out []DurableActionLogRow
	for _, r := range s.rows {
		if r.ActorID == id && r.ActionType == ActionTypeFarmToolsWorn {
			out = append(out, r)
		}
	}
	return out
}

func wornRingEntries(w *World, id ActorID) []ActionLogEntry {
	var out []ActionLogEntry
	for _, e := range w.ActionLog {
		if e.ActorID == id && e.ActionType == ActionTypeFarmToolsWorn {
			out = append(out, e)
		}
	}
	return out
}

func TestAssessFarmUpkeep_RecordsWornShovels(t *testing.T) {
	day1 := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	// Elizabeth: 95 coins (owes 3), holds the 3 shovels she bought that day.
	w, owner, _ := farmTestWorld(30, 20, 95, 3)
	sink := &wornSink{}
	w.SetActionLogSink(sink)

	assessFarmUpkeep(w, day1)

	if owner.Inventory[ShovelItemKind] != 0 {
		t.Fatalf("shovels should be consumed, have %d", owner.Inventory[ShovelItemKind])
	}
	if owner.FarmToolsWorn == nil || owner.FarmToolsWorn.Qty != 3 || !owner.FarmToolsWorn.At.Equal(day1) {
		t.Fatalf("FarmToolsWorn = %+v, want 3 at %v", owner.FarmToolsWorn, day1)
	}
	want := "The 3 shovels you held were worn out in the farm's upkeep — that is the way of farm tools."
	ring := wornRingEntries(w, owner.ID)
	if len(ring) != 1 || ring[0].Text != want {
		t.Errorf("ring farm_tools_worn entries = %+v, want one with text %q", ring, want)
	}
	rows := sink.worn(owner.ID)
	if len(rows) != 1 {
		t.Fatalf("durable farm_tools_worn rows = %d, want 1", len(rows))
	}
	p := rows[0].Payload
	if p["item"] != string(ShovelItemKind) || p["qty"] != 3 || p["text"] != want {
		t.Errorf("durable payload = %+v", p)
	}

	// Next day she held none at the assessment: the record clears and no new
	// rows are written.
	assessFarmUpkeep(w, day1.Add(24*time.Hour))
	if owner.FarmToolsWorn != nil {
		t.Errorf("FarmToolsWorn after a zero assessment = %+v, want nil", owner.FarmToolsWorn)
	}
	if n := len(sink.worn(owner.ID)); n != 1 {
		t.Errorf("durable rows after a zero assessment = %d, want still 1", n)
	}
	if n := len(wornRingEntries(w, owner.ID)); n != 1 {
		t.Errorf("ring entries after a zero assessment = %d, want still 1", n)
	}

	// Day 3 she held one: the record is replaced, singular wording.
	owner.Inventory[ShovelItemKind] = 1
	day3 := day1.Add(48 * time.Hour)
	assessFarmUpkeep(w, day3)
	if owner.FarmToolsWorn == nil || owner.FarmToolsWorn.Qty != 1 || !owner.FarmToolsWorn.At.Equal(day3) {
		t.Errorf("FarmToolsWorn day 3 = %+v, want 1 at %v", owner.FarmToolsWorn, day3)
	}
	if rows := sink.worn(owner.ID); len(rows) != 2 || rows[1].Payload["text"] != "The shovel you held was worn out in the farm's upkeep — that is the way of farm tools." {
		t.Errorf("day-3 durable rows = %+v", rows)
	}
}

// The loss is recorded whether or not she still owes and whether or not she can
// be warranted: the explanation is about the shovels that went, not the levy.
func TestAssessFarmUpkeep_RecordsWornEvenWhenNothingOwedOrBusy(t *testing.T) {
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)

	w, owner, _ := farmTestWorld(30, 20, 25, 2) // below the floor: owes nothing
	assessFarmUpkeep(w, now)
	if owner.FarmToolsWorn == nil || owner.FarmToolsWorn.Qty != 2 {
		t.Errorf("below floor: FarmToolsWorn = %+v, want 2", owner.FarmToolsWorn)
	}
	if hasFarmUpkeepWarrant(owner) {
		t.Error("below floor: no upkeep warrant expected")
	}

	w2, busy, _ := farmTestWorld(30, 20, 95, 2)
	busy.TickInFlight = true // mid-turn: not warrant-eligible
	assessFarmUpkeep(w2, now)
	if busy.FarmToolsWorn == nil || busy.FarmToolsWorn.Qty != 2 {
		t.Errorf("mid-turn: FarmToolsWorn = %+v, want 2", busy.FarmToolsWorn)
	}
}

func TestFarmUpkeepReserve(t *testing.T) {
	w, owner, _ := farmTestWorld(30, 20, 95, 0) // owes 3
	cases := []struct {
		held, want int
	}{
		{0, 0},
		{2, 2}, // all held shovels are owed
		{3, 3},
		{5, 3}, // two beyond the obligation are spare
	}
	for _, c := range cases {
		got := FarmUpkeepReserve(w.VillageObjects, owner.ID, owner.Coins, c.held, 30, 20)
		if got != c.want {
			t.Errorf("held %d: reserve = %d, want %d", c.held, got, c.want)
		}
	}
	if got := FarmUpkeepReserve(w.VillageObjects, "someone-else", 95, 5, 30, 20); got != 0 {
		t.Errorf("non-owner reserve = %d, want 0", got)
	}
	if got := FarmUpkeepReserve(w.VillageObjects, owner.ID, 95, 5, 30, 0); got != 0 {
		t.Errorf("feature off: reserve = %d, want 0", got)
	}
}

// LiveBarterHolder carries the reserve into SpokenFor, so the barter gates and
// the carter leave upkeep shovels in her pack; the surplus stays spare.
func TestSpokenFor_UpkeepShovels(t *testing.T) {
	w, owner, _ := farmTestWorld(30, 20, 95, 5) // owes 3, holds 5
	claims := SpokenFor(nil, nil, LiveBarterHolder(w, owner))
	c := claims[ShovelItemKind]
	if c.Qty != 3 || c.Reason != SpokenForUpkeep {
		t.Errorf("shovel claim = %+v, want 3 for upkeep", c)
	}
	if spare := SpareQty(owner.Inventory, claims, ShovelItemKind); spare != 2 {
		t.Errorf("spare shovels = %d, want 2", spare)
	}
	sa := w.publishedActorSnapshot(owner, time.Now(), nil, nil)
	if sa.FarmUpkeepReserve != 3 {
		t.Errorf("published FarmUpkeepReserve = %d, want 3", sa.FarmUpkeepReserve)
	}

	// The prompt side (published snapshot) and the trade gate (live world) must
	// agree on what she may put up: 2 spare, so a 2-shovel bundle passes and a
	// 3-shovel bundle is refused.
	snap := &Snapshot{VillageObjects: w.VillageObjects, Actors: map[ActorID]*ActorSnapshot{owner.ID: sa}}
	snapClaims := SpokenFor(nil, nil, SnapshotBarterHolder(snap, sa))
	if spare := SpareQty(sa.Inventory, snapClaims, ShovelItemKind); spare != 2 {
		t.Errorf("snapshot-side spare shovels = %d, want 2", spare)
	}
	if !buyerCanSparePayItems(w, owner, []ItemKindQty{{Kind: ShovelItemKind, Qty: 2}}) {
		t.Error("live gate refused 2 spare shovels")
	}
	if buyerCanSparePayItems(w, owner, []ItemKindQty{{Kind: ShovelItemKind, Qty: 3}}) {
		t.Error("live gate accepted 3 shovels, one of them owed to upkeep")
	}
}
