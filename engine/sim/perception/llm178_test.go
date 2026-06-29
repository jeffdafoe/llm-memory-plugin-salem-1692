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
// move_to as the verb to act on that id — unless every listed place is the
// on-shift home bed deferred to after the shift (LLM-62), where the verb is
// withheld in favor of "stay at your post until then". The menu used to dangle
// structure_ids with no verb, so a tired model reached for a rest()/sleep() that
// isn't registered.
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
		if !strings.Contains(section, "move_to") && !strings.Contains(section, "stay at your post until then") {
			t.Errorf("scenario %q: rest menu lists a place but names neither move_to nor a deferral:\n%s", sc.name, section)
		}
	}
	if !sawPlaces {
		t.Error("matrix must exercise a rest menu with at least one place (structure_id) bullet")
	}
}

// TestRestMenuMoveToLineRendersForActionablePlace: a menu with a place reachable
// now names move_to.
func TestRestMenuMoveToLineRendersForActionablePlace(t *testing.T) {
	var b strings.Builder
	renderRecoveryOptions(&b, &RecoveryOptionsView{
		Options: []RecoveryOption{
			{Kind: "inn", Label: "the Inn", CostText: "ask the keeper", StructureID: "inn1"},
		},
	})
	out := b.String()
	if !strings.Contains(out, "move_to") {
		t.Errorf("expected the move_to line for an actionable place:\n%s", out)
	}
}

// TestRestMenuMoveToLineSkipsAfterShiftOnlyHome guards the LLM-178/LLM-62 seam:
// when the only rest option is the on-shift home bed (deferred until shift-end),
// the "call move_to … now" line must NOT render — it would contradict the
// bullet's "stay at your post until then" directive (code_review).
func TestRestMenuMoveToLineSkipsAfterShiftOnlyHome(t *testing.T) {
	var b strings.Builder
	renderRecoveryOptions(&b, &RecoveryOptionsView{
		Options: []RecoveryOption{
			{Kind: "home", Label: "your home", CostText: "free", StructureID: "home1", AfterShiftOnly: true},
		},
	})
	out := b.String()
	if !strings.Contains(out, "## How you can rest") {
		t.Fatalf("expected the rest menu to render:\n%s", out)
	}
	if !strings.Contains(out, "stay at your post until then") {
		t.Errorf("expected the after-shift home bullet's stay-put directive:\n%s", out)
	}
	if strings.Contains(out, "move_to") {
		t.Errorf("after-shift-only menu must not render the move_to-now line:\n%s", out)
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
