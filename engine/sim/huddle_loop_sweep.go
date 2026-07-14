package sim

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// huddle_loop_sweep.go — LLM-159. Loop-conclusion sweep for huddles.
//
// The silence sweep ([[huddle_silence_sweep.go]]) concludes a DORMANT huddle —
// one with no spoken line, join, or transaction for hours. This sweep concludes
// the exact inverse: a huddle that is hyper-ACTIVE but going nowhere. The live
// case (huddle hud-9fb3…): three NPCs at home spent ~5 minutes repeating "let's
// go to the market" / "I'm ready to go" ~50 times and never moved — 100 spoken
// lines in 8 minutes, an LLM deliberation burned every ~4 seconds.
//
// The degeneracy observer ([[degeneracy.go]]) cannot see this: it scores a tick
// futile only when every tool failed (arm A), when there was no audience (arm
// B), or when the actor shuttles between structures with no progress (the LLM-124
// oscillation arm). A speak into a populated huddle succeeds, has an audience,
// and involves no movement — so every tick scores productive and the per-actor
// futility streak resets each tick. The pathology is a property of the HUDDLE
// (the conversation produces no action), not of any single actor, so the
// detection and the response both live here, at the huddle level.
//
// Detection is deterministic, via four OR'd arms sharing one LoopingSince
// onset + persistence gate: the LEXICAL arm (a highly repetitive
// RecentUtterances ring — huddleUtteranceRepetition — with no progress newer
// than it), the LEDGER arm (LLM-309, a silent offer→decline standoff), the
// ENDURANCE arm (LLM-333, HuddleLoopMaxTurns spoken lines with no progress —
// content-blind, because a creative model paraphrases past any lexical
// threshold), and the LINGERING arm (LLM-397, a conversation older than
// HuddleConversationWindDown — the only arm that is not a pathology verdict, and
// the only one that can see a healthy, productive conversation that has simply
// gone on long enough; it is commerce-guarded and its conclusion drops the
// structure's carry-over so the scene genuinely ends). A persistence gate
// (Huddle.LoopingSince, must hold for HuddleLoopTimeout) keeps it high-precision:
// a brief repetitive patch is spared, only a sustained livelock is concluded —
// and for the lingering arm the gate is the wind-down grace period, the members'
// chance to close the scene themselves before the engine does. Conclusion is SILENT (no per-member
// warrant), like the silence sweep — breaking the loop must not itself wake the
// members into a fresh re-pitch round; their next genuine warrant (a need, a
// schedule duty) drives real behavior. Members' pending social-cadence-only
// warrant cycles are also cleared at conclusion (LLM-333) so the leftover
// npc_spoke beats of the dead conversation can't tick once more and re-form it.
// A `stuck` tick-telemetry record is emitted per member so the loop surfaces in
// the umbilical exactly where the degeneracy observer's records do.
//
// Posture: OFF by default. The master knob HuddleLoopTimeout env-defaults to 0
// (disabled); an operator turns it on and tunes it live, the same opt-in shape
// as the degeneracy observer — concluding a live conversation is heavier than
// the silence sweep's dormant-conclude, so it should never act unbidden.
//
// Cadence + lifecycle mirror the silence sweep's coalesced AfterFunc self-rearm
// chain:
//
//	RunHuddleLoopSweep(ctx, w)
//	└─> kickHuddleLoopSweep
//	     └─> armNextHuddleLoopSweep
//	          └─> [cadence] fireScheduledHuddleLoopSweep
//	               └─> SendContext(evaluateHuddleLoopAndRearm(now))
//	                    └─> Fn: clear flag, run scan, re-arm

// HuddleLoopRepeatPercentDefault is the default repetition threshold when
// WorldSettings.HuddleLoopRepeatPercent is unset (zero): the percent of the
// ring's content-bearing turns that must be near-duplicates of another turn for
// the conversation to read as looping. 60 is conservative — a healthy
// conversation advances (each turn adds new content) and sits far below it,
// while a livelock approaches 100.
const HuddleLoopRepeatPercentDefault = 60

// HuddleLoopSweepCadenceDefault is the default scan cadence when
// WorldSettings.HuddleLoopSweepCadence is unset (zero). 30s — finer than the
// silence sweep's 60s because the persistence gate is minutes, not hours, so a
// coarser cadence would add meaningful latency to the conclusion.
const HuddleLoopSweepCadenceDefault = 30 * time.Second

// huddleLoopMinUtterances is how full the RecentUtterances ring must be before
// the repetition metric is trusted. Below this there are too few turns to tell a
// loop from a normal short exchange. The ring caps at MaxRecentUtterancesPerHuddle
// (8), so 6 means "nearly full."
const huddleLoopMinUtterances = 6

// huddleLoopLiveWindow is how recent the last spoken line must be for a huddle to
// count as ACTIVELY looping. A huddle that has gone quiet is the silence sweep's
// domain, not this one — without this guard a stale-but-repetitive ring would
// keep a long-dormant huddle flagged. A real loop speaks every few seconds, so
// 90s is a wide safety margin.
const huddleLoopLiveWindow = 90 * time.Second

// huddleLoopNearDupJaccard is the content-token Jaccard at or above which two
// utterances count as near-duplicates of each other. 0.5 means "share at least
// half their combined vocabulary" — "let's go to the market" vs "I'm ready to go
// to the market" clusters; two turns about different topics do not.
const huddleLoopNearDupJaccard = 0.5

// huddleLoopLedgerMinTerminals is how many non-completed pay-ledger terminals
// (huddleLoopLedgerFutileState) between the SAME buyer→seller pair for the SAME
// item must pile up within one huddle before the transactional-futility arm
// (LLM-309) reads the negotiation as a silent livelock. One decline is ordinary
// haggling; two is a pattern; three is a loop that isn't going to converge (the
// live Elizabeth→Josiah incident ran to eleven). This is the ledger analog of
// huddleLoopMinUtterances — a shape constant, not a live knob: the arm rides the
// loop sweep's master enable (HuddleLoopTimeout) and shared persistence gate.
const huddleLoopLedgerMinTerminals = 3

