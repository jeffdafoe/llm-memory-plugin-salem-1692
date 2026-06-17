package sim

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"log"
	"time"
)

// shift_duty.go — tick-driver producer #2 (ZBBS-WORK-278): the shift/duty
// producer. A once-a-minute, level-triggered check that drives NPCs to and from
// their workplace on their shift boundaries.
//
// LEVEL-TRIGGERED (not edge-triggered): each scan re-evaluates the standing
// condition, so one mechanism gives BOTH the opening nudge and the keep-trying
// retry — the v2 form of v1 npc_scheduler's shouldNudgeReturnToWork /
// shouldNudgeReturnHome.
//
// AGENT/DECORATIVE SPLIT (ported from v1 npc_scheduler.go, which walks
// decoratives mechanically but only NUDGES agent NPCs and lets the LLM decide
// how to walk):
//   - Decorative NPCs are MECHANICALLY walked (MoveActor) — no warrant, no LLM.
//   - Agent NPCs get a WarrantKindShiftDuty warrant; they deliberate and
//     self-walk via the move_to tool (home). move_to is not yet wired into the
//     agent tool surface, so the agent half is INERT until it lands — but the
//     warrant is correct to stamp now (mirrors #1 shipping before take_break's
//     warrant path was exercised).
//
// UNSCHEDULED NPCs fall back to the world's dawn/dusk day window (decision B,
// 2026-05-22) so they're active by day and home/asleep at night rather than
// perpetually parked.
//
// REST SUPPRESSION: sleeping / on-break NPCs are skipped (SleepingUntil /
// BreakUntil) — the same rest suppressor the reactor and #1 producer use.
// NEED SUPPRESSION: an agent with a mild-or-worse need is NOT nudged to WORK
// (let it resolve the need first — v1 WORK-234), but IS still sent home (going
// home is how it rests). Decoratives carry no real needs (the needs tick skips
// them; their need rows are inert seed values), so they are never need-suppressed.
//
// LAST-RESORT REST FLOOR (ZBBS-WORK-355): the agent half above leans on the LLM
// acting on the go-home duty cue. When it doesn't and tiredness reaches NeedPeak
// ("exhausted"), ShiftTick stops warranting and MECHANICALLY marches the agent
// home — the deterministic catch a homed NPC otherwise lacks (npc_rest_fallback's
// RouteHomelessToRest only covers home-LESS NPCs). Off-shift only; an exhausted
// on-shift agent is already need-suppressed out of the to-work nudge and keeps
// its post. See classifyAgentDuty.
//
// The 2b perception cue ("your shift has started/ended — head to your
// workplace/home (structure_id: <id>)") that tells a warranted agent why it
// ticked and what to do is rendered from the ShiftDutyWarrantReason payload —
// see renderShiftDutyWarrantLine in engine/sim/perception/render.go. The cue +
// home's move_to tool (ZBBS-HOME-285) together make the agent half work.
//
// DEFERRED from this slice:
//   - Per-NPC lateness offset that staggers arrivals (v1 lateness_window_minutes)
//     — a feel refinement; without it all due NPCs head out on the same minute.

// ShiftDutyWarrantReason is the WarrantReason stamped when an agent NPC is on
// the wrong side of its shift boundary (on-shift away from work, or off-shift
// still at work). ToWork distinguishes the two so the 2b perception cue can
// render the right line. TargetStructureID is the structure the NPC should walk
// to (its WorkStructureID when ToWork, else its HomeStructureID) — the 2b cue
// surfaces it so the model passes it straight back to move_to(structure_id),
// the same way #1's need cue surfaces the satisfier. It is a w.Structures key
// (the shared structure/village-object identity), which is exactly what
// MoveToStructure looks up. Zero-sourced (a wall-clock boundary is not an
// event); the per-actor WarrantedSince gate prevents double-stamp. Mirrors
// IdleBackstopWarrantReason / NeedThresholdWarrantReason.
type ShiftDutyWarrantReason struct {
	ToWork            bool
	TargetStructureID StructureID
}

func (ShiftDutyWarrantReason) isWarrantReason()           {}
func (ShiftDutyWarrantReason) Kind() WarrantKind          { return WarrantKindShiftDuty }
func (ShiftDutyWarrantReason) DedupDiscriminator() uint64 { return 0 }

// ShiftTickerInterval — once a minute, matching RunNeedsTicker / RunSleepTicker
// / RunPhaseTicker. ~60s duty-retry granularity, easily tuned later.
const ShiftTickerInterval = time.Minute

