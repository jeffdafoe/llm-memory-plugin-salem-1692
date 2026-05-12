package main

// Businessowner attribute (ZBBS-HOME-273) — engine-authored hospitality
// triggers for shopkeepers, innkeepers, smiths, and other customer-
// facing NPCs.
//
// Today the village's keepers respond only to direct transactional
// input (a pay, a speak addressed at them, a deliver_order opportunity
// pushed onto their order book). Walking into John Ellis's tavern
// produces no acknowledgment until the customer asks for something.
// That reads as cold, and burning an LLM call on routine "hello there,
// welcome in" is the wrong tool — the line varies by character voice
// but doesn't need cognition.
//
// This file is the engine-side hospitality layer. NPCs with the
// `businessowner` attribute get scripted greet (and later handover /
// farewell) lines from per-flavor phrase pools, fired deterministically
// at structure entry. The `params.flavor` jsonb field on
// actor_attribute selects the pool — "flamboyant" for the tavern /
// inn / general store crowd, "reserved" for Ezekiel's smithy. Adding
// new flavors is a phrase-pool addition plus an init() exhaustiveness
// check.
//
// Coupling to the existing perception system: none beyond writing
// `agent_action_log` rows with `source='engine'`. The chat panel
// renders these as ordinary speak events, the LLM's next perception
// sees them in chat history. We do NOT inject any "you have already
// greeted them" preamble in the LLM prompt — the greet line is just
// a normal chat row in scene history, the same as any other speech.
// The keeper's next tick reads it like any other prior turn.
//
// Scope of this initial cut: greet only. Handover refactor and
// farewell land in follow-up commits. Hooks are minimal — one call
// from each huddle-entry path (NPC via joinOrCreateHuddle, PC via
// joinOrCreateHuddleForPC).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	businessownerSlug = "businessowner"

	// Trigger constants — used for cooldown table rows and audit log
	// payload. Keep short; the column is varchar(16).
	triggerGreet    = "greet"
	triggerHandover = "handover"
	triggerFarewell = "farewell"

	// Default cooldown windows when the operator setting row is
	// missing or unparseable. 30 minutes for both greet and farewell
	// is the task-note default (the customer pops out for an errand
	// and comes back — re-greet feels warm; same hour, no).
	defaultBusinessownerGreetCooldownMinutes    = 30
	defaultBusinessownerFarewellCooldownMinutes = 30

	// Reactor-tick suppression TTL. Read by the reactor-tick scheduler
	// before invoking a keeper's agent — if the engine just emitted a
	// hospitality line for them within this window, the keeper's
	// reactor tick on the SAME triggering event is skipped (otherwise
	// the LLM would follow up with a redundant "hello there" speak
	// that we already covered for free).
	//
	// Not currently consulted in this MVP cut (greet has no reactor
	// follow-up wired today — the customer's arrival doesn't queue a
	// keeper tick yet). Plumbed for the handover-refactor and farewell
	// PRs to come; keeping the helper here so both can call it.
	engineSpeechSuppressionTTL = 5 * time.Second
)

// businessownerPhrases — per-flavor, per-trigger phrase pools. Plain
// Go maps so changes are one file edit + a build, no DB churn.
//
// Template tokens:
//   {customer}  — listener's display name (interpolated at fire time)
//
// New flavor or new trigger: add the entry here AND extend the
// init() exhaustiveness check below. Adding a new flavor without a
// matching trigger pool panics at engine startup so the mismatch
// can't reach a running deploy.
var businessownerPhrases = map[string]map[string][]string{
	"flamboyant": {
		triggerGreet: {
			"Welcome, friend! Come in out of the cold.",
			"Ah, {customer}! Come in, come in — what can I do for you?",
			"Welcome, {customer}! Sit and warm yourself.",
			"Come in, {customer} — make yourself at home!",
			"There you are, {customer}! Glad to have you in.",
			"Welcome — the hearth's lit and the door's open. Come in.",
		},
		triggerHandover: {
			"Here you are, {customer} — enjoy.",
			"There you go, {customer}!",
			"For you, friend. Enjoy it.",
			"All yours, {customer}.",
			"There — fresh and ready, {customer}.",
			"With my compliments, {customer}.",
		},
		triggerFarewell: {
			"Safe travels, {customer}! Come back anytime.",
			"Until next time, {customer}. The hearth's always warm.",
			"Take care out there, {customer} — we'll see you again soon.",
			"Mind the cold, friend! Door's open whenever you're back.",
			"Stay well, {customer}!",
			"Off you go, then — safe to you, {customer}.",
		},
	},
	"reserved": {
		triggerGreet: {
			"Greetings, {customer}.",
			"Good day. Come in.",
			"{customer}. What do you need?",
			"Step in.",
			"You're in. State your business when you're ready.",
		},
		triggerHandover: {
			"Yours, {customer}.",
			"There.",
			"Done.",
			"Take it.",
			"For you, {customer}.",
		},
		triggerFarewell: {
			"Until next time, {customer}.",
			"Mind yourself.",
			"Safe to you.",
			"Go on, then.",
			"{customer}. Mm.",
		},
	},
}

