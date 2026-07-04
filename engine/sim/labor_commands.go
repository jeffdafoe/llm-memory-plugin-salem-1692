package sim

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// labor_commands.go — LLM-26 Command Fns for the worker-initiated
// service-for-pay flow on top of the labor_ledger.go substrate +
// events_labor.go. Three commands:
//
//   - SolicitWork  — worker-side, mints the pending offer {employer,
//                    reward, duration}.
//   - AcceptWork   — employer-side, starts the work window (non-terminal —
//                    the employer→worker reward transfer settles at
//                    completion via the sweep in labor_settle.go).
//   - DeclineWork  — employer-side, declines a pending offer (no coins
//                    move).
//
// Deliberately super-basic (Jeff, 2026-06-26): the worker proposes terms,
// the employer says yes or no, and EVERYTHING ELSE — what the work is, why,
// any haggling — happens in conversation. There is no counter, no message
// field, no task taxonomy: the engine is task-agnostic and the fiction
// carries the variety. The whole machine is gated to actors carrying the
// AttrWorker marker.
//
// Mirrors the pay-with-item Command pattern (pay_with_item_commands.go):
// every Fn re-validates on the world goroutine, mutates atomically, emits
// events, and uses the shared huddle-peer resolver + funds predicate. The
// one structural difference from pay is settlement timing: pay settles
// atomically at accept, whereas labor accept only starts a work window and
// the reward transfers when that window completes — the worker has to put in
// the time before getting paid, so no coins move until then.

// LaborStateBarterPossible is a SolicitWork RESULT-only signal — it is never
// assigned to a LaborOffer nor written to the ledger. It means the employer
// can't cover the reward as ASKED but holds tradeable goods, so an in-kind hire
// is still possible (LLM-225). SolicitWork returns it INSTEAD of minting a
// Declined offer, so the employer is not foreclosed (no employerDeclinedSubject
// drop, no ObservedDeclinedWork stamp) and the worker is steered to re-ask in
// kind (harness). LLM-243 — the labor-side mirror of LLM-222's coin-or-goods
// means-to-pay: don't render a false dead-end for a broke-but-goods-rich
// employer that can still hire in kind.
const LaborStateBarterPossible LaborLedgerState = "barter_possible"

// LaborSolicitResult is SolicitWork's success value — the minted pending
// offer's id + state, plus the resolved employer display name so the tool
// feedback can name who the offer went to. State carries a real ledger state
// (Pending on a placed offer, Declined on the LLM-193 destitute auto-decline)
// or the result-only LaborStateBarterPossible (LLM-243), which mints no offer.
type LaborSolicitResult struct {
	ID           LaborID
	State        LaborLedgerState
	EmployerName string
}

// LaborAcceptResult is AcceptWork's value. On a gate-driven terminal flip
// (expired / failed) State carries that terminal and WorkingUntil is zero.
// On a real accept: State is Working with WorkingUntil the completion deadline
// when work started immediately (on-site hire / workless employer), or EnRoute
// with a zero WorkingUntil when the worker must first relocate to the workplace
// (LLM-229 — the window is unknown until they arrive). Payment is the
// pre-formatted reward phrase ("5 coins", "1 porridge and 2 coins" — LLM-225)
// so the harness steer can name the full terms without re-formatting
// (formatPayment is sim-internal).
type LaborAcceptResult struct {
	ID           LaborID
	State        LaborLedgerState
	WorkerName   string
	Reward       int
	Payment      string
	WorkingUntil time.Time
}

// LaborDeclineResult is DeclineWork's value.
type LaborDeclineResult struct {
	ID    LaborID
	State LaborLedgerState
}

