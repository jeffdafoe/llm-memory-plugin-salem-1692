package main

// Room-event narration shared between the live WS broadcast path
// (agent_tick.go's commit branches) and the talk-panel backload
// (pc_handlers.go's loadRecentSpeechAtStructure). Each helper takes the
// action's payload map (the same shape that lands in agent_action_log)
// and returns the prerendered third-person line a player observes.
//
// Why payload-shaped, not request-struct-shaped: backload reads JSON
// from the audit row, live broadcast reads tc.Input. Both are
// map[string]interface{}, so a single function serves both call sites
// and there's no risk of the live and history lines drifting.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// narrateServe builds the room line for a successful serve commit.
//
//   serverName       — the tavernkeeper
//   payload          — {item, qty?, recipients[], consume_now?}
//
// Examples:
//   "John Ellis serves Jefferey ale."
//   "John Ellis serves Jefferey 2 ales."
//   "John Ellis serves Jefferey and Wendy stew."
//   "John Ellis hands Jefferey bread to take."
func narrateServe(serverName string, payload map[string]interface{}) string {
	item, _ := payload["item"].(string)
	item = strings.TrimSpace(strings.ToLower(item))
	if item == "" {
		return ""
	}
	qty := payloadInt(payload, "qty")
	if qty <= 0 {
		qty = 1
	}
	recipients := payloadStringSlice(payload, "recipients")
	if len(recipients) == 0 {
		return ""
	}
	// consume_now defaults to true at the tool layer; only false flips
	// us into the take-home phrasing.
	consumeNow := true
	if v, ok := payload["consume_now"].(bool); ok {
		consumeNow = v
	}

	itemPhrase := item
	if qty > 1 {
		itemPhrase = fmt.Sprintf("%d %s", qty, pluralize(item, qty))
	}

	if consumeNow {
		return fmt.Sprintf("%s serves %s %s.", serverName, joinNames(recipients), itemPhrase)
	}
	return fmt.Sprintf("%s hands %s %s to take.", serverName, joinNames(recipients), itemPhrase)
}

// narratePay builds the room line for a successful pay commit.
//
//   buyerName        — the actor who initiated pay
//   payload          — {recipient, amount, item?, qty?, consume_now?, for?}
//
// Examples:
//   "Jefferey pays John Ellis 9 coins."
//   "Jefferey pays John Ellis 9 coins for ale."
//   "Jefferey pays John Ellis 9 coins for 2 breads."
//   "Jefferey gives John Ellis ale."   (amount==0 with item)
//   "Jefferey thanks John Ellis."      (amount==0 no item — gesture)
func narratePay(buyerName string, payload map[string]interface{}) string {
	recipient, _ := payload["recipient"].(string)
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return ""
	}
	amount := payloadInt(payload, "amount")
	item, _ := payload["item"].(string)
	item = strings.TrimSpace(strings.ToLower(item))
	qty := payloadInt(payload, "qty")
	if qty <= 0 {
		qty = 1
	}

	if amount == 0 && item == "" {
		return fmt.Sprintf("%s thanks %s.", buyerName, recipient)
	}
	if amount == 0 && item != "" {
		itemPhrase := item
		if qty > 1 {
			itemPhrase = fmt.Sprintf("%d %s", qty, pluralize(item, qty))
		}
		return fmt.Sprintf("%s gives %s %s.", buyerName, recipient, itemPhrase)
	}

	coinWord := "coins"
	if amount == 1 {
		coinWord = "coin"
	}
	if item == "" {
		return fmt.Sprintf("%s pays %s %d %s.", buyerName, recipient, amount, coinWord)
	}
	itemPhrase := item
	if qty > 1 {
		itemPhrase = fmt.Sprintf("%d %s", qty, pluralize(item, qty))
	}
	return fmt.Sprintf("%s pays %s %d %s for %s.", buyerName, recipient, amount, coinWord, itemPhrase)
}

// narrateConsume builds the room line for a successful consume commit.
//
//   actorName        — the actor eating/drinking
//   payload          — {item, qty?}
//   itemAttribute    — satisfies_attribute from item_kind ("hunger" |
//                      "thirst" | "tiredness" | ""), used to pick verb
//
// Examples:
//   "John Ellis eats stew."
//   "Jefferey drinks 2 ales."
func narrateConsume(actorName string, payload map[string]interface{}, itemAttribute string) string {
	item, _ := payload["item"].(string)
	item = strings.TrimSpace(strings.ToLower(item))
	if item == "" {
		return ""
	}
	qty := payloadInt(payload, "qty")
	if qty <= 0 {
		qty = 1
	}

	verb := "consumes"
	switch itemAttribute {
	case "hunger":
		verb = "eats"
	case "thirst":
		verb = "drinks"
	case "tiredness":
		verb = "rests with"
	}

	itemPhrase := item
	if qty > 1 {
		itemPhrase = fmt.Sprintf("%d %s", qty, pluralize(item, qty))
	}
	return fmt.Sprintf("%s %s %s.", actorName, verb, itemPhrase)
}

