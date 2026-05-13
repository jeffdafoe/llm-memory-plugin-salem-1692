package sim

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Huddle / Scene lifecycle commands. In-memory port of the legacy
// engine/scene_huddles.go + engine/scenes.go primitives, redesigned per
// the Phase 2 plan to:
//
//  1. Collapse the legacy NPC/PC join split into one path (ActorID is
//     unified across populations in the in-memory model).
//  2. Eliminate parallel-huddle consolidation logic — single-goroutine
//     ownership means at most one active huddle per structure by
//     construction.
//  3. Move acquaintance recording, audit emission, greet/farewell, and
//     loiter-slot adoption out of the lifecycle primitive — those are
//     event subscribers (see events.go), not bolt-ons inside join.
//  4. Make peer-change actionable for tick gating: every join/leave
//     stamps WarrantedSince on every affected actor so the reactor
//     scheduler (Phase 2 PR 2) can fire ticks state-first instead of
//     timer-first.
//  5. Capture per-actor snapshots at scene mint so perception build
//     (Phase 2 PR 3) can diff "what changed since this scene started"
//     — the seam that fixes the "Prudence thinks she has bread" /
//     Moses-James circles bug class without bolting on more defensive
//     scaffolding (legacy speech_state_claim_gate.go).

// JoinHuddleResult is the payload returned by the JoinHuddle command.
// HuddleNew is true when the join created a fresh huddle; OtherMembers
// lists the actors who were already in the huddle at the moment of join
// (not including the joining actor).
type JoinHuddleResult struct {
	HuddleID     HuddleID
	HuddleNew    bool
	OtherMembers []ActorID
}

// LeaveHuddleResult is the payload returned by LeaveHuddle. Concluded is
// true when the departure left the huddle empty and triggered conclusion.
// RemainingMembers lists the actors still in the huddle after the leave
// (empty when Concluded=true).
type LeaveHuddleResult struct {
	HuddleID         HuddleID
	Concluded        bool
	RemainingMembers []ActorID
}

// CreateScene returns a Command that mints a fresh Scene at cascade
// origin. originStructureID may be empty for non-structure-tied cascades
// (chronicler atmosphere refresh, admin-triggered fires); in that case
// ParticipantStateAtOrigin is empty and no origin huddle is associated.
//
// When originStructureID is set, the scene:
//   - is rejected with an error if the structure is unknown — silent
//     mint at a typo'd structureID would produce a scene that fails
//     downstream perception lookups in non-obvious ways;
//   - captures the snapshot of every actor currently inside that
//     structure into ParticipantStateAtOrigin (perception build within
//     the scene reads those snapshots to diff "what changed for me
//     since the scene started" — the diff seam supporting loop
//     detection and inventory-continuity claims);
//   - adds the structure's currently active huddle (if any) to
//     Scene.Huddles so the scene observes the in-flight conversation
//     from mint instead of only catching huddles that form during the
//     scene's lifetime.
//
// Returns the new SceneID through the command reply.
func CreateScene(originKind string, originStructureID StructureID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if originStructureID != "" {
				if _, ok := w.Structures[originStructureID]; !ok {
					return SceneID(""), fmt.Errorf("structure %q not found", originStructureID)
				}
			}

			id := SceneID(newSceneID())
			scene := &Scene{
				ID:                       id,
				OriginAt:                 now,
				OriginKind:               originKind,
				OriginStructureID:        originStructureID,
				Huddles:                  make(map[HuddleID]struct{}),
				ParticipantStateAtOrigin: make(map[ActorID]*ActorSnapshot),
			}

			if originStructureID != "" {
				// Capture snapshots of every actor observable in the
				// scene at mint: union of (a) actors physically present
				// inside the structure (actorsByStructure index), and
				// (b) members of the structure's active huddle (which
				// may not yet be in actorsByStructure until the
				// locomotion port lands InsideStructureID-setting
				// commands in a later phase). Long-term the two sets
				// are identical by invariant — joining a huddle
				// requires physical presence at the structure — but
				// the union keeps PR 1 robust against the not-yet-
				// wired locomotion gap and produces the right diff
				// baseline either way.
				captured := map[ActorID]struct{}{}
				capture := func(actorID ActorID) {
					if _, seen := captured[actorID]; seen {
						return
					}
					a, ok := w.Actors[actorID]
					if !ok {
						return
					}
					scene.ParticipantStateAtOrigin[actorID] = snapshotActor(a, w.TickCounter)
					captured[actorID] = struct{}{}
				}
				if members, ok := w.actorsByStructure[originStructureID]; ok {
					for actorID := range members {
						capture(actorID)
					}
				}

				// Associate the structure's active huddle (if any) so
				// the scene observes the in-flight conversation from
				// mint, and capture its members' baselines too.
				if huddleID, ok := findActiveHuddleAt(w, originStructureID); ok {
					scene.Huddles[huddleID] = struct{}{}
					if h := w.Huddles[huddleID]; h != nil {
						for actorID := range h.Members {
							capture(actorID)
						}
					}
				}
			}

			w.Scenes[id] = scene
			w.emit(SceneMinted{
				SceneID:           id,
				OriginKind:        originKind,
				OriginStructureID: originStructureID,
				At:                now,
			})
			return id, nil
		},
	}
}

