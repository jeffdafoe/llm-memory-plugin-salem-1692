package sim

import (
	"fmt"
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
// What's deferred:
//
//   - Per-state content-capacity-N tags (v1's ZBBS-112 supported
//     variable line counts; v2 MVP is single-line per state).
//   - Per-instance noticeboard opt-in tag (v1's ZBBS-112 let admins
//     opt specific placements in via village_object_tag; v2 MVP
//     treats every rotatable + notice-board-tagged state as a
//     content surface).
//   - Concerns layer (v1's ZBBS-117 added a `{notice, concerns}` JSON
//     envelope tying notices to named-entity facts; v2 MVP authors
//     a flat string).
//   - NPC perception line for actors loitering at a noticeboard
//     (v1's `noticeBoardLineForLoiterer`); will land with the
//     loiter-perception slot.

// MaxNoticeboardContentLen caps content text at write time to defend
// downstream surfaces (Spoke event text, perception rendering)
// against runaway LLM output that ignored the prompt's "one short
// line" instruction. Rune-aware, mirrors MaxSalientFactTextLen's
// posture but sized larger for a notice-line vs a per-fact excerpt.
const MaxNoticeboardContentLen = 280

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
				At:       time.Now().UTC(),
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
// Content is trimmed + rune-truncated to MaxNoticeboardContentLen
// (mirrors SaveNoticeboardContent's defensive cap; the typical caller
// pulls from NoticeboardContent which is already capped, but the
// guard defends against future authoring paths that skip the cap).
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
			if runes := []rune(trimmed); len(runes) > MaxNoticeboardContentLen {
				trimmed = string(runes[:MaxNoticeboardContentLen])
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
