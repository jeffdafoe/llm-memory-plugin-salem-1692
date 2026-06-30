package perception

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// RenderConfig holds the deterministic limits the prompt renderer enforces.
// Every limit is a hard cap applied after deterministic ordering, so the
// same Payload + RenderConfig always produce the same RenderedPrompt and
// the same DroppedWarrants set.
//
// Any field left <= 0 falls back to its DefaultRenderConfig value — the
// same "<= 0 means default" convention the engine's WorldSettings use.
type RenderConfig struct {
	// MaxWarrants is the most warrants rendered into the "what just
	// happened" section. Warrants past the cap are dropped (carried
	// forward), not silently consumed.
	MaxWarrants int

	// MaxBytesPerWarrant caps the untrusted free-text payload of a single
	// warrant (e.g. a speech excerpt). Text past the cap is truncated with
	// a marker; the warrant is still rendered.
	MaxBytesPerWarrant int

	// MaxSectionBytes caps the total byte size of the rendered warrant
	// section. Once a warrant would push the section past the cap, that
	// warrant and every warrant after it are dropped (carried forward).
	MaxSectionBytes int
}

// DefaultRenderConfig returns the baseline limits. These are mechanism
// defaults — sized to keep the prompt bounded, not tuned for final prompt
// content (content fills in incrementally in later work).
func DefaultRenderConfig() RenderConfig {
	return RenderConfig{
		MaxWarrants:        12,
		MaxBytesPerWarrant: 600,
		MaxSectionBytes:    4000,
	}
}

// normalized returns a copy with every <= 0 field replaced by its default.
func (c RenderConfig) normalized() RenderConfig {
	d := DefaultRenderConfig()
	if c.MaxWarrants <= 0 {
		c.MaxWarrants = d.MaxWarrants
	}
	if c.MaxBytesPerWarrant <= 0 {
		c.MaxBytesPerWarrant = d.MaxBytesPerWarrant
	}
	if c.MaxSectionBytes <= 0 {
		c.MaxSectionBytes = d.MaxSectionBytes
	}
	return c
}

// RenderedPrompt is the output of Render: the prompt text plus the
// accounting the harness loop needs.
type RenderedPrompt struct {
	// Text is the DURABLE turn — the "what just happened" events, what the NPC
	// should REMEMBER. This is what the chat adapter persists and replays as
	// conversation history (lean sim-history, ZBBS-WORK-364). Self-state (## You)
	// was moved OUT of here into EphemeralText by ZBBS-WORK-410 — it is point-in-
	// time and a prior tick's stale "Coins in your purse: 0" was replaying as if
	// it were the actor's current balance.
	Text string

	// EphemeralText is per-tick decision-support that must NOT persist into
	// history: the ## You self-state (coins/needs/carried goods, ZBBS-WORK-410),
	// identity, surroundings, affordances (rest/food/lodging), owed orders, pay
	// offers, and the act-now coda. The adapter attaches it to the CURRENT turn
	// only (memory-api: /chat/send ephemeral_context). Splitting it out keeps
	// replayed history lean — neither the static furniture nor the stale self-
	// state can pile up once per historical tick.
	EphemeralText string

	// ContinuationText is the lean post-speak decision body the harness swaps in
	// after the actor's first committed speak this tick (ZBBS-HOME-411). It leads
	// with the current ## You self-state (ZBBS-WORK-410, so a multi-round tick
	// keeps live coins/needs in view once EphemeralText is swapped out), then
	// drops the actionable affordances EphemeralText carries — the inn/food/rest
	// cues and the act-now coda that prime a re-pitch — for a stop-biased decision
	// instead. Round 1 sends EphemeralText (the model may act); once the actor has
	// spoken, the recency-dominant text biases it to done() rather than re-offer
	// what it just said. Pairs with HOME-402's speak cap (the backstop) and the
	// WORK-375 per-speak tool-result steer.
	ContinuationText string

	// RenderedWarrantCount is how many warrants made it into the prompt.
	RenderedWarrantCount int

	// TruncatedWarrants is how many rendered warrants had their free-text
	// payload truncated by MaxBytesPerWarrant. They were still rendered —
	// this is a quality signal, not a drop.
	TruncatedWarrants int

	// DroppedWarrants are warrants that were consumed by the tick but did
	// not fit under MaxWarrants / MaxSectionBytes. They MUST be carried
	// forward — the harness loop puts them in TickResult.UnaddressedWarrants
	// so CompleteReactorTick re-opens them. Dropping them silently would
	// recreate the "consumed but never addressed" state the warrant system
	// exists to eliminate.
	DroppedWarrants []sim.WarrantMeta
}

// continuationDecisionText is the recency-dominant body the harness sends on
// rounds AFTER the actor has already spoken this tick (RenderedPrompt.
// ContinuationText, ZBBS-HOME-411). It replaces EphemeralText's affordances +
// act-now coda with a stop-biased decision, mirroring the WORK-375 per-speak
// tool-result steer so the two stop-signals agree. It deliberately omits any
// "unless something new arrived" clause: within a single tick no new external
// event is incorporated (the prompt is rendered once; new events spawn separate
// ticks via the reactor), so there is nothing new for the model to inspect, and
// naming the possibility would only invite it to invent one (code_review, HOME-411).
const continuationDecisionText = "## Decide\n" +
	"You have already spoken this turn — let others respond. Call done() unless a prior tool result needs a word, you owe a distinct answer someone asked of you, or a needed non-speaking action remains (such as moving to where you can rest or tend a need). Do not greet again, re-pitch, or rephrase what you have already said.\n"

// Render turns a Payload into a prompt string. It is a pure function:
// deterministic ordering (already applied in Build) is preserved, the
// caps in cfg are applied after ordering, and dropped warrants are
// surfaced for carry-forward rather than discarded.
//
// PR 3c ships the rendering *mechanism* — section structure, escaping of
// untrusted text, the deterministic caps, and the drop→carry-forward
// path. The prompt *content* (the exact prose, the persona framing, the
// tool-schema block) fills in incrementally; this is intentionally a
// plain, structured rendering.
func Render(p Payload, cfg RenderConfig) RenderedPrompt {
	cfg = cfg.normalized()

	var out RenderedPrompt
	// Two streams (lean sim-history, ZBBS-WORK-364). `durable` is the "what just
	// happened" events — what the NPC should REMEMBER; the chat adapter persists
	// and replays it as conversation history. `ephemeral` is per-tick decision-
	// support (self-state, identity, surroundings, affordances, owed orders, pay
	// offers, the act-now coda) the adapter attaches to the CURRENT turn only and
	// never persists, so the static furniture can't accumulate once per historical
	// tick. The split is by SECTION — each renderer below is routed to one stream.
	var durable strings.Builder
	var ephemeral strings.Builder

	// Self-state (## You: coins, felt needs, carried goods) is per-tick decision-
	// support, NOT durable memory — it is point-in-time and goes stale the instant
	// the tick ends. It rides the EPHEMERAL stream (and is prepended to the post-
	// speak continuation body below), so it shows on every round of the CURRENT
	// tick but never enters the persisted/replayed history. When it was durable, a
	// prior tick's "Coins in your purse: 0" replayed as if current and the NPC
	// behaved as though broke (ZBBS-WORK-410). Rendered once, reused for both
	// ephemeral bodies.
	var selfState strings.Builder
	renderActor(&selfState, p.Actor)

	// Durable: just the turn header here; the "what just happened" events append
	// below (## You is ephemeral now — ZBBS-WORK-410).
	durable.WriteString("# Your turn\n\n")

	// nameOf resolves an actor UUID to the subject's name for them — "you" for
	// self, the acquaintance-gated label (Build's WarrantActorNames) for
	// others, "someone" when unresolvable. The fix for warrant lines leaking
	// raw UUIDs ("[arrived] involving 019da6af…"). ZBBS-HOME-339.
	nameOf := func(id sim.ActorID) string {
		if id == "" {
			return "someone"
		}
		if id == p.ActorID {
			return "you"
		}
		if label, ok := p.WarrantActorNames[id]; ok && label != "" {
			return sanitizeInline(label)
		}
		return "someone"
	}

	// placeNameOf resolves a destination id (structure or village object) named
	// by an arrival warrant to its display name, "" when unresolvable — the
	// counterpart to nameOf for the "You arrived at <place>" line (ZBBS-WORK-358).
	placeNameOf := func(id string) string {
		if id == "" {
			return ""
		}
		return sanitizeInline(p.WarrantPlaceNames[id])
	}

	// eatHereKind reports whether a kind always settles eat-here (Build's
	// EatHereKinds set, ZBBS-WORK-405) — the quote warrant line states the
	// disposition fact so the model never plans a carry-out it can't have.
	eatHereKind := func(kind sim.ItemKind) bool {
		return p.EatHereKinds[kind]
	}

	// buyRedundancy reports, for a quoted item, whether the buyer MAKES it itself
	// (produced) or already holds it at cap (atCap) — LLM-171. renderWarrantLine
	// uses it to strip the actionable take from a buy-quote whose every line is
	// redundant, so a co-present seller's mis-pitched quote can't drive the buyer
	// to buy back its own ware or overflow its carry.
	buyRedundancy := func(kind sim.ItemKind) (produced, atCap bool) {
		return p.OwnProducedKinds[kind], p.AtCapKinds[kind]
	}

	// stockOf reports the subject's current on-hand of a kind and whether they
	// stock it at all — the seller-side bound for the pay-offer cue
	// (ZBBS-HOME-459). Built from the standing inventory readout (real goods,
	// qty>0); a service or never-stocked kind is absent, so stocked is false and
	// the cue's "you hold only N" annotation is skipped for it.
	sellerStock := make(map[sim.ItemKind]int, len(p.Actor.Inventory))
	for _, it := range p.Actor.Inventory {
		sellerStock[it.kind] = it.Qty
	}
	stockOf := func(kind sim.ItemKind) (int, bool) {
		qty, ok := sellerStock[kind]
		return qty, ok
	}

	// Pay offers render as an actionable decision section (renderPayOffers)
	// so the seller gets the ledger_id it must echo into accept_pay/
	// decline_pay/counter_pay. Sourced from the standing ledger scan
	// (Payload.PayOffersForMe, ZBBS-HOME-453), NOT the consumed warrant
	// batch — the offer warrant only wakes the seller's first tick, and a
	// seller who speaks through that tick must keep seeing the offer until
	// it resolves or expires. The same PendingPayOffers(p) predicate drives
	// the handlers tool-gate (gateTools), so the rendered offer and the
	// advertised response tools cannot drift. Rendering them in a dedicated,
	// uncapped section (rather than as a capped warrant line) guarantees the
	// ledger_id is present whenever the tools are advertised.
	payOffers := PendingPayOffers(p)

	// Ephemeral: self-state first (ZBBS-WORK-410), then identity, surroundings,
	// anchors, steers, relationships, the offers awaiting this actor's decision,
	// owed orders, recovery/satiation/restock/lodging affordances, summons, scene.
	ephemeral.WriteString(selfState.String())
	renderLaborSelfState(&ephemeral, p.Laboring, nameOf, p.RenderedAt)
	renderPendingLaborOfferOut(&ephemeral, p.PendingLaborOfferOut, nameOf)
	renderNarrativeState(&ephemeral, p.NarrativeState)
	renderVendorOperating(&ephemeral, p.AtOwnBusinessOperating)
	renderSurroundings(&ephemeral, p.Surroundings)
	renderAnchors(&ephemeral, p.Anchors, p.DutySteer != nil && p.DutySteer.AtPost)
	renderDutySteer(&ephemeral, p.DutySteer)
	renderEveningLeisure(&ephemeral, p.EveningLeisure)
	renderRelationships(&ephemeral, p.Relationships)
	renderRecentConversation(&ephemeral, p.RecentConversation)
	// The decision section renders ABOVE the affordance dumps (it used to land
	// after them): a buyer's coin on the table is the seller's most actionable
	// fact, and burying it under eat/drink and room-to-let cues let the
	// seller's own mild needs outrank a waiting customer for whole minutes
	// (conversation hud-6c849d…, ZBBS-HOME-424). renderTriage reinforces the
	// same priority at the decision point.
	renderPayOffers(&ephemeral, payOffers, nameOf, stockOf, p.RoomAlreadySoldOrderByLedger)
	// LLM-138: a gift offered TO this actor is the same "someone wants my answer"
	// decision class as a pay offer, so it renders right alongside.
	renderGiftsForMe(&ephemeral, p.GiftsForMe)
	// LLM-26: the employer's pending work-offer decisions sit alongside pay
	// offers (both are "someone wants my answer"); the worker affordance cue
	// follows so a free worker sees the option to offer their labor.
	renderLaborOffers(&ephemeral, p.LaborOffersForMe, p.Actor.Coins, nameOf)
	renderLaborAffordance(&ephemeral, p.CanSolicitWork)
	// LLM-152/160: the directional half of seek-work — the town's businesses to head
	// to, by their resolvable names. Sits with the labor affordance; non-empty
	// whenever the subject is a broke idle worker with no employer present to solicit
	// (a STANDING cue, see the build-side gate), so move_to always has a real target.
	renderSeekWorkPlaces(&ephemeral, p.SeekWorkPlaces)
	renderOfferableCustomers(&ephemeral, p.OfferableCustomers)
	renderTradeValue(&ephemeral, p.TradeValue)
	renderStandingQuotesFromMe(&ephemeral, p.StandingQuotesFromMe)
	renderPendingDeliveriesFromMe(&ephemeral, p.PendingDeliveriesFromMe, p.LocalDateUTC, p.RenderedAt)
	renderPendingDeliveriesToMe(&ephemeral, p.PendingDeliveriesToMe, p.LocalDateUTC, p.RenderedAt)
	renderPendingOffersFromMe(&ephemeral, p.PendingOffersFromMe)
	renderRecentlyResolvedOffersFromMe(&ephemeral, p.RecentlyResolvedOffersFromMe)
	// LLM-138: the giver-side gift counterparts — own gifts still standing, then
	// own gifts just settled.
	renderGiftsFromMe(&ephemeral, p.GiftsFromMe)
	renderSettledGiftsFromMe(&ephemeral, p.SettledGiftsFromMe)
	renderCountersAwaitingMyResponse(&ephemeral, p.CountersAwaitingMyResponse)
	renderRecoveryOptions(&ephemeral, p.RecoveryOptions)
	renderSatiation(&ephemeral, p.Satiation)
	renderProductionInputs(&ephemeral, p.ProductionInputs)
	renderForgeChoice(&ephemeral, p.ForgeChoice)
	renderStallRepair(&ephemeral, p.StallRepair)
	renderStallCondition(&ephemeral, p.StallCondition)
	renderRestocking(&ephemeral, p.Restocking)
	renderForage(&ephemeral, p.Forage)
	renderLodging(&ephemeral, p.Lodging)
	renderRetire(&ephemeral, p.Retire)
	renderKeeperLodging(&ephemeral, p.KeeperLodging)
	renderKeeperHeldLodgers(&ephemeral, p.KeeperHeldLodgers)
	renderLodgingOffer(&ephemeral, p.LodgingOffer)
	renderSummonsForYou(&ephemeral, p.SummonsForYou)
	renderSummonRefusal(&ephemeral, p.SummonRefusal)
	renderScene(&ephemeral, p)
	// "## Other scenes in play" (renderSecondary) was dropped — it surfaced raw
	// scene/huddle UUIDs and a "N signal(s)" count the LLM can't act on
	// (ZBBS-HOME-339). Secondary-scene warrants still render in the flat
	// "what just happened" list; only the machine telemetry block is gone.

	// Shift-duty warrants drive the wake tick but are NOT rendered — the standing
	// DutySteer cue (renderDutySteer, above) is the single voice for
	// return-to-post (ZBBS-HOME-352). Filtering here also keeps them out of the
	// cap / carry-forward budget; consuming them unrendered is fine since their
	// job is to wake the actor, which the tick already did.
	warrants := nonShiftDutyWarrants(p.Warrants)
	if len(payOffers) > 0 {
		warrants = nonPayOfferWarrants(warrants)
	}
	// Durable: the "what just happened" events are the NPC's memory of the
	// scene. Skip the generic block only when the pay-offer section already
	// covered the whole batch; otherwise render it (this also preserves the
	// routine-check-in line for the genuinely-empty case). Warrant caps +
	// carry-forward accounting land in `out` as before.
	if len(warrants) > 0 || len(payOffers) == 0 {
		renderWarrants(&durable, warrants, nameOf, placeNameOf, eatHereKind, buyRedundancy, cfg, &out)
	}

	// Ephemeral: the turn-state nudge, the act-now coda, and the rest-first steer
	// are instructions for THIS tick, not facts to remember. The turn-line lands
	// before the coda so the coda's "weigh everything above" sees it; the coda
	// itself swaps to a wait-framing when the actor is awaiting a reply.
	// LLM-160: a populated SeekWorkPlaces means a workless worker with no employer
	// present — the directive is "leave for a business". That overrides the
	// conversational reply-pressure (suppress the owed-reply nag) and swaps the coda
	// to a decisive go-line, so the model stops agree-looping and actually moves.
	seekWorkDirective := len(p.SeekWorkPlaces) > 0
	// LLM-169: a looping huddle (members re-echoing a settled agreement) ALSO
	// suppresses the owed-reply nag — that nag is exactly what manufactures the
	// echo — while renderTriage's coda swaps to an "act now or done()" steer below.
	conversationLooping := p.TurnState.ConversationLooping
	renderTurnState(&ephemeral, p.TurnState, seekWorkDirective || conversationLooping)
	renderTriage(&ephemeral, p.Actor.Needs, p.Actor.NeedThresholds, p.TurnState.AwaitingReply(), conversationLooping, p.NeedRedirect, seekWorkDirective, len(payOffers) > 0, p.Actor.InFlightMove, p.Actor.InFlightSourceActivity)

	out.Text = durable.String()
	out.EphemeralText = ephemeral.String()
	// The post-speak continuation body also leads with current self-state so a
	// multi-round tick keeps live coins/needs in view when EphemeralText's
	// affordance furniture is swapped out (ZBBS-WORK-410).
	out.ContinuationText = selfState.String() + continuationDecisionText
	return out
}

