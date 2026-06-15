package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_handlers.go — Phase 3 PR S4 step 6.
//
// Tool handlers for the five buyer-initiated pay-with-item Commands
// shipped in step 5:
//
//   - pay_with_item   — buyer-side, creates the offer (slow path or
//                       quote fast-path).
//   - accept_pay      — seller-side, accepts a pending offer.
//   - decline_pay     — seller-side, declines a pending offer.
//   - counter_pay     — seller-side, proposes counter terms.
//   - withdraw_pay    — buyer-side, withdraws a pending offer.
//
// Each handler follows the established pattern (PR B pay.go, PR S3
// scene_quote.go):
//
//   1. Narrow JSON schema — advertises only the fields the engine
//      actually uses, with explicit min/max ranges and additionalProperties=false.
//   2. Decoder hardens against null / bare values / unknown fields /
//      trailing data + applies rune-length + numeric bounds.
//   3. Handle<X> is a pure builder — trims, scans control chars,
//      runs static dup-name checks where applicable, returns the
//      sim.<X> Command. World-state validation runs inside the
//      Command Fn on the world goroutine (already shipped in
//      pay_with_item_commands.go).
//
// Co-locating all five handlers because the ledger family is a unit:
// the model can't reach an `accept_pay` flow without first calling
// `pay_with_item`, and the shared LedgerID type + Message-rune cap make
// the handlers cross-reference each other. Five register helpers live
// in register_pay_with_item.go.

// ---- shared constants ------------------------------------------------

// MaxPayWithItemItemChars caps the item-kind length on the model-facing
// schema. Mirrors MaxConsumeItemChars / MaxSceneQuoteItemChars.
const MaxPayWithItemItemChars = 64

// MaxPayWithItemNameChars caps each name field (seller, consumers[i]).
// Mirrors MaxPayRecipientChars / MaxSceneQuoteNameChars.
const MaxPayWithItemNameChars = 100

// MaxPayWithItemForChars caps the optional `for` flavor text. Mirrors
// MaxPayForChars (the PR B pay tool's flavor cap), since the field
// serves the same role on the buyer side here.
const MaxPayWithItemForChars = 200

// MaxPayWithItemConsumersHandler caps len(consumers[]) in the schema.
// Re-enforced by the sim.PayWithItem Command Fn against
// sim.MaxPayWithItemConsumers; the two MUST stay in sync. Schema-side
// cap is defense-in-depth so the LLM can't blow per-tool token budget
// on a runaway consumer list before validation gets a chance.
const MaxPayWithItemConsumersHandler = 8

// MaxPayMessageHandlerRunes caps free-text message fields
// (decline_pay.reason, counter_pay.message, withdraw_pay.message).
// Mirrors sim.MaxPayMessageRunes — schema literal must stay in sync
// with the substrate constant.
const MaxPayMessageHandlerRunes = 220

// MaxPayWithItemPayItemsHandler caps len(pay_items[]) in the
// pay_with_item / counter_pay schemas (the barter goods-payment lines,
// ZBBS-HOME-393). Re-enforced by sim.PayWithItem / sim.CounterPay against
// sim.MaxPayWithItemPayItems; the two MUST stay in sync.
const MaxPayWithItemPayItemsHandler = 8

// payItemArg is one barter goods-payment line as decoded from a tool call:
// a free-text item name + a positive quantity. Shared by pay_with_item
// (the buyer's pay_items) and counter_pay (the seller's counter goods).
// ZBBS-HOME-393.
type payItemArg struct {
	Item string `json:"item"`
	Qty  int    `json:"qty"`
}

// payItemList is the decode type for a barter goods array (pay_items on
// pay_with_item / counter_pay; give on offer_trade). It exists to tolerate a
// quirk of the weak stateful-NPC model (llama-3.3-70b): it INTERMITTENTLY
// emits the array as a STRINGIFIED JSON array — `"[{\"item\":\"milk\",
// \"qty\":2}]"` — instead of a real array. The ZBBS-HOME-407 live-verify
// caught this bouncing ~half of Elizabeth's offer_trade calls (and it hit
// pay_with_item the same way in the original Josiah/Elizabeth episode).
// UnmarshalJSON unwraps a single JSON-string layer before decoding, so both
// the well-formed array and the stringified array parse to the same result;
// a real array is unaffected. The schema still advertises an array (that's
// the form we want the model to send) — this is a tolerance layer, not a
// contract change.
type payItemList []payItemArg

