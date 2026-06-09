package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ZBBS-WORK-374 — prompt re-architecture coverage: the engine-owned vendor
// operating-block (replacing salem-vendor's startup_instructions sell-pressure)
// and the same-tick de-dup of a just-heard utterance that the "## What just
// happened" section already renders.

const vendorOperatingMarker = "How you trade:"

// TestRenderVendorOperating_Gate: the trade-conduct block renders only for a
// businessowner. A non-keeper (visitor, plain NPC) gets nothing.
func TestRenderVendorOperating_Gate(t *testing.T) {
	var on strings.Builder
	renderVendorOperating(&on, true)
	got := on.String()
	if !strings.Contains(got, vendorOperatingMarker) {
		t.Errorf("businessowner should get the operating block, got: %q", got)
	}
	// The scoped wording — "a greeting is not a sale" — is the whole point of the
	// move (replacing the always-be-closing sell-pressure).
	if !strings.Contains(got, "don't quote prices or pitch your goods or rooms unless they ask") {
		t.Errorf("operating block missing the greet-is-not-a-sale rule, got: %q", got)
	}
	// ZBBS-HOME-385: the "tend to your trade" working framing keeps vendors at
	// their business instead of drifting off-post with nothing to do.
	if !strings.Contains(got, "- Tend to your trade — your living depends on it.") {
		t.Errorf("operating block missing the tend-to-your-trade framing, got: %q", got)
	}

	var off strings.Builder
	renderVendorOperating(&off, false)
	if off.Len() != 0 {
		t.Errorf("non-businessowner should get no operating block, got: %q", off.String())
	}
}

// TestBuild_BusinessownerFlag: Build sets Payload.Businessowner off the
// snapshot's BusinessownerState pointer (the keeper predicate), and the flag
// drives the rendered block end-to-end.
func TestBuild_BusinessownerFlag(t *testing.T) {
	keeper := sharedSnap("moses", "Moses", "")
	keeper.BusinessownerState = &sim.BusinessownerState{Flavor: "reserved"}
	// Place Moses at his own post so the vendor block renders — the cue now gates
	// on AtOwnBusiness (InsideStructureID == WorkStructureID). ZBBS-WORK-385.
	keeper.WorkStructureID = "post"
	keeper.InsideStructureID = "post"
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"moses": keeper}}
	p := Build(snap, "moses", nil)
	if !p.Businessowner {
		t.Fatal("Businessowner flag not set for actor with BusinessownerState")
	}
	if got := combinedPrompt(Render(p, DefaultRenderConfig())); !strings.Contains(got, vendorOperatingMarker) {
		t.Errorf("businessowner prompt missing the operating block:\n%s", got)
	}

	plain := sharedSnap("visitor", "A Visitor", "")
	pp := Build(&sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{"visitor": plain}}, "visitor", nil)
	if pp.Businessowner {
		t.Fatal("Businessowner flag set for actor without BusinessownerState")
	}
	if got := combinedPrompt(Render(pp, DefaultRenderConfig())); strings.Contains(got, vendorOperatingMarker) {
		t.Errorf("non-businessowner prompt should not carry the operating block:\n%s", got)
	}
}

// TestBuild_VendorCues_GatedOnAtOwnBusiness covers ZBBS-WORK-385: the vendor
// operating block AND the OfferableCustomers "offer your wares" cue fire only
// when a businessowner is physically at their own business (InsideStructureID ==
// WorkStructureID). A keeper huddled with a customer carrying sellable goods but
// AWAY from their post — a customer in someone else's establishment — gets
// neither cue, even though Businessowner stays true. Expresses WHERE they are,
// not just WHO they are.
func TestBuild_VendorCues_GatedOnAtOwnBusiness(t *testing.T) {
	// inside varies; her business is always "tavern".
	newSnap := func(inside sim.StructureID) *sim.Snapshot {
		seller := &sim.ActorSnapshot{
			DisplayName:        "Goodwife Ellis",
			Kind:               sim.KindNPCShared,
			CurrentHuddleID:    "h1",
			BusinessownerState: &sim.BusinessownerState{},
			Inventory:          map[sim.ItemKind]int{"stew": 5},
			Acquaintances:      map[string]sim.Acquaintance{"Goodwife Mary": {}},
			WorkStructureID:    "tavern",
			InsideStructureID:  inside,
		}
		customer := &sim.ActorSnapshot{
			DisplayName:     "Goodwife Mary",
			Kind:            sim.KindNPCStateful,
			CurrentHuddleID: "h1",
		}
		return &sim.Snapshot{
			Actors: map[sim.ActorID]*sim.ActorSnapshot{"ellis": seller, "mary": customer},
			Huddles: map[sim.HuddleID]*sim.Huddle{
				"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"ellis": {}, "mary": {}}},
			},
		}
	}

	// Off-post: huddled at "market" while her business is "tavern" → neither cue.
	off := Build(newSnap("market"), "ellis", nil)
	if !off.Businessowner {
		t.Fatal("Businessowner should stay true off-post")
	}
	if off.AtOwnBusiness {
		t.Fatal("AtOwnBusiness should be false when InsideStructureID != WorkStructureID")
	}
	if off.OfferableCustomers != nil {
		t.Error("off-post businessowner should get no OfferableCustomers cue")
	}
	if got := combinedPrompt(Render(off, DefaultRenderConfig())); strings.Contains(got, vendorOperatingMarker) {
		t.Errorf("off-post businessowner should not carry the operating block:\n%s", got)
	}

	// On-post: huddled at "tavern", her own business → both cues fire.
	on := Build(newSnap("tavern"), "ellis", nil)
	if !on.AtOwnBusiness {
		t.Fatal("AtOwnBusiness should be true when InsideStructureID == WorkStructureID")
	}
	if on.OfferableCustomers == nil {
		t.Error("on-post businessowner huddled with a customer should get the OfferableCustomers cue")
	}
	if got := combinedPrompt(Render(on, DefaultRenderConfig())); !strings.Contains(got, vendorOperatingMarker) {
		t.Errorf("on-post businessowner prompt missing the operating block:\n%s", got)
	}
}