// renderTurnState writes the conversation turn-state lines (ZBBS-WORK-370): who
// the actor owes a reply to, and who it is awaiting a reply from. The awaiting
// line is the cadence fix — it tells the model it has already spoken and must
// not re-pitch a peer who hasn't answered; renderTriage's coda swap reinforces
// it. Both lists are acquaintance-gated labels resolved at build time. Emits
// nothing when there is no pending turn (the common case).
func renderTurnState(b *strings.Builder, ts TurnStateView, suppressOwedReply bool) {
	// suppressOwedReply drops the "X is waiting for your reply" nag (LLM-160): when
	// the actor's one productive move is to leave for work (the seek-work directive),
	// the reply-pressure is exactly what kept it agree-looping instead of going. The
	// "you already spoke, wait" half below still renders — it discourages re-pitching
	// and is aligned with leaving.
	if !suppressOwedReply {
		for _, name := range ts.OwedReplyTo {
			fmt.Fprintf(b, "%s is waiting for your reply.\n", sanitizeInline(name))
		}
	}
	if len(ts.AwaitingReplyFrom) > 0 {
		fmt.Fprintf(b,
			"You already spoke to %s and are waiting for their reply. Do not repeat "+
				"yourself or address them again — attend to your own work, or simply wait.\n",
			joinNames(ts.AwaitingReplyFrom))
	}
}

// joinNames renders a name list as readable prose: "A", "A and B", or
// "A, B, and C". Each name is sanitized inline (the build-time labels are
// already acquaintance-gated). Returns "" for an empty list.
func joinNames(names []string) string {
	clean := make([]string, 0, len(names))
	for _, n := range names {
		clean = append(clean, sanitizeInline(n))
	}
	switch len(clean) {
	case 0:
		return ""
	case 1:
		return clean[0]
	case 2:
		return clean[0] + " and " + clean[1]
	default:
		return strings.Join(clean[:len(clean)-1], ", ") + ", and " + clean[len(clean)-1]
	}
}

// dormantClause renders the co-present sleepers and resters as a single
// not-addressable clause, e.g. " Prudence Ward is asleep and Goodman Stark is
// resting; neither will respond if you speak to them." (leading space, trailing
// period, so it appends cleanly after a presence line). Same-state members are
// grouped ("X and Y are asleep") and the two groups joined; the tail agrees in
// number. Empty when no one nearby is dormant. ZBBS-WORK-426.
func dormantClause(asleep, resting []HuddleMember) string {
	n := len(asleep) + len(resting)
	if n == 0 {
		return ""
	}
	groups := make([]string, 0, 2)
	if len(asleep) > 0 {
		groups = append(groups, stateGroup(asleep, "asleep"))
	}
	if len(resting) > 0 {
		groups = append(groups, stateGroup(resting, "resting"))
	}
	if n == 1 {
		return fmt.Sprintf(" %s and won't respond if you speak to them.", groups[0])
	}
	tail := "neither will respond if you speak to them"
	if n >= 3 {
		tail = "none of them will respond if you speak to them"
	}
	// At most two groups (asleep, resting), so a plain " and " join reads right.
	return fmt.Sprintf(" %s; %s.", strings.Join(groups, " and "), tail)
}

// stateGroup renders one same-state set of dormant actors with name-vs-descriptor
// gating, e.g. "Prudence Ward and the farmer are asleep" / "Goodman Stark is
// resting". ZBBS-WORK-426.
func stateGroup(members []HuddleMember, state string) string {
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = descriptorLabel(m.DisplayName, m.Role, m.Acquainted)
	}
	verb := "is"
	if len(names) > 1 {
		verb = "are"
	}
	return joinNames(names) + " " + verb + " " + state
}

// renderNeedRedirect writes the LLM-176 need-driven loop coda: in place of the
// generic "do what you've agreed" line — which endorses a confabulated plan when
// the agreement is imaginary ("there's bread in the kitchen") — it names the one
// affordance the engine knows resolves the actor's pressing consumable need plus
// the imperative to act on it now. Need-agnostic phrasing via Verb (eat/drink);
// the move targets carry the inline structure_id every actionable cue does, so
// move_to resolves them. Mirrors the seek-work go-line and the duty steer.
func renderNeedRedirect(v NeedRedirectView) string {
	switch v.Kind {
	case NeedRedirectConsume:
		return fmt.Sprintf("You and the others here keep saying the same thing, but you already carry %s. Don't talk it over again — consume it now to %s.\n",
			sanitizeInline(v.ItemLabel), v.Verb)
	case NeedRedirectBuy:
		return fmt.Sprintf("You and the others here keep saying the same thing, but there is nothing to %s here. Don't talk it over again — go to %s (structure_id: %s) now and buy %s to %s.\n",
			v.Verb, sanitizeInline(v.TargetLabel), sanitizeInline(v.TargetID), sanitizeInline(v.ItemLabel), v.Verb)
	default: // NeedRedirectFree
		return fmt.Sprintf("You and the others here keep saying the same thing, but there is nothing to %s here. Don't talk it over again — go to %s (structure_id: %s) now and %s.\n",
			v.Verb, sanitizeInline(v.TargetLabel), sanitizeInline(v.TargetID), v.Verb)
	}
}

// renderTriage writes the closing prioritization instruction — the synthesis
// keystone (ZBBS-HOME-355). The per-tick prompt is a set of equal-weight context
// sections (felt needs, return-to-post, owed orders, vendor cues, what-just-
// happened), several of which can carry an imperative at once (e.g. "Address
// now: hunger" AND "head to your post"). Nothing told the model how to choose
// between them, so it acted on whatever was most salient and drifted. This line
// does NOT impose an engine-computed ranking (the model is capable — the
// prioritization stays in the model); it just instructs the model to weigh the
// context and commit to ONE action, nudging the KIND of triage that the live
// wandering exposed: obligations to others and pressing needs over idle drift.
// Rendered unconditionally — Render is only called on the NPC reactor-tick path
// (handlers.Harness.RunTick), never for a PC.
func renderTriage(b *strings.Builder, needs map[sim.NeedKey]int, thresholds sim.NeedThresholds, awaitingReply bool, conversationLooping bool, needRedirect *NeedRedirectView, seekWork bool, hasPayOffers bool, inFlightMove *InFlightMoveView, inFlightSourceActivity *InFlightSourceActivityView) {
	// A buyer's offer awaiting this actor's answer outranks everything below —
	// including the actor's own felt needs, which the coda's "pressing needs"
	// phrasing otherwise licenses to win. Without this, a starving seller read
	// his own hunger as the obligation and let a customer's coin sit for whole
	// minutes (conversation hud-6c849d…, ZBBS-HOME-424).
	if hasPayOffers {
		b.WriteString("A buyer's offer awaits your answer — settle it first with accept_pay, decline_pay, or counter_pay, before tending to your own needs.\n")
	}
	switch {
	case inFlightSourceActivity != nil:
		// Mid-activity coda (LLM-69) — the source-activity analogue of the mid-walk
		// coda below. A tick that fires while the actor is mid eat/drink/harvest
		// (a PC speaking, a red need — the interrupts the reactor now lets through)
		// must not render the act-now coda and steer the model into a move that
		// abandons the pick. Make finishing the legible default; responding stays
		// available when what the tick surfaced gives real cause.
		fmt.Fprintf(b,
			"You are %s and it will finish on its own shortly. Weigh what's above — "+
				"answer anyone who needs you, but do not walk away without real cause: "+
				"leaving now abandons it. Otherwise call done() and let it finish.\n",
			sourceActivityPhrase(*inFlightSourceActivity))
	case inFlightMove != nil:
		// Mid-walk coda (ZBBS-HOME-439) — the walking analogue of WORK-370's
		// awaiting-reply swap. A tick that fires while the actor has a
		// committed walk used to render the act-now coda ("Choose one
		// action") against a toolset the walk gating had narrowed to
		// essentially stop / move_to / done — and the model obliged with
		// stop, killing its own commute (live: Josiah cancelled both of his
		// morning walks to the General Store within seconds, 2026-06-12).
		// Make continuing the legible default; stop stays available for a
		// genuine change of course prompted by what the tick surfaced.
		fmt.Fprintf(b,
			"You are already %s. Weigh what's above — unless it gives you a real "+
				"reason to change course, call done() and keep walking. Do not stop "+
				"without cause.\n",
			renderInFlightMove(*inFlightMove))
	case seekWork:
		// Seek-work directive coda (LLM-160/168): a workless worker with no employer
		// present has one productive move — leave and go to a business. The awaiting-
		// reply and default codas let the huddle's "X is waiting for your reply" social
		// pressure win, and the model re-agreed ("yes, let's go") tick after tick without
		// ever calling move_to (the live Walker agree-loop). Make leaving the imperative;
		// the businesses directory rendered above carries the resolvable destination
		// names. Coins-neutral — a workless worker may hold a little coin and still have
		// no work of its own (LLM-168), so the coda asserts only the actionable facts (no
		// hirer here, go now), not the purse state, which the self-state line already
		// carries. Ordered below the in-flight codas so an actor already walking keeps walking.
		b.WriteString("No one here can hire you. Don't keep talking about going — pick one of the businesses listed above and call move_to now.\n")
	case conversationLooping:
		// Conversational-loop coda (LLM-169): the actor's huddle is going in
		// circles — members re-stating the same agreement without it converting to
		// action (the live Walker "let's go to the well" / "let's go!" echo). The
		// default and awaiting-reply codas both let the reply-pressure win and the
		// echo re-arms; this names the loop and makes resolving it the imperative —
		// act on what's agreed, or let it rest with done(), anything but say it
		// again. The social-loop analogue of the seek-work go-line above; the
		// owed-reply nag is suppressed in renderTurnState so the two steers agree.
		// Ordered below seek-work so a workless worker still gets the leave-for-work
		// directive, and above awaiting-reply since "looping" is the more specific
		// read of why a reply is pending.
		//
		// LLM-176: a need-driven loop circles a CONFABULATED plan ("check the kitchen
		// for bread"), and the generic line above tells them to "do what you've
		// agreed" — endorsing the confabulation. When the actor has a felt consumable
		// need with a real listed source, swap in a concrete redirect that names the
		// engine's known affordance + a move_to/consume imperative (the duty-steer
		// pattern). Falls back to the generic line when no target resolves.
		if needRedirect != nil {
			b.WriteString(renderNeedRedirect(*needRedirect))
		} else {
			b.WriteString("You and the others here keep saying the same thing — the matter is already settled between you. Don't say it again: do what you've agreed — move, tend your work or a need — or call done() and let the moment rest. Speak again only if you truly have something new.\n")
		}
	case awaitingReply:
		// Turn-state coda (ZBBS-WORK-370): the actor has spoken and is awaiting a
		// reply. The default "choose one thing and do it" imperative is exactly
		// what drove the re-pitch loop (live-trace finding #2) — it commands an
		// action every tick even when the right move is to wait. Swap it for a
		// wait-permitting framing: real needs/obligations above still license an
		// action, but "nothing new to add" now resolves to done() instead of a
		// repeated pitch.
		b.WriteString("Weigh everything above. If the most pressing matter is simply awaiting someone's reply, do not repeat yourself — wait and call done(). Otherwise act on what matters most: obligations to others and pressing needs come before idle matters.\n")
	default:
		// Universal decision section (ZBBS-WORK-374), replacing the bare HOME-355
		// "choose one thing and do it" coda. Same intent — weigh the context, act
		// on what matters — but it now carries the turn-discipline at the decision
		// point: after one utterance, default to done() and let others answer,
		// speaking again only on genuinely new input. The live storm (Hannah's six
		// room-pitches in one tick) read the old action-command coda every round
		// and re-pitched; this makes "say your piece, then yield" the recency-
		// dominant instruction. (The hard within-tick stop is WORK-375; this is the
		// prompt half.)
		b.WriteString("Weigh what's in front of you — obligations and pressing needs come before idle matters. Choose one action. After you speak, call done() and let others respond; speak again only if something new happens or someone asks you for more — never repeat or rephrase what you've already said.\n")
	}
	// Rest-first steer (ZBBS-WORK-354). When the actor is deeply fatigued AND
	// another need is also pressing, the model otherwise flip-flops between "buy
	// food" and "I need rest" and resolves neither. Steer it to rest first: an
	// actor that sleeps both clears tiredness and pauses all other need growth
	// (IncrementNeedsTick skips a sleeping actor), so resolving rest first is
	// unambiguously the better ordering. Gated on Peak fatigue only — while
	// merely mild/moderately tired the model is free to choose food-vs-rest
	// itself (Jeff: "early on they can make a choice").
	if deepFatigueDominatesNeeds(needs, thresholds) {
		b.WriteString("You are exhausted — rest before tending to other needs; you will handle them better once you have recovered.\n")
	}
}

// deepFatigueDominatesNeeds reports whether the rest-first triage steer should
// fire: tiredness is at NeedPeak (maxed — "exhausted") AND at least one other
// need (hunger or thirst) is also pressing (NeedRed or worse). This is the
// dual-distress case the steer targets. Returns false below Peak fatigue (the
// model chooses freely) or when tiredness alone is pressing (nothing to order
// against). nil thresholds is safe — NeedThresholds.Get falls back to registry
// defaults. ZBBS-WORK-354.
func deepFatigueDominatesNeeds(needs map[sim.NeedKey]int, thresholds sim.NeedThresholds) bool {
	tiredValue, ok := needs["tiredness"]
	if !ok {
		return false
	}
	if sim.NeedLabelTier(tiredValue, thresholds.Get("tiredness")) < sim.NeedPeak {
		return false
	}
	for _, key := range []sim.NeedKey{"hunger", "thirst"} {
		value, ok := needs[key]
		if !ok {
			continue
		}
		if sim.NeedLabelTier(value, thresholds.Get(key)) >= sim.NeedRed {
			return true
		}
	}
	return false
}

