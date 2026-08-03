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

// actionLogIgnoresActor reports whether the action log should drop a row for
// this actor. True for waterfowl, whose movement is ambient scenery motion
// rather than a doing worth recording (LLM-593). The pond ducks wander every
// few seconds: 15,672 "walked" rows a day into a log the rest of the village
// fills at ~1,000/day, drowning the admin Village tab that renders it.
//
// Gated here at the write funnel rather than in the one subscriber that
// tripped over it. Waterfowl reach the log only through locomotion
// (ActorArrived / ActorLeftStructure), and the arrival subscriber was the
// second site to miss them after the LLM-582 huddle gate; a funnel gate means
// the next subscriber to observe movement cannot reopen it.
//
// NOT gated on KindDecorative, which is the wider population and would be
// wrong. The lamplighter, washerwoman and town crier are all decorative
// carriers (see routeIsBeat) — the engine walks them because they have no
// LLM volition, but they tour and they speak, and the town crier alone has
// written thousands of announcement rows. Their doings belong in the log:
// agent_action_log is the sole input to the day note behind the nightly
// dream pipeline, so dropping them would silently amputate that history.
// actorIsWaterfowl is the canonical "ambient motion" predicate and already
// carries its own Kind gate.
//
// An unresolvable ActorID is NOT ignored: tests append under synthetic ids,
// and a visitor's row is deliberately kept with its id blanked (see
// AppendActionLogDurable and LLM-573). That asymmetry is why this keys on a
// RESOLVED duck rather than on a failed lookup — reading a miss as scenery
// would re-drop the very rows LLM-573 restored.
//
// The lookup needs no registry of departed decoratives to be sound: World.emit
// dispatches subscribers synchronously and inline on the world goroutine, so
// the append for a duck's ActorArrived completes inside the same command that
// emitted it and no removal can interleave. What remains is any append made
// for an id ALREADY deleted from w.Actors — one surviving row per such append,
// not one per deleted duck, since nothing stops a caller repeating it. That is
// accepted rather than closed: a deleted duck emits no further locomotion, so
// in practice the count is zero, and the alternative (splitting movement rows
// onto their own unresolved-id policy so identity resolution stops carrying
// the semantics) buys nothing against a case production does not produce.
//
// MUST be called from inside a Command.Fn — actorIsWaterfowl reads w.Sprites.
// Not a new constraint on AppendActionLogDurable's exported surface: it
// already read w.Actors on this line's behalf, so every caller was required
// to be on the world goroutine before this gate existed.
func actionLogIgnoresActor(w *World, id ActorID) bool {
	return actorIsWaterfowl(w, w.Actors[id])
}

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
//   - waterfowl → dropped silently (LLM-593, see actionLogIgnoresActor).
//   - Text rune-truncated at the boundary so the substrate can't
//     accumulate oversized rows even if a subscriber forgot to
//     truncate: MaxSpokenActionLogTextLen for spoken lines (kept full
//     for the player-facing talk-panel backload), the tighter
//     MaxActionLogTextLen for every other action type.
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
			if actionLogIgnoresActor(w, entry.ActorID) {
				return nil, nil
			}
			// Spoken lines keep the full utterance for the player-facing
			// talk-panel backload; every other type stays at the tighter
			// budget the C2 consolidation prompt shares. Both are catch-all
			// bounds — the speak handler already validates the utterance to
			// MaxSpeakTextChars upstream, so this only fires as a safety net.
			maxLen := MaxActionLogTextLen
			if entry.ActionType == ActionTypeSpoke {
				maxLen = MaxSpokenActionLogTextLen
			}
			if utf8.RuneCountInString(entry.Text) > maxLen {
				runes := []rune(entry.Text)
				entry.Text = string(runes[:maxLen])
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
