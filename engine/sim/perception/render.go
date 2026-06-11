package perception

import (
	"fmt"
	"strings"
	"time"

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
	// Text is the DURABLE turn — felt-state (## You) + the "what just
	// happened" events. This is what the chat adapter persists and replays as
	// conversation history (lean sim-history, ZBBS-WORK-364).
	Text string

	// EphemeralText is per-tick decision-support that must NOT persist into
	// history: identity, surroundings, affordances (rest/food/lodging), owed
	// orders, pay offers, and the act-now coda. The adapter attaches it to the
	// CURRENT turn only (memory-api: /chat/send ephemeral_context). Splitting
	// it out keeps replayed history lean — the static furniture can't pile up
	// once per historical tick.
	EphemeralText string

	// ContinuationText is the lean post-speak decision body the harness swaps in
	// after the actor's first committed speak this tick (ZBBS-HOME-411). It drops
	// the actionable affordances carried in EphemeralText — the inn/food/rest cues
	// and the act-now coda that prime a re-pitch — and presents a stop-biased
	// decision instead. Round 1 sends EphemeralText (the model may act); once the
	// actor has spoken, the recency-dominant text biases it to done() rather than
	// re-offer what it just said. Pairs with HOME-402's speak cap (the backstop)
	// and the WORK-375 per-speak tool-result steer.
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
	"You have already spoken this turn — let others respond. Call done() unless a prior tool result needs a word, you owe a distinct answer someone asked of you, or a needed non-speaking action remains (such as moving or resting). Do not greet again, re-pitch, or rephrase what you have already said.\n"

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
	// Two streams (lean sim-history, ZBBS-WORK-364). `durable` is the felt-
	// state (## You) + the "what just happened" events — what the NPC should
	// REMEMBER; the chat adapter persists and replays it as conversation
	// history. `ephemeral` is per-tick decision-support (identity, surroundings,
	// affordances, owed orders, pay offers, the act-now coda) the adapter
	// attaches to the CURRENT turn only and never persists, so the static
	// furniture can't accumulate once per historical tick. The split is by
	// SECTION — each renderer below is routed to one stream.
	var durable strings.Builder
	var ephemeral strings.Builder

	// Durable: felt-state.
	durable.WriteString("# Your turn\n\n")
	renderActor(&durable, p.Actor)

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

	// Pay offers are pulled out of the generic warrant list and rendered as
	// an actionable decision section (renderPayOffers) so the seller gets the
	// ledger_id it must echo into accept_pay/decline_pay/counter_pay. The same
	// PayOfferWarrants(p) predicate drives the handlers tool-gate (gateTools),
	// so the rendered offer and the advertised response tools cannot drift.
	// Rendering them in a dedicated, uncapped section (rather than as a capped
	// warrant line) guarantees the ledger_id is present whenever the tools are
	// advertised.
	payOffers := PayOfferWarrants(p)

	// Ephemeral: identity, surroundings, anchors, steers, relationships, the
	// offers awaiting this actor's decision, owed orders, recovery/satiation/
	// restock/lodging affordances, summons, scene.
	renderNarrativeState(&ephemeral, p.NarrativeState)
	renderVendorOperating(&ephemeral, p.AtOwnBusiness)
	renderSurroundings(&ephemeral, p.Surroundings)
	renderAnchors(&ephemeral, p.Anchors)
	renderDutySteer(&ephemeral, p.DutySteer)
	renderRelationships(&ephemeral, p.Relationships)
	renderRecentConversation(&ephemeral, p.RecentConversation)
	// The decision section renders ABOVE the affordance dumps (it used to land
	// after them): a buyer's coin on the table is the seller's most actionable
	// fact, and burying it under eat/drink and room-to-let cues let the
	// seller's own mild needs outrank a waiting customer for whole minutes
	// (conversation hud-6c849d…, ZBBS-HOME-424). renderTriage reinforces the
	// same priority at the decision point.
	renderPayOffers(&ephemeral, payOffers, nameOf)
	renderOfferableCustomers(&ephemeral, p.OfferableCustomers)
	renderPendingDeliveriesFromMe(&ephemeral, p.PendingDeliveriesFromMe, p.LocalDateUTC)
	renderPendingDeliveriesToMe(&ephemeral, p.PendingDeliveriesToMe, p.LocalDateUTC)
	renderPendingOffersFromMe(&ephemeral, p.PendingOffersFromMe)
	renderRecoveryOptions(&ephemeral, p.RecoveryOptions)
	renderSatiation(&ephemeral, p.Satiation)
	renderRestocking(&ephemeral, p.Restocking)
	renderLodging(&ephemeral, p.Lodging)
	renderKeeperLodging(&ephemeral, p.KeeperLodging)
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
		renderWarrants(&durable, warrants, nameOf, placeNameOf, cfg, &out)
	}

	// Ephemeral: the turn-state nudge, the act-now coda, and the rest-first steer
	// are instructions for THIS tick, not facts to remember. The turn-line lands
	// before the coda so the coda's "weigh everything above" sees it; the coda
	// itself swaps to a wait-framing when the actor is awaiting a reply.
	renderTurnState(&ephemeral, p.TurnState)
	renderTriage(&ephemeral, p.Actor.Needs, p.Actor.NeedThresholds, p.TurnState.AwaitingReply(), len(payOffers) > 0)

	out.Text = durable.String()
	out.EphemeralText = ephemeral.String()
	out.ContinuationText = continuationDecisionText
	return out
}

