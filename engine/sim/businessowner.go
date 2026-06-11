package sim

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// businessowner.go — Phase 3 Group A businessowner archetype substrate.
// Engine-authored hospitality speech for shopkeepers / innkeepers / smiths.
// Today the village's keepers respond only to direct transactional input
// (a pay, a speak addressed at them, a deliver_order opportunity); walking
// into the tavern produces no acknowledgment until the customer asks for
// something. That reads as cold, and burning an LLM call on routine
// "welcome in" boilerplate is the wrong tool — the line varies by
// character voice but doesn't need cognition.
//
// Substrate (this file): BusinessownerState type, per-flavor phrase pools,
// per-trigger render helper, EmitBusinessownerSpeech Command (atomic
// cooldown-check + render + Spoke-emit + cooldown-stamp + suppression-
// stamp). The cascade driver (engine/sim/cascade/businessowner.go) wires
// three event subscribers (HuddleJoined / OrderDelivered / HuddleLeft)
// that gate eligible keepers and call the Command per-fire.
//
// Three triggers:
//   - greet on HuddleJoined: the customer enters a huddle a keeper shares
//   - handover on OrderDelivered: the keeper hands over goods
//   - farewell on HuddleLeft: the customer leaves the huddle
//
// The Spoke event we emit reuses the standard speech subscribers:
//   - cascade/action_log handleSpokeActionLog appends an ActionTypeSpoke
//     entry (no engine_authored marker — atmosphere digest treats engine
//     and LLM speech the same).
//   - handlers/speech_reactor handleSpokeWarrants stamps
//     NPCSpeechWarrantReason on each recipient so co-present NPCs react
//     normally. The customer's reactor engages on the keeper's hello.
//
// What we deliberately don't do via the Spoke path: RecordInteraction.
// Engine-authored hospitality boilerplate should not pollute the keeper's
// or customer's per-pair salient-fact trails. The standard sim.Speak
// Command calls RecordInteraction per peer; emitting Spoke directly
// from EmitBusinessownerSpeech bypasses it deliberately.
//
// Cross-cascade gating: BusinessownerState != nil is the predicate, but
// no cascade currently SKIPS based on it (cf. visitor's VisitorState !=
// nil skips). Businessowners are persistent KindNPCShared NPCs that
// participate fully in narrative state — the attribute is descriptive,
// not exclusive.
//
// Cooldowns are in-memory on World.BusinessownerCooldowns; restart loses
// the cooldown stamps. A first-greet on re-encounter after restart is a
// UX wrinkle, not a correctness failure (per the "Postgres is for durable
// storage" rule). v1 used a pg actor_interaction_cooldown table; v2's
// in-memory map fits the substrate posture.
//
// Reactor-tick suppression sits on World.BusinessownerSpeechAt: a 5s
// per-keeper stamp consulted by actorCanReactNow before the evaluator
// admits a tick. Prevents the LLM from following up with a redundant
// "welcome friend" after the engine just spoke for the keeper.

// BusinessownerTrigger discriminates the three hospitality triggers.
// Closed-set typed string — adding a new trigger requires extending
// init()'s exhaustiveness check below.
type BusinessownerTrigger string

const (
	// BusinessownerTriggerGreet — fires on HuddleJoined when a non-keeper
	// joins a huddle a keeper is in (at-post check applied separately).
	BusinessownerTriggerGreet BusinessownerTrigger = "greet"

	// BusinessownerTriggerHandover — fires on OrderDelivered when the
	// seller has the businessowner attribute. No cooldown; every
	// transaction deserves a verbal handover.
	BusinessownerTriggerHandover BusinessownerTrigger = "handover"

	// BusinessownerTriggerFarewell — fires on HuddleLeft when a non-keeper
	// leaves a huddle a keeper remains in (at-post check applied
	// separately).
	BusinessownerTriggerFarewell BusinessownerTrigger = "farewell"
)

