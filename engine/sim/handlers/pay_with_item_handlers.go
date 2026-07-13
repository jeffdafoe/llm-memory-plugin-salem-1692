package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
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

// MaxPayMessageHandlerRunes caps withdraw_pay's free-text message field.
// Mirrors sim.MaxPayMessageRunes — schema literal must stay in sync
// with the substrate constant.
//
// decline_pay and counter_pay no longer carry a silent note of their own
// (LLM-350): their words are spoken through `say`, capped at MaxSpeakTextChars
// like every other utterance, and sim.DeclinePay / sim.CounterPay truncate what
// they record on PayLedgerEntry.Message to sim.MaxPayMessageRunes. withdraw_pay
// keeps its message — it is unilateral and reaches no one in the room, and no cue
// pairs it with a speak, so there is no live failure to fix and no reason to widen
// this ticket to it (the same call LLM-346 made for solicit_work).
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

// LenientID is the decode type for a uint64 tool-arg identifier:
// quote_id / in_response_to on pay_with_item, ledger_id on the accept /
// decline / counter / withdraw family, and order_id on deliver_order. It
// exists to tolerate the same weak stateful-NPC model (llama-3.3-70b) that
// payItemList does — here, the model INTERMITTENTLY emits the STRING
// "null" (or "", or a bare numeric string like "42") for an identifier
// instead of a JSON number, a real JSON null, or omission. A plain uint64
// field hard-fails the WHOLE argument decode on any of those, and because
// that failure is a raw encoding/json error (not a hand-authored
// modelSafeError) it surfaces to the model as an opaque "argument decode
// failed" rather than a correctable reason — so the model retries the same
// payload and loops. LLM-42: a horseshoe-for-cheese barter reject-retried
// for ~7.5 minutes of dead air because every pay_with_item carried
// quote_id:"null" / in_response_to:"null".
//
// UnmarshalJSON coerces null / "null" / "" → 0 (the unset sentinel the pay
// path already gates on) and parses a bare numeric string as the id. For an
// OPTIONAL id (quote_id, in_response_to) a coerced 0 means "no quote / no
// parent" — the correct plain-offer path. For a REQUIRED id the downstream
// `< 1` checks (decodeLedgerOnly, the counter / withdraw / decline /
// deliver decoders) now fire on the coerced 0 and return a model-safe
// "ledger_id must be at least 1" instead of the opaque decode failure. A
// real integer is unaffected; the schemas still advertise integer /
// minimum-1 — this is a tolerance layer, not a contract change (mirrors
// payItemList, ZBBS-HOME-407).
type LenientID uint64