// businessownerFlavors returns the configured flavor list — used by
// init() and by tests / probes. Adding a flavor is one entry in
// businessownerPhrases AND a build to surface the init() panic if a
// trigger pool is missing.
func businessownerFlavors() []string {
	out := make([]string, 0, len(businessownerPhrases))
	for flavor := range businessownerPhrases {
		out = append(out, flavor)
	}
	return out
}

// init enforces phrase-pool exhaustiveness at engine startup. Every
// flavor MUST have a non-empty pool for every defined trigger
// (currently just greet — handover and farewell pools get added when
// those triggers land). Missing pool → panic → engine refuses to
// start, so the mismatch can't reach a running deploy.
func init() {
	requiredTriggers := []string{triggerGreet, triggerHandover, triggerFarewell}
	for flavor, pools := range businessownerPhrases {
		for _, trig := range requiredTriggers {
			pool, ok := pools[trig]
			if !ok || len(pool) == 0 {
				panic(fmt.Sprintf(
					"businessowner.go: flavor %q is missing required trigger pool %q",
					flavor, trig))
			}
		}
	}
}

// renderBusinessownerPhrase picks a random line from the flavor's
// pool for the given trigger and interpolates {customer}. Returns
// "" when the flavor or trigger is unknown — caller treats empty
// as "skip the fire" so a misconfigured actor_attribute row degrades
// to silence rather than a panic during runtime.
func renderBusinessownerPhrase(flavor, trigger, customerName string) string {
	pools, ok := businessownerPhrases[flavor]
	if !ok {
		return ""
	}
	pool, ok := pools[trigger]
	if !ok || len(pool) == 0 {
		return ""
	}
	line := pool[rand.Intn(len(pool))]
	if customerName != "" {
		line = strings.ReplaceAll(line, "{customer}", customerName)
	} else {
		// Fall back: strip the comma-prefix form ", {customer}" when
		// we have no name, leaving a still-grammatical line.
		line = strings.ReplaceAll(line, ", {customer}", "")
		line = strings.ReplaceAll(line, " {customer}", "")
		line = strings.ReplaceAll(line, "{customer}", "")
	}
	return line
}

// loadBusinessownerFlavor reads the flavor value from
// actor_attribute.params for the businessowner attribute on an actor.
// Returns "" when the actor doesn't have the attribute (caller treats
// that as "not a businessowner, skip the trigger entirely").
func (app *App) loadBusinessownerFlavor(ctx context.Context, actorID string) string {
	var flavor sql.NullString
	err := app.DB.QueryRow(ctx,
		`SELECT params->>'flavor'
		   FROM actor_attribute
		  WHERE actor_id = $1 AND slug = $2`,
		actorID, businessownerSlug,
	).Scan(&flavor)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("businessowner: load flavor for %s: %v", actorID, err)
		}
		return ""
	}
	if !flavor.Valid {
		return ""
	}
	return flavor.String
}

// businessownerCooldownActive returns true when the (speaker, listener,
// trigger) row was last fired within the cooldown window. False when
// no row exists OR the row is past the window — both mean "go ahead."
//
// Read-only; the call site upserts on actual fire to bump last_fired_at.
func (app *App) businessownerCooldownActive(ctx context.Context, speakerID, listenerID, trigger string, cooldownMinutes int) bool {
	if cooldownMinutes <= 0 {
		return false
	}
	var lastFired sql.NullTime
	err := app.DB.QueryRow(ctx,
		`SELECT last_fired_at
		   FROM actor_interaction_cooldown
		  WHERE speaker_id = $1 AND listener_id = $2 AND trigger = $3`,
		speakerID, listenerID, trigger,
	).Scan(&lastFired)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("businessowner: cooldown read %s→%s %s: %v",
				speakerID, listenerID, trigger, err)
		}
		return false
	}
	if !lastFired.Valid {
		return false
	}
	return time.Since(lastFired.Time) < time.Duration(cooldownMinutes)*time.Minute
}

