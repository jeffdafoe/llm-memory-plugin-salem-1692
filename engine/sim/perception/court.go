package perception

import (
	"fmt"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// court.go — the constable's side of the magistrates (LLM-695). The "## The
// magistrates" section, rendered to the constable only.
//
// A matter a villager cannot settle — a theft, a debt in dispute — used to have
// no ending: the engine does not parse conversation, so a claim made in talk was
// never checked against the record, and the constable pursued the theft of a
// ledger that never existed for eleven days. He can now bring such a matter
// before the magistrates in Salem Town (bring_before_magistrates); they sit once
// a day, read what passed in the village, and send word to everyone concerned.
//
// TOOL-CUE LOCKSTEP: the section is the bring_before_magistrates gate. The tool
// is offered iff OffersFiling() — a constable with matters left in today's limit
// — and the section then says so; at the limit the section says the court hears
// no more from him today and the tool is withdrawn. Not co-location-gated: the
// court is off the map, and the matters he would bring arise anywhere.
//
// No imperative to file. The section states how justice works and what is
// before the court now; whether a matter is one he cannot settle himself is his
// judgment, and an instruction here would turn every grievance he hears into a
// filing.

// CourtView is the "## The magistrates" payload: present only for a constable.
type CourtView struct {
	// Sitting is the sitting time as the village says it ("noon").
	Sitting string
	// Limit is how many new matters he may bring in a game-day; FiledToday how
	// many he has.
	Limit      int
	FiledToday int
	// Pending is every matter he brought that the court has not yet ruled on.
	Pending []CourtPendingView
}

// CourtPendingView is one matter waiting for the court.
type CourtPendingView struct {
	Complaint string
	Parties   []string
	Hearing   sim.CourtHearing
}

// OffersFiling gates bring_before_magistrates: a constable under today's limit.
func (v *CourtView) OffersFiling() bool {
	return v != nil && v.FiledToday < v.Limit
}

// buildCourt returns the section payload for a constable, nil for anyone else.
func buildCourt(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *CourtView {
	if snap == nil || actorSnap == nil || !isConstableSnapshot(actorSnap) {
		return nil
	}
	limit := snap.CourtDailyCaseLimit
	if limit <= 0 {
		limit = sim.DefaultCourtDailyCaseLimit
	}
	v := &CourtView{
		Sitting:    sim.CourtSittingPhrase(snap.CourtSittingTime),
		Limit:      limit,
		FiledToday: snap.CourtFiledToday[actorID],
	}
	for _, e := range snap.CourtDocket {
		if e.Case == nil || e.Case.FiledByID != actorID {
			continue
		}
		parties := make([]string, 0, len(e.Case.Parties))
		for _, p := range e.Case.Parties {
			parties = append(parties, p.Name)
		}
		v.Pending = append(v.Pending, CourtPendingView{
			Complaint: e.Case.Complaint,
			Parties:   parties,
			Hearing:   e.Hearing,
		})
	}
	return v
}

// renderCourt writes the "## The magistrates" section. nil writes nothing.
func renderCourt(b *strings.Builder, v *CourtView) {
	if v == nil {
		return
	}
	b.WriteString("## The magistrates\n")
	b.WriteString("Matters you cannot settle yourself — a theft, a debt in dispute, a wrong one villager lays at another's door — go before the magistrates in Salem Town. ")
	b.WriteString("They sit each day at " + v.Sitting + ", read what passed in the village, and send word of their ruling to everyone the matter concerns; their ruling ends it. ")
	if v.OffersFiling() {
		fmt.Fprintf(b, "You may bring up to %d new matters a day with bring_before_magistrates, naming the villagers concerned and saying what the matter is.\n", v.Limit)
	} else {
		fmt.Fprintf(b, "You have brought %d matters today; the court hears no more from you until tomorrow.\n", v.FiledToday)
	}
	for _, p := range v.Pending {
		b.WriteString("Before the court now, as you brought it: \"" + elideRunes(p.Complaint, 160) + "\"")
		if len(p.Parties) > 0 {
			b.WriteString(" (" + strings.Join(p.Parties, ", ") + ")")
		}
		switch p.Hearing {
		case sim.CourtHearingToday:
			b.WriteString(" — it is heard today at " + v.Sitting + ".\n")
		case sim.CourtHearingTomorrow:
			b.WriteString(" — it is heard tomorrow at " + v.Sitting + ".\n")
		default:
			b.WriteString(" — the court has sat, and its word has not yet come.\n")
		}
	}
}

// elideRunes shortens s to at most max runes, marking the cut.
func elideRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}

// CourtRulingsView is the "## The magistrates' word" payload: every ruling of
// the last sim.CourtRulingNoticeDays on a matter the subject was party to or
// brought, as a standing line. The ruling's beat reaches only the next turn,
// and a sleeping villager's beats are dropped once stale (Josiah slept through
// the first ruling), so the closed matter also stands here — built from the
// case record, so sleep and restarts cannot lose it. No tool, no imperative.
type CourtRulingsView struct {
	Lines []string
}

// buildCourtRulings returns the section for a party or filer of a recent
// ruling, nil otherwise.
func buildCourtRulings(snap *sim.Snapshot, actorID sim.ActorID) *CourtRulingsView {
	if snap == nil {
		return nil
	}
	var v *CourtRulingsView
	for _, r := range snap.CourtRecentRulings {
		if r.Case == nil || !r.Case.Involves(actorID) {
			continue
		}
		if v == nil {
			v = &CourtRulingsView{}
		}
		v.Lines = append(v.Lines, sim.CourtRulingStandingLine(r.Case, actorID, r.Day))
	}
	return v
}

// renderCourtRulings writes the "## The magistrates' word" section. nil writes
// nothing.
func renderCourtRulings(b *strings.Builder, v *CourtRulingsView) {
	if v == nil || len(v.Lines) == 0 {
		return
	}
	b.WriteString("## The magistrates' word\n")
	for _, l := range v.Lines {
		b.WriteString(l + "\n")
	}
}
