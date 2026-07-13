package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
// The model emits {"lines": [{"item": "...", "qty": N}, ...],
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
// a free-text item name + per-consumer qty (LLM-101). Resolved + merged into
// canonical sim.QuoteLine values inside sim.SceneQuoteCreate.
//
// The model-facing field is `item` (LLM-326 — the generic name every other
// tool uses: consume / produce / pay_with_item / speak.mentions), with
// `item_kind` tolerated as a decode-only alias (see DecodeSceneQuoteArgs). The
// Go field keeps the name ItemKind — the substrate reads it and renaming it
// would ripple into sim.SceneQuoteCreate; only the json tag moved.
type SceneQuoteLineArg struct {
	ItemKind string `json:"item"`
	Qty      int    `json:"qty"`
}

// SceneQuoteArgs is the decoded shape of the scene_quote tool's
// arguments. The model-facing schema enforces:
//
//   - lines:       array, minItems 1, maxItems MaxSceneQuoteLines; each
//     entry { item: minLength 1 / maxLength MaxSceneQuoteItemChars,
//     qty: integer 1..math.MaxInt32 }
//   - amount:      integer, minimum 1, maximum math.MaxInt32
//   - consume_now: required boolean (no default — per S2's lesson,
//     load-bearing fields don't get inferred defaults)
//   - target_buyer: maxLength MaxSceneQuoteNameChars (optional)
//   - consumers:   array (optional), maxItems MaxSceneQuoteConsumers,
//     each item minLength 1, maxLength MaxSceneQuoteNameChars
//   - say:         maxLength MaxSpeakTextChars (optional)
type SceneQuoteArgs struct {
	Lines       []SceneQuoteLineArg `json:"lines"`
	Amount      int                 `json:"amount"`
	ConsumeNow  bool                `json:"consume_now"`
	TargetBuyer string              `json:"target_buyer"`
	Consumers   []string            `json:"consumers"`
	// Say is the seller's spoken line, delivered as the offer is posted
	// (LLM-343). Both speak and sell are tick-terminal, so a keeper who
	// answers "what's the stew?" with a speak never reaches sell — the price
	// is voiced and no payable offer exists. Folding the utterance into the
	// quote makes naming the price and posting the offer one act, which is
	// what they are in the fiction. Optional: a cold offer (no words) is still
	// the common shape.
	Say string `json:"say"`
}

// MaxSceneQuoteLines caps len(lines[]) in the schema. Mirrors
// sim.MaxSceneQuoteLines; the two MUST stay in sync (the schema literal is
// asserted against the substrate const in the handler test). Schema-side cap
// is defense-in-depth so the LLM can't blow the per-tool token budget on a
// runaway line list before validation runs.
const MaxSceneQuoteLines = 8

