package sim

import "testing"

// sourceActivityWireLabel must resolve a repair's label the SAME DisplayName-first,
// structures-first way as httpapi.sourceActivityLabel (the AgentDTO snapshot) and
// perception.resolveDwellPinLabel (the LLM-440 line), so the live tooltip frame and
// a mid-window reconnect can't drift on a business name (LLM-441).
func TestSourceActivityWireLabel(t *testing.T) {
	w := &World{
		Structures: map[StructureID]*Structure{
			"market": {ID: "market", DisplayName: "Market"},
		},
		VillageObjects: map[VillageObjectID]*VillageObject{
			"stall": {ID: "stall", DisplayName: "Corner Stall"},
			"bare":  {ID: "bare"}, // no DisplayName override → place-less
		},
	}
	cases := []struct {
		name  string
		objID VillageObjectID
		want  string
	}{
		{"structure wins", "market", "Market"},
		{"village-object fallback", "stall", "Corner Stall"},
		{"empty id", "", ""},
		{"unresolvable id", "ghost", ""},
		{"resolved but unnamed is place-less", "bare", ""},
	}
	for _, c := range cases {
		if got := sourceActivityWireLabel(w, c.objID); got != c.want {
			t.Errorf("%s: sourceActivityWireLabel(%q) = %q, want %q", c.name, c.objID, got, c.want)
		}
	}
}
