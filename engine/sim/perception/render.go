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
	// Text is the DURABLE turn — the "since your last turn" events, what the NPC
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

	// Durable: just the turn header here; the "since your last turn" events append
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

	// placeKeeperOf resolves an arrived-at structure id to its keeper's display
	// name, "" when the structure has no keeper other than the arriver — the
	// possessive counterpart to placeNameOf that lets the arrival line read "You
	// arrived at <keeper>'s <place>" (LLM-284).
	placeKeeperOf := func(id string) string {
		if id == "" {
			return ""
		}
		return sanitizeInline(p.WarrantPlaceKeepers[id])
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
	// LLM-229: the pre-work leg — a worker who accepted a job and is relocating
	// to (or waiting at) the employer's workplace. Mutually exclusive with the
	// in-progress line above (a worker is either relocating or working).
	renderLaborEnRoute(&ephemeral, p.LaborEnRoute, nameOf)
	// LLM-202: the employer-side mirror — workers currently on a job for this
	// actor, so they don't re-hire or pay again for work already covered.
	renderWorkersForMe(&ephemeral, p.WorkersForMe, nameOf, p.RenderedAt)
	renderPendingLaborOfferOut(&ephemeral, p.PendingLaborOfferOut, nameOf)
	renderNarrativeState(&ephemeral, p.NarrativeState)
	renderVendorOperating(&ephemeral, p.AtOwnBusinessOperating)
	renderSurroundings(&ephemeral, p.Surroundings)
	renderAnchors(&ephemeral, p.Anchors, p.DutySteer != nil && p.DutySteer.AtPost, p.Surroundings.InsideStructureID)
	renderDutySteer(&ephemeral, p.DutySteer)
	renderEveningLeisure(&ephemeral, p.EveningLeisure)
	renderRelationships(&ephemeral, p.Relationships)
	// LLM-217: the subject's own recent deeds render just above the spoken
	// turns — together they are the actor's short-term memory of the scene,
	// and the action trail is what makes a self-loop (leave ↔ bounce back)
	// visible to the model.
	renderSelfActions(&ephemeral, p.SelfActions, p.RenderedAt)
	renderRecentConversation(&ephemeral, p.RecentConversation, p.RenderedAt)
	// The decision section renders ABOVE the affordance dumps (it used to land
	// after them): a buyer's coin on the table is the seller's most actionable
	// fact, and burying it under eat/drink and room-to-let cues let the
	// seller's own mild needs outrank a waiting customer for whole minutes
	// (conversation hud-6c849d…, ZBBS-HOME-424). renderTriage reinforces the
	// same priority at the decision point.
	renderPayOffers(&ephemeral, payOffers, nameOf, p.PayOfferShortfalls, p.RoomAlreadySoldOrderByLedger)
	// LLM-138: a gift offered TO this actor is the same "someone wants my answer"
	// decision class as a pay offer, so it renders right alongside.
	renderGiftsForMe(&ephemeral, p.GiftsForMe)
	// LLM-26: the employer's pending work-offer decisions sit alongside pay
	// offers (both are "someone wants my answer"); the worker affordance cue
	// follows so a free worker sees the option to offer their labor.
	renderLaborOffers(&ephemeral, p.LaborOffersForMe, p.Actor.Coins, p.SubjectProducesGoods, nameOf)
	renderLaborAffordance(&ephemeral, p.CanSolicitWork)
	// LLM-346: the hiring-side twin of the affordance above. Sits immediately after
	// it so the two mints of the labor market read as one pair — offer your labor,
	// or ask for someone else's.
	renderOfferWorkAffordance(&ephemeral, p.HireableWorkers, nameOf)
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
	renderStallRepairBuy(&ephemeral, p.StallRepairBuy)
	renderFarmUpkeep(&ephemeral, p.FarmUpkeep)
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
	// "since your last turn" list; only the machine telemetry block is gone.

	// Shift-duty warrants drive the wake tick but are NOT rendered — the standing
	// DutySteer cue (renderDutySteer, above) is the single voice for
	// return-to-post (ZBBS-HOME-352). Filtering here also keeps them out of the
	// cap / carry-forward budget; consuming them unrendered is fine since their
	// job is to wake the actor, which the tick already did.
	warrants := nonShiftDutyWarrants(p.Warrants)
	if len(payOffers) > 0 {
		warrants = nonPayOfferWarrants(warrants)
	}
	// Durable: the "since your last turn" events are the NPC's memory of the
	// scene. Skip the generic block only when the pay-offer section already
	// covered the whole batch; otherwise render it (this also preserves the
	// routine-check-in line for the genuinely-empty case). Warrant caps +
	// carry-forward accounting land in `out` as before.
	if len(warrants) > 0 || len(payOffers) == 0 {
		renderWarrants(&durable, warrants, nameOf, placeNameOf, placeKeeperOf, eatHereKind, buyRedundancy, p.RenderedAt, cfg, &out)
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
	// LLM-333: the endurance wind-down suppresses the owed-reply nag for the same
	// reason the loop steer does — reply-pressure is what keeps the over-long
	// conversation alive.
	conversationRunLong := p.TurnState.ConversationRunLong
	renderTurnState(&ephemeral, p.TurnState, seekWorkDirective || conversationLooping || conversationRunLong)
	renderTriage(&ephemeral, p.Actor.Needs, p.Actor.NeedThresholds, p.TurnState.AwaitingReply(), conversationLooping, conversationRunLong, p.NeedRedirect, seekWorkDirective, len(payOffers) > 0, p.Actor.InFlightMove, p.Actor.InFlightSourceActivity)

	out.Text = durable.String()
	out.EphemeralText = ephemeral.String()
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
		return fmt.Sprintf("You and the others here keep saying the same thing, but there is nothing to %s here. Don't talk it over again — go to %s (destination: %s) now and buy %s to %s.\n",
			v.Verb, sanitizeInline(v.TargetLabel), sanitizeInline(v.TargetID), sanitizeInline(v.ItemLabel), v.Verb)
	default: // NeedRedirectFree
		return fmt.Sprintf("You and the others here keep saying the same thing, but there is nothing to %s here. Don't talk it over again — go to %s (destination: %s) now and %s.\n",
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
func renderTriage(b *strings.Builder, needs map[sim.NeedKey]int, thresholds sim.NeedThresholds, awaitingReply bool, conversationLooping bool, conversationRunLong bool, needRedirect *NeedRedirectView, seekWork bool, hasPayOffers bool, inFlightMove *InFlightMoveView, inFlightSourceActivity *InFlightSourceActivityView) {
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
	case conversationRunLong:
		// Endurance wind-down coda (LLM-333): the huddle has talked for a long
		// stretch with nothing coming of it — no trade, no one new, no player —
		// without lexically looping (the model paraphrases, so the case above
		// never fires; the live farewell loop measured 0.00 repetition). The
		// scene's truth is "this has run its course", so say exactly that and
		// make ending it the imperative. Ordered below conversationLooping
		// (never true together — publish picks the more specific diagnosis) and
		// above awaitingReply for the same reason the loop coda is: "run long"
		// is the more specific read of why a reply is pending. The needRedirect
		// swap is deliberately NOT applied here — it exists to break a
		// confabulated plan-loop, and this case is by definition not a loop.
		b.WriteString("This conversation has gone on a good while and nothing new is coming of it. Bring it to a close — say a brief farewell or simply turn to your own affairs, then call done(). Do not start a new topic.\n")
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
		// "choose one thing and do it" coda. Weigh the context, act on what matters,
		// take one action. Speaking is terminal (LLM-321): a successful speak ends
		// the tick on its own, so the old "after you speak, call done()" turn-
		// discipline — the prompt half of the WORK-375 re-pitch fix — is now
		// enforced by the engine rather than the prompt, and is dropped here so the
		// instruction doesn't contradict the mechanic. done() still ends a turn that
		// took a non-terminal action (or none).
		b.WriteString("Weigh what's in front of you — obligations and pressing needs come before idle matters. Choose one action, then call done() when nothing pressing remains.\n")
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
	// buys the pay path rejects (engine/sim/pay_commands.go). But "cannot pay for
	// anything" is only true with an EMPTY PACK too: a 0-coin actor holding goods
	// can still offer them in trade (pay_with_item / offer_trade, ZBBS-HOME-393/407),
	// and the satiation buy cue now steers exactly that (LLM-222) — so asserting it
	// can't pay would contradict that cue in the same prompt. `len(a.Inventory) > 0`
	// is the render-side mirror of the satiation gate's holdsBarterableGoods (both =
	// holds >=1 good, off the same snapshot inventory), so the two lines agree.
	if a.Coins == 0 {
		if len(a.Inventory) > 0 {
			b.WriteString("Coins in your purse: 0 — you have no coins to spend, but you may be able to offer goods you carry in trade.\n")
		} else {
			b.WriteString("Coins in your purse: 0 — you have no coins to spend, so you cannot pay for anything until you earn some.\n")
		}
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
			// Count-aware noun (LLM-339): "flasks of water (x20)" not "Water
			// (x20)", so the model isn't left inventing a container ("buckets").
			// Fall back to the display label for a directly-constructed item that
			// didn't resolve a count noun (e.g. the for-sale test fixtures).
			noun := it.CountNoun
			if noun == "" {
				noun = it.Label
			}
			// The use annotation folds into the quantity parens (LLM-166) so the
			// comma-separated item list stays unambiguous: "cuts of meat (x7, used
			// to produce stew)". Empty for edibles / non-ingredients.
			if it.Use != "" {
				fmt.Fprintf(b, "%s (x%d, %s)", sanitizeInline(noun), it.Qty, sanitizeInline(it.Use))
			} else {
				fmt.Fprintf(b, "%s (x%d)", sanitizeInline(noun), it.Qty)
			}
		}
		b.WriteString(".\n")
	}
	// Standing in-progress batch (LLM-319): the producer's current work,
	// surfaced on EVERY tick — including a social one when someone approaches —
	// so it can always say what it is making (a PC can ask), and a tick firing
	// mid-batch knows to stay at the post rather than re-decide from scratch.
	if a.InFlightProduction != nil {
		fmt.Fprintf(b, "You are making a batch of %s — about %s of work left; it only moves along while you're at your post.\n",
			sanitizeInline(a.InFlightProduction.ItemLabel), a.InFlightProduction.WorkLeft)
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
		// LLM-212: annotate when the actor is inside its OWN home/workplace, so a
		// weak model reads "inside the James Residence, your home" and can tell it
		// is already at its anchor (the legibility half of the move_to(home)
		// confusion). Set only for the inside branch (Build computes it from the
		// actor's home/work ids); empty otherwise.
		if s.InsideRelation != "" {
			location += ", " + s.InsideRelation
		}
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
		// the SAME set the speak path would reach (ZBBS-WORK-407). Presence is
		// stated neutrally — no "speak to them" coaching. The directive fired on
		// every arrival and pushed NPCs into unprompted monologues at whoever was
		// present, PCs included; naming alone is enough for a greeting to happen
		// when the actor has a social reason for one (LLM-220).
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
			label += laboringPhrase(m)
			names[i] = label
		}
		verb := "is"
		if len(names) > 1 {
			verb = "are"
		}
		fmt.Fprintf(b, "You are %s. %s %s here with you.%s\n",
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
func renderAnchors(b *strings.Builder, v *AnchorsView, atPost bool, insideID sim.StructureID) {
	if v == nil {
		return
	}
	work := anchorPlace(v.WorkLabel, "your workplace")
	home := anchorPlace(v.HomeLabel, "your home")
	// You can't "head back to" the structure you're standing in — pointing the
	// model at the CURRENT structure's id is the LLM-214 no-op move it looped on
	// (Lewis Walker, inside the Walker Residence, calling move_to{residence} every
	// tick). When the actor is inside its own home/work, state that in-place (no move
	// id) and keep only the OTHER anchor as a reachable target.
	insideHome := v.HomeID != "" && insideID == v.HomeID
	insideWork := v.WorkID != "" && insideID == v.WorkID
	switch {
	case v.SamePlace:
		if insideHome { // SamePlace ⇒ insideHome and insideWork coincide
			b.WriteString("You're at your home and workplace.\n\n")
		} else {
			fmt.Fprintf(b, "Your home and your trade are both at %s (destination: %s) — you can head back there whenever you wish.\n\n", work, v.WorkID)
		}
	case v.WorkID != "" && v.HomeID != "":
		switch {
		case atPost:
			// On-shift AT its own post, the open "head to either whenever you wish"
			// invitation actively pulls an idle owner home (the Prudence shop↔house
			// oscillation, ZBBS-WORK-431). Keep both structure_ids — they are the
			// load-bearing move_to tokens (HOME-349) — but frame home as after-hours
			// rather than an open door; the at-post duty steer carries "stay put".
			fmt.Fprintf(b, "You keep your trade at %s (destination: %s); your home is at %s (destination: %s) — head home once your work is done.\n\n", work, v.WorkID, home, v.HomeID)
		case insideHome:
			// Standing at home: its id is a no-op move target, so state it in-place and
			// keep the workplace as the reachable anchor (LLM-214).
			fmt.Fprintf(b, "You're home. You keep your trade at %s (destination: %s) — you can head there whenever you wish.\n\n", work, v.WorkID)
		case insideWork:
			// Standing at the workplace off-shift (atPost handles on-shift above): state
			// it in-place and keep home as the reachable anchor (LLM-214).
			fmt.Fprintf(b, "You're at your workplace. Your home is at %s (destination: %s) — you can head home whenever you wish.\n\n", home, v.HomeID)
		default:
			fmt.Fprintf(b, "You keep your trade at %s (destination: %s), and your home is at %s (destination: %s) — you can head to either whenever you wish.\n\n", work, v.WorkID, home, v.HomeID)
		}
	case v.WorkID != "":
		if insideWork {
			b.WriteString("You're at your workplace.\n\n")
		} else {
			fmt.Fprintf(b, "You keep your trade at %s (destination: %s) — you can head back there whenever you wish.\n\n", work, v.WorkID)
		}
	case v.HomeID != "":
		if insideHome {
			b.WriteString("You're home.\n\n")
		} else {
			fmt.Fprintf(b, "Your home is at %s (destination: %s) — you can head back there whenever you wish.\n\n", home, v.HomeID)
		}
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
	//
	// LLM-337: dropped the explicit "wait here for customers rather than wandering
	// off" pin — a llama-era crutch that also suppressed legitimate restock trips
	// (a keeper leaving post to buy an off-circuit input, e.g. sage from the
	// apothecary). The stronger model doesn't need it: the mild "stay and look
	// after your work" steer plus the close time remain, and the away-from-post
	// arm still recovers a keeper that genuinely wandered.
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
			// stall). The default stabilizer's "stay and look after your work" steer
			// pulls against the "## Your bushes to harvest" cue's "walk out to your
			// bushes" — so swap it for a step-out-and-return line the two cues agree
			// on. Stepping out to one's OWN bushes to restock an empty shelf is tending
			// the trade; the post stays the home base she returns to. The to-work arm
			// defers a forage errand
			// (buildDutySteer), so she isn't yanked back once she sets off.
			if closeAt != "" {
				fmt.Fprintf(b, "It is your working hours and you are at your post (you close at %s), but your shelves are bare — step out to your own bushes to restock, then return to your post.\n\n", closeAt)
			} else {
				b.WriteString("It is your working hours and you are at your post, but your shelves are bare — step out to your own bushes to restock, then return to your post.\n\n")
			}
			return
		}
		if closeAt != "" {
			fmt.Fprintf(b, "It is your working hours and you are at your post — stay and look after your work; you close at %s.\n\n", closeAt)
		} else {
			b.WriteString("It is your working hours and you are at your post — stay and look after your work.\n\n")
		}
		return
	}
	if v.ToWork {
		fmt.Fprintf(b, "It is your working hours, yet you are away from your post — make your way to %s (destination: %s) now.\n\n",
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
			fmt.Fprintf(b, "Your working hours are over — close up and head to your rented room at %s (destination: %s) to rest for the night.", l, v.TargetID)
		} else {
			fmt.Fprintf(b, "Your working hours are over — close up and head to your rented room at the inn (destination: %s) to rest for the night.", v.TargetID)
		}
	default:
		if l := sanitizeInline(v.TargetLabel); l != "" {
			fmt.Fprintf(b, "Your working hours are over and you are not yet home — head home to %s (destination: %s) now.", l, v.TargetID)
		} else {
			fmt.Fprintf(b, "Your working hours are over and you are not yet home — head home (destination: %s) now.", v.TargetID)
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
	// LLM-335: a batch in the works pins the keeper to its post, so the invitation
	// yields to a quiet diegetic hold that agrees with the standing "you are making a
	// batch of X" line rather than contradicting it. Hung on "the batch" (singular) so
	// it reads for mass nouns (cheese) and count nouns (nails) alike, and names the
	// good the same way the in-flight line does ("a batch of Cheese"). No destination —
	// the steer is "stay put a little longer", and the invitation returns on the tick
	// the batch lands.
	if v.BatchHold {
		fmt.Fprintf(b, "Your day's work is nearly done, but the batch of %s still wants a few more minutes of your eye before you can call it a day.\n\n",
			sanitizeInline(v.BatchItemLabel))
		return
	}
	// LLM-345: the settled-in tier — the agent took the invitation and is standing in
	// the venue. The invitation has been acted on, so the cue stops offering places to
	// walk to and simply IS the room. No imperative and no "stay" instruction: the room
	// is the argument. The closing clause is the load-bearing one — it answers, in the
	// diegesis rather than as an instruction, the coda's "obligations before idle
	// matters", whose plain reading at seven in the evening sent the lingerer home.
	if v.SettledIn {
		fmt.Fprintf(b, "Your day's work is behind you, and here you are inside %s of an evening — the fire lit, the room warm. Whatever the morning asks of you can wait for the morning.\n\n",
			anchorPlace(v.VenueLabel, "the tavern"))
		return
	}
	venue := anchorPlace(v.VenueLabel, "the tavern")
	home := anchorPlace(v.HomeLabel, "your home")
	fmt.Fprintf(b, "Your day's work is done, and the tavern is open of an evening — you might make your way to %s (destination: %s) for company, pass a quiet evening at %s (destination: %s), or turn in for the night, as you please.\n\n",
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
	return sanitizeInline(descriptorLabel(m.DisplayName, m.Role, m.Acquainted)) + laborTiePhrase(m.SolicitTie) + laboringPhrase(m)
}

// laboringPhrase renders the LLM-231 busy annotation for a co-present member who is
// mid-job (fulfilling a Working LaborOffer). It names the employer when the subject
// can resolve them, otherwise omits the name. The wording signals the member is
// occupied and not a trade prospect right now — it deliberately does NOT say "won't
// respond" the way a sleeper is rendered: a laboring worker can still answer speech
// (LLM-230), it just shouldn't be pitched a sale. Empty for a non-laboring member.
func laboringPhrase(m HuddleMember) string {
	if !m.LaboringBystander {
		return ""
	}
	if m.LaboringForLabel != "" {
		return fmt.Sprintf(" (working a job for %s just now — not free to trade)", sanitizeInline(m.LaboringForLabel))
	}
	return " (working a job just now — not free to trade)"
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

// renderNarrativeState writes the "## Who you are" section for shared-VA
// actors. Content-gated: a nil view skips the section entirely so
// stateful and PC actors don't see an empty block. The contract
// matches the perception note — Render is kind-agnostic; Build is the
// one that gates on Kind.
//
// The body is the actor's AboutMe — the accreting first-person soul the
// per-actor narrative sweep synthesizes each day via the dream-sim-soul agent
// (LLM-199). Build gates the view on a non-empty AboutMe, so an actor whose
// soul hasn't been synthesized yet gets no section rather than a bare header
// (the original empty-block bug). SeedText/EvolvingSummary are not rendered —
// SeedText is never populated for shared VAs, and EvolvingSummary was the
// frozen, unconsolidated diary prose that primed the repeat-pitch loop
// (ZBBS-WORK-374); the identity-framed soul prompt is what avoids that loop.
func renderNarrativeState(b *strings.Builder, n *NarrativeStateView) {
	if n == nil {
		return
	}
	b.WriteString("## Who you are\n")
	if n.AboutMe != "" {
		b.WriteString(sanitizeInline(n.AboutMe))
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
	// LLM-343: the cue names ONE tool. It used to ask the seller to speak the
	// price and THEN call sell — but speak and sell are both tick-terminal, so
	// obeying that sentence in the order written ended the turn on the speech
	// and the offer was never posted. sell's `say` argument carries the words
	// now, making the price and the offer a single act.
	fmt.Fprintf(b, "%s %s here with you. If one of them names a specific good they want, or asks the price of a specific good, call sell — the named item and quantity in lines, your price in coins in amount, and the words you speak aloud in say, naming the coins outright rather than asking whether they would like to hear the price. The offer reaches their pay screen as you say it. Do not name a price with the speak tool: speaking ends your turn, and the offer would never be made. If they name several goods at once, give each its own line in the SAME offer under one total price. If they speak only in general — that they are hungry, ask what you have, or ask the cost of a meal without naming a dish — tell them what is for sale and let them choose; do not sell unless the buyer has named the good. Use target_buyer only for a named person you know; for a stranger or someone known only by trade, omit target_buyer to offer the whole room. The buyer is then free to take it or leave it.\n", who, verb)
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
// spoke and what was just asked, instead of re-pitching. Each line carries an
// interval stamp ("said (40s ago):") measured against renderedAt (LLM-217), so
// the model can tell rapid-fire churn from a normally paced exchange; the stamp
// is omitted when either clock is missing (hand-built payloads). Empty list
// skips the section.
func renderRecentConversation(b *strings.Builder, lines []UtteranceView, renderedAt time.Time) {
	if len(lines) == 0 {
		return
	}
	b.WriteString("## Recent conversation here\n")
	for _, u := range lines {
		text, _ := sanitizeText(u.Text, 0)
		said := "said"
		if stamp := agoPhrase(u.At, renderedAt); stamp != "" {
			said = "said (" + stamp + ")"
		}
		if u.IsSelf {
			fmt.Fprintf(b, "- You %s: %s\n", said, text)
			continue
		}
		name := sanitizeInline(u.SpeakerName)
		if name == "" {
			name = "someone"
		}
		fmt.Fprintf(b, "- %s %s: %s\n", name, said, text)
	}
	b.WriteString("\n")
}

// agoPhrase renders how long before now `at` happened, in the coarse buckets a
// prompt line needs: "just now" inside 15s, whole seconds under 90s, then whole
// minutes, then whole hours. Returns "" when either clock is zero (hand-built
// test payloads) — callers drop the stamp rather than show a bogus interval.
// LLM-217.
func agoPhrase(at, now time.Time) string {
	if at.IsZero() || now.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < 15*time.Second:
		// Covers negative deltas too: an At a hair after the snapshot's
		// publish instant (clock race) clamps to "just now" rather than
		// leaking a nonsense future interval.
		return "just now"
	case d < 90*time.Second:
		return fmt.Sprintf("%ds ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	default:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
}

// renderSelfActions writes the "## What you've recently done" section (LLM-217)
// — the subject's own recent committed actions, most-recent-first, each with an
// interval stamp. This is the self-action memory that lets a vacillating NPC
// SEE its own churn ("You arrived at the Tavern (just now). You left the Tavern
// (2m ago). You arrived at the Tavern (4m ago).") and break the loop — the
// information gap behind the live go-home ↔ seek-work oscillation. Phrasing
// mirrors the talk-panel narration (httpapi renderActionLogEntry) in second
// person; an entry whose type has no phrasing here should not have passed the
// build-side selfActionTrailTypes filter, but is skipped defensively. Empty
// list skips the section.
func renderSelfActions(b *strings.Builder, actions []SelfActionView, renderedAt time.Time) {
	if len(actions) == 0 {
		return
	}
	wrote := false
	for _, a := range actions {
		line := selfActionLine(a)
		if line == "" {
			continue
		}
		if !wrote {
			b.WriteString("## What you've recently done\n")
			b.WriteString("Most recent first.\n")
			wrote = true
		}
		if stamp := agoPhrase(a.At, renderedAt); stamp != "" {
			fmt.Fprintf(b, "- %s (%s)\n", line, stamp)
			continue
		}
		fmt.Fprintf(b, "- %s\n", line)
	}
	if wrote {
		b.WriteString("\n")
	}
}

// selfActionLine phrases one SelfActionView second-person, no trailing period
// (the interval stamp follows). Degrades on missing counterparty/amount the
// same way the talk-panel narration does. Returns "" for a type it can't
// phrase.
func selfActionLine(a SelfActionView) string {
	coins := func(n int) string {
		if n == 1 {
			return "1 coin"
		}
		return fmt.Sprintf("%d coins", n)
	}
	switch a.ActionType {
	case sim.ActionTypeSpoke:
		text, _ := sanitizeText(a.Text, 0)
		if text == "" {
			return ""
		}
		return fmt.Sprintf("You said: %q", text)
	case sim.ActionTypePaid:
		if a.CounterpartyName == "" {
			return "You made a payment"
		}
		line := "You paid " + sanitizeInline(a.CounterpartyName)
		if a.Amount > 0 {
			line += " " + coins(a.Amount)
		}
		if a.Text != "" {
			line += " for " + sanitizeInline(a.Text)
		}
		return line
	case sim.ActionTypeConsumed:
		if a.Text == "" {
			return ""
		}
		return "You consumed " + sanitizeInline(a.Text)
	case sim.ActionTypeDelivered:
		if a.Text == "" {
			return ""
		}
		line := "You delivered " + sanitizeInline(a.Text)
		if a.CounterpartyName != "" {
			line += " to " + sanitizeInline(a.CounterpartyName)
		}
		return line
	case sim.ActionTypeWalked:
		if a.Text == "" {
			return "You arrived"
		}
		return "You arrived at " + sim.WithDefiniteArticle(sanitizeInline(a.Text))
	case sim.ActionTypeDeparted:
		if a.Text == "" {
			return "You left"
		}
		return "You left " + sim.WithDefiniteArticle(sanitizeInline(a.Text))
	case sim.ActionTypeTookBreak:
		return "You stepped away to rest"
	case sim.ActionTypeLabored:
		switch {
		case a.Amount > 0 && a.CounterpartyName != "":
			return "You earned " + coins(a.Amount) + " working for " + sanitizeInline(a.CounterpartyName)
		case a.Amount > 0:
			return "You earned " + coins(a.Amount) + " for a job"
		case a.CounterpartyName != "":
			return "You finished a job for " + sanitizeInline(a.CounterpartyName)
		default:
			return "You finished a job"
		}
	case sim.ActionTypeSolicitedWork:
		switch {
		case a.Amount > 0 && a.CounterpartyName != "":
			return "You offered to work for " + sanitizeInline(a.CounterpartyName) + " for " + coins(a.Amount)
		case a.CounterpartyName != "":
			return "You offered to work for " + sanitizeInline(a.CounterpartyName)
		default:
			return "You offered to work for coin"
		}
	case sim.ActionTypeHired:
		switch {
		case a.Amount > 0 && a.CounterpartyName != "":
			return "You hired " + sanitizeInline(a.CounterpartyName) + " for " + coins(a.Amount)
		case a.CounterpartyName != "":
			return "You hired " + sanitizeInline(a.CounterpartyName)
		default:
			return "You took someone on"
		}
	default:
		return ""
	}
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
		// Two gates decide whether this order is deliverable NOW — if not, render
		// it passively and don't count it toward the deliver_order instruction:
		//   - Commission not yet forged (LLM-338): the seller took payment for a
		//     good it still has to make, so DeliverOrder gate 5 (stock) would bounce
		//     a deliver_order call. Steer to making it, not into a bounce loop.
		//   - Absent recipient (ZBBS-WORK-373): the recipient isn't in the seller's
		//     huddle, so gate 6 (co-presence) would reject the handover — never name
		//     the absent buyer as a chase target.
		switch {
		case o.AwaitingMake:
			b.WriteString(" — you've yet to make it")
		case len(o.AbsentRecipientNames) > 0:
			fmt.Fprintf(b, " — waiting for %s to return", sanitizeInline(strings.Join(o.AbsentRecipientNames, ", ")))
		}
		// The same DeliverableNow predicate the deliver_order tool-advertising gate
		// reads (handlers.gateTools), so the cue's instruction and the tool surface
		// together — never one without the other.
		if o.DeliverableNow() {
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

// renderWarrants renders the "since your last turn" section and fills in the
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
// instead of the generic "since your last turn" list, so they must not also
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

func renderPayOffers(b *strings.Builder, offers []sim.PayOfferWarrantReason, nameOf func(sim.ActorID) string, shortfalls map[sim.LedgerID]StockShortfall, roomAlreadySold map[sim.LedgerID]sim.OrderID) {
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
		// only, and only when it bites (buildPayOfferShortfalls carries an entry
		// only then, services excluded). LLM-303: fire at zero held too — "you hold
		// no nails" for a non-vendor offeree, not just a vendor short of some stock.
		if sf, short := shortfalls[o.LedgerID]; short {
			if sf.Held == 0 {
				fmt.Fprintf(b, " — you hold no %s", sf.Noun)
			} else {
				fmt.Fprintf(b, " — you hold only %d %s", sf.Held, item)
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
	// ONE tool, and the words ride on it (LLM-350). This cue used to read "Respond
	// first with accept_pay… Then also use speak", which no NPC could obey: the pay
	// responses and speak are all terminal-on-success, so whichever landed first
	// ended the tick and the other was skipped as post_terminal. Obeying the order
	// as written settled the sale in silence; obeying it literally — speaking first —
	// cost the seller the sale, because the offer went unanswered and expired.
	// The response no longer passes in silence, so the cue no longer says it does.
	// Mirrors the seller cue's sell(say=…) shape (LLM-343).
	b.WriteString("Respond with accept_pay, decline_pay, or counter_pay, passing the offer id as ledger_id and the words you speak aloud in say. Do not reply with the speak tool: speaking ends your turn, and the offer would go unanswered.\n")
}

// renderLaborOffers renders the pending-work-offer decision section for whoever
// must ANSWER: one line per offer carrying the labor_id (the load-bearing field
// the model must echo into accept_work/decline_work), the other party, the
// reward, and how long the job takes. Uncapped by design — labor offers are
// inherently few (bounded by co-present actors), and the section must always
// carry the labor_id whenever gateTools advertises the response tools (the
// discussion-109 invariant). LLM-26.
//
// Two directions share the section (LLM-346), keyed off LaborOfferView.SubjectIsWorker().
// When the subject is the employer (the zero value of EmployerInitiated), a worker has offered to do a
// job and the affordability steer applies — the subject would be the one paying.
// When the subject is the worker, an employer has asked them to lend a hand: no
// affordability steer (they cannot see the keeper's purse), no returning-helper
// recall (that memory is the employer's), and the pay is something they would
// RECEIVE, not spend.
func renderLaborOffers(b *strings.Builder, offers []LaborOfferView, employerCoins int, employerProduces bool, nameOf func(sim.ActorID) string) {
	if len(offers) == 0 {
		return
	}
	b.WriteString("## Work offers awaiting your decision\n")
	anyAffordable, anyUnaffordable := false, false
	for i, o := range offers {
		// The pay may be coins, goods the employer holds, or both (LLM-225) —
		// formatOfferPayment renders whichever legs are present ("5 coins",
		// "1 porridge", "1 porridge and 2 coins").
		if o.SubjectIsWorker() {
			fmt.Fprintf(b, "%d. %s has asked you to do a job for them — %s for about %s of work (offer id %d)\n",
				i+1, nameOf(o.Employer), formatOfferPayment(o.Reward, o.RewardItems), humanizeWorkMinutes(o.DurationMin), o.LaborID)
			anyAffordable = true // no coin gate on the worker's side — the pay comes to them
			continue
		}
		worker := nameOf(o.Worker)
		fmt.Fprintf(b, "%d. %s offers to do a job for you for %s — about %s of work (offer id %d)\n",
			i+1, worker, formatOfferPayment(o.Reward, o.RewardItems), humanizeWorkMinutes(o.DurationMin), o.LaborID)
		// LLM-228: the returning-helper recall. When this worker completed a paid
		// job for the employer within the memory window, name the past help so the
		// re-hire choice is informed experientially — not by an engine hire-value
		// pitch at the decision point (a pitch removed in #691). Plain recall, no
		// directive: it states the fact and leaves accept/decline to the model.
		// Only a producing keeper actually "got more done" from the help; a
		// non-producer gets the bare social beat so the line never claims output
		// that never happened.
		if o.HelpedBeforeRecently {
			if employerProduces {
				fmt.Fprintf(b, "You remember %s lending you a hand recently, and you got more done for it.\n", worker)
			} else {
				fmt.Fprintf(b, "You remember %s lending you a hand recently.\n", worker)
			}
		}
		// A reward the employer can't cover is a doomed accept: accept_work's
		// gate 8 would only flip the offer to failed_unavailable
		// (employerCanCoverLaborReward, labor_commands.go), so the model
		// "accepts" verbally and the deal dies in silence. Steer the employer
		// to decline WITH a spoken reason instead — carried in decline_work's own
		// `say` (LLM-350), not a second speak call, which being terminal would
		// have skipped the decline or been skipped by it. The two checks here
		// mirror gate 8's two legs exactly — Coins < Reward, and the
		// build-time MissingRewardItems holdings scan — so the cue and the
		// substrate never disagree. LLM-158; goods leg LLM-225.
		shortOnCoins := employerCoins < o.Reward
		missing := o.MissingRewardItems
		if shortOnCoins || len(missing) > 0 {
			anyUnaffordable = true
			switch {
			case shortOnCoins && len(missing) > 0:
				fmt.Fprintf(b, "You only have %s and do not hold the %s they ask to be paid in, so you cannot pay for this — call decline_work (offer id %d), telling them in say that you cannot pay what they ask.\n",
					coinsPhrase(employerCoins), formatOfferPayment(0, missing), o.LaborID)
			case len(missing) > 0:
				fmt.Fprintf(b, "You do not hold the %s they ask to be paid in, so you cannot pay for this — call decline_work (offer id %d), telling them in say that you cannot pay what they ask.\n",
					formatOfferPayment(0, missing), o.LaborID)
			default:
				fmt.Fprintf(b, "You only have %s, so you cannot pay for this — call decline_work (offer id %d), telling them in say that you have not enough coin to take them on.\n",
					coinsPhrase(employerCoins), o.LaborID)
			}
			continue
		}
		anyAffordable = true
	}
	// One tool, and the words ride on it — the same shape the pay decision section
	// uses, for the same reason (LLM-350): accept_work, decline_work and speak are
	// all terminal, so a cue naming two of them can only ever have one obeyed. When
	// SOME offers are unaffordable, scope the footer to the affordable ones so a
	// weak model can't apply a generic "accept_work or decline_work" to an offer
	// that was just steered to decline. Suppressed entirely when EVERY offer is
	// unaffordable — each carried its own decline steer above. LLM-158.
	switch {
	case anyAffordable && anyUnaffordable:
		b.WriteString("For an offer you can afford, respond with accept_work or decline_work, passing the offer id as labor_id and the words you speak aloud in say; decline_work the ones you cannot pay. Do not reply with the speak tool: speaking ends your turn, and the offer would go unanswered.\n")
	case anyAffordable:
		b.WriteString("Respond with accept_work or decline_work, passing the offer id as labor_id and the words you speak aloud in say. Do not reply with the speak tool: speaking ends your turn, and the offer would go unanswered.\n")
	}
}

// renderLaborSelfState renders the worker's own in-progress job as a self-state
// line (LLM-26) — who they're working for and roughly how much longer, with the
// nudge to stay with it. Placed in the self-state block (top) because it is
// point-in-time "what I'm doing right now." Content-gated on Laboring != nil.
//
// Off-post surface (LLM-268): when the worker has wandered off the post (OffPost),
// or her employer has left it (EmployerAway), the line becomes a directional cue —
// head back, or follow along — paired with the move_to gateTools re-grants for the
// same LaboringView predicate, so cue and tool can't drift. The employer-away
// (accompany) case takes precedence: if the employer has left, there is no held
// post to return to, so "head back" would be wrong.
func renderLaborSelfState(b *strings.Builder, laboring *LaboringView, nameOf func(sim.ActorID) string, renderedAt time.Time) {
	if laboring == nil {
		return
	}
	employer := nameOf(laboring.Employer)
	post := "the workplace"
	if laboring.PostLabel != "" {
		post = sim.WithDefiniteArticle(sanitizeInline(laboring.PostLabel))
	}
	switch {
	case laboring.EmployerAway:
		if laboring.EmployerPlace != "" {
			fmt.Fprintf(b, "You are in the middle of a job for %s, but they have left %s and gone to %s. If they want you along, follow after them with move_to; otherwise carry on with the work. You are paid when the job is done.\n",
				employer, post, sim.WithDefiniteArticle(sanitizeInline(laboring.EmployerPlace)))
			return
		}
		fmt.Fprintf(b, "You are in the middle of a job for %s, but they have stepped away from %s. If they want you along, follow after them with move_to; otherwise carry on with the work. You are paid when the job is done.\n",
			employer, post)
		return
	case laboring.OffPost:
		fmt.Fprintf(b, "You took on a job for %s at %s, but you have wandered off from it — and you are still on the clock. Head back there with move_to and get on with the work; you are paid when it is done.\n",
			employer, post)
		return
	}
	mins := minutesUntil(laboring.Until, renderedAt)
	if mins <= 0 {
		fmt.Fprintf(b, "You are finishing a job for %s — the work is just about done; you'll be paid as you finish.\n", employer)
		return
	}
	fmt.Fprintf(b, "You are working a job for %s — about %s of work left. Stay with it until it's done; you are paid when you finish.\n",
		employer, humanizeWorkMinutes(mins))
}

// renderLaborEnRoute renders the relocation self-state for a worker who has
// accepted a job but not yet started it (LLM-229): they are on their way to the
// employer's workplace, or waiting there for the owner to show. It keeps the
// tickable relocating worker on task — go to the post and get to work — rather
// than wandering off or soliciting a second job. No reward is named; the work
// window hasn't started. Placed in the self-state block, mutually exclusive with
// renderLaborSelfState's in-progress line. Content-gated on LaborEnRoute != nil.
func renderLaborEnRoute(b *strings.Builder, enRoute *LaborEnRouteView, nameOf func(sim.ActorID) string) {
	if enRoute == nil {
		return
	}
	employer := nameOf(enRoute.Employer)
	if enRoute.Waiting {
		fmt.Fprintf(b, "You've taken on a job for %s and you're at their workplace waiting for them to arrive so you can start — stay put until they do; you are paid once the work is done.\n", employer)
		return
	}
	fmt.Fprintf(b, "You've taken on a job for %s — make your way to their workplace and get to work once you are there (if they aren't in yet, wait for them); you are paid once the work is done.\n", employer)
}

// renderWorkersForMe renders the employer-side active-labor cue (LLM-202): the
// workers currently on a job for the subject, with roughly how much longer and
// what they're owed on completion, plus a steer not to double up. The
// employer-side mirror of renderLaborSelfState's worker line — where the worker
// gets "you are working for X," the employer gets "X is working for you."
// Without it the employer sees only the pending-decision view (renderLaborOffers)
// and has no signal an accepted job is already underway, so they re-hire a second
// body or pay by hand for work already covered (the live John Ellis re-hire of
// Patience mid-way through Silence's contract). A standing situational line in the
// self-state block; one line per worker (an employer can have several), then one
// shared steer. Content-gated on a non-empty WorkersForMe.
func renderWorkersForMe(b *strings.Builder, workers []WorkerForMeView, nameOf func(sim.ActorID) string, renderedAt time.Time) {
	if len(workers) == 0 {
		return
	}
	for _, wkr := range workers {
		worker := nameOf(wkr.Worker)
		// The owed pay may be coins, goods, or both (LLM-225).
		payment := formatOfferPayment(wkr.Reward, wkr.RewardItems)
		mins := minutesUntil(wkr.Until, renderedAt)
		if mins <= 0 {
			fmt.Fprintf(b, "%s is finishing a job for you — almost done; %s owed as they finish.\n",
				worker, payment)
			continue
		}
		fmt.Fprintf(b, "%s is working a job for you — about %s left; %s owed when it's done.\n",
			worker, humanizeWorkMinutes(mins), payment)
	}
	// Trailing blank line so the following section keeps its separator, matching
	// the self-state-gap convention (renderNarrativeState / renderVendorOperating).
	b.WriteString("That work is already covered and the pay settles on its own when it's finished — don't hire someone else for it or pay again by hand.\n\n")
}

// renderPendingLaborOfferOut renders the subject's OWN outgoing labor offer that
// is still awaiting the other party's answer (LLM-164) — the awaiting-acceptance
// mirror of renderLaborSelfState's in-progress line. Whoever minted an offer has
// no Working job yet, so this is the only labor self-state they get while waiting;
// it names what's on the table and says plainly to sit tight, the anchor that
// keeps the weak model from flailing into an unrelated tool under the quiet
// backstop / "choose one action" pressure. Content-gated on PendingLaborOfferOut.
//
// Both mints get a line (LLM-346): the worker who solicited waits on the employer,
// the employer who offered work waits on the worker. Without the second one a
// keeper who has just asked someone to lend a hand has no anchor at all, and the
// quiet backstop pushes her to ask again.
func renderPendingLaborOfferOut(b *strings.Builder, offer *PendingLaborOfferOutView, nameOf func(sim.ActorID) string) {
	if offer == nil {
		return
	}
	// The pay may be coins, goods, or both (LLM-225).
	payment := formatOfferPayment(offer.Reward, offer.RewardItems)
	duration := humanizeWorkMinutes(offer.DurationMin)
	if offer.SubjectIsEmployer() {
		fmt.Fprintf(b, "You've asked %s to work for you for %s (about %s) — your offer stands and it is their move now. There's nothing more to do on it; wait for their answer, say a brief word if you like, then call done().\n",
			nameOf(offer.Worker), payment, duration)
		return
	}
	fmt.Fprintf(b, "You've offered to work for %s for %s (about %s) — your offer stands and it is their move now. There's nothing more to do on it; wait for their answer, say a brief word if you like, then call done().\n",
		nameOf(offer.Employer), payment, duration)
}

// renderLaborAffordance renders the free-worker option cue (LLM-26): the
// subject takes work for pay and has someone here to offer it to. Content-gated
// on CanSolicitWork, the same signal that gates the solicit_work tool.
func renderLaborAffordance(b *strings.Builder, canSolicit bool) {
	if !canSolicit {
		return
	}
	b.WriteString("You take work for pay. If someone here outside your own household or trade has a task you could do and you want the pay, offer your labor with solicit_work — name them, the pay you want (coins, goods they hold such as a meal, or both), and roughly how long the job will take.\n")
}

// renderOfferWorkAffordance renders the hiring-side option cue (LLM-346): people
// are here who take work for pay, and the subject may ask one of them to lend a
// hand. Content-gated on a non-empty HireableWorkers — the same slice that gates
// the offer_work tool, so the cue and the tool surface together or not at all
// (discussion-109).
//
// It NAMES them because nothing else in the prompt does. Whether a villager takes
// odd jobs is not visible from the co-presence line, and offer_work resolves its
// target by exact display name — a keeper left to guess spends her turn being told
// the person she asked is not a worker.
//
// It also warns off the terminal speak, for the reason LLM-343 folded `say` into
// sell: offer_work and speak both end the tick, so a keeper who voices the request
// first never reaches the tool, and the offer she just made aloud does not exist.
func renderOfferWorkAffordance(b *strings.Builder, workers []sim.ActorID, nameOf func(sim.ActorID) string) {
	if len(workers) == 0 {
		return
	}
	names := make([]string, 0, len(workers))
	for _, id := range workers {
		names = append(names, nameOf(id))
	}
	takes := "takes"
	if len(names) > 1 {
		takes = "take"
	}
	fmt.Fprintf(b, "%s %s work for pay and could lend you a hand. If you have a task worth paying for, ask with offer_work — name them, the pay you will hand over when the work is done (coins, goods you hold such as a meal, or both), and roughly how long the job will take. Put what you say to them in offer_work's `say`, in your own voice; do NOT ask with speak first, because speaking ends your turn and the offer would never reach them.\n",
		joinNames(names), takes)
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
				// LLM-188: when the needs-clamp ate fewer than purchased
				// (consumableUnits, ZBBS-WORK-391), say what was eaten vs kept
				// so this line reconciles with the carried-inventory count
				// rather than asserting all Qty were consumed on the spot — the
				// contradiction that made buyers confabulate a short-count. The
				// 0 < KeptUnits < Qty guard holds for the self-consume case (the
				// clamp floors at 1, so kept <= Qty-1); a rare group-order split
				// that breaks the invariant falls back to the plain line.
				if o.KeptUnits > 0 && o.KeptUnits < o.Qty {
					gotIt = fmt.Sprintf("you ate %d on the spot and kept the other %d", o.Qty-o.KeptUnits, o.KeptUnits)
				}
			}
			fmt.Fprintf(b, "%d. %s accepted your offer — you paid %s for %d %s; %s. That deal is done — don't offer for it again (offer id %d).\n",
				i+1, seller, payment, o.Qty, item, gotIt, o.LedgerID)
			continue
		}
		// LLM-296: name what was OFFERED (not just the want-item) so two declines
		// aren't byte-identical — the thin line gave the standing "never repeat
		// what you said" instruction nothing to bind to, and the model re-posted
		// the same bundle. Where the engine knows the seller is short the bought
		// kind, append it as the informed "why" the deal closed (the buyer's
		// mirror of the seller-side "you hold only N"); only when it bites.
		offered := formatOfferPayment(o.Amount, o.PayItems)
		reason := "it's closed, so stop waiting on it"
		if o.SellerStocks && o.Qty > o.SellerStock {
			// LLM-303: at zero held, name it "they hold no nails" (plural noun)
			// rather than the awkward "only 0 nail"; above zero keeps the LLM-296
			// "they hold only N <kind>" form on the raw kind key.
			if o.SellerStock == 0 {
				reason = fmt.Sprintf("they hold no %s, so it's closed; stop waiting on it", o.SellerStockNoun)
			} else {
				reason = fmt.Sprintf("they hold only %d %s, so it's closed; stop waiting on it", o.SellerStock, item)
			}
		}
		fmt.Fprintf(b, "%d. Your offer of %s to %s for %d %s didn't go through — %s (offer id %d).\n",
			i+1, offered, seller, o.Qty, item, reason, o.LedgerID)
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
// tick but must NOT emit a generic "## Since your last turn" line — rendering one
// produced the vague "something happened nearby" catch-all (ZBBS-WORK-407).
// These warrants are still consumed to wake the actor (that is how it ticks to
// read the rest of the prompt); they just have no standalone event line. Most
// carry their content in a dedicated section; the bare operator nudge has no
// in-world content at all:
//   - pay_offer   -> "## Offers awaiting your decision" (PayOffersForMe)
//   - labor_offer -> "## Work offers awaiting your decision" (LaborOffersForMe,
//     LLM-187). The employer is woken to accept_work / decline_work; its
//     content is the labor decision section, so it must not also fabricate a
//     bare "something happened" line.
//   - shift_duty -> the return-to-post steer (DutySteer)
//   - admin      -> a bare operator force-tick (umbilical /nudge with no
//     message). Not an in-world event, so it falls to the routine
//     check-in line rather than a fabricated "something happened"
//     (ZBBS-WORK-418). A nudge WITH a message keeps its felt-
//     impulse line (WarrantKindImpulse) — that is real content.
func isSectionSurfacedKind(k sim.WarrantKind) bool {
	switch k {
	case sim.WarrantKindPayOffer, sim.WarrantKindLaborOffer, sim.WarrantKindShiftDuty, sim.WarrantKindAdmin:
		return true
	default:
		return false
	}
}

func renderWarrants(b *strings.Builder, warrants []sim.WarrantMeta, nameOf func(sim.ActorID) string, placeNameOf func(string) string, placeKeeperOf func(string) string, eatHereKind func(sim.ItemKind) bool, buyRedundancy func(sim.ItemKind) (produced, atCap bool), renderedAt time.Time, cfg RenderConfig, out *RenderedPrompt) {
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
	// Same nil-safety for the LLM-284 keeper-possessive callback: a nil here must
	// degrade to "no keeper", so an arrival line keeps its plain, articled form.
	if placeKeeperOf == nil {
		placeKeeperOf = func(string) string { return "" }
	}
	// ZBBS-WORK-407: drop warrants already surfaced by a dedicated section so they
	// don't double-render as the vague "something happened nearby" catch-all. They
	// still WAKE the actor (the reactor consumed them — that's how it ticks to read
	// the section); they just have no standalone "since your last turn" line. Filter
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
	// "Since your last turn", not "What just happened" (LLM-316): a carried-forward,
	// shelve-delayed, or slept-through warrant can be minutes-to-hours old by the
	// time it renders, and the batch semantics ARE "what accumulated since you last
	// acted" — the header shouldn't promise a recency the queue can't guarantee.
	// The per-line agoPhrase stamp below carries the actual staleness.
	b.WriteString("## Since your last turn\n")
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
		line, truncated := renderWarrantLine(i+1, w, nameOf, placeNameOf, placeKeeperOf, eatHereKind, buyRedundancy, cfg.MaxBytesPerWarrant)
		// Interval-stamp each signal against the render clock (LLM-316), the
		// LLM-217 treatment the conversation ring and self-action trail already
		// get: a carried-forward or shelve-delayed warrant renders honestly as
		// "(4m ago)" instead of masquerading as fresh. agoPhrase returns "" for
		// zero clocks (hand-built payloads / unstamped metas) — no stamp then.
		if stamp := agoPhrase(w.OccurredAt, renderedAt); stamp != "" {
			line = strings.TrimSuffix(line, "\n") + " (" + stamp + ")\n"
		}
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
func renderWarrantLine(n int, w sim.WarrantMeta, nameOf func(sim.ActorID) string, placeNameOf func(string) string, placeKeeperOf func(string) string, eatHereKind func(sim.ItemKind) bool, buyRedundancy func(sim.ItemKind) (produced, atCap bool), maxTextBytes int) (string, bool) {
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
	case sim.DwellEndedWarrantReason:
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.DwellTickAppliedWarrantReason:
		// ZBBS-WORK-407: the per-tick beat used to be suppressed (fell through to
		// the vague "something happened" fallback) because it fired every minute.
		// The wake is now cadenced to the red-tier boundary (handlers/dwell_reactor.go),
		// so this fires at most once per dwell — render its felt line like its
		// DwellEnded sibling.
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
	case sim.ReturnToPostWarrantReason:
		// LLM-268: the felt pull that wakes a laboring worker who has wandered off
		// the post. Generic — the actionable specifics (which post, whose job, and
		// the move_to to get there) render from her LaboringView self-state
		// (renderLaborSelfState), the same predicate that re-grants her move_to, so
		// this line and the tool can't drift. Mirrors the SeekWork felt-impulse line.
		return fmt.Sprintf("%d. It weighs on you that you have drifted away from the paid job you are in the middle of — you should get back to it.\n", n), false
	case sim.ArrivalWarrantReason:
		return renderArrivalWarrantLine(n, nameOf(w.TriggerActorID), r, placeNameOf, placeKeeperOf), false
	case sim.NeedThresholdWarrantReason:
		return renderNeedNudgeLine(n, r.Need), false
	case sim.TendNeedWarrantReason:
		// LLM-276: the gentle "you've grown peckish and have the means to see to it"
		// pull for a workless idle worker, stamped by the seek-work backstop in place
		// of the go-earn impulse. Generic felt line — the actionable targets (what to
		// eat/drink and where) render from the "## What you can eat or drink" section
		// and the need-redirect steer, both keyed off this same warrant.
		return renderTendNeedLine(n, r.Need), false
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
		// LLM-116/LLM-319: nothing is in the works at the actor's post — the
		// "## Your trade" cue carries the scene + the produce tool; this line is
		// just the "why you ticked" beat, like the idle-backstop / need-nudge
		// lines. Deliberately not an instruction to produce: whether to make
		// more is the decision the tick exists to grant.
		return fmt.Sprintf("%d. Your thoughts turn to your trade — nothing is in the works right now.\n", n), false
	case sim.ProductionDoneWarrantReason:
		// LLM-319: a production cycle landed its batch — pre-rendered at the
		// subscriber, same narration-line path as the source-activity beat.
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.StallRepairWarrantReason:
		// LLM-118 (generalized LLM-247): the business just wore through the repair
		// threshold. At the business the "## Your business" cue carries the nail
		// count + buy-from-the-smith steer; this is the wake-from-anywhere nudge to
		// go tend it. The place name resolves the worn business (structure/object);
		// falls back to a generic noun if unnamed.
		place := placeNameOf(string(r.StallID))
		if place == "" {
			place = "place of business"
		}
		return fmt.Sprintf("%d. Your %s has worn from use and needs mending — go to it and repair it (you'll need nails; the smith sells them).\n", n, place), false
	case sim.StallRepairHiredWarrantReason:
		// LLM-271: hired-worker twin. Same wake-to-mend nudge, framed as the
		// employer's premises the worker was taken on to help with — the "## The
		// business you're working at" cue carries the nail count + repair steer.
		place := placeNameOf(string(r.StallID))
		if place == "" {
			return fmt.Sprintf("%d. The business you're working at has worn from use and needs mending — you can repair it (you'll need nails; the smith sells them).\n", n), false
		}
		return fmt.Sprintf("%d. The %s you're working at has worn from use and needs mending — you can repair it (you'll need nails; the smith sells them).\n", n, place), false
	case sim.FarmUpkeepWarrantReason:
		// LLM-215: the season wore out the farm's upkeep shovels. The "## Farm upkeep"
		// cue carries the shovel count + buy-from-the-blacksmith steer; this is the
		// wake-from-anywhere "why you ticked" nudge, like production-choice / seek-work.
		return fmt.Sprintf("%d. Your farm tools are worn from the season's work — buy fresh shovels from the blacksmith.\n", n), false
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
//
// When the arrived-at structure has a keeper (someone other than the mover works
// there), the place reads as that keeper's possessive — "<keeper>'s <place>",
// article-free — so the model sees whose shop it walked into and hosts as a
// guest instead of greeting the keeper as if it owned the place (LLM-284). The
// keeper name is a proper noun, so it takes no definite article; sim.Possessive
// forms the case (a name ending in "s" gets a bare apostrophe), and only the
// plain, keeperless form runs through WithDefiniteArticle.
func renderArrivalWarrantLine(n int, who string, r sim.ArrivalWarrantReason, placeNameOf func(string) string, placeKeeperOf func(string) string) string {
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
	// A keeper only ever resolves for the structure destination (objects have
	// none), so key the possessive off AtStructureID regardless of which id named
	// the place above.
	if keeper := placeKeeperOf(string(r.AtStructureID)); keeper != "" {
		return fmt.Sprintf("%d. %s arrived at %s %s.\n", n, subject, sim.Possessive(keeper), place)
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

// renderTendNeedLine renders a tend-need warrant (LLM-276) as a gentle felt pull to
// eat or drink before the need grows sharp — the seek-work backstop's redirect for a
// workless idle worker who has grown hungry/thirsty and can resolve it now. Softer
// than renderNeedNudgeLine (which is the red-tier "pressing on you" distress beat):
// this fires below the red-line, so it reads as foresight, not urgency. The
// actionable specifics (what to eat/drink, where) render from the satiation section
// + the need-redirect steer. Falls back to a generic pull for an unrecognized need.
func renderTendNeedLine(n int, need sim.NeedKey) string {
	switch need {
	case "hunger":
		return fmt.Sprintf("%d. Hunger is beginning to tug at you, and you have the means to see to it — better to get something to eat now than to let it grow sharp.\n", n)
	case "thirst":
		return fmt.Sprintf("%d. A thirst is creeping up on you, and you have the means to see to it — better to get a drink now than to let it grow sharp.\n", n)
	default:
		return fmt.Sprintf("%d. A need is beginning to press, and you have the means to see to it now.\n", n)
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