// narrateRefreshAtSourceSelf builds a private second-person line for
// the actor who just received a refresh-tagged object's effect —
// "You drink at the Well — the parching ebbs." Intended for the
// actor's own talk panel, not the room (the room shouldn't see your
// private felt experience). Composes a verb appropriate to the
// primary attribute (the largest-magnitude one in hits) with a felt
// clause that scales by the pre-value of the affected need.
//
//   sourceName   — display name of the refresh source ("Well",
//                  "Maple Tree", etc.)
//   hits         — applied attribute drops (attribute, amount, new
//                  value) from applyObjectRefreshAtArrival
//   pre          — map of pre-refresh need values keyed by attribute
//                  name ("hunger" / "thirst" / "tiredness")
//
// Examples:
//
//   "You drink at the Well — the parching ebbs."
//   "You drink at the Well — the slight thirst is gone."
//   "You rest under the Maple Tree — fatigue lifts."
//
// Returns "" when hits is empty (defensive — caller should guard).
func narrateRefreshAtSourceSelf(sourceName string, hits []refreshHit, pre map[string]int) string {
	if len(hits) == 0 {
		return ""
	}
	if sourceName == "" {
		sourceName = "the source"
	}
	// Pick the strongest hit as the primary effect for the verb. Most
	// real sources hit one attribute; oaks-with-acorns and similar
	// multi-attribute placements anchor on whichever produced the
	// bigger drop.
	primary := hits[0]
	for _, h := range hits[1:] {
		// amount is negative — bigger magnitude means smaller (more
		// negative) value.
		if h.Amount < primary.Amount {
			primary = h
		}
	}
	verb := "use"
	switch primary.Attribute {
	case "thirst":
		verb = "drink at"
	case "hunger":
		verb = "eat at"
	case "tiredness":
		verb = "rest under"
	}
	clauses := make([]string, 0, len(hits))
	for _, h := range hits {
		oldValue := pre[h.Attribute]
		clause := feltSatisfactionClause(h.Attribute, oldValue, h.NewValue)
		if clause != "" {
			clauses = append(clauses, clause)
		}
	}
	base := fmt.Sprintf("You %s the %s.", verb, sourceName)
	if len(clauses) == 0 {
		return base
	}
	return fmt.Sprintf("%s — %s.", strings.TrimSuffix(base, "."), strings.Join(clauses, "; "))
}

// feltSatisfactionClause returns a short felt-language fragment
// describing the experience of a need dropping from oldValue to
// newValue. Returns "" when the drop is too small to comment on
// (already-calm starting state, no actual movement) so callers can
// build clean sentences with or without the clause.
//
// Tiering mirrors the engine's mild/red/peak thresholds (8, 18, 24)
// — same boundaries the satiation block already uses, so the felt
// language reads consistently with the perception text the LLMs see.
//
//   - oldValue >= 24 (peak)              → "barely a dent" if still ≥18, "the parching/gnawing ebbs" if dropped under 18
//   - oldValue >= 18 (red)               → "but the X persists" if still ≥18, "the X eases" if dropped under 18
//   - oldValue >= 8  (mild)              → "the slight X is gone" if dropped under 8, "" otherwise
//   - oldValue <  8  (calm)              → "" (no need to narrate the un-needed)
//
// Returned clauses are without leading capitalization so they fit
// after a verb phrase, and without trailing punctuation so the caller
// composes the sentence terminator.
func feltSatisfactionClause(attribute string, oldValue, newValue int) string {
	noun := ""
	verbWeak := ""
	verbStrong := ""
	switch attribute {
	case "thirst":
		noun = "thirst"
		verbWeak = "the thirst eases"
		verbStrong = "the parching ebbs"
	case "hunger":
		noun = "hunger"
		verbWeak = "the hunger fades"
		verbStrong = "the gnawing ebbs"
	case "tiredness":
		noun = "weariness"
		verbWeak = "the weariness eases"
		verbStrong = "fatigue lifts"
	default:
		return ""
	}
	switch {
	case oldValue >= 24 && newValue < 18:
		return verbStrong
	case oldValue >= 24:
		return fmt.Sprintf("barely a dent in the %s", noun)
	case oldValue >= 18 && newValue < 18:
		return verbWeak
	case oldValue >= 18:
		return fmt.Sprintf("but the %s persists", noun)
	case oldValue >= 8 && newValue < 8:
		return fmt.Sprintf("the slight %s is gone", noun)
	}
	return ""
}

