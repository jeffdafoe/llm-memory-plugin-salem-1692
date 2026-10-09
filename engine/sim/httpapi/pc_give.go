package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_give.go — POST /api/village/pc/give (LLM-725): the player hands coins to
// someone in their conversation with nothing bought — a gift, a tip, a debt
// paid back. It is the one caller of sim.Pay, the bare coin transfer.
//
// PC-only by construction: like every pc/* route there is no payer field — the
// payer is the authenticated session's own PC. NPCs have no session and no tool
// that reaches sim.Pay (LLM-726 removed theirs: coin moved for goods that never
// moved and for debts paid again and again). The recipient may be a PC or an
// NPC; sim.Pay resolves the name among the payer's huddle peers.
//
// The handler owns request shape, bounds and text; sim.Pay owns every world
// rule (same conversation, spendable coins, no self-pay, and the guards that
// refuse a gift that is really a purchase, a room, a wage or a false refund).

// maxGiveBodyBytes caps the pc/give request body: a name, an int and a short
// line, so 8 KiB is ample headroom while refusing a flood before decode.
const maxGiveBodyBytes = 8 << 10

// pcGiveRequest is the POST /api/village/pc/give body. For is the optional
// "what for" line; it reaches the recipient's perception and the room's talk
// log ("X pays Y 5 coins for <for>").
type pcGiveRequest struct {
	Recipient string `json:"recipient"`
	Amount    int    `json:"amount"`
	For       string `json:"for,omitempty"`
}

// pcGiveResponse confirms the gift and carries the giver's purse after it, so
// the client can show the new count before its next pc/me poll.
type pcGiveResponse struct {
	Recipient string `json:"recipient"`
	Amount    int    `json:"amount"`
	Coins     int    `json:"coins"`
}

func (s *Server) handlePCGive(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxGiveBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req pcGiveRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	recipient, forText, msg := validateGiveFields(req)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	res, err := s.world.SendContext(r.Context(), givePCCommand(user.Username, recipient, req.Amount, forText))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, errPCNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// Every sim.Pay refusal is a well-formed request the world state
		// rejects; its text is written for the player and shown as-is.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	coins, _ := res.(int)
	writeJSON(w, pcGiveResponse{Recipient: recipient, Amount: req.Amount, Coins: coins})
}

// validateGiveFields returns the trimmed recipient and for-text, or a non-empty
// msg (→ 400). The caps mirror pc/pay's (maxPayNameChars / maxPayForChars).
func validateGiveFields(req pcGiveRequest) (recipient, forText, msg string) {
	recipient = strings.TrimSpace(req.Recipient)
	if recipient == "" {
		return "", "", "recipient is required"
	}
	if utf8.RuneCountInString(recipient) > maxPayNameChars {
		return "", "", "recipient exceeds the length limit"
	}
	if hasInvalidControlChar(recipient) {
		return "", "", "recipient contains a disallowed control character"
	}
	if req.Amount < 1 {
		return "", "", "amount must be at least 1"
	}
	if req.Amount > sim.MaxPayAmount {
		return "", "", "amount exceeds the maximum"
	}
	forText = strings.Join(strings.Fields(req.For), " ")
	if utf8.RuneCountInString(forText) > maxPayForChars {
		return "", "", "for exceeds the length limit"
	}
	if forText != "" && hasInvalidControlChar(forText) {
		return "", "", "for contains a disallowed control character"
	}
	return recipient, forText, ""
}

// givePCCommand resolves username → PC on the world goroutine and runs sim.Pay
// with the PC as payer. It returns the giver's coins after the gift. Like
// pc/pay it stamps the input cursor and forms the co-located huddle first, so
// a PC who walked into a shop without speaking can still give to the keeper.
func givePCCommand(username, recipient string, amount int, forText string) sim.Command {
	return sim.Command{
		Fn: func(world *sim.World) (any, error) {
			actorID, ok := findPCByLogin(world, username)
			if !ok {
				return nil, errPCNotFound
			}
			now := time.Now().UTC()
			sim.TouchPCInput(world, actorID, now)
			if _, err := sim.EnsureColocatedHuddle(actorID, now).Fn(world); err != nil {
				return nil, err
			}
			if _, err := sim.Pay(actorID, recipient, amount, forText, now).Fn(world); err != nil {
				return nil, err
			}
			return world.Actors[actorID].Coins, nil
		},
	}
}
