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
// the client renders), with a pooled morning-descent line.

// handleLodgingMorningDescent relocates a naturally-woken lodger PC to its
// structure's common room. Gated on:
//
//   - PCSleepEnded with Reason "auto" — the sleep sweep woke them (rested or the
//     safety cap). "manual"/"input" wakes are the player taking control, so we
//     leave them where they are (no agency-stealing auto-move).
//   - The PC is in a private room with an ACTIVE ledger grant whose expiry is in
//     the FUTURE (IsActiveLedgerGrant). A lapsed grant skips descent and is left
//     for EvictExpiredOccupants. And if descent DOES move the PC, it leaves the
//     private room — so a later eviction sweep, which only matches private-room
//     occupancy, won't relocate the same PC again. The private-room guard and the
//     active-grant predicate are BOTH load-bearing for that no-double-relocate
//     property; the predicate alone is not the whole story.
//
// Runs synchronously inside the world goroutine (event dispatch). It mutates
// InsideRoomID and emits PCRelocatedToCommon re-entrantly, both of which the
// event bus supports (World.emit inherits the waking event's cascade root).
func handleLodgingMorningDescent(w *World, evt Event) {
	e, ok := evt.(*PCSleepEnded)
	if !ok || e.Reason != "auto" {
		return
	}
	actor := w.Actors[e.ActorID]
	if actor == nil || actor.Kind != KindPC || actor.InsideRoomID == 0 {
		return
	}
	room := findRoom(w, actor.InsideRoomID)
	if room == nil || room.Kind != RoomKindPrivate {
		return
	}
	key := RoomAccessKey{RoomID: actor.InsideRoomID, Source: AccessSourceLedger}
	if !IsActiveLedgerGrant(actor.RoomAccess[key], e.At) {
		return // lapsed grant => checkout, not morning descent — eviction owns it
	}
	common := commonRoomForStructure(w, room.StructureID)
	if common == 0 || common == actor.InsideRoomID {
		return
	}
	text := pickLodgingNarration(LodgingReasonMorning)
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
