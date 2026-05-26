package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_gather.go — the PC entry into the v2 environmental-harvest surface
// (ZBBS-WORK-328). Until this route, gather was reachable only by NPCs (the
// LLM tool path in engine/sim/handlers). This route lets a human player run
// the SAME action — harvest the gatherable source they're standing at — by
// resolving the PC from the authenticated session and delegating to sim.Gather.
// NPC and PC draw from the same source row's shared AvailableQuantity, so a
// well/bush is one stock both deplete and the regen tick refills.
//
// Mirrors the pc/pay posture (pc_pay.go): the handler owns request shape +
// numeric bounds; sim.Gather (on the world goroutine) owns all world-state
// validation (resolve the loitering source, gatherable check, depletion gate)
// and the mutation. The client gather BUTTON is a separate client task; this
// route is the server half.

// maxGatherBodyBytes caps the pc/gather request body — just an optional int.
const maxGatherBodyBytes = 4 << 10

// pcGatherRequest is the POST /api/village/pc/gather body. Like the other pc/*
// routes there is no actor field: the harvester is the caller's own PC,
// resolved from the authenticated session. qty is a pointer so an OMITTED qty
// (nil — including an empty body) means "gather 1" (the v1 default), while an
// explicit qty must be >= 1; explicit 0 is a 400 rather than coerced. There is
// no item field — the source the PC stands at determines what's gathered.
type pcGatherRequest struct {
	Qty *int `json:"qty,omitempty"`
}

// pcGatherResponse reports what was harvested. Qty is the amount actually
// gathered (a finite source may yield fewer than requested when it ran low);
// Item is the kind credited to the PC's inventory; Source is the source's
// display name.
type pcGatherResponse struct {
	Item   string `json:"item"`
	Qty    int    `json:"qty"`
	Source string `json:"source"`
}

// handlePCGather harvests the gatherable source the caller's PC is loitering
// at. requireAuth has populated the session AuthUser; the PC is resolved from
// that session inside the command (world goroutine, no TOCTOU). sim.Gather owns
// all world-state validation; the handler owns only request shape + qty bounds.
func (s *Server) handlePCGather(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxGatherBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req pcGatherRequest
	// An empty body is valid (gather 1) — only a present-but-malformed body
	// (or an unknown field) is a 400. io.EOF on the first decode means no body.
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	qty := 0 // omitted → 0; sim.Gather treats < 1 as the default 1.
	if req.Qty != nil {
		if *req.Qty < 1 {
			writeError(w, http.StatusBadRequest, "qty must be at least 1")
			return
		}
		if *req.Qty > sim.MaxGatherQty {
			writeError(w, http.StatusBadRequest, "qty exceeds the maximum")
			return
		}
		qty = *req.Qty
	}

	res, err := s.world.SendContext(r.Context(), gatherPCCommand(user.Username, qty))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// ErrNoGatherSource / ErrGatherableDepleted (and a misconfigured
		// gather_item) are state-validation failures: the request was
		// well-formed but the harvest can't proceed in the current world state.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out, ok := res.(sim.GatherResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected gather result")
		return
	}
	writeJSON(w, pcGatherResponse{
		Item:   string(out.Item),
		Qty:    out.Qty,
		Source: out.SourceName,
	})
}

// gatherPCCommand resolves username → PC actor (on the world goroutine) and
// delegates to sim.Gather. Same session→actor identity rule as the other pc/*
// commands; the clock is captured inside the Fn so the event timestamp reflects
// execution time. A deliberate PC action, so it stamps the input cursor +
// input-wakes an asleep PC (ZBBS-WORK-324) before harvesting.
func gatherPCCommand(username string, qty int) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			return sim.Gather(actorID, qty, now).Fn(world)
		},
	}
}
