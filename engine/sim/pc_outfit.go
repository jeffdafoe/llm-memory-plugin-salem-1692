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
// Emits NPCSpriteChanged even when the sprite id is unchanged — a re-save
// keeps the id and changes the layers, and every client must redraw.
func SetPCOutfit(loginUsername string, sprite *Sprite) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			id, ok := findPCByLoginUsername(w, loginUsername)
			if !ok || sprite == nil || sprite.ID != PCOutfitSpriteID(id) {
				return nil, ErrPCNotFound
			}
			w.InstallSprite(sprite)
			w.Actors[id].SpriteID = sprite.ID
			w.emit(&NPCSpriteChanged{ActorID: id, Sprite: sprite, At: time.Now().UTC()})
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
