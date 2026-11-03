package httpapi

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// lantern_wire_test.go — LLM-846: the lantern on /agents, the
// npc_lantern_changed frame, and the part-burned candle on the pack row.

var lanternWireNow = time.Date(2026, 11, 3, 21, 0, 0, 0, time.UTC)

// A client that connects late sees a light already burning; an out or burned-out
// lantern omits the key.
func TestAgentsFromSnapshot_LanternLitUntil(t *testing.T) {
	litUntil := lanternWireNow.Add(90 * time.Minute)
	snap := &sim.Snapshot{PublishedAt: lanternWireNow, Actors: map[sim.ActorID]*sim.ActorSnapshot{
		"lit":    {Kind: sim.KindPC, LanternLitUntil: litUntil},
		"burned": {Kind: sim.KindPC, LanternLitUntil: lanternWireNow.Add(-time.Minute)},
		"never":  {Kind: sim.KindPC},
	}}
	byID := map[string]AgentDTO{}
	for _, a := range agentsFromSnapshot(snap, nil) {
		byID[a.ID] = a
	}
	if got := byID["lit"].LanternLitUntil; got != litUntil.Format(time.RFC3339) {
		t.Errorf("lit lantern_lit_until = %q, want %s", got, litUntil.Format(time.RFC3339))
	}
	for _, id := range []string{"burned", "never"} {
		if got := byID[id].LanternLitUntil; got != "" {
			t.Errorf("%s lantern_lit_until = %q, want omitted", id, got)
		}
	}
	b, err := json.Marshal(byID["never"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "lantern_lit_until") {
		t.Errorf("unlit agent json carries lantern_lit_until: %s", b)
	}
}

// Lit carries lit_until; put out omits it.
func TestTranslateEvent_LanternChanged(t *testing.T) {
	litUntil := lanternWireNow.Add(3 * time.Hour)
	frame, ok := TranslateEvent(&sim.LanternChanged{ActorID: "pc-1", LitUntil: litUntil, At: lanternWireNow})
	if !ok || frame.Type != "npc_lantern_changed" {
		t.Fatalf("lit: frame = %+v (sent %v), want npc_lantern_changed", frame, ok)
	}
	want := lanternChangedWireDTO{ID: "pc-1", LitUntil: litUntil.Format(time.RFC3339), At: lanternWireNow.Format(time.RFC3339)}
	if d := frame.Data.(lanternChangedWireDTO); d != want {
		t.Errorf("lit payload = %+v, want %+v", d, want)
	}
	b, err := json.Marshal(frame.Data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"id"`, `"lit_until"`, `"at"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("lit json missing %s: %s", key, b)
		}
	}

	frame, ok = TranslateEvent(&sim.LanternChanged{ActorID: "pc-1", At: lanternWireNow})
	if !ok || frame.Type != "npc_lantern_changed" {
		t.Fatalf("out: frame = %+v (sent %v), want npc_lantern_changed", frame, ok)
	}
	b, err = json.Marshal(frame.Data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "lit_until") {
		t.Errorf("out json carries lit_until: %s", b)
	}
}

// The lantern row offers Light or Put out and shows a part-burned candle,
// rounded up; the candle row shows neither.
func TestPCInventoryEntries_Lantern(t *testing.T) {
	catalog := map[sim.ItemKind]*sim.ItemKindDef{
		sim.HornLanternKind:  {Name: sim.HornLanternKind, Capabilities: []string{"portable", sim.CapabilityLight}},
		sim.TallowCandleKind: {Name: sim.TallowCandleKind, Capabilities: []string{"portable"}},
	}
	inv := map[sim.ItemKind]int{sim.HornLanternKind: 1, sim.TallowCandleKind: 2}
	rows := func(st sim.PackState) map[string]pcInventoryEntry {
		out := map[string]pcInventoryEntry{}
		for _, e := range pcInventoryEntries(st, catalog, lanternWireNow) {
			out[e.ItemKind] = e
		}
		return out
	}

	got := rows(sim.PackState{Inventory: inv, LanternCandleLeft: 90*time.Minute + 10*time.Second})
	lantern, candle := got[string(sim.HornLanternKind)], got[string(sim.TallowCandleKind)]
	if !slices.Equal(lantern.Actions, []string{sim.ItemActionLight}) || lantern.CandleLeftMinutes != 91 {
		t.Errorf("unlit lantern row = %+v, want Light and 91 minutes left", lantern)
	}
	if len(candle.Actions) != 0 || candle.CandleLeftMinutes != 0 {
		t.Errorf("candle row = %+v, want no actions and no stub", candle)
	}

	got = rows(sim.PackState{Inventory: inv, LanternLitUntil: lanternWireNow.Add(time.Hour)})
	if lantern := got[string(sim.HornLanternKind)]; !slices.Equal(lantern.Actions, []string{sim.ItemActionPutOut}) || lantern.CandleLeftMinutes != 0 {
		t.Errorf("lit lantern row = %+v, want Put out and no stub", lantern)
	}
}
