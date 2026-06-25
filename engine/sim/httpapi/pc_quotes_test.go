package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_quotes_test.go — ZBBS-HOME-426 take-able quote list for the Pay modal.

// quotesWorld stands up a running world with the full eligibility matrix:
// a login-bound PC and seller John co-huddled in "h1" anchored to scene
// "sc1", plus one quote per eligibility rule so a single response asserts
// every include/exclude branch. Seeded through Send so the republish lands
// it all in Published() (the handler is a pure snapshot read).
func quotesWorld(t *testing.T) *sim.World {
	t.Helper()
	w := seededWorld(t)
	now := time.Now().UTC()
	live := now.Add(5 * time.Minute)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
			"stew": {Name: "stew", DisplayLabel: "Stew", Category: "food"},
		}
		world.Actors["pc-tester"] = &sim.Actor{
			ID: "pc-tester", DisplayName: "Tester", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "tester",
			Pos: sim.TilePos{X: 3, Y: 4}, CurrentHuddleID: "h1",
		}
		world.Actors["npc-john"] = &sim.Actor{
			ID: "npc-john", DisplayName: "John Ellis", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, Pos: sim.TilePos{X: 3, Y: 4},
			CurrentHuddleID: "h1",
		}
		// Mary is in the scene's structure but NOT in the PC's huddle — her
		// quote must be filtered (fast-path predicate 3 would reject a take).
		world.Actors["npc-mary"] = &sim.Actor{
			ID: "npc-mary", DisplayName: "Mary", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Pos: sim.TilePos{X: 3, Y: 4},
		}
		// Two co-huddled "Bob"s: pc/pay's seller-by-display-name resolution
		// rejects the ambiguous match (case-insensitively), so Bob's quote
		// must be filtered too.
		world.Actors["npc-bob1"] = &sim.Actor{
			ID: "npc-bob1", DisplayName: "Bob", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Pos: sim.TilePos{X: 3, Y: 4},
			CurrentHuddleID: "h1",
		}
		world.Actors["npc-bob2"] = &sim.Actor{
			ID: "npc-bob2", DisplayName: "bob", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Pos: sim.TilePos{X: 3, Y: 4},
			CurrentHuddleID: "h1",
		}
		world.Huddles["h1"] = &sim.Huddle{
			ID: "h1",
			Members: map[sim.ActorID]struct{}{
				"pc-tester": {}, "npc-john": {}, "npc-bob1": {}, "npc-bob2": {},
			},
		}
		world.Scenes["sc1"] = &sim.Scene{
			ID: "sc1", Bound: sim.NewUnboundedBound(),
			Huddles: map[sim.HuddleID]struct{}{"h1": {}},
		}
		world.Scenes["sc2"] = &sim.Scene{
			ID: "sc2", Bound: sim.NewUnboundedBound(),
			Huddles: map[sim.HuddleID]struct{}{},
		}
		world.Quotes = map[sim.QuoteID]*sim.SceneQuote{
			// Included: public, John's, in the PC's scene, live.
			1: {ID: 1, SceneID: "sc1", SellerID: "npc-john",
				Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 2, ConsumeNow: true,
				State: sim.SceneQuoteStateActive, CreatedAt: now.Add(-2 * time.Minute), ExpiresAt: live},
			// Included + targeted: addressed to this PC; newer than 1 but
			// targeted sorts first anyway.
			2: {ID: 2, SceneID: "sc1", SellerID: "npc-john", TargetBuyer: "pc-tester",
				Lines: []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}}, Amount: 4, ConsumeNow: false,
				State: sim.SceneQuoteStateActive, CreatedAt: now.Add(-1 * time.Minute), ExpiresAt: live},
			// Excluded: targeted at someone else.
			3: {ID: 3, SceneID: "sc1", SellerID: "npc-john", TargetBuyer: "npc-mary",
				Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 2, ConsumeNow: true,
				State: sim.SceneQuoteStateActive, CreatedAt: now, ExpiresAt: live},
			// Excluded: past ExpiresAt though still marked Active (sweep lag).
			4: {ID: 4, SceneID: "sc1", SellerID: "npc-john",
				Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 2, ConsumeNow: true,
				State: sim.SceneQuoteStateActive, CreatedAt: now.Add(-20 * time.Minute),
				ExpiresAt: now.Add(-10 * time.Minute)},
			// Excluded: terminal state.
			5: {ID: 5, SceneID: "sc1", SellerID: "npc-john",
				Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 2, ConsumeNow: true,
				State: sim.SceneQuoteStateSuperseded, CreatedAt: now, ExpiresAt: live},
			// Excluded: scene doesn't observe the PC's huddle.
			6: {ID: 6, SceneID: "sc2", SellerID: "npc-john",
				Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 2, ConsumeNow: true,
				State: sim.SceneQuoteStateActive, CreatedAt: now, ExpiresAt: live},
			// Excluded: group order (ConsumerIDs non-empty) — V1 scope.
			7: {ID: 7, SceneID: "sc1", SellerID: "npc-john",
				Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 6, ConsumeNow: true,
				ConsumerIDs: []sim.ActorID{"pc-tester", "npc-mary"},
				State:       sim.SceneQuoteStateActive, CreatedAt: now, ExpiresAt: live},
			// Excluded: seller not co-huddled with the PC.
			8: {ID: 8, SceneID: "sc1", SellerID: "npc-mary",
				Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 2, ConsumeNow: true,
				State: sim.SceneQuoteStateActive, CreatedAt: now, ExpiresAt: live},
			// Excluded: seller's display name is ambiguous within the huddle
			// (the other Bob), so a take's seller resolution would reject.
			9: {ID: 9, SceneID: "sc1", SellerID: "npc-bob1",
				Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 2, ConsumeNow: true,
				State: sim.SceneQuoteStateActive, CreatedAt: now, ExpiresAt: live},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed quotes world: %v", err)
	}
	return w
}

