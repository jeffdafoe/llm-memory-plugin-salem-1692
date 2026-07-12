package sim

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"
)

// visitor.go — transient salem-visitor archetype substrate (Phase 3
// Group A). Visitors are shared-VA NPCs that arrive on a random map edge,
// walk to the tavern (or any tagged fallback structure), hang around for
// hours-to-a-day, then walk off another edge. v1 used the chronicler's
// "outside news injection" role for the same purpose; v2 carries it as a
// first-class actor population.
//
// Substrate (this file): pools, sprite map, persona generation, spawn /
// despawn / cleanup Commands, edge-tile + destination pickers. The
// cascade driver (engine/sim/cascade/visitor.go) owns the ticker that
// pumps TickVisitorCascade on a cadence.
//
// Phase 1 scope: spawn / despawn / cleanup framework + perception cue.
// Payloads (news / rumor / letter / goods / quest_hook), recurring-visitor
// returner state — deferred. The feature is gated by default
// (VisitorSpawnChancePermille == 0), so deploying the framework with no
// admin opt-in is a no-op.
//
// Gates and cross-cascade behavior — visitors are KindNPCShared but use
// the VisitorState != nil predicate to skip:
//   - RecordInteraction (relationship_commands.go)
//   - FindConsolidationCandidates / ApplyConsolidation (C1)
//   - FindNarrativeConsolidationCandidates / ApplyNarrativeConsolidation /
//     StampNarrativeConsolidated (C2)
//   - EvaluateIdleBackstop scope
//
// What stays unchanged:
//   - Action-log subscribers (Spoke / Paid / Consumed / Delivered /
//     Walked) fire for visitors naturally — emit, log, atmosphere digest
//     picks them up.
//   - Acquaintance recording (persistent NPCs DO remember meeting a
//     visitor by display name; the entry survives the visitor's removal).
//   - Speech / huddle reactors stamp warrants when a visitor joins a
//     huddle, so visitors react to nearby PC / NPC speech the way any
//     other shared-VA NPC does.
//   - LLM routing: Actor.LLMAgent = VisitorAgentName ("salem-visitor")
//     points the cascade slices that DO fire (warranted reactor ticks at
//     huddle scope) at the shared salem-visitor VA. Per-visitor identity
//     is engine-injected per call from VisitorState; the VA itself stays
//     stateless across visitors (dream_mode='none').
//
// Concurrency: TickVisitorCascade runs on the world goroutine (issued via
// SendContext from the cascade ticker). Spawn / despawn / cleanup are
// three inline phases of a single Command — atomic from the rest of the
// world's perspective, no inter-phase SendContext round-trip.

// Default constants — fall back when WorldSettings.* zero values are
// observed. Tests that bypass the environment loader get these for free.
const (
	// DefaultVisitorSpawnChancePermille is the permille (per-thousand)
	// roll on every visitor cascade tick. 0 disables spawn entirely —
	// the deploy default so the framework is no-op until an admin opts
	// in by raising WorldSettings.VisitorSpawnChancePermille above 0.
	DefaultVisitorSpawnChancePermille = 0

	// Default stay-window bounds in minutes. Per-visitor stay is a
	// uniform random pull from [min, max] at spawn time.
	DefaultVisitorMinStayMinutes = 240
	DefaultVisitorMaxStayMinutes = 1440

	// DefaultVisitorMaxConcurrent caps simultaneous visitors. Zero on the
	// settings field falls back to this default — the documented halt-spawn
	// dial is VisitorSpawnChancePermille=0, not a sentinel here.
	DefaultVisitorMaxConcurrent = 2

	// DefaultVisitorTickInterval is the cadence the cascade driver pumps
	// TickVisitorCascade on when WorldSettings.VisitorTickInterval is
	// zero. 60s matches v1's runServerTickOnce cadence the visitor
	// handlers piggybacked on. Owned by cascade in spirit (the driver
	// reads it); defined here so the substrate's tests can construct a
	// realistic-looking settings block.
	DefaultVisitorTickInterval = 60 * time.Second

	// VisitorCleanupGraceMinutes is the slack past ExpiresAt before a
	// visitor is hard-removed. Lets a despawn walk complete (or fail-
	// and-stall) before the actor row disappears. 5 min covers a
	// cross-village walk at default speed.
	VisitorCleanupGraceMinutes = 5

	// VisitorAgentName is the shared memory-api VA slug every visitor's
	// Actor.LLMAgent points at. Provisioned once at operator setup on
	// memory-api with dream_mode='none' / learning_enabled=false /
	// cache_prompts=false. Per-visitor identity flows from VisitorState
	// + engine-injected perception preface; the VA itself stays stateless
	// across visitors.
	VisitorAgentName = "salem-visitor"

	// VisitorEdgeScanMaxDepth caps how far inward the edge-tile picker
	// scans from each map edge. 30 tiles is roughly 1/6 of map width
	// (200 tiles) — enough slack for villages with a setback approach
	// road, tight enough that "arriving from outside" still reads.
	VisitorEdgeScanMaxDepth = 30

	// surnameScrubMaxTries is the cap on profile re-rolls when scrubbing
	// visitor surnames against seated actors. 5 tries is enough headroom
	// in practice — collision residual rate at ~33% per-roll drops well
	// under 1% after 5 independent rolls.
	surnameScrubMaxTries = 5

	// VisitorPerceptionRadius is a reserved bounding-box (Chebyshev) tile radius
	// for a possible future "a traveler is about across the room" ambient observer
	// line — a wider scan than the observer cue that shipped. LLM-370's cue is
	// co-presence-scoped instead: it names a co-present traveler by archetype +
	// origin in the regular "## Around you" company line, off the same
	// ColocatedAudienceIDs earshot set every other nearby-actor line uses (see
	// perception.travelerCoPresentLabel). Disposition is deliberately NOT surfaced
	// to observers — it colors only the traveler's own self-identity preface. This
	// radius has no consumer yet; 2 tiles ≈ same-tile, adjacent, one-step-away.
	VisitorPerceptionRadius = 2

	// VisitorRumorLookback bounds how far back selectVisitorRumor reaches into
	// the action log for a grounded rumor to hand a spawning traveler (LLM-371).
	// The log itself is retention-bounded (DefaultActionLogRetention, 48h); this
	// tighter window keeps the carried word feeling like recent news ("lately",
	// "this week") rather than something stale from two days ago.
	VisitorRumorLookback = 24 * time.Hour
)

// VisitorTagTavern is the per-instance VillageObject tag the destination
// picker prefers. Tavern is the village's natural gathering point for
// outsiders; falls back to any tagged structure if no tavern is placed.
const VisitorTagTavern = "tavern"

// visitorNamePool — period-flavored full names for spawned visitors.
// Male-coded only because every available sprite family in
// visitorArchetypeSprite is male-coded (Merchant / Old Man / Man) — a
// female-coded name on a male sprite reads as a sprite-asset bug, not a
// stylistic choice. Surnames are chosen to not match Salem's seated
// villagers; the dynamic surname scrub in dispatchVisitorSpawn handles
// drift as new villagers are added or this pool grows.
var visitorNamePool = []string{
	"Master Whitcombe", "Brother Ashford", "Elias Drum",
	"Roger Standish", "Tobias Hewes", "Master Babbage",
	"Jonas Penhallow", "Jeremiah Soames", "Nathaniel Pratt",
	"Caleb Wendell", "Obadiah Brewster", "Ephraim Pollard",
	"Silas Withrow", "Asa Larkin", "Daniel Holcomb",
}

// visitorArchetypePool — closed-set archetypes a small village would
// actually receive. Adding an archetype here requires a matching
// visitorArchetypeSprite entry below — init() enforces.
var visitorArchetypePool = []string{
	"peddler", "traveling scholar", "messenger", "itinerant musician",
	"journeyman tinsmith", "circuit preacher", "wool-buyer",
	"pewterer", "wandering surgeon", "almanac-seller",
}

