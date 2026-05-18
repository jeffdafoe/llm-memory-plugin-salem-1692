package cascade

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// noticeboard.go — engine-authored noticeboard content cascade.
//
// Triggered by VillageObjectStateChanged for any object whose new
// state carries both TagRotatable + TagNoticeBoard. The subscriber
// runs synchronously on the world goroutine (per the Subscribe
// contract); the LLM call must run off-world (it blocks for
// seconds) so the subscriber spawns a goroutine.
//
// Goroutine flow:
//
//  1. SendContext FetchVillageContext to snapshot world state
//     (visitors, business catalog, recent atmosphere) atomically.
//  2. Build the hardened prompt (engine pushes the FULL instruction
//     set; salem-generic VA has no persona, no startup_instructions,
//     no prompt cache — see atmosphere cascade for the same pattern).
//  3. Call llm.Complete with Model="salem-generic".
//  4. Trim response.Content; SendContext SaveNoticeboardContent gated
//     on the object's CurrentState still matching the new-state
//     captured at trigger time (stale-guard, mirrors v1's ZBBS-112
//     R1 fix).
//
// Failure modes (per atmosphere cascade's posture):
//
//   - World SendContext error → log + return. Sweep dead; nothing
//     to retry.
//   - LLM call error → log + return. Next rotation cycle retries.
//   - Empty / whitespace reply → log + return.
//   - SaveNoticeboardContent stale_state → log + return. The board
//     rotated again before we landed; next cycle handles the new
//     state.
//
// Curation principle (v1 anti-fabrication lesson): the prompt is
// engine-pushed in full, narrowly curated. Personal-state data
// (distress / per-NPC action counts) is deliberately NOT in the
// prompt — that's the seed v1 chronicler fabricated noticeboard
// surveillance prose from ("Ezekiel is tired at the forge"). See
// shared/notes/codebase/salem-engine-v2/noticeboard for the full
// design history.

// noticeboardLLMModel is the VA slug routed in llm.Request.Model.
// The cutover HTTP adapter routes this to salem-generic — the same
// shared utility VA the atmosphere cascade uses, intentionally
// stateless and prompt-cache-disabled.
//
// FakeClient ignores Model; tests still assert it's passed through
// so a future adapter rename doesn't silently break routing.
const noticeboardLLMModel = "salem-generic"

// noticeboardLLMTimeout caps the off-world LLM call so a wedged
// provider doesn't block the goroutine indefinitely. 90s matches
// the atmosphere cascade's posture.
const noticeboardLLMTimeout = 90 * time.Second

// RegisterNoticeboard wires the VillageObjectStateChanged subscriber.
// Must run on the world goroutine — call before World.Run, or from
// inside a Command.Fn.
//
// Panics on nil w or nil client to fail fast at wiring time.
func RegisterNoticeboard(ctx context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: RegisterNoticeboard requires a non-nil world")
	}
	if client == nil {
		panic("cascade: RegisterNoticeboard requires a non-nil LLM client")
	}
	w.Subscribe(sim.SubscriberFunc(func(world *sim.World, evt sim.Event) {
		handleNoticeboardStateChange(ctx, world, evt, client)
	}))
}

// handleNoticeboardStateChange is the VillageObjectStateChanged
// subscriber. Gates on the new state carrying TagRotatable +
// TagNoticeBoard (noticeboards are the only state-change kind we
// author content for today). Captures the prior content from
// World.NoticeboardContent before spawning the off-world goroutine
// so the goroutine has it without an extra round-trip.
func handleNoticeboardStateChange(ctx context.Context, w *sim.World, evt sim.Event, client llm.Client) {
	changed, ok := evt.(*sim.VillageObjectStateChanged)
	if !ok {
		return
	}
	obj, ok := w.VillageObjects[changed.ObjectID]
	if !ok {
		return
	}
	asset, ok := w.Assets[obj.AssetID]
	if !ok {
		return
	}
	state := asset.FindState(changed.ToState)
	if state == nil {
		return
	}
	if !state.HasTag(sim.TagRotatable) || !state.HasTag(sim.TagNoticeBoard) {
		return
	}
	var priorText string
	if w.NoticeboardContent != nil {
		if prior, ok := w.NoticeboardContent[changed.ObjectID]; ok && prior != nil {
			priorText = prior.Text
		}
	}
	boardLabel := obj.EffectiveDisplayName(asset.Name)
	go runNoticeboardAuthor(ctx, w, client, changed.ObjectID, changed.ToState, boardLabel, priorText)
}

