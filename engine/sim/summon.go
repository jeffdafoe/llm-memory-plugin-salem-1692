package sim

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// summon.go — the summon messenger-errand substrate (ZBBS-HOME-311,
// reworked LLM-414). v2 in-memory port of v1's summon(target, reason?)
// tool. An NPC asks the engine to fetch another villager; the engine does
// NOT teleport anyone — it runs a multi-leg messenger errand:
//
//  1. the summoner walks to a summon_point village object (the bell — the
//     diegetic "get a messenger's attention" ritual),
//  2. a free messenger-attribute NPC walks to the same summon_point,
//  3. the messenger pauses (chat beat), the summoner commissions it and the
//     messenger acknowledges ("At once") — the summoner's errand role ends
//     HERE (LLM-414): the ack speech warrants him and his own steers walk
//     him back to his business; he does not wait at the bell,
//  4. the messenger walks to the target's location,
//  5. the messenger pauses (chat beat), then delivers
//     "<target>, <summoner> summons you. Come to <the summoner's place>.
//     <reason>" — the MEET PLACE is the summoner's own place (his post /
//     wherever he is headed), NOT the bell (v1's back half, restored),
//  6. the messenger walks home (untracked — nothing depends on it) and the
//     errand waits in awaiting_target for the target to ANSWER: the
//     target-side perception cue drives a model-chosen move_to; on the
//     target's arrival at the meet place the errand forms/joins the huddle
//     there (EnsureColocatedHuddle) so the meeting actually happens, then
//     finishes ("met"). The target going anywhere else is a choice, not an
//     answer — the cue stands until the errand TTL sweeps it.
//
// Refusal branch: if at the delivery dispatch the target can no longer be
// located (gone, or has no walkable location), the messenger turns around
// and walks back to the summoner, delivering "<target> could not be found"
// on arrival.
//
// ARCHITECTURE (locked):
//   - In-memory only. The errand lives in World.SummonErrands
//     (map[ErrandID]*summonErrand); NO DB table, NO migration. Restart-loss
//     is accepted (matches v1's transient ticker). Precedent:
//     World.BusinessownerCooldowns / World.ActiveRoutes.
//   - Walk-leg completion advances the machine off the ActorArrived event
//     (RegisterSummonSubscriber). The two chat beats (at the point before
//     commissioning, at the target before delivery) are scheduled via the
//     existing time.AfterFunc → world.SendContext pattern (see
//     reactor_evaluator.go / world_phase.go).
//   - BOUNDED MEMBERSHIP: every terminal path removes the errand from the
//     map (see finishErrand). A leaked errand would suppress the
//     summoner's arrival warrants forever (see suppressArrivalWarrant
//     below), so there is no exit that leaves a stale entry.

// ErrandID is the monotonic per-run identifier for a summon errand. Same
// shape and rules as the other per-run seq counters (eventSeq, quoteSeq):
// world-goroutine-only, first minted id is 1, ErrandID(0) is the unset
// sentinel.
type ErrandID uint64

// AttrMessenger is the actor attribute slug marking an NPC eligible to run
// summon errands. The messenger must additionally be a non-VA NPC
// (LLMAgent == "") so dispatching it never burns an LLM tick, and must not
// already be busy on another errand.
const AttrMessenger = "messenger"

// SummonPointTag is the VillageObject tag marking a structure-visit anchor
// where the summoner and messenger rendezvous before the errand fans out.
const SummonPointTag = "summon_point"

// summonState enumerates the legs of an errand. Transitions:
//
//	dispatched          --ActorArrived(summoner)--> summonerAtPoint
//	summonerAtPoint      --ActorArrived(messenger)--> messengerAtPoint
//	messengerAtPoint     --chat-pause callback------> messengerToTarget
//	messengerToTarget    --ActorArrived(messenger)--> messengerAtTarget
//	messengerAtTarget    --chat-pause callback------> awaitingTarget
//	awaitingTarget       --ActorArrived(target at meet place)--> done ("met")
//
// The messenger's walk home after delivery is untracked (LLM-414): nothing
// downstream depends on it, and freeing the messenger at delivery lets it
// carry another summons while this errand waits for its target.
//
// Refusal branch (entered from messengerAtPoint's callback when the target
// can no longer be located):
//
//	messengerToSummoner  --ActorArrived(messenger)--> done (errand removed)
type summonState string

const (
	summonDispatched          summonState = "dispatched"
	summonSummonerAtPoint     summonState = "summoner_at_point"
	summonMessengerAtPoint    summonState = "messenger_at_point"
	summonMessengerToTarget   summonState = "messenger_to_target"
	summonMessengerAtTarget   summonState = "messenger_at_target"
	summonAwaitingTarget      summonState = "awaiting_target"
	summonMessengerToSummoner summonState = "messenger_to_summoner"
)

// summonChatPause is the beat the messenger holds at the summon point
// (before commissioning) and at the target (before delivering) so the
// canned speech reads as turn-taking rather than instant teleport-chatter.
// Fixed (not settings-driven) — the errand is rare and the pause is pure
// pacing flavor, the same posture as the businessowner speech beats.
const summonChatPause = 1500 * time.Millisecond

// summonErrandTTL bounds an errand's lifetime. The arrival-warrant suppression
// hook (actorInActiveSummonErrand) keys off errand MEMBERSHIP with no time
// bound, so an errand that never reaches a terminal arrival — a walk leg
// superseded by an out-of-band MoveActor before its ActorArrived fires, or any
// other stall — would suppress the summoner's arrival warrants forever (a dead
// NPC). v1 avoided this implicitly: its suppression was a time-boxed
// agent_override_until that expired on its own even when a summon_errand row
// got stuck. This is the v2 analog: on dispatch we arm a one-shot timer that
// removes the errand if it's still in flight at the cap, guaranteeing bounded
// membership regardless of cause. Generous — a legitimate errand (a few short
// walks + two 1.5s beats) completes in well under a minute; the TTL only ever
// fires on a genuinely stuck errand.
const summonErrandTTL = 10 * time.Minute

