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

// scene_quote.go — production scene_quote tool registration + handler.
// Phase 3 PR S3 — scope: vendor-side quote posting (seller-callable).
// Buyer-side fast-path via pay_with_item(quote_id=...) lands in PR S4
// alongside the pay-ledger substrate.
//
// The model emits {"lines": [{"item_kind": "...", "qty": N}, ...],
// "amount": M, "consume_now": true|false, "target_buyer": "...",
// "consumers": [...]} as the scene_quote tool's arguments — one or more
// item lines bundled under a single total price (LLM-101). Decode parses +
// applies schema-bounded length + numeric range; HandleSceneQuote applies
// semantic static validation (per-line trim-empty / control-char scans /
// consumer-list duplicate-name reject); the returned sim.SceneQuoteCreate
// Command runs on the world goroutine and performs world-state-dependent
// validation (scene resolution, per-line ItemKind catalog lookup + merge,
// break gate, per-line stock check, target-buyer + consumer resolution,
// duplicate-key + per-(seller, scene) cap) + mint + emit.

// SceneQuoteLineArg is one bundle line in the scene_quote tool's arguments:
// a free-text item_kind + per-consumer qty (LLM-101). Resolved + merged into
// canonical sim.QuoteLine values inside sim.SceneQuoteCreate.
type SceneQuoteLineArg struct {
	ItemKind string `json:"item_kind"`
	Qty      int    `json:"qty"`
}

// SceneQuoteArgs is the decoded shape of the scene_quote tool's
// arguments. The model-facing schema enforces:
//
//   - lines:       array, minItems 1, maxItems MaxSceneQuoteLines; each
//     entry { item_kind: minLength 1 / maxLength MaxSceneQuoteItemChars,
//     qty: integer 1..math.MaxInt32 }
//   - amount:      integer, minimum 1, maximum math.MaxInt32
//   - consume_now: required boolean (no default — per S2's lesson,
//     load-bearing fields don't get inferred defaults)
//   - target_buyer: maxLength MaxSceneQuoteNameChars (optional)
//   - consumers:   array (optional), maxItems MaxSceneQuoteConsumers,
//     each item minLength 1, maxLength MaxSceneQuoteNameChars
type SceneQuoteArgs struct {
	Lines       []SceneQuoteLineArg `json:"lines"`
	Amount      int                 `json:"amount"`
	ConsumeNow  bool                `json:"consume_now"`
	TargetBuyer string              `json:"target_buyer"`
	Consumers   []string            `json:"consumers"`
}

// MaxSceneQuoteLines caps len(lines[]) in the schema. Mirrors
// sim.MaxSceneQuoteLines; the two MUST stay in sync (the schema literal is
// asserted against the substrate const in the handler test). Schema-side cap
// is defense-in-depth so the LLM can't blow the per-tool token budget on a
// runaway line list before validation runs.
const MaxSceneQuoteLines = 8

// MaxSceneQuoteItemChars caps the item_kind length on the model-facing
// schema. Matches MaxConsumeItemChars — same catalog, same headroom
// for prompt-typo flexibility.
const MaxSceneQuoteItemChars = 64

// MaxSceneQuoteNameChars caps each name field's length (target_buyer
// and each consumers[] entry). Matches MaxPayRecipientChars — same
// canonical "First Last" headroom rationale.
const MaxSceneQuoteNameChars = 100

// MaxSceneQuoteConsumers caps len(consumers[]) in the schema. The
// runtime Command Fn also enforces sim.SceneQuoteMaxConsumers; the
// two MUST stay in sync. Schema-side cap is defense-in-depth so the
// LLM can't blow per-Tool token budget on a runaway consumer list
// before validation gets a chance.
const MaxSceneQuoteConsumers = 8

// SceneQuote's numeric upper bounds are shared with the substrate-level
// constants via sim.MaxSceneQuoteAmount / sim.MaxSceneQuoteQty
// (= math.MaxInt32). The JSON Schema below restates 2147483647
// literally because schema bytes are static — schema + constant must
// stay in sync.

