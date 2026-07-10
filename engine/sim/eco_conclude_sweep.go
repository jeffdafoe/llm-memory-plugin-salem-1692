package sim

import (
	"context"
	"log"
	"sort"
	"time"
)

// eco_conclude_sweep.go — LLM-334. Bounded conversation arcs while unwatched.
//
// Eco mode (LLM-313) paces social warrant cycles to EcoSocialGap while no
// player is present — but pacing bounds a conversation's RATE, not its COUNT:
// npc_spoke re-stamps on every reply, so an NPC-NPC conversation's total LLM
// spend is turn-bound and the delay just meters the same beats over more
// wall-clock. Live 2026-07-08 (eco engaged, unwatched): three conversation
// marathons — 110 spokes/54 min, 86/143 min, 40/87 min — were 75% of all NPC
// speech in a 3.5h window, each ending only when a schedule event happened to
// pull a member away. The loop sweep can't help: a varied, healthy
// conversation is invisible to it by design.
//
// This sweep gives every unwatched conversation a bounded ARC instead: once a
// huddle has been continuously unwatched (and commerce-free) for
// EcoConversationMax, it is concluded SILENTLY via the same path the loop
// sweep uses, and the members' social-only pending warrant cycles are cleared
// (clearSocialWarrantCycles, LLM-333) so the conclusion sticks — no leftover
// npc_spoke beat ticks once more and re-forms the scene. The scene still
// HAPPENS (an arc of EcoConversationMax at the eco cadence ≈ 3-6 beats —
// greetings exchanged, memories formed); it just can't run forever with
// nobody watching. When a player returns, AudienceActive flips on their first
// /pc/me poll, the sweep stops stamping/concluding, and every arc stamp is
// cleared — already-concluded conversations are not resurrected, the actors
// are simply quiet and respond to greetings like any other quiet moment.
//
// Commerce guard: a huddle carrying a live deal — a pending/countered
// pay-ledger entry or a non-terminal labor offer (pending/en_route/working)
// stamped with its HuddleID, or a member holding a commerce-commitment warrant
// (isCommerceCommitmentWarrantKind) — is never concluded; its arc stamp is
// pushed to now, so the clock effectively starts when the deal settles.
// Commerce was always exempt from eco (a counterparty is blocked); this keeps
// that contract.
//
// Cadence + lifecycle mirror the loop sweep's coalesced AfterFunc self-rearm
// chain:
//
//	RunEcoConcludeSweep(ctx, w)
//	└─> kickEcoConcludeSweep
//	     └─> armNextEcoConcludeSweep
//	          └─> [cadence] fireScheduledEcoConcludeSweep
//	               └─> SendContext(evaluateEcoConcludeAndRearm(now))
//	                    └─> Fn: clear flag, run scan, re-arm

// ecoConcludeSweepCadence is how often the sweep scans World.Huddles. Fixed
// (not a knob): 30s is well inside the minutes-scale arc, and the scan is a
// cheap map walk — the arc length (eco_conversation_max_seconds) is the only
// tunable this feature needs.
const ecoConcludeSweepCadence = 30 * time.Second

// ecoConcludeSweepEnabled reports whether the sweep can ever act: eco mode's
// master switch is on AND the arc is positive (0 disables the sweep — the
// pre-LLM-334 meter-forever behavior).
func ecoConcludeSweepEnabled(s WorldSettings) bool {
	return s.EcoEnabled && effectiveEcoConversationMax(s) > 0
}

// RunEcoConcludeSweep owns the sweep's periodic schedule. Caller starts this in
// a goroutine alongside World.Run (next to RunHuddleLoopSweep); returns when
// ctx is cancelled.
func RunEcoConcludeSweep(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, kickEcoConcludeSweep())
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/eco_conclude: initial sweep arm failed: %v", err)
	}
	<-ctx.Done()
}

// kickEcoConcludeSweep returns a Command whose Fn arms the first sweep on the
// world goroutine — mirrors kickHuddleLoopSweep.
func kickEcoConcludeSweep() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			armNextEcoConcludeSweep(w)
			return nil, nil
		},
	}
}

