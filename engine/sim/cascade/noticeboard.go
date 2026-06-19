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
// Authoring is initiated explicitly, never reactively off a board flip
// (LLM-44):
//
//   - KickstartNoticeboards seeds every blank board once at boot.
//   - The town-crier route authors a board on arrival (beginCrierBoardStop),
//     posts it with the variant matched to the authored count, reads it, then
//     advances. See cascade/npc_route.go.
//
// The VillageObjectStateChanged subscriber (handleNoticeboardStateChange) does
// NOT author — it only clears content when a board rotates to a zero-capacity
// (empty) state, enforcing the "empty board holds no content" invariant. A
// reactive author here would fire on the crier's own count-matched flip and
// overwrite the notices she just posted and read.
//
// Author flow (authorNoticeboardText, off-world goroutine):
//
//  1. SendContext FetchVillageContext to snapshot world state
//     (visitors, business catalog, recent atmosphere) atomically.
//  2. Build the hardened prompt (engine pushes the FULL instruction
//     set; salem-generic VA has no persona, no startup_instructions,
//     no prompt cache — see atmosphere cascade for the same pattern).
//  3. Call llm.Complete with Model="salem-generic".
//  4. Trim + clamp to the board's line capacity. The caller then stores it via
//     SaveNoticeboardContent, gated on the object's CurrentState matching the
//     atState the save is for (stale-guard, mirrors v1's ZBBS-112 R1 fix).
//
// Failure modes (per atmosphere cascade's posture):
//
//   - World SendContext error → log + return. Nothing to retry.
//   - LLM call error → log + return ("" content). Next visit retries.
//   - Empty / whitespace reply → log + return "".
//   - SaveNoticeboardContent stale_state → log + skip. A later visit handles
//     the board's current state.
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

// RegisterNoticeboard wires the VillageObjectStateChanged subscriber that
// enforces the empty-board invariant (a zero-capacity board holds no
// content). It does NOT author content reactively off a flip — authoring
// is initiated explicitly by KickstartNoticeboards (boot seeding) and the
// town-crier route (runtime, LLM-44). Must run on the world goroutine —
// call before World.Run, or from inside a Command.Fn.
//
// Panics on nil w or nil client to fail fast at wiring time. The client is
// not consumed by the subscriber itself (the clear path is LLM-free) but a
// non-nil client is the noticeboard subsystem's wiring contract — its
// authoring paths (KickstartNoticeboards, the crier) require one.
func RegisterNoticeboard(_ context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: RegisterNoticeboard requires a non-nil world")
	}
	if client == nil {
		panic("cascade: RegisterNoticeboard requires a non-nil LLM client")
	}
	w.Subscribe(sim.SubscriberFunc(handleNoticeboardStateChange))
}

// kickstartBoard is one blank board found by KickstartNoticeboards'
// world-goroutine enumeration — the trigger-time capture (id, state,
// label) runNoticeboardAuthor needs, mirroring what the state-change
// subscriber captures.
type kickstartBoard struct {
	id       sim.VillageObjectID
	atState  string
	label    string
	capacity int
}

// kickstartClear is a board sitting in a zero-capacity (empty) state that still
// holds stored content — collected during the enumeration and cleared after, so
// the "an empty board holds no content" invariant the runtime rotation path
// enforces also holds at boot.
type kickstartClear struct {
	id      sim.VillageObjectID
	atState string
}

// kickstartScan is the enumeration result: blank capacity>0 boards to author
// and zero-capacity boards with stale content to clear.
type kickstartScan struct {
	blanks []kickstartBoard
	clears []kickstartClear
}

