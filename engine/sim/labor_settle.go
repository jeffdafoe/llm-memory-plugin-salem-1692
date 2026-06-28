package sim

import (
	"context"
	"log"
	"math"
	"sort"
	"time"
)

// labor_settle.go — LLM-26 background lifecycle for LaborOffers. One sweep,
// two jobs:
//
//  1. Expire PENDING offers past ExpiresAt (the employer never answered).
//  2. Complete WORKING offers past WorkingUntil — transfer the reward from
//     the employer to the worker, clear the worker's StateLaboring/
//     LaboringUntil mirror, and finalize Completed. If the employer can no
//     longer cover the reward, resolve FailedUnavailable instead (the
//     finished work goes unpaid).
//
// then reap terminal offers past the retention window. This is the labor
// analog of pay_ledger_sweep.go and shares its exact coalesced AfterFunc
// self-rearm scheduling; the only difference is the Working→Completed
// settlement, which the pay machine has no equivalent of (pay settles
// atomically at accept, labor at completion).
//
// AcceptWork also drives an in-band Expired flip when a pending offer is
// accepted past its TTL (gate 5). The sweep is the backstop for offers
// nobody answers AND the sole driver of completion for accepted work —
// without it an accepted job would never pay out.

// effectiveLaborLedgerSweepCadence returns the labor sweep cadence. No
// WorldSettings override is plumbed in the MVP, so this is the constant.
func effectiveLaborLedgerSweepCadence() time.Duration {
	return LaborLedgerSweepCadenceDefault
}

// RunLaborLedgerSweep owns the labor-ledger sweep's periodic schedule.
// Caller starts this in a goroutine alongside World.Run (next to
// RunPayLedgerSweep); returns when ctx is cancelled. The first sweep is
// kicked immediately so an offer already past its window at startup doesn't
// wait a full cadence interval.
func RunLaborLedgerSweep(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, kickLaborLedgerSweep())
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/labor: initial sweep arm failed: %v", err)
	}
	<-ctx.Done()
}

// kickLaborLedgerSweep returns a Command whose Fn arms the first sweep on
// the world goroutine — mirrors kickPayLedgerSweep.
func kickLaborLedgerSweep() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			armNextLaborLedgerSweep(w)
			return nil, nil
		},
	}
}

// armNextLaborLedgerSweep schedules the next sweep after one cadence
// interval. MUST be called from inside a Command.Fn — touches
// w.laborLedgerSweep.scheduled without coordination. Coalescing: no-op when
// a sweep is already scheduled.
func armNextLaborLedgerSweep(w *World) {
	if w.laborLedgerSweep.scheduled {
		return
	}
	w.laborLedgerSweep.scheduled = true
	cadence := effectiveLaborLedgerSweepCadence()
	time.AfterFunc(cadence, func() { fireScheduledLaborLedgerSweep(w) })
}

// fireScheduledLaborLedgerSweep is the AfterFunc callback body. Uses
// LifecycleContext so a shutdown-while-armed unblocks SendContext instead of
// deadlocking on a send to a dead cmds channel. Mirrors
// fireScheduledPayLedgerSweep.
func fireScheduledLaborLedgerSweep(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		return
	}
	w.beatTicker("labor_ledger_sweep")
	_, err := w.SendContext(ctx, evaluateLaborLedgerAndRearm(time.Now()))
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/labor: scheduled sweep failed: %v", err)
	}
}

// evaluateLaborLedgerAndRearm clears the scheduled flag, runs one sweep, and
// re-arms — all in one Fn on the world goroutine. Clearing the flag first
// means the re-arm starts a fresh chain rather than no-opping.
func evaluateLaborLedgerAndRearm(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.laborLedgerSweep.scheduled = false
			res, err := EvaluateLaborLedgerSweep(now).Fn(w)
			armNextLaborLedgerSweep(w)
			return res, err
		},
	}
}

// EvaluateLaborLedgerSweep returns a Command that, in one pass: flips every
// pending offer past ExpiresAt to Expired; settles every working offer past
// WorkingUntil to Completed (crediting the worker); then reaps terminal
// offers past the retention window. Exposed as a Command (not just an
// internal Fn) so tests can drive sweeps deterministically without the
// AfterFunc timing chain.
//
// Ids are collected before any mutation and processed in sorted order so
// LaborResolved events emit stably — w.emit dispatches subscribers
// synchronously, and a subscriber could in principle mutate the ledger map,
// so iterating it while mutating is unsafe even on the single world
// goroutine.
func EvaluateLaborLedgerSweep(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(w.LaborLedger) == 0 {
				return nil, nil
			}
			var expired, completed []LaborID
			for id, o := range w.LaborLedger {
				if o == nil {
					continue
				}
				switch o.State {
				case LaborStatePending:
					if o.ExpiresAt.IsZero() || now.Before(o.ExpiresAt) {
						continue
					}
					expired = append(expired, id)
				case LaborStateWorking:
					if o.WorkingUntil == nil || now.Before(*o.WorkingUntil) {
						continue
					}
					completed = append(completed, id)
				}
			}
			sort.Slice(expired, func(i, j int) bool { return expired[i] < expired[j] })
			sort.Slice(completed, func(i, j int) bool { return completed[i] < completed[j] })
			for _, id := range expired {
				o, ok := w.LaborLedger[id]
				if !ok || o == nil || o.State != LaborStatePending {
					continue
				}
				finalizeLaborTerminal(w, o, LaborTerminalStateExpired, now)
			}
			for _, id := range completed {
				o, ok := w.LaborLedger[id]
				if !ok || o == nil || o.State != LaborStateWorking {
					continue
				}
				settleCompletedLabor(w, o, now)
			}
			reapTerminalLaborOffers(w, now)
			return nil, nil
		},
	}
}

