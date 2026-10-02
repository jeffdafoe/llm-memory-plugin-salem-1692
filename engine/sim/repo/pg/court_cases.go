package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// CourtCasesRepo reads and writes court_case (LLM-695): the magistrates' docket
// and their record of rulings. Upsert-only inside the checkpoint Tx, like
// RecurringVisitorsRepo — a ruled case is never removed, so there is nothing for
// a sweep to reclaim, and the in-memory set only grows.
type CourtCasesRepo struct {
	pool Pool
}

// NewCourtCasesRepo constructs a CourtCasesRepo. Normal wiring is pg.NewRepository.
func NewCourtCasesRepo(pool Pool) *CourtCasesRepo {
	return &CourtCasesRepo{pool: pool}
}

const loadCourtCasesSQL = `
SELECT id, filed_at, filed_by_actor_id, filed_by_name, parties, complaint, status,
       ruled_at, result, found_for_actor_id, payer_actor_id, payee_actor_id,
       amount_ordered, amount_paid, words
  FROM court_case`

const upsertCourtCaseSQL = `
INSERT INTO court_case (
    id, filed_at, filed_by_actor_id, filed_by_name, parties, complaint, status,
    ruled_at, result, found_for_actor_id, payer_actor_id, payee_actor_id,
    amount_ordered, amount_paid, words
) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (id) DO UPDATE SET
    filed_by_name      = EXCLUDED.filed_by_name,
    parties            = EXCLUDED.parties,
    complaint          = EXCLUDED.complaint,
    status             = EXCLUDED.status,
    ruled_at           = EXCLUDED.ruled_at,
    result             = EXCLUDED.result,
    found_for_actor_id = EXCLUDED.found_for_actor_id,
    payer_actor_id     = EXCLUDED.payer_actor_id,
    payee_actor_id     = EXCLUDED.payee_actor_id,
    amount_ordered     = EXCLUDED.amount_ordered,
    amount_paid        = EXCLUDED.amount_paid,
    words              = EXCLUDED.words`

const advisoryLockCourtCasesSQL = `SELECT pg_advisory_xact_lock(hashtext('court_case_snapshot'), 0)`

// courtPartyWire is one element of court_case.parties.
type courtPartyWire struct {
	ActorID string `json:"actor_id"`
	Name    string `json:"name"`
}

// LoadAll loads every case. A malformed parties array fails the boot rather than
// dropping a case: a lost pending case is a matter never heard.
func (r *CourtCasesRepo) LoadAll(ctx context.Context) (map[sim.CourtCaseID]*sim.CourtCase, error) {
	out := make(map[sim.CourtCaseID]*sim.CourtCase)
	rows, err := r.pool.Query(ctx, loadCourtCasesSQL)
	if err != nil {
		return nil, fmt.Errorf("pg court_cases LoadAll query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, filedByName, complaint, status string
			filedAt                            time.Time
			filedBy, result, foundFor          *string
			payer, payee, words                *string
			partiesJSON                        []byte
			ruledAt                            *time.Time
			amountOrdered, amountPaid          *int
		)
		if err := rows.Scan(&id, &filedAt, &filedBy, &filedByName, &partiesJSON, &complaint, &status,
			&ruledAt, &result, &foundFor, &payer, &payee, &amountOrdered, &amountPaid, &words); err != nil {
			return nil, fmt.Errorf("pg court_cases LoadAll scan: %w", err)
		}
		var wire []courtPartyWire
		if err := json.Unmarshal(partiesJSON, &wire); err != nil {
			return nil, fmt.Errorf("pg court_cases LoadAll id=%s parties: %w", id, err)
		}
		c := &sim.CourtCase{
			ID:          sim.CourtCaseID(id),
			FiledAt:     filedAt,
			FiledByID:   sim.ActorID(deref(filedBy)),
			FiledByName: filedByName,
			Complaint:   complaint,
			Status:      status,
			Result:      sim.CourtResult(deref(result)),
			FoundForID:  sim.ActorID(deref(foundFor)),
			PayerID:     sim.ActorID(deref(payer)),
			PayeeID:     sim.ActorID(deref(payee)),
			Words:       deref(words),
		}
		for _, p := range wire {
			c.Parties = append(c.Parties, sim.CourtParty{ActorID: sim.ActorID(p.ActorID), Name: p.Name})
		}
		if ruledAt != nil {
			c.RuledAt = *ruledAt
		}
		if amountOrdered != nil {
			c.AmountOrdered = *amountOrdered
		}
		if amountPaid != nil {
			c.AmountPaid = *amountPaid
		}
		out[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg court_cases LoadAll iter: %w", err)
	}
	return out, nil
}

// nullable binds "" as SQL NULL.
func nullable(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// SaveSnapshot upserts every case inside the checkpoint Tx. No sweep.
func (r *CourtCasesRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, cases map[sim.CourtCaseID]*sim.CourtCase) error {
	if tx == nil {
		return fmt.Errorf("pg court_cases SaveSnapshot: nil tx")
	}
	if len(cases) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, advisoryLockCourtCasesSQL); err != nil {
		return fmt.Errorf("pg court_cases SaveSnapshot: advisory lock: %w", err)
	}
	for id, c := range cases {
		if c == nil {
			continue
		}
		if c.ID != id || strings.TrimSpace(string(c.ID)) == "" {
			return fmt.Errorf("pg court_cases SaveSnapshot: bad case id %q (key %q)", c.ID, id)
		}
		wire := make([]courtPartyWire, 0, len(c.Parties))
		for _, p := range c.Parties {
			wire = append(wire, courtPartyWire{ActorID: string(p.ActorID), Name: p.Name})
		}
		partiesJSON, err := json.Marshal(wire)
		if err != nil {
			return fmt.Errorf("pg court_cases SaveSnapshot: marshal parties id=%s: %w", c.ID, err)
		}
		var ruledAt, amountOrdered, amountPaid any
		if c.Status == sim.CourtCaseStatusRuled {
			ruledAt = c.RuledAt
			if c.Result == sim.CourtResultPay {
				amountOrdered = c.AmountOrdered
				amountPaid = c.AmountPaid
			}
		}
		if _, err := tx.Exec(ctx, upsertCourtCaseSQL,
			string(c.ID),                   // $1 id
			c.FiledAt,                      // $2 filed_at
			nullable(string(c.FiledByID)),  // $3 filed_by_actor_id
			c.FiledByName,                  // $4 filed_by_name
			string(partiesJSON),            // $5 parties (::jsonb)
			c.Complaint,                    // $6 complaint
			c.Status,                       // $7 status
			ruledAt,                        // $8 ruled_at
			nullable(string(c.Result)),     // $9 result
			nullable(string(c.FoundForID)), // $10 found_for_actor_id
			nullable(string(c.PayerID)),    // $11 payer_actor_id
			nullable(string(c.PayeeID)),    // $12 payee_actor_id
			amountOrdered,                  // $13 amount_ordered
			amountPaid,                     // $14 amount_paid
			nullable(c.Words),              // $15 words
		); err != nil {
			return fmt.Errorf("pg court_cases SaveSnapshot: upsert id=%s: %w", c.ID, err)
		}
	}
	return nil
}
