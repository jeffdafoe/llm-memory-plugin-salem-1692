package sim

import (
	"math"
	"time"
)

// labor_ledger.go — LLM-26 substrate for the work / service-for-pay
// primitive. Carries the LaborLedger aggregate (the worker-initiated
// labor-offer state machine), the per-entry state enum, the LaborID type,
// the tunable constants, and the deep-clone helper.
//
// Mirrors the pay_ledger.go patterns (an LLM-visible integer id, a flat
// World-level map, a periodic aging sweep + terminal reaper) but is a
// SEPARATE machine, deliberately not an extension of PayLedgerEntry:
// pay_with_item settles at accept (immediate, terminal, atomic transfer);
// a labor offer's accept is NON-terminal ("now working") and the reward
// settles later, when the work window elapses. That held-until-completion
// shape is the whole reason labor is parallel to PayLedger rather than a
// new branch inside it.
//
// Super-basic MVP scope: the worker submits {employer, reward, duration};
// the employer accepts or declines — no counter, no haggle (different
// terms are re-negotiated in conversation and the worker re-submits).
// Coins only, no goods. Soliciting is gated to actors carrying the
// `worker` attribute.
//
// Persistence: the LaborLedger lives only in the in-memory World — it has
// NO repo and is intentionally restart-lossy, the same call PayLedger made
// (save_world.go, decided 2026-05-20). This is clean here because no coins
// are ever held: solicit and accept move nothing, and the employer→worker
// reward transfer settles atomically at completion (settle-at-completion,
// labor_settle.go). So losing any offer on restart — pending OR working —
// just means a deal that hadn't paid out yet didn't happen, with no torn
// coin state to recover. The actor's LaboringUntil mirror is transient for
// the same reason: persisting it without the ledger would orphan the worker
// in StateLaboring forever.
//
// Consequence for the LLM-187 employer-wake warrant: there is deliberately NO
// labor analog of pay_ledger.go's restartReStampLaborOfferWarrants. The pay
// re-stamp is itself dormant-by-design (PayLedger is likewise restart-lossy),
// and labor's restart-lossy ledger means LoadWorld holds zero pending offers
// to re-advertise — so a re-stamp pass would never have a row to walk. The
// restart-stable dedup the migration enabled is still honored: the
// LaborOfferWarrantReason.DedupDiscriminator is uint64(LaborID), so the live
// LaborOfferReceived stamp self-dedupes against a double registration.

// LaborID identifies a LaborOffer within a single world run. uint64,
// LLM-visible — the employer reads the id off perception text and emits it
// back in accept_work(labor_id=N) / decline_work. Same readback rationale
// as LedgerID / QuoteID (UUID-style strings are unreliable for LLM
// readback). LaborID(0) is the reserved invalid/unset sentinel:
// World.laborLedgerSeq starts at 0 and is incremented before assignment,
// so the first minted offer gets ID 1.
type LaborID uint64

// LaborLedgerState is the lifecycle state of a LaborOffer.
//
// Unlike PayLedgerState, `working` (the post-accept state) is NOT terminal:
// accepting a labor offer starts the work window (the worker enters
// StateLaboring until WorkingUntil), and the reward transfers only when that
// window completes. So the ACTIVE (non-terminal) states are
// Pending and Working; the TERMINAL states are Completed / Declined /
// Expired / FailedUnavailable.
type LaborLedgerState string

const (
	// LaborStatePending — the worker has solicited; the employer has not
	// yet responded. Non-terminal.
	LaborStatePending LaborLedgerState = "pending"

	// LaborStateWorking — the employer accepted and the worker is laboring
	// until WorkingUntil. No coins have moved yet; the reward transfers from
	// the employer to the worker when the completion sweep settles this.
	// Non-terminal.
	LaborStateWorking LaborLedgerState = "working"

	// LaborStateCompleted — the work window elapsed and the reward
	// transferred from the employer to the worker. Terminal.
	LaborStateCompleted LaborLedgerState = "completed"

	// LaborStateDeclined — the employer declined a pending offer. No coins
	// move; any reason is spoken in conversation, not carried as a field
	// (the worker and employer are co-present by construction). Terminal.
	LaborStateDeclined LaborLedgerState = "declined"

	// LaborStateExpired — a pending offer's TTL elapsed with no employer
	// response. The aging sweep flips it; an accept arriving past TTL
	// drives the same flip in-band. Terminal.
	LaborStateExpired LaborLedgerState = "expired"

	// LaborStateFailedUnavailable — umbrella for a deal that fell through.
	// At accept: co-presence lost between solicit and accept, or the employer
	// can't cover the reward. At completion: the employer's balance drifted
	// over the work window and can no longer cover the reward, so the
	// finished work goes unpaid. Terminal.
	LaborStateFailedUnavailable LaborLedgerState = "failed_unavailable"
)

// LaborTerminalState is LaborLedgerState minus the active states (pending,
// working) — the four terminal values. Used on the resolved event's
// TerminalState field so the type signature documents the invariant that
// the event never carries an active state. Same underlying string values;
// the split is compile-time enforcement, not a runtime conversion.
type LaborTerminalState string

const (
	LaborTerminalStateCompleted         LaborTerminalState = "completed"
	LaborTerminalStateDeclined          LaborTerminalState = "declined"
	LaborTerminalStateExpired           LaborTerminalState = "expired"
	LaborTerminalStateFailedUnavailable LaborTerminalState = "failed_unavailable"
)