// JoinHuddle returns a Command that places actorID in the active huddle
// at structureID, creating one if none exists. Atomic: any prior huddle
// the actor was in is left first (concluding it if empty), then the
// target huddle is joined; back-references and indices flip together;
// tick warrants stamp on every affected actor; events emit.
//
// sceneID may be empty. When non-empty, the joined huddle is added to
// the scene's Huddles set so subsequent perception builds for the scene
// observe this huddle. A non-empty sceneID that does not match a known
// scene is rejected with an error — silently dropping the association
// would produce subscriber/perception state that contradicts the
// emitted HuddleJoined event's SceneID field.
//
// Same-huddle idempotency: when the actor is already in the active
// huddle at structureID (typically because a redundant join command
// arrived, or because two arrival paths both fired), no leave/rejoin
// churn happens. The current huddle is returned with HuddleNew=false
// and the current other-members list; no events fire and no warrants
// are stamped. The only mutation in the idempotent path is the optional
// scene association — repeated joins under different sceneIDs progress
// the scene's observation set without recycling the huddle.
//
// One join path replaces the legacy NPC/PC split — actorID is unified
// across populations in the in-memory model. Single-goroutine ownership
// of huddle state eliminates the parallel-huddle race that motivated
// legacy consolidateHuddlesAtStructure; no consolidation logic exists
// here.
//
// Returns JoinHuddleResult through the command reply.
func JoinHuddle(actorID ActorID, structureID StructureID, sceneID SceneID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return JoinHuddleResult{}, fmt.Errorf("actor %q not found", actorID)
			}
			if structureID == "" {
				return JoinHuddleResult{}, fmt.Errorf("structureID required")
			}
			if _, ok := w.Structures[structureID]; !ok {
				return JoinHuddleResult{}, fmt.Errorf("structure %q not found", structureID)
			}
			var scene *Scene
			if sceneID != "" {
				s, ok := w.Scenes[sceneID]
				if !ok {
					return JoinHuddleResult{}, fmt.Errorf("scene %q not found", sceneID)
				}
				scene = s
			}

			// Same-huddle idempotency: actor already in the active huddle
			// at this structure. Skip the leave/rejoin churn (which would
			// emit a fake HuddleLeft, possibly conclude the huddle if
			// they're alone, mint a new HuddleID, and emit fake ActorMet
			// events for peers they already know). Optional scene
			// association still happens — that's the one bit of state
			// progress a repeated join can legitimately carry.
			if actor.CurrentHuddleID != "" {
				if current, ok := w.Huddles[actor.CurrentHuddleID]; ok &&
					current.ConcludedAt == nil &&
					current.StructureID == structureID {
					if scene != nil {
						scene.Huddles[actor.CurrentHuddleID] = struct{}{}
					}
					others := make([]ActorID, 0, len(current.Members))
					for id := range current.Members {
						if id != actorID {
							others = append(others, id)
						}
					}
					return JoinHuddleResult{
						HuddleID:     actor.CurrentHuddleID,
						HuddleNew:    false,
						OtherMembers: others,
					}, nil
				}
				// Different huddle (or stale back-ref): leave first.
				// Atomic with the join — no window where the actor is in
				// two huddles or zero huddles from an external observer's
				// perspective (snapshot publishes only after the entire
				// Fn returns).
				leaveCurrentHuddle(w, actor, now)
			}

			// Find or create the active huddle at this structure. By
			// invariant there is at most one active huddle per structure,
			// so the search returns the unique one or signals create.
			huddleID, exists := findActiveHuddleAt(w, structureID)
			var huddle *Huddle
			huddleNew := false
			if !exists {
				huddleID = HuddleID(newHuddleID())
				huddle = &Huddle{
					ID:          huddleID,
					Members:     make(map[ActorID]struct{}),
					StructureID: structureID,
					StartedAt:   now,
				}
				w.Huddles[huddleID] = huddle
				huddleNew = true
			} else {
				huddle = w.Huddles[huddleID]
			}

			// Capture other members BEFORE adding the joiner — the
			// HuddleJoined / ActorMet payloads describe "who was here
			// when I arrived."
			otherMembers := make([]ActorID, 0, len(huddle.Members))
			for id := range huddle.Members {
				otherMembers = append(otherMembers, id)
			}

			huddle.Members[actorID] = struct{}{}
			actor.CurrentHuddleID = huddleID
			if w.actorsByHuddle[huddleID] == nil {
				w.actorsByHuddle[huddleID] = make(map[ActorID]struct{})
			}
			w.actorsByHuddle[huddleID][actorID] = struct{}{}

			if scene != nil {
				scene.Huddles[huddleID] = struct{}{}
			}

			// Tick-warrant the joiner and every prior member: peer change
			// is actionable. Idempotent — already-warranted actors
			// preserve their original WarrantedSince timestamp.
			stampWarrant(actor, now)
			for _, id := range otherMembers {
				if other, ok := w.Actors[id]; ok {
					stampWarrant(other, now)
				}
			}

			w.emit(HuddleJoined{
				ActorID:      actorID,
				HuddleID:     huddleID,
				SceneID:      sceneID,
				StructureID:  structureID,
				OtherMembers: otherMembers,
				HuddleNew:    huddleNew,
				At:           now,
			})
			// One ActorMet per pair so subscribers receive the full set
			// of introductions without having to derive pairs from the
			// HuddleJoined event.
			for _, otherID := range otherMembers {
				w.emit(ActorMet{
					A:        actorID,
					B:        otherID,
					HuddleID: huddleID,
					At:       now,
				})
			}

			return JoinHuddleResult{
				HuddleID:     huddleID,
				HuddleNew:    huddleNew,
				OtherMembers: otherMembers,
			}, nil
		},
	}
}

