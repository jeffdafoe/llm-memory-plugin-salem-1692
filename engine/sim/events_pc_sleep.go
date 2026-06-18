package sim

import "time"

// events_pc_sleep.go — PCSleepStarted / PCSleepEnded: the player-facing sleep
// transitions of a human-controlled PC. These are the v2 source events the
// httpapi hub translates into the pc_sleep_started / pc_sleep_ended WS frames
// the Godot client already handles (event_client.gd → sleep_fade.gd overlay +
// top_bar.gd "Sleeping — wake HH:MM" chip). NPC sleep (npc_sleep.go) stays
// engine-internal with no client surface, so these events are PC-only — mirror
// of v1 engine/sleep.go's pc_sleep_started / pc_sleep_ended broadcasts.

// PCSleepStarted is emitted on a fresh PC bed-down (idle auto-bed or the
// explicit /pc/sleep route) after SleepingUntil is stamped. An already-sleeping
// PC is a no-op that emits nothing, so every PCSleepStarted is a real
// transition. WakeAt is the safety-cap instant the client renders as the
// "wake by" time (the PC usually wakes earlier, when fully rested); At is the
// bed-down instant. Both UTC.
type PCSleepStarted struct {
	EventBase
	ActorID ActorID
	WakeAt  time.Time
	At      time.Time
}

func (PCSleepStarted) isSimEvent() {}

// PCSleepEnded is emitted when a PC's sleep actually clears (the PC was
// sleeping). Reason carries which path woke them, matching v1's three reasons:
//
//   - "manual" — the player hit Wake (the /pc/wake route, sim.WakePC).
//   - "auto"   — the sleep sweep woke them: fully rested (tiredness <= 0, the
//     normal case) or the safety cap fired (WakeExpiredPCSleepers).
//   - "input"  — the player took an action while asleep (touchPCInput), so the
//     action both wakes them and proceeds.
//
// At is the wake instant (UTC). FromRoomID is the room the PC was sleeping in
// (its bed-down InsideRoomID), captured BEFORE the wake clears it: the
// morning-descent subscriber relocates the PC from this room to the common room
// off the event rather than live actor state, because the wake clears
// InsideRoomID immediately (LLM-14: an awake PC must never be bedroom-scoped). 0
// when the PC held no room scope.
type PCSleepEnded struct {
	EventBase
	ActorID    ActorID
	Reason     string
	FromRoomID RoomID
	At         time.Time
}

func (PCSleepEnded) isSimEvent() {}
