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
