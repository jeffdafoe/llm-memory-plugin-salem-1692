package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

// lenient_args.go — weak-model scalar tolerance for tool arguments (LLM-377).
//
// The stateful-NPC model (llama-3.3-70b) INTERMITTENTLY emits scalar tool
// arguments in shapes JSON Schema forbids: a whole number as a STRING
// ("qty":"1", "amount":"5"), a boolean as a STRING ("consume_now":"false"),
// and the coin field under a synonym it learned from the sibling `offer_trade`
// tool ("coins", or nested "payment":{"coins":N}) instead of the canonical
// `amount`. With the decoders' DisallowUnknownFields + strict scalar types,
// every one of those hard-fails the whole call, the model reject-retries the
// same shape, and it storms the per-tick iteration budget — the loop that
// pinned Prudence Ward at Ezekiel's blacksmith, unable to buy a nail, for
// hours (351 malformed_args rejections in one afternoon).
//
// These types are the scalar counterparts of the existing tolerance layers
// (LenientID for identifiers, payItemList for goods arrays) — same policy:
// accept the shape the model actually sends, leave the schema advertising the
// canonical form, and keep genuinely-malformed input rejected so the
// downstream bound checks keep their model-safe messages. Tolerance layers,
// not contract changes.

// LenientInt decodes a signed integer that may arrive as a JSON number or as a
// numeric string ("5"). A real number is unaffected; a fractional, non-numeric,
// or overflowing value is still rejected exactly as a bare int field would be,
// so callers' range checks (qty >= 1, amount >= 0, "exceeds maximum") keep
// firing with their existing reasons. An empty string coerces to 0 (the unset
// sentinel), mirroring LenientID.
type LenientInt int

func (n *LenientInt) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	// encoding/json never hands UnmarshalJSON empty bytes during normal struct
	// decoding, but a direct call might — and silently accepting it would be
	// more lenient than JSON itself.
	if len(trimmed) == 0 {
		return io.ErrUnexpectedEOF
	}
	// String form: the weak model wraps the number in quotes. Unwrap one
	// JSON-string layer, then parse the inner text as an integer.
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*n = 0
			return nil
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return modelSafef("%q is not a whole number", s)
		}
		*n = LenientInt(v)
		return nil
	}
	// Real JSON number — decode strictly into int, which rejects fractions and
	// overflow exactly as the bare int field did.
	var v int
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return err
	}
	*n = LenientInt(v)
	return nil
}

// LenientBool decodes a boolean that may arrive as a JSON bool, a string
// ("true"/"false"/"1"/"0"/"yes"/"no", case-insensitive), or a 0/1 number. A
// real bool is unaffected; anything else is rejected with a model-safe reason.
// An empty string coerces to false (the zero value a bare bool field would
// carry for an omitted required field).
type LenientBool bool

func (b *LenientBool) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return io.ErrUnexpectedEOF
	}
	// String form: "true"/"false"/"1"/"0"/"yes"/"no".
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "yes":
			*b = true
		case "false", "0", "no", "":
			*b = false
		default:
			return modelSafef("%q is not a boolean (true or false)", s)
		}
		return nil
	}
	// Numeric form: only 0/1 (some models emit consume_now:0). A number outside
	// {0,1} is NOT a boolean — reject it with a model-safe reason rather than
	// silently widening "true" to "any non-zero". The leading '-' is admitted to
	// this branch only so a negative like -1 lands on this model-safe reject
	// instead of the generic json bool-decode error.
	if trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9') {
		var v int
		if err := json.Unmarshal(trimmed, &v); err != nil {
			return err
		}
		switch v {
		case 0:
			*b = false
		case 1:
			*b = true
		default:
			return modelSafef("%d is not a boolean (use true/false or 0/1)", v)
		}
		return nil
	}
	// Real JSON bool.
	var v bool
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return err
	}
	*b = LenientBool(v)
	return nil
}

// coinPayment is the decode target for the nested `payment: {"coins": N}`
// synonym the weak model reaches for on the pay tools. Its own UnmarshalJSON
// reads only `coins` and ignores any other keys the model tacks on (currency,
// method, …). The custom Unmarshaler is load-bearing: a plainly-typed nested
// struct is decoded by the SAME top-level decoder, so its DisallowUnknownFields
// would reject those extras and re-create the loop we are fixing. Handing the
// nested object to json.Unmarshal detaches it from that strictness. A
// non-object payment still errors.
type coinPayment struct {
	Coins LenientInt
}

func (c *coinPayment) UnmarshalJSON(data []byte) error {
	var wire struct {
		Coins LenientInt `json:"coins"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	c.Coins = wire.Coins
	return nil
}

// resolveCoinAmount folds the weak-model coin synonyms into the canonical
// `amount`: an explicit non-zero `amount` always wins; otherwise a top-level
// `coins`, then a nested `payment.coins`. The model uses `coins`/`payment`
// because the sibling offer_trade tool names the same concept `coins`. A
// resolved value flows through the caller's existing amount bound checks
// (negative / over-max / must-offer-something) unchanged.
func resolveCoinAmount(amount int, coins LenientInt, payment *coinPayment) int {
	if amount != 0 {
		return amount
	}
	if coins != 0 {
		return int(coins)
	}
	if payment != nil && payment.Coins != 0 {
		return int(payment.Coins)
	}
	return amount
}
