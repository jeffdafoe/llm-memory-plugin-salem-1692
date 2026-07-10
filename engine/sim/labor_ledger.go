package sim

import (
	"math"
	"time"
)

// labor_ledger.go — LLM-26 substrate for the work / service-for-pay
// primitive. Carries the LaborLedger aggregate (the labor-offer state
// machine), the per-entry state enum, the LaborID type, the tunable
// constants, and the deep-clone helper.
//
// An offer is minted from EITHER side (LLM-346): a worker offers their labor
// (solicit_work) or an employer offers a job (offer_work). InitiatedBy records
// which, and the party who did NOT mint it is the responder — the only one who
// may accept_work / decline_work. Everything downstream of the mint is
// direction-agnostic: same work window, same settle-at-completion, same
// worker/employer roles.
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
// Super-basic MVP scope: the initiator submits {counterparty, reward,
// duration}; the responder accepts or declines — no counter, no haggle
// (different terms are re-negotiated in conversation and the initiator
// re-offers). The reward may be coins, goods, or both (LLM-225 — "a bowl of
// porridge for some help" is a real contract, not just talk). Both directions
// require the WORKER side to carry the `worker` attribute: solicit_work is
// gated to carriers, and offer_work may only name one.
//
// Persistence: only the ACCEPTED, non-terminal subset is durable —
// BuildCheckpointSnapshot mirrors the en_route + working offers into
// labor_contract (LLM-259). Pending and terminal offers live only in the
// in-memory World and are intentionally restart-lossy. That is clean because no
// coins are ever held: solicit/offer and accept move nothing, and the
// employer→worker reward transfer settles atomically at completion
// (settle-at-completion, labor_settle.go), so losing a PENDING offer on restart
// just means a deal that hadn't been answered didn't happen, with no torn coin
// state to recover.
//
// InitiatedBy is deliberately NOT among the persisted columns. It is read only
// while an offer is Pending — the accept/decline auth gate, the decline-facts
// direction, and the worker's "this shop turned me down" memory — and a
// restored contract is en_route or working, from which Declined is unreachable.
// A restored offer therefore reads as worker-initiated (the zero value is not
// the employer's id), which is exactly the legacy behavior on every path such an
// offer can still take: complete, or fail unpaid.
//
// Consequence for the LLM-187 responder-wake warrant: there is deliberately NO
// labor analog of pay_ledger.go's restartReStampLaborOfferWarrants. The pay
// re-stamp is itself dormant-by-design (PayLedger is likewise restart-lossy),
// and pending offers are not persisted, so LoadWorld holds zero of them to
// re-advertise — a re-stamp pass would never have a row to walk. The
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
// window completes. Between accept and work there is an EnRoute leg (LLM-229):
// when the deal is struck away from the employer's establishment, the worker
// relocates to the workplace and the work window only starts once they are at
// the post with the owner present — so the ACTIVE (non-terminal) states are
// Pending, EnRoute, and Working; the TERMINAL states are Completed / Declined /
// Expired / FailedUnavailable.
type LaborLedgerState string

const (
	// LaborStatePending — the worker has solicited; the employer has not
	// yet responded. Non-terminal.
	LaborStatePending LaborLedgerState = "pending"

	// LaborStateEnRoute — the employer accepted, but the deal was struck away
	// from the employer's establishment, so the worker is relocating to the
	// workplace before the work window starts (LLM-229). The worker is NOT yet
	// laboring: no worker mirror is set (LaborID/LaboringUntil stay clear, the
	// worker stays tickable so it walks itself there / a red need can still
	// interrupt), and the boost does not count them until they are working.
	// The arrival subscriber flips this to Working once the worker is at the
	// post with the owner present; the bounded-wait backstop (EnRouteDeadline)
	// voids it unpaid if that never happens. Non-terminal.
	LaborStateEnRoute LaborLedgerState = "en_route"

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

	// LaborEnRouteWaitDefault caps how long a hired worker may spend relocating
	// to (and then waiting at) the employer's workplace before the work window
	// starts (LLM-229). If the worker isn't at the post with the owner present
	// by AcceptedAt + this cap (also clamped to the employer's closing time),
	// the bounded-wait backstop in the ledger sweep voids the offer unpaid — a
	// walk-deadlocked worker or an owner who never shows must not occupy the
	// worker forever, since the completion sweep only fires on WorkingUntil,
	// which is unset until work actually starts. No coins move on this void; no
	// work happened.
	LaborEnRouteWaitDefault = 30 * time.Minute

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

	// MinLaborReward floors the COIN leg of a reward that carries no goods —
	// a labor offer must pay something (a pay-nothing "job" is the free-work
	// hole, the labor analog of the pay-with-item all-zero-offer reject).
	// Since LLM-225 the reward may instead (or additionally) carry goods
	// (RewardItems), so the enforced invariant is coins >= 1 OR >= 1 goods
	// line, not a bare coin floor.
	MinLaborReward = 1
)

