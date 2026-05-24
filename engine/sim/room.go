package sim

import "time"

// Room primitive (ZBBS-149) — first-class rooms within a structure with
// per-instance IDs and access control. In-memory port of engine/room.go.
//
// Three concepts:
//
//   - Room: per-instance room declaration. The Tavern has one "common"
//     (bar/floor) plus N "bedroom_*" private rooms.
//   - RoomAccess: who can enter a private or staff room. Lodgers get
//     access when deliver_order(nights_stay) flips fulfillment to
//     'delivered'. Common rooms don't need access — anyone can enter.
//   - Actor.InsideRoomID: which room the actor is currently in. Zero
//     when not inside any structure / room.
//
// Lifecycle:
//
//   - AssignBedroomForLodger picks an available private room and stamps
//     a RoomAccess row tied to the ledger.
//   - ExpireRoomAccess flips Active=false on rows whose ExpiresAt has
//     passed. Idempotent; pairs with EvictExpiredOccupants which
//     teleports a PC out of an expired private bedroom.
//   - EvictExpiredOccupants moves PCs whose private-room access lapsed
//     back to the common room of the same structure.

// RoomID is a per-instance room identifier (legacy: BIGSERIAL).
type RoomID int64

// RoomKind discriminates the three room categories.
type RoomKind string

const (
	RoomKindCommon  RoomKind = "common"
	RoomKindPrivate RoomKind = "private"
	RoomKindStaff   RoomKind = "staff"
)

// Room is one per-instance room declaration. Lives as a child of Structure.
type Room struct {
	ID          RoomID
	StructureID StructureID
	Kind        RoomKind
	Name        string // e.g. "common", "bedroom_1"
}

// RoomAccessSource discriminates how an access row came to exist.
type RoomAccessSource string

const (
	// AccessSourceLedger — granted by a paid lodging ledger row
	// (deliver_order's nights_stay branch). Carries LedgerID and
	// ExpiresAt.
	AccessSourceLedger RoomAccessSource = "ledger"
	// AccessSourceStaff — staff access, implicit by work_structure_id.
	// Typically not stored as an explicit RoomAccess row — the
	// canEnterRoom staff branch reads Actor.WorkStructureID directly.
	AccessSourceStaff RoomAccessSource = "staff"
)

// RoomAccessKey is the per-actor composite key. ActorID is implicit
// (it's the parent of the RoomAccess map).
type RoomAccessKey struct {
	RoomID RoomID
	Source RoomAccessSource
}

// RoomAccess is one (actor, room, source) grant. Stored as a child of
// Actor — keyed by RoomAccessKey on Actor.RoomAccess.
type RoomAccess struct {
	RoomID    RoomID
	Source    RoomAccessSource
	LedgerID  int64      // 0 when Source != AccessSourceLedger
	ExpiresAt *time.Time // nil = never expires (staff)
	Active    bool       // flipped to false by ExpireRoomAccess sweep
	CreatedAt time.Time
}

// commonRoomForStructure returns the ID of the "common" room in
// structureID, or 0 if the structure has no common room declared.
// Matches the legacy commonRoomForStructure soft-failure semantics
// (callers leave InsideRoomID at zero when no common room exists).
//
// MUST be called from inside a Command.Fn (reads w.Structures).
// Unexported by design — see buildWalkGrid for the rationale.
func commonRoomForStructure(w *World, structureID StructureID) RoomID {
	s, ok := w.Structures[structureID]
	if !ok {
		return 0
	}
	for _, r := range s.Rooms {
		if r.Kind == RoomKindCommon {
			return r.ID
		}
	}
	return 0
}

// canEnterRoom reports whether actor may enter room. Matches the legacy
// gate exactly:
//
//   - common: always allow
//   - private: require an unexpired Active RoomAccess row on actor
//   - staff: require actor.WorkStructureID == room.StructureID
//   - unknown: fail closed
//
// MUST be called from inside a Command.Fn. Unexported by design.
// `now` lets callers control the expiry-clock for deterministic tests.
func canEnterRoom(w *World, actor *Actor, roomID RoomID, now time.Time) bool {
	if actor == nil || roomID == 0 {
		return false
	}
	room := findRoom(w, roomID)
	if room == nil {
		return false
	}
	switch room.Kind {
	case RoomKindCommon:
		return true
	case RoomKindPrivate:
		key := RoomAccessKey{RoomID: roomID, Source: AccessSourceLedger}
		ra, ok := actor.RoomAccess[key]
		// Defensive nil guard: a map can carry a key with a nil value
		// (e.g. a half-built test fixture). This is an auth gate — fail
		// closed rather than risking a nil-deref panic.
		if !ok || ra == nil || !ra.Active {
			return false
		}
		// Gate against expiry directly here so a request landing between
		// ExpireRoomAccess sweeps doesn't get through on a stale Active=true
		// row. Staff has nil ExpiresAt (never expires).
		if ra.ExpiresAt != nil && !ra.ExpiresAt.After(now) {
			return false
		}
		return true
	case RoomKindStaff:
		return actor.WorkStructureID == room.StructureID
	default:
		return false
	}
}

