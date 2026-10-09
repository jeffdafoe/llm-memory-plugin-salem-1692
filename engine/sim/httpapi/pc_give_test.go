package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// giveWorld stands up a running mem-backed world with two login-bound PCs and
// an NPC keeper co-huddled in "h1", anchored to scene "sc1". The payer is the
// "tester" session's PC (okAuth). Actors are seeded before LoadWorld so the
// huddle-membership index sim.Pay's recipient resolver reads is built.
func giveWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"pc-giver": {
			ID: "pc-giver", DisplayName: "Giver", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "tester",
			Pos: sim.TilePos{X: 3, Y: 4}, CurrentHuddleID: "h1", Coins: 20,
		},
		"pc-friend": {
			ID: "pc-friend", DisplayName: "Friend", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "friend",
			Pos: sim.TilePos{X: 3, Y: 4}, CurrentHuddleID: "h1", Coins: 1,
		},
		"hannah": {
			ID: "hannah", DisplayName: "Hannah Boggs", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Role: "innkeeper", LLMAgent: "hannah-va",
			Pos: sim.TilePos{X: 3, Y: 4}, CurrentHuddleID: "h1", Coins: 5,
			Inventory: map[sim.ItemKind]int{"stew": 5},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles["h1"] = &sim.Huddle{
			ID:      "h1",
			Members: map[sim.ActorID]struct{}{"pc-giver": {}, "pc-friend": {}, "hannah": {}},
		}
		world.Scenes["sc1"] = &sim.Scene{
			ID: "sc1", Bound: sim.NewUnboundedBound(),
			Huddles: map[sim.HuddleID]struct{}{"h1": {}},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed give world: %v", err)
	}
	return w
}

func giveCoins(t *testing.T, w *sim.World) map[sim.ActorID]int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		out := map[sim.ActorID]int{}
		for id, a := range world.Actors {
			out[id] = a.Coins
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("read coins: %v", err)
	}
	return res.(map[sim.ActorID]int)
}

func TestHandlePCGive_ToNPC(t *testing.T) {
	w := giveWorld(t)
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/pc/give", `{"recipient":"Hannah Boggs","amount":4,"for":"your kindness"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res pcGiveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Recipient != "Hannah Boggs" || res.Amount != 4 || res.Coins != 16 {
		t.Errorf("response = %+v, want {Hannah Boggs 4 16}", res)
	}
	coins := giveCoins(t, w)
	if coins["pc-giver"] != 16 || coins["hannah"] != 9 {
		t.Errorf("coins giver=%d hannah=%d, want 16 and 9", coins["pc-giver"], coins["hannah"])
	}
}

// TestHandlePCGive_PCToPC — the ticket's player-to-player case: the recipient
// is another PC in the same conversation.
func TestHandlePCGive_PCToPC(t *testing.T) {
	w := giveWorld(t)
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/pc/give", `{"recipient":"Friend","amount":5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	coins := giveCoins(t, w)
	if coins["pc-giver"] != 15 || coins["pc-friend"] != 6 {
		t.Errorf("coins giver=%d friend=%d, want 15 and 6", coins["pc-giver"], coins["pc-friend"])
	}
}

// TestHandlePCGive_PayerIsTheSession — PC-only by construction: the body has no
// payer field, so naming someone else as payer is ignored and the coins still
// leave the session's own PC.
func TestHandlePCGive_PayerIsTheSession(t *testing.T) {
	w := giveWorld(t)
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/pc/give", `{"payer":"hannah","buyer":"hannah","recipient":"Friend","amount":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	coins := giveCoins(t, w)
	if coins["hannah"] != 5 || coins["pc-giver"] != 18 {
		t.Errorf("coins hannah=%d giver=%d, want 5 (untouched) and 18", coins["hannah"], coins["pc-giver"])
	}
}

// TestHandlePCGive_OpenQuoteRefused — the LLM-172 guard, worded for a player:
// a gift whose "for" names a good the recipient is offering points at the
// offer card and moves nothing.
func TestHandlePCGive_OpenQuoteRefused(t *testing.T) {
	w := giveWorld(t)
	now := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Quotes = map[sim.QuoteID]*sim.SceneQuote{
			7: {ID: 7, SceneID: "sc1", SellerID: "hannah",
				Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 3, ConsumeNow: true,
				State: sim.SceneQuoteStateActive, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed quote: %v", err)
	}
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/pc/give", `{"recipient":"Hannah Boggs","amount":3,"for":"stew"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "offer card") || strings.Contains(body, "pay_with_item") || strings.Contains(body, "quote_id") {
		t.Errorf("refusal %s should point at the offer card and name no tool or quote id", body)
	}
	if coins := giveCoins(t, w); coins["pc-giver"] != 20 {
		t.Errorf("giver coins = %d, want 20 (nothing moved)", coins["pc-giver"])
	}
}

func TestHandlePCGive_NotEnoughCoins(t *testing.T) {
	srv := NewServer(giveWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/give", `{"recipient":"Friend","amount":21}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "you have only 20 coins to spend") {
		t.Errorf("refusal = %s, want the player's purse named", rec.Body.String())
	}
}

func TestHandlePCGive_UnknownRecipient(t *testing.T) {
	srv := NewServer(giveWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/give", `{"recipient":"Nobody","amount":1}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCGive_PCNotFound(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/give", `{"recipient":"Hannah","amount":1}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCGive_BadRequests(t *testing.T) {
	srv := NewServer(giveWorld(t), okAuth{})
	cases := map[string]string{
		"malformed":        `{not json`,
		"trailing content": `{"recipient":"Friend","amount":1} junk`,
		"missing name":     `{"recipient":"  ","amount":1}`,
		"amount below 1":   `{"recipient":"Friend","amount":0}`,
		"for too long":     `{"recipient":"Friend","amount":1,"for":"` + strings.Repeat("a", maxPayForChars+1) + `"}`,
		"control char":     `{"recipient":"Fri\u0007end","amount":1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(t, srv, "/api/village/pc/give", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