// summonErrand is the in-flight state of one summon. Stored in
// World.SummonErrands keyed by ErrandID. Owned by the world goroutine
// (mutated only from inside Command.Fn or an inline subscriber, both of
// which run on the world goroutine).
type summonErrand struct {
	ID          ErrandID
	SummonerID  ActorID
	MessengerID ActorID
	TargetID    ActorID
	Reason      string // optional; "" when the summoner gave none

	State summonState

	// SummonPointID is the village object the rendezvous happens at — kept
	// for the place label in the target-side perception cue.
	SummonPointID VillageObjectID

	// MessengerOrigin is where the messenger started, so the final return
	// leg sends it home (a Position destination — the messenger may be a
	// homeless decorative NPC, so we don't assume a HomeStructureID).
	MessengerOrigin Position

	// LegAttemptID is the MovementAttemptID of the walk leg currently in
	// flight for the actor we're waiting on. ActorArrived advances the
	// machine only when its MovementAttemptID matches — a superseded or
	// stale arrival (admin force-move, an out-of-band MoveActor) is ignored,
	// same guard posture as npc_route's WalkTo check. NOT used for the
	// awaiting_target leg: the target's walk is its own model-chosen move_to,
	// so that leg matches on DESTINATION (MeetStructureID) instead.
	LegAttemptID MovementAttemptID

	// MeetStructureID is where the target was told to come — the summoner's
	// own place, resolved at delivery time (summonMeetPlace). Empty when the
	// summoner had no resolvable structure (the delivery fell back to the
	// summon point's label and the bell's structure identity, when it has one).
	MeetStructureID StructureID

	// DispatchedAt / History are the observability trail (LLM-414): every
	// state transition is stamped so the umbilical summon-errands view can
	// show where an errand is — or, for the finished ring, where it died.
	DispatchedAt time.Time
	History      []SummonStateStamp
}

// SummonStateStamp is one observability breadcrumb: the errand entered State
// at At. Exported because it rides the umbilical SummonErrandDTO unchanged.
type SummonStateStamp struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

// setSummonState is the single transition chokepoint: stamps the new state,
// appends the history breadcrumb, and logs the transition — the operator-
// visible trail the live incident lacked (the action log showed movement but
// not which state the errand died in).
func setSummonState(e *summonErrand, s summonState, now time.Time) {
	log.Printf("sim/summon: errand %d state %s -> %s", e.ID, e.State, s)
	e.State = s
	e.History = append(e.History, SummonStateStamp{State: string(s), At: now})
}

// RegisterSummonSubscriber wires the summon errand machine into the world:
// the ActorArrived subscriber that advances walk legs, the response-fade
// subscriber that drops a summon cue when its holder acts (move/speak/break),
// and the arrival-warrant suppression hook that keeps an errand participant
// from LLM-ticking mid-errand. Must run on the world goroutine (call before
// World.Run or from inside a Command.Fn).
//
// Idempotency: the suppression hook is installed unconditionally (last
// writer wins; the predicate is stateless over the errand map). Registering
// the subscriber twice would advance the machine twice per ActorArrived —
// the second pass sees the leg already advanced (LegAttemptID no longer
// matches) and no-ops. Worth not doing, but not a correctness violation.
func RegisterSummonSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterSummonSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleSummonArrival))
	w.Subscribe(SubscriberFunc(handleSummonResponseFade))
	w.suppressArrivalWarrant = func(a *Actor) bool {
		return actorInActiveSummonErrand(w, a)
	}
}

// actorInActiveSummonErrand reports whether actor is currently PLAYING a role
// in an active errand's choreography. Consulted by the locomotion ticker's
// finishArrival (via World.suppressArrivalWarrant) to suppress the arrival
// warrant on an errand participant: the summoner shouldn't LLM-tick and
// wander off mid-errand. (The messenger is a non-VA NPC and never
// LLM-ticks anyway, but suppressing it too costs nothing and keeps the
// predicate simple.)
//
// The summoner's role ends at the commission (LLM-414): from
// messengerToTarget onward his arrival warrants must NOT be suppressed —
// the "At once." ack speech warrants him, and his subsequent walk back to
// his business needs its normal arrival tick, or he stands wherever he
// lands with no turn to act on. The messenger stays suppressed for the
// states it is actually walking in (it is freed at delivery by clearing
// e.MessengerID, so awaiting_target matches no one).
//
// MUST be called from inside a Command.Fn / inline subscriber (reads the
// errand map).
func actorInActiveSummonErrand(w *World, actor *Actor) bool {
	if actor == nil {
		return false
	}
	for _, e := range w.SummonErrands {
		if e == nil {
			continue
		}
		if e.SummonerID == actor.ID {
			switch e.State {
			case summonDispatched, summonSummonerAtPoint, summonMessengerAtPoint:
				return true
			}
			continue
		}
		if e.MessengerID != "" && e.MessengerID == actor.ID {
			return true
		}
	}
	return false
}

// summonerHasActiveErrand / messengerIsBusy are the one-active-errand-per-
// participant guards. A summoner with an in-flight errand can't dispatch a
// second; a messenger already running one isn't "free" for selection.
func summonerHasActiveErrand(w *World, summoner ActorID) bool {
	for _, e := range w.SummonErrands {
		if e != nil && e.SummonerID == summoner {
			return true
		}
	}
	return false
}

func messengerIsBusy(w *World, messenger ActorID) bool {
	for _, e := range w.SummonErrands {
		if e != nil && e.MessengerID == messenger {
			return true
		}
	}
	return false
}

// nextErrandSeq increments the per-run errand counter and returns the new
// ErrandID. World-goroutine-only. First id is 1.
func (w *World) nextErrandSeq() ErrandID {
	w.errandSeq++
	return ErrandID(w.errandSeq)
}

