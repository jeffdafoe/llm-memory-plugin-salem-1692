package perception

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// item_noun.go — LLM-113 count-aware item noun phrases for in-world prose and
// cues. The catalog stores a singular and plural counting phrase per kind
// (article-less, e.g. "raspberry"/"raspberries", "tankard of ale"/"tankards of
// ale"); these helpers resolve them off the snapshot, falling back through the
// def's own Singular()/Plural() (which fall back to DisplayLabel) to the raw key
// for an unknown/unlabeled kind. itemDisplayLabel stays the menu/catalog label.

// itemPlural is the plural counting phrase for a kind, falling back through the
// def's Plural() (then DisplayLabel) to the raw key for an unknown kind. Drives
// the "you can gather X here" cue. The singular/count-aware forms are produced
// where they're needed: the consume copy via ItemKindDef.Singular()/CountNoun
// (sim), the buy cue via buyCueNoun below.
func itemPlural(snap *sim.Snapshot, kind sim.ItemKind) string {
	if snap != nil {
		if def := snap.ItemKinds[kind]; def != nil {
			if p := def.Plural(); p != "" {
				return p
			}
		}
	}
	return string(kind)
}

// The indefinite-article helper lives in sim (sim.WithIndefiniteArticle) so the
// consume failure copy and the perception buy cue share one rule (LLM-113).

// buyCueNoun is the noun for the "buy X" cue: the singular counting phrase with
// an indefinite article when the kind carries one ("buy a wedge of cheese"), or
// the plain menu label otherwise ("buy Cheese"). Only article-prefix a real
// authored phrase — a phrase-less kind (a discovery mint, or pre-backfill data)
// must not get an article glued onto its menu label (code_review, LLM-113).
func buyCueNoun(snap *sim.Snapshot, kind sim.ItemKind) string {
	if snap != nil {
		if def := snap.ItemKinds[kind]; def != nil && def.DisplayLabelSingular != "" {
			return sim.WithIndefiniteArticle(def.DisplayLabelSingular)
		}
	}
	return itemDisplayLabel(snap, kind)
}
