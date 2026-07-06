package sim

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// MaxConsumeQty is the upper bound on qty accepted by the Consume Command,
// mirroring the handler-side cap. Re-enforced inside the Command Fn because
// Consume is exported — non-handler callers (tests, admin paths, future
// in-engine cascades) could otherwise pass an oversized qty and trigger
// silent int wrap on Inventory math.
const MaxConsumeQty = math.MaxInt32

// Sentinel errors for the inventory + consume paths. Callers wrap with
// %w so downstream code (LLM tool error formatter, tests) can match via
// errors.Is.
var (
	// ErrInsufficientInventory — actor doesn't have enough of the item to
	// satisfy the request. Covers both "missing kind entirely" and "have
	// some but not enough" since the LLM-facing signal is the same: "you
	// don't have that to give/consume." Splittable later if a caller
	// actually needs to distinguish.
	ErrInsufficientInventory = errors.New("insufficient inventory")

	// ErrNotConsumable — the item kind exists in the catalog but has no
	// satisfactions, so it can't be consumed (raw materials: wheat, iron).
	// Distinct from ErrUnknownItemKind because the failure mode is different:
	// "you can't ever consume this" vs "we don't know what that is."
	ErrNotConsumable = errors.New("item is not consumable")

	// ErrUnknownItemKind — the item name doesn't resolve to anything in
	// w.ItemKinds (case-insensitive). LLM typo or hallucinated kind.
	ErrUnknownItemKind = errors.New("unknown item kind")

	// ErrOwnProduceStock — the actor works a wholesaler-tagged business and the
	// item is one of its produce rows, so it is stock to sell, not food for the
	// producer (LLM-267). Distinct from ErrNotConsumable: the item IS edible
	// (a farmer's carrots), it just isn't the farmer's to eat.
	ErrOwnProduceStock = errors.New("item is your wholesale stock, not food for you")
)

// transferItem moves qty units of kind from `from` to `to`. Unexported —
// called from inside Command Fns that already hold the world goroutine
// (the upcoming PR S4 accept_pay commit path is the primary caller).
// NOT a public Command: off-goroutine callers shouldn't transfer items
// directly; a hypothetical public TransferItem Command would re-validate
// everything here because the function trusts that callers have already
// done world-state lookups.
//
// Pre-validates qty + seller's stock before any mutation, so by the time
// the writes happen both sides are guaranteed to succeed — no rollback
// machinery needed. Single-goroutine substrate = no observer can see a
// partial state mid-call, so ordering is purely readability.
//
// Mutation behavior:
//
//   - Seller's Inventory[kind] decrements by qty. If the post-decrement
//     value is 0, the entry is deleted (delete-on-zero invariant — keeps
//     perception text clean of "ale: 0" entries, matches the v1 schema's
//     CHECK (quantity > 0) constraint the pg-impl will enforce, and
//     prevents every iteration-over-inventory site from needing a `> 0`
//     filter).
//   - Buyer's Inventory map lazy-inits if nil — Actor zero-value is
//     usable, no per-constructor allocation required.
//   - Buyer's Inventory[kind] += qty.
//
// Returns ErrInsufficientInventory on either "missing kind entirely" or
// "have some but not enough." Other validation failures return
// fmt.Errorf-wrapped descriptions.
func transferItem(_ *World, from, to *Actor, kind ItemKind, qty int) error {
	if qty <= 0 {
		return fmt.Errorf("transferItem: qty must be positive (got %d)", qty)
	}
	have := from.Inventory[kind]
	if have < qty {
		return ErrInsufficientInventory
	}
	from.Inventory[kind] = have - qty
	if from.Inventory[kind] == 0 {
		delete(from.Inventory, kind)
	}
	if to.Inventory == nil {
		to.Inventory = make(map[ItemKind]int)
	}
	to.Inventory[kind] += qty
	return nil
}

