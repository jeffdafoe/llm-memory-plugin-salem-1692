package cascade

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// narration_expansion.go — LLM expansion of the narration phrase pools
// (ZBBS-WORK-399). When a pool's draw counter crosses its threshold
// (sim/narration_pool.go narrationDraw), the world nudges this slice's
// trigger channel; the goroutine here fetches the pool snapshot, fires
// ONE tool-free completion against salem-generic ("here are my lines
// for this moment — write N more in the same voice, JSON out"),
// validates the reply against the pool's content rules, persists the
// accepted lines durably (narration_pool_expansion), and applies them
// to the live pool. Then the pool is deterministic again until the next
// threshold crossing.
//
// Same off-world shape as the atmosphere sweep: the LLM call blocks for
// seconds, so it never runs on the world goroutine — the loop bounces
// to the world for the snapshot (FetchNarrationExpansionContext), calls
// the LLM off-world, and bounces back to land the result
// (FinishNarrationExpansion).
//
// Failure modes — ALL of them log + FinishNarrationExpansion(key, nil),
// which clears the pool's in-flight flag without appending anything.
// Draws were already reset when the nudge fired, so a failed attempt
// retries only after another cycle-factor's worth of draws — natural
// rate-limiting against a misbehaving model, no backoff bookkeeping.
// Rejection is batch-level and deliberate (reject-don't-retry): a reply
// that violates the contract anywhere (bad JSON, wrong count, a line
// failing content rules) is discarded whole rather than salvaged —
// a model emitting garbage in one line isn't trusted for the rest.
// Already-known lines are NOT a violation: duplicates are silently
// dropped and the novel remainder persists.

// narrationExpansionLLMModel is the VA slug routed in llm.Request.Model —
// the same shared utility VA the atmosphere/noticeboard slices use
// (blank startup_instructions, no persona; the prompt self-frames).
// Also recorded as narration_pool_expansion.generated_by: the engine
// knows the VA it asked, not the provider model behind it.
const narrationExpansionLLMModel = "salem-generic"

// narrationExpansionTriggerBuffer sizes the nudge channel. There are 9
// pools and a pool can't re-nudge while in flight, so 16 means the
// non-blocking send in narrationDraw effectively never drops.
const narrationExpansionTriggerBuffer = 16

// RegisterNarrationExpansion installs the trigger channel on the world
// and spawns the expansion goroutine. The goroutine returns when ctx is
// cancelled. Call once at world startup, after main.go has wired the
// durable sink (SetNarrationExpansionSink) so the first expansion
// persists; panics on nil w or client per the cascade wiring convention.
func RegisterNarrationExpansion(ctx context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: RegisterNarrationExpansion requires a non-nil world")
	}
	if client == nil {
		panic("cascade: RegisterNarrationExpansion requires a non-nil LLM client")
	}
	trigger := make(chan string, narrationExpansionTriggerBuffer)
	w.SetNarrationExpansionTrigger(trigger)
	go runNarrationExpansionLoop(ctx, w, client, trigger)
}

// runNarrationExpansionLoop drains the trigger channel for the life of
// the engine. One expansion at a time — a second pool nudged while one
// is expanding just waits in the channel; expansions are rare (cycle
// thresholds) and seconds-long, so serializing them costs nothing and
// keeps the LLM pressure at one in-flight call.
func runNarrationExpansionLoop(ctx context.Context, w *sim.World, client llm.Client, trigger <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-trigger:
			runOneNarrationExpansion(ctx, w, client, key)
		}
	}
}

