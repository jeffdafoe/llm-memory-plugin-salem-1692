package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_satisfies_test.go — handler coverage for the item-satiation write
// control route (LLM-119). The durable item_satisfies write is covered against
// real pg in repo/pg/refdata_integration_test.go; here a fake SatisfiesWriter
// stands in so the tests assert the HTTP plumbing: validation, item-kind
// resolution, the write-then-update ordering, and status mapping.

// fakeSatisfiesWriter records the upserted entry, or returns a forced error.
type fakeSatisfiesWriter struct {
	kind   sim.ItemKind
	attr   sim.NeedKey
	amount int
	calls  int
	err    error
}

func (f *fakeSatisfiesWriter) UpsertItemSatisfies(_ context.Context, kind sim.ItemKind, attribute sim.NeedKey, amount int) error {
	if f.err != nil {
		return f.err
	}
	f.kind, f.attr, f.amount = kind, attribute, amount
	f.calls++
	return nil
}

// satisfiesServer builds a control-enabled server with the given writer (nil =
// leave it unwired) and a catalog of berry (food, eases hunger) + milk (a
// material with no satiation).
func satisfiesServer(t *testing.T, writer SatisfiesWriter) (*Server, http.Handler) {
	t.Helper()
	srv, h := controlServer(t, operatorPerms)
	if writer != nil {
		srv.SetSatisfiesWriter(writer)
	}
	_, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
			"berry": {Name: "berry", DisplayLabel: "Berry", Category: sim.ItemCategoryFood,
				Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 2}}},
			"milk": {Name: "milk", DisplayLabel: "Milk", Category: sim.ItemCategoryMaterial},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	return srv, h
}

func TestUmbilicalSetSatisfies_EditAndAdd(t *testing.T) {
	fake := &fakeSatisfiesWriter{}
	srv, h := satisfiesServer(t, fake)

	// Edit the existing hunger entry (case-insensitive item resolution).
	rec := postReq(t, h, "/api/village/umbilical/item/set-satisfies", "tok",
		`{"item":"Berry","attribute":"hunger","amount":5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalSatisfiesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Item != "berry" || out.Attribute != "hunger" || out.Amount != 5 {
		t.Fatalf("response = %+v, want berry/hunger/5", out)
	}
	if fake.calls != 1 || fake.kind != "berry" || fake.attr != "hunger" || fake.amount != 5 {
		t.Fatalf("writer not called as expected: %+v", fake)
	}
	// The live catalog reflects the new immediate magnitude.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ItemKinds["berry"].Satisfies[0].Immediate, nil
	}})
	if mag, _ := res.(int); mag != 5 {
		t.Errorf("live berry hunger magnitude = %d, want 5", mag)
	}

	// Add a brand-new attribute (thirst) — appends, doesn't replace hunger.
	rec = postReq(t, h, "/api/village/umbilical/item/set-satisfies", "tok",
		`{"item":"berry","attribute":"thirst","amount":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	res, _ = srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return len(world.ItemKinds["berry"].Satisfies), nil
	}})
	if n, _ := res.(int); n != 2 {
		t.Errorf("berry satisfies count = %d, want 2 (hunger + thirst)", n)
	}
}

func TestUmbilicalSetSatisfies_Validation(t *testing.T) {
	_, h := satisfiesServer(t, &fakeSatisfiesWriter{})
	cases := []struct{ name, body string }{
		{"missing item", `{"attribute":"hunger","amount":2}`},
		{"missing attribute", `{"item":"berry","amount":2}`},
		{"zero amount", `{"item":"berry","attribute":"hunger","amount":0}`},
		{"negative amount", `{"item":"berry","attribute":"hunger","amount":-3}`},
		{"unknown attribute", `{"item":"berry","attribute":"joy","amount":2}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postReq(t, h, "/api/village/umbilical/item/set-satisfies", "tok", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400; body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUmbilicalSetSatisfies_UnknownItem(t *testing.T) {
	_, h := satisfiesServer(t, &fakeSatisfiesWriter{})
	rec := postReq(t, h, "/api/village/umbilical/item/set-satisfies", "tok",
		`{"item":"dragonfruit","attribute":"hunger","amount":2}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown item = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUmbilicalSetSatisfies_Unwired(t *testing.T) {
	_, h := satisfiesServer(t, nil) // writer not wired
	rec := postReq(t, h, "/api/village/umbilical/item/set-satisfies", "tok",
		`{"item":"berry","attribute":"hunger","amount":2}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUmbilicalSetSatisfies_WriterError(t *testing.T) {
	_, h := satisfiesServer(t, &fakeSatisfiesWriter{err: errors.New("db down")})
	rec := postReq(t, h, "/api/village/umbilical/item/set-satisfies", "tok",
		`{"item":"berry","attribute":"hunger","amount":2}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("writer error = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}