// runNoticeboardAuthor is the off-world goroutine body. Fetches a
// fresh village snapshot, builds the prompt, calls salem-generic,
// applies the result via SaveNoticeboardContent.
func runNoticeboardAuthor(ctx context.Context, w *sim.World, client llm.Client, objectID sim.VillageObjectID, atState, boardLabel, priorText string) {
	// Cap the LLM-call budget so a wedged provider doesn't pin this
	// goroutine forever.
	callCtx, cancel := context.WithTimeout(ctx, noticeboardLLMTimeout)
	defer cancel()

	ctxRes, err := w.SendContext(callCtx, sim.FetchVillageContext(time.Now()))
	if err != nil {
		// Check callCtx (the timeout-wrapped ctx) — timeout sets
		// callCtx.Err() but not ctx.Err(); we want to suppress
		// both shutdown-cancellation AND timeout noise.
		if callCtx.Err() == nil {
			log.Printf("cascade/noticeboard: fetch context (%q): %v", objectID, err)
		}
		return
	}
	snap, ok := ctxRes.(sim.VillageContext)
	if !ok {
		log.Printf("cascade/noticeboard: unexpected FetchVillageContext result type %T", ctxRes)
		return
	}

	messages := buildNoticeboardPrompt(snap, boardLabel, priorText)
	resp, err := client.Complete(callCtx, llm.Request{
		Messages:    messages,
		Model:       noticeboardLLMModel,
		Temperature: 0.7,
		MaxTokens:   200,
	})
	if err != nil {
		if callCtx.Err() == nil {
			log.Printf("cascade/noticeboard: Complete (%q, state=%q): %v", objectID, atState, err)
		}
		return
	}
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		log.Printf("cascade/noticeboard: empty LLM reply for %q state=%q", objectID, atState)
		return
	}
	// Drop wrapping quotes the model may have added despite the
	// "no quotation marks" instruction.
	text = strings.Trim(text, "\"'")
	text = strings.TrimSpace(text)
	if text == "" {
		log.Printf("cascade/noticeboard: empty after unquote for %q state=%q", objectID, atState)
		return
	}

	saveRes, err := w.SendContext(callCtx, sim.SaveNoticeboardContent(objectID, text, atState, time.Now()))
	if err != nil {
		if callCtx.Err() == nil {
			log.Printf("cascade/noticeboard: SaveNoticeboardContent (%q, state=%q): %v", objectID, atState, err)
		}
		return
	}
	r, ok := saveRes.(sim.SaveNoticeboardContentResult)
	if !ok {
		log.Printf("cascade/noticeboard: unexpected SaveNoticeboardContent result type %T", saveRes)
		return
	}
	if !r.Applied && r.SkipReason != "stale_state" {
		// stale_state is expected (rotation overtook us); other
		// reasons are worth logging at info-level so admin tools
		// can see odd skips.
		log.Printf("cascade/noticeboard: save skipped %q state=%q reason=%s",
			objectID, atState, r.SkipReason)
	}
}

// buildNoticeboardPrompt assembles the [system, user] message pair
// for a single noticeboard authoring call. Engine pushes the full
// instruction set; salem-generic VA has no persona.
//
// Curated context the user message renders:
//
//   - Recent visitors (Name + archetype + origin) — drives the
//     "news of visitors" genre.
//   - Business catalog (per-keeper wares + prices) — drives the
//     "wares and services offered" genre.
//   - Recent atmosphere prose (framed as anti-anchor — vibe context
//     but DO NOT echo).
//   - Prior board text (framed as anti-anchor — DO NOT repeat).
//
// Deliberately excluded:
//
//   - ActivityDigest (per-NPC action counts) — surveillance-shaped
//     fodder the v1 chronicler fabricated noticeboard prose from
//     ("X spake at Y 4 times"). The atmosphere cascade uses it
//     legitimately, but noticeboards must not.
//   - Distress list — same anti-fabrication reason; not in the
//     snapshot at all.
//   - Roster (NPCs by structure) — same anti-surveillance posture
//     ("Goodman X is at the smithy" is the exact anti-pattern).
func buildNoticeboardPrompt(snap sim.VillageContext, boardLabel, priorText string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleSystem, Content: noticeboardSystemPrompt()},
		{Role: llm.RoleUser, Content: buildNoticeboardUserPrompt(snap, boardLabel, priorText)},
	}
}

