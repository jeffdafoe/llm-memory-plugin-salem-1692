package sim

// events_pc_needs.go — LLM-56. PCNeedsChanged fires when a PLAYER character's
// needs change via an eat/drink in place (applyObjectRefreshEffect), carrying
// the PC's full post-bite need snapshot. The hub (httpapi) translates it into a
// `pc_needs_changed` WebSocket frame so the player's top-bar HUD updates the
// instant a bite lands, rather than on the next ~10s /pc/me poll — smooth,
// per-bite ticking during the LLM-55 auto-repeat eating loop.
//
// PC-only by construction: NPC needs are not client-rendered, so the emit is
// gated on KindPC and never fires for the village population.
type PCNeedsChanged struct {
	EventBase
	ActorID ActorID
	Needs   map[NeedKey]int
}

func (PCNeedsChanged) isSimEvent() {}
