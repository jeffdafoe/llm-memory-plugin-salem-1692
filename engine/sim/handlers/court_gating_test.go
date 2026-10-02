package handlers

import (
	"encoding/json"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// TestGateTools_BringBeforeMagistratesOnlyForAConstableUnderTheLimit (LLM-695)
// — the tool rides payload.Court.OffersFiling(), the same signal the
// "## The magistrates" section renders its tool line from.
func TestGateTools_BringBeforeMagistratesOnlyForAConstableUnderTheLimit(t *testing.T) {
	r := NewRegistry()
	if err := RegisterBringBeforeMagistrates(r); err != nil {
		t.Fatalf("RegisterBringBeforeMagistrates: %v", err)
	}
	cases := []struct {
		name string
		view *perception.CourtView
		want int
	}{
		{"not the constable", nil, 0},
		{"the constable, none brought today", &perception.CourtView{Limit: 2}, 1},
		{"the constable, one brought", &perception.CourtView{Limit: 2, FiledToday: 1}, 1},
		{"the constable at the limit", &perception.CourtView{Limit: 2, FiledToday: 2}, 0},
	}
	for _, c := range cases {
		got := specNameSet(gateTools(r, perception.Payload{ActorID: "gideon", Court: c.view}, nil))
		if got["bring_before_magistrates"] != c.want {
			t.Errorf("%s: count %d, want %d", c.name, got["bring_before_magistrates"], c.want)
		}
	}
}

func TestDecodeBringBeforeMagistratesArgs(t *testing.T) {
	ok := `{"parties":["Josiah Thorne"],"complaint":"the ledger was taken"}`
	if _, err := DecodeBringBeforeMagistratesArgs(json.RawMessage(ok)); err != nil {
		t.Fatalf("valid args refused: %v", err)
	}
	for name, raw := range map[string]string{
		"no parties":    `{"parties":[],"complaint":"x"}`,
		"no complaint":  `{"parties":["A"],"complaint":"  "}`,
		"five parties":  `{"parties":["A","B","C","D","E"],"complaint":"x"}`,
		"unknown field": `{"parties":["A"],"complaint":"x","verdict":"guilty"}`,
		"not an object": `["A"]`,
		"trailing data": ok + ` {}`,
	} {
		if _, err := DecodeBringBeforeMagistratesArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
}
