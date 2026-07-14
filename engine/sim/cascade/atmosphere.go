package cascade

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// atmosphere.go — world-level atmosphere refresh cascade slice. The
// sim-package primitives (FetchAtmosphereContext + ApplyAtmosphereRefresh
// Commands, AtmosphereContext) live in engine/sim/atmosphere.go; this
// file owns the long-running goroutine that drives the periodic refresh
// and the LLM-call adapter against salem-generic.
//
// Why off-world: the LLM call blocks for seconds. Running it on the
// world goroutine would freeze the engine. The sweep ticker runs on a
// dedicated goroutine, bounces to the world for context data (via
// FetchAtmosphereContext), issues the LLM call off-world against
// salem-generic, then bounces back to the world to apply the result
// (via ApplyAtmosphereRefresh). Same shape as the consolidation cascade
// slice — but world-scoped instead of per-relationship.
//
// Lifecycle:
//
//   RegisterAtmosphere(ctx, w, client)
//   ├─> w.Subscribe(PhaseApplied | WeatherChanged → nudge refresh chan)
//   │                                       (ZBBS-WORK-379 / LLM-364)
//   └─> go runAtmosphereSweep(ctx, w, client, refresh)
//        ├─> immediate first sweep (no initial-interval wait)
//        ├─> time.Ticker @ AtmosphereRefreshInterval until ctx.Done
//        └─> refresh chan: a phase flip (dawn/dusk, or RunPhaseTicker's
//            boot correction) or a weather transition (storm start/clear)
//            re-authors the mood line out of cadence
//
// Failure modes (per consolidation):
//
//   - World SendContext error → log + return (sweep is shut down and
//     the world goroutine is gone; nothing to do).
//   - LLM call error → log + continue. The atmosphere is left untouched;
//     the next sweep retries.
//   - Empty / whitespace-only LLM reply → log + continue. Same retry
//     posture.
//   - ApplyAtmosphereRefresh error → log + continue. Defensive against
//     substrate invariant violations (empty-after-trim should already
//     be caught above, but the substrate enforces it independently).
//   - Dedup (LLM emitted the existing atmosphere) → log + continue. No
//     write, no stamp change.

// defaultAtmosphereRefreshInterval is the fallback cadence when
// WorldSettings.AtmosphereRefreshInterval is unset. 4h — matches the
// design locked in shared/tasks/engine-in-memory-rewrite/start-here
// (replaces v1 chronicler phase-boundary fires; same wall-clock
// cadence as v1's three-fires-per-game-day at dawn / midday / dusk
// when game time is wall-clock-paced).
//
// Lives in cascade rather than sim because cascade owns the goroutine
// driver; sim owns the substrate Commands. sim/atmosphere.go re-exports
// the same value as AtmosphereRefreshIntervalDefault for callers
// authoring tests or admin tools that don't pull cascade in.
const defaultAtmosphereRefreshInterval = 4 * time.Hour

// atmosphereLLMModel is the VA slug routed in llm.Request.Model. The
// real cutover-layer HTTP adapter routes this to the salem-generic
// shared utility VA — blank startup_instructions, no persona, no
// dream/learning state, no prompt cache. The caller (this slice) ships
// the full instruction set inline in the user message.
//
// FakeClient in tests ignores Model; tests still assert it's passed
// through correctly so a future adapter rename doesn't silently break
// routing.
const atmosphereLLMModel = "salem-generic"