// BusinessownerState is the per-actor businessowner attribute. Non-nil
// marks the actor as a keeper for hospitality purposes. Flavor selects
// the phrase pool ("flamboyant" for tavern / inn / general store crowd,
// "reserved" for smiths). The struct exists rather than a bare string
// field on Actor so future per-keeper config (custom phrase overrides,
// per-keeper cooldown nudges) lands without re-typing every callsite.
//
// Set at world seeding from the operator-supplied actor attribute config;
// the engine itself does not mint BusinessownerState rows at runtime.
type BusinessownerState struct {
	Flavor string
}

// cloneBusinessownerState deep-copies a BusinessownerState pointer. All
// fields are value types today, so a struct copy is sufficient — but the
// helper exists so future pointer-bearing fields don't silently alias
// across the snapshot / mem-repo boundary. Mirrors cloneVisitorState.
func cloneBusinessownerState(src *BusinessownerState) *BusinessownerState {
	if src == nil {
		return nil
	}
	cp := *src
	return &cp
}

// Defaults — fall back when WorldSettings.Businessowner* zero values are
// observed. Tests that bypass the environment loader get these for free.
const (
	// DefaultBusinessownerGreetCooldownMinutes is the per-(keeper, customer)
	// gap between greets. 30 min covers "the customer popped out for an
	// errand and came back" with a re-greet on the second visit, but
	// suppresses the redundant "welcome friend" when the same customer
	// rejoins the huddle ten seconds later after stepping outside to
	// fetch something.
	DefaultBusinessownerGreetCooldownMinutes = 30

	// DefaultBusinessownerFarewellCooldownMinutes mirrors the greet
	// cooldown shape. Same UX reasoning.
	DefaultBusinessownerFarewellCooldownMinutes = 30

	// businessownerEngineSpeechSuppressionTTL is the reactor-tick
	// suppression window after an engine-authored hospitality line.
	// actorCanReactNow consults the World.BusinessownerSpeechAt stamp
	// before admitting a tick — if the keeper just engine-spoke in the
	// last 5s, their LLM tick on the same triggering event is skipped
	// so the model doesn't follow up with a redundant "welcome friend!"
	// of its own.
	//
	// 5s is generous — the speech reactor's same-event warrant fires
	// within milliseconds; a real LLM call won't complete within the
	// window. The width also handles cascades where the engine line
	// triggers a follow-up event (e.g. a peer reaction) that re-warrants
	// the keeper on the same conversational moment.
	businessownerEngineSpeechSuppressionTTL = 5 * time.Second
)

// businessownerPhrases — per-flavor, per-trigger phrase pools. Plain Go
// maps so changes are one file edit + rebuild, no DB churn. Mirrors v1's
// shape verbatim — the rune-truncated speech line lands in ActionLog
// where atmosphere digest and narrative consolidation pick it up.
//
// Template tokens:
//
//	{customer}  — listener's display name (interpolated at fire time)
//
// New flavor or new trigger: add the entry here AND extend the init()
// exhaustiveness check below. Adding a flavor without a matching
// trigger pool panics at engine startup so the mismatch can't reach a
// running deploy.
var businessownerPhrases = map[string]map[BusinessownerTrigger][]string{
	"flamboyant": {
		BusinessownerTriggerGreet: {
			"Welcome, friend! Come in out of the cold.",
			"Ah, {customer}! Come in, come in — what can I do for you?",
			"Welcome, {customer}! Sit and warm yourself.",
			"Come in, {customer} — make yourself at home!",
			"There you are, {customer}! Glad to have you in.",
			"Welcome — the hearth's lit and the door's open. Come in.",
		},
		BusinessownerTriggerHandover: {
			"Here you are, {customer} — enjoy.",
			"There you go, {customer}!",
			"For you, friend. Enjoy it.",
			"All yours, {customer}.",
			"There — fresh and ready, {customer}.",
			"With my compliments, {customer}.",
		},
		BusinessownerTriggerFarewell: {
			"Safe travels, {customer}! Come back anytime.",
			"Until next time, {customer}. The hearth's always warm.",
			"Take care out there, {customer} — we'll see you again soon.",
			"Mind the cold, friend! Door's open whenever you're back.",
			"Stay well, {customer}!",
			"Off you go, then — safe to you, {customer}.",
		},
	},
	"reserved": {
		BusinessownerTriggerGreet: {
			"Greetings, {customer}.",
			"Good day. Come in.",
			"{customer}. What do you need?",
			"Step in.",
			"You're in. State your business when you're ready.",
		},
		BusinessownerTriggerHandover: {
			"Yours, {customer}.",
			"There.",
			"Done.",
			"Take it.",
			"For you, {customer}.",
		},
		BusinessownerTriggerFarewell: {
			"Until next time, {customer}.",
			"Mind yourself.",
			"Safe to you.",
			"Go on, then.",
			"{customer}. Mm.",
		},
	},
}

