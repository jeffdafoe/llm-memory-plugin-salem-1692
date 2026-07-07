package httpapi

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestUmbilicalLaborLedgerFromSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 4, 14, 19, 0, 0, time.UTC)
	accepted := now.Add(-5 * time.Minute)
	workingUntil := now.Add(2 * time.Hour)
	enRouteDeadline := now.Add(30 * time.Minute)

	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"lewis":    {DisplayName: "Lewis Walker"},
			"prudence": {DisplayName: "Prudence Ward"},
			"mae":      {DisplayName: "Mae"},
			"seth":     {DisplayName: "Seth"},
			"tom":      {DisplayName: "Tom"},
			// "ada" deliberately absent — an unknown id resolves to an empty name.
		},
		LaborLedger: map[sim.LaborID]*sim.LaborOffer{
			1: {
				ID: 1, WorkerID: "lewis", EmployerID: "prudence",
				State:       sim.LaborStateWorking,
				Reward:      10,
				RewardItems: []sim.ItemKindQty{{Kind: "porridge", Qty: 1}},
				DurationMin: 120,
				HuddleID:    "hud-1", SceneID: "sc-1",
				CreatedAt:     now,
				ExpiresAt:     now.Add(3 * time.Minute),
				AcceptedAt:    &accepted,
				WorkStartedAt: &accepted,
				WorkingUntil:  &workingUntil,
			},
			2: nil, // a nil offer must be skipped, not panic
			3: {
				ID: 3, WorkerID: "mae", EmployerID: "seth",
				State:       sim.LaborStatePending,
				Reward:      5,
				DurationMin: 180,
				CreatedAt:   now,
				ExpiresAt:   now.Add(3 * time.Minute),
			},
			4: {
				ID: 4, WorkerID: "tom", EmployerID: "ada",
				State:           sim.LaborStateEnRoute,
				Reward:          8,
				DurationMin:     240,
				CreatedAt:       now,
				ExpiresAt:       now.Add(3 * time.Minute),
				AcceptedAt:      &accepted,
				EnRouteDeadline: enRouteDeadline,
				EnRouteWaiting:  true,
			},
		},
	}

	out := umbilicalLaborLedgerFromSnapshot(snap, 0)
	if out.Total != 3 || out.Returned != 3 {
		t.Fatalf("total/returned = %d/%d, want 3/3 (nil offer skipped)", out.Total, out.Returned)
	}
	// Most-recent first: id 4, 3, 1.
	if out.Offers[0].ID != 4 || out.Offers[1].ID != 3 || out.Offers[2].ID != 1 {
		t.Fatalf("order = [%d,%d,%d], want [4,3,1] (id desc)", out.Offers[0].ID, out.Offers[1].ID, out.Offers[2].ID)
	}

	// Working offer: names resolve, both reward legs surface, the Working
	// timestamps are set and no terminal timestamp yet.
	working := out.Offers[2]
	if working.WorkerName != "Lewis Walker" || working.EmployerName != "Prudence Ward" {
		t.Errorf("working name resolution wrong: worker=%q employer=%q", working.WorkerName, working.EmployerName)
	}
	if working.State != "working" || working.RewardCoins != 10 || working.DurationMin != 120 {
		t.Errorf("working state/reward/duration wrong: %+v", working)
	}
	if len(working.RewardItems) != 1 || working.RewardItems[0].Item != "porridge" || working.RewardItems[0].Qty != 1 {
		t.Errorf("working in-kind reward leg wrong: %+v", working.RewardItems)
	}
	if working.HuddleID != "hud-1" || working.SceneID != "sc-1" {
		t.Errorf("working co-presence ids wrong: huddle=%q scene=%q", working.HuddleID, working.SceneID)
	}
	if working.AcceptedAt == nil || working.WorkStartedAt == nil || working.WorkingUntil == nil {
		t.Errorf("working timestamps should be set: %+v", working)
	}
	if working.ResolvedAt != nil || working.EnRouteDeadline != nil {
		t.Errorf("working should have no resolved/en-route-deadline: %+v", working)
	}

	// Pending offer: no goods leg, no accept/work/en-route timestamps.
	pending := out.Offers[1]
	if pending.State != "pending" || len(pending.RewardItems) != 0 {
		t.Errorf("pending state/items wrong: %+v", pending)
	}
	if pending.AcceptedAt != nil || pending.WorkStartedAt != nil || pending.WorkingUntil != nil || pending.EnRouteDeadline != nil {
		t.Errorf("pending should carry no accept/work/en-route timestamps: %+v", pending)
	}

	// EnRoute offer: accepted + en-route-deadline set, work not started, and an
	// unknown employer id resolves to an empty name (not a panic).
	enRoute := out.Offers[0]
	if enRoute.State != "en_route" || !enRoute.EnRouteWaiting || enRoute.EnRouteDeadline == nil {
		t.Errorf("en_route state/waiting/deadline wrong: %+v", enRoute)
	}
	if enRoute.WorkStartedAt != nil || enRoute.WorkingUntil != nil {
		t.Errorf("en_route should not have work-started/working-until: %+v", enRoute)
	}
	if enRoute.WorkerName != "Tom" || enRoute.EmployerName != "" {
		t.Errorf("en_route name resolution wrong (unknown employer → empty): worker=%q employer=%q", enRoute.WorkerName, enRoute.EmployerName)
	}

	// limit caps to the newest.
	if got := umbilicalLaborLedgerFromSnapshot(snap, 1); got.Returned != 1 || got.Total != 3 || got.Offers[0].ID != 4 {
		t.Errorf("limit=1 should return only the newest (id 4): total=%d returned=%d", got.Total, got.Returned)
	}

	// nil snapshot → empty, no panic.
	if got := umbilicalLaborLedgerFromSnapshot(nil, 0); got.Total != 0 || len(got.Offers) != 0 {
		t.Errorf("nil snapshot should yield empty, got %+v", got)
	}
}
