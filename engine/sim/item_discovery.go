package sim

import (
	"regexp"
	"strings"
)

// item_discovery.go — ZBBS-WORK-412 hallucinated-item discovery.
//
// When an agent-backed NPC references an item kind that isn't in the catalog
// through a transaction-intent tool (consume / pay_with_item / scene_quote),
// the engine MINTS the kind at quantity 0 instead of hard-failing with
// ErrUnknownItemKind. The minted kind carries no recipe, price, satisfies, or
// instances — it is economically inert until an operator sources it by hand.
//
// The value is telemetry: the minted kinds are a curated discovery list of
// what the NPCs think the world should contain, surfaced in the Village Config
// items table (category "unknown", "0 in world") for review. There is NO
// classifier and NO separate proposal store — the catalog row IS the record,
// and deleting the row is the rejection (Jeff, 2026-06-15). Sourcing a kept
// kind (recipe/price/gather) is a separate, hand-wired effort.
//
// Effect on the failing call: once the kind resolves, the original
// ErrUnknownItemKind flips to a truthful inventory failure ("you have no X to
// give") — groundable by the NPC's own perception — instead of "we don't know
// what that is".

var (
	// discoveryLeadingFillerRe strips a leading article or vague-quantity
	// phrase the LLM tends to prepend to a good's name, so "a pinch of dried
	// chamomile" and "dried chamomile" normalize to the same key. The longer
	// "<x> of" phrases precede the bare articles in the alternation so it can't
	// strip just "a " and leave "pinch of ...". Applied repeatedly (+).
	discoveryLeadingFillerRe = regexp.MustCompile(
		`^(?:(?:a pinch of|a bit of|a piece of|a slice of|a handful of|a sprig of|a bunch of|a cup of|a glass of|a bowl of|a jar of|a pot of|a loaf of|a few|some|a|an|the)\s+)+`)

	// discoveryNonAlnumRe collapses every run of non-alphanumeric characters
	// into a single underscore, yielding a clean snake_case canonical key
	// consistent with the seeded catalog (lowercase keys, e.g. "coca_tea").
	discoveryNonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)
)

// normalizeDiscoveredKey turns free-text LLM item naming into a canonical
// ItemKind key: lowercased, leading filler stripped, non-alphanumeric runs
// collapsed to underscores, surrounding underscores trimmed.
//
//	"a pinch of Dried Chamomile" -> "dried_chamomile"
//	"Lavender sprigs"            -> "lavender_sprigs"
//	"!?!"                        -> ""  (caller keeps ErrUnknownItemKind)
//
// Near-duplicates with genuinely different words ("chamomile" vs "dried
// chamomile") deliberately do NOT collapse — the operator merges those at
// review. Fuzzy matching is out of scope; over-collapsing would lose distinct
// goods.
func normalizeDiscoveredKey(freeText string) string {
	s := strings.ToLower(strings.TrimSpace(freeText))
	s = discoveryLeadingFillerRe.ReplaceAllString(s, "")
	s = discoveryNonAlnumRe.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

// mintDiscoveredKind registers a never-seen item kind at quantity 0 and returns
// its canonical key. Returns ("", false) only when freeText normalizes to an
// empty key (all punctuation/filler) — the caller then keeps ErrUnknownItemKind.
//
// COPY-ON-WRITE per the World.ItemKinds IMMUTABILITY CONTRACT (world.go): the
// published Snapshot aliases the map, so we never mutate it in place. We clone,
// add, and reassign the field — an already-published snapshot keeps its old,
// still-immutable map, and the next republish() aliases the new one. Safe
// because every mint site runs inside a Command.Fn on the world goroutine.
func mintDiscoveredKind(w *World, freeText string) (ItemKind, bool) {
	// Coins are currency, not a good — never mint a coin token as a kind
	// (LLM-290 removed the phantom 'coin' row an earlier mint created). The
	// coin steers at the mint call sites fire first with better messages;
	// this is the backstop that keeps the catalog clean whatever the path.
	if IsCoinToken(freeText) {
		return "", false
	}
	key := normalizeDiscoveredKey(freeText)
	if key == "" {
		return "", false
	}
	kind := ItemKind(key)
	if _, exists := w.ItemKinds[kind]; exists {
		// Already catalogued — a prior mint this session, or a real kind whose
		// label pass just missed because resolveItemKind doesn't strip filler
		// ("a pinch of stew" normalizes to the real "stew"). Dedup; never
		// duplicate or overwrite an existing def.
		return kind, true
	}
	next := make(map[ItemKind]*ItemKindDef, len(w.ItemKinds)+1)
	for k, v := range w.ItemKinds {
		next[k] = v
	}
	next[kind] = &ItemKindDef{
		Name:         kind,
		DisplayLabel: strings.ReplaceAll(key, "_", " "),
		Category:     ItemCategoryUnknown,
		// No Satisfies, Capabilities, or price — economically inert until an
		// operator sources it (recipe/price/gather) or deletes the row.
	}
	w.ItemKinds = next
	return kind, true
}

// resolveOrMintItemKind resolves a free-text item name against the catalog and,
// on a miss, mints it as a discovered kind (ZBBS-WORK-412). Used ONLY at the
// agent tool sites that REJECT the same tick after a mint — consume,
// scene_quote, and the pay_items goods (resolvePayItems) — so a discovery never
// leaves a doomed pending entry behind. The pay_with_item BUY path (the good
// being bought) deliberately does NOT mint: it would register a pending offer
// the seller can't fill, recreating the poisoned-ledger retry loop. Non-
// discovery callers (gather config, admin holdings, order re-validation) keep
// plain resolveItemKind so authored-config typos still fail loudly.
func resolveOrMintItemKind(w *World, freeText string) (ItemKind, bool) {
	if kind, ok := resolveItemKind(w, freeText); ok {
		return kind, true
	}
	return mintDiscoveredKind(w, freeText)
}
