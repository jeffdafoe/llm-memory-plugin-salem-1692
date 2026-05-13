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

// CommonRoomForStructure returns the ID of the "common" room in
// structureID, or 0 if the structure has no common room declared.
// Matches the legacy commonRoomForStructure soft-failure semantics
// (callers leave InsideRoomID at zero when no common room exists).
//
// MUST be called from inside a Command.Fn (reads w.Structures).
func CommonRoomForStructure(w *World, structureID StructureID) RoomID {
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

// CanEnterRoom reports whether actor may enter room. Matches the legacy
// gate exactly:
//
//   - common: always allow
//   - private: require an unexpired Active RoomAccess row on actor
//   - staff: require actor.WorkStructureID == room.StructureID
//   - unknown: fail closed
//
// MUST be called from inside a Command.Fn.
func CanEnterRoom(w *World, actor *Actor, roomID RoomID) bool {
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
		if !ok {
			return false
		}
		return ra.Active
	case RoomKindStaff:
		return actor.WorkStructureID == room.StructureID
	default:
		return false
	}
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

// ComputeEarliestCheckIn returns the earliest wall-clock instant a
// nights_stay can be checked in: ready_by at check-in hour, in the
// world timezone. Pure helper, matches legacy computeEarliestCheckIn.
func ComputeEarliestCheckIn(readyBy time.Time, checkInHour int, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(readyBy.Year(), readyBy.Month(), readyBy.Day(), checkInHour, 0, 0, 0, loc)
}