// SolicitWork returns the Command for a worker offering their labor to a
// co-present employer. Pending offer only — the employer resolves it with
// accept_work / decline_work. The reward may be coins, goods the employer
// holds (rewardItems, the LLM-105 goods-leg shape), or both (LLM-225); at
// least one leg must be non-empty. Gates first-failure-wins: numeric bounds
// → worker exists → worker attribute → not-walking → in-conversation →
// not-already-laboring → scene anchor → employer resolve → not-self → no
// duplicate pending offer to the same employer → goods resolve.
func SolicitWork(workerID ActorID, employerName string, reward int, rewardItems []PayItemInput, durationMin int, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Numeric defense. SolicitWork is exported — non-handler callers
			// could pass shapes the decode side rejects.
			if reward < 0 {
				return nil, fmt.Errorf("solicit_work: reward cannot be negative (got %d)", reward)
			}
			// The pay-nothing hole: a reward must carry coins, goods, or both.
			// The coin floor applies only when no goods leg is offered.
			if reward < MinLaborReward && len(rewardItems) == 0 {
				return nil, fmt.Errorf(
					"solicit_work: the reward must be worth something — ask for at least %d coin, or goods via reward_items, or both.",
					MinLaborReward,
				)
			}
			if reward > MaxLaborReward {
				return nil, fmt.Errorf("solicit_work: reward exceeds maximum (got %d, max %d)", reward, MaxLaborReward)
			}
			if durationMin < MinLaborDurationMinutes {
				return nil, fmt.Errorf("solicit_work: duration must be at least %d minute (got %d)", MinLaborDurationMinutes, durationMin)
			}
			if durationMin > MaxLaborDurationMinutes {
				return nil, fmt.Errorf("solicit_work: duration exceeds maximum (got %d, max %d minutes)", durationMin, MaxLaborDurationMinutes)
			}

			worker, ok := w.Actors[workerID]
			if !ok {
				return nil, fmt.Errorf("SolicitWork: worker %q not in world", workerID)
			}
			// Worker-attribute gate. The tool-gating layer only advertises
			// solicit_work to AttrWorker carriers; re-checked here because
			// SolicitWork is exported and tool/test/cascade callers reach the
			// substrate directly.
			if !actorIsWorker(worker) {
				return nil, errors.New(
					"you aren't taken on as a worker — only villagers minded up as workers can offer their labor for pay.",
				)
			}
			if worker.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — finish your move before offering to work. " +
						"Either offer BEFORE the move_to, or wait until you arrive.",
				)
			}
			if worker.CurrentHuddleID == "" {
				return nil, errors.New(
					"you're not in a conversation — start one with the person you want to work for first.",
				)
			}
			// Already committed to a job — can't take a second. Ledger-
			// authoritative (workerHasLiveJob): a Working offer occupies the
			// worker until the sweep settles it, even past its window, so this
			// can't be fooled by the actor mirror reading "free" during sweep
			// lag.
			if workerHasLiveJob(w, workerID) {
				return nil, errors.New(
					"you're already on a job — finish your current work before offering to take on more.",
				)
			}

			sceneID, ok := resolveSellerScene(w, worker.CurrentHuddleID)
			if !ok {
				return nil, errors.New(
					"your current conversation isn't anchored to a scene — wait for it to be established before offering to work.",
				)
			}

			// Resolve the employer against huddle peers — same tight scope as
			// pay (same huddle, case-insensitive, ambiguity reject).
			employerID, ok, ambiguous := findHuddlePeerByDisplayName(w, workerID, worker.CurrentHuddleID, employerName)
			if ambiguous {
				return nil, fmt.Errorf(
					"more than one person named %q is in this conversation — use a unique full name before offering.",
					employerName,
				)
			}
			if !ok {
				return nil, fmt.Errorf(
					"no one named %q in this conversation — re-check who is here before offering to work.",
					employerName,
				)
			}
			if employerID == workerID {
				return nil, errors.New("you cannot offer to work for yourself")
			}
			employer, ok := w.Actors[employerID]
			if !ok {
				return nil, fmt.Errorf("SolicitWork: employer %q vanished mid-resolve", employerID)
			}

			// Co-resident / co-worker gate (LLM-145): a worker can't bill its
			// own household or workplace crew. The Walkers all share the Walker
			// Residence; unchecked, a broke worker shut in with kin just bids
			// family for coin they don't have. The perception affordance already
			// hides when only housemates/co-workers are present (CanSolicitWork);
			// this is the substrate backstop for direct / stale-perception callers,
			// the same posture as the worker-attribute re-check above.
			if worker.HomeStructureID != "" && worker.HomeStructureID == employer.HomeStructureID {
				return nil, fmt.Errorf(
					"you live with %s — offer your labor to someone outside your own household.",
					employer.DisplayName,
				)
			}
			if worker.WorkStructureID != "" && worker.WorkStructureID == employer.WorkStructureID {
				return nil, fmt.Errorf(
					"you and %s keep the same workplace — offer your labor to someone else.",
					employer.DisplayName,
				)
			}

			// Duplicate-offer gate: at most ONE pending outgoing offer per
			// worker (any employer). A worker bids one job at a time and waits
			// for an answer — this prevents both the weak-model re-offer storm
			// AND a worker staking valid-looking offers to several employers at
			// once, where every late acceptor would then hit failed_unavailable
			// (code_review). Past-TTL entries are skipped (they resolve on the
			// sweep, not here).
			if o := workerPendingLaborOffer(w, workerID, at); o != nil {
				return nil, fmt.Errorf(
					"you already have a work offer out awaiting an answer (offer id %d) — wait for a response before offering again.",
					o.ID,
				)
			}

			// Resolve the in-kind reward leg (LLM-225). resolvePayItems is the
			// shared goods-line resolver (pay_with_item / counter_pay / give):
			// free-text → canonical kind, duplicate-kind reject, qty bounds, the
			// LLM-167 labor-token steer, and the service-kind reject. Resolved
			// LAST among the gates so a solicit that bounces on an earlier gate
			// (ambiguous employer, duplicate offer) doesn't mint a qty-0
			// discovery kind for nothing.
			resolvedRewardItems, err := resolvePayItems(w, rewardItems)
			if err != nil {
				return nil, err
			}

			// Build the pending offer — but do NOT record it yet. The LLM-243
			// barter branch below leaves NO ledger entry, so the id is minted and
			// the offer recorded only once we know it will be placed (Pending) or
			// stand as the LLM-193 destitute decline.
			expiresAt := at.Add(LaborLedgerTTLDefault)
			offer := &LaborOffer{
				WorkerID:    workerID,
				EmployerID:  employerID,
				Reward:      reward,
				RewardItems: resolvedRewardItems,
				DurationMin: durationMin,
				State:       LaborStatePending,
				HuddleID:    worker.CurrentHuddleID,
				SceneID:     sceneID,
				CreatedAt:   at,
				ExpiresAt:   expiresAt,
			}
			// canCover spans both reward legs — coins AND the in-kind goods the
			// worker asked for (LLM-225). Computed once and reused by the barter
			// branch and the destitute decline below.
			canCover := employerCanCoverLaborReward(employer, offer)

			// LLM-243: coin-anchored-gate mirror of LLM-222 on the hiring side. The
			// employer can't cover the reward the worker ASKED (coins and/or the
			// named goods), but holds tradeable goods, so an in-kind hire is still
			// possible (LLM-225, the accept_work in-kind path). Do NOT mint a
			// Declined offer: that over-generalizes "can't meet these terms" into
			// "can't hire you at all" — the Declined ledger entry drops the employer
			// from the solicit audience (employerDeclinedSubject) and the
			// LaborResolved→ObservedDeclinedWork stamp drops its shop from the
			// seek-work directory, so the worker perceives "No one here can hire you"
			// and routes past an employer it could hire from. Return the barter
			// signal with no ledger entry — the employer stays solicitable, no
			// decline is remembered — and the harness steers the worker to re-ask in
			// kind. Only a genuinely destitute employer (no coin AND no goods —
			// employerCanHireInKind false) still hits the LLM-193 auto-decline below.
			if !canCover && employerCanHireInKind(employer) {
				return LaborSolicitResult{
					State:        LaborStateBarterPossible,
					EmployerName: employer.DisplayName,
				}, nil
			}

			// Mint: assign the id and record the offer.
			id := w.nextLaborSeq()
			offer.ID = id
			w.LaborLedger[id] = offer

			// LLM-193: affordability gate. A broke employer that can neither pay the
			// coin nor barter any goods (employerCanHireInKind false above) can only
			// refuse, but the solicit still emitted LaborOfferReceived, which WOKE the
			// employer for a full LLM tick that ended in "my purse is empty": a tick
			// burned on BOTH sides for no hire (the live Walker/Ward store-to-store
			// hunt — 68% of NPC speech was unconverted work-chatter). Resolve the offer
			// Declined immediately, WITHOUT emitting LaborOfferReceived, so the employer
			// is never woken. The Declined terminal reuses the existing seek-work
			// off-ramp with no new perception code: it stamps the worker's 12h "this
			// shop declined me" memory (handleDeclinedWorkOnResolved → ObservedDeclinedWork
			// on the employer's workplace), which drops the shop from buildSeekWorkPlaces,
			// and the Declined ledger entry drops the employer from the solicit audience
			// (employerDeclinedSubject). So the worker solicits once, learns, and is
			// steered to a shop that can actually pay. The balance can rise and the
			// memory decays, so a shop that later has coin re-enters the directory.
			// RootEventID/SourceEventID stay unset — there was no received event, and
			// finalizeLaborTerminal doesn't need them. recordFacts=false: no conscious
			// decline happened, so no relationship fact is written (matches AcceptWork's
			// accept-time funds failure).
			if !canCover {
				state := finalizeLaborTerminalOpts(w, offer, LaborTerminalStateDeclined, false, at, false)
				return LaborSolicitResult{
					ID:           id,
					State:        state,
					EmployerName: employer.DisplayName,
				}, nil
			}

			evt := &LaborOfferReceived{
				LaborID:     id,
				WorkerID:    workerID,
				EmployerID:  employerID,
				Reward:      reward,
				RewardItems: cloneItemKindQtys(resolvedRewardItems),
				DurationMin: durationMin,
				SceneID:     sceneID,
				HuddleID:    worker.CurrentHuddleID,
				ExpiresAt:   expiresAt,
				At:          at,
			}
			w.emit(evt)
			offer.RootEventID = evt.RootEventID()
			offer.SourceEventID = evt.EventID()

			return LaborSolicitResult{
				ID:           id,
				State:        LaborStatePending,
				EmployerName: employer.DisplayName,
			}, nil
		},
	}
}

