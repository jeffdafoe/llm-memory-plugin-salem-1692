package sim

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// scene_quote_commands.go — Phase 3 PR S3.
//
// sim.SceneQuoteCreate is the substrate-level Command for the
// scene_quote tool. Implements the 10 validation gates locked at
// scene-quote-design § 5 (seller scene, item catalog, break, stock,
// target buyer resolution, consumer-huddle requirement, consumer
// resolution, numeric guards, duplicate-key replacement, per-(seller,
// scene) cap), then mints the quote and emits SceneQuoteCreated.
//
// The handler (handlers/scene_quote.go) is a pure builder per PR shape
// rule #3 — static-only validation, no world reads. World-state
// validation runs here per rule #4 — Command Fn re-validates
// everything the handler did, since SceneQuoteCreate is exported and
// non-handler callers (tests, future in-engine cascades) could
// otherwise bypass the bounds.

// MaxSceneQuoteAmount caps the bundle total in coins. Matches
// MaxPayAmount so a buyer's pay_with_item fast-path doesn't need to
// reconcile a different ceiling at acceptance time.
const MaxSceneQuoteAmount = math.MaxInt32

// MaxSceneQuoteQty caps qty-per-consumer. Same ceiling as
// MaxConsumeQty. The Command Fn additionally enforces that
// Qty * effectiveConsumerCount doesn't overflow before the stock
// check uses the product.
const MaxSceneQuoteQty = math.MaxInt32

// SceneQuoteCreateResult is the value returned by the SceneQuoteCreate
// Command — the handler narrates QuoteID and ExpiresAt back to the
// LLM so it has a stable identifier to reference in a follow-up
// speak ("I've got 2 stew for 6 coins, quote #5") and a timer
// horizon. Returned by Command.Fn; the handler wraps it for the
// tool result.
type SceneQuoteCreateResult struct {
	QuoteID   QuoteID
	ExpiresAt time.Time
}