// KickstartNoticeboards authors content for every noticeboard that has
// none — the restart-gap closer (ZBBS-HOME-443). World.NoticeboardContent
// is transient by design ("first cycle after restart authors fresh"), but
// the daily rotation that triggers authoring won't re-fire until the next
// midnight boundary, so a mid-day engine restart left every board blank —
// and unreadable in the client, which gates the read modal on non-empty
// content — for the rest of the day. It also closes the designed
// cold-start gap: the crier's first stop of the day now has content to
// read instead of a silent first cycle.
//
// Call once in a goroutine after World.Run starts (it needs the command
// loop). Enumerates boards on the world goroutine, then spawns one
// runNoticeboardAuthor per blank board. A same-boot rotation flip racing
// this is benign: SaveNoticeboardContent's atState stale-guard drops
// whichever save lost, and the flip-triggered author re-runs for the new
// state.
//
// Panics on nil w or nil client to fail fast at wiring time (same posture
// as RegisterNoticeboard).
func KickstartNoticeboards(ctx context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: KickstartNoticeboards requires a non-nil world")
	}
	if client == nil {
		panic("cascade: KickstartNoticeboards requires a non-nil LLM client")
	}
	res, err := w.SendContext(ctx, sim.Command{Fn: func(world *sim.World) (any, error) {
		scan := kickstartScan{}
		for id, obj := range world.VillageObjects {
			if obj == nil {
				continue
			}
			asset, ok := world.Assets[obj.AssetID]
			if !ok {
				continue
			}
			state := asset.FindState(obj.CurrentState)
			if state == nil || !state.HasTag(sim.TagRotatable) || !state.HasTag(sim.TagNoticeBoard) {
				continue
			}
			capacity := sim.ContentCapacityForState(state)
			if capacity <= 0 {
				// Empty board: nothing to author. Clear any stale content
				// left on it (e.g. by an older binary that authored for this
				// state) so the "a zero-capacity board holds no content"
				// invariant holds at boot, not just on a runtime rotation
				// into the empty state. NoticeboardContent is transient
				// today, so this is normally a no-op — it fails closed if
				// that changes.
				if nc := world.NoticeboardContent[id]; nc != nil {
					scan.clears = append(scan.clears, kickstartClear{id: id, atState: obj.CurrentState})
				}
				continue
			}
			if nc := world.NoticeboardContent[id]; nc != nil {
				continue
			}
			scan.blanks = append(scan.blanks, kickstartBoard{
				id:       id,
				atState:  obj.CurrentState,
				label:    obj.EffectiveDisplayName(asset.Name),
				capacity: capacity,
			})
		}
		return scan, nil
	}})
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/noticeboard: kickstart enumerate: %v", err)
		}
		return
	}
	scan, ok := res.(kickstartScan)
	if !ok {
		log.Printf("cascade/noticeboard: unexpected kickstart result type %T", res)
		return
	}
	// Clear stale content on empty boards first (cheap, world-goroutine
	// Commands), then author the blanks.
	for _, c := range scan.clears {
		if _, err := w.SendContext(ctx, sim.ClearNoticeboardContent(c.id, c.atState, time.Now())); err != nil && ctx.Err() == nil {
			log.Printf("cascade/noticeboard: kickstart clear board %q state=%q: %v", c.id, c.atState, err)
		}
	}
	if len(scan.blanks) == 0 {
		return
	}
	log.Printf("cascade/noticeboard: kickstart authoring %d blank board(s)", len(scan.blanks))
	for _, b := range scan.blanks {
		go runNoticeboardAuthor(ctx, w, client, b.id, b.atState, b.label, "", b.capacity)
	}
}

// handleNoticeboardStateChange is the VillageObjectStateChanged
// subscriber. It enforces the empty-board invariant: when a board rotates
// to a zero-capacity (empty) state, any prior content is cleared so the
// emptied board stops showing a stale notice (v1 ZBBS-112).
//
// It does NOT author content. Authoring is initiated explicitly —
// KickstartNoticeboards at boot, the town-crier route at runtime
// (LLM-44) — never reactively off a flip. A reactive author here would
// fire on the crier's own count-matched flip and overwrite the notices
// she just posted and read, reintroducing the read/shown mismatch this
// design eliminates. Boards have no other flip source (the bulk rotation
// ticker excludes TagNoticeBoard), so the only non-crier flip to guard is
// an admin/manual one — hence the clear path stays as a safety net.
func handleNoticeboardStateChange(w *sim.World, evt sim.Event) {
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
	if sim.ContentCapacityForState(state) > 0 {
		return
	}
	// Zero-capacity (empty) board — clear any prior content. Inline on the
	// world goroutine (we're in a subscriber); cheap, no LLM call.
	sim.ClearNoticeboardContent(changed.ObjectID, changed.ToState, time.Now()).Fn(w)
}

