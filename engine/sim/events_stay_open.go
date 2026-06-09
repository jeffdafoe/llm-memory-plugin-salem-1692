package sim

import "time"

// events_stay_open.go — StayingOpen: emitted when a stay_open tool call commits
// (sim.StayOpen). The wind-down-side twin of TookBreak (events_take_break.go):
// embed EventBase for identity, carry the actor + the committed facts, implement
// the unexported isSimEvent marker so the type joins the closed Event set.
//
// Consumers today:
//   - cascade/action_log.go's handleStayedOpenActionLog appends an
//     ActionTypeStayedOpen row (Text = Reason), so a stay-open commitment shows
//     up in the in-memory audit trail (and its durable mirror) like every other
//     committing tool action.

// StayingOpen is emitted by sim.StayOpen after the actor's OpenUntil window is
// stamped. At is the commit instant (UTC); OpenUntil is the resolved hour the
// keeper has committed to staying open until (world-timezone-anchored instant);
// Reason is the model-supplied, already-validated reason fragment.
type StayingOpen struct {
	EventBase
	ActorID   ActorID
	Reason    string
	OpenUntil time.Time
	At        time.Time
}

func (StayingOpen) isSimEvent() {}