// DefaultShiftLatenessWindowMinutes is the default arrival-stagger window when
// shift_lateness_window_minutes isn't set in the DB. A 30-minute spread breaks
// up the synchronized "everyone leaves at shift start" departure that the
// WORK-278 slice deferred; 0 would disable it. See ShiftLatenessWindowMinutes.
const DefaultShiftLatenessWindowMinutes = 30

// shiftLatenessOffset returns an NPC's to-work arrival delay (minutes after
// shift start) before its duty becomes eligible — a deterministic value in
// [0, window) seeded by (actor id, shift-start minute). The hash spreads NPCs
// across the window so they don't all leave on the same minute, and folding in
// the shift-start minute lets two NPCs sharing a start still differ. It is NOT
// per-day: a given NPC on a given shift-start gets the same offset every day
// (deterministic + restart-stable) — which is exactly what keeps the offset
// constant across a shift's minute-ticks so the level-triggered retry stays
// sound. (Fold a day counter into the seed if per-day variation is ever wanted.)
// window <= 0 disables (returns 0 → no stagger). FNV-1a, matching hashActorID.
func shiftLatenessOffset(id ActorID, shiftStartMinute, window int) int {
	if window <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], uint16(shiftStartMinute))
	_, _ = h.Write(buf[:])
	return int(h.Sum32() % uint32(window))
}

// minuteInShiftWindow reports whether nowMinute (0..1439) falls in [start, end),
// handling wrap-midnight windows (start > end, e.g. a 16:00–03:00 tavern shift).
// Mirrors the wrap logic in npc_sleep.go's isActorOnShift — and shares the same
// schedule fields with it — so start == end is an EMPTY window (never on shift),
// NOT all-day; kept consistent with the sleep machine on purpose. An all-day
// window is a full [start, end) span (e.g. 0..1440). Takes an explicit window so
// it serves both per-NPC schedules and the dawn/dusk fallback.
func minuteInShiftWindow(start, end, nowMinute int) bool {
	if start <= end {
		return nowMinute >= start && nowMinute < end
	}
	return nowMinute >= start || nowMinute < end
}

// OnShiftAtMinute reports whether an actor with the given (possibly nil) schedule
// bounds is on shift at nowMinute (local minute-of-day, 0..1439), with the same
// semantics as the sleep machine's isActorOnShift: unscheduled (either bound nil)
// is always off shift, and the window is the half-open wrap-aware [start, end) of
// minuteInShiftWindow. Exported so the read surface (the umbilical structures
// roster) can compute a keeper's on_shift off the published snapshot's
// ScheduleStartMin/EndMin + LocalMinuteOfDay without reaching into the live world
// or restating the window logic.
func OnShiftAtMinute(startMin, endMin *int, nowMinute int) bool {
	if startMin == nil || endMin == nil {
		return false
	}
	return minuteInShiftWindow(*startMin, *endMin, nowMinute)
}

// effectiveShiftWindow returns the actor's [start, end) minute-of-day shift
// window: its own schedule when both bounds are set, else the world's dawn/dusk
// day window (decision B — unscheduled NPCs are day-active). ok=false only when
// dawn/dusk fail to parse (the phase system logs that at load).
func effectiveShiftWindow(w *World, a *Actor) (start, end int, ok bool) {
	if a.ScheduleStartMin != nil && a.ScheduleEndMin != nil {
		return *a.ScheduleStartMin, *a.ScheduleEndMin, true
	}
	dawnH, dawnM, err := ParseHM(w.Settings.DawnTime)
	if err != nil {
		return 0, 0, false
	}
	duskH, duskM, err := ParseHM(w.Settings.DuskTime)
	if err != nil {
		return 0, 0, false
	}
	return dawnH*60 + dawnM, duskH*60 + duskM, true
}

// anyNeedRedOrWorse reports whether the actor has any need at the red tier or
// above (value >= the need's red threshold). Suppresses the to-WORK commute so a
// genuinely distressed NPC resolves the pressing need first. ZBBS-HOME-463
// narrowed this from the old mild-or-worse gate (v1 WORK-234): the mild gate
// stranded chronically-needy NPCs in the mild-but-not-red band — blocked from
// work, yet not yet driven to resolve (need-resolution only fires at red) — e.g.
// a homeless blacksmith parked at the inn all shift. Aligning the commute gate
// with the resolution threshold (red) closes that dead zone.
func anyNeedRedOrWorse(w *World, a *Actor) bool {
	for _, n := range Needs {
		value, ok := a.Needs[n.Key]
		if !ok {
			continue // a missing need is not an unmet need — don't suppress on it
		}
		threshold := w.Settings.NeedThresholds.Get(n.Key)
		if NeedLabelTier(value, threshold) >= NeedRed {
			return true
		}
	}
	return false
}

