package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func prepareOperation(t *testing.T, db *testutil.Database, s ledger.Scope, key, operation string, args any) ledger.OperationProposal {
	t.Helper()
	var p ledger.OperationProposal
	result := db.Call(t, s, "prepare_operation", ledger.PrepareOperation{RequestKey: key, Operation: operation, Arguments: value(args)})
	if json.Unmarshal(result, &p) != nil || !ledger.ValidID(p.ID) || len(p.SHA256) != 64 || p.Preview.Version != 1 {
		t.Fatal("invalid prepared operation")
	}
	return p
}

func proposalOwner(t *testing.T, db *testutil.Database, s ledger.Scope) (string, int64) {
	t.Helper()
	var id string
	var epoch int64
	err := db.Admin.QueryRow(db.Context, "SELECT id::text,epoch FROM access.identities WHERE issuer=$1 AND subject=$2", "https://identity.example.com", "example-owner").Scan(&id, &epoch)
	if err == nil {
		return id, epoch
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	operator := access.Operator{DB: db.Admin}
	if _, err := operator.BindOwner(db.Context, s.TenantID, access.Identity{Issuer: "https://identity.example.com", Subject: "example-owner", AuthenticatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.Admin.QueryRow(db.Context, "SELECT id::text,epoch FROM access.identities WHERE issuer=$1 AND subject=$2", "https://identity.example.com", "example-owner").Scan(&id, &epoch); err != nil {
		t.Fatal(err)
	}
	return id, epoch
}

// A trusted fixture records an attributed decision; production owner-session
// authorization is exercised separately through the restricted owner role.
func approveOperation(t *testing.T, db *testutil.Database, s ledger.Scope, p ledger.OperationProposal, executor ledger.Scope) {
	t.Helper()
	owner, epoch := proposalOwner(t, db, s)
	_, err := db.Admin.Exec(db.Context, `INSERT INTO public.operation_decisions
        (tenant_id,book_id,proposal_id,revision,owner_id,owner_epoch,request_key,sha256,decision,reason,executor_actor_id,executor_connection_id)
        VALUES($1,$2,$3,1,$4,$5,$6,$7,'approve','Example accepted operation',$8,$9)`, s.TenantID, s.BookID, p.ID, owner, epoch, "decision-"+p.ID, p.SHA256, executor.ActorID, executor.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
}

func executeOperation(t *testing.T, db *testutil.Database, s ledger.Scope, p ledger.OperationProposal) json.RawMessage {
	t.Helper()
	return db.Call(t, s, "execute_proposal", ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256})
}

func TestOperationPreparationDerivesEffectsWithoutPosting(t *testing.T) {
	db, _, s, a, b := setup(t)
	q := post(a.ID, b.ID, "accepted-funding")
	p := prepareOperation(t, db, s, "prepare-funding", "post_journal", q)
	if p.Preview.EntriesAffected != 1 || p.Preview.Currency != "USD" || p.Preview.Scale != 2 || len(p.Preview.Classes) != 2 ||
		p.Preview.Classes[0] != (ledger.OperationClassEffect{Class: "asset", DebitMinor: "1250", CreditMinor: "0", ChangeMinor: "1250"}) ||
		p.Preview.Classes[1] != (ledger.OperationClassEffect{Class: "equity", DebitMinor: "0", CreditMinor: "1250", ChangeMinor: "-1250"}) {
		t.Fatal("independent effect expectation differs", p.Preview)
	}
	var count int
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.entries").Scan(&count); err != nil || count != 0 {
		t.Fatal("preparation posted accounting", err)
	}
	again := prepareOperation(t, db, s, "prepare-funding", "post_journal", q)
	if !bytes.Equal(value(p), value(again)) {
		t.Fatal("preparation retry changed the retained plan")
	}
	q.Lines[0].AmountMinor = "2500"
	q.Lines[1].AmountMinor = "2500"
	if _, err := db.Store.Call(db.Context, s, "prepare_operation", value(ledger.PrepareOperation{RequestKey: "prepare-funding", Operation: "post_journal", Arguments: value(q)})); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("changed preparation did not conflict", err)
	}
	if _, err := db.Store.Call(db.Context, s, "execute_proposal", value(ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256})); !errors.Is(err, ledger.ErrApprovalRequired) {
		t.Fatal("unapproved work posted", err)
	}
}

