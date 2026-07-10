package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_pay.go — the PC entry into the v2 buyer-initiated pay-with-item commerce
// surface. Until this route, the five pay_with_item Commands were reachable
// only by NPCs (the LLM tool path in engine/sim/handlers). This route lets a
// human player make the SAME offer — coins for an item from a seller in their
// current huddle — by resolving the buyer from the authenticated session and
// delegating to sim.PayWithItem. Mirrors the pc/move + pc/speak posture in
// write_handlers.go: the handler owns shape/bounds/text validation; the command
// (on the world goroutine) owns all world-state validation and the mutation.
//
// Only the buyer-side offer-creation tool is exposed to the PC here. The
// seller-side resolution (accept/decline/counter) is NPC-driven and async: a
// slow-path offer mints a Pending entry + emits PayOfferReceived, which stamps
// the seller's warrant; the seller decides on a later reactor tick. The PC sees
// the outcome via the pay ledger carried on the world snapshot. The fast path
// (a matched quote_id) settles immediately. Design: work note
// tasks/pc-pay-v2/design-seed.

// maxPayBodyBytes caps the pc/pay request body. An offer is a seller name + an
// item + a few ints + an optional short consumer list + flavor text; 64 KiB is
// generous headroom while rejecting an attacker-controlled flood before decode.
const maxPayBodyBytes = 64 << 10

// These character caps mirror the LLM tool-path schema
// (handlers.MaxPayWithItemItemChars / MaxPayWithItemNameChars /
// MaxPayWithItemForChars). Kept local rather than importing the handlers
// package — same posture as validateSpeakText: that contract lives in the heavy
// tool-handler package and httpapi shouldn't depend on it. The NUMERIC caps
// (amount / qty / consumer-count) instead reuse the exported sim.* maxima,
// since those are the authority sim.PayWithItem itself re-enforces.
const (
	maxPayItemChars = 64  // mirrors handlers.MaxPayWithItemItemChars
	maxPayNameChars = 100 // mirrors handlers.MaxPayWithItemNameChars (seller + each consumer)
	maxPayForChars  = 200 // mirrors handlers.MaxPayWithItemForChars
)

// pcPayRequest is the POST /api/village/pc/pay body. Like the other pc/* routes
// there is deliberately no buyer field: the buyer is the caller's own PC,
// resolved from the authenticated session, so a caller can only ever offer as
// themselves — ownership is structural, not a checked field. The shape mirrors
// the pay_with_item tool args so a PC reaches the same commerce surface NPCs do.
//
// seller and item are names — seller is resolved against the PC's current
// huddle peers (case-insensitive, ambiguity-reject) inside sim.PayWithItem.
// quote_id and in_response_to are optional (0 = unset): a non-zero quote_id
// takes the fast path (immediate accept against a posted scene quote); a
// non-zero in_response_to links this offer as the buyer's response to a
// previously countered offer.
type pcPayRequest struct {
	Seller       string   `json:"seller"`
	Item         string   `json:"item"`
	Qty          int      `json:"qty"`
	Amount       int      `json:"amount"`
	ConsumeNow   bool     `json:"consume_now"`
	Consumers    []string `json:"consumers,omitempty"`
	QuoteID      uint64   `json:"quote_id,omitempty"`
	InResponseTo uint64   `json:"in_response_to,omitempty"`
	For          string   `json:"for,omitempty"`
	// ReadyInDays is the optional lodging advance-booking offset (ZBBS-HOME-403):
	// 0/omitted = a room for tonight, N = book N days ahead. Ignored for
	// non-lodging items (the command rejects N>0 there).
	ReadyInDays int `json:"ready_in_days,omitempty"`
}

// pcPayResponse reports the minted offer. On the slow path state is "pending"
// and fast_path is false — the seller resolves it on a later tick and the PC
// reads the outcome off the world snapshot's pay ledger. On the fast path (a
// matched quote_id) the offer is minted already accepted and fast_path is true.
type pcPayResponse struct {
	LedgerID uint64 `json:"ledger_id"`
	State    string `json:"state"`
	FastPath bool   `json:"fast_path"`
}