// resolveItemKind looks up the canonical ItemKind for a free-text name from
// an LLM tool call. Case-insensitive + leading/trailing whitespace trim.
// Returns ("", false) on no match.
//
// Two-pass match, canonical key first then DisplayLabel:
//
//  1. Canonical key (authoritative, drift-proof). Canonical IDs in
//     w.ItemKinds are lowercase by convention (mem.SeedItemKinds and v1's
//     ZBBS-091/125 seed both lowercase). If two kinds ever differed only by
//     case the lookup would be ambiguous (same trap as
//     findHuddlePeerByDisplayName), but the convention prevents it.
//
//  2. DisplayLabel fallback. The deliberation prompt renders items by
//     DisplayLabel ("Coca Tea" for key "coca_tea"; HOME-361's inventory line,
//     the satiation buy menu), so the model passes the LABEL back in its tool
//     call. Without this pass, consume/pay/etc. fail ErrUnknownItemKind for
//     any item whose label differs from its key — "coca tea" != "coca_tea"
//     (space vs underscore) is the live case (ZBBS-HOME-370). Single-word
//     items happen to work key-only because label-lowercased == key.
//
// Key match wins over label match so a (free-form, possibly colliding) label
// can never shadow a different kind's canonical id. A label reworded in admin
// UI still resolves by key, so the drift concern that originally motivated
// key-only matching is preserved.
//
// Linear scan over ~10 catalog entries per call. No precomputed lookup
// map needed at this scale.
func resolveItemKind(w *World, name string) (ItemKind, bool) {
	// Normalize both sides identically — trim + lowercase. The label passes in
	// particular must trim the catalog labels too: a seeded/admin-edited label
	// with stray surrounding whitespace should still match (code_review).
	normalize := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	needle := normalize(name)
	if needle == "" {
		return "", false
	}
	// LLM-113: tolerate a leading article so the model can echo a cue verbatim
	// ("a tankard of ale" -> "tankard of ale", "an axe" -> "axe"). The
	// singular/plural phrases are stored article-less.
	stripped := stripLeadingArticle(needle)
	matches := func(form string) bool {
		f := normalize(form)
		return f != "" && (f == needle || f == stripped)
	}

	// LLM-113: match on any of key, display label, singular phrase, or plural
	// phrase, so the model may name an item however it likes. Sort the keys so
	// the result is deterministic even when two kinds collide on a form (Go map
	// iteration is randomized); ordered passes (key > label > singular > plural)
	// give the more specific form precedence. The migration keeps the phrases
	// unique across kinds, so a within-pass collision means an admin edit — and
	// lowest-id-wins is then the least-surprising stable resolution.
	kinds := make([]ItemKind, 0, len(w.ItemKinds))
	for kind := range w.ItemKinds {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })

	for _, kind := range kinds {
		if matches(string(kind)) {
			return kind, true
		}
	}
	for _, kind := range kinds {
		if def := w.ItemKinds[kind]; def != nil && matches(def.DisplayLabel) {
			return kind, true
		}
	}
	for _, kind := range kinds {
		if def := w.ItemKinds[kind]; def != nil && matches(def.DisplayLabelSingular) {
			return kind, true
		}
	}
	for _, kind := range kinds {
		if def := w.ItemKinds[kind]; def != nil && matches(def.DisplayLabelPlural) {
			return kind, true
		}
	}
	return "", false
}

// IsCoinToken reports whether an item-name argument is really coins/currency
// rather than a good. A closed allow-list, mirroring isLaborToken (LLM-167):
// no coin token is an authored item kind, so a match unambiguously means the
// model conflated currency with goods. Normalized trim + lower +
// leading-article tolerant, so "the coins" / "a coin" / "Coins" all match.
// Consumers (LLM-290): the pay_with_item coin-payment translation
// (handlers.HandlePayWithItem), the pay_items / scene_quote / offer_trade
// steers, and mintDiscoveredKind's guard — so a coin token can never
// (re-)mint a phantom 'coin' catalog kind.
func IsCoinToken(name string) bool {
	switch stripLeadingArticle(strings.TrimSpace(strings.ToLower(name))) {
	case "coin", "coins":
		return true
	}
	return false
}

// consumeItemLabel is the NPC-facing noun for a kind in a Consume failure
// message (LLM-113): the singular counting phrase ("raspberry", "skillet",
// "bowl of stew"), falling back to the raw kind key when the def is missing or
// unlabeled (a phantom-minted discovery still carries its key). Keeps the
// failure prose plain ("you don't have any raspberry to consume", "you cannot
// eat a skillet") instead of leaking the snake_case key or the plural menu label.
func consumeItemLabel(def *ItemKindDef, kind ItemKind) string {
	if def != nil {
		if s := def.Singular(); s != "" {
			return s
		}
	}
	return string(kind)
}

// notConsumableError carries a model-facing reason that REPLACES the bare
// "item is not consumable" sentinel text. A weak model reads the bare sentinel
// as a system glitch and retries the same eat (LLM-166 — the Josiah raw-meat
// loop, 22 rejected consume() calls in one turn). Unwrap keeps
// errors.Is(err, ErrNotConsumable) matching for callers and tests.
type notConsumableError struct{ msg string }

