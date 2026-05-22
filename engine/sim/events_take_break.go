package sim

import "time"

// events_take_break.go — TookBreak: emitted when a take_break tool call
// commits (sim.TakeBreak). Mirrors the events_consume.go shape: embed
// EventBase for identity, carry the actor + the committed facts, implement the
// unexported isSimEvent marker so the type joins the closed Event set.
//
// Consumers today:
//   - cascade/action_log.go's handleTookBreakActionLog appends an
//     ActionTypeTookBreak row (Text = Reason), so a break shows up in the
//     in-memory audit trail like every other committing tool action.
//
// Future consumers (deferred, same bucket as #2's npc_sleep_started/ended WS
// frames): a client-facing broadcast that surfaces the spoken excuse / "shop
// closed" beat. v1 spoke an excuse on take_break; v2's broadcast layer for
// rest-state isn't wired yet, so the Reason rides on the event for whoever
// surfaces it next.

// TookBreak is emitted by sim.TakeBreak after the actor's BreakUntil window is
// stamped. At is the commit instant (UTC); BreakUntil is the resolved end of
// the break window (world-timezone-anchored instant); Reason is the
// model-supplied, already-validated reason fragment.
type TookBreak struct {
	EventBase
	ActorID    ActorID
	Reason     string
	BreakUntil time.Time
	At         time.Time
}

func (TookBreak) isSimEvent() {}