// AttrWorker is the marker attribute that gates solicit_work. An actor
// carrying it is a "Worker" — a body minded up to take service-for-pay
// jobs (LLM-26). Presence-only, like AttrTownCrier / AttrMessenger: the
// value is unused, the key's existence is the grant. The tool-gating
// layer advertises solicit_work only to carriers, and SolicitWork
// re-checks it as defense-in-depth for non-handler callers.
const AttrWorker = "worker"

// actorIsWorker reports whether the actor carries the AttrWorker marker. The
// attribute is presence-only, so existence of the key in a.Attributes is the
// grant. Shared by the labor command gate (SolicitWork) and the worker-aware
// shift check (actorOnShift) that gives an unscheduled worker a default
// dawn/dusk day shift (LLM-137).
func actorIsWorker(a *Actor) bool {
	if a == nil || a.Attributes == nil {
		return false
	}
	_, ok := a.Attributes[AttrWorker]
	return ok
}

const (
	// LaborLedgerTTLDefault — how long a solicited offer stays Pending
	// before the aging sweep flips it Expired. No WorldSettings override is
	// plumbed in the MVP (unlike PayLedgerTTL), so this constant IS the
	// value; the duration matches PayLedgerTTLDefault because a labor
	// solicitation is a conversational moment, same as a pay offer.
	LaborLedgerTTLDefault = 3 * time.Minute

	// LaborLedgerSweepCadenceDefault — how often the sweep scans for both
	// expired-pending offers AND completed work windows. Matches the
	// pay-ledger / scene-quote cadence so admin tuning sees one model.
	LaborLedgerSweepCadenceDefault = 60 * time.Second

	// LaborLedgerTerminalRetentionDefault — how long a terminal offer
	// lingers in the map before the reaper removes it. Bounds the
	// otherwise-unbounded growth of the offer-side map.
	LaborLedgerTerminalRetentionDefault = time.Hour

	// MinLaborDurationMinutes / MaxLaborDurationMinutes clamp the
	// model-proposed work duration to the 2h–8h band (LLM-190). The 2h floor
	// stops the weak model lowballing to a near-instant job it then spends the
	// rest of the conversation talking about; the 8h ceiling is the longest a
	// full work day runs. A job is bounded in practice by the employer's
	// closing time, not this ceiling: AcceptWork clamps WorkingUntil to a
	// keeper-employer's shift end, and the establishment close-up settles any
	// still-running job when the shop shuts (so an 8h offer taken late in the
	// day ends when the shop closes, not 8h later).
	MinLaborDurationMinutes = 120
	MaxLaborDurationMinutes = 480

	// MaxLaborReward caps the reward coins (matches MaxPayWithItemAmount).
	MaxLaborReward = math.MaxInt32

	// MinLaborReward floors the reward — a labor offer must pay something
	// (a 0-coin "job" is the free-work hole, the labor analog of the
	// pay-with-item free-goods reject).
	MinLaborReward = 1
)

// LaborOffer is one worker→employer labor commitment. Minted Pending by
// SolicitWork; AcceptWork flips it Working with a WorkingUntil window (no
// coins move); the completion sweep flips it Completed and transfers the
// reward from the employer to the worker.
type LaborOffer struct {
	ID         LaborID
	WorkerID   ActorID // does the work; solicited the offer
	EmployerID ActorID // pays the reward; accepts or declines

	Reward      int // coins the employer pays on completion (> 0)
	DurationMin int // minutes the work takes (clamped to [Min,Max]LaborDurationMinutes)

	State LaborLedgerState

	// Co-presence context captured at solicitation. AcceptWork revalidates
	// both parties still share HuddleID before starting the job.
	HuddleID HuddleID
	SceneID  SceneID

	CreatedAt    time.Time
	ExpiresAt    time.Time  // pending-TTL deadline (solicitation expiry)
	AcceptedAt   *time.Time // when the employer accepted (work started); nil while Pending
	WorkingUntil *time.Time // AcceptedAt + DurationMin; the completion deadline; nil until Working
	ResolvedAt   *time.Time // terminal timestamp; nil while active

	RootEventID   EventID
	SourceEventID EventID
}

// nextLaborSeq increments the per-run LaborID counter and returns the new
// identifier. World-goroutine-only — called exclusively from the
// SolicitWork Command Fn. The counter starts at 0, so the first minted
// offer gets ID 1; LaborID(0) is the reserved unset sentinel.
func (w *World) nextLaborSeq() LaborID {
	w.laborLedgerSeq++
	return LaborID(w.laborLedgerSeq)
}

// CloneLaborOffer returns a deep copy so a published snapshot can't reach
// back into live world state. The value copy handles the scalar fields;
// the three *time.Time pointers are copied into fresh pointers.
func CloneLaborOffer(o *LaborOffer) *LaborOffer {
	if o == nil {
		return nil
	}
	c := *o
	if o.AcceptedAt != nil {
		t := *o.AcceptedAt
		c.AcceptedAt = &t
	}
	if o.WorkingUntil != nil {
		t := *o.WorkingUntil
		c.WorkingUntil = &t
	}
	if o.ResolvedAt != nil {
		t := *o.ResolvedAt
		c.ResolvedAt = &t
	}
	return &c
}
