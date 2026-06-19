package sim

// lodging_descent.go — ZBBS-HOME-312 Part 2: morning descent. A PC that wakes
// naturally (rested or safety-capped) while still holding a valid lodging grant
// is walked down from its private bedroom to the structure's common room — "no
// one stays in their room in the morning." v1 did this in
// maybeWalkWokenLodgerToCommon (engine/sleep.go); v2 reaches it as a
// PCSleepEnded subscriber so work's PC-sleep tick owns no lodging knowledge
// (the ZBBS-HOME-312 #2 seam: Option C, a subscriber, not a hook in pc_sleep.go).
//
// It reuses Part 1's relocate-and-narrate substrate: set InsideRoomID to the
// common room, then emit PCRelocatedToCommon (translated to a private room_event
// the client renders), with a pooled morning-descent line. The room the PC slept
// in comes from the wake event's FromRoomID, not live actor state — the wake
// clears InsideRoomID immediately to keep the LLM-14 invariant (a private
// InsideRoomID means "asleep in it"), so by the time this subscriber runs the
// live field is already 0.

// handleLodgingMorningDescent relocates a naturally-woken lodger PC to its
// structure's common room. Gated on:
//
//   - PCSleepEnded with Reason "auto" — the sleep sweep woke them (rested or the
//     safety cap). "manual"/"input" wakes are the player taking control, so we
//     leave them where they are (no agency-stealing auto-move).
//   - FromRoomID (the room they slept in, carried on the event) is a private room
//     with an ACTIVE ledger grant whose expiry is in the FUTURE
//     (IsActiveLedgerGrant). A lapsed grant skips descent and is left for
//     EvictExpiredOccupants — that discriminator is what guarantees descent and
//     eviction never both relocate the same PC.
//
// Reads FromRoomID rather than live InsideRoomID because the wake clears
// InsideRoomID immediately (LLM-14). Runs synchronously inside the world
// goroutine (event dispatch); mutates InsideRoomID and emits PCRelocatedToCommon
// re-entrantly, both of which the event bus supports (World.emit inherits the
// waking event's cascade root).
func handleLodgingMorningDescent(w *World, evt Event) {
	e, ok := evt.(*PCSleepEnded)
	if !ok || e.Reason != "auto" || e.FromRoomID == 0 {
		return
	}
	actor := w.Actors[e.ActorID]
	if actor == nil || actor.Kind != KindPC {
		return
	}
	room := findRoom(w, e.FromRoomID)
	if room == nil || room.Kind != RoomKindPrivate {
		return
	}
	key := RoomAccessKey{RoomID: e.FromRoomID, Source: AccessSourceLedger}
	if !IsActiveLedgerGrant(actor.RoomAccess[key], e.At) {
		return // lapsed grant => checkout, not morning descent — eviction owns it
	}
	common := commonRoomForStructure(w, room.StructureID)
	if common == 0 || common == actor.InsideRoomID {
		return // no common room, or descent already ran (idempotent vs a dup subscriber)
	}
	text := w.pickLodgingNarration(LodgingReasonMorning)
	if text == "" {
		return
	}
	actor.InsideRoomID = common
	w.emit(&PCRelocatedToCommon{
		ActorID:     e.ActorID,
		StructureID: room.StructureID,
		Reason:      LodgingReasonMorning,
		Text:        text,
		At:          e.At,
	})
}

// RegisterLodgingMorningDescentSubscriber wires the morning-descent subscriber
// into the world. Must run on the world goroutine (call before World.Run or
// from inside a Command.Fn). Idempotent in effect: a duplicate registration
// would relocate a still-private woken lodger twice, but the second pass finds
// the PC already in the common room (room.Kind != private) and no-ops.
func RegisterLodgingMorningDescentSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterLodgingMorningDescentSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleLodgingMorningDescent))
}