// SceneQuoteCreate returns a Command that mints a SceneQuote on the
// seller's current scene and emits SceneQuoteCreated. Phase 3 PR S3.
//
// itemName is raw free-text from the LLM tool call; the Command
// resolves it to a canonical ItemKind via resolveItemKind
// (case-insensitive + trim) — same pattern as Consume.
//
// targetBuyerName empty = public quote; non-empty = single addressed
// buyer (resolved against the scene's participants, NOT the huddle —
// scene is the quote's visibility scope, design § 5 gate 5).
//
// consumerNames empty = buyer is implicit single consumer; non-empty =
// group-order participant list (resolved against the seller's huddle
// peers, design § 5 gate 7). Requires seller to have an active huddle.
//
// Per design § 5 the gates run in this order: cheap structural checks
// first (scene, catalog), then state (break, stock, resolution), then
// maintenance (duplicate-key, cap). Maximizes early-exit and lets
// the costly cap-displacement and emit work only on validated inputs.
//
// On success:
//
//   - Mints a fresh QuoteID via w.nextQuoteSeq.
//   - Inserts SceneQuote into w.Quotes (state Active, ExpiresAt =
//     now + effective TTL).
//   - Appends QuoteID to scene.QuoteIDs.
//   - Emits SceneQuoteCreated (subscriber stamps targeted-buyer
//     warrant if TargetBuyer is an NPC).
//   - Returns SceneQuoteCreateResult{QuoteID, ExpiresAt}.
//
// On duplicate-key (gate 9): old quote → terminal Superseded,
// removed from scene index, SceneQuoteExpired{Reason: "superseded"}
// emitted BEFORE the new quote's SceneQuoteCreated, so admin replay
// sees the displacement in causal order.
//
// On cap-hit (gate 10): oldest active quote in (seller, scene)
// bucket → terminal CapDisplaced, same removal + emit shape as
// supersede.
func SceneQuoteCreate(
	sellerID ActorID,
	itemName string,
	qty int,
	amount int,
	consumeNow bool,
	targetBuyerName string,
	consumerNames []string,
	at time.Time,
) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Defense-in-depth re-validation. SceneQuoteCreate is
			// exported — non-handler callers (tests, admin paths,
			// future cascades) could pass through bad shapes.
			if qty < 1 {
				return nil, fmt.Errorf("SceneQuoteCreate: qty must be at least 1 (got %d)", qty)
			}
			if qty > MaxSceneQuoteQty {
				return nil, fmt.Errorf("SceneQuoteCreate: qty exceeds maximum (got %d, max %d)", qty, MaxSceneQuoteQty)
			}
			if amount < 1 {
				return nil, fmt.Errorf("SceneQuoteCreate: amount must be at least 1 (got %d)", amount)
			}
			if amount > MaxSceneQuoteAmount {
				return nil, fmt.Errorf("SceneQuoteCreate: amount exceeds maximum (got %d, max %d)", amount, MaxSceneQuoteAmount)
			}

			seller, ok := w.Actors[sellerID]
			if !ok {
				return nil, fmt.Errorf("SceneQuoteCreate: seller %q not in world", sellerID)
			}

			// Gate 1: seller has an active scene. Scenes observe
			// huddles; the seller's scene is derived from
			// seller.CurrentHuddleID → look up scene in w.Scenes
			// whose Huddles set contains that huddle. Quotes have
			// no perceptive audience without one.
			if seller.CurrentHuddleID == "" {
				return nil, errors.New(
					"you're not in a conversation — start one with the people you want to quote items to first.",
				)
			}
			sceneID, ok := resolveSellerScene(w, seller.CurrentHuddleID)
			if !ok {
				return nil, errors.New(
					"your current conversation isn't anchored to a scene — wait for the scene to be established before posting a quote.",
				)
			}
			scene := w.Scenes[sceneID]

			// Gate 2: ItemKind exists in catalog. resolveItemKind
			// handles trim + case-insensitive match.
			kind, ok := resolveItemKind(w, itemName)
			if !ok {
				return nil, fmt.Errorf(
					"unknown item kind %q — check the items available in this world before quoting.",
					itemName,
				)
			}

			// Gate 3: closed-shop / break gate (simple-strict).
			// Same rule applied at pay_with_item / accept_pay per
			// ledger-substrate § 11. Future structure-level
			// closed-shop check goes here too.
			if seller.BreakUntil != nil && seller.BreakUntil.After(at) {
				return nil, errors.New(
					"you're on a break right now — wait until your break ends before posting a quote.",
				)
			}

			// Gate 6 (before gate 4 — consumer requires huddle is
			// a structural prerequisite to the stock-check
			// arithmetic, since effectiveConsumerCount depends on
			// the consumer list shape).
			if len(consumerNames) > 0 && seller.CurrentHuddleID == "" {
				// Defensive — gate 1 already enforced the huddle
				// requirement, but a future change could relax
				// gate 1 (e.g. public-scene quotes without a
				// huddle). Keep this check tight to the consumer
				// semantic.
				return nil, errors.New(
					"consumers can only be specified within an active huddle.",
				)
			}
			if len(consumerNames) > SceneQuoteMaxConsumers {
				return nil, fmt.Errorf(
					"too many consumers (got %d, max %d) — split the order into smaller quotes.",
					len(consumerNames), SceneQuoteMaxConsumers,
				)
			}

			// Gate 7: consumer resolution. Per-name case-insensitive
			// trim-match against seller's huddle peers; ambiguity
			// + missing both reject; duplicate-name reject;
			// seller-as-consumer reject. Buyer-as-consumer
			// allowed (the buyer often is one of the consumers in
			// "round of ale for the table" semantics).
			consumerIDs, err := resolveQuoteConsumers(w, seller, consumerNames)
			if err != nil {
				return nil, err
			}

			// Gate 8 + 4: numeric range guard, then stock check.
			// Computed together because the multiplication is the
			// overflow risk.
			effectiveConsumers := len(consumerIDs)
			if effectiveConsumers == 0 {
				effectiveConsumers = 1
			}
			// Overflow guard: Qty * effectiveConsumers must fit in
			// int. Both inputs already capped at MaxSceneQuoteQty
			// (MaxInt32), so on a 32-bit int platform the product
			// could wrap; on 64-bit int it cannot. Defensive guard
			// anyway, since Inventory[kind] is int and a wrapped
			// negative would silently pass the stock check.
			if qty > math.MaxInt/effectiveConsumers {
				return nil, fmt.Errorf(
					"SceneQuoteCreate: qty %d × %d consumers overflows int — split the order.",
					qty, effectiveConsumers,
				)
			}
			needed := qty * effectiveConsumers
			if seller.Inventory[kind] < needed {
				return nil, fmt.Errorf(
					"insufficient stock (have %d %s, need %d) — quote a smaller quantity before posting.",
					seller.Inventory[kind], kind, needed,
				)
			}

			// Gate 5: TargetBuyer resolution. Empty stays empty
			// (public quote). Non-empty resolves against actors
			// currently in the scene — every actor in any huddle
			// the scene observes is a scene participant. Same
			// ambiguity / missing rejection as findHuddlePeerByDisplayName.
			var targetBuyerID ActorID
			if strings.TrimSpace(targetBuyerName) != "" {
				targetBuyerID, err = resolveQuoteTargetBuyer(w, sellerID, scene, targetBuyerName)
				if err != nil {
					return nil, err
				}
			}

			// Gate 9: duplicate-key resolution. Non-Amount key —
			// the legitimate use case for "same key, new amount"
			// IS re-pricing. Replace any matching active quote in
			// the same (seller, scene) bucket; the replaced
			// quote → terminal Superseded BEFORE the new quote's
			// SceneQuoteCreated emit so admin replay sees the
			// displacement in causal order.
			//
			// Run BEFORE the cap check (design § 4): an exact-key
			// duplicate frees a slot, which may save us from
			// hitting the cap.
			supersedeMatchingQuotes(w, scene, sellerID, kind, qty, consumeNow, targetBuyerID, consumerIDs, at)

			// Gate 10: per-(seller, scene) cap. Count remaining
			// active quotes in the bucket (post-supersede); if at
			// cap, displace the oldest active quote.
			displaceQuotesIfAtCap(w, scene, sellerID, at)

			// Mint + insert + emit.
			id := w.nextQuoteSeq()
			ttl := effectiveSceneQuoteTTL(w.Settings)
			expiresAt := at.Add(ttl)
			q := &SceneQuote{
				ID:          id,
				SceneID:     sceneID,
				SellerID:    sellerID,
				TargetBuyer: targetBuyerID,
				ItemKind:    kind,
				Qty:         qty,
				ConsumeNow:  consumeNow,
				ConsumerIDs: consumerIDs,
				Amount:      amount,
				State:       SceneQuoteStateActive,
				CreatedAt:   at,
				ExpiresAt:   expiresAt,
			}
			w.Quotes[id] = q
			scene.QuoteIDs = append(scene.QuoteIDs, id)

			evt := &SceneQuoteCreated{
				QuoteID:     id,
				SceneID:     sceneID,
				SellerID:    sellerID,
				TargetBuyer: targetBuyerID,
				ItemKind:    kind,
				Qty:         qty,
				Amount:      amount,
				ConsumeNow:  consumeNow,
				ConsumerIDs: cloneActorIDs(consumerIDs),
				ExpiresAt:   expiresAt,
				At:          at,
			}
			w.emit(evt)

			// Stamp the source-event lineage on the quote AFTER
			// emit so the IDs are populated. Subscriber dispatch
			// has already run by this point (synchronous), so
			// the warrant subscriber sees the quote's lineage
			// via the event itself rather than via this back-
			// stamp; the back-stamp is for admin / replay queries
			// that walk World.Quotes directly.
			q.RootEventID = evt.RootEventID()
			q.SourceEventID = evt.EventID()

			return SceneQuoteCreateResult{
				QuoteID:   id,
				ExpiresAt: expiresAt,
			}, nil
		},
	}
}