// huddleLoopLedgerRecencyWindow bounds how far back a terminal counts toward the
// standoff: only terminals resolved within this window of `now` are tallied, so an
// old cluster of declines that has since gone quiet decays out of the signal
// instead of pinning the huddle. Wider than huddleLoopLiveWindow (a slow loop
// still accumulates) but far inside the pay ledger's 1h terminal-retention reap.
// Mirrors coPresentBuyStandoff's recentlyResolvedOfferWindow role (LLM-297).
const huddleLoopLedgerRecencyWindow = 5 * time.Minute

// huddleLoopReasonLingering tags a conclusion by the LLM-397 lingering arm — the
// only arm that is not a pathology verdict. The other three name a conversation
// that is stuck (repeating, standing off, or spending turns on nothing); this one
// names a conversation that has simply run its course, and it is the reason a
// conclusion drops the structure's carry-over instead of preserving it.
const huddleLoopReasonLingering = "conversation_lingering"

// HuddleConversationWindDownDefault is the lingering arm's clock (LLM-397): how
// long a conversation may run — measured on Huddle.ConversationSince, so churned
// huddle ids don't restart it — before the wind-down steer arms and the
// persistence gate starts running toward a silent conclude. The hard end of a
// conversation is therefore this PLUS HuddleLoopTimeout (12m + 3m = 15m at the
// live settings), and the members get the whole gate to close the scene
// themselves before the engine does it for them.
//
// 12 minutes is deliberately generous. The arc this replaces (the eco-conclude
// sweep, LLM-334) cut every unwatched conversation at 3 minutes, which severed
// the best scenes the village produces mid-sentence — on 2026-07-14 it cut the
// innkeeper's story about her dead husband ten times in a hundred minutes, while
// the clique simply re-formed and resumed each time, so it bought nothing. Eco
// mode's gaps are what slow an unwatched village down; ending a scene is not a
// pacing instrument and is not gated on the audience (a conversation that reads
// well unwatched is the same conversation a player would walk in on).
const HuddleConversationWindDownDefault = 12 * time.Minute

// HuddleLoopMaxTurnsDefault is the endurance arm's default turn budget (LLM-333)
// when WorldSettings.HuddleLoopMaxTurns is unset: how many spoken lines a huddle
// may accumulate with no progress event (completed transaction, genuine
// membership change, player line) before it reads as stuck regardless of
// content. 16 = two ring-fulls — the live huddles in the 2026-07-08 telemetry
// window that were healthy ran 2-10 spokes between progress events, while the
// uncaught loops ran 24-110. Tunable via huddle_loop_max_turns.
const HuddleLoopMaxTurnsDefault = 16

// huddleLoopTurnsPerMember scales the endurance budget for crowded huddles: the
// effective budget is max(configured, members × this), so a 5+-actor scene gets
// roughly this many progress-free turns per participant before it reads as
// stuck, while the configured budget stays the floor for the common 2-4 actor
// case (code_review: 16 total is aggressive across many actors, and the
// wind-down steer starts the moment the budget is exhausted).
const huddleLoopTurnsPerMember = 4

// huddleLoopEnabled reports whether the loop sweep is active. OFF unless
// HuddleLoopTimeout is positive — the master enable, mirroring the degeneracy
// observer's single-positive-number posture. The timeout doubles as the
// persistence gate, so one knob both turns the sweep on and sets how long a loop
// must persist before it is concluded.
func huddleLoopEnabled(s WorldSettings) bool {
	return s.HuddleLoopTimeout > 0
}

// effectiveHuddleLoopSweepCadence returns the configured scan cadence or the
// default when WorldSettings.HuddleLoopSweepCadence is zero/unset.
func effectiveHuddleLoopSweepCadence(s WorldSettings) time.Duration {
	if s.HuddleLoopSweepCadence > 0 {
		return s.HuddleLoopSweepCadence
	}
	return HuddleLoopSweepCadenceDefault
}

// effectiveHuddleLoopMaxTurns returns the configured endurance turn budget or
// the default when WorldSettings.HuddleLoopMaxTurns is zero/unset. The arm has
// no independent off-switch — it rides the sweep's master enable
// (HuddleLoopTimeout), like the ledger arm; an operator who wants it inert can
// set the budget absurdly high.
func effectiveHuddleLoopMaxTurns(s WorldSettings) int {
	if s.HuddleLoopMaxTurns > 0 {
		return s.HuddleLoopMaxTurns
	}
	return HuddleLoopMaxTurnsDefault
}

// effectiveHuddleConversationWindDown returns the configured lingering clock or
// the default when WorldSettings.HuddleConversationWindDown is zero/unset. Like
// the endurance budget the arm has no independent off-switch — it rides the
// sweep's master enable (HuddleLoopTimeout); an operator who wants conversations
// unbounded again sets the window absurdly high.
func effectiveHuddleConversationWindDown(s WorldSettings) time.Duration {
	if s.HuddleConversationWindDown > 0 {
		return s.HuddleConversationWindDown
	}
	return HuddleConversationWindDownDefault
}

// hardConcludeSeconds is when a lingering conversation is actually ended: the
// wind-down window plus the persistence gate the members get to close it
// themselves. 0 when the sweep is disabled — there is then no hard conclude at
// all, and reporting a number would tell an operator the engine will end a
// conversation that it will in fact let run forever.
func hardConcludeSeconds(s WorldSettings, windDown time.Duration) int {
	if !huddleLoopEnabled(s) {
		return 0
	}
	return int((windDown + s.HuddleLoopTimeout) / time.Second)
}

// effectiveHuddleLoopRepeatFraction returns the configured near-duplicate
// fraction threshold (0..1) or the default, clamped to [0,1]. Stored as a
// percent so it sits in the int-valued setting table alongside the other tunables.
func effectiveHuddleLoopRepeatFraction(s WorldSettings) float64 {
	pct := s.HuddleLoopRepeatPercent
	if pct <= 0 {
		pct = HuddleLoopRepeatPercentDefault
	}
	if pct > 100 {
		pct = 100
	}
	return float64(pct) / 100
}

