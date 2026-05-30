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
	// Text is the rendered prompt.
	Text string

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
	var b strings.Builder

	b.WriteString("# Your turn\n\n")
	renderNarrativeState(&b, p.NarrativeState)
	renderActor(&b, p.Actor)
	renderSurroundings(&b, p.Surroundings)
	renderAnchors(&b, p.Anchors)
	renderDutySteer(&b, p.DutySteer)
	renderRelationships(&b, p.Relationships)
	renderPendingDeliveriesFromMe(&b, p.PendingDeliveriesFromMe)
	renderPendingDeliveriesToMe(&b, p.PendingDeliveriesToMe)
	renderRecoveryOptions(&b, p.RecoveryOptions)
	renderSatiation(&b, p.Satiation)
	renderRestocking(&b, p.Restocking)
	renderLodging(&b, p.Lodging)
	renderKeeperLodging(&b, p.KeeperLodging)
	renderSummonsForYou(&b, p.SummonsForYou)
	renderSummonRefusal(&b, p.SummonRefusal)
	renderScene(&b, p)
	// "## Other scenes in play" (renderSecondary) was dropped — it surfaced raw
	// scene/huddle UUIDs and a "N signal(s)" count the LLM can't act on
	// (ZBBS-HOME-339). Secondary-scene warrants still render in the flat
	// "what just happened" list; only the machine telemetry block is gone.

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

	// Pay offers are pulled out of the generic warrant list and rendered as
	// an actionable decision section (renderPayOffers) so the seller gets the
	// ledger_id it must echo into accept_pay/decline_pay/counter_pay. The same
	// PayOfferWarrants(p) predicate drives the handlers tool-gate (gateTools),
	// so the rendered offer and the advertised response tools cannot drift.
	// Rendering them in a dedicated, uncapped section (rather than as a capped
	// warrant line) guarantees the ledger_id is present whenever the tools are
	// advertised.
	payOffers := PayOfferWarrants(p)
	renderPayOffers(&b, payOffers, nameOf)

	// Shift-duty warrants drive the wake tick but are NOT rendered — the standing
	// DutySteer cue (renderDutySteer, above) is the single voice for
	// return-to-post (ZBBS-HOME-352). Filtering here also keeps them out of the
	// cap / carry-forward budget; consuming them unrendered is fine since their
	// job is to wake the actor, which the tick already did.
	warrants := nonShiftDutyWarrants(p.Warrants)
	if len(payOffers) > 0 {
		warrants = nonPayOfferWarrants(warrants)
	}
	// Skip the generic "what just happened" block only when the pay-offer
	// section already covered the whole batch; otherwise render it (this also
	// preserves the routine-check-in line for the genuinely-empty case).
	if len(warrants) > 0 || len(payOffers) == 0 {
		renderWarrants(&b, warrants, nameOf, cfg, &out)
	}

	renderTriage(&b)

	out.Text = b.String()
	return out
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
func renderTriage(b *strings.Builder) {
	b.WriteString("Weigh everything above and act on what matters most right now — obligations to others and pressing needs come before idle matters. Choose one thing and do it.\n")
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

	if atmosphere := strings.TrimSpace(sanitizeInline(s.Atmosphere)); atmosphere != "" {
		b.WriteString(atmosphere)
		b.WriteString("\n")
	}
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
	if l := sanitizeInline(v.TargetLabel); l != "" {
		fmt.Fprintf(b, "Your working hours are over and you are not yet home — head home to %s (structure_id: %s) now.\n\n", l, v.TargetID)
	} else {
		fmt.Fprintf(b, "Your working hours are over and you are not yet home — head home (structure_id: %s) now.\n\n", v.TargetID)
	}
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
func renderNarrativeState(b *strings.Builder, n *NarrativeStateView) {
	if n == nil {
		return
	}
	b.WriteString("## Who you are\n")
	if n.SeedText != "" {
		b.WriteString(sanitizeInline(n.SeedText))
		b.WriteString("\n")
	}
	if n.EvolvingSummary != "" {
		b.WriteString(sanitizeInline(n.EvolvingSummary))
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// renderRelationships writes the "What you remember of those here:"
// section. One subsection per co-huddle peer the subject actor has a
// Relationship row for — summary line first, then up to N most-recent
// salient facts (Build already truncated and reversed to most-recent-
// first). Empty when there are no per-peer entries (Build returns nil
// for non-shared actors and for huddles with no relationships).
func renderRelationships(b *strings.Builder, peers []RelationshipPeerView) {
	if len(peers) == 0 {
		return
	}
	b.WriteString("## What you remember of those here\n")
	for _, p := range peers {
		name := sanitizeInline(p.PeerName)
		if name == "" {
			name = string(p.PeerID)
		}
		fmt.Fprintf(b, "- %s:", name)
		if p.SummaryText != "" {
			fmt.Fprintf(b, " %s", sanitizeInline(p.SummaryText))
		}
		b.WriteString("\n")
		for _, f := range p.RecentFacts {
			excerpt, _ := sanitizeText(f.Text, 0)
			kind := string(f.Kind)
			if kind == "" {
				kind = "noted"
			}
			fmt.Fprintf(b, "  - [%s] %s\n", kind, excerpt)
		}
	}
	b.WriteString("\n")
}

// renderPendingDeliveriesFromMe writes the "Orders to deliver:" section
// for the seller side. One line per pending Order — item + qty,
// buyer name, optional consumer-list if this is a group order, and a
// time-remaining hint. Empty list skips the section.
//
// Phase 3 PR S6 — surfacing pending deliveries to the seller's LLM
// is the load-bearing perception mechanism (no warrant kind for
// Order state; the seller relies on baseline perception to remember
// to call deliver_order).
func renderPendingDeliveriesFromMe(b *strings.Builder, orders []OrderView) {
	if len(orders) == 0 {
		return
	}
	b.WriteString("## Orders to deliver\n")
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
		if clause, ok := expiryClause(o.ExpiresAt, time.Now()); ok {
			b.WriteString(clause)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// renderPendingDeliveriesToMe writes the "Orders you're waiting on:"
// section for the buyer/consumer side. One line per pending Order —
// item + qty, seller name, time-remaining hint. Empty list skips
// the section.
//
// Phase 3 PR S6 — gives the buyer's LLM a structured "I'm waiting
// for X from Y" cue so they can speak follow-ups ("Hannah, where's
// my stew?") or make wait/depart decisions.
func renderPendingDeliveriesToMe(b *strings.Builder, orders []OrderView) {
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
	if p.Primary == nil {
		return
	}
	b.WriteString("## Since you got here\n")
	switch p.Baseline {
	case BaselinePresent:
		b.WriteString(renderDiff(p.Primary.Diff))
	default:
		// A scene resolved but no usable baseline (no origin snapshot, or you
		// joined after it started) — can't claim anything changed or didn't.
		b.WriteString("You can't yet tell whether anything has changed.")
	}
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
func renderPayOffers(b *strings.Builder, offers []sim.PayOfferWarrantReason, nameOf func(sim.ActorID) string) {
	if len(offers) == 0 {
		return
	}
	b.WriteString("## Offers awaiting your decision\n")
	for i, o := range offers {
		unit := "coins"
		if o.Amount == 1 {
			unit = "coin"
		}
		disposition := "to keep"
		if o.ConsumeNow {
			disposition = "to consume now"
		}
		buyer := nameOf(o.Buyer)
		item := sanitizeInline(string(o.Item))
		if item == "" {
			item = "item"
		}
		fmt.Fprintf(b, "%d. %s offers %d %s for %d %s %s (offer id %d)\n",
			i+1, buyer, o.Amount, unit, o.Qty, item, disposition, o.LedgerID)
	}
	b.WriteString("Respond with accept_pay, decline_pay, or counter_pay, passing the offer id as ledger_id.\n")
}

func renderWarrants(b *strings.Builder, warrants []sim.WarrantMeta, nameOf func(sim.ActorID) string, cfg RenderConfig, out *RenderedPrompt) {
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
		line, truncated := renderWarrantLine(i+1, w, nameOf, cfg.MaxBytesPerWarrant)
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
func renderWarrantLine(n int, w sim.WarrantMeta, nameOf func(sim.ActorID) string, maxTextBytes int) (string, bool) {
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
		return fmt.Sprintf("%d. %s arrived nearby.\n", n, nameOf(w.TriggerActorID)), false
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
// terms come straight off the warrant payload.
func renderQuoteWarrantLine(n int, seller string, r sim.SceneQuoteTargetedWarrantReason) string {
	unit := "coins"
	if r.Amount == 1 {
		unit = "coin"
	}
	item := sanitizeInline(string(r.ItemKind))
	if r.Qty > 1 {
		return fmt.Sprintf("%d. %s offers you %d %s for %d %s.\n", n, seller, r.Qty, item, r.Amount, unit)
	}
	return fmt.Sprintf("%d. %s offers you %s for %d %s.\n", n, seller, item, r.Amount, unit)
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
		return fmt.Sprintf("%d. %s countered: %d for %s.\n", n, seller, r.CounterAmount, qty)
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