// atPeakTiredness reports whether the actor's tiredness is at NeedPeak (maxed —
// "exhausted"). The trigger for the last-resort home march: at peak there is no
// rest decision left worth an LLM turn, so a homed agent off-shift is walked home
// deterministically. Mirrors the peak gate deepFatigueDominatesNeeds uses in
// perception/render.go (NeedLabelTier vs NeedPeak on the tiredness need). A missing
// tiredness entry (nil Needs map or absent key) reads as 0 → below peak, so this
// never fires spuriously.
func atPeakTiredness(w *World, a *Actor) bool {
	return NeedLabelTier(a.Needs["tiredness"], w.Settings.NeedThresholds.Get("tiredness")) >= NeedPeak
}

// agentDutyAction is what ShiftTick should do for an agent NPC that has a
// standing duty (target/toWork already resolved by shiftDutyTarget).
type agentDutyAction int

const (
	// agentDutySkip — leave the actor alone this pass (mid-tick, an open warrant
	// cycle, or already en route to the rest-floor home target).
	agentDutySkip agentDutyAction = iota
	// agentDutyMarchHome — mechanically walk the actor home (the last-resort rest
	// floor): no warrant, no LLM turn.
	agentDutyMarchHome
	// agentDutyWarrant — stamp a shift-duty warrant for the actor to deliberate.
	agentDutyWarrant
)

// classifyAgentDuty decides ShiftTick's dispatch for an agent NPC (ZBBS-WORK-355).
//
// The last-resort rest floor: a peak-exhausted agent on a standing GO-HOME duty
// (toWork == false) is marched home mechanically rather than left to the LLM —
// the deterministic catch a homed NPC otherwise lacks (npc_rest_fallback's
// RouteHomelessToRest only covers home-LESS NPCs). The duty cue + recovery_options
// remain the path while merely red, so this fires only once those have failed to
// land the NPC home. On arrival handleAutoSleepOnArrival beds it and the tiredness
// recovery sweep restores it.
//
// HOME-ONLY, enforced locally (target == HomeStructureID) rather than trusting the
// caller, so the helper can't be reused to mechanically move an exhausted actor to
// a non-home target. To-work duties are never marched — an exhausted on-shift agent
// is already need-suppressed out of the to-work nudge in shiftDutyTarget anyway.
//
// The march is DEFERRED while a tick is pending or in flight (WarrantedSince /
// TickInFlight) so it never races the reactor over this actor's move, and the peak
// branch never stamps a fresh warrant. Liveness therefore depends on the reactor
// eventually clearing those flags — which it does: actorCanReactNow clears a stale
// warrant, a consumed tick clears both, and resetReactorStateOnLoad wipes in-flight
// state on restart. The gate is deliberately identical to the #1/#2 producers', so
// the floor inherits no new starvation risk. In the target case (the LLM ticked but
// didn't head home) the warrant is CONSUMED by that tick, so the floor fires the
// next minute — it never needs to override an open warrant. Idempotent via
// alreadyEnRouteTo.
func classifyAgentDuty(w *World, a *Actor, target StructureID, toWork bool) agentDutyAction {
	if !toWork && target == a.HomeStructureID && atPeakTiredness(w, a) {
		if a.WarrantedSince != nil || a.TickInFlight {
			return agentDutySkip
		}
		if alreadyEnRouteTo(a, target) {
			return agentDutySkip
		}
		return agentDutyMarchHome
	}
	if a.WarrantedSince != nil || a.TickInFlight {
		return agentDutySkip
	}
	return agentDutyWarrant
}

// windDownTarget resolves where an off-shift agent should head to wind down for
// the night, by housing relationship (ZBBS-WORK-387):
//
//   - Homed   → its HomeStructureID (the long-standing behavior).
//   - Lodger  → the inn (structure) where it holds an active ledger room grant
//     (soonestActiveLedgerGrant → the grant's room → that room's StructureID),
//     so the wind-down warrant + the renderDutySteer cue agree on the rented
//     room as the target. Today lodger routing-to-the-inn is purely LLM-driven;
//     this gives a lodger the same standing wind-down warrant a homed NPC gets.
//   - Homeless → no engine target (ok=false). A bed-less keeper has no single
//     place to march to, so its wind-down is a perception-only nudge
//     (renderDutySteer) that defers to recovery_options' rent-a-room / shade-tree
//     affordances, with RouteHomelessToRest as the red-tiredness floor.
//
// MUST be called from inside a Command.Fn (findRoom reads world state).
func windDownTarget(w *World, a *Actor, now time.Time) (StructureID, bool) {
	if a.HomeStructureID != "" {
		return a.HomeStructureID, true
	}
	if grant := soonestActiveLedgerGrant(a, now); grant != nil {
		if room := findRoom(w, grant.RoomID); room != nil && room.StructureID != "" {
			return room.StructureID, true
		}
	}
	return "", false
}