// DispatchSummon returns a Command that runs the summon pre-checks and, on
// success, starts the errand (summoner walks to the summon point). It is the
// world-goroutine half of the summon tool — the handler (handlers.HandleSummon)
// is a pure builder that returns this command.
//
// Rejections (surfaced to the model as tool errors so it can retry / pick
// another action):
//   - summoner not in world (wiring bug — defense in depth).
//   - target is the summoner itself ("you cannot summon yourself").
//   - target names no actor in the world.
//   - the summoner already has an errand in flight.
//   - no summon_point village object exists.
//   - no free messenger NPC is available.
//   - the summoner can't path to the summon point.
//
// say (LLM-414) is the summoner's own in-character acknowledgement — the
// social beat spoken to whoever asked, BEFORE the summoner sets off. summon
// is terminal-on-success and so is speak, so without this the model had to
// choose between agreeing out loud and actually summoning (the live LLM-414
// incident: it agreed, the tick ended, and the summon waited 12.5 minutes
// for an unrelated wake). Emitted through the real Speak pipeline (audience,
// action log, turn-state) AFTER the pre-checks pass — a rejected summon must
// not leave a stray "I'll send for him" hanging — and best-effort: a speak
// rejection (no audience, a vocative gate) is logged, never fails the
// dispatch the model committed to.
//
// On success the summoner is dispatched (MoveActor visit → summon point) and
// the errand is inserted at state `dispatched`.
func DispatchSummon(summonerID ActorID, targetRaw string, reason string, say string, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if _, ok := w.Actors[summonerID]; !ok {
				return nil, fmt.Errorf("summon: actor %q not in world", summonerID)
			}
			// Resolve the model-supplied target to an actor id. The tool invites
			// a display name ("Ezekiel Crane"); before LLM-323 that string was
			// cast straight to an ActorID and looked up as a UUID key, so every
			// named summon failed. Existence is the only target pre-check — the
			// target's LOCATION is re-resolved at the delivery leg (it may move
			// between now and the messenger's arrival).
			targetID, ok, ambiguous := resolveSummonTarget(w, targetRaw)
			if ambiguous {
				return nil, fmt.Errorf(
					"more than one villager is called %q — name them more precisely", strings.TrimSpace(targetRaw))
			}
			if !ok {
				return nil, fmt.Errorf(
					"there is no one called %q to summon — name someone you know is in the village", strings.TrimSpace(targetRaw))
			}
			if targetID == summonerID {
				return nil, fmt.Errorf("you cannot summon yourself")
			}
			if summonerHasActiveErrand(w, summonerID) {
				return nil, fmt.Errorf("you have already sent for someone — wait for your messenger to return")
			}

			pointID, ok := findSummonPoint(w)
			if !ok {
				return nil, fmt.Errorf("there is nowhere to send for someone from — no summoning place exists in the village")
			}
			pointDest, ok := summonPointDestination(w, pointID)
			if !ok {
				// The summon_point object vanished between findSummonPoint and
				// here (a world mutation mid-command). Treat as "no summon point"
				// rather than dispatching an unreachable errand.
				return nil, fmt.Errorf("there is nowhere to send for someone from — the summoning place cannot be reached")
			}

			messengerID, ok := findFreeMessenger(w, summonerID, targetID)
			if !ok {
				return nil, fmt.Errorf("there is no messenger free to carry your summons right now")
			}
			messenger := w.Actors[messengerID]

			// The social beat: the summoner's spoken acknowledgement lands in
			// its current conversation BEFORE the walk to the bell pulls it
			// out. Best-effort — the dispatch stands even if the line is
			// rejected (e.g. genuinely no one within earshot).
			if say != "" {
				if _, err := Speak(summonerID, say, now).Fn(w); err != nil {
					log.Printf("sim/summon: summoner %q say beat rejected (dispatch continues): %v", summonerID, err)
				}
			}

			// Dispatch the summoner to the summon point (visit slot — they stand
			// beside it, no entry required). leaveHuddleFirst=true: the summoner
			// leaves any conversation to go wait for the messenger, matching
			// move_to's "choosing to walk away leaves it" posture.
			res, err := MoveActor(summonerID, pointDest, true, now).Fn(w)
			if err != nil {
				return nil, fmt.Errorf("you cannot reach the summoning place from here: %w", err)
			}
			moveRes := res.(MoveActorResult)

			errand := &summonErrand{
				ID:              w.nextErrandSeq(),
				SummonerID:      summonerID,
				MessengerID:     messengerID,
				TargetID:        targetID,
				Reason:          reason,
				State:           summonDispatched,
				SummonPointID:   pointID,
				MessengerOrigin: Position{X: messenger.Pos.X, Y: messenger.Pos.Y},
				LegAttemptID:    moveRes.MovementAttemptID,
				DispatchedAt:    now,
				History:         []SummonStateStamp{{State: string(summonDispatched), At: now}},
			}
			if w.SummonErrands == nil {
				w.SummonErrands = map[ErrandID]*summonErrand{}
			}
			w.SummonErrands[errand.ID] = errand
			armSummonErrandTTL(w, errand.ID)

			log.Printf("sim/summon: errand %d dispatched — %q summons %q via messenger %q",
				errand.ID, summonerID, targetID, messengerID)
			return errand.ID, nil
		},
	}
}

// findSummonPoint returns the VillageObjectID of a summon_point-tagged
// object, deterministically (lowest id) when several exist, or ok=false
// when none do.
func findSummonPoint(w *World) (VillageObjectID, bool) {
	var best VillageObjectID
	found := false
	for id, obj := range w.VillageObjects {
		if obj == nil || !obj.HasTag(SummonPointTag) {
			continue
		}
		if !found || id < best {
			best = id
			found = true
		}
	}
	return best, found
}

// findFreeMessenger returns the id of a free messenger NPC: carries the
// messenger attribute, is a non-VA NPC (LLMAgent == ""), and is not already
// running an errand. Excludes the summoner and target so neither is dispatched
// to fetch itself — a self-messenger can never be observed in the messenger
// role (errandForArrival resolves the summoner role first), stranding the
// machine; a target-messenger would be sent to fetch itself. v1's
// findNearestMessenger likewise excluded the summoner. Deterministic (lowest
// id) when several qualify, ok=false when none do.
func findFreeMessenger(w *World, summonerID, targetID ActorID) (ActorID, bool) {
	var best ActorID
	found := false
	for id, a := range w.Actors {
		if a == nil {
			continue
		}
		if id == summonerID || id == targetID {
			continue
		}
		if _, ok := a.Attributes[AttrMessenger]; !ok {
			continue
		}
		if a.LLMAgent != "" { // VA-backed actor — not eligible (we don't burn LLM ticks on errands)
			continue
		}
		if messengerIsBusy(w, id) {
			continue
		}
		if !found || id < best {
			best = id
			found = true
		}
	}
	return best, found
}

// resolveSummonTarget resolves the model-supplied target string to an actor id
// (LLM-323 gate 1). The summon tool invites a display name, so the string is
// almost always a name ("Ezekiel Crane"), not the UUIDv7 the world map is keyed
// by. Resolution order:
//
//   - exact-id fast path — a model that echoes a raw id, or a future caller that
//     passes one, resolves without a scan;
//   - village-wide display-name match — summon fetches someone who is by
//     definition NOT co-present, so the scan spans every actor, not just the
//     summoner's huddle (unlike findHuddlePeerByDisplayName). Matching is
//     case-insensitive and tolerant of the trailing punctuation / surrounding
//     quotes a weak model appends to a spoken name (summonTargetMatches).
//
// Tri-state like findHuddlePeerByDisplayName:
//
//   - (id, true, false)  — single match;
//   - ("", false, false) — no actor by that id or name;
//   - ("", false, true)  — two or more actors share the name; the caller rejects
//     as ambiguous rather than pick one non-deterministically. Village display
//     names are unique in practice, so this guard is defensive.
//
// The summoner is deliberately NOT excluded from the scan: a model that names
// itself resolves to its own id, and DispatchSummon's self-summon check then
// returns the precise "you cannot summon yourself" instead of a misleading
// "no one by that name".
func resolveSummonTarget(w *World, raw string) (ActorID, bool, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, false
	}
	if _, ok := w.Actors[ActorID(trimmed)]; ok {
		return ActorID(trimmed), true, false
	}
	var found ActorID
	for id, a := range w.Actors {
		if a == nil {
			continue
		}
		if summonTargetMatches(a.DisplayName, trimmed) {
			if found != "" {
				return "", false, true
			}
			found = id
		}
	}
	if found == "" {
		return "", false, false
	}
	return found, true, false
}

