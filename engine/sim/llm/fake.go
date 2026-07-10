package llm

import (
	"context"
	"fmt"
	"sync"
)

// FakeClient is a scripted LLM client for deterministic pipeline tests.
// Tests construct it with a sequence of scripted turns; each Complete
// call returns (and consumes) the next entry from the script.
//
// Concurrency: FakeClient is safe for concurrent Complete calls — the
// internal cursor is protected by a mutex. Tests that need to observe
// the requests their code made can read Requests() after the fact.
//
// Failure modes:
//
//   - Empty script + Complete call → returns ErrorMalformed (a test that
//     under-scripts is a test bug, not a tick failure to simulate; use
//     a scripted *Error turn to simulate a real LLM failure).
//   - Scripted entry has both Response AND Err set → Err wins (Response
//     is returned zero-valued).
//   - Context cancelled before Complete is called → returns Error{Class:
//     ErrorContextCancelled} and does NOT record the request, since the
//     work was never done.
type FakeClient struct {
	mu              sync.Mutex
	script          []ScriptedTurn
	cursor          int
	requests        []Request
	persistRequests []PersistRequest
	persistErr      error
	soulRequests    []SoulRequest
}

// ScriptedTurn is one entry in the FakeClient script. Exactly one of
// Response or Err should be set; if both are, Err wins.
type ScriptedTurn struct {
	Response Response
	Err      error
}

// NewFakeClient returns a FakeClient that will return the given turns in
// order. The script may be empty (tests that want to assert "Complete
// was never called" or that script lazily via Push).
func NewFakeClient(turns ...ScriptedTurn) *FakeClient {
	return &FakeClient{script: append([]ScriptedTurn(nil), turns...)}
}

// Complete returns the next scripted turn, advancing the cursor and
// recording a deep-copy of the Request for later inspection via
// Requests(). Safe for concurrent calls.
func (f *FakeClient) Complete(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, &Error{
			Class:   ErrorContextCancelled,
			Message: "ctx cancelled before fake Complete",
			Cause:   err,
		}
	}

	f.mu.Lock()
	f.requests = append(f.requests, cloneRequest(req))
	if f.cursor >= len(f.script) {
		called := f.cursor + 1
		f.mu.Unlock()
		return Response{}, &Error{
			Class:   ErrorMalformed,
			Message: fmt.Sprintf("FakeClient script exhausted at call %d", called),
		}
	}
	turn := f.script[f.cursor]
	f.cursor++
	f.mu.Unlock()

	if turn.Err != nil {
		return Response{}, turn.Err
	}
	return turn.Response, nil
}

// Push appends one more scripted turn. Useful for tests that script
// reactively (in response to observed behavior between Complete calls).
func (f *FakeClient) Push(turn ScriptedTurn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, turn)
}

// Requests returns a deep copy of every Request seen so far, in call
// order. Safe to call from any goroutine, and safe to mutate the returned
// slice (including nested Messages / Tools / ToolCalls / Arguments) — the
// FakeClient's recorded history will not be corrupted.
func (f *FakeClient) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.requests))
	for i, req := range f.requests {
		out[i] = cloneRequest(req)
	}
	return out
}

// CallCount returns the number of Complete calls that recorded a request
// (calls that errored on ctx-cancel before any work happened do NOT count).
func (f *FakeClient) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// SearchMemory implements MemorySearcher — the capability the recall tool needs
// (ZBBS-WORK-321), which cmd/engine's run() type-asserts on the LLM client at
// startup. The fake returns no hits (an empty result is not an error, per the
// interface), which is enough to satisfy the assertion so boot-wiring tests like
// TestRun_WiresOffWorldCascades reach world.Run instead of erroring out before
// it. Tests that exercise recall itself use a dedicated searcher mock, not this.
// Ctx-cancel handling mirrors Complete/PersistToolResults for consistency.
func (f *FakeClient) SearchMemory(ctx context.Context, namespace, query, slugPrefix string, limit int) ([]MemoryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, &Error{
			Class:   ErrorContextCancelled,
			Message: "ctx cancelled before fake SearchMemory",
			Cause:   err,
		}
	}
	return nil, nil
}

// SaveNote / ListNotes / DeleteNote implement MemoryWriter — the capability the
// memorize tool needs (LLM-356), which cmd/engine's run() type-asserts on the
// LLM client at startup alongside MemorySearcher. No-ops here (SaveNote/Delete
// succeed, ListNotes returns nothing), enough to satisfy the assertion so
// boot-wiring tests reach world.Run. Tests that exercise memorize itself use a
// dedicated writer mock, not this. Ctx-cancel handling mirrors SearchMemory.
func (f *FakeClient) SaveNote(ctx context.Context, namespace, slug, title, content, cognitiveType string) error {
	if err := ctx.Err(); err != nil {
		return &Error{Class: ErrorContextCancelled, Message: "ctx cancelled before fake SaveNote", Cause: err}
	}
	return nil
}