// AcceptWork returns the Command for an employer accepting a pending labor
// offer. Gates 1-2 (caller exists, offer exists) and gates 3-4 (auth,
// state) are idempotent rejects — tool error, NO transition. Gates 5+ (TTL,
// co-presence, worker-free, funds) DRIVE a terminal flip: the gate failure
// IS the resolution (FailedUnavailable / Expired), not a tool error.
//
// On all-pass the hire is struck (AcceptedAt stamped, LaborOfferAccepted
// emitted), but where the work happens depends on the deal site (LLM-229): a
// deal struck at the employer's own post with the owner present — or an
// employer with no work structure at all — starts the work window immediately
// in place (startLaborWork); a deal struck anywhere else flips the offer to
// EnRoute and relocates the worker to the workplace, with the window starting
// only once they are at the post with the owner present (the arrival
// subscriber). No coins move here — the employer→worker reward transfer
// settles atomically when the completion sweep (labor_settle.go) fires, after
// the worker has put in the time.
func AcceptWork(callerID ActorID, laborID LaborID, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Gate 1: caller exists.
			caller, ok := w.Actors[callerID]
			if !ok {
				return nil, fmt.Errorf("AcceptWork: caller %q not in world", callerID)
			}

			// Gate 2: offer exists.
			offer, ok := w.LaborLedger[laborID]
			if !ok || offer == nil {
				return nil, fmt.Errorf(
					"AcceptWork: labor offer %d not found — re-check the labor_id.",
					laborID,
				)
			}

			// Gate 3: auth (idempotent reject — NO transition).
			if offer.EmployerID != callerID {
				return nil, fmt.Errorf(
					"AcceptWork: only the employer of labor offer %d may accept it",
					laborID,
				)
			}

			// Gate 4: state idempotent reject (NO transition).
			if offer.State != LaborStatePending {
				return nil, fmt.Errorf(
					"AcceptWork: labor offer %d is no longer pending (currently %s) — nothing to accept.",
					laborID, offer.State,
				)
			}

			// Gate 5: TTL. From here gate failures drive terminal transitions.
			if !offer.ExpiresAt.IsZero() && !at.Before(offer.ExpiresAt) {
				return finalizeLaborTerminal(w, offer, LaborTerminalStateExpired, false, at), nil
			}

			// Gate 6: co-presence. Worker and employer must both still be in
			// offer.HuddleID (captured at solicitation).
			worker, workerOK := w.Actors[offer.WorkerID]
			if !workerOK ||
				offer.HuddleID == "" ||
				worker.CurrentHuddleID != offer.HuddleID ||
				caller.CurrentHuddleID != offer.HuddleID {
				return finalizeLaborTerminal(w, offer, LaborTerminalStateFailedUnavailable, false, at), nil
			}

			// Gate 7: worker free. Ledger-authoritative — a worker with ANY
			// Working offer is occupied until it settles, even if its window has
			// elapsed but the sweep hasn't run yet. Reading the actor mirror's
			// timestamp here would let a second job slip in during sweep lag and
			// orphan the first job's mirror (code_review).
			if workerHasLiveJob(w, offer.WorkerID) {
				return finalizeLaborTerminal(w, offer, LaborTerminalStateFailedUnavailable, false, at), nil
			}

			// Gate 8: funds + goods (courtesy check, NOT authoritative). Nothing
			// moves at accept — the employer→worker transfer settles at
			// completion. But taking on a job the employer plainly can't pay for
			// right now — short of coins OR of the promised in-kind goods
			// (LLM-225) — is a bad deal, so fail it here rather than let the
			// worker labor (possibly for hours) toward a payout that was never
			// going to land. The completion sweep re-checks both legs
			// authoritatively, since the employer's holdings can drift across a
			// long work window.
			if !employerCanCoverLaborReward(caller, offer) {
				return finalizeLaborTerminal(w, offer, LaborTerminalStateFailedUnavailable, false, at), nil
			}

			// All gates pass. The employer has accepted — stamp AcceptedAt and,
			// for the struck deal, touch the huddle's silence + loop-sweep
			// (LLM-159) progress clocks so neither concludes it mid-arrangement.
			// Both run BEFORE any relocation walk, which pulls the worker out of
			// the huddle. No coins move at accept — the employer→worker reward
			// transfer settles atomically at completion (labor_settle.go).
			offer.AcceptedAt = timePtrLabor(at)
			touchHuddleProgress(w, offer.HuddleID, at)

			// LLM-229: decide where the work happens. Accept used to flip the
			// worker to StateLaboring IN PLACE, which paid for presence rather
			// than help whenever the deal was struck away from the employer's shop
			// (the boost only counts a worker at the post). Now the worker
			// relocates to the workplace and the window starts on arrival — except
			// two cases that start immediately, in place:
			//   1. the employer has no work structure (an unscheduled dawn/dusk
			//      employer) — there is no post to relocate to; today's behavior.
			//   2. the worker is already at the employer's post with the owner
			//      present (a deal struck on-site, e.g. the seek-work push) — they
			//      are already where the work happens.
			var resultState LaborLedgerState
			var resultWorkingUntil time.Time
			ws := caller.WorkStructureID
			switch {
			case ws == "" || (actorAtWorkpost(w, worker, ws) && actorAtWorkpost(w, caller, ws)):
				startLaborWork(w, offer, worker, caller, at)
				resultState, resultWorkingUntil = LaborStateWorking, *offer.WorkingUntil
			default:
				// Relocate: the worker heads to the employer's workplace; the work
				// window only starts once they are at the post with the owner
				// present (handleLaborArrivalOnArrival). The worker never enters
				// ahead of the owner. EnRouteDeadline bounds the wait so a walk
				// that deadlocks or an owner who never shows can't occupy the
				// worker forever — the sweep voids it unpaid past the deadline.
				offer.State = LaborStateEnRoute
				offer.EnRouteDeadline = clampWorkingUntilToEmployerClose(w, caller, at.Add(LaborEnRouteWaitDefault), at)
				sendWorkerToWorkplace(w, worker, caller, true, at)
				resultState = LaborStateEnRoute
			}

			// The hire itself happened here regardless of when the work starts, so
			// emit LaborOfferAccepted at accept (the action-log "hired" beat keys
			// off it). WorkingUntil is the real window for an immediate start, zero
			// for a relocation (unknown until the worker arrives and work begins).
			evt := &LaborOfferAccepted{
				LaborID:      offer.ID,
				WorkerID:     offer.WorkerID,
				EmployerID:   offer.EmployerID,
				Reward:       offer.Reward,
				RewardItems:  cloneItemKindQtys(offer.RewardItems),
				DurationMin:  offer.DurationMin,
				WorkingUntil: resultWorkingUntil,
				SceneID:      offer.SceneID,
				HuddleID:     offer.HuddleID,
				At:           at,
			}
			w.emit(evt)

			return LaborAcceptResult{
				ID:           offer.ID,
				State:        resultState,
				WorkerName:   worker.DisplayName,
				Reward:       offer.Reward,
				Payment:      formatPayment(offer.Reward, offer.RewardItems),
				WorkingUntil: resultWorkingUntil,
			}, nil
		},
	}
}

