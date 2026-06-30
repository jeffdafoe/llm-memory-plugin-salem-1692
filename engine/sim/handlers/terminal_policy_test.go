package handlers

import "testing"

// terminal_policy_test.go — LLM-184. A single table that pins the
// terminal-on-success policy of the commerce/labor commit surface, so a new
// commerce/labor verb added non-terminal (the storm-prone default) trips this
// test instead of silently regressing. The per-family register tests
// (register_labor_test.go, TestRegisterPayWithItemFamily, TestRegisterSceneQuote_AddsTool,
// TestRegisterOfferTrade_IsTerminalOnSuccess) stay; this is the consolidated
// invariant.
//
// Why these are terminal: a placed or answered economic offer ends the tick —
// nothing useful chains after it, and the courtesy after-word was the re-fire
// vector a weak model stormed to the round budget (LLM-184). speak / pay /
// consume stay non-terminal (each has a real same-tick follow-on) and are pinned
// here as the guard against an over-broad future flip.
func TestCommitVerbs_TerminalPolicy(t *testing.T) {
	r := NewRegistry()
	registrations := []struct {
		name string
		fn   func(*Registry) error
	}{
		{"speak", RegisterSpeak},
		{"pay", RegisterPay},
		{"consume", RegisterConsume},
		{"scene_quote", RegisterSceneQuote},
		{"pay_with_item_family", RegisterPayWithItemFamily},
		{"offer_trade", RegisterOfferTrade},
		{"labor_family", RegisterLaborFamily},
	}
	for _, reg := range registrations {
		if err := reg.fn(r); err != nil {
			t.Fatalf("register %s: %v", reg.name, err)
		}
	}

	cases := []struct {
		tool string
		want TerminalPolicy
	}{
		// Terminal (LLM-184): one successful economic commitment ends the tick.
		{"pay_with_item", TerminalOnSuccess},
		{"accept_pay", TerminalOnSuccess},
		{"decline_pay", TerminalOnSuccess},
		{"counter_pay", TerminalOnSuccess},
		{"withdraw_pay", TerminalOnSuccess},
		{"offer_trade", TerminalOnSuccess},
		{"sell", TerminalOnSuccess},
		{"solicit_work", TerminalOnSuccess},
		{"accept_work", TerminalOnSuccess},
		{"decline_work", TerminalOnSuccess},
		// Non-terminal: a legitimate same-tick follow-on. Pinned so an over-broad
		// flip of the whole commit class can't slip through.
		{"speak", TerminalNever},
		{"pay", TerminalNever},
		{"consume", TerminalNever},
	}
	for _, tc := range cases {
		e, ok := r.Lookup(tc.tool)
		if !ok {
			t.Errorf("%s not registered", tc.tool)
			continue
		}
		if e.Class != ClassCommit {
			t.Errorf("%s Class = %v, want ClassCommit", tc.tool, e.Class)
		}
		if e.TerminalPolicy != tc.want {
			t.Errorf("%s TerminalPolicy = %v, want %v (LLM-184 commit terminal policy)", tc.tool, e.TerminalPolicy, tc.want)
		}
	}
}
