package handlers

import "testing"

// register_gather_test.go — LLM-175. Gather is tick-terminal: a started pick ends
// the tick, so the within-tick re-fire loop (a weak model calling gather x6 to the
// round budget, observed live) cannot recur. This pins the TerminalOnSuccess
// policy so a regression that flips it back to non-terminal fails here.
func TestRegisterGather_IsTerminalOnSuccess(t *testing.T) {
	r := NewRegistry()
	if err := RegisterGather(r); err != nil {
		t.Fatalf("RegisterGather: %v", err)
	}
	e, ok := r.Lookup("gather")
	if !ok {
		t.Fatal("gather not registered")
	}
	if e.Class != ClassCommit {
		t.Errorf("gather Class = %v, want ClassCommit", e.Class)
	}
	if e.TerminalPolicy != TerminalOnSuccess {
		t.Errorf("gather TerminalPolicy = %v, want TerminalOnSuccess — a started pick must end the tick (LLM-175)", e.TerminalPolicy)
	}
}