func TestOperationConsumptionIsAtomicConcurrentAndRetryable(t *testing.T) {
	db, book, s, a, b := setup(t)
	p := prepareOperation(t, db, s, "prepare", "post_journal", post(a.ID, b.ID, "financial-operation"))
	approveOperation(t, db, s, p, s)
	var wg sync.WaitGroup
	results := make([]json.RawMessage, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = db.Store.Call(db.Context, s, "execute_proposal", value(ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256}))
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !bytes.Equal(results[0], results[1]) {
		t.Fatal("concurrent consumption differs", errs)
	}
	var entry ledger.Entry
	if json.Unmarshal(results[0], &entry) != nil || entry.ActorID != s.ActorID {
		t.Fatal("executor attribution lost")
	}
	var entries, consumptions int
	if err := db.Admin.QueryRow(db.Context, "SELECT (SELECT count(*) FROM public.entries),(SELECT count(*) FROM public.operation_consumptions)").Scan(&entries, &consumptions); err != nil || entries != 1 || consumptions != 1 {
		t.Fatal("approval was spent twice", err)
	}
	outcome := db.Call(t, s, "get_request_outcome", ledger.OutcomeQuery{RequestKey: "financial-operation"})
	var o ledger.Outcome
	if json.Unmarshal(outcome, &o) != nil || o.Operation != "post_journal" || !bytes.Equal(o.Result, results[0]) {
		t.Fatal("normal receipt not recoverable")
	}
	err := (access.Operator{DB: db.Admin}).RevokeIdentity(db.Context, access.Identity{Issuer: "https://identity.example.com", Subject: "example-owner"})
	if err != nil {
		t.Fatal("owner revocation failed", err)
	}
	if !bytes.Equal(executeOperation(t, db, s, p), results[0]) {
		t.Fatal("retained effect depended on later approval validity")
	}
	verified, err := db.Owner.Verify(db.Context, book.ID)
	if err != nil || verified.Entries != 1 {
		t.Fatal("verification differs", err)
	}
}

func TestOperationDependenciesRejectClosedPeriodsButNotBalanceChanges(t *testing.T) {
	db, _, s, a, b := setup(t)
	moved := prepareOperation(t, db, s, "prepare-moved", "post_journal", post(a.ID, b.ID, "moved-post"))
	approveOperation(t, db, s, moved, s)
	db.Call(t, s, "post_journal", post(a.ID, b.ID, "different-post"))
	if _, err := db.Store.Call(db.Context, s, "execute_proposal", value(ledger.ExecuteProposal{ProposalID: moved.ID, SHA256: moved.SHA256})); err != nil {
		t.Fatal("another entry moving the balance refused approved work", err)
	}
	for _, mode := range []string{"period"} {
		t.Run(mode, func(t *testing.T) {
			db, _, s, a, b := setup(t)
			p := prepareOperation(t, db, s, "prepare", "post_journal", post(a.ID, b.ID, "prepared-post"))
			approveOperation(t, db, s, p, s)
			if err := db.ClosePeriod(db.Context, s.BookID, "2026-01-01", "2026-01-31"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Store.Call(db.Context, s, "execute_proposal", value(ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256})); !errors.Is(err, ledger.ErrProposalStale) {
				t.Fatal("changed relevant state was accepted", err)
			}
			var n int
			if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.operation_consumptions").Scan(&n); err != nil || n != 0 {
				t.Fatal("rejected work consumed approval", err)
			}
		})
	}
}