// RunHuddleLoopSweep owns the loop-sweep periodic schedule. Caller starts this
// in a goroutine alongside World.Run (next to RunHuddleSilenceSweep); returns
// when ctx is cancelled. The first sweep is kicked immediately so a huddle
// already looping at startup doesn't wait a full cadence.
func RunHuddleLoopSweep(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, kickHuddleLoopSweep())
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/huddle_loop: initial sweep arm failed: %v", err)
	}
	<-ctx.Done()
}

// kickHuddleLoopSweep returns a Command whose Fn arms the first sweep on the
// world goroutine — mirrors kickHuddleSilenceSweep.
func kickHuddleLoopSweep() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			armNextHuddleLoopSweep(w)
			return nil, nil
		},
	}
}

// armNextHuddleLoopSweep schedules the next sweep after one cadence interval.
// MUST be called from inside a Command.Fn — touches w.huddleLoopSweep.scheduled
// without coordination.
//
// Coalescing: no-op when a sweep is already scheduled. The flag clears at the
// start of the scheduled Fn (evaluateHuddleLoopAndRearm), so a re-arm during
// that Fn queues the next sweep rather than no-opping.
func armNextHuddleLoopSweep(w *World) {
	if w.huddleLoopSweep.scheduled {
		return
	}
	w.huddleLoopSweep.scheduled = true
	cadence := effectiveHuddleLoopSweepCadence(w.Settings)
	// Re-declare the live-tunable cadence on each re-arm (LLM-395) — see
	// armNextEvaluation.
	w.RegisterTicker("huddle_loop_sweep", cadence)
	time.AfterFunc(cadence, func() { fireScheduledHuddleLoopSweep(w) })
}

// fireScheduledHuddleLoopSweep is the AfterFunc callback body. Factored out so
// tests can drive the post-shutdown path directly (matches
// fireScheduledHuddleSilenceSweep). Uses LifecycleContext so a shutdown-while-
// armed unblocks SendContext instead of deadlocking on a send to a dead channel.
func fireScheduledHuddleLoopSweep(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		// Shutdown raced us. Clearing the flag would race with the world
		// goroutine; fresh worlds come from LoadWorld / NewWorld, so a
		// post-shutdown stale flag has no effect.
		return
	}
	w.beatTicker("huddle_loop_sweep")
	_, err := w.SendContext(ctx, evaluateHuddleLoopAndRearm(time.Now()))
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/huddle_loop: scheduled sweep failed: %v", err)
	}
}

// evaluateHuddleLoopAndRearm clears the scheduled flag, runs one sweep, and
// re-arms — all in one Fn on the world goroutine. Clearing the flag first means
// the re-arm starts a fresh chain rather than seeing the still-set flag and
// no-opping.
func evaluateHuddleLoopAndRearm(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.huddleLoopSweep.scheduled = false
			res, err := EvaluateHuddleLoopSweep(now).Fn(w)
			armNextHuddleLoopSweep(w)
			return res, err
		},
	}
}

