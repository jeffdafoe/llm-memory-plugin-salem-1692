package sim

import "time"

// HuddleID identifies one co-located conversational pocket.
type HuddleID string

// Huddle is the set of actors who are conversationally co-present at one
// structure within one scene. A scene may contain multiple huddles if the
// physical layout fragments conversation (e.g. front room + back room).
type Huddle struct {
	ID          HuddleID
	SceneID     SceneID
	Members     map[ActorID]struct{}
	StructureID StructureID
	StartedAt   time.Time
	ConcludedAt *time.Time
}

// CloneHuddle returns a deep copy suitable for publication via Snapshot or
// for serialization-equivalent boundaries (mem repo Seed / LoadAll /
// SaveSnapshot). World goroutine mutations to the live Huddle (Members
// add/remove, ConcludedAt rebind) won't leak into the returned copy.
func CloneHuddle(h *Huddle) *Huddle {
	if h == nil {
		return nil
	}
	cp := *h
	if h.Members != nil {
		cp.Members = make(map[ActorID]struct{}, len(h.Members))
		for k := range h.Members {
			cp.Members[k] = struct{}{}
		}
	}
	if h.ConcludedAt != nil {
		t := *h.ConcludedAt
		cp.ConcludedAt = &t
	}
	return &cp
}
