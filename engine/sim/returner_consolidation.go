package sim

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// returner_consolidation.go — visit-end fold of a returner's per-PC episodic
// memory (LLM-383). Reuses the persistent-relationship consolidation MACHINERY
// (ConsolidationCandidate + buildConsolidationPrompt + the prefix-verify apply
// shape) but with a different CADENCE. A returner accrues facts only during a
// visit (hours) then goes dormant for weeks, so the trigger is "the visit ended"
// (the returner is no longer present in the village) — not the persistent
// 24h-floor / co-present-pair sweep. A mid-visit ceiling backstop folds a single
// marathon visit before its trail out-runs the FIFO cap.
//
// The off-world worker lives in cascade/returner_consolidation.go (it issues LLM
// calls); this file is sim-package primitives only, mirroring consolidation.go. It
// reuses ErrStaleConsolidationSnapshot + salientFactsPrefixEqual from that file.

// ReturnerConsolidationFirstMinFacts is the minimum trail length that qualifies a
// returner acquaintance's FIRST fold (LastConsolidatedAt still nil). Mirrors
// ConsolidationFirstMinFacts (WORK-233): distilling a deep impression from a
// single "good morrow" reads as fake depth. Below the gate the raw facts simply
// persist in the durable trail (unrendered — only the folded summary reaches
// perception) and fold once a later visit adds enough; nothing is lost, only the
// summary waits.
const ReturnerConsolidationFirstMinFacts = 3

// MaxReturnerSummaryRunes bounds a folded summary_text in Go BEFORE it is stored,
// so Go provably satisfies the recurring_visitor_acquaintance_summary_sane DB CHECK
// (char_length(summary_text) <= 4000). Without this, a runaway LLM fold would
// persist in memory and then fail the CHECK at the next checkpoint upsert, aborting
// the checkpoint Tx and wedging persistence — the exact failure mode the generous
// CHECK posture exists to avoid. Kept below the DB bound for headroom; Postgres
// char_length counts code points, matching Go rune count. The fold prompt asks for
// one or two sentences, so truncation is a backstop for a runaway reply, not a
// routine path.
const MaxReturnerSummaryRunes = 3800

// BoundReturnerSummary rune-truncates a folded returner summary to
// MaxReturnerSummaryRunes. Exported so the store path (ApplyReturnerConsolidation)
// and the render surface (perception) enforce the same bound — the latter defends
// against an out-of-band DB edit that set a summary between the Go bound and the
// looser DB CHECK.
func BoundReturnerSummary(s string) string {
	if utf8.RuneCountInString(s) > MaxReturnerSummaryRunes {
		return string([]rune(s)[:MaxReturnerSummaryRunes])
	}
	return s
}