// EvaluateHuddleLoopSweep returns a Command that concludes every active huddle
// caught in a sustained conversational livelock. Exposed as a Command (not just
// an internal Fn) so tests can drive sweeps deterministically without the
// AfterFunc timing chain.
//
// Per active huddle: huddleLoopContentPresent (the DURABLE condition — repetitive +
// progress-free, NOT recency-gated) decides whether to stamp/hold LoopingSince. A
// huddle that isn't content-present has it cleared (a genuine recovery — varied
// content or a progress event newer than the ring). The durable condition (vs the
// live one) is what lets the spell survive a churned clique's brief silence between
// concluding one huddle and re-forming the next (LLM-170), so the gate counts
// continuously across the churn. Conclusion additionally requires the loop to be LIVE
// (huddleLoopArmed) — a repetitive huddle that has merely gone quiet is the silence
// sweep's job. Once the spell has persisted HuddleLoopTimeout AND is live, conclude.
//
// Collect-then-conclude: concludeHuddleInner emits HuddleConcluded and a
// subscriber could in principle mutate w.Huddles, so the looping set is gathered
// before any conclusion runs. Iteration order is sorted by HuddleID for a stable
// conclusion order (replay-test + admin-trace readability). No-op when the
// observer is disabled.
func EvaluateHuddleLoopSweep(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if !huddleLoopEnabled(w.Settings) || len(w.Huddles) == 0 {
				return nil, nil
			}
			timeout := w.Settings.HuddleLoopTimeout
			// LLM-309: the silent transactional-futility signal, computed once for
			// every huddle so the per-huddle loop below is a map lookup rather than a
			// per-huddle ledger walk. OR'd into the utterance arm's onset + conclude
			// gates so a zero-utterance offer→decline loop rides the same machinery.
			ledgerPresentHuddles, ledgerArmedHuddles := ledgerStandoffHuddles(w, now)
			// LLM-397: the live-deal guard for the lingering arm, computed once for
			// the whole scan like the standoff sets above. Only the lingering arm
			// consults it — the other three conclude BECAUSE commerce is going
			// nowhere, which is the opposite verdict from "leave this deal alone."
			commerceHuddles := ledgerCommerceHuddles(w)
			var looping []HuddleID
			for id, h := range w.Huddles {
				if h == nil || h.ConcludedAt != nil {
					continue
				}
				// Onset/clear are gated on huddleLoopContentPresent — the DURABLE
				// condition (repetitive ring + progress guard), NOT recency. So the
				// spell survives the brief quiet between a churned huddle concluding
				// and the same clique re-forming + speaking again (LLM-170): the
				// carried ring keeps the condition true across the gap, so the gate
				// counts continuously instead of restarting each cycle. A real
				// recovery (varied content, or a transaction/new-member progress
				// event newer than the ring) flips it false and clears the spell.
				// LLM-185: a player-attended huddle (a PC spoke within huddlePCAttentionWindow)
				// is an active human conversation — never conclude it. Clear any spell so the
				// gate restarts fresh once the player goes quiet; a silent parked PC's NPC
				// loops then get swept after a full timeout, so a hub like the tavern isn't
				// permanently immune.
				if huddlePCAttended(h, now) {
					h.LoopingSince = nil
					h.LoopingReason = ""
					continue
				}
				// LLM-309: OR the durable transactional-futility condition into the
				// onset gate — a silent decline loop holds LoopingSince exactly as a
				// repetitive utterance ring does. LLM-333: the endurance arm's
				// exhausted turn budget ORs in the same way. A huddle that is none of
				// content-present, ledger-standoff, or budget-exhausted has its spell
				// cleared.
				_, ledgerPresent := ledgerPresentHuddles[id]
				// LLM-397: the lingering arm ORs into the same onset, with one
				// difference — a huddle mid-deal is never lingering. The wind-down
				// would tell a buyer to say farewell with coin already on the table,
				// and the backstop would strand the pending entry. The pathology arms
				// need no such guard: a standoff IS the dead deal.
				lingeringPresent := huddleLingeringPresent(w.Settings, h, now) &&
					!huddleCarriesLiveCommerce(w, h, commerceHuddles)
				if !huddleLoopContentPresent(w.Settings, h) && !ledgerPresent &&
					!huddleEndurancePresent(w.Settings, h) && !lingeringPresent {
					h.LoopingSince = nil
					h.LoopingReason = ""
					continue
				}
				if h.LoopingSince == nil {
					t := now
					h.LoopingSince = &t
					// Latch the onset cause for the conclusion telemetry — the arms
					// can drift apart over a spell, and re-diagnosing at conclude
					// time would misattribute which detector caught the incident
					// (LLM-333 code_review). Precedence mirrors the conclude tag:
					// ledger > lexical > endurance > lingering. Lingering is last
					// because it is the only non-pathological reading: if any arm
					// says the conversation is actually STUCK, that is the truer
					// diagnosis and the one the incident should be filed under.
					switch {
					case ledgerPresent:
						h.LoopingReason = "huddle_loop_ledger"
					case huddleLoopContentPresent(w.Settings, h):
						h.LoopingReason = "huddle_loop"
					case huddleEndurancePresent(w.Settings, h):
						h.LoopingReason = "huddle_loop_endurance"
					default:
						h.LoopingReason = huddleLoopReasonLingering
					}
				}
				// Conclude only a spell that has BOTH persisted past the gate AND is
				// live right now — a repetitive huddle that has merely gone quiet is
				// the silence sweep's job, not a livelock to break. huddleLoopArmed
				// (content-present + live) is the same signal the LLM-169 per-tick
				// steer arms on, so the gentle nudge and this destructive conclude
				// stay coupled.
				_, ledgerArmed := ledgerArmedHuddles[id]
				if now.Sub(*h.LoopingSince) >= timeout &&
					(huddleLoopArmed(w.Settings, h, now) || ledgerArmed ||
						huddleEnduranceArmed(w.Settings, h, now) ||
						(lingeringPresent && huddleNewestUtteranceLive(h, now))) {
					looping = append(looping, id)
				}
			}
			if len(looping) == 0 {
				return nil, nil
			}
			sort.Slice(looping, func(i, j int) bool { return looping[i] < looping[j] })
			for _, id := range looping {
				// Re-check: an earlier conclusion's subscriber could have already
				// concluded/removed this one (defensive, matches the silence sweep).
				h, ok := w.Huddles[id]
				if !ok || h == nil || h.ConcludedAt != nil {
					continue
				}
				// Telemetry BEFORE conclude — concludeHuddleInner clears Members.
				// The reason is the LATCHED onset cause (h.LoopingReason) so the
				// record names the arm that actually caught the incident, not a
				// re-diagnosis at conclude time. One override: tag
				// "huddle_loop_ledger" whenever the transactional arm (LLM-309) is
				// armed at conclusion, so the ledger shape is never hidden in a
				// mixed chatty+transactional loop; the per-record `utterances`
				// count still distinguishes a truly silent loop (0) from a mixed
				// one (>0). "huddle_loop_endurance" (LLM-333) marks an incident only
				// the turn budget could see, keeping paraphrase-loop kills separable.
				reason := h.LoopingReason
				if _, ok := ledgerArmedHuddles[id]; ok {
					reason = "huddle_loop_ledger"
				}
				if reason == "" {
					reason = "huddle_loop"
				}
				emitHuddleLoopTelemetry(w, h, now, reason)
				// Member set BEFORE conclude, for the post-conclude warrant clear.
				members := make([]ActorID, 0, len(h.Members))
				for memberID := range h.Members {
					members = append(members, memberID)
				}
				structureID := h.StructureID
				concludeHuddleInner(w, id, now, false)
				// LLM-333: make the conclusion stick. Pending npc_spoke warrants
				// survive concludeHuddleInner (nothing filters them downstream), so
				// each member would burn one more paced tick and a re-speak would
				// re-form the huddle with the carried ring — the conclusion leaks.
				// Drop each member's pending cycle when it consists ONLY of
				// social-cadence kinds: those are beats of the conversation the sweep
				// just ended, the exact "moment has passed" case the staleness paths
				// clear at zero cost. A cycle holding ANY other kind (a red need, a
				// pay offer, a Force nudge) is left whole — the tick it drives is
				// real work, not an echo of the dead conversation.
				clearSocialWarrantCycles(w, members)
				// concludeHuddleInner wrote a carry-over (LLM-170).
				//
				// LLM-397: a LINGERING conclude drops it entirely. The carry-over
				// exists so a clique that churns huddles mid-conversation isn't
				// re-greeted as strangers — but this conversation is not mid-anything,
				// it is over: it ran its full window, was told to wind down for a whole
				// persistence gate, and didn't. Preserving the ring and the clock here
				// is precisely what made the old eco arc toothless — the clique
				// re-formed within seconds, inherited an already-elapsed clock, and got
				// cut again three minutes later, ten times over. Dropping it means the
				// next huddle at this structure is a genuinely NEW conversation: a fresh
				// clock, a fresh window, and yes, a greeting — which is what actually
				// happens when people finish talking and later strike up again.
				//
				// Every other reason keeps today's behavior: hold the ring, reset the
				// carried loop clock + latched reason so a re-formed loop earns one
				// fresh timeout rather than an instantly-elapsed inherited spell. The
				// endurance counter stays — it is the durable CONDITION, not the clock.
				if reason == huddleLoopReasonLingering {
					delete(w.carryoverByStructure, structureID)
				} else if cb := w.carryoverByStructure[structureID]; cb != nil {
					cb.loopingSince = nil
					cb.loopingReason = ""
				}
			}
			return nil, nil
		},
	}
}