// UnmarshalJSON decodes an identifier that may arrive as a JSON number, a
// real JSON null, or — the weak-model cases — the string "null", an empty
// string, or a numeric string. Genuinely malformed input (a non-numeric
// string, a float, a negative, a boolean/object/array) is still rejected;
// the non-numeric-string case returns a model-safe reason so the model can
// self-correct rather than loop on an opaque failure.
func (id *LenientID) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	// encoding/json never hands UnmarshalJSON empty bytes during normal
	// struct decoding, but a direct call might — and silently accepting it
	// would be more lenient than JSON itself.
	if len(trimmed) == 0 {
		return io.ErrUnexpectedEOF
	}
	// json calls UnmarshalJSON for null too — treat it as "unset".
	if string(trimmed) == "null" {
		*id = 0
		return nil
	}
	// String forms: the weak model wraps the value (or the literal word
	// "null") in quotes. Unwrap exactly one JSON-string layer, then treat
	// empty / "null" as unset and a numeric string as the id.
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" || s == "null" {
			*id = 0
			return nil
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return modelSafef("id %q is not a non-negative integer", s)
		}
		*id = LenientID(n)
		return nil
	}
	// Real JSON number — decode strictly into uint64, which rejects floats,
	// negatives, and overflow exactly as the bare field did.
	var n uint64
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return err
	}
	*id = LenientID(n)
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
//   - say:            optional, maxLength MaxSpeakTextChars (LLM-350)
type PayWithItemArgs struct {
	Seller       string      `json:"seller"`
	Item         string      `json:"item"`
	Qty          int         `json:"qty"`
	Amount       int         `json:"amount"`
	ConsumeNow   bool        `json:"consume_now"`
	Consumers    []string    `json:"consumers"`
	PayItems     payItemList `json:"pay_items"`
	QuoteID      LenientID   `json:"quote_id"`
	InResponseTo LenientID   `json:"in_response_to"`
	For          string      `json:"for"`
	ReadyInDays  int         `json:"ready_in_days"`
	// Deposit is the coins to put down NOW on a made-to-order commission
	// (LLM-357); the balance settles at pickup. Coin-only, takeaway only, and
	// only honored when the offer resolves to a commission. 0 = pay in full.
	Deposit int `json:"deposit"`
	// Say is the buyer's spoken line, delivered as the offer is placed (LLM-350).
	// The restock cue used to ask for "a brief handoff line" via a separate speak
	// after the pay_with_item, which the terminal offer had already made
	// unreachable. Optional: a wordless offer is legal.
	Say string `json:"say"`
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
        },
        "deposit": {
            "type": "integer",
            "minimum": 0,
            "maximum": 2147483647,
            "description": "Optional down payment for a made-to-order good the seller must still craft: pay this many coins now and settle the rest when you collect it. Must be less than amount, coin-only (no pay_items), and takeaway (consume_now=false). Omit or 0 to pay in full up front. Ignored unless the order is a genuine commission — the seller makes this good and is out of stock."
        },
        "say": {
            "type": "string",
            "maxLength": 1000,
            "description": "What you say aloud as you make the offer, in your own voice (e.g. 'Four coins for a bowl of your stew, if you'll have it.'). Spoken to the seller. Optional: omit to offer without a word."
        }
    },
    "required": ["seller", "item", "qty", "consume_now"],
    "additionalProperties": false
}`)

const payWithItemDescription = "Offer to buy items from another villager in your current conversation. " +
	"You set the item, qty (per consumer), and the payment — coins (amount), goods you carry (pay_items), or both — and whether it's eat-here or takeaway. " +
	"Paying with goods is barter: the seller weighs your goods just like a coin offer. " +
	"By default this creates a pending offer the seller must accept, decline, or counter. " +
	"Say your piece in the same breath by passing `say` — do NOT speak first and then call this, because speaking ends your turn and the offer would never be made. " +
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
	// Decode into a wire struct that tolerates the weak model's scalar shapes
	// (LLM-377): string-wrapped numbers, a string boolean, and the coin
	// synonyms `coins` / `payment.coins` it learned from offer_trade. The
	// public PayWithItemArgs stays plain int/bool, so HandlePayWithItem and the
	// substrate are untouched — this is a decode-only tolerance layer.
	var wire struct {
		Seller       string       `json:"seller"`
		Item         string       `json:"item"`
		Qty          LenientInt   `json:"qty"`
		Amount       LenientInt   `json:"amount"`
		Coins        LenientInt   `json:"coins"`   // synonym → amount
		Payment      *coinPayment `json:"payment"` // synonym → amount
		ConsumeNow   LenientBool  `json:"consume_now"`
		Consumers    []string     `json:"consumers"`
		PayItems     payItemList  `json:"pay_items"`
		QuoteID      LenientID    `json:"quote_id"`
		InResponseTo LenientID    `json:"in_response_to"`
		For          string       `json:"for"`
		ReadyInDays  LenientInt   `json:"ready_in_days"`
		Deposit      LenientInt   `json:"deposit"`
		Say          string       `json:"say"`
	}
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("pay_with_item: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("pay_with_item: trailing data after JSON object")
		}
		return nil, fmt.Errorf("pay_with_item: malformed trailing data: %w", err)
	}
	args := PayWithItemArgs{
		Seller:       wire.Seller,
		Item:         wire.Item,
		Qty:          int(wire.Qty),
		Amount:       resolveCoinAmount(int(wire.Amount), wire.Coins, wire.Payment),
		ConsumeNow:   bool(wire.ConsumeNow),
		Consumers:    wire.Consumers,
		PayItems:     wire.PayItems,
		QuoteID:      wire.QuoteID,
		InResponseTo: wire.InResponseTo,
		For:          wire.For,
		ReadyInDays:  int(wire.ReadyInDays),
		Deposit:      int(wire.Deposit),
		Say:          wire.Say,
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
	if err := validatePayItemsDecode("pay_with_item", "pay_items", args.PayItems); err != nil {
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
	// deposit bounds (partial-payment commission, LLM-357). The substrate
	// re-enforces coin-only / takeaway / < amount and only honors it for a real
	// commission; this is defense in depth at intake.
	if args.Deposit < 0 {
		return nil, modelSafef("pay_with_item: deposit cannot be negative (got %d)", args.Deposit)
	}
	if args.Deposit > 0 && args.Deposit >= args.Amount {
		return nil, modelSafef("pay_with_item: deposit %d must be less than the amount %d — a deposit is a partial payment; omit it to pay in full", args.Deposit, args.Amount)
	}
	// say shares speak's rune cap — it lands on the same utterance path (LLM-350).
	if n := utf8.RuneCountInString(args.Say); n > MaxSpeakTextChars {
		return nil, modelSafef(
			"pay_with_item: say exceeds %d-character cap (got %d characters)", MaxSpeakTextChars, n)
	}
	// Same utterance path ⇒ same mojibake guard as speak (LLM-235).
	if err := checkUtteranceText("pay_with_item", "say", args.Say); err != nil {
		return nil, err
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

	// LLM-290: coins named as the good to buy are currency, not an item —
	// observed live as pay_with_item(item:"coins"). The intent is unambiguous
	// (a closed token list), so TRANSLATE to the pay flow instead of steering:
	// recipient = seller, coins = amount when set (the schema's "coins offered"
	// field is authoritative), else qty (the "pay 5 coins" as qty=5 shape; the
	// schema requires qty >= 1, so the payment is never zero). sim.Pay applies
	// the full payment validation (huddle gate, recipient resolve, balance,
	// the quote/lodging/labor mis-pay guards), and commitResultContent voices
	// the settle so the model doesn't wait on an offer that isn't pending.
	// consume_now/consumers are meaningless for currency and are dropped;
	// a coin item alongside pay_items GOODS is a sale shape, steered instead
	// (offer_trade's want_item=coins case steers at decode, so this branch
	// only ever sees genuine pay_with_item calls).
	if sim.IsCoinToken(item) {
		if len(args.PayItems) > 0 {
			return sim.Command{}, modelSafef(
				"pay_with_item: you named coins as the good while also offering goods in payment — that shape is a sale, not a buy. To sell your goods for coins, post them with sell; to just hand over coins, use pay.")
		}
		// A quote take must name the QUOTED GOOD — translating this shape to a
		// bare payment would move coins while the quote stays open and the
		// goods never change hands (sim.Pay's LLM-172 guard only fires when
		// the pay's for-text names the good, which a coin call's doesn't).
		// Steer to the correct take instead (code_review, round 1).
		if args.QuoteID != 0 {
			return sim.Command{}, modelSafef(
				"pay_with_item: quote_id %d names goods to receive — coins are the payment amount, not the item. Call pay_with_item with quote_id %d, the quoted item name, and your coins in amount.",
				args.QuoteID, args.QuoteID)
		}
		coins := args.Amount
		if coins <= 0 {
			coins = args.Qty
		}
		forText := strings.Join(strings.Fields(args.For), " ")
		if forText != "" {
			if i := indexInvalidControlChar(forText); i >= 0 {
				return sim.Command{}, modelSafef(
					"pay_with_item: 'for' contains a disallowed control character at byte offset %d", i)
			}
		}
		say, err := normalizeSayLine("pay_with_item", args.Say)
		if err != nil {
			return sim.Command{}, err
		}
		now := time.Now().UTC()
		actorID := in.ActorID
		hasNewNews := in.HasNewNews
		pay := sim.Pay(actorID, seller, coins, forText, now)
		if say == "" {
			return withHuddleBootstrap(actorID, now, pay), nil
		}
		// The translated payment is still a pay_with_item call, and pay_with_item is
		// tick-terminal — so the buyer's line has to ride along here too (LLM-350),
		// even though sim.Pay itself is not. Payment first, then the words.
		return withHuddleBootstrap(actorID, now, sim.Command{Fn: func(w *sim.World) (any, error) {
			res, err := pay.Fn(w)
			if err != nil {
				return nil, err
			}
			out := payCoinTranslationResult{Result: res}
			out.Announced, out.SayRefused = sim.SpeakAlongside(
				w, actorID, say, seller, hasNewNews, now, "pay_with_item handed over coins")
			return out, nil
		}}), nil
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

	amount, payItemArgs, err := foldCoinPayItems(args.Amount, args.PayItems)
	if err != nil {
		return sim.Command{}, err
	}

	payItems, err := buildPayItemInputs("pay_with_item", "pay_items", payItemArgs)
	if err != nil {
		return sim.Command{}, err
	}

	say, err := normalizeSayLine("pay_with_item", args.Say)
	if err != nil {
		return sim.Command{}, err
	}

	// ZBBS-HOME-400: form/join the co-located huddle on the offer itself so a
	// buyer who walked up to a stall can make the offer on arrival without a
	// separate prior speak (the live restock-thrash). No-op when already
	// huddled, alone, or out of stall scope, so an offer to an absent seller
	// still rejects at the gate exactly as before.
	now := time.Now().UTC()
	actorID := in.ActorID
	hasNewNews := in.HasNewNews
	offer := sim.PayWithItem(
		actorID,
		seller,
		item,
		args.Qty,
		amount,
		args.ConsumeNow,
		consumers,
		payItems,
		sim.QuoteID(args.QuoteID),
		sim.LedgerID(args.InResponseTo),
		forText,
		now,
		sim.PayWithItemOpts{ReadyInDays: args.ReadyInDays, Deposit: args.Deposit},
	)
	if say == "" {
		return withHuddleBootstrap(actorID, now, offer), nil
	}

	// Offer FIRST, then the words — sell's ordering (LLM-343), for its reason: an
	// offer the world refuses (no such seller, no stock, terms that don't match the
	// quote) leaves the buyer silent rather than haggling over a deal that was never
	// placed. The speak is best-effort the other way; see sim.SpeakAlongside.
	return withHuddleBootstrap(actorID, now, sim.Command{Fn: func(w *sim.World) (any, error) {
		res, err := offer.Fn(w)
		if err != nil {
			return nil, err
		}
		placed, ok := res.(sim.PayWithItemResult)
		if !ok {
			return res, nil
		}
		// Speak only for an offer that actually stands: Pending (the slow path) or
		// Accepted (a quote take, settled inside this call). sim.PayWithItem returns
		// no other state on a nil error today — the failure modes reject before
		// minting — but the wrapper must not ASSUME that, or a future failed-terminal
		// return would have the buyer haggling over a dead offer. The assumption is
		// pinned by TestPayWithItem_ReturnsOnlyLivedStatesOnSuccess (code_review).
		switch placed.State {
		case sim.PayLedgerStatePending, sim.PayLedgerStateAccepted:
			// The offer names one seller, so the buyer's line is spoken to them.
			placed.Announced, placed.SayRefused = sim.SpeakAlongside(
				w, actorID, say, seller, hasNewNews, now,
				fmt.Sprintf("pay_with_item placed offer %d", placed.LedgerID),
			)
		}
		return placed, nil
	}}), nil
}

// ====================================================================
// the spoken line the three pay RESPONSES fold in (LLM-350)
// ====================================================================

// payResponseResult is what a pay RESPONSE tool returns once it carries a `say`:
// the ledger state the response resolved to, plus the fate of the spoken line.
// Announced is true when the words went out; SayRefused carries SpeakTo's own
// model-facing reason when the response landed but the speech did not. The
// response stands either way (see sim.SpeakAlongside).
//
// A response with no `say` returns the bare sim.PayLedgerState, exactly as it did
// before this ticket — payResponseState unwraps both shapes for the harness.
type payResponseResult struct {
	State      sim.PayLedgerState
	Announced  bool
	SayRefused string
}

// payCoinTranslationResult wraps sim.Pay's result on pay_with_item's coin-token
// translation path (LLM-290), so the buyer's folded `say` can be echoed back the
// way every other folded line is. sim.Pay has no result shape of its own to carry
// Announced / SayRefused, and the translated call is still a pay_with_item — a
// terminal tool — so its speech has nowhere else to be reported. Without this the
// one path that speaks would be the one path that never told the model whether the
// room heard it (code_review).
type payCoinTranslationResult struct {
	Result     any
	Announced  bool
	SayRefused string
}

// payResponseState unwraps a pay-response commit result into the ledger state and
// the spoken line's fate, tolerating both the bare-state (no `say`) and the
// payResponseResult (with `say`) shapes. ok is false for any other result type.
func payResponseState(cmdResult any) (state sim.PayLedgerState, announced bool, sayRefused string, ok bool) {
	switch r := cmdResult.(type) {
	case sim.PayLedgerState:
		return r, false, "", true
	case payResponseResult:
		return r.State, r.Announced, r.SayRefused, true
	default:
		return "", false, "", false
	}
}

// payCounterpartyName resolves the display name of the party on the other side of
// ledgerID from callerID — the buyer, when a seller answers. Empty when it can't
// be resolved, which addresses the utterance to the whole huddle rather than
// failing: SpeakTo's vocative gate must never cost the seller their answer.
func payCounterpartyName(w *sim.World, callerID sim.ActorID, ledgerID sim.LedgerID) string {
	entry, ok := w.PayLedger[ledgerID]
	if !ok || entry == nil {
		return ""
	}
	other := entry.BuyerID
	if other == callerID {
		other = entry.SellerID
	}
	peer, ok := w.Actors[other]
	if !ok || peer == nil {
		return ""
	}
	return peer.DisplayName
}

// respondAndSpeak wraps a terminal pay-response Command so the caller's spoken
// line goes out as the response commits, in one terminal act (LLM-350). Returns
// cmd unchanged when there is nothing to say.
//
// Response FIRST, then the words — the LLM-343 ordering, for the LLM-343 reason.
// A response that falls through to a failed terminal (the buyer's coins are gone,
// the stock ran out, either party walked off) settled nothing, and `landed` reports
// that: the seller stays silent rather than thanking a customer for a sale that
// did not happen. Nothing the pay responses do can gate the speech that follows —
// they move no one and dissolve no huddle — which is why this composite lives at
// the handler layer, where accept_work's cannot.
func respondAndSpeak(
	actorID sim.ActorID,
	ledgerID sim.LedgerID,
	say string,
	hasNewNews bool,
	now time.Time,
	act string,
	landed func(sim.PayLedgerState) bool,
	cmd sim.Command,
) sim.Command {
	if say == "" {
		return cmd
	}
	return sim.Command{Fn: func(w *sim.World) (any, error) {
		to := payCounterpartyName(w, actorID, ledgerID)
		res, err := cmd.Fn(w)
		if err != nil {
			return nil, err
		}
		state, ok := res.(sim.PayLedgerState)
		if !ok {
			return res, nil
		}
		out := payResponseResult{State: state}
		if !landed(state) {
			return out, nil
		}
		out.Announced, out.SayRefused = sim.SpeakAlongside(w, actorID, say, to, hasNewNews, now, act)
		return out, nil
	}}
}

// normalizeSayLine trims a folded `say` line and runs speak's permissive control-char
// scan (\n \r \t allowed — it is prose, not an identifier). Returns the trimmed
// text, or a model-safe error naming toolName.
func normalizeSayLine(toolName, say string) (string, error) {
	trimmed := strings.TrimSpace(say)
	if trimmed == "" {
		return "", nil
	}
	if i := indexInvalidControlChar(trimmed); i >= 0 {
		return "", modelSafef(
			"%s: say contains a disallowed control character at byte offset %d "+
				"(only \\n, \\r, \\t allowed)", toolName, i)
	}
	return trimmed, nil
}

// ====================================================================
// accept_pay — seller-side accept
// ====================================================================

// AcceptPayArgs is the decoded shape of the accept_pay tool's arguments.
type AcceptPayArgs struct {
	LedgerID LenientID `json:"ledger_id"`
	// Say is the seller's spoken line, delivered as the sale settles (LLM-350).
	// accept_pay and speak are both tick-terminal, so a seller who answered with
	// speak never reached the accept, and one who accepted first had the speak
	// skipped as post_terminal — either way the transaction passed in silence.
	// Optional: a wordless accept is still legal.
	Say string `json:"say"`
}

var acceptPaySchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "ledger_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric ledger ID of the pending offer to accept. You'll see this in your perception of the buyer's offer."
        },
        "say": {
            "type": "string",
            "maxLength": 1000,
            "description": "What you say aloud as you take their coins, in your own voice (e.g. 'Six coins it is — here's your stew and bread.'). Spoken to the buyer. Optional: omit to settle without a word."
        }
    },
    "required": ["ledger_id"],
    "additionalProperties": false
}`)

