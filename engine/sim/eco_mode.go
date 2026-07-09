package sim

import "time"

// eco_mode.go — LLM-313. Eco mode: throttle LLM deliberation cadence when no
// player is present.
//
// 2026-07-06 spend was $4.70 with ~5 minutes of player time — effectively every
// deliberation turn played to an empty auditorium. The village keeps its economy
// and survival loops at full fidelity when unwatched; what slows is the SOCIAL
// tableau nobody is watching (NPC-to-NPC chatter re-prompts, greet/farewell
// beats, engine-injected liveness pokes, visitor spawns).
//
// Presence predicate: any KindPC actor with a fresh LastPCSeenAt — the exact
// signal the ghost-ejection sweep already trusts (pc_presence.go,
// PCPresenceStale, 40s staleness over the client's 10s /pc/me polls). Watching
// and playing are the same thing (admins use the same Godot client; the
// umbilical's JSON reads deliberately do NOT count as audience). Wake is
// instant and free: the first /pc/me poll stamps presence and the next reactor
// scan runs at full cadence.
//
// Mechanism: DELAY, not drop. Warrants mint exactly as today — dedup,
// perception build, and staleness handling unchanged. When no audience is
// present, the reactor's emit path applies a per-cycle pacing floor (the same
// push-WarrantDueAt idiom as MinReactorTickGap) sized by the cycle's eco
// bucket. Commerce runs THROUGH conversation (npc_spoke carries buyer-seller
// negotiation), so suppression would strand buyers mid-purchase and starve
// NPCs; delay only slows the tableau — the sale still completes. A delayed
// warrant whose moment has passed is cleared by the existing staleness paths
// at zero LLM cost.
//
// Buckets (ecoWarrantGap):
//   - Survival / duty / in-flight completion / commerce commitments → no gap,
//     always full speed. This is the DEFAULT for unlisted kinds — a newly
//     added warrant kind is never slowed by accident (same default-is-salient
//     posture as isAmbientWarrantKind).
//   - Economy pacing (restock, production choice, farm upkeep, stall repair,
//     seek work) → EcoEconomyGap.
//   - Social cadence (npc_spoke, huddle join/leave/conclude beats) →
//     EcoSocialGap.
//
// Also gated when unwatched: the plain idle backstop stops stamping (an idle
// NPC costs nothing until poked; the stranded-recovery upgrade still fires —
// world integrity is not audience-dependent) and visitor SPAWNING pauses
// (visitors exist to be seen; despawn/cleanup keep running so existing
// visitors age out normally).

const (
	// DefaultEcoSocialGap is the fallback per-actor pacing floor for a
	// social-bucket warrant cycle while unwatched. 60s ≈ a chatter response
	// every minute instead of every few seconds — conversations still
	// conclude, at a fraction of the token burn. Deliberately BELOW the
	// default warrant stale horizon (defaultMaxWarrantAge, 90s) with margin:
	// a cycle parked by the eco gate must come due before it can age out, or
	// delay-not-drop would silently become drop (code_review R1). SetEcoMode
	// rejects gaps above the live ceiling (maxEcoGap: horizon minus the
	// scan-lateness margin) and the reactor gate clamps to the same ceiling
	// as a second line for values that arrived outside the setter.
	DefaultEcoSocialGap = 60 * time.Second

	// DefaultEcoEconomyGap is the fallback pacing floor for an economy-bucket
	// cycle while unwatched. Mild: restock/production/upkeep decisions land
	// within half a minute of their trigger instead of seconds.
	DefaultEcoEconomyGap = 30 * time.Second
)

// AudienceActive reports whether any player character has a fresh presence
// stamp — the world-level "someone is watching" predicate. Reuses the
// ghost-ejection staleness gate so "present" means exactly what the huddle
// sweep already means by it. MUST run on the world goroutine (reads actors).
func AudienceActive(w *World, now time.Time) bool {
	if w == nil {
		return false
	}
	staleAfter := PCPresenceStaleAfter(w)
	for _, a := range w.Actors {
		if a == nil || a.Kind != KindPC {
			continue
		}
		if !PCPresenceStale(a.LastPCSeenAt, now, staleAfter) {
			return true
		}
	}
	return false
}

// ecoModeEngaged reports whether the eco throttles apply right now: the master
// switch is on AND nobody is watching. Callers still consult the per-bucket
// gaps (a zero gap disables that bucket's throttle individually).
func ecoModeEngaged(w *World, now time.Time) bool {
	if w == nil || !w.Settings.EcoEnabled {
		return false
	}
	return !AudienceActive(w, now)
}

// effectiveEcoSocialGap returns the configured social-bucket pacing floor,
// falling back to DefaultEcoSocialGap when unset. An explicit negative value
// never occurs (SetEcoMode validates >= 0); zero means "this bucket's throttle
// is off" and is returned as-is.
func effectiveEcoSocialGap(s WorldSettings) time.Duration {
	if s.EcoSocialGap < 0 {
		return DefaultEcoSocialGap
	}
	return s.EcoSocialGap
}

// effectiveEcoEconomyGap is effectiveEcoSocialGap's economy-bucket twin.
func effectiveEcoEconomyGap(s WorldSettings) time.Duration {
	if s.EcoEconomyGap < 0 {
		return DefaultEcoEconomyGap
	}
	return s.EcoEconomyGap
}

