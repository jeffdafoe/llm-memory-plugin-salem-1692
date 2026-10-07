package sim

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// pc_outfit.go — the in-memory half of a player saving the character creator
// (LLM-691). The HTTP handler validates the layer list, upserts the PC's own
// npc_sprite row (OutfitSpriteID), then sends SetPCOutfit, which installs the
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
			return OutfitTarget{ID: id, DisplayName: w.Actors[id].DisplayName}, nil
		},
	}
}

// OutfitTarget is PCForLogin's and DressableNPC's result: the actor an outfit
// is for, and the name its sprite row takes.
type OutfitTarget struct {
	ID          ActorID
	DisplayName string
}

// OutfitNotHeldError refuses an outfit naming wardrobe goods (pieces or dyes)
// the PC does not hold (LLM-710). Missing holds the goods' catalog labels.
type OutfitNotHeldError struct {
	Missing []string
}

func (e *OutfitNotHeldError) Error() string {
	return "you do not have: " + strings.Join(e.Missing, ", ")
}

// PCForOutfit is PCForLogin for a save: it also refuses, with
// *OutfitNotHeldError, an outfit that needs a good the PC does not hold. The
// handler checks this before it writes the outfit row.
func PCForOutfit(loginUsername string, layers json.RawMessage) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			id, ok := findPCByLoginUsername(w, loginUsername)
			if !ok {
				return nil, ErrPCNotFound
			}
			a := w.Actors[id]
			var missing []string
			for _, kind := range OutfitGoods(layers) {
				if a.Inventory[kind] > 0 {
					continue
				}
				label := string(kind)
				if def := w.ItemKinds[kind]; def != nil && def.DisplayLabel != "" {
					label = def.DisplayLabel
				}
				missing = append(missing, label)
			}
			if len(missing) > 0 {
				return nil, &OutfitNotHeldError{Missing: missing}
			}
			return OutfitTarget{ID: id, DisplayName: a.DisplayName}, nil
		},
	}
}

// SetPCOutfit installs sprite in the catalog and points the PC at it. sprite
// carries the chosen outfit; the catalog gets what the PC can show of it
// (pcShownSprite). The PC is resolved again by login, so a PC removed since
// PCForOutfit is ErrPCNotFound (the row already written is then unused, which
// is harmless). A good sold between the check and this install is not refused
// here: the next ReconcilePCOutfits leaves it off.
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
			if !ok || sprite == nil || sprite.ID != OutfitSpriteID(id) {
				return nil, ErrPCNotFound
			}
			a := w.Actors[id]
			appears := a.SpriteID == ""
			shown := pcShownSprite(a, sprite, sprite.Layers)
			w.InstallSprite(shown)
			a.SpriteID = shown.ID
			now := time.Now().UTC()
			if appears {
				w.emit(&NPCCreated{
					ActorID:           id,
					DisplayName:       a.DisplayName,
					Kind:              a.Kind,
					X:                 a.Pos.X,
					Y:                 a.Pos.Y,
					Facing:            "south",
					Sprite:            shown,
					InsideStructureID: a.InsideStructureID,
					At:                now,
				})
			} else {
				w.emit(&NPCSpriteChanged{ActorID: id, Sprite: shown, At: now})
			}
			return shown.ID, nil
		},
	}
}

// pcShownSprite is a copy of sprite showing what a holds of chosen
// (VisibleOutfit), with chosen kept on it.
func pcShownSprite(a *Actor, sprite *Sprite, chosen json.RawMessage) *Sprite {
	shown := *sprite
	shown.Layers = VisibleOutfit(chosen, func(kind ItemKind) bool { return a.Inventory[kind] > 0 })
	shown.Chosen = chosen
	return &shown
}

// chosenOutfit is the layer list a PC last saved: its outfit sprite's Chosen,
// else its Layers (a row as loaded at boot). Nil for a PC not wearing its own
// outfit.
func (w *World) chosenOutfit(id ActorID) json.RawMessage {
	a := w.Actors[id]
	if a == nil || a.SpriteID != OutfitSpriteID(id) {
		return nil
	}
	sp := w.Sprites[a.SpriteID]
	if sp == nil {
		return nil
	}
	if sp.Chosen != nil {
		return sp.Chosen
	}
	return sp.Layers
}

// ReconcilePCOutfits re-dresses every PC wearing its own outfit in what it
// still holds (LLM-710): a piece sold, given away or worn out comes off its
// sprite, a dye's colours fall back to undyed, and both come back if the PC
// holds them again. Emits NPCSpriteChanged only for a PC whose shown layers
// changed. Run by the garment-wear ticker at start and every minute; the
// outfit row is never rewritten, so it keeps the chosen outfit.
func ReconcilePCOutfits() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			changed := 0
			now := time.Now().UTC()
			for id, a := range w.Actors {
				if a.Kind != KindPC {
					continue
				}
				chosen := w.chosenOutfit(id)
				if chosen == nil {
					continue
				}
				current := w.Sprites[a.SpriteID]
				shown := pcShownSprite(a, current, chosen)
				if bytes.Equal(shown.Layers, current.Layers) {
					continue
				}
				w.InstallSprite(shown)
				w.emit(&NPCSpriteChanged{ActorID: id, Sprite: shown, At: now})
				changed++
			}
			return changed, nil
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

// ErrNotDressable is returned when an outfit is asked for an actor whose
// sprite carries engine behaviors (the animals: grazer, waterfowl, ambient).
// Behaviors ride the sprite, so a farmer-base outfit would strip them.
var ErrNotDressable = errors.New("this actor cannot be dressed")

// DressableNPC resolves an editable, dressable NPC to its id and display
// name — the admin editor's half of PCForLogin. Read-only.
func DressableNPC(id ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, err := dressableNPC(w, id)
			if err != nil {
				return nil, err
			}
			return OutfitTarget{ID: id, DisplayName: a.DisplayName}, nil
		},
	}
}

func dressableNPC(w *World, id ActorID) (*Actor, error) {
	a, err := editableNPC(w, id)
	if err != nil {
		return nil, err
	}
	if sp := w.Sprites[a.SpriteID]; sp != nil && len(sp.Behaviors) > 0 {
		return nil, ErrNotDressable
	}
	return a, nil
}

// SetNPCOutfit dresses a villager in sprite from the admin editor (LLM-691):
// install it in the catalog, point the actor at it, emit NPCSpriteChanged.
// The same per-actor outfit row as a player's; re-checked on the world
// goroutine, so an actor removed or re-sprited as an animal since
// DressableNPC is refused (the row already written is then unused).
func SetNPCOutfit(id ActorID, sprite *Sprite) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, err := dressableNPC(w, id)
			if err != nil {
				return nil, err
			}
			if sprite == nil || sprite.ID != OutfitSpriteID(id) {
				return nil, ErrNotDressable
			}
			w.InstallSprite(sprite)
			a.SpriteID = sprite.ID
			w.emit(&NPCSpriteChanged{ActorID: id, Sprite: sprite, At: time.Now().UTC()})
			return sprite.ID, nil
		},
	}
}
