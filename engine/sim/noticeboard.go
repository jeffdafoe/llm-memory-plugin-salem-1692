package sim

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// noticeboard.go — substrate for engine-authored noticeboard content.
//
// World.NoticeboardContent stores per-board prose (the line a town
// crier reads on arrival; what NPCs loitering at the board perceive
// when that path ports). The content is engine-authored: a cascade
// subscriber on VillageObjectStateChanged spawns an off-world LLM
// call against the salem-generic VA, and SaveNoticeboardContent
// installs the result back on the world.
//
// Stale-guard pattern (mirrors v1's ZBBS-112 R1 fix): the authoring
// goroutine captures the new-state name at trigger time and threads
// it into SaveNoticeboardContent. The Command only persists if the
// object's CurrentState still matches that name at apply time. A
// rapid second rotation that flipped the board to a different state
// before the LLM responded → save rejected silently; the next
// rotation cycle re-triggers authoring.
//
// EmitTownCrierAnnouncement is the speech emitter the town_crier
// route cascade calls on stop arrival to voice the existing content.
// Simpler than EmitBusinessownerSpeech — no cooldown, no per-pair
// suppression, no listener-specific recipients (atmospheric speech,
// not interactive).
//
// Per-state content capacity (ZBBS-HOME-456, v1 ZBBS-112 parity): a board
// state declares how many notice lines it holds via a content-capacity-N
// asset_state_tag — the Notice Board asset's variant-2..5 carry
// content-capacity-1..4, and variant-1 carries none (an empty board). The
// authoring cascade sizes its request to the current state's capacity and
// SaveNoticeboardContent stores N newline-separated lines, so the number of
// notices shown matches the number depicted on the sprite. Rotating to a
// zero-capacity state clears prior content (see ClearNoticeboardContent).
//
// What's deferred:
//
//   - Per-instance noticeboard opt-in tag (v1's ZBBS-112 let admins
//     opt specific placements in via village_object_tag; v2 MVP
//     treats every rotatable + notice-board-tagged state as a
//     content surface).
//   - Concerns layer (v1's ZBBS-117 added a `{notice, concerns}` JSON
//     envelope tying notices to named-entity facts; v2 MVP authors
//     flat lines).
//   - NPC perception line for actors loitering at a noticeboard
//     (v1's `noticeBoardLineForLoiterer`); will land with the
//     loiter-perception slot.

// objectContentCapacityTagPrefix is the asset_state_tag prefix declaring how
// many notice lines a board state holds. The suffix is the integer capacity,
// parsed at lookup time (see ContentCapacityForState). Matches v1's ZBBS-112
// tag form so the live asset data (variant-2..5 → content-capacity-1..4) is
// reused as-is.
const objectContentCapacityTagPrefix = "content-capacity-"

// MaxNoticeboardLineLen caps each individual notice line (rune-aware). One
// line is what the crier voices per beat and what a panel row renders, so the
// per-line cap — not the total — is the defensive bound against a runaway
// model line. Mirrors v1's per-line clamp.
const MaxNoticeboardLineLen = 240

// MaxNoticeboardContentLen caps the total stored content (all lines joined
// with newlines). Sized to hold the maximum board capacity (5) of
// MaxNoticeboardLineLen lines plus separators, with headroom. The authoring
// cascade already clamps to the state's exact capacity via
// ClampNoticeboardContent; this is the substrate's defensive backstop against
// a caller that skips that clamp.
const MaxNoticeboardContentLen = 1280

// ContentCapacityForState returns the notice-line capacity declared by the
// state's content-capacity-N tag, or 0 when the state carries no such tag (an
// empty board). A missing, malformed, negative, or duplicated capacity tag is
// treated as 0 — controlled reference data, but never panic on a bad tag.
func ContentCapacityForState(state *AssetState) int {
	if state == nil {
		return 0
	}
	capacity, found := 0, 0
	for _, t := range state.Tags {
		if !strings.HasPrefix(t, objectContentCapacityTagPrefix) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(t, objectContentCapacityTagPrefix))
		if err != nil || n < 0 {
			return 0
		}
		capacity = n
		found++
	}
	if found != 1 {
		return 0
	}
	return capacity
}