// noticeboardSystemPrompt is the static system message — role + genre
// catalog + anti-patterns + voice anchor + output format. Constant
// per call (built fresh each invocation but content is fixed); the
// dynamic context lives in the user message.
//
// Genre catalog ported from v1's ZBBS-112 prompt (see
// shared/notes/codebase/salem/object-content-and-noticeboards
// "Prompt content and voice"). Anti-pattern callouts are the
// v1-hardened guard against surveillance-shaped prose.
func noticeboardSystemPrompt() string {
	return strings.Join([]string{
		"You are composing a single notice for a noticeboard in a Salem, Massachusetts village in the year 1692.",
		"",
		"GENRES of notices that are appropriate (one notice falls into one genre):",
		"  - Civic announcements: town meetings, militia musters, sermon hours, court days, public fasts.",
		"      Example: \"A town meeting is called for Friday next, at the meeting house, third hour after noon.\"",
		"  - News of visitors arriving: a peddler, surgeon, preacher, or other traveller has come to the village.",
		"      Example: \"A travelling cobbler, Master Wendell of Salem Town, lodges at the Ordinary; see him for boots by week's end.\"",
		"  - Wares and services offered: what a villager sells or trades at their post.",
		"      Example: \"Goodwife Wells at the Ordinary serves ale and pottage of an evening, three pence the cup.\"",
		"  - Lost and found: an item left behind at a public place, or recovered.",
		"      Example: \"A plain woolen shawl was left at the meeting house Sabbath last; inquire of the deacon.\"",
		"  - Warnings: wolves in the wood, a stranger seen, a hazard on the road.",
		"      Example: \"Take care upon the Andover road, the bridge being weakened by yesterday's rain.\"",
		"  - Petitions and grievances: a public ask of the community.",
		"      Example: \"All who can spare hands at the Whittredge raising on Tuesday, the family would have your help.\"",
		"",
		"DO NOT write any of the following — these are not the purpose of a noticeboard:",
		"  - Reports of where individual villagers currently are, what they are doing, or how they feel.",
		"      DO NOT write: \"Goodman Reeves is at the forge.\"",
		"      DO NOT write: \"Goodwife Wells is tired.\"",
		"      DO NOT write: \"Ezekiel is at work today.\"",
		"  - Counts or summaries of villager activity (\"X spoke 4 times today\").",
		"  - Surveillance-shaped statements about individuals' health, mood, comings and goings.",
		"  Notices are durable public posts that stand for hours or days — by the time anyone reads them, the moment has passed. Write things that remain useful to read: events scheduled, things offered, things lost, warnings to heed.",
		"",
		"VOICE:",
		"  - The voice of a 1692 New England villager. Formal for civic announcements; plain for offerings and warnings.",
		"  - One short sentence, no preamble, no salutation, no signature.",
		"  - No quotation marks around the notice itself.",
		"",
		"OUTPUT FORMAT: Return only the notice text itself, on a single line. No prefatory \"Here is the notice:\" or similar. No surrounding quotes.",
	}, "\n")
}

// buildNoticeboardUserPrompt renders the dynamic per-call context:
// board label + curated village state + framing instruction.
func buildNoticeboardUserPrompt(snap sim.VillageContext, boardLabel, priorText string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Compose a single notice for the board: %s.\n\n", strings.TrimSpace(boardLabel))

	if len(snap.Visitors) > 0 {
		b.WriteString("Visitors lately come to the village:\n")
		for _, v := range snap.Visitors {
			line := strings.TrimSpace(v.DisplayName)
			extras := []string{}
			if v.Archetype != "" {
				extras = append(extras, "a "+v.Archetype)
			}
			if v.Origin != "" {
				extras = append(extras, "from "+v.Origin)
			}
			if len(extras) > 0 {
				line = line + " (" + strings.Join(extras, ", ") + ")"
			}
			fmt.Fprintf(&b, "  - %s\n", line)
		}
		b.WriteString("\n")
	}

	if len(snap.BusinessCatalog) > 0 {
		b.WriteString("Wares and services offered in the village:\n")
		for _, e := range snap.BusinessCatalog {
			items := renderBusinessItems(e.Items)
			// FetchVillageContext skips entries with empty Items
			// today; defensive skip here in case a direct caller
			// passes a malformed BusinessCatalog (prompt builder
			// is exported via tests and may be reused).
			if items == "" {
				continue
			}
			fmt.Fprintf(&b, "  - %s at %s: %s\n", e.OwnerDisplayName, e.StructureLabel, items)
		}
		b.WriteString("\n")
	}

	if atmosphere := strings.TrimSpace(snap.PriorAtmosphere); atmosphere != "" {
		b.WriteString("The recent atmosphere of the village reads: " + atmosphere + "\n")
		b.WriteString("(Do not repeat or echo the atmosphere prose in the notice — use it only as the village's current vibe.)\n\n")
	}

	if prior := strings.TrimSpace(priorText); prior != "" {
		b.WriteString("The board's prior notice (now coming down) read: " + prior + "\n")
		b.WriteString("(Do not repeat or paraphrase the prior notice. Write something genuinely different.)\n\n")
	}

	b.WriteString("Now compose today's single notice for this board, drawing on the genre catalog and the village context above.")
	return b.String()
}

// renderBusinessItems formats one business entry's items as a
// comma-separated list, "<item> (<price> coins)". Items already
// sorted by the snapshot builder.
func renderBusinessItems(items []sim.BusinessItem) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if it.Price > 0 {
			parts = append(parts, fmt.Sprintf("%s (%d coins)", it.Item, it.Price))
		} else {
			parts = append(parts, string(it.Item))
		}
	}
	// Stable order — the snapshot builder pre-sorts by ItemKind, but
	// a defensive sort guards against a future caller passing
	// un-sorted items. Cheap at single-digit list lengths.
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