// init enforces phrase-pool exhaustiveness at package load. Every flavor
// MUST have a non-empty pool for every defined trigger. Missing pool →
// panic → engine refuses to start, so the mismatch can't reach a running
// deploy. Mirrors visitor.go's init() pattern.
func init() {
	required := []BusinessownerTrigger{
		BusinessownerTriggerGreet,
		BusinessownerTriggerHandover,
		BusinessownerTriggerFarewell,
	}
	for flavor, pools := range businessownerPhrases {
		for _, trig := range required {
			pool, ok := pools[trig]
			if !ok || len(pool) == 0 {
				panic(fmt.Sprintf(
					"sim/businessowner: flavor %q is missing required trigger pool %q",
					flavor, trig))
			}
		}
	}
}

// BusinessownerFlavors returns the configured flavor list, sorted for
// deterministic iteration. Used by tests and by future operator-setup
// probes that want to enumerate supported flavors.
func BusinessownerFlavors() []string {
	out := make([]string, 0, len(businessownerPhrases))
	for flavor := range businessownerPhrases {
		out = append(out, flavor)
	}
	// Deterministic order for test reproducibility.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// RenderBusinessownerPhrase picks a random line from pool and
// interpolates {customer}. Returns "" on an empty pool — caller treats
// empty as "skip the fire", so a misconfigured BusinessownerState.Flavor
// (which resolves to no registry pool via BusinessownerNarrationKey)
// degrades to silence rather than a panic during runtime. r is non-nil;
// production callers thread a per-driver seeded rand, tests use a
// deterministic seed.
//
// The pool comes from the world's narration registry (ZBBS-WORK-399:
// seed lines + any LLM-expanded extras), drawn by the caller via
// narrationDraw so the draw is counted; this function stays pure
// selection + interpolation.
//
// customerName empty falls back to a token-stripped form: ", {customer}"
// and " {customer}" tokens are removed (preserving grammar), and bare
// "{customer}" tokens become empty. Lets a missing display name still
// produce a sensible line rather than literal "Welcome, {customer}!".
func RenderBusinessownerPhrase(r *rand.Rand, pool []string, customerName string) string {
	if r == nil {
		return ""
	}
	if len(pool) == 0 {
		return ""
	}
	line := pool[r.Intn(len(pool))]
	if customerName != "" {
		line = strings.ReplaceAll(line, "{customer}", customerName)
		return line
	}
	line = strings.ReplaceAll(line, ", {customer}", "")
	line = strings.ReplaceAll(line, " {customer}", "")
	line = strings.ReplaceAll(line, "{customer}", "")
	return line
}

// BusinessownerCooldownKey identifies a per-(keeper, customer, trigger)
// cooldown row. World.BusinessownerCooldowns is map[Key]time.Time of
// last-fired stamps. Restart-loss is acceptable: see file header.
type BusinessownerCooldownKey struct {
	Speaker  ActorID
	Listener ActorID
	Trigger  BusinessownerTrigger
}

// businessownerCooldownActive returns true when the (speaker, listener,
// trigger) row was last fired within cooldownMinutes. False when no row
// exists OR the row is past the window — both mean "go ahead."
//
// cooldownMinutes <= 0 disables the gate (handover passes 0 — no
// cooldown by design).
//
// MUST be called from inside a Command.Fn (reads World.BusinessownerCooldowns
// directly). The map is lazy-allocated; nil is treated as empty.
func businessownerCooldownActive(w *World, speaker, listener ActorID, trigger BusinessownerTrigger, cooldownMinutes int, now time.Time) bool {
	if cooldownMinutes <= 0 {
		return false
	}
	if w.BusinessownerCooldowns == nil {
		return false
	}
	key := BusinessownerCooldownKey{Speaker: speaker, Listener: listener, Trigger: trigger}
	lastFired, ok := w.BusinessownerCooldowns[key]
	if !ok {
		return false
	}
	return now.Sub(lastFired) < time.Duration(cooldownMinutes)*time.Minute
}

// stampBusinessownerCooldown upserts the (speaker, listener, trigger) row
// with last-fired = now. Lazy-allocates the map on first write. MUST be
// called from inside a Command.Fn.
func stampBusinessownerCooldown(w *World, speaker, listener ActorID, trigger BusinessownerTrigger, now time.Time) {
	if w.BusinessownerCooldowns == nil {
		w.BusinessownerCooldowns = make(map[BusinessownerCooldownKey]time.Time)
	}
	w.BusinessownerCooldowns[BusinessownerCooldownKey{
		Speaker:  speaker,
		Listener: listener,
		Trigger:  trigger,
	}] = now
}

// stampBusinessownerEngineSpeech records that actorID just engine-spoke a
// hospitality line at now. Read by actorCanReactNow via
// businessownerEngineSpeechRecent before admitting a tick. Lazy-allocates
// the map. MUST be called from inside a Command.Fn.
func stampBusinessownerEngineSpeech(w *World, actorID ActorID, now time.Time) {
	if w.BusinessownerSpeechAt == nil {
		w.BusinessownerSpeechAt = make(map[ActorID]time.Time)
	}
	w.BusinessownerSpeechAt[actorID] = now
}

// businessownerEngineSpeechRecent reports whether actorID has an engine-
// hospitality stamp within businessownerEngineSpeechSuppressionTTL.
// Consulted by actorCanReactNow before admitting a tick. The map is
// lazy-allocated; nil is treated as no-recent-speech.
//
// Stale entries are not GC'd here — they're effectively garbage-collected
// on the next overwrite, which on an active keeper is "every hospitality
// fire." Footprint stays bounded at one entry per active keeper.
//
// MUST be called from inside a Command.Fn or from a subscriber dispatched
// from emit (both run on the world goroutine).
func businessownerEngineSpeechRecent(w *World, actorID ActorID, now time.Time) bool {
	if w.BusinessownerSpeechAt == nil {
		return false
	}
	stamp, ok := w.BusinessownerSpeechAt[actorID]
	if !ok {
		return false
	}
	return now.Sub(stamp) < businessownerEngineSpeechSuppressionTTL
}

// BusinessownerSpeechArgs bundles the inputs to EmitBusinessownerSpeech
// so the Command signature stays readable. All fields required except
// RecipientIDs, which may be empty for the "no peer to address" edge case
// (rare but possible — the listener may have left between the cascade
// handler's read and the Command's execution; the Spoke event still emits
// for atmosphere-digest pickup).
//
// SpeakerID + ListenerID are the keeper + customer. SpeakerName +
// ListenerName are denormalized DisplayName values captured by the cascade
// handler (so the Command doesn't re-read w.Actors); empty ListenerName
// falls back to the token-stripped phrase form per RenderBusinessownerPhrase.
//
// Trigger drives the phrase pool lookup AND the cooldown branch (handover
// is no-cooldown by design, see cooldownMinutes(0) at the call site).
//
// HuddleID is the speaker's huddle at fire time; carries through to the
// Spoke event and to the ActionLog row.
//
// CooldownMinutes is the per-(keeper, customer, trigger) gate window;
// pass 0 to disable (handover path). The Command also stamps the
// cooldown on success — passing 0 skips the stamp (handover doesn't
// upsert).
//
// Rand is the per-driver seeded source threaded through for phrase
// selection. Nil is rejected (caller bug).
type BusinessownerSpeechArgs struct {
	SpeakerID       ActorID
	SpeakerName     string
	ListenerID      ActorID
	ListenerName    string
	Trigger         BusinessownerTrigger
	HuddleID        HuddleID
	RecipientIDs    []ActorID
	CooldownMinutes int
	Rand            *rand.Rand
	Now             time.Time
}

// BusinessownerSpeechResult reports what EmitBusinessownerSpeech did.
// Fired==true means a Spoke event was emitted; SkipReason carries a short
// label for any path that returned without emitting (cooldown, flavor
// resolution, empty phrase, missing actor). Consumers (tests, future
// telemetry) read this rather than trying to detect emission via event
// counts.
type BusinessownerSpeechResult struct {
	Fired      bool
	SkipReason string
}

// EmitBusinessownerSpeech is the substrate Command that atomically:
//
//  1. Validates the speaker exists and has BusinessownerState != nil.
//  2. Checks the cooldown (when CooldownMinutes > 0).
//  3. Renders the phrase for the speaker's flavor + trigger.
//  4. Emits a Spoke event with the keeper as speaker — reuses the
//     standard speech subscribers (action_log + speech_reactor).
//  5. Stamps the cooldown (when CooldownMinutes > 0).
//  6. Stamps the engine-speech suppression flag on the speaker.
//
// All five mutations land on the world goroutine in one Command.Fn —
// atomic from the rest of the world's perspective.
//
// Result carries Fired + SkipReason for tests and future telemetry. The
// Command returns no error on skip — skipping is the normal flow when
// the cooldown or flavor gate trips. Errors come back only on
// caller-bug shapes (zero SpeakerID, nil Rand).
func EmitBusinessownerSpeech(args BusinessownerSpeechArgs) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			res := BusinessownerSpeechResult{}
			if args.SpeakerID == "" {
				return res, fmt.Errorf("EmitBusinessownerSpeech: SpeakerID required")
			}
			if args.Rand == nil {
				return res, fmt.Errorf("EmitBusinessownerSpeech: Rand required")
			}
			speaker, ok := w.Actors[args.SpeakerID]
			if !ok {
				res.SkipReason = "speaker missing"
				return res, nil
			}
			if speaker.BusinessownerState == nil {
				res.SkipReason = "speaker not businessowner"
				return res, nil
			}
			flavor := speaker.BusinessownerState.Flavor
			if flavor == "" {
				res.SkipReason = "speaker flavor empty"
				return res, nil
			}
			if args.CooldownMinutes > 0 && businessownerCooldownActive(
				w, args.SpeakerID, args.ListenerID, args.Trigger,
				args.CooldownMinutes, args.Now,
			) {
				res.SkipReason = "cooldown active"
				return res, nil
			}
			text := RenderBusinessownerPhrase(args.Rand, w.narrationDraw(BusinessownerNarrationKey(flavor, args.Trigger)), args.ListenerName)
			if text == "" {
				// Unknown flavor or trigger reaching here means the gate
				// above missed something — defensive skip rather than emit
				// an empty Spoke.
				res.SkipReason = "phrase empty"
				return res, nil
			}
			// Truncate at rune boundary in case future phrase additions grow.
			// MaxActionLogTextLen is the same as MaxSalientFactTextLen (220
			// runes) — the speech reactor's warrant excerpt and the action
			// log's Text share the same per-token-budget concern.
			textRunes := []rune(text)
			if len(textRunes) > MaxActionLogTextLen {
				text = string(textRunes[:MaxActionLogTextLen])
			}
			// Emit a Spoke event with the keeper as speaker. Reuses the
			// standard subscribers — cascade/action_log writes the
			// ActionTypeSpoke entry, handlers/speech_reactor stamps an
			// NPCSpeechWarrantReason on each recipient so co-present NPCs
			// react. RecordInteraction is NOT called (deliberate — engine
			// hospitality boilerplate doesn't go in salient-fact trails).
			recipients := args.RecipientIDs
			if recipients == nil {
				recipients = []ActorID{}
			}
			w.emit(&Spoke{
				SpeakerID:    args.SpeakerID,
				HuddleID:     args.HuddleID,
				RecipientIDs: recipients,
				Text:         text,
				At:           args.Now,
			})
			if args.CooldownMinutes > 0 {
				stampBusinessownerCooldown(w, args.SpeakerID, args.ListenerID, args.Trigger, args.Now)
			}
			stampBusinessownerEngineSpeech(w, args.SpeakerID, args.Now)
			res.Fired = true
			return res, nil
		},
	}
}