// LeaveHuddle returns a Command that removes actorID from their current
// huddle. If the huddle becomes empty, it is concluded. Stamps tick
// warrants on remaining members. Emits HuddleLeft and (when applicable)
// HuddleConcluded.
//
// No-op when the actor is not currently in any huddle (returns a zero
// LeaveHuddleResult with no error).
func LeaveHuddle(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return LeaveHuddleResult{}, fmt.Errorf("actor %q not found", actorID)
			}
			if actor.CurrentHuddleID == "" {
				return LeaveHuddleResult{}, nil
			}
			return leaveCurrentHuddle(w, actor, now), nil
		},
	}
}

// ConcludeHuddle returns a Command that force-concludes a huddle,
// evicting all members. Used for admin operations and structured
// shutdown sweeps where individual leaves aren't appropriate.
//
// Idempotent: re-concluding an already-concluded huddle is a no-op.
// Emits HuddleConcluded; no per-member HuddleLeft events fire (this is
// a bulk operation, not a sequence of individual leaves).
func ConcludeHuddle(huddleID HuddleID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			huddle, ok := w.Huddles[huddleID]
			if !ok {
				return nil, fmt.Errorf("huddle %q not found", huddleID)
			}
			if huddle.ConcludedAt != nil {
				return nil, nil
			}

			members := make([]ActorID, 0, len(huddle.Members))
			for id := range huddle.Members {
				members = append(members, id)
			}
			for _, actorID := range members {
				if actor, ok := w.Actors[actorID]; ok {
					actor.CurrentHuddleID = ""
					stampWarrant(actor, now)
				}
			}
			huddle.Members = make(map[ActorID]struct{})
			t := now
			huddle.ConcludedAt = &t
			delete(w.actorsByHuddle, huddleID)
			detachHuddleFromAllScenes(w, huddleID)

			w.emit(HuddleConcluded{
				HuddleID:    huddleID,
				StructureID: huddle.StructureID,
				At:          now,
			})
			return nil, nil
		},
	}
}

