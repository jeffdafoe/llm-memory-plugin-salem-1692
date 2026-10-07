package sim

import (
	"errors"
	"time"
)

// pc_outfit.go — the in-memory half of a player saving the character creator
// (LLM-691). The HTTP handler validates the layer list, upserts the PC's own
// npc_sprite row (PCOutfitSpriteID), then sends SetPCOutfit, which installs the
// sprite in the live catalog and dresses the PC. The row is written first
// because actor.sprite_id has a foreign key to npc_sprite: the checkpoint that
// persists the actor must never see a sprite id the table lacks.

// ErrPCNotFound is returned when the session's login has no PC actor.
var ErrPCNotFound = errors.New("pc not found")

// PCForLogin resolves a login to its PC's id and display name. Read-only;
// the handler needs both before it can write the outfit row.
func PCForLogin(loginUsername string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			id, ok := findPCByLoginUsername(w, loginUsername)
			if !ok {
				return nil, ErrPCNotFound
			}
			return PCIdentity{ID: id, DisplayName: w.Actors[id].DisplayName}, nil
		},
	}
}

// PCIdentity is PCForLogin's result.
type PCIdentity struct {
	ID          ActorID
	DisplayName string
}

// SetPCOutfit installs sprite in the catalog and points the PC at it. The PC
// is resolved again by login, so a PC removed since PCForLogin is
// ErrPCNotFound (the row already written is then unused, which is harmless).
//
// A PC with no sprite yet is drawn by no client — it was just created by the
// creator's pc/create — so its first outfit emits NPCCreated, which carries
// the position and inn room every client needs to draw it. After that, every
// save emits NPCSpriteChanged, even with the sprite id unchanged: a re-save
// keeps the id and changes the layers, and every client must redraw.
func SetPCOutfit(loginUsername string, sprite *Sprite) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			id, ok := findPCByLoginUsername(w, loginUsername)
			if !ok || sprite == nil || sprite.ID != PCOutfitSpriteID(id) {
				return nil, ErrPCNotFound
			}
			a := w.Actors[id]
			appears := a.SpriteID == ""
			w.InstallSprite(sprite)
			a.SpriteID = sprite.ID
			now := time.Now().UTC()
			if appears {
				w.emit(&NPCCreated{
					ActorID:           id,
					DisplayName:       a.DisplayName,
					Kind:              a.Kind,
					X:                 a.Pos.X,
					Y:                 a.Pos.Y,
					Facing:            "south",
					Sprite:            sprite,
					InsideStructureID: a.InsideStructureID,
					At:                now,
				})
			} else {
				w.emit(&NPCSpriteChanged{ActorID: id, Sprite: sprite, At: now})
			}
			return sprite.ID, nil
		},
	}
}

// InstallSprite adds or replaces one catalog sprite. The published snapshot
// aliases World.Sprites and HTTP goroutines read it without a lock, so the
// map is copied and swapped, never written in place. World goroutine only.
func (w *World) InstallSprite(sprite *Sprite) {
	next := make(map[SpriteID]*Sprite, len(w.Sprites)+1)
	for k, v := range w.Sprites {
		next[k] = v
	}
	next[sprite.ID] = sprite
	w.Sprites = next
}
