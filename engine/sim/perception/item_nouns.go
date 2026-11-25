package perception

import (
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// itemNouns is the catalog's counting nouns, copied onto the payload so the
// offer and order lines can say "3 shovels" and "5 nails" instead of the raw
// kind key ("3 shovel", "1 fried_meat") while render stays catalog-free
// (LLM-1029). A kind with no entry here, or with no phrase in the catalog,
// renders as its raw key, as orderCountNoun does. A hand-built test payload
// leaves the table nil, which renders every kind as its key.
type itemNouns map[sim.ItemKind]countNouns

// countNouns is one kind's article-less singular and plural counting phrase
// (ItemKindDef.Singular / Plural, LLM-113).
type countNouns struct {
	Singular string
	Plural   string
}

// buildItemNouns copies the singular and plural phrase of every catalog kind.
// The catalog is a few dozen kinds, so copying it whole is cheaper to reason
// about than collecting the kinds each section happens to name.
func buildItemNouns(snap *sim.Snapshot) itemNouns {
	if snap == nil || len(snap.ItemKinds) == 0 {
		return nil
	}
	nouns := make(itemNouns, len(snap.ItemKinds))
	for kind, def := range snap.ItemKinds {
		if def == nil {
			continue
		}
		nouns[kind] = countNouns{Singular: def.Singular(), Plural: def.Plural()}
	}
	return nouns
}

// count is the noun for "<qty> <noun>": the singular phrase at qty 1, the plural
// otherwise, the raw key when the catalog has no phrase. The result is
// sanitized, because catalog labels are admin-edited text.
func (n itemNouns) count(kind sim.ItemKind, qty int) string {
	if c, ok := n[kind]; ok {
		if qty == 1 && c.Singular != "" {
			return sanitizeInline(c.Singular)
		}
		if qty != 1 && c.Plural != "" {
			return sanitizeInline(c.Plural)
		}
	}
	return sanitizeInline(string(kind))
}

// counted is "<qty> <noun>" ("3 shovels", "1 sack of flour").
func (n itemNouns) counted(kind sim.ItemKind, qty int) string {
	return fmt.Sprintf("%d %s", qty, n.count(kind, qty))
}

// isPluralVerb reports whether "<noun> is/are" takes "are" for qty of kind. A
// mass noun whose plural phrase is its singular ("sage") keeps "is" at any
// count; a count noun ("nails", "sacks of flour") takes "are" above one.
func (n itemNouns) isPluralVerb(kind sim.ItemKind, qty int) bool {
	if qty == 1 {
		return false
	}
	// A kind with no phrase renders as its raw key, which reads as a mass noun
	// ("the flour is worth").
	c, ok := n[kind]
	if !ok || c.Plural == "" {
		return false
	}
	return c.Plural != c.Singular
}