func renderActor(b *strings.Builder, a ActorView) {
	b.WriteString("## You\n")
	if line := renderFeltNeeds(a.Needs, a.NeedThresholds); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}
	// Tiredness renders on its own situated line (LLM-85), separate from the
	// hunger/thirst felt line above: a descriptive tier phrase anchored to hours
	// awake, with NO "address this" imperative — the actionable rest affordances
	// live in the "## How you can rest" menu (buildRecoveryOptions).
	if v, ok := a.Needs[recoveryTirednessNeed]; ok {
		if line := renderTiredness(v, a.NeedThresholds.Get(recoveryTirednessNeed), a.HoursAwake); line != "" {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	// An empty purse is a hard constraint on paying, not just a number (LLM-153).
	// Without the consequence spelled out, 0-coin NPCs burned tool calls attempting
	// buys the pay path rejects (engine/sim/pay_commands.go). The wording is coin-
	// specific so it leaves barter untouched — a 0-coin actor can still offer_trade
	// goods-for-goods (ZBBS-HOME-393/407), which needs no coins.
	if a.Coins == 0 {
		b.WriteString("Coins in your purse: 0 — you have no coins to spend, so you cannot pay for anything until you earn some.\n")
	} else {
		fmt.Fprintf(b, "Coins in your purse: %d.\n", a.Coins)
	}
	// Standing inventory readout (ZBBS-HOME-361): neutral statement of what the
	// actor holds, so it's aware of its own goods (to eat, to sell, to give)
	// without being pushed to act — the "consume to eat" nudge stays in the
	// need-gated satiation own-stock line. Omitted when carrying nothing.
	if len(a.Inventory) > 0 {
		b.WriteString("You are carrying: ")
		for i, it := range a.Inventory {
			if i > 0 {
				b.WriteString(", ")
			}
			// The use annotation folds into the quantity parens (LLM-166) so the
			// comma-separated item list stays unambiguous: "Meat (x7, used to
			// produce stew)". Empty for edibles / non-ingredients.
			if it.Use != "" {
				fmt.Fprintf(b, "%s (x%d, %s)", sanitizeInline(it.Label), it.Qty, sanitizeInline(it.Use))
			} else {
				fmt.Fprintf(b, "%s (x%d)", sanitizeInline(it.Label), it.Qty)
			}
		}
		b.WriteString(".\n")
	}
	// Standing production focus (LLM-116): a multi-output crafter's chosen work,
	// surfaced on EVERY tick — including a social one when someone approaches —
	// so the crafter can always say what it is making (a PC can ask).
	if a.ProductionFocusLabel != "" {
		fmt.Fprintf(b, "You are making %s.\n", sanitizeInline(a.ProductionFocusLabel))
	}
	// In-progress activity reads as felt self-state. A meal/rest/walk already
	// under way is surfaced so a tick firing mid-activity doesn't re-pick a
	// goal from scratch (the dwell-credit/in-flight-move parking fix). These
	// also cover the resting/walking macro-states, so the bare state line only
	// fires when nothing else already conveys what the actor is doing.
	activity := false
	// A timed source activity (eat/drink/harvest in flight, LLM-69) leads — it is
	// the most occupied state, and the reactor now lets high-value interrupts tick
	// the actor mid-window, so this standing line is what tells it to hold rather
	// than walk off (the live forage→walk-off bug).
	if a.InFlightSourceActivity != nil {
		fmt.Fprintf(b, "You are %s.\n", renderInFlightSourceActivity(*a.InFlightSourceActivity))
		activity = true
	}
	for _, c := range a.ActiveDwellCredits {
		fmt.Fprintf(b, "You are %s.\n", renderActiveDwellCredit(c))
		activity = true
	}
	if a.InFlightMove != nil {
		fmt.Fprintf(b, "You are %s.\n", renderInFlightMove(*a.InFlightMove))
		activity = true
	}
	if !activity {
		if line := renderFeltState(a.State); line != "" {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
}

// renderFeltNeeds turns the hunger/thirst need values into felt language in the
// fixed hunger→thirst order. Needs below the awareness floor stay silent.
// Red/peak needs lead with an "Address now:" imperative — v1's 2026-05-02 fix
// that made NPCs act on distress instead of reading a flat integer they
// couldn't calibrate (the original "needs: hunger=24" dump gave the model no
// sense that 24 is peak starvation). Tiredness is intentionally NOT handled here
// (LLM-85) — it renders as its own situated, descriptive line, renderTiredness.
// Returns "" when nothing is surfaced. ZBBS-HOME-339.
func renderFeltNeeds(needs map[sim.NeedKey]int, thresholds sim.NeedThresholds) string {
	if len(needs) == 0 {
		return ""
	}
	var felt, pressing []string
	for _, key := range []sim.NeedKey{"hunger", "thirst"} {
		value, ok := needs[key]
		if !ok {
			continue
		}
		n, ok := sim.FindNeed(key)
		if !ok {
			continue
		}
		tier := n.Tier(value, thresholds.Get(key))
		label := n.Label(tier)
		if label == "" {
			continue // NeedSilent — below the awareness floor
		}
		felt = append(felt, label)
		if tier >= sim.NeedRed {
			pressing = append(pressing, string(key))
		}
	}
	if len(felt) == 0 {
		return ""
	}
	if len(pressing) > 0 {
		return fmt.Sprintf("Address now: %s. You feel %s.",
			strings.Join(pressing, ", "), strings.Join(felt, ", "))
	}
	return fmt.Sprintf("You feel %s.", strings.Join(felt, ", "))
}

// renderTiredness renders the actor's tiredness as its own situated, descriptive
// line: the qualitative tier (a little tired / weary / exhausted) anchored to how
// long the actor has been awake, so the model weighs rest against real elapsed
// time instead of over-reacting to a bare adjective (LLM-85 — a merchant closed
// his shop 4h on a mild "tired"). It deliberately carries NO "address this"
// imperative at any tier: the concrete rest affordances live in the "## How you
// can rest" menu (buildRecoveryOptions), and dropping the imperative everywhere
// completes LLM-67 (the felt imperative was the stimulus for the re-take_break
// loop). hoursAwake is nil off-shift, for an unscheduled NPC, or a clock-less
// snapshot — then the awake-hours tail is dropped and only the tier phrase
// renders. Returns "" below the awareness floor.
func renderTiredness(value, threshold int, hoursAwake *int) string {
	n, ok := sim.FindNeed(recoveryTirednessNeed)
	if !ok {
		return ""
	}
	var lead string
	switch n.Tier(value, threshold) {
	case sim.NeedMild:
		lead = "You're starting to feel a little tired"
	case sim.NeedRed:
		lead = "You're weary"
	case sim.NeedPeak:
		lead = "You're exhausted"
	default:
		return "" // NeedSilent — below the awareness floor
	}
	if hoursAwake != nil && *hoursAwake >= 1 {
		unit := "hours"
		if *hoursAwake == 1 {
			unit = "hour"
		}
		return fmt.Sprintf("%s — you've been awake for %d %s.", lead, *hoursAwake, unit)
	}
	return lead + "."
}

// renderFeltState renders a macro-state as a felt line, or "" for states that
// carry no standalone meaning (idle) or are already conveyed by the dwell/move
// lines (walking). Only reached when renderActor surfaced no in-progress
// activity. ZBBS-HOME-339.
func renderFeltState(state sim.ActorState) string {
	switch state {
	case sim.StateResting:
		return "You are taking a rest."
	case sim.StateSleeping:
		return "You are asleep."
	case sim.StateWorking:
		return "You are at work."
	case sim.StateConversing:
		return "You are in conversation."
	case sim.StateShopping:
		return "You are out shopping."
	case sim.StateInTransaction:
		return "You are in the middle of a transaction."
	case sim.StateEating:
		return "You are eating."
	default: // idle, walking, unknown — nothing standalone to add
		return ""
	}
}

// renderInFlightMove produces the felt-language self-perception line for an
// actor mid-walk ("walking to enter the Tavern"). The movement analogue of
// renderActiveDwellCredit: present on every perception build while the walk is
// live, so a reactor tick triggered mid-journey (by heard speech, a need,
// anything) shows the LLM it already has a destination and shouldn't re-pick
// one from scratch — the fix for the senseless goal-flipping that the
// dwell-credit line already prevents for meals. ZBBS-HOME-336.
func renderInFlightMove(m InFlightMoveView) string {
	dest := m.DestinationLabel
	if dest == "" {
		return "walking to your destination"
	}
	if m.Kind == sim.MoveDestinationStructureEnter {
		return fmt.Sprintf("walking to enter %s", sim.WithDefiniteArticle(sanitizeInline(dest)))
	}
	if m.Kind == sim.MoveDestinationPosition {
		// A bare coordinate label ("(41, 44)") names no place — no article.
		return fmt.Sprintf("walking to %s", sanitizeInline(dest))
	}
	return fmt.Sprintf("walking to %s", sim.WithDefiniteArticle(sanitizeInline(dest)))
}

// sourceActivityVerb picks the second-person verb for an in-flight source
// activity: "gathering" for a harvest, and eat/drink/rest for a refresh keyed on
// the eased need (falling back to "busy" for an unknown attribute). LLM-69.
func sourceActivityVerb(v InFlightSourceActivityView) string {
	switch v.Kind {
	case sim.SourceActivityHarvest:
		return "gathering"
	case sim.SourceActivityRepair:
		return "mending"
	}
	switch v.Attribute {
	case "hunger":
		return "eating"
	case "thirst":
		return "drinking"
	case "tiredness":
		return "resting"
	}
	return "busy"
}

// sourceActivityPhrase is the bare "<verb> at <source>" clause shared by the
// standing self-state line and the mid-activity triage coda, so both name the
// activity identically. Drops the "at <source>" when the label didn't resolve.
func sourceActivityPhrase(v InFlightSourceActivityView) string {
	verb := sourceActivityVerb(v)
	if v.SourceLabel != "" {
		return verb + " at " + sanitizeInline(v.SourceLabel)
	}
	return verb
}

// renderInFlightSourceActivity produces the standing self-perception line for an
// actor mid eat/drink/harvest ("gathering at the bush — stay where you are; if
// you walk off now you abandon the pick"). The source-activity analogue of
// renderInFlightMove: present on every perception build while the window is live,
// so a reactor tick that fires mid-activity (a PC speaking, a red need) shows the
// LLM it is occupied and holds it in place rather than re-deciding into a move
// that abandons the pick (LLM-69). The caller prepends "You are ".
func renderInFlightSourceActivity(v InFlightSourceActivityView) string {
	tail := "if you walk off now you won't finish"
	switch v.Kind {
	case sim.SourceActivityHarvest:
		tail = "if you walk off now you abandon the pick and gather nothing"
	case sim.SourceActivityRepair:
		tail = "if you walk off now the mending is unfinished and the stall stays worn"
	}
	return fmt.Sprintf("%s — stay where you are; %s", sourceActivityPhrase(v), tail)
}

// renderActiveDwellCredit produces the felt-language self-perception
// line for one in-progress dwell credit ("eating stew at the tavern, it
// will take you 14 more minutes to finish eating it all. ..."). The
// load-bearing prompt line that keeps
// LLM-driven NPCs from walking away mid-meal: every perception build
// during the meal renders this, so plan-stage always sees the active
// effect even if no per-tick narration warrant landed this turn.
//
// Source=item with a known Kind → "eating <kind> at <where>".
// Source=item with empty Kind → "having a meal at <where>" (fallback).
// Source=object → "resting at <where>" / "drawing from <where>" by
// attribute (covers shade-tree tiredness, well thirst, berry-bush
// hunger).
func renderActiveDwellCredit(c DwellCreditView) string {
	where := c.StructureLabel
	if where == "" && c.ObjectID != "" {
		where = string(c.ObjectID)
	}
	verb := dwellActivityVerb(c)
	var subject string
	if c.Source == sim.DwellSourceItem && c.Kind != "" {
		subject = fmt.Sprintf("%s %s", verb, sanitizeInline(string(c.Kind)))
	} else {
		subject = verb
	}
	if where != "" {
		subject = fmt.Sprintf("%s at %s", subject, sanitizeInline(where))
	}
	if c.RemainingTicks != nil && c.PeriodMinutes > 0 {
		// ZBBS-WORK-409: "~N minute(s) remaining" never said remaining OF WHAT —
		// it read as a countdown until the actor was free to go, so NPCs walked
		// off mid-meal and forfeited the slow-burn payoff (the credit deletes on
		// walk-away). Spell out, in prose, how long it takes to FINISH and what
		// leaving costs (sim.DwellStayClause — shared with the settle feedback so
		// the buyer hears one consistent message), so this load-bearing parking
		// line does its job instead of inviting an exit. No coins clause here: an
		// item dwell can also be self-consumed pack food, not a purchase.
		minutes := (*c.RemainingTicks) * c.PeriodMinutes
		subject = fmt.Sprintf("%s, %s", subject, sim.DwellStayClause(minutes, c.Attribute, ""))
	} else if c.Source == sim.DwellSourceObject {
		// ZBBS-WORK-411: object dwells (shade tree, well, berry bush) are free,
		// open-ended recovery sources with no countdown, so they skip the item
		// branch above and would otherwise render bare ("You are resting at the
		// old oak") — no stake, leaving NPCs free to wander off mid-recovery while
		// the duty-steer / "## How you can rest" alternatives pull them away. The
		// open-ended sibling clause says staying keeps easing the need and that
		// leaving stops it.
		subject = fmt.Sprintf("%s, %s", subject, sim.ObjectDwellStayClause(c.Attribute))
	}
	return subject
}

// dwellActivityVerb picks the verb for a dwell-in-progress line based
// on source + attribute. Item-source meals lead with "eating" /
// "drinking" / "resting" by attribute; object-source lines lead with
// the activity matching the pin (resting under a tree, sipping at a
// well). Defaults to "lingering" when nothing fits.
func dwellActivityVerb(c DwellCreditView) string {
	if c.Source == sim.DwellSourceItem {
		switch c.Attribute {
		case "hunger":
			return "eating"
		case "thirst":
			return "drinking"
		case "tiredness":
			return "resting with"
		}
		return "having"
	}
	switch c.Attribute {
	case "hunger":
		return "foraging"
	case "thirst":
		return "drinking"
	case "tiredness":
		return "resting"
	}
	return "lingering"
}

// timeOfDayProse maps a village wall-clock minute-of-day (0–1439) to a felt
// ambient sentence — the deterministic, in-world time-of-day analogue of the
// LLM-authored atmosphere line. The engine itself tracks only a binary
// day/night Phase (dawn/dusk flips); these finer narration bands are a
// presentation detail derived from the clock minute, so the NPC gets a sense of
// the hour without the engine modelling more than it needs. ZBBS-HOME-351.
func timeOfDayProse(minute int) string {
	if minute < 0 || minute > 1439 {
		return "" // out of range — fail closed rather than render a misleading band
	}
	switch {
	case minute < 300: // before 05:00
		return "It is the dead of night."
	case minute < 420: // 05:00–07:00
		return "Dawn is breaking over the village."
	case minute < 720: // 07:00–12:00
		return "It is morning in the village."
	case minute < 840: // 12:00–14:00
		return "It is midday."
	case minute < 1080: // 14:00–18:00
		return "The afternoon wears on."
	case minute < 1260: // 18:00–21:00
		return "Evening settles over the village."
	default: // 21:00–24:00
		return "Night lies over the village."
	}
}

// deadEndClause renders the at-location dead-end sentence for the place the
// actor is physically at (LLM-154), or "" when the place can serve them. The
// place is named from the same SurroundingsView fields the location line uses
// (StructureName inside, NearbyStructureName outdoors), so the clause and the
// "You are ..." line can never name different places. Sentence-start, so the
// mid-clause article from WithDefiniteArticle is capitalized ("the Tavern" →
// "The Tavern").
func deadEndClause(s SurroundingsView) string {
	switch s.LocationDeadEnd {
	case DeadEndShutBusiness:
		name := s.StructureName
		if name == "" {
			name = s.NearbyStructureName
		}
		if name == "" {
			return ""
		}
		place := capitalizeFirst(sim.WithDefiniteArticle(sanitizeInline(name)))
		return place + " is shut — no one is tending it."
	case DeadEndNoConsumableHere:
		// LLM-176: name the missing affordance at a foodless spot so a weak model
		// can't confabulate food here ("bread in the kitchen"). "Here" is unambiguous
		// next to the location line; phrasing follows which felt need has no source.
		switch {
		case s.DeadEndHunger && s.DeadEndThirst:
			return "There's nothing to eat or drink here — you'll need to forage or buy it elsewhere."
		case s.DeadEndThirst:
			return "There's nothing to drink here — you'll need to find a well or buy a drink elsewhere."
		default:
			return "There's no food to be had here — you'll need to forage or buy a meal elsewhere."
		}
	default:
		return ""
	}
}

// capitalizeFirst upper-cases the leading letter of a mid-clause label so it can
// open a sentence — WithDefiniteArticle yields "the Tavern", and a sentence-
// start caller (the LLM-154 shut-business clause) needs "The Tavern". Rune-aware
// so a non-ASCII leading character in a display name is upper-cased, not mangled
// byte-wise; empty in, empty out.
func capitalizeFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func renderSurroundings(b *strings.Builder, s SurroundingsView) {
	b.WriteString("## Around you\n")

	// Location + company in one felt sentence. The struct-field form ("inside:
	// outdoors" / "huddle: not in a huddle") was a raw dump and engine jargon —
	// "huddle" is a word the LLM was never taught. ZBBS-HOME-339.
	var location string
	switch {
	case s.InsideStructureID != "":
		name := s.StructureName
		if name == "" {
			name = "a building"
		}
		location = "inside " + sim.WithDefiniteArticle(sanitizeInline(name))
	case s.NearbyStructureName != "":
		// Standing at a structure's loiter slot while outdoors — a keeper at
		// their own stall, a customer outside a shop. Names where they are so
		// the model doesn't read raw coordinates and re-walk to a place it is
		// already standing at.
		location = "outdoors by " + sim.WithDefiniteArticle(sanitizeInline(s.NearbyStructureName))
	default:
		location = "outdoors"
	}
	// Co-present sleepers and resters are visible but not addressable by THIS
	// actor — sleep is never interrupted by speech, and an NPC's speech can't
	// rouse a rester either (reactor.go actorCanReactNow; only a PC / red-tier
	// need / operator nudge wakes a rester). Render them in a distinct
	// not-addressable clause so the actor doesn't talk to someone who won't
	// answer and read the silence as rudeness (ZBBS-WORK-426).
	dormant := dormantClause(s.CoPresentAsleep, s.CoPresentResting)
	switch {
	case len(s.HuddleMembers) > 0:
		// A huddle is a conversational cluster, so "with" names who the actor
		// is gathered with — the speak tool reaches exactly these people.
		// (CoPresentAsleep/Resting are only populated for an unhuddled actor, so
		// there is no dormant clause to append in this case.)
		fmt.Fprintf(b, "You are %s, with %s.\n", location, joinHuddleMembers(s.HuddleMembers))
	case len(s.CoPresent) > 0:
		// Not conversing, but others are within earshot. Name them (every turn) so
		// the actor can address someone and start conversing, instead of
		// discovering "no one here to hear you" by tripping the speak gate. This is
		// the SAME set the speak path would reach (ZBBS-WORK-407).
		names := make([]string, len(s.CoPresent))
		for i, m := range s.CoPresent {
			label := descriptorLabel(m.DisplayName, m.Role, m.Acquainted)
			if m.JustArrived {
				// ZBBS-WORK-422: flag a newcomer so a stateless NPC reads the
				// "someone just walked up — greet them" beat. Without it a fresh
				// arrival is indistinguishable from someone who has stood here a
				// while, since "## Around you" only lists standing presence.
				label += " (just arrived)"
			}
			label += laborTiePhrase(m.SolicitTie)
			names[i] = label
		}
		verb := "is"
		if len(names) > 1 {
			verb = "are"
		}
		fmt.Fprintf(b, "You are %s. %s %s here with you — speak to start conversing with them.%s\n",
			location, joinNames(names), verb, dormant)
	case len(s.CoPresentAsleep) > 0 || len(s.CoPresentResting) > 0:
		// No one awake within earshot, but someone is here asleep or resting. Name
		// them so the actor knows the room isn't empty, while making clear there's
		// no one it can talk to right now (ZBBS-WORK-426).
		fmt.Fprintf(b, "You are %s.%s There is no one awake here to hear you speak.\n",
			location, dormant)
	default:
		// No one within earshot. State it plainly, every turn, so the actor turns
		// to a solo task or moves to find company rather than speaking to an empty
		// room. Echoes the speak gate's wording ("no one here to hear you").
		fmt.Fprintf(b, "You are %s, with no one else here to hear you speak.\n", location)
	}

	// LLM-154: a live dead-end at the actor's current location, stated plainly on
	// its own line so a weak model isn't left to infer "closed" from "the keeper
	// is asleep". Branch-independent (fires whether the actor is huddled, has
	// company, or is alone) and named from the same fields the location line uses,
	// so the two can't name different places.
	if clause := deadEndClause(s); clause != "" {
		b.WriteString(clause)
		b.WriteString("\n")
	}

	// Time of day as ambient prose (ZBBS-HOME-351). v2 rendered no clock at all,
	// so an NPC couldn't tell its working hours from the dead of night — the
	// missing context HOME-352 (return-to-post) builds on. nil only for a
	// hand-built snapshot with no clock established; in a running engine the
	// publish path always sets it, so the line is always present there.
	if s.LocalMinuteOfDay != nil {
		if prose := timeOfDayProse(*s.LocalMinuteOfDay); prose != "" {
			b.WriteString(prose)
			b.WriteString("\n")
		}
	}

	// ZBBS-WORK-374: the LLM-authored literary atmosphere line (ZBBS-WORK-327,
	// "The night abideth over the village in a sober hush…") is NOT rendered into
	// the decision prompt — ~45 words of restart-lossy scene prose irrelevant to
	// the action at hand, part of the low-signal bulk that buried the actual
	// stimulus. The deterministic time-of-day line above is kept (it's the clock
	// context HOME-352 relies on). SurroundingsView.Atmosphere stays populated for
	// any other consumer; we just don't spend prompt budget on it here.

	// Harvest affordance (ZBBS-WORK-328): the model often stands at a well/bush
	// without connecting "I'm here" to "I can gather." This line makes the
	// affordance explicit. Same SurroundingsView fields drive gateTools'
	// gather advertising, so the cue and the offered tool can't drift.
	if s.GatherableItem != "" {
		source := strings.TrimSpace(sanitizeInline(s.GatherableSource))
		if source == "" {
			source = "this spot"
		}
		// LLM-113: render the plural counting phrase ("raspberries"), not the raw
		// catalog key. GatherableNoun is empty when a caller builds the view
		// directly (some tests) — fall back to the key so the cue still renders.
		noun := s.GatherableNoun
		if noun == "" {
			noun = string(s.GatherableItem)
		}
		fmt.Fprintf(b, "You're at %s — you can gather %s here.\n",
			source, sanitizeInline(noun))
	}
	b.WriteString("\n")
}

// renderAnchors writes the actor's standing home/work move targets as a prose
// line carrying each structure_id. Always emitted when the view is non-nil, so
// a wandering NPC always has its own home and work as reachable destinations —
// not only when a need-cue happens to point somewhere (the gap that let John
// Ellis cycle to a closed farm with no id for his own tavern to head back to).
// The "(structure_id: …)" form matches the satiation / restock / shift-duty
// cues — it's the load-bearing token the model echoes into move_to.
// ZBBS-HOME-349.
func renderAnchors(b *strings.Builder, v *AnchorsView, atPost bool) {
	if v == nil {
		return
	}
	work := anchorPlace(v.WorkLabel, "your workplace")
	home := anchorPlace(v.HomeLabel, "your home")
	switch {
	case v.SamePlace:
		fmt.Fprintf(b, "Your home and your trade are both at %s (structure_id: %s) — you can head back there whenever you wish.\n\n", work, v.WorkID)
	case v.WorkID != "" && v.HomeID != "":
		// On-shift AT its own post, the open "head to either whenever you wish"
		// invitation actively pulls an idle owner home (the Prudence shop↔house
		// oscillation, ZBBS-WORK-431). Keep both structure_ids — they are the
		// load-bearing move_to tokens (HOME-349) — but frame home as after-hours
		// rather than an open door; the at-post duty steer carries "stay put".
		if atPost {
			fmt.Fprintf(b, "You keep your trade at %s (structure_id: %s); your home is at %s (structure_id: %s) — head home once your work is done.\n\n", work, v.WorkID, home, v.HomeID)
		} else {
			fmt.Fprintf(b, "You keep your trade at %s (structure_id: %s), and your home is at %s (structure_id: %s) — you can head to either whenever you wish.\n\n", work, v.WorkID, home, v.HomeID)
		}
	case v.WorkID != "":
		fmt.Fprintf(b, "You keep your trade at %s (structure_id: %s) — you can head back there whenever you wish.\n\n", work, v.WorkID)
	case v.HomeID != "":
		fmt.Fprintf(b, "Your home is at %s (structure_id: %s) — you can head back there whenever you wish.\n\n", home, v.HomeID)
	}
}

// anchorPlace returns the sanitized structure label, or a generic fallback
// phrase when the structure has no DisplayName (the id still rides in the
// caller's line, so the target stays actionable even unlabeled).
func anchorPlace(label, fallback string) string {
	if label == "" {
		return fallback
	}
	return sim.WithDefiniteArticle(sanitizeInline(label))
}

// renderDutySteer writes the standing return-to-post cue (ZBBS-HOME-352) — the
// single voice for shift duty (the engine's ShiftDutyWarrant line is filtered
// out in Render). It carries the destination's structure_id inline — the
// load-bearing token the model echoes into move_to, matching the anchors /
// satiation / restock cues — so the cue is self-sufficient and does not depend
// on another section rendering the id (code_review).
func renderDutySteer(b *strings.Builder, v *DutySteerView) {
	if v == nil {
		return
	}
	// At-post stabilizer (ZBBS-WORK-431): on-shift, standing at your own post.
	// The symmetric complement to the to-work line — without it an idle owner
	// with no custom wanders off and the away-from-post arm drags it back
	// (Prudence shop↔house). The anchors line is reframed in tandem (renderAnchors
	// atPost) so the two cues agree: you belong here right now.
	if v.AtPost {
		// State the close time (LLM-40) so "stay open later" is a bounded
		// decision — the model otherwise read the diligence cues as license to
		// extend with no customer present and no sense of how near close was.
		closeAt := ""
		if v.ShiftEndMin != nil {
			closeAt = sim.ClockHourProse(*v.ShiftEndMin)
		}
		if v.ForageErrand {
			// LLM-90: a bare sell-shelf plus ripe own bushes, and NOT mid-customer
			// (buildForage defers the harvest cue while a customer is engaged at the
			// stall). The default stabilizer's "wait here rather than wandering off"
			// line directly contradicts the "## Your bushes to harvest" cue's
			// "walk out to your bushes" — so swap it for a step-out-and-return line
			// the two cues agree on. Stepping out to one's OWN bushes to restock an
			// empty shelf is tending the trade, not wandering off; the post stays the
			// home base she returns to. The to-work arm defers a forage errand
			// (buildDutySteer), so she isn't yanked back once she sets off.
			if closeAt != "" {
				fmt.Fprintf(b, "It is your working hours and you are at your post (you close at %s), but your shelves are bare — step out to your own bushes to restock, then return to your post.\n\n", closeAt)
			} else {
				b.WriteString("It is your working hours and you are at your post, but your shelves are bare — step out to your own bushes to restock, then return to your post.\n\n")
			}
			return
		}
		if closeAt != "" {
			fmt.Fprintf(b, "It is your working hours and you are at your post — stay and look after your work; you close at %s. If no one needs you right now, wait here for customers rather than wandering off.\n\n", closeAt)
		} else {
			b.WriteString("It is your working hours and you are at your post — stay and look after your work. If no one needs you right now, wait here for customers rather than wandering off.\n\n")
		}
		return
	}
	if v.ToWork {
		fmt.Fprintf(b, "It is your working hours, yet you are away from your post — make your way to %s (structure_id: %s) now.\n\n",
			anchorPlace(v.TargetLabel, "your workplace"), v.TargetID)
		return
	}
	// Off-shift wind-down (ZBBS-WORK-387). Housing-dependent target line, plus —
	// for a keeper standing at its post — the stay_open choice appended after it.
	switch {
	case v.TargetID == "":
		// Homeless: no fixed place. The WHERE (rent a room / a shade tree) is
		// carried by the recovery-options cue; this is only the schedule beat.
		b.WriteString("Your working hours are over — it is time to close up for the night and find yourself a place to rest.")
	case v.Lodging:
		if l := sanitizeInline(v.TargetLabel); l != "" {
			fmt.Fprintf(b, "Your working hours are over — close up and head to your rented room at %s (structure_id: %s) to rest for the night.", l, v.TargetID)
		} else {
			fmt.Fprintf(b, "Your working hours are over — close up and head to your rented room at the inn (structure_id: %s) to rest for the night.", v.TargetID)
		}
	default:
		if l := sanitizeInline(v.TargetLabel); l != "" {
			fmt.Fprintf(b, "Your working hours are over and you are not yet home — head home to %s (structure_id: %s) now.", l, v.TargetID)
		} else {
			fmt.Fprintf(b, "Your working hours are over and you are not yet home — head home (structure_id: %s) now.", v.TargetID)
		}
	}
	// The stay-open choice: encouraged when a concrete reason is present, else
	// offered as a discretionary option. Always names that the closing hour must
	// be supplied (until_hour) — the stay_open tool requires it.
	if v.OfferStayOpen {
		if v.StayOpenReason != "" {
			fmt.Fprintf(b, " However, %s — if you wish to keep your business open later instead, call stay_open and state the hour you will close (until_hour).", v.StayOpenReason)
		} else {
			b.WriteString(" Or, if you have reason to keep your business open later instead, you may call stay_open and state the hour you will close (until_hour).")
		}
	}
	b.WriteString("\n\n")
}

// renderEveningLeisure writes the evening "tavern's open" cue (LLM-149) — a
// non-coercive invitation: the day's work is done, the tavern is open of an
// evening, and the agent may head over, pass a quiet evening at home, or turn in
// as it likes. It carries the tavern's structure_id (the new move_to token) and
// the home structure_id (the co-equal stay-home choice) inline, so the cue is
// self-sufficient like the duty steer. No imperative and no "turn in" pressure —
// the three options are equal-weight; bedtime is Lever 1's 22:00 gate. Renders
// in ## Around you, in the slot the off-shift go-home steer occupies the rest of
// the day (suppressed in-window so this is the single voice).
func renderEveningLeisure(b *strings.Builder, v *EveningLeisureView) {
	if v == nil {
		return
	}
	venue := anchorPlace(v.VenueLabel, "the tavern")
	home := anchorPlace(v.HomeLabel, "your home")
	fmt.Fprintf(b, "Your day's work is done, and the tavern is open of an evening — you might make your way to %s (structure_id: %s) for company, pass a quiet evening at %s (structure_id: %s), or turn in for the night, as you please.\n\n",
		venue, v.VenueID, home, v.HomeID)
}

// joinHuddleMembers renders co-huddle peers with name-vs-descriptor
// gating per Acquaintance. Acquainted → DisplayName; unacquainted with
// a Role → "the <role>"; otherwise → "a stranger". Mirrors v1's
// coLocatedHuddleMembers descriptor swap so unknown others don't get
// greeted by name.
func joinHuddleMembers(members []HuddleMember) string {
	parts := make([]string, len(members))
	for i, m := range members {
		parts[i] = renderHuddleMember(m)
	}
	return strings.Join(parts, ", ")
}

func renderHuddleMember(m HuddleMember) string {
	return sanitizeInline(descriptorLabel(m.DisplayName, m.Role, m.Acquainted)) + laborTiePhrase(m.SolicitTie)
}

// laborTiePhrase names a co-present member's relationship to the subject —
// housemate or workmate (LLM-157) — so a worker reads them as kin/crew rather than a
// paid-work prospect, without the engine spelling out the instruction. Empty for
// laborTieNone, so it composes onto any member label without adding a separator of
// its own.
func laborTiePhrase(t laborTie) string {
	switch t {
	case laborTieHousehold:
		return " (your housemate)"
	case laborTieWorkplace:
		return " (your workmate)"
	default:
		return ""
	}
}

// renderNarrativeState writes the "Who you are:" section for shared-VA
// actors. Content-gated: a nil view skips the section entirely so
// stateful and PC actors don't see an empty block. The contract
// matches the perception note — Render is kind-agnostic; Build is the
// one that gates on Kind.
//
// ZBBS-WORK-374: EvolvingSummary is NOT rendered into the live decision prompt.
// The per-actor narrative consolidation that would rewrite it is not ported to
// v2 (see the perception note "Not in this package yet"), so it is frozen seed
// data — and that frozen prose is the first-person rumination ("the same
// greetings, the same offers of rooms…") that primed the repeat-pitch loop. The
// field stays on the view + snapshot for when consolidation lands; we just keep
// model-generated diary prose out of the prompt the model decides from.
func renderNarrativeState(b *strings.Builder, n *NarrativeStateView) {
	if n == nil {
		return
	}
	b.WriteString("## Who you are\n")
	if n.SeedText != "" {
		b.WriteString(sanitizeInline(n.SeedText))
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// renderVendorOperating writes the businessowner trade-conduct block — the
// operating rules that used to live in salem-vendor's startup_instructions (the
// memory-api <Instructions> system block) and drove the "instant room pitch on a
// bare Hello" sell-pressure. Moved engine-side (ZBBS-WORK-374) so the whole
// decision prompt is code-owned and the rules sit near the decision point rather
// than in a detached, far-away system preamble. Gated on AtOwnBusinessOperating
// — a businessowner physically at their own post (ZBBS-WORK-385) AND within
// operating hours (on shift, or staying open past close; LLM-123) — so it reaches
// vendors (innkeeper, farmers, shopkeepers) tending their business during the day,
// but not visitors, stateful NPCs, a keeper off-post in someone else's place, or a
// keeper standing at their own CLOSED post after hours (the off-shift forge<->Tavern
// oscillation). The scoped wording replaces "always be closing" with "a greeting is
// not a sale". ZBBS-HOME-385 restores the "tend to your trade" working framing that
// the WORK-374 port dropped (the producers were drifting off-post with nothing to
// do); kept generic ("your trade", not "your stall") since a vendor may keep a
// stall or a building.
func renderVendorOperating(b *strings.Builder, atOwnBusinessOperating bool) {
	if !atOwnBusinessOperating {
		return
	}
	b.WriteString("How you trade:\n")
	b.WriteString("- Tend to your trade — your living depends on it. Look after your goods and your custom; what goes unsold earns nothing, so see to the day's business rather than let it pass idle.\n")
	b.WriteString("- If someone only greets you, greet them and let them state their business — don't quote prices or pitch your goods or rooms unless they ask or show interest.\n")
	b.WriteString("- When trade is slow, make a reasonable deal rather than hold the line on price; decline plainly if a stranger's purse is short.\n")
	b.WriteString("Plain 1692 New England speech; no modern idioms.\n\n")
}

// renderOfferableCustomers writes the seller-side "offer your wares" cue
// (ZBBS-HOME-404): the businessowner's co-present customers, the goods they
// carry, and the scene_quote mechanism with its args spelled out — so the
// keeper LLM can proactively offer a sale instead of only reacting to a buyer's
// pay_with_item. It names the tool + arg form (the Finding-1 idiom: an
// actionable cue, not the bare "what goes unsold earns nothing" exhortation),
// but the decision stays with the model — it judges interest (the vendor
// block's "don't pitch unless they show interest" rule still governs) and sets
// the price, and the buyer keeps full accept/decline agency via pay_with_item.
// ZBBS-HOME-467 sharpens the in-cue trigger: scene_quote is for a ware the
// buyer has actually named (or asked the price of); a generic opener ("I'm
// hungry" / "what do you have") should get the menu, not a guessed-item quote.
// The constraint sits next to the tool here because the distant vendor-block
// "don't pitch unless they show interest" rule wasn't biting on a 70B keeper.
// Content-gated: a nil/empty view skips the section. Build guarantees both
// slices are non-empty when the view is non-nil.
func renderOfferableCustomers(b *strings.Builder, v *OfferableCustomersView) {
	if v == nil || len(v.CustomerNames) == 0 || len(v.Goods) == 0 {
		return
	}
	b.WriteString("## Custom at hand\n")
	who := joinNames(v.CustomerNames) // sanitizes each name inline
	verb := "is"
	if len(v.CustomerNames) > 1 {
		verb = "are"
	}
	goods := make([]string, 0, len(v.Goods))
	for _, g := range v.Goods {
		if s := sanitizeInline(g.Label); s != "" {
			// The on-hand count is the sizing fact (ZBBS-HOME-459): the cue asks
			// the seller to name a quantity, so it must see what it actually holds.
			// An inedible ingredient also carries its use (LLM-166), folded into
			// the same parens as the carry readout.
			if g.Use != "" {
				goods = append(goods, fmt.Sprintf("%s (%d on hand, %s)", s, g.OnHand, sanitizeInline(g.Use)))
			} else {
				goods = append(goods, fmt.Sprintf("%s (%d on hand)", s, g.OnHand))
			}
		}
	}
	if len(goods) == 0 {
		// Defensive: Build filters raw empty labels, but a label could sanitize
		// down to empty — render nothing rather than an empty goods list.
		return
	}
	fmt.Fprintf(b, "%s %s here with you. If one of them names a specific good they want, or asks the price of a specific good, say your price for it plainly in your reply — name the coins outright, do not ask whether they would like to hear it — and call sell with that named item, the quantity, and your price in coins to post the offer (the buyer can take it on their own pay screen). If they speak only in general — that they are hungry, ask what you have, or ask the cost of a meal without naming a dish — tell them what is for sale and let them choose; do not sell unless the buyer has named the good. Use target_buyer only for a named person you know; for a stranger or someone known only by trade, omit target_buyer to offer the whole room. The buyer is then free to take it or leave it.\n", who, verb)
	// ZBBS-HOME-407: the barter counterpart to the coin-sale cue above. When a
	// customer is carrying goods the keeper would rather have than coin, point
	// at offer_trade so a goods-for-goods swap has a legible execution path
	// instead of dissolving into a verbal agreement nothing commits.
	fmt.Fprintf(b, "If one of them is carrying something you would rather have than coin, you can instead propose a direct trade — call offer_trade with the goods you will give and what you want from them. They are then free to accept, decline, or counter.\n")
	fmt.Fprintf(b, "Your goods to sell: %s.\n", strings.Join(goods, ", "))
	// LLM-171: a co-present customer who MAKES one of these goods is the wrong
	// person to pitch it to — your stock of it came from a maker like them. Name
	// the overlap so the keeper doesn't sell a smith his own skillet back (which a
	// 70B keeper otherwise does, reading the maker's own sell-offer as a buy-ask).
	for _, note := range v.ProducerNotes {
		fmt.Fprintf(b, "%s makes %s themselves — don't pitch those back to their own maker; offer them to other customers instead.\n", sanitizeInline(note.CustomerName), joinNames(note.Goods))
	}
	b.WriteString("\n")
}

// renderRelationships writes the "## What you remember of those here" section —
// the consolidated per-peer SUMMARY only. ZBBS-HOME-412 moved the turn-by-turn
// to "## Recent conversation here" (renderRecentConversation, sourced from the
// huddle ring for ALL NPCs), so the per-peer RecentFacts list is no longer
// rendered here — in particular the [heard] re-surface that drove the cross-tick
// re-pitch (a remembered ask read as a live one). A peer with no consolidated
// summary contributes nothing now, so the section is skipped entirely when no
// peer has one. (Still shared-VA-only: Build leaves Relationships nil for
// stateful/PC kinds.)
func renderRelationships(b *strings.Builder, peers []RelationshipPeerView) {
	if len(peers) == 0 {
		return
	}
	wrote := false
	for _, p := range peers {
		if strings.TrimSpace(p.SummaryText) == "" {
			continue
		}
		if !wrote {
			b.WriteString("## What you remember of those here\n")
			wrote = true
		}
		name := sanitizeInline(p.PeerName)
		if name == "" {
			name = string(p.PeerID)
		}
		fmt.Fprintf(b, "- %s: %s\n", name, sanitizeInline(p.SummaryText))
	}
	if wrote {
		b.WriteString("\n")
	}
}

// renderRecentConversation writes the "## Recent conversation here" section
// (ZBBS-HOME-412) — the huddle's last few spoken turns, oldest-first, marking
// the subject's own lines "You said" and everyone else "<Name> said". This is
// the cross-tick conversational continuity EVERY NPC (stateful included) and the
// player's own lines feed into, so a re-engaging actor sees that it already
// spoke and what was just asked, instead of re-pitching. Empty list skips the
// section.
func renderRecentConversation(b *strings.Builder, lines []UtteranceView) {
	if len(lines) == 0 {
		return
	}
	b.WriteString("## Recent conversation here\n")
	for _, u := range lines {
		text, _ := sanitizeText(u.Text, 0)
		if u.IsSelf {
			fmt.Fprintf(b, "- You said: %s\n", text)
			continue
		}
		name := sanitizeInline(u.SpeakerName)
		if name == "" {
			name = "someone"
		}
		fmt.Fprintf(b, "- %s said: %s\n", name, text)
	}
	b.WriteString("\n")
}

// fallbackToday derives the order-book "today" for a hand-built payload that
// supplied no village calendar date (LocalDateUTC zero): the UTC day of the
// render instant (now) when present, else the host UTC day. A real snapshot
// always supplies LocalDateUTC, so this is only reached by hand-built test
// payloads — deriving from `now` keeps such a fixture deterministic when it sets
// a clock, and only a fully clockless payload touches the wall clock. LLM-106.
func fallbackToday(now time.Time) time.Time {
	if now.IsZero() {
		return startOfUTCDay(time.Now())
	}
	return startOfUTCDay(now)
}

// renderPendingDeliveriesFromMe writes the seller-side order book, split by the
// order's ReadyBy date (ZBBS-HOME-403): orders due today (or earlier) render as
// "## Orders to deliver" — the actionable hand-over section — and orders booked
// for a future day render as "## Upcoming bookings" — a passive reservation
// list with no deliver_order nudge. Empty list skips both.
//
// Phase 3 PR S6 — surfacing pending deliveries to the seller's LLM
// is the load-bearing perception mechanism (no warrant kind for
// Order state; the seller relies on baseline perception to remember
// to call deliver_order).
func renderPendingDeliveriesFromMe(b *strings.Builder, orders []OrderView, today, now time.Time) {
	if len(orders) == 0 {
		return
	}
	if today.IsZero() {
		today = fallbackToday(now)
	}
	var ready, future []OrderView
	for _, o := range orders {
		// A future booking renders as a reservation; everything else (due
		// today, overdue, or with no booked date) is ready to hand over now.
		if !o.ReadyBy.IsZero() && startOfUTCDay(o.ReadyBy).After(today) {
			future = append(future, o)
		} else {
			ready = append(ready, o)
		}
	}
	renderOrdersReadyToHandOver(b, ready, now)
	renderFutureReservations(b, future)
}

// renderOrdersReadyToHandOver writes the actionable "## Orders to deliver"
// section — one line per order due now, with the deliver_order nudge.
//
// ZBBS-WORK-372 — the section closes with an explicit actionable
// instruction naming the deliver_order tool + order_id arg, mirroring
// the pay-offer section. Before this, a bare list of order ids read as
// data, not an action: keepers spoke a delivery promise and never fired
// the tool, so orders sat open forever (boot-collapse Finding 1).
//
// ZBBS-WORK-373 — co-presence gate. DeliverOrder's gate 6 rejects a handover to
// any recipient not sharing the seller's huddle, so an order whose recipient has
// stepped away renders passively ("waiting for X to return"), and the actionable
// instruction is suppressed unless at least one order is deliverable now — the
// keeper isn't cued to chase an absent buyer (boot-collapse Finding 6 bundle).
func renderOrdersReadyToHandOver(b *strings.Builder, orders []OrderView, now time.Time) {
	if len(orders) == 0 {
		return
	}
	b.WriteString("## Orders to deliver\n")
	anyDeliverable := false
	for _, o := range orders {
		itemDesc := string(o.Item)
		if o.Qty > 1 {
			itemDesc = fmt.Sprintf("%d %s", o.Qty, o.Item)
		}
		buyer := sanitizeInline(o.BuyerName)
		fmt.Fprintf(b, "- #%d: %s for %s", uint64(o.ID), itemDesc, buyer)
		if len(o.ConsumerNames) > 0 {
			fmt.Fprintf(b, " (to deliver to: %s)", sanitizeInline(strings.Join(o.ConsumerNames, ", ")))
		}
		// Co-presence gate (ZBBS-WORK-373): an order whose recipient isn't in
		// the seller's huddle can't be delivered now — DeliverOrder gate 6
		// would reject it — so render it passively rather than as an action,
		// and never name the absent buyer as a chase target.
		if len(o.AbsentRecipientNames) > 0 {
			fmt.Fprintf(b, " — waiting for %s to return", sanitizeInline(strings.Join(o.AbsentRecipientNames, ", ")))
		} else {
			anyDeliverable = true
		}
		if clause, ok := expiryClause(o.ExpiresAt, now); ok {
			b.WriteString(clause)
		}
		b.WriteString("\n")
	}
	// Only surface the actionable instruction when at least one order is
	// deliverable now. Telling the keeper to "call deliver_order — the recipient
	// must be here" while nobody is present is the exact absent-recipient chase
	// the gate guards against.
	if anyDeliverable {
		// ZBBS-WORK-373 handover-line nudge: deliver_order is silent (it moves the
		// goods + writes the interaction fact, no speech) and non-terminal, so the
		// keeper can chain deliver_order(N) -> speak in the same tick. Ask for the
		// line so a delivery reads as "here's your bread, Ezekiel" rather than items
		// silently appearing — the "actions are socially expressed" convention.
		b.WriteString("To hand one of these over, call deliver_order with the order's number as order_id (the recipient must be here with you). The handover itself is silent, so say a word to them as you pass it across.\n")
	}
	b.WriteString("\n")
}

// renderFutureReservations writes the seller's upcoming bookings — orders whose
// ReadyBy hasn't arrived yet. Framed as reservations, not deliveries, with an
// explicit "don't hand over yet" so the keeper doesn't waste a deliver_order on
// a booking that isn't due (DeliverOrder's gate 4b rejects a premature check-in,
// so the call would fail anyway) or forget the booking exists. The live case is
// an advance lodging booking. ZBBS-HOME-403.
func renderFutureReservations(b *strings.Builder, orders []OrderView) {
	if len(orders) == 0 {
		return
	}
	b.WriteString("## Upcoming bookings\n")
	for _, o := range orders {
		itemDesc := string(o.Item)
		if o.Qty > 1 {
			itemDesc = fmt.Sprintf("%d %s", o.Qty, o.Item)
		}
		buyer := sanitizeInline(o.BuyerName)
		fmt.Fprintf(b, "- #%d: %s for %s — booked for %s\n",
			uint64(o.ID), itemDesc, buyer, o.ReadyBy.Format("Mon Jan 2"))
	}
	b.WriteString("These aren't due yet — don't hand them over until the booked day arrives.\n\n")
}

// renderPendingDeliveriesToMe writes the buyer/consumer-side view, split by the
// order's ReadyBy date (ZBBS-HOME-403): orders still within their window render
// as "## Orders you're waiting on", and orders whose ReadyBy has passed without
// delivery render as "## Overdue — paid but not delivered" — the buyer-side
// robbery cue. Empty list skips both.
//
// Phase 3 PR S6 — gives the buyer's LLM a structured "I'm waiting
// for X from Y" cue so they can speak follow-ups ("Hannah, where's
// my stew?") or make wait/depart decisions.
func renderPendingDeliveriesToMe(b *strings.Builder, orders []OrderView, today, now time.Time) {
	if len(orders) == 0 {
		return
	}
	if today.IsZero() {
		today = fallbackToday(now)
	}
	var waiting, overdue []OrderView
	for _, o := range orders {
		if !o.ReadyBy.IsZero() && startOfUTCDay(o.ReadyBy).Before(today) {
			overdue = append(overdue, o)
		} else {
			waiting = append(waiting, o)
		}
	}
	renderOrdersWaitingOn(b, waiting, now)
	renderOverdueOrders(b, overdue)
}

// renderOrdersWaitingOn writes the buyer's "## Orders you're waiting on"
// section — one line per order still within its delivery window.
func renderOrdersWaitingOn(b *strings.Builder, orders []OrderView, now time.Time) {
	if len(orders) == 0 {
		return
	}
	b.WriteString("## Orders you're waiting on\n")
	for _, o := range orders {
		itemDesc := string(o.Item)
		if o.Qty > 1 {
			itemDesc = fmt.Sprintf("%d %s", o.Qty, o.Item)
		}
		seller := sanitizeInline(o.SellerName)
		fmt.Fprintf(b, "- #%d: %s from %s", uint64(o.ID), itemDesc, seller)
		if clause, ok := expiryClause(o.ExpiresAt, now); ok {
			b.WriteString(clause)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// renderOverdueOrders writes the buyer's "## Overdue" section — orders the buyer
// paid for whose ReadyBy has passed but the seller still hasn't delivered (the
// orders carried here are all still Ready; buildPendingOrderViews drops terminal
// ones). The live case is a lodging booking the keeper never honored. The cue is
// informative, not prescriptive — the LLM decides whether to chase, complain, or
// let it go; the engine refunds the coins when the order finally expires
// (ZBBS-HOME-403).
func renderOverdueOrders(b *strings.Builder, orders []OrderView) {
	if len(orders) == 0 {
		return
	}
	b.WriteString("## Overdue — paid but not delivered\n")
	for _, o := range orders {
		itemDesc := string(o.Item)
		if o.Qty > 1 {
			itemDesc = fmt.Sprintf("%d %s", o.Qty, o.Item)
		}
		seller := sanitizeInline(o.SellerName)
		fmt.Fprintf(b, "- #%d: %s from %s — was due %s, still not delivered\n",
			uint64(o.ID), itemDesc, seller, o.ReadyBy.Format("Mon Jan 2"))
	}
	b.WriteString("\n")
}

// startOfUTCDay returns midnight UTC of the calendar date `t` falls on. Shared
// by the order-book date splits; ReadyBy is already midnight UTC of its date, so
// applying this to it is a defensive normalization (and gives a single notion of
// "today" to compare against). ZBBS-HOME-403.
func startOfUTCDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// maxRenderableExpiryHorizon bounds how far out an order deadline can be and
// still render a literal "expires in X" clause. An order's real TTL is minutes
// (OrderTTLDefault is 10m), so anything beyond a generous day is not a real
// deadline — it is the NULL-expires_at sentinel the PG loader substitutes for
// legacy v1 rows (9999-12-31, orders.go), or an overflow. Feeding that to
// humanizeDurationUntil produced "expires in 153722867 minutes" (~292 years —
// time.Time.Sub saturating at MaxInt64 ns) in a live NPC's prompt (ZBBS-HOME-357).
const maxRenderableExpiryHorizon = 24 * time.Hour

// expiryClause returns the " — expires in X" suffix for an order deadline, and
// ok=false (render nothing) when there is no meaningful expiry: a zero deadline
// (never set) OR an implausibly-far one (the legacy NULL sentinel / an overflow
// — see maxRenderableExpiryHorizon). Gating on the horizon here, at the render
// boundary, fixes the garbage duration regardless of which upstream sentinel or
// overflow produced the far-future time. ZBBS-HOME-357.
func expiryClause(deadline, now time.Time) (string, bool) {
	// No deadline, or no render clock (a hand-built payload that supplied no
	// PublishedAt → RenderedAt): nothing meaningful to render. The explicit
	// now-zero guard keeps "no clock omits expiry" obvious here rather than
	// leaning on the far-future-horizon check below to swallow deadline.Sub(zero).
	// LLM-106.
	if deadline.IsZero() || now.IsZero() {
		return "", false
	}
	if deadline.Sub(now) > maxRenderableExpiryHorizon {
		return "", false
	}
	return " — expires in " + humanizeDurationUntil(deadline, now), true
}

// humanizeDurationUntil renders a coarse "X minute(s)" string for a
// future time relative to now. Returns "now" when the deadline has
// passed (clamped to 0) — keeps the render readable even if a clock
// drift causes a brief past-due window before the sweep flips state.
func humanizeDurationUntil(deadline, now time.Time) string {
	d := deadline.Sub(now)
	if d <= 0 {
		return "now"
	}
	mins := int(d / time.Minute)
	if mins <= 0 {
		return "<1 minute"
	}
	if mins == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", mins)
}

// renderScene renders the loop-detection cue — "what's changed since you got
// here" — when a scene baseline is established. The raw "scene: <uuid> — origin
// <kind>" header and the "(missing_no_scene)" baseline enum it used to print
// were engine jargon the LLM can't use, so they're gone; the no-scene case now
// renders nothing at all rather than an empty diagnostic. ZBBS-HOME-339.
func renderScene(b *strings.Builder, p Payload) {
	// ZBBS-WORK-374: render the loop-detection cue only when a real baseline diff
	// exists. The missing-baseline branch used to print "You can't yet tell
	// whether anything has changed." — pure filler that carries no loop signal
	// (the actual stuck-loop signal is the BaselinePresent + AnyChange==false case
	// in renderDiff, unaffected here). Dropping it removes a noise section from
	// conversational and freshly-joined ticks without weakening loop detection.
	if p.Primary == nil || p.Baseline != BaselinePresent {
		return
	}
	b.WriteString("## Since you got here\n")
	b.WriteString(renderDiff(p.Primary.Diff))
	b.WriteString("\n\n")
}

// renderDiff renders the loop-detection line as felt prose. When nothing
// changed it says so explicitly — the "you may be looping" signal — but it
// never asserts "no change" unless the Diff is real (Build only attaches a
// Diff for BaselinePresent).
func renderDiff(d *Diff) string {
	if d == nil {
		return "You can't yet tell whether anything has changed."
	}
	if !d.AnyChange {
		return "Nothing about your situation has changed — if this keeps up, you may be repeating yourself."
	}
	var parts []string
	if d.StateChanged {
		parts = append(parts, "what you're doing")
	}
	if d.PositionChanged {
		parts = append(parts, "where you stand")
	}
	if d.StructureChanged {
		parts = append(parts, "where you are")
	}
	if d.HuddleChanged {
		parts = append(parts, "who you're with")
	}
	if d.CoinsChanged {
		parts = append(parts, "your coins")
	}
	if d.InventoryChanged {
		parts = append(parts, "what you're carrying")
	}
	if d.NeedsChanged {
		parts = append(parts, "how you feel")
	}
	return "What's changed: " + strings.Join(parts, ", ") + "."
}

// renderWarrants renders the "what just happened" section and fills in the
// RenderedPrompt accounting. Warrants arrive already ordered by
// SourceEventID (Build's job); the caps are applied here, after ordering,
// and any warrant past a cap is moved to DroppedWarrants for carry-forward.
// PendingPayOffers returns the offers currently pending against this actor
// as seller — the payload's standing ledger view (Build's buildPayOffersForMe
// scan over snap.PayLedger, ZBBS-HOME-453). It is the single source of truth
// shared by the perception offer-decision section (renderPayOffers, below)
// and the handlers tool-gate (gateTools): the rendered offer and the
// advertised accept_pay/decline_pay/counter_pay tools both key off this one
// predicate so they cannot drift.
//
// Until HOME-453 this read the consumed warrant batch instead — which gave
// the seller exactly ONE tick with the cue and the tools (the warrant is
// consumed by the tick it triggers), and a seller who spoke through it was
// locked out of resolving until the TTL sweep expired the offer.
//
// Contract: the "these offers are pending against p.ActorID as seller"
// invariant is established by Build (buildPayOffersForMe filters on
// SellerID == subject and State == Pending); this accessor trusts the
// field and does not re-verify it — the projection shape carries no
// SellerID to verify against. A payload assembled outside Build (tests)
// is responsible for honoring that invariant itself.
func PendingPayOffers(p Payload) []sim.PayOfferWarrantReason {
	return p.PayOffersForMe
}

// PendingLaborOffers returns the labor offers currently pending against this
// actor as EMPLOYER — the payload's standing ledger view (Build's
// buildLaborOffersForMe scan over snap.LaborLedger, LLM-26). The single source
// of truth shared by the perception decision section (renderLaborOffers) and
// the handlers tool-gate (gateTools): the rendered offer and the advertised
// accept_work/decline_work tools both key off this one predicate so they
// cannot drift (discussion-109). Same contract as PendingPayOffers — Build
// established the "pending against subject as employer" invariant; this
// accessor trusts the field.
func PendingLaborOffers(p Payload) []LaborOfferView {
	return p.LaborOffersForMe
}

// nonPayOfferWarrants returns the consumed batch with pay-offer warrants
// removed — they render in the dedicated decision section (renderPayOffers)
// instead of the generic "what just happened" list, so they must not also
// appear there, nor consume the warrant-section cap / carry-forward budget
// (a rendered offer is addressed).
func nonPayOfferWarrants(warrants []sim.WarrantMeta) []sim.WarrantMeta {
	out := make([]sim.WarrantMeta, 0, len(warrants))
	for _, w := range warrants {
		if _, ok := w.Reason.(sim.PayOfferWarrantReason); ok {
			continue
		}
		out = append(out, w)
	}
	return out
}

// nonShiftDutyWarrants returns the consumed batch with shift-duty warrants
// removed. The shift/duty producer's warrant still drives the wake tick, but its
// line is not rendered — the standing DutySteer cue (renderDutySteer) is the
// single voice for return-to-post (ZBBS-HOME-352). Dropping them here also keeps
// them out of the warrant-section cap / carry-forward budget; consuming them
// unrendered is correct since their purpose (waking the actor) is already done.
func nonShiftDutyWarrants(warrants []sim.WarrantMeta) []sim.WarrantMeta {
	out := make([]sim.WarrantMeta, 0, len(warrants))
	for _, w := range warrants {
		if _, ok := w.Reason.(sim.ShiftDutyWarrantReason); ok {
			continue
		}
		out = append(out, w)
	}
	return out
}

// renderPayOffers renders the pending-pay-offer decision section: one line
// per offer carrying the ledger_id (the load-bearing field — the model must
// echo it back into accept_pay/decline_pay/counter_pay), the buyer, the goods
// (qty x item), the amount, and whether the buyer wants it consumed now or
// kept. There is no untrusted free-text payload, so nothing is truncated;
// buyer and item are structurally sanitized like other inline fields.
//
// Uncapped by design: pay offers are inherently few (bounded by co-present
// buyers), and the section must always carry the ledger_id whenever gateTools
// advertises the response tools.
// formatOfferPayment renders a barter offer's payment terms for a
// perception line: coins, goods, or both ("5 coins", "5 nails", "5 nails
// and 3 coins", "5 nails, 2 hammers and 3 coins"). Item kinds are
// sanitized inline (they reach the prompt). Returns "nothing" only for an
// all-empty payment, a state the intake gates reject. ZBBS-HOME-393.
func formatOfferPayment(amount int, payItems []sim.ItemKindQty) string {
	parts := make([]string, 0, len(payItems)+1)
	for _, pi := range payItems {
		name := sanitizeInline(string(pi.Kind))
		if name == "" {
			name = "item"
		}
		parts = append(parts, fmt.Sprintf("%d %s", pi.Qty, name))
	}
	if amount > 0 {
		unit := "coins"
		if amount == 1 {
			unit = "coin"
		}
		parts = append(parts, fmt.Sprintf("%d %s", amount, unit))
	}
	switch len(parts) {
	case 0:
		return "nothing"
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

func renderPayOffers(b *strings.Builder, offers []sim.PayOfferWarrantReason, nameOf func(sim.ActorID) string, stockOf func(sim.ItemKind) (int, bool), roomAlreadySold map[sim.LedgerID]sim.OrderID) {
	if len(offers) == 0 {
		return
	}
	b.WriteString("## Offers awaiting your decision\n")
	for i, o := range offers {
		disposition := "to keep"
		if o.ConsumeNow {
			disposition = "to consume now"
		}
		buyer := nameOf(o.Buyer)
		item := sanitizeInline(string(o.Item))
		if item == "" {
			item = "item"
		}
		// Payment may be coins, goods (barter), or both (ZBBS-HOME-393) —
		// render whatever the buyer offered so the seller judges the goods
		// the same way they judge coins.
		payment := formatOfferPayment(o.Amount, o.PayItems)
		fmt.Fprintf(b, "%d. %s offers %s for %d %s %s (offer id %d)",
			i+1, buyer, payment, o.Qty, item, disposition, o.LedgerID)
		// ZBBS-HOME-459: when the buyer asks for more than the seller actually
		// holds, surface the gap so they counter or decline against real stock
		// instead of accepting an offer the deliver gate would then bounce. Fact
		// only, and only when it bites — sufficient stock adds nothing. stockOf
		// reports (on-hand, stocked); a service or never-stocked kind returns
		// stocked=false and is skipped (no inventory to compare against).
		if stockOf != nil {
			if have, stocked := stockOf(o.Item); stocked && o.Qty > have {
				fmt.Fprintf(b, " — you hold only %d %s", have, item)
			}
		}
		// LLM-89: this buyer already holds a room from you that you have not
		// handed over. Accepting a second mints a duplicate order (and the
		// AcceptPay gate now rejects it), so steer to deliver the one already
		// sold rather than sell another night.
		if oid, ok := roomAlreadySold[o.LedgerID]; ok {
			fmt.Fprintf(b, " — you already sold %s a room (order #%d) you have not handed over; deliver that with deliver_order before accepting another", buyer, oid)
		}
		b.WriteString("\n")
	}
	// Action first, then an explicit speak: accept/decline/counter pass in silence,
	// so prompt the speak TOOL alongside the response — same "say a word as you pass
	// it across" pattern deliver_order uses — so an NPC-to-NPC trade is visible as a
	// speech bubble (the pay_* lifecycle frames render only for the PC's own
	// transactions; bubbles spawn only from the speak tool, so the cue must name it
	// rather than just "say a word", which a weak model may satisfy as plain text).
	// ZBBS-HOME-388.
	b.WriteString("Respond first with accept_pay, decline_pay, or counter_pay, passing the offer id as ledger_id. Then also use speak for a brief reply, because the pay response itself passes in silence.\n")
}

// renderLaborOffers renders the employer-side pending-work-offer decision
// section: one line per offer carrying the labor_id (the load-bearing field
// the model must echo into accept_work/decline_work), the worker, the reward,
// and how long the job takes. Uncapped by design — labor offers are inherently
// few (bounded by co-present workers), and the section must always carry the
// labor_id whenever gateTools advertises the response tools (the discussion-109
// invariant). LLM-26.
func renderLaborOffers(b *strings.Builder, offers []LaborOfferView, employerCoins int, nameOf func(sim.ActorID) string) {
	if len(offers) == 0 {
		return
	}
	b.WriteString("## Work offers awaiting your decision\n")
	anyAffordable, anyUnaffordable := false, false
	for i, o := range offers {
		worker := nameOf(o.Worker)
		unit := "coins"
		if o.Reward == 1 {
			unit = "coin"
		}
		fmt.Fprintf(b, "%d. %s offers to do a job for you for %d %s — about %s of work (offer id %d)\n",
			i+1, worker, o.Reward, unit, humanizeWorkMinutes(o.DurationMin), o.LaborID)
		// A reward the employer can't cover is a doomed accept: accept_work's
		// funds gate would only flip the offer to failed_unavailable
		// (buyerCanAfford, labor_commands.go), so the model "accepts" verbally
		// and the deal dies in silence. Steer the broke employer to decline WITH
		// a spoken reason instead — naming speak explicitly, because decline_work
		// (like accept_work) passes in silence, the same reason the all-affordable
		// footer below names it. Matches the funds gate exactly (Coins < Reward)
		// so the cue and the substrate never disagree. LLM-158.
		if employerCoins < o.Reward {
			anyUnaffordable = true
			coinUnit := "coins"
			if employerCoins == 1 {
				coinUnit = "coin"
			}
			fmt.Fprintf(b, "You only have %d %s, so you cannot pay for this — call decline_work (offer id %d), then use speak to tell them you have not enough coin to take them on.\n",
				employerCoins, coinUnit, o.LaborID)
			continue
		}
		anyAffordable = true
	}
	// Action first, then an explicit speak — same "say a word as you decide"
	// pattern the pay decision section uses (the accept_work/decline_work call
	// itself passes in silence). When SOME offers are unaffordable, scope the
	// footer to the affordable ones so a weak model can't apply a generic
	// "accept_work or decline_work" to an offer that was just steered to decline.
	// Suppressed entirely when EVERY offer is unaffordable — each carried its own
	// decline steer above. LLM-158.
	switch {
	case anyAffordable && anyUnaffordable:
		b.WriteString("For an offer you can afford, respond with accept_work or decline_work, passing the offer id as labor_id; decline_work the ones you cannot pay. Then also use speak for a brief reply, because the work response itself passes in silence.\n")
	case anyAffordable:
		b.WriteString("Respond with accept_work or decline_work, passing the offer id as labor_id. Then also use speak for a brief reply, because the work response itself passes in silence.\n")
	}
}

// renderLaborSelfState renders the worker's own in-progress job as a self-state
// line (LLM-26) — who they're working for and roughly how much longer, with the
// nudge to stay with it. Placed in the self-state block (top) because it is
// point-in-time "what I'm doing right now." Content-gated on Laboring != nil.
func renderLaborSelfState(b *strings.Builder, laboring *LaboringView, nameOf func(sim.ActorID) string, renderedAt time.Time) {
	if laboring == nil {
		return
	}
	employer := nameOf(laboring.Employer)
	mins := minutesUntil(laboring.Until, renderedAt)
	if mins <= 0 {
		fmt.Fprintf(b, "You are finishing a job for %s — the work is just about done; you'll be paid as you finish.\n", employer)
		return
	}
	fmt.Fprintf(b, "You are working a job for %s — about %s of work left. Stay with it until it's done; you are paid when you finish.\n",
		employer, humanizeWorkMinutes(mins))
}

// renderPendingLaborOfferOut renders the worker's OWN outgoing labor offer that
// is still awaiting the employer's answer (LLM-164) — the awaiting-acceptance
// mirror of renderLaborSelfState's in-progress line. A worker who has solicited
// has no Working job yet, so this is the only labor self-state they get while
// waiting; it names what's on the table and says plainly to sit tight, the anchor
// that keeps the weak model from flailing into an unrelated tool under the quiet
// backstop / "choose one action" pressure. Content-gated on PendingLaborOfferOut.
func renderPendingLaborOfferOut(b *strings.Builder, offer *PendingLaborOfferOutView, nameOf func(sim.ActorID) string) {
	if offer == nil {
		return
	}
	unit := "coins"
	if offer.Reward == 1 {
		unit = "coin"
	}
	fmt.Fprintf(b, "You've offered to work for %s for %d %s (about %s) — your offer stands and it is their move now. There's nothing more to do on it; wait for their answer, say a brief word if you like, then call done().\n",
		nameOf(offer.Employer), offer.Reward, unit, humanizeWorkMinutes(offer.DurationMin))
}

// renderLaborAffordance renders the free-worker option cue (LLM-26): the
// subject takes work for pay and has someone here to offer it to. Content-gated
// on CanSolicitWork, the same signal that gates the solicit_work tool.
func renderLaborAffordance(b *strings.Builder, canSolicit bool) {
	if !canSolicit {
		return
	}
	b.WriteString("You take work for pay. If someone here outside your own household or trade has a task you could do and you want the coin, offer your labor with solicit_work — name them, the coins you want, and roughly how long the job will take.\n")
}

// renderSeekWorkPlaces lists the town's businesses as move_to destinations for a
// broke worker nudged to go earn (LLM-152) — the directional companion to the
// seek-work impulse line (the "go seek work" warrant renders separately in the
// what-just-happened block). Content-gated on a non-empty list, which Build
// populates only for a broke idle worker with no employer present. Each business is
// a bullet carrying its qualitative distance + direction (LLM-155), matching the
// eat/drink cue's "a fair walk south" phrasing so the worker favours a near, open
// shop. Names only: each is a structure navigable by move_to-by-name (LLM-142).
func renderSeekWorkPlaces(b *strings.Builder, places []SeekWorkPlace) {
	if len(places) == 0 {
		return
	}
	b.WriteString("If you mean to take paid work, use move_to to head to one of the town's businesses and offer your labor once you arrive:\n")
	for _, p := range places {
		b.WriteString("- ")
		b.WriteString(sanitizeInline(p.Name))
		if p.Distance != "" {
			fmt.Fprintf(b, " — %s", p.Distance)
			if p.Direction != "" {
				fmt.Fprintf(b, " %s", p.Direction)
			}
		}
		b.WriteString("\n")
	}
}

// humanizeWorkMinutes renders a work duration in minutes as legible prose for a
// weak model ("45 minutes", "2 hours", "1 hour 30 minutes") — concrete time,
// not a terse count (the salem-prose convention). LLM-26.
func humanizeWorkMinutes(min int) string {
	if min < 60 {
		return fmt.Sprintf("%d minutes", min)
	}
	h := min / 60
	m := min % 60
	hUnit := "hours"
	if h == 1 {
		hUnit = "hour"
	}
	if m == 0 {
		return fmt.Sprintf("%d %s", h, hUnit)
	}
	return fmt.Sprintf("%d %s %d minutes", h, hUnit, m)
}

// minutesUntil returns whole minutes from now to t, floored at 1 for any
// positive sub-minute remainder (so "about 1 minute" rather than "0"), and 0
// when t is at or before now. A zero renderedAt (hand-built payload with no
// clock) yields a far-future duration; callers content-gate on Laboring, so a
// missing clock just renders a long "left" value rather than crashing. LLM-26.
func minutesUntil(t, now time.Time) int {
	d := t.Sub(now)
	if d <= 0 {
		return 0
	}
	m := int(d / time.Minute)
	if m == 0 {
		m = 1
	}
	return m
}

// renderPendingOffersFromMe renders the buyer-side "## Offers you have
// standing" section — the subject's OWN pay-with-item offers still awaiting the
// seller's answer (ZBBS-HOME-413; copy re-registered to light period voice in
// ZBBS-HOME-421 — NPCs mirror the register of what they read, and the old
// contract language came back out of their mouths verbatim). Semantics and
// functional tokens (offer ids, tool names, counts) are load-bearing; rewordings
// must keep them intact. It is the mirror of renderPayOffers (the seller's
// "offers awaiting your decision"): the seller sees offers staked AGAINST them;
// the buyer sees offers they HAVE staked. Its job is suppression — a hungry
// buyer who already has an open offer should wait, not re-stake the same offer
// next tick (the cross-tick repeat-offer storm). One line per offer; the
// closing line is an explicit "don't re-offer" instruction.
//
// Uncapped by design, like renderPayOffers: pending outgoing offers are bounded
// by the buyer's own tool calls (few), and the whole point is that every open
// offer is visible so none gets re-staked. SellerName is already acquaintance-
// gated at build time; item kinds are sanitized inline here (they reach the
// prompt). Payment terms reuse formatOfferPayment so the buyer reads the same
// "5 nails and 3 coins" shape the seller sees.
func renderPendingOffersFromMe(b *strings.Builder, offers []PendingOfferView) {
	if len(offers) == 0 {
		return
	}
	b.WriteString("## Offers you have standing\n")
	for i, o := range offers {
		seller := sanitizeInline(o.SellerName)
		if seller == "" {
			seller = "someone"
		}
		item := sanitizeInline(string(o.Item))
		if item == "" {
			item = "item"
		}
		payment := formatOfferPayment(o.Amount, o.PayItems)
		fmt.Fprintf(b, "%d. You have asked %s for %d %s, %s offered — they have yet to give their answer (offer id %d).\n",
			i+1, seller, o.Qty, item, payment, o.LedgerID)
	}
	b.WriteString("Bide for their answer; make no second offer for the same goods while this one stands. Should you think better of it, withdraw_pay recalls it.\n")
}

// renderStandingQuotesFromMe renders the seller-side "## Offers you've put out"
// section — the subject's OWN active scene-quotes still awaiting a buyer's answer
// (LLM-45). It is the seller/scene_quote mirror of renderPendingOffersFromMe (the
// buyer/pay_with_item "## Offers you have standing"): there the buyer sees offers
// it staked; here the seller sees the wares it has offered. The job is the same —
// give cross-tick memory so the seller neither re-posts a standing quote (the
// already_quoted thrash) nor invents a queue between two co-present askers because
// it can't recall whom it already served (the John Ellis two-room scene). One line
// per quote, targeted or public; the closing line is an explicit "await, don't
// re-offer".
//
// Distinct header from the buyer-side "## Offers you have standing": a keeper can
// hold both at once — its own quotes here AND pending pay offers it must answer
// under "## Offers awaiting your decision". Uncapped, like its buyer twin:
// standing quotes are bounded by the seller's own tool calls, and every open offer
// must stay visible so none gets re-posted. BuyerName is acquaintance-gated at
// build time; item kinds sanitized inline. Price reuses formatOfferPayment (coins
// only — a scene-quote names a coin price; any barter leg rides the buyer's
// pay_with_item) for the shape the other offer sections use.
func renderStandingQuotesFromMe(b *strings.Builder, quotes []StandingQuoteView) {
	if len(quotes) == 0 {
		return
	}
	b.WriteString("## Offers you've put out\n")
	for i, q := range quotes {
		items := formatQuoteLines(q.Lines)
		if items == "" {
			items = "item"
		}
		price := formatOfferPayment(q.Amount, nil)
		if q.BuyerName != "" {
			fmt.Fprintf(b, "%d. You have offered %s %s for %s — they have yet to answer.\n",
				i+1, sanitizeInline(q.BuyerName), items, price)
			continue
		}
		fmt.Fprintf(b, "%d. You have offered %s for %s to anyone here — none has yet taken it.\n",
			i+1, items, price)
	}
	// Steer against re-posting a STANDING offer (the already_quoted thrash), not
	// against making a fresh offer to a different buyer — a keeper with rooms or
	// stock to spare can legitimately offer the same kind to a second seeker
	// (the two-room scene this fixes). So the close names the listed offers, not
	// "the same goods" (which the buyer-side close uses, where double-buying a
	// single need IS wrong).
	b.WriteString("Bide for an answer; an offer listed above already stands — do not post it again.\n")
}

// renderRecentlyResolvedOffersFromMe renders the buyer-side "## Recently settled
// offers" section — the subject's OWN offers that JUST resolved (built by
// buildRecentlyResolvedOffersFromMe). It is the reliable, snapshot-scanned
// counterpart to the PayResolvedWarrantReason event line, which can arrive a
// tick late (the warrant opens a fresh cycle when the seller accepts mid-tick),
// leaving the buyer to re-perceive "the seller has it for sale" and re-buy a
// need already met. An accepted line says the deal is done (and, for an eat-here
// deal, that the goods were used on the spot) and tells the buyer not to offer
// for it again; a close-without-a-deal line tells the buyer to stop waiting.
// Copy is plain modern English on purpose — the weak stateful models parse it
// more reliably than period voice. Item kinds are sanitized inline. Uncapped —
// bounded by the buyer's own recent offers and the short resolution window.
func renderRecentlyResolvedOffersFromMe(b *strings.Builder, offers []ResolvedOfferView) {
	if len(offers) == 0 {
		return
	}
	b.WriteString("## Recently settled offers\n")
	for i, o := range offers {
		seller := sanitizeInline(o.SellerName)
		if seller == "" {
			seller = "someone"
		}
		item := sanitizeInline(string(o.Item))
		if item == "" {
			item = "item"
		}
		if o.Accepted {
			payment := formatOfferPayment(o.Amount, o.PayItems)
			gotIt := "it's in your pack now"
			if o.ConsumeNow {
				gotIt = "you had it right away"
			}
			fmt.Fprintf(b, "%d. %s accepted your offer — you paid %s for %d %s; %s. That deal is done — don't offer for it again (offer id %d).\n",
				i+1, seller, payment, o.Qty, item, gotIt, o.LedgerID)
			continue
		}
		fmt.Fprintf(b, "%d. Your offer to %s for %d %s didn't go through — it's closed, so stop waiting on it (offer id %d).\n",
			i+1, seller, o.Qty, item, o.LedgerID)
	}
}

// renderCountersAwaitingMyResponse renders the buyer-side "## A counter to your
// offer" section — a seller's counter to an offer the buyer placed that the
// buyer has not yet answered, surfaced from the standing ledger scan
// (buildCountersAwaitingMyResponse) rather than the timing-fragile
// PayResolvedWarrantReason{Countered} event so it cannot ride a tick late or
// vanish if the warrant is evicted while the buyer is shelved (LLM-21). It is the
// buyer-side mirror of renderPayOffers (the seller's "offers awaiting your
// decision"): the buyer learns the seller wants different terms and how to act on
// them. Copy is plain modern English, like its settled-offers sibling, for the
// weak stateful models — it tells the buyer to answer with a fresh pay_with_item
// carrying in_response_to, or let the counter go. Payment terms reuse
// formatOfferPayment so the buyer reads the same "5 nails and 3 coins" shape the
// seller proposed. Item kinds sanitized inline. Uncapped — bounded by the buyer's
// own recent counters and the short response window.
func renderCountersAwaitingMyResponse(b *strings.Builder, counters []CounterOfferView) {
	if len(counters) == 0 {
		return
	}
	b.WriteString("## A counter to your offer\n")
	for i, c := range counters {
		seller := sanitizeInline(c.SellerName)
		if seller == "" {
			seller = "someone"
		}
		item := sanitizeInline(string(c.Item))
		if item == "" {
			item = "item"
		}
		terms := formatOfferPayment(c.CounterAmount, c.CounterPayItems)
		fmt.Fprintf(b, "%d. %s countered your offer for %d %s — they now want %s (offer id %d).\n",
			i+1, seller, c.Qty, item, terms, c.LedgerID)
	}
	b.WriteString("To take a counter, make a fresh offer at their terms with pay_with_item, setting in_response_to to the offer id above. If the new terms don't suit you, you may simply let it go.\n")
}

// isSectionSurfacedKind reports whether a warrant kind wakes the actor for a
// tick but must NOT emit a generic "## What just happened" line — rendering one
// produced the vague "something happened nearby" catch-all (ZBBS-WORK-407).
// These warrants are still consumed to wake the actor (that is how it ticks to
// read the rest of the prompt); they just have no standalone event line. Most
// carry their content in a dedicated section; the bare operator nudge has no
// in-world content at all:
//   - pay_offer  -> "## Offers awaiting your decision" (PayOffersForMe)
//   - shift_duty -> the return-to-post steer (DutySteer)
//   - admin      -> a bare operator force-tick (umbilical /nudge with no
//     message). Not an in-world event, so it falls to the routine
//     check-in line rather than a fabricated "something happened"
//     (ZBBS-WORK-418). A nudge WITH a message keeps its felt-
//     impulse line (WarrantKindImpulse) — that is real content.
func isSectionSurfacedKind(k sim.WarrantKind) bool {
	switch k {
	case sim.WarrantKindPayOffer, sim.WarrantKindShiftDuty, sim.WarrantKindAdmin:
		return true
	default:
		return false
	}
}

func renderWarrants(b *strings.Builder, warrants []sim.WarrantMeta, nameOf func(sim.ActorID) string, placeNameOf func(string) string, eatHereKind func(sim.ItemKind) bool, buyRedundancy func(sim.ItemKind) (produced, atCap bool), cfg RenderConfig, out *RenderedPrompt) {
	// Nil-safe for direct/test callers — the main Render path always passes
	// its closure, but the signature grew by a callback (ZBBS-WORK-405) and
	// a nil here must degrade to "no eat-here tag", not panic (code_review).
	if eatHereKind == nil {
		eatHereKind = func(sim.ItemKind) bool { return false }
	}
	// Same nil-safety for the LLM-171 buyer-redundancy callback: a nil here must
	// degrade to "never redundant" (every quote keeps its actionable take).
	if buyRedundancy == nil {
		buyRedundancy = func(sim.ItemKind) (bool, bool) { return false, false }
	}
	// ZBBS-WORK-407: drop warrants already surfaced by a dedicated section so they
	// don't double-render as the vague "something happened nearby" catch-all. They
	// still WAKE the actor (the reactor consumed them — that's how it ticks to read
	// the section); they just have no standalone "what just happened" line. Filter
	// a local copy so the caller's p.Warrants (scene grouping, telemetry) is
	// untouched and the surviving lines keep contiguous numbering.
	renderable := warrants[:0:0]
	for _, wm := range warrants {
		if isSectionSurfacedKind(wm.Kind()) {
			continue
		}
		renderable = append(renderable, wm)
	}
	warrants = renderable
	// Neutral event log, not an imperative: a self-caused beat (you arrived where
	// you walked to) is nothing to "address", and the act-now coda already carries
	// the "respond to this" weight, so "— address these" over-claimed (ZBBS-WORK-419).
	b.WriteString("## What just happened\n")
	if len(warrants) == 0 {
		b.WriteString("(nothing specific — this is a routine check-in)\n")
		return
	}

	// Render each candidate warrant into its own line first, so the
	// MaxSectionBytes accounting can measure real rendered size before
	// committing it.
	var section strings.Builder
	sectionBytes := 0
	cutoff := len(warrants)
	for i, w := range warrants {
		if i >= cfg.MaxWarrants {
			cutoff = i
			break
		}
		line, truncated := renderWarrantLine(i+1, w, nameOf, placeNameOf, eatHereKind, buyRedundancy, cfg.MaxBytesPerWarrant)
		if sectionBytes+len(line) > cfg.MaxSectionBytes && i > 0 {
			// At least one warrant already rendered; this one would
			// overflow the section cap — stop here and carry the rest.
			cutoff = i
			break
		}
		section.WriteString(line)
		sectionBytes += len(line)
		out.RenderedWarrantCount++
		if truncated {
			out.TruncatedWarrants++
		}
	}

	b.WriteString(section.String())

	if cutoff < len(warrants) {
		dropped := warrants[cutoff:]
		out.DroppedWarrants = make([]sim.WarrantMeta, len(dropped))
		copy(out.DroppedWarrants, dropped)
		fmt.Fprintf(b, "(%d more signal(s) not shown here — they are carried forward to your next turn)\n",
			len(out.DroppedWarrants))
	}
}

// renderWarrantLine renders one warrant as a single numbered prose line. Every
// actor reference is resolved to a name via nameOf (never a raw UUID), and the
// old "[kind] (scene <uuid>)" machine prefix is gone — each kind reads as a
// sentence. The untrusted free-text payload (a speech excerpt) is sanitized and
// capped; the returned bool reports whether that text was truncated.
// ZBBS-HOME-339.
func renderWarrantLine(n int, w sim.WarrantMeta, nameOf func(sim.ActorID) string, placeNameOf func(string) string, eatHereKind func(sim.ItemKind) bool, buyRedundancy func(sim.ItemKind) (produced, atCap bool), maxTextBytes int) (string, bool) {
	switch r := w.Reason.(type) {
	case sim.PCSpeechWarrantReason:
		return renderSpeechWarrantLine(n, nameOf(r.Speaker), r.Excerpt, maxTextBytes)
	case sim.NPCSpeechWarrantReason:
		return renderSpeechWarrantLine(n, nameOf(r.Speaker), r.Excerpt, maxTextBytes)
	case sim.PaidWarrantReason:
		return renderPaidWarrantLine(n, nameOf(r.Buyer), r.Amount, r.ForText, maxTextBytes)
	case sim.IdleBackstopWarrantReason:
		return renderIdleBackstopWarrantLine(n, r.QuietDuration), false
	case sim.StrandedWarrantReason:
		return renderStrandedWarrantLine(n), false
	case sim.RestockWarrantReason:
		return renderRestockWarrantLine(n, r.Item, r.Source), false
	case sim.ConsumedWarrantReason:
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.DwellStartedWarrantReason:
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.DwellEndedWarrantReason:
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.DwellTickAppliedWarrantReason:
		// ZBBS-WORK-407: the per-tick beat used to be suppressed (fell through to
		// the vague "something happened" fallback) because it fired every minute.
		// The wake is now cadenced to the red-tier boundary (handlers/dwell_reactor.go),
		// so this fires at most once per dwell — render its felt line like its
		// DwellStarted / DwellEnded siblings.
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.SourceActivityCompletedWarrantReason:
		// LLM-69: the NPC completion beat for a finished eat/drink/harvest, pre-
		// rendered at the subscriber — same narration-line path as the dwell beats.
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.AdminDirectiveWarrantReason:
		return renderImpulseWarrantLine(n, r.Message, maxTextBytes)
	case sim.SeekWorkWarrantReason:
		// LLM-141/168: a workless worker (no post of its own) is woken to go find
		// odd jobs. Engine-authored felt impulse, generic (no named hirer) — the
		// worker decides freely where to go and whom to ask. Wake-from-anywhere
		// nudge in the style of the stall-repair / production-choice lines; the
		// standing labor affordance ("you take work for pay … solicit_work") renders
		// separately once it is co-present with someone. Framed on having no work of
		// its own, not an empty purse — the nudge fires for a workless worker whether
		// or not it holds coin (LLM-168).
		return fmt.Sprintf("%d. You have no work of your own to tend, and you take work for pay — seek out someone who could use a hand and offer your labor.\n", n), false
	case sim.ArrivalWarrantReason:
		return renderArrivalWarrantLine(n, nameOf(w.TriggerActorID), r, placeNameOf), false
	case sim.NeedThresholdWarrantReason:
		return renderNeedNudgeLine(n, r.Need), false
	case sim.SceneQuoteTargetedWarrantReason:
		// A bundle is eat-here if ANY line is non-portable (the whole bundle
		// was clamped to eat-here at quote creation, LLM-101).
		eatHere := false
		for _, ln := range r.Lines {
			if eatHereKind(ln.ItemKind) {
				eatHere = true
				break
			}
		}
		// LLM-171: when EVERY line in the quote is a good this buyer makes itself
		// or already holds at cap, the take is degenerate — strip the actionable
		// take and steer them off it. A mixed bundle (≥1 genuinely wanted good)
		// renders with its normal take.
		redundancy := buyQuoteRedundancyReason(r.Lines, buyRedundancy)
		return renderQuoteWarrantLine(n, nameOf(r.SellerID), r, eatHere, redundancy), false
	case sim.PayResolvedWarrantReason:
		return renderPayResolvedWarrantLine(n, nameOf(r.Seller), r, maxTextBytes), false
	case sim.ServeHandoverWarrantReason:
		return renderServeHandoverWarrantLine(n, nameOf(r.Buyer), r), false
	case sim.ProductionChoiceWarrantReason:
		// LLM-116: the workplace is free and there's work to do — the "## Time to
		// produce" cue carries the options + the produce tool; this line is just the
		// "why you ticked" beat, like the idle-backstop / need-nudge lines.
		return fmt.Sprintf("%d. It's time to produce — decide what to make next.\n", n), false
	case sim.StallRepairWarrantReason:
		// LLM-118: the stall just wore through the repair threshold. At the stall
		// the "## Your stall" cue carries the nail count + buy-from-the-smith
		// steer; this is the wake-from-anywhere nudge to go tend it.
		return fmt.Sprintf("%d. Your market stall has worn from use and needs mending — go to it and repair it (you'll need nails; the smith sells them).\n", n), false
	default:
		return renderBasicWarrantLine(n, w.Kind(), nameOf(w.TriggerActorID)), false
	}
}

// renderArrivalWarrantLine renders an arrival as "<who> arrived at <place>."
// naming the destination the mover walked to (ZBBS-WORK-358) — decision-useful
// ("you reached the General Store, do what you came for") rather than the old
// vacuous "arrived nearby". Falls back to "<who> arrived." when the destination
// was a bare position with no nameable place. who is the pre-resolved subject
// ("you" for self), capitalized to match the huddle self-lines.
func renderArrivalWarrantLine(n int, who string, r sim.ArrivalWarrantReason, placeNameOf func(string) string) string {
	subject := who
	if subject == "you" {
		subject = "You"
	}
	// A valid MoveDestination names exactly one kind, so at most one of these
	// is set; if a malformed reason ever set both, structure wins by design.
	place := placeNameOf(string(r.AtStructureID))
	if place == "" {
		place = placeNameOf(string(r.AtObjectID))
	}
	if place == "" {
		return fmt.Sprintf("%d. %s arrived.\n", n, subject)
	}
	return fmt.Sprintf("%d. %s arrived at %s.\n", n, subject, sim.WithDefiniteArticle(place))
}

// renderBasicWarrantLine renders the kinds carried by BasicWarrantReason (the
// huddle lifecycle events) plus any future kind without a dedicated case. The
// huddle events get felt prose; "joined"/"left" are stamped on the actor
// themselves (so the subject is "you"), the "peer_" variants on the others (so
// the trigger is the peer who came or went). An unrecognized kind falls back to
// a quiet, name-resolved line rather than a raw "[kind] involving <uuid>".
func renderBasicWarrantLine(n int, kind sim.WarrantKind, who string) string {
	switch kind {
	case sim.WarrantKindHuddleJoined:
		return fmt.Sprintf("%d. You joined a conversation.\n", n)
	case sim.WarrantKindHuddleLeft:
		return fmt.Sprintf("%d. You left the conversation.\n", n)
	case sim.WarrantKindHuddlePeerJoined:
		return fmt.Sprintf("%d. %s joined your conversation.\n", n, who)
	case sim.WarrantKindHuddlePeerLeft:
		return fmt.Sprintf("%d. %s stepped away from your conversation.\n", n, who)
	case sim.WarrantKindHuddleConcluded:
		return fmt.Sprintf("%d. Your conversation has broken up.\n", n)
	default:
		// A kind with no felt template lands here — an unhandled warrant kind, or
		// a narration warrant (dwell/consumed) whose text came back empty and fell
		// through renderNarrationWarrantLine. The line is useless to the model
		// either way; we tag it with the originating warrant kind (ZBBS-WORK-417)
		// so an operator who spots a vague "something happened" can trace its
		// source from the prompt alone — the kind is otherwise consumed by this
		// switch and never shown, and the engine's per-tick ring resets on restart.
		if who != "" && who != "someone" && who != "you" {
			return fmt.Sprintf("%d. Something happened involving %s. [debug: unrendered warrant kind=%q]\n", n, who, kind)
		}
		return fmt.Sprintf("%d. Something happened nearby. [debug: unrendered warrant kind=%q]\n", n, kind)
	}
}

// renderNeedNudgeLine renders a need-threshold warrant as a felt pang. The
// "## You" needs line carries the real urgency (Address now: …); this is the
// in-the-moment beat that the need just crossed into distress. Falls back to a
// generic pang for an unrecognized need key.
func renderNeedNudgeLine(n int, need sim.NeedKey) string {
	switch need {
	case "hunger":
		return fmt.Sprintf("%d. Your hunger is pressing on you.\n", n)
	case "thirst":
		return fmt.Sprintf("%d. Your thirst is pressing on you.\n", n)
	case "tiredness":
		return fmt.Sprintf("%d. Weariness is settling over you.\n", n)
	default:
		return fmt.Sprintf("%d. A need is pressing on you.\n", n)
	}
}

// buyQuoteRedundancyReason classifies whether a buy-quote is pointless for the
// buyer: every line is a good they MAKE themselves ("produced") or already hold
// at cap ("atcap"), so taking it just buys back their own ware or overflows
// their carry (LLM-171). Returns "" when at least one line is a good worth
// buying, so the quote renders with its normal actionable take. "produced" wins
// the label when every line is produced; a mix of produced + at-cap lines is
// "atcap" so the steer leads with the carry reason. Nil/empty inputs → "".
func buyQuoteRedundancyReason(lines []sim.QuoteLine, redundant func(sim.ItemKind) (produced, atCap bool)) string {
	if len(lines) == 0 || redundant == nil {
		return ""
	}
	allProduced := true
	for _, ln := range lines {
		produced, atCap := redundant(ln.ItemKind)
		if !produced && !atCap {
			return "" // a genuinely wanted good — render the normal take
		}
		if !produced {
			allProduced = false
		}
	}
	if allProduced {
		return "produced"
	}
	return "atcap"
}

// renderQuoteWarrantLine renders a vendor's scene quote aimed directly at this
// actor — a standing offer they can take by paying. Names the seller; the
// terms come straight off the warrant payload. The take-instruction carries
// the quote_id: without it the buyer model answered a standing quote with a
// bare pay_with_item, minting a crossing offer that deadlocked against the
// quote (ZBBS-HOME-424) — the fast path existed but was never legible.
//
// redundancy (LLM-171), when non-empty, replaces the actionable take with a
// steer: "produced" — the buyer makes these wares itself; "atcap" — it already
// holds all of these it can carry. Either way there is no reason to buy, so the
// quote_id take is withheld and the line tells the buyer to decline.
func renderQuoteWarrantLine(n int, seller string, r sim.SceneQuoteTargetedWarrantReason, eatHere bool, redundancy string) string {
	unit := "coins"
	if r.Amount == 1 {
		unit = "coin"
	}
	items := formatQuoteLines(r.Lines)
	// The eat-here disposition fact (ZBBS-WORK-405): goods of this class
	// can't be carried away, so say so up front rather than letting the
	// buyer plan a take-home the clamp will quietly rewrite.
	disposition := ""
	if eatHere {
		disposition = ", to eat here (it can't be carried away)"
	}
	// The take-instruction carries the quote_id. A bundle (LLM-101) is taken
	// whole, so it needs only the quote_id + total amount; a single-item quote
	// names the concrete item/qty/amount (LLM-172 — the prior "the same item,
	// qty, and amount" phrasing had no anchor, so a buyer carrying other goods
	// bound "item" to one of those: pay_with_item then rejected the term
	// mismatch and the model fell back to a bare pay, leaking coins for an
	// undelivered good with the quote still open).
	//
	// LLM-136: a single-item quote is the COIN settlement path — goods can't ride
	// a quote_id (that rejects). A coin-short buyer isn't stuck, though: barter is
	// a first-class path via a SEPARATE offer_trade that names the item it wants.
	// Saying so on the take-line keeps a coinless buyer (e.g. a homeless smith
	// eyeing a room) from looping on a price it can't meet. The want_item is the
	// concrete kind, not "this", so a weak model sends the real machine value.
	// Bundles stay coin-only here: offer_trade takes one item kind, and a bundle
	// has no single want_item to name.
	var take string
	switch {
	case len(r.Lines) > 1:
		take = fmt.Sprintf(" To take the whole bundle, call pay_with_item with quote_id %d and amount %d — it settles at once.", r.QuoteID, r.Amount)
	case len(r.Lines) == 1:
		take = fmt.Sprintf(" To take this coin quote, call pay_with_item with quote_id %d, item %q, qty %d, and amount %d — it settles at once. Don't put goods on a quote_id; if you lack coins but have goods to offer, propose a separate trade instead — call offer_trade with the goods you'll give and want_item %q; they can accept or counter.", r.QuoteID, string(r.Lines[0].ItemKind), r.Lines[0].Qty, r.Amount, string(r.Lines[0].ItemKind))
	default:
		// Defensive (code_review): a quote with zero lines shouldn't reach here —
		// sell/scene_quote require ≥1 item — but the single-item arm indexes
		// r.Lines[0], so guard the empty case instead of risking a panic on
		// malformed/legacy warrant data. Bare coin take, no item to name.
		take = fmt.Sprintf(" To take it, call pay_with_item with quote_id %d and the stated amount — it settles at once.", r.QuoteID)
	}
	// LLM-171: the buyer makes or is at cap on every quoted good — withhold the
	// take entirely and steer them to decline, so a mis-pitched quote can't drive
	// a buy-back of their own ware or an over-cap purchase.
	switch redundancy {
	case "produced":
		take = " But these are wares you make yourself — there's no reason to buy them. Decline and tend to your own work."
	case "atcap":
		take = " But you already hold all of these you can carry — there's no reason to buy more. Decline and move on."
	}
	// An overheard public quote (huddle fan-out, ZBBS-HOME-431) is an ad
	// announced to the conversation, not a direct address — "offers" not
	// "offers you", so the actor doesn't perceive a personal offer.
	offers := "offers you"
	if r.Overheard {
		offers = "offers"
	}
	return fmt.Sprintf("%d. %s %s %s for %d %s%s.%s\n", n, seller, offers, items, r.Amount, unit, disposition, take)
}

// formatQuoteLines renders a quote's item lines as a readable phrase:
// "2 blueberries", or for a bundle "2 blueberries and 2 raspberries", or
// "2 blueberries, 2 raspberries, and 3 bread" (LLM-101). Qty 1 drops the
// leading count, matching the prior single-item rendering. Items are
// sanitized inline (catalog keys, but defensive against odd labels).
func formatQuoteLines(lines []sim.QuoteLine) string {
	parts := make([]string, 0, len(lines))
	for _, ln := range lines {
		item := sanitizeInline(string(ln.ItemKind))
		if ln.Qty > 1 {
			parts = append(parts, fmt.Sprintf("%d %s", ln.Qty, item))
		} else {
			parts = append(parts, item)
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

// renderPayResolvedWarrantLine renders, to the buyer, how the seller resolved
// their pay-with-item offer. Only the buyer-meaningful terminal states get a
// bespoke line; the rest collapse to a neutral "fell through" so the buyer
// stops waiting without the engine narrating internal ledger states.
func renderPayResolvedWarrantLine(n int, seller string, r sim.PayResolvedWarrantReason, maxTextBytes int) string {
	item := sanitizeInline(string(r.ItemKind))
	qty := item
	if r.Qty > 1 {
		qty = fmt.Sprintf("%d %s", r.Qty, item)
	}
	switch r.TerminalState {
	case sim.PayTerminalStateAccepted:
		return fmt.Sprintf("%d. %s accepted your offer of %d for %s.\n", n, seller, r.Amount, qty)
	case sim.PayTerminalStateDeclined:
		return fmt.Sprintf("%d. %s declined your offer for %s.\n", n, seller, qty)
	case sim.PayTerminalStateCountered:
		// Counter terms may be coins, goods (barter), or both (ZBBS-HOME-393).
		return fmt.Sprintf("%d. %s countered: %s for %s.\n", n, seller, formatOfferPayment(r.CounterAmount, r.CounterPayItems), qty)
	default:
		return fmt.Sprintf("%d. Your offer to %s for %s fell through.\n", n, seller, qty)
	}
}

// renderServeHandoverWarrantLine renders, to the SELLER, the moment a buyer
// instantly took their posted quote (ZBBS-WORK-423). The settle already
// happened — coins and goods have changed hands — so this isn't a decision
// cue; it states the sale and steers the handover BEAT. The instant quote-take
// is the one serving path that never ticks the seller, so unlike deliver_order
// (whose tool description steers "pair with a brief speak") there's nothing
// else asking the keeper to acknowledge the customer. "Hand it over with a
// word" is that steer, kept to a greeting beat — not a re-pitch (a greeting is
// not a sale). The model voices the line in character; the engine doesn't
// supply the words. buyer is pre-resolved by the caller.
func renderServeHandoverWarrantLine(n int, buyer string, r sim.ServeHandoverWarrantReason) string {
	if buyer == "" {
		buyer = "someone"
	}
	unit := "coins"
	if r.Amount == 1 {
		unit = "coin"
	}
	item := sanitizeInline(string(r.ItemKind))
	qty := item
	if r.Qty > 1 {
		qty = fmt.Sprintf("%d %s", r.Qty, item)
	}
	// ConsumeNow is the buyer's disposition term (ZBBS-WORK-402): when they're
	// eating on the spot, say so, so the keeper's line fits a sit-down serve
	// rather than a counter handoff.
	if r.ConsumeNow {
		return fmt.Sprintf("%d. %s paid you %d %s for %s, to eat here now. Hand it over with a word.\n", n, buyer, r.Amount, unit, qty)
	}
	return fmt.Sprintf("%d. %s paid you %d %s for %s. Hand it over with a word.\n", n, buyer, r.Amount, unit, qty)
}

// renderNarrationWarrantLine renders a felt-language self-perception beat
// (ZBBS-HOME-302): the consume self-line and the dwell started/ended lines all
// carry a pre-rendered second-person NarrationText. Surfaces it as the warrant
// line, sanitized + capped like the speech excerpt to bound prompt cost.
//
// DwellTickApplied is deliberately NOT routed here — the per-tick "another
// bite" beat would be prompt spam, and the sustained state is already conveyed
// by the ActiveDwellCredits projection; the per-tick warrant keeps its bare
// fallback line.
//
// Empty narration (e.g. a catalog-unknown dwell end) falls back to the generic
// kind line so the warrant still registers rather than vanishing.
func renderNarrationWarrantLine(n int, kind sim.WarrantKind, narration, who string, maxTextBytes int) (string, bool) {
	if narration == "" {
		return renderBasicWarrantLine(n, kind, who), false
	}
	sanitized, truncated := sanitizeText(narration, maxTextBytes)
	return fmt.Sprintf("%d. %s\n", n, sanitized), truncated
}

// renderSpeechWarrantLine renders the warrant line for both PC- and NPC-speech
// warrant reasons (structurally identical — SpeechID / Speaker / Excerpt). The
// speaker is already name-resolved by the caller. An empty excerpt renders "X
// spoke to you" rather than a dangling `said: ""`.
func renderSpeechWarrantLine(n int, speaker, excerpt string, maxTextBytes int) (string, bool) {
	if speaker == "" {
		speaker = "someone"
	}
	sanitized, truncated := sanitizeText(excerpt, maxTextBytes)
	if strings.TrimSpace(sanitized) == "" {
		return fmt.Sprintf("%d. %s spoke to you.\n", n, speaker), truncated
	}
	return fmt.Sprintf("%d. %s said: \"%s\"\n", n, speaker, sanitized), truncated
}

// renderPaidWarrantLine renders the warrant line for a PaidWarrantReason.
// Surfaces the (name-resolved) buyer, amount, and optional flavor text to the
// seller's perception prompt — the seller's next reactor tick reads this and
// decides what to do (speak thanks, walk over, ignore).
//
// Without ForText: `N. <buyer> paid you N coins.`
// With ForText:    `N. <buyer> paid you N coins — "<for>"`.
//
// The ForText excerpt is sanitized + capped like the speech excerpt to keep
// the per-tick prompt cost bounded. Returned bool reports truncation.
func renderPaidWarrantLine(n int, buyer string, amount int, forText string, maxTextBytes int) (string, bool) {
	if buyer == "" {
		buyer = "someone"
	}
	unit := "coins"
	if amount == 1 {
		unit = "coin"
	}
	if strings.TrimSpace(forText) == "" {
		return fmt.Sprintf("%d. %s paid you %d %s.\n", n, buyer, amount, unit), false
	}
	sanitized, truncated := sanitizeText(forText, maxTextBytes)
	return fmt.Sprintf("%d. %s paid you %d %s — \"%s\"\n", n, buyer, amount, unit, sanitized), truncated
}

// renderIdleBackstopWarrantLine renders the warrant line for an
// IdleBackstopWarrantReason — the engine-injected liveness tick for an
// actor that no other warrant has engaged.
//
// Surfaces the quiet duration so the actor's LLM tick can decide what
// (if anything) to do: pursue a need, walk somewhere, sit and wait.
// The replacement for v1's chronicler-attend-to dispatch; v1 had the
// chronicler decide who to engage, v2 lets the actor's own tick decide
// what to do given that they've been quiet.
//
// Form: `N. You've been quiet for <duration> — consider what to do next.`
// The duration is rounded to whole seconds (sub-second resolution is noise at
// the minute-scale this warrant fires at).
//
// Returned without truncation since there's no untrusted free-text
// payload — the line is composed of fixed prose and an engine-computed
// duration.
func renderIdleBackstopWarrantLine(n int, quiet time.Duration) string {
	if quiet <= 0 {
		return fmt.Sprintf("%d. You've been quiet — consider what to do next.\n", n)
	}
	return fmt.Sprintf("%d. You've been quiet for %s — consider what to do next.\n",
		n, quiet.Round(time.Second))
}

// renderStrandedWarrantLine renders the anomalous-position backstop line
// (ZBBS-HOME-450): the actor is standing in the open at no anchor with
// nothing under way. The wording is a neutral observation of the actor's
// situation — it names where they are, not what to do, so the model
// re-decides freely (the same no-coercion discipline as the felt-impulse
// and atmosphere lines). Fixed prose, no untrusted payload.
func renderStrandedWarrantLine(n int) string {
	return fmt.Sprintf("%d. You find yourself standing out in the open, between places, with nothing under way.\n", n)
}

// renderRestockWarrantLine renders the warrant line for a RestockWarrantReason —
// the reorder producer's nudge to an actor whose sell-stock has dropped below the
// reorder threshold. It names the representative low item; the actionable detail
// (current/cap, suppliers or bushes, structure_ids) is in the section the line
// points to, so the line stays a short pointer. The Source routes the pointer:
// a `forage` low (LLM-90) points at "## Your bushes to harvest", everything else
// at "## Restocking" — so the cue line never sends a grower to a buy-side section
// she has no entries in.
//
// Form: `N. Your stock of <item> is running low — see <section>.`
// Form (no item): `N. Your shop stock is running low — see <section>.`
//
// Rendered without truncation: the item is an engine-controlled catalog key,
// not model- or user-supplied text.
func renderRestockWarrantLine(n int, item sim.ItemKind, source sim.RestockSource) string {
	section := "Restocking"
	if source == sim.RestockSourceForage {
		section = "Your bushes to harvest"
	}
	if item == "" {
		return fmt.Sprintf("%d. Your shop stock is running low — see %s.\n", n, section)
	}
	return fmt.Sprintf("%d. Your stock of %s is running low — see %s.\n", n, item, section)
}

// renderImpulseWarrantLine renders the warrant line for an
// AdminDirectiveWarrantReason — an operator-authored directive injected via the
// umbilical /nudge route (ZBBS-WORK-329). The operator's message is wrapped in
// an in-world felt-impulse frame so the NPC reads it as a spontaneous internal
// urge, NOT an out-of-world instruction — the same in-world-voice discipline the
// atmosphere + noticeboard prompts keep. The colon form keeps the line
// grammatical regardless of how the operator phrases the directive (it does not
// assume the message completes a "pull to …" clause).
//
// Form: `N. You feel a strong, insistent pull: <message>`
//
// The message is untrusted operator free text, so it is sanitized + capped like
// the speech excerpt; the returned bool reports truncation. An empty message
// does not reach here in practice — the handler stamps this reason only for a
// non-empty directive (an empty nudge stamps the bare admin reason) — but it is
// handled defensively so a stray empty directive renders a bare impulse rather
// than a dangling colon.
func renderImpulseWarrantLine(n int, message string, maxTextBytes int) (string, bool) {
	sanitized, truncated := sanitizeText(message, maxTextBytes)
	if sanitized == "" {
		return fmt.Sprintf("%d. You feel a strong, insistent pull to act.\n", n), false
	}
	return fmt.Sprintf("%d. You feel a strong, insistent pull: %s\n", n, sanitized), truncated
}

// sanitizeText neutralizes untrusted free text for inclusion in the prompt
// and caps its length. Control characters — crucially newlines — are
// collapsed to spaces so the text cannot inject a fake prompt section or
// otherwise break the prompt's structure. This is structural escaping, not
// semantic injection defense: it cannot stop a payload that reads like an
// instruction, only one that forges prompt *layout*. The returned bool
// reports whether the text was truncated by maxBytes.
func sanitizeText(s string, maxBytes int) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		// Replace C0 controls (incl. \n \r \t) and DEL with a space — those
		// are what could forge prompt layout. U+FFFD is left intact: ranging
		// over invalid UTF-8 already yields it (so the rebuilt string is
		// valid UTF-8 regardless), and a legitimate U+FFFD in trusted input
		// is indistinguishable from a decode-error one — stripping it would
		// be data loss with no structural benefit.
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		if r == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		b.WriteRune(r)
	}
	cleaned := strings.TrimSpace(b.String())
	return capBytes(cleaned, maxBytes)
}

// sanitizeInline is sanitizeText with no length cap — used for short
// trusted-ish fields (structure names, origin kinds) that still must not
// carry newlines into the prompt.
func sanitizeInline(s string) string {
	out, _ := sanitizeText(s, 0)
	return out
}

// capBytes truncates s to at most maxBytes bytes on a rune boundary,
// appending an ellipsis marker when it truncates. maxBytes <= 0 means no
// cap. The returned bool reports whether truncation happened.
//
// The byte cap is hard: when maxBytes is smaller than the marker itself,
// capBytes returns an empty string rather than emit a marker that would
// exceed the cap (and rather than a raw byte slice that could split a
// rune). Such a tiny cap is a misconfiguration — RenderConfig's defaults
// are far larger — but capBytes still honors the contract.
func capBytes(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}
	const marker = "…"
	if maxBytes < len(marker) {
		return "", true
	}
	budget := maxBytes - len(marker)
	// Largest rune-start index <= budget; s[:n] is then whole runes only.
	n := 0
	for i := range s {
		if i > budget {
			break
		}
		n = i
	}
	return s[:n] + marker, true
}