// armNextEcoConcludeSweep schedules the next sweep after one cadence interval.
// MUST be called from inside a Command.Fn — touches w.ecoConcludeSweep.scheduled
// without coordination. Coalescing: no-op when a sweep is already scheduled.
func armNextEcoConcludeSweep(w *World) {
	if w.ecoConcludeSweep.scheduled {
		return
	}
	w.ecoConcludeSweep.scheduled = true
	time.AfterFunc(ecoConcludeSweepCadence, func() { fireScheduledEcoConcludeSweep(w) })
}

// fireScheduledEcoConcludeSweep is the AfterFunc callback body. Uses
// LifecycleContext so a shutdown-while-armed unblocks SendContext instead of
// deadlocking on a send to a dead channel (matches the sibling sweeps).
func fireScheduledEcoConcludeSweep(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		return
	}
	w.beatTicker("eco_conclude_sweep")
	_, err := w.SendContext(ctx, evaluateEcoConcludeAndRearm(time.Now()))
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/eco_conclude: scheduled sweep failed: %v", err)
	}
}

// evaluateEcoConcludeAndRearm clears the scheduled flag, runs one sweep, and
// re-arms — all in one Fn on the world goroutine.
func evaluateEcoConcludeAndRearm(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.ecoConcludeSweep.scheduled = false
			res, err := EvaluateEcoConcludeSweep(now).Fn(w)
			armNextEcoConcludeSweep(w)
			return res, err
		},
	}
}

// EvaluateEcoConcludeSweep returns a Command that runs one arc scan. Exposed as
// a Command (not just an internal Fn) so tests can drive sweeps
// deterministically without the AfterFunc timing chain.
//
// Disengaged (audience present, eco off, or arc 0): every EcoUnwatchedSince is
// cleared so a later re-engage starts fresh arcs — a stamp left over from a
// prior unwatched stretch must not conclude a conversation the moment the
// player leaves again.
//
// Engaged, per active huddle: a player-attended huddle (defensive — a PC line
// within huddlePCAttentionWindow can outlive the presence stamp by ~2 min) and
// a commerce-carrying huddle get their stamp reset/pushed; anything else is
// stamped on first sight and concluded once the stamp is older than the arc.
//
// Collect-then-conclude with sorted iteration, matching the sibling sweeps.
func EvaluateEcoConcludeSweep(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(w.Huddles) == 0 {
				return nil, nil
			}
			if !ecoConcludeSweepEnabled(w.Settings) || AudienceActive(w, now) {
				for _, h := range w.Huddles {
					if h != nil {
						h.EcoUnwatchedSince = nil
					}
				}
				return nil, nil
			}
			arc := effectiveEcoConversationMax(w.Settings)
			commerceHuddles := ledgerCommerceHuddles(w)
			var due []HuddleID
			for id, h := range w.Huddles {
				if h == nil || h.ConcludedAt != nil {
					continue
				}
				if huddlePCAttended(h, now) {
					h.EcoUnwatchedSince = nil
					continue
				}
				_, ledgerLive := commerceHuddles[id]
				if ledgerLive || huddleMemberHoldsCommerceWarrant(w, h) {
					// The arc restarts when the deal settles — a conversation
					// that just closed a sale earns a fresh (short) scene for
					// the handover beats before the sweep ends it.
					t := now
					h.EcoUnwatchedSince = &t
					continue
				}
				if h.EcoUnwatchedSince == nil {
					t := now
					h.EcoUnwatchedSince = &t
					continue
				}
				if now.Sub(*h.EcoUnwatchedSince) >= arc {
					due = append(due, id)
				}
			}
			if len(due) == 0 {
				return nil, nil
			}
			sort.Slice(due, func(i, j int) bool { return due[i] < due[j] })
			for _, id := range due {
				h, ok := w.Huddles[id]
				if !ok || h == nil || h.ConcludedAt != nil {
					continue
				}
				// Telemetry + member set BEFORE conclude — concludeHuddleInner
				// clears Members. Reason "eco_conclude" keeps arc conclusions
				// separable from the loop sweep's livelock kills in the ring.
				emitHuddleLoopTelemetry(w, h, now, "eco_conclude")
				members := make([]ActorID, 0, len(h.Members))
				for memberID := range h.Members {
					members = append(members, memberID)
				}
				structureID := h.StructureID
				concludeHuddleInner(w, id, now, false)
				// Same post-conclude posture as the loop sweep: keep the
				// carried ring (a re-form doesn't re-greet) but reset the
				// carried loop clock + latched reason — the arc conclude is not
				// a livelock verdict, and a re-form earns fresh gates.
				if cb := w.carryoverByStructure[structureID]; cb != nil {
					cb.loopingSince = nil
					cb.loopingReason = ""
				}
				// Make it stick (LLM-333): leftover social-only cycles are the
				// dead conversation's own beats — clearing them is what turns
				// "concluded" into "quiet". Mixed cycles are kept whole.
				clearSocialWarrantCycles(w, members)
			}
			return nil, nil
		},
	}
}

