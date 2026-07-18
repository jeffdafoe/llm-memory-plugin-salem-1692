package sim

// CoinSupply is the on-map money-supply gauge: total coin held across every
// non-decorative actor, split by whether the holder is a transient visitor or
// a resident. It exists so an operator can watch external trade change the
// money supply — a wholesale factor (LLM-410) arrives carrying coin (Visitor
// rises), spends it into the village (Resident rises as Visitor falls), and
// departs with his unspent remainder (Visitor falls, that coin off-map). Before
// this we had no view of the money supply at all, and the import/export tier is
// about to start changing it.
//
// Decorative sprite-only actors are excluded: they are scenery, never economic
// participants — the same exclusion the cold sweep makes (coldEligible). Kind is
// derived at load from the login/agent columns (ClassifyActorKind) and copied
// onto the snapshot, so `Kind == KindDecorative` is exactly coldEligible's
// "no agent and no login" test without duplicating the classification. The count
// is over the published snapshot, so the /state read stays lock-free.
type CoinSupply struct {
	// Total is coin held across every non-decorative actor (Resident + Visitor).
	Total int `json:"total"`
	// Resident is coin held by non-visitor actors — the village's own NPCs and
	// any PC. This is the coin that stays put when travellers leave.
	Resident int `json:"resident"`
	// Visitor is coin carried by transient visitors (VisitorState != nil). It
	// entered with them and leaves the map when they depart, so a spike here is
	// external coin passing through, not durable village money.
	Visitor int `json:"visitor"`
	// Holders is the number of non-decorative actors counted — the economic-
	// participant headcount, distinct from counts.actors (which includes
	// decoratives).
	Holders int `json:"holders"`
}

// ComputeCoinSupply sums the on-map coin supply off a published snapshot,
// excluding decorative actors. Nil-safe (nil snapshot → zero value), following
// the ConfigWarnings pattern so the /state read stays a lock-free snapshot map.
func ComputeCoinSupply(s *Snapshot) CoinSupply {
	var cs CoinSupply
	if s == nil {
		return cs
	}
	for _, a := range s.Actors {
		if a == nil || a.Kind == KindDecorative {
			continue
		}
		cs.Total += a.Coins
		cs.Holders++
		if a.VisitorState != nil {
			cs.Visitor += a.Coins
		} else {
			cs.Resident += a.Coins
		}
	}
	return cs
}

// residentCoinOnMap sums the coin held by resident actors (non-visitor,
// non-decorative) straight off the live World (LLM-455) — the same Resident total
// ComputeCoinSupply derives from a Snapshot, but read on the world goroutine where
// the visitor spawn valve runs, so it needs no published snapshot. The coin-valve
// (chooseVisitorTradeDirection) reads it to bias a merchant visitor buy vs sell
// against the operator band. Called only from inside a Command.Fn.
func residentCoinOnMap(w *World) int {
	total := 0
	for _, a := range w.Actors {
		if a == nil || a.Kind == KindDecorative || a.VisitorState != nil {
			continue
		}
		total += a.Coins
	}
	return total
}
