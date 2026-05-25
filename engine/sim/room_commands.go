package sim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"
)

// Room commands — AssignBedroomForLodger, ExpireRoomAccess,
// EvictExpiredOccupants. In-memory port of the engine/room.go mutation
// helpers.

// ErrNoPrivateRooms is returned by AssignBedroomForLodger when the
// structure has zero private rooms declared (distinct from "all
// private rooms occupied"). Lets callers surface the operator-data
// case differently from the runtime-contention case (per legacy
// observation 2026-05-11: an Inn tagged for lodging with no bedrooms
// landed paid lodgers without rooms).
var ErrNoPrivateRooms = errors.New("structure has no private bedrooms")

// AssignBedroomResult is the command-reply payload — the assigned room
// (zero on contention), and the actor's pre-assignment room (for
// rollback by callers if the broader transaction fails).
type AssignBedroomResult struct {
	RoomID        RoomID
	PreviousRoom  RoomID
	WasReassigned bool // true if the buyer already had an active access in this structure
}

// AssignBedroomForLodger returns a Command that picks an available
// private bedroom in structureID and grants buyerID access:
//
//  1. If buyerID already has an active access on a private room in
//     this structure, extend that one (extension, not room-hop —
//     "ON CONFLICT extend" semantics from legacy).
//  2. Otherwise pick the first private room without an active access,
//     deterministically by Name ASC.
//
// On success, also stamps Actor.InsideRoomID so the lodger is in their
// room immediately after the keeper's deliver_order.
//
// Returns (0, ErrNoPrivateRooms) when the structure has no private
// rooms at all. Returns (0, nil) — RoomID=0 — when all private rooms
// are occupied by OTHER actors (callers surface as "Try again
// shortly").
func AssignBedroomForLodger(structureID StructureID, buyerID ActorID, ledgerID int64, expiresAt time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			structure, ok := w.Structures[structureID]
			if !ok {
				return AssignBedroomResult{}, fmt.Errorf("structure %q not found", structureID)
			}
			actor, ok := w.Actors[buyerID]
			if !ok {
				return AssignBedroomResult{}, fmt.Errorf("actor %q not found", buyerID)
			}

			// Find private rooms in this structure, sorted by Name ASC.
			var privateRooms []*Room
			for _, r := range structure.Rooms {
				if r.Kind == RoomKindPrivate {
					privateRooms = append(privateRooms, r)
				}
			}
			if len(privateRooms) == 0 {
				return AssignBedroomResult{}, ErrNoPrivateRooms
			}
			sort.Slice(privateRooms, func(i, j int) bool {
				return privateRooms[i].Name < privateRooms[j].Name
			})

			previousRoom := actor.InsideRoomID
			if actor.RoomAccess == nil {
				actor.RoomAccess = make(map[RoomAccessKey]*RoomAccess)
			}

			// Branch 1: extend an existing active access (no room-hop).
			// Must be a private room in THIS structure, and no other
			// actor must have a conflicting active access on it.
			for _, r := range privateRooms {
				key := RoomAccessKey{RoomID: r.ID, Source: AccessSourceLedger}
				if existing, ok := actor.RoomAccess[key]; ok && existing.Active {
					if !roomOccupiedByOther(w, r.ID, buyerID) {
						existing.ExpiresAt = &expiresAt
						existing.LedgerID = ledgerID
						actor.InsideRoomID = r.ID
						return AssignBedroomResult{
							RoomID:        r.ID,
							PreviousRoom:  previousRoom,
							WasReassigned: true,
						}, nil
					}
				}
			}

			// Branch 2: pick the first unoccupied private room.
			now := time.Now().UTC()
			for _, r := range privateRooms {
				if roomOccupiedByAny(w, r.ID) {
					continue
				}
				actor.RoomAccess[RoomAccessKey{RoomID: r.ID, Source: AccessSourceLedger}] = &RoomAccess{
					RoomID:    r.ID,
					Source:    AccessSourceLedger,
					LedgerID:  ledgerID,
					ExpiresAt: &expiresAt,
					Active:    true,
					CreatedAt: now,
				}
				actor.InsideRoomID = r.ID
				return AssignBedroomResult{
					RoomID:       r.ID,
					PreviousRoom: previousRoom,
				}, nil
			}

			// All private rooms occupied by others.
			return AssignBedroomResult{PreviousRoom: previousRoom}, nil
		},
	}
}

// roomOccupiedByAny returns true if any actor in the world holds an
// active access on roomID.
func roomOccupiedByAny(w *World, roomID RoomID) bool {
	for _, a := range w.Actors {
		if ra, ok := a.RoomAccess[RoomAccessKey{RoomID: roomID, Source: AccessSourceLedger}]; ok && ra.Active {
			return true
		}
	}
	return false
}

// roomOccupiedByOther returns true if any actor OTHER than exceptID
// holds an active access on roomID.
func roomOccupiedByOther(w *World, roomID RoomID, exceptID ActorID) bool {
	for id, a := range w.Actors {
		if id == exceptID {
			continue
		}
		if ra, ok := a.RoomAccess[RoomAccessKey{RoomID: roomID, Source: AccessSourceLedger}]; ok && ra.Active {
			return true
		}
	}
	return false
}

