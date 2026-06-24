package sim_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// restock_commands_test.go — sim-level coverage of the live restock-policy
// editing commands (LLM-95): SetRestockEntry / RemoveRestockEntry mutate the
// actor's attribute params and re-project RestockPolicy, plus RebuildRestockPolicy
// union parity.

// buildRestockTestWorld seeds a catalog (cheese has a recipe; ale/milk don't)
// and a roster covering every edit case: a plain worker (empty attribute, the
// append target), a vendor already stocking ale (update target), a keeper whose
// attribute carries sibling params (flavor — preservation), an attribute-less
// NPC, and a PC.
func buildRestockTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"cheese": {Name: "cheese", DisplayLabel: "Cheese"},
		"ale":    {Name: "ale", DisplayLabel: "Ale"},
		"milk":   {Name: "milk", DisplayLabel: "Milk"},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"cheese": {OutputItem: "cheese", OutputQty: 1, RateQty: 1, RatePerHours: 1},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	// Insert the roster via a post-load command so Attributes are populated on
	// the live world goroutine (the mem seed path isn't relied on for raw
	// attribute bytes).
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["ezekiel"] = &sim.Actor{
			ID: "ezekiel", DisplayName: "Ezekiel", Kind: sim.KindNPCShared, State: sim.StateIdle,
			Attributes: map[string][]byte{"worker": []byte("{}")},
		}
		world.Actors["prudence"] = &sim.Actor{
			ID: "prudence", DisplayName: "Prudence", Kind: sim.KindNPCShared, State: sim.StateIdle,
			Attributes: map[string][]byte{
				"vendor": []byte(`{"restock":[{"item":"ale","source":"buy","max":20}]}`),
			},
		}
		world.Actors["keeper"] = &sim.Actor{
			ID: "keeper", DisplayName: "Keeper", Kind: sim.KindNPCShared, State: sim.StateIdle,
			Attributes: map[string][]byte{"businessowner": []byte(`{"flavor":"warm"}`)},
		}
		world.Actors["noattr"] = &sim.Actor{
			ID: "noattr", DisplayName: "Noattr", Kind: sim.KindNPCShared, State: sim.StateIdle,
			Attributes: map[string][]byte{},
		}
		world.Actors["pc"] = &sim.Actor{
			ID: "pc", DisplayName: "Player", Kind: sim.KindPC, State: sim.StateIdle,
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed roster: %v", err)
	}
	return w, func() { cancel(); <-done }
}

// actorAttr reads one actor's raw attribute params back off the live world.
func actorAttr(t *testing.T, w *sim.World, id sim.ActorID, slug string) []byte {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil {
			return []byte(nil), nil
		}
		return a.Attributes[slug], nil
	}})
	if err != nil {
		t.Fatalf("read attr: %v", err)
	}
	b, _ := res.([]byte)
	return b
}

// restockEntries reads one actor's projected RestockPolicy entries.
func restockEntries(t *testing.T, w *sim.World, id sim.ActorID) []sim.RestockEntry {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil || a.RestockPolicy == nil {
			return []sim.RestockEntry(nil), nil
		}
		return append([]sim.RestockEntry(nil), a.RestockPolicy.Restock...), nil
	}})
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	e, _ := res.([]sim.RestockEntry)
	return e
}

func TestSetRestockEntry_AddProduce(t *testing.T) {
	w, stop := buildRestockTestWorld(t)
	defer stop()

	res, err := w.Send(sim.SetRestockEntry("ezekiel", "cheese", sim.RestockSourceProduce, 12))
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	out, ok := res.(sim.RestockPolicyResult)
	if !ok {
		t.Fatalf("result type %T", res)
	}
	if len(out.Entries) != 1 || out.Entries[0].Item != "cheese" ||
		out.Entries[0].Source != sim.RestockSourceProduce || out.Entries[0].Cap() != 12 {
		t.Fatalf("entries = %+v, want [cheese produce cap 12]", out.Entries)
	}
	// The projection on the live actor reflects it too.
	if got := restockEntries(t, w, "ezekiel"); len(got) != 1 || got[0].Item != "cheese" {
		t.Errorf("live policy = %+v, want one cheese entry", got)
	}
	// The attribute params blob now carries the entry (durable shape).
	var params restockBlob
	if err := json.Unmarshal(actorAttr(t, w, "ezekiel", "worker"), &params); err != nil {
		t.Fatalf("attr params unparseable: %v", err)
	}
	if len(params.Restock) != 1 || params.Restock[0].Item != "cheese" || params.Restock[0].Max != 12 {
		t.Errorf("worker params restock = %+v, want one cheese max 12", params.Restock)
	}
}

func TestSetRestockEntry_ProduceRequiresRecipe(t *testing.T) {
	w, stop := buildRestockTestWorld(t)
	defer stop()

	// ale is a real item but has no recipe — a produce entry would never fire.
	_, err := w.Send(sim.SetRestockEntry("ezekiel", "ale", sim.RestockSourceProduce, 5))
	if !errors.Is(err, sim.ErrNoRecipeForProduce) {
		t.Fatalf("err = %v, want ErrNoRecipeForProduce", err)
	}
	// A buy entry for the same recipe-less item is fine.
	if _, err := w.Send(sim.SetRestockEntry("ezekiel", "ale", sim.RestockSourceBuy, 5)); err != nil {
		t.Fatalf("buy ale: %v", err)
	}
}

