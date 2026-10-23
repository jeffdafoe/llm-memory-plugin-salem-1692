package sim

import "time"

// pc_trace.go — LLM-847. A durable trace of a player's character going to bed,
// waking, and changing room inside a building, so "what happened to my
// character overnight" can be answered from agent_action_log. Before this, a
// PC's sleep and room moves left no row and no log line; only the nightly dump
// showed that a player had slept at all.
//
// Every row is DURABLE-ONLY (AppendActionLogDurable, never the in-memory ring),
// source "engine", with no huddle id. No reader of the table picks such a row
// up: the day push reads an NPC's own rows and overheard speech, the talk-panel
// transcript and dealings read by huddle, the court by case, the coin record by
// paid/labored. So villagers, the court, the dream notes and the player's own
// talk panel never see them.
//
// The wire PCSleepEnded.Reason stays "auto" for every sweep wake (the morning
// descent and the client key on it); the trace alone splits it into rested /
// cap / checkout / moved_out.

// How a player was put to bed.
const (
	pcSleptIdle    = "idle"
	pcSleptOffline = "offline"
	pcSleptBedTap  = "bed_tap"
)

// Why a player woke.
const (
	pcWokeRested    = "rested"
	pcWokeCap       = "cap"
	pcWokeCheckout  = "checkout"
	pcWokeMovedOut  = "moved_out"
	pcWokeReconnect = "reconnect"
	pcWokeManual    = "manual"
	pcWokeInput     = "input"
)

// What changed a player's room.
const (
	pcRoomGoRoom          = "go_room"
	pcRoomBedDown         = "bed_down"
	pcRoomWake            = "wake"
	pcRoomMorningDescent  = "morning_descent"
	pcRoomCheckout        = "checkout"
	pcRoomStructureChange = "structure_change"
	pcRoomUnattributed    = "unattributed"
)

// pcTraceRoomKey folds the common room to 0: an actor in the common room is
// sometimes at InsideRoomID 0 and sometimes at the common room's own id (the
// morning descent and the checkout set the id, a walk downstairs sets 0), and
// the two must not read as a move.
func pcTraceRoomKey(w *World, id RoomID) RoomID {
	if id == 0 {
		return 0
	}
	if rm := findRoom(w, id); rm != nil && rm.Kind == RoomKindCommon {
		return 0
	}
	return id
}

// pcTraceRoomName names a room for the trace: its own name, "common" for the
// common room, "outdoors" for no room when the actor is outdoors.
func pcTraceRoomName(w *World, a *Actor, id RoomID) string {
	if id != 0 {
		if rm := findRoom(w, id); rm != nil {
			if rm.Kind == RoomKindCommon {
				return "common"
			}
			return rm.Name
		}
		return "unknown"
	}
	if a.InsideStructureID == "" {
		return "outdoors"
	}
	return "common"
}

func pcTraceStructureName(w *World, id StructureID) string {
	if st := w.Structures[id]; st != nil {
		return st.DisplayName
	}
	return ""
}

func pcTraceSpeaker(a *Actor) string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return string(a.ID)
}

func appendPCTrace(w *World, a *Actor, t ActionType, payload map[string]any, now time.Time) {
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:     a.ID,
		OccurredAt:  now,
		ActionType:  t,
		Payload:     payload,
		SpeakerName: pcTraceSpeaker(a),
		Source:      "engine",
	})
}

// tracePCSlept records a bed-down. Call after executePCSleep returned true.
func tracePCSlept(w *World, pc *Actor, how string, now time.Time) {
	if pc == nil || pc.Kind != KindPC {
		return
	}
	payload := map[string]any{
		"structure_id": string(pc.InsideStructureID),
		"structure":    pcTraceStructureName(w, pc.InsideStructureID),
		"room_id":      int64(pc.InsideRoomID),
		"room":         pcTraceRoomName(w, pc, pc.InsideRoomID),
		"how":          how,
		"tiredness":    pc.Needs["tiredness"],
	}
	if pc.SleepingUntil != nil {
		payload["wake_cap_at"] = pc.SleepingUntil.UTC().Format(time.RFC3339)
	}
	appendPCTrace(w, pc, ActionTypePCSlept, payload, now)
}