// narrateGather builds the room line for a successful gather commit.
//
// Examples:
//   "John Ellis fills a pail of water at the Well."
//   "John Ellis takes 2 berries at the Orchard."
func narrateGather(actorName, item string, qty int, sourceName string) string {
	if item == "" {
		return ""
	}
	if sourceName == "" {
		sourceName = "the source"
	}
	switch item {
	case "water":
		// Pail is the right verb-image for water from a well; sticking
		// to one phrasing keeps observers oriented.
		if qty <= 1 {
			return fmt.Sprintf("%s fills a pail of water at the %s.", actorName, sourceName)
		}
		return fmt.Sprintf("%s fills %d pails of water at the %s.", actorName, qty, sourceName)
	default:
		// Generic "takes" form for future gatherables (berries, fish,
		// etc.) until each gets a tailored verb.
		itemPhrase := item
		if qty > 1 {
			itemPhrase = fmt.Sprintf("%d %s", qty, pluralize(item, qty))
		}
		return fmt.Sprintf("%s takes %s at the %s.", actorName, itemPhrase, sourceName)
	}
}

// narrateSummon builds the room line for a successful summon commit.
//
//   summonerName     — the actor doing the summoning
//   payload          — {target, reason?}
//
// Examples:
//   "John Ellis sends a messenger for Ezekiel Crane."
//   "John Ellis sends a messenger for Ezekiel Crane: 'come share an ale'."
func narrateSummon(summonerName string, payload map[string]interface{}) string {
	target, _ := payload["target"].(string)
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	reason, _ := payload["reason"].(string)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Sprintf("%s sends a messenger for %s.", summonerName, target)
	}
	return fmt.Sprintf("%s sends a messenger for %s: %q.", summonerName, target, reason)
}

// itemAttributeFor returns the primary satisfaction attribute for an
// item_kind — the one with the largest amount in item_satisfies. Used
// by the consume narration to pick "eats" vs "drinks". Multi-effect
// items like ale (thirst 4 + hunger 2) anchor on the bigger one
// (thirst → "drinks ale"). Returns empty string when the item has no
// satisfactions (materials, unknowns) so callers can fall back to a
// generic verb.
func (app *App) itemAttributeFor(ctx context.Context, item string) string {
	satisfactions, err := loadItemSatisfactions(ctx, app.DB, strings.ToLower(strings.TrimSpace(item)))
	if err != nil {
		return ""
	}
	return primarySatisfactionAttribute(satisfactions)
}

// joinNames renders a list of names as "A", "A and B", or "A, B, and C"
// — the comma-separated form a reader expects in narration. Callers
// should pass at least one name (caller-checked).
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

// pluralize produces a naive plural for the small set of item names
// the narration covers. "ale" → "ales", "stew" → "stews", "bread" →
// "breads". For items that don't pluralize cleanly with -s the right
// fix is the display_label column, but qty>1 serves are rare enough
// today that the simple form is acceptable.
func pluralize(noun string, qty int) string {
	if qty <= 1 {
		return noun
	}
	if strings.HasSuffix(noun, "s") || strings.HasSuffix(noun, "x") || strings.HasSuffix(noun, "ch") {
		return noun + "es"
	}
	return noun + "s"
}

// payloadInt coerces a payload field to int. JSON numbers come through
// as float64; some providers stringify; missing keys yield 0. Mirrors
// the coerceIntInput pattern used in agent_tick.go but stays
// payload-shaped for the audit-row backload path.
func payloadInt(payload map[string]interface{}, key string) int {
	switch v := payload[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		// A misbehaving provider that stringified a number — best
		// effort. Empty / unparseable returns 0, which the caller
		// treats as "use the default".
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		return n
	}
	return 0
}

// payloadStringSlice extracts a []string from a payload field that
// could arrive in any of three shapes:
//
//   1. []interface{} — canonical JSON array.
//   2. string with [..] wrapping — provider re-serialized the array
//      as a JSON string (saw this with Llama 3.3). Without unwrapping,
//      narration renders the literal "[\"Jefferey\",\"Wendy\"]" and
//      the agent_action_log's payload->>'recipients' joins are off.
//   3. plain string — single recipient stringified.
//
// Returns empty when missing or unparseable.
func payloadStringSlice(payload map[string]interface{}, key string) []string {
	switch v := payload[key].(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			var parsed []string
			if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
				out := make([]string, 0, len(parsed))
				for _, s := range parsed {
					if strings.TrimSpace(s) != "" {
						out = append(out, s)
					}
				}
				return out
			}
		}
		return []string{trimmed}
	}
	return nil
}
