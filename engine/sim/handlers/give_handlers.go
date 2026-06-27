package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// give_handlers.go — LLM-138. Tool handlers for the one-way gift family:
//
//   - give          — giver-side, offers goods to a co-present recipient for
//                     nothing in return (mints a pending gift).
//   - accept_gift   — recipient-side, accepts a pending gift (goods move).
//   - decline_gift  — recipient-side, declines a pending gift (nothing moves).
//
// give lowers onto sim.GiveItems (a sibling of PayWithItem on the shared
// pay-ledger). accept_gift / decline_gift are thin front doors onto the
// EXISTING sim.AcceptPay / sim.DeclinePay commands — a gift entry resolves
// through the same ledger machinery; only the model-facing verb differs (the
// offer_trade precedent: a dedicated legible verb over a shared command).
// Dedicated verbs because "accept_pay" for a free gift is a payment verb on a
// non-payment act; the resolve tools are gated (advertised only when a gift
// is pending), so the live tool surface doesn't grow.

// ---- give -----------------------------------------------------------

// GiveArgs is the decoded shape of the give tool's arguments, read from the
// giver's point of view: `with` is the recipient, `give` are the goods to
// hand over.
type GiveArgs struct {
	With string      `json:"with"`
	Give payItemList `json:"give"`
	For  string      `json:"for"`
}

var giveSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "with": {
            "type": "string",
            "minLength": 1,
            "maxLength": 100,
            "description": "Display name of the person in your conversation you want to give to."
        },
        "give": {
            "type": "array",
            "minItems": 1,
            "maxItems": 8,
            "items": {
                "type": "object",
                "properties": {
                    "item": {"type": "string", "minLength": 1, "maxLength": 64, "description": "Canonical item kind you carry and will hand over (e.g. 'blueberries', 'bread')."},
                    "qty": {"type": "integer", "minimum": 1, "maximum": 2147483647, "description": "How many of this item you give."}
                },
                "required": ["item", "qty"],
                "additionalProperties": false
            },
            "description": "The goods you hand over as a gift. Each line is an item you carry and a quantity."
        },
        "for": {
            "type": "string",
            "maxLength": 200,
            "description": "Optional brief note describing what the gift is for."
        }
    },
    "required": ["with", "give"],
    "additionalProperties": false
}`)

const giveDescription = "Give goods you carry to another villager in your current conversation, free, expecting nothing in return. " +
	"Set `with` (their name) and `give` (the goods you hand over). " +
	"This places a pending gift they can accept or decline; when they accept, the goods move from your pack to theirs. " +
	"Use this for generosity — sharing food with someone hungry, handing over a tool. To get something in return, use sell or offer_trade instead."

// DecodeGiveArgs parses the raw give tool-call arguments. World-state checks
// (recipient resolve, goods held, co-presence) run in sim.GiveItems.
func DecodeGiveArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("give: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args GiveArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("give: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("give: trailing data after JSON object")
		}
		return nil, fmt.Errorf("give: malformed trailing data: %w", err)
	}
	if args.With == "" {
		return nil, modelSafef("give: with (the recipient) is required")
	}
	if n := utf8.RuneCountInString(args.With); n > MaxPayWithItemNameChars {
		return nil, modelSafef("give: with exceeds %d-character cap (got %d characters)", MaxPayWithItemNameChars, n)
	}
	if len(args.Give) == 0 {
		return nil, modelSafef("give: name at least one item to give")
	}
	if err := validatePayItemsDecode("give", args.Give); err != nil {
		return nil, err
	}
	if n := utf8.RuneCountInString(args.For); n > MaxPayWithItemForChars {
		return nil, modelSafef("give: 'for' text exceeds %d-character cap (got %d characters)", MaxPayWithItemForChars, n)
	}
	return args, nil
}

// HandleGive is the CommitFn for the give tool. Pure builder — trims, scans
// control chars, builds sim.GiveItems wrapped in the huddle-bootstrap so a
// giver who walked up to someone can give on arrival (mirrors pay_with_item).
func HandleGive(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(GiveArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("give: handler received unexpected args type %T", in.Args)
	}
	recipient := strings.TrimSpace(args.With)
	if recipient == "" {
		return sim.Command{}, modelSafef("give: with is empty after trim")
	}
	if i := indexStrictControlChar(recipient); i >= 0 {
		return sim.Command{}, modelSafef("give: with contains a disallowed control character at byte offset %d", i)
	}
	forText := strings.Join(strings.Fields(args.For), " ")
	if forText != "" {
		if i := indexInvalidControlChar(forText); i >= 0 {
			return sim.Command{}, modelSafef("give: 'for' contains a disallowed control character at byte offset %d", i)
		}
	}
	giftItems, err := buildPayItemInputs("give", args.Give)
	if err != nil {
		return sim.Command{}, err
	}
	now := time.Now().UTC()
	return withHuddleBootstrap(in.ActorID, now, sim.GiveItems(
		in.ActorID,
		recipient,
		giftItems,
		forText,
		now,
	)), nil
}

// ---- accept_gift ----------------------------------------------------

// AcceptGiftArgs is the decoded shape of the accept_gift tool's arguments.
type AcceptGiftArgs struct {
	LedgerID LenientID `json:"ledger_id"`
}

var acceptGiftSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "ledger_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric ledger ID of the pending gift to accept. You'll see this in your perception of the gift offered to you."
        }
    },
    "required": ["ledger_id"],
    "additionalProperties": false
}`)

