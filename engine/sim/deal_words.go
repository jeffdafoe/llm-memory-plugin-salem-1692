package sim

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// DealWords reports goods a trade's spoken line names that the deal itself does
// not carry. An offer, a counter and an accept each hold the goods on the ledger
// entry, while the words riding on them are free prose — and the live failure was
// the two coming apart: John Ellis offered for "a fried meat and a flask of
// water" on a fried-meat-only offer, Hannah Boggs countered "the fried meat and
// the water together for eight" on the same meat-only entry, and the water was
// then charged again on top. Named lists the seller's goods the words spoke of,
// as spoken; Deal is the deal's goods as prose ("1 fried meat"). Empty Named
// means the words and the deal agree.
type DealWords struct {
	Named []string
	Deal  string
}

// DealWordsFor checks say against the ledger entry ledgerID: which of the
// seller's goods it names that the entry neither sells nor takes in payment.
// The deal's own goods are masked out of the line first, longest phrase first,
// so "fried meat" in the line never also reads as the seller's raw "meat". A
// good counts only while the seller holds some — the words can only promise
// what there is to sell. MUST be called from inside a Command.Fn.
func DealWordsFor(w *World, ledgerID LedgerID, say string) DealWords {
	entry := w.PayLedger[ledgerID]
	if entry == nil || strings.TrimSpace(say) == "" {
		return DealWords{}
	}
	seller := w.Actors[entry.SellerID]
	if seller == nil {
		return DealWords{}
	}
	inDeal := map[ItemKind]bool{}
	var dealParts []string
	if len(entry.Lines) > 0 {
		for _, ln := range entry.Lines {
			inDeal[ln.ItemKind] = true
			dealParts = append(dealParts, countedGood(w, ln.ItemKind, ln.Qty))
		}
	} else if entry.ItemKind != "" {
		inDeal[entry.ItemKind] = true
		dealParts = append(dealParts, countedGood(w, entry.ItemKind, entry.Qty))
	}
	for _, p := range entry.PayItems {
		inDeal[p.Kind] = true
	}
	if len(dealParts) == 0 {
		return DealWords{}
	}

	// One pass over every phrase, longest first, masking each match so a shorter
	// phrase cannot re-read it. The deal's own goods mask without counting, and so
	// does each one's head noun — "the meat" in a fried-meat deal means the fried
	// meat, not the seller's raw meat — winning a tie with a good of the same
	// length. A LONGER phrase for another good still wins: "fried meat" spoken in
	// a raw-meat deal is the fried meat.
	type phrase struct {
		kind   ItemKind
		term   string
		inDeal bool
	}
	var phrases []phrase
	for kind := range inDeal {
		for _, term := range goodTerms(w, kind) {
			phrases = append(phrases, phrase{kind, term, true})
			if i := strings.LastIndexByte(term, ' '); i >= 0 {
				phrases = append(phrases, phrase{kind, term[i+1:], true})
			}
		}
	}
	for kind, qty := range seller.Inventory {
		if qty <= 0 || inDeal[kind] {
			continue
		}
		for _, term := range goodTerms(w, kind) {
			phrases = append(phrases, phrase{kind, term, false})
		}
	}
	sort.Slice(phrases, func(i, j int) bool {
		a, b := phrases[i], phrases[j]
		if len(a.term) != len(b.term) {
			return len(a.term) > len(b.term)
		}
		if a.inDeal != b.inDeal {
			return a.inDeal
		}
		if a.term != b.term {
			return a.term < b.term
		}
		return a.kind < b.kind
	})
	line := " " + wordsOnly(say) + " "
	var named []string
	seen := map[ItemKind]bool{}
	for _, c := range phrases {
		if !strings.Contains(line, " "+c.term+" ") {
			continue
		}
		line = strings.ReplaceAll(line, " "+c.term+" ", " ")
		if c.inDeal {
			continue
		}
		if !seen[c.kind] {
			seen[c.kind] = true
			named = append(named, c.term)
		}
	}
	if len(named) == 0 {
		return DealWords{}
	}
	sort.Strings(named)
	return DealWords{Named: named, Deal: strings.Join(dealParts, " and ")}
}

// goodTerms are the phrases a speaker might call a good by: its catalog name
// with underscores as spaces, its display label, and its singular and plural
// counting nouns — each lowercased to words, longest first.
func goodTerms(w *World, kind ItemKind) []string {
	set := map[string]bool{wordsOnly(strings.ReplaceAll(string(kind), "_", " ")): true}
	if def := w.ItemKinds[kind]; def != nil {
		for _, s := range []string{def.DisplayLabel, def.Singular(), def.Plural()} {
			set[wordsOnly(s)] = true
		}
	}
	delete(set, "")
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// countedGood is "N <count noun>" for a deal line, falling back to the kind name.
func countedGood(w *World, kind ItemKind, qty int) string {
	noun := strings.ReplaceAll(string(kind), "_", " ")
	if def := w.ItemKinds[kind]; def != nil && def.CountNoun(qty) != "" {
		noun = def.CountNoun(qty)
	}
	return strconv.Itoa(qty) + " " + noun
}

// wordsOnly lowercases s and turns every run of non-letter, non-digit runes
// (apostrophes included) into one space, so phrases compare as whole words.
func wordsOnly(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