// settleCompletedLabor settles a finished work window: it frees the worker
// (clears StateLaboring/LaboringUntil), then transfers the reward from the
// employer to the worker and finalizes Completed. Caller guarantees
// offer.State == Working and now is at or past *offer.WorkingUntil.
//
// No coins were held during the window — the transfer happens HERE, at
// completion (settle-at-completion, not escrow-at-accept). So funds are
// re-checked authoritatively now: the employer's balance can have drifted
// across a long window. If the employer is gone, can no longer cover the
// reward, or paying would overflow the worker's purse, the deal falls
// through unpaid — terminal FailedUnavailable, no coins move. The worker
// already did the work; that is the risk of working for someone who turns
// out unable to pay. Either way the worker is freed first (the work IS
// finished regardless of whether payment lands).
func settleCompletedLabor(w *World, offer *LaborOffer, now time.Time) {
	worker := w.Actors[offer.WorkerID]
	employer := w.Actors[offer.EmployerID]

	// Free the worker regardless of the payment outcome — but ONLY if the
	// worker's ownership key still points at THIS offer. AcceptWork's ledger-
	// based busy-gate already makes two concurrent Working offers per worker
	// unreachable, so in practice this always matches; the id guard is
	// defense-in-depth so settling a stale offer can never free a worker who
	// has since taken a different job — a true ownership check, not a window-
	// timestamp proxy (code_review). StateLaboring is always paired with a
	// non-zero LaborID; with it cleared the actor returns to idle and the next
	// tick's observers re-derive conversing if still huddled.
	if worker != nil && worker.LaborID == offer.ID {
		worker.LaborID = 0
		worker.LaboringUntil = nil
		if worker.State == StateLaboring {
			worker.State = StateIdle
		}
	}

	canPay := worker != nil &&
		employer != nil &&
		buyerCanAfford(employer, offer.Reward) &&
		worker.Coins <= math.MaxInt-offer.Reward
	if !canPay {
		if employer == nil || worker == nil {
			log.Printf("sim/labor: completion of offer %d found worker/employer missing — resolving unpaid", offer.ID)
		}
		finalizeLaborTerminal(w, offer, LaborTerminalStateFailedUnavailable, now)
		return
	}

	// Atomic transfer: the employer pays the worker now.
	employer.Coins -= offer.Reward
	worker.Coins += offer.Reward
	finalizeLaborTerminal(w, offer, LaborTerminalStateCompleted, now)
}

// reconcileStrandedLaboringOnLoad frees an actor that was checkpointed mid-job
// (LLM-162). Actor.State IS persisted (sim_state), so a worker in StateLaboring
// at checkpoint reloads still laboring — but its backing LaborOffer does not:
// World.LaborLedger has no repo and is restart-lossy by design (labor_ledger.go),
// and LaborID/LaboringUntil are transient (never checkpointed). The only path
// that clears StateLaboring is the completion sweep, which settles off the
// ledger; with the ledger empty at load, a still-laboring actor would never be
// freed and would sit occupied forever. Because the ledger is ALWAYS empty when
// FinalizeLoad runs (it only ever fills from live solicit/accept commands during
// the run), any laboring actor at load is necessarily stranded — so the reset is
// unconditional. No coins are touched: the reward only ever moves at completion,
// never before, so there is no torn coin state to recover (the WORK-410 orphan in
// reverse the actor.go doc describes). Idempotent; world-goroutine-only (called
// from FinalizeLoad).
func reconcileStrandedLaboringOnLoad(a *Actor) {
	if a == nil || a.State != StateLaboring {
		return
	}
	a.State = StateIdle
	a.LaborID = 0
	a.LaboringUntil = nil
}

// reapTerminalLaborOffers removes terminal LaborOffers from World.LaborLedger
// once they are older than LaborLedgerTerminalRetentionDefault (measured from
// ResolvedAt). This bounds the offer-side map: terminal offers
// (completed / declined / expired / failed_unavailable) are otherwise never
// removed, and the map is their sole home (restart-lossy, no checkpoint, no
// sink). The active states (pending, working) are skipped; an offer with a
// nil ResolvedAt is skipped defensively (a terminal offer should always
// carry one). MUST be called from inside a Command.Fn (world goroutine).
func reapTerminalLaborOffers(w *World, now time.Time) {
	if w == nil || len(w.LaborLedger) == 0 {
		return
	}
	for id, o := range w.LaborLedger {
		if o == nil {
			delete(w.LaborLedger, id)
			continue
		}
		if o.State == LaborStatePending || o.State == LaborStateWorking {
			continue
		}
		if o.ResolvedAt == nil {
			continue
		}
		if now.Sub(*o.ResolvedAt) > LaborLedgerTerminalRetentionDefault {
			delete(w.LaborLedger, id)
		}
	}
}