// DeclineWork returns the Command for an employer declining a pending labor
// offer. Three gates first-failure-wins, all idempotent rejects until the
// flip: caller exists → offer exists → caller is the employer → offer is
// pending. No coins move; no co-presence or TTL gate (a decline is
// unconditional on a pending offer the caller owns). Any explanation is
// spoken in conversation, not carried as a field.
func DeclineWork(callerID ActorID, laborID LaborID, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if _, ok := w.Actors[callerID]; !ok {
				return nil, fmt.Errorf("DeclineWork: caller %q not in world", callerID)
			}
			offer, ok := w.LaborLedger[laborID]
			if !ok || offer == nil {
				return nil, fmt.Errorf(
					"DeclineWork: labor offer %d not found — re-check the labor_id.",
					laborID,
				)
			}
			if offer.EmployerID != callerID {
				return nil, fmt.Errorf(
					"DeclineWork: only the employer of labor offer %d may decline it",
					laborID,
				)
			}
			if offer.State != LaborStatePending {
				return nil, fmt.Errorf(
					"DeclineWork: labor offer %d is no longer pending (currently %s) — nothing to decline.",
					laborID, offer.State,
				)
			}
			state := finalizeLaborTerminal(w, offer, LaborTerminalStateDeclined, false, at)
			return LaborDeclineResult{ID: offer.ID, State: state}, nil
		},
	}
}

// finalizeLaborTerminal flips a non-terminal offer to the given terminal
// state, stamps ResolvedAt, emits LaborResolved, and returns the new state.
// Used by DeclineWork, AcceptWork's gate-driven flips (Expired /
// FailedUnavailable), AND the completion sweep (Completed /
// FailedUnavailable) — settleCompletedLabor does the coin transfer + worker-
// state cleanup itself, then calls this to flip + emit.
//
// This helper flips the offer and emits, nothing more. Caller guarantees the
// offer is currently non-terminal and that any worker-state mirror
// (LaboringUntil/StateLaboring) — set only on a Working offer — has already
// been cleared. A Pending offer never set that mirror, so there is nothing to
// clear on the decline/expire paths.
//
// workPerformed records whether the work window actually elapsed before this
// terminal (Completed, or the completion-sweep's unpaid FailedUnavailable). It
// rides onto LaborResolved.WorkPerformed and drives the relationship-fact split
// below — see recordLaborInteractions. Callers on the no-work paths (decline,
// accept-time gate flips, the pending-expire sweep) pass false; only
// settleCompletedLabor passes true.
func finalizeLaborTerminal(w *World, offer *LaborOffer, terminal LaborTerminalState, workPerformed bool, at time.Time) LaborLedgerState {
	return finalizeLaborTerminalOpts(w, offer, terminal, workPerformed, at, true)
}

// finalizeLaborTerminalOpts is finalizeLaborTerminal with control over whether the
// bidirectional relationship facts are written. Every conscious-terminal caller
// (decline_work, AcceptWork's gate flips, the completion/expiry sweep) goes through
// finalizeLaborTerminal (recordFacts=true). The LLM-193 affordability auto-decline
// passes recordFacts=false: no one consciously declined — the engine resolved the
// offer because the employer couldn't cover the reward and was never woken — so an
// "I declined X" employer fact would fabricate a social decision the employer never
// made (the same reason AcceptWork's accept-time funds FailedUnavailable writes no
// facts). The LaborResolved event and the worker's 12h ObservedDeclinedWork memory
// still fire; only the relationship facts are suppressed.
func finalizeLaborTerminalOpts(w *World, offer *LaborOffer, terminal LaborTerminalState, workPerformed bool, at time.Time, recordFacts bool) LaborLedgerState {
	// A Completed terminal means the work window elapsed by definition — pin the
	// invariant locally so a future caller can't emit Completed with
	// workPerformed=false (which would still write the "worked" facts below).
	// code_review, LLM-165.
	if terminal == LaborTerminalStateCompleted {
		workPerformed = true
	}
	priorState := offer.State
	offer.State = LaborLedgerState(terminal)
	offer.ResolvedAt = timePtrLabor(at)

	// LLM-186 diagnostic: log every labor terminal with the PRE-flip state and
	// the work window. An early-finalized Working offer — priorState "working"
	// while now is well before workingUntil — is the live PW-Apothecary symptom
	// (a hired worker's job vanishing before its window) and previously left no
	// journal trace, since resolutions were unlogged. acceptedAt/workingUntil are
	// *time.Time; %v deref-prints the time (or <nil>).
	log.Printf("sim/labor: finalize offer %d %s->%s worker=%s employer=%s reward=%d rewardItems=%v acceptedAt=%v workingUntil=%v now=%v workPerformed=%t",
		offer.ID, priorState, terminal, offer.WorkerID, offer.EmployerID, offer.Reward, offer.RewardItems, offer.AcceptedAt, offer.WorkingUntil, at, workPerformed)

	evt := &LaborResolved{
		LaborID:       offer.ID,
		WorkerID:      offer.WorkerID,
		EmployerID:    offer.EmployerID,
		Reward:        offer.Reward,
		RewardItems:   cloneItemKindQtys(offer.RewardItems),
		DurationMin:   offer.DurationMin,
		TerminalState: terminal,
		WorkPerformed: workPerformed,
		SceneID:       offer.SceneID,
		HuddleID:      offer.HuddleID,
		At:            at,
	}
	w.emit(evt)

	// Relationship facts (LLM-165) — written inline here, the labor mirror of
	// finalizePayLedgerTerminal's decline-path RecordInteraction. Only the
	// terminals that are a social move between the two NPCs write; the
	// KindNPCShared + visitor gates inside RecordInteraction decide which of the
	// two writes actually persists. Suppressed for the LLM-193 affordability
	// auto-decline (recordFacts=false) — that resolution is not a conscious social
	// move by either party.
	if recordFacts {
		recordLaborInteractions(w, offer, terminal, workPerformed, at)
	}
	return offer.State
}