func TestSetRestockEntry_UpdateExisting(t *testing.T) {
	w, stop := buildRestockTestWorld(t)
	defer stop()

	// prudence already stocks ale (buy, cap 20) on her vendor attribute. Update
	// the cap to 30 — it must land in place, not append a second ale.
	res, err := w.Send(sim.SetRestockEntry("prudence", "ale", sim.RestockSourceBuy, 30))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	out := res.(sim.RestockPolicyResult)
	if len(out.Entries) != 1 || out.Entries[0].Item != "ale" || out.Entries[0].Cap() != 30 {
		t.Fatalf("entries = %+v, want single ale cap 30", out.Entries)
	}
}

func TestSetRestockEntry_PreservesSiblingParams(t *testing.T) {
	w, stop := buildRestockTestWorld(t)
	defer stop()

	// keeper's only attribute is businessowner {flavor:warm}. Adding a restock
	// entry must not clobber the flavor sibling.
	if _, err := w.Send(sim.SetRestockEntry("keeper", "cheese", sim.RestockSourceProduce, 8)); err != nil {
		t.Fatalf("set: %v", err)
	}
	var params struct {
		Flavor  string       `json:"flavor"`
		Restock []restockRow `json:"restock"`
	}
	if err := json.Unmarshal(actorAttr(t, w, "keeper", "businessowner"), &params); err != nil {
		t.Fatalf("attr unparseable: %v", err)
	}
	if params.Flavor != "warm" {
		t.Errorf("flavor = %q, want warm (sibling clobbered)", params.Flavor)
	}
	if len(params.Restock) != 1 || params.Restock[0].Item != "cheese" {
		t.Errorf("restock = %+v, want one cheese entry", params.Restock)
	}
}

func TestSetRestockEntry_Rejections(t *testing.T) {
	w, stop := buildRestockTestWorld(t)
	defer stop()

	cases := []struct {
		name   string
		id     sim.ActorID
		item   string
		source sim.RestockSource
		want   error
	}{
		{"unknown item", "ezekiel", "dragonfruit", sim.RestockSourceBuy, sim.ErrUnknownItemKind},
		{"pc rejected", "pc", "cheese", sim.RestockSourceProduce, sim.ErrActorNotFound},
		{"unknown actor", "ghost", "cheese", sim.RestockSourceProduce, sim.ErrActorNotFound},
		{"bad source", "ezekiel", "cheese", sim.RestockSource("hoard"), sim.ErrInvalidRestockSource},
		{"no attribute", "noattr", "cheese", sim.RestockSourceProduce, sim.ErrNoAttributeForRestock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.Send(sim.SetRestockEntry(tc.id, tc.item, tc.source, 5))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRemoveRestockEntry(t *testing.T) {
	w, stop := buildRestockTestWorld(t)
	defer stop()

	res, err := w.Send(sim.RemoveRestockEntry("prudence", "ale"))
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out := res.(sim.RestockPolicyResult); len(out.Entries) != 0 {
		t.Fatalf("entries = %+v, want empty after removing the only entry", out.Entries)
	}
	if got := restockEntries(t, w, "prudence"); len(got) != 0 {
		t.Errorf("live policy = %+v, want empty", got)
	}
	// Removing a non-stocked item is a 404-worthy not-found.
	if _, err := w.Send(sim.RemoveRestockEntry("prudence", "ale")); !errors.Is(err, sim.ErrRestockEntryNotFound) {
		t.Fatalf("second remove err = %v, want ErrRestockEntryNotFound", err)
	}
}

// TestRebuildRestockPolicy_Union covers the projection: entries union across
// attributes in sorted-slug order, first-listed-wins on item ties, unknown
// sources skipped.
func TestRebuildRestockPolicy_Union(t *testing.T) {
	a := &sim.Actor{
		ID:   "x",
		Kind: sim.KindNPCShared,
		Attributes: map[string][]byte{
			// "aaa" sorts first → its ale entry (cap 5) wins the tie over "bbb"'s.
			"aaa": []byte(`{"restock":[{"item":"ale","source":"buy","max":5}]}`),
			"bbb": []byte(`{"restock":[{"item":"ale","source":"buy","max":99},{"item":"milk","source":"forage","max":7},{"item":"junk","source":"bogus","max":1}]}`),
		},
	}
	sim.RebuildRestockPolicy(a)
	if a.RestockPolicy == nil {
		t.Fatal("policy nil, want entries")
	}
	caps := map[sim.ItemKind]int{}
	for _, e := range a.RestockPolicy.Restock {
		caps[e.Item] = e.Cap()
	}
	if caps["ale"] != 5 {
		t.Errorf("ale cap = %d, want 5 (first-slug wins)", caps["ale"])
	}
	if caps["milk"] != 7 {
		t.Errorf("milk cap = %d, want 7", caps["milk"])
	}
	if _, ok := caps["junk"]; ok {
		t.Errorf("junk (unknown source) projected, want skipped")
	}
}

// restockBlob / restockRow decode an attribute params blob's restock array in
// tests (the unexported sim DTOs aren't visible from the external test package).
type restockBlob struct {
	Restock []restockRow `json:"restock"`
}

type restockRow struct {
	Item   string `json:"item"`
	Source string `json:"source"`
	Max    int    `json:"max"`
	Target int    `json:"target"`
}
