package sim

import (
	"context"
	"log"
	"time"
)

// PC presence cleanup (ZBBS-WORK-326; presence signal reworked in LLM-342) — the
// v2 port of v1's pc_presence_sweep.
//
// A human player drives their PC through the Godot client. The presence signal is
// the player's live WebSocket (client/scripts/event_client.gd): while a socket is
// connected the server re-stamps Actor.LastPCSeenAt every
// PCPresenceHeartbeatInterval (RunPCPresenceHeartbeat), so a fresh stamp means "a
// live client is here." The WS is the right signal because the browser's network
// stack answers the ping/pong even when the tab is hidden or occluded — unlike the
// old /pc/me poll, which rode the render loop and stopped whenever the window quit
// painting, erasing a player who was still sitting there (LLM-342). Deliberate PC
// actions (TouchPCInput) also stamp presence, as defence in depth against a
// momentary socket blip.
//
// When the player closes the tab the socket's close frame drops it at once; a
// silent loss (sleep, network) is caught within ~pongWait (~60s) via the missed
// pong. Either way the heartbeat then stops stamping it and the stamp goes stale.
// Without cleanup a stale (ghost) PC stays pinned in whatever
// conversational huddle it was in, and co-located LLM-NPCs keep getting warranted
// to greet a player who isn't actually there — a real prod cost bug (2026-05-11/12).
// The sweep ejects stale PCs from their huddles, and the encounter cascades skip
// stale PCs when forming new huddles (PCPresenceStale is the shared gate), so the
// greeting cost stops. The PC actor stays in the world (idle/offline) and
// re-attaches the moment its client reconnects — it is NOT despawned.

const (
	// DefaultPCPresenceStaleAfter is the fallback staleness threshold when
	// WorldSettings.PCPresenceStaleAfter is unset. At ~2.6× the heartbeat interval
	// it rides out a single missed heartbeat or a brief socket blip, while still
	// clearing a genuinely dropped socket within a sweep interval of the ~60s pong
	// timeout.
	DefaultPCPresenceStaleAfter = 40 * time.Second

	// PCPresenceSweepInterval is how often the presence sweep scans for stale
	// PCs. A const (not a setting), matching RoomSweepInterval / SleepTickerInterval.
	PCPresenceSweepInterval = 15 * time.Second

	// PCPresenceHeartbeatInterval is how often the server re-stamps LastPCSeenAt
	// for every WS-connected PC (LLM-342). It MUST stay comfortably below the stale
	// threshold (DefaultPCPresenceStaleAfter) so a live socket always refreshes a
	// PC before the sweep could judge it stale; 15s against 40s leaves ~2 stamps of
	// margin for scheduling jitter. Matches PCPresenceSweepInterval — presence is
	// generated and reclaimed on the same cadence.
	PCPresenceHeartbeatInterval = 15 * time.Second
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

// StampPCSeen sets one PC's LastPCSeenAt to the instant the command EXECUTES on
// the world goroutine (not request-receipt time) — so a backed-up command channel
// can't stamp an already-old time and make a live client look stale earlier than
// it is (code_review R1). A no-op for a missing actor or a non-PC id, so a stray
// caller can't stamp an NPC. MUST run on the world goroutine (mutates the actor).
//
// The live presence signal is the WS heartbeat (StampConnectedPCsSeen, driven by
// RunPCPresenceHeartbeat) plus TouchPCInput on deliberate actions; this
// single-actor primitive is retained for tests and as a utility.
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

// SweepStalePCPresence marks every stale PC that is in a huddle as absent —
// co-present but quiet (LLM-342). The returned count is the number of stale-in-
// huddle PCs touched THIS pass, a diagnostic/test figure — not a transition count:
// there is no persistent absent flag, so a PC that stays stale is re-counted (and
// re-quieted, idempotently) on each 15s pass. It does NOT evict
// the PC: eviction was a heavy hammer (a HuddlePeerLeft to every peer, killed
// in-flight commerce, and leave/join churn the moment the client reconnected) for
// what is fundamentally a cost guard. markPCAbsentInHuddle instead keeps the PC in
// the huddle and just lets the conversation let go of it, so perception can render
// it "stepped away" (out of the addressable set) while a genuinely departed player
// is concluded by the huddle silence sweep and a returning one resumes seamlessly.
// Only PCs actually in a huddle are touched — an absent PC standing alone costs
// nothing. Idempotent across the 15s re-runs: an absent PC sources no new warrants,
// so a later pass finds nothing to clear. Safe to iterate w.Actors while mutating
// warrants (not the actor map).
func SweepStalePCPresence(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			staleAfter := PCPresenceStaleAfter(w)
			marked := 0
			for _, a := range w.Actors {
				if a.Kind != KindPC || a.CurrentHuddleID == "" {
					continue
				}
				if !PCPresenceStale(a.LastPCSeenAt, now, staleAfter) {
					continue
				}
				markPCAbsentInHuddle(w, a)
				marked++
			}
			return marked, nil
		},
	}
}

