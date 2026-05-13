package sim

import "time"

// HuddleID identifies one co-located conversational pocket.
type HuddleID string

// Huddle is the set of actors who are conversationally co-present at one
// structure. A huddle is the persistent co-location pocket — independent
// of any single Scene (one huddle may be observed across many scenes over
// its lifetime; one scene may observe many huddles).
//
// Single-goroutine ownership of huddle state means at most one active
// huddle per structure by construction — the legacy parallel-huddle
// consolidator (ZBBS-WORK-232) has no analog here because the race that
// motivated it cannot occur.
//
// Membership is canonical on Members; Actor.CurrentHuddleID is a
// denormalized back-reference kept in sync atomically by JoinHuddle /
// LeaveHuddle / ConcludeHuddle. The invariant
// "Huddle.Members[a] iff Actor[a].CurrentHuddleID == h.ID" holds across
// every command-handler return.
type Huddle struct {
	ID          HuddleID
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
