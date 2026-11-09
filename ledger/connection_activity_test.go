package ledger_test

import (
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// Each agent's last recorded change, and agents the owner disconnected, are
// listed separately; an agent that recorded nothing has no activity time.
func TestConnectionsReportRecordedActivityAndDisconnections(t *testing.T) {
	db := testutil.New(t)
	book, writer := db.Book(t, "Example agents Book")
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := owner.CreateConnection(db.Context, book.ID, "Example reader", "read")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := owner.CreateConnection(db.Context, book.ID, "Example former agent", "write")
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.Revoke(db.Context, gone.ID); err != nil {
		t.Fatal(err)
	}
	cash, equity := db.Account(t, writer, "1000", "asset"), db.Account(t, writer, "3000", "equity")
	db.Call(t, writer, "post_journal", ledger.PostJournal{RequestKey: "funding", EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example funding",
		Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "100"}, {AccountID: equity.ID, Side: "credit", AmountMinor: "100"}}})

	live, err := owner.ConnectionsWithActivity(db.Context, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range live {
		seen[c.ID] = true
		switch c.ID {
		case writer.ConnectionID:
			if c.LastRecordedAt == nil {
				t.Fatal("the agent that posted has no recorded activity")
			}
		case reader.ID:
			if c.LastRecordedAt != nil {
				t.Fatal("an agent that recorded nothing has an activity time")
			}
		}
		if c.RevokedAt != nil {
			t.Fatal("a disconnected agent is listed as connected")
		}
	}
	if !seen[writer.ConnectionID] || !seen[reader.ID] || seen[gone.ID] {
		t.Fatalf("connected agents %v", seen)
	}
	disconnected, err := owner.ConnectionsWithActivity(db.Context, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(disconnected) != 1 || disconnected[0].ID != gone.ID || disconnected[0].RevokedAt == nil {
		t.Fatalf("disconnected agents %+v", disconnected)
	}
}