// RegisterAtmosphere spawns the atmosphere refresh goroutine. The
// goroutine returns when ctx is cancelled. Call once at world startup;
// order relative to other Register*(...) calls doesn't matter
// functionally, but keep the registrations grouped for readability.
//
// Panics on nil w or nil client to fail fast at wiring time rather
// than silently no-op.
//
// Event-driven refresh (ZBBS-WORK-379 / LLM-364): subscribes to PhaseApplied
// AND WeatherChanged so a day/night flip or a weather transition (a storm
// starting or clearing) re-authors the mood line immediately, instead of
// letting it lag up to a full AtmosphereRefreshInterval behind the sky (the
// "night is fallen" prose still rendering at 09:00, or a downpour the mood
// line hasn't caught up to). The subscriber runs inline on the world goroutine
// inside emit, so it MUST NOT block or issue world commands — it only nudges
// the sweep goroutine via a buffered, coalescing channel. The off-world LLM
// sweep stays in runAtmosphereSweep; routing every refresh through that one
// goroutine keeps sweeps serialized, so an event-triggered sweep can't race or
// clobber an in-flight boot sweep (and it reads the corrected phase / current
// weather).
func RegisterAtmosphere(ctx context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: RegisterAtmosphere requires a non-nil world")
	}
	if client == nil {
		panic("cascade: RegisterAtmosphere requires a non-nil LLM client")
	}

	refresh := make(chan struct{}, 1)
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		// A day/night flip (PhaseApplied) or a weather transition
		// (WeatherChanged — a storm starting or clearing, LLM-364) both
		// re-author the mood line out of cadence so the ambient prose tracks
		// the sky instead of lagging up to a full AtmosphereRefreshInterval
		// behind it.
		switch evt.(type) {
		case *sim.PhaseApplied, *sim.WeatherChanged:
		default:
			return
		}
		select {
		case refresh <- struct{}{}:
		default:
			// A refresh is already queued; one sweep covers both edges.
		}
	}))

	// Cadence contract, phase one — default now, settings-resolved interval once the
	// goroutine can read it (LLM-395; see RegisterIdleBackstop).
	w.RegisterTicker("atmosphere", defaultAtmosphereRefreshInterval)
	go runAtmosphereSweep(ctx, w, client, refresh)
}

// runAtmosphereSweep is the goroutine body. Runs an immediate first
// sweep on entry (so a fresh-loaded world's stale-or-empty atmosphere
// doesn't have to wait a full 4h before its first refresh), then ticks
// at AtmosphereRefreshInterval. Also re-sweeps out of cadence whenever
// `refresh` fires — the PhaseApplied / WeatherChanged subscriber installed by
// RegisterAtmosphere nudges it on each day/night flip or weather transition
// (ZBBS-WORK-379 / LLM-364). Every sweep runs on this one goroutine, so the
// ticker, boot, and event-driven paths never overlap.
//
// Exported as a package-private symbol for tests; integration tests
// drive single sweeps via runOneAtmosphereSweep directly.
func runAtmosphereSweep(ctx context.Context, w *sim.World, client llm.Client, refresh <-chan struct{}) {
	interval := readAtmosphereRefreshInterval(ctx, w)
	if ctx.Err() != nil {
		return
	}
	// Cadence contract, phase two (LLM-395) — see runIdleBackstopSweep.
	w.RegisterTicker("atmosphere", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Immediate first sweep.
	runOneAtmosphereSweep(ctx, w, client)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("atmosphere")
			runOneAtmosphereSweep(ctx, w, client)
		case <-refresh:
			// Day/night phase flipped (dawn/dusk boundary, or the immediate
			// boot correction in RunPhaseTicker), or the weather transitioned
			// (storm start/clear, LLM-364). Re-author the mood line against the
			// new phase / weather out of the normal cadence.
			runOneAtmosphereSweep(ctx, w, client)
		}
	}
}