// recordLaborInteractions writes the bidirectional SalientFacts for a labor
// terminal, the labor counterpart to pay's Paid/Declined relationship writes.
// Three social terminals get facts; everything else returns early:
//
//   - Completed                         → Worked (worker) / Hired (employer)
//   - FailedUnavailable && workPerformed → WorkedUnpaid / LeftWorkerUnpaid
//     (the worker finished the job but the employer could no longer pay — the
//     one labor beat pay has no analog for; see LaborResolved.WorkPerformed)
//   - Declined                          → WorkDeclinedBy / DeclinedWork
//
// Expired and the accept-time FailedUnavailable fall-through (workPerformed
// false) are low-signal lifecycle events — nobody acted, or the deal never
// started — and write nothing, matching finalizePayLedgerTerminal's
// expired/withdrawn/failed skip. Each fact is first-person from its rememberer's
// POV; RecordInteraction's KindNPCShared + visitor gates filter which side
// actually persists. Errors are logged, not surfaced — a relationship write
// never fails the terminal itself (mirrors the pay/deliver callsites).
func recordLaborInteractions(w *World, offer *LaborOffer, terminal LaborTerminalState, workPerformed bool, at time.Time) {
	// Pick the interaction kinds for this terminal. The non-social terminals
	// (expired, and the accept-time failed_unavailable fall-through) return here
	// — before the name lookups below — and write nothing.
	var workerKind, employerKind InteractionKind
	switch {
	case terminal == LaborTerminalStateCompleted:
		workerKind, employerKind = InteractionWorked, InteractionHired
	case terminal == LaborTerminalStateFailedUnavailable && workPerformed:
		workerKind, employerKind = InteractionWorkedUnpaid, InteractionLeftWorkerUnpaid
	case terminal == LaborTerminalStateDeclined:
		workerKind, employerKind = InteractionWorkDeclinedBy, InteractionDeclinedWork
	default:
		return
	}

	workerName := actorDisplayName(w, offer.WorkerID)
	employerName := actorDisplayName(w, offer.EmployerID)
	// The worked-duration facts report the ACTUAL time put in, not the offered
	// duration: a job clamped to the employer's closing time (LLM-190) finishes
	// early, so "worked about 8 hours" would overstate a shift-bounded job. The
	// real window runs WorkStartedAt→WorkingUntil — WorkStartedAt is when work
	// actually began (accept time for an on-site hire, arrival time for a
	// relocated one, LLM-229), so the relocation walk is never billed as work.
	// Fall back to the offered duration if either bound is unset (declines never
	// set them and don't take this path anyway).
	workedMin := offer.DurationMin
	if offer.WorkStartedAt != nil && offer.WorkingUntil != nil {
		if m := int(offer.WorkingUntil.Sub(*offer.WorkStartedAt).Minutes()); m > 0 {
			workedMin = m
		}
	}
	// The payment phrase names both legs of the reward — "5 coins",
	// "1 porridge", "1 porridge and 2 coins" (formatPayment, LLM-225) — so an
	// in-kind promise is remembered as concretely as a coin one, including in
	// the stiffed-worker facts.
	payment := formatPayment(offer.Reward, offer.RewardItems)
	var workerFact, employerFact string
	switch workerKind {
	case InteractionWorked:
		workerFact, employerFact = laborCompletedFacts(workerName, employerName, payment, workedMin)
	case InteractionWorkedUnpaid:
		workerFact, employerFact = laborUnpaidFacts(workerName, employerName, payment, workedMin)
	case InteractionWorkDeclinedBy:
		workerFact, employerFact = laborDeclinedFacts(workerName, employerName, payment)
	}

	if _, err := RecordInteraction(offer.WorkerID, offer.EmployerID, workerKind, workerFact, at).Fn(w); err != nil {
		log.Printf("sim.finalizeLaborTerminal: RecordInteraction worker→employer %q→%q: %v", offer.WorkerID, offer.EmployerID, err)
	}
	if _, err := RecordInteraction(offer.EmployerID, offer.WorkerID, employerKind, employerFact, at).Fn(w); err != nil {
		log.Printf("sim.finalizeLaborTerminal: RecordInteraction employer→worker %q→%q: %v", offer.EmployerID, offer.WorkerID, err)
	}
}

// laborCoinsPhrase renders a coin amount with the right singular/plural unit
// ("5 coins" / "1 coin"), mirroring payFactText's inline coin handling.
func laborCoinsPhrase(n int) string {
	if n == 1 {
		return "1 coin"
	}
	return fmt.Sprintf("%d coins", n)
}

// humanizeLaborMinutes renders a labor duration in hours/minutes for a salient
// fact ("2 hours", "1 hour 30 minutes", "45 minutes", "1 minute"). A sim-local
// copy of perception's humanizeWorkMinutes — perception imports sim, so the
// renderer can't be shared the other way without an import cycle, and the two
// serve different layers (a relationship-fact sentence vs. a live perception cue).
func humanizeLaborMinutes(min int) string {
	if min < 1 {
		min = 1
	}
	if min < 60 {
		if min == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", min)
	}
	h := min / 60
	m := min % 60
	hUnit := "hours"
	if h == 1 {
		hUnit = "hour"
	}
	if m == 0 {
		return fmt.Sprintf("%d %s", h, hUnit)
	}
	mUnit := "minutes"
	if m == 1 {
		mUnit = "minute"
	}
	return fmt.Sprintf("%d %s %d %s", h, hUnit, m, mUnit)
}

// laborCompletedFacts returns the (worker→employer, employer→worker) salient
// fact texts for a completed, paid job — the labor analogue of payFactText's
// Paid/PaidBy pair, with the work duration folded in. payment is the
// pre-formatted reward phrase (formatPayment — coins, goods, or both).
func laborCompletedFacts(workerName, employerName, payment string, durationMin int) (workerFact, employerFact string) {
	dur := humanizeLaborMinutes(durationMin)
	workerFact = fmt.Sprintf("I worked for %s and earned %s for about %s of work.", employerName, payment, dur)
	employerFact = fmt.Sprintf("%s worked for me and I paid them %s for about %s of work.", workerName, payment, dur)
	return workerFact, employerFact
}

