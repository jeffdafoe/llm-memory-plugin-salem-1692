package sim

import "time"

// ItemGathered fires when an actor commits a `gather` against a world that
// has resolved the gatherable source they're loitering at and credited the
// harvested item to their inventory. ZBBS-WORK-328 — the v2 revival of v1's
// gather verb as a general environmental-harvest substrate.
//
// Both actor kinds emit this: an NPC via the `gather` tool, a PC via
// POST /api/village/pc/gather. They draw from the same source row's shared
// AvailableQuantity (the regen tick refills it), so a single event shape
// covers both.
//
// Qty is whole units actually gathered, post-validation and post-supply-cap:
// >= 1, and clamped down to the source's remaining AvailableQuantity when the
// source is finite (so a request for 5 from a bush with 3 left emits Qty=3).
// ObjectID is the source the item came from; Item is the canonical kind
// credited to the actor's inventory.
//
// At is wall-clock at commit time.
//
// No client WS frame is mapped for this event yet (ZBBS-WORK-328 ships
// NPC-active gather + a PC route; NPC inventory isn't client-rendered and the
// PC reads its own inventory via the pc/me poll). TranslateEvent drops it
// (ok=false) until an inventory-changed frame is wanted. Emitted now so the
// action-log / audit path can pick it up when wired.
type ItemGathered struct {
	EventBase
	ActorID  ActorID
	ObjectID VillageObjectID
	Item     ItemKind
	Qty      int
	At       time.Time
}

func (ItemGathered) isSimEvent() {}