// ClampNoticeboardContent normalizes authored text to a board's capacity
// contract: trims, splits on newlines, drops empty lines, caps the count to
// maxLines, truncates each surviving line to maxLineLen runes (rune-aware so a
// multi-byte sequence is never split), and rejoins with "\n". Returns "" when
// nothing survives or maxLines <= 0. Port of v1's clampNoticeContent
// (ZBBS-112): the stored shape is exactly N notice lines, one per sprite slip.
func ClampNoticeboardContent(text string, maxLines, maxLineLen int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	out := make([]string, 0, maxLines)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if maxLineLen > 0 {
			if runes := []rune(trimmed); len(runes) > maxLineLen {
				trimmed = string(runes[:maxLineLen])
			}
		}
		out = append(out, trimmed)
		if len(out) == maxLines {
			break
		}
	}
	return strings.Join(out, "\n")
}

// NoticeboardContent is one board's authored prose at a given state.
// AtState is the state name the content was authored for — the
// SaveNoticeboardContent stale-guard rejects a save whose AtState
// doesn't match the object's current CurrentState at apply time.
//
// PostedAt is the wall-clock instant the content landed via
// SaveNoticeboardContent.
type NoticeboardContent struct {
	Text     string
	PostedAt time.Time
	AtState  string
}

// SaveNoticeboardContentResult is the typed reply from
// SaveNoticeboardContent. Applied=true iff the content was actually
// stored; SkipReason describes the "not stored" cases:
//
//   - "not_found": no village_object with that ID.
//   - "stale_state": object's CurrentState != atState (a rotation
//     overtook the authoring goroutine).
//   - "empty_after_trim": text was empty / whitespace-only after
//     trimming (caller already trims; substrate enforces the
//     invariant).
type SaveNoticeboardContentResult struct {
	Applied    bool
	SkipReason string
}

// SaveNoticeboardContent installs `text` as the noticeboard content
// for the given village_object, gated on the object's CurrentState
// still matching atState (stale-guard). Returns Applied=true on
// successful store; Applied=false + populated SkipReason for the
// rejection branches.
//
// The text is trimmed (mirrors atmosphere's posture) and rune-
// truncated to MaxNoticeboardContentLen before save. An empty post-
// trim string returns "empty_after_trim" and stores nothing.
//
// MUST be invoked on the world goroutine. The cascade authoring
// goroutine routes through w.SendContext.
func SaveNoticeboardContent(id VillageObjectID, text, atState string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return SaveNoticeboardContentResult{Applied: false, SkipReason: "empty_after_trim"}, nil
			}
			if runes := []rune(trimmed); len(runes) > MaxNoticeboardContentLen {
				trimmed = string(runes[:MaxNoticeboardContentLen])
			}

			obj, ok := w.VillageObjects[id]
			if !ok {
				return SaveNoticeboardContentResult{Applied: false, SkipReason: "not_found"}, nil
			}
			if obj.CurrentState != atState {
				// Rotation overtook us — silently drop. Next cycle's
				// authoring will produce content for whatever state
				// the board has landed on.
				return SaveNoticeboardContentResult{Applied: false, SkipReason: "stale_state"}, nil
			}
			if w.NoticeboardContent == nil {
				w.NoticeboardContent = map[VillageObjectID]*NoticeboardContent{}
			}
			w.NoticeboardContent[id] = &NoticeboardContent{
				Text:     trimmed,
				PostedAt: at,
				AtState:  atState,
			}
			w.emit(&NoticeboardContentChanged{
				ObjectID: id,
				Text:     trimmed,
				PostedAt: at,
				At:       at,
			})
			return SaveNoticeboardContentResult{Applied: true}, nil
		},
	}
}