// summonTargetMatches reports whether an actor's display name matches the
// model-supplied target string, case-insensitively and tolerant of the trailing
// sentence punctuation and surrounding quotes a weak model wraps a spoken name in
// ("Ezekiel Crane." / 'Ezekiel Crane,'). Mirrors placeNameMatches's forgiving
// posture, for people instead of places — but does NOT strip a leading article,
// since a proper name never carries one and stripping would break a display name
// that legitimately begins with one ("the boy").
//
// Empty on either side never matches: a nameless actor (a decorative with no
// display name — the scan spans every actor, so these are in range) must not be
// reachable, and an all-punctuation query normalizes to "" and names no one.
// Without these guards `summonTargetMatches("", ".")` would be true.
func summonTargetMatches(displayName, query string) bool {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return false
	}
	q := normalizeSummonQuery(query)
	if q == "" {
		return false
	}
	return strings.EqualFold(name, q)
}

// normalizeSummonQuery strips the surrounding quotes and trailing sentence
// punctuation a model tends to wrap a spoken name in, so the EqualFold compare
// sees just the name. Handles straight and curly quotes. An all-punctuation
// query normalizes to "" — summonTargetMatches treats that empty result as
// "names no one" rather than letting it match a nameless actor.
func normalizeSummonQuery(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'“”‘’")
	s = strings.TrimRight(s, " .,!?;:")
	return strings.TrimSpace(s)
}

// summonPointDestination resolves how the summoner and messenger walk to the
// rendezvous point (LLM-323 gate 3). When the summon_point object also backs a
// Structure it uses the structure's visitor slot (the original ZBBS-HOME-311
// anchor shape); when it is a bare placement — the live village's summon_point is
// one — it walks to a visitor slot beside the object itself (NewObjectVisitDestination).
// A summoning place is a spot (a well, a market cross), not necessarily a
// building, so requiring a Structure shell was an over-constraint that left the
// live feature dead at this gate. ok=false only when the object itself is gone.
func summonPointDestination(w *World, pointID VillageObjectID) (MoveDestination, bool) {
	if _, ok := w.Structures[StructureID(pointID)]; ok {
		return NewStructureVisitDestination(StructureID(pointID)), true
	}
	if obj, ok := w.VillageObjects[pointID]; ok && obj != nil {
		return NewObjectVisitDestination(pointID), true
	}
	return MoveDestination{}, false
}

// handleSummonArrival is the inline ActorArrived subscriber. Runs on the
// world goroutine during emit; advances the matching errand leg. Non-arrival
// events and arrivals for actors in no errand fall through.
//
// Two matching regimes (LLM-414), processed INDEPENDENTLY — one arrival can
// legitimately serve both (an actor that is the target of one errand and the
// summoner of another):
//
//   - The awaiting_target leg is the target's OWN model-chosen move_to, whose
//     attempt id the errand never sees — it matches on DESTINATION instead,
//     scanning EVERY errand awaiting this actor at the arrived structure
//     (first-match resolution dropped valid arrivals when the same target
//     carried a second, non-awaiting summons — code_review). A target
//     arriving at the meet place for ANY reason completes the meeting by
//     design: the summons asked them to come, and they are physically there;
//     the errand-scoped cue clear + idempotent huddle join make a
//     coincidental arrival harmless. A target arrival anywhere else is a
//     choice, not an answer, and is ignored.
//
//   - The summoner/messenger legs are engine-dispatched walks matching on the
//     tracked MovementAttemptID. First-match resolution is safe THERE: the
//     one-errand-per-summoner and one-errand-per-messenger guards make a
//     second simultaneous match impossible.
func handleSummonArrival(w *World, evt Event) {
	arrived, ok := evt.(*ActorArrived)
	if !ok {
		return
	}
	now := arrived.At

	// Target-role arrivals: complete every errand awaiting this actor at
	// this destination. Collect first (finishErrand mutates the map), sort
	// for determinism, then complete.
	if arrived.DestStructureID != "" {
		var met []*summonErrand
		for _, e := range w.SummonErrands {
			if e == nil || e.State != summonAwaitingTarget {
				continue
			}
			if e.TargetID == arrived.ActorID && e.MeetStructureID == arrived.DestStructureID {
				met = append(met, e)
			}
		}
		sort.Slice(met, func(i, j int) bool { return met[i].ID < met[j].ID })
		for _, e := range met {
			completeSummonMeeting(w, e, now)
		}
	}

	e, role := errandForArrival(w, arrived.ActorID)
	if e == nil {
		return
	}
	// Stale-arrival guard: only the leg we dispatched advances the machine.
	// A superseded MoveActor (admin force-move, out-of-band walk) carries a
	// different MovementAttemptID and is ignored — the next legitimate
	// arrival resyncs.
	if arrived.MovementAttemptID != e.LegAttemptID {
		return
	}
	switch e.State {
	case summonDispatched:
		if role == summonRoleSummoner {
			onSummonerAtPoint(w, e, now)
		}
	case summonSummonerAtPoint:
		if role == summonRoleMessenger {
			onMessengerAtPoint(w, e, now)
		}
	case summonMessengerToTarget:
		if role == summonRoleMessenger {
			onMessengerAtTarget(w, e, now)
		}
	case summonMessengerToSummoner:
		if role == summonRoleMessenger {
			onMessengerBackAtSummoner(w, e, now)
		}
	}
}

type summonRole int

const (
	summonRoleNone summonRole = iota
	summonRoleSummoner
	summonRoleMessenger
)

// errandForArrival finds the active errand an arriving actor DRIVES a
// tracked walk leg for — as summoner or messenger. Returns nil when the
// actor drives no leg. First-match is safe: summonerHasActiveErrand and
// messengerIsBusy each cap an actor to one errand in that role. The target
// role is deliberately NOT resolved here — a target can be awaited by
// several errands at once, so the caller scans those exhaustively instead.
func errandForArrival(w *World, actorID ActorID) (*summonErrand, summonRole) {
	for _, e := range w.SummonErrands {
		if e == nil {
			continue
		}
		if e.SummonerID == actorID {
			return e, summonRoleSummoner
		}
		if e.MessengerID != "" && e.MessengerID == actorID {
			return e, summonRoleMessenger
		}
	}
	return nil, summonRoleNone
}