const acceptPayDescription = "Accept a pending offer from a buyer in your current conversation. " +
	"At acceptance time the engine verifies you are both still in the same conversation, you have stock, the buyer has coins, " +
	"and you are not on a break — if any check fails, the offer flips to a terminal failed state and the " +
	"transfer does not happen. On success: coins move to you, items leave your inventory, and (for eat-here " +
	"deals) the consumers' needs are satisfied immediately. " +
	"Answer them aloud in the same breath by passing `say` — do NOT reply with the speak tool, because speaking ends your turn and the offer would go unanswered."

// DecodeAcceptPayArgs parses raw args into an AcceptPayArgs.
func DecodeAcceptPayArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("accept_pay: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args AcceptPayArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("accept_pay: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("accept_pay: trailing data after JSON object")
		}
		return nil, fmt.Errorf("accept_pay: malformed trailing data: %w", err)
	}
	if args.LedgerID < 1 {
		return nil, modelSafef("accept_pay: ledger_id must be at least 1 (got %d)", args.LedgerID)
	}
	if n := utf8.RuneCountInString(args.Say); n > MaxSpeakTextChars {
		return nil, modelSafef(
			"accept_pay: say exceeds %d-character cap (got %d characters)", MaxSpeakTextChars, n)
	}
	// Same utterance path ⇒ same mojibake guard as speak (LLM-235).
	if err := checkUtteranceText("accept_pay", "say", args.Say); err != nil {
		return nil, err
	}
	return args, nil
}