// renderTurnState writes the conversation turn-state lines (ZBBS-WORK-370): who
// the actor owes a reply to, and who it is awaiting a reply from. The awaiting
// line is the cadence fix — it tells the model it has already spoken and must
// not re-pitch a peer who hasn't answered; renderTriage's coda swap reinforces
// it. Both lists are acquaintance-gated labels resolved at build time. Emits
// nothing when there is no pending turn (the common case).
func renderTurnState(b *strings.Builder, ts TurnStateView) {
	for _, name := range ts.OwedReplyTo {
		fmt.Fprintf(b, "%s is waiting for your reply.\n", sanitizeInline(name))
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
func renderTriage(b *strings.Builder, needs map[sim.NeedKey]int, thresholds sim.NeedThresholds, awaitingReply bool, hasPayOffers bool) {
	// A buyer's offer awaiting this actor's answer outranks everything below —
	// including the actor's own felt needs, which the coda's "pressing needs"
	// phrasing otherwise licenses to win. Without this, a starving seller read
	// his own hunger as the obligation and let a customer's coin sit for whole
	// minutes (conversation hud-6c849d…, ZBBS-HOME-424).
	if hasPayOffers {
		b.WriteString("A buyer's offer awaits your answer — settle it first with accept_pay, decline_pay, or counter_pay, before tending to your own needs.\n")
	}
	if awaitingReply {
		// Turn-state coda (ZBBS-WORK-370): the actor has spoken and is awaiting a
		// reply. The default "choose one thing and do it" imperative is exactly
		// what drove the re-pitch loop (live-trace finding #2) — it commands an
		// action every tick even when the right move is to wait. Swap it for a
		// wait-permitting framing: real needs/obligations above still license an
		// action, but "nothing new to add" now resolves to done() instead of a
		// repeated pitch.
		b.WriteString("Weigh everything above. If the most pressing matter is simply awaiting someone's reply, do not repeat yourself — wait and call done(). Otherwise act on what matters most: obligations to others and pressing needs come before idle matters.\n")
	} else {
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
	fmt.Fprintf(b, "Coins in your purse: %d.\n", a.Coins)
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
			fmt.Fprintf(b, "%s (x%d)", sanitizeInline(it.Label), it.Qty)
		}
		b.WriteString(".\n")
	}
	// In-progress activity reads as felt self-state. A meal/rest/walk already
	// under way is surfaced so a tick firing mid-activity doesn't re-pick a
	// goal from scratch (the dwell-credit/in-flight-move parking fix). These
	// also cover the resting/walking macro-states, so the bare state line only
	// fires when nothing else already conveys what the actor is doing.
	activity := false
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

// renderFeltNeeds turns the raw need values into felt language in the fixed
// hunger→thirst→tiredness order. Needs below the awareness floor stay silent.
// Red/peak needs lead with an "Address now:" imperative — v1's 2026-05-02 fix
// that made NPCs act on distress instead of reading a flat integer they
// couldn't calibrate (the original "needs: hunger=24" dump gave the model no
// sense that 24 is peak starvation). Returns "" when nothing is surfaced.
// ZBBS-HOME-339.
func renderFeltNeeds(needs map[sim.NeedKey]int, thresholds sim.NeedThresholds) string {
	if len(needs) == 0 {
		return ""
	}
	var felt, pressing []string
	for _, key := range []sim.NeedKey{"hunger", "thirst", "tiredness"} {
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
		return fmt.Sprintf("walking to enter %s", sanitizeInline(dest))
	}
	return fmt.Sprintf("walking to %s", sanitizeInline(dest))
}

// renderActiveDwellCredit produces the felt-language self-perception
// line for one in-progress dwell credit ("eating stew at the tavern,
// ~14 minutes remaining"). The load-bearing prompt line that keeps
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
		minutes := (*c.RemainingTicks) * c.PeriodMinutes
		subject = fmt.Sprintf("%s, ~%d minute(s) remaining", subject, minutes)
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
		location = "inside " + sanitizeInline(name)
	case s.NearbyStructureName != "":
		// Standing at a structure's loiter slot while outdoors — a keeper at
		// their own stall, a customer outside a shop. Names where they are so
		// the model doesn't read raw coordinates and re-walk to a place it is
		// already standing at.
		location = "outdoors by " + sanitizeInline(s.NearbyStructureName)
	default:
		location = "outdoors"
	}
	if len(s.HuddleMembers) > 0 {
		// A huddle is a conversational cluster, so "with" names who the actor
		// is gathered with — the speak tool reaches exactly these people.
		fmt.Fprintf(b, "You are %s, with %s.\n", location, joinHuddleMembers(s.HuddleMembers))
	} else {
		fmt.Fprintf(b, "You are %s.\n", location)
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
		fmt.Fprintf(b, "You're at %s — you can gather %s here.\n",
			source, sanitizeInline(string(s.GatherableItem)))
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
func renderAnchors(b *strings.Builder, v *AnchorsView) {
	if v == nil {
		return
	}
	work := anchorPlace(v.WorkLabel, "your workplace")
	home := anchorPlace(v.HomeLabel, "your home")
	switch {
	case v.SamePlace:
		fmt.Fprintf(b, "Your home and your trade are both at %s (structure_id: %s) — you can head back there whenever you wish.\n\n", work, v.WorkID)
	case v.WorkID != "" && v.HomeID != "":
		fmt.Fprintf(b, "You keep your trade at %s (structure_id: %s), and your home is at %s (structure_id: %s) — you can head to either whenever you wish.\n\n", work, v.WorkID, home, v.HomeID)
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
	return sanitizeInline(label)
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
	return sanitizeInline(descriptorLabel(m.DisplayName, m.Role, m.Acquainted))
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
// than in a detached, far-away system preamble. Gated on AtOwnBusiness — a
// businessowner physically at their own post (ZBBS-WORK-385) — so it reaches
// vendors (innkeeper, farmers, shopkeepers) tending their business, but not
// visitors, stateful NPCs, or a keeper off-post in someone else's place. The
// scoped wording replaces "always be closing" with "a greeting is not a sale".
// ZBBS-HOME-385 restores the "tend to your trade" working framing that the
// WORK-374 port dropped (the producers were drifting off-post with nothing to
// do); kept generic ("your trade", not "your stall") since a vendor may keep a
// stall or a building.
func renderVendorOperating(b *strings.Builder, atOwnBusiness bool) {
	if !atOwnBusiness {
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
		if s := sanitizeInline(g); s != "" {
			goods = append(goods, s)
		}
	}
	if len(goods) == 0 {
		// Defensive: Build filters raw empty labels, but a label could sanitize
		// down to empty — render nothing rather than an empty goods list.
		return
	}
	fmt.Fprintf(b, "%s %s here with you. If interest is shown in your wares, name a fair price and offer it — call scene_quote with the item, the quantity, and your price in coins. Use target_buyer only for a named person you know; for a stranger or someone known only by trade, omit target_buyer to offer the whole room. The buyer is then free to take it or leave it.\n", who, verb)
	// ZBBS-HOME-407: the barter counterpart to the coin-sale cue above. When a
	// customer is carrying goods the keeper would rather have than coin, point
	// at offer_trade so a goods-for-goods swap has a legible execution path
	// instead of dissolving into a verbal agreement nothing commits.
	fmt.Fprintf(b, "If one of them is carrying something you would rather have than coin, you can instead propose a direct trade — call offer_trade with the goods you will give and what you want from them. They are then free to accept, decline, or counter.\n")
	fmt.Fprintf(b, "Your goods to sell: %s.\n\n", strings.Join(goods, ", "))
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
func renderPendingDeliveriesFromMe(b *strings.Builder, orders []OrderView, today time.Time) {
	if len(orders) == 0 {
		return
	}
	if today.IsZero() {
		// Hand-built payload with no world clock — fall back to the host UTC
		// day. A running engine always supplies Payload.LocalDateUTC.
		today = startOfUTCDay(time.Now())
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
	renderOrdersReadyToHandOver(b, ready)
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
func renderOrdersReadyToHandOver(b *strings.Builder, orders []OrderView) {
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
		if clause, ok := expiryClause(o.ExpiresAt, time.Now()); ok {
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
func renderPendingDeliveriesToMe(b *strings.Builder, orders []OrderView, today time.Time) {
	if len(orders) == 0 {
		return
	}
	if today.IsZero() {
		today = startOfUTCDay(time.Now())
	}
	var waiting, overdue []OrderView
	for _, o := range orders {
		if !o.ReadyBy.IsZero() && startOfUTCDay(o.ReadyBy).Before(today) {
			overdue = append(overdue, o)
		} else {
			waiting = append(waiting, o)
		}
	}
	renderOrdersWaitingOn(b, waiting)
	renderOverdueOrders(b, overdue)
}

// renderOrdersWaitingOn writes the buyer's "## Orders you're waiting on"
// section — one line per order still within its delivery window.
func renderOrdersWaitingOn(b *strings.Builder, orders []OrderView) {
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
		if clause, ok := expiryClause(o.ExpiresAt, time.Now()); ok {
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
	if deadline.IsZero() {
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
// PayOfferWarrants returns the pending pay-offer warrants in the payload's
// consumed batch (the PayOfferWarrantReason entries). It is the single
// source of truth shared by the perception offer-decision section
// (renderPayOffers, below) and the handlers tool-gate (gateTools): the
// rendered offer and the advertised accept_pay/decline_pay/counter_pay tools
// both key off this one predicate so they cannot drift.
//
// Pay offers warrant the SELLER only (the PayOfferReceived subscriber stamps
// the seller, never the buyer), so a non-empty result means "this actor is
// the seller of one or more pending offers awaiting their decision".
func PayOfferWarrants(p Payload) []sim.PayOfferWarrantReason {
	var offers []sim.PayOfferWarrantReason
	for _, w := range p.Warrants {
		if r, ok := w.Reason.(sim.PayOfferWarrantReason); ok {
			offers = append(offers, r)
		}
	}
	return offers
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

func renderPayOffers(b *strings.Builder, offers []sim.PayOfferWarrantReason, nameOf func(sim.ActorID) string) {
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
		fmt.Fprintf(b, "%d. %s offers %s for %d %s %s (offer id %d)\n",
			i+1, buyer, payment, o.Qty, item, disposition, o.LedgerID)
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

// renderPendingOffersFromMe renders the buyer-side "## Your pending offers"
// section — the subject's OWN pay-with-item offers still awaiting the seller's
// answer (ZBBS-HOME-413). It is the mirror of renderPayOffers (the seller's
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
	b.WriteString("## Your pending offers\n")
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
		fmt.Fprintf(b, "%d. You offered %s for %d %s to %s — awaiting their answer (offer id %d)\n",
			i+1, payment, o.Qty, item, seller, o.LedgerID)
	}
	b.WriteString("Wait for their answer — do not place another offer while one for the same goods is still pending.\n")
}

func renderWarrants(b *strings.Builder, warrants []sim.WarrantMeta, nameOf func(sim.ActorID) string, placeNameOf func(string) string, cfg RenderConfig, out *RenderedPrompt) {
	b.WriteString("## What just happened — address these\n")
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
		line, truncated := renderWarrantLine(i+1, w, nameOf, placeNameOf, cfg.MaxBytesPerWarrant)
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
func renderWarrantLine(n int, w sim.WarrantMeta, nameOf func(sim.ActorID) string, placeNameOf func(string) string, maxTextBytes int) (string, bool) {
	switch r := w.Reason.(type) {
	case sim.PCSpeechWarrantReason:
		return renderSpeechWarrantLine(n, nameOf(r.Speaker), r.Excerpt, maxTextBytes)
	case sim.NPCSpeechWarrantReason:
		return renderSpeechWarrantLine(n, nameOf(r.Speaker), r.Excerpt, maxTextBytes)
	case sim.PaidWarrantReason:
		return renderPaidWarrantLine(n, nameOf(r.Buyer), r.Amount, r.ForText, maxTextBytes)
	case sim.IdleBackstopWarrantReason:
		return renderIdleBackstopWarrantLine(n, r.QuietDuration), false
	case sim.RestockWarrantReason:
		return renderRestockWarrantLine(n, r.Item), false
	case sim.ConsumedWarrantReason:
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.DwellStartedWarrantReason:
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.DwellEndedWarrantReason:
		return renderNarrationWarrantLine(n, w.Kind(), r.NarrationText, nameOf(w.TriggerActorID), maxTextBytes)
	case sim.AdminDirectiveWarrantReason:
		return renderImpulseWarrantLine(n, r.Message, maxTextBytes)
	case sim.ArrivalWarrantReason:
		return renderArrivalWarrantLine(n, nameOf(w.TriggerActorID), r, placeNameOf), false
	case sim.NeedThresholdWarrantReason:
		return renderNeedNudgeLine(n, r.Need), false
	case sim.SceneQuoteTargetedWarrantReason:
		return renderQuoteWarrantLine(n, nameOf(r.SellerID), r), false
	case sim.PayResolvedWarrantReason:
		return renderPayResolvedWarrantLine(n, nameOf(r.Seller), r, maxTextBytes), false
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
	return fmt.Sprintf("%d. %s arrived at %s.\n", n, subject, place)
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
		// A kind with no felt template and a real other actor — name them;
		// otherwise (self-triggered, or unresolvable) keep it vague.
		if who != "" && who != "someone" && who != "you" {
			return fmt.Sprintf("%d. Something happened involving %s.\n", n, who)
		}
		return fmt.Sprintf("%d. Something happened nearby.\n", n)
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

// renderQuoteWarrantLine renders a vendor's scene quote aimed directly at this
// actor — a standing offer they can take by paying. Names the seller; the
// terms come straight off the warrant payload. The take-instruction carries
// the quote_id: without it the buyer model answered a standing quote with a
// bare pay_with_item, minting a crossing offer that deadlocked against the
// quote (ZBBS-HOME-424) — the fast path existed but was never legible.
func renderQuoteWarrantLine(n int, seller string, r sim.SceneQuoteTargetedWarrantReason) string {
	unit := "coins"
	if r.Amount == 1 {
		unit = "coin"
	}
	item := sanitizeInline(string(r.ItemKind))
	take := fmt.Sprintf(" To take it, call pay_with_item with quote_id %d and the same item, qty, and amount — it settles at once.", r.QuoteID)
	if r.Qty > 1 {
		return fmt.Sprintf("%d. %s offers you %d %s for %d %s.%s\n", n, seller, r.Qty, item, r.Amount, unit, take)
	}
	return fmt.Sprintf("%d. %s offers you %s for %d %s.%s\n", n, seller, item, r.Amount, unit, take)
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

// renderRestockWarrantLine renders the warrant line for a RestockWarrantReason —
// the buy-side restock producer's nudge to a reseller whose bought-in stock has
// dropped below the reorder threshold. It names the representative low item; the
// actionable detail (current/cap, suppliers, structure_ids) is in the
// "## Restocking" section, so the line stays a short pointer to it.
//
// Form: `N. Your stock of <item> is running low — see Restocking.`
// Form (no item): `N. Your shop stock is running low — see Restocking.`
//
// Rendered without truncation: the item is an engine-controlled catalog key,
// not model- or user-supplied text.
func renderRestockWarrantLine(n int, item sim.ItemKind) string {
	if item == "" {
		return fmt.Sprintf("%d. Your shop stock is running low — see Restocking.\n", n)
	}
	return fmt.Sprintf("%d. Your stock of %s is running low — see Restocking.\n", n, item)
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
