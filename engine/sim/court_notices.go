package sim

import (
	"sort"
	"time"
)

// court_notices.go — LLM-706: the magistrates on the notice boards and the
// ticker.
//
// A pending case posts a neutral hearing notice — the parties and the sitting,
// never the complaint, which can name a suspect before the court has read
// anything. A ruling posts a short form of the outcome in the engine's words for
// CourtRulingNoticeDays; the magistrate's own words stay with the parties (they
// address them directly and run past a board line).
//
// Like the public-works lines, these are DERIVED from World.CourtCases, so they
// come back after a restart. Filing and ruling resync at once; the court runner
// resyncs every minute (SyncTownNotices) so a ruling leaves the boards when its
// days run out.

// TownNoticeLines is every pinned line the town puts on its boards: the public
// works (LLM-654) first, then the magistrates. Each notice is two lines, so a
// full board drops whole notices from the end (PostNoticeboardWithPinned).
func TownNoticeLines(w *World, now time.Time) []string {
	return append(PublicWorksNoticeLines(w), CourtNoticeLines(w, now)...)
}

// CourtNoticeLines returns two lines per pending case (oldest filing first),
// then two per ruling still inside its notice window (oldest ruling first).
func CourtNoticeLines(w *World, now time.Time) []string {
	var pending, ruled []*CourtCase
	for _, c := range w.CourtCases {
		switch {
		case c == nil:
		case c.Status == CourtCaseStatusPending:
			pending = append(pending, c)
		case courtRulingStands(c, now):
			ruled = append(ruled, c)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if !pending[i].FiledAt.Equal(pending[j].FiledAt) {
			return pending[i].FiledAt.Before(pending[j].FiledAt)
		}
		return pending[i].ID < pending[j].ID
	})
	sort.Slice(ruled, func(i, j int) bool {
		if !ruled[i].RuledAt.Equal(ruled[j].RuledAt) {
			return ruled[i].RuledAt.Before(ruled[j].RuledAt)
		}
		return ruled[i].ID < ruled[j].ID
	})
	var out []string
	for _, c := range pending {
		out = append(out, courtHearingNoticeLines(c, w.Settings.CourtSittingTime)...)
	}
	for _, c := range ruled {
		out = append(out, courtRulingNoticeLines(c)...)
	}
	return out
}

// courtRulingStands reports whether a ruled case is still inside the notice
// window — on the boards, the ticker, and the parties' standing line.
func courtRulingStands(c *CourtCase, now time.Time) bool {
	return c != nil && c.Status == CourtCaseStatusRuled && c.RuledAt.After(now.AddDate(0, 0, -CourtRulingNoticeDays))
}

func courtHearingNoticeLines(c *CourtCase, sittingSpec string) []string {
	return []string{
		"A matter concerning " + joinCourtNames(courtPartyNames(c.Parties)) + " goes before the magistrates in Salem Town at " + CourtSittingPhrase(sittingSpec) + ".",
		"Their word will be posted here once given.",
	}
}

func courtRulingNoticeLines(c *CourtCase) []string {
	return []string{
		"The magistrates in Salem Town have ruled on the matter concerning " + joinCourtNames(courtPartyNames(c.Parties)) + ".",
		courtRulingOutcome(c) + " The matter is closed.",
	}
}

func courtRulingOutcome(c *CourtCase) string {
	switch c.Result {
	case CourtResultFoundFor:
		return "They found for " + c.PartyName(c.FoundForID) + "."
	case CourtResultPay:
		return "They ordered " + c.PartyName(c.PayerID) + " to hand " + c.PartyName(c.PayeeID) + " " + coinCount(c.AmountOrdered) + "."
	case CourtResultNoSuchCharge:
		// Never name the charge: "witchcraft" posted beside a villager's name
		// would spread the accusation the court refused to hear.
		return "They would not hear it."
	default:
		return "They found no case."
	}
}

// CourtTickerLine is one court notice for the top ticker.
type CourtTickerLine struct {
	CaseID CourtCaseID
	Text   string
}

// CourtTickerLines lists the court notices as one ticker line each: the hearing
// notice's first line, or the whole ruling. Pure over the snapshot, whose docket
// and recent rulings are already ordered and windowed (courtDocketForSnapshot).
func CourtTickerLines(s *Snapshot) []CourtTickerLine {
	var out []CourtTickerLine
	for _, e := range s.CourtDocket {
		out = append(out, CourtTickerLine{CaseID: e.Case.ID, Text: courtHearingNoticeLines(e.Case, s.CourtSittingTime)[0]})
	}
	for _, r := range s.CourtRecentRulings {
		lines := courtRulingNoticeLines(r.Case)
		out = append(out, CourtTickerLine{CaseID: r.Case.ID, Text: lines[0] + " " + lines[1]})
	}
	return out
}

// SyncTownNotices resyncs the boards and the ticker against the clock. The court
// runner sends it each minute so a ruling comes down when its notice days run
// out; a no-op while nothing changed.
func SyncTownNotices(now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		syncPublicWorksNews(w, now)
		return nil, nil
	}}
}