// HandleAcceptPay is the CommitFn for the accept_pay tool. Pure builder — the
// spoken line rides along on the settle itself (LLM-350).
func HandleAcceptPay(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(AcceptPayArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("accept_pay: handler received unexpected args type %T", in.Args)
	}
	say, err := normalizeSayLine("accept_pay", args.Say)
	if err != nil {
		return sim.Command{}, err
	}
	now := time.Now().UTC()
	ledgerID := sim.LedgerID(args.LedgerID)
	return respondAndSpeak(
		in.ActorID, ledgerID, say, in.HasNewNews, now,
		fmt.Sprintf("accept_pay settled ledger %d", ledgerID),
		func(s sim.PayLedgerState) bool { return s == sim.PayLedgerStateAccepted },
		sim.AcceptPay(in.ActorID, ledgerID, now),
	), nil
}

// ====================================================================
// decline_pay — seller-side decline
// ====================================================================

// DeclinePayArgs is the decoded shape of the decline_pay tool's arguments.
//
// Say folds in the field this tool used to call `reason` (LLM-350). They were
// always the same thing — the seller's reason for refusing — but `reason` was
// never spoken and never reached the buyer: renderPayResolvedWarrantLine drops
// PayLedgerEntry.Message, and CounterOfferView withholds it on purpose as a
// cross-actor prompt-injection surface. Only the umbilical operator console ever
// read it. Advertising a silent `reason` NEXT TO a spoken `say` would just move
// this ticket's bug one layer down — its own description promised the buyer would
// see it — so there is one field. It is spoken, and it still lands on the ledger.
type DeclinePayArgs struct {
	LedgerID LenientID `json:"ledger_id"`
	Say      string    `json:"say"`
}

var declinePaySchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "ledger_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric ledger ID of the pending offer to decline."
        },
        "say": {
            "type": "string",
            "maxLength": 1000,
            "description": "What you say aloud as you refuse, in your own voice (e.g. 'I've none to spare today — come back tomorrow.'). Spoken to the buyer. Optional: omit to decline without a word."
        }
    },
    "required": ["ledger_id"],
    "additionalProperties": false
}`)

const declinePayDescription = "Decline a pending offer from a buyer in your current conversation. No goods or coins move. " +
	"Refuse them aloud in the same breath by passing `say` — do NOT reply with the speak tool, because speaking ends your turn and the offer would go unanswered."

// DecodeDeclinePayArgs parses raw args into a DeclinePayArgs.
//
// Alias: `reason` is tolerated as a decode-only name for `say` (the pre-LLM-350
// field), so a model that reaches for it still lands the decline WITH its words.
// A non-empty `say` wins. Per-tool by design, exactly like sell's item_kind→item
// alias (LLM-326) and speak's message→text (LLM-315) — never a global alias.
func DecodeDeclinePayArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("decline_pay: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire struct {
		LedgerID LenientID `json:"ledger_id"`
		Say      string    `json:"say"`
		Reason   string    `json:"reason"` // decode-only alias (LLM-350)
	}
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("decline_pay: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("decline_pay: trailing data after JSON object")
		}
		return nil, fmt.Errorf("decline_pay: malformed trailing data: %w", err)
	}
	if wire.LedgerID < 1 {
		return nil, modelSafef("decline_pay: ledger_id must be at least 1 (got %d)", wire.LedgerID)
	}
	say, err := selectSayAlias("decline_pay", "reason", wire.Say, wire.Reason)
	if err != nil {
		return nil, err
	}
	return DeclinePayArgs{LedgerID: wire.LedgerID, Say: say}, nil
}

// HandleDeclinePay is the CommitFn for the decline_pay tool. The refusal is
// spoken as it commits, and the same words are recorded on the ledger entry —
// where sim.DeclinePay truncates them to MaxPayMessageRunes for the operator
// console. The two normalizations differ on purpose: speech keeps its line
// breaks, the ledger field does not.
func HandleDeclinePay(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(DeclinePayArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("decline_pay: handler received unexpected args type %T", in.Args)
	}
	say, err := normalizeSayLine("decline_pay", args.Say)
	if err != nil {
		return sim.Command{}, err
	}
	now := time.Now().UTC()
	ledgerID := sim.LedgerID(args.LedgerID)
	return respondAndSpeak(
		in.ActorID, ledgerID, say, in.HasNewNews, now,
		fmt.Sprintf("decline_pay refused ledger %d", ledgerID),
		func(s sim.PayLedgerState) bool { return s == sim.PayLedgerStateDeclined },
		sim.DeclinePay(in.ActorID, ledgerID, normalizeShortMessage(say), now),
	), nil
}

// ====================================================================
// counter_pay — seller-side counter
// ====================================================================

// CounterPayArgs is the decoded shape of the counter_pay tool's arguments. Say
// folds in the old `message` field for the reasons DeclinePayArgs gives.
type CounterPayArgs struct {
	LedgerID LenientID   `json:"ledger_id"`
	Amount   int         `json:"amount"`
	PayItems payItemList `json:"pay_items"`
	Say      string      `json:"say"`
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
        "say": {
            "type": "string",
            "maxLength": 1000,
            "description": "What you say aloud as you counter, in your own voice (e.g. 'Four is too little for a whole loaf — six, and it's yours.'). Spoken to the buyer. Optional: omit to counter without a word."
        }
    },
    "required": ["ledger_id"],
    "additionalProperties": false
}`)