func (f *FakeClient) ListNotes(ctx context.Context, namespace, slugPrefix string) ([]NoteMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, &Error{Class: ErrorContextCancelled, Message: "ctx cancelled before fake ListNotes", Cause: err}
	}
	return nil, nil
}

func (f *FakeClient) DeleteNote(ctx context.Context, namespace, slug string) error {
	if err := ctx.Err(); err != nil {
		return &Error{Class: ErrorContextCancelled, Message: "ctx cancelled before fake DeleteNote", Cause: err}
	}
	return nil
}

// SynthesizeSoul implements the cascade's SoulSynthesizer capability
// (LLM-199) — the narrative soul sweep type-asserts the client to it at wiring
// time, so the boot-wiring / compose tests that pass a FakeClient need it
// satisfied (same role SearchMemory plays for recall). Records the request for
// inspection via SoulRequests() and returns a canned non-empty soul, enough for
// a sweep driven through this client to install something. Tests that exercise
// the sweep's branching use a dedicated SoulSynthesizer fake, not this.
// Ctx-cancel handling mirrors Complete.
func (f *FakeClient) SynthesizeSoul(ctx context.Context, req SoulRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", &Error{
			Class:   ErrorContextCancelled,
			Message: "ctx cancelled before fake SynthesizeSoul",
			Cause:   err,
		}
	}
	f.mu.Lock()
	f.soulRequests = append(f.soulRequests, req)
	f.mu.Unlock()
	return "a synthesized soul", nil
}

// SoulRequests returns a copy of every SynthesizeSoul call seen so far, in
// call order. Safe to call from any goroutine.
func (f *FakeClient) SoulRequests() []SoulRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SoulRequest(nil), f.soulRequests...)
}

// PersistToolResults implements ToolResultPersister. Records the request
// for inspection via PersistRequests(). Returns the configured persistErr
// when set (use SetPersistError to script a failure); otherwise nil.
//
// Ctx-cancel before the call is observed returns ErrorContextCancelled
// without recording — symmetric with Complete's posture so a test that
// asserts "no persist was made under cancel" reads cleanly.
func (f *FakeClient) PersistToolResults(ctx context.Context, req PersistRequest) error {
	if err := ctx.Err(); err != nil {
		return &Error{
			Class:   ErrorContextCancelled,
			Message: "ctx cancelled before fake PersistToolResults",
			Cause:   err,
		}
	}
	f.mu.Lock()
	f.persistRequests = append(f.persistRequests, clonePersistRequest(req))
	err := f.persistErr
	f.mu.Unlock()
	return err
}

// PersistRequests returns a deep copy of every PersistToolResults call
// seen so far, in call order. Safe to call from any goroutine, and safe
// to mutate the returned slice — the FakeClient's recorded history is
// not corrupted.
func (f *FakeClient) PersistRequests() []PersistRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]PersistRequest, len(f.persistRequests))
	for i, req := range f.persistRequests {
		out[i] = clonePersistRequest(req)
	}
	return out
}

// SetPersistError configures the error PersistToolResults returns on
// every call. Pass nil to clear. Useful for testing the harness's
// "persist failed, log and proceed" posture.
func (f *FakeClient) SetPersistError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.persistErr = err
}

// clonePersistRequest deep-copies the Results slice so a later caller
// mutation can't corrupt FakeClient's recorded history.
func clonePersistRequest(req PersistRequest) PersistRequest {
	out := req
	if req.Results != nil {
		out.Results = make([]ToolResult, len(req.Results))
		copy(out.Results, req.Results)
	}
	return out
}

// cloneRequest deep-copies the Request so a caller observing Requests()
// can't be confused by a later mutation to a slice the harness reused
// (the harness appends to req.Messages across iterations within a tick).
func cloneRequest(req Request) Request {
	out := req
	if req.Messages != nil {
		out.Messages = make([]Message, len(req.Messages))
		for i, m := range req.Messages {
			cm := m
			if m.ToolCalls != nil {
				cm.ToolCalls = make([]RawToolCall, len(m.ToolCalls))
				for j, tc := range m.ToolCalls {
					ctc := tc
					if tc.Arguments != nil {
						ctc.Arguments = append([]byte(nil), tc.Arguments...)
					}
					cm.ToolCalls[j] = ctc
				}
			}
			out.Messages[i] = cm
		}
	}
	if req.Tools != nil {
		out.Tools = make([]ToolSpec, len(req.Tools))
		for i, t := range req.Tools {
			ct := t
			if t.Schema != nil {
				ct.Schema = append([]byte(nil), t.Schema...)
			}
			out.Tools[i] = ct
		}
	}
	return out
}