// completeSummonMeeting is the errand's happy ending (LLM-414): the target
// arrived at the meet place. Clear the target-side cue (the summons is
// answered), join the target into the active huddle at the meet structure —
// EnsureColocatedHuddle find-or-creates it, pulling in the summoner and
// anyone already conversing there (the player who asked, typically) — and
// finish. The HuddleJoined/PeerJoined warrants that join stamps are what
// produce the greeting: both sides get a turn, and the arrival beat is the
// model's own words, not a canned line.
//
// The huddle join is best-effort: the meeting is the target standing at the
// meet place either way, and a join rejection (summoner wandered off, the
// structure emptied) must not strand the errand — the outcome string records
// which ending it was.
func completeSummonMeeting(w *World, e *summonErrand, now time.Time) {
	if target, ok := w.Actors[e.TargetID]; ok && target != nil {
		clearSummonCueForErrand(target, e.ID)
	}
	outcome := "met"
	if _, err := EnsureColocatedHuddle(e.TargetID, now).Fn(w); err != nil {
		log.Printf("sim/summon: errand %d meeting huddle failed: %v", e.ID, err)
		outcome = "met_no_huddle"
	}
	finishErrand(w, e, outcome)
}

// onSummonerAtPoint: the summoner reached the summon point. Dispatch the
// messenger to the same point. Advance to summonerAtPoint, tracking the
// messenger's walk-leg attempt id.
func onSummonerAtPoint(w *World, e *summonErrand, now time.Time) {
	pointDest, ok := summonPointDestination(w, e.SummonPointID)
	if !ok {
		// The summon point vanished mid-errand. Abandon cleanly rather than
		// leave a dangling entry no arrival can advance.
		log.Printf("sim/summon: errand %d abandoned — summon point %q gone", e.ID, e.SummonPointID)
		finishErrand(w, e, "summon_point_gone")
		return
	}
	res, err := MoveActor(e.MessengerID, pointDest, true, now).Fn(w)
	if err != nil {
		// The messenger can't reach the rendezvous (path blocked). Abandon
		// the errand cleanly — better than a dangling entry no arrival can
		// advance. The summoner waited in vain; no refusal speech (the
		// messenger never set out), the summoner's perception simply has no
		// summons cue.
		log.Printf("sim/summon: errand %d abandoned — messenger %q cannot reach summon point: %v",
			e.ID, e.MessengerID, err)
		finishErrand(w, e, "messenger_unreachable")
		return
	}
	e.LegAttemptID = res.(MoveActorResult).MovementAttemptID
	setSummonState(e, summonSummonerAtPoint, now)
}

// onMessengerAtPoint: the messenger reached the summon point. Hold the chat
// beat, then commission the messenger. Advance to messengerAtPoint and
// schedule the commissioning callback.
func onMessengerAtPoint(w *World, e *summonErrand, now time.Time) {
	setSummonState(e, summonMessengerAtPoint, now)
	scheduleSummonChatPause(w, e.ID, summonCommission)
}

// onMessengerAtTarget: the messenger reached the target's location. Hold the
// chat beat, then deliver. Advance to messengerAtTarget and schedule the
// delivery callback.
func onMessengerAtTarget(w *World, e *summonErrand, now time.Time) {
	setSummonState(e, summonMessengerAtTarget, now)
	scheduleSummonChatPause(w, e.ID, summonDeliver)
}

// onMessengerBackAtSummoner: the messenger returned to the summoner on the
// refusal branch. Emit the refusal line, stamp the summoner-side perception
// cue, and finish.
func onMessengerBackAtSummoner(w *World, e *summonErrand, now time.Time) {
	summoner := w.Actors[e.SummonerID]
	messenger := w.Actors[e.MessengerID]
	if summoner != nil && messenger != nil {
		emitSummonSpoke(w, e.MessengerID, summoner.CurrentHuddleID, []ActorID{e.SummonerID},
			summonRefusalText(targetDisplayName(w, e.TargetID)), now)
		summoner.SummonRefusal = &SummonRefusal{
			TargetName: targetDisplayName(w, e.TargetID),
			At:         now,
		}
	}
	finishErrand(w, e, "refused")
}

// summonChatPauseKind selects which canned beat a scheduled chat-pause
// callback runs when it fires on the world goroutine.
type summonChatPauseKind int

const (
	summonCommission summonChatPauseKind = iota // commissioning at the summon point
	summonDeliver                               // delivery at the target
)

// scheduleSummonChatPause arms a one-shot time.AfterFunc that, after the
// chat beat, sends the matching follow-up command onto the world goroutine.
// Mirrors the reactor evaluator / phase flip scheduled-callback pattern:
// the timer callback uses LifecycleContext so a shutdown-while-armed aborts
// cleanly via ctx.Err() instead of parking on a dead cmds channel.
//
// The follow-up command re-reads the errand by id and bails if it's gone or
// has moved on (superseded by an abandon, or the world advanced) — the
// errand-generation check is the counter-free analog of the phase
// flip's guard: identity by (id, expected state) rather than a generation
// counter, since an errand is single-owner and can only legitimately be in
// one state when its beat fires.
//
// MUST be called from inside a Command.Fn / inline subscriber.
func scheduleSummonChatPause(w *World, id ErrandID, kind summonChatPauseKind) {
	time.AfterFunc(summonChatPause, func() {
		ctx := w.LifecycleContext()
		if ctx.Err() != nil {
			return
		}
		_, err := w.SendContext(ctx, runSummonChatPause(id, kind, time.Now().UTC()))
		if err != nil && ctx.Err() == nil {
			log.Printf("sim/summon: chat-pause callback for errand %d failed: %v", id, err)
		}
	})
}

// runSummonChatPause is the body of a scheduled chat-pause callback, run on
// the world goroutine. Re-resolves the errand and fires the commissioning or
// delivery beat. Factored as a Command so tests can drive it synchronously
// (via .Fn(w)) without real-time waits.
func runSummonChatPause(id ErrandID, kind summonChatPauseKind, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			e, ok := w.SummonErrands[id]
			if !ok || e == nil {
				return nil, nil // errand already terminal (abandoned/superseded) — no-op
			}
			switch kind {
			case summonCommission:
				if e.State == summonMessengerAtPoint {
					summonDoCommission(w, e, now)
				}
			case summonDeliver:
				if e.State == summonMessengerAtTarget {
					summonDoDeliver(w, e, now)
				}
			}
			return nil, nil
		},
	}
}

// armSummonErrandTTL starts the one-shot bounded-lifetime timer for an errand
// (see summonErrandTTL). Mirrors scheduleSummonChatPause: a time.AfterFunc hops
// onto the world goroutine via SendContext, guarded by LifecycleContext so a
// shutdown-while-armed aborts cleanly. The fire re-resolves the errand by id
// (no-op if it already reached a terminal path and was removed), so no explicit
// cancel is needed on normal completion — monotonic ErrandIDs are never reused.
// MUST be called from inside a Command.Fn / inline subscriber.
func armSummonErrandTTL(w *World, id ErrandID) {
	time.AfterFunc(summonErrandTTL, func() {
		ctx := w.LifecycleContext()
		if ctx.Err() != nil {
			return
		}
		if _, err := w.SendContext(ctx, expireSummonErrand(id)); err != nil && ctx.Err() == nil {
			log.Printf("sim/summon: TTL expiry for errand %d failed: %v", id, err)
		}
	})
}