// runOneAtmosphereSweep executes one refresh cycle: fetch context from
// the world, issue the LLM call, apply the result. Single round-trip
// per sweep — atmosphere is world-scoped, not per-candidate like
// consolidation, so there's no inner loop.
//
// Honors ctx cancellation between the world round-trips so a shutdown
// mid-sweep returns promptly.
func runOneAtmosphereSweep(ctx context.Context, w *sim.World, client llm.Client) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	res, err := w.SendContext(ctx, sim.FetchAtmosphereContext(now))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/atmosphere: fetch context: %v", err)
		}
		return
	}
	actx, ok := res.(sim.AtmosphereContext)
	if !ok {
		log.Printf("cascade/atmosphere: fetch returned %T, want sim.AtmosphereContext", res)
		return
	}

	prompt := buildAtmospherePrompt(actx)
	text, ok := completeAtmosphere(ctx, client, prompt)
	if !ok {
		return
	}

	// Weather-coherence clamp (LLM-399). The prompt states the sky as fact, so
	// this should rarely fire; when it does, correct the model once, and if it
	// insists on its own weather, publish the sky itself rather than prose that
	// contradicts it. The pair must never disagree in a single snapshot — both
	// reach a reader (the client ticker, /umbilical/state) and the atmosphere
	// feeds the noticeboard authoring prompt as PriorAtmosphere, so an
	// incoherent mood line propagates into engine-authored content.
	if phrase := weatherContradiction(actx.Weather, text); phrase != "" {
		log.Printf("cascade/atmosphere: reply says %q under weather=%q; correcting once", phrase, actx.Weather)
		retry, retryOK := completeAtmosphere(ctx, client, prompt+"\n\n"+atmosphereCorrection(actx.Weather))
		if !retryOK {
			return
		}
		if phrase := weatherContradiction(actx.Weather, retry); phrase != "" {
			// Twice told, twice wrong. Fall back to the deterministic weather
			// scene — thin as a mood line, but true, and it breaks the loop that
			// kept the contradiction alive: the next sweep gets THIS as its
			// prior instead of being re-anchored on the rain.
			fallback := sim.WeatherScene(actx.Weather)
			if fallback == "" {
				log.Printf("cascade/atmosphere: reply says %q under weather=%q on retry, and no scene to fall back to; skipping write", phrase, actx.Weather)
				return
			}
			log.Printf("cascade/atmosphere: reply says %q under weather=%q on retry; installing the deterministic scene instead", phrase, actx.Weather)
			text = fallback
		} else {
			text = retry
		}
	}

	applyAt := time.Now().UTC()
	res, err = w.SendContext(ctx, sim.ApplyAtmosphereRefresh(text, applyAt))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/atmosphere: apply failed: %v", err)
		}
		return
	}
	// Same posture as post-LLM: suppress success/dedup logging during
	// shutdown for consistency with the error-path suppression above.
	// (code_review R0 finding #2.)
	if ctx.Err() != nil {
		return
	}
	wrote, _ := res.(bool)
	if wrote {
		log.Printf("cascade/atmosphere: refreshed (len=%d)", len(text))
	} else {
		log.Printf("cascade/atmosphere: dedup — LLM returned current atmosphere; skipping write")
	}
}

// completeAtmosphere issues one salem-generic round-trip and returns the
// trimmed prose. ok=false means "this sweep is over" — the call failed, the
// reply was empty, or ctx was cancelled — and the caller returns without
// touching world state; the next sweep retries (the ticker cadence is the retry
// interval, no backoff at this layer).
//
// Extracted (LLM-399) so the first attempt and the weather-correction retry are
// literally the same round-trip, rather than two copies that can drift on
// Model, SceneID, or the empty-reply guard.
func completeAtmosphere(ctx context.Context, client llm.Client, prompt string) (string, bool) {
	req := llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		// No tools — atmosphere is prose-only. The llm.Client contract
		// allows empty Tools (rare but legal).
		Tools: nil,
		// Routes through the cutover-layer HTTP adapter to salem-generic
		// (blank instructions, no persona, no state). FakeClient ignores
		// Model; tests assert it's passed through.
		Model: atmosphereLLMModel,
		// Fresh scene per call: memory-api's chat_messages history loader
		// filters by scene_id when set, so each call is its own isolated
		// conversation — without this, salem-generic would accumulate every
		// prior atmosphere prompt as history. The correction retry gets its own
		// scene too: it re-sends the full prompt plus the correction, so it
		// needs no memory of the attempt it is correcting.
		SceneID: llm.NewSceneID(),
	}
	reply, err := client.Complete(ctx, req)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/atmosphere: LLM call failed: %v", err)
		}
		return "", false
	}
	// Cancellation can land between client.Complete's start and its return
	// (the response arrived just before ctx-cancel reached the client).
	// Stop here rather than proceed to log empty-reply or attempt apply
	// during shutdown. (code_review R0 finding #1.)
	if ctx.Err() != nil {
		return "", false
	}
	text := strings.TrimSpace(reply.Content)
	if text == "" {
		log.Printf("cascade/atmosphere: empty reply (tool_calls=%d)", len(reply.ToolCalls))
		return "", false
	}
	return text, true
}