// ExpireRoomAccessResult is the count of access rows flipped to
// inactive on this sweep.
type ExpireRoomAccessResult struct {
	Expired int
}

// ExpireRoomAccess returns a Command that flips Active=false on any
// RoomAccess row whose ExpiresAt has passed.
//
// Idempotent and cheap. Runs per server tick.
func ExpireRoomAccess(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			count := 0
			for _, actor := range w.Actors {
				for _, ra := range actor.RoomAccess {
					if !ra.Active || ra.ExpiresAt == nil {
						continue
					}
					if !ra.ExpiresAt.After(now) {
						ra.Active = false
						count++
					}
				}
			}
			return ExpireRoomAccessResult{Expired: count}, nil
		},
	}
}

// EvictedOccupant is one evicted PC — surfaced for narration
// (Hub-layer port translates to a private room_event broadcast).
type EvictedOccupant struct {
	ActorID     ActorID
	ActorName   string
	StructureID StructureID
	FromRoomID  RoomID
	ToRoomID    RoomID // common room of the same structure (0 if missing)
	Text        string
}

// EvictExpiredOccupantsResult is the per-tick eviction outcome.
type EvictExpiredOccupantsResult struct {
	Evicted []EvictedOccupant
}

// EvictExpiredOccupants returns a Command that moves PCs whose private-
// room access has lapsed back to the common room of the same structure.
//
// Pairs with ExpireRoomAccess: expiring the access row revokes the
// future gate, but the PC remains physically in the private room until
// this sweep relocates them.
//
// Filters to PCs (LoginUsername set) in private rooms with no active
// access. NPCs aren't affected — staff hold permanent access via
// WorkStructureID, owners via a different mechanism.
//
// Each evictee is surfaced to its client via a PCRelocatedToCommon event
// (translated to a private room_event narration frame), since v2 otherwise
// leaves the client's room scope stale until the next /pc/me poll. The
// narration line is drawn from the lodging-checkout phrase pool
// (pickLodgingNarration), not a single frozen string, and the same line rides
// the returned result so callers and tests see what the PC was shown. now
// stamps the emitted event.
func EvictExpiredOccupants(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			var evicted []EvictedOccupant
			for actorID, actor := range w.Actors {
				if actor.LoginUsername == "" {
					continue // PCs only
				}
				if actor.InsideRoomID == 0 {
					continue
				}
				room := findRoom(w, actor.InsideRoomID)
				if room == nil || room.Kind != RoomKindPrivate {
					continue
				}
				key := RoomAccessKey{RoomID: actor.InsideRoomID, Source: AccessSourceLedger}
				if ra, ok := actor.RoomAccess[key]; ok && ra.Active {
					continue // still has access
				}
				common := commonRoomForStructure(w, room.StructureID)
				fromRoom := actor.InsideRoomID
				actor.InsideRoomID = common
				text := pickLodgingNarration(LodgingReasonCheckout)
				evicted = append(evicted, EvictedOccupant{
					ActorID:     actorID,
					ActorName:   actor.DisplayName,
					StructureID: room.StructureID,
					FromRoomID:  fromRoom,
					ToRoomID:    common,
					Text:        text,
				})
				if text != "" {
					w.emit(&PCRelocatedToCommon{
						ActorID:     actorID,
						StructureID: room.StructureID,
						Reason:      LodgingReasonCheckout,
						Text:        text,
						At:          now,
					})
				}
			}
			return EvictExpiredOccupantsResult{Evicted: evicted}, nil
		},
	}
}

// RoomSweepInterval is how often RunRoomSweep wakes. Matches legacy
// cadence (server-tick interval, ~1 minute).
const RoomSweepInterval = time.Minute

// RunRoomSweep owns the room-access sweep goroutine. Wakes every
// RoomSweepInterval and submits, in order: RebookLodgersDue (renew grants in
// their 6h-pre-expiry window — must run before they're expired), then
// ExpireRoomAccess (flag truly-lapsed grants), then EvictExpiredOccupants
// (teleport PCs out of lapsed private rooms).
//
// Uses SendContext so shutdown unblocks the commands cleanly even if the
// world goroutine has already exited.
func RunRoomSweep(ctx context.Context, w *World) {
	t := time.NewTicker(RoomSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// One instant for the whole sweep: renew grants in their pre-
			// expiry window, then expire against the SAME now, so a grant
			// can't slip from "not yet due for rebook" to "expired" between
			// two separate clock reads (code_review).
			now := time.Now().UTC()
			if _, err := w.SendContext(ctx, RebookLodgersDue(now)); err != nil && ctx.Err() == nil {
				log.Printf("sim/room_sweep: rebook failed: %v", err)
				// Non-fatal — fall through to expire/evict so a rebook
				// hiccup doesn't strand lapsed grants un-swept.
			}
			if _, err := w.SendContext(ctx, ExpireRoomAccess(now)); err != nil && ctx.Err() == nil {
				log.Printf("sim/room_sweep: expire failed: %v", err)
				continue
			}
			if _, err := w.SendContext(ctx, EvictExpiredOccupants(now)); err != nil && ctx.Err() == nil {
				log.Printf("sim/room_sweep: evict failed: %v", err)
			}
		}
	}
}