func TestOperationDependenciesIgnoreUnrelatedChanges(t *testing.T) {
	db, _, s, a, b := setup(t)
	c := db.Account(t, s, "1200", "asset")
	d := db.Account(t, s, "4000", "income")
	p := prepareOperation(t, db, s, "prepare", "post_journal", post(a.ID, b.ID, "prepared-post"))
	approveOperation(t, db, s, p, s)
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: a.ID, ExpectedRevision: 1, Code: "1010", Name: "Renamed example"})
	db.Call(t, s, "post_journal", post(c.ID, d.ID, "unrelated-post"))
	executeOperation(t, db, s, p)
}

func TestOperationPreparationRechecksAfterValidationRollback(t *testing.T) {
	db, _, s, a, b := setup(t)
	checks := 0
	db.Store.Authorize = func(ctx context.Context, _ pgx.Tx, scope ledger.Scope, method string, _ json.RawMessage) error {
		if method == "prepare_operation" {
			checks++
		}
		if method == "prepare_operation" && checks == 2 {
			_, err := (ledger.Store{Runtime: db.Runtime}).Call(ctx, scope, "post_journal", value(post(a.ID, b.ID, "intervening-post")))
			return err
		}
		return nil
	}
	q := ledger.PrepareOperation{RequestKey: "prepare", Operation: "post_journal", Arguments: value(post(a.ID, b.ID, "prepared-post"))}
	if _, err := db.Store.Call(db.Context, s, "prepare_operation", value(q)); !errors.Is(err, ledger.ErrProposalStale) {
		t.Fatal("changed state between validation and retention was accepted", err)
	}
	var entries, proposals int
	if err := db.Admin.QueryRow(db.Context, "SELECT (SELECT count(*) FROM public.entries),(SELECT count(*) FROM public.operation_proposals)").Scan(&entries, &proposals); err != nil || entries != 1 || proposals != 0 {
		t.Fatal("temporary preview escaped its rollback", err)
	}
}

func TestOperationSuccessorCompletesWithoutOriginalAgent(t *testing.T) {
	db, book, s, a, b := setup(t)
	p := prepareOperation(t, db, s, "prepare", "post_journal", post(a.ID, b.ID, "accepted-post"))
	next, err := db.Owner.CreateConnection(db.Context, book.ID, "Example successor", "write")
	if err != nil {
		t.Fatal(err)
	}
	successor := ledger.Scope{TenantID: next.TenantID, BookID: next.BookID, ActorID: next.ActorID, ConnectionID: next.ID, Access: next.Access}
	if err = db.Owner.Revoke(db.Context, s.ConnectionID); err != nil {
		t.Fatal(err)
	}
	approveOperation(t, db, s, p, successor)
	result := executeOperation(t, db, successor, p)
	var entry ledger.Entry
	if json.Unmarshal(result, &entry) != nil || entry.ActorID != successor.ActorID || p.ActorID != s.ActorID {
		t.Fatal("successor attribution differs")
	}
	if _, err = db.Store.Call(db.Context, s, "execute_proposal", value(ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256})); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("revoked proposer revived", err)
	}
	var state ledger.ProposalState
	if json.Unmarshal(db.Call(t, successor, "get_operation_proposal", ledger.ProposalQuery{ProposalID: p.ID}), &state) != nil || state.Status != "committed" || state.Outcome == nil || !bytes.Equal(state.Outcome.Result, result) {
		t.Fatal("successor cannot recover retained outcome")
	}
}

