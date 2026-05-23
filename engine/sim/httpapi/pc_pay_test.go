package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// payWorld stands up a running mem-backed world with a login-bound PC buyer and
// an NPC seller co-present in a huddle anchored to a scene — the minimal state
// sim.PayWithItem needs to mint a slow-path offer. Item kinds are seeded so the
// fast/accept paths (not exercised here) would have a catalog; the slow-path
// mint itself doesn't require the item to exist (AcceptPay revalidates that
// later). Mirrors buildPayWithItemWorld in the sim package, scoped to what the
// httpapi plumbing tests need.
func payWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	// Seed the actors (both in huddle "h1") through the repo BEFORE LoadWorld
	// so LoadWorld's index build populates actorsByHuddle["h1"] from their
	// CurrentHuddleID — that's the index findHuddlePeerByDisplayName reads. The
	// sim-package test that adds a huddle post-load uses an unexported rebuild
	// helper not visible here; seeding pre-load avoids needing it.
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"pc-buyer": {
			ID: "pc-buyer", DisplayName: "Buyer", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: "tester",
			CurrentX: 3, CurrentY: 4, CurrentHuddleID: "h1", Coins: 100,
		},
		"hannah": {
			ID: "hannah", DisplayName: "Hannah", Kind: sim.KindNPCShared,
			State: sim.StateIdle, Role: "innkeeper", LLMAgent: "hannah-va",
			CurrentX: 3, CurrentY: 4, CurrentHuddleID: "h1",
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

	// Add the huddle aggregate + a scene anchoring it. resolveSellerScene
	// iterates world.Scenes directly (no index), so a post-load Send is enough.
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles["h1"] = &sim.Huddle{
			ID:      "h1",
			Members: map[sim.ActorID]struct{}{"pc-buyer": {}, "hannah": {}},
		}
		world.Scenes["sc1"] = &sim.Scene{
			ID:      "sc1",
			Bound:   sim.NewUnboundedBound(),
			Huddles: map[sim.HuddleID]struct{}{"h1": {}},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed pay world: %v", err)
	}
	return w
}

func TestHandlePCPay_SlowPathPending(t *testing.T) {
	srv := NewServer(payWorld(t), okAuth{})

	rec := post(t, srv, "/api/village/pc/pay",
		`{"seller":"Hannah","item":"stew","qty":1,"amount":2,"consume_now":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res pcPayResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.LedgerID == 0 {
		t.Errorf("ledger_id = 0, want a minted entry id")
	}
	if res.State != string(sim.PayLedgerStatePending) {
		t.Errorf("state = %q, want %q", res.State, sim.PayLedgerStatePending)
	}
	if res.FastPath {
		t.Errorf("fast_path = true, want false on a slow-path (no quote) offer")
	}
}

func TestHandlePCPay_PCNotFound(t *testing.T) {
	// The base seeded world has no PC bound to login "tester", so the session
	// resolves to no PC.
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/pay",
		`{"seller":"Hannah","item":"stew","qty":1,"amount":2}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCPay_NotInHuddle(t *testing.T) {
	// A login-bound PC with no current huddle: the request is well-formed but
	// sim.PayWithItem rejects (no conversation) → 422.
	w := seededWorld(t)
	seedPC(t, w, "pc-tester", "tester", 10, 10)
	srv := NewServer(w, okAuth{})
	rec := post(t, srv, "/api/village/pc/pay",
		`{"seller":"Hannah","item":"stew","qty":1,"amount":2}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCPay_MalformedBody(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/pay", `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCPay_TrailingContent(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	rec := post(t, srv, "/api/village/pc/pay",
		`{"seller":"Hannah","item":"stew","qty":1,"amount":2} garbage`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCPay_FieldValidation(t *testing.T) {
	srv := NewServer(seededWorld(t), okAuth{})
	cases := map[string]string{
		"missing seller":                    `{"seller":"  ","item":"stew","qty":1,"amount":2}`,
		"missing item":                      `{"seller":"Hannah","item":"","qty":1,"amount":2}`,
		"amount below 1":                    `{"seller":"Hannah","item":"stew","qty":1,"amount":0}`,
		"qty below 1":                       `{"seller":"Hannah","item":"stew","qty":0,"amount":2}`,
		"dup consumer":                      `{"seller":"Hannah","item":"stew","qty":1,"amount":2,"consume_now":true,"consumers":["Ann","ann"]}`,
		"quote and in_response_to both set": `{"seller":"Hannah","item":"stew","qty":1,"amount":2,"quote_id":7,"in_response_to":42}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(t, srv, "/api/village/pc/pay", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
