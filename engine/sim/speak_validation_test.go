package sim

import (
	"strings"
	"testing"
	"time"
)

// speak_validation_test.go — ZBBS-WORK-323. Unit coverage of the three speak
// prose-validation gates (item-presence, transfer-verb, state-claim) + the
// helpers (isAskShapeSpeech, extractItemMentions). Internal (package sim) so it
// can call the unexported gate functions directly on a hand-built World.

func gateCatalog() map[ItemKind]*ItemKindDef {
	return map[ItemKind]*ItemKindDef{
		"stew":  {Name: "stew", DisplayLabel: "stew"},
		"bread": {Name: "bread", DisplayLabel: "bread"},
		"ale":   {Name: "ale", DisplayLabel: "ale"},
	}
}

func TestIsAskShapeSpeech(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"Do you have any bread?", true},
		{"I'd like some stew.", true},
		{"I'll take an ale.", true},
		{"I'm out of bread.", true},
		{"We don't stock ale.", true},
		{"Can I get a stew?", true},
		{"Here is fresh bread for sale.", false},
		{"I have hot stew today.", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isAskShapeSpeech(c.text); got != c.want {
			t.Errorf("isAskShapeSpeech(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestExtractItemMentions(t *testing.T) {
	w := &World{ItemKinds: gateCatalog()}
	cases := []struct {
		text string
		want []ItemKind
	}{
		{"fresh bread and ale", []ItemKind{"ale", "bread"}}, // sorted
		{"ALE, please", []ItemKind{"ale"}},                  // case-insensitive + punctuation boundary
		{"a fine ale-house", []ItemKind{"ale"}},             // hyphen boundary
		{"the sale is on", nil},                             // "sale" must NOT match "ale"
		{"this stale bread", []ItemKind{"bread"}},           // "stale" must NOT match "ale"; bread does
		{"stew stew stew", []ItemKind{"stew"}},              // dedup
		{"nothing on the menu", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := extractItemMentions(w, c.text)
		if len(got) != len(c.want) {
			t.Errorf("extractItemMentions(%q) = %v, want %v", c.text, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("extractItemMentions(%q) = %v, want %v", c.text, got, c.want)
				break
			}
		}
	}
	// Empty catalog → no mentions (fail-open).
	if got := extractItemMentions(&World{}, "fresh bread and ale"); got != nil {
		t.Errorf("empty catalog should yield nil, got %v", got)
	}
}

// gateActor is a minimal NPC speaker for the gate tests.
func gateActor(id ActorID, inv map[ItemKind]int) *Actor {
	return &Actor{ID: id, Kind: KindNPCShared, Inventory: inv}
}

func TestValidateSpeechClaims_ItemPresence(t *testing.T) {
	w := &World{ItemKinds: gateCatalog(), Actors: map[ActorID]*Actor{}}
	now := time.Now().UTC()

	// Claims bread it doesn't have → reject.
	a := gateActor("hannah", map[ItemKind]int{"stew": 2})
	if msg := validateSpeechClaims(w, a, "I have fresh bread for you", now); msg == "" || !strings.Contains(msg, "bread") {
		t.Errorf("expected item-presence reject naming bread, got %q", msg)
	}
	// Has the stew it mentions → pass.
	if msg := validateSpeechClaims(w, a, "I have hot stew today", now); msg != "" {
		t.Errorf("expected pass for stew-in-hand, got %q", msg)
	}
	// Ask-shape skips the gate even though bread isn't held.
	if msg := validateSpeechClaims(w, a, "Do you have any bread?", now); msg != "" {
		t.Errorf("ask-shape should skip item gate, got %q", msg)
	}
}

func TestValidateSpeechClaims_TransferVerb(t *testing.T) {
	w := &World{ItemKinds: gateCatalog(), Actors: map[ActorID]*Actor{}}
	now := time.Now().UTC()
	a := gateActor("hannah", map[ItemKind]int{"stew": 5, "ale": 5})

	// Has stew, but narrates a handover → transfer-verb reject (option B).
	msg := validateSpeechClaims(w, a, "served stew to Ezekiel", now)
	if msg == "" || !strings.Contains(msg, "pay_with_item") {
		t.Errorf("expected transfer-verb reject pointing at pay_with_item, got %q", msg)
	}
	// Has ale, just advertises it (no transfer verb) → pass.
	if msg := validateSpeechClaims(w, a, "I have cold ale on tap", now); msg != "" {
		t.Errorf("advertising in-stock ale should pass, got %q", msg)
	}
	// Transfer verb with NO item mention → pass (not a transfer claim).
	if msg := validateSpeechClaims(w, a, "I served this town for thirty years", now); msg != "" {
		t.Errorf("transfer verb without item should pass, got %q", msg)
	}
	// Ask-shape with verb+item → skip.
	if msg := validateSpeechClaims(w, a, "Did you want me to serve you stew?", now); msg != "" {
		t.Errorf("ask-shape transfer should skip, got %q", msg)
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

func TestValidateStateClaims_Huddleless(t *testing.T) {
	now := time.Now().UTC()
	a := &Actor{ID: "john", Kind: KindNPCShared, CurrentHuddleID: ""} // no listener
	w := &World{Actors: map[ActorID]*Actor{"john": a}, ItemKinds: gateCatalog()}
	if msg := validateStateClaims(w, a, "Welcome, lodger!", now); msg == "" || !strings.Contains(msg, "not in a conversation") {
		t.Errorf("huddleless state claim should reject (no listener), got %q", msg)
	}
}

// TestSpeak_PCExempt confirms the gates fire for NPCs but not PCs (the Kind
// check lives in the Speak command). A PC and an NPC both speak the same
// unbacked item claim; only the NPC is rejected.
func TestSpeak_PCExempt(t *testing.T) {
	now := time.Now().UTC()
	npc := &Actor{ID: "hannah", Kind: KindNPCShared, Inventory: map[ItemKind]int{}}
	pc := &Actor{ID: "jeff", Kind: KindPC, Inventory: map[ItemKind]int{}}
	w := &World{Actors: map[ActorID]*Actor{"hannah": npc, "jeff": pc}, ItemKinds: gateCatalog()}

	if _, err := Speak("hannah", "I have fresh bread", now).Fn(w); err == nil {
		t.Error("NPC fabricating bread should be rejected by the gate")
	}
	if _, err := Speak("jeff", "I have fresh bread", now).Fn(w); err != nil {
		t.Errorf("PC speech should be exempt from the gates, got error: %v", err)
	}
}
