package sim

import (
	"strings"
	"testing"
	"time"
)

// speak_validation_test.go — ZBBS-WORK-323. Unit coverage of the speak
// prose-validation gate (state-claim; item-presence removed in ZBBS-HOME-416,
// transfer-verb in ZBBS-WORK-397 — see the speak_validation.go header).
// Internal (package sim) so it can call the unexported gate functions directly
// on a hand-built World.

func gateCatalog() map[ItemKind]*ItemKindDef {
	return map[ItemKind]*ItemKindDef{
		"stew":  {Name: "stew", DisplayLabel: "stew"},
		"bread": {Name: "bread", DisplayLabel: "bread"},
		"ale":   {Name: "ale", DisplayLabel: "ale"},
	}
}

// TestStateClaims_RemovedGatesStayRemoved pins the ZBBS-HOME-416 and
// ZBBS-WORK-397 removals: item-talk and transfer-narration that the old gates
// 1-2 rejected (or risked rejecting) now pass speak validation. Each line was a
// real or corpus-identified false positive of the gate named in its comment.
func TestStateClaims_RemovedGatesStayRemoved(t *testing.T) {
	w := &World{ItemKinds: gateCatalog(), Actors: map[ActorID]*Actor{}}
	now := time.Now().UTC()
	a := &Actor{ID: "hannah", Kind: KindNPCShared, Inventory: map[ItemKind]int{"stew": 5}}

	cases := []struct {
		text string
		why  string
	}{
		{"I shall buy a mug of ale from thee for 2 coins", "gate-1 class: buyer naming goods to purchase (live Ezekiel FP)"},
		{"I have fresh bread for you", "gate-1 class: unheld-item sell-claim"},
		{"served stew to Ezekiel", "gate-2 class: possession-backed handover narration"},
		{"I am still waiting on water and bread from John Ellis, and I must see to it that these goods are delivered promptly.", "gate-2 class: the one corpus-eligible line — buyer-side passive receipt (Prudence, 2026-06-05)"},
	}
	for _, c := range cases {
		if msg := validateStateClaims(w, a, c.text, now); msg != "" {
			t.Errorf("%s: %q should pass after the gate removals, got %q", c.why, c.text, msg)
		}
	}
}

func TestValidateStateClaims_Payment(t *testing.T) {
	now := time.Now().UTC()
	seller := &Actor{ID: "john", Kind: KindNPCShared, CurrentHuddleID: "h1"}
	buyer := &Actor{ID: "jeff", Kind: KindPC, CurrentHuddleID: "h1"}
	w := &World{
		Actors:    map[ActorID]*Actor{"john": seller, "jeff": buyer},
		ItemKinds: gateCatalog(),
		PayLedger: map[LedgerID]*PayLedgerEntry{},
		actorsByHuddle: map[HuddleID]map[ActorID]struct{}{
			"h1": {"john": {}, "jeff": {}},
		},
	}

	// No ledger → "you've paid me" rejects.
	if msg := validateStateClaims(w, seller, "Thank you, you've paid me in full.", now); msg == "" {
		t.Error("payment claim with no ledger should reject")
	}
	// Fresh accepted ledger seller→buyer → backed, passes.
	w.PayLedger[1] = &PayLedgerEntry{ID: 1, SellerID: "john", BuyerID: "jeff", State: PayLedgerStateAccepted, ResolvedAt: now.Add(-1 * time.Minute)}
	if msg := validateStateClaims(w, seller, "Thank you, you've paid me in full.", now); msg != "" {
		t.Errorf("fresh accepted payment should back the claim, got %q", msg)
	}
	// Stale (>5 min) → rejects again.
	w.PayLedger[1].ResolvedAt = now.Add(-6 * time.Minute)
	if msg := validateStateClaims(w, seller, "you've paid me", now); msg == "" {
		t.Error("stale payment (>5min) should not back the claim")
	}
	// Non-claim text → pass.
	if msg := validateStateClaims(w, seller, "Lovely weather today.", now); msg != "" {
		t.Errorf("non-claim should pass, got %q", msg)
	}
}

func TestValidateStateClaims_Booking(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(24 * time.Hour)
	keeper := &Actor{ID: "john", Kind: KindNPCShared, CurrentHuddleID: "h1", WorkStructureID: "inn"}
	guest := &Actor{ID: "jeff", Kind: KindPC, CurrentHuddleID: "h1"}
	w := &World{
		Actors:    map[ActorID]*Actor{"john": keeper, "jeff": guest},
		ItemKinds: gateCatalog(),
		Structures: map[StructureID]*Structure{
			"inn": {ID: "inn", Rooms: []*Room{{ID: 1, StructureID: "inn", Kind: RoomKindPrivate}}},
		},
		actorsByHuddle: map[HuddleID]map[ActorID]struct{}{
			"h1": {"john": {}, "jeff": {}},
		},
	}

	// Guest holds no grant → "you are booked" rejects.
	if msg := validateStateClaims(w, keeper, "Yes, you are booked for tonight.", now); msg == "" {
		t.Error("booking claim with no grant should reject")
	}
	// Grant the guest an active ledger room access at the inn → backed.
	guest.RoomAccess = map[RoomAccessKey]*RoomAccess{
		{RoomID: 1, Source: AccessSourceLedger}: {RoomID: 1, Source: AccessSourceLedger, Active: true, ExpiresAt: &future},
	}
	if msg := validateStateClaims(w, keeper, "Yes, you are booked for tonight.", now); msg != "" {
		t.Errorf("active grant should back the booking claim, got %q", msg)
	}
}