// sceneQuoteSchema is the JSON Schema bytes shipped to the LLM
// provider. Narrow on purpose — PR S3 advertises the quote-creation
// shape only. Withdraw / cancel / amend operations don't exist (a
// quote naturally expires; an LLM that wants to revise re-quotes with
// the duplicate-key replacement path doing the supersede automatically).
var sceneQuoteSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "lines": {
            "type": "array",
            "minItems": 1,
            "maxItems": 8,
            "items": {
                "type": "object",
                "properties": {
                    "item_kind": {
                        "type": "string",
                        "minLength": 1,
                        "maxLength": 64,
                        "description": "Canonical item kind from your inventory (e.g. 'blueberries', 'ale', 'stew')."
                    },
                    "qty": {
                        "type": "integer",
                        "minimum": 1,
                        "maximum": 2147483647,
                        "description": "Quantity of this item per consumer (e.g. qty=2 for 'two each')."
                    }
                },
                "required": ["item_kind", "qty"],
                "additionalProperties": false
            },
            "description": "The items to offer for sale, one entry per item kind. One offer can bundle several kinds for a single total price (e.g. 2 blueberries + 2 raspberries for 8 coins); most offers have a single line."
        },
        "amount": {
            "type": "integer",
            "minimum": 1,
            "maximum": 2147483647,
            "description": "Total bundle price in coins. A buyer paying this exact amount takes the quote; paying more is tipping; paying less is rejected."
        },
        "consume_now": {
            "type": "boolean",
            "description": "True for eat-here / drink-here (immediate consumption); false for takeaway (the buyer carries the items away). Applies to the whole bundle. Some goods (a served meal, a poured drink) can't be carried away — an offer including those always stands as eat-here regardless."
        },
        "target_buyer": {
            "type": "string",
            "maxLength": 100,
            "description": "Optional display name of a specific buyer this quote is addressed to. Empty means anyone in your current scene can take it."
        },
        "consumers": {
            "type": "array",
            "maxItems": 8,
            "items": {
                "type": "string",
                "minLength": 1,
                "maxLength": 100
            },
            "description": "Optional list of display names for a group order (e.g. 'a round of ale for the table'). Empty means the buyer is the sole consumer. All consumers must be in your current conversation."
        }
    },
    "required": ["lines", "amount", "consume_now"],
    "additionalProperties": false
}`)

// sceneQuoteDescription is the tool description advertised to the
// model. Concrete enough that the LLM understands when to use it
// over speak ("the ale is 4 coins" is flavor; scene_quote is the
// actual transactional offer).
const sceneQuoteDescription = "Post an offer to sell items from your inventory to the people in your current conversation. " +
	"This is the transactional surface — speech that mentions a price is just flavor, this is what a buyer can actually pay against. " +
	"You set the item lines (one or more item kinds, each with a per-consumer quantity), one total price for the whole offer, and whether it's eat-here or takeaway. " +
	"Bundle several kinds in one offer when a buyer wants some of each (e.g. 2 blueberries + 2 raspberries for 8 coins). " +
	"Optionally target a specific buyer or specify consumers for a group order. " +
	"Quotes expire after about 10 minutes; posting the same shape again replaces the old quote."

// DecodeSceneQuoteArgs parses the raw tool-call arguments into a
// SceneQuoteArgs. Errors are typed validation failures the harness
// surfaces to the model as tool errors so it can retry.
//
// Checks:
//
//   - JSON parses, no trailing data
//   - No unknown fields (DisallowUnknownFields)
//   - item_kind present and non-empty post-decode
//   - qty in [1, math.MaxInt32]
//   - amount in [1, math.MaxInt32]
//   - field byte-length caps (defense in depth vs schema): item_kind
//     <= MaxSceneQuoteItemChars; target_buyer + each consumers[] entry
//     <= MaxSceneQuoteNameChars; len(consumers) <= MaxSceneQuoteConsumers
//
// What DecodeSceneQuoteArgs does NOT check (handled in HandleSceneQuote
// or the Command Fn):
//
//   - Trim-emptiness: HandleSceneQuote trims and rejects after
//     normalization.
//   - Control-character scans: HandleSceneQuote's responsibility.
//   - Duplicate consumer names: HandleSceneQuote runs the static
//     dup-name check on the trimmed list (the Command Fn does the
//     ActorID-level dup-check after resolution, but a static check
//     is cheap defense in depth).
//   - Catalog lookup / break gate / stock / scene / consumer
//     resolution: world-state checks done by sim.SceneQuoteCreate on
//     the world goroutine.
func DecodeSceneQuoteArgs(raw json.RawMessage) (any, error) {
	// Reject non-object payloads early. Same rationale as pay /
	// consume — bare null / number / string decode quietly to zero
	// values, producing misleading downstream errors.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, modelSafef("sell: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args SceneQuoteArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("scene_quote: malformed arguments: %w", err)
	}
	// Trailing-data check — matches the pay/speak/consume pattern.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, modelSafef("sell: trailing data after JSON object")
		}
		return nil, fmt.Errorf("scene_quote: malformed trailing data: %w", err)
	}

	if len(args.Lines) == 0 {
		return nil, modelSafef("sell: at least one line is required (set lines to a non-empty array of {item_kind, qty})")
	}
	if len(args.Lines) > MaxSceneQuoteLines {
		return nil, modelSafef(
			"sell: too many lines (got %d, max %d) — bundle fewer item kinds.",
			len(args.Lines), MaxSceneQuoteLines,
		)
	}
	for i, ln := range args.Lines {
		if ln.ItemKind == "" {
			return nil, modelSafef("sell: lines[%d].item_kind is required", i)
		}
		if n := utf8.RuneCountInString(ln.ItemKind); n > MaxSceneQuoteItemChars {
			return nil, modelSafef(
				"sell: lines[%d].item_kind exceeds %d-character cap (got %d characters)",
				i, MaxSceneQuoteItemChars, n,
			)
		}
		if ln.Qty < 1 {
			return nil, modelSafef("sell: lines[%d].qty must be at least 1 (got %d)", i, ln.Qty)
		}
		if ln.Qty > sim.MaxSceneQuoteQty {
			return nil, modelSafef("sell: lines[%d].qty exceeds maximum (got %d, max %d)", i, ln.Qty, sim.MaxSceneQuoteQty)
		}
	}
	if args.Amount < 1 {
		return nil, modelSafef("sell: amount must be at least 1 (got %d)", args.Amount)
	}
	if args.Amount > sim.MaxSceneQuoteAmount {
		return nil, modelSafef("sell: amount exceeds maximum (got %d, max %d)", args.Amount, sim.MaxSceneQuoteAmount)
	}
	if n := utf8.RuneCountInString(args.TargetBuyer); n > MaxSceneQuoteNameChars {
		return nil, modelSafef(
			"sell: target_buyer exceeds %d-character cap (got %d characters)",
			MaxSceneQuoteNameChars, n,
		)
	}
	if len(args.Consumers) > MaxSceneQuoteConsumers {
		return nil, modelSafef(
			"sell: consumers exceeds %d-entry cap (got %d)",
			MaxSceneQuoteConsumers, len(args.Consumers),
		)
	}
	for i, c := range args.Consumers {
		if n := utf8.RuneCountInString(c); n > MaxSceneQuoteNameChars {
			return nil, modelSafef(
				"sell: consumers[%d] exceeds %d-character cap (got %d characters)",
				i, MaxSceneQuoteNameChars, n,
			)
		}
	}
	return args, nil
}

// HandleSceneQuote is the CommitFn for the scene_quote tool. Pure
// builder — does NOT touch the world. Static validation that JSON
// Schema cannot express runs here (trim-empty / control-char scans /
// dup-consumer-name reject on the trimmed list); world-state
// validation (scene, catalog, break, stock, resolution, cap, mint,
// emit) runs inside the returned sim.SceneQuoteCreate Command on the
// world goroutine.
//
// Returns:
//
//   - sim.SceneQuoteCreate Command on success — harness submits via
//     sim.RunTickToolCommand which runs Fn on the world goroutine.
//   - typed error on static-validation failure — surfaces to the model
//     as a tool error so it can retry.
func HandleSceneQuote(in HandlerInput) (sim.Command, error) {
	args, ok := in.Args.(SceneQuoteArgs)
	if !ok {
		return sim.Command{}, fmt.Errorf("scene_quote: handler received unexpected args type %T", in.Args)
	}

	// Normalize each bundle line's catalog-ish item_kind: trim +
	// strict-control-char scan (same posture as consume — short-form
	// identifier, no legitimate \n/\r/\t). Duplicate canonical kinds are
	// merged downstream in sim.SceneQuoteCreate after catalog resolution, so
	// the handler does NOT dedup here. qty was range-checked in decode.
	lines := make([]sim.QuoteLineInput, 0, len(args.Lines))
	for i, ln := range args.Lines {
		itemKind := strings.TrimSpace(ln.ItemKind)
		if itemKind == "" {
			return sim.Command{}, modelSafef("sell: lines[%d].item_kind is empty after trim", i)
		}
		if idx := indexStrictControlChar(itemKind); idx >= 0 {
			return sim.Command{}, modelSafef(
				"sell: lines[%d].item_kind contains a disallowed control character at byte offset %d", i, idx)
		}
		lines = append(lines, sim.QuoteLineInput{ItemName: itemKind, Qty: ln.Qty})
	}

	// Normalize target_buyer (optional). Empty after trim is fine —
	// stays empty and signals "public-to-scene." Non-empty gets
	// the strict-control-char scan (name is identifier-shaped, no
	// legitimate paragraph shaping).
	targetBuyer := strings.TrimSpace(args.TargetBuyer)
	if targetBuyer != "" {
		if i := indexStrictControlChar(targetBuyer); i >= 0 {
			return sim.Command{}, modelSafef(
				"sell: target_buyer contains a disallowed control character at byte offset %d", i)
		}
	}

	// Normalize consumer names. Per-entry trim, control-char scan,
	// post-trim non-empty enforcement, and a static-string-level
	// duplicate check. The Command Fn does the canonical ActorID-
	// level duplicate check after resolution — the static check
	// catches obvious typos ("aldous", "Aldous") that resolve to
	// the same actor before we burn a world-goroutine round-trip
	// on something we know is bad.
	var consumers []string
	if len(args.Consumers) > 0 {
		consumers = make([]string, 0, len(args.Consumers))
		seen := make(map[string]struct{}, len(args.Consumers))
		for i, raw := range args.Consumers {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				return sim.Command{}, modelSafef(
					"sell: consumers[%d] is empty after trim — every consumer must have a name.", i)
			}
			if idx := indexStrictControlChar(trimmed); idx >= 0 {
				return sim.Command{}, modelSafef(
					"sell: consumers[%d] contains a disallowed control character at byte offset %d", i, idx)
			}
			key := strings.ToLower(trimmed)
			if _, dup := seen[key]; dup {
				return sim.Command{}, modelSafef(
					"sell: %q appears more than once in the consumer list — list each person only once.", trimmed)
			}
			seen[key] = struct{}{}
			consumers = append(consumers, trimmed)
		}
	}

	// ZBBS-HOME-400: form/join the co-located huddle on the quote itself so a
	// seller can post a quote to a customer present at the stall without a
	// separate prior speak. No-op when already huddled, alone, or out of scope.
	now := time.Now().UTC()
	return withHuddleBootstrap(in.ActorID, now, sim.SceneQuoteCreate(
		in.ActorID,
		lines,
		args.Amount,
		args.ConsumeNow,
		targetBuyer,
		consumers,
		now,
	)), nil
}