// authorNoticeboardText runs the off-world salem-generic author call for a
// board sized to `capacity` and returns the clamped content (at most
// `capacity` notice lines, one per sprite slip, newline-separated), or ""
// on any failure / empty reply. Shared by KickstartNoticeboards (boot
// seeding) and the town-crier route (runtime, LLM-44). objectID/atState are
// log context only — the caller decides what state to store the result at.
//
// Fetches a fresh village snapshot, builds the prompt, calls salem-generic,
// trims/unquotes/clamps. The LLM-call budget is capped so a wedged provider
// doesn't pin this goroutine forever.
func authorNoticeboardText(ctx context.Context, w *sim.World, client llm.Client, objectID sim.VillageObjectID, atState, boardLabel, priorText string, capacity int) string {
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
		return ""
	}
	snap, ok := ctxRes.(sim.VillageContext)
	if !ok {
		log.Printf("cascade/noticeboard: unexpected FetchVillageContext result type %T", ctxRes)
		return ""
	}

	messages := buildNoticeboardPrompt(snap, boardLabel, priorText, capacity)
	resp, err := client.Complete(callCtx, llm.Request{
		Messages:    messages,
		Model:       noticeboardLLMModel,
		Temperature: 0.7,
		// Scale the budget to the board's line capacity — up to 4 short
		// notices need more room than one. ~100 tokens/line plus a base.
		MaxTokens: noticeboardMaxTokens(capacity),
		// Fresh scene per authoring call: memory-api's chat_messages
		// history loader filters by scene_id when set, so each notice
		// authoring is its own isolated conversation — without this,
		// salem-generic would accumulate every prior board prompt as
		// history.
		SceneID: llm.NewSceneID(),
	})
	if err != nil {
		if callCtx.Err() == nil {
			log.Printf("cascade/noticeboard: Complete (%q, state=%q): %v", objectID, atState, err)
		}
		return ""
	}
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		log.Printf("cascade/noticeboard: empty LLM reply for %q state=%q", objectID, atState)
		return ""
	}
	// Drop wrapping quotes the model may have added despite the
	// "no quotation marks" instruction.
	text = strings.Trim(text, "\"'")
	text = strings.TrimSpace(text)
	if text == "" {
		log.Printf("cascade/noticeboard: empty after unquote for %q state=%q", objectID, atState)
		return ""
	}

	// Clamp to the board's capacity contract: at most N notice lines (one per
	// sprite slip), each within the per-line cap, newline-separated. The model
	// is asked for N lines but may over- or under-produce; this is the
	// authoritative shaping before the content lands.
	text = sim.ClampNoticeboardContent(text, capacity, sim.MaxNoticeboardLineLen)
	if text == "" {
		log.Printf("cascade/noticeboard: empty after clamp for %q state=%q", objectID, atState)
		return ""
	}
	return text
}

