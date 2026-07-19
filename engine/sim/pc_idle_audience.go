package sim

import (
	"context"
	"log"
	"time"
)

// pc_idle_audience.go — LLM-466. The "is a human actually there" signal, as
// distinct from "is a socket alive".
//
// Eco mode (eco_mode.go) throttles the village when nobody is watching, but its
// audience predicate trusted LastPCSeenAt — the WS presence heartbeat, which the
// server re-stamps every 15s for as long as a browser tab holds the socket open.
// A tab left open all night therefore held audience true forever and eco mode
// never engaged once: on 2026-07-19 John Ellis spent six hours offering ale to a
// player character whose last tick was five weeks earlier.
//
// Two questions, two stamps. Ghost-ejection genuinely wants "is the socket
// alive" (a hidden tab whose render loop has stopped is still a present player,
// LLM-342) and keeps LastPCSeenAt. Eco mode wants "is a human there", which only
// an input event can answer, and that is LastPCActivityAt.
//
// Watching without playing is legitimate — the requirement is that you can leave
// the village running and just look at it. So idleness is not itself taken as
// absence: at PCAudienceIdleAfter the engine drops audience AND asks, and a
// single click restores it for another hour. Nobody there, nobody clicks, and
// the village paces down. The prompt is a click rather than a keypress because
// play mode stays mobile-portable — a tap is the one input every client has.
//
// What the ack costs when a player IS there: one click an hour. What it saves
// when nobody is: an abandoned tab is bounded at a single hour of full cadence
// (~600-800 LLM calls, $0.40-0.50) instead of that much every hour forever.

const (
	// DefaultPCAudienceIdleAfter is the fallback idle horizon: how long a
	// connected client may go without a single input before the engine stops
	// counting it as an audience and asks whether anyone is still there. An hour
	// is long enough that a player reading the tableau is never interrupted mid-
	// watch, and short enough that an abandoned tab costs one hour of full
	// cadence rather than a night of it. Live-tunable (eco_audience_idle_seconds)
	// so a live verification doesn't have to wait an hour.
	DefaultPCAudienceIdleAfter = 60 * time.Minute

	// PCIdleAudienceSweepInterval is how often the idle sweep looks for clients
	// that have crossed the horizon. A const, not a setting, matching
	// PCPresenceSweepInterval. It bounds only how late the PROMPT is, never how
	// late the throttle is: AudienceActive is computed from the stamps at read
	// time, so eco mode engages on the first reactor scan past the horizon
	// regardless of this cadence.
	PCIdleAudienceSweepInterval = 15 * time.Second
)

// PCAudienceIdleAfter returns the configured idle horizon, falling back to
// DefaultPCAudienceIdleAfter when unset/zero.
func PCAudienceIdleAfter(w *World) time.Duration {
	if w != nil && w.Settings.PCAudienceIdleAfter > 0 {
		return w.Settings.PCAudienceIdleAfter
	}
	return DefaultPCAudienceIdleAfter
}

// PCActivityStale reports whether a PC with the given activity stamp has gone
// idle past the horizon at now. A nil stamp is stale by design — same posture as
// PCPresenceStale: a PC nobody has touched this session has proven nothing. The
// connect path stamps activity (opening the client IS an input), so a live
// client reads fresh from its first frame and only a genuinely untouched session
// sits at nil.
func PCActivityStale(lastActivityAt *time.Time, now time.Time, idleAfter time.Duration) bool {
	if lastActivityAt == nil {
		return true
	}
	return now.Sub(*lastActivityAt) > idleAfter
}

// TouchPCActivity stamps a PC's player-activity cursor — the answer to "is a
// human there". Called from TouchPCInput (every deliberate in-world action), the
// WS connect path (opening a client is an input), and the /pc/attend ack. A
// no-op for a missing actor or a non-PC id, so a stray caller can't credit an
// NPC with an audience. Clears any pending idle prompt and reports whether one
// was actually cleared, so callers can emit the client-facing transition exactly
// once. MUST run on the world goroutine (mutates the actor).
func TouchPCActivity(w *World, actorID ActorID, now time.Time) bool {
	pc := w.Actors[actorID]
	if pc == nil || pc.Kind != KindPC {
		return false
	}
	stamp := now
	pc.LastPCActivityAt = &stamp
	if !pc.IdlePromptPending {
		return false
	}
	pc.IdlePromptPending = false
	return true
}

