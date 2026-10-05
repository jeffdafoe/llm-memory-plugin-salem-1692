package sim

import (
	"strings"
	"testing"
	"time"
)

// court_notices_internal_test.go — LLM-706: the magistrates' notices — the
// neutral hearing notice, the short-form ruling per result, the notice window,
// and the order on the board.

func ruleCase(t *testing.T, w *World, id CourtCaseID, r CourtRuling, at time.Time) {
	t.Helper()
	if _, err := ApplyCourtRuling(id, r, at).Fn(w); err != nil {
		t.Fatalf("ApplyCourtRuling: %v", err)
	}
}

func TestCourtNoticeLines_HearingNoticeNamesThePartiesNotTheComplaint(t *testing.T) {
	w, _ := courtWorld()
	fileCase(t, w, "gideon", []string{"Josiah Thorne"}, courtMorning, false)
	lines := CourtNoticeLines(w, courtMorning)
	want := []string{
		"A matter concerning Josiah Thorne goes before the magistrates in Salem Town at noon.",
		"Their word will be posted here once given.",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if strings.Contains(strings.Join(lines, " "), "ledger") {
		t.Fatalf("the complaint leaked onto the board: %q", lines)
	}
}

func TestCourtNoticeLines_RulingReplacesTheHearingNotice(t *testing.T) {
	w, _ := courtWorld()
	c := fileCase(t, w, "gideon", []string{"Josiah Thorne", "Lewis Walker"}, courtMorning, false)
	noon := courtMorning.Add(3 * time.Hour)
	ruleCase(t, w, c.ID, CourtRuling{Result: CourtResultNoCase, Words: "Josiah Thorne, and you, Constable Marsh: no ledger was ever kept."}, noon)
	lines := CourtNoticeLines(w, noon)
	want := []string{
		"The magistrates in Salem Town have ruled on the matter concerning Josiah Thorne and Lewis Walker.",
		"They found no case. The matter is closed.",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}

func TestCourtNoticeLines_OutcomePerResult(t *testing.T) {
	cases := []struct {
		name   string
		ruling CourtRuling
		want   string
	}{
		{"found_for", CourtRuling{Result: CourtResultFoundFor, FoundFor: "Lewis Walker", Words: "w"}, "They found for Lewis Walker. The matter is closed."},
		{"pay", CourtRuling{Result: CourtResultPay, Payer: "Josiah Thorne", Payee: "Lewis Walker", Amount: 5, Words: "w"}, "They ordered Josiah Thorne to hand Lewis Walker 5 coins. The matter is closed."},
		{"pay_one", CourtRuling{Result: CourtResultPay, Payer: "Josiah Thorne", Payee: "Lewis Walker", Amount: 1, Words: "w"}, "They ordered Josiah Thorne to hand Lewis Walker 1 coin. The matter is closed."},
		{"no_such_charge", CourtRuling{Result: CourtResultNoSuchCharge, Words: "This court hears no charge of witchcraft."}, "They would not hear it. The matter is closed."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := courtWorld()
			c := fileCase(t, w, "gideon", []string{"Josiah Thorne", "Lewis Walker"}, courtMorning, false)
			ruleCase(t, w, c.ID, tc.ruling, courtMorning.Add(3*time.Hour))
			lines := CourtNoticeLines(w, courtMorning.Add(3*time.Hour))
			if len(lines) != 2 || lines[1] != tc.want {
				t.Fatalf("lines = %q, want second line %q", lines, tc.want)
			}
			if strings.Contains(strings.ToLower(strings.Join(lines, " ")), "witch") {
				t.Fatalf("the charge was posted: %q", lines)
			}
		})
	}
}

func TestCourtNoticeLines_RulingStandsThreeDays(t *testing.T) {
	w, _ := courtWorld()
	c := fileCase(t, w, "gideon", []string{"Josiah Thorne"}, courtMorning, false)
	ruled := courtMorning.Add(3 * time.Hour)
	ruleCase(t, w, c.ID, CourtRuling{Result: CourtResultNoCase, Words: "w"}, ruled)
	if got := CourtNoticeLines(w, ruled.AddDate(0, 0, CourtRulingNoticeDays).Add(-time.Minute)); len(got) != 2 {
		t.Fatalf("a minute before the window closes: %q, want the ruling", got)
	}
	if got := CourtNoticeLines(w, ruled.AddDate(0, 0, CourtRulingNoticeDays)); len(got) != 0 {
		t.Fatalf("at the window's end: %q, want nothing", got)
	}
}

func TestCourtNoticeLines_PendingFirstThenRulings(t *testing.T) {
	w, _ := courtWorld()
	old := fileCase(t, w, "gideon", []string{"Lewis Walker"}, courtMorning, false)
	ruleCase(t, w, old.ID, CourtRuling{Result: CourtResultNoCase, Words: "w"}, courtMorning.Add(3*time.Hour))
	fileCase(t, w, "gideon", []string{"Anne Walker"}, courtMorning.Add(5*time.Hour), false)
	lines := CourtNoticeLines(w, courtMorning.Add(6*time.Hour))
	if len(lines) != 4 || !strings.Contains(lines[0], "concerning Anne Walker goes before") || !strings.Contains(lines[2], "ruled on the matter concerning Lewis Walker") {
		t.Fatalf("lines = %q, want Anne's hearing then Lewis's ruling", lines)
	}
}