func TestOperationScopesAndDatabaseDecisionsFailClosed(t *testing.T) {
	db, _, s, a, b := setup(t)
	p := prepareOperation(t, db, s, "prepare", "post_journal", post(a.ID, b.ID, "accepted-post"))
	_, foreign := db.Book(t, "Other example")
	if _, err := db.Store.Call(db.Context, foreign, "get_operation_proposal", value(ledger.ProposalQuery{ProposalID: p.ID})); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("foreign plan visible", err)
	}
	bad := ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256[:63] + "f"}
	if bad.SHA256 == p.SHA256 {
		bad.SHA256 = p.SHA256[:63] + "0"
	}
	if _, err := db.Store.Call(db.Context, s, "execute_proposal", value(bad)); !errors.Is(err, ledger.ErrProposalStale) {
		t.Fatal("changed digest accepted", err)
	}
	tx, err := db.Runtime.Begin(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(db.Context)
	_, err = tx.Exec(db.Context, `INSERT INTO public.operation_decisions(tenant_id,book_id,proposal_id,revision,owner_id,owner_epoch,request_key,sha256,decision,reason)
        VALUES($1,$2,$3,1,$4,0,'forged',$5,'reject','Example')`, s.TenantID, s.BookID, p.ID, ledger.ID(), p.SHA256)
	if err == nil {
		t.Fatal("agent database role forged an owner decision")
	}
}

func TestOperationExpiryAndAdmissionFailureLeaveNoEffect(t *testing.T) {
	db, _, s, a, b := setup(t)
	p := prepareOperation(t, db, s, "prepare", "post_journal", post(a.ID, b.ID, "accepted-post"))
	approveOperation(t, db, s, p, s)
	db.Store.AdmitWrite = func(_ context.Context, _ pgx.Tx, _ ledger.Scope, _ string, _ json.RawMessage) error {
		return ledger.ErrDenied
	}
	if _, err := db.Store.Call(db.Context, s, "execute_proposal", value(ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256})); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("admission denial ignored", err)
	}
	var n int
	if err := db.Admin.QueryRow(db.Context, "SELECT (SELECT count(*) FROM public.entries)+(SELECT count(*) FROM public.operation_consumptions)").Scan(&n); err != nil || n != 0 {
		t.Fatal("failed admission left effects", err)
	}
	db.Store.AdmitWrite = nil
	oldID := ledger.ID()
	_, err := db.Admin.Exec(db.Context, `INSERT INTO public.operation_proposals(tenant_id,book_id,id,sequence,actor_id,connection_id,request_key,operation,operation_key,payload_bytes,created_at,expires_at)
        SELECT tenant_id,book_id,$4,sequence+100000,actor_id,connection_id,'expired',operation,operation_key,payload_bytes,
            clock_timestamp()-interval '1 hour',clock_timestamp()-interval '50 minutes'
        FROM public.operation_proposals WHERE tenant_id=$1 AND book_id=$2 AND id=$3`, s.TenantID, s.BookID, p.ID, oldID)
	if err != nil {
		t.Fatal(err)
	}
	var old ledger.ProposalState
	if json.Unmarshal(db.Call(t, s, "get_operation_proposal", ledger.ProposalQuery{ProposalID: oldID}), &old) != nil || old.Status != "expired" {
		t.Fatal("expiry was hidden")
	}
	if _, err = db.Store.Call(db.Context, s, "execute_proposal", value(ledger.ExecuteProposal{ProposalID: oldID, SHA256: old.Proposal.SHA256})); !errors.Is(err, ledger.ErrProposalExpired) {
		t.Fatal("expired work accepted", err)
	}
}

func TestOperationReversalPreservesExactMeasurements(t *testing.T) {
	db, _, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "JPY", 0, true)
	q := measuredPost(cash, equity.ID, "acquisition", "1", "0")
	quoteOn(&q, "0.001")
	var original ledger.Entry
	if json.Unmarshal(db.Call(t, s, "post_journal", q), &original) != nil {
		t.Fatal("invalid acquisition")
	}
	p := prepareOperation(t, db, s, "prepare-reversal", "reverse_journal", ledger.ReverseJournal{RequestKey: "accepted-reversal", EntryID: original.ID, AccountingDate: "2026-01-04", Reason: "Example correction"})
	if p.Preview.Lines[0].AmountMinor != "0" || p.Preview.Lines[0].Native == nil || p.Preview.Lines[0].Native.AmountMinor != "1" {
		t.Fatal("zero Book effect lost native movement")
	}
	approveOperation(t, db, s, p, s)
	var reversal ledger.Entry
	if json.Unmarshal(executeOperation(t, db, s, p), &reversal) != nil || reversal.ReversalOf != original.ID {
		t.Fatal("reversal target lost")
	}
	for i, line := range reversal.Lines {
		if line.Side == original.Lines[i].Side || !bytes.Equal(value(line.Native), value(original.Lines[i].Native)) || !bytes.Equal(value(line.Valuation), value(original.Lines[i].Valuation)) {
			t.Fatal("reversal revalued accepted measurements")
		}
	}
}

