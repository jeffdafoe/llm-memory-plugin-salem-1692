package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// offer_trade_handlers.go — ZBBS-HOME-407.
//
// offer_trade is a proposer-POV front door onto the EXISTING barter
// machinery (ZBBS-HOME-393's pay_with_item / accept_pay / counter_pay).
// It exists for one reason: model legibility.
//
// The underlying pay_with_item tool is framed buyer-centrically —
// `seller` is who you buy FROM, `item` is what you WANT, `pay_items` is
// what you GIVE. A villager who wants to propose "my milk for your bread"
// has to mentally invert all three (seller = the other person, item =
// their bread, pay_items = my milk). The weak stateful-NPC model
// (llama-3.3-70b) reliably scrambles that inversion: in the live
// Josiah×Elizabeth episode (2026-06-06) the milk-seller filled
// pay_with_item with item == pay_items == milk and the trade never
// closed. A seller had no legible way to put "I'll give you X for your Y"
// on the table, so a verbally-agreed swap had no execution path.
//
// offer_trade removes the inversion. Its args read from the proposer's
// own point of view:
//
//   - with:       the other villager (the counterparty)
//   - give:       the goods you hand over (+ optional coins)
//   - coins:      optional coins you add to your side
//   - want_item:  the single good you want from them
//   - want_qty:   how many
//
// The decoder LOWERS those onto a PayWithItemArgs — proposer becomes the
// buyer-role, `want` becomes the bought item (flows counterparty→proposer
// on accept), `give` becomes pay_items (flows proposer→counterparty). It
// then reuses HandlePayWithItem and the whole downstream ledger flow
// (warrant on the counterparty, accept/decline/counter advertised
// alongside speak, the atomic two-way swap in commitPayTransfer)
// untouched. Because the decoded shape IS a PayWithItemArgs, the harness's
// pay-offer steer + same-tick dedup (commitResultContent / payOfferKey)
// apply once their tool-name guards also accept "offer_trade".
//
// Scope (MVP, ZBBS-HOME-407): want is a single item kind (maps 1:1 to the
// ledger's single bought item); give may be up to MaxPayWithItemPayItemsHandler
// lines. consume_now is always false — offer_trade is a goods HANDOVER
// (into inventory), not an eat-here purchase. A buyer who wants to barter
// AND consume immediately still uses pay_with_item directly (consume_now
// + pay_items). A proposer who wants coins in return is making a coin
// sale, which is scene_quote, not a trade.

// offerTradeArgs is the raw JSON shape of the offer_trade tool's
// arguments, decoded from the proposer's point of view before being
// lowered onto a PayWithItemArgs. Private — callers only ever see the
// PayWithItemArgs that DecodeOfferTradeArgs returns.
type offerTradeArgs struct {
	With     string      `json:"with"`
	Give     payItemList `json:"give"`
	Coins    int         `json:"coins"`
	WantItem string      `json:"want_item"`
	WantQty  int         `json:"want_qty"`
	For      string      `json:"for"`
}

var offerTradeSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "with": {
            "type": "string",
            "minLength": 1,
            "maxLength": 100,
            "description": "Display name of the person in your conversation you want to trade with."
        },
        "give": {
            "type": "array",
            "minItems": 1,
            "maxItems": 8,
            "items": {
                "type": "object",
                "properties": {
                    "item": {"type": "string", "minLength": 1, "maxLength": 64, "description": "Canonical item kind you carry and will hand over (e.g. 'milk', 'nail')."},
                    "qty": {"type": "integer", "minimum": 1, "maximum": 2147483647, "description": "How many of this item you hand over."}
                },
                "required": ["item", "qty"],
                "additionalProperties": false
            },
            "description": "Goods you hand over in the trade. Each line is an item you carry and a quantity. You must give goods, coins, or both."
        },
        "coins": {
            "type": "integer",
            "minimum": 0,
            "maximum": 2147483647,
            "description": "Coins you add to your side of the trade (optional; defaults to 0). You must give goods, coins, or both."
        },
        "want_item": {
            "type": "string",
            "minLength": 1,
            "maxLength": 64,
            "description": "Canonical item kind you want from them (e.g. 'bread', 'stew')."
        },
        "want_qty": {
            "type": "integer",
            "minimum": 1,
            "maximum": 2147483647,
            "description": "How many of want_item you want."
        },
        "for": {
            "type": "string",
            "maxLength": 200,
            "description": "Optional brief note describing what the trade is for."
        }
    },
    "required": ["with", "want_item", "want_qty"],
    "additionalProperties": false
}`)

const offerTradeDescription = "Propose a direct trade (barter) with someone in your current conversation: hand over goods you carry — and/or coins — in exchange for goods they have. " +
	"Set `with` (their name), `give` (the goods you hand over), optional `coins`, and `want_item` + `want_qty` (what you want from them). " +
	"This places a pending offer they can accept, decline, or counter; when they accept, both sides' goods change hands at once. " +
	"Use this whenever you want something another villager is carrying. To sell your own wares to a buyer for coins, use sell instead."

// DecodeOfferTradeArgs parses the raw offer_trade tool-call arguments,
// validates them (mirroring DecodePayWithItemArgs's bounds + rune caps),
// and LOWERS them onto a PayWithItemArgs so the call reuses
// HandlePayWithItem and the existing barter flow. The returned `any` is a
// PayWithItemArgs, not an offerTradeArgs — that is deliberate (see the
// file header): it lets the harness's PayWithItemArgs-typed steer + dedup
// treat an offer_trade exactly like a buyer-initiated barter offer.
//
// Mapping (proposer POV → buyer-centric ledger):
//   - with       → Seller   (the counterparty; they provide want_item)
//   - want_item  → Item      (what flows counterparty→proposer on accept)
//   - want_qty   → Qty
//   - coins      → Amount    (coins the proposer adds)
//   - give       → PayItems  (what flows proposer→counterparty on accept)
//   - consume_now is forced false (handover into inventory, not eat-here);
//     consumers / quote_id / in_response_to / ready_in_days are all unused.
func DecodeOfferTradeArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("offer_trade: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args offerTradeArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("offer_trade: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("offer_trade: trailing data after JSON object")
		}
		return nil, fmt.Errorf("offer_trade: malformed trailing data: %w", err)
	}

	if args.With == "" {
		return nil, modelSafef("offer_trade: with is required")
	}
	if n := utf8.RuneCountInString(args.With); n > MaxPayWithItemNameChars {
		return nil, modelSafef(
			"offer_trade: with exceeds %d-character cap (got %d characters)",
			MaxPayWithItemNameChars, n,
		)
	}
	if args.WantItem == "" {
		return nil, modelSafef("offer_trade: want_item is required")
	}
	if n := utf8.RuneCountInString(args.WantItem); n > MaxPayWithItemItemChars {
		return nil, modelSafef(
			"offer_trade: want_item exceeds %d-character cap (got %d characters)",
			MaxPayWithItemItemChars, n,
		)
	}
	if args.WantQty < 1 {
		return nil, modelSafef("offer_trade: want_qty must be at least 1 (got %d)", args.WantQty)
	}
	if args.WantQty > sim.MaxPayWithItemQty {
		return nil, modelSafef("offer_trade: want_qty exceeds maximum (got %d, max %d)", args.WantQty, sim.MaxPayWithItemQty)
	}
	// Coins are optional (>= 0) — a trade may give goods, coins, or both,
	// but must give at least one. The "must give something" rule is checked
	// after the give-items decode below.
	if args.Coins < 0 {
		return nil, modelSafef("offer_trade: coins cannot be negative (got %d)", args.Coins)
	}
	if args.Coins > sim.MaxPayWithItemAmount {
		return nil, modelSafef("offer_trade: coins exceeds maximum (got %d, max %d)", args.Coins, sim.MaxPayWithItemAmount)
	}
	// Reuse the pay_items decode checks (count cap, rune cap, qty bounds) on
	// the give lines — they share the payItemArg shape — but scope the error
	// messages to the proposer-facing `give` field name.
	if err := validateGiveItemsDecode(args.Give); err != nil {
		return nil, err
	}
	if args.Coins == 0 && len(args.Give) == 0 {
		return nil, modelSafef("offer_trade: trade must give goods or coins (add give lines, set coins, or both)")
	}
	if n := utf8.RuneCountInString(args.For); n > MaxPayWithItemForChars {
		return nil, modelSafef(
			"offer_trade: 'for' text exceeds %d-character cap (got %d characters)",
			MaxPayWithItemForChars, n,
		)
	}

	// Lower onto the buyer-centric PayWithItemArgs. HandlePayWithItem owns
	// the remaining trim / control-char / huddle-bootstrap work and builds
	// the sim.PayWithItem command from here.
	return PayWithItemArgs{
		Seller:     args.With,
		Item:       args.WantItem,
		Qty:        args.WantQty,
		Amount:     args.Coins,
		ConsumeNow: false,
		PayItems:   args.Give,
		For:        args.For,
	}, nil
}

// validateGiveItemsDecode runs the decode-stage checks on offer_trade's
// `give` lines — the same count / rune / qty bounds validatePayItemsDecode
// applies to pay_items, but with `give[i]`-scoped error messages so the
// proposer sees the field name they actually used.
func validateGiveItemsDecode(items []payItemArg) error {
	if len(items) > MaxPayWithItemPayItemsHandler {
		return modelSafef(
			"offer_trade: give exceeds %d-entry cap (got %d)",
			MaxPayWithItemPayItemsHandler, len(items),
		)
	}
	for i, pi := range items {
		if n := utf8.RuneCountInString(pi.Item); n > MaxPayWithItemItemChars {
			return modelSafef(
				"offer_trade: give[%d].item exceeds %d-character cap (got %d characters)",
				i, MaxPayWithItemItemChars, n,
			)
		}
		if pi.Qty < 1 {
			return modelSafef("offer_trade: give[%d].qty must be at least 1 (got %d)", i, pi.Qty)
		}
		if pi.Qty > sim.MaxPayWithItemQty {
			return modelSafef("offer_trade: give[%d].qty exceeds maximum (got %d, max %d)", i, pi.Qty, sim.MaxPayWithItemQty)
		}
	}
	return nil
}
