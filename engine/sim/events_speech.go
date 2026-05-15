package sim

import "time"

// Spoke fires when an actor commits a speak tool call against a world that
// has resolved the recipient set. RecipientIDs is the speaker's current
// huddle peer set at commit time (excluding the speaker), sorted by
// ActorID for deterministic iteration. An empty huddle (speaker not in any
// huddle, or in a huddle of size one) is a valid state — the event still
// emits so any non-relationship subscribers (future audit log, future WS
// broadcast) see the speech happened; the speech reactor subscriber and
// the per-peer relationship writes both no-op when the set is empty.
//
// HuddleID is the speaker's CurrentHuddleID at commit time, or empty when
// the speaker has no current huddle. It is a denormalization for
// subscribers that want huddle context without a second lookup; the
// authoritative set is RecipientIDs.
//
// Text is the post-validation speech text: whitespace-trimmed,
// rune-length ≤ MaxSpeakTextChars (1000 Unicode characters; JSON
// Schema's maxLength is character-based per spec, and the engine-side
// check uses utf8.RuneCountInString so the two layers agree),
// control-character-rejected (only \n \r \t allowed outside the
// printable range). The speech reactor subscriber truncates
// this to MaxSalientFactTextLen runes for the warrant Excerpt, but the
// full text travels on the event for any consumer that wants the complete
// utterance (debug logs, future audit, future WS clients).
//
// SpeechID is the canonical identifier for this utterance. By design it
// aliases the event's EventID — the speech reactor subscriber copies
// EventID into both the warrant payload's SpeechID and the WarrantMeta's
// SourceEventID, so a single number traces a speech through emit →
// payload → warrant dedup key → admin replay. See PCSpeechWarrantReason /
// NPCSpeechWarrantReason in reactor.go.
//
// At is wall-clock; it carries the same instant the per-peer
// RecordInteraction calls use for SalientFact timestamps so the engine
// log and the relationship facts line up exactly.
type Spoke struct {
	EventBase
	SpeakerID    ActorID
	HuddleID     HuddleID
	RecipientIDs []ActorID
	Text         string
	At           time.Time
}

func (Spoke) isSimEvent() {}
