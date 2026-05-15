package sim

import "time"

// Paid fires when an actor commits a pay tool call against a world that has
// resolved the recipient. Unlike Spoke (which fans out across a huddle peer
// set), Paid is 1:1 — one buyer, one seller. The same-huddle gate inside
// sim.Pay guarantees BuyerID and SellerID share CurrentHuddleID at commit
// time, but the event itself doesn't carry HuddleID: any consumer that needs
// huddle context can look up either actor's current state.
//
// Amount is in whole coins, post-validation: > 0 and <= math.MaxInt32 (the
// handler decode-side rejects zero/negative/oversized values, and the
// Command Fn rejects insufficient-balance before this event emits).
//
// ForText is the post-validation flavor text: trimmed, length <= 200
// characters, control-character-rejected (only \n \r \t allowed outside the
// printable range). The pay reactor subscriber rune-truncates this to
// MaxSalientFactTextLen for the warrant Excerpt, but the full string travels
// on the event for any consumer that wants it.
//
// PaidID — the canonical identifier for this pay transaction — aliases the
// event's EventID (same pattern as SpeechID/Spoke). The pay reactor
// subscriber copies EventID into both the warrant payload's PaidID and the
// WarrantMeta's SourceEventID, so one number traces a pay through emit →
// payload → warrant dedup key → admin replay.
//
// At is wall-clock; it carries the same instant the per-pair
// RecordInteraction calls use for SalientFact timestamps so the engine log
// and the relationship facts line up exactly.
type Paid struct {
	EventBase
	BuyerID  ActorID
	SellerID ActorID
	Amount   int
	ForText  string
	At       time.Time
}

func (Paid) isSimEvent() {}