// visitorOriginPool — fictional/historical next-village strings. Drives
// the "from <origin>" prose in the perception cue and the LLM identity
// preface.
var visitorOriginPool = []string{
	"Boston", "Marblehead", "Andover", "Ipswich", "Topsfield",
	"Lynn", "Salem Town", "the next valley over",
	"the coast road", "Beverly", "Wenham", "Rowley",
}

// visitorDispositionPool — short adjectives the model can use to color
// voice on the per-call preface ("you are weary today" / "you are
// curious about Salem").
var visitorDispositionPool = []string{
	"weary", "warm", "reserved", "curious", "mercenary",
	"talkative", "wary", "earnest", "wry", "withdrawn",
}

// VisitorArchetypeSprite maps each archetype to an npc_sprite.name. The
// spawn / rehydrate paths resolve this name to the uuid-keyed SpriteID via
// the loaded catalog (visitorSpriteID) and stamp it on the Actor, so the
// client renders the traveler instead of drawing nothing (LLM-379). The
// init() below enforces every archetype in visitorArchetypePool has an entry
// here; an archetype-without-sprite makes the package fail to load, so the
// mismatch can't reach a running deploy.
//
// Sprite reuse across archetypes is intentional given the current
// shortage of period-appropriate sheets — variant suffixes (v00, v01)
// give visually-distinct options within a family but we run out before
// covering ten archetypes 1:1. Expand once the sprite library grows.
var VisitorArchetypeSprite = map[string]string{
	"peddler":             "Merchant B (v00)",
	"traveling scholar":   "Old Man A (v01)",
	"messenger":           "Man A (v00)",
	"itinerant musician":  "Man B (v00)",
	"journeyman tinsmith": "Merchant C (v00)",
	"circuit preacher":    "Old Man B (v00)",
	"wool-buyer":          "Merchant A (v01)",
	"pewterer":            "Merchant C (v01)",
	"wandering surgeon":   "Old Man A (v02)",
	"almanac-seller":      "Old Man B (v01)",
}

func init() {
	for _, archetype := range visitorArchetypePool {
		if _, ok := VisitorArchetypeSprite[archetype]; !ok {
			panic("sim/visitor: archetype " + archetype + " has no sprite mapping in VisitorArchetypeSprite")
		}
	}
}

// visitorSpriteID resolves an archetype's configured sprite NAME
// (VisitorArchetypeSprite) to the uuid-keyed SpriteID the client renders by.
// World.Sprites is keyed by the sprite id with the display name carried on
// Sprite.Name, so the lookup is a name scan of the loaded catalog. Spawn /
// rehydrate stamp the result on the Actor; without it a visitor ships with an
// empty sprite_id and the client draws nothing (LLM-379).
//
// ok=false when the archetype has no mapping (init() prevents that for pooled
// archetypes) or the named sheet isn't in the loaded catalog. The caller logs
// and ships spriteless rather than failing the spawn — an invisible traveler
// is a lesser fault than a dropped one.
func visitorSpriteID(w *World, archetype string) (SpriteID, bool) {
	if w == nil {
		return "", false
	}
	name, ok := VisitorArchetypeSprite[archetype]
	if !ok {
		return "", false
	}
	for id, sp := range w.Sprites {
		if sp != nil && sp.Name == name {
			return id, true
		}
	}
	return "", false
}

// VisitorCascadeTelemetry captures what each tick did. Used by the
// cascade driver for log lines and load-bearing for the substrate-side
// unit tests. Fields parallel the three inline phases of
// TickVisitorCascade.
//
// SpawnSkipChance currently lumps two cases — "feature disabled by
// config" (chance=0) AND "roll didn't fire on an enabled cascade." Until
// the separate SpawnDisabled counter lands (deferred from R1 review),
// consumers must branch on SpawnSkipReason to disambiguate; a dashboard
// or alert that treats SpawnSkipChance == 1 as "unlucky roll" without
// reading SpawnSkipReason will misreport disabled worlds as low-roll
// luck. See shared/notes/codebase/salem-engine-v2/visitor "Future work."
type VisitorCascadeTelemetry struct {
	DespawnsStarted  int // visitors whose despawn walk was issued this tick
	CleanedUp        int // visitor rows removed past ExpiresAt + grace
	Spawned          int // new visitors created (0 or 1 per tick)
	CircuitAdvanced  int // circuit legs advanced this tick (arrived + moved on, or first leg picked)
	CircuitToLodging int // visitors that turned to the lodging phase this tick (evening / rounds done)
	SpawnSkipChance  int // 1 if spawn skipped — chance=0 OR unlucky roll; check SpawnSkipReason
	SpawnSkipCap     int // 1 if spawn skipped because MaxConcurrent reached
	SpawnSkipReason  string
}

// VisitorTickInputs carries the per-tick inputs the dispatcher reads.
// Bundled as a struct so callers can construct a deterministic input set
// in tests (overriding Now, RNG, etc.) without piggybacking on world
// state.
//
// Now: wall-clock moment the tick fires. Drives ExpiresAt comparison for
// despawn / cleanup, and stamps spawn-time fields.
//
// Rand: random source. Drives spawn-chance roll, persona pick, edge-tile
// shuffle. Passed in so tests can use a deterministic seed; production
// uses a per-driver rand seeded once at registration.
type VisitorTickInputs struct {
	Now  time.Time
	Rand *rand.Rand
}

// TickVisitorCascade returns a Command that runs the three inline visitor
// phases in order: despawn → cleanup → spawn. Single round-trip per tick
// — all three phases run atomically inside one Command.Fn on the world
// goroutine.
//
// Despawn before cleanup ensures a visitor that just expired this tick
// gets a chance to start its return walk before being eligible for hard
// removal (which only fires after VisitorCleanupGraceMinutes past
// ExpiresAt, so first-tick removal is impossible regardless of ordering
// — the order is for clarity, not correctness).
//
// Spawn last so a freshly-created visitor doesn't get hit by despawn /
// cleanup on the same tick.
//
// Telemetry captures what the tick did; the cascade driver logs it
// when any field is non-zero.
func TickVisitorCascade(inputs VisitorTickInputs) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			t := VisitorCascadeTelemetry{}
			dispatchVisitorDespawn(w, inputs, &t)
			dispatchVisitorCleanup(w, inputs, &t)
			// Eco mode (LLM-313): visitors exist to be seen — pause the circuit and
			// SPAWNING while unwatched. Despawn/cleanup above keep running so existing
			// visitors age out normally; the circuit resumes stepping (and spawning
			// resumes) on the first tick after a player's presence stamp is fresh
			// again. Circuit before spawn so a freshly-spawned visitor (whose first
			// leg spawn already issued) isn't re-stepped on its spawn tick.
			if !ecoModeEngaged(w, inputs.Now) {
				dispatchVisitorCircuit(w, inputs, &t)
				dispatchVisitorSpawn(w, inputs, &t)
			}
			return t, nil
		},
	}
}