// upsertBusinessownerCooldown writes (or refreshes) the cooldown row
// after a successful fire. Always sets last_fired_at to NOW() so a
// subsequent fire within the window finds an active cooldown.
func (app *App) upsertBusinessownerCooldown(ctx context.Context, speakerID, listenerID, trigger string) {
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO actor_interaction_cooldown
		     (speaker_id, listener_id, trigger, last_fired_at)
		 VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (speaker_id, listener_id, trigger)
		   DO UPDATE SET last_fired_at = NOW()`,
		speakerID, listenerID, trigger,
	); err != nil {
		log.Printf("businessowner: cooldown upsert %s→%s %s: %v",
			speakerID, listenerID, trigger, err)
	}
}

// maybeFireGreetOnEntry is the entry-side dispatcher. Called from
// huddle-join paths (NPC via joinOrCreateHuddle, PC via
// joinOrCreateHuddleForPC) right after the entering actor's
// current_huddle_id has been set. enteringID is the actor entering
// (NPC actor.id or PC actor.id); huddleID is the huddle they just
// joined; structureID is the structure they entered.
//
// Predicate gates (any false → skip):
//   1. Entering actor does NOT have the businessowner attribute
//      themselves. Businessowners don't greet each other; the rule
//      avoids John and Hannah trading "welcome friend!" lines.
//   2. At least one businessowner is currently inside structureID
//      AND has work_structure_id = structureID (at-post check).
//   3. That businessowner is not asleep, not on break.
//   4. The (businessowner, entering, greet) cooldown row has expired
//      or doesn't exist.
//
// Fires the greet from each eligible businessowner. Multiple greets
// in a single entry are unlikely (one shopkeeper per structure in
// practice) but supported — Hannah greets and Josiah greets too if
// both are co-tenants of a hypothetical shared business.
func (app *App) maybeFireGreetOnEntry(ctx context.Context, enteringID, structureID, huddleID string) {
	if enteringID == "" || structureID == "" || huddleID == "" {
		return
	}
	// Gate 1: entering actor's own businessowner status. Read once.
	if app.loadBusinessownerFlavor(ctx, enteringID) != "" {
		return
	}

	// Resolve the entering actor's display name once for the greet
	// template. NPC or PC — actor.display_name covers both.
	var customerName string
	if err := app.DB.QueryRow(ctx,
		`SELECT display_name FROM actor WHERE id = $1`,
		enteringID,
	).Scan(&customerName); err != nil {
		log.Printf("businessowner: lookup customer name for %s: %v", enteringID, err)
		return
	}

	// Gate 2: find businessowners co-located at structureID, at their
	// own work_structure_id, not on break or asleep. inside_structure_id
	// filters to "inside this structure right now"; the attribute
	// presence + flavor presence filters to "actually a businessowner."
	rows, err := app.DB.Query(ctx,
		`SELECT a.id::text, a.display_name, aa.params->>'flavor'
		   FROM actor a
		   JOIN actor_attribute aa
		     ON aa.actor_id = a.id AND aa.slug = $1
		  WHERE a.inside_structure_id::text = $2
		    AND a.work_structure_id::text   = $2
		    AND (a.sleeping_until IS NULL OR a.sleeping_until <= NOW())
		    AND (a.break_until    IS NULL OR a.break_until    <= NOW())
		    AND a.id <> $3`,
		businessownerSlug, structureID, enteringID,
	)
	if err != nil {
		log.Printf("businessowner: scan keepers at %s: %v", structureID, err)
		return
	}
	defer rows.Close()

	type keeper struct{ id, name, flavor string }
	var keepers []keeper
	for rows.Next() {
		var k keeper
		if err := rows.Scan(&k.id, &k.name, &k.flavor); err != nil {
			log.Printf("businessowner: scan row at %s: %v", structureID, err)
			continue
		}
		if k.flavor == "" {
			continue // missing params.flavor — skip, don't panic at runtime
		}
		keepers = append(keepers, k)
	}
	if len(keepers) == 0 {
		return
	}

	cooldownMin := app.loadNonNegativeIntSetting(ctx,
		"businessowner_greet_cooldown_minutes",
		defaultBusinessownerGreetCooldownMinutes)

	for _, k := range keepers {
		if app.businessownerCooldownActive(ctx, k.id, enteringID, triggerGreet, cooldownMin) {
			continue
		}
		text := renderBusinessownerPhrase(k.flavor, triggerGreet, customerName)
		if text == "" {
			continue
		}
		app.fireBusinessownerSpeech(ctx, fireSpeechArgs{
			speakerID:   k.id,
			speakerName: k.name,
			listenerID:  enteringID,
			listenerName: customerName,
			huddleID:    huddleID,
			structureID: structureID,
			trigger:     triggerGreet,
			text:        text,
		})
		app.upsertBusinessownerCooldown(ctx, k.id, enteringID, triggerGreet)
	}
}

// maybeFireHandoverIfBusinessowner is called from executeDeliverOrder
// right after the existing room_event ("X hands Y the Z") broadcast.
// When the seller has the businessowner attribute, the engine emits
// an additional npc_spoke event from the seller's flavor pool — the
// verbal handover line ("Here you are, {customer}!" etc.) that the
// LLM would otherwise be expected to produce in iter N+1.
//
// Why both narrations: the existing narrateDeliverHandover physical
// action line ("John Ellis hands Jefferey the stew.") is third-person
// engine narration and reads as the visual moment of the goods
// changing hands. This new line is first-person speech from the
// keeper, anchoring the social beat. Pre-fix, the keeper's LLM tick
// in iter N+1 produced this line at cost; engine-authored at this
// point makes the standard hospitality cadence deterministic and free.
//
// No cooldown for handover (the task design — every transaction
// deserves a verbal handover). The LLM may still emit a follow-up
// speak in iter N+1; the suppression flag is stamped here so any
// reactor-tick-driven follow-up by the same actor in the next 5s
// is skipped, but the in-tick iter N+1 speak path is not currently
// suppressed. Acceptable v1 quirk — if the LLM says "here you are"
// after the engine already did, it reads as a personal flourish.
//
// Non-businessowner sellers get no engine speak — only the existing
// narrateDeliverHandover line, exactly as before this PR.
//
// `qty` and `item` are accepted for future template tokens; today
// only {customer} is interpolated. Plumbed for handover-pool growth
// without touching call sites.
func (app *App) maybeFireHandoverIfBusinessowner(ctx context.Context, sellerID, buyerID, buyerName, item string, qty int, huddleID, structureID string) {
	if sellerID == "" || buyerID == "" || huddleID == "" || structureID == "" {
		return
	}
	flavor := app.loadBusinessownerFlavor(ctx, sellerID)
	if flavor == "" {
		return
	}
	var sellerName string
	if err := app.DB.QueryRow(ctx,
		`SELECT display_name FROM actor WHERE id = $1`,
		sellerID,
	).Scan(&sellerName); err != nil {
		log.Printf("businessowner: lookup seller name for %s: %v", sellerID, err)
		return
	}
	text := renderBusinessownerPhrase(flavor, triggerHandover, buyerName)
	if text == "" {
		return
	}
	app.fireBusinessownerSpeech(ctx, fireSpeechArgs{
		speakerID:    sellerID,
		speakerName:  sellerName,
		listenerID:   buyerID,
		listenerName: buyerName,
		huddleID:     huddleID,
		structureID:  structureID,
		trigger:      triggerHandover,
		text:         text,
	})
	// No cooldown upsert — handover has no cooldown by design.
	// item / qty unused today; reserved for future template tokens.
	_ = item
	_ = qty
}

// maybeFireFarewellOnExit is the exit-side counterpart to
// maybeFireGreetOnEntry. Called from leaveHuddle / leaveHuddleForPC
// BEFORE the leaving actor's current_huddle_id is cleared, so the
// outgoing speak's huddle attribution lands on the room they're
// leaving — not the empty/null state that follows.
//
// huddleID is the huddle the actor is about to leave; the leaver's
// actor.id is leavingID. The dispatcher reads the structure_id from
// scene_huddle (the leaver's inside_structure_id may have already
// flipped to the new destination via setNPCInside above us).
//
// Same gate stack as greet:
//   1. Leaver does NOT have businessowner attribute.
//   2. At least one businessowner is co-located at the LEFT structure
//      AND work_structure_id matches AND not asleep/on-break.
//   3. (keeper, leaver, farewell) cooldown row has expired or is
//      absent.
//
// Lateness note: by the time leaveHuddle is invoked, the leaver's
// WS connection may have already advanced past the source structure
// (PC moved to a different scope, NPC moved on). The farewell still
// broadcasts to the structure scope so anyone else in the room sees
// the bubble; the leaver may miss it client-side but the line lands
// in agent_action_log either way.
func (app *App) maybeFireFarewellOnExit(ctx context.Context, leavingID, huddleID string) {
	if leavingID == "" || huddleID == "" {
		return
	}
	if app.loadBusinessownerFlavor(ctx, leavingID) != "" {
		return
	}

	// Resolve the structure the huddle anchors to. Without this we
	// can't bound the keeper search.
	var structureID string
	if err := app.DB.QueryRow(ctx,
		`SELECT structure_id::text FROM scene_huddle WHERE id::text = $1`,
		huddleID,
	).Scan(&structureID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("businessowner: farewell huddle lookup %s: %v", huddleID, err)
		}
		return
	}
	if structureID == "" {
		return
	}

	var customerName string
	if err := app.DB.QueryRow(ctx,
		`SELECT display_name FROM actor WHERE id = $1`,
		leavingID,
	).Scan(&customerName); err != nil {
		log.Printf("businessowner: lookup leaver name for %s: %v", leavingID, err)
		return
	}

	rows, err := app.DB.Query(ctx,
		`SELECT a.id::text, a.display_name, aa.params->>'flavor'
		   FROM actor a
		   JOIN actor_attribute aa
		     ON aa.actor_id = a.id AND aa.slug = $1
		  WHERE a.inside_structure_id::text = $2
		    AND a.work_structure_id::text   = $2
		    AND (a.sleeping_until IS NULL OR a.sleeping_until <= NOW())
		    AND (a.break_until    IS NULL OR a.break_until    <= NOW())
		    AND a.id <> $3`,
		businessownerSlug, structureID, leavingID,
	)
	if err != nil {
		log.Printf("businessowner: farewell scan keepers at %s: %v", structureID, err)
		return
	}
	defer rows.Close()

	type keeper struct{ id, name, flavor string }
	var keepers []keeper
	for rows.Next() {
		var k keeper
		if err := rows.Scan(&k.id, &k.name, &k.flavor); err != nil {
			log.Printf("businessowner: farewell scan row at %s: %v", structureID, err)
			continue
		}
		if k.flavor == "" {
			continue
		}
		keepers = append(keepers, k)
	}
	if len(keepers) == 0 {
		return
	}

	cooldownMin := app.loadNonNegativeIntSetting(ctx,
		"businessowner_farewell_cooldown_minutes",
		defaultBusinessownerFarewellCooldownMinutes)

	for _, k := range keepers {
		if app.businessownerCooldownActive(ctx, k.id, leavingID, triggerFarewell, cooldownMin) {
			continue
		}
		text := renderBusinessownerPhrase(k.flavor, triggerFarewell, customerName)
		if text == "" {
			continue
		}
		app.fireBusinessownerSpeech(ctx, fireSpeechArgs{
			speakerID:    k.id,
			speakerName:  k.name,
			listenerID:   leavingID,
			listenerName: customerName,
			huddleID:     huddleID,
			structureID:  structureID,
			trigger:      triggerFarewell,
			text:         text,
		})
		app.upsertBusinessownerCooldown(ctx, k.id, leavingID, triggerFarewell)
	}
}

// fireSpeechArgs bundles the inputs to fireBusinessownerSpeech so the
// helper signature stays readable when handover / farewell pile on
// more triggers.
type fireSpeechArgs struct {
	speakerID    string
	speakerName  string
	listenerID   string
	listenerName string
	huddleID     string // customer's huddle — where the line gets attributed
	structureID  string
	trigger      string
	text         string
}

// fireBusinessownerSpeech emits the engine-authored hospitality line.
// Three side effects:
//   1. WS broadcast (npc_spoke) so connected clients render the bubble.
//   2. agent_action_log row with source='engine' for audit + perception
//      replay. The huddle_id is the LISTENER's huddle so the line lands
//      in their scene history; the keeper's own huddle (if different,
//      under the parallel-huddle bug) doesn't see it but doesn't need
//      to — the keeper isn't going to follow up with a tick on this
//      event, the engine already spoke for them.
//   3. Reactor-tick suppression flag stamp on the speaker (5s TTL) —
//      future handover-refactor / farewell PRs will read this before
//      invoking the keeper's agent on the same triggering event.
//      Unused on the greet path today (no follow-up reactor tick is
//      scheduled on entry), but stamped for consistency.
func (app *App) fireBusinessownerSpeech(ctx context.Context, args fireSpeechArgs) {
	if args.text == "" {
		return
	}
	now := time.Now().UTC()

	// 1. WS broadcast — talk panel + world-view speech bubble.
	data := map[string]interface{}{
		"npc_id":         args.speakerID,
		"name":           args.speakerName,
		"text":           args.text,
		"at":             now.Format(time.RFC3339),
		"structure_id":   args.structureID,
		"addressee_id":   args.listenerID,
		"addressee_name": args.listenerName,
	}
	if roomScope := app.actorPrivateRoomScope(ctx, args.speakerID); roomScope != "" {
		data["room_id"] = roomScope
	}
	app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: data})

	// 2. Audit / perception row. source='engine' lets later analysis
	// distinguish engine-authored from LLM-authored. payload carries
	// the same fields a normal speak commit would, plus trigger and
	// engine_authored=true so an inspector can tell at a glance.
	payload := map[string]any{
		"text":            args.text,
		"trigger":         args.trigger,
		"addressee_id":    args.listenerID,
		"addressee_name":  args.listenerName,
		"structure_id":    args.structureID,
		"engine_authored": true,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("businessowner: payload marshal %s→%s %s: %v",
			args.speakerID, args.listenerID, args.trigger, err)
		return
	}
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO agent_action_log
		     (occurred_at, source, actor_id, speaker_name,
		      action_type, huddle_id, payload, result)
		 VALUES ($1, 'engine', $2::uuid, $3, 'speak', $4::uuid, $5::jsonb, 'ok')`,
		now, args.speakerID, args.speakerName, args.huddleID, payloadBytes,
	); err != nil {
		log.Printf("businessowner: audit log %s→%s %s: %v",
			args.speakerID, args.listenerID, args.trigger, err)
	}

	// 3. Reactor-tick suppression — see businessownerRecentEngineSpeech.
	app.markBusinessownerEngineSpeech(args.speakerID)

	log.Printf("businessowner: %s engine-speaks to %s (%s): %q",
		args.speakerName, args.listenerName, args.trigger, args.text)
}