func (e notConsumableError) Error() string { return e.msg }
func (e notConsumableError) Unwrap() error { return ErrNotConsumable }

// inedibleReason builds the "you cannot eat X — <why>" rejection for an item the
// catalog says isn't consumable. When the kind is a recipe INPUT it names what
// the item is for ("it's used to produce stew") so the model redirects instead
// of re-trying to eat a raw ingredient (LLM-166); otherwise it states the honest
// floor ("it isn't something you can eat as it is"). label is the article-less
// eaten phrase (e.g. "cut of meat") the caller already resolved.
func (w *World) inedibleReason(kind ItemKind, label string) error {
	reason := "it isn't something you can eat as it is"
	if uses := w.ensureRecipeUses()[kind]; len(uses) > 0 {
		labels := make([]string, 0, len(uses))
		for _, out := range uses {
			labels = append(labels, itemKindDisplayLabel(w, out))
		}
		if clause := RecipeUseClause(labels); clause != "" {
			reason = "it's " + clause
		}
	}
	return notConsumableError{msg: "you cannot eat " + WithIndefiniteArticle(label) + " — " + reason}
}

// ownProduceStockError carries a model-facing reason that REPLACES a bare rejection
// when a wholesaler owner tries to eat its own produce (LLM-267). Like
// notConsumableError, a legible reason steers a weak model to a real food source
// instead of retrying the same blocked consume. Unwrap keeps
// errors.Is(err, ErrOwnProduceStock) matching for callers and tests.
type ownProduceStockError struct{ msg string }

func (e ownProduceStockError) Error() string { return e.msg }
func (e ownProduceStockError) Unwrap() error { return ErrOwnProduceStock }

// ownProduceReason builds the "you cannot eat your own X — it is stock to sell"
// rejection for a wholesaler owner's produce item. label is the article-less eaten
// phrase (e.g. "carrots") the caller already resolved.
func ownProduceReason(label string) error {
	return ownProduceStockError{msg: "you cannot eat your own " + label +
		" — it is stock to sell, not your larder. Buy a meal, forage, or trade for food."}
}