// laborUnpaidFacts returns the (worker→employer, employer→worker) salient fact
// texts for a job the worker finished but the employer could no longer pay for
// (the completion-time failed_unavailable). The aggrieved beat pay has no
// equivalent of — pay settles at accept, so "work done, never paid" can't arise
// there. payment names the full promised reward (coins and/or goods), so a
// worker stiffed of a promised bowl of porridge remembers the porridge.
func laborUnpaidFacts(workerName, employerName, payment string, durationMin int) (workerFact, employerFact string) {
	dur := humanizeLaborMinutes(durationMin)
	workerFact = fmt.Sprintf("I worked for %s for about %s but was never paid the %s I was owed.", employerName, dur, payment)
	employerFact = fmt.Sprintf("%s worked for me for about %s but I could not pay the %s I owed.", workerName, dur, payment)
	return workerFact, employerFact
}

// laborDeclinedFacts returns the (worker→employer, employer→worker) salient
// fact texts for an offer the employer declined — the labor analogue of
// payDeclinedFactText. No duration (the work never started); the payment names
// the terms that were refused.
func laborDeclinedFacts(workerName, employerName, payment string) (workerFact, employerFact string) {
	workerFact = fmt.Sprintf("%s declined my offer to work for them for %s.", employerName, payment)
	employerFact = fmt.Sprintf("I declined %s's offer to work for me for %s.", workerName, payment)
	return workerFact, employerFact
}

// timePtrLabor returns a pointer to a COPY of t. Because the argument is
// passed by value, each call yields a distinct *time.Time — so the same
// instant can be handed to multiple fields (offer.WorkingUntil and
// worker.LaboringUntil) without aliasing them through one pointer.
func timePtrLabor(t time.Time) *time.Time { return &t }

// clampWorkingUntilToEmployerClose caps a job's completion deadline to the
// employer's closing time — a worker can't keep working past when the shop
// stops serving (LLM-190). Returns the earlier of the proposed workingUntil and
// the wall-clock instant the employer's current shift ends: a scheduled keeper's
// posted close, or the dawn/dusk day window for an unscheduled employer
// (effectiveShiftWindow). A no-op when the employer has no shift window or is
// already off shift at `at` (no live shift to bound against — an off-shift
// employer taking on work is abnormal, so the natural window stands). The
// village clock is wall-clock (localMinuteOfDay), so the shift-end minute-of-day
// maps straight onto the wall clock; the modulo carries a wrap-midnight shift to
// tomorrow's close. World-goroutine-only.
func clampWorkingUntilToEmployerClose(w *World, employer *Actor, workingUntil, at time.Time) time.Time {
	if employer == nil {
		return workingUntil
	}
	start, end, ok := effectiveShiftWindow(w, employer)
	if !ok {
		return workingUntil
	}
	loc := time.UTC
	if w != nil && w.Settings.Location != nil {
		loc = w.Settings.Location
	}
	local := at.In(loc)
	nowMin := local.Hour()*60 + local.Minute()
	if !minuteInShiftWindow(start, end, nowMin) {
		return workingUntil // off shift now — no live shift to bound against
	}
	// Build the close instant from the local wall-clock DATE at the shift-end
	// minute, seconds zeroed. Adding whole minutes to `at` would carry at's
	// seconds past the close (minute-of-day drops sub-minute precision), leaving
	// the worker laboring up to ~59s past close and the fact one minute long
	// (code_review).
	closeAt := time.Date(local.Year(), local.Month(), local.Day(), end/60, end%60, 0, 0, loc)
	if end <= start && nowMin >= start {
		// Wrap-midnight shift in its pre-midnight half — the close is tomorrow.
		closeAt = closeAt.AddDate(0, 0, 1)
	}
	if closeAt.Before(workingUntil) {
		return closeAt
	}
	return workingUntil
}

// announceLaborCloseoutIfShopClosed emits the keeper's "we're shutting, your
// work's done" closing line to the worker when a completed job ended because the
// shop closed for the day (LLM-190): the employer is an establishment keeper now
// OFF shift, so this completion is the shift-end close-out rather than a job that
// finished mid-shift. AcceptWork clamps a job to the keeper's closing time, so a
// shift-bounded job lands here exactly as the keeper goes off shift; a job that
// finished earlier in the day completes silently (keeper still on shift). Engine-
// authored Spoke (HuddleID empty — the businessowner / establishment-closeup
// pattern); the worker rides RecipientIDs so the speech reactor lets it perceive
// the line. No-op for a non-keeper employer or one still on shift.
// World-goroutine-only.
func announceLaborCloseoutIfShopClosed(w *World, employer, worker *Actor, payment string, now time.Time) {
	if worker == nil || !shopClosedForCloseout(w, employer, now) {
		return
	}
	w.emit(&Spoke{
		SpeakerID:    employer.ID,
		RecipientIDs: []ActorID{worker.ID},
		Text:         laborCloseoutLine(payment),
		At:           now,
	})
}

// shopClosedForCloseout reports whether a just-completed job ended because the
// employer's shop closed for the day: the employer is an establishment keeper
// (BusinessownerState set) who is now OFF shift. A non-keeper employer, or a
// keeper still on shift (the job finished earlier in the day), is not a close-out
// — the completion stays silent. World-goroutine-only.
func shopClosedForCloseout(w *World, employer *Actor, now time.Time) bool {
	if employer == nil || employer.BusinessownerState == nil {
		return false
	}
	start, end, ok := effectiveShiftWindow(w, employer)
	if !ok {
		return false
	}
	return !minuteInShiftWindow(start, end, localMinuteOfDay(w, now))
}

// laborCloseoutLine composes the keeper's closing call to a worker whose job
// ended because the shop shut for the day — telling them they're done and
// handing over the pay. Plain modern register, matching the establishment
// close-up's closingLines; the payment phrase (coins and/or goods —
// formatPayment) is templated, so this is composed in Go rather than drawn
// from a narration pool.
func laborCloseoutLine(payment string) string {
	return fmt.Sprintf(
		"That's the shop shut for the day — your work's done, and well done. Here's your %s, with my thanks.",
		payment,
	)
}

// employerCanCoverLaborReward reports whether the employer currently holds
// BOTH legs of the offer's reward: the coins (buyerCanAfford) and every
// in-kind goods line (buyerHoldsPayItems) — LLM-225. The single "can the
// employer pay this" predicate, shared by the three sites that ask it:
// SolicitWork's LLM-193 affordability auto-decline, AcceptWork's courtesy
// gate 8, and settleCompletedLabor's authoritative completion re-check.
// Centralized so the cue, the gates, and the settle can never disagree on
// what "can cover" means. The ACTION on false stays per-site (auto-decline /
// terminal flip / unpaid settle), mirroring the buyerCanAfford posture.
func employerCanCoverLaborReward(employer *Actor, offer *LaborOffer) bool {
	return buyerCanAfford(employer, offer.Reward) && buyerHoldsPayItems(employer, offer.RewardItems)
}