// UnmarshalJSON decodes a goods array that may arrive either as a real JSON
// array or as a JSON string wrapping one (the weak-model stringified-array
// case). Unknown fields in the elements are still rejected and trailing data
// after the array is still an error, so the strict-shape guarantees the rest
// of the pay family relies on are preserved.
func (p *payItemList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	// Empty input isn't valid JSON — encoding/json never hands UnmarshalJSON
	// empty bytes during normal struct decoding, but a direct call might, and
	// silently accepting it would be more lenient than JSON itself.
	if len(trimmed) == 0 {
		return io.ErrUnexpectedEOF
	}
	// json calls UnmarshalJSON for null too — treat it as "no goods".
	if string(trimmed) == "null" {
		*p = nil
		return nil
	}
	// Stringified-array case: unwrap exactly one JSON-string layer, then decode
	// the inner text as the array it was meant to be.
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*p = nil
			return nil
		}
		trimmed = []byte(s)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var arr []payItemArg
	if err := dec.Decode(&arr); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing data after goods array")
		}
		return err
	}
	*p = arr
	return nil
}

// payItemsSchemaFragment is the shared JSON-schema fragment for a
// pay_items array — embedded into both the pay_with_item and counter_pay
// schemas so the two stay identical. maxItems mirrors
// MaxPayWithItemPayItemsHandler; qty max mirrors MaxPayWithItemQty.
const payItemsSchemaFragment = `{
        "type": "array",
        "maxItems": 8,
        "items": {
            "type": "object",
            "properties": {
                "item": {"type": "string", "minLength": 1, "maxLength": 64, "description": "Canonical item kind you carry and offer as payment (e.g. 'nail', 'hammer')."},
                "qty": {"type": "integer", "minimum": 1, "maximum": 2147483647, "description": "How many of this item you offer."}
            },
            "required": ["item", "qty"],
            "additionalProperties": false
        },
        "description": "Optional goods you offer as payment (barter). Each line is an item you carry and a quantity. You must offer coins, goods, or both."
    }`

// ====================================================================
// pay_with_item — buyer-side offer creation
// ====================================================================

// PayWithItemArgs is the decoded shape of the pay_with_item tool's
// arguments. quote_id (fast-path) and in_response_to (counter-chain
// follow-up) are both optional uint64 IDs — zero means "not present,"
// matching LedgerID(0) / QuoteID(0) sentinel semantics in the substrate.
//
// Schema-enforced constraints:
//
//   - seller:         minLength 1, maxLength MaxPayWithItemNameChars
//   - item:           minLength 1, maxLength MaxPayWithItemItemChars
//   - qty:            integer, minimum 1, maximum math.MaxInt32
//   - amount:         integer, minimum 1, maximum math.MaxInt32
//   - consume_now:    required boolean (no default — load-bearing field)
//   - consumers:      array (optional), maxItems MaxPayWithItemConsumersHandler,
//     each entry minLength 1, maxLength MaxPayWithItemNameChars
//   - quote_id:       integer (optional), minimum 1
//   - in_response_to: integer (optional), minimum 1
//   - for:            string (optional), maxLength MaxPayWithItemForChars
type PayWithItemArgs struct {
	Seller       string      `json:"seller"`
	Item         string      `json:"item"`
	Qty          int         `json:"qty"`
	Amount       int         `json:"amount"`
	ConsumeNow   bool        `json:"consume_now"`
	Consumers    []string    `json:"consumers"`
	PayItems     payItemList `json:"pay_items"`
	QuoteID      uint64      `json:"quote_id"`
	InResponseTo uint64      `json:"in_response_to"`
	For          string      `json:"for"`
	ReadyInDays  int         `json:"ready_in_days"`
}

var payWithItemSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "seller": {
            "type": "string",
            "minLength": 1,
            "maxLength": 100,
            "description": "Display name of the person in your conversation you want to offer to."
        },
        "item": {
            "type": "string",
            "minLength": 1,
            "maxLength": 64,
            "description": "Canonical item kind to buy from the seller (e.g. 'ale', 'stew', 'bread')."
        },
        "qty": {
            "type": "integer",
            "minimum": 1,
            "maximum": 2147483647,
            "description": "Quantity per consumer in the bundle."
        },
        "amount": {
            "type": "integer",
            "minimum": 0,
            "maximum": 2147483647,
            "description": "Coins offered for the bundle (optional; defaults to 0). You must offer coins, goods (pay_items), or both. If quote_id is set, must be at least the quote's amount."
        },
        "consume_now": {
            "type": "boolean",
            "description": "True for eat-here / drink-here (immediate consumption); false for takeaway. Some goods (a served meal, a poured drink) can't be carried away — those always settle eat-here regardless."
        },
        "consumers": {
            "type": "array",
            "maxItems": 8,
            "items": {
                "type": "string",
                "minLength": 1,
                "maxLength": 100
            },
            "description": "Optional list of display names for a group order. Empty means you (the buyer) are the sole consumer."
        },
        "pay_items": ` + payItemsSchemaFragment + `,
        "quote_id": {
            "type": "integer",
            "minimum": 1,
            "description": "Optional. Take a posted quote by ID. All terms (item, qty, consume_now, consumers) must match the quote exactly; amount must be at least the quote's amount. Strict-reject on any mismatch. Cannot be combined with pay_items (a quote is a coin price)."
        },
        "in_response_to": {
            "type": "integer",
            "minimum": 1,
            "description": "Optional. Respond to a previously countered offer by its ledger_id. Required to be your own offer that the seller has countered, and recent (within an hour)."
        },
        "for": {
            "type": "string",
            "maxLength": 200,
            "description": "Optional brief note describing what the purchase is for."
        },
        "ready_in_days": {
            "type": "integer",
            "minimum": 0,
            "maximum": 30,
            "description": "Lodging only: book a room starting this many days from now (0 or omitted = a room for tonight). Ignored for anything else — ordinary goods are handed over when you pay. Max 30."
        }
    },
    "required": ["seller", "item", "qty", "consume_now"],
    "additionalProperties": false
}`)

const payWithItemDescription = "Offer to buy items from another villager in your current conversation. " +
	"You set the item, qty (per consumer), and the payment — coins (amount), goods you carry (pay_items), or both — and whether it's eat-here or takeaway. " +
	"Paying with goods is barter: the seller weighs your goods just like a coin offer. " +
	"By default this creates a pending offer the seller must accept, decline, or counter. " +
	"If you pass quote_id, the deal closes instantly when terms match (strict reject on any mismatch — no silent fall-through). " +
	"If you pass in_response_to, you're following up on a counter the seller made on a previous offer of yours."

// DecodePayWithItemArgs parses the raw tool-call arguments into a
// PayWithItemArgs. Errors are typed validation failures surfaced to the
// model as tool errors.
//
// What's checked here:
//   - JSON parses, no trailing data, no unknown fields
//   - required fields present (seller, item, qty, amount, consume_now)
//   - numeric bounds (qty / amount / quote_id / in_response_to)
//   - rune-length caps (seller, item, consumers[i], for)
//   - consumers[] length cap
//
// What's deferred:
//   - Trim-emptiness, control-char scans, dup-name reject → HandlePayWithItem
//   - World-state lookups (huddle, seller resolve, item catalog, quote,
//     parent ledger, stock, coins, break) → sim.PayWithItem on the
//     world goroutine.
func DecodePayWithItemArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("pay_with_item: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args PayWithItemArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("pay_with_item: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("pay_with_item: trailing data after JSON object")
		}
		return nil, fmt.Errorf("pay_with_item: malformed trailing data: %w", err)
	}

	if args.Seller == "" {
		return nil, modelSafef("pay_with_item: seller is required")
	}
	if n := utf8.RuneCountInString(args.Seller); n > MaxPayWithItemNameChars {
		return nil, modelSafef(
			"pay_with_item: seller exceeds %d-character cap (got %d characters)",
			MaxPayWithItemNameChars, n,
		)
	}
	if args.Item == "" {
		return nil, modelSafef("pay_with_item: item is required")
	}
	if n := utf8.RuneCountInString(args.Item); n > MaxPayWithItemItemChars {
		return nil, modelSafef(
			"pay_with_item: item exceeds %d-character cap (got %d characters)",
			MaxPayWithItemItemChars, n,
		)
	}
	if args.Qty < 1 {
		return nil, modelSafef("pay_with_item: qty must be at least 1 (got %d)", args.Qty)
	}
	if args.Qty > sim.MaxPayWithItemQty {
		return nil, modelSafef("pay_with_item: qty exceeds maximum (got %d, max %d)", args.Qty, sim.MaxPayWithItemQty)
	}
	// Coins are optional (>= 0) — an offer may pay with coins, goods
	// (pay_items), or both, but must carry at least one. The "must offer
	// something" rule is checked after pay_items decode below.
	if args.Amount < 0 {
		return nil, modelSafef("pay_with_item: amount cannot be negative (got %d)", args.Amount)
	}
	if args.Amount > sim.MaxPayWithItemAmount {
		return nil, modelSafef("pay_with_item: amount exceeds maximum (got %d, max %d)", args.Amount, sim.MaxPayWithItemAmount)
	}
	if err := validatePayItemsDecode("pay_with_item", args.PayItems); err != nil {
		return nil, err
	}
	if args.Amount == 0 && len(args.PayItems) == 0 {
		return nil, modelSafef("pay_with_item: offer must include coins or goods (set amount, add pay_items, or both)")
	}
	if len(args.Consumers) > MaxPayWithItemConsumersHandler {
		return nil, modelSafef(
			"pay_with_item: consumers exceeds %d-entry cap (got %d)",
			MaxPayWithItemConsumersHandler, len(args.Consumers),
		)
	}
	for i, c := range args.Consumers {
		if n := utf8.RuneCountInString(c); n > MaxPayWithItemNameChars {
			return nil, modelSafef(
				"pay_with_item: consumers[%d] exceeds %d-character cap (got %d characters)",
				i, MaxPayWithItemNameChars, n,
			)
		}
	}
	if n := utf8.RuneCountInString(args.For); n > MaxPayWithItemForChars {
		return nil, modelSafef(
			"pay_with_item: 'for' text exceeds %d-character cap (got %d characters)",
			MaxPayWithItemForChars, n,
		)
	}
	// ready_in_days bounds (ZBBS-HOME-403 advance booking). The substrate
	// re-enforces the same cap and the lodging-only rule; this is defense in
	// depth at intake. Literal 30 mirrors sim.MaxOrderReadyInDays.
	if args.ReadyInDays < 0 {
		return nil, modelSafef("pay_with_item: ready_in_days cannot be negative (got %d)", args.ReadyInDays)
	}
	if args.ReadyInDays > sim.MaxOrderReadyInDays {
		return nil, modelSafef("pay_with_item: ready_in_days too far ahead (got %d, max %d)", args.ReadyInDays, sim.MaxOrderReadyInDays)
	}
	return args, nil
}

// HandlePayWithItem is the CommitFn for the pay_with_item tool. Pure
// builder — does NOT touch the world. Trims, runs control-char scans,
// applies static duplicate-name check on consumers, returns the
// sim.PayWithItem Command.
func HandlePayWithItem(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(PayWithItemArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("pay_with_item: handler received unexpected args type %T", in.Args)
	}

	seller := strings.TrimSpace(args.Seller)
	if seller == "" {
		return sim.Command{}, modelSafef("pay_with_item: seller is empty after trim")
	}
	if i := indexStrictControlChar(seller); i >= 0 {
		return sim.Command{}, modelSafef(
			"pay_with_item: seller contains a disallowed control character at byte offset %d", i)
	}

	item := strings.TrimSpace(args.Item)
	if item == "" {
		return sim.Command{}, modelSafef("pay_with_item: item is empty after trim")
	}
	if i := indexStrictControlChar(item); i >= 0 {
		return sim.Command{}, modelSafef(
			"pay_with_item: item contains a disallowed control character at byte offset %d", i)
	}

	// Normalize the consumer list. Per-entry trim + strict-control-char
	// scan + post-trim non-empty + static dup-name check. The Command
	// Fn does the ActorID-level dup-check after resolution; the static
	// check catches obvious typos before we burn a world-goroutine
	// round-trip.
	var consumers []string
	if len(args.Consumers) > 0 {
		consumers = make([]string, 0, len(args.Consumers))
		seen := make(map[string]struct{}, len(args.Consumers))
		for i, raw := range args.Consumers {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				return sim.Command{}, modelSafef(
					"pay_with_item: consumers[%d] is empty after trim — every consumer must have a name.", i)
			}
			if idx := indexStrictControlChar(trimmed); idx >= 0 {
				return sim.Command{}, modelSafef(
					"pay_with_item: consumers[%d] contains a disallowed control character at byte offset %d", i, idx)
			}
			key := strings.ToLower(trimmed)
			if _, dup := seen[key]; dup {
				return sim.Command{}, modelSafef(
					"pay_with_item: %q appears more than once in the consumer list — list each person only once.", trimmed)
			}
			seen[key] = struct{}{}
			consumers = append(consumers, trimmed)
		}
	}

	// Normalize ForText whitespace + reject control chars (same posture
	// as PR B's pay handler — short-form metadata, no legitimate \n
	// shaping the relationship fact).
	forText := strings.Join(strings.Fields(args.For), " ")
	if forText != "" {
		if i := indexInvalidControlChar(forText); i >= 0 {
			return sim.Command{}, modelSafef(
				"pay_with_item: 'for' contains a disallowed control character at byte offset %d", i)
		}
	}

	payItems, err := buildPayItemInputs("pay_with_item", args.PayItems)
	if err != nil {
		return sim.Command{}, err
	}

	// ZBBS-HOME-400: form/join the co-located huddle on the offer itself so a
	// buyer who walked up to a stall can make the offer on arrival without a
	// separate prior speak (the live restock-thrash). No-op when already
	// huddled, alone, or out of stall scope, so an offer to an absent seller
	// still rejects at the gate exactly as before.
	now := time.Now().UTC()
	return withHuddleBootstrap(in.ActorID, now, sim.PayWithItem(
		in.ActorID,
		seller,
		item,
		args.Qty,
		args.Amount,
		args.ConsumeNow,
		consumers,
		payItems,
		sim.QuoteID(args.QuoteID),
		sim.LedgerID(args.InResponseTo),
		forText,
		now,
		args.ReadyInDays,
	)), nil
}

// ====================================================================
// accept_pay — seller-side accept
// ====================================================================

// AcceptPayArgs is the decoded shape of the accept_pay tool's arguments.
type AcceptPayArgs struct {
	LedgerID uint64 `json:"ledger_id"`
}

var acceptPaySchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "ledger_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric ledger ID of the pending offer to accept. You'll see this in your perception of the buyer's offer."
        }
    },
    "required": ["ledger_id"],
    "additionalProperties": false
}`)