// ClearNoticeboardContent removes any stored content for a board, gated on the
// object's CurrentState still matching atState (the same stale-guard
// SaveNoticeboardContent uses). The authoring cascade calls this when a board
// rotates to a zero-capacity state (the empty sprite) — v1 cleared prior
// content on such a flip so an emptied board doesn't keep voicing/showing a
// stale notice. Emits NoticeboardContentChanged with empty Text so the client
// clears its panel. Returns Applied=false + "nothing_to_clear" when no content
// was stored.
//
// MUST be invoked on the world goroutine.
func ClearNoticeboardContent(id VillageObjectID, atState string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			obj, ok := w.VillageObjects[id]
			if !ok {
				return SaveNoticeboardContentResult{Applied: false, SkipReason: "not_found"}, nil
			}
			if obj.CurrentState != atState {
				return SaveNoticeboardContentResult{Applied: false, SkipReason: "stale_state"}, nil
			}
			if w.NoticeboardContent == nil {
				return SaveNoticeboardContentResult{Applied: false, SkipReason: "nothing_to_clear"}, nil
			}
			if _, present := w.NoticeboardContent[id]; !present {
				return SaveNoticeboardContentResult{Applied: false, SkipReason: "nothing_to_clear"}, nil
			}
			delete(w.NoticeboardContent, id)
			w.emit(&NoticeboardContentChanged{
				ObjectID: id,
				Text:     "",
				PostedAt: at,
				At:       at,
			})
			return SaveNoticeboardContentResult{Applied: true}, nil
		},
	}
}

// EmitTownCrierAnnouncementResult is the typed reply from
// EmitTownCrierAnnouncement. Fired=true iff a Spoke event was
// emitted; SkipReason describes the "not fired" cases:
//
//   - "speaker_missing": no actor with the given SpeakerID.
//   - "speaker_not_town_crier": actor exists but doesn't carry
//     AttrTownCrier — enforces the command's town-crier-only
//     scope (the substrate-level v1-faithful gate).
//   - "empty_content": content was empty / whitespace-only after
//     trim.
type EmitTownCrierAnnouncementResult struct {
	Fired      bool
	SkipReason string
}

// EmitTownCrierAnnouncement returns a Command that emits a Spoke
// event for the town crier with `content` as the spoken text. The
// recipient set is empty (atmospheric speech, no listener warrants
// stamped); HuddleID is empty (outdoor). The standard Spoke
// subscribers handle ActionLog + downstream propagation.
//
// Content is one notice line — the crier voices a multi-line board one
// line per call (each its own Spoke beat). It is trimmed + rune-truncated
// to MaxNoticeboardLineLen (the per-line bound; the typical caller passes a
// single ClampNoticeboardContent line which is already within it, but the
// guard defends against callers that skip that clamp).
//
// Speaker MUST carry AttrTownCrier — this Command is the engine-
// authored town crier voice, NOT a general atmospheric speech
// helper. Calling with a non-crier actor returns Fired=false +
// "speaker_not_town_crier" (defense-in-depth even though the cascade
// already gates on `route.Label == AttrTownCrier`).
//
// MUST be invoked on the world goroutine. Cascade subscribers call
// inline via cmd.Fn(w); external callers go through w.SendContext.
func EmitTownCrierAnnouncement(speakerID ActorID, content string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if speakerID == "" {
				return EmitTownCrierAnnouncementResult{}, fmt.Errorf("EmitTownCrierAnnouncement: SpeakerID required")
			}
			trimmed := strings.TrimSpace(content)
			if trimmed == "" {
				return EmitTownCrierAnnouncementResult{Fired: false, SkipReason: "empty_content"}, nil
			}
			if runes := []rune(trimmed); len(runes) > MaxNoticeboardLineLen {
				trimmed = string(runes[:MaxNoticeboardLineLen])
			}
			actor, ok := w.Actors[speakerID]
			if !ok || actor == nil {
				return EmitTownCrierAnnouncementResult{Fired: false, SkipReason: "speaker_missing"}, nil
			}
			if _, hasAttr := actor.Attributes[AttrTownCrier]; !hasAttr {
				return EmitTownCrierAnnouncementResult{Fired: false, SkipReason: "speaker_not_town_crier"}, nil
			}
			w.emit(&Spoke{
				SpeakerID:    speakerID,
				HuddleID:     "",
				RecipientIDs: nil,
				Text:         trimmed,
				At:           at,
			})
			return EmitTownCrierAnnouncementResult{Fired: true}, nil
		},
	}
}
