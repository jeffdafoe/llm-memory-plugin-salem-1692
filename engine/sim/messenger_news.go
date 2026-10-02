package sim

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// messenger_news.go — the messenger's news from outside (LLM-700).
//
// The messenger is the one traveler whose trade is news. He carries a single
// item of real 1692 news from beyond the village — Boston, the ports, the
// frontier, ships from England — written for the current calendar date by an
// off-world LLM call made when he spawns (cascade/messenger_news.go). It rides
// VisitorState.Payload, so the traveler preface voices it ("Word reached you on
// the road that …") and LLM-545's shared-word memory tracks who has heard it.
// Every other traveler carries no word at all.
//
// It replaced the village road word (LLM-371), which handed EVERY traveler one of
// the village's own trades from the last day as word "from the road". A sale made
// that afternoon at the Tavern cannot have reached Lynn, so the traveler invented a
// source; a messenger, whose calling is delivery, read the clause as a message for
// the villager it named and went looking for him (2026-10-02: Roger Standish told
// the village John Ellis had died). News from outside can name no villager and
// contradict nothing that happened here.

// MessengerArchetype is the passer-through calling that carries outside news.
const MessengerArchetype = "messenger"

// MaxMessengerNewsLen bounds the installed news clause in runes. The prompt asks
// for under 35 words; this is the backstop against a rambling reply.
const MaxMessengerNewsLen = 280

// messengerNewsBannedWords are refused in any form inside the news, whatever the
// prompt said: no witch-trial news for now (Jeff, 2026-10-02), and the news is
// from beyond the village, never about it.
var messengerNewsBannedWords = []string{"witch", "salem"}

// CarriesOutsideNews reports whether a traveler is the one who brings news from
// outside — a passer-through messenger. A merchant never does, whatever label
// his errand renders.
func (vs *VisitorState) CarriesOutsideNews() bool {
	return vs != nil && vs.Trade == nil && vs.Archetype == MessengerArchetype
}

// MessengerNewsOutcome reports what SetMessengerNews did with an authored item.
type MessengerNewsOutcome string

const (
	// MessengerNewsInstalled — the news is now his Payload.
	MessengerNewsInstalled MessengerNewsOutcome = "installed"
	// MessengerNewsGone — he is no longer a messenger in the village waiting for
	// news (left, departing, or already carrying some).
	MessengerNewsGone MessengerNewsOutcome = "gone"
	// MessengerNewsEmpty — nothing usable was left after cleaning.
	MessengerNewsEmpty MessengerNewsOutcome = "empty"
	// MessengerNewsTooLong — the cleaned clause exceeds MaxMessengerNewsLen.
	MessengerNewsTooLong MessengerNewsOutcome = "too_long"
	// MessengerNewsBannedWord — it touches witchcraft or Salem.
	MessengerNewsBannedWord MessengerNewsOutcome = "banned_word"
	// MessengerNewsNamesVillager — it names someone who lives in the village.
	MessengerNewsNamesVillager MessengerNewsOutcome = "names_villager"
)

// SetMessengerNews installs authored news as a messenger's Payload. It refuses
// news that is empty, too long, touches witchcraft or Salem, or names a
// villager — the prompt asks for none of these, and this is the door that
// holds when the model does not listen. A refused item leaves him without news;
// the preface then simply drops the clause. Never overwrites news he already
// carries. Always succeeds as a Command; the outcome says what happened.
func SetMessengerNews(id ActorID, text string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a := w.Actors[id]
			if a == nil || !a.VisitorState.CarriesOutsideNews() ||
				a.VisitorState.Phase == VisitorPhaseDeparting || a.VisitorState.Payload != "" {
				return MessengerNewsGone, nil
			}
			clause := CleanMessengerNews(text)
			if outcome := messengerNewsRefusal(w, clause); outcome != "" {
				return outcome, nil
			}
			a.VisitorState.Payload = clause
			return MessengerNewsInstalled, nil
		},
	}
}

// CleanMessengerNews shapes an authored reply into the bare clause the preface
// completes ("Word reached you on the road that <clause>."): first non-empty
// line only, wrapping quotes and list markers dropped, a leading "that" dropped,
// trailing punctuation dropped, first letter kept as written.
func CleanMessengerNews(text string) string {
	line := ""
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	line = messengerNewsListMarker.ReplaceAllString(line, "")
	line = strings.Trim(line, "\"'“”‘’ ")
	if rest, ok := cutPrefixFold(line, "that "); ok {
		line = rest
	}
	line = strings.TrimRight(line, ".!;:, ")
	return strings.TrimSpace(line)
}

// messengerNewsRefusal returns why a cleaned clause cannot be carried, or "" when
// it can. MUST run on the world goroutine (reads w.Actors).
func messengerNewsRefusal(w *World, clause string) MessengerNewsOutcome {
	if clause == "" {
		return MessengerNewsEmpty
	}
	if utf8.RuneCountInString(clause) > MaxMessengerNewsLen {
		return MessengerNewsTooLong
	}
	lower := strings.ToLower(clause)
	for _, banned := range messengerNewsBannedWords {
		if strings.Contains(lower, banned) {
			return MessengerNewsBannedWord
		}
	}
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) && r != '\'' }) {
		words[word] = true
	}
	for _, surname := range villagerSurnames(w) {
		if words[surname] {
			return MessengerNewsNamesVillager
		}
	}
	return ""
}

// villagerSurnames is the last name of every person who lives in the village —
// residents and players, not visitors and not decoratives (a decorative's name is
// a prop's or an animal's: "Cow", "Cow 2"). Matched as whole words, so a surname
// like Ward cannot hit "toward". MUST run on the world goroutine.
func villagerSurnames(w *World) []string {
	var out []string
	for _, a := range w.Actors {
		if a == nil || a.VisitorState != nil || a.Kind == KindDecorative {
			continue
		}
		if s := extractSurname(a.DisplayName); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// messengerNewsListMarker matches a leading list marker ("- ", "* ", "• ", "1. ",
// "2) ") — and only a marker, so a clause that opens on a number keeps it.
var messengerNewsListMarker = regexp.MustCompile(`^(?:[-*•]|\d+[.)])\s+`)

// cutPrefixFold is strings.CutPrefix, case-insensitive on the prefix.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// MessengerNewsDate is the calendar day a messenger's news is written for: the
// village's local date, carried back to 1692.
func MessengerNewsDate(w *World, at time.Time) (time.Month, int) {
	if loc := w.Settings.Location; loc != nil {
		at = at.In(loc)
	}
	return at.Month(), at.Day()
}
