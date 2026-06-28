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
// Detection is deterministic and content-based: a huddle is "looping" when its
// RecentUtterances ring is highly repetitive (most turns near-duplicate another
// turn — see huddleUtteranceRepetition) AND no non-conversational progress (a
// transaction or membership change — Huddle.LastProgressAt) has happened during
// the repetition spell. A persistence gate (Huddle.LoopingSince, must hold for
// HuddleLoopTimeout) keeps it high-precision: a brief repetitive patch is spared,
// only a sustained livelock is concluded. Conclusion is SILENT (no per-member
// warrant), like the silence sweep — breaking the loop must not itself wake the
// members into a fresh re-pitch round; their next genuine warrant (a need, a
// schedule duty) drives real behavior. A `stuck` tick-telemetry record is
// emitted per member so the loop surfaces in the umbilical exactly where the
// degeneracy observer's records do.
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
// Per active huddle: huddleConversationLooping decides whether the conversation
// is repetitive-and-live right now. A huddle that isn't has its LoopingSince
// cleared (a recovery — the loop broke on its own). A huddle that is gets
// LoopingSince stamped on first sight; a progress event (transaction / membership
// change) recorded AFTER that onset resets it (the huddle is advancing after
// all). Once the spell has persisted HuddleLoopTimeout the huddle is concluded.
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
			var looping []HuddleID
			for id, h := range w.Huddles {
				if h == nil || h.ConcludedAt != nil {
					continue
				}
				// huddleLoopArmed folds the point-in-time looping predicate and
				// the post-dates-progress guard. Extracted (LLM-169) so the
				// per-tick perception steer (republish → ActorSnapshot.
				// ConversationLooping) arms on the EXACT same condition this sweep
				// does — the gentle "you've agreed, act now" nudge and this
				// destructive silent conclude read one signal.
				if !huddleLoopArmed(w.Settings, h, now) {
					h.LoopingSince = nil
					continue
				}
				if h.LoopingSince == nil {
					t := now
					h.LoopingSince = &t
				}
				if now.Sub(*h.LoopingSince) >= timeout {
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
				emitHuddleLoopTelemetry(w, h, now)
				concludeHuddleInner(w, id, now, false)
			}
			return nil, nil
		},
	}
}

// huddleConversationLooping reports whether the huddle's conversation is, right
// now, a repetitive live loop: the RecentUtterances ring is full enough to judge
// (huddleLoopMinUtterances), the last spoken line is recent (huddleLoopLiveWindow
// — a quiet huddle belongs to the silence sweep), and the near-duplicate fraction
// meets the configured threshold. Persistence (LoopingSince) and the progress
// guard (LastProgressAt) are applied by the caller, not here — this is the
// point-in-time "does it look like a loop" predicate.
func huddleConversationLooping(s WorldSettings, h *Huddle, now time.Time) bool {
	if h == nil || h.ConcludedAt != nil {
		return false
	}
	ring := h.RecentUtterances
	if len(ring) < huddleLoopMinUtterances {
		return false
	}
	// A negative age (now earlier than the newest utterance — possible with
	// caller-supplied/replayed timestamps) is not "live": treat it the same as
	// stale so a loop never arms off an out-of-order clock.
	age := now.Sub(ring[len(ring)-1].At)
	if age < 0 || age > huddleLoopLiveWindow {
		return false
	}
	return huddleUtteranceRepetition(ring) >= effectiveHuddleLoopRepeatFraction(s)
}

// huddleLoopArmed reports whether the huddle is, right now, in a repetitive live
// loop whose repetition POST-DATES the last non-conversational progress — the
// point-in-time "this is a stuck loop" condition, with the progress guard the
// loop sweep applies before stamping LoopingSince. A loop spell is only armed by
// repetition newer than the last progress event (a transaction or membership
// change, Huddle.LastProgressAt): if the newest utterance is not newer than that,
// the repetitive ring is stale relative to the progress — the huddle advanced and
// has produced no fresh repetitive speech since, so it is not stuck. Without this
// a single mid-loop transaction would only postpone conclusion by one timeout
// while the unchanged, still-repetitive ring re-armed against itself.
//
// Persistence (LoopingSince / HuddleLoopTimeout) stays in the sweep; this is the
// armed-now predicate shared by the sweep's persistence gate and the per-tick
// perception steer (republish → ActorSnapshot.ConversationLooping, LLM-169), so
// the gentle "you've agreed, act now" nudge and the destructive silent conclude
// fire on the same signal. The ring is non-empty whenever huddleConversationLooping
// is true (it requires >= huddleLoopMinUtterances), so the newest-utterance index
// is safe.
func huddleLoopArmed(s WorldSettings, h *Huddle, now time.Time) bool {
	if !huddleConversationLooping(s, h, now) {
		return false
	}
	newestAt := h.RecentUtterances[len(h.RecentUtterances)-1].At
	if !h.LastProgressAt.IsZero() && !newestAt.After(h.LastProgressAt) {
		return false
	}
	return true
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

// emitHuddleLoopTelemetry writes a `stuck` tick-telemetry record per member of a
// huddle being concluded as a livelock, so the loop surfaces in the umbilical
// the same way the degeneracy observer's escalations do. Kind reuses "stuck";
// Detail.reason="huddle_loop" distinguishes it. Best-effort and redacted (labels
// only) like all tick telemetry; no-op when no sink is wired.
func emitHuddleLoopTelemetry(w *World, h *Huddle, now time.Time) {
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
				"reason":     "huddle_loop",
				"huddle":     string(h.ID),
				"members":    members,
				"utterances": utterances,
			},
		})
	}
}
