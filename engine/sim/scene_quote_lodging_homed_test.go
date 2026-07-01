package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// scene_quote_lodging_homed_test.go — LLM-208. The seller-side mirror of the
// LLM-182 buyer guard: a keeper who FREELANCES a nights_stay quote at a homed
// guest (no engine cue prompts it) is rejected at creation. The guest can't take
// a room — the pay_with_item buyer guard rejects it — so the offer only dangles a
// doomed nightly negotiation (John Ellis → Prudence Ward, live 2026-06-30). Keyed
// on the "lodging" capability + the resolved target's HomeStructureID. A homeless
// seeker (Ezekiel's case) is still quotable, and a PUBLIC quote is not gated here
// (the homed viewer is spared it in perception instead). Reuses buildQuoteTestWorld
// / seedLodgingFixture / captureSceneQuoteCreated / mustSend.

// TestSceneQuoteCreate_Lodging_HomedTargetBuyer_Rejected — a targeted room quote
// at a homed guest is refused before any quote is minted, and the steer names her
// home.
func TestSceneQuoteCreate_Lodging_HomedTargetBuyer_Rejected(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "prudence", displayName: "Prudence Ward", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "john", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	// Prudence already has a home — the keeper should be steered off offering her a room.
	mustSend(t, w, func(world *sim.World) {
		world.Structures["ward-residence"] = &sim.Structure{ID: "ward-residence", DisplayName: "Ward Residence"}
		world.Actors["prudence"].HomeStructureID = "ward-residence"
	})

	captured := captureSceneQuoteCreated(t, w)
	_, err := w.Send(sim.SceneQuoteCreate("john", []sim.QuoteLineInput{{ItemName: "nights_stay", Qty: 1}}, 4, false, "Prudence Ward", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "has a home") {
		t.Fatalf("want homed-target reject, got %v", err)
	}
	if !strings.Contains(err.Error(), "Ward Residence") {
		t.Errorf("reject should name the guest's home: %v", err)
	}
	if len(*captured) != 0 {
		t.Errorf("a quote was minted despite the homed-target reject: %+v", *captured)
	}
}

// TestSceneQuoteCreate_Lodging_HomedTargetBuyer_RejectedWithoutStructureRow — the
// home structure id is set but its row is absent; the gate still fires with the
// generic copy rather than naming a place.
func TestSceneQuoteCreate_Lodging_HomedTargetBuyer_RejectedWithoutStructureRow(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "prudence", displayName: "Prudence Ward", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "john", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	mustSend(t, w, func(world *sim.World) {
		world.Actors["prudence"].HomeStructureID = "ghost-home"
	})

	_, err := w.Send(sim.SceneQuoteCreate("john", []sim.QuoteLineInput{{ItemName: "nights_stay", Qty: 1}}, 4, false, "Prudence Ward", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "has a home") {
		t.Fatalf("want homed-target reject with generic copy, got %v", err)
	}
}

// TestSceneQuoteCreate_Lodging_HomelessTargetBuyer_Allowed — the legitimate
// seeker: a homeless guest with no home. The keeper's room quote still mints.
func TestSceneQuoteCreate_Lodging_HomelessTargetBuyer_Allowed(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "john", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})

	captured := captureSceneQuoteCreated(t, w)
	res, err := w.Send(sim.SceneQuoteCreate("john", []sim.QuoteLineInput{{ItemName: "nights_stay", Qty: 1}}, 4, false, "Ezekiel Crane", nil, time.Now().UTC()))
	if err != nil {
		t.Fatalf("a homeless seeker should still be quotable a room: %v", err)
	}
	if _, ok := res.(sim.SceneQuoteCreateResult); !ok {
		t.Fatalf("unexpected result type %T", res)
	}
	if len(*captured) != 1 {
		t.Errorf("want exactly one minted quote, got %d", len(*captured))
	}
}

// TestSceneQuoteCreate_Lodging_PublicQuote_NotSellerGated — a PUBLIC nights_stay
// quote (no target_buyer) mints even with a homed actor co-present: the seller
// gate only pre-checks a resolved target. The homed viewer is spared the offer in
// perception instead (filterHomedLodgingQuoteWarrants), so a homeless seeker in
// the same scene can still take it.
func TestSceneQuoteCreate_Lodging_PublicQuote_NotSellerGated(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "prudence", displayName: "Prudence Ward", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()
	seedLodgingFixture(t, w, "john", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	mustSend(t, w, func(world *sim.World) {
		world.Structures["ward-residence"] = &sim.Structure{ID: "ward-residence", DisplayName: "Ward Residence"}
		world.Actors["prudence"].HomeStructureID = "ward-residence"
	})

	_, err := w.Send(sim.SceneQuoteCreate("john", []sim.QuoteLineInput{{ItemName: "nights_stay", Qty: 1}}, 4, false, "", nil, time.Now().UTC()))
	if err != nil {
		t.Fatalf("a public room quote should mint even with a homed actor present: %v", err)
	}
}
