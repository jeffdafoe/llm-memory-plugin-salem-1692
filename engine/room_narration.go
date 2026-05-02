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

// itemAttributeFor returns the satisfies_attribute for an item_kind, or
// empty string if the item isn't a known consumable. Used by the
// consume narration to pick "eats" vs "drinks".
func (app *App) itemAttributeFor(ctx context.Context, item string) string {
	var attr string
	err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(satisfies_attribute, '') FROM item_kind WHERE name = $1`,
		strings.ToLower(strings.TrimSpace(item)),
	).Scan(&attr)
	if err != nil {
		return ""
	}
	return attr
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
// could be []interface{} (canonical JSON) or a single string (some
// providers single-element-stringify). Returns empty when missing.
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
		if strings.TrimSpace(v) != "" {
			return []string{v}
		}
	}
	return nil
}