func TestOperationRecoveryPreservesWorkWithoutRestoringAuthority(t *testing.T) {
	db, book, s, a, b := setup(t)
	p := prepareOperation(t, db, s, "prepare", "post_journal", post(a.ID, b.ID, "accepted-post"))
	approveOperation(t, db, s, p, s)
	result := executeOperation(t, db, s, p)
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	if err = target.Owner.Restore(target.Context, before); err != nil {
		t.Fatal("prepared work restore failed", err)
	}
	after, err := target.Owner.Snapshot(target.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"operation_proposals", "operation_decisions", "operation_consumptions"} {
		if !bytes.Equal(before.Tables[table], after.Tables[table]) {
			t.Fatal("work history changed", table)
		}
	}
	var count int
	if err = target.Admin.QueryRow(target.Context, "SELECT count(*) FROM public.connections").Scan(&count); err != nil || count != 0 {
		t.Fatal("restore resurrected a grant", err)
	}
	next, err := target.Owner.CreateConnection(target.Context, book.ID, "Example recovery reader", "read")
	if err != nil {
		t.Fatal(err)
	}
	reader := ledger.Scope{TenantID: next.TenantID, BookID: next.BookID, ActorID: next.ActorID, ConnectionID: next.ID, Access: next.Access}
	var state ledger.ProposalState
	if json.Unmarshal(target.Call(t, reader, "get_operation_proposal", ledger.ProposalQuery{ProposalID: p.ID}), &state) != nil || state.Outcome == nil || !bytes.Equal(state.Outcome.Result, result) {
		t.Fatal("retained outcome unavailable to new reader")
	}
}

func TestOperationSourceRetentionAndPortableRecovery(t *testing.T) {
	db, s, _ := documentFixture(t)
	traceProposalQueries(t, db)
	doc := readyOriginal(t, db, s, "proposal-original")
	a := db.Account(t, s, "1000", "asset")
	b := db.Account(t, s, "3000", "equity")
	q := post(a.ID, b.ID, "accepted-post")
	q.Sources = []ledger.SourceInput{{DocumentID: doc.ID, Location: ledger.Location{Page: locationNumber(1)}}}
	p := prepareOperation(t, db, s, "prepare", "post_journal", q)
	var used bool
	if err := db.Admin.QueryRow(db.Context, "SELECT used_at IS NOT NULL FROM public.documents WHERE id=$1", doc.ID).Scan(&used); err != nil || !used {
		t.Fatal("proposal evidence was left expirable", err)
	}
	var archive bytes.Buffer
	if err := db.Owner.WriteArchive(db.Context, db.Store.Documents, &archive, ""); err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	vault, _ := restoreVault(t)
	if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("proposal evidence archive failed", err)
	}
	var n int
	if err := target.Admin.QueryRow(target.Context, "SELECT count(*) FROM public.operation_proposals WHERE id=$1", p.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("portable recovery lost pending work", err)
	}
}

