package main

// Subspace-aware scope helper for npc_spoke broadcasts.
//
// ZBBS-149 introduced first-class rooms within a structure (`structure_room`,
// `actor.inside_room_id`, `room_kind` ENUM common/private/staff). The
// speech-broadcast pipeline kept its pre-subspace shape: every npc_spoke
// event carries `structure_id` only, and the talk panel filters by structure.
// Symptom: a PC in a private bedroom hears tavern-common-room speech
// because both share the same `loaded_structure_id`.
//
// actorPrivateRoomScope is the audibility model's "non-public" tag. It
// returns the actor's `inside_room_id` only when the actor is in a
// non-common indoor room (private/staff). Common-room and outdoor speakers
// return "" — those are public scope, audible to common-room and outdoor
// listeners alike. The talk-panel filter pairs with this:
//
//   - listener room_id == event room_id  → audible
//   - listener "" + event ""             → audible (public ↔ public)
//   - listener "X" + event ""            → drop (private listener doesn't hear common chatter)
//   - listener "" + event "X"            → drop (common listener doesn't hear private speech)
//
// One DB lookup per emission. Each speak is rare compared to ticks —
// acceptable cost. If the volume ever justifies it, plumb `inside_room_id`
// through the existing actor row already loaded by callers.

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/jackc/pgx/v5"
)

// addRoomScopeToData adds the actor's private-room id to the broadcast
// data map when applicable. Convenience wrapper over actorPrivateRoomScope
// for the many room_event / npc_spoke broadcast sites that follow the
// "build map → set structure_id → broadcast" pattern. No-op when the
// actor is in a common room or outdoors.
func (app *App) addRoomScopeToData(ctx context.Context, data map[string]interface{}, actorID string) {
	if rs := app.actorPrivateRoomScope(ctx, actorID); rs != "" {
		data["room_id"] = rs
	}
}

// actorPrivateRoomScope returns the actor's inside_room_id IF the actor
// is in a non-common indoor room (private/staff) AND that room belongs
// to the actor's current inside_structure_id. Returns "" in every other
// case — common rooms, outdoors, missing actor, stale inside_room_id
// pointing at a deleted or wrong-structure room, or unknown room kind.
//
// The structure-bound JOIN is intentional: `actor.inside_room_id` can
// briefly fall out of sync with `inside_structure_id` during room
// transitions, and stamping a wrong-structure room id onto a broadcast
// would mismatch the talk-panel's (structure_id, room_id) filter in
// confusing ways. Failing closed to public scope is the safe behavior —
// over-scoped speech (heard slightly more broadly than ideal) is much
// better than mis-targeted speech that vanishes for the right audience.
//
// pgx.ErrNoRows (actor missing) returns "" without logging — that's an
// expected race (deleted actor, just-renamed PC). Other errors log and
// also return "".
func (app *App) actorPrivateRoomScope(ctx context.Context, actorID string) string {
	if actorID == "" {
		return ""
	}
	var roomID sql.NullString
	var kind sql.NullString
	err := app.DB.QueryRow(ctx, `
		SELECT sr.id::text, sr.kind::text
		  FROM actor a
		  LEFT JOIN structure_room sr
		    ON sr.id = a.inside_room_id
		   AND sr.structure_id = a.inside_structure_id
		 WHERE a.id = $1::uuid
	`, actorID).Scan(&roomID, &kind)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ""
		}
		log.Printf("actorPrivateRoomScope %s: %v", actorID, err)
		return ""
	}
	if !roomID.Valid || !kind.Valid {
		return ""
	}
	// Whitelist private/staff explicitly. An unrecognized room_kind value
	// (future ENUM addition, or corrupt data) returns "" rather than
	// silently inheriting "private" semantics — better to under-scope
	// than to invent a private bucket nobody can match.
	switch kind.String {
	case "private", "staff":
		return roomID.String
	default:
		return ""
	}
}