// expireSummonErrand is the body of the TTL timer, run on the world goroutine.
// If the errand is still in flight at the cap, it finishes it — removing the
// map entry (which lifts the arrival-warrant suppression on the summoner, the
// whole point) and abandoning whatever leg stalled. A no-op when the errand
// already completed normally (re-resolved by id, gone from the map). Factored
// as a Command so tests can drive it synchronously without a real-time wait.
func expireSummonErrand(id ErrandID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			e, ok := w.SummonErrands[id]
			if !ok || e == nil {
				return nil, nil // already terminal — nothing to clean up
			}
			finishErrand(w, e, "expired")
			return nil, nil
		},
	}
}

// summonDoCommission emits the commissioning beat (summoner asks the
// messenger to fetch the target; messenger acknowledges) and dispatches the
// messenger toward the target. If the target can no longer be located, it
// turns the messenger around toward the summoner (refusal branch). Advances
// the errand to messengerToTarget or messengerToSummoner.
func summonDoCommission(w *World, e *summonErrand, now time.Time) {
	summoner := w.Actors[e.SummonerID]
	messenger := w.Actors[e.MessengerID]
	if summoner == nil || messenger == nil {
		// A participant vanished mid-errand. Nothing to commission; finish.
		finishErrand(w, e, "participant_gone")
		return
	}

	targetName := targetDisplayName(w, e.TargetID)
	// Commissioning line (summoner → messenger) and the messenger's "At once."
	emitSummonSpoke(w, e.SummonerID, messenger.CurrentHuddleID, []ActorID{e.MessengerID},
		summonCommissionText(targetName, e.Reason), now)
	emitSummonSpoke(w, e.MessengerID, messenger.CurrentHuddleID, []ActorID{e.SummonerID},
		summonAcknowledgeText(), now)

	dest, ok := summonTargetDestination(w, e.TargetID)
	if !ok {
		// Target can't be located — turn the messenger around. The refusal
		// is carried back to the summoner on the return walk's arrival.
		startRefusalReturn(w, e, now)
		return
	}
	res, err := MoveActor(e.MessengerID, dest, true, now).Fn(w)
	if err != nil {
		// Path to the target failed (e.g. target indoors behind a locked
		// door we can't visit). Treat as not-found → refusal return.
		startRefusalReturn(w, e, now)
		return
	}
	e.LegAttemptID = res.(MoveActorResult).MovementAttemptID
	setSummonState(e, summonMessengerToTarget, now)
}

// summonDoDeliver emits the delivery beat at the target, stamps the
// target-side perception cue + the action-log entry, sends the messenger
// home (untracked — see below), and parks the errand in awaiting_target
// until the target's own move_to answers the summons (LLM-414).
func summonDoDeliver(w *World, e *summonErrand, now time.Time) {
	messenger := w.Actors[e.MessengerID]
	target := w.Actors[e.TargetID]
	if messenger == nil || target == nil {
		// Target left between arrival and the delivery beat. No one to
		// deliver to → refusal return to the summoner.
		startRefusalReturn(w, e, now)
		return
	}

	// The meet place is the summoner's OWN place, resolved now (his post,
	// the place he's walking back to, or where he stands) — the v1 back
	// half. The bell is only the commissioning ritual; nobody meets there.
	meetID, meetLabel := summonMeetPlace(w, e)
	e.MeetStructureID = meetID

	summonerName := summonerDisplayName(w, e.SummonerID)
	deliveryText := summonDeliveryText(target.DisplayName, summonerName, meetLabel, e.Reason)
	emitSummonSpoke(w, e.MessengerID, target.CurrentHuddleID, []ActorID{e.TargetID}, deliveryText, now)

	// Target-side perception cue — drives the summoned NPC to move_to the
	// meet place. It survives the target's speech and its walk (the walk IS
	// the answer in progress); it is cleared by the meeting itself, a
	// take_break, or the errand's TTL. See handleSummonResponseFade.
	target.PendingSummon = &PendingSummon{
		ErrandID:         e.ID,
		SummonerName:     summonerName,
		Place:            meetLabel,
		PlaceStructureID: meetID,
		Reason:           e.Reason,
		At:               now,
	}

	// Action-log row: the delivery is the target's summons event. Best-effort
	// — a log-append failure must not abort the errand (the delivery already
	// landed), so log and continue rather than propagate.
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:    e.TargetID,
		OccurredAt: now,
		ActionType: ActionTypeSummoned,
		Text:       deliveryText,
		HuddleID:   target.CurrentHuddleID,
	}).Fn(w); err != nil {
		log.Printf("sim/summon: errand %d action-log append failed: %v", e.ID, err)
	}

	// Send the messenger home, UNTRACKED, and free it from the errand:
	// nothing downstream depends on its walk, and a freed messenger can
	// carry another summons while this errand waits for its target. A
	// failed return walk strands nothing (the stranded backstop covers a
	// parked NPC).
	if _, err := MoveActor(e.MessengerID, NewPositionDestination(e.MessengerOrigin), true, now).Fn(w); err != nil {
		log.Printf("sim/summon: errand %d messenger %q cannot return home: %v",
			e.ID, e.MessengerID, err)
	}
	e.MessengerID = ""

	setSummonState(e, summonAwaitingTarget, now)

	// Degenerate immediate meeting: the target is ALREADY at the meet place
	// (summoned someone standing in the same building). Complete on the spot
	// rather than waiting for a walk that will never happen.
	if meetID != "" && target.InsideStructureID == meetID {
		completeSummonMeeting(w, e, now)
	}
}