// handlePCPay makes the caller's PC initiate a pay-with-item offer to a seller
// in their current huddle. requireAuth has populated the session AuthUser. The
// PC is resolved from that session inside the command (world goroutine, no
// TOCTOU); sim.PayWithItem owns all world-state validation — co-presence, scene
// anchor, seller resolution, stock/funds on the fast path, counter-chain rules.
// The handler owns only request shape, numeric bounds, and text validation.
func (s *Server) handlePCPay(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		// requireAuth always populates this; guard rather than nil-deref.
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req pcPayRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Reject trailing content after the JSON object — a clean body leaves
	// exactly io.EOF on the next read.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	seller, item, consumers, forText, msg := validatePayFields(req)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	res, err := s.world.SendContext(r.Context(), payWithItemPCCommand(
		user.Username, seller, item, req.Qty, req.Amount,
		req.ConsumeNow, consumers, sim.QuoteID(req.QuoteID),
		sim.LedgerID(req.InResponseTo), forText, req.ReadyInDays,
	))
	if err != nil {
		// Client disconnected / deadline lapsed before the world replied —
		// nothing useful to write back.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// Every other sim.PayWithItem rejection (not in a huddle, no scene
		// anchor, seller absent/ambiguous, fast-path predicate miss,
		// insufficient stock/funds on the fast path, bad counter chain) is a
		// state-validation failure: the request named real terms but the offer
		// can't proceed in the current world state.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.PayWithItemResult)
	if !ok {
		// PayWithItem always returns a PayWithItemResult on success; a mismatch
		// is a wiring bug, not a client error.
		writeError(w, http.StatusInternalServerError, "unexpected pay result")
		return
	}
	writeJSON(w, pcPayResponse{
		LedgerID: uint64(out.LedgerID),
		State:    string(out.State),
		FastPath: out.FastPath,
	})
}

// validatePayFields applies the handler-owned precondition for pc/pay and
// returns the trimmed seller, item, consumers, and forText — or a non-empty msg
// describing the rejection (→ 400). Numeric lower bounds (amount/qty >= 1) are
// checked here so a malformed offer is a clean 400 rather than a generic 422
// surfaced from the command. The numeric maxima reuse the exported sim.*
// constants (the command's own authority); the char caps mirror the tool schema.
func validatePayFields(req pcPayRequest) (seller, item string, consumers []string, forText, msg string) {
	// Conflicting offer-mode guard. quote_id (fast-path quote accept) and
	// in_response_to (counter-chain response) are mutually exclusive lifecycle
	// intents — allowing both lets the fast path silently win. Reject up front
	// so the ambiguity is a clean 400. sim.PayWithItem enforces the same rule
	// for the NPC/tool callers that bypass this handler.
	if req.QuoteID != 0 && req.InResponseTo != 0 {
		return "", "", nil, "", "quote_id and in_response_to cannot both be set"
	}
	// ready_in_days bounds (ZBBS-HOME-403). The command re-enforces this and the
	// lodging-only rule; checked here so a malformed offset is a clean 400.
	if req.ReadyInDays < 0 || req.ReadyInDays > sim.MaxOrderReadyInDays {
		return "", "", nil, "", "ready_in_days out of range"
	}

	seller = strings.TrimSpace(req.Seller)
	if seller == "" {
		return "", "", nil, "", "seller is required"
	}
	if utf8.RuneCountInString(seller) > maxPayNameChars {
		return "", "", nil, "", "seller exceeds the length limit"
	}
	if hasInvalidControlChar(seller) {
		return "", "", nil, "", "seller contains a disallowed control character"
	}

	item = strings.TrimSpace(req.Item)
	if item == "" {
		return "", "", nil, "", "item is required"
	}
	if utf8.RuneCountInString(item) > maxPayItemChars {
		return "", "", nil, "", "item exceeds the length limit"
	}
	if hasInvalidControlChar(item) {
		return "", "", nil, "", "item contains a disallowed control character"
	}

	if req.Amount < 1 {
		return "", "", nil, "", "amount must be at least 1"
	}
	if req.Amount > sim.MaxPayWithItemAmount {
		return "", "", nil, "", "amount exceeds the maximum"
	}
	if req.Qty < 1 {
		return "", "", nil, "", "qty must be at least 1"
	}
	if req.Qty > sim.MaxPayWithItemQty {
		return "", "", nil, "", "qty exceeds the maximum"
	}

	if len(req.Consumers) > sim.MaxPayWithItemConsumers {
		return "", "", nil, "", "too many consumers"
	}
	if len(req.Consumers) > 0 {
		consumers = make([]string, 0, len(req.Consumers))
		seen := make(map[string]struct{}, len(req.Consumers))
		for _, c := range req.Consumers {
			name := strings.TrimSpace(c)
			if name == "" {
				return "", "", nil, "", "consumer name is required"
			}
			if utf8.RuneCountInString(name) > maxPayNameChars {
				return "", "", nil, "", "consumer name exceeds the length limit"
			}
			if hasInvalidControlChar(name) {
				return "", "", nil, "", "consumer name contains a disallowed control character"
			}
			// Case-insensitive dup reject mirrors the tool handler — the same
			// person named twice is a malformed group order, not two consumers.
			key := strings.ToLower(name)
			if _, dup := seen[key]; dup {
				return "", "", nil, "", "duplicate consumer name"
			}
			seen[key] = struct{}{}
			consumers = append(consumers, name)
		}
	}

	forText = strings.TrimSpace(req.For)
	if utf8.RuneCountInString(forText) > maxPayForChars {
		return "", "", nil, "", "for exceeds the length limit"
	}
	if forText != "" && hasInvalidControlChar(forText) {
		return "", "", nil, "", "for contains a disallowed control character"
	}

	return seller, item, consumers, forText, ""
}

