package httpapi

import (
	"net/http"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_quotes.go — ZBBS-HOME-426. GET /api/village/pc/quotes lists the live
// scene quotes the caller's PC is eligible to take RIGHT NOW, so the Pay
// modal can render them as take-able rows instead of making the player
// reconstruct terms from chat history. A take-row submits pc/pay with the
// quote_id and the terms copied verbatim from this response — verbatim copy
// satisfies the single-item fast path's exact-term predicates by construction
// (runPayWithItemFastPath, scene-quote-design § 8). A bundle row (LLM-101:
// >1 line) is taken WHOLE off the quote_id + amount; the representative Item/
// Qty the client echoes are ignored by the engine for a bundle, so any line
// serves and the take submission stays identical to the single-item case.
//
// Pure read over s.world.Published(), same posture as pc/me: Snapshot.Quotes
// is deep-cloned per publish exactly so client perception can read it
// lock-free (snapshot.go). No command-channel round trip; reconnect- and
// walk-in-safe because every fetch sees the current snapshot rather than
// riding a broadcast the PC may have missed.
//
// Eligibility mirrors the fast path's own predicates so the list only shows
// quotes a take would actually succeed on (modulo races, which the fast
// path's strict reject surfaces and the client re-fetches over):
//
//   - quote Active and not past ExpiresAt (clock = snap.PublishedAt; the
//     ±60s sweep lag means an aged-out quote can linger Active, so the
//     handler applies the expiry itself rather than trusting the sweep)
//   - the quote's scene observes the PC's huddle (predicate 3's scene gate)
//   - public, or targeted at this PC (predicate 2)
//   - seller co-huddled with the PC (predicate 3's huddle gate)
//   - no group-order quotes (non-empty ConsumerIDs): a verbatim take would
//     need the consumer ActorID set echoed through pc/pay's display-name
//     consumers field, and PC group orders aren't a real surface today.
//     Deliberate V1 scope, not an oversight.
//
// pcQuoteLineDTO is one item line of a quote (LLM-101): the wire item kind +
// catalog DisplayLabel (falls back to Item) + per-consumer qty. A single-item
// quote has one; a bundle has several.
type pcQuoteLineDTO struct {
	Item         string `json:"item"`
	DisplayLabel string `json:"display_label"`
	Qty          int    `json:"qty"`
}

type pcQuoteDTO struct {
	QuoteID uint64 `json:"quote_id"`
	// Seller is the seller's DisplayName — the exact string pc/pay's seller
	// field resolves (findHuddlePeerByDisplayName), so the client echoes it
	// back rather than translating ids.
	Seller string `json:"seller"`
	// Lines are the offer's item lines (LLM-101): single-element for an
	// ordinary quote, several for a bundle. The client renders these.
	Lines []pcQuoteLineDTO `json:"lines"`
	// Item / DisplayLabel / Qty echo the FIRST line — a representative the
	// client passes back to pc/pay verbatim. For a single-line quote that IS
	// the exact-match term; for a bundle the engine ignores them (a bundle
	// take is wholesale by quote_id + amount), so any line serves. Kept so the
	// client's take submission is identical for single-item and bundle.
	Item         string `json:"item"`
	DisplayLabel string `json:"display_label"`
	// Qty is units per consumer (of the representative line); Amount the
	// bundle-total floor (overpaying is tipping — fast-path predicate 5).
	Qty        int  `json:"qty"`
	Amount     int  `json:"amount"`
	ConsumeNow bool `json:"consume_now"`
	// Targeted is true when the quote addresses this PC specifically
	// (vs open to the whole huddle). Targeted rows sort first.
	Targeted bool `json:"targeted"`
	// ExpiresInSeconds is the remaining quote lifetime at snapshot time,
	// floored at 0. Display hint only — the take's authority is the fast
	// path's own expiry check.
	ExpiresInSeconds int `json:"expires_in_seconds"`

	// createdAt orders the wire list (newest first within a target class);
	// not serialized — the client doesn't need the raw timestamp.
	createdAt int64
}

type pcQuotesResponse struct {
	Quotes []pcQuoteDTO `json:"quotes"`
}

func (s *Server) handlePCQuotes(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		// requireAuth always populates this; guard rather than nil-deref.
		writeAuthError(w, "invalid")
		return
	}

	snap := s.world.Published()
	resp := pcQuotesResponse{Quotes: []pcQuoteDTO{}}

	// No PC / no huddle → stable empty shape, 200 (same posture as pc/me's
	// exists=false). An unhuddled PC genuinely has no takeable quotes —
	// predicate 3 requires the shared huddle — and the compose path plus the
	// pay-time bootstrap (ZBBS-HOME-427) still cover a walk-in offer.
	pcID, pc, ok := findPCSnapshotByLogin(snap, user.Username)
	if !ok || pc.CurrentHuddleID == "" {
		writeJSON(w, resp)
		return
	}

	for id, q := range snap.Quotes {
		if q == nil || q.State != sim.SceneQuoteStateActive {
			continue
		}
		if !q.ExpiresAt.IsZero() && !snap.PublishedAt.Before(q.ExpiresAt) {
			continue
		}
		scene := snap.Scenes[q.SceneID]
		if scene == nil {
			continue
		}
		if _, observes := scene.Huddles[pc.CurrentHuddleID]; !observes {
			continue
		}
		if q.TargetBuyer != "" && q.TargetBuyer != pcID {
			continue
		}
		if q.SellerID == pcID {
			continue
		}
		if len(q.ConsumerIDs) > 0 {
			continue
		}
		seller := snap.Actors[q.SellerID]
		if seller == nil || seller.CurrentHuddleID != pc.CurrentHuddleID {
			continue
		}
		// pc/pay resolves the seller by display name within the huddle and
		// REJECTS an ambiguous match (findHuddlePeerByDisplayName, EqualFold).
		// The take echoes seller.DisplayName back, so a quote whose seller
		// shares a display name with another co-huddled actor would render a
		// row that can only strict-reject — filter it here instead.
		if huddlePeerNameAmbiguous(snap, pc.CurrentHuddleID, pcID, q.SellerID, seller.DisplayName) {
			continue
		}

		lines := make([]pcQuoteLineDTO, 0, len(q.Lines))
		for _, ln := range q.Lines {
			label := string(ln.ItemKind)
			if def := snap.ItemKinds[ln.ItemKind]; def != nil && def.DisplayLabel != "" {
				label = def.DisplayLabel
			}
			lines = append(lines, pcQuoteLineDTO{
				Item:         string(ln.ItemKind),
				DisplayLabel: label,
				Qty:          ln.Qty,
			})
		}
		if len(lines) == 0 {
			// Defensive — a quote always carries >=1 line; skip a malformed one
			// rather than emit a row the client can't render or take.
			continue
		}
		remaining := 0
		if q.ExpiresAt.After(snap.PublishedAt) {
			remaining = int(q.ExpiresAt.Sub(snap.PublishedAt).Seconds())
		}
		resp.Quotes = append(resp.Quotes, pcQuoteDTO{
			QuoteID:          uint64(id),
			Seller:           seller.DisplayName,
			Lines:            lines,
			Item:             lines[0].Item,
			DisplayLabel:     lines[0].DisplayLabel,
			Qty:              lines[0].Qty,
			Amount:           q.Amount,
			ConsumeNow:       q.ConsumeNow,
			Targeted:         q.TargetBuyer == pcID,
			ExpiresInSeconds: remaining,
			createdAt:        q.CreatedAt.UnixNano(),
		})
	}

	// Targeted-at-me first, then newest, then id desc as the deterministic
	// tiebreak (map iteration order must not leak into the wire).
	sort.Slice(resp.Quotes, func(i, j int) bool {
		a, b := resp.Quotes[i], resp.Quotes[j]
		if a.Targeted != b.Targeted {
			return a.Targeted
		}
		if a.createdAt != b.createdAt {
			return a.createdAt > b.createdAt
		}
		return a.QuoteID > b.QuoteID
	})

	writeJSON(w, resp)
}

// huddlePeerNameAmbiguous reports whether any OTHER actor in the huddle
// (besides the buyer and the seller itself) carries the seller's display
// name, case-insensitively — the same EqualFold match and buyer exclusion
// findHuddlePeerByDisplayName applies when pc/pay resolves the seller.
func huddlePeerNameAmbiguous(snap *sim.Snapshot, huddleID sim.HuddleID, buyerID, sellerID sim.ActorID, sellerName string) bool {
	for id, a := range snap.Actors {
		if a == nil || id == buyerID || id == sellerID {
			continue
		}
		if a.CurrentHuddleID != huddleID {
			continue
		}
		if strings.EqualFold(a.DisplayName, sellerName) {
			return true
		}
	}
	return false
}