// TestBuild_DedupHeardFactInCurrentBatch: a `heard` SalientFact whose text the
// current tick already surfaces as a speech warrant is dropped from
// "## What you remember" (the live "Hello" double-render), while an older heard
// fact from the same peer — not in this batch — is kept and backfills the slot.
func TestBuild_DedupHeardFactInCurrentBatch(t *testing.T) {
	now := time.Now()
	subject := sharedSnap("hannah", "Hannah", "h1")
	subject.Relationships = map[sim.ActorID]*sim.Relationship{
		"bob": {
			SummaryText: "A traveller.",
			SalientFacts: []sim.SalientFact{
				{At: now.Add(-time.Hour), Kind: sim.InteractionHeard, Text: "Have you any bread?"},
				{At: now, Kind: sim.InteractionHeard, Text: "Hello"}, // == this tick's warrant
			},
		},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": subject,
			"bob":    peerSnap("bob", "Bob", "traveller", sim.KindPC, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "bob": {}}},
		},
	}
	// "Hello" arrives this tick as bob's speech warrant — so it's already in
	// "## What just happened" and must not also appear as remembered.
	p := Build(snap, "hannah", []sim.WarrantMeta{speechWarrant(1, "", "bob", "Hello")})

	var bob *RelationshipPeerView
	for i := range p.Relationships {
		if p.Relationships[i].PeerID == "bob" {
			bob = &p.Relationships[i]
			break
		}
	}
	if bob == nil {
		t.Fatal("expected a relationship view for bob")
	}
	for _, f := range bob.RecentFacts {
		if f.Text == "Hello" {
			t.Errorf("the just-heard 'Hello' should be de-duped from remembered facts: %+v", bob.RecentFacts)
		}
	}
	// The older, non-duplicated heard fact survives and backfills the slot.
	kept := false
	for _, f := range bob.RecentFacts {
		if f.Text == "Have you any bread?" {
			kept = true
		}
	}
	if !kept {
		t.Errorf("older non-duplicate heard fact should be retained: %+v", bob.RecentFacts)
	}
}

// TestBuild_DedupHeardFact_MaxLengthExcerpt: the (peer, text) match is exact, and
// both producers cap at MaxSalientFactTextLen — the warrant Excerpt via
// truncateRunes(spoke.Text, …) and the SalientFact via the same write-time cap.
// This exercises the boundary the match relies on: a max-length (already-
// truncated) utterance still de-dups. If the two caps ever diverged, this case
// would catch the silent double-render. (truncateRunes lives in the handlers
// package; both producers share sim.MaxSalientFactTextLen, so the fixed point is
// reproduced here by truncating to that rune count directly.)
func TestBuild_DedupHeardFact_MaxLengthExcerpt(t *testing.T) {
	long := strings.Repeat("x", sim.MaxSalientFactTextLen+20)
	truncated := string([]rune(long)[:sim.MaxSalientFactTextLen])

	subject := sharedSnap("hannah", "Hannah", "h1")
	subject.Relationships = map[sim.ActorID]*sim.Relationship{
		"bob": {SalientFacts: []sim.SalientFact{
			{At: time.Now(), Kind: sim.InteractionHeard, Text: truncated},
		}},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": subject,
			"bob":    peerSnap("bob", "Bob", "traveller", sim.KindPC, "h1"),
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"hannah": {}, "bob": {}}},
		},
	}
	p := Build(snap, "hannah", []sim.WarrantMeta{speechWarrant(1, "", "bob", truncated)})

	for i := range p.Relationships {
		if p.Relationships[i].PeerID != "bob" {
			continue
		}
		for _, f := range p.Relationships[i].RecentFacts {
			if f.Text == truncated {
				t.Errorf("a max-length heard utterance should still de-dup against its warrant excerpt")
			}
		}
		return
	}
	// No bob view at all is also acceptable here: the sole fact was de-duped, so
	// buildRelationships drops the now-empty peer (len(out)==0 → nil).
}
