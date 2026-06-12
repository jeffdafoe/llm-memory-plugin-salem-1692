package sim

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// action_log_commands.go — substrate primitives for the action-log
// cascade slice. AppendActionLogEntry is the write funnel called by
// event subscribers; CompactActionLog drops entries older than the
// retention cutoff. Both run on the world goroutine.

// AppendActionLogEntry returns a Command that appends entry to
// World.ActionLog. Used by event subscribers (cascade.RegisterActionLog
// wires Spoke / Paid / ItemConsumed / OrderDelivered / ActorArrived).
// Subscribers run inline on the world goroutine inside emit, so they
// invoke AppendActionLogEntry(entry).Fn(w) directly rather than going
// through SendContext (which would deadlock the single goroutine).
//
// Validation funnel:
//   - ActorID empty → error (caller bug; surfaces in the subscriber's
//     log line so we don't silently drop a row).
//   - OccurredAt zero → error (same).
//   - Text rune-truncated to MaxActionLogTextLen at the boundary so
//     the substrate can't accumulate oversized rows even if a
//     subscriber forgot to truncate.
//
// Append-only — no dedup, no ordering pass. The slice grows
// monotonically until CompactActionLog drops entries past the
// retention cutoff. Time-of-events is approximately monotonic
// (subscribers stamp evt.At from the world goroutine) but compaction
// is ordering-tolerant either way.
func AppendActionLogEntry(entry ActionLogEntry) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if entry.ActorID == "" {
				return nil, fmt.Errorf("sim.AppendActionLogEntry: empty ActorID")
			}
			if entry.OccurredAt.IsZero() {
				return nil, fmt.Errorf("sim.AppendActionLogEntry: zero OccurredAt")
			}
			if utf8.RuneCountInString(entry.Text) > MaxActionLogTextLen {
				runes := []rune(entry.Text)
				entry.Text = string(runes[:MaxActionLogTextLen])
			}
			// Stamp the actor's conversational scope at action time
			// (ZBBS-HOME-437) — append runs synchronously in the emitting
			// subscriber, so the actor hasn't moved since the action. Done
			// centrally here so every ActionType gets the scope without each
			// cascade handler repeating it. Missing actor (e.g. a test
			// appending for a synthetic id) leaves the zero public scope.
			if actor, ok := w.Actors[entry.ActorID]; ok && actor != nil {
				entry.StructureID = conversationalScopeStructure(w, actor)
				entry.RoomID = audienceRoomScope(w, actor)
			}
			w.actionLogSeq++
			entry.Seq = w.actionLogSeq
			w.ActionLog = append(w.ActionLog, entry)
			return nil, nil
		},
	}
}

// CompactActionLog returns a Command that drops entries with
// OccurredAt strictly before cutoff (entries exactly at cutoff are
// kept). Called periodically by the action-log sweep goroutine in
// cascade. Returns the number of entries dropped (telemetry — useful
// for log lines / admin dashboards / sweep-driver-side assertions).
//
// Implementation: single-pass filter into a fresh slice. O(n) at
// Hannah scale (<10K entries) is microseconds. Ordering-tolerant —
// doesn't assume the slice is sorted by OccurredAt, so a subscriber
// that uses a slightly out-of-band timestamp can't corrupt
// compaction. Empty log fast-paths.
func CompactActionLog(cutoff time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(w.ActionLog) == 0 {
				return 0, nil
			}
			kept := make([]ActionLogEntry, 0, len(w.ActionLog))
			for _, e := range w.ActionLog {
				if !e.OccurredAt.Before(cutoff) {
					kept = append(kept, e)
				}
			}
			dropped := len(w.ActionLog) - len(kept)
			w.ActionLog = kept
			return dropped, nil
		},
	}
}
