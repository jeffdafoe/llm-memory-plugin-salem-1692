package sim

import (
	"fmt"
	"log"
	"time"
)

// lodger_rebook.go — ZBBS-HOME-296 PR2. Engine-auto rebook backstop: the
// last resort that keeps a lodger housed when the LLM-driven renewal
// negotiation doesn't fire in time.
//
// v2 shape (a deliberate departure from v1's pay_ledger-driven sweep):
//
//   - The lodger relationship IS an active ledger RoomAccess (checkpointed),
//     not a pay_ledger row. So candidate-finding is an in-memory scan of
//     w.Actors for the soonest active ledger grant in the renewal window —
//     no SQL CTE, no location gate (v1 gated on "physically inside the inn",
//     a pay_ledger-era heuristic; in v2 holding an active grant IS being a
//     lodger, and PCs aren't in the engine yet).
//   - The renewal is an in-place extension of that grant's ExpiresAt plus a
//     direct coin transfer (lodger -> keeper), exactly how pay_commands.go /
//     pay_with_item_commands.go move coins.
//   - The audit is an ActionLog entry (ActionTypePaid), NOT a fabricated
//     delivered pay_ledger row. World.PayLedger models buyer-initiated pay
//     OFFERS (offer -> accept -> deliver); an engine-auto rebook is not an
//     offer, so it stays off that substrate and uses the action-log audit
//     funnel instead. (ZBBS-HOME-296 §5 predates this v2 pivot; cleared with
//     Jeff 2026-05-23.)
//
// The whole sweep runs as one Command on the world goroutine, so it's
// atomic and naturally consistent — no cross-command race, no partial debit.
// Idempotent: extending ExpiresAt pushes the grant past the window, so a
// re-run (or an LLM renewal that already extended the same grant in place)
// no-ops the next tick. Folded into RunRoomSweep, ahead of ExpireRoomAccess,
// so a still-active grant is renewed before the expiry sweep could flip it.
//
// Live room-scoped narration ("X settled for another night") is deferred —
// the ActionLog entry is the durable, perception-visible record (it surfaces
// in the lodger's "what just happened" and feeds consolidation); the cosmetic
// Hub broadcast can ride a follow-on once the engine is live.

// autoRebookLeadTime is how far ahead of a grant's expiry the sweep renews.
// 6h is the engine giving up on the LLM — every escalating perception cue
// (the ## Your lodging tiers + the affordability cue) has fired across the
// prior 48h; this catches the "they didn't act" outcome.
const autoRebookLeadTime = 6 * time.Hour

// RebookLodgerRecord is one renewal performed this sweep — returned for
// telemetry / logging (and, later, narration).
type RebookLodgerRecord struct {
	LodgerID     ActorID
	KeeperID     ActorID
	StructureID  StructureID
	Nightly      int
	NewExpiresAt time.Time
}

// RebookLodgersResult carries the renewals performed this sweep.
type RebookLodgersResult struct {
	Renewals []RebookLodgerRecord
}