const acceptPayDescription = "Accept a pending offer from a buyer in your current conversation. " +
	"At acceptance time the engine verifies you are both still in the same conversation, you have stock, the buyer has coins, " +
	"and you are not on a break — if any check fails, the offer flips to a terminal failed state and the " +
	"transfer does not happen. On success: coins move to you, items leave your inventory, and (for eat-here " +
	"deals) the consumers' needs are satisfied immediately."

// DecodeAcceptPayArgs parses raw args into an AcceptPayArgs.
func DecodeAcceptPayArgs(raw json.RawMessage) (any, error) {
	args, err := decodeLedgerOnly(raw, "accept_pay")
	if err != nil {
		return nil, err
	}
	return AcceptPayArgs{LedgerID: args.LedgerID}, nil
}

// HandleAcceptPay is the CommitFn for the accept_pay tool. Pure builder.
func HandleAcceptPay(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(AcceptPayArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("accept_pay: handler received unexpected args type %T", in.Args)
	}
	return sim.AcceptPay(in.ActorID, sim.LedgerID(args.LedgerID), time.Now().UTC()), nil
}

// ====================================================================
// decline_pay — seller-side decline
// ====================================================================

// DeclinePayArgs is the decoded shape of the decline_pay tool's arguments.
type DeclinePayArgs struct {
	LedgerID uint64 `json:"ledger_id"`
	Reason   string `json:"reason"`
}

var declinePaySchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "ledger_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric ledger ID of the pending offer to decline."
        },
        "reason": {
            "type": "string",
            "maxLength": 220,
            "description": "Optional short reason for the decline. The buyer sees this in their perception of the resolution."
        }
    },
    "required": ["ledger_id"],
    "additionalProperties": false
}`)

const declinePayDescription = "Decline a pending offer from a buyer in your current conversation. " +
	"No goods or coins move. Optionally include a brief reason the buyer sees in their perception."

// DecodeDeclinePayArgs parses raw args into a DeclinePayArgs.
func DecodeDeclinePayArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("decline_pay: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args DeclinePayArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("decline_pay: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("decline_pay: trailing data after JSON object")
		}
		return nil, fmt.Errorf("decline_pay: malformed trailing data: %w", err)
	}
	if args.LedgerID < 1 {
		return nil, modelSafef("decline_pay: ledger_id must be at least 1 (got %d)", args.LedgerID)
	}
	if n := utf8.RuneCountInString(args.Reason); n > MaxPayMessageHandlerRunes {
		return nil, modelSafef(
			"decline_pay: reason exceeds %d-character cap (got %d characters)",
			MaxPayMessageHandlerRunes, n,
		)
	}
	return args, nil
}

// HandleDeclinePay is the CommitFn for the decline_pay tool.
func HandleDeclinePay(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(DeclinePayArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("decline_pay: handler received unexpected args type %T", in.Args)
	}
	reason := normalizeShortMessage(args.Reason)
	if reason != "" {
		if i := indexInvalidControlChar(reason); i >= 0 {
			return sim.Command{}, modelSafef(
				"decline_pay: reason contains a disallowed control character at byte offset %d", i)
		}
	}
	return sim.DeclinePay(in.ActorID, sim.LedgerID(args.LedgerID), reason, time.Now().UTC()), nil
}

// ====================================================================
// counter_pay — seller-side counter
// ====================================================================

// CounterPayArgs is the decoded shape of the counter_pay tool's arguments.
type CounterPayArgs struct {
	LedgerID uint64      `json:"ledger_id"`
	Amount   int         `json:"amount"`
	PayItems payItemList `json:"pay_items"`
	Message  string      `json:"message"`
}

var counterPaySchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "ledger_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric ledger ID of the pending offer to counter."
        },
        "amount": {
            "type": "integer",
            "minimum": 0,
            "maximum": 2147483647,
            "description": "Your counter-proposal in coins (optional; defaults to 0). You must counter with coins, goods (pay_items), or both. The buyer can respond with a fresh pay_with_item using in_response_to set to this offer's ledger_id."
        },
        "pay_items": ` + payItemsSchemaFragment + `,
        "message": {
            "type": "string",
            "maxLength": 220,
            "description": "Optional short note explaining your counter. The buyer sees this in their perception."
        }
    },
    "required": ["ledger_id"],
    "additionalProperties": false
}`)