// ledgerCommerceHuddles returns the set of huddles carrying a LIVE commitment
// negotiation — a pay-ledger entry that is pending or countered, or a labor
// offer in a non-terminal state (pending / en_route / working) — each stamped
// with the huddle's ID. One O(ledger) pass per ledger, shared by the whole
// scan, mirroring ledgerStandoffHuddles' posture. Terminal states do not hold a
// conversation open: a settled sale's handover beats are covered by the
// member-warrant check (serve_handover, paid, pay_resolved), and a settled hire
// is a terminal labor state (completed/declined/expired/failed_unavailable)
// whose scene is already done.
//
// Labor parity (LLM-348): a hire negotiated over several beats consumes its
// LaborOffer warrant on the first tick, so by the time the arc elapses the
// remaining beats are social-only and huddleMemberHoldsCommerceWarrant no
// longer fires. The live ledger entry is what keeps the scene from being cut
// mid-hire — exactly as a pending pay entry does for a sale.
func ledgerCommerceHuddles(w *World) map[HuddleID]struct{} {
	var out map[HuddleID]struct{}
	for _, e := range w.PayLedger {
		if e == nil || e.HuddleID == "" {
			continue
		}
		if e.State != PayLedgerStatePending && e.State != PayLedgerStateCountered {
			continue
		}
		if out == nil {
			out = make(map[HuddleID]struct{})
		}
		out[e.HuddleID] = struct{}{}
	}
	for _, o := range w.LaborLedger {
		if o == nil || o.HuddleID == "" {
			continue
		}
		if o.State != LaborStatePending && o.State != LaborStateEnRoute && o.State != LaborStateWorking {
			continue
		}
		if out == nil {
			out = make(map[HuddleID]struct{})
		}
		out[o.HuddleID] = struct{}{}
	}
	return out
}

// huddleMemberHoldsCommerceWarrant reports whether any member's pending warrant
// cycle contains a commerce-commitment kind belonging to THIS conversation — a
// counterparty is blocked on that member's answer, so the conversation is
// commerce-carrying even if the ledger entry has already resolved
// (handover/thanks beats ride paid/pay_resolved/serve_handover warrants).
//
// Scoped, not blanket (code_review): a commerce warrant stamped with a
// DIFFERENT HuddleID belongs to another conversation and must not hold this
// one open — otherwise an actor carrying a stale pay_offer from elsewhere
// would commerce-protect every huddle it joins and the arc would never run. A
// warrant with huddle identity counts only on an exact match; a warrant
// WITHOUT one (not every commerce mint site stamps meta.HuddleID) counts only
// when its counterparty (TriggerActorID / SourceActorID) is another member of
// this huddle — the deal is between people in this room. MUST run on the
// world goroutine.
func huddleMemberHoldsCommerceWarrant(w *World, h *Huddle) bool {
	for memberID := range h.Members {
		a := w.Actors[memberID]
		if a == nil {
			continue
		}
		for _, m := range a.Warrants {
			if !isCommerceCommitmentWarrantKind(m.Kind()) {
				continue
			}
			if m.HuddleID == h.ID {
				return true
			}
			if m.HuddleID != "" {
				continue // scoped to another conversation
			}
			if counterpartyInHuddle(h, memberID, m.TriggerActorID) ||
				counterpartyInHuddle(h, memberID, m.SourceActorID) {
				return true
			}
		}
	}
	return false
}

// counterpartyInHuddle reports whether id names a huddle member OTHER than the
// warrant holder — the "the deal's other side is in this room" test for
// commerce warrants that carry no huddle identity.
func counterpartyInHuddle(h *Huddle, holder, id ActorID) bool {
	if id == "" || id == holder {
		return false
	}
	_, ok := h.Members[id]
	return ok
}