// effectiveMaxWarrantAge is the live warrant stale horizon — the same
// fallback warrantCycleStale applies. The eco gaps must stay strictly below
// it: a cycle parked by the eco gate anchors on the actor's last tick, and
// WarrantedSince always postdates that tick (a tick consumes the prior
// cycle), so age-at-due < gap — a gap below the horizon guarantees a parked
// cycle comes due before the shelved/fairness stale paths can evict it,
// keeping delay-not-drop true. SetEcoMode rejects violating knobs at the
// door; ecoCycleGapClamped is the defensive second line for values that
// arrived outside the setter (a direct DB edit, or MaxWarrantAge lowered
// after the gaps were set).
func effectiveMaxWarrantAge(s WorldSettings) time.Duration {
	if s.MaxWarrantAge > 0 {
		return s.MaxWarrantAge
	}
	return defaultMaxWarrantAge
}

// ecoStaleMargin is the safety margin the eco ceiling keeps under the warrant
// stale horizon: the worst-case lateness between a parked cycle coming due and
// the scan that would emit it — the configured warrant jitter and evaluator
// cadence, floored at one second (code_review R2: a fixed 1s margin was not a
// guarantee under a >1s jitter or scan cadence).
func ecoStaleMargin(s WorldSettings) time.Duration {
	margin := time.Second
	if s.ReactorJitterMax > margin {
		margin = s.ReactorJitterMax
	}
	if s.ReactorEvaluatorCadence > margin {
		margin = s.ReactorEvaluatorCadence
	}
	return margin
}

// maxEcoGap is the ceiling an eco gap may reach: the live warrant stale
// horizon minus the scan-lateness margin, so "comes due" strictly precedes
// "ages out" even on a late scan. Can be <= 0 under a pathologically small
// MaxWarrantAge — callers treat that as "no room to throttle at all".
func maxEcoGap(s WorldSettings) time.Duration {
	return effectiveMaxWarrantAge(s) - ecoStaleMargin(s)
}

// ecoCycleGapClamped is ecoCycleGap bounded to maxEcoGap (see above). A
// non-positive ceiling disables the throttle outright — with a stale horizon
// that tight there is no parking window in which a delayed cycle would still
// be alive.
func ecoCycleGapClamped(warrants []WarrantMeta, s WorldSettings) time.Duration {
	gap := ecoCycleGap(warrants, s)
	if gap <= 0 {
		return gap
	}
	ceiling := maxEcoGap(s)
	if ceiling <= 0 {
		return 0
	}
	if gap > ceiling {
		return ceiling
	}
	return gap
}

// ecoWarrantGap classifies a warrant kind into its eco pacing bucket and
// returns the gap that bucket carries under the given settings. The default
// is 0 (full speed): survival (need_threshold, tend_need, dwell lifecycle,
// consumed, source_activity_done, arrived, stranded), duty (shift_duty,
// return_to_post), operator (admin/impulse), player-driven (pc_spoke — it
// cannot fire without a player anyway), and every commerce-commitment kind
// where a counterparty is blocked waiting (pay_offer, paid, pay_resolved,
// scene_quote_targeted, labor_offer, serve_handover, stall_repair_hired) all
// fall through to it, as does any future kind nobody classified — a new kind
// is never slowed by accident.
func ecoWarrantGap(k WarrantKind, s WorldSettings) time.Duration {
	if isSocialCadenceWarrantKind(k) {
		return effectiveEcoSocialGap(s)
	}
	switch k {
	// Economy pacing: periodic economic housekeeping with no counterparty
	// blocked on the answer.
	case WarrantKindRestock,
		WarrantKindProductionChoice,
		WarrantKindFarmUpkeep,
		WarrantKindStallRepair,
		WarrantKindSeekWork:
		return effectiveEcoEconomyGap(s)
	default:
		return 0
	}
}

// isSocialCadenceWarrantKind reports whether k belongs to the social-cadence
// bucket: the self-sustaining NPC-to-NPC chatter engine (npc_spoke), the huddle
// membership beats that fund greet/farewell rounds, and the idle backstop (it
// stops STAMPING while unwatched — EvaluateIdleBackstop — but a pre-eco cycle
// may still hold one, so it paces like social liveness). Two consumers: the eco
// pacing gate above, and the loop sweep's post-conclude warrant clearing
// (LLM-333) — a cycle made ONLY of these kinds is decorative conversation beats
// whose moment has passed once its huddle is concluded as a livelock. Every
// commerce, survival, duty, and player-driven kind is deliberately NOT here.
func isSocialCadenceWarrantKind(k WarrantKind) bool {
	switch k {
	case WarrantKindNPCSpoke,
		WarrantKindHuddleJoined,
		WarrantKindHuddlePeerJoined,
		WarrantKindHuddleLeft,
		WarrantKindHuddlePeerLeft,
		WarrantKindHuddleConcluded,
		WarrantKindIdleBackstop:
		return true
	default:
		return false
	}
}

// ecoCycleGap returns the pacing floor for an actor's open warrant cycle: the
// MINIMUM gap across its pending warrants. Any full-speed warrant in the pile
// (a red need, a pending pay offer) makes the whole cycle full speed — the
// actor is due for a reason that must not wait, and the tick consumes the
// whole batch anyway. An empty cycle returns 0 (defensive; the evaluator
// never emits one).
func ecoCycleGap(warrants []WarrantMeta, s WorldSettings) time.Duration {
	if len(warrants) == 0 {
		return 0
	}
	min := time.Duration(-1)
	for _, m := range warrants {
		g := ecoWarrantGap(m.Kind(), s)
		if g == 0 {
			return 0
		}
		if min < 0 || g < min {
			min = g
		}
	}
	if min < 0 {
		return 0
	}
	return min
}