// LaborOffer is one labor commitment between a worker and an employer. Minted
// Pending by SolicitWork (the worker asks for the job) or OfferWork (the
// employer asks for the help); AcceptWork flips it Working with a WorkingUntil
// window (no coins move); the completion sweep flips it Completed and transfers
// the reward from the employer to the worker.
type LaborOffer struct {
	ID         LaborID
	WorkerID   ActorID // does the work
	EmployerID ActorID // pays the reward

	// InitiatedBy is whichever of WorkerID / EmployerID minted the offer
	// (LLM-346). The OTHER party is the responder — the only actor AcceptWork
	// and DeclineWork will take the call from, and the one the LaborOfferReceived
	// warrant wakes. Read LaborOffer.Responder / EmployerInitiated rather than
	// comparing ids at the callsite, so the two directions can't drift apart.
	InitiatedBy ActorID

	// Reward is the coin leg the employer pays on completion; RewardItems is
	// the in-kind leg — goods (canonical kind + qty, the LLM-105 goods-leg
	// shape) the employer hands over at the same settle (LLM-225). At least
	// one leg is non-empty (coins >= MinLaborReward OR >= 1 goods line); both
	// transfer together, atomically, when the completion sweep settles the
	// job. Nothing is escrowed: the ledger is restart-lossy, so items held
	// against a row that a restart destroys would vanish with it — the settle
	// re-checks the employer holds both legs at completion instead.
	Reward      int
	RewardItems []ItemKindQty
	DurationMin int // minutes the work takes (clamped to [Min,Max]LaborDurationMinutes)

	State LaborLedgerState

	// Co-presence context captured at solicitation. AcceptWork revalidates
	// both parties still share HuddleID before starting the job.
	HuddleID HuddleID
	SceneID  SceneID

	CreatedAt     time.Time
	ExpiresAt     time.Time  // pending-TTL deadline (solicitation expiry)
	AcceptedAt    *time.Time // when the employer accepted the offer; nil while Pending
	WorkStartedAt *time.Time // when the work window actually began — accept time for an on-site hire, arrival time for a relocated one (LLM-229); nil until Working
	WorkingUntil  *time.Time // WorkStartedAt + DurationMin (clamped to the employer's close); the completion deadline; nil until Working
	ResolvedAt    *time.Time // terminal timestamp; nil while active

	// EnRouteDeadline bounds the relocation wait (LLM-229): the instant by which
	// an EnRoute worker must be at the post with the owner present or the sweep
	// voids the offer unpaid. Set on accept for a relocated hire (AcceptedAt +
	// LaborEnRouteWaitDefault, clamped to the employer's close); zero for an
	// on-site hire that started work immediately.
	EnRouteDeadline time.Time
	// EnRouteWaiting is true once a relocating worker has arrived at the
	// workplace and is waiting for the owner to show (as opposed to still
	// walking) — the perception self-state reads it to say "waiting for X" vs
	// "on your way to X." Only meaningful while State is EnRoute (LLM-229).
	EnRouteWaiting bool

	RootEventID   EventID
	SourceEventID EventID
}

// EmployerInitiated reports whether the employer minted this offer (offer_work)
// rather than the worker (solicit_work). LLM-346.
//
// A restored labor_contract row carries no InitiatedBy (the column is not
// persisted — see the file header), so it reads worker-initiated. That is the
// correct legacy answer for every path a restored en_route/working offer can
// still take: this predicate is consulted only on Pending offers.
func (o *LaborOffer) EmployerInitiated() bool {
	return o != nil && o.InitiatedBy != "" && o.InitiatedBy == o.EmployerID
}

// Initiator is the party who minted the offer, and Responder the party who must
// answer it with accept_work / decline_work. Together they partition the offer's
// two roles — always, including for a zero InitiatedBy, which both read as the
// solicit_work direction (worker asked, employer answers). That default is what a
// restored labor_contract row carries and what every LaborOffer looked like before
// LLM-346, so legacy values keep their legacy meaning rather than falling into a
// hole where the offer has a responder but nobody who made it.
//
// Prefer these over comparing ids at the callsite: the two directions then cannot
// drift apart. LLM-346.
func (o *LaborOffer) Initiator() ActorID {
	if o == nil {
		return ""
	}
	if o.EmployerInitiated() {
		return o.EmployerID
	}
	return o.WorkerID
}

// Responder is the counterpart of Initiator — see its doc comment.
func (o *LaborOffer) Responder() ActorID {
	if o == nil {
		return ""
	}
	if o.EmployerInitiated() {
		return o.WorkerID
	}
	return o.EmployerID
}

// nextLaborSeq increments the per-run LaborID counter and returns the new
// identifier. World-goroutine-only — called exclusively from the SolicitWork /
// OfferWork Command Fns. The counter starts at 0, so the first minted offer gets
// ID 1; LaborID(0) is the reserved unset sentinel.
func (w *World) nextLaborSeq() LaborID {
	w.laborLedgerSeq++
	return LaborID(w.laborLedgerSeq)
}

// CloneLaborOffer returns a deep copy so a published snapshot can't reach
// back into live world state. The value copy handles the scalar fields;
// the three *time.Time pointers are copied into fresh pointers and the
// RewardItems slice into a fresh backing array.
func CloneLaborOffer(o *LaborOffer) *LaborOffer {
	if o == nil {
		return nil
	}
	c := *o
	c.RewardItems = cloneItemKindQtys(o.RewardItems)
	if o.AcceptedAt != nil {
		t := *o.AcceptedAt
		c.AcceptedAt = &t
	}
	if o.WorkStartedAt != nil {
		t := *o.WorkStartedAt
		c.WorkStartedAt = &t
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