const counterPayDescription = "Counter a pending offer with different terms. No coins or goods move. " +
	"You propose new payment — coins (amount), goods you want instead (pay_items), or both — and the buyer can respond by calling pay_with_item with in_response_to set to this ledger's ID."

// DecodeCounterPayArgs parses raw args into a CounterPayArgs.
func DecodeCounterPayArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("counter_pay: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args CounterPayArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("counter_pay: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("counter_pay: trailing data after JSON object")
		}
		return nil, fmt.Errorf("counter_pay: malformed trailing data: %w", err)
	}
	if args.LedgerID < 1 {
		return nil, modelSafef("counter_pay: ledger_id must be at least 1 (got %d)", args.LedgerID)
	}
	// Coins are optional (>= 0) — a counter may propose coins, goods
	// (pay_items), or both, but must propose at least one.
	if args.Amount < 0 {
		return nil, modelSafef("counter_pay: amount cannot be negative (got %d)", args.Amount)
	}
	if args.Amount > sim.MaxPayWithItemAmount {
		return nil, modelSafef("counter_pay: amount exceeds maximum (got %d, max %d)", args.Amount, sim.MaxPayWithItemAmount)
	}
	if err := validatePayItemsDecode("counter_pay", args.PayItems); err != nil {
		return nil, err
	}
	if args.Amount == 0 && len(args.PayItems) == 0 {
		return nil, modelSafef("counter_pay: counter must propose coins or goods (set amount, add pay_items, or both)")
	}
	if n := utf8.RuneCountInString(args.Message); n > MaxPayMessageHandlerRunes {
		return nil, modelSafef(
			"counter_pay: message exceeds %d-character cap (got %d characters)",
			MaxPayMessageHandlerRunes, n,
		)
	}
	return args, nil
}