const counterPayDescription = "Counter a pending offer with different terms. No coins or goods move. " +
	"You propose new payment — coins (amount), goods you want instead (pay_items), or both — and the buyer can respond by calling pay_with_item with in_response_to set to this ledger's ID. " +
	"Name your terms aloud in the same breath by passing `say` — do NOT reply with the speak tool, because speaking ends your turn and the offer would go unanswered."

// DecodeCounterPayArgs parses raw args into a CounterPayArgs. `message` is
// tolerated as a decode-only alias for `say` (see DecodeDeclinePayArgs).
func DecodeCounterPayArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("counter_pay: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire struct {
		LedgerID LenientID    `json:"ledger_id"`
		Amount   LenientInt   `json:"amount"`
		Coins    LenientInt   `json:"coins"`   // synonym → amount (LLM-377)
		Payment  *coinPayment `json:"payment"` // synonym → amount (LLM-377)
		PayItems payItemList  `json:"pay_items"`
		Say      string       `json:"say"`
		Message  string       `json:"message"` // decode-only alias (LLM-350)
	}
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("counter_pay: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("counter_pay: trailing data after JSON object")
		}
		return nil, fmt.Errorf("counter_pay: malformed trailing data: %w", err)
	}
	if wire.LedgerID < 1 {
		return nil, modelSafef("counter_pay: ledger_id must be at least 1 (got %d)", wire.LedgerID)
	}
	// Coins are optional (>= 0) — a counter may propose coins, goods
	// (pay_items), or both, but must propose at least one. Fold the weak-model
	// coin synonyms (coins / payment.coins) into amount (LLM-377).
	amount := resolveCoinAmount(int(wire.Amount), wire.Coins, wire.Payment)
	if amount < 0 {
		return nil, modelSafef("counter_pay: amount cannot be negative (got %d)", amount)
	}
	if amount > sim.MaxPayWithItemAmount {
		return nil, modelSafef("counter_pay: amount exceeds maximum (got %d, max %d)", amount, sim.MaxPayWithItemAmount)
	}
	if err := validatePayItemsDecode("counter_pay", "pay_items", wire.PayItems); err != nil {
		return nil, err
	}
	if amount == 0 && len(wire.PayItems) == 0 {
		return nil, modelSafef("counter_pay: counter must propose coins or goods (set amount, add pay_items, or both)")
	}
	say, err := selectSayAlias("counter_pay", "message", wire.Say, wire.Message)
	if err != nil {
		return nil, err
	}
	return CounterPayArgs{LedgerID: wire.LedgerID, Amount: amount, PayItems: wire.PayItems, Say: say}, nil
}

