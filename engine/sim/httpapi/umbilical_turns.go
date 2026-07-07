package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

// umbilical_turns.go — the /api/village/umbilical/turns read route (ZBBS-HOME-396).
//
// Unlike every other umbilical read (which serves an in-memory ring or the
// published snapshot), the full raw LLM turn — the composed system_prompt, the
// provider's token counts, cost, and HTTP status/error — exists ONLY in
// llm-memory-api's virtual_agent_calls table. The engine's LLM client sends
// perception as the user message and gets back {reply, tool_calls}; it never
// sees the system prompt the API composes per agent. So this route can't read a
// local ring like /chat or /agent/prompts — it's a thin authenticated PROXY.
//
// It forwards the OPERATOR'S OWN bearer token (already validated by
// requireOperator: salem realm + plugins/administer) to memory-api's
// operator-gated POST /v1/sim/raw-turns and relays the response verbatim.
// Forwarding the operator's token rather than the engine's service key keeps
// least privilege: the downstream call is authorized as the actual human
// operator who holds plugins/administer, so the salem-engine service account
// needs no standing capability over every agent's raw turns. memory-api
// re-checks plugins/administer on its side, so authorization is enforced
// end-to-end.

const (
	// turnsUpstreamTimeout bounds the proxied raw-turns fetch. It's a small,
	// indexed DB read on memory-api; 20s is generous headroom for a cold
	// connection without hanging the operator's request indefinitely.
	turnsUpstreamTimeout = 20 * time.Second
	// turnsUpstreamPath is memory-api's operator-gated raw-turns endpoint, hung
	// off the configured base URL.
	turnsUpstreamPath = "/v1/sim/raw-turns"
)

// rawTurnsUpstreamRequest is the JSON body POSTed to memory-api. Every field is
// optional; omitempty lets memory-api apply its own defaults (e.g. limit) and
// skip absent filters. Mirrors the query params the operator passes to /turns.
type rawTurnsUpstreamRequest struct {
	SceneID string `json:"scene_id,omitempty"`
	Agent   string `json:"agent,omitempty"`
	// Conversation filters to one huddle's whole conversation: the engine threads
	// the huddle id (hud-<hex>) onto virtual_agent_calls.conversation_id via
	// /v1/chat/send (ZBBS-HOME-397), stable across every tick and participant of
	// the conversation. Indexed upstream (idx_va_calls_conversation). This is the
	// single-call "what happened in this huddle" lookup (ZBBS-WORK-431) that
	// otherwise needed a cross-DB dig; durable, so it answers for a PAST huddle.
	Conversation string `json:"conversation,omitempty"`
	// SimActor filters to one in-world actor: the salem engine actor id the turn
	// was made ON BEHALF OF, stamped on virtual_agent_calls.sim_actor_id via
	// /v1/chat/send (LLM-236). This is the attribution the `agent` filter can't
	// give for a SHARED VA — agent=salem-vendor returns every character that VA
	// backs; sim_actor=<id> returns just this one. Indexed upstream
	// (idx_va_calls_sim_actor).
	SimActor string `json:"sim_actor,omitempty"`
	Since    string `json:"since,omitempty"`
	// Until is the EXCLUSIVE created_at upper bound (ZBBS-WORK-391) — the
	// walk-back cursor for episodes buried behind newer turns, since
	// memory-api returns newest-first with no offset pagination. Exclusive so
	// the oldest row's created_at can be passed verbatim without repeating
	// the boundary row.
	Until  string `json:"until,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// handleUmbilicalTurns proxies a raw-LLM-turn query to memory-api, forwarding the
// operator's bearer token. Query params (all optional): scene, agent, conversation
// (a hud-<hex> huddle id — every turn in that conversation), sim_actor (one
// in-world actor id — the shared-VA attribution `agent` can't give), since,
// until, status, limit. memory-api owns the response contract (it returns the
// virtual_agent_calls rows), so the engine relays its status + body verbatim
// rather than re-modeling the row schema — deliberately the one umbilical read
// that doesn't wrap its payload in a contract_version DTO.
func (s *Server) handleUmbilicalTurns(w http.ResponseWriter, r *http.Request) {
	if s.memoryAPIBaseURL == "" {
		// The route registers with the rest of the read surface (it's in the
		// umbilical table), but it can't serve without an upstream. cmd/engine
		// wires the base URL whenever the umbilical is on, so this is only hit in
		// a misconfigured/headless wiring.
		writeError(w, http.StatusServiceUnavailable, "raw-turns upstream not configured")
		return
	}

	q := r.URL.Query()
	reqBody := rawTurnsUpstreamRequest{
		SceneID:      q.Get("scene"),
		Agent:        q.Get("agent"),
		Conversation: q.Get("conversation"),
		SimActor:     q.Get("sim_actor"),
		Since:        q.Get("since"),
		Until:        q.Get("until"),
		Status:       q.Get("status"),
	}
	// Parse limit leniently — a valid positive int is forwarded; anything else
	// is omitted so memory-api applies its own default/cap.
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		reqBody.Limit = n
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode upstream request")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), turnsUpstreamTimeout)
	defer cancel()

	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.memoryAPIBaseURL+turnsUpstreamPath, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build upstream request")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	// Forward the operator's own bearer token. requireOperator already validated
	// it (salem realm + plugins/administer), so it's present and well-formed here;
	// bearerToken returns the RAW token (scheme stripped + trimmed — see auth.go),
	// so we reconstruct a single canonical "Bearer <token>" header rather than
	// relaying the inbound Authorization header verbatim. The empty check is pure
	// defense-in-depth: we never forward an empty credential downstream even if a
	// future auth path were to reach this handler without one.
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	upstream.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.turnsClient.Do(upstream)
	if err != nil {
		// memory-api unreachable / timed out — an honest gateway failure.
		writeError(w, http.StatusBadGateway, "raw-turns upstream unreachable")
		return
	}
	defer resp.Body.Close()

	// Relay the upstream response verbatim: its status, its JSON body. memory-api
	// owns the raw-turn schema; re-modeling it here would only be drift.
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// Header + status already sent; the operator re-runs on a truncated body.
		log.Printf("httpapi: relay raw-turns response: %v", err)
	}
}