// summonMeetPlace resolves where the target should be told to come — the
// summoner's own place at delivery time. Ladder: the structure the summoner
// is inside; the structure an in-flight walk is headed to (the usual case
// right after the commission — he is walking back to his post); the
// structure whose loiter pin he stands at; else the summon point (the bell)
// as neutral-ground fallback, which for a bare placement yields an empty
// structure id — the cue then names the label alone, and the errand can only
// finish by TTL (no destination to match), which the outcome trail records.
func summonMeetPlace(w *World, e *summonErrand) (StructureID, string) {
	summoner := w.Actors[e.SummonerID]
	if summoner != nil {
		if summoner.InsideStructureID != "" {
			if st := w.Structures[summoner.InsideStructureID]; st != nil {
				return summoner.InsideStructureID, structureLabel(w, summoner.InsideStructureID)
			}
		}
		if summoner.MoveIntent != nil && summoner.MoveIntent.Destination.StructureID != nil {
			dest := *summoner.MoveIntent.Destination.StructureID
			if st := w.Structures[dest]; st != nil {
				return dest, structureLabel(w, dest)
			}
		}
		if objID, ok := resolveLoiteringObject(w, summoner.Pos, LoiterAttributionTiles); ok {
			if st := w.Structures[StructureID(objID)]; st != nil {
				return StructureID(objID), structureLabel(w, StructureID(objID))
			}
		}
	}
	if _, ok := w.Structures[StructureID(e.SummonPointID)]; ok {
		return StructureID(e.SummonPointID), summonPointLabel(w, e.SummonPointID)
	}
	return "", summonPointLabel(w, e.SummonPointID)
}

// structureLabel resolves a structure's display name, falling back to the raw
// id so the cue never renders an empty place.
func structureLabel(w *World, id StructureID) string {
	if st, ok := w.Structures[id]; ok && st != nil && st.DisplayName != "" {
		return st.DisplayName
	}
	return string(id)
}

// startRefusalReturn turns the messenger toward the summoner to deliver the
// "could not be found" refusal. If the messenger can't even path back, the
// errand is finished in place (the summoner gets the refusal perception cue
// directly so the failure still surfaces). Advances to messengerToSummoner.
func startRefusalReturn(w *World, e *summonErrand, now time.Time) {
	summoner := w.Actors[e.SummonerID]
	if summoner == nil {
		finishErrand(w, e, "participant_gone")
		return
	}
	// Walk to the summoner's current tile.
	dest := NewPositionDestination(Position{X: summoner.Pos.X, Y: summoner.Pos.Y})
	res, err := MoveActor(e.MessengerID, dest, true, now).Fn(w)
	if err != nil {
		// Can't return — stamp the refusal cue directly and finish.
		summoner.SummonRefusal = &SummonRefusal{
			TargetName: targetDisplayName(w, e.TargetID),
			At:         now,
		}
		finishErrand(w, e, "refused_no_return")
		return
	}
	e.LegAttemptID = res.(MoveActorResult).MovementAttemptID
	setSummonState(e, summonMessengerToSummoner, now)
}

// finishErrand removes the errand from the world map. THE bounded-membership
// chokepoint: every terminal path (normal completion, refusal, abandon,
// participant-gone, TTL expiry) routes through here, so no exit leaves a
// stale entry that would suppress the summoner's arrival warrants forever.
//
// It also (LLM-414):
//   - clears the target's PendingSummon cue when it belongs to THIS errand —
//     a cue must not outlive its errand (the steer suppression keys on it),
//     and an expired errand's target must get its go-home steers back;
//   - records the errand into the recent-errands observability ring so the
//     umbilical can show where a finished errand ended after the live map
//     entry is gone (the live incident was undiagnosable precisely because
//     completion erased the trail).
func finishErrand(w *World, e *summonErrand, outcome string) {
	delete(w.SummonErrands, e.ID)
	if target, ok := w.Actors[e.TargetID]; ok && target != nil {
		clearSummonCueForErrand(target, e.ID)
	}
	recordFinishedSummonErrand(w, e, outcome, time.Now().UTC())
	log.Printf("sim/summon: errand %d finished (%s)", e.ID, outcome)
}

// clearSummonCueForErrand drops an actor's PendingSummon only when it was
// stamped by the given errand — a newer errand's cue (the same actor summoned
// again) must not be wiped by an older errand's terminal path.
func clearSummonCueForErrand(a *Actor, id ErrandID) {
	if a == nil || a.PendingSummon == nil {
		return
	}
	if a.PendingSummon.ErrandID == id {
		a.PendingSummon = nil
	}
}

// summonErrandHistoryCap bounds the recent-errands observability ring: big
// enough to hold days of real summon traffic (the errand is rare), small
// enough to never matter in memory.
const summonErrandHistoryCap = 32

// FinishedSummonErrand is one recent-errands ring entry — the post-mortem
// record of a completed/failed errand for the umbilical summon-errands view.
// Names are resolved at finish time (the actors could be deleted later).
type FinishedSummonErrand struct {
	ID            ErrandID           `json:"id"`
	SummonerID    ActorID            `json:"summoner_id"`
	SummonerName  string             `json:"summoner_name"`
	TargetID      ActorID            `json:"target_id"`
	TargetName    string             `json:"target_name"`
	Reason        string             `json:"reason,omitempty"`
	Outcome       string             `json:"outcome"`
	MeetStructure StructureID        `json:"meet_structure_id,omitempty"`
	DispatchedAt  time.Time          `json:"dispatched_at"`
	FinishedAt    time.Time          `json:"finished_at"`
	History       []SummonStateStamp `json:"history"`
}

// recordFinishedSummonErrand appends the errand's post-mortem to the world's
// recent-errands ring, evicting the oldest past the cap. World-goroutine-only.
func recordFinishedSummonErrand(w *World, e *summonErrand, outcome string, finishedAt time.Time) {
	rec := FinishedSummonErrand{
		ID:            e.ID,
		SummonerID:    e.SummonerID,
		SummonerName:  targetDisplayName(w, e.SummonerID),
		TargetID:      e.TargetID,
		TargetName:    targetDisplayName(w, e.TargetID),
		Reason:        e.Reason,
		Outcome:       outcome,
		MeetStructure: e.MeetStructureID,
		DispatchedAt:  e.DispatchedAt,
		FinishedAt:    finishedAt,
		History:       append([]SummonStateStamp(nil), e.History...),
	}
	w.recentSummonErrands = append(w.recentSummonErrands, rec)
	if len(w.recentSummonErrands) > summonErrandHistoryCap {
		w.recentSummonErrands = w.recentSummonErrands[len(w.recentSummonErrands)-summonErrandHistoryCap:]
	}
}

// summonTargetDestination resolves where the messenger should walk to reach
// the target: the target's structure (visit slot) when it's inside or anchored
// to one, else its current tile. ok=false when the target actor is gone.
func summonTargetDestination(w *World, targetID ActorID) (MoveDestination, bool) {
	target, ok := w.Actors[targetID]
	if !ok {
		return MoveDestination{}, false
	}
	// Prefer the structure the target is inside (visit its slot — the
	// messenger stands outside to deliver). Fall back to the target's home
	// structure if it has one and the structure exists, else the target's
	// bare tile.
	if target.InsideStructureID != "" {
		if _, ok := w.Structures[target.InsideStructureID]; ok {
			return NewStructureVisitDestination(target.InsideStructureID), true
		}
	}
	return NewPositionDestination(Position{X: target.Pos.X, Y: target.Pos.Y}), true
}