// employerCanHireInKind reports whether the employer holds any tradeable goods
// it could offer as an in-kind wage — the "could this employer hire AT ALL"
// predicate (LLM-243), distinct from employerCanCoverLaborReward's "can it cover
// THIS ask". Any carried ItemKind with a positive quantity counts: the worker
// names the reward goods and the employer either holds them or not, so this
// gates only on whether goods exist to pay WITH, never on whether these
// particular goods match a given ask (that is the per-offer coverage check).
// The sim-side mirror of perception.holdsBarterableGoods (LLM-222), kept in step
// so the hiring gate and the buy-side means-to-pay cue agree on what "has goods
// to trade" means. World-goroutine-only.
func employerCanHireInKind(employer *Actor) bool {
	for _, qty := range employer.Inventory {
		if qty > 0 {
			return true
		}
	}
	return false
}

// workerHasLiveJob reports whether the worker currently holds a committed labor
// offer — EnRoute (accepted, relocating to the workplace) or Working (accepted,
// not-yet-settled) — the authoritative "this worker is occupied" signal.
// Ledger-based, NOT the actor's LaboringUntil mirror: the mirror is unset while
// EnRoute and can read "free" in the gap between a work window elapsing and the
// sweep settling it. Reading it for the busy-gate (SolicitWork, AcceptWork)
// keeps a second job from slipping in during relocation or sweep lag, which
// would break the one-live-job-per-worker invariant and orphan the first job's
// mirror (code_review). EnRoute counts because the worker is already committed —
// they're on their way to the post; taking a second job mid-walk would strand
// the first (LLM-229). World-goroutine-only.
func workerHasLiveJob(w *World, workerID ActorID) bool {
	for _, o := range w.LaborLedger {
		if o != nil && o.WorkerID == workerID &&
			(o.State == LaborStateWorking || o.State == LaborStateEnRoute) {
			return true
		}
	}
	return false
}

// actorAtWorkpost reports whether actor a is physically at workStructureID's
// work post — the location gate for "help is happening here" (LLM-229). For a
// building with an interior, that means standing INSIDE it (InsideStructureID);
// for a doorless market stall (no interior to enter), it means standing at the
// stall's staff/loiter pin, the same place its keeper works from. Shared by the
// accept-time immediate-start decision, the arrival subscriber's start gate, and
// the produce boost's helper count, so all three agree on "at the post." An
// empty workStructureID (a workless employer) is never a post.
// World-goroutine-only.
func actorAtWorkpost(w *World, a *Actor, workStructureID StructureID) bool {
	if a == nil || workStructureID == "" {
		return false
	}
	if a.InsideStructureID == workStructureID {
		return true
	}
	// A doorless structure has no interior to be inside — its staff position IS
	// the loiter pin (the keeper works the stall from there). structureEntryTile
	// ok=false marks doorless; only then does loiter proximity count as at-post,
	// so an interior shop still requires actually being inside (a worker
	// loitering outside the door is NOT at the post).
	if _, hasDoor := structureEntryTile(w, workStructureID); hasDoor {
		return false
	}
	pin, ok := effectiveLoiterTile(w, workStructureID)
	if !ok {
		return false
	}
	return a.Pos.Chebyshev(pin) <= LoiterAttributionTiles
}

// startLaborWork begins a hired worker's work window: the offer flips to
// Working, the clock starts NOW (WorkStartedAt), and the worker enters
// StateLaboring until the clamped WorkingUntil. Called at accept time for a job
// that starts in place (an on-site hire, or an employer with no work structure)
// and from the arrival subscriber when a relocated worker reaches the post with
// the owner present (LLM-229). No coins move — the reward settles at completion
// (labor_settle.go). timePtrLabor copies per call so offer.WorkingUntil and
// worker.LaboringUntil never alias one instant. Caller guarantees the offer is
// non-terminal (Pending from AcceptWork, or EnRoute from the arrival subscriber)
// and holds live worker/employer refs. World-goroutine-only.
func startLaborWork(w *World, offer *LaborOffer, worker, employer *Actor, at time.Time) {
	// LLM-190: a worker can't keep working past the employer's closing time.
	// Clamp the completion deadline to the end of the employer's current shift,
	// measured from the real work start — so a relocated worker who arrives late
	// in the day gets a window that ends at close, not start + DurationMin. The
	// full agreed reward is still paid for the shortened window, and the keeper
	// announces the close-out when the clamped job completes off-shift
	// (settleCompletedLabor).
	workingUntil := clampWorkingUntilToEmployerClose(w, employer, at.Add(time.Duration(offer.DurationMin)*time.Minute), at)
	offer.State = LaborStateWorking
	offer.WorkStartedAt = timePtrLabor(at)
	offer.WorkingUntil = timePtrLabor(workingUntil)
	offer.EnRouteWaiting = false
	// StateLaboring is ALWAYS paired with a non-zero LaborID + live window
	// (WORK-410 orphan lesson); the completion sweep clears them together.
	// LaborID is the authoritative ownership key the settle path guards on.
	worker.LaborID = offer.ID
	worker.LaboringUntil = timePtrLabor(workingUntil)
	worker.State = StateLaboring
	// LLM-271: the worker is now on-post. If the workplace is a business already
	// worn to the repair threshold, wake them once to mend it — the wear crossed
	// before the hire, so there is no owner-style accrual edge to ride, and a
	// StateLaboring worker is otherwise shelved by the reactor.
	maybeStampHiredRepairWarrant(w, worker, employer, at)
}

// sendWorkerToWorkplace walks a hired worker to the employer's workplace to
// start (or wait for) their job (LLM-229). With the owner present at the post,
// the worker goes to the post itself — inside for a building (MoveToStructure
// derives the enter; the workerHiredAt leg of structureMembershipAllows admits
// them even to an owner-only shop) or to the staff pin for a doorless stall.
// With the owner ABSENT, the worker walks only to the loiter pin and waits there
// — a worker never enters an establishment ahead of its owner (spec); the
// arrival subscriber sends them in once the owner shows. leaveHuddle ends any
// conversation the worker is in — true at accept (the deal was just struck in a
// huddle), false from the arrival subscriber (the worker is already solo). A
// TerminalNoOpError (already there / already walking there) is expected and
// silent; any other walk-start failure is logged and leaves the offer EnRoute
// for the bounded-wait backstop to void. World-goroutine-only.
func sendWorkerToWorkplace(w *World, worker, employer *Actor, leaveHuddle bool, at time.Time) {
	ws := employer.WorkStructureID
	if ws == "" {
		return // no post to walk to (a workless employer starts work in place)
	}
	var err error
	if actorAtWorkpost(w, employer, ws) {
		_, err = MoveToStructure(worker.ID, ws, at).Fn(w)
	} else {
		_, err = MoveActor(worker.ID, NewStructureVisitDestination(ws), leaveHuddle, at).Fn(w)
	}
	if err == nil {
		return
	}
	if _, isNoOp := err.(TerminalNoOpError); isNoOp {
		return
	}
	log.Printf("sim/labor: LLM-229 relocate walk for worker %s to %q failed: %v (offer stays en_route; bounded-wait backstop voids it if work never starts)",
		worker.ID, ws, err)
}

