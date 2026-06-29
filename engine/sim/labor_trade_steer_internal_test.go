package sim

import "testing"

// labor_trade_steer_internal_test.go — LLM-167. isLaborToken is the closed
// allow-list that recognizes an NPC naming the labor/work concept where a good
// is expected (offer_trade / pay_with_item / sell / scene_quote). It must match
// the labor vocabulary in the forms a model writes it — bare, articled, cased,
// pluralized — and must NOT false-positive on real goods or words that merely
// contain a labor token as a substring ("homework", "network").

func TestIsLaborToken(t *testing.T) {
	match := []string{
		"work", "labor", "labour", "job", "jobs",
		"Work", "LABOR", "Labour", // case
		"a job", "the work", "an labor", // leading article
		"  work  ", // surrounding space
	}
	for _, s := range match {
		if !isLaborToken(s) {
			t.Errorf("isLaborToken(%q) = false, want true", s)
		}
	}

	noMatch := []string{
		"",
		"bread", "stew", "ale", "nail", "nights_stay", // real goods
		"homework", "network", "workbench", "jobber", // substring near-misses, not whole tokens
		"working", "worker", // morphology we deliberately don't fold in
	}
	for _, s := range noMatch {
		if isLaborToken(s) {
			t.Errorf("isLaborToken(%q) = true, want false", s)
		}
	}
}
