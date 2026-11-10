package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestProposalSocketPreservesApprovalStaleAndExpiry(t *testing.T) {
	db := testutil.New(t)
	_, scope := db.Book(t, "Example Book")
	a, b := db.Account(t, scope, "1000", "asset"), db.Account(t, scope, "3000", "equity")
	uid := uint32(os.Getuid())
	token := secret()
	s := &Server{Admin: db.Owner, Ledger: db.Store, sessions: map[string]session{tokenKey(token): {Scope: scope, UID: uid, Expires: time.Now().Add(time.Hour)}}}
	dir, err := os.MkdirTemp("/tmp", "vult-proposals-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "service.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(db.Context)
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("proposal socket did not stop")
		}
	})
	client := local.Client{Socket: path, ExpectedUID: uid}
	journal := ledger.PostJournal{RequestKey: "accepted-post", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example funding", Lines: []ledger.PostLine{
		{AccountID: a.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: b.ID, Side: "credit", AmountMinor: "1250"},
	}}
	result, err := client.Call(ctx, "prepare_operation", token, ledger.PrepareOperation{RequestKey: "prepare", Operation: "post_journal", Arguments: marshal(journal)})
	var p ledger.OperationProposal
	if err != nil || json.Unmarshal(result, &p) != nil || p.ID == "" {
		t.Fatal("socket preparation failed", err)
	}
	assertError := func(q ledger.ExecuteProposal, want error) {
		t.Helper()
		_, err := client.Call(ctx, "execute_proposal", token, q)
		if err == nil || err.Error() != want.Error() {
			t.Fatal("socket lost the exact refusal and next step", err, want)
		}
	}
	q := ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256}
	assertError(q, ledger.ErrApprovalRequired)
	assertError(ledger.ExecuteProposal{ProposalID: p.ID, SHA256: strings.Repeat("0", 64)}, ledger.ErrProposalStale)
	if err := db.ClosePeriod(ctx, scope.BookID, "2026-01-01", "2026-01-31"); err != nil {
		t.Fatal(err)
	}
	assertError(q, ledger.ErrProposalStale)
	expired := ledger.ID()
	_, err = db.Admin.Exec(ctx, `INSERT INTO public.operation_proposals(tenant_id,book_id,id,sequence,actor_id,connection_id,request_key,operation,operation_key,payload_bytes,created_at,expires_at)
        SELECT tenant_id,book_id,$4,sequence+100000,actor_id,connection_id,'expired',operation,operation_key,payload_bytes,
            clock_timestamp()-interval '1 hour',clock_timestamp()-interval '50 minutes'
        FROM public.operation_proposals WHERE tenant_id=$1 AND book_id=$2 AND id=$3`, scope.TenantID, scope.BookID, p.ID, expired)
	if err != nil {
		t.Fatal(err)
	}
	result, err = client.Call(ctx, "get_operation_proposal", token, ledger.ProposalQuery{ProposalID: expired})
	var old ledger.ProposalState
	if err != nil || json.Unmarshal(result, &old) != nil || old.Status != "expired" {
		t.Fatal("socket inspection lost expiry", err)
	}
	assertError(ledger.ExecuteProposal{ProposalID: expired, SHA256: old.Proposal.SHA256}, ledger.ErrProposalExpired)
	var rows int
	if err = db.Admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM public.entries)+(SELECT count(*) FROM public.operation_consumptions)").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("refused socket operations posted", rows, err)
	}
}
