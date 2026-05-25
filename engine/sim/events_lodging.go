package sim

import "time"

// events_lodging.go — PCRelocatedToCommon: a PC was moved out of a private room
// to its structure's common room by the lodging day-cycle. Two emitters:
// checkout eviction (EvictExpiredOccupants, Reason LodgingReasonCheckout) and,
// from ZBBS-HOME-312 Part 2, natural morning descent (Reason LodgingReasonMorning).
//
// The httpapi hub translates this to a private `room_event` narration frame the
// Godot client already renders in the talk panel (event_client.gd ->
// world.apply_room_event -> talk_panel _on_room_event). The relocation mutation
// (InsideRoomID -> the common room) is performed by the emitter BEFORE emit;
// this event only surfaces it to the client, since v2 otherwise leaves the
// client's room scope stale until the next /pc/me poll. PC-only — the only
// relocate-to-common paths are PC lodging flows.
//
// Reason is one of LodgingReason* and doubles as the client room_event `kind`.
// Text is the pre-picked pooled narration line (pickLodgingNarration) and should
// be non-empty: emitters skip emit when the pool yields "", and the translator
// also drops an empty-Text frame, so a blank narration never reaches the client.
type PCRelocatedToCommon struct {
	EventBase
	ActorID     ActorID
	StructureID StructureID
	Reason      string
	Text        string
	At          time.Time
}

func (PCRelocatedToCommon) isSimEvent() {}