// runNoticeboardAuthor is KickstartNoticeboards' off-world goroutine body:
// author content for a blank board and save it at the board's boot state.
// (The crier's runtime path does NOT go through here — it sets the board
// variant to match the authored count before saving; see finishCrierBoardStop.)
func runNoticeboardAuthor(ctx context.Context, w *sim.World, client llm.Client, objectID sim.VillageObjectID, atState, boardLabel, priorText string, capacity int) {
	text := authorNoticeboardText(ctx, w, client, objectID, atState, boardLabel, priorText, capacity)
	if text == "" {
		return
	}
	// Bound the save like the fetch/LLM call (authorNoticeboardText owns its own
	// timeout context and cancels it on return), so a wedged world command queue
	// can't block this goroutine indefinitely.
	saveCtx, cancel := context.WithTimeout(ctx, noticeboardLLMTimeout)
	defer cancel()
	saveRes, err := w.SendContext(saveCtx, sim.SaveNoticeboardContent(objectID, text, atState, time.Now()))
	if err != nil {
		if saveCtx.Err() == nil {
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

// beginCrierBoardStop drives one town-crier board visit (LLM-44). Author-first:
// the crier authors today's notices, posts them (the board variant is set to
// match the number actually authored), reads them aloud, then advances — so she
// reads exactly what she posts, not the prior cycle's stale notice. A no-news
// day (the route's target for this stop is the empty, zero-capacity variant)
// posts the empty board, clears any prior content, says nothing, and moves on.
//
// Runs on the world goroutine (from the ActorArrived subscriber). For the
// capacity>0 case the LLM author runs off-world, so this kicks a goroutine and
// returns; the post/read/route-advance happen back on the world goroutine in
// finishCrierBoardStop. The empty-day case is fully synchronous here.
func beginCrierBoardStop(ctx context.Context, w *sim.World, client llm.Client, route *sim.NPCRoute, stopIdx int, stop sim.RouteStop) {
	crierID := route.NPCID
	obj, hasObj := w.VillageObjects[stop.ObjectID]
	if !hasObj || obj == nil {
		advanceCrierWalk(w, crierID, stop.ObjectID)
		return
	}
	asset, hasAsset := w.Assets[obj.AssetID]
	if !hasAsset || asset == nil {
		advanceCrierWalk(w, crierID, stop.ObjectID)
		return
	}

	capacity := sim.ContentCapacityForState(asset.FindState(stop.NewState))
	if capacity <= 0 {
		// No-news day: post the empty variant, clear prior content, say nothing.
		// SetVillageObjectState never returns an error (it no-ops on not_found /
		// already_at_target); the empty variant is a valid state, so the flip
		// always lands and ClearNoticeboardContent's stale-guard then matches.
		sim.SetVillageObjectState(stop.ObjectID, stop.NewState).Fn(w)
		sim.ClearNoticeboardContent(stop.ObjectID, stop.NewState, time.Now()).Fn(w)
		advanceCrierWalk(w, crierID, stop.ObjectID)
		return
	}

	boardLabel := obj.EffectiveDisplayName(asset.Name)
	var priorText string
	if w.NoticeboardContent != nil {
		if prior, ok := w.NoticeboardContent[stop.ObjectID]; ok && prior != nil {
			priorText = prior.Text
		}
	}
	objectID := stop.ObjectID
	targetState := stop.NewState
	// Mark the stop as authoring so a duplicate arrival is ignored until the
	// callback completes (set/cleared only on the world goroutine).
	route.Authoring = true
	go func() {
		text := authorNoticeboardText(ctx, w, client, objectID, targetState, boardLabel, priorText, capacity)
		post := sim.Command{Fn: func(world *sim.World) (any, error) {
			finishCrierBoardStop(world, route, stopIdx, objectID, text)
			return nil, nil
		}}
		if _, err := w.SendContext(ctx, post); err != nil && ctx.Err() == nil {
			log.Printf("cascade/noticeboard: crier post (%q): %v", objectID, err)
		}
	}()
}

// finishCrierBoardStop posts + reads the authored notices and schedules the
// route advance. Runs on the world goroutine (the author goroutine marshals
// back here via SendContext). Guards against a fresh tour having superseded
// this route while the author LLM call was in flight.
func finishCrierBoardStop(w *sim.World, route *sim.NPCRoute, stopIdx int, objectID sim.VillageObjectID, text string) {
	// Clear the in-flight marker on the captured route. If a fresh tour
	// superseded us, this route object is now orphaned and clearing it is
	// harmless; the guard below drops the post against the new route.
	route.Authoring = false
	crierID := route.NPCID

	if !crierStillAtStop(w, route, stopIdx, objectID) {
		return // a fresh tour superseded this exact route during the author call
	}

	if strings.TrimSpace(text) == "" {
		// Authoring failed/empty — keep the prior posting, say nothing, move on.
		// (Distinct from the intentional no-news day handled in beginCrierBoardStop:
		// there she posts the empty variant; here the board is left unchanged.)
		advanceCrierWalk(w, crierID, objectID)
		return
	}

	// Set the board variant to match the number of notices actually authored
	// (LLM-44: drawn = read = shown), then save the content at that state.
	lines := splitNoticeLines(text)
	matchState := noticeboardStateForCapacity(w, objectID, len(lines))
	if matchState == "" {
		// No state declares this capacity (misconfigured asset) — save against
		// the current state so the stale-guard doesn't drop the content; the
		// variant just won't match the count (the pre-LLM-44 status quo).
		if obj, ok := w.VillageObjects[objectID]; ok && obj != nil {
			matchState = obj.CurrentState
		}
		log.Printf("cascade/noticeboard: no board state for capacity %d (object %q) — content kept, variant unmatched", len(lines), objectID)
	}

	// SetVillageObjectState never errors (no-ops on not_found / already_at_target),
	// so the success signal is the SAVE result: only read aloud once the content
	// is actually stored, so a stale-dropped save never leaves her reading what
	// the board doesn't show.
	sim.SetVillageObjectState(objectID, matchState).Fn(w)
	saveRes, err := sim.SaveNoticeboardContent(objectID, text, matchState, time.Now()).Fn(w)
	if r, ok := saveRes.(sim.SaveNoticeboardContentResult); err != nil || !ok || !r.Applied {
		reason := "send_error"
		if r, ok := saveRes.(sim.SaveNoticeboardContentResult); ok {
			reason = r.SkipReason
		}
		log.Printf("cascade/noticeboard: crier save not applied (%q state=%q reason=%s) — skipping read", objectID, matchState, reason)
		advanceCrierWalk(w, crierID, objectID)
		return
	}

	// Read the notices she just posted — one bubble per beat — then advance the
	// route after the spiel so she finishes reading before walking on. The
	// trailing beat gives the final notice the same on-screen window every
	// earlier one gets (ZBBS-HOME-457 reasoning, now applied to the read rather
	// than a post-read flip).
	voiced := voiceCrierNotices(w, crierID, text, time.Now())
	if voiced < 1 {
		voiced = 1
	}
	scheduleCrierAdvance(w, route, stopIdx, objectID, time.Duration(voiced)*crierNoticeBeatDelay)
}

// noticeboardStateForCapacity returns the asset's rotatable notice-board state
// whose content-capacity tag equals `capacity` (lowest AssetStateID first, via
// RotatablePool's stable order), or "" if none. The crier uses it to set a
// board's variant to match the number of notices it authored.
func noticeboardStateForCapacity(w *sim.World, objectID sim.VillageObjectID, capacity int) string {
	obj, ok := w.VillageObjects[objectID]
	if !ok || obj == nil {
		return ""
	}
	asset, ok := w.Assets[obj.AssetID]
	if !ok || asset == nil {
		return ""
	}
	for _, s := range asset.RotatablePool() {
		if s.HasTag(sim.TagNoticeBoard) && sim.ContentCapacityForState(s) == capacity {
			return s.State
		}
	}
	return ""
}

// crierStillAtStop reports whether `route` is STILL the crier's installed
// active route, parked at the same stop on the same board — the guard a
// deferred post/advance must pass so it doesn't act on a route a fresh tour
// superseded mid-author/mid-dwell. The pointer compare (w.ActiveRoutes[crier]
// == route) proves identity: StartNPCRoute installs a fresh *NPCRoute on
// supersede, so a superseding tour replaces the map entry and the captured
// pointer no longer matches — even if the new route happens to sit at the same
// stop index / board.
func crierStillAtStop(w *sim.World, route *sim.NPCRoute, stopIdx int, objectID sim.VillageObjectID) bool {
	r, ok := w.ActiveRoutes[route.NPCID]
	return ok && r == route && r.Phase == sim.RoutePhaseActive &&
		r.StopIdx == stopIdx && r.StopIdx < len(r.Stops) && r.Stops[r.StopIdx].ObjectID == objectID
}

// advanceCrierWalk advances the crier's route by one stop WITHOUT a flip (she
// owns her board state). Inline on the world goroutine — used for the no-news
// day and the author-failed path, where there is no spiel to dwell through.
func advanceCrierWalk(w *sim.World, crierID sim.ActorID, objectID sim.VillageObjectID) {
	if _, err := sim.AdvanceNPCRouteSkipFlip(crierID).Fn(w); err != nil {
		log.Printf("cascade/noticeboard: crier advance (object %q): %v", objectID, err)
	}
}

// scheduleCrierAdvance advances the crier's route after a dwell (the spiel's
// length) so she finishes reading before walking on. Guarded against a fresh
// tour superseding this route during the dwell. Mirrors the AfterFunc +
// SendContext + LifecycleContext pattern the silence / pay-ledger sweeps use.
func scheduleCrierAdvance(w *sim.World, route *sim.NPCRoute, stopIdx int, objectID sim.VillageObjectID, dwell time.Duration) {
	crierID := route.NPCID
	time.AfterFunc(dwell, func() {
		ctx := w.LifecycleContext()
		if ctx.Err() != nil {
			return // shutdown raced the dwell
		}
		guarded := sim.Command{Fn: func(world *sim.World) (any, error) {
			if !crierStillAtStop(world, route, stopIdx, objectID) {
				return nil, nil // route changed during the dwell — drop the advance
			}
			return sim.AdvanceNPCRouteSkipFlip(crierID).Fn(world)
		}}
		if _, err := w.SendContext(ctx, guarded); err != nil && ctx.Err() == nil {
			log.Printf("cascade/noticeboard: deferred crier advance (actor %q): %v", crierID, err)
		}
	})
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
func buildNoticeboardPrompt(snap sim.VillageContext, boardLabel, priorText string, capacity int) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleSystem, Content: noticeboardSystemPrompt(capacity)},
		{Role: llm.RoleUser, Content: buildNoticeboardUserPrompt(snap, boardLabel, priorText, capacity)},
	}
}

// noticeboardMaxTokens scales the completion budget to the board's line
// capacity — one short notice fits comfortably in ~200 tokens, and each
// additional line needs roughly another 100.
func noticeboardMaxTokens(capacity int) int {
	if capacity < 1 {
		capacity = 1
	}
	return 100 + 100*capacity
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
func noticeboardSystemPrompt(capacity int) string {
	if capacity < 1 {
		capacity = 1
	}
	return strings.Join([]string{
		noticeboardIntroLine(capacity),
		"",
		"GENRES of notices that are appropriate (each notice falls into one genre):",
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
		noticeboardVoiceCountLine(capacity),
		"  - No quotation marks around the notice itself.",
		"",
		noticeboardOutputFormatLine(capacity),
	}, "\n")
}

// noticeboardIntroLine is the capacity-aware opening of the system prompt —
// singular for a one-line board, plural with the slot count otherwise.
func noticeboardIntroLine(capacity int) string {
	if capacity == 1 {
		return "You are composing a single notice for a noticeboard in a Salem, Massachusetts village in the year 1692."
	}
	return fmt.Sprintf("You are composing %d distinct notices for a noticeboard in a Salem, Massachusetts village in the year 1692. The board has room for %d notices pinned together.", capacity, capacity)
}

// noticeboardVoiceCountLine is the VOICE bullet governing notice length.
func noticeboardVoiceCountLine(capacity int) string {
	if capacity == 1 {
		return "  - One short sentence, no preamble, no salutation, no signature."
	}
	return "  - Each notice is one short sentence, no preamble, no salutation, no signature."
}

// noticeboardOutputFormatLine is the OUTPUT FORMAT instruction — one line vs.
// exactly N lines, one notice per line.
func noticeboardOutputFormatLine(capacity int) string {
	if capacity == 1 {
		return "OUTPUT FORMAT: Return only the notice text itself, on a single line. No prefatory \"Here is the notice:\" or similar. No surrounding quotes."
	}
	return fmt.Sprintf("OUTPUT FORMAT: Return exactly %d notices, one per line, each on its own line. No blank lines, no numbering, no bullet points, no prefatory text, and no surrounding quotes. Each notice stands alone and concerns a different matter.", capacity)
}

// buildNoticeboardUserPrompt renders the dynamic per-call context:
// board label + curated village state + framing instruction.
func buildNoticeboardUserPrompt(snap sim.VillageContext, boardLabel, priorText string, capacity int) string {
	if capacity < 1 {
		capacity = 1
	}
	var b strings.Builder

	if capacity == 1 {
		fmt.Fprintf(&b, "Compose a single notice for the board: %s.\n\n", strings.TrimSpace(boardLabel))
	} else {
		fmt.Fprintf(&b, "Compose %d notices for the board: %s.\n\n", capacity, strings.TrimSpace(boardLabel))
	}

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

	if capacity == 1 {
		b.WriteString("Now compose today's single notice for this board, drawing on the genre catalog and the village context above.")
	} else {
		fmt.Fprintf(&b, "Now compose today's %d notices for this board — one per line, each concerning a different matter — drawing on the genre catalog and the village context above.", capacity)
	}
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