// FindReturnerConsolidationCandidates scans the durable returner set for
// acquaintances whose fact trail should be folded, returning up to `limit`
// ConsolidationCandidates. The candidate reuses the actor-tier type: ActorID
// carries the rvis- returner id, PeerID the PC actor id, ActorName the returner's
// bare persona, ActorLLMAgent the shared salem-visitor slug.
//
// A pair qualifies when its trail is non-empty AND either:
//   - the returner has DEPARTED (no in-flight actor links to it) — the visit is
//     over, so fold what happened, subject to the first-fold min-facts gate; or
//   - the trail has reached ReturnerConsolidationCeiling — a mid-visit backstop
//     that folds a marathon visit before the FIFO cap (MaxReturnerSalientFacts)
//     evicts its oldest beats.
//
// Ordered ceiling-first, then departed-first, then longest trail, then by
// (rvis, pc) id for a deterministic sweep. Never returns an error.
func FindReturnerConsolidationCandidates(at time.Time, limit int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			out := []ConsolidationCandidate{}
			if limit <= 0 || len(w.RecurringVisitors) == 0 {
				return out, nil
			}
			present := presentReturnerIDs(w)

			type entry struct {
				rv       *RecurringVisitor
				pcID     ActorID
				acq      *RecurringAcquaintance
				ceiling  bool
				departed bool
			}
			var all []entry
			for _, rv := range w.RecurringVisitors {
				if rv == nil {
					continue
				}
				_, here := present[rv.ID]
				departed := !here
				for pcID, acq := range rv.Acquaintances {
					if acq == nil {
						continue
					}
					n := len(acq.SalientFacts)
					if n == 0 {
						continue
					}
					ceilingOverdue := n >= ReturnerConsolidationCeiling
					// First fold needs a floor of facts to be worth distilling; a
					// subsequent fold (prior summary exists) folds any non-empty
					// trail, since the prior summary carries the depth.
					firstMinMet := acq.LastConsolidatedAt != nil || n >= ReturnerConsolidationFirstMinFacts
					departedFold := departed && firstMinMet
					if !ceilingOverdue && !departedFold {
						continue
					}
					all = append(all, entry{rv: rv, pcID: pcID, acq: acq, ceiling: ceilingOverdue, departed: departed})
				}
			}

			sort.Slice(all, func(i, j int) bool {
				a, b := all[i], all[j]
				if a.ceiling != b.ceiling {
					return a.ceiling
				}
				if a.departed != b.departed {
					return a.departed
				}
				an, bn := len(a.acq.SalientFacts), len(b.acq.SalientFacts)
				if an != bn {
					return an > bn
				}
				if a.rv.ID != b.rv.ID {
					return a.rv.ID < b.rv.ID
				}
				return a.pcID < b.pcID
			})
			if len(all) > limit {
				all = all[:limit]
			}

			for _, e := range all {
				factsCopy := make([]SalientFact, len(e.acq.SalientFacts))
				copy(factsCopy, e.acq.SalientFacts)
				var lastCons *time.Time
				if e.acq.LastConsolidatedAt != nil {
					t := *e.acq.LastConsolidatedAt
					lastCons = &t
				}
				out = append(out, ConsolidationCandidate{
					ActorID:          ActorID(e.rv.ID),
					PeerID:           e.pcID,
					ActorName:        e.rv.Name,
					PeerName:         e.acq.PCDisplayName,
					ActorLLMAgent:    VisitorAgentName,
					PriorSummary:     e.acq.SummaryText,
					Facts:            factsCopy,
					LastConsolidated: lastCons,
				})
			}
			return out, nil
		},
	}
}

// ApplyReturnerConsolidation installs a folded SummaryText onto a returner's
// per-PC acquaintance, prunes the consolidated facts, and stamps
// LastConsolidatedAt — after verifying the snapshot still matches the live trail's
// prefix. The prefix-verify defends the mid-visit ceiling case, where capture may
// have appended (and FIFO-evicted) facts during the LLM call; a departed
// returner's trail is quiescent so the check trivially passes. Mirrors
// ApplyConsolidation for the actor tier.
//
// On a stale snapshot returns ErrStaleConsolidationSnapshot with NO writes; the
// next sweep re-snapshots and retries. An empty snapshot installs the summary +
// stamp (benign edge case — all facts evicted before apply). Rejects an empty
// summary, unknown returner, or unknown pair. rvID carries the rvis- id (the
// candidate's ActorID).
func ApplyReturnerConsolidation(rvID ActorID, pcID ActorID, newSummary string, snapshotFacts []SalientFact, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			newSummary = strings.TrimSpace(newSummary)
			if newSummary == "" {
				return nil, fmt.Errorf("ApplyReturnerConsolidation: empty new summary for %q→%q", rvID, pcID)
			}
			// Bound in Go so this write provably satisfies the summary_sane DB CHECK
			// (char_length <= 4000); an unbounded fold would wedge the next checkpoint.
			newSummary = BoundReturnerSummary(newSummary)
			rv, ok := w.RecurringVisitors[RecurringVisitorID(rvID)]
			if !ok || rv == nil {
				return nil, fmt.Errorf("ApplyReturnerConsolidation: returner %q not found", rvID)
			}
			acq, ok := rv.Acquaintances[pcID]
			if !ok || acq == nil {
				return nil, fmt.Errorf("ApplyReturnerConsolidation: acquaintance %q→%q not found", rvID, pcID)
			}
			n := len(snapshotFacts)
			if n == 0 {
				acq.SummaryText = newSummary
				t := at
				acq.LastConsolidatedAt = &t
				return nil, nil
			}
			if n > len(acq.SalientFacts) || !salientFactsPrefixEqual(acq.SalientFacts[:n], snapshotFacts) {
				return nil, ErrStaleConsolidationSnapshot
			}
			acq.SummaryText = newSummary
			acq.SalientFacts = acq.SalientFacts[n:]
			t := at
			acq.LastConsolidatedAt = &t
			return nil, nil
		},
	}
}
