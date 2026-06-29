package perception

import (
	"strings"
	"testing"
)

// llm178_test.go — LLM-178. The perception must name the concrete rest action
// (move_to a place, or take_break in place); it must never invite a bare rest()
// verb, which isn't a registered tool and dead-ends as unknown_tool. A tired
// Ezekiel Crane read "## How you can rest" + the "(such as moving or resting)"
// coda and called rest(), getting `unknown_tool`.

// TestRestMenuNamesMoveToForPlaces is the LLM-178 cross-scenario invariant: any
// "## How you can rest" menu that lists a place (a structure_id bullet) must name
// move_to as the verb to act on that id. The menu used to dangle structure_ids
// with no verb, so a tired model reached for a rest()/sleep() that isn't registered.
func TestRestMenuNamesMoveToForPlaces(t *testing.T) {
	const header = "## How you can rest"
	var sawPlaces bool
	for _, sc := range perceptionScenarios {
		out := renderScenario(sc)
		idx := strings.Index(out, header)
		if idx < 0 {
			continue
		}
		// Bound to the rest section: header → the trailing blank line the menu
		// renderer always emits, so a move_to elsewhere in the prompt can't satisfy it.
		section := out[idx:]
		if end := strings.Index(section, "\n\n"); end >= 0 {
			section = section[:end]
		}
		if !strings.Contains(section, "(structure_id:") {
			continue // a menu with no place bullet (e.g. own-stock only) — no move_to needed
		}
		sawPlaces = true
		if !strings.Contains(section, "move_to") {
			t.Errorf("scenario %q: rest menu lists a place but never names move_to:\n%s", sc.name, section)
		}
	}
	if !sawPlaces {
		t.Error("matrix must exercise a rest menu with at least one place (structure_id) bullet")
	}
}

// TestContinuationCodaNamesNoBareRestVerb guards the post-speak decision coda: it
// must frame moving as the action and must not name a bare "resting" verb. The old
// "(such as moving or resting)" wording drove tired NPCs to call an unregistered
// rest() (Ezekiel at the Inn, LLM-178).
func TestContinuationCodaNamesNoBareRestVerb(t *testing.T) {
	if strings.Contains(continuationDecisionText, "or resting") {
		t.Errorf("continuation coda invites a bare rest verb: %q", continuationDecisionText)
	}
	if !strings.Contains(continuationDecisionText, "moving to where you can rest") {
		t.Errorf("continuation coda should frame moving as the rest action: %q", continuationDecisionText)
	}
}
