package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// TestUmbilical_Agent_RestockAndProduceState covers the LLM-111 additions to the
// /agent view: a producer actor returns its restock entries (all three sources,
// in policy order) plus its per-item produce anchors (sorted by item), while an
// actor with no policy omits both fields entirely.
func TestUmbilical_Agent_RestockAndProduceState(t *testing.T) {
	w := seededWorld(t)
	stewAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	breadAt := time.Date(2026, 6, 25, 9, 30, 0, 0, time.UTC)
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["hannah"]
		a.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 8},
			{Item: "bread", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "ale", Source: sim.RestockSourceBuy, Target: 20}, // legacy Target alias
			{Item: "raspberries", Source: sim.RestockSourceForage, Max: 10},
		}}
		// Seeded out of item order to exercise the produce_state sort.
		a.ProduceState = map[sim.ItemKind]*sim.ProduceState{
			"stew":  {Item: "stew", LastProducedAt: stewAt},
			"bread": {Item: "bread", LastProducedAt: breadAt},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	// Producer actor: both fields populated.
	rec := req(t, h, "/api/village/umbilical/agent?id=hannah", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("agent = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalAgentDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// restock_policy preserves the policy slice order and carries every source;
	// Cap() resolves Max, falling back to the legacy Target alias (ale).
	if len(out.RestockPolicy) != 4 {
		t.Fatalf("restock_policy = %+v, want 4 entries", out.RestockPolicy)
	}
	if out.RestockPolicy[0].Item != "stew" || out.RestockPolicy[0].Source != "produce" || out.RestockPolicy[0].Cap != 8 {
		t.Errorf("entry[0] = %+v, want stew/produce/8", out.RestockPolicy[0])
	}
	if out.RestockPolicy[2].Item != "ale" || out.RestockPolicy[2].Source != "buy" || out.RestockPolicy[2].Cap != 20 {
		t.Errorf("entry[2] = %+v, want ale/buy/20 (legacy Target)", out.RestockPolicy[2])
	}
	if out.RestockPolicy[3].Item != "raspberries" || out.RestockPolicy[3].Source != "forage" || out.RestockPolicy[3].Cap != 10 {
		t.Errorf("entry[3] = %+v, want raspberries/forage/10", out.RestockPolicy[3])
	}

	// produce_state is sorted by item (bread < stew) regardless of map order.
	if len(out.ProduceState) != 2 {
		t.Fatalf("produce_state = %+v, want 2 anchors", out.ProduceState)
	}
	if out.ProduceState[0].Item != "bread" || !out.ProduceState[0].LastProducedAt.Equal(breadAt) {
		t.Errorf("anchor[0] = %+v, want bread @ %s", out.ProduceState[0], breadAt)
	}
	if out.ProduceState[1].Item != "stew" || !out.ProduceState[1].LastProducedAt.Equal(stewAt) {
		t.Errorf("anchor[1] = %+v, want stew @ %s", out.ProduceState[1], stewAt)
	}

	// Actor with no policy: both fields empty and omitted from the wire.
	rec = req(t, h, "/api/village/umbilical/agent?id=bram", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("agent bram = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var bram UmbilicalAgentDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &bram); err != nil {
		t.Fatalf("decode bram: %v", err)
	}
	if len(bram.RestockPolicy) != 0 || len(bram.ProduceState) != 0 {
		t.Errorf("bram restock=%+v produce=%+v, want both empty", bram.RestockPolicy, bram.ProduceState)
	}
	if raw := rec.Body.String(); strings.Contains(raw, "restock_policy") || strings.Contains(raw, "produce_state") {
		t.Errorf("bram payload should omit restock_policy/produce_state via omitempty: %s", raw)
	}
}
