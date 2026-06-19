package sim

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// create_npc.go — NPC authoring (the v2 port of v1's POST /api/village/npcs,
// handleCreateNPC). Materializes a new stateful villager at a placement point.
// Mirrors v1's contract exactly:
//   - name defaults to "Villager" when blank;
//   - sprite_id is REQUIRED and must resolve in the catalog (ErrUnknownSprite);
//   - the NPC spawns at pos (world-pixel → tile), facing south, with the full
//     need-row set seeded and empty inventory/attributes;
//   - agent link, schedule, social, home/work anchors, and attributes all start
//     UNSET — assigning them is a separate admin action via the SetActor* edit
//     routes, exactly as v1 ("behavior and llm_memory_agent stay null at
//     creation").
//
// Emits NPCCreated carrying the resolved *Sprite so the httpapi hub can inline
// the render data into the npc_created frame (the client's add_npc_from_broadcast
// renders directly off the inlined sprite, with no follow-up fetch — the same
// shape the initial /api/village/agents load delivers). Lives in package sim
// because materialization touches the unexported world indexes (the actor map,
// outdoorActors), the same posture as CreatePC.

// CreateNPCResult reports a freshly created NPC's id.
type CreateNPCResult struct {
	ActorID ActorID
}

// CreateNPC materializes a new KindNPCStateful actor. pos is the placement point
// in world-pixel space (what the editor's click resolves to); it's converted to
// the actor's tile via WorldPos.Tile(). now is the creation clock.
func CreateNPC(name, spriteID string, pos WorldPos, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			name = strings.TrimSpace(name)
			if name == "" {
				name = "Villager"
			}
			// Validate the (post-default) name the same way SetActorDisplayName
			// does, so create can't introduce an overlong / control-char name
			// that the edit route would reject.
			if utf8.RuneCountInString(name) > MaxActorDisplayNameLen || containsControlChar(name) {
				return nil, ErrInvalidDisplayName
			}
			spriteID = strings.TrimSpace(spriteID)
			if spriteID == "" {
				return nil, ErrUnknownSprite
			}
			sprite := w.Sprites[SpriteID(spriteID)]
			if sprite == nil {
				return nil, ErrUnknownSprite
			}
			// mintPCActorID is a generic collision-checked v4-UUID minter despite
			// the name (it only checks w.Actors); reused here for NPC ids.
			id := mintPCActorID(w)
			if id == "" {
				return nil, fmt.Errorf("sim: CreateNPC: actor-ID minting exhausted retries")
			}
			tile := pos.Tile()
			actor := &Actor{
				ID:          id,
				DisplayName: name,
				Kind:        KindNPCStateful,
				SpriteID:    SpriteID(spriteID),
				Facing:      "south",
				Pos:         tile,
				Needs:       seedVisitorNeeds(),
				Inventory:   map[ItemKind]int{},
				Attributes:  map[string][]byte{},
				State:       StateIdle,
			}
			w.Actors[id] = actor
			w.outdoorActors[id] = struct{}{}
			w.emit(&NPCCreated{
				ActorID:     id,
				DisplayName: name,
				Kind:        KindNPCStateful,
				X:           tile.X,
				Y:           tile.Y,
				Facing:      "south",
				Sprite:      sprite,
				At:          now.UTC(),
			})
			return CreateNPCResult{ActorID: id}, nil
		},
	}
}