// huddleLoopRepetitive reports whether the recent-utterance ring is full enough to
// judge (huddleLoopMinUtterances) and repetitive past the configured threshold — the
// content SHAPE of a loop, independent of recency or progress. The building block
// shared by the live predicate (huddleConversationLooping) and the durable one
// (huddleLoopContentPresent).
func huddleLoopRepetitive(s WorldSettings, h *Huddle) bool {
	if h == nil || h.ConcludedAt != nil {
		return false
	}
	if len(h.RecentUtterances) < huddleLoopMinUtterances {
		return false
	}
	return huddleUtteranceRepetition(h.RecentUtterances) >= effectiveHuddleLoopRepeatFraction(s)
}

// huddleNewestUtteranceLive reports whether the newest spoken line is recent enough
// (huddleLoopLiveWindow) for the huddle to count as ACTIVELY looping — a quiet huddle
// is the silence sweep's domain. A negative age (now earlier than the newest line,
// possible with caller-supplied / replayed timestamps) is treated as not-live so a
// loop never arms off an out-of-order clock.
func huddleNewestUtteranceLive(h *Huddle, now time.Time) bool {
	ring := h.RecentUtterances
	if len(ring) == 0 {
		return false
	}
	age := now.Sub(ring[len(ring)-1].At)
	return age >= 0 && age <= huddleLoopLiveWindow
}

// huddleNewestAfterProgress reports whether the newest utterance post-dates the last
// non-conversational progress (a transaction or a genuinely-new participant —
// Huddle.LastProgressAt). When it does not, the repetitive ring is stale relative to
// progress: the huddle advanced and has produced no fresh repetitive speech since, so
// it is not stuck. A zero LastProgressAt (none ever recorded) trivially passes.
func huddleNewestAfterProgress(h *Huddle) bool {
	ring := h.RecentUtterances
	if len(ring) == 0 {
		return false
	}
	newestAt := ring[len(ring)-1].At
	return h.LastProgressAt.IsZero() || newestAt.After(h.LastProgressAt)
}

// huddleConversationLooping reports whether the huddle's conversation is, right now, a
// repetitive LIVE loop: repetitive ring + the newest line is recent. The progress
// guard is NOT applied here — this is the point-in-time "does it look like an active
// loop" predicate. Exported for tests.
func huddleConversationLooping(s WorldSettings, h *Huddle, now time.Time) bool {
	return huddleLoopRepetitive(s, h) && huddleNewestUtteranceLive(h, now)
}

// huddleLoopContentPresent is the DURABLE loop condition that gates the sweep's
// LoopingSince onset: a repetitive ring whose newest line post-dates the last
// progress — with NO recency requirement. Unlike huddleLoopArmed it stays true while
// the huddle is briefly quiet (LLM-170: the gap between a churned huddle concluding
// and the same clique re-forming + speaking again, bridged by the carried ring), so
// the persistence gate counts continuously across the churn instead of restarting
// each cycle. It flips false only on a genuine recovery — the repetition drops
// (varied content) or a progress event post-dates the ring.
func huddleLoopContentPresent(s WorldSettings, h *Huddle) bool {
	return huddleLoopRepetitive(s, h) && huddleNewestAfterProgress(h)
}

// huddleLoopArmed reports whether the huddle is, right now, in a repetitive LIVE loop
// whose repetition post-dates the last progress — content-present AND live. The
// armed-now signal shared by the sweep's conclude gate and the per-tick perception
// steer (republish → ActorSnapshot.ConversationLooping, LLM-169), so the gentle
// "you've agreed, act now" nudge and the destructive silent conclude fire on the same
// condition. Without the progress guard a single mid-loop transaction would only
// postpone conclusion by one timeout while the unchanged repetitive ring re-armed.
// huddlePCAttentionWindow is how long after a PLAYER (KindPC) member's last spoken
// line a huddle stays "player-attended" and thus exempt from the loop sweep + the
// ConversationLooping steer (LLM-185). Keyed on recent PC SPEECH, not mere PC
// membership, so a parked-and-silent PC at a hub (the tavern) doesn't permanently
// shield NPC loops there. 3 minutes — long enough to span a human's read-and-reply
// pause, short enough that a player who wanders off frees the huddle to be swept.
const huddlePCAttentionWindow = 3 * time.Minute

// huddlePCAttended reports whether a player spoke in this huddle within
// huddlePCAttentionWindow — i.e. it's an active player conversation the loop sweep +
// steer must leave alone (LLM-185). A negative age (out-of-order / replayed clock)
// is treated as not-attended, matching huddleNewestUtteranceLive.
func huddlePCAttended(h *Huddle, now time.Time) bool {
	if h == nil || h.LastPCUtteranceAt.IsZero() {
		return false
	}
	age := now.Sub(h.LastPCUtteranceAt)
	return age >= 0 && age < huddlePCAttentionWindow
}

func huddleLoopArmed(s WorldSettings, h *Huddle, now time.Time) bool {
	return huddleLoopContentPresent(s, h) && huddleNewestUtteranceLive(h, now)
}