// runOneNarrationExpansion executes one expansion attempt for key. Every
// exit path that got far enough to know the pool exists lands at
// FinishNarrationExpansion exactly once (with nil on failure) so the
// in-flight flag can't leak set.
func runOneNarrationExpansion(ctx context.Context, w *sim.World, client llm.Client, key string) {
	if ctx.Err() != nil {
		return
	}
	res, err := w.SendContext(ctx, sim.FetchNarrationExpansionContext(key))
	if err != nil {
		// Unknown key (wiring drift) or world shut down — in neither case
		// is there a reachable pool flag to clear.
		if ctx.Err() == nil {
			log.Printf("cascade/narration_expansion: fetch %q: %v", key, err)
		}
		return
	}
	nctx, ok := res.(sim.NarrationExpansionContext)
	if !ok {
		log.Printf("cascade/narration_expansion: fetch %q returned %T, want sim.NarrationExpansionContext", key, res)
		return
	}
	if nctx.Wanted <= 0 {
		// Pool reached cap between the nudge and the fetch. Just clear
		// the in-flight flag; narrationDraw's cap gate stops re-nudges.
		finishNarrationExpansion(ctx, w, key, nil)
		return
	}

	req := llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: buildNarrationExpansionPrompt(nctx)}},
		// No tools — the reply is a single JSON object.
		Tools: nil,
		Model: narrationExpansionLLMModel,
		// Fresh scene per expansion, same isolation rationale as the
		// atmosphere sweep: salem-generic must not accumulate prior
		// expansion prompts as conversation history.
		SceneID: llm.NewSceneID(),
	}
	reply, err := client.Complete(ctx, req)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/narration_expansion: %q LLM call failed: %v", key, err)
			finishNarrationExpansion(ctx, w, key, nil)
		}
		return
	}
	if ctx.Err() != nil {
		// Response raced shutdown (atmosphere's posture) — don't proceed.
		return
	}

	phrases, rejectReason := parseNarrationExpansionReply(reply.Content, nctx)
	if rejectReason != "" {
		log.Printf("cascade/narration_expansion: %q reply rejected: %s", key, rejectReason)
		finishNarrationExpansion(ctx, w, key, nil)
		return
	}
	novel := dropKnownNarrationPhrases(phrases, nctx.Phrases)
	if len(novel) > nctx.Wanted {
		novel = novel[:nctx.Wanted]
	}
	if len(novel) == 0 {
		log.Printf("cascade/narration_expansion: %q reply valid but all %d lines duplicate the pool", key, len(phrases))
		finishNarrationExpansion(ctx, w, key, nil)
		return
	}

	// Durable-first: a phrase the DB never saw would vanish on restart
	// and could be re-generated as a near-duplicate later. Only after
	// the write lands does the live pool grow.
	if err := w.AppendNarrationExpansionDurable(ctx, key, novel, narrationExpansionLLMModel); err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/narration_expansion: %q persist failed, discarding %d lines: %v", key, len(novel), err)
			finishNarrationExpansion(ctx, w, key, nil)
		}
		return
	}
	finishNarrationExpansion(ctx, w, key, novel)
	log.Printf("cascade/narration_expansion: %q grew by %d lines (pool was %d)", key, len(novel), len(nctx.Phrases))
}

// finishNarrationExpansion lands the attempt on the world (append +
// clear in-flight). A SendContext error here means the world is gone;
// the flag it would have cleared is gone with it.
func finishNarrationExpansion(ctx context.Context, w *sim.World, key string, phrases []string) {
	if _, err := w.SendContext(ctx, sim.FinishNarrationExpansion(key, phrases)); err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/narration_expansion: finish %q: %v", key, err)
		}
	}
}