// targetDisplayName / summonerDisplayName resolve a display name, falling
// back to the raw id when the actor is missing (defensive — an actor could
// be deleted mid-errand).
func targetDisplayName(w *World, id ActorID) string {
	if a, ok := w.Actors[id]; ok && a.DisplayName != "" {
		return a.DisplayName
	}
	return string(id)
}

func summonerDisplayName(w *World, id ActorID) string {
	return targetDisplayName(w, id)
}

// summonPointLabel resolves the human label for the summon point used in the
// target's "come to <place>" cue. Falls back to a generic phrase.
func summonPointLabel(w *World, id VillageObjectID) string {
	if obj, ok := w.VillageObjects[id]; ok && obj != nil && obj.DisplayName != "" {
		return obj.DisplayName
	}
	if st, ok := w.Structures[StructureID(id)]; ok && st != nil && st.DisplayName != "" {
		return st.DisplayName
	}
	return "the summoning place"
}

// emitSummonSpoke emits a canned (non-LLM) Spoke from the errand machine.
// Mirrors businessowner.go's engine-authored Spoke emission: the standard
// Spoke subscribers (action log, speech reactor) render it identically to
// any speech. recipients is the single addressed listener; HuddleID is the
// speaker's current huddle context (may be empty — a valid state).
func emitSummonSpoke(w *World, speaker ActorID, huddle HuddleID, recipients []ActorID, text string, now time.Time) {
	if recipients == nil {
		recipients = []ActorID{}
	}
	w.emit(&Spoke{
		SpeakerID:    speaker,
		HuddleID:     huddle,
		RecipientIDs: recipients,
		Text:         text,
		At:           now,
	})
}

// --- canned speech ---------------------------------------------------------

func summonCommissionText(targetName, reason string) string {
	if reason != "" {
		return fmt.Sprintf("Go and fetch %s for me. %s", targetName, reason)
	}
	return fmt.Sprintf("Go and fetch %s for me.", targetName)
}

func summonAcknowledgeText() string {
	return "At once."
}

func summonDeliveryText(targetName, summonerName, meetLabel, reason string) string {
	if reason != "" {
		return fmt.Sprintf("%s, %s summons you — come to %s. %s", targetName, summonerName, meetLabel, reason)
	}
	return fmt.Sprintf("%s, %s summons you — come to %s.", targetName, summonerName, meetLabel)
}

func summonRefusalText(targetName string) string {
	return fmt.Sprintf("I could not find %s anywhere.", targetName)
}

// ActiveSummonErrandDTO is one live errand on the umbilical summon-errands
// wire: the in-flight mirror of FinishedSummonErrand (no outcome yet).
type ActiveSummonErrandDTO struct {
	ID            ErrandID           `json:"id"`
	State         string             `json:"state"`
	SummonerID    ActorID            `json:"summoner_id"`
	SummonerName  string             `json:"summoner_name"`
	MessengerID   ActorID            `json:"messenger_id,omitempty"`
	TargetID      ActorID            `json:"target_id"`
	TargetName    string             `json:"target_name"`
	Reason        string             `json:"reason,omitempty"`
	MeetStructure StructureID        `json:"meet_structure_id,omitempty"`
	DispatchedAt  time.Time          `json:"dispatched_at"`
	History       []SummonStateStamp `json:"history"`
}

// SummonErrandsReportResult is the SummonErrandsReport payload: live errands
// (dispatch order) + the recent finished ring (oldest first).
type SummonErrandsReportResult struct {
	Active []ActiveSummonErrandDTO `json:"active"`
	Recent []FinishedSummonErrand  `json:"recent"`
}

// SummonErrandsReport returns a Command that snapshots the live errand map +
// the recent-errands ring for the umbilical GET /summon-errands read (LLM-414
// observability DoD). Read-only; names resolved live.
func SummonErrandsReport() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			out := SummonErrandsReportResult{
				Active: []ActiveSummonErrandDTO{},
				Recent: append([]FinishedSummonErrand{}, w.recentSummonErrands...),
			}
			for _, e := range w.SummonErrands {
				if e == nil {
					continue
				}
				out.Active = append(out.Active, ActiveSummonErrandDTO{
					ID:            e.ID,
					State:         string(e.State),
					SummonerID:    e.SummonerID,
					SummonerName:  targetDisplayName(w, e.SummonerID),
					MessengerID:   e.MessengerID,
					TargetID:      e.TargetID,
					TargetName:    targetDisplayName(w, e.TargetID),
					Reason:        e.Reason,
					MeetStructure: e.MeetStructureID,
					DispatchedAt:  e.DispatchedAt,
					History:       append([]SummonStateStamp(nil), e.History...),
				})
			}
			sort.Slice(out.Active, func(i, j int) bool { return out.Active[i].ID < out.Active[j].ID })
			return out, nil
		},
	}
}

// handleSummonResponseFade is the inline subscriber that fades summon cues.
// The two cues fade on DIFFERENT verbs (LLM-414):
//
//   - SummonRefusAL (summoner-side, informational) keeps the v1 posture: any
//     move / speak / take_break by its holder is acting past the news, so all
//     three clear it.
//
//   - PendingSummon (target-side, actionable) is deliberately STICKIER than
//     v1. The live incident showed why the v1 fade loses the summons on weak
//     models: a spoken "Aye, I'm coming" is terminal-on-success, cleared the
//     cue, and the next tick had no summons left to act on — and a move
//     ANYWHERE (home, in the incident) also read as an answer. Now the cue
//     survives the target's speech and its walk (the walk toward the meet
//     place IS the answer in progress, and the arrival tick needs the cue as
//     scene context for the greeting — a shared-VA target has no transcript
//     memory of the delivery). It clears on:
//
//   - the meeting itself (completeSummonMeeting / finishErrand),
//
//   - take_break — an explicit settling-in that says "not going",
//
//   - the errand's TTL sweep (finishErrand("expired")),
//     and perception additionally stops rendering a cue older than
//     summonCueRenderTTL as read-time defense (perception/summon.go).
//
// Cheap: one map lookup + a nil-field check per response event. Runs on the
// world goroutine during emit.
func handleSummonResponseFade(w *World, evt Event) {
	var actorID ActorID
	clearPending := false
	switch e := evt.(type) {
	case *ActorMoveStarted:
		actorID = e.ActorID
	case *TookBreak:
		actorID = e.ActorID
		clearPending = true
	case *Spoke:
		actorID = e.SpeakerID
	default:
		return
	}
	if a, ok := w.Actors[actorID]; ok && a != nil {
		a.SummonRefusal = nil
		if clearPending {
			a.PendingSummon = nil
		}
	}
}