// RebookLodgersDue returns a Command that renews every lodger whose soonest
// active ledger grant expires within autoRebookLeadTime, charging the
// per-night rate and extending the grant by one night. Skips (lets the grant
// lapse) when the lodger can't afford a night or the keeper is gone.
func RebookLodgersDue(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if now.IsZero() {
				// `now` stamps the audit entry's OccurredAt; a zero value
				// would make AppendActionLogEntry reject the audit AFTER the
				// coins+grant were already mutated, breaking the
				// "renewed implies audited" invariant. Reject up front so no
				// renewal happens without an audit (code_review).
				return RebookLodgersResult{}, fmt.Errorf("RebookLodgersDue: zero now")
			}
			nightly := LodgingNightlyRate(w.Settings.LodgingDefaultWeeklyRate)
			if nightly <= 0 {
				// Rate unset / disabled (or < 7, can't bill 1/night) — the
				// whole backstop is off; existing grants expire naturally.
				return RebookLodgersResult{}, nil
			}
			loc := w.Settings.Location
			if loc == nil {
				loc = time.UTC
			}
			checkOut := w.Settings.LodgingCheckOutHour

			var result RebookLodgersResult
			for lodgerID, lodger := range w.Actors {
				if lodger == nil {
					continue
				}
				grant := soonestActiveLedgerGrant(lodger, now)
				if grant == nil {
					continue
				}
				if grant.ExpiresAt.Sub(now) > autoRebookLeadTime {
					continue // not yet in the renewal window
				}
				if lodgerCoveredBeyond(lodger, now, *grant.ExpiresAt) {
					// Another active grant already covers a later window —
					// renewing the soonest is redundant. Also the idempotency
					// guard against an LLM renewal that landed a second grant.
					continue
				}

				room := findRoom(w, grant.RoomID)
				if room == nil {
					continue
				}
				keeperID, keeper := keeperForStructure(w, room.StructureID)
				if keeper == nil {
					log.Printf("sim/lodger_rebook: no keeper at structure %q for lodger %q — skipping renewal (grant will lapse)",
						room.StructureID, lodgerID)
					continue
				}
				if keeperID == lodgerID {
					// The keeper holding a lodging grant at their own
					// structure would debit+credit the same actor (net zero)
					// and fabricate a paid renewal + audit. Skip — a keeper
					// isn't a paying lodger of their own inn (code_review).
					continue
				}
				if lodger.Coins < nightly {
					// A broke lodger stays a candidate every minute across the
					// whole 6h window — logging each skip would emit ~360 lines
					// per lodger and drown real signal (work's note). Log only
					// in the final window before the grant lapses, so we still
					// get the "went homeless" beat once, near when it matters,
					// without the spam. Stateless — no per-grant bookkeeping.
					if grant.ExpiresAt.Sub(now) <= 2*RoomSweepInterval {
						log.Printf("sim/lodger_rebook: lodger %q has %d coins; need %d — room lapsing (can't afford renewal)",
							lodgerID, lodger.Coins, nightly)
					}
					continue
				}

				newExpires := ComputeLodgerUntil(*grant.ExpiresAt, 1, checkOut, loc)
				lodger.Coins -= nightly
				keeper.Coins += nightly
				grant.ExpiresAt = &newExpires

				structLabel := structureLabel(w, room.StructureID)
				if _, err := AppendActionLogEntry(ActionLogEntry{
					ActorID:    lodgerID,
					OccurredAt: now,
					ActionType: ActionTypePaid,
					Text: fmt.Sprintf("Lodging at %s renewed with %s for %d coins; room paid through %s.",
						structLabel, keeper.DisplayName, nightly, newExpires.In(loc).Format("Mon 15:04")),
				}).Fn(w); err != nil {
					// Audit append failed (empty ActorID / zero time — caller
					// bug). The coin transfer + extension already happened; log
					// loudly rather than roll back, so the lodger stays housed.
					log.Printf("sim/lodger_rebook: action-log append failed for lodger %q: %v", lodgerID, err)
				}

				result.Renewals = append(result.Renewals, RebookLodgerRecord{
					LodgerID:     lodgerID,
					KeeperID:     keeperID,
					StructureID:  room.StructureID,
					Nightly:      nightly,
					NewExpiresAt: newExpires,
				})
			}
			return result, nil
		},
	}
}

// soonestActiveLedgerGrant returns the actor's active ledger RoomAccess with
// the nearest future expiry, or nil when they hold none. The renewal targets
// the soonest grant — the one about to lapse first.
func soonestActiveLedgerGrant(a *Actor, now time.Time) *RoomAccess {
	if a == nil {
		return nil
	}
	var best *RoomAccess
	for _, ra := range a.RoomAccess {
		if !IsActiveLedgerGrant(ra, now) {
			continue
		}
		if best == nil || ra.ExpiresAt.Before(*best.ExpiresAt) {
			best = ra
		}
	}
	return best
}

// lodgerCoveredBeyond reports whether the actor holds an active ledger grant
// expiring strictly later than exp — i.e. a later grant already covers the
// window the renewal would add, so renewing the soonest grant is redundant.
func lodgerCoveredBeyond(a *Actor, now time.Time, exp time.Time) bool {
	for _, ra := range a.RoomAccess {
		if !IsActiveLedgerGrant(ra, now) {
			continue
		}
		if ra.ExpiresAt.After(exp) {
			return true
		}
	}
	return false
}

// keeperForStructure returns the actor working at structureID (its keeper),
// or ("", nil) when none. When several actors work there (unusual), the
// lexicographically smallest ID is chosen so the resolved keeper — and thus
// the coin credit — is deterministic across runs (w.Actors is a map). Mirrors
// perception.keeperOf, on the live world.
func keeperForStructure(w *World, structureID StructureID) (ActorID, *Actor) {
	var bestID ActorID
	var best *Actor
	for id, a := range w.Actors {
		if a == nil || a.WorkStructureID != structureID {
			continue
		}
		if best == nil || id < bestID {
			bestID = id
			best = a
		}
	}
	return bestID, best
}

// structureLabel returns the structure's display name, or a generic fallback.
func structureLabel(w *World, structureID StructureID) string {
	if s, ok := w.Structures[structureID]; ok && s.DisplayName != "" {
		return s.DisplayName
	}
	return "the inn"
}