// Consume returns a Command that consumes qty units of an item from
// actorID's inventory, applies the immediate satisfaction, stamps any
// item-source dwell credits, and emits ItemConsumed.
//
// Phase 3 PR S2 — the v2 port of v1's `case "consume":` commit arm from
// engine/agent_tick.go, scoped to self-consume only. Group-feed
// (consume.consumers in v1) ports in a later PR alongside the buy/serve
// commerce verbs.
//
// itemName is the raw free-text from the LLM tool call (or test caller);
// the Command resolves it to a canonical ItemKind via resolveItemKind
// (case-insensitive + trim). Handlers pass the trimmed item name through;
// the canonical lookup happens here on the world goroutine where
// w.ItemKinds is safe to read.
//
// Pre-conditions checked here (Consume is exported — non-handler callers
// must not bypass):
//
//   - qty in [1, MaxConsumeQty]
//   - itemName resolves to a kind in w.ItemKinds (case-insensitive)
//   - the ItemKindDef is Consumable() (Satisfies slice non-empty)
//   - actorID resolves to a real actor in w.Actors
//   - actor.MoveIntent == nil (not walk-in-flight)
//   - actor.Inventory[kind] >= qty (sufficient stock)
//
// On success:
//
//   - actor.Inventory[kind] decrements by qty (delete-on-zero invariant)
//   - per-need decrements applied to actor.Needs: for each satisfaction
//     entry with Immediate > 0, ClampNeed(pre - Immediate*qty); qty
//     stacks linearly (3 bowls of stew applies 3× the immediate hit
//     against the clamp). Per-need actual decrement is recorded in
//     Applied for the event.
//   - dwell credits stamped via UpsertItemDwellCredits for any
//     satisfaction entries with HasDwell() — pinned to the named village
//     object the actor is loitering at (resolveLoiteringObject). If no
//     such object (eating-while-walking far from any pin-able structure),
//     dwell upsert is silent-skipped: actor gets the immediate hit but no
//     per-tick payoff. Matches v1 behavior.
//   - emits ItemConsumed{ActorID, Kind, Qty, Applied, At}.
//
// NOT done here (deferred to later PRs alongside their substrate):
//
//   - One-shot dwell hint narration ("This stew looks really good,
//     going to take some time to enjoy properly..."): a PC-only HUD
//     beat the LLM-perception layer will surface from dwell credits when
//     the dwell narration substrate lands.
//
// See shared/tasks/engine-in-memory-rewrite/dwell-substrate-design for
// the dwell-related items still on the roadmap.
func Consume(actorID ActorID, itemName string, qty int, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Re-validate qty inside the Command Fn — Consume is exported,
			// so non-handler callers (tests, admin paths, future in-engine
			// cascades) could pass qty<=0 (silent inventory math wrap) or
			// qty>MaxInt32 (silent int32 wrap if Inventory ever becomes
			// int32-typed downstream). Both rejected at decode for the
			// handler path; defense in depth here.
			if qty < 1 {
				return nil, fmt.Errorf("Consume: qty must be at least 1 (got %d)", qty)
			}
			if qty > MaxConsumeQty {
				return nil, fmt.Errorf("Consume: qty exceeds maximum (got %d, max %d)", qty, MaxConsumeQty)
			}

			// ZBBS-WORK-412: mint an unknown kind at qty 0 instead of failing
			// with ErrUnknownItemKind. The actor holds 0 of a freshly-minted
			// kind, so this consume still fails below — at the inventory gate,
			// surfacing the honest "you don't have any X" (the discovery design's
			// own stated intent) rather than a catalog-shape rejection.
			kind, ok := resolveOrMintItemKind(w, itemName)
			if !ok {
				return nil, fmt.Errorf("Consume: %w %q", ErrUnknownItemKind, itemName)
			}
			def := w.ItemKinds[kind]

			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("Consume: actor %q not in world", actorID)
			}
			if actor.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — finish your move before consuming. " +
						"Either consume BEFORE the move_to, or wait until you arrive.",
				)
			}

			// LLM-113: order the gates none-held -> inedible -> not-enough so each
			// failure reads as the actor's own pack-perception would ground it.
			//   - holds none (incl. a phantom-minted qty-0 kind): the honest "you
			//     don't have any X", never "has no satisfactions".
			//   - holds some but it's not food (an axe in the pack): "you cannot
			//     eat an axe" wins over a quantity quibble, even on a consume-2.
			//   - holds some food but short: "you only have N".
			label := consumeItemLabel(def, kind)
			have := actor.Inventory[kind]
			if have == 0 {
				return nil, fmt.Errorf("you don't have any %s to consume: %w", label, ErrInsufficientInventory)
			}
			if def == nil || !def.Consumable() {
				return nil, w.inedibleReason(kind, label)
			}
			// LLM-267: a wholesaler owner cannot eat its own produce — the item is
			// stock to sell, not its larder. Rejected even at starvation (no red-need
			// escape): forage is free and always legal, and barter works. Keyed on the
			// same sim.IsOwnProduce the satiation eat-cue filters on, so the cue never
			// offers what this guard would block.
			if IsOwnProduce(w.VillageObjects, actor.WorkStructureID, actor.RestockPolicy, kind) {
				return nil, ownProduceReason(label)
			}
			if have < qty {
				return nil, fmt.Errorf("you only have %d of those to consume, not %d: %w", have, qty, ErrInsufficientInventory)
			}

			// ZBBS-WORK-391: consume only what the actor's needs can absorb;
			// the surplus stays in inventory rather than burning into an
			// already-zeroed need. Shares consumableUnits with the
			// commitPayTransfer consume_now clamp so a pocketed purchase
			// surplus can't be wasted by a follow-up consume either.
			eat := consumableUnits(actor, def, qty)

			// Mutate inventory: decrement (delete-on-zero invariant). Same
			// invariant transferItem enforces — keeps inventory iteration
			// sites (perception, S3 scene_quote, future inventory-render)
			// free of `> 0` guards.
			actor.Inventory[kind] -= eat
			if actor.Inventory[kind] == 0 {
				delete(actor.Inventory, kind)
			}

			// Apply immediate satisfactions. Qty stacks linearly — eating 3
			// bowls of stew applies Immediate*3 to hunger (then clamps at 0).
			// Pre-need=post-need entries are dropped from Applied so the event
			// only carries needs that actually moved (rendering "the gnawing
			// ebbs" only fires when hunger actually dropped, not for a
			// not-hungry consume).
			if actor.Needs == nil {
				actor.Needs = make(map[NeedKey]int)
			}
			var applied map[NeedKey]int
			for _, s := range def.Satisfies {
				if s.Immediate <= 0 {
					continue
				}
				pre := actor.Needs[s.Attribute]
				post := ClampNeed(pre - s.Immediate*eat)
				if pre == post {
					continue
				}
				actor.Needs[s.Attribute] = post
				if applied == nil {
					applied = make(map[NeedKey]int)
				}
				applied[s.Attribute] = pre - post
			}

			// Stamp item-source dwell credits for satisfactions with a
			// complete dwell triple. Pin to the named village object whose
			// loiter pin the actor stands at (Chebyshev <= 1 tile) via
			// resolveLoiteringObject — the v1 resolveLoiteringStructure
			// attribution every reverse-lookup now shares. If no qualifying
			// object (eating-while-walking far from any pin), structureID=""
			// and UpsertItemDwellCredits silent-skips — matches v1 behavior
			// where eat-while-walking gets only the immediate hit, not the
			// per-tick payoff.
			//
			// When at least one credit lands, emit DwellStarted so dwell-
			// reactor subscribers can stamp the next-tick perception cue
			// ("this stew looks really good — you'll need some time to
			// enjoy it properly"). No event when nothing landed (skipped
			// item, eat-while-walking, no dwell triples on satisfactions).
			structureID, _ := resolveLoiteringObject(w, actor.Pos, LoiterAttributionTiles)
			var stamped []DwellCreditSnapshot
			if structureID != "" {
				stamped = UpsertItemDwellCredits(actor, kind, def.Satisfies, structureID, at)
			}

			w.emit(&ItemConsumed{
				ActorID: actorID,
				Kind:    kind,
				Qty:     eat,
				Kept:    qty - eat,
				Applied: applied,
				At:      at,
			})
			if len(stamped) > 0 {
				w.emit(&DwellStarted{
					ActorID:       actorID,
					Kind:          kind,
					StructureID:   structureID,
					Credits:       stamped,
					NarrationText: def.ConsumeDwellNarration,
					At:            at,
				})
			}
			// LLM-7: surface the post-consume felt state (the same helper the
			// pay_with_item eat path uses) so the harness can steer a sated NPC to
			// stop, instead of a bare [ok] that the stale eat-affordance furniture
			// overrides into a re-eat loop. Only meaningful when something was eaten.
			res := ConsumeResult{Kind: kind, Requested: qty, Consumed: eat, Kept: qty - eat, EasedNeed: len(applied) > 0, ConsumedNoun: def.CountNoun(eat)}
			if eat > 0 {
				res.SatisfiesNeed, res.FeltAfter = buyerFeltAfterConsume(actor, def, w.Settings.NeedThresholds)
			}
			return res, nil
		},
	}
}

