package perception

import (
	"fmt"
	"sort"
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
	renderRelationships(&b, p.Relationships)
	renderPendingDeliveriesFromMe(&b, p.PendingDeliveriesFromMe)
	renderPendingDeliveriesToMe(&b, p.PendingDeliveriesToMe)
	renderRecoveryOptions(&b, p.RecoveryOptions)
	renderSatiation(&b, p.Satiation)
	renderLodging(&b, p.Lodging)
	renderKeeperLodging(&b, p.KeeperLodging)
	renderScene(&b, p)
	renderSecondary(&b, p.Secondary)
	renderWarrants(&b, p.Warrants, cfg, &out)

	out.Text = b.String()
	return out
}

func renderActor(b *strings.Builder, a ActorView) {
	b.WriteString("## You\n")
	state := string(a.State)
	if state == "" {
		state = "unknown"
	}
	fmt.Fprintf(b, "state: %s\n", state)
	if a.InsideStructureID != "" {
		fmt.Fprintf(b, "position: (%d, %d)\n", a.Position.X, a.Position.Y)
	} else {
		fmt.Fprintf(b, "position: (%d, %d) outdoors\n", a.Position.X, a.Position.Y)
	}
	fmt.Fprintf(b, "coins: %d\n", a.Coins)
	if len(a.Needs) > 0 {
		fmt.Fprintf(b, "needs: %s\n", renderNeeds(a.Needs))
	}
	for _, c := range a.ActiveDwellCredits {
		fmt.Fprintf(b, "currently: %s\n", renderActiveDwellCredit(c))
	}
	b.WriteString("\n")
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

func renderSurroundings(b *strings.Builder, s SurroundingsView) {
	b.WriteString("## Around you\n")
	if s.InsideStructureID != "" {
		name := s.StructureName
		if name == "" {
			name = string(s.InsideStructureID)
		}
		fmt.Fprintf(b, "inside: %s [%s]\n", sanitizeInline(name), s.InsideStructureID)
	} else {
		b.WriteString("inside: outdoors\n")
	}
	if s.HuddleID != "" {
		if len(s.HuddleMembers) > 0 {
			fmt.Fprintf(b, "huddle: %s with %s\n", s.HuddleID, joinHuddleMembers(s.HuddleMembers))
		} else {
			fmt.Fprintf(b, "huddle: %s (you are the only member)\n", s.HuddleID)
		}
	} else {
		b.WriteString("huddle: not in a huddle\n")
	}
	b.WriteString("\n")
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
	if m.Acquainted && m.DisplayName != "" {
		return sanitizeInline(m.DisplayName)
	}
	if m.Role != "" {
		return "the " + sanitizeInline(m.Role)
	}
	return "a stranger"
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
		if !o.ExpiresAt.IsZero() {
			fmt.Fprintf(b, " — expires in %s", humanizeDurationUntil(o.ExpiresAt, time.Now()))
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
		if !o.ExpiresAt.IsZero() {
			fmt.Fprintf(b, " — expires in %s", humanizeDurationUntil(o.ExpiresAt, time.Now()))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
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

func renderScene(b *strings.Builder, p Payload) {
	b.WriteString("## This scene\n")
	if p.Primary == nil {
		fmt.Fprintf(b, "no active scene — perceiving from current state only (%s)\n\n", p.Baseline)
		return
	}
	kind := p.Primary.OriginKind
	if kind == "" {
		kind = "unknown"
	}
	fmt.Fprintf(b, "scene: %s — origin %s\n", p.Primary.SceneID, sanitizeInline(kind))

	switch p.Baseline {
	case BaselinePresent:
		b.WriteString("since the scene started: ")
		b.WriteString(renderDiff(p.Primary.Diff))
		b.WriteString("\n")
	case BaselineMissingNoOriginSnapshot:
		b.WriteString("baseline: unavailable — this scene captured no participant baseline; " +
			"treat your situation since it started as undetermined\n")
	case BaselineMissingJoinedAfterOrigin:
		b.WriteString("baseline: unavailable — you joined after this scene started; " +
			"loop detection inconclusive, treat your situation since it started as undetermined\n")
	default:
		b.WriteString("baseline: unavailable\n")
	}
	b.WriteString("\n")
}

// renderDiff renders the loop-detection line. When nothing changed it says
// so explicitly — that is the "you may be looping" signal — but it never
// asserts "no change" unless the Diff is real (Build only attaches a Diff
// for BaselinePresent).
func renderDiff(d *Diff) string {
	if d == nil {
		return "unknown"
	}
	if !d.AnyChange {
		return "no observable change in your state — if this persists you may be repeating yourself"
	}
	var parts []string
	if d.StateChanged {
		parts = append(parts, "your state")
	}
	if d.PositionChanged {
		parts = append(parts, "your position")
	}
	if d.StructureChanged {
		parts = append(parts, "your location")
	}
	if d.HuddleChanged {
		parts = append(parts, "your huddle")
	}
	if d.CoinsChanged {
		parts = append(parts, "your coins")
	}
	if d.InventoryChanged {
		parts = append(parts, "your inventory")
	}
	if d.NeedsChanged {
		parts = append(parts, "your needs")
	}
	return "changed: " + strings.Join(parts, ", ")
}

func renderSecondary(b *strings.Builder, secondary []SceneSignal) {
	if len(secondary) == 0 {
		return
	}
	b.WriteString("## Other scenes in play\n")
	b.WriteString("signals from scenes other than the one above — treat each on its own terms; " +
		"the diff above does not apply to them\n")
	for _, s := range secondary {
		if s.HuddleID != "" {
			fmt.Fprintf(b, "- scene %s (huddle %s): %d signal(s)\n", s.SceneID, s.HuddleID, len(s.Warrants))
		} else {
			fmt.Fprintf(b, "- scene %s: %d signal(s)\n", s.SceneID, len(s.Warrants))
		}
	}
	b.WriteString("\n")
}

// renderWarrants renders the "what just happened" section and fills in the
// RenderedPrompt accounting. Warrants arrive already ordered by
// SourceEventID (Build's job); the caps are applied here, after ordering,
// and any warrant past a cap is moved to DroppedWarrants for carry-forward.
func renderWarrants(b *strings.Builder, warrants []sim.WarrantMeta, cfg RenderConfig, out *RenderedPrompt) {
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
		line, truncated := renderWarrantLine(i+1, w, cfg.MaxBytesPerWarrant)
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

// renderWarrantLine renders one warrant as a single numbered line. The
// untrusted free-text payload (a speech excerpt) is sanitized and capped;
// the returned bool reports whether that text was truncated.
func renderWarrantLine(n int, w sim.WarrantMeta, maxTextBytes int) (string, bool) {
	kind := string(w.Kind())
	if kind == "" {
		kind = "unknown"
	}

	scope := ""
	if w.SceneID != "" {
		scope = fmt.Sprintf(" (scene %s)", w.SceneID)
	}

	switch r := w.Reason.(type) {
	case sim.PCSpeechWarrantReason:
		return renderSpeechWarrantLine(n, w, kind, scope, r.Speaker, r.Excerpt, maxTextBytes)
	case sim.NPCSpeechWarrantReason:
		return renderSpeechWarrantLine(n, w, kind, scope, r.Speaker, r.Excerpt, maxTextBytes)
	case sim.PaidWarrantReason:
		return renderPaidWarrantLine(n, w, kind, scope, r.Buyer, r.Amount, r.ForText, maxTextBytes)
	case sim.IdleBackstopWarrantReason:
		return renderIdleBackstopWarrantLine(n, kind, scope, r.QuietDuration), false
	case sim.ShiftDutyWarrantReason:
		return renderShiftDutyWarrantLine(n, kind, scope, r.ToWork, r.TargetStructureID), false
	case sim.ConsumedWarrantReason:
		return renderNarrationWarrantLine(n, kind, scope, r.NarrationText, w.TriggerActorID, maxTextBytes)
	case sim.DwellStartedWarrantReason:
		return renderNarrationWarrantLine(n, kind, scope, r.NarrationText, w.TriggerActorID, maxTextBytes)
	case sim.DwellEndedWarrantReason:
		return renderNarrationWarrantLine(n, kind, scope, r.NarrationText, w.TriggerActorID, maxTextBytes)
	default:
		if w.TriggerActorID != "" {
			return fmt.Sprintf("%d. [%s]%s involving %s\n", n, kind, scope, w.TriggerActorID), false
		}
		return fmt.Sprintf("%d. [%s]%s\n", n, kind, scope), false
	}
}

// renderNarrationWarrantLine renders a felt-language self-perception beat
// (ZBBS-HOME-302): the consume self-line and the dwell started/ended lines all
// carry a pre-rendered second-person NarrationText. Surfaces it as the warrant
// line, sanitized + capped like the speech excerpt to bound prompt cost.
//
// DwellTickApplied is deliberately NOT routed here — the per-tick "another
// bite" beat would be prompt spam, and the sustained state is already conveyed
// by the ActiveDwellCredits projection ("currently: eating stew"); the per-tick
// warrant keeps its bare default line.
//
// Empty narration (e.g. a catalog-unknown dwell end) falls back to the generic
// involvement line so the warrant still registers rather than vanishing.
func renderNarrationWarrantLine(n int, kind, scope, narration string, trigger sim.ActorID, maxTextBytes int) (string, bool) {
	if narration == "" {
		if trigger != "" {
			return fmt.Sprintf("%d. [%s]%s involving %s\n", n, kind, scope, trigger), false
		}
		return fmt.Sprintf("%d. [%s]%s\n", n, kind, scope), false
	}
	sanitized, truncated := sanitizeText(narration, maxTextBytes)
	return fmt.Sprintf("%d. [%s]%s %s\n", n, kind, scope, sanitized), truncated
}

// renderSpeechWarrantLine renders the warrant line for both PC- and NPC-
// speech warrant reasons. The two reason types are structurally identical
// (SpeechID / Speaker / Excerpt) and the rendered line differs only in the
// kind tag the caller already extracted via WarrantMeta.Kind(); so the
// switch above hands the fields to this single body rather than
// duplicating the format calls.
func renderSpeechWarrantLine(n int, w sim.WarrantMeta, kind, scope string, speaker sim.ActorID, excerpt string, maxTextBytes int) (string, bool) {
	if speaker == "" {
		speaker = w.TriggerActorID
	}
	sanitized, truncated := sanitizeText(excerpt, maxTextBytes)
	if speaker != "" {
		return fmt.Sprintf("%d. [%s]%s %s said: \"%s\"\n", n, kind, scope, speaker, sanitized), truncated
	}
	return fmt.Sprintf("%d. [%s]%s someone said: \"%s\"\n", n, kind, scope, sanitized), truncated
}

// renderPaidWarrantLine renders the warrant line for a PaidWarrantReason.
// Surfaces the buyer, amount, and (optional) flavor text to the seller's
// perception prompt — the seller's next reactor tick reads this and decides
// what to do (speak thanks, walk over, ignore).
//
// Without ForText: `N. [paid] (scene X) <buyer> paid you N coins`.
// With ForText:    `N. [paid] (scene X) <buyer> paid you N coins — "<for>"`.
//
// The ForText excerpt is sanitized + capped like the speech excerpt to keep
// the per-tick prompt cost bounded. Returned bool reports truncation.
func renderPaidWarrantLine(n int, w sim.WarrantMeta, kind, scope string, buyer sim.ActorID, amount int, forText string, maxTextBytes int) (string, bool) {
	if buyer == "" {
		buyer = w.TriggerActorID
	}
	buyerLabel := string(buyer)
	if buyerLabel == "" {
		buyerLabel = "someone"
	}
	unit := "coins"
	if amount == 1 {
		unit = "coin"
	}
	if strings.TrimSpace(forText) == "" {
		return fmt.Sprintf("%d. [%s]%s %s paid you %d %s\n", n, kind, scope, buyerLabel, amount, unit), false
	}
	sanitized, truncated := sanitizeText(forText, maxTextBytes)
	return fmt.Sprintf("%d. [%s]%s %s paid you %d %s — \"%s\"\n", n, kind, scope, buyerLabel, amount, unit, sanitized), truncated
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
// Form: `N. [idle_backstop] you've been quiet for <duration> — consider
// what to do next`. The duration is rounded to whole seconds (sub-second
// resolution is noise at the minute-scale this warrant fires at).
//
// Returned without truncation since there's no untrusted free-text
// payload — the line is composed of fixed prose and an engine-computed
// duration.
func renderIdleBackstopWarrantLine(n int, kind, scope string, quiet time.Duration) string {
	if quiet <= 0 {
		return fmt.Sprintf("%d. [%s]%s you've been quiet — consider what to do next\n", n, kind, scope)
	}
	return fmt.Sprintf("%d. [%s]%s you've been quiet for %s — consider what to do next\n",
		n, kind, scope, quiet.Round(time.Second))
}

// renderShiftDutyWarrantLine renders the warrant line for a
// ShiftDutyWarrantReason — the shift/duty producer's nudge to walk to your
// workplace (shift started) or home (shift ended). It surfaces the target
// structure's id so the model can pass it straight back to the move_to tool
// (move_to(structure_id)); the engine derives enter-vs-visit, so the model only
// needs the id. ToWork picks the direction prose.
//
// Form (to work): `N. [shift_duty] your shift has started — head to your workplace (structure_id: <id>)`
// Form (to home): `N. [shift_duty] your shift has ended — head home (structure_id: <id>)`
//
// An empty target (defensive — the producer only stamps with a real
// Work/HomeStructureID) drops the parenthetical so the line never renders a
// dangling "(structure_id: )".
//
// Rendered without truncation: there is no untrusted free-text payload — the
// id is an engine-controlled world key, not model- or user-supplied text.
func renderShiftDutyWarrantLine(n int, kind, scope string, toWork bool, target sim.StructureID) string {
	dir := "your shift has ended — head home"
	if toWork {
		dir = "your shift has started — head to your workplace"
	}
	if target == "" {
		return fmt.Sprintf("%d. [%s]%s %s\n", n, kind, scope, dir)
	}
	return fmt.Sprintf("%d. [%s]%s %s (structure_id: %s)\n", n, kind, scope, dir, target)
}

// renderNeeds renders a need map as a deterministic "key=value" list,
// sorted by key.
func renderNeeds(needs map[sim.NeedKey]int) string {
	keys := make([]string, 0, len(needs))
	for k := range needs {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, needs[sim.NeedKey(k)]))
	}
	return strings.Join(parts, ", ")
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