// payWithItemPCCommand resolves username → PC actor (on the world goroutine) and
// delegates to sim.PayWithItem with the PC as the buyer. Same session→actor
// identity rule as movePCCommand / speakPCCommand; the clock is captured inside
// the Fn so the minted offer's timestamps reflect execution time, not how long
// the command sat in the channel.
func payWithItemPCCommand(
	username, seller, item string,
	qty, amount int,
	consumeNow bool,
	consumers []string,
	quoteID sim.QuoteID,
	parentID sim.LedgerID,
	forText string,
	readyInDays int,
) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			// Deliberate PC action: stamp the input cursor + input-wake an asleep
			// PC (ZBBS-WORK-324) before the offer. One clock for both.
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			// ZBBS-HOME-427: form the conversation on the explicit pay action,
			// mirroring pc/speak (ZBBS-HOME-358) and the NPC pay/quote tools'
			// withHuddleBootstrap (ZBBS-HOME-400). sim.PayWithItem gates on the
			// buyer's CurrentHuddleID, and a PC who walked into an open structure
			// has none (a plain walk-in mints no huddle; the huddle can also have
			// concluded while the pay modal sat open, ZBBS-HOME-417's silence
			// sweep) — so without this a well-formed offer to a co-present keeper
			// rejects "not in a conversation". No-op when already huddled, alone,
			// or outdoors, so it never disturbs an existing conversation.
			if _, err := sim.EnsureColocatedHuddle(actorID, now).Fn(world); err != nil {
				return nil, err
			}
			return sim.PayWithItem(
				// PC-side barter (pay_items) is a follow-on slice
				// (ZBBS-HOME-393); the PC pay route stays coin-only for now.
				actorID, seller, item, qty, amount, consumeNow,
				consumers, nil, quoteID, parentID, forText, now,
				// PC partial-payment (deposit) is a follow-on; the PC route
				// stays full-prepay for now (LLM-357 lands NPC-side first).
				sim.PayWithItemOpts{ReadyInDays: readyInDays},
			).Fn(world)
		},
	}
}