// MaxSceneQuoteItemChars caps the item length on the model-facing
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
                    "item": {
                        "type": "string",
                        "minLength": 1,
                        "maxLength": 64,
                        "description": "Canonical item from your inventory (e.g. 'blueberries', 'ale', 'stew')."
                    },
                    "qty": {
                        "type": "integer",
                        "minimum": 1,
                        "maximum": 2147483647,
                        "description": "Quantity of this item per consumer (e.g. qty=2 for 'two each')."
                    }
                },
                "required": ["item", "qty"],
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
        },
        "say": {
            "type": "string",
            "maxLength": 1000,
            "description": "What you say aloud as you make the offer, in your own voice (e.g. 'A bowl of stew runs four coins, and a loaf two — six for the both.'). Spoken to the room, or to target_buyer if you named one. Optional: omit to set out goods without a word."
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
	"Say your price aloud in the same breath by passing `say` — do NOT name a price with the speak tool and then call this, because speaking ends your turn and the offer would never be posted. " +
	"Optionally target a specific buyer or specify consumers for a group order. " +
	"Quotes expire after about 10 minutes; posting the same shape again replaces the old quote."

// DecodeSceneQuoteArgs parses the raw tool-call arguments into a
// SceneQuoteArgs. Errors are typed validation failures the harness
// surfaces to the model as tool errors so it can retry.
//
// Alias (LLM-326): the canonical per-line field is `item`, but `item_kind`
// (the engine's older jargon name for it) is tolerated as a decode-only alias
// so a model that reaches for it still lands the sell. Per line, a non-empty
// `item` wins; else the `item_kind` alias. Mirrors the speak `message`→`text`
// alias (LLM-315) and move_to's destination aliases (LLM-320), and is likewise
// per-tool by design — NOT a global decoder alias.
//
// Checks:
//
//   - JSON parses, no trailing data
//   - No unknown fields (DisallowUnknownFields) beyond the item_kind alias
//   - item (or the item_kind alias) present and non-empty post-decode
//   - qty in [1, math.MaxInt32]
//   - amount in [1, math.MaxInt32]
//   - field byte-length caps (defense in depth vs schema): item / item_kind
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
	// sceneQuoteArgsWire mirrors SceneQuoteArgs but decodes each line through
	// sceneQuoteLineWire, which carries the canonical `item` plus the
	// `item_kind` decode alias (LLM-326). Decoding into the wire type keeps
	// DisallowUnknownFields strict — the only extra line key tolerated is the
	// alias; everything else outside the schema is still rejected.
	type sceneQuoteLineWire struct {
		Item     string `json:"item"`
		ItemKind string `json:"item_kind"` // decode-only alias (LLM-326)
		Qty      int    `json:"qty"`
	}
	type sceneQuoteArgsWire struct {
		Lines       []sceneQuoteLineWire `json:"lines"`
		Amount      int                  `json:"amount"`
		ConsumeNow  bool                 `json:"consume_now"`
		TargetBuyer string               `json:"target_buyer"`
		Consumers   []string             `json:"consumers"`
		Say         string               `json:"say"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire sceneQuoteArgsWire
	if err := dec.Decode(&wire); err != nil {
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

	if len(wire.Lines) == 0 {
		return nil, modelSafef("sell: at least one line is required (set lines to a non-empty array of {item, qty})")
	}
	if len(wire.Lines) > MaxSceneQuoteLines {
		return nil, modelSafef(
			"sell: too many lines (got %d, max %d) — bundle fewer item kinds.",
			len(wire.Lines), MaxSceneQuoteLines,
		)
	}
	// Fold wire lines → canonical SceneQuoteArgs lines. Per line, validate the
	// character cap on every item field the model actually sent (strict-decode
	// posture — an over-cap alias is a malformed call even when `item` would
	// win), then select the item name canonical-first: `item`, else the
	// `item_kind` alias. Emptiness is judged as exact-empty here, NOT trimmed —
	// a whitespace-only value still selects and passes decode, and
	// HandleSceneQuote does the trim-and-reject (preserving the existing
	// decode/handler split; see harness_handler_reason_test.go).
	args := SceneQuoteArgs{
		Lines:       make([]SceneQuoteLineArg, len(wire.Lines)),
		Amount:      wire.Amount,
		ConsumeNow:  wire.ConsumeNow,
		TargetBuyer: wire.TargetBuyer,
		Consumers:   wire.Consumers,
		Say:         wire.Say,
	}
	for i, ln := range wire.Lines {
		item := ""
		for _, f := range []struct {
			name string
			val  string
		}{
			{"item", ln.Item},
			{"item_kind", ln.ItemKind},
		} {
			if f.val == "" {
				continue
			}
			if n := utf8.RuneCountInString(f.val); n > MaxSceneQuoteItemChars {
				return nil, modelSafef(
					"sell: lines[%d].%s exceeds %d-character cap (got %d characters)",
					i, f.name, MaxSceneQuoteItemChars, n,
				)
			}
			if item == "" {
				item = f.val
			}
		}
		if item == "" {
			return nil, modelSafef("sell: lines[%d].item is required", i)
		}
		if ln.Qty < 1 {
			return nil, modelSafef("sell: lines[%d].qty must be at least 1 (got %d)", i, ln.Qty)
		}
		if ln.Qty > sim.MaxSceneQuoteQty {
			return nil, modelSafef("sell: lines[%d].qty exceeds maximum (got %d, max %d)", i, ln.Qty, sim.MaxSceneQuoteQty)
		}
		args.Lines[i] = SceneQuoteLineArg{ItemKind: item, Qty: ln.Qty}
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
	// say shares speak's rune cap — it lands on the same utterance path, so a
	// line that speak would refuse must not sneak in through sell (LLM-343).
	if n := utf8.RuneCountInString(args.Say); n > MaxSpeakTextChars {
		return nil, modelSafef(
			"sell: say exceeds %d-character cap (got %d characters)",
			MaxSpeakTextChars, n,
		)
	}
	// Same utterance path ⇒ same mojibake guard as speak (LLM-235).
	if err := checkUtteranceText("sell", "say", args.Say); err != nil {
		return nil, err
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

	// Normalize each bundle line's catalog-ish item: trim +
	// strict-control-char scan (same posture as consume — short-form
	// identifier, no legitimate \n/\r/\t). Duplicate canonical kinds are
	// merged downstream in sim.SceneQuoteCreate after catalog resolution, so
	// the handler does NOT dedup here. qty was range-checked in decode.
	lines := make([]sim.QuoteLineInput, 0, len(args.Lines))
	for i, ln := range args.Lines {
		itemKind := strings.TrimSpace(ln.ItemKind)
		if itemKind == "" {
			return sim.Command{}, modelSafef("sell: lines[%d].item is empty after trim", i)
		}
		if idx := indexStrictControlChar(itemKind); idx >= 0 {
			return sim.Command{}, modelSafef(
				"sell: lines[%d].item contains a disallowed control character at byte offset %d", i, idx)
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

	// Normalize the optional spoken line (LLM-343). Prose, so it takes speak's
	// permissive control-char scan (\n \r \t allowed) rather than the strict
	// identifier scan the item/name fields above use.
	say := strings.TrimSpace(args.Say)
	if say != "" {
		if i := indexInvalidControlChar(say); i >= 0 {
			return sim.Command{}, modelSafef(
				"sell: say contains a disallowed control character at byte offset %d "+
					"(only \\n, \\r, \\t allowed)", i)
		}
	}

	// Captured outside the closure — the harness may reuse `in` across
	// iterations (same rationale as HandleSpeak).
	actorID := in.ActorID
	hasNewNews := in.HasNewNews

	// ZBBS-HOME-400: form/join the co-located huddle on the quote itself so a
	// seller can post a quote to a customer present at the stall without a
	// separate prior speak. No-op when already huddled, alone, or out of scope.
	now := time.Now().UTC()
	quote := sim.SceneQuoteCreate(
		actorID,
		lines,
		args.Amount,
		args.ConsumeNow,
		targetBuyer,
		consumers,
		now,
	)
	if say == "" {
		return withHuddleBootstrap(actorID, now, quote), nil
	}

	// Quote FIRST, then speak (LLM-343). The order is the whole point: if the
	// quote fails (no stock, unknown item, no scene) nothing has been said, and
	// the seller never voices a price against an offer that doesn't exist —
	// which is the failure this ticket exists to kill. The reverse order would
	// reproduce it exactly.
	//
	// The speak is best-effort. Once the quote is minted the world has moved;
	// there is no rollback, and returning an error here would leave a posted
	// quote behind a tool the model believes failed (it would re-sell, and the
	// same-tick quote guard would bounce it). SpeakTo still has reachable
	// rejections — the vocative gate (the line names someone who has left the
	// conversation) and the turn-state gate (the seller already spoke and is
	// owed a reply). In those cases the offer stands, Announced stays false, and
	// SpeakTo's own reason rides back on the result so the tool feedback tells
	// the seller what actually happened rather than guessing.
	return withHuddleBootstrap(actorID, now, sim.Command{Fn: func(w *sim.World) (any, error) {
		res, err := quote.Fn(w)
		if err != nil {
			return nil, err
		}
		created, ok := res.(sim.SceneQuoteCreateResult)
		if !ok {
			return res, nil
		}
		// targetBuyer doubles as the speak addressee: an offer made to one named
		// buyer is spoken to them, a public offer is spoken to the room.
		if _, serr := sim.SpeakTo(actorID, say, targetBuyer, nil, hasNewNews, now).Fn(w); serr != nil {
			log.Printf("sim/handlers: sell posted quote %d but its say was refused: %v", created.QuoteID, serr)
			created.SayRefused = serr.Error()
			return created, nil
		}
		created.Announced = true
		return created, nil
	}}), nil
}