// --- endurance arm (LLM-333) ---
//
// The utterance arm is lexical: it needs most ring turns to share half their
// vocabulary with a twin (Jaccard >= 0.5). The 2026-07-08 live loops proved a
// creative model never re-words a farewell closely enough to trip it — the
// John+Elizabeth loop ("Safe home… give the cows a scratch" / "Godspeed…
// mind the muddy spots") measured 0.00 repetition in EVERY ring window against
// the 0.60 threshold, and no threshold tune fixes that without also catching
// healthy talk. This arm is content-BLIND: a huddle that has accumulated
// HuddleLoopMaxTurns spoken lines with no progress event (completed
// transaction, genuine membership change, player line — the TurnsSinceProgress
// resets) is stuck no matter how varied its wording, because an unbounded loop
// always exceeds any turn budget while a healthy scene keeps producing progress
// or ends. Same durable/live split as the other two arms, OR'd into the same
// LoopingSince onset, silent conclude, and per-tick steer — the steer renders
// its own wind-down line (ConversationRunLong) rather than the loop arm's
// "you keep saying the same thing", which would be false for varied talk.

// huddleEndurancePresent is the endurance arm's DURABLE onset condition: the
// spend-without-progress budget is exhausted. Counter-based, so it stays true
// across a churned clique's brief silence (the LLM-170 carry-over carries the
// counter) exactly as the carried ring keeps huddleLoopContentPresent true.
// The budget scales with the member count (huddleLoopTurnsPerMember, floored at
// the configured budget) so a crowded-but-healthy scene isn't read as stuck at
// a per-actor allowance the 2-actor default never intended.
func huddleEndurancePresent(s WorldSettings, h *Huddle) bool {
	if h == nil || h.ConcludedAt != nil {
		return false
	}
	budget := effectiveHuddleLoopMaxTurns(s)
	if scaled := len(h.Members) * huddleLoopTurnsPerMember; scaled > budget {
		budget = scaled
	}
	return h.TurnsSinceProgress >= budget
}

// huddleEnduranceArmed is the endurance arm's LIVE conclude + steer condition:
// budget exhausted AND the conversation is still speaking right now. A huddle
// that overran its budget and then went quiet is the silence sweep's domain,
// matching the other arms' live gates.
func huddleEnduranceArmed(s WorldSettings, h *Huddle, now time.Time) bool {
	return huddleEndurancePresent(s, h) && huddleNewestUtteranceLive(h, now)
}

// --- lingering arm (LLM-397) ---
//
// The other three arms all detect a PATHOLOGY: the conversation is repeating
// (lexical), standing off over a dead deal (ledger), or burning turns with
// nothing happening (endurance). None of them fires on a conversation that is
// healthy, productive, and simply endless — and that is the live case. The
// 2026-07-14 inn conversation sold porridge (which stamps LastProgressAt and so
// RESETS the endurance counter, by design), never repeated itself (the lexical
// arm measured near-zero), and carried no failed deals — and then ran for a
// hundred minutes, because there is no such thing in the model as a conversation
// that has simply gone on long enough.
//
// This arm supplies that: a clock on the CONVERSATION, blind to content and to
// progress, and unlike the other three it is not a verdict of anything being
// wrong. Its purpose is the steer — arming ConversationLingering so the members
// close the scene themselves, in the fiction, which they demonstrably do when
// told ("I'll let this fine meal settle... I'll see you back at the house this
// evening"). The silent conclude one persistence-gate later is only the backstop
// for a scene that won't take the hint.

// huddleLingeringPresent is the lingering arm's DURABLE onset condition: the
// conversation has been going longer than the wind-down window. Read off
// ConversationSince (carried across re-formation), NOT StartedAt — a clique that
// churns huddles every couple of minutes must not present a fresh clock each
// cycle, which is exactly how the live conversation stayed young forever while
// running for an hour and a half. Falls back to StartedAt for a huddle minted
// before this field existed (or by a creation site that forgot to stamp), so the
// arm degrades to per-huddle age rather than never arming.
func huddleLingeringPresent(s WorldSettings, h *Huddle, now time.Time) bool {
	if h == nil || h.ConcludedAt != nil {
		return false
	}
	since := h.ConversationSince
	if since.IsZero() {
		since = h.StartedAt
	}
	if since.IsZero() {
		return false
	}
	age := now.Sub(since)
	// A negative age (out-of-order / replayed clock) reads as not-lingering,
	// matching huddleNewestUtteranceLive — a replay must never arm the arm early.
	return age >= 0 && age >= effectiveHuddleConversationWindDown(s)
}

// huddleLingeringArmed is the lingering arm's LIVE steer + conclude condition:
// the conversation has run past the wind-down window AND is still being spoken
// right now. A long conversation that has gone quiet needs no wind-down — it
// wound down — and belongs to the silence sweep, matching the other arms.
func huddleLingeringArmed(s WorldSettings, h *Huddle, now time.Time) bool {
	return huddleLingeringPresent(s, h, now) && huddleNewestUtteranceLive(h, now)
}

// --- transactional-futility arm (LLM-309) ---
//
// The utterance arm above is blind to an all-mechanical offer→decline loop: with
// zero spoken lines the RecentUtterances ring never fills, so huddleLoopRepetitive
// is always false. The live case (Elizabeth Ellis → Josiah Thorne, General Store,
// 2026-07-06) ran eleven pay_with_item→decline_pay rounds in five minutes with
// recent_utterance_count 0 the whole time — every tool call SUCCEEDED (a valid
// ledger entry, an audience, no movement), so the per-actor degeneracy observer
// scored each tick productive too. This arm gives the sweep a second, ledger-based
// signal with the SAME durable/live split, OR'd into the same LoopingSince onset,
// silent-conclude, and per-tick ConversationLooping steer as the utterance arm.