// tracePCWoke records a wake, after the wake has settled the room. fromRoom is
// the room slept in; presenceStale is whether the player was away at the wake.
func tracePCWoke(w *World, pc *Actor, reason string, fromRoom RoomID, presenceStale bool, now time.Time) {
	if pc == nil || pc.Kind != KindPC {
		return
	}
	appendPCTrace(w, pc, ActionTypePCWoke, map[string]any{
		"reason":         reason,
		"structure_id":   string(pc.InsideStructureID),
		"from_room_id":   int64(fromRoom),
		"from_room":      pcTraceRoomName(w, pc, fromRoom),
		"to_room_id":     int64(pc.InsideRoomID),
		"to_room":        pcTraceRoomName(w, pc, pc.InsideRoomID),
		"tiredness":      pc.Needs["tiredness"],
		"presence_stale": presenceStale,
	}, now)
}

// tracePCRoomChange records that a player's room went from `from` to their
// current InsideRoomID for `cause`, and updates what the safety net last saw.
// No row when the room did not really change (see pcTraceRoomKey). Call right
// after the write to InsideRoomID.
//
// When the room the trace last recorded is not `from`, something moved the
// player in between without a word: that gap is written first, as
// "unattributed" — here and not only in the per-minute sweep, or a recorded
// move landing before the sweep would swallow it (code_review).
func tracePCRoomChange(w *World, a *Actor, from RoomID, cause string, now time.Time) {
	if a == nil || a.Kind != KindPC {
		return
	}
	fromKey := pcTraceRoomKey(w, from)
	toKey := pcTraceRoomKey(w, a.InsideRoomID)
	if a.TracedRoomSet && a.TracedRoomID != fromKey {
		writePCRoomChanged(w, a, a.TracedRoomID, from, pcRoomUnattributed, now)
	}
	a.TracedRoomID, a.TracedRoomSet = toKey, true
	if fromKey == toKey {
		return
	}
	writePCRoomChanged(w, a, from, a.InsideRoomID, cause, now)
}

func writePCRoomChanged(w *World, a *Actor, from, to RoomID, cause string, now time.Time) {
	structureID := a.InsideStructureID
	if structureID == "" && from != 0 {
		if rm := findRoom(w, from); rm != nil {
			structureID = rm.StructureID // left the building from a room upstairs
		}
	}
	appendPCTrace(w, a, ActionTypePCRoomChanged, map[string]any{
		"structure_id": string(structureID),
		"structure":    pcTraceStructureName(w, structureID),
		"from_room_id": int64(from),
		"from_room":    pcTraceRoomName(w, a, from),
		"to_room_id":   int64(to),
		"to_room":      pcTraceRoomName(w, a, to),
		"cause":        cause,
	}, now)
}

// TracePCRooms is the safety net, run every minute from the sleep ticker: a
// player whose room differs from the one the trace last recorded was moved by a
// path that records nothing, and gets a pc_room_changed row with cause
// "unattributed" — the row that names the gap. The first pass after a boot
// only records the restored room.
func TracePCRooms(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			caught := 0
			for _, a := range w.Actors {
				if a == nil || a.Kind != KindPC {
					continue
				}
				key := pcTraceRoomKey(w, a.InsideRoomID)
				if !a.TracedRoomSet {
					a.TracedRoomID, a.TracedRoomSet = key, true
					continue
				}
				if key == a.TracedRoomID {
					continue
				}
				from := a.TracedRoomID
				a.TracedRoomID = key
				writePCRoomChanged(w, a, from, a.InsideRoomID, pcRoomUnattributed, now)
				caught++
			}
			return caught, nil
		},
	}
}
