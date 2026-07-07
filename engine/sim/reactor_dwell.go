package sim

// reactor_dwell.go — Phase 3 dwell perception PR. WarrantReason types
// for the dwell-lifecycle decision points (DwellTickApplied /
// DwellEnded). Subscribers in engine/sim/handlers/dwell_reactor.go
// mint these onto the eater's reactor warrant list so the LLM's
// NEXT-tick perception surfaces the cue text. (DwellStarted mints no
// warrant — LLM-316; see dwell_reactor.go.)
//
// Both Reasons return DedupDiscriminator=0 — same posture as
// BasicWarrantReason for lifecycle warrants. Each dwell event already
// fires 1:1 with its triggering moment (per-tick payoff /
// terminal transition), so the existing event-sourced dedup paths in
// tryStampWarrant have nothing to suppress; bypass dedup keeps
// (Kind, 0) from collapsing unrelated dwell stamps. Restart re-stamping
// is not needed either — the structured perception surface
// (ActorView.ActiveDwellCredits) carries the "you are still eating"
// signal across LoadWorld for free; momentary cues don't replay.
//
// Payload shape: each Reason carries enough to render the cue without
// re-reading world state. NarrationText is pre-rendered at the
// subscriber so render-time work stays cheap, and the same string is
// available for the Hub broadcast layer (when ported) to fan out as a
// PC HUD line.

// DwellTickAppliedWarrantReason captures one per-minute payoff. Each
// tick during a meal/rest produces one. The LLM perception cue is the
// felt-language line from DwellTickNarration ("you take another bite,
// the gnawing ebbs"), so the actor experiences the meal as a thread of
// updates rather than a silent need-value drop.
//
// RemainingTicks is the post-decrement count for source=item credits
// (so the final tick reports 0 and a paired DwellEndedWarrantReason
// fires alongside). Nil for source=object credits.
//
// PeriodMinutes carried through so Hub clients can render the next-
// tick wall-clock without state.
type DwellTickAppliedWarrantReason struct {
	ObjectID       VillageObjectID
	Source         DwellCreditSource
	ItemKind       ItemKind
	Attribute      NeedKey
	NeedDelta      int
	NewNeedValue   int
	RemainingTicks *int
	PeriodMinutes  int
	NarrationText  string
}

func (DwellTickAppliedWarrantReason) isWarrantReason()           {}
func (DwellTickAppliedWarrantReason) Kind() WarrantKind          { return WarrantKindDwellTickApplied }
func (DwellTickAppliedWarrantReason) DedupDiscriminator() uint64 { return 0 }

// DwellEndedWarrantReason captures the terminal cue for a finished /
// abandoned / fulfilled credit. Reason discriminates which terminal
// narration to use ("you finish the last bite, satisfied" vs "you feel
// full" vs "you walk away from your meal"). CatalogUnknown is the
// defensive case and carries no narration.
type DwellEndedWarrantReason struct {
	ObjectID      VillageObjectID
	Source        DwellCreditSource
	ItemKind      ItemKind
	Attribute     NeedKey
	Reason        DwellEndReason
	NarrationText string
}

func (DwellEndedWarrantReason) isWarrantReason()           {}
func (DwellEndedWarrantReason) Kind() WarrantKind          { return WarrantKindDwellEnded }
func (DwellEndedWarrantReason) DedupDiscriminator() uint64 { return 0 }
