package sim

import "time"

// events_labor.go — LLM-26 event family for the worker-initiated
// service-for-pay (solicit_work) flow. Three event types covering the
// offer lifecycle, mirroring the pay-with-item family's split:
//
//   - LaborOfferReceived fires when sim.SolicitWork inserts a new pending
//     LaborOffer into World.LaborLedger. A later subscriber stamps the
//     employer's warrant so their next reactor tick perceives the offer
//     and can accept_work / decline_work.
//
//   - LaborOfferAccepted fires when sim.AcceptWork flips the offer to
//     Working (no coins move). NON-terminal — the commerce isn't ended,
//     the worker is now laboring until WorkingUntil and the reward transfers
//     when the completion sweep fires. Carried as its own event (rather
//     than folded into a "resolved" signal) because a subscriber that
//     warns "you're committed until T" needs the WorkingUntil deadline,
//     and the worker/employer warrant stamps differ from the terminal one.
//
//   - LaborResolved fires on every TERMINAL transition — completed,
//     declined, expired, failed_unavailable. Single canonical "the labor
//     ENDED" signal (same posture as PayWithItemResolved). The Accepted
//     transition is excluded because the labor isn't ended there — it's
//     in flight.
//
// All three embed full term snapshots (Reward, DurationMin) rather than
// asking subscribers to chase the offer back out of World.LaborLedger,
// keeping event handlers snapshot-clean — the same rationale as the
// pay-with-item events.

// LaborOfferReceived fires when sim.SolicitWork inserts a new pending
// LaborOffer. WorkerID solicited; EmployerID is the peer who must accept
// or decline. Reward + DurationMin snapshot the proposed terms. SceneID +
// HuddleID anchor the co-presence context captured at solicitation;
// accept-time revalidation re-checks both parties are still in HuddleID.
// ExpiresAt is the pending-TTL boundary the aging sweep enforces off the
// offer.
type LaborOfferReceived struct {
	EventBase

	LaborID     LaborID
	WorkerID    ActorID
	EmployerID  ActorID
	Reward      int
	DurationMin int

	SceneID   SceneID
	HuddleID  HuddleID
	ExpiresAt time.Time
	At        time.Time
}

func (LaborOfferReceived) isSimEvent() {}

// LaborOfferAccepted fires when sim.AcceptWork accepts a pending offer: the
// worker entered StateLaboring until WorkingUntil. No coins moved — the
// reward transfers from employer to worker at completion. Distinct from
// LaborResolved because the labor isn't ENDED — the completion sweep does
// the transfer and emits LaborResolved{Completed} when WorkingUntil elapses.
type LaborOfferAccepted struct {
	EventBase

	LaborID      LaborID
	WorkerID     ActorID
	EmployerID   ActorID
	Reward       int
	DurationMin  int
	WorkingUntil time.Time

	SceneID  SceneID
	HuddleID HuddleID
	At       time.Time
}

func (LaborOfferAccepted) isSimEvent() {}

// LaborResolved fires on every terminal LaborOffer transition. Single
// source of truth for "this labor ended" — covers
// LaborTerminalStateCompleted, Declined, Expired, and FailedUnavailable.
//
// TerminalState is typed LaborTerminalState for compile-time enforcement
// that the event never carries an active state (pending / working).
//
// Reward is carried on every terminal so a Completed event records what
// the worker was paid and a Declined/Expired/failed event records what
// was forgone, without a ledger lookup. DurationMin likewise snapshots
// the agreed work length.
type LaborResolved struct {
	EventBase

	LaborID     LaborID
	WorkerID    ActorID
	EmployerID  ActorID
	Reward      int
	DurationMin int

	TerminalState LaborTerminalState

	// WorkPerformed is true when the work window actually elapsed before this
	// terminal — the offer reached Completed, or reached FailedUnavailable from
	// the completion sweep (the worker finished the job but the employer could no
	// longer cover the reward). It is false for every terminal reached WITHOUT
	// the work happening: Declined, Expired, and the accept-time
	// FailedUnavailable fall-throughs (co-presence lost / worker busy / employer
	// visibly broke at accept). It exists because FailedUnavailable is overloaded
	// across both the accept-time deal that never started and the completion-time
	// job that ran unpaid — and a consumer keying off TerminalState alone can't
	// tell the aggrieved "worked and stiffed" case (a real relationship beat)
	// from the never-started one (a non-event). LLM-165.
	WorkPerformed bool

	SceneID  SceneID
	HuddleID HuddleID
	At       time.Time
}

func (LaborResolved) isSimEvent() {}