// shiftDutyTarget computes the actor's standing shift duty as of nowMinute:
// where it should be and isn't. Returns ok=false when there's no duty (already
// where it belongs, out of scope, resting, or a to-work nudge suppressed by an
// unmet need). Pure read of world + actor state — the dispatch (walk vs warrant)
// is the caller's, keyed on Kind.
func shiftDutyTarget(w *World, a *Actor, nowMinute int, now time.Time) (target StructureID, toWork, ok bool) {
	// Scope: NPCs only. PCs are player-driven; transient visitors run their own
	// ExpiresAt lifecycle.
	isAgent := a.Kind == KindNPCStateful || a.Kind == KindNPCShared
	if !isAgent && a.Kind != KindDecorative {
		return "", false, false
	}
	// Transient visitors run their own ExpiresAt lifecycle. Visitors are
	// KindNPCShared today, so this is agent-only in practice, but the guard is
	// unconditional for robustness if a decorative-visitor ever exists.
	if a.VisitorState != nil {
		return "", false, false
	}
	// Resting NPCs are left alone (same suppressor as the reactor / #1 / sleep).
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return "", false, false
	}
	if a.BreakUntil != nil && a.BreakUntil.After(now) {
		return "", false, false
	}
	// An actor mid scheduled-route (lamplighter / washerwoman / town_crier) is
	// owned by that route until it walks home and clears itself — same
	// "busy, leave it alone" class as the rest suppressors above. A route NPC is
	// unscheduled, so effectiveShiftWindow falls it back to the dawn/dusk day
	// window: at night it reads off-shift with a standing "head home" duty. Left
	// un-suppressed, the once-a-minute go-home nudge supersedes the route's walk
	// to a far stop — stranding that stop (never lit) and, for a homed route NPC,
	// marching it into its (sprite-hiding) house mid-round so the client never
	// shows it doing the round. ActiveRoutes[a.ID] is non-nil only while a route
	// is in flight (StartNPCRoute installs it, AdvanceNPCRoute clears it on the
	// home leg); the nil-map read is safe.
	if w.ActiveRoutes[a.ID] != nil {
		return "", false, false
	}

	start, end, winOK := effectiveShiftWindow(w, a)
	if !winOK {
		return "", false, false
	}
	onShift := minuteInShiftWindow(start, end, nowMinute)

	atWork := a.WorkStructureID != "" && a.InsideStructureID == a.WorkStructureID

	switch {
	case onShift && a.WorkStructureID != "" && !atWork:
		// Heading to work — suppressed only for an agent with a RED (pressing)
		// need, so it resolves that first; a merely mild need no longer defers the
		// commute (ZBBS-HOME-463). Decoratives have no real needs, so they always go.
		if isAgent && anyNeedRedOrWorse(w, a) {
			return "", false, false
		}
		// Arrival stagger (ZBBS-HOME-309): delay the to-work duty's INITIAL
		// eligibility by a per-NPC offset so the village doesn't all leave on the
		// same minute. Gated as "minutes since shift start >= offset" (not an
		// exact-minute match) so once eligible it stays eligible — preserving the
		// once-a-minute level-triggered retry. The delta is wrap-aware (the
		// (x+1440)%1440 form) for night shifts that cross midnight. Applied here
		// in the shared target so it covers both agents and decoratives. Only the
		// to-work arm is staggered; going-home (below) is never delayed.
		offset := shiftLatenessOffset(a.ID, start, w.Settings.ShiftLatenessWindowMinutes)
		if offset > 0 {
			minutesSinceShiftStart := (nowMinute - start + 1440) % 1440
			if minutesSinceShiftStart < offset {
				return "", false, false
			}
		}
		return a.WorkStructureID, true, true
	case !onShift:
		// Off-shift wind-down. The target is housing-dependent (ZBBS-WORK-387):
		// a homed NPC heads home; a lodger heads to the inn it rents. A homeless
		// NPC has no engine target (windDownTarget ok=false) — its wind-down is a
		// perception-only nudge that defers to recovery_options — so it falls
		// through here to no duty (preserving the prior homeless behavior, which
		// also produced no shift duty for a homeless off-shift actor).
		target, ok := windDownTarget(w, a, now)
		if !ok {
			return "", false, false
		}
		if a.InsideStructureID == target {
			return "", false, false // already wound down at the target
		}
		// "Open until" commit window (stay_open): a keeper that has committed to
		// staying open late suppresses the routine wind-down — but ONLY while not
		// peak-exhausted. At peak the needs floor must still win (the homed
		// MarchHome in classifyAgentDuty; a lodger's standing inn warrant), so the
		// commitment yields to exhaustion. Same "needs trump duty" layering as the
		// red-need gate on the to-work arm. OpenUntil is read live here; the
		// perception cue reads the ActorSnapshot mirror so warrant and cue agree.
		if a.OpenUntil != nil && a.OpenUntil.After(now) && !atPeakTiredness(w, a) {
			return "", false, false
		}
		// Heading to the wind-down target is NOT need-suppressed; resting is how an
		// NPC recovers. But a mid-meal NPC is left alone (same "busy, leave it
		// alone" class as the rest suppressors above): while an item-source dwell
		// credit is live the actor is finishing a consumed item whose slow-burn pays
		// out only while it stays put, so the wind-down duty would yank it off the
		// meal and forfeit the recovery. The meal is finite and this producer is
		// level-triggered, so the duty re-fires once the dwell ends. ZBBS-WORK-386.
		if HasActiveItemDwell(a.DwellCredits) {
			return "", false, false
		}
		return target, false, true
	default:
		return "", false, false
	}
}

