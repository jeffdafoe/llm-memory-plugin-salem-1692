package perception

import (
	"strings"
	"testing"
)

// TestEstateRateCollectorLineFollowsTheFloor pins the floor in the constable's line
// to Snapshot.EstateRateFloor (LLM-665). The goldens render the default, which a
// hard-coded 100 would also satisfy; a non-default figure proves the mirror is read.
func TestEstateRateCollectorLineFollowsTheFloor(t *testing.T) {
	snap, _, constableID := estateRateSnapshot(true)
	snap.EstateRateFloor = 37
	out := combinedPrompt(Render(Build(snap, constableID, nil), DefaultRenderConfig()))
	const want = "A keeper holding 37 coins or fewer owes nothing today."
	if !strings.Contains(out, want) {
		t.Errorf("collector line must carry the snapshot's floor.\nwant: %s\n--- got ---\n%s", want, out)
	}
	if strings.Contains(out, "100 coins") {
		t.Errorf("collector line still carries the default floor:\n%s", out)
	}
}
