package sim

import (
	"log"
	"sort"
	"time"
)

// arrival_business_huddle.go — ZBBS-HOME-425. The indoor-arrival huddle
// bootstrap for STAFFED BUSINESSES, closing the hospitality gap left between
// two deliberate boundaries:
//
//   - The arrival-encounter cascade is outdoor-only (cascade/
//     arrival_encounter.go skips any arriver with InsideStructureID != "").
//   - EnsureColocatedHuddle (this package) bootstraps the indoor huddle only
//     from a SPEAK.
//
// So a customer who walked into the tavern stood in silence until they spoke
// first — and the businessowner hospitality greet (which fires on
// HuddleJoined with a keeper already present) could never fire on entry,
// despite being built precisely because cold-on-entry "reads as cold"
// (businessowner.go). Observed live 2026-06-11: a PC entered the tavern with
// the keeper at post and got nothing.
//
// EnsureArrivalBusinessHuddle forms the conversation ON the arrival, but only
// when the structure has an at-post keeper to receive the customer — a home,
// or a shop whose keeper is away/asleep, forms no huddle (walk-throughs are
// never grabbed; that churn is exactly what HOME-358/363 kept the speak-time
// trigger boundary tight against). Join order is load-bearing: keepers join
// FIRST, the arriver LAST, so the greet subscriber sees the keeper among
// HuddleJoined.OtherMembers when the arriver's join fires — the existing
// greet path (at-post check, cooldowns, reactor suppression) runs unchanged.
func EnsureArrivalBusinessHuddle(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok || actor == nil {
				return nil, nil
			}
			// Conversational kinds only — a decorative villager drifting home
			// must not mint a huddle with a keeper.
			switch actor.Kind {
			case KindPC, KindNPCStateful, KindNPCShared:
			default:
				return nil, nil
			}
			if actor.CurrentHuddleID != "" {
				return nil, nil // already conversing
			}
			structureID := actor.InsideStructureID
			if structureID == "" {
				return nil, nil // outdoor arrivals belong to the encounter cascade
			}
			// The structure's own keeper returning to post greets no one.
			if actor.BusinessownerState != nil && actor.WorkStructureID == structureID {
				return nil, nil
			}
			// A ghost PC (closed tab, stale /pc/me stamp) must not be welcomed
			// — same gate the encounter cascade and the speak-time pull-in use
			// (ZBBS-WORK-326).
			if actor.Kind == KindPC && PCPresenceStale(actor.LastPCSeenAt, now, PCPresenceStaleAfter(w)) {
				return nil, nil
			}

			// Receiving keepers: at their own post inside this structure and
			// receptive. Mirrors the greet subscriber's keeper gates
			// (cascade/businessowner.go) so we never mint a huddle the greet
			// would then skip anyway.
			var keepers []ActorID
			for id, a := range w.Actors {
				if a == nil || id == actor.ID || a.BusinessownerState == nil {
					continue
				}
				if a.WorkStructureID != structureID || a.InsideStructureID != structureID {
					continue
				}
				if a.State == StateSleeping || a.State == StateResting {
					continue
				}
				if a.CurrentHuddleID != "" {
					continue // already conversing — the arriver joins their huddle below
				}
				keepers = append(keepers, id)
			}

			// An already-active structure huddle counts only if an at-post
			// keeper is conversing in it — the arriver then joins that
			// conversation (and the greet fires for the join). Without a
			// keeper in the picture this command does nothing: two villagers
			// chatting must not have every passer-through auto-joined.
			huddleID, hasActive := findActiveHuddleAt(w, structureID)
			activeHasKeeper := false
			if hasActive {
				for pid := range w.actorsByHuddle[huddleID] {
					p := w.Actors[pid]
					if p == nil || p.BusinessownerState == nil || p.CurrentHuddleID != huddleID {
						continue
					}
					if p.WorkStructureID == structureID && p.State != StateSleeping && p.State != StateResting {
						activeHasKeeper = true
						break
					}
				}
			}
			if len(keepers) == 0 && !activeHasKeeper {
				return nil, nil
			}

			// Same scene anchoring as the speak-time bootstrap — the
			// transaction tools need it (ZBBS-HOME-375).
			sceneID, sceneErr := findOrCreateStructureScene(w, structureID, now)
			if sceneErr != nil {
				log.Printf("sim: EnsureArrivalBusinessHuddle scene for %q: %v", structureID, sceneErr)
				return nil, nil
			}

			// Keepers first (sorted for deterministic event order), arriver
			// last — the ordering the greet subscriber depends on.
			sort.Slice(keepers, func(i, j int) bool { return keepers[i] < keepers[j] })
			for _, id := range keepers {
				if _, err := JoinHuddle(id, structureID, sceneID, now).Fn(w); err != nil {
					log.Printf("sim: EnsureArrivalBusinessHuddle join keeper %q at %q: %v", id, structureID, err)
				}
			}
			if _, err := JoinHuddle(actor.ID, structureID, sceneID, now).Fn(w); err != nil {
				log.Printf("sim: EnsureArrivalBusinessHuddle join arriver %q at %q: %v", actor.ID, structureID, err)
			}
			return nil, nil
		},
	}
}