// buildNarrationExpansionPrompt composes the salem-generic user message.
// Self-framing in full (blank startup_instructions on the VA), same as
// the atmosphere prompt: task, voice, the existing lines, token rules,
// and the exact output contract.
func buildNarrationExpansionPrompt(c sim.NarrationExpansionContext) string {
	var b strings.Builder
	b.WriteString("You extend a small pool of engine-authored narration lines for a colonial-period village simulation (1690s Salem). There are no tools available for this turn; respond with a single JSON object only.\n\n")

	fmt.Fprintf(&b, "The moment these lines narrate: %s\n\n", c.Description)

	b.WriteString("Voice: period-appropriate colonial New England — neither archaic-stiff nor modern-casual. Match the tone and length range of the existing lines.\n\n")

	b.WriteString("Existing lines (write nothing that repeats or near-duplicates any of these):\n")
	for i, p := range c.Phrases {
		fmt.Fprintf(&b, "%d. %s\n", i+1, p)
	}
	b.WriteString("\n")

	if c.CustomerToken {
		b.WriteString("Lines may include the literal token {customer} where the customer's name belongs, used the way the existing lines use it. No other {token} placeholders.\n\n")
	} else {
		b.WriteString("Do not use any {token} placeholders.\n\n")
	}

	fmt.Fprintf(&b, "Write exactly %d new lines in the same voice, varying the verbs and sensory detail so the pool feels lived-in. Each line on its own, no numbering inside the strings. Return ONLY this JSON, no preamble, no code fence:\n", c.Wanted)
	b.WriteString(`{"phrases": ["...", "..."]}`)
	return b.String()
}

// narrationExpansionReply is the reply contract. DisallowUnknownFields
// in the decoder rejects extra keys.
type narrationExpansionReply struct {
	Phrases []string `json:"phrases"`
}

// parseNarrationExpansionReply validates the raw completion text against
// the contract: a single JSON object (an optional markdown code fence is
// tolerated and stripped — the one accommodation to LLM habit), exactly
// Wanted phrases, every phrase passing sim.ValidateNarrationPhrase for
// the pool's token rules, and no duplicates WITHIN the batch. Returns
// (phrases, "") on success or (nil, reason) for a batch-level reject.
// Duplicates against the EXISTING pool are not checked here — those are
// dropped, not rejected (dropKnownNarrationPhrases).
func parseNarrationExpansionReply(content string, c sim.NarrationExpansionContext) ([]string, string) {
	text := strings.TrimSpace(content)
	text = stripCodeFence(text)
	if text == "" {
		return nil, "empty reply"
	}

	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var reply narrationExpansionReply
	if err := dec.Decode(&reply); err != nil {
		return nil, fmt.Sprintf("not the contract JSON: %v", err)
	}
	// Trailing content after the object (a second object, prose) is a
	// contract violation too.
	if dec.More() {
		return nil, "trailing content after the JSON object"
	}
	if len(reply.Phrases) != c.Wanted {
		return nil, fmt.Sprintf("wanted exactly %d phrases, got %d", c.Wanted, len(reply.Phrases))
	}

	seen := make(map[string]struct{}, len(reply.Phrases))
	out := make([]string, 0, len(reply.Phrases))
	for i, p := range reply.Phrases {
		p = strings.TrimSpace(p)
		if reason := sim.ValidateNarrationPhrase(p, c.CustomerToken); reason != "" {
			return nil, fmt.Sprintf("phrase %d %s", i+1, reason)
		}
		key := strings.ToLower(p)
		if _, dup := seen[key]; dup {
			return nil, fmt.Sprintf("phrase %d duplicates another phrase in the batch", i+1)
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out, ""
}

// dropKnownNarrationPhrases removes batch lines already present in the
// pool (case-insensitive, trimmed). Not a rejection: the model writing
// one line we already had doesn't invalidate the rest.
func dropKnownNarrationPhrases(phrases, existing []string) []string {
	known := make(map[string]struct{}, len(existing))
	for _, p := range existing {
		known[strings.ToLower(strings.TrimSpace(p))] = struct{}{}
	}
	out := make([]string, 0, len(phrases))
	for _, p := range phrases {
		if _, dup := known[strings.ToLower(strings.TrimSpace(p))]; dup {
			continue
		}
		out = append(out, p)
	}
	return out
}

// stripCodeFence unwraps a reply the model wrapped in a markdown code
// fence (``` or ```json) despite the no-fence instruction. Strict about
// shape: both fence lines must be the first and last lines; anything
// else returns the input unchanged and the JSON decoder rejects it.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) < 3 {
		return s
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	if (first != "```" && first != "```json") || last != "```" {
		return s
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}