// huddleLoopLedgerFutileState reports whether a pay-ledger state is a non-completed
// terminal that signals a stuck negotiation — a seller decline or a buyer/seller
// material shortfall. Accepted is the completed sale (progress, not futility);
// Pending is non-terminal; Countered is a live negotiation move, not a dead-end;
// Expired and WithdrawnByBuyer are abandonment rather than repeated rejection; and
// FailedUnavailable is a one-off broken-context terminal. The counted set mirrors
// exactly the terminals coPresentBuyStandoff (LLM-297) treats as a standoff —
// declined plus the three insufficient-* failures — so the detector and the
// buyer-side "the deal isn't meeting" cue read the same dead-ends.
func huddleLoopLedgerFutileState(st PayLedgerState) bool {
	switch st {
	case PayLedgerStateDeclined,
		PayLedgerStateFailedInsufficientFunds,
		PayLedgerStateFailedInsufficientStock,
		PayLedgerStateFailedInsufficientGoods:
		return true
	default:
		return false
	}
}

// withinHuddleLoopLiveWindow reports whether t is recent enough (within
// huddleLoopLiveWindow of now, and not in the future) to count as live — the same
// "actively happening right now" bound huddleNewestUtteranceLive applies to the
// newest spoken line, applied here to the newest futile pay terminal. A negative
// age (out-of-order / replayed clock) reads as not-live, matching the utterance side.
func withinHuddleLoopLiveWindow(t, now time.Time) bool {
	if t.IsZero() {
		return false
	}
	age := now.Sub(t)
	return age >= 0 && age <= huddleLoopLiveWindow
}

// ledgerStandoffHuddles does ONE pass over the pay ledger and returns the huddles
// caught in a silent transactional-futility loop — the ledger analog of the
// utterance arm's content-present / armed split, computed for every huddle at once.
//
// A huddle enters `present` (the DURABLE onset condition, gating LoopingSince) when
// some (buyer, seller, item) negotiation inside it has amassed at least
// huddleLoopLedgerMinTerminals futile terminals resolved within
// huddleLoopLedgerRecencyWindow of now, all post-dating the huddle's LastProgressAt.
// It additionally enters `armed` (the LIVE conclude + steer condition) when that
// negotiation's newest terminal is within huddleLoopLiveWindow — the loop is still
// rejecting right now, not a stale cluster.
//
// The progress guard reuses Huddle.LastProgressAt, which is stamped ONLY by a
// completed transaction (touchHuddleProgress) or a genuinely-new member joining
// (JoinHuddle) — never by a decline (finalizePayLedgerTerminal touches only the
// entry's own ResolvedAt). So a negotiation that actually closed, or a huddle whose
// composition changed, drops every pre-progress terminal from the tally and is not
// read as a loop — exactly the "no intervening completed transaction or membership
// change" requirement, satisfied by the same field the utterance arm uses.
//
// One shared O(ledger) pass — rather than a per-huddle / per-actor walk — keeps the
// hot republish path cheap (a busy village's terminal-retention window can hold
// hundreds of entries). Generalizes LLM-297's coPresentBuyStandoff ledger walk (a
// perception-snapshot walk over a single buyer→seller pair) to every huddle on the
// world map. MUST run on the world goroutine (reads w.PayLedger + w.Huddles).
func ledgerStandoffHuddles(w *World, now time.Time) (present, armed map[HuddleID]struct{}) {
	if len(w.PayLedger) == 0 {
		return nil, nil
	}
	type standoffKey struct {
		huddle HuddleID
		buyer  ActorID
		seller ActorID
		item   ItemKind
	}
	counts := make(map[standoffKey]int)
	newest := make(map[standoffKey]time.Time)
	cutoff := now.Add(-huddleLoopLedgerRecencyWindow)
	for _, e := range w.PayLedger {
		if e == nil || e.HuddleID == "" || !huddleLoopLedgerFutileState(e.State) {
			continue
		}
		// A zero ResolvedAt is a still-pending / mid-construction entry; an older
		// one has decayed out of the recency window; a future-dated one (out-of-
		// order / replayed clock) must not count toward the durable `present` set
		// either, matching withinHuddleLoopLiveWindow's negative-age rejection on
		// the live side — otherwise a replay could stamp LoopingSince early.
		if e.ResolvedAt.IsZero() || e.ResolvedAt.Before(cutoff) || e.ResolvedAt.After(now) {
			continue
		}
		h := w.Huddles[e.HuddleID]
		if h == nil || h.ConcludedAt != nil {
			continue
		}
		// A terminal at or before the huddle's last completed progress is
		// pre-progress and does not count — the deal advanced or the room changed.
		if !h.LastProgressAt.IsZero() && !e.ResolvedAt.After(h.LastProgressAt) {
			continue
		}
		k := standoffKey{e.HuddleID, e.BuyerID, e.SellerID, e.ItemKind}
		counts[k]++
		if e.ResolvedAt.After(newest[k]) {
			newest[k] = e.ResolvedAt
		}
	}
	for k, c := range counts {
		if c < huddleLoopLedgerMinTerminals {
			continue
		}
		if present == nil {
			present = make(map[HuddleID]struct{})
		}
		present[k.huddle] = struct{}{}
		if withinHuddleLoopLiveWindow(newest[k], now) {
			if armed == nil {
				armed = make(map[HuddleID]struct{})
			}
			armed[k.huddle] = struct{}{}
		}
	}
	return present, armed
}

// huddleUtteranceRepetition returns the fraction (0..1) of the ring's content-
// bearing utterances that are near-duplicates of at least one OTHER utterance —
// i.e. share content-token Jaccard >= huddleLoopNearDupJaccard with some peer.
//
// A healthy conversation advances: each turn introduces new content, so few
// turns have a near-duplicate twin and the fraction stays low (~0.1). A livelock
// restates the same intent, so nearly every turn has a twin and the fraction
// approaches 1.0. This near-duplicate FRACTION is deliberately used instead of a
// mean pairwise Jaccard: a single varied turn (e.g. "...let's head out") drags
// the mean below threshold even while the loop is obvious — a real Walker window
// measured ~0.48 mean yet 1.0 near-duplicate fraction. Utterances that normalize
// to no content tokens (pure filler like "Yes.") are excluded from both the
// numerator and the denominator.
func huddleUtteranceRepetition(ring []Utterance) float64 {
	sets := make([]map[string]struct{}, 0, len(ring))
	for _, u := range ring {
		if toks := contentTokens(u.Text); len(toks) > 0 {
			sets = append(sets, toks)
		}
	}
	if len(sets) < 2 {
		return 0
	}
	dup := 0
	for i := range sets {
		for j := range sets {
			if i == j {
				continue
			}
			if jaccardSet(sets[i], sets[j]) >= huddleLoopNearDupJaccard {
				dup++
				break
			}
		}
	}
	return float64(dup) / float64(len(sets))
}

