package handlers

import "testing"

// register_stoke_repair_terminal_test.go — LLM-443. Stoke and repair are the two
// timed source-activity starts that were left non-terminal after LLM-175 flipped
// their sibling gather to terminal-on-success. Like gather, a started stoke/repair
// opens a window that a second same-tick call bounces ("already busy") and a move
// abandons — so ending the tick is the fix for the within-tick re-fire storm (the
// live john-ellis stoke loop). These pin the TerminalOnSuccess policy so a regression
// that flips either back to non-terminal fails here, mirroring
// TestRegisterGather_IsTerminalOnSuccess.

func TestRegisterStoke_IsTerminalOnSuccess(t *testing.T) {
	r := NewRegistry()
	if err := RegisterStoke(r); err != nil {
		t.Fatalf("RegisterStoke: %v", err)
	}
	e, ok := r.Lookup("stoke")
	if !ok {
		t.Fatal("stoke not registered")
	}
	if e.Class != ClassCommit {
		t.Errorf("stoke Class = %v, want ClassCommit", e.Class)
	}
	if e.TerminalPolicy != TerminalOnSuccess {
		t.Errorf("stoke TerminalPolicy = %v, want TerminalOnSuccess — a started stoke must end the tick (LLM-443)", e.TerminalPolicy)
	}
}

func TestRegisterRepair_IsTerminalOnSuccess(t *testing.T) {
	r := NewRegistry()
	if err := RegisterRepair(r); err != nil {
		t.Fatalf("RegisterRepair: %v", err)
	}
	e, ok := r.Lookup("repair")
	if !ok {
		t.Fatal("repair not registered")
	}
	if e.Class != ClassCommit {
		t.Errorf("repair Class = %v, want ClassCommit", e.Class)
	}
	if e.TerminalPolicy != TerminalOnSuccess {
		t.Errorf("repair TerminalPolicy = %v, want TerminalOnSuccess — a started repair must end the tick (LLM-443)", e.TerminalPolicy)
	}
}