// dispatchVisitorDespawn finds visitors whose stay window has expired and
// who have not already had a despawn walk issued, then issues a MoveActor
// command targeting a fresh edge tile picked via pickVisitorEdgeTile. Each
// visitor may exit a different edge than they arrived on — narratively
// reads as "wandered off down the road," not "retraced their steps."
//
// VisitorState.Phase == VisitorPhaseDeparting is the one-shot gate. v1 used
// "actor still in a structure" as the despawn-eligibility proxy; v2's substrate
// has the dedicated phase so the gate doesn't entangle with whatever happened
// to the actor's InsideStructureID mid-walk (e.g. a stale back-ref clear).
//
// On any failure (no edge tile, no path) we still set the departing phase so
// the despawn isn't re-attempted every tick — cleanup will collect the
// stranded actor after the grace window regardless.
func dispatchVisitorDespawn(w *World, inputs VisitorTickInputs, t *VisitorCascadeTelemetry) {
	now := inputs.Now
	r := inputsRandOrDefault(inputs.Rand)
	for id, actor := range w.Actors {
		if actor == nil || actor.VisitorState == nil {
			continue
		}
		if actor.VisitorState.Phase == VisitorPhaseDeparting {
			continue
		}
		if !now.After(actor.VisitorState.ExpiresAt) {
			continue
		}
		// Pick a fresh anchor (any visitor destination) to validate the
		// edge tile is connected to the village core. If no destination
		// is placed at all, leave the visitor alone — cleanup will
		// collect them after the grace window.
		_, anchorTile, ok := pickVisitorDestination(w)
		if !ok {
			actor.VisitorState.Phase = VisitorPhaseDeparting
			continue
		}
		grid, err := buildWalkGrid(w)
		if err != nil {
			log.Printf("sim/visitor: dispatchDespawn build walk grid: %v", err)
			actor.VisitorState.Phase = VisitorPhaseDeparting
			continue
		}
		edgeTile, ok := pickVisitorEdgeTile(w, grid, anchorTile, r)
		if !ok {
			actor.VisitorState.Phase = VisitorPhaseDeparting
			continue
		}
		dest := NewPositionDestination(edgeTile)
		// LeaveHuddleFirst=true so a visitor mid-conversation can still
		// be despawn-dispatched (rather than the cascade silently stalling
		// because the visitor is gossiping). MoveActor's huddle-leave
		// emits HuddleLeft / HuddleConcluded events as appropriate.
		if _, err := MoveActor(id, dest, true, now).Fn(w); err != nil {
			// No path is typical for a visitor stranded somewhere
			// unreachable. Cleanup will hard-remove past the grace
			// window regardless.
			log.Printf("sim/visitor: dispatchDespawn MoveActor %s: %v", id, err)
		}
		actor.VisitorState.Phase = VisitorPhaseDeparting
		t.DespawnsStarted++
	}
}

// dispatchVisitorCleanup hard-removes visitor actor rows whose ExpiresAt
// passed more than VisitorCleanupGraceMinutes ago. Position-agnostic — a
// visitor stranded with no walk path still gets cleaned up after the
// grace window so we don't leak rows. Emits ActorDeparted before delete
// so subscribers can capture the departure event.
func dispatchVisitorCleanup(w *World, inputs VisitorTickInputs, t *VisitorCascadeTelemetry) {
	now := inputs.Now
	r := inputsRandOrDefault(inputs.Rand)
	grace := time.Duration(VisitorCleanupGraceMinutes) * time.Minute
	for id, actor := range w.Actors {
		if actor == nil || actor.VisitorState == nil {
			continue
		}
		if !now.After(actor.VisitorState.ExpiresAt.Add(grace)) {
			continue
		}
		// LLM-372: a promoted returner leaving schedules its comeback — stamp
		// last_seen + next_return_at on the durable row before the actor row goes.
		// A one-shot (unpromoted) visitor has no RecurringID and is simply gone.
		if actor.VisitorState.RecurringID != "" {
			w.scheduleReturnerDeparture(RecurringVisitorID(actor.VisitorState.RecurringID),
				now, r, w.Settings.VisitorReturnMinDays, w.Settings.VisitorReturnMaxDays)
		}
		// Capture before-removal state for the event.
		evt := &ActorDeparted{
			ActorID:               id,
			DisplayName:           actor.DisplayName,
			LastInsideStructureID: actor.InsideStructureID,
			LastPosition:          actor.Pos,
			VisitorContext:        cloneVisitorState(actor.VisitorState),
			At:                    now,
		}
		// Emit BEFORE removal so subscribers can still look up the actor
		// in w.Actors mid-event if they need to. The event already carries
		// the load-bearing pre-removal fields directly, but the actor row
		// remains reachable for any subscriber that wants more (e.g. a
		// future debug logger reading Acquaintances).
		w.emit(evt)
		// Remove from secondary indices. setActorInsideStructure handles
		// outdoorActors / actorsByStructure transitions when we drop the
		// actor's inside flag; the actorsByHuddle index doesn't have a
		// removal helper, so detach inline.
		if actor.CurrentHuddleID != "" {
			if members, ok := w.actorsByHuddle[actor.CurrentHuddleID]; ok {
				delete(members, id)
				if len(members) == 0 {
					delete(w.actorsByHuddle, actor.CurrentHuddleID)
				}
			}
		}
		setActorInsideStructure(w, actor, "")
		delete(w.outdoorActors, id)
		delete(w.Actors, id)
		t.CleanedUp++
	}
}