// ConsumeResult reports what a Consume command actually did once the
// ZBBS-WORK-391 needs-clamp has applied: Consumed units left inventory and
// eased needs; Kept (= Requested - Consumed) stayed in the actor's pack
// because their needs couldn't absorb more. The harness uses Kept > 0 to
// tell the model its over-sized consume was clamped — without that signal a
// "consume 10" answered by a bare [ok] reads as fully eaten, and the model
// re-consumes the surplus it doesn't know it still holds.
type ConsumeResult struct {
	Kind      ItemKind
	Requested int
	Consumed  int
	Kept      int
	// ConsumedNoun is the count-aware display noun for Consumed (LLM-113):
	// "raspberry" at 1, "raspberries" at >1, "bowl of stew"/"bowls of stew" for
	// a mass noun — so commitResultContent renders "you consume 3 raspberries"
	// off the catalog phrasing rather than the raw kind key.
	ConsumedNoun string
	// SatisfiesNeed / FeltAfter mirror PayWithItemResult: the primary need the
	// consumed item eases and the eater's post-consume felt label ("" = sated).
	// LLM-7: lets commitResultContent voice "your hunger is met — eat no more
	// now" / "you still feel peckish" after a need-moving consume, so the stale
	// within-tick eat-affordance furniture stops priming a re-eat loop.
	SatisfiesNeed NeedKey
	FeltAfter     string
	// EasedNeed reports whether this consume actually moved at least one need
	// (len(applied) > 0). It is the senseless-repeat signal the LLM-91 harness
	// guard arms on. NOTE it is NOT "Consumed == 0": a sated actor's consume
	// still eats and wastes a unit (Consumed >= 1) by design (ZBBS-WORK-391
	// consumableUnits floors eat to 1 — consuming while full wastes a unit), so
	// the only way to detect "this consume eased nothing" is whether a need
	// moved, not whether stock left the pack (LLM-107).
	EasedNeed bool
}
