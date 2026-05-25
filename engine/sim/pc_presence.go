package sim

import (
	"context"
	"log"
	"time"
)

// PC presence cleanup (ZBBS-WORK-326) — the v2 port of v1's pc_presence_sweep.
//
// A human player drives their PC through the Godot client, which polls
// POST /api/village/pc/me every 10s while the game is open (talk_panel.gd
// REFRESH_INTERVAL). StampPCSeen records the instant of each poll on
// Actor.LastPCSeenAt, so a fresh stamp means "a live client is here." When the
// player closes the tab the polls stop and the stamp goes stale.
//
// Without cleanup a stale (ghost) PC stays pinned in whatever conversational
// huddle it was in, and co-located LLM-NPCs keep getting warranted to greet a
// player who isn't actually there — a real prod cost bug (2026-05-11/12). The
// sweep ejects stale PCs from their huddles, and the encounter cascades skip
// stale PCs when forming new huddles (PCPresenceStale is the shared gate), so
// the greeting cost stops. The PC actor stays in the world (idle/offline) and
// re-attaches the moment its client polls again — it is NOT despawned.

const (
	// DefaultPCPresenceStaleAfter is the fallback staleness threshold when
	// WorldSettings.PCPresenceStaleAfter is unset. ~4 missed 10s polls — long
	// enough to ride out a network hiccup, short enough to clear a closed tab
	// promptly.
	DefaultPCPresenceStaleAfter = 40 * time.Second

	// PCPresenceSweepInterval is how often the presence sweep scans for stale
	// PCs. A const (not a setting), matching RoomSweepInterval / SleepTickerInterval.
	PCPresenceSweepInterval = 15 * time.Second
)

// PCPresenceStaleAfter returns the configured staleness threshold, falling back
// to DefaultPCPresenceStaleAfter when unset/zero. Exported so the encounter
// cascades (package cascade) read the same threshold the sweep uses.
func PCPresenceStaleAfter(w *World) time.Duration {
	if w != nil && w.Settings.PCPresenceStaleAfter > 0 {
		return w.Settings.PCPresenceStaleAfter
	}
	return DefaultPCPresenceStaleAfter
}

// PCPresenceStale reports whether a PC with the given last-seen stamp should be
// treated as an absent ghost at now. A nil stamp is stale by design: no client
// has polled /pc/me for this PC this session, so nothing is driving it (a
// just-loaded PC reads nil until its first poll, which is correct — after a
// restart the client must re-attach to be "present"). The shared gate used by
// both the sweep and the encounter cascades.
func PCPresenceStale(lastSeenAt *time.Time, now time.Time, staleAfter time.Duration) bool {
	if lastSeenAt == nil {
		return true
	}
	return now.Sub(*lastSeenAt) > staleAfter
}

// StampPCSeen records a /pc/me poll: sets the caller's PC LastPCSeenAt to the
// instant the command EXECUTES on the world goroutine (not request-receipt
// time) — so a backed-up command channel can't stamp an already-old time and
// make a live client look stale earlier than it is (code_review R1). A no-op
// for a missing actor or a non-PC id, so a stray caller can't stamp an NPC.
// MUST run on the world goroutine (mutates the actor) — sent as a command from
// the pc/me handler.
func StampPCSeen(actorID ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a := w.Actors[actorID]
			if a == nil || a.Kind != KindPC {
				return nil, nil
			}
			t := time.Now().UTC()
			a.LastPCSeenAt = &t
			return nil, nil
		},
	}
}

// SweepStalePCPresence ejects every stale PC from its current huddle, returning
// the count ejected. Only PCs actually in a huddle are touched — an absent PC
// standing alone costs nothing, so there's nothing to clear. leaveCurrentHuddle
// emits HuddleLeft (remaining NPCs notice the player left once, then settle) and
// clears CurrentHuddleID; the PC itself stays in the world. Safe to iterate
// w.Actors while calling leaveCurrentHuddle — it mutates huddle membership, not
// the actor map.
func SweepStalePCPresence(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			staleAfter := PCPresenceStaleAfter(w)
			ejected := 0
			for _, a := range w.Actors {
				if a.Kind != KindPC || a.CurrentHuddleID == "" {
					continue
				}
				if !PCPresenceStale(a.LastPCSeenAt, now, staleAfter) {
					continue
				}
				leaveCurrentHuddle(w, a, now)
				ejected++
			}
			return ejected, nil
		},
	}
}

// RunPCPresenceSweep ticks SweepStalePCPresence every PCPresenceSweepInterval.
// SendContext so shutdown unblocks the command cleanly even if the world
// goroutine has already exited. Mirrors RunRoomSweep.
func RunPCPresenceSweep(ctx context.Context, w *World) {
	t := time.NewTicker(PCPresenceSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now().UTC()
			if _, err := w.SendContext(ctx, SweepStalePCPresence(now)); err != nil && ctx.Err() == nil {
				log.Printf("sim/pc_presence: stale-PC sweep failed: %v", err)
			}
		}
	}
}