// AckPCIdlePrompt is the /pc/attend route command: the player answered the
// candle prompt. Stamps activity (restoring audience for another horizon) and
// emits PCIdlePromptCleared when a prompt was actually pending, so the client's
// overlay is dismissed by the server round-trip rather than optimistically —
// the same source-of-truth contract the sleep overlay keeps. Answering with no
// prompt up still stamps: a click is a click, and the ack route arriving late
// (the prompt cleared by an in-world action in the meantime) must not be
// treated as an error.
func AckPCIdlePrompt(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			cleared := TouchPCActivity(w, actorID, now)
			if cleared {
				w.emit(&PCIdlePromptCleared{ActorID: actorID, At: now})
			}
			return cleared, nil
		},
	}
}

// StampConnectedPCsActive refreshes the activity horizon for every PC whose
// login just opened a client connection (LLM-466) — opening the village IS an
// input, so a player who connects and then only watches holds audience for a
// full horizon before being asked anything. Deliberately NOT called from the
// presence heartbeat: that runs every 15s for as long as a tab exists, and
// stamping activity from it would rebuild the exact bug this ticket fixes.
// Emits PCIdlePromptCleared for any PC whose prompt was pending, so a client
// reconnecting into a prompted PC doesn't inherit a stale overlay. MUST run on
// the world goroutine.
func StampConnectedPCsActive(connectedLogins map[string]struct{}, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(connectedLogins) == 0 {
				return 0, nil
			}
			stamped := 0
			for _, a := range w.Actors {
				if a == nil || a.Kind != KindPC || a.LoginUsername == "" {
					continue
				}
				if _, ok := connectedLogins[a.LoginUsername]; !ok {
					continue
				}
				if TouchPCActivity(w, a.ID, now) {
					w.emit(&PCIdlePromptCleared{ActorID: a.ID, At: now})
				}
				stamped++
			}
			return stamped, nil
		},
	}
}

// StampPCConnected is the WS connect path's single command: presence AND
// activity in one trip to the world goroutine. Two separate commands would let
// a presence sweep or reactor scan land between them and observe a client that
// is socket-fresh but activity-nil — a harmless transient (it reads as "not an
// audience", which is the safe direction) but an avoidable one, since
// registration is logically one transition.
func StampPCConnected(connectedLogins map[string]struct{}, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if _, err := StampConnectedPCsSeen(connectedLogins).Fn(w); err != nil {
				return nil, err
			}
			return StampConnectedPCsActive(connectedLogins, now).Fn(w)
		},
	}
}

// SweepPCIdleAudience raises the candle prompt for every PC that is still
// CONNECTED (fresh presence — there is a client to render the prompt) but has
// gone idle past the horizon (stale activity — no human has touched it). The
// IdlePromptPending flag makes the emit edge-triggered: a PC that stays idle is
// not re-prompted on every 15s pass. A disconnected PC is skipped entirely —
// there is no one to ask, and its reconnect will stamp activity anyway.
//
// The prompt and the audience drop are simultaneous by construction: both read
// the same stamp against the same horizon, so there is no grace window in which
// the engine pays for full cadence while waiting for an answer. The returned
// count is the number of prompts raised THIS pass (a transition count, unlike
// SweepStalePCPresence's). MUST run on the world goroutine.
func SweepPCIdleAudience(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			idleAfter := PCAudienceIdleAfter(w)
			staleAfter := PCPresenceStaleAfter(w)
			raised := 0
			for _, a := range w.Actors {
				if a == nil || a.Kind != KindPC || a.IdlePromptPending {
					continue
				}
				if PCPresenceStale(a.LastPCSeenAt, now, staleAfter) {
					continue // no client attached — nobody to ask
				}
				if !PCActivityStale(a.LastPCActivityAt, now, idleAfter) {
					continue // a human touched this recently
				}
				a.IdlePromptPending = true
				w.emit(&PCIdlePromptShown{ActorID: a.ID, At: now})
				raised++
			}
			return raised, nil
		},
	}
}

// RunPCIdleAudienceSweep ticks SweepPCIdleAudience every
// PCIdleAudienceSweepInterval. SendContext so shutdown unblocks the command
// cleanly even if the world goroutine has already exited. Mirrors
// RunPCPresenceSweep.
func RunPCIdleAudienceSweep(ctx context.Context, w *World) {
	t := time.NewTicker(PCIdleAudienceSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("pc_idle_audience")
			now := time.Now().UTC()
			if _, err := w.SendContext(ctx, SweepPCIdleAudience(now)); err != nil && ctx.Err() == nil {
				log.Printf("sim/pc_idle_audience: idle-audience sweep failed: %v", err)
			}
		}
	}
}