// alreadyEnRouteTo reports whether the actor is already walking into the target
// structure, so the decorative walk dispatch stays idempotent across the
// once-a-minute re-evaluation (don't re-issue a walk every tick).
func alreadyEnRouteTo(a *Actor, target StructureID) bool {
	mi := a.MoveIntent
	return mi != nil &&
		mi.Destination.Kind == MoveDestinationStructureEnter &&
		mi.Destination.StructureID != nil &&
		*mi.Destination.StructureID == target
}

// ShiftTick returns a Command that applies one pass of the shift/duty producer.
// Decoratives are mechanically walked; agents get a duty warrant. Runs on the
// world goroutine, so the MoveActor / tryStampWarrant calls are serialized.
func ShiftTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			nowMinute := localMinuteOfDay(w, now)
			for _, a := range w.Actors {
				target, toWork, ok := shiftDutyTarget(w, a, nowMinute, now)
				if !ok {
					continue
				}
				if a.Kind == KindDecorative {
					if alreadyEnRouteTo(a, target) {
						continue
					}
					if _, err := MoveActor(a.ID, NewStructureEnterDestination(target), false, now).Fn(w); err != nil {
						log.Printf("sim/shift_duty: walk %s -> %s: %v", a.ID, target, err)
					}
					continue
				}
				// Agent NPC — dispatch decided by classifyAgentDuty:
				//   - MarchHome: a peak-exhausted agent off-shift and not yet
				//     home is walked home mechanically (the last-resort rest
				//     floor), same as a decorative — no warrant, no LLM turn.
				//   - Warrant: otherwise stamp a duty warrant (gated like #1 —
				//     leave an already-pending / mid-tick actor alone; the level
				//     check re-fires next minute if the duty still stands). The
				//     NPC self-walks via move_to.
				switch classifyAgentDuty(w, a, target, toWork) {
				case agentDutyMarchHome:
					if _, err := MoveActor(a.ID, NewStructureEnterDestination(target), false, now).Fn(w); err != nil {
						log.Printf("sim/shift_duty: rest-floor march %s -> home %s: %v", a.ID, target, err)
					}
				case agentDutyWarrant:
					tryStampWarrant(w, a, WarrantMeta{
						TriggerActorID: a.ID,
						Reason:         ShiftDutyWarrantReason{ToWork: toWork, TargetStructureID: target},
					}, now)
				case agentDutySkip:
					// Mid-tick, an open warrant cycle, or already en route home —
					// nothing to do this pass.
				}
			}
			return nil, nil
		},
	}
}

// RunShiftTicker owns the shift/duty goroutine: once a minute, submit a
// ShiftTick. Same time.NewTicker idiom as RunNeedsTicker / RunSleepTicker.
// Returns when ctx is cancelled.
func RunShiftTicker(ctx context.Context, w *World) {
	t := time.NewTicker(ShiftTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("shift")
			if _, err := w.SendContext(ctx, ShiftTick(time.Now().UTC())); err != nil {
				if ctx.Err() == nil {
					log.Printf("sim/shift_duty: tick failed: %v", err)
				}
			}
		}
	}
}