// TestValidateStateClaims_StaleHuddleMembership: a peer present in the
// actorsByHuddle index but whose CurrentHuddleID no longer matches must NOT back
// a claim — the gate scopes "you" to current listeners. (code_review)
func TestValidateStateClaims_StaleHuddleMembership(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(24 * time.Hour)
	keeper := &Actor{ID: "john", Kind: KindNPCShared, CurrentHuddleID: "h1", WorkStructureID: "inn"}
	// guest holds a real lodging grant but has DRIFTED to another huddle.
	guest := &Actor{
		ID: "jeff", Kind: KindPC, CurrentHuddleID: "h2",
		RoomAccess: map[RoomAccessKey]*RoomAccess{
			{RoomID: 1, Source: AccessSourceLedger}: {RoomID: 1, Source: AccessSourceLedger, Active: true, ExpiresAt: &future},
		},
	}
	w := &World{
		Actors:    map[ActorID]*Actor{"john": keeper, "jeff": guest},
		ItemKinds: gateCatalog(),
		Structures: map[StructureID]*Structure{
			"inn": {ID: "inn", Rooms: []*Room{{ID: 1, StructureID: "inn", Kind: RoomKindPrivate}}},
		},
		PayLedger: map[LedgerID]*PayLedgerEntry{
			// a fresh accepted payment too — but from the drifted guest.
			1: {ID: 1, SellerID: "john", BuyerID: "jeff", State: PayLedgerStateAccepted, ResolvedAt: now.Add(-1 * time.Minute)},
		},
		actorsByHuddle: map[HuddleID]map[ActorID]struct{}{
			"h1": {"john": {}, "jeff": {}}, // stale: jeff is really in h2
		},
	}
	if msg := validateStateClaims(w, keeper, "Yes, you are booked for tonight.", now); msg == "" {
		t.Error("booking should reject — the lodging guest has left this huddle (stale index)")
	}
	if msg := validateStateClaims(w, keeper, "Thank you, you've paid me in full.", now); msg == "" {
		t.Error("payment should reject — the payer has left this huddle (stale index)")
	}
}

// TestValidateStateClaims_FuturePayment: a payment resolved in the future (clock
// skew / non-wall-clock test time) must not back "you've paid me". (code_review)
func TestValidateStateClaims_FuturePayment(t *testing.T) {
	now := time.Now().UTC()
	seller := &Actor{ID: "john", Kind: KindNPCShared, CurrentHuddleID: "h1"}
	buyer := &Actor{ID: "jeff", Kind: KindPC, CurrentHuddleID: "h1"}
	w := &World{
		Actors:    map[ActorID]*Actor{"john": seller, "jeff": buyer},
		ItemKinds: gateCatalog(),
		PayLedger: map[LedgerID]*PayLedgerEntry{
			1: {ID: 1, SellerID: "john", BuyerID: "jeff", State: PayLedgerStateAccepted, ResolvedAt: now.Add(1 * time.Minute)},
		},
		actorsByHuddle: map[HuddleID]map[ActorID]struct{}{"h1": {"john": {}, "jeff": {}}},
	}
	if msg := validateStateClaims(w, seller, "you've paid me", now); msg == "" {
		t.Error("future-dated payment should not back the claim")
	}
}

func TestValidateStateClaims_Huddleless(t *testing.T) {
	now := time.Now().UTC()
	a := &Actor{ID: "john", Kind: KindNPCShared, CurrentHuddleID: ""} // no listener
	w := &World{Actors: map[ActorID]*Actor{"john": a}, ItemKinds: gateCatalog()}
	if msg := validateStateClaims(w, a, "Welcome, lodger!", now); msg == "" || !strings.Contains(msg, "not in a conversation") {
		t.Errorf("huddleless state claim should reject (no listener), got %q", msg)
	}
}

// TestSpeak_PCExempt confirms the NPC-discipline gates fire for NPCs but not
// PCs (the Kind check lives in the Speak command). Both speakers are huddleless,
// so what actually rejects the NPC here is the ZBBS-HOME-402 no-audience gate
// (the item-presence gate this fixture originally drove was removed in
// ZBBS-HOME-416) — the PC commits the same line untouched.
func TestSpeak_PCExempt(t *testing.T) {
	now := time.Now().UTC()
	npc := &Actor{ID: "hannah", Kind: KindNPCShared, Inventory: map[ItemKind]int{}}
	pc := &Actor{ID: "jeff", Kind: KindPC, Inventory: map[ItemKind]int{}}
	w := &World{Actors: map[ActorID]*Actor{"hannah": npc, "jeff": pc}, ItemKinds: gateCatalog()}

	if _, err := Speak("hannah", "I have fresh bread", now).Fn(w); err == nil {
		t.Error("huddleless NPC speech should be rejected (no-audience gate)")
	}
	if _, err := Speak("jeff", "I have fresh bread", now).Fn(w); err != nil {
		t.Errorf("PC speech should be exempt from the gates, got error: %v", err)
	}
}
