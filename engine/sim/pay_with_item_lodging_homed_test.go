package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_lodging_homed_test.go — LLM-182. The lodging seek/offer cues
// are home-gated in perception, but nothing stopped a homed villager who
// FREELANCES a room request (no engine cue prompts it) from minting a real
// nights_stay purchase — paying for a bed it never uses, then walking home to
// sleep (Prudence Ward → Ward Residence, live 2026-06-29). PayWithItem's
// lodging intake now rejects a buyer who already has a home, the buyer-side
// mirror of the WORK-343 seller gate. Keyed on HomeStructureID ONLY — an active
// grant is not a disqualifier, so a homeless lodger renewing still books. Reuses
// buildPayWithItemWorld / seedLodgingFixture / readPayLedger / mustSend.

// TestPayWithItem_Lodging_HomedBuyer_Rejected — a buyer with a home is steered
// home, mints no ledger, and keeps their coins.
func TestPayWithItem_Lodging_HomedBuyer_Rejected(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	// Alice already has a home — she should be steered home, not sold a room.
	mustSend(t, w, func(world *sim.World) {
		world.Structures["alice-home"] = &sim.Structure{ID: "alice-home", DisplayName: "Ward Residence"}
		world.Actors["alice"].HomeStructureID = "alice-home"
	})

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "already have a home") {
		t.Fatalf("want homed-buyer reject, got %v", err)
	}
	if !strings.Contains(err.Error(), "Ward Residence") {
		t.Errorf("reject should name the buyer's home: %v", err)
	}
	if ledger := readPayLedger(t, w); len(ledger) != 0 {
		t.Errorf("ledger has %d entries after rejected homed-buyer intake, want 0", len(ledger))
	}
	if snap := w.Published(); snap.Actors["alice"].Coins != 100 {
		t.Errorf("alice.Coins moved on rejected intake: %d", snap.Actors["alice"].Coins)
	}
}

// TestPayWithItem_Lodging_HomedBuyer_RejectedWithoutStructureRow — the home
// structure isn't resolvable (id set, row absent); the gate still fires with the
// generic copy rather than naming a place.
func TestPayWithItem_Lodging_HomedBuyer_RejectedWithoutStructureRow(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	mustSend(t, w, func(world *sim.World) {
		world.Actors["alice"].HomeStructureID = "ghost-home"
	})

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "already have a home") {
		t.Fatalf("want homed-buyer reject with generic copy, got %v", err)
	}
	if ledger := readPayLedger(t, w); len(ledger) != 0 {
		t.Errorf("ledger has %d entries after rejected intake, want 0", len(ledger))
	}
}

// TestPayWithItem_Lodging_HomelessBuyer_StillBooks — the legitimate seeker
// (Ezekiel's case): no home, no grant. Books normally.
func TestPayWithItem_Lodging_HomelessBuyer_StillBooks(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("a homeless buyer should still book a room: %v", err)
	}
	if result := res.(sim.PayWithItemResult); result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
}

// TestPayWithItem_Lodging_HomelessRenewer_NotBlocked — a homeless lodger who
// ALREADY holds an active room grant, renewing the next night (LLM-46/96). The
// gate keys on HomeStructureID only, NOT on an active grant, so renewal still
// books — guards against a future tightening that would re-break renewals.
func TestPayWithItem_Lodging_HomelessRenewer_NotBlocked(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	exp := time.Now().UTC().Add(24 * time.Hour)
	mustSend(t, w, func(world *sim.World) {
		key := sim.RoomAccessKey{RoomID: 2, Source: sim.AccessSourceLedger}
		world.Actors["alice"].RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
			key: {RoomID: 2, Source: sim.AccessSourceLedger, Active: true, ExpiresAt: &exp},
		}
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("a homeless lodger renewing should not be blocked: %v", err)
	}
	if result := res.(sim.PayWithItemResult); result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
}