// leaveCurrentHuddle implements the leave half of join (atomic transition)
// and the body of LeaveHuddle. Mutates state, stamps warrants on every
// state-changed actor (the leaver, plus any remaining members observing
// the peer departure), emits events. Caller must verify
// actor.CurrentHuddleID is set.
//
// If the huddle's back-ref is stale (CurrentHuddleID points at a missing
// huddle), the back-ref is cleared and a zero result returned — the
// caller's intent (leave whatever huddle the actor thought they were in)
// is satisfied without panicking on the stale pointer.
//
// On the leaver-was-last-member path, the huddle is concluded: ConcludedAt
// stamped, the huddle is detached from every Scene.Huddles set that
// referenced it (Scene.Huddles is "currently observed active huddles" —
// readers don't need to filter ConcludedAt), and HuddleConcluded fires
// after HuddleLeft so subscribers can rely on the ordering.
//
// Unexported by design — see buildWalkGrid for the rationale; lifecycle
// helpers stay package-internal so external callers can only reach the
// transition through Commands.
func leaveCurrentHuddle(w *World, actor *Actor, now time.Time) LeaveHuddleResult {
	huddleID := actor.CurrentHuddleID
	huddle, ok := w.Huddles[huddleID]
	if !ok {
		actor.CurrentHuddleID = ""
		return LeaveHuddleResult{}
	}

	delete(huddle.Members, actor.ID)
	actor.CurrentHuddleID = ""
	// The leaver's macro-state changed (they're no longer in this huddle);
	// the warrant flag preserves oldest timestamp so a stamp here doesn't
	// clobber an earlier one, but explicit LeaveHuddle from an unwarranted
	// actor needs to register an actionable change for the scheduler.
	stampWarrant(actor, now)
	if members, ok := w.actorsByHuddle[huddleID]; ok {
		delete(members, actor.ID)
		if len(members) == 0 {
			delete(w.actorsByHuddle, huddleID)
		}
	}

	remaining := make([]ActorID, 0, len(huddle.Members))
	for id := range huddle.Members {
		remaining = append(remaining, id)
	}
	for _, id := range remaining {
		if other, ok := w.Actors[id]; ok {
			stampWarrant(other, now)
		}
	}

	w.emit(HuddleLeft{
		ActorID:          actor.ID,
		HuddleID:         huddleID,
		StructureID:      huddle.StructureID,
		RemainingMembers: remaining,
		At:               now,
	})

	concluded := false
	if len(huddle.Members) == 0 {
		t := now
		huddle.ConcludedAt = &t
		concluded = true
		detachHuddleFromAllScenes(w, huddleID)
		w.emit(HuddleConcluded{
			HuddleID:    huddleID,
			StructureID: huddle.StructureID,
			At:          now,
		})
	}

	return LeaveHuddleResult{
		HuddleID:         huddleID,
		Concluded:        concluded,
		RemainingMembers: remaining,
	}
}

// detachHuddleFromAllScenes removes the huddle from every scene's
// observation set. Called when a huddle concludes (either through a
// last-member leave or an explicit ConcludeHuddle) so Scene.Huddles
// keeps its "currently active observed huddles" contract — readers do
// not need to filter ConcludedAt themselves.
//
// O(scenes); active scene count is bounded by the cascade-origin rate
// times scene lifetime, which is small (single-digit scenes typical).
// If profiling shows this is hot, a per-huddle reverse index can be
// added later without changing callers.
//
// Unexported by design.
func detachHuddleFromAllScenes(w *World, huddleID HuddleID) {
	for _, scene := range w.Scenes {
		delete(scene.Huddles, huddleID)
	}
}

// findActiveHuddleAt returns the active huddle at structureID and true,
// or zero/false if none exists. By the single-active-huddle-per-structure
// invariant (single-goroutine ownership eliminates the parallel-huddle
// race), at most one match exists; the linear scan is over total Huddles
// because PR 1 doesn't index huddles by structure (a future PR can add
// the index when the scan becomes a profile hot spot).
//
// Unexported by design.
func findActiveHuddleAt(w *World, structureID StructureID) (HuddleID, bool) {
	for id, h := range w.Huddles {
		if h.StructureID == structureID && h.ConcludedAt == nil {
			return id, true
		}
	}
	return "", false
}

// stampWarrant marks an actor as having actionable state-change since
// their last tick. Reactor scheduler (Phase 2 PR 2) reads WarrantedSince
// to gate scheduled ticks: a tick fires when warranted OR when timer
// floor reached. Tick handler clears the field on consumption (Phase 2
// PR 3).
//
// Idempotent: an already-warranted actor preserves its original timestamp
// so the scheduler can sort oldest-warrant-first when load-shedding.
//
// Unexported by design — warrant stamping is the privilege of mutation
// commands; consumers (perception, scheduler) only read.
func stampWarrant(a *Actor, now time.Time) {
	if a.WarrantedSince != nil {
		return
	}
	t := now
	a.WarrantedSince = &t
}

// newHuddleID mints a random HuddleID. v1 uses 16 random bytes hex-
// encoded; the pg impl at cutover will likely use UUIDv7 so chronological
// ordering falls out of the ID. Bytes-then-hex is sufficient for the
// in-memory test path and any v1 SaveSnapshot writer that needs a
// deterministic-shape string.
func newHuddleID() string {
	return "hud-" + randomHex(16)
}

// newSceneID mints a random SceneID. Same rationale as newHuddleID.
func newSceneID() string {
	return "sc-" + randomHex(16)
}

// randomHex returns hex-encoded random bytes of the requested length.
// Panics on read failure — crypto/rand is documented to never return an
// error on platforms we run on, and we'd rather discover any future
// degradation immediately than silently mint colliding IDs.
func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic("sim: rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