// readAtmosphereRefreshInterval reads WorldSettings.AtmosphereRefreshInterval
// via a context-aware Command and falls back to
// defaultAtmosphereRefreshInterval when unset, when the read can't
// complete, or when the configured value is non-positive (which would
// panic time.NewTicker). Same shape as cascade/idle_backstop.go's
// readSweepInterval — see the comment there for the SendContext-vs-Send
// rationale.
//
// Production tuning is intended to happen via environment config +
// restart, not hot-reload mid-run.
func readAtmosphereRefreshInterval(ctx context.Context, w *sim.World) time.Duration {
	res, err := w.SendContext(ctx, sim.Command{Fn: func(world *sim.World) (any, error) {
		interval := world.Settings.AtmosphereRefreshInterval
		if interval <= 0 {
			interval = defaultAtmosphereRefreshInterval
		}
		return interval, nil
	}})
	if err != nil {
		return defaultAtmosphereRefreshInterval
	}
	interval, ok := res.(time.Duration)
	if !ok || interval <= 0 {
		return defaultAtmosphereRefreshInterval
	}
	return interval
}

// buildAtmospherePrompt composes the user-message text the salem-generic
// VA reads. salem-generic ships with blank startup_instructions, so the
// prompt MUST self-frame in full — task description, inputs, and output
// constraints all inline.
//
// Tool disclaimer mirrors the consolidation prompt's posture: explicit
// "no tools" override, since some cutover-layer system-prompt loader
// down the line may add tool-discipline boilerplate.
//
// "Biblical in cadence" is lifted verbatim from v1's chronicler
// set_environment tool description — it shapes the prose without
// requiring a long stylistic preamble. The brevity ask is a soft
// target; the LLM tends to honor "1-2 sentences" loosely.
func buildAtmospherePrompt(c sim.AtmosphereContext) string {
	var b strings.Builder
	b.WriteString("You author the village's current atmosphere — weather, mood, ambient texture. There are no tools available for this turn; respond with prose only.\n\n")

	fmt.Fprintf(&b, "It is %s.", c.Phase)
	// The sky, ALWAYS — clear included (LLM-399). LLM-117 deliberately rendered
	// clear as NO weather line (byte-identical to the pre-weather-cascade
	// prompt), which left the model with zero information about the sky on the
	// common state while still handing it its own rain prose as the prior. Told
	// nothing and shown rain, it kept the rain: a cleared storm went on falling
	// in the mood line for hours ("The rain YET falleth…" — a continuation
	// clause, not a fresh scene). The calm sky has to be stated to be believed.
	if scene := sim.WeatherScene(c.Weather); scene != "" {
		b.WriteString(" ")
		b.WriteString(scene)
	}
	b.WriteString("\n\n")

	if prior := strings.TrimSpace(c.PriorAtmosphere); prior != "" {
		// Naming the prior as STALE when the sky has turned is the other half of
		// the fix: without it the model reads its own last writing as a
		// description of the scene in front of it and continues the weather it
		// finds there.
		if c.WeatherChangedSinceAtmosphere {
			b.WriteString("The sky has turned since you last wrote. What follows is the OLD sky — it is not the weather over the village now:\n")
		} else {
			b.WriteString("The previous atmosphere you wrote:\n")
		}
		b.WriteString(prior)
		b.WriteString("\n\n")
	} else {
		b.WriteString("You haven't written the atmosphere before now.\n\n")
	}

	if len(c.Roster) > 0 {
		b.WriteString("The village right now:\n")
		for _, e := range c.Roster {
			// FetchAtmosphereContext doesn't produce empty buckets, but
			// buildAtmospherePrompt is tested directly with synthetic
			// contexts — skip empty-name entries so a future caller can't
			// generate `- At the X: .` lines. (code_review R0 finding #4.)
			if len(e.DisplayNames) == 0 {
				continue
			}
			label := e.StructureLabel
			if label == "" {
				label = "Out in the open"
			} else {
				label = "At the " + label
			}
			fmt.Fprintf(&b, "- %s: %s.\n", label, strings.Join(e.DisplayNames, ", "))
		}
		b.WriteString("\n")
	}

	if len(c.ActivityDigest) > 0 {
		// Render the per-actor digest only if at least one actor has
		// at least one verb to render. An actor whose Counts map is
		// non-empty but contains only unmapped ActionTypes contributes
		// nothing; in that case the header still prints, but the loop
		// produces no lines. wroteAny tracks whether we emitted any
		// body lines so we can suppress the header in pathological
		// no-verbs-mapped cases (defensive — today's enum is fully
		// mapped).
		var rendered []string
		for _, e := range c.ActivityDigest {
			parts := digestActorParts(e.Counts)
			if len(parts) == 0 {
				continue
			}
			rendered = append(rendered, fmt.Sprintf("- %s %s.", e.DisplayName, strings.Join(parts, ", ")))
		}
		if len(rendered) > 0 {
			b.WriteString("Since your last attention:\n")
			for _, line := range rendered {
				b.WriteString(line)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("Write 1-2 brief sentences capturing the village's current atmosphere. Plain prose, biblical in cadence. No preamble, no sign-off — just the prose. The sky described above is the truth of it: write no weather that contradicts it.")
	return b.String()
}

// atmosphereWetPattern matches prose claiming a wet sky; atmosphereDryPattern,
// prose claiming a fair one. The guard behind weatherContradiction (LLM-399).
//
// Word-prefix matching (`\brain\w*`) rather than exact words, because the prose
// is biblical in cadence and inflects freely — "raineth", "falleth", "stormy".
// The leading \b is what keeps "restraineth" from reading as rain. "hail" is
// deliberately ABSENT from the wet list: in this register it is overwhelmingly
// a greeting ("Hail, brethren"), not weather.
//
// The dry list is phrases, not bare words: "clear" and "sun" appear constantly
// in innocent prose ("a clear voice", "the sun is hid behind the cloud" — which
// is a storm-compatible line), so only unambiguous fair-sky claims count.
var (
	atmosphereWetPattern = regexp.MustCompile(`(?i)\b(rain|storm|downpour|drizzle|thunder|lightning|tempest|deluge|overcast|cloudburst|squall|sleet|shower)\w*`)
	atmosphereDryPattern = regexp.MustCompile(`(?i)(cloudless|unclouded|sunlit|sunshine|sunny|clear sk(y|ies)|clear heaven|fair weather|no rain)`)

	// Mentioning rain is not claiming rain. "No rain falls", "the rain hath
	// passed and the sky opens" — these are the RIGHT things to write on the
	// clear side of a storm, and a bare keyword match rejects every one of them.
	// A guard that fires on those would replace the best post-storm prose we
	// produce with a flat deterministic line, every time a storm cleared. So a
	// wet word only contradicts a clear sky when it is ASSERTED: not negated
	// before, not called off after.
	//
	// Both patterns are searched only within the wet word's own clause (the
	// caller cuts at sentence punctuation), and both are deliberately GENEROUS —
	// they bias the guard toward letting a mention through. That bias is the
	// right one: a missed contradiction is one imperfect mood line until the next
	// sweep, while a false alarm is good prose thrown away and replaced with the
	// fallback. The prompt is the fix; this is only the net beneath it.
	atmosphereWetNegated   = regexp.MustCompile(`(?i)\b(no|not|nor|never|without|nary|after|since|once)\b`)
	atmosphereWetCeased    = regexp.MustCompile(`(?i)\b(pass\w*|ceas\w*|abat\w*|spent|gone|fled|lift\w*|clear\w*|stopp\w*|subsid\w*|withdraw\w*|broke\w*|end\w*|no more)\b`)
	atmosphereClauseBreaks = ".;:!?"
)

// weatherContradiction reports the phrase by which `text` describes a sky the
// village is not having, or "" when the prose and the weather agree.
//
// This is a clamp, and it is meant to be the second line, not the first — the
// prompt stating the sky as fact (buildAtmospherePrompt) is what should make
// contradictions rare. The clamp exists because the definition of done is that
// weather and atmosphere "cannot report different conditions from the same
// snapshot", and a prompt can only make that unlikely, not impossible: prose is
// generated, and a weak model handed a rain-soaked prior will sometimes reach
// for the rain anyway.
//
// A weather token with no scene (a future `fog` before its prose is written) is
// unjudgeable — no vocabulary to check against — so it never contradicts.
func weatherContradiction(weather, text string) string {
	switch strings.TrimSpace(weather) {
	case sim.WeatherStorm:
		return atmosphereDryPattern.FindString(text)
	case sim.WeatherClear, "":
		for _, m := range atmosphereWetPattern.FindAllStringIndex(text, -1) {
			if wetMentionAssertsRain(text, m[0], m[1]) {
				return text[m[0]:m[1]]
			}
		}
		return ""
	default:
		return ""
	}
}

// wetMentionAssertsRain reports whether the wet word at text[start:end] claims
// weather that is falling NOW, rather than weather being denied ("no rain
// falls") or called off ("the rain hath passed"). Scoped to the word's own
// clause on each side, so a wet word in one sentence isn't excused by a negation
// in another.
func wetMentionAssertsRain(text string, start, end int) bool {
	before := text[:start]
	if i := strings.LastIndexAny(before, atmosphereClauseBreaks); i >= 0 {
		before = before[i+1:]
	}
	if atmosphereWetNegated.MatchString(before) {
		return false
	}
	after := text[end:]
	if i := strings.IndexAny(after, atmosphereClauseBreaks); i >= 0 {
		after = after[:i]
	}
	return !atmosphereWetCeased.MatchString(after)
}

// atmosphereCorrection is appended to the prompt for the single retry after a
// reply contradicted the sky. Restates the weather as a direct instruction
// rather than as scene-setting — the scene evidently didn't land, so the retry
// stops being subtle about it.
func atmosphereCorrection(weather string) string {
	scene := sim.WeatherScene(weather)
	if scene == "" {
		return ""
	}
	return "Your last attempt described weather the village is not having. " + scene + " Write the mood of the village under THAT sky, and name no other weather."
}

// atmosphereDigestVerbs maps each ActionType to the past-tense verb
// rendered into the digest. Closed set — ActionType values not in the
// map are silently skipped (graceful degradation if a new ActionType
// lands without a verb mapping, the digest still renders for known
// types and the new type is invisible until a verb is added here).
//
// "ate" for ActionTypeConsumed mirrors v1 chronicler's "completed N
// chore" framing — past-tense verb that reads naturally in the
// "Since your last attention: Hannah ate 2 times" rendering.
var atmosphereDigestVerbs = map[sim.ActionType]string{
	sim.ActionTypeSpoke:     "spoke",
	sim.ActionTypeWalked:    "walked",
	sim.ActionTypeConsumed:  "ate",
	sim.ActionTypePaid:      "paid",
	sim.ActionTypeDelivered: "delivered",
	sim.ActionTypeLabored:   "labored",
	// LLM-354: a mend is the one source-activity beat other NPCs perceive. Without
	// an entry here the repairing row reaches only the mender's own consolidation
	// (which filters to its own actor), so the verb is what makes a neighbour
	// notice the hammer at all.
	sim.ActionTypeRepairing: "mended",
}

// digestActorParts renders one actor's per-action-type counts as
// ordered "verb N time(s)" parts. Output ordered alphabetically by
// verb for deterministic prompt rendering. Counts of zero or negative
// are skipped (defensive — FetchAtmosphereContext doesn't produce
// non-positive counts, but the helper is tested directly with
// synthetic input). ActionTypes not in atmosphereDigestVerbs are
// silently skipped.
func digestActorParts(counts map[sim.ActionType]int) []string {
	type kv struct {
		verb  string
		count int
	}
	entries := make([]kv, 0, len(counts))
	for at, n := range counts {
		if n <= 0 {
			continue
		}
		verb, ok := atmosphereDigestVerbs[at]
		if !ok {
			continue
		}
		entries = append(entries, kv{verb: verb, count: n})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].verb < entries[j].verb })
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		suffix := "time"
		if e.count != 1 {
			suffix = "times"
		}
		out = append(out, fmt.Sprintf("%s %d %s", e.verb, e.count, suffix))
	}
	return out
}