// workerHiredAt reports whether workerID may enter structureID by virtue of a
// live labor job there (LLM-229) — the labor leg of structureMembershipAllows,
// admitting a hired worker to the employer's workplace (even an owner-only one)
// the same way the permanent Staff leg admits a regular employee. The grant is
// state-scoped to hold the "never enter ahead of the owner" invariant:
//
//   - Working — full staff-for-the-window grant (the worker is already at the
//     post working; the owner was present when it started).
//   - EnRoute — admitted ONLY while the owner is at the post. A relocating
//     worker must not be able to walk into an owner-only establishment before
//     its owner; the arrival subscriber sends them in exactly when the owner is
//     present, and this gate is what MoveActor re-validates on the enter path.
//
// The grant lives only as long as the offer does: once the job settles or the
// sweep voids it, the worker is no longer a member. World-goroutine-only.
func workerHiredAt(w *World, workerID ActorID, structureID StructureID) bool {
	if workerID == "" || structureID == "" {
		return false
	}
	for _, o := range w.LaborLedger {
		if o == nil || o.WorkerID != workerID {
			continue
		}
		employer := w.Actors[o.EmployerID]
		if employer == nil || employer.WorkStructureID != structureID {
			continue
		}
		switch o.State {
		case LaborStateWorking:
			return true
		case LaborStateEnRoute:
			if actorAtWorkpost(w, employer, structureID) {
				return true
			}
		}
	}
	return false
}

// workerPendingLaborOffer returns the worker's live (not past-TTL) pending
// outgoing labor offer, or nil. Shared by SolicitWork's duplicate-offer gate
// and the seek-work backstop's eligibility (LLM-141) so the "already bidding"
// predicate can't drift between them. Past-TTL pending entries are skipped —
// they resolve on the labor sweep, not here. World-goroutine-only.
func workerPendingLaborOffer(w *World, workerID ActorID, now time.Time) *LaborOffer {
	for _, o := range w.LaborLedger {
		if o == nil || o.State != LaborStatePending || o.WorkerID != workerID {
			continue
		}
		if !o.ExpiresAt.IsZero() && !now.Before(o.ExpiresAt) {
			continue
		}
		return o
	}
	return nil
}

// activeLaborBetween returns a live (Pending, EnRoute, or Working) labor offer
// standing between the two actors in EITHER direction, or nil. Used by sim.Pay
// to keep labor compensation out of the bare-pay channel (LLM-202): a labor
// offer's reward settles at completion through the labor sweep, so a separate
// bare pay between the same pair double-compensates the one job (the live John
// Ellis / Silence Walker case — 8 coins paid by hand AND a 2-coin labor contract
// booked on top). Either direction because the worker who solicits and the
// employer who accepts can each be the one who then mistakenly reaches for pay.
// A pending offer past its TTL is dead (the aging sweep just hasn't flipped it
// yet) and is skipped, mirroring workerPendingLaborOffer; EnRoute and Working
// offers have no such TTL — an EnRoute worker is committed (relocating) and a
// Working one settles at WorkingUntil, so both block a bare pay (LLM-229). When
// more than one live offer stands between the pair (e.g. each is the other's
// worker in opposite-direction deals), the pick is deterministic — the more
// pressing state first (Working, then EnRoute, then Pending), then lowest
// LaborID — so the steer's message branch and named reward never ride map-
// iteration order (code_review). World-goroutine-only.
func activeLaborBetween(w *World, partyA, partyB ActorID, now time.Time) *LaborOffer {
	var ids []LaborID
	for id, o := range w.LaborLedger {
		if o == nil {
			continue
		}
		if o.State != LaborStatePending && o.State != LaborStateEnRoute && o.State != LaborStateWorking {
			continue
		}
		matched := (o.WorkerID == partyA && o.EmployerID == partyB) ||
			(o.WorkerID == partyB && o.EmployerID == partyA)
		if !matched {
			continue
		}
		if o.State == LaborStatePending && !o.ExpiresAt.IsZero() && !now.Before(o.ExpiresAt) {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := w.LaborLedger[ids[i]], w.LaborLedger[ids[j]]
		if ra, rb := laborBetweenRank(a.State), laborBetweenRank(b.State); ra != rb {
			return ra < rb
		}
		return ids[i] < ids[j]
	})
	return w.LaborLedger[ids[0]]
}

// laborBetweenRank orders the live labor states by how pressing a "don't pay
// this pair separately" signal each is, for activeLaborBetween's deterministic
// pick: an in-progress job (Working) outranks a committed-but-not-started one
// (EnRoute), which outranks a not-yet-accepted offer (Pending). A total order,
// so sort.Slice's less function stays a valid strict weak ordering across all
// three states (a bare "!= Pending" would tie Working against EnRoute).
func laborBetweenRank(s LaborLedgerState) int {
	switch s {
	case LaborStateWorking:
		return 0
	case LaborStateEnRoute:
		return 1
	default: // LaborStatePending
		return 2
	}
}

// laborTradeSteerMsg redirects an NPC that reaches for the goods-trade tools
// (offer_trade / pay_with_item / sell / scene_quote) to transact labor — naming
// "work"/"labor" as an item kind — toward the first-class labor verbs. Labor is
// NOT a tradeable item: it has its own worker-initiated flow, so naming it as a
// good either dead-ends on "unknown item kind" (buy side) or mints a phantom
// inert kind into the catalog and then dead-ends on a holdings/stock shortfall
// (pay_items / quote side) — in both cases with no hint the labor flow exists.
// That is the LLM-167 symptom (Ezekiel Crane burned ~20 trade-tool turns before
// stumbling onto accept_work). The copy names ONLY the real verbs: the labor
// market is worker-initiated, so the worker solicits and the employer
// accepts/declines — there is no employer-initiated "offer_work". LLM-167.
const laborTradeSteerMsg = "labor isn't a tradeable good. To offer to do a job for someone in exchange for coins, use solicit_work. If someone has offered to work for you, respond with accept_work or decline_work."

// isLaborToken reports whether an item-kind argument is really the labor/work
// concept rather than a good. A closed allow-list — none of these tokens is an
// authored item kind (verified against the catalog), so a match unambiguously
// means the model is conflating the labor market with item trade. Normalized
// trim + lower + leading-article tolerant, mirroring resolveItemKind, so
// "a job" / "the work" / "Labour" all match. LLM-167.
func isLaborToken(name string) bool {
	switch stripLeadingArticle(strings.TrimSpace(strings.ToLower(name))) {
	case "work", "labor", "labour", "job", "jobs":
		return true
	}
	return false
}