// IsActiveLedgerGrant reports whether ra is an active, unexpired *ledger*
// grant at now — i.e. a paid lodging grant still in effect. This is the
// single definition of "is a current lodging grant", shared by the live-world
// consumers (the engine-auto rebook sweep and lodger-sleep target resolution,
// landing in later slices) and the snapshot-pure perception lodging views,
// which scan ActorSnapshot.RoomAccess. A lodging grant always carries a future
// ExpiresAt by construction (AssignBedroomForLodger), so a ledger row with
// nil/past ExpiresAt does not count — fail closed. Staff grants (Source !=
// ledger) never count. Exported so the perception package (a different
// package) can key its lodger/keeper views off the same predicate.
func IsActiveLedgerGrant(ra *RoomAccess, now time.Time) bool {
	if ra == nil || !ra.Active || ra.Source != AccessSourceLedger {
		return false
	}
	return ra.ExpiresAt != nil && ra.ExpiresAt.After(now)
}

// actorIsLodgerAt reports whether actor holds an active, unexpired ledger
// RoomAccess for a room inside structureID at now — i.e. is a paying lodger of
// that structure. The canonical "does this actor lodge here" predicate, shared
// by the lodger leg of structureMembershipAllows (may-enter-as-lodger) and the
// lodger branch of the NPC auto-sleep machine (may-bed-down-as-lodger, see
// npc_sleep.go) so the two can never diverge. Per-grant gate is
// IsActiveLedgerGrant (ledger source, Active, future ExpiresAt); each grant's
// room resolves to its structure via findRoom. Staff grants never qualify —
// staff presence is a WorkStructureID concern, not lodging.
//
// MUST be called from inside a Command.Fn (reads w.Structures via findRoom).
func actorIsLodgerAt(w *World, actor *Actor, structureID StructureID, now time.Time) bool {
	if actor == nil || structureID == "" {
		return false
	}
	for key, ra := range actor.RoomAccess {
		if !IsActiveLedgerGrant(ra, now) {
			continue
		}
		if room := findRoom(w, key.RoomID); room != nil && room.StructureID == structureID {
			return true
		}
	}
	return false
}

// actorHoldsActiveLodging reports whether actor holds ANY active, unexpired
// ledger RoomAccess at now — the structure-agnostic counterpart to
// actorIsLodgerAt. The homeless rest-fallback floor (npc_rest_fallback.go)
// uses it to exclude a paying lodger, who beds at their rented inn via the
// lodger-sleep arm rather than being routed to a free outdoor rest object.
// Per-grant gate is the same IsActiveLedgerGrant predicate; no structure
// match is needed because the floor only asks "is this actor lodging
// anywhere", not "lodging here".
//
// Pure over actor.RoomAccess (no world reads) — safe to call from inside a
// Command.Fn or any context that already holds an actor reference.
func actorHoldsActiveLodging(actor *Actor, now time.Time) bool {
	if actor == nil {
		return false
	}
	for _, ra := range actor.RoomAccess {
		if IsActiveLedgerGrant(ra, now) {
			return true
		}
	}
	return false
}

// findRoom walks all structures looking for a room with the given ID.
// Linear in total room count — fine for Salem's scale (~20 structures,
// ~80 rooms). When room counts grow, a World.Rooms secondary index
// would help; deferred until profiling shows it.
func findRoom(w *World, id RoomID) *Room {
	for _, s := range w.Structures {
		for _, r := range s.Rooms {
			if r.ID == id {
				return r
			}
		}
	}
	return nil
}

// ComputeLodgerUntil returns the wall-clock expires_at instant for a
// nights_stay: ready_by + qty days, at check-out hour, in the world
// timezone. Pure helper, matches legacy computeLodgerUntil.
func ComputeLodgerUntil(readyBy time.Time, qty int, checkOutHour int, loc *time.Location) time.Time {
	if qty < 1 {
		qty = 1
	}
	if loc == nil {
		loc = time.UTC
	}
	d := readyBy.AddDate(0, 0, qty)
	return time.Date(d.Year(), d.Month(), d.Day(), checkOutHour, 0, 0, 0, loc)
}

// LodgingNightlyRate derives the per-night rent from the operator-set weekly
// rate: weeklyRate / 7. Operators keep the rate divisible by 7 so this floors
// cleanly (a weekly rate of 29 charges 4/night = 28/week, losing 1 to integer
// truncation). Returns 0 when weeklyRate < 7 — integer coins can't bill less
// than 1/night, so a sub-7 weekly rate is treated as "lodging rate disabled"
// (the rate surfaces and the auto-rebook both go silent), matching v1. The
// single derivation point shared by perception (off the snapshot) and the
// engine-auto rebook sweep (off live WorldSettings).
func LodgingNightlyRate(weeklyRate int) int {
	if weeklyRate < 7 {
		return 0
	}
	return weeklyRate / 7
}

// ComputeEarliestCheckIn returns the earliest wall-clock instant a
// nights_stay can be checked in: ready_by at check-in hour, in the
// world timezone. Pure helper, matches legacy computeEarliestCheckIn.
func ComputeEarliestCheckIn(readyBy time.Time, checkInHour int, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(readyBy.Year(), readyBy.Month(), readyBy.Day(), checkInHour, 0, 0, 0, loc)
}