// markPCAbsentInHuddle quiets the huddle around a stale PC without evicting it
// (LLM-342). The PC keeps its membership — perception renders it "stepped away" —
// but the conversation releases it: its own outgoing reply edges and warrant cycle
// are dropped (it will not act), and every huddle-mate's warrants that were
// TRIGGERED BY this PC (its speech, its join) are cleared so no one is driven to
// keep addressing an absent player. Warrants a peer holds for its own reasons or
// for another NPC are left untouched, so NPC↔NPC threads continue. MUST run on the
// world goroutine.
func markPCAbsentInHuddle(w *World, pc *Actor) {
	pc.dropAwaitingReplies()
	clearWarrant(pc)
	h := w.Huddles[pc.CurrentHuddleID]
	if h == nil {
		return
	}
	for peerID := range h.Members {
		if peerID == pc.ID {
			continue
		}
		if peer := w.Actors[peerID]; peer != nil {
			dropWarrantsSourcedBy(peer, pc.ID)
		}
	}
}

// dropWarrantsSourcedBy removes from an actor's warrant cycle every non-Force
// warrant whose source event was produced by sourceID (a huddle-mate's speech or
// join, whose WarrantMeta.SourceActorID is the speaker/joiner). Clears the whole
// cycle when nothing survives; leaves it untouched when nothing matched (the cheap
// idempotent path). Force warrants (operator nudges) always survive. The cycle
// clock (WarrantedSince/WarrantDueAt) stays put for the surviving warrants — they
// are still pending and will fire on schedule.
func dropWarrantsSourcedBy(a *Actor, sourceID ActorID) {
	if len(a.Warrants) == 0 {
		return
	}
	kept := make([]WarrantMeta, 0, len(a.Warrants))
	for _, wm := range a.Warrants {
		if wm.SourceActorID == sourceID && !wm.Force {
			continue
		}
		kept = append(kept, wm)
	}
	if len(kept) == len(a.Warrants) {
		return
	}
	if len(kept) == 0 {
		clearWarrant(a)
		return
	}
	a.Warrants = kept
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
			w.beatTicker("pc_presence")
			now := time.Now().UTC()
			if _, err := w.SendContext(ctx, SweepStalePCPresence(now)); err != nil && ctx.Err() == nil {
				log.Printf("sim/pc_presence: stale-PC sweep failed: %v", err)
			}
		}
	}
}

// ConnectedPCSource reports which login names currently hold at least one live
// client connection. The WS hub (httpapi.Hub) implements it; the presence
// heartbeat reads it each tick. Declared here, not in httpapi, so package sim
// owns no dependency on the HTTP layer (httpapi already imports sim).
type ConnectedPCSource interface {
	ConnectedLogins() map[string]struct{}
}

// StampConnectedPCsSeen refreshes LastPCSeenAt for every PC whose login currently
// holds a live WS connection (LLM-342). This is the server-side presence
// heartbeat that replaces the client's render-loop /pc/me poll as the liveness
// signal: a hidden or occluded browser tab suspends the render loop (and its poll)
// but not the WebSocket, whose ping/pong is serviced by the browser's network
// stack — so a connected login means the player is still here. Stamps the
// execution-time instant (like StampPCSeen) so a backed-up command channel can't
// backdate a live client into staleness. NPCs and logins with no matching PC match
// nothing. Returns the count stamped. MUST run on the world goroutine.
func StampConnectedPCsSeen(connectedLogins map[string]struct{}) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(connectedLogins) == 0 {
				return 0, nil
			}
			now := time.Now().UTC()
			stamped := 0
			for _, a := range w.Actors {
				if a.Kind != KindPC || a.LoginUsername == "" {
					continue
				}
				if _, ok := connectedLogins[a.LoginUsername]; !ok {
					continue
				}
				a.LastPCSeenAt = &now
				stamped++
			}
			return stamped, nil
		},
	}
}

// RunPCPresenceHeartbeat re-stamps presence for every WS-connected PC on
// PCPresenceHeartbeatInterval (LLM-342). Started only when the WS surface exists
// (the hub is the ConnectedPCSource). Cleanup after the client goes away: a clean
// tab close sends a close frame, so the read pump drops the login at once; a
// silent drop (sleep, network loss) is caught within ~pongWait (~60s) via the
// missed pong, and the heartbeat keeps stamping until then. Once the login is
// gone the stamps stop and the existing SweepStalePCPresence reclaims the ghost
// within staleAfter + one sweep interval — so worst-case cleanup for a silent
// drop is ~pongWait + staleAfter + PCPresenceSweepInterval (~115s). That is
// slower than the old poll on a silent drop, but the ticket accepts the WS blind
// spots; not ejecting a still-present player is the point. The ZBBS-WORK-326 cost
// guard is preserved, now keyed on the socket instead of the render-loop poll.
// SendContext so shutdown unblocks cleanly. Mirrors RunPCPresenceSweep.
func RunPCPresenceHeartbeat(ctx context.Context, w *World, src ConnectedPCSource) {
	t := time.NewTicker(PCPresenceHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("pc_presence_heartbeat")
			logins := src.ConnectedLogins()
			if len(logins) == 0 {
				continue
			}
			if _, err := w.SendContext(ctx, StampConnectedPCsSeen(logins)); err != nil && ctx.Err() == nil {
				log.Printf("sim/pc_presence: presence heartbeat failed: %v", err)
			}
		}
	}
}