// Reactor-tick suppression flag. Keyed by actor.id (string), value is
// the UTC instant of the last engine-authored hospitality line for
// that actor. Reactor schedulers check via reactorTickSuppressedFor
// before invoking the agent on the SAME triggering event so the LLM
// doesn't follow up with a redundant "welcome friend!" speak.
//
// In-memory only — restart resets to empty, which is correct: an
// engine restart loses the in-flight reactor schedule too, so there's
// nothing to suppress.
var businessownerEngineSpeechAt sync.Map // map[string]time.Time

// markBusinessownerEngineSpeech stamps NOW() into the suppression map
// for actorID. Called from fireBusinessownerSpeech after every engine
// line.
func (app *App) markBusinessownerEngineSpeech(actorID string) {
	businessownerEngineSpeechAt.Store(actorID, time.Now())
}

// reactorTickSuppressedFor returns true when actorID has an engine
// hospitality stamp within engineSpeechSuppressionTTL. Reactor-tick
// schedulers consult this before invoking the keeper's agent.
//
// Stale entries (older than TTL) are not GC'd here — sync.Map doesn't
// support cheap iteration with mutation. They're effectively garbage-
// collected on next overwrite, which on a real village is "every
// hospitality fire on that keeper." Footprint stays bounded.
func (app *App) reactorTickSuppressedFor(actorID string) bool {
	v, ok := businessownerEngineSpeechAt.Load(actorID)
	if !ok {
		return false
	}
	stamp, ok := v.(time.Time)
	if !ok {
		return false
	}
	return time.Since(stamp) < engineSpeechSuppressionTTL
}