// resolveSellerScene picks the scene the seller's quote should anchor
// to from the set of scenes observing seller.CurrentHuddleID. A
// structure huddle may be observed by multiple structure scenes
// minted at the same structure over time; pick deterministically by
// lexicographically smallest SceneID — matches the perception layer's
// resolvePrimaryScene fallback so the quote ends up on the same
// scene the LLM is reasoning within.
//
// Returns ("", false) when no scene observes the huddle (the
// "huddle exists but no scene anchors it" race window).
func resolveSellerScene(w *World, huddleID HuddleID) (SceneID, bool) {
	var candidates []SceneID
	for id, scene := range w.Scenes {
		if scene == nil {
			continue
		}
		if _, observed := scene.Huddles[huddleID]; observed {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	return candidates[0], true
}

// resolveQuoteTargetBuyer walks every actor in every huddle the scene
// observes, looking for a single case-insensitive DisplayName match
// against name. Excludes the seller (you can't target-quote yourself).
// Ambiguity → reject, missing → reject — same rules as
// findHuddlePeerByDisplayName.
func resolveQuoteTargetBuyer(w *World, sellerID ActorID, scene *Scene, name string) (ActorID, error) {
	target := strings.TrimSpace(name)
	if target == "" {
		return "", errors.New("target_buyer is empty after trim")
	}
	var found ActorID
	seen := make(map[ActorID]struct{})
	for huddleID := range scene.Huddles {
		members, ok := w.actorsByHuddle[huddleID]
		if !ok {
			continue
		}
		for actorID := range members {
			if actorID == sellerID {
				continue
			}
			if _, dup := seen[actorID]; dup {
				continue
			}
			seen[actorID] = struct{}{}
			actor, ok := w.Actors[actorID]
			if !ok {
				continue
			}
			if strings.EqualFold(actor.DisplayName, target) {
				if found != "" {
					return "", fmt.Errorf(
						"more than one person named %q is in this scene — use a unique full name before quoting.",
						target,
					)
				}
				found = actorID
			}
		}
	}
	if found == "" {
		return "", fmt.Errorf(
			"no one named %q in this scene — re-check who is here before quoting.",
			target,
		)
	}
	return found, nil
}

// resolveQuoteConsumers resolves each consumer name to a huddle peer
// ActorID. Same rules per consumer entry as findHuddlePeerByDisplayName
// PLUS: duplicate-name reject (model intent unclear), seller-as-
// consumer reject. Buyer-as-consumer is allowed (the buyer often is
// in the consumer list for "round at the table" semantics — though
// at this layer we don't know who the buyer is, so the actual
// buyer-as-consumer enforcement happens at pay_with_item time).
//
// Returns the resolved IDs in input order; an empty input returns nil
// (matches the convention used elsewhere — empty == buyer is sole
// consumer).
func resolveQuoteConsumers(w *World, seller *Actor, names []string) ([]ActorID, error) {
	if len(names) == 0 {
		return nil, nil
	}
	members, ok := w.actorsByHuddle[seller.CurrentHuddleID]
	if !ok {
		return nil, errors.New(
			"consumers can only be specified within an active huddle.",
		)
	}
	resolved := make([]ActorID, 0, len(names))
	seen := make(map[ActorID]struct{}, len(names))
	for _, raw := range names {
		target := strings.TrimSpace(raw)
		if target == "" {
			return nil, errors.New("consumer name is empty after trim — every consumer must have a name.")
		}
		var found ActorID
		for peerID := range members {
			peer, ok := w.Actors[peerID]
			if !ok {
				continue
			}
			if strings.EqualFold(peer.DisplayName, target) {
				if found != "" {
					return nil, fmt.Errorf(
						"more than one person named %q is in this conversation — use a unique full name.",
						target,
					)
				}
				found = peerID
			}
		}
		if found == "" {
			return nil, fmt.Errorf(
				"no one named %q in this conversation — re-check who is here before quoting.",
				target,
			)
		}
		if found == seller.ID {
			return nil, errors.New(
				"the seller can't be a consumer of their own quote — drop your own name from the consumer list.",
			)
		}
		if _, dup := seen[found]; dup {
			return nil, fmt.Errorf(
				"%q appears more than once in the consumer list — list each person only once.",
				target,
			)
		}
		seen[found] = struct{}{}
		resolved = append(resolved, found)
	}
	return resolved, nil
}

// supersedeMatchingQuotes finds any active quote in (sellerID, scene)
// whose non-Amount key matches and flips it to Superseded. Amount is
// NOT part of the key — re-pricing the same terms IS the legitimate
// use case. Per scene-quote-design § 4 the consumer-set match is
// order-independent.
//
// Emits SceneQuoteExpired{Reason: "superseded"} for each replaced
// quote BEFORE the caller's subsequent mint emits
// SceneQuoteCreated, so admin replay sees the displacement in
// causal order.
//
// Typically replaces 0 or 1 quote (the key is tight enough that
// natural use produces unique entries); the design doesn't promise
// "at most one match," so the loop handles any count.
func supersedeMatchingQuotes(
	w *World,
	scene *Scene,
	sellerID ActorID,
	kind ItemKind,
	qty int,
	consumeNow bool,
	targetBuyerID ActorID,
	consumerIDs []ActorID,
	at time.Time,
) {
	if scene == nil || len(scene.QuoteIDs) == 0 {
		return
	}
	// Snapshot the index — we'll mutate scene.QuoteIDs via
	// removeQuoteFromSceneIndex inside the loop.
	ids := make([]QuoteID, len(scene.QuoteIDs))
	copy(ids, scene.QuoteIDs)
	for _, qid := range ids {
		q, ok := w.Quotes[qid]
		if !ok || q == nil || q.State != SceneQuoteStateActive {
			continue
		}
		if q.SellerID != sellerID {
			continue
		}
		if q.ItemKind != kind || q.Qty != qty || q.ConsumeNow != consumeNow || q.TargetBuyer != targetBuyerID {
			continue
		}
		if !actorIDSetsEqual(q.ConsumerIDs, consumerIDs) {
			continue
		}
		flipQuoteTerminal(w, scene, q, SceneQuoteStateSuperseded, SceneQuoteExpiredReasonSuperseded, at)
	}
}

// displaceQuotesIfAtCap counts active quotes in (sellerID, scene) and,
// if at SceneQuoteMaxPerSellerScene, flips the oldest active one to
// CapDisplaced. Loops in case the bucket is over-cap (defensive —
// natural use can't exceed cap by more than one, but a future
// pathway could).
func displaceQuotesIfAtCap(w *World, scene *Scene, sellerID ActorID, at time.Time) {
	if scene == nil {
		return
	}
	for {
		var active []*SceneQuote
		for _, qid := range scene.QuoteIDs {
			q, ok := w.Quotes[qid]
			if !ok || q == nil || q.State != SceneQuoteStateActive {
				continue
			}
			if q.SellerID != sellerID {
				continue
			}
			active = append(active, q)
		}
		if len(active) < SceneQuoteMaxPerSellerScene {
			return
		}
		// Find oldest by CreatedAt (tie-break by QuoteID for
		// determinism — sub-microsecond mints in tests can share
		// a timestamp).
		sort.Slice(active, func(i, j int) bool {
			if !active[i].CreatedAt.Equal(active[j].CreatedAt) {
				return active[i].CreatedAt.Before(active[j].CreatedAt)
			}
			return active[i].ID < active[j].ID
		})
		flipQuoteTerminal(w, scene, active[0], SceneQuoteStateCapDisplaced, SceneQuoteExpiredReasonCapDisplaced, at)
	}
}

// flipQuoteTerminal performs the common terminal-state transition:
// flip State + stamp ResolvedAt, drop from scene.QuoteIDs index,
// emit SceneQuoteExpired with the given reason. Caller guarantees q
// is currently active.
func flipQuoteTerminal(w *World, scene *Scene, q *SceneQuote, state SceneQuoteState, reason string, at time.Time) {
	q.State = state
	q.ResolvedAt = at
	removeQuoteFromSceneIndex(scene, q.ID)
	w.emit(&SceneQuoteExpired{
		QuoteID:  q.ID,
		SceneID:  q.SceneID,
		SellerID: q.SellerID,
		Reason:   reason,
		At:       at,
	})
}

// actorIDSetsEqual reports whether two ActorID slices contain the same
// elements (order-independent, multiplicity-aware). Used by the
// duplicate-key check (consumer sets must match exactly regardless of
// the order the LLM listed names). Cheap O(n²) given the
// SceneQuoteMaxConsumers=8 cap; a map-based path isn't worth the
// allocation for tiny slices.
func actorIDSetsEqual(a, b []ActorID) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	counts := make(map[ActorID]int, len(a))
	for _, id := range a {
		counts[id]++
	}
	for _, id := range b {
		counts[id]--
		if counts[id] < 0 {
			return false
		}
	}
	return true
}

// cloneActorIDs returns an independent copy of ids. Used to give
// emitted events their own backing array so a mutation of the
// SceneQuote's slice post-emit doesn't reach back into a subscriber's
// retained event reference.
func cloneActorIDs(ids []ActorID) []ActorID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]ActorID, len(ids))
	copy(out, ids)
	return out
}