// HandleCounterPay is the CommitFn for the counter_pay tool.
func HandleCounterPay(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(CounterPayArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("counter_pay: handler received unexpected args type %T", in.Args)
	}
	message := normalizeShortMessage(args.Message)
	if message != "" {
		if i := indexInvalidControlChar(message); i >= 0 {
			return sim.Command{}, modelSafef(
				"counter_pay: message contains a disallowed control character at byte offset %d", i)
		}
	}
	payItems, err := buildPayItemInputs("counter_pay", args.PayItems)
	if err != nil {
		return sim.Command{}, err
	}
	return sim.CounterPay(in.ActorID, sim.LedgerID(args.LedgerID), args.Amount, payItems, message, time.Now().UTC()), nil
}

// ====================================================================
// withdraw_pay — buyer-side withdraw
// ====================================================================

// WithdrawPayArgs is the decoded shape of the withdraw_pay tool's
// arguments.
type WithdrawPayArgs struct {
	LedgerID uint64 `json:"ledger_id"`
	Message  string `json:"message"`
}

var withdrawPaySchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "ledger_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric ledger ID of your own pending offer to withdraw."
        },
        "message": {
            "type": "string",
            "maxLength": 220,
            "description": "Optional short note explaining the withdrawal. The seller sees this in their perception."
        }
    },
    "required": ["ledger_id"],
    "additionalProperties": false
}`)

const withdrawPayDescription = "Withdraw one of your own pending offers. You can do this even if you've left " +
	"the conversation — withdrawal is unilateral. Optionally include a brief note the seller sees."

// DecodeWithdrawPayArgs parses raw args into a WithdrawPayArgs.
func DecodeWithdrawPayArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("withdraw_pay: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args WithdrawPayArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("withdraw_pay: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("withdraw_pay: trailing data after JSON object")
		}
		return nil, fmt.Errorf("withdraw_pay: malformed trailing data: %w", err)
	}
	if args.LedgerID < 1 {
		return nil, modelSafef("withdraw_pay: ledger_id must be at least 1 (got %d)", args.LedgerID)
	}
	if n := utf8.RuneCountInString(args.Message); n > MaxPayMessageHandlerRunes {
		return nil, modelSafef(
			"withdraw_pay: message exceeds %d-character cap (got %d characters)",
			MaxPayMessageHandlerRunes, n,
		)
	}
	return args, nil
}

// HandleWithdrawPay is the CommitFn for the withdraw_pay tool.
func HandleWithdrawPay(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(WithdrawPayArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("withdraw_pay: handler received unexpected args type %T", in.Args)
	}
	message := normalizeShortMessage(args.Message)
	if message != "" {
		if i := indexInvalidControlChar(message); i >= 0 {
			return sim.Command{}, modelSafef(
				"withdraw_pay: message contains a disallowed control character at byte offset %d", i)
		}
	}
	return sim.WithdrawPay(in.ActorID, sim.LedgerID(args.LedgerID), message, time.Now().UTC()), nil
}

// ---- shared helpers --------------------------------------------------

// ledgerOnlyArgs is the inner shape decoded for accept_pay (and the
// shared prelude of decline / counter / withdraw). Kept private — the
// public AcceptPayArgs / DeclinePayArgs / etc. carry the per-tool fields.
type ledgerOnlyArgs struct {
	LedgerID uint64 `json:"ledger_id"`
}

// decodeLedgerOnly handles the strict-object / no-trailing / unknown-
// fields / minimum=1 boilerplate for tools that take only a ledger_id.
// Accept_pay is the only such tool; decline / counter / withdraw repeat
// the same checks inline because they each have additional fields.
func decodeLedgerOnly(raw json.RawMessage, toolName string) (ledgerOnlyArgs, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ledgerOnlyArgs{}, modelSafef("%s: arguments must be a JSON object", toolName)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args ledgerOnlyArgs
	if err := dec.Decode(&args); err != nil {
		return ledgerOnlyArgs{}, fmt.Errorf("%s: malformed arguments: %w", toolName, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return ledgerOnlyArgs{}, modelSafef("%s: trailing data after JSON object", toolName)
		}
		return ledgerOnlyArgs{}, fmt.Errorf("%s: malformed trailing data: %w", toolName, err)
	}
	if args.LedgerID < 1 {
		return ledgerOnlyArgs{}, modelSafef("%s: ledger_id must be at least 1 (got %d)", toolName, args.LedgerID)
	}
	return args, nil
}

// normalizeShortMessage trims surrounding whitespace and collapses runs
// of internal whitespace to single spaces (same posture as PR B pay's
// ForText). Used for decline reason / counter message / withdraw note —
// all short-form metadata that lands on PayLedgerEntry.Message and
// gets rendered into the other party's perception prompt, where a
// literal newline would split the warrant line.
func normalizeShortMessage(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// validatePayItemsDecode runs the decode-stage checks on a barter
// pay_items array (ZBBS-HOME-393): count cap, per-item rune cap, and qty
// bounds. Trim-emptiness, control-char scans, dup-kind reject, and item
// resolution are deferred to buildPayItemInputs / the Command Fn (the
// same split pay_with_item's other fields use). toolName scopes the
// error messages.
func validatePayItemsDecode(toolName string, items []payItemArg) error {
	if len(items) > MaxPayWithItemPayItemsHandler {
		return modelSafef(
			"%s: pay_items exceeds %d-entry cap (got %d)",
			toolName, MaxPayWithItemPayItemsHandler, len(items),
		)
	}
	for i, pi := range items {
		if n := utf8.RuneCountInString(pi.Item); n > MaxPayWithItemItemChars {
			return modelSafef(
				"%s: pay_items[%d].item exceeds %d-character cap (got %d characters)",
				toolName, i, MaxPayWithItemItemChars, n,
			)
		}
		if pi.Qty < 1 {
			return modelSafef("%s: pay_items[%d].qty must be at least 1 (got %d)", toolName, i, pi.Qty)
		}
		if pi.Qty > sim.MaxPayWithItemQty {
			return modelSafef("%s: pay_items[%d].qty exceeds maximum (got %d, max %d)", toolName, i, pi.Qty, sim.MaxPayWithItemQty)
		}
	}
	return nil
}

// buildPayItemInputs is the pure-builder counterpart to
// validatePayItemsDecode: it trims each item name, rejects control chars,
// catches obvious duplicate names statically (the Command Fn does the
// canonical-kind dedup after resolution), and returns the []sim.PayItemInput
// the Command consumes. ZBBS-HOME-393.
func buildPayItemInputs(toolName string, items []payItemArg) ([]sim.PayItemInput, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]sim.PayItemInput, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for i, pi := range items {
		item := strings.TrimSpace(pi.Item)
		if item == "" {
			return nil, modelSafef("%s: pay_items[%d].item is empty after trim", toolName, i)
		}
		if idx := indexStrictControlChar(item); idx >= 0 {
			return nil, modelSafef(
				"%s: pay_items[%d].item contains a disallowed control character at byte offset %d", toolName, i, idx)
		}
		key := strings.ToLower(item)
		if _, dup := seen[key]; dup {
			return nil, modelSafef(
				"%s: %q appears more than once in pay_items — combine it into a single line.", toolName, item)
		}
		seen[key] = struct{}{}
		out = append(out, sim.PayItemInput{Item: item, Qty: pi.Qty})
	}
	return out, nil
}