const acceptGiftDescription = "Accept a gift someone in your conversation has offered you. " +
	"The goods move from their pack into yours. At acceptance the engine verifies you are both still here and they still hold the goods."

// DecodeAcceptGiftArgs parses raw args into an AcceptGiftArgs.
func DecodeAcceptGiftArgs(raw json.RawMessage) (any, error) {
	args, err := decodeLedgerOnly(raw, "accept_gift")
	if err != nil {
		return nil, err
	}
	return AcceptGiftArgs{LedgerID: args.LedgerID}, nil
}

// HandleAcceptGift is the CommitFn for accept_gift. It reuses sim.AcceptPay —
// a gift entry resolves through the same accept path (acceptPendingOffer
// skips the bought-item gates for it).
func HandleAcceptGift(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(AcceptGiftArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("accept_gift: handler received unexpected args type %T", in.Args)
	}
	return sim.AcceptPay(in.ActorID, sim.LedgerID(args.LedgerID), time.Now().UTC()), nil
}

// ---- decline_gift ---------------------------------------------------

// DeclineGiftArgs is the decoded shape of the decline_gift tool's arguments.
type DeclineGiftArgs struct {
	LedgerID LenientID `json:"ledger_id"`
	Reason   string    `json:"reason"`
}

var declineGiftSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "ledger_id": {
            "type": "integer",
            "minimum": 1,
            "description": "The numeric ledger ID of the pending gift to decline."
        },
        "reason": {
            "type": "string",
            "maxLength": 220,
            "description": "Optional short reason for declining. The giver sees this in their perception."
        }
    },
    "required": ["ledger_id"],
    "additionalProperties": false
}`)

const declineGiftDescription = "Decline a gift offered to you. Nothing moves. Optionally include a brief reason the giver sees in their perception."

// DecodeDeclineGiftArgs parses raw args into a DeclineGiftArgs (same shape as
// decline_pay: ledger_id + optional reason).
func DecodeDeclineGiftArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("decline_gift: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args DeclineGiftArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("decline_gift: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("decline_gift: trailing data after JSON object")
		}
		return nil, fmt.Errorf("decline_gift: malformed trailing data: %w", err)
	}
	if args.LedgerID < 1 {
		return nil, modelSafef("decline_gift: ledger_id must be at least 1 (got %d)", args.LedgerID)
	}
	if n := utf8.RuneCountInString(args.Reason); n > MaxPayMessageHandlerRunes {
		return nil, modelSafef("decline_gift: reason exceeds %d-character cap (got %d characters)", MaxPayMessageHandlerRunes, n)
	}
	return args, nil
}

// HandleDeclineGift is the CommitFn for decline_gift. Reuses sim.DeclinePay.
func HandleDeclineGift(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(DeclineGiftArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("decline_gift: handler received unexpected args type %T", in.Args)
	}
	reason := normalizeShortMessage(args.Reason)
	if reason != "" {
		if i := indexInvalidControlChar(reason); i >= 0 {
			return sim.Command{}, modelSafef("decline_gift: reason contains a disallowed control character at byte offset %d", i)
		}
	}
	return sim.DeclinePay(in.ActorID, sim.LedgerID(args.LedgerID), reason, time.Now().UTC()), nil
}
