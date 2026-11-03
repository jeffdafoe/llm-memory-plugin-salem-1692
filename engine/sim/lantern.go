package sim

import "time"

// lantern.go — LLM-846. A horn lantern a player carries: Light takes a tallow
// candle from the pack (or the rest of one kept by Put out) and the lantern
// burns until LanternLitUntil, the same no-sweep shape as the hearth
// (HearthLitUntil): lit means the time is in the future, and the clock puts it
// out. Put out keeps what is left of the candle in LanternCandleLeft for the
// next Light. The client draws a light on the actor while it burns
// (LanternChanged); nothing in the engine reads the light.

// CapabilityLight marks a good that is lit from the pack — the horn lantern
// (migration LLM-846).
const CapabilityLight = "light"

// HornLanternKind is the lantern, and TallowCandleKind the candle it burns.
// Josiah sells both; the factor brings more each visit.
const (
	HornLanternKind  = ItemKind("horn_lantern")
	TallowCandleKind = ItemKind("tallow_candle")
)

// The lantern's item actions.
const (
	ItemActionLight  = "light"
	ItemActionPutOut = "put_out"
)

// DefaultLanternCandleMinutes is how long one candle burns (live setting
// lantern_candle_minutes). A setting of 0 or less, or above
// MaxLanternCandleMinutes, burns the default: a huge value would overflow the
// duration and light the lantern into the past.
const (
	DefaultLanternCandleMinutes = 180
	MaxLanternCandleMinutes     = 24 * 60
)

// LanternChanged is emitted when an actor's lantern is lit or put out. LitUntil
// is when it burns out; zero means it is out. Burning out on the clock emits
// nothing: every reader holds LitUntil and puts the light out itself.
type LanternChanged struct {
	EventBase
	ActorID  ActorID
	LitUntil time.Time
	At       time.Time
}

func (LanternChanged) isSimEvent() {}

// LanternLit reports whether a's lantern is burning as of now.
func LanternLit(a *Actor, now time.Time) bool {
	return a.LanternLitUntil.After(now)
}

// isLantern reports whether def is the lantern. Only the canonical kind lights,
// as with the tent: the lit state on the actor is the horn lantern's, so
// another light-capable good shows no button it cannot use.
func isLantern(def *ItemKindDef) bool {
	return def.Name == HornLanternKind && def.HasCapability(CapabilityLight)
}

// lanternCandleDuration is the burn time of one fresh candle.
func lanternCandleDuration(s WorldSettings) time.Duration {
	minutes := s.LanternCandleMinutes
	if minutes <= 0 || minutes > MaxLanternCandleMinutes {
		minutes = DefaultLanternCandleMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// lightLantern lights a's lantern: the kept stub first, else one candle from
// the pack.
func lightLantern(w *World, a *Actor, now time.Time) error {
	if LanternLit(a, now) {
		return refuseItemAction("Your lantern is already lit.")
	}
	burn := a.LanternCandleLeft
	if burn > 0 {
		a.LanternCandleLeft = 0
	} else {
		if a.Inventory[TallowCandleKind] <= 0 {
			return refuseItemAction("You have no candle for it.")
		}
		burn = lanternCandleDuration(w.Settings)
		a.Inventory[TallowCandleKind]--
		if a.Inventory[TallowCandleKind] <= 0 {
			delete(a.Inventory, TallowCandleKind)
		}
	}
	// LLM-845: the lantern omen. The candle is spent all the same, but it burns
	// out in omenLanternGutter.
	omen := takeArmedOmen(w, OmenLanternGutters, a.ID, now)
	if omen && burn > omenLanternGutter {
		burn = omenLanternGutter
	}
	a.LanternLitUntil = now.Add(burn)
	w.emit(&LanternChanged{ActorID: a.ID, LitUntil: a.LanternLitUntil, At: now})
	if omen {
		noticeOmen(w, OmenLanternGutters, a, a.ID, now)
	}
	return nil
}

// putOutLantern puts a's lantern out and keeps the rest of the candle. A
// lantern already out is a no-op.
func putOutLantern(w *World, a *Actor, now time.Time) {
	if !LanternLit(a, now) {
		return
	}
	a.LanternCandleLeft = a.LanternLitUntil.Sub(now)
	a.LanternLitUntil = time.Time{}
	w.emit(&LanternChanged{ActorID: a.ID, At: now})
}

// putOutUnheldLantern puts out a lit lantern the actor no longer holds — sold,
// given or paid away. Run by the familiar ticker, so the pay paths need no hook.
func putOutUnheldLantern(w *World, a *Actor, now time.Time) {
	if a.Inventory[HornLanternKind] > 0 {
		return
	}
	putOutLantern(w, a, now)
}