// HandleCounterPay is the CommitFn for the counter_pay tool. The counter's terms
// are spoken as it commits; the same words are recorded on the ledger entry.
//
// `landed` admits Accepted as well as Countered: a non-increasing pure-coin
// counter coerces to an accept inside sim.CounterPay (the "I'll let it go at your
// price" path, LLM-13). Those words were said over a real settle, so they carry.
func HandleCounterPay(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(CounterPayArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("counter_pay: handler received unexpected args type %T", in.Args)
	}
	say, err := normalizeSayLine("counter_pay", args.Say)
	if err != nil {
		return sim.Command{}, err
	}
	payItems, err := buildPayItemInputs("counter_pay", "pay_items", args.PayItems)
	if err != nil {
		return sim.Command{}, err
	}
	now := time.Now().UTC()
	ledgerID := sim.LedgerID(args.LedgerID)
	return respondAndSpeak(
		in.ActorID, ledgerID, say, in.HasNewNews, now,
		fmt.Sprintf("counter_pay countered ledger %d", ledgerID),
		func(s sim.PayLedgerState) bool {
			return s == sim.PayLedgerStateCountered || s == sim.PayLedgerStateAccepted
		},
		sim.CounterPay(in.ActorID, ledgerID, args.Amount, payItems, normalizeShortMessage(say), now),
	), nil
}

// ====================================================================
// withdraw_pay — buyer-side withdraw
// ====================================================================

