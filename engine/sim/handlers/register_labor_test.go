package handlers

import "testing"

// register_labor_test.go — LLM-180 / LLM-184. The whole labor family is
// tick-terminal: a placed labor offer (solicit_work, LLM-180), an accept, and a
// decline each end the tick, so the within-tick re-fire loop (a weak model
// calling the verb x6 to the round budget, observed live) cannot recur. This
// pins the TerminalOnSuccess policy so a regression that flips any of them back
// to non-terminal fails here. LLM-184 converted accept_work / decline_work from
// non-terminal to terminal — the answering side announces BEFORE answering
// (speak is non-terminal) and the courtesy word AFTER was the re-fire vector.
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

func TestRegisterLaborFamily_AcceptAndDeclineAreTerminal(t *testing.T) {
	r := NewRegistry()
	if err := RegisterLaborFamily(r); err != nil {
		t.Fatalf("RegisterLaborFamily: %v", err)
	}
	for _, name := range []string{"accept_work", "decline_work"} {
		e, ok := r.Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if e.TerminalPolicy != TerminalOnSuccess {
			t.Errorf("%s TerminalPolicy = %v, want TerminalOnSuccess — an answer ends the tick (LLM-184); a forced second answer is the storm", name, e.TerminalPolicy)
		}
	}
}