// selectVisitorRumor picks one grounded rumor for a spawning traveler to carry
// (LLM-371). It draws from the in-memory action log — the same recent-happenings
// ring the atmosphere digest reads — filtered to rumor-worthy beats within
// VisitorRumorLookback whose subject is a real resident (not another visitor, not
// the PC, not decorative), and renders one to a diegetic past-tense clause. This
// is the v2-faithful stand-in for the ticket's "recent village_event": engine-v2
// has no village_event table, but the action log records every actor's real beats
// (a stateful keeper's delivery / a shared-VA vendor's sale alike), so a traveler
// can carry checkable word about anyone in the village. Returns "" when nothing
// rumor-worthy is on hand — the caller leaves Payload empty and the preface drops
// the clause. Random pick (not most-recent) so back-to-back spawns don't all echo
// the same freshest beat. Runs on the world goroutine (called from
// dispatchVisitorSpawn), so reading w.ActionLog / w.Actors is race-free.
func selectVisitorRumor(w *World, r *rand.Rand, now time.Time) string {
	if w == nil || len(w.ActionLog) == 0 {
		return ""
	}
	cutoff := now.Add(-VisitorRumorLookback)
	var candidates []string
	for _, e := range w.ActionLog {
		if e.OccurredAt.Before(cutoff) {
			continue
		}
		subject := w.Actors[e.ActorID]
		if subject == nil || subject.VisitorState != nil {
			continue // subject must be a resident villager, not a passing traveler
		}
		if subject.Kind == KindPC || subject.Kind == KindDecorative {
			continue // rumors are about the village's own, not the player or props
		}
		if clause := renderRumorClause(w, e); clause != "" {
			candidates = append(candidates, clause)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[r.Intn(len(candidates))]
}

// renderRumorClause turns one action-log entry into the diegetic, past-tense
// clause a traveler carries as a rumor — "Ezekiel Crane turned out a plow for the
// Hale farm" — or "" for a beat that doesn't make a rumor worth carrying. The
// preface owns the "Word reached you on the road that …" framing
// (renderTravelerPreface); this returns just the grounded fact. Deliberately a
// curated allow-set of the socially legible economic beats: the private
// (consumed / took_break), the dull (walked / departed), the utterance itself
// (spoke — long, contextual, and already carried by the speaker's own memory),
// and the feed-only negotiation types (offered / declined / countered, filtered
// everywhere NPC-facing) all render "". Amounts and exact coin counts are dropped
// on purpose — scene, not ledger. The subject name is resolved by the caller's
// guard (w.Actors[e.ActorID] non-nil), re-checked here for safety.
func renderRumorClause(w *World, e ActionLogEntry) string {
	subject := w.Actors[e.ActorID]
	if subject == nil || subject.DisplayName == "" {
		return ""
	}
	name := subject.DisplayName
	switch e.ActionType {
	case ActionTypePaid:
		if e.CounterpartyName == "" {
			return "" // a payment to no one named isn't a rumor worth carrying
		}
		clause := name + " settled up with " + e.CounterpartyName
		if e.Text != "" {
			clause += " over " + e.Text
		}
		return clause
	case ActionTypeDelivered:
		if e.Text == "" {
			return ""
		}
		clause := name + " turned out " + e.Text
		if e.CounterpartyName != "" {
			clause += " for " + e.CounterpartyName
		}
		return clause
	case ActionTypeLabored:
		if e.CounterpartyName != "" {
			return name + " put in a day's work for " + e.CounterpartyName
		}
		return name + " took on a piece of work"
	case ActionTypeHired:
		if e.CounterpartyName == "" {
			return ""
		}
		return name + " took " + e.CounterpartyName + " on for a job"
	case ActionTypeSolicitedWork:
		if e.CounterpartyName != "" {
			return name + " went looking to work for " + e.CounterpartyName
		}
		return name + " was about looking for work"
	case ActionTypeGathered:
		if e.Text == "" {
			return ""
		}
		clause := name + " was out gathering " + e.Text
		if e.CounterpartyName != "" {
			clause += " at " + WithDefiniteArticle(e.CounterpartyName)
		}
		return clause
	case ActionTypeRepairing:
		if e.Text != "" {
			return name + " was mending " + WithDefiniteArticle(e.Text)
		}
		return name + " was busy at repairs"
	default:
		return ""
	}
}

// dispatchVisitorSpawn rolls the per-tick spawn chance and — when it
// fires and the concurrent cap isn't reached — generates a persona,
// picks an arrival edge tile + tavern destination, inserts a fresh
// Actor + VisitorState, seeds need rows, and issues a MoveActor
// targeting the destination.
//
// Single gate: WorldSettings.VisitorSpawnChancePermille (default 0)
// disables spawn entirely. Other failure paths (no destination placed,
// no walkable edge tile) log + skip the cycle without setting any
// telemetry counters — they're configuration / map issues, not skip
// reasons of architectural interest.
func dispatchVisitorSpawn(w *World, inputs VisitorTickInputs, t *VisitorCascadeTelemetry) {
	chance := w.Settings.VisitorSpawnChancePermille
	if chance < 0 {
		chance = 0
	}
	if chance > 1000 {
		chance = 1000
	}
	if chance == 0 {
		t.SpawnSkipChance = 1
		t.SpawnSkipReason = "disabled (chance=0)"
		return
	}
	// Daytime spawns only (LLM-373): a traveler arrives on the road in daylight so
	// it has business hours left to make its rounds before nightfall. A visitor that
	// spawned after dusk would find every shop shut and skip straight to seeking a
	// bed — off-key for the "peddler making the rounds" arc. Gate on the world's
	// dawn/dusk window; a world with no usable dawn/dusk clock spawns anytime
	// (fail-open, matching how the perception evening gates degrade on a bad clock).
	if dawn, dusk, ok := worldDawnDuskMinutes(w); ok {
		nowMin := localMinuteOfDay(w, inputs.Now)
		if nowMin < dawn || nowMin >= dusk {
			t.SpawnSkipReason = "outside daytime spawn window"
			return
		}
	}
	r := inputsRandOrDefault(inputs.Rand)
	if r.Intn(1000) >= chance {
		t.SpawnSkipChance = 1
		t.SpawnSkipReason = "roll didn't fire"
		return
	}
	// Zero / unset → fall back to default (matches every other settings
	// field in this file). The documented "halt spawn" admin dial is
	// VisitorSpawnChancePermille=0 (already gated above), not
	// VisitorMaxConcurrent — keeping both as halt-spawn signals would
	// give two ways to say the same thing.
	maxConcurrent := w.Settings.VisitorMaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultVisitorMaxConcurrent
	}
	current := 0
	for _, a := range w.Actors {
		if a != nil && a.VisitorState != nil {
			current++
		}
	}
	if current >= maxConcurrent {
		t.SpawnSkipCap = 1
		t.SpawnSkipReason = fmt.Sprintf("at cap %d/%d", current, maxConcurrent)
		return
	}

	destID, destAnchor, ok := pickVisitorDestination(w)
	if !ok {
		log.Printf("sim/visitor: dispatchSpawn: no destination structure placed; skipping")
		return
	}
	grid, err := buildWalkGrid(w)
	if err != nil {
		log.Printf("sim/visitor: dispatchSpawn build walk grid: %v", err)
		return
	}
	edgeTile, ok := pickVisitorEdgeTile(w, grid, destAnchor, r)
	if !ok {
		log.Printf("sim/visitor: dispatchSpawn: no valid edge tile this cycle; skipping")
		return
	}

	// Persona. A due returner (LLM-372) comes back as the SAME person — prefer one
	// over a fresh stranger, reusing its established persona verbatim (and skipping
	// the surname scrub, since the name is already in play and unique enough).
	// Only READ the returner here; the durable mutation (beginReturnerVisit — bump
	// visit count, clear next_return_at) is deferred until AFTER the actor is
	// committed below, so a spawn that bails out (ID-mint exhaustion) leaves the
	// returner still due to try again rather than consumed-but-not-arrived.
	// Otherwise roll a new persona and scrub its surname against seated villagers.
	var returnerID string
	var dueReturner *RecurringVisitor
	var profile visitorProfile
	if rv, ok := w.pickDueReturner(inputs.Now); ok {
		profile = visitorProfile{Name: rv.Name, Archetype: rv.Archetype, Origin: rv.Origin, Disposition: rv.Disposition}
		returnerID = string(rv.ID)
		dueReturner = rv
	} else {
		existing := loadActorSurnames(w)
		profile = generateVisitorProfile(r)
		for tries := 0; tries < surnameScrubMaxTries; tries++ {
			if !existing[extractSurname(profile.Name)] {
				break
			}
			profile = generateVisitorProfile(r)
		}
		if existing[extractSurname(profile.Name)] {
			log.Printf("sim/visitor: dispatchSpawn: surname for %q still collides after %d tries; shipping anyway",
				profile.Name, surnameScrubMaxTries)
		}
	}

	// Departure is schedule-anchored to the next daybreak (LLM-373), replacing the
	// old random [min,max] stay: a traveler stays the night and leaves at first
	// light. Default one night; a multi-night stay is a later setting. The
	// Foundation despawn/cleanup reconcile is unchanged — it just reads this
	// ExpiresAt. nextDaybreak fails open to a one-day fallback if the world has no
	// usable dawn/dusk clock, so a bad clock can't mint a never-expiring visitor.
	expiresAt := nextDaybreak(w, inputs.Now)

	// Display name uniqueness — "Name the Archetype" with a numeric
	// disambiguator on collision. Collisions with persistent NPCs are
	// unlikely given the period names; the suffix covers same-tick
	// concurrent visitors with the same pull.
	displayName := fmt.Sprintf("%s the %s", profile.Name, profile.Archetype)
	if displayNameInUse(w, displayName) {
		displayName = fmt.Sprintf("%s the %s (%d)", profile.Name, profile.Archetype, inputs.Now.Unix()%10000)
	}

	// Mint actor ID with collision retry. 8 hex chars = 32 bits of
	// entropy — collision is astronomically unlikely at Salem scale but
	// not impossible, and a collision means silently replacing an
	// existing actor row. The retry loop checks against w.Actors and
	// caps at 10 attempts; on exhaustion (genuinely shouldn't happen),
	// log + skip this spawn.
	id := ActorID("")
	for attempt := 0; attempt < 10; attempt++ {
		candidate := ActorID(newVisitorActorID())
		if _, exists := w.Actors[candidate]; !exists {
			id = candidate
			break
		}
	}
	if id == "" {
		log.Printf("sim/visitor: dispatchSpawn: actor-ID minting exhausted 10 retries; skipping")
		return
	}
	// Means to pay (LLM-373): the traveler spawns carrying a pack of trade goods
	// and a small purse. This is both its lodging payment — a room is bought by
	// barter (pay_with_item), per LLM-353, or by coin — and its trade stock for the
	// circuit. Without it, good prompting still yields an empty promise: a booking
	// it can't complete with a tool call.
	pack, purse := seedVisitorPack(r)
	// Give the traveler a visible form: resolve its archetype's sprite to the
	// uuid-keyed SpriteID the client draws by (LLM-379). "" on miss ships the
	// visitor spriteless — logged, but the spawn still proceeds.
	spriteID, ok := visitorSpriteID(w, profile.Archetype)
	if !ok {
		log.Printf("sim/visitor: dispatchSpawn: no sprite for archetype %q (name=%q); shipping spriteless",
			profile.Archetype, VisitorArchetypeSprite[profile.Archetype])
	}
	visitor := &Actor{
		ID:                id,
		DisplayName:       displayName,
		Kind:              KindNPCShared,
		LLMAgent:          VisitorAgentName,
		Pos:               edgeTile,
		InsideStructureID: "",
		SpriteID:          spriteID,
		Facing:            "south",
		Needs:             seedVisitorNeeds(),
		Inventory:         pack,
		Coins:             purse,
		VisitorState: &VisitorState{
			Archetype:   profile.Archetype,
			Origin:      profile.Origin,
			Disposition: profile.Disposition,
			ExpiresAt:   expiresAt,
			// Arriving: on the road, walking in to the first stop on its circuit. The
			// circuit dispatcher flips it to making_rounds on arrival (LLM-373).
			Phase: VisitorPhaseArriving,
			// A returner's PERSONA is stable across visits, but its road-rumor is
			// deliberately fresh each trip: a peddler coming back through carries the
			// latest news, not last season's. So Payload is (re)selected here every
			// spawn and is NOT stored on the recurring_visitor row (LLM-372).
			Payload:     selectVisitorRumor(w, r, inputs.Now),
			RecurringID: returnerID, // "" for a fresh stranger; set for a returning traveler
		},
		State: StateIdle,
	}
	w.Actors[id] = visitor
	w.outdoorActors[id] = struct{}{}

	// The spawn is committed — now record the returner's arrival (bump visit count,
	// clear next_return_at) on the durable row it came back as (LLM-372).
	if dueReturner != nil {
		dueReturner.beginReturnerVisit()
		log.Printf("sim/visitor: returner arrived — %s the %s from %s (rvis=%s, id=%s, visit #%d)",
			dueReturner.Name, dueReturner.Archetype, dueReturner.Origin, dueReturner.ID, id, dueReturner.VisitCount)
	}

	// First circuit leg (LLM-373): head to the nearest open business to trade and
	// pass news. If none is open right now (keepers off-post), fall back to the
	// town's gathering place (the tavern / destID) so the traveler still heads into
	// the village core; the circuit re-routes to a shop on a later tick once one is
	// tending. RoundTarget stays "" in the fallback so the circuit knows it has not
	// been sent to a business yet.
	target := destID
	if biz, ok := pickNextOpenBusiness(w, visitor.Pos, nil); ok {
		target = biz
		visitor.VisitorState.RoundTarget = biz
	}

	// Walk toward the target. Entry policy decides whether the visitor walks into
	// the interior (open / default) or stops at a visitor slot (owner-only /
	// closed) — issueVisitorWalk hands MoveActor a StructureEnter destination and
	// falls back to StructureVisit on rejection. leaveHuddle=false: a freshly-spawned
	// visitor is not in a conversation. A dead-end leaves it at the edge tile for
	// despawn/cleanup to collect after the stay window.
	issueVisitorWalk(w, id, target, false, inputs.Now)
	t.Spawned++
	log.Printf("sim/visitor: spawn %s (id=%s, archetype=%s, origin=%s, disposition=%s, depart=%s, target=%s, edge=(%d,%d))",
		displayName, id, profile.Archetype, profile.Origin, profile.Disposition,
		expiresAt.Format("2006-01-02 15:04"), target, edgeTile.X, edgeTile.Y)
}

// rehydrateVisitorsOnLoad restores the durable in-flight visitor mirror
// (LLM-369) into World.Actors so a restart resumes travelers instead of dropping
// them — the reverse of ActorsRepo.SaveSnapshot's filter that keeps visitors out
// of the actor aggregate. Runs from FinalizeLoad AFTER rebuildIndices (so the
// secondary-index maps exist to append to); world-goroutine-only (FinalizeLoad
// runs before Run starts).
//
// Reconcile against the wall-clock ExpiresAt: a visitor still within its stay
// window is rebuilt into a live Actor at its checkpointed tile; one whose stay
// elapsed while the engine was down is dropped — not resurrected for another
// stay, not walked off — and its row is swept from the table on the next
// checkpoint (absent from cp.Actors -> delete-stale). A dropped / dup / loader-
// inconsistent row is logged, never fatal: a live village must boot, and a lost
// visitor is data-clean (transient by design). Such a row never arises from a
// consistent checkpoint (a visitor and the rest of the world write in the SAME
// SaveWorld Tx); it means a manual / out-of-band edit.
//
// The Actor is reconstructed the way dispatchVisitorSpawn mints one —
// KindNPCShared, the shared salem-visitor VA, needs seeded to 0, StateIdle —
// differing in the persisted identity / position / VisitorState and, from LLM-373,
// the day-plan pack / purse / booked-room grant restored off the plan jsonb.
// Secondary-index membership (outdoorActors / actorsByStructure) is set to match
// the loaded InsideStructureID.
func (w *World) rehydrateVisitorsOnLoad(ctx context.Context) error {
	visitors, err := w.repo.Visitors.LoadAll(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var restored, elapsed int
	for id, lv := range visitors {
		if lv == nil {
			continue
		}
		if lv.ID != id {
			log.Printf("sim: rehydrate visitor: map key %q != LoadedVisitor.ID %q (loader inconsistency) — dropping", id, lv.ID)
			continue
		}
		if lv.VisitorState == nil {
			log.Printf("sim: rehydrate visitor %q: nil VisitorState — dropping", id)
			continue
		}
		if _, exists := w.Actors[id]; exists {
			log.Printf("sim: rehydrate visitor %q: id already present in loaded actors — dropping visitor row", id)
			continue
		}
		if !lv.VisitorState.Phase.Valid() {
			log.Printf("sim: rehydrate visitor %q: invalid phase %q — dropping", id, lv.VisitorState.Phase)
			continue
		}
		// inside_structure_id is a soft ref (no FK). The normal actor structure-ref
		// validation ran in LoadWorld before visitors are added here, so validate it
		// now — a dangling ref (only possible from an out-of-band edit; a consistent
		// checkpoint writes structures and visitors in one Tx) would otherwise index
		// the visitor under a structure that doesn't exist.
		if lv.InsideStructureID != "" {
			if w.Structures[lv.InsideStructureID] == nil {
				log.Printf("sim: rehydrate visitor %q: inside_structure_id %q not among loaded structures — dropping", id, lv.InsideStructureID)
				continue
			}
		}
		if now.After(lv.VisitorState.ExpiresAt) {
			elapsed++
			continue
		}
		// Pack / purse / booked-room grant ride on the plan jsonb (LLM-373), restored
		// here so a mid-stay deploy resumes the traveler with its wares to pay with
		// and its room still booked. nil maps seed empty (a traveler that carried
		// nothing, or a row written before the plan column existed).
		inventory := lv.Inventory
		if inventory == nil {
			inventory = map[ItemKind]int{}
		}
		roomAccess := lv.RoomAccess
		if roomAccess == nil {
			roomAccess = map[RoomAccessKey]*RoomAccess{}
		}
		// Sprite is derived from the archetype (persisted on VisitorState), not stored
		// separately — resolve it the same way spawn does so a restart doesn't strand
		// the traveler invisible (LLM-379).
		spriteID, ok := visitorSpriteID(w, lv.VisitorState.Archetype)
		if !ok {
			log.Printf("sim: rehydrate visitor %q: no sprite for archetype %q (name=%q); restoring spriteless",
				id, lv.VisitorState.Archetype, VisitorArchetypeSprite[lv.VisitorState.Archetype])
		}
		actor := &Actor{
			ID:                id,
			DisplayName:       lv.DisplayName,
			Kind:              KindNPCShared,
			LLMAgent:          VisitorAgentName,
			Pos:               lv.Pos,
			InsideStructureID: lv.InsideStructureID,
			SpriteID:          spriteID,
			Facing:            "south",
			Needs:             seedVisitorNeeds(),
			Inventory:         inventory,
			Coins:             lv.Coins,
			RoomAccess:        roomAccess,
			VisitorState:      lv.VisitorState,
			State:             StateIdle,
		}
		w.Actors[id] = actor
		if actor.InsideStructureID == "" {
			w.outdoorActors[id] = struct{}{}
		} else {
			if w.actorsByStructure[actor.InsideStructureID] == nil {
				w.actorsByStructure[actor.InsideStructureID] = make(map[ActorID]struct{})
			}
			w.actorsByStructure[actor.InsideStructureID][id] = struct{}{}
		}
		restored++
	}
	if restored > 0 || elapsed > 0 {
		log.Printf("sim: rehydrated %d in-flight visitor(s); dropped %d whose stay elapsed while down", restored, elapsed)
	}
	return nil
}

// DefaultVisitorRoundDwellMinutes is how long a traveler lingers at each business
// on its circuit before moving on — long enough for a real exchange with the
// keeper (conversation runs on the reactor cadence while it stands there), short
// enough to visit several shops over an afternoon. Wall-clock minutes: the Salem
// clock is real-time.
const DefaultVisitorRoundDwellMinutes = 20

// dispatchVisitorCircuit steps each in-flight traveler's day-plan (LLM-373). The
// ENGINE owns the movement and the phase transitions; the shared salem-visitor VA
// owns only the speech that happens once the traveler is co-present with a keeper.
//
// Per phase:
//   - arriving / making_rounds (and a legacy 'present' row): the daytime business
//     circuit. Walk to the nearest open, unvisited business; on arrival linger
//     (DwellUntil) so a conversation has room to happen, then move on to the next.
//     When the civil evening opens — or no open business remains and it is late —
//     turn to the lodging phase.
//   - lodging: the evening. The engine walks the traveler to the tavern; from there
//     the evening-leisure + seek-a-bed perception cues drive the booking. The engine
//     does not force the tool call — a traveler that won't book is a prompt bug.
//   - departing: owned by dispatchVisitorDespawn; skipped here.
//
// MUST run inside a Command.Fn on the world goroutine (mutates actors + issues
// MoveActor). A walk in flight (MoveIntent != nil) is left to finish.
func dispatchVisitorCircuit(w *World, inputs VisitorTickInputs, t *VisitorCascadeTelemetry) {
	now := inputs.Now
	for id, actor := range w.Actors {
		if actor == nil || actor.VisitorState == nil {
			continue
		}
		vs := actor.VisitorState
		if vs.Phase == VisitorPhaseDeparting {
			continue // despawn owns it
		}
		if vs.Phase == VisitorPhaseLodging {
			stepVisitorLodging(w, actor, now)
			continue
		}
		// Daytime circuit phases: arriving / making_rounds / present (legacy).
		if !visitorDaytime(w, now) {
			// Evening: stop the rounds and turn to lodging. Clear the in-flight round
			// leg so the evening cues (not a stale shop target) drive the traveler on.
			vs.Phase = VisitorPhaseLodging
			vs.RoundTarget = ""
			vs.DwellUntil = nil
			t.CircuitToLodging++
			stepVisitorLodging(w, actor, now)
			continue
		}
		if actor.MoveIntent != nil {
			continue // still walking to its current target — let it finish
		}
		if vs.Phase == VisitorPhaseArriving || vs.Phase == VisitorPhasePresent {
			vs.Phase = VisitorPhaseMakingRounds
		}
		// Arrived (or the move ended): record the visit and start the dwell, then
		// linger until DwellUntil before advancing to the next shop.
		if vs.RoundTarget != "" {
			switch {
			case vs.DwellUntil == nil:
				// Arrived at the round target (the walk finished). Mark the shop dealt-with
				// and linger — whether the walk ended INSIDE the structure or at its
				// doorstep visitor slot (owner-only / entry-policy fallback). The arrival
				// business huddle forms either way, so the traveler is co-present with the
				// keeper and the VA can trade / pass news from the slot just as from inside.
				// Marking visited UNCONDITIONALLY is what breaks the re-arrival loop
				// (LLM-379): a doorstep arrival left unmarked would drop the leg and re-pick
				// the SAME shop every tick, walking back to it forever. The rare walk that
				// failed outright dwells one idle interval here, then advances — never loops.
				vs.VisitedBusinesses = appendUniqueStructure(vs.VisitedBusinesses, vs.RoundTarget)
				dwell := now.Add(time.Duration(DefaultVisitorRoundDwellMinutes) * time.Minute)
				vs.DwellUntil = &dwell
				continue
			case now.Before(*vs.DwellUntil):
				continue // still lingering
			default:
				vs.RoundTarget = ""
				vs.DwellUntil = nil
			}
		}
		// Between legs: route to the next open, unvisited business.
		if vs.RoundTarget == "" {
			next, ok := pickNextOpenBusiness(w, actor.Pos, vs.VisitedBusinesses)
			if !ok {
				continue // no open unvisited shop right now — idle until evening / retry
			}
			vs.RoundTarget = next
			issueVisitorWalk(w, id, next, true, now)
			t.CircuitAdvanced++
		}
	}
}

// stepVisitorLodging walks a lodging-phase traveler to the tavern so it is
// co-present with the innkeeper (arrival forms the business huddle → a turn where
// the seek-a-bed cue fires) and, once booked, is inside its inn to bed down. The
// engine issues the walk; the model books. No-op while the traveler is already
// walking, asleep, inside the inn, or standing at its door — so it is never
// re-walked or woken.
func stepVisitorLodging(w *World, actor *Actor, now time.Time) {
	if actor.State == StateSleeping {
		return // abed — the lodger sleep arm owns it
	}
	tavern, ok := findLodgingStructure(w)
	if !ok {
		return // no inn placed — nothing to do
	}
	if actor.InsideStructureID == tavern {
		return // inside the inn — the seek-a-bed / evening cues + lodger sleep arm take over
	}
	if moveIntentTargetsStructure(actor.MoveIntent, tavern) {
		return // already walking in — let it finish
	}
	// (Re)issue a walk to the inn, superseding any stale daytime move (a shop leg
	// still in flight when dusk fell — LeaveHuddleFirst pulls the traveler off it).
	// Keep aiming for the interior (StructureEnter) rather than stopping at the
	// doorstep, so the traveler ends up co-present with the innkeeper where the
	// seek-a-bed cue fires and it can book — not stranded outside.
	issueVisitorWalk(w, actor.ID, tavern, true, now)
}

// moveIntentTargetsStructure reports whether an in-flight move is aimed at
// structureID — a StructureEnter or StructureVisit destination both carry the id.
// Used to tell "already walking to the inn" from a stale daytime leg (LLM-373).
func moveIntentTargetsStructure(mi *MoveIntent, structureID StructureID) bool {
	return mi != nil && mi.Destination.StructureID != nil && *mi.Destination.StructureID == structureID
}

// issueVisitorWalk sends a visitor toward a structure, entering the interior when
// entry policy allows and falling back to a loiter slot outside otherwise — the
// same StructureEnter → StructureVisit fallback the spawn walk uses. leaveHuddle
// pulls the visitor out of any conversation first (true when moving on from a shop
// or to the inn). A dead-end (no path either way) is logged, never fatal.
func issueVisitorWalk(w *World, id ActorID, target StructureID, leaveHuddle bool, now time.Time) {
	dest := NewStructureEnterDestination(target)
	if _, err := MoveActor(id, dest, leaveHuddle, now).Fn(w); err != nil {
		dest = NewStructureVisitDestination(target)
		if _, err := MoveActor(id, dest, leaveHuddle, now).Fn(w); err != nil {
			log.Printf("sim/visitor: circuit: %s no walk to %s: %v", id, target, err)
		}
	}
}

// pickNextOpenBusiness returns the nearest open, unvisited business to `from` — a
// structure kept by a present businessowner, excluding the inn (the evening lodging
// venue, not a daytime round stop) and anything already visited. keeperPresentAt is
// the LLM-366 shut-status check, so a shut shop is skipped and may be picked up on a
// later tick once its keeper is tending. Distance is Chebyshev to the structure's
// placement; ties break on the smaller structure id for determinism. ok=false when
// no eligible business is open.
func pickNextOpenBusiness(w *World, from TilePos, visited []StructureID) (StructureID, bool) {
	visitedSet := make(map[StructureID]bool, len(visited))
	for _, s := range visited {
		visitedSet[s] = true
	}
	var bestID StructureID
	bestDist := -1
	for _, keeper := range w.Actors {
		if keeper == nil || keeper.BusinessownerState == nil || keeper.WorkStructureID == "" {
			continue
		}
		sid := keeper.WorkStructureID
		if visitedSet[sid] || structureIsLodging(w, sid) {
			continue
		}
		if !keeperPresentAt(w, sid) {
			continue // shut right now — a later tick may find it tending
		}
		vobj, ok := villageObjectForStructureOnly(w, sid)
		if !ok || vobj == nil {
			continue
		}
		d := from.Chebyshev(vobj.Pos.Tile())
		if bestDist == -1 || d < bestDist || (d == bestDist && sid < bestID) {
			bestDist = d
			bestID = sid
		}
	}
	return bestID, bestDist != -1
}

// structureIsLodging reports whether a structure is the village's inn (its backing
// VillageObject carries the "lodging" tag) — the evening venue the circuit excludes
// from its daytime rounds.
func structureIsLodging(w *World, sid StructureID) bool {
	vobj, ok := villageObjectForStructureOnly(w, sid)
	if !ok || vobj == nil {
		return false
	}
	return vobj.HasTag("lodging")
}

// appendUniqueStructure appends id to s only if not already present — the visited
// set stays deduped as the circuit revisits the same RoundTarget across ticks.
func appendUniqueStructure(s []StructureID, id StructureID) []StructureID {
	for _, x := range s {
		if x == id {
			return s
		}
	}
	return append(s, id)
}

// worldDawnDuskMinutes returns the world's dawn and dusk as minute-of-day in the
// village timezone, or ok=false when the configured DawnTime/DuskTime don't parse
// (callers fail open). Mirrors effectiveShiftWindow's unscheduled branch.
func worldDawnDuskMinutes(w *World) (dawn, dusk int, ok bool) {
	dawnH, dawnM, err := ParseHM(w.Settings.DawnTime)
	if err != nil {
		return 0, 0, false
	}
	duskH, duskM, err := ParseHM(w.Settings.DuskTime)
	if err != nil {
		return 0, 0, false
	}
	return dawnH*60 + dawnM, duskH*60 + duskM, true
}

// visitorDaytime reports whether now falls in the village's business hours
// [dawn, dusk) — when the circuit runs. Fails open to daytime on an unusable
// dawn/dusk clock (the circuit keeps running rather than lodging on a bad clock;
// ExpiresAt still bounds the stay).
func visitorDaytime(w *World, now time.Time) bool {
	dawn, dusk, ok := worldDawnDuskMinutes(w)
	if !ok {
		return true
	}
	nowMin := localMinuteOfDay(w, now)
	return nowMin >= dawn && nowMin < dusk
}

// nextDaybreak returns the next daybreak instant (the village dawn minute in the
// world timezone strictly after now) — the traveler's schedule-anchored departure
// deadline (LLM-373). Fails open to a one-day fallback if the world has no usable
// dawn clock, so a misconfiguration can't mint a never-expiring visitor.
func nextDaybreak(w *World, now time.Time) time.Time {
	dawn, _, ok := worldDawnDuskMinutes(w)
	loc := w.Settings.Location
	if !ok || loc == nil {
		return now.Add(24 * time.Hour)
	}
	local := now.In(loc)
	dawnInstant := time.Date(local.Year(), local.Month(), local.Day(), dawn/60, dawn%60, 0, 0, loc)
	if !dawnInstant.After(now) {
		dawnInstant = dawnInstant.AddDate(0, 0, 1)
	}
	return dawnInstant
}

// visitorWareKinds are the trade goods a traveler may spawn carrying (LLM-373),
// drawn from the seeded item catalog so a keeper values them for barter. A small
// varied pack reads as a peddler's stock and, with the purse, pays for a room
// (LLM-353). Generic, not archetype-specific — per-archetype packs are a later
// refinement.
var visitorWareKinds = []ItemKind{"cheese", "ale", "iron", "bread"}

// seedVisitorPack returns the pack (inventory) and purse (coins) a freshly-spawned
// traveler carries: two distinct wares (a few units each) plus a modest purse. The
// coins guarantee it can cover a room (default 4/night); the wares give the
// barter-first payment its flavor and stock the circuit's trade talk. r is non-nil.
func seedVisitorPack(r *rand.Rand) (map[ItemKind]int, int) {
	pack := map[ItemKind]int{}
	i := r.Intn(len(visitorWareKinds))
	j := (i + 1 + r.Intn(len(visitorWareKinds)-1)) % len(visitorWareKinds)
	pack[visitorWareKinds[i]] = 3 + r.Intn(4) // 3..6
	pack[visitorWareKinds[j]] = 2 + r.Intn(3) // 2..4
	purse := 30 + r.Intn(21)                  // 30..50
	return pack, purse
}

// visitorProfile holds the four persona slots a freshly-spawned visitor
// receives. Drawn from the hardcoded pools above.
type visitorProfile struct {
	Name        string
	Archetype   string
	Origin      string
	Disposition string
}

// generateVisitorProfile pulls one entry from each pool using the supplied
// random source. r is non-nil — callers thread the per-driver seeded rand
// in production and a deterministic seed in tests.
func generateVisitorProfile(r *rand.Rand) visitorProfile {
	return visitorProfile{
		Name:        visitorNamePool[r.Intn(len(visitorNamePool))],
		Archetype:   visitorArchetypePool[r.Intn(len(visitorArchetypePool))],
		Origin:      visitorOriginPool[r.Intn(len(visitorOriginPool))],
		Disposition: visitorDispositionPool[r.Intn(len(visitorDispositionPool))],
	}
}

// extractSurname returns the lowercase last whitespace-delimited token of
// a display name. "Master Whitcombe" → "whitcombe"; "Ezekiel Crane" →
// "crane". Empty string for empty / whitespace-only names; for single-
// token names the token itself (treats "Tobias" as both first name AND
// surname for collision purposes — defensive).
func extractSurname(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return ""
	}
	return strings.ToLower(parts[len(parts)-1])
}

// loadActorSurnames returns a set of last-token surnames for every
// non-visitor actor in the world. Used by spawn to avoid colliding with
// a seated villager. Visitors themselves are excluded so two visitors
// don't collide-check against each other when the second rolls.
//
// MUST be called from inside a Command.Fn (reads w.Actors directly).
func loadActorSurnames(w *World) map[string]bool {
	out := map[string]bool{}
	for _, a := range w.Actors {
		if a == nil || a.VisitorState != nil {
			continue
		}
		if s := extractSurname(a.DisplayName); s != "" {
			out[s] = true
		}
	}
	return out
}

// displayNameInUse reports whether any existing actor in the world has
// the supplied display_name. Linear in actor count — fine at Salem
// scale (a few dozen actors). v1 ran the same check via a SELECT.
func displayNameInUse(w *World, name string) bool {
	for _, a := range w.Actors {
		if a != nil && a.DisplayName == name {
			return true
		}
	}
	return false
}

// pickVisitorDestination picks a structure for a freshly-spawned visitor
// to walk to. Prefers the tavern (oldest VillageObject tagged "tavern");
// falls back to any tagged VillageObject backed by a Structure. Returns
// (structureID, anchor-tile, true) on success, or false when no eligible
// destination is placed in the village.
//
// MUST be called from inside a Command.Fn (reads world maps directly).
func pickVisitorDestination(w *World) (StructureID, GridPoint, bool) {
	// Pass 1: smallest-ID tavern that is also backed by a Structure
	// (shared-identity bridge). v2's VillageObject doesn't carry created_at
	// today; iterating w.VillageObjects in map order isn't stable, so the
	// determinism tie-break is the lexicographic smallest ID. Filtering on
	// structureIDValid HERE (rather than after picking smallest) ensures a
	// rare tavern-without-Structure row doesn't make us fall through to
	// Pass 2 and pick a non-tavern when another structureIDValid tavern
	// would have qualified.
	var tavern VillageObjectID
	for id, vobj := range w.VillageObjects {
		if vobj == nil || !vobj.HasTag(VisitorTagTavern) {
			continue
		}
		if !structureIDValid(w, id) {
			continue
		}
		if tavern == "" || id < tavern {
			tavern = id
		}
	}
	if tavern != "" {
		anchor := w.VillageObjects[tavern].Pos.Tile()
		return StructureID(tavern), anchor, true
	}
	// Pass 2: smallest-ID VillageObject backed by a Structure. Untagged
	// or otherwise-tagged structures qualify; decoratives without a
	// Structure entry don't. v1 used random() which is less reproducible.
	var fallback VillageObjectID
	for id, vobj := range w.VillageObjects {
		if vobj == nil {
			continue
		}
		if !structureIDValid(w, id) {
			continue
		}
		if fallback == "" || id < fallback {
			fallback = id
		}
	}
	if fallback != "" {
		anchor := w.VillageObjects[fallback].Pos.Tile()
		return StructureID(fallback), anchor, true
	}
	return "", GridPoint{}, false
}

// structureIDValid reports whether a VillageObject's ID also exists in
// World.Structures — i.e. the placement is backed by a Structure (the
// shared-identity bridge). Decoratives / trees / wells have VillageObject
// rows without matching Structure rows; visitors don't walk to those.
func structureIDValid(w *World, id VillageObjectID) bool {
	_, ok := w.Structures[StructureID(id)]
	return ok
}

// pickVisitorEdgeTile picks a road tile near a randomly-chosen map edge
// for a visitor to spawn or depart on. In-memory port of v1's
// engine/visitor.go pickVisitorEdgeTile.
//
// Algorithm:
//
//  1. Shuffle the four edges (top / bottom / left / right) using the
//     supplied random source.
//  2. For each edge in order, sweep depths in [0, VisitorEdgeScanMaxDepth)
//     (exclusive upper bound — matches v1's loop, depth tile 30 itself is
//     not sampled) perpendicular to the edge. At each depth, collect tiles
//     whose raw terrain byte is TerrainDirt or TerrainCobblestone (roads).
//     Shuffle candidates at that depth and return the first one that is
//     both walkable in the obstacle-aware WalkGrid AND path-connected to
//     anchorTile via FindPathToAdjacent.
//  3. If no edge yields a candidate within the depth cap, return ok=false.
//     Caller skips this cycle.
//
// Edges blocked entirely by impassable terrain (Salem's north edge has
// continuous water) are skipped naturally — zero road candidates at any
// depth, the algorithm rotates to the next shuffled edge.
//
// r is non-nil. anchorTile is in internal grid coords (PadX/PadY-offset).
//
// MUST be called from inside a Command.Fn (reads w.Terrain).
func pickVisitorEdgeTile(w *World, grid *WalkGrid, anchorTile GridPoint, r *rand.Rand) (Position, bool) {
	if w.Terrain == nil || len(w.Terrain.Data) != MapW*MapH {
		return Position{}, false
	}
	if grid == nil {
		return Position{}, false
	}
	isRoadByte := func(b byte) bool { return b == TerrainDirt || b == TerrainCobblestone }

	type edgeMap struct {
		coord    func(depth, along int) GridPoint
		alongLen int
	}
	edges := []edgeMap{
		{func(d, a int) GridPoint { return GridPoint{X: a, Y: d} }, MapW},
		{func(d, a int) GridPoint { return GridPoint{X: a, Y: MapH - 1 - d} }, MapW},
		{func(d, a int) GridPoint { return GridPoint{X: d, Y: a} }, MapH},
		{func(d, a int) GridPoint { return GridPoint{X: MapW - 1 - d, Y: a} }, MapH},
	}
	r.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })

	for _, e := range edges {
		for depth := 0; depth < VisitorEdgeScanMaxDepth; depth++ {
			var candidates []GridPoint
			for along := 0; along < e.alongLen; along++ {
				p := e.coord(depth, along)
				if p.X < 0 || p.X >= MapW || p.Y < 0 || p.Y >= MapH {
					continue
				}
				if isRoadByte(w.Terrain.Data[p.Y*MapW+p.X]) {
					candidates = append(candidates, p)
				}
			}
			if len(candidates) == 0 {
				continue
			}
			r.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
			for _, c := range candidates {
				if !grid.CanWalk(c.X, c.Y) {
					continue
				}
				if path, _ := FindPathToAdjacent(grid, c, anchorTile); path != nil {
					return Position{X: c.X, Y: c.Y}, true
				}
			}
		}
	}
	return Position{}, false
}

