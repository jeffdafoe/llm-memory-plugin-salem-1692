package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// estate_rate_integration_test.go — real-pg coverage for the durable edges of the
// estate rate (LLM-652 and its collection slice). Run against embedded Postgres
// with the prod baseline + post-baseline migrations applied (so both migrations
// are themselves under test); skipped under `go test -short`.

// The chest is the one piece of state the levy adds that MUST survive a restart:
// the coin has left the purses, so a chest that reloaded as zero would have
// destroyed it. Proves the column the migration adds, the upsert that writes it and
// the scan that reads it back agree, through the real checkpoint and load paths.
func TestIntegration_EstateRate_TownChestSurvivesCheckpoint(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	w := checkpointableWorld(repo)
	w.Environment.TownChest = 69
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if loaded.Environment.TownChest != 69 {
		t.Errorf("TownChest = %d after checkpoint + load, want 69", loaded.Environment.TownChest)
	}

	// A second checkpoint UPDATES the singleton rather than inserting beside it —
	// the chest must follow the ON CONFLICT arm too.
	loaded.Environment.TownChest = 107
	if err := SaveWorld(ctx, repo, loaded.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld (second): %v", err)
	}
	var chest int
	if err := f.Pool.QueryRow(ctx, `SELECT town_chest_coins FROM world_state WHERE id = 1`).Scan(&chest); err != nil {
		t.Fatalf("read world_state.town_chest_coins: %v", err)
	}
	if chest != 107 {
		t.Errorf("world_state.town_chest_coins = %d after the second checkpoint, want 107", chest)
	}
}

// The once-a-day stamp is the collection's only stored state, and it is durable
// for one reason: the village restarts many times a day for deploys, and a stamp
// lost between two rounds would collect twice. Proves the actor column the
// migration adds round-trips through the checkpoint (value and NULL both), and
// that a reloaded world still refuses a second collection in the same game-day.
func TestIntegration_EstateRate_AssessedAtStampSurvivesCheckpoint(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	const (
		payerID = "44444444-4444-4444-4444-444444444444"
		otherID = "55555555-5555-5555-5555-555555555555"
	)
	stamp := time.Date(2026, 9, 9, 14, 30, 0, 0, time.UTC)
	w := checkpointableWorld(repo)
	w.Actors = map[sim.ActorID]*sim.Actor{
		payerID: {ID: payerID, DisplayName: "Joseph Scott", Kind: sim.KindNPCShared, LLMAgent: sim.VendorAgentName, State: sim.StateIdle, Coins: 826, Inventory: map[sim.ItemKind]int{}, EstateRateAssessedAt: &stamp},
		otherID: {ID: otherID, DisplayName: "Moses James", Kind: sim.KindNPCShared, LLMAgent: sim.VendorAgentName, State: sim.StateIdle, Coins: 47, Inventory: map[sim.ItemKind]int{}},
	}
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	got := loaded.Actors[payerID].EstateRateAssessedAt
	if got == nil || !got.Equal(stamp) {
		t.Errorf("Joseph's EstateRateAssessedAt after checkpoint + load = %v, want %v", got, stamp)
	}
	if other := loaded.Actors[otherID].EstateRateAssessedAt; other != nil {
		t.Errorf("Moses's EstateRateAssessedAt = %v after checkpoint + load, want nil (never assessed)", other)
	}
}

