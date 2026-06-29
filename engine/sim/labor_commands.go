package sim

import (
	"errors"
	"fmt"
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

// LaborSolicitResult is SolicitWork's success value — the minted pending
// offer's id + state, plus the resolved employer display name so the tool
// feedback can name who the offer went to.
type LaborSolicitResult struct {
	ID           LaborID
	State        LaborLedgerState
	EmployerName string
}

// LaborAcceptResult is AcceptWork's value. On a gate-driven terminal flip
// (expired / failed) State carries that terminal and WorkingUntil is zero;
// on a real accept State is Working and WorkingUntil is the completion
// deadline.
type LaborAcceptResult struct {
	ID           LaborID
	State        LaborLedgerState
	WorkerName   string
	Reward       int
	WorkingUntil time.Time
}

// LaborDeclineResult is DeclineWork's value.
type LaborDeclineResult struct {
	ID    LaborID
	State LaborLedgerState
}

// SolicitWork returns the Command for a worker offering their labor to a
// co-present employer. Pending offer only — the employer resolves it with
// accept_work / decline_work. Gates first-failure-wins: numeric bounds →
// worker exists → worker attribute → not-walking → in-conversation →
// not-already-laboring → scene anchor → employer resolve → not-self → no
// duplicate pending offer to the same employer.
func SolicitWork(workerID ActorID, employerName string, reward int, durationMin int, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Numeric defense. SolicitWork is exported — non-handler callers
			// could pass shapes the decode side rejects.
			if reward < MinLaborReward {
				return nil, fmt.Errorf("solicit_work: reward must be at least %d coin (got %d)", MinLaborReward, reward)
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

			// Mint the pending offer.
			id := w.nextLaborSeq()
			expiresAt := at.Add(LaborLedgerTTLDefault)
			offer := &LaborOffer{
				ID:          id,
				WorkerID:    workerID,
				EmployerID:  employerID,
				Reward:      reward,
				DurationMin: durationMin,
				State:       LaborStatePending,
				HuddleID:    worker.CurrentHuddleID,
				SceneID:     sceneID,
				CreatedAt:   at,
				ExpiresAt:   expiresAt,
			}
			w.LaborLedger[id] = offer

			evt := &LaborOfferReceived{
				LaborID:     id,
				WorkerID:    workerID,
				EmployerID:  employerID,
				Reward:      reward,
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
// On all-pass: the offer flips to Working with a WorkingUntil window and the
// worker enters StateLaboring with the mirrored LaboringUntil. No coins move
// here — the employer→worker reward transfer settles atomically when the
// completion sweep (labor_settle.go) fires, after the worker has put in the
// time.
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
				return finalizeLaborTerminal(w, offer, LaborTerminalStateExpired, at), nil
			}

			// Gate 6: co-presence. Worker and employer must both still be in
			// offer.HuddleID (captured at solicitation).
			worker, workerOK := w.Actors[offer.WorkerID]
			if !workerOK ||
				offer.HuddleID == "" ||
				worker.CurrentHuddleID != offer.HuddleID ||
				caller.CurrentHuddleID != offer.HuddleID {
				return finalizeLaborTerminal(w, offer, LaborTerminalStateFailedUnavailable, at), nil
			}

			// Gate 7: worker free. Ledger-authoritative — a worker with ANY
			// Working offer is occupied until it settles, even if its window has
			// elapsed but the sweep hasn't run yet. Reading the actor mirror's
			// timestamp here would let a second job slip in during sweep lag and
			// orphan the first job's mirror (code_review).
			if workerHasLiveJob(w, offer.WorkerID) {
				return finalizeLaborTerminal(w, offer, LaborTerminalStateFailedUnavailable, at), nil
			}

			// Gate 8: funds (courtesy check, NOT authoritative). No coins move
			// at accept — the employer→worker transfer settles at completion.
			// But taking on a job the employer plainly can't pay for right now
			// is a bad deal, so fail it here rather than let the worker labor
			// (possibly for hours) toward a payout that was never going to
			// land. The completion sweep re-checks funds authoritatively, since
			// the employer's balance can drift across a long work window.
			if !buyerCanAfford(caller, offer.Reward) {
				return finalizeLaborTerminal(w, offer, LaborTerminalStateFailedUnavailable, at), nil
			}

			// All gates pass — flip + mirror the worker's state. No coins move
			// here: the employer→worker reward transfer settles atomically at
			// completion (labor_settle.go). timePtrLabor copies per call so
			// offer.WorkingUntil and worker.LaboringUntil don't alias one
			// instant.
			workingUntil := at.Add(time.Duration(offer.DurationMin) * time.Minute)
			offer.State = LaborStateWorking
			offer.AcceptedAt = timePtrLabor(at)
			offer.WorkingUntil = timePtrLabor(workingUntil)
			// StateLaboring is ALWAYS paired with a non-zero LaborID + live
			// window (WORK-410 orphan lesson); the completion sweep clears them
			// together. LaborID is the authoritative ownership key the settle
			// path guards on (not the window timestamp).
			worker.LaborID = offer.ID
			worker.LaboringUntil = timePtrLabor(workingUntil)
			worker.State = StateLaboring

			// A struck deal is conversational activity — keep the huddle's
			// silence sweep from concluding it mid-arrangement. It is also
			// non-conversational PROGRESS (LLM-159), so it spares the huddle from
			// the loop sweep too: touchHuddleProgress stamps both clocks.
			touchHuddleProgress(w, offer.HuddleID, at)

			evt := &LaborOfferAccepted{
				LaborID:      offer.ID,
				WorkerID:     offer.WorkerID,
				EmployerID:   offer.EmployerID,
				Reward:       offer.Reward,
				DurationMin:  offer.DurationMin,
				WorkingUntil: workingUntil,
				SceneID:      offer.SceneID,
				HuddleID:     offer.HuddleID,
				At:           at,
			}
			w.emit(evt)

			return LaborAcceptResult{
				ID:           offer.ID,
				State:        LaborStateWorking,
				WorkerName:   worker.DisplayName,
				Reward:       offer.Reward,
				WorkingUntil: workingUntil,
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
			state := finalizeLaborTerminal(w, offer, LaborTerminalStateDeclined, at)
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
func finalizeLaborTerminal(w *World, offer *LaborOffer, terminal LaborTerminalState, at time.Time) LaborLedgerState {
	offer.State = LaborLedgerState(terminal)
	offer.ResolvedAt = timePtrLabor(at)

	evt := &LaborResolved{
		LaborID:       offer.ID,
		WorkerID:      offer.WorkerID,
		EmployerID:    offer.EmployerID,
		Reward:        offer.Reward,
		DurationMin:   offer.DurationMin,
		TerminalState: terminal,
		SceneID:       offer.SceneID,
		HuddleID:      offer.HuddleID,
		At:            at,
	}
	w.emit(evt)
	return offer.State
}

// timePtrLabor returns a pointer to a COPY of t. Because the argument is
// passed by value, each call yields a distinct *time.Time — so the same
// instant can be handed to multiple fields (offer.WorkingUntil and
// worker.LaboringUntil) without aliasing them through one pointer.
func timePtrLabor(t time.Time) *time.Time { return &t }

// workerHasLiveJob reports whether the worker currently holds a Working
// (accepted, not-yet-settled) labor offer — the authoritative "this worker is
// occupied" signal. Ledger-based, NOT the actor's LaboringUntil mirror: the
// mirror's timestamp can read "free" in the gap between a work window elapsing
// and the sweep settling it, while the offer is still Working. Reading it for
// the busy-gate (SolicitWork, AcceptWork) keeps a second job from slipping in
// during sweep lag, which would break the one-live-job-per-worker invariant
// and orphan the first job's mirror (code_review). World-goroutine-only.
func workerHasLiveJob(w *World, workerID ActorID) bool {
	for _, o := range w.LaborLedger {
		if o != nil && o.State == LaborStateWorking && o.WorkerID == workerID {
			return true
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