// seedVisitorNeeds returns an Actor.Needs map populated with every entry
// in the Needs registry at value 0. v1's spawn path called the standard
// seedNeedRowsIfMissing helper (ZBBS-HOME-255 fix); v2's substrate-side
// equivalent is this constructor — atomic with Actor insert because both
// run in the same Command.Fn on the world goroutine.
func seedVisitorNeeds() map[NeedKey]int {
	out := make(map[NeedKey]int, len(Needs))
	for _, n := range Needs {
		out[n.Key] = 0
	}
	return out
}

// newVisitorActorID mints a fresh ActorID for a spawned visitor. Prefix
// "vstr-" so visitor rows are visually distinguishable from persistent
// NPC IDs (UUID-style) and PC IDs (login-username derived) in admin
// reads. Uses crypto/rand via randomHex for collision resistance.
func newVisitorActorID() string {
	return "vstr-" + randomHex(8)
}

// inputsRandOrDefault returns r when non-nil, otherwise a fresh
// time-seeded *rand.Rand. Production callers (the cascade driver) always
// supply a non-nil source seeded once at registration; the nil-fallback
// exists so tests that construct VisitorTickInputs ad-hoc don't have to
// thread a Rand through for codepaths that don't strictly need
// determinism (cleanup, the no-op spawn path under chance=0).
func inputsRandOrDefault(r *rand.Rand) *rand.Rand {
	if r != nil {
		return r
	}
	return rand.New(rand.NewSource(time.Now().UnixNano()))
}