func TestOperationPoliciesApplyToPreparedAndExecutedOperations(t *testing.T) {
	for _, operation := range []string{"post_journal", "reverse_journal"} {
		for _, phase := range []string{"prepare", "execute"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				db, _, s, a, b := setup(t)
				var args any = post(a.ID, b.ID, "accepted-post")
				baseline := 0
				if operation == "reverse_journal" {
					var entry ledger.Entry
					if json.Unmarshal(db.Call(t, s, "post_journal", args), &entry) != nil {
						t.Fatal("invalid original")
					}
					args = ledger.ReverseJournal{RequestKey: "accepted-reversal", EntryID: entry.ID, AccountingDate: "2026-01-04", Reason: "Example correction"}
					baseline = 1
				}
				var p ledger.OperationProposal
				if phase == "execute" {
					p = prepareOperation(t, db, s, "prepare", operation, args)
					approveOperation(t, db, s, p, s)
				}
				calls := 0
				db.Store.Authorize = func(_ context.Context, _ pgx.Tx, scope ledger.Scope, method string, input json.RawMessage) error {
					if method != operation {
						return nil
					}
					calls++
					if scope != s || !bytes.Equal(input, value(args)) {
						t.Fatal("restriction received another operation")
					}
					return ledger.ErrDenied
				}
				var err error
				if phase == "prepare" {
					_, err = db.Store.Call(db.Context, s, "prepare_operation", value(ledger.PrepareOperation{RequestKey: "prepare", Operation: operation, Arguments: value(args)}))
				} else {
					_, err = db.Store.Call(db.Context, s, "execute_proposal", value(ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256}))
				}
				if !errors.Is(err, ledger.ErrDenied) || calls != 1 {
					t.Fatal("underlying restriction bypassed", err, calls)
				}
				var entries, consumptions, receipts int
				if err = db.Admin.QueryRow(db.Context, "SELECT (SELECT count(*) FROM public.entries),(SELECT count(*) FROM public.operation_consumptions),(SELECT count(*) FROM public.requests WHERE request_key=ANY($1::text[]))", []string{"accepted-post", "accepted-reversal"}).Scan(&entries, &consumptions, &receipts); err != nil || entries != baseline || consumptions != 0 || receipts != baseline {
					t.Fatal("denied operation left an effect", err)
				}
			})
		}
	}
}

func TestOperationRecoveryReplaysEveryDecisionInNumericOrder(t *testing.T) {
	db, s, _ := documentFixture(t)
	a, b := db.Account(t, s, "1000", "asset"), db.Account(t, s, "3000", "equity")
	p := prepareOperation(t, db, s, "prepare", "post_journal", post(a.ID, b.ID, "accepted-post"))
	owner, epoch := proposalOwner(t, db, s)
	for revision := 1; revision <= 12; revision++ {
		decision, actor, connection := "reject", "", ""
		if revision%2 == 0 {
			decision, actor, connection = "approve", s.ActorID, s.ConnectionID
		}
		_, err := db.Admin.Exec(db.Context, `INSERT INTO public.operation_decisions
            (tenant_id,book_id,proposal_id,revision,owner_id,owner_epoch,request_key,sha256,decision,reason,executor_actor_id,executor_connection_id)
            VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,nullif($11,'')::uuid,nullif($12,'')::uuid)`, s.TenantID, s.BookID, p.ID, revision, owner, epoch, fmt.Sprintf("decision-%d", revision), p.SHA256, decision, fmt.Sprintf("Example decision %02d", 13-revision), actor, connection)
		if err != nil {
			t.Fatal(err)
		}
	}
	executeOperation(t, db, s, p)
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err = db.Owner.WriteArchive(db.Context, db.Store.Documents, &archive, ""); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"snapshot", "portable"} {
		t.Run(format, func(t *testing.T) {
			target := testutil.New(t)
			if format == "snapshot" {
				err = target.Owner.Restore(target.Context, before)
			} else {
				vault, _ := restoreVault(t)
				err = target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil)
			}
			if err != nil {
				t.Fatal("decision history did not restore", err)
			}
			after, err := target.Owner.Snapshot(target.Context)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"operation_proposals", "operation_decisions", "operation_consumptions", "requests", "entries", "lines"} {
				if !bytes.Equal(before.Tables[table], after.Tables[table]) {
					t.Fatal("decision or accounting history changed", table)
				}
			}
		})
	}
}
