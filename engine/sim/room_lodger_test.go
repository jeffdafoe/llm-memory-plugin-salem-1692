package sim

import (
	"testing"
	"time"
)

// room_lodger_test.go — ZBBS-HOME-296 PR2. The canonical per-grant lodging
// predicate: an active, unexpired ledger RoomAccess.

func TestIsActiveLedgerGrant(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	future := now.Add(72 * time.Hour)
	past := now.Add(-time.Hour)

	mk := func(src RoomAccessSource, active bool, exp *time.Time) *RoomAccess {
		return &RoomAccess{RoomID: 2, Source: src, Active: active, ExpiresAt: exp}
	}

	cases := []struct {
		name  string
		grant *RoomAccess
		want  bool
	}{
		{"active ledger, future expiry", mk(AccessSourceLedger, true, &future), true},
		{"active ledger, past expiry", mk(AccessSourceLedger, true, &past), false},
		{"inactive ledger", mk(AccessSourceLedger, false, &future), false},
		{"ledger with nil expiry", mk(AccessSourceLedger, true, nil), false},
		{"staff grant (non-ledger)", mk(AccessSourceStaff, true, nil), false},
		{"nil grant", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsActiveLedgerGrant(tc.grant, now); got != tc.want {
				t.Errorf("IsActiveLedgerGrant = %v, want %v", got, tc.want)
			}
		})
	}
}
