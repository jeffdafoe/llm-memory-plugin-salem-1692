package handlers

import "testing"

// register_labor_test.go — LLM-180. solicit_work is tick-terminal: a placed
// labor offer ends the tick, so the within-tick re-fire loop (a weak model
// calling solicit_work x6 to the round budget, observed live) cannot recur.
// This pins the TerminalOnSuccess policy so a regression that flips it back to
// non-terminal fails here. accept_work / decline_work stay non-terminal (the
// employer/worker answers in-conversation and may chain a word) — pinned below
// so the family's deliberate asymmetry doesn't drift.
func TestRegisterSolicitWork_IsTerminalOnSuccess(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSolicitWork(r); err != nil {
		t.Fatalf("RegisterSolicitWork: %v", err)
	}
	e, ok := r.Lookup("solicit_work")
	if !ok {
		t.Fatal("solicit_work not registered")
	}
	if e.Class != ClassCommit {
		t.Errorf("solicit_work Class = %v, want ClassCommit", e.Class)
	}
	if e.TerminalPolicy != TerminalOnSuccess {
		t.Errorf("solicit_work TerminalPolicy = %v, want TerminalOnSuccess — a placed labor offer must end the tick (LLM-180)", e.TerminalPolicy)
	}
}

func TestRegisterLaborFamily_AcceptAndDeclineStayNonTerminal(t *testing.T) {
	r := NewRegistry()
	if err := RegisterLaborFamily(r); err != nil {
		t.Fatalf("RegisterLaborFamily: %v", err)
	}
	for _, name := range []string{"accept_work", "decline_work"} {
		e, ok := r.Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if e.TerminalPolicy != TerminalNever {
			t.Errorf("%s TerminalPolicy = %v, want TerminalNever — the answering side may chain a word", name, e.TerminalPolicy)
		}
	}
}