// contentTokens lowercases the text, strips punctuation, and returns the set of
// content tokens — words that carry meaning, with function/filler words
// (articles, pronouns, auxiliaries, politeness) and single characters dropped.
// Set semantics: a token repeated within one utterance counts once, which is
// what Jaccard wants. The filler set is deliberately SMALL and excludes content
// verbs/nouns ("go", "market", "ready", …) so a near-repeat like "let's go" vs
// "let's go to the market" still registers as similar.
func contentTokens(text string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, raw := range strings.Fields(strings.ToLower(text)) {
		tok := strings.Map(keepAlphaNum, raw)
		if len(tok) < 2 {
			continue
		}
		if _, filler := huddleLoopStopwords[tok]; filler {
			continue
		}
		out[tok] = struct{}{}
	}
	return out
}

// keepAlphaNum maps a rune to itself when it is an ASCII letter or digit, else
// drops it (returns -1). The caller lowercases first, so only a-z/0-9 survive —
// in particular apostrophes are stripped, folding "let's" to "lets".
func keepAlphaNum(r rune) rune {
	if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
		return r
	}
	return -1
}

// jaccardSet returns the Jaccard similarity (|A∩B| / |A∪B|) of two token sets,
// 0 when either is empty.
func jaccardSet(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// huddleLoopStopwords are the function/filler words dropped from utterances
// before the repetition comparison. Kept intentionally small — only words that
// carry no topical meaning. Content verbs and nouns (including "go", "market",
// "want", "shall", …) are NOT here: they are exactly the tokens whose repetition
// signals a loop.
var huddleLoopStopwords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "and": {}, "or": {}, "but": {}, "to": {},
	"of": {}, "in": {}, "on": {}, "at": {}, "for": {}, "with": {}, "as": {},
	"is": {}, "am": {}, "are": {}, "be": {}, "was": {}, "were": {}, "do": {},
	"does": {}, "did": {}, "have": {}, "has": {}, "had": {}, "i": {}, "im": {},
	"id": {}, "you": {}, "your": {}, "youre": {}, "we": {}, "our": {}, "us": {},
	"my": {}, "me": {}, "it": {}, "its": {}, "this": {}, "that": {}, "these": {},
	"those": {}, "here": {}, "there": {}, "then": {}, "so": {}, "just": {},
	"ok": {}, "okay": {}, "yes": {}, "no": {}, "please": {}, "thank": {},
	"thanks": {}, "hello": {}, "hi": {}, "hey": {}, "lets": {}, "let": {},
	"well": {}, "now": {},
	// Apostrophe-folded contractions: keepAlphaNum strips the apostrophe before
	// stopword matching, so "I'll" -> "ill", "don't" -> "dont", etc. Filtering
	// these stops conversational boilerplate from creating false content overlap.
	"ill": {}, "youll": {}, "theyll": {}, "ive": {}, "youve": {},
	"weve": {}, "theyve": {}, "cant": {}, "dont": {}, "wont": {}, "didnt": {},
	"doesnt": {}, "isnt": {}, "arent": {}, "wasnt": {}, "werent": {}, "havent": {},
	"hasnt": {}, "hadnt": {}, "shouldnt": {}, "couldnt": {}, "wouldnt": {},
	"thats": {}, "whats": {}, "hes": {}, "shes": {}, "theres": {}, "heres": {},
}

// clearSocialWarrantCycles drops each listed actor's pending warrant cycle when
// every warrant in it is a social-cadence kind (isSocialCadenceWarrantKind) and
// none is Force — see the callsite in EvaluateHuddleLoopSweep for the why. A
// mixed cycle is kept WHOLE rather than filtered down: partial pruning is the
// shelved-stale path's semantics (retainForcedWarrants), and here the non-social
// warrant will drive a tick within seconds anyway, consuming the batch. MUST run
// on the world goroutine.
func clearSocialWarrantCycles(w *World, members []ActorID) {
	for _, id := range members {
		a := w.Actors[id]
		if a == nil || len(a.Warrants) == 0 {
			continue
		}
		droppable := true
		for _, m := range a.Warrants {
			if m.Force || !isSocialCadenceWarrantKind(m.Kind()) {
				droppable = false
				break
			}
		}
		if droppable {
			clearWarrant(a)
		}
	}
}

// emitHuddleLoopTelemetry writes a `stuck` tick-telemetry record per member of a
// huddle being concluded as a livelock, so the loop surfaces in the umbilical
// the same way the degeneracy observer's escalations do. Kind reuses "stuck";
// reason is "huddle_loop" for the chatty utterance arm and "huddle_loop_ledger"
// for the silent transactional arm (LLM-309), so the two shapes stay separable.
// Best-effort and redacted (labels only) like all tick telemetry; no-op when no
// sink is wired.
func emitHuddleLoopTelemetry(w *World, h *Huddle, now time.Time, reason string) {
	if w.repo.TickTelemetry == nil {
		return
	}
	members := strconv.Itoa(len(h.Members))
	utterances := strconv.Itoa(len(h.RecentUtterances))
	for id := range h.Members {
		w.repo.TickTelemetry.WriteTickTelemetry(TickTelemetryRecord{
			At:      now,
			ActorID: id,
			Kind:    "stuck",
			Detail: map[string]string{
				"reason":     reason,
				"huddle":     string(h.ID),
				"members":    members,
				"utterances": utterances,
			},
		})
	}
}
