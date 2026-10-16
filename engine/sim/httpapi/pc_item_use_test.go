package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// LLM-800: each pack row carries its item actions, and a pendant's flips
// between Wear and Remove with what the PC has on.
func TestPCInventoryEntriesActions(t *testing.T) {
	catalog := map[sim.ItemKind]*sim.ItemKindDef{
		"duck_pendant":  {Name: "duck_pendant", Capabilities: []string{sim.CapabilityWear}},
		"silver_locket": {Name: "silver_locket", Capabilities: []string{sim.CapabilityWear}},
		"bread":         {Name: "bread", Capabilities: []string{"portable"}},
	}
	inv := map[sim.ItemKind]int{"duck_pendant": 1, "silver_locket": 2, "bread": 3, "mystery": 1}
	got := pcInventoryEntries(inv, []sim.ItemKind{"duck_pendant"}, catalog)
	want := map[string][]string{
		"bread":         {},
		"duck_pendant":  {sim.ItemActionRemove},
		"mystery":       {},
		"silver_locket": {sim.ItemActionWear},
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %d, want %d", len(got), len(want))
	}
	for _, e := range got {
		if e.Actions == nil || !slices.Equal(e.Actions, want[e.ItemKind]) {
			t.Errorf("%s actions = %#v, want %v", e.ItemKind, e.Actions, want[e.ItemKind])
		}
	}
}

// A refusal is a 422 carrying the engine's own line for the pack.
func TestItemUseErrorResponse(t *testing.T) {
	status, msg := itemUseErrorResponse(errors.New("boom"))
	if status != http.StatusInternalServerError {
		t.Errorf("plain error: status %d, want 500", status)
	}
	if msg == "boom" {
		t.Errorf("plain error leaked its text: %q", msg)
	}
	if status, _ := itemUseErrorResponse(errPCNotFound); status != http.StatusNotFound {
		t.Errorf("no pc: status %d, want 404", status)
	}

	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{"p1": {ID: "p1", Inventory: map[sim.ItemKind]int{}}}
	_, refusal := sim.UseItem("p1", "duck_pendant", sim.ItemActionWear, time.Now()).Fn(w)
	status, msg = itemUseErrorResponse(refusal)
	if status != http.StatusUnprocessableEntity || msg != "You are not carrying that." {
		t.Errorf("refusal: %d %q, want 422 \"You are not carrying that.\"", status, msg)
	}
}