func TestHandlePCQuotes_EligibilityMatrix(t *testing.T) {
	srv := NewServer(quotesWorld(t), okAuth{})

	rec := get(t, srv, "/api/village/pc/quotes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res pcQuotesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Quotes) != 2 {
		t.Fatalf("len = %d, want 2 (quotes 2 + 1; 3-9 excluded); body=%s", len(res.Quotes), rec.Body.String())
	}

	// Targeted-at-me sorts first.
	q := res.Quotes[0]
	if q.QuoteID != 2 || !q.Targeted {
		t.Errorf("quotes[0] = %+v, want quote 2 targeted", q)
	}
	if q.Seller != "John Ellis" || q.Item != "nights_stay" || q.Amount != 4 || q.ConsumeNow {
		t.Errorf("quotes[0] terms = %+v, want John Ellis / nights_stay / 4 / consume_now=false", q)
	}
	// No catalog entry for nights_stay in this seed — label falls back to
	// the wire name.
	if q.DisplayLabel != "nights_stay" {
		t.Errorf("quotes[0].DisplayLabel = %q, want fallback nights_stay", q.DisplayLabel)
	}

	q = res.Quotes[1]
	if q.QuoteID != 1 || q.Targeted {
		t.Errorf("quotes[1] = %+v, want quote 1 untargeted", q)
	}
	if q.DisplayLabel != "Stew" {
		t.Errorf("quotes[1].DisplayLabel = %q, want catalog label Stew", q.DisplayLabel)
	}
	if q.ExpiresInSeconds <= 0 {
		t.Errorf("quotes[1].ExpiresInSeconds = %d, want > 0", q.ExpiresInSeconds)
	}
}

func TestHandlePCQuotes_UnhuddledPCEmpty(t *testing.T) {
	// A login-bound PC with no huddle sees no quotes — predicate 3 would
	// reject any take, so the list is honestly empty (the compose path and
	// the ZBBS-HOME-427 pay-time bootstrap cover walk-in offers).
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	srv := NewServer(w, okAuth{})

	rec := get(t, srv, "/api/village/pc/quotes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res pcQuotesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Quotes) != 0 {
		t.Errorf("len = %d, want 0", len(res.Quotes))
	}
}

func TestHandlePCQuotes_NoPCEmpty(t *testing.T) {
	// No PC bound to the session login: stable empty shape, 200 — same
	// posture as pc/me's exists=false, so the modal renders no rows rather
	// than erroring.
	srv := NewServer(seededWorld(t), okAuth{})

	rec := get(t, srv, "/api/village/pc/quotes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "{\"quotes\":[]}\n" && got != "{\"quotes\":[]}" {
		t.Errorf("body = %q, want {\"quotes\":[]}", got)
	}
}