// One real collection through the production sink: the durable row the engine
// writes for the payer lands in agent_action_log as result 'ok' with the marker,
// the coin-record seed leaves it out, the constable's own row is a `collected`
// beat the seed never selects, and an ordinary purchase from the same payer still
// seeds. A collection row is a `paid` row with result ok and a payer — exactly the
// shape the seed selects — so only the marker keeps it out; the chest is not an
// actor, and a row that got through would credit the constable with coin he does
// not hold.
func TestIntegration_EstateRate_DurableRowsThroughTheSinkAreExcludedFromTheSeed(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)

	const (
		payerID     = "44444444-4444-4444-4444-444444444444"
		otherID     = "55555555-5555-5555-5555-555555555555"
		constableID = "66666666-6666-6666-6666-666666666666"
		millID      = "77777777-7777-7777-7777-777777777777"
	)
	w := checkpointableWorld(repo)
	w.Settings.EstateRateFloor = 100
	w.Settings.EstateRatePctPerDay = 5
	w.Settings.Location = time.UTC
	w.Actors = map[sim.ActorID]*sim.Actor{
		payerID:     {ID: payerID, DisplayName: "Joseph Scott", Kind: sim.KindNPCShared, LLMAgent: sim.VendorAgentName, State: sim.StateIdle, Coins: 864, Inventory: map[sim.ItemKind]int{}, InsideStructureID: millID},
		otherID:     {ID: otherID, DisplayName: "Moses James", Kind: sim.KindNPCShared, LLMAgent: sim.VendorAgentName, State: sim.StateIdle, Coins: 47, Inventory: map[sim.ItemKind]int{}},
		constableID: {ID: constableID, DisplayName: "Constable Gideon Marsh", Kind: sim.KindNPCStateful, LLMAgent: "zbbs-gideon-marsh", State: sim.StateIdle, Coins: 7, Inventory: map[sim.ItemKind]int{}, Attributes: map[string][]byte{sim.AttrConstable: nil}},
	}
	// Persist the actors first: agent_action_log.actor_id is a FK to actor(id).
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}
	// The business lives in memory only — the collection reads w.VillageObjects,
	// and a placement with no real asset would not survive the checkpoint anyway.
	w.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{
		millID: {ID: millID, DisplayName: "Mill", OwnerActorID: payerID, Tags: []string{sim.TagBusiness}},
	}

	sink := NewActionLogRepo(f.Pool)
	w.SetActionLogSink(sink)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := sim.CollectEstateRate(constableID, millID, now).Fn(w); err != nil {
		t.Fatalf("CollectEstateRate: %v", err)
	}
	// An ordinary purchase beside it, the shape handlePayResolvedActionLog writes.
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID: payerID, OccurredAt: now.Add(time.Minute), ActionType: sim.ActionTypePaid,
		Payload:     map[string]any{"recipient": "Moses James", "recipient_actor_id": otherID, "amount": 4, "ledger_id": 8123},
		SpeakerName: "Joseph Scott", Source: "agent",
	})
	// Run drains what is buffered and returns once ctx is cancelled — a cancelled
	// ctx makes it a synchronous flush.
	drained, cancel := context.WithCancel(ctx)
	cancel()
	sink.Run(drained)

	if w.Actors[payerID].Coins != 864-38 || w.Environment.TownChest != 38 || w.Actors[constableID].Coins != 7 {
		t.Fatalf("collection: Joseph %d, chest %d, Gideon %d; want 826 / 38 / 7", w.Actors[payerID].Coins, w.Environment.TownChest, w.Actors[constableID].Coins)
	}

	var (
		result, source, recipient, forText, marker, amount, collectedBy string
		hasRecipientID                                                  bool
	)
	if err := f.Pool.QueryRow(ctx, `
		SELECT result, source, payload->>'recipient', payload->>'for', payload->>'estate_rate',
		       payload->>'amount', payload->>'collected_by', payload ? 'recipient_actor_id'
		  FROM agent_action_log
		 WHERE actor_id = $1 AND payload ? 'estate_rate'`, payerID,
	).Scan(&result, &source, &recipient, &forText, &marker, &amount, &collectedBy, &hasRecipientID); err != nil {
		t.Fatalf("read the payer's collection row: %v", err)
	}
	if result != "ok" || source != "engine" || recipient != "the town" || forText != "the rate on your estate" || marker != "true" || amount != "38" || collectedBy != "Constable Gideon Marsh" || hasRecipientID {
		t.Errorf("payer row = result %q source %q recipient %q for %q marker %q amount %q collected_by %q recipient_id %v",
			result, source, recipient, forText, marker, amount, collectedBy, hasRecipientID)
	}
	var collectorType, payer string
	if err := f.Pool.QueryRow(ctx, `
		SELECT action_type, payload->>'payer' FROM agent_action_log WHERE actor_id = $1`, constableID,
	).Scan(&collectorType, &payer); err != nil {
		t.Fatalf("read the constable's row: %v", err)
	}
	if collectorType != "collected" || payer != "Joseph Scott" {
		t.Errorf("constable row = %s from %q, want collected from Joseph Scott", collectorType, payer)
	}

	// A marked row whose value is null is still a collection — the exclusion keys
	// on presence, not text.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, occurred_at, source, action_type, payload, result, speaker_name)
		 VALUES ($1, $2, 'engine', 'paid', '{"recipient": "the town", "amount": 9, "estate_rate": null}'::jsonb, 'ok', 'Joseph Scott')`,
		payerID, now.Add(2*time.Minute),
	); err != nil {
		t.Fatalf("insert null-marker row: %v", err)
	}

	rows, err := NewCoinRecordsRepo(f.Pool).LoadPaymentsSince(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("LoadPaymentsSince: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want 1 (the purchase only): %+v", len(rows), rows)
	}
	if rows[0].Amount != "4" || rows[0].CounterpartyActorID != otherID {
		t.Errorf("surviving row = %+v, want the 4-coin purchase from Moses", rows[0])
	}
}
