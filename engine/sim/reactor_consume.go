package sim

// reactor_consume.go — the immediate consume self-narration warrant reason
// (ZBBS-HOME-302 §A). Sibling to reactor_dwell.go: where the dwell reasons
// carry the per-tick / terminal felt beats, this carries the ONE-SHOT
// "you eat the bread, the gnawing ebbs" beat that fires the moment an item is
// consumed and a need actually moves. Stamped by the subscriber in
// engine/sim/handlers/consume_reactor.go so the eater's next reactor tick
// perceives the cue.
//
// DedupDiscriminator=0 — same posture as the dwell reasons. Each ItemConsumed
// is 1:1 with a consume action, so there is nothing to suppress; bypassing
// dedup keeps unrelated consume beats from collapsing under (Kind, 0).
//
// NarrationText is pre-rendered at the subscriber (sim.ConsumeNarration) so
// render-time work stays cheap and the immediate beat shares the dwell felt-
// language vocab.

// ConsumedWarrantReason captures the immediate consume beat. NarrationText is
// the rendered felt line; ItemKind is carried for any future per-item
// rendering or audit, though the line is already composed at stamp time.
type ConsumedWarrantReason struct {
	ItemKind      ItemKind
	NarrationText string
}

func (ConsumedWarrantReason) isWarrantReason()           {}
func (ConsumedWarrantReason) Kind() WarrantKind          { return WarrantKindConsumed }
func (ConsumedWarrantReason) DedupDiscriminator() uint64 { return 0 }