// WithdrawPayArgs is the decoded shape of the withdraw_pay tool's
// arguments.
type WithdrawPayArgs struct {
	LedgerID LenientID `json:"ledger_id"`
	Message  string    `json:"message"`
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
	LedgerID LenientID `json:"ledger_id"`
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
// ForText). Used for the decline / counter spoken line as it is recorded on
// PayLedgerEntry.Message, and for the withdraw note — short-form metadata read by
// the umbilical operator console, where a literal newline would split the row.
func normalizeShortMessage(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// selectSayAlias picks the spoken line from the canonical `say` and a tool's
// legacy alias for it (decline_pay's `reason`, counter_pay's `message` —
// LLM-350). A `say` with actual content wins; otherwise the alias.
//
// Selection is TRIMMED, unlike sell's item/item_kind alias which selects on exact
// emptiness. There, a whitespace-only `item` still selects and the handler rejects
// it as empty-after-trim, so nothing is lost. Here the handler would silently drop
// a whitespace-only `say` and speak nothing — so `{"say":"   ","reason":"No bread
// today."}` would swallow a real refusal. A semantically empty canonical field must
// not beat a meaningful legacy one (code_review).
//
// Both names are rune-capped even when the other wins, matching the strict-decode
// posture of the sell alias: an over-cap value the model actually sent is a
// malformed call, not something to silently ignore. Each is capped under its own
// name so the model can map the error back to the field it sent.
func selectSayAlias(toolName, aliasName, say, alias string) (string, error) {
	for _, f := range []struct{ name, val string }{{"say", say}, {aliasName, alias}} {
		if f.val == "" {
			continue
		}
		if n := utf8.RuneCountInString(f.val); n > MaxSpeakTextChars {
			return "", modelSafef(
				"%s: %s exceeds %d-character cap (got %d characters)",
				toolName, f.name, MaxSpeakTextChars, n,
			)
		}
		// Same utterance path ⇒ same mojibake guard as speak (LLM-235). Each
		// field is checked under its own name (say / reason / message) so the
		// model can map the correction back to the field it sent.
		if err := checkUtteranceText(toolName, f.name, f.val); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(say) != "" {
		return say, nil
	}
	return alias, nil
}

// validatePayItemsDecode runs the decode-stage checks on a barter
// pay_items array (ZBBS-HOME-393): count cap, per-item rune cap, and qty
// bounds. Trim-emptiness, control-char scans, dup-kind reject, and item
// resolution are deferred to buildPayItemInputs / the Command Fn (the
// same split pay_with_item's other fields use). toolName scopes the
// error messages; fieldName names the tool's actual goods-array arg
// ("pay_items", "give", "reward_items" — LLM-225) so a weak model can map
// the error back to the field it sent.
func validatePayItemsDecode(toolName, fieldName string, items []payItemArg) error {
	if len(items) > MaxPayWithItemPayItemsHandler {
		return modelSafef(
			"%s: %s exceeds %d-entry cap (got %d)",
			toolName, fieldName, MaxPayWithItemPayItemsHandler, len(items),
		)
	}
	for i, pi := range items {
		if n := utf8.RuneCountInString(pi.Item); n > MaxPayWithItemItemChars {
			return modelSafef(
				"%s: %s[%d].item exceeds %d-character cap (got %d characters)",
				toolName, fieldName, i, MaxPayWithItemItemChars, n,
			)
		}
		if pi.Qty < 1 {
			return modelSafef("%s: %s[%d].qty must be at least 1 (got %d)", toolName, fieldName, i, pi.Qty)
		}
		if pi.Qty > sim.MaxPayWithItemQty {
			return modelSafef("%s: %s[%d].qty exceeds maximum (got %d, max %d)", toolName, fieldName, i, pi.Qty, sim.MaxPayWithItemQty)
		}
	}
	return nil
}

// foldCoinPayItems folds coin-token pay_items rows into the coin amount —
// "buying bread paying with 3 coins" is amount+=3, not a goods row (LLM-290).
// Without the fold the row dead-ends on the resolvePayItems coin steer (or,
// before the phantom-kind cleanup, resolved onto the minted 'coin' kind and
// staked an offer the holdings check then bounced). Non-coin rows pass
// through untouched, order preserved. Pure — errors are model-safe statics.
func foldCoinPayItems(amount int, items []payItemArg) (int, []payItemArg, error) {
	if len(items) == 0 {
		return amount, items, nil
	}
	goods := make([]payItemArg, 0, len(items))
	for _, pi := range items {
		if !sim.IsCoinToken(pi.Item) {
			goods = append(goods, pi)
			continue
		}
		if pi.Qty < 1 {
			return 0, nil, modelSafef(
				"pay_with_item: pay_items coin quantity must be at least 1 (got %d)", pi.Qty)
		}
		if amount > sim.MaxPayWithItemAmount-pi.Qty {
			return 0, nil, modelSafef(
				"pay_with_item: coins offered exceed the maximum — lower the amount.")
		}
		amount += pi.Qty
	}
	return amount, goods, nil
}

// buildPayItemInputs is the pure-builder counterpart to
// validatePayItemsDecode: it trims each item name, rejects control chars,
// catches obvious duplicate names statically (the Command Fn does the
// canonical-kind dedup after resolution), and returns the []sim.PayItemInput
// the Command consumes. fieldName names the tool's actual goods-array arg,
// same as validatePayItemsDecode. ZBBS-HOME-393.
func buildPayItemInputs(toolName, fieldName string, items []payItemArg) ([]sim.PayItemInput, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]sim.PayItemInput, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for i, pi := range items {
		item := strings.TrimSpace(pi.Item)
		if item == "" {
			return nil, modelSafef("%s: %s[%d].item is empty after trim", toolName, fieldName, i)
		}
		if idx := indexStrictControlChar(item); idx >= 0 {
			return nil, modelSafef(
				"%s: %s[%d].item contains a disallowed control character at byte offset %d", toolName, fieldName, i, idx)
		}
		key := strings.ToLower(item)
		if _, dup := seen[key]; dup {
			return nil, modelSafef(
				"%s: %q appears more than once in %s — combine it into a single line.", toolName, item, fieldName)
		}
		seen[key] = struct{}{}
		out = append(out, sim.PayItemInput{Item: item, Qty: pi.Qty})
	}
	return out, nil
}
