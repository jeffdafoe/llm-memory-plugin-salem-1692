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

	// VisitorPerceptionRadius is the bounding-box (Chebyshev) tile radius
	// around a perceiver within which a transient visitor is named with
	// archetype + origin + disposition by the "Visitors here" perception
	// block. 2 tiles ≈ same-tile, adjacent, one-step-away. Persistent
	// NPCs are not surfaced by this block — they go through the regular
	// huddle / acquaintance perception sections. Consumed by perception
	// build when the visitor cue lands.
	VisitorPerceptionRadius = 2
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

// VisitorArchetypeSprite maps each archetype to an npc_sprite.name. v2's
// Actor doesn't yet carry SpriteID — the rendering / client layer reads
// this map at cutover. The init() below enforces every archetype in
// visitorArchetypePool has an entry here; an archetype-without-sprite
// makes the package fail to load, so the mismatch can't reach a running
// deploy.
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
	DespawnsStarted int // visitors whose despawn walk was issued this tick
	CleanedUp       int // visitor rows removed past ExpiresAt + grace
	Spawned         int // new visitors created (0 or 1 per tick)
	SpawnSkipChance int // 1 if spawn skipped — chance=0 OR unlucky roll; check SpawnSkipReason
	SpawnSkipCap    int // 1 if spawn skipped because MaxConcurrent reached
	SpawnSkipReason string
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
			dispatchVisitorCleanup(w, inputs.Now, &t)
			// Eco mode (LLM-313): visitors exist to be seen — pause SPAWNING
			// while unwatched. Despawn/cleanup above keep running so existing
			// visitors age out normally; spawning resumes on the first tick
			// after a player's presence stamp is fresh again.
			if !ecoModeEngaged(w, inputs.Now) {
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
func dispatchVisitorCleanup(w *World, now time.Time, t *VisitorCascadeTelemetry) {
	grace := time.Duration(VisitorCleanupGraceMinutes) * time.Minute
	for id, actor := range w.Actors {
		if actor == nil || actor.VisitorState == nil {
			continue
		}
		if !now.After(actor.VisitorState.ExpiresAt.Add(grace)) {
			continue
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

	// Persona generation with surname scrub.
	existing := loadActorSurnames(w)
	profile := generateVisitorProfile(r)
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

	// Stay window.
	minStay := w.Settings.VisitorMinStayMinutes
	if minStay <= 0 {
		minStay = DefaultVisitorMinStayMinutes
	}
	maxStay := w.Settings.VisitorMaxStayMinutes
	if maxStay <= 0 {
		maxStay = DefaultVisitorMaxStayMinutes
	}
	if maxStay < minStay {
		maxStay = minStay
	}
	stayMinutes := minStay
	if maxStay > minStay {
		// +1 makes the upper bound inclusive — matches the documented
		// [min, max] semantics. r.Intn(n) returns [0, n), so n=maxStay-
		// minStay+1 produces additions in [0, maxStay-minStay].
		stayMinutes = minStay + r.Intn(maxStay-minStay+1)
	}
	expiresAt := inputs.Now.Add(time.Duration(stayMinutes) * time.Minute)

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
	visitor := &Actor{
		ID:                id,
		DisplayName:       displayName,
		Kind:              KindNPCShared,
		LLMAgent:          VisitorAgentName,
		Pos:               edgeTile,
		InsideStructureID: "",
		Needs:             seedVisitorNeeds(),
		Inventory:         map[ItemKind]int{},
		VisitorState: &VisitorState{
			Archetype:   profile.Archetype,
			Origin:      profile.Origin,
			Disposition: profile.Disposition,
			ExpiresAt:   expiresAt,
			Phase:       VisitorPhasePresent,
		},
		State: StateIdle,
	}
	w.Actors[id] = visitor
	w.outdoorActors[id] = struct{}{}

	// Walk toward the destination. Tavern entry policy decides whether
	// the visitor walks into the interior (open / default) or stops at
	// a visitor slot (owner-only / closed). MoveActor itself classifies;
	// the visitor cascade just hands it a StructureEnter destination and
	// falls back to StructureVisit on rejection.
	dest := NewStructureEnterDestination(destID)
	if _, err := MoveActor(id, dest, false, inputs.Now).Fn(w); err != nil {
		// EntryPolicy not open, no door, etc. — fall back to a visitor
		// slot. structure_visit accepts every structure and pickVisitorSlot
		// finds a tile in the loiter ring.
		dest = NewStructureVisitDestination(destID)
		if _, err := MoveActor(id, dest, false, inputs.Now).Fn(w); err != nil {
			// Both failed. The visitor row is in place but standing at
			// the edge tile; despawn / cleanup will collect after the
			// stay window. Log so the admin can see it.
			log.Printf("sim/visitor: dispatchSpawn: %s no walk to dest %s: %v", id, destID, err)
		}
	}
	t.Spawned++
	log.Printf("sim/visitor: spawn %s (id=%s, archetype=%s, origin=%s, disposition=%s, stay=%dm, edge=(%d,%d))",
		displayName, id, profile.Archetype, profile.Origin, profile.Disposition, stayMinutes, edgeTile.X, edgeTile.Y)
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
// The Actor is reconstructed exactly the way dispatchVisitorSpawn mints one —
// KindNPCShared, the shared salem-visitor VA, needs seeded to 0, empty inventory,
// StateIdle — differing only in the persisted identity / position / VisitorState.
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
		actor := &Actor{
			ID:                id,
			DisplayName:       lv.DisplayName,
			Kind:              KindNPCShared,
			LLMAgent:          VisitorAgentName,
			Pos:               lv.Pos,
			InsideStructureID: lv.InsideStructureID,
			Needs:             seedVisitorNeeds(),
			Inventory:         map[ItemKind]int{},
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
