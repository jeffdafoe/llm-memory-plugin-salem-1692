package sim

import (
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"
)

// create_pc.go — PC onboarding (the v2 port of v1's pc/create). Materializes a
// brand-new PC actor for an already-authenticated user (identified by
// login_username), seeds needs + a starter purse, places them at the village
// inn, and grants a free first-night bedroom so they arrive already lodged —
// matching v1's "you have a room from minute one" feel. After the free first
// night the PC is an ordinary lodger: the auto-rebook sweep charges night 2 if
// they have coins, or the grant expires and EvictExpiredOccupants relocates
// them to the common room.
//
// Lives in package sim (not httpapi) because materialization touches unexported
// world internals (the actor index, setActorInsideStructure, seedVisitorNeeds).
// The httpapi handler is a thin validate-and-delegate shell over this command,
// the same posture as sim.PayWithItem / sim.MoveActor.
//
// Onboarding context: there is no public account signup — a "player" is an
// llm-memory account provisioned out-of-band; this is only the in-village
// provisioning step. Idempotent: re-running for an existing PC updates the
// display name + sprite (and ensures needs), it does NOT re-lodge — onboarding
// lodging is a one-time welcome, and the realistic re-call is a name/sprite
// tweak.

// PCStarterCoins is the welcome purse a freshly-created PC starts with — enough
// to cover several nights of rent (default lodging is 28/week = 4/night) so a
// new player isn't evicted before they've earned anything. Tunable.
const PCStarterCoins = 100

// starterLodgerLedgerID is the synthetic ledger id stamped on a new PC's
// free first-night RoomAccess grant. v2 models the lodger relationship as the
// RoomAccess grant itself (not a persisted pay_ledger row), but the grant's
// validation requires a positive granted-via-ledger id. This is a marker, not a
// real ledger entry (the first night is free — offered_amount 0 in v1); it's
// overwritten by the real rebook ledger id when auto-rebook first renews. Not a
// map key, so sharing one value across PCs is harmless.
const starterLodgerLedgerID int64 = 1

// ErrUnknownSprite is returned when CreatePC is given a sprite_id absent from
// the catalog. Exported so the httpapi handler maps it to 400 by identity.
var ErrUnknownSprite = errors.New("unknown sprite_id")

// CreatePCResult reports the outcome of a CreatePC command.
type CreatePCResult struct {
	ActorID            ActorID
	Created            bool // false when an existing PC was updated instead
	LodgingStructureID StructureID
}

// CreatePC materializes or idempotently updates the PC actor for loginUsername.
// characterName is the in-world display name; spriteID is optional ("" = leave
// the sprite unset so the client opens its picker). now is the creation clock.
func CreatePC(loginUsername, characterName, spriteID string, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if spriteID != "" && w.Sprites[SpriteID(spriteID)] == nil {
				return CreatePCResult{}, ErrUnknownSprite
			}

			// Idempotent update path: an existing PC for this login keeps its
			// row + lodging; we just refresh name/sprite and ensure needs.
			if existingID, ok := findPCByLoginUsername(w, loginUsername); ok {
				a := w.Actors[existingID]
				a.DisplayName = characterName
				if spriteID != "" {
					a.SpriteID = SpriteID(spriteID)
				}
				if len(a.Needs) == 0 {
					a.Needs = seedVisitorNeeds()
				}
				return CreatePCResult{ActorID: existingID, Created: false}, nil
			}

			id := mintPCActorID(w)
			if id == "" {
				return CreatePCResult{}, fmt.Errorf("sim: CreatePC: actor-ID minting exhausted retries")
			}
			actor := &Actor{
				ID:             id,
				DisplayName:    characterName,
				Kind:           KindPC,
				LoginUsername:  loginUsername,
				SpriteID:       SpriteID(spriteID), // "" when not yet picked
				Needs:          seedVisitorNeeds(),
				Inventory:      map[ItemKind]int{},
				Coins:          PCStarterCoins,
				State:          StateIdle,
				StateEnteredAt: now,
			}
			w.Actors[id] = actor
			w.outdoorActors[id] = struct{}{}

			result := CreatePCResult{ActorID: id, Created: true}

			// Place at the inn + grant a free first-night bedroom. Best-effort:
			// if no lodging structure is placed, or it has no private rooms, the
			// PC is still created — just outdoors and unlodged (they can rent via
			// the normal flow). Mirrors v1's soft-fail starter seeding.
			if lodgingID, ok := findLodgingStructure(w); ok {
				if st := w.Structures[lodgingID]; st != nil {
					actor.Pos = st.Position
					setActorInsideStructure(w, actor, lodgingID)
					loc := w.Settings.Location
					if loc == nil {
						loc = time.UTC
					}
					expiresAt := ComputeLodgerUntil(now, 1, w.Settings.LodgingCheckOutHour, loc)
					if _, err := AssignBedroomForLodger(lodgingID, id, starterLodgerLedgerID, expiresAt).Fn(w); err != nil {
						log.Printf("sim: CreatePC: %s starter bedroom at %s soft-failed: %v", id, lodgingID, err)
					}
					result.LodgingStructureID = lodgingID
				}
			}

			return result, nil
		},
	}
}

// findPCByLoginUsername returns the id of the PC actor whose LoginUsername
// matches, or ok=false. Sim-package twin of httpapi.findPCByLogin (which can't
// be reached from here); login_username is unique by construction so the first
// match is authoritative.
func findPCByLoginUsername(w *World, loginUsername string) (ActorID, bool) {
	for id, a := range w.Actors {
		if a != nil && a.Kind == KindPC && a.LoginUsername == loginUsername {
			return id, true
		}
	}
	return "", false
}

// findLodgingStructure picks the village's lodging structure: a structure-backed
// VillageObject tagged "lodging", preferring a pure inn over a tavern-combo
// (mirrors v1's IS_TAVERN ordering — a dedicated inn is the quintessential
// traveler's home), deterministic by id within a tier. ok=false when none is
// placed.
func findLodgingStructure(w *World) (StructureID, bool) {
	var pureInns, taverns []VillageObjectID
	for objID, o := range w.VillageObjects {
		if o == nil || !o.HasTag("lodging") {
			continue
		}
		if _, ok := w.Structures[StructureID(objID)]; !ok {
			continue
		}
		if o.HasTag("tavern") {
			taverns = append(taverns, objID)
		} else {
			pureInns = append(pureInns, objID)
		}
	}
	pick := func(ids []VillageObjectID) (StructureID, bool) {
		if len(ids) == 0 {
			return "", false
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return StructureID(ids[0]), true
	}
	if id, ok := pick(pureInns); ok {
		return id, true
	}
	return pick(taverns)
}

// mintPCActorID generates a fresh random v4 UUID actor id (the actor.id column
// is uuid; v1 used gen_random_uuid()), retrying on the astronomically unlikely
// collision with an existing actor. "" on exhaustion.
func mintPCActorID(w *World) ActorID {
	for attempt := 0; attempt < 10; attempt++ {
		candidate := ActorID(newUUIDv4())
		if _, exists := w.Actors[candidate]; !exists {
			return candidate
		}
	}
	return ""
}

// newUUIDv4 returns a canonical random version-4 UUID string. Uses crypto/rand
// (panics on read failure, like randomHex — we'd rather fail loudly than mint
// colliding ids).
func newUUIDv4() string {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		panic("sim: rand.Read failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
