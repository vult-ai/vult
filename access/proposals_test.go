package access_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func ownerProposal(t *testing.T) (*fixture, string, string, access.Identity, ledger.Scope, ledger.OperationProposal) {
	t.Helper()
	f := fresh(t)
	tenant, session, identity := f.owner(t, "example")
	book := f.book(t, tenant, session)
	var connection ledger.Connection
	if err := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		connection, err = owner.CreateConnection(f.db.Context, book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: tenant, BookID: book.ID, ConnectionID: connection.ID, ActorID: connection.ActorID, Access: "write"}
	a, b := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
	args := ledger.PostJournal{RequestKey: "accepted-post", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example entry", Lines: []ledger.PostLine{
		{AccountID: a.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: b.ID, Side: "credit", AmountMinor: "1250"},
	}}
	data, _ := json.Marshal(args)
	var proposal ledger.OperationProposal
	if json.Unmarshal(f.db.Call(t, scope, "prepare_operation", ledger.PrepareOperation{RequestKey: "prepare", Operation: "post_journal", Arguments: data}), &proposal) != nil {
		t.Fatal("invalid proposal")
	}
	return f, tenant, session, identity, scope, proposal
}

func decisionRequest(scope ledger.Scope, p ledger.OperationProposal) ledger.DecideOperation {
	return ledger.DecideOperation{BookID: scope.BookID, ProposalID: p.ID, SHA256: p.SHA256, RequestKey: "decision", Decision: "approve", Reason: "Example accepted effects"}
}

func countRows(t *testing.T, f *fixture, table string) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public."+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func postedEntry(t *testing.T, out ledger.OperationDecisionResult) ledger.Entry {
	t.Helper()
	var entry ledger.Entry
	if out.Status != "committed" || out.Outcome == nil || out.Outcome.Operation != "post_journal" || json.Unmarshal(out.Outcome.Result, &entry) != nil || entry.ID == "" {
		t.Fatal("approval did not return its posted entry", out.Status)
	}
	return entry
}

func TestOperationOwnerApprovalPostsTheExactEntryOnce(t *testing.T) {
	f, tenant, session, identity, scope, p := ownerProposal(t)
	if p.ExpiresAt != nil {
		t.Fatal("a new proposal has a time limit")
	}
	q := decisionRequest(scope, p)
	out, err := f.service.DecideOperation(f.db.Context, session, tenant, q, nil)
	if err != nil || out.Decision.Revision != 1 || out.Decision.Decision != "approve" || out.Decision.ExecutorConnectionID != "" || out.Decision.ExpiresAt != nil {
		t.Fatal("owner approval failed", err)
	}
	entry := postedEntry(t, out)
	if entry.ActorID != scope.ActorID || entry.Description != "Example entry" || len(entry.Lines) != 2 {
		t.Fatal("posted entry differs from the prepared entry")
	}
	if countRows(t, f, "entries") != 1 || countRows(t, f, "operation_consumptions") != 1 {
		t.Fatal("approval did not post exactly one entry")
	}
	var owner, poster, receiptActor, receiptKey string
	if err = f.db.Admin.QueryRow(f.db.Context, `SELECT d.owner_id::text,c.actor_id::text,r.actor_id::text,r.request_key FROM public.operation_consumptions c
        JOIN public.operation_decisions d ON d.tenant_id=c.tenant_id AND d.book_id=c.book_id AND d.proposal_id=c.proposal_id AND d.revision=c.decision_revision
        JOIN public.requests r ON r.tenant_id=c.tenant_id AND r.book_id=c.book_id AND r.id=c.receipt_id`).Scan(&owner, &poster, &receiptActor, &receiptKey); err != nil {
		t.Fatal(err)
	}
	if poster != owner || receiptActor != scope.ActorID || receiptKey != "accepted-post" {
		t.Fatal("record does not keep the preparer, the approving owner and the financial key", poster, owner, receiptActor)
	}

	// The agent sees the posted entry, and retrying its own financial request
	// returns that entry instead of posting another.
	var state ledger.ProposalState
	if json.Unmarshal(f.db.Call(t, scope, "get_operation_proposal", ledger.ProposalQuery{ProposalID: p.ID}), &state) != nil || state.Status != "committed" || state.Outcome == nil {
		t.Fatal("agent cannot observe the posted outcome", state.Status)
	}
	var retried ledger.Entry
	if json.Unmarshal(f.db.Call(t, scope, "post_journal", json.RawMessage(p.Arguments)), &retried) != nil || retried.ID != entry.ID || countRows(t, f, "entries") != 1 {
		t.Fatal("agent retry posted a second entry")
	}
	input, _ := json.Marshal(ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256})
	if _, err = f.db.Store.Call(f.db.Context, scope, "execute_proposal", input); !errors.Is(err, ledger.ErrDenied) || countRows(t, f, "entries") != 1 {
		t.Fatal("agent executed an owner-posted proposal again", err)
	}

	// A lost reply is recovered without another sign-in and without posting.
	identity.AuthenticatedAt = time.Now().UTC()
	stale, err := f.service.IssueSession(f.db.Context, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(stale)); err != nil {
		t.Fatal(err)
	}
	retry, err := f.service.DecideOperation(f.db.Context, stale, tenant, q, nil)
	firstJSON, _ := json.Marshal(out)
	retryJSON, _ := json.Marshal(retry)
	if err != nil || !bytes.Equal(firstJSON, retryJSON) || countRows(t, f, "entries") != 1 {
		t.Fatal("lost approval response cannot be recovered exactly", err)
	}
	changed := q
	changed.Reason = "Changed interpretation"
	if _, err = f.service.DecideOperation(f.db.Context, stale, tenant, changed, nil); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("changed decision retry accepted", err)
	}
	again := q
	again.RequestKey = "second-approval"
	again.ExpectedRevision = 1
	if _, err = f.service.DecideOperation(f.db.Context, session, tenant, again, nil); !errors.Is(err, ledger.ErrProposalDecided) || countRows(t, f, "entries") != 1 {
		t.Fatal("a second approval posted again", err)
	}
	_, foreignSession, _ := f.owner(t, "other")
	if _, err = f.service.DecideOperation(f.db.Context, foreignSession, tenant, q, nil); !errors.Is(err, access.ErrDenied) {
		t.Fatal("foreign identity recovered owner decision", err)
	}
}

func TestOperationOwnerApprovalRequiresFreshSignIn(t *testing.T) {
	f, tenant, session, _, scope, p := ownerProposal(t)
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(session)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.DecideOperation(f.db.Context, session, tenant, decisionRequest(scope, p), nil); !errors.Is(err, ledger.ErrFreshOwnerProof) {
		t.Fatal("approval without a fresh sign-in", err)
	}
	if countRows(t, f, "entries") != 0 || countRows(t, f, "operation_decisions") != 0 {
		t.Fatal("refused approval left effects")
	}
}

func TestOperationOwnerApprovalPostsAfterTheAgentDisconnects(t *testing.T) {
	f, tenant, session, _, scope, p := ownerProposal(t)
	if err := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		return owner.Revoke(f.db.Context, scope.ConnectionID)
	}); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.DecideOperation(f.db.Context, session, tenant, decisionRequest(scope, p), nil)
	if err != nil {
		t.Fatal("owner could not post prepared work after disconnecting its agent", err)
	}
	if entry := postedEntry(t, out); entry.ActorID != scope.ActorID {
		t.Fatal("posted entry lost its preparer")
	}
}

func TestOperationOwnerRejectionAndExplicitReplacementRemainRecorded(t *testing.T) {
	f, tenant, session, _, scope, p := ownerProposal(t)
	q := decisionRequest(scope, p)
	q.Decision = "reject"
	q.Reason = "Example needs review"
	out, err := f.service.DecideOperation(f.db.Context, session, tenant, q, nil)
	if err != nil || out.Status != "rejected" || out.Outcome != nil || countRows(t, f, "entries") != 0 {
		t.Fatal("rejection failed or posted", err)
	}
	var state ledger.ProposalState
	if json.Unmarshal(f.db.Call(t, scope, "get_operation_proposal", ledger.ProposalQuery{ProposalID: p.ID}), &state) != nil || state.Status != "rejected" || state.Decision == nil || state.Decision.Reason != "Example needs review" {
		t.Fatal("agent cannot read the rejection and its reason")
	}
	input, _ := json.Marshal(ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256})
	if _, err = f.db.Store.Call(f.db.Context, scope, "execute_proposal", input); !errors.Is(err, ledger.ErrApprovalRequired) {
		t.Fatal("rejected operation executed", err)
	}
	replacement := decisionRequest(scope, p)
	replacement.RequestKey = "replacement"
	replacement.ExpectedRevision = 1
	out, err = f.service.DecideOperation(f.db.Context, session, tenant, replacement, nil)
	if err != nil || out.Decision.Revision != 2 {
		t.Fatal("explicit replacement decision failed", err)
	}
	postedEntry(t, out)
	if countRows(t, f, "operation_decisions") != 2 || countRows(t, f, "entries") != 1 {
		t.Fatal("decision history overwritten or posting repeated")
	}
}

func TestOperationOwnerDecisionRejectsStaleForeignAndRefusedWork(t *testing.T) {
	for _, mode := range []string{"changed-period", "foreign-plan", "admission", "posting-admission", "agent-posted-first"} {
		t.Run(mode, func(t *testing.T) {
			f, tenant, session, _, scope, p := ownerProposal(t)
			q := decisionRequest(scope, p)
			var admit ledger.DecisionAdmission
			want := ledger.ErrNotFound
			entries := 0
			switch mode {
			case "changed-period":
				if err := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
					return owner.ClosePeriod(f.db.Context, scope.BookID, "2026-01-01", "2026-01-31")
				}); err != nil {
					t.Fatal(err)
				}
				want = ledger.ErrProposalStale
			case "foreign-plan":
				q.ProposalID = ledger.ID()
			case "admission":
				admit = func(context.Context, pgx.Tx, ledger.Scope, string, json.RawMessage) error { return access.ErrDenied }
				want = access.ErrDenied
			case "posting-admission":
				admit = func(_ context.Context, _ pgx.Tx, _ ledger.Scope, operation string, _ json.RawMessage) error {
					if operation == "post_journal" {
						return access.ErrDenied
					}
					return nil
				}
				want = access.ErrDenied
			case "agent-posted-first":
				// The agent's own posting used the prepared request key, so the
				// approval can't post it a second time.
				f.db.Call(t, scope, "post_journal", json.RawMessage(p.Arguments))
				want, entries = ledger.ErrConflict, 1
			}
			if _, err := f.service.DecideOperation(f.db.Context, session, tenant, q, admit); !errors.Is(err, want) {
				t.Fatal("invalid owner decision accepted or misreported", mode, err)
			}
			if countRows(t, f, "operation_decisions") != 0 || countRows(t, f, "operation_consumptions") != 0 || countRows(t, f, "entries") != entries {
				t.Fatal("refused decision left history or posted")
			}
		})
	}
}

func TestOperationOwnerApprovesWorkAfterOtherEntriesMoveTheBalance(t *testing.T) {
	f, tenant, session, _, scope, p := ownerProposal(t)
	var args ledger.PostJournal
	if json.Unmarshal(p.Arguments, &args) != nil {
		t.Fatal("invalid arguments")
	}
	// Another entry on the same accounts, such as a second item approved first.
	args.RequestKey = "other-post"
	f.db.Call(t, scope, "post_journal", args)
	if _, err := f.service.DecideOperation(f.db.Context, session, tenant, decisionRequest(scope, p), nil); err != nil {
		t.Fatal("work prepared before another entry could not be approved", err)
	}
	if countRows(t, f, "operation_consumptions") != 1 || countRows(t, f, "entries") != 2 {
		t.Fatal("the approved work was not posted exactly once")
	}
}

func TestOperationOwnerSeesAnOlderCopyAsADuplicate(t *testing.T) {
	f, tenant, session, _, scope, p := ownerProposal(t)
	var args ledger.PostJournal
	if json.Unmarshal(p.Arguments, &args) != nil {
		t.Fatal("invalid arguments")
	}
	args.RequestKey = "copy-post"
	data, _ := json.Marshal(args)
	f.db.Call(t, scope, "prepare_operation", ledger.PrepareOperation{RequestKey: "copy", Operation: "post_journal", Arguments: data})
	read := func(id string) ledger.ProposalState {
		t.Helper()
		var out ledger.ProposalState
		if err := f.service.WithOwner(f.db.Context, session, tenant, false, func(owner ledger.TenantAdmin) error {
			var err error
			out, err = owner.OperationProposal(f.db.Context, scope.BookID, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	older := read(p.ID)
	if older.DuplicateOf <= p.Sequence {
		t.Fatal("the older copy was not marked as a duplicate of the newer one", older.DuplicateOf)
	}
	var newer string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT id::text FROM public.operation_proposals WHERE sequence=$1", older.DuplicateOf).Scan(&newer); err != nil {
		t.Fatal(err)
	}
	if read(newer).DuplicateOf != 0 {
		t.Fatal("the newest copy was marked as a duplicate")
	}
}

func TestOperationOwnerQueueCountsWaitingWork(t *testing.T) {
	f, tenant, session, _, scope, p := ownerProposal(t)
	var args ledger.PostJournal
	if json.Unmarshal(p.Arguments, &args) != nil {
		t.Fatal("invalid arguments")
	}
	for _, key := range []string{"second", "third"} {
		args.RequestKey = key + "-post"
		data, _ := json.Marshal(args)
		f.db.Call(t, scope, "prepare_operation", ledger.PrepareOperation{RequestKey: key, Operation: "post_journal", Arguments: data})
	}
	page := func(waitingOnly bool) ledger.OperationProposalPage {
		t.Helper()
		var out ledger.OperationProposalPage
		if err := f.service.WithOwner(f.db.Context, session, tenant, false, func(owner ledger.TenantAdmin) error {
			var err error
			out, err = owner.OperationProposals(f.db.Context, scope.BookID, 0, 25, waitingOnly)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if all := page(false); all.Waiting != 3 || len(all.Plans) != 3 {
		t.Fatal("waiting count differs", all.Waiting, len(all.Plans))
	}
	if _, err := f.service.DecideOperation(f.db.Context, session, tenant, decisionRequest(scope, p), nil); err != nil {
		t.Fatal(err)
	}
	rejected := page(true).Plans[0]
	q := ledger.DecideOperation{BookID: scope.BookID, ProposalID: rejected.ID, SHA256: rejected.SHA256, RequestKey: "reject", Decision: "reject", Reason: "Example duplicate"}
	if _, err := f.service.DecideOperation(f.db.Context, session, tenant, q, nil); err != nil {
		t.Fatal(err)
	}
	all, waiting := page(false), page(true)
	if all.Waiting != 1 || len(all.Plans) != 3 || waiting.Waiting != 1 || len(waiting.Plans) != 1 || waiting.Plans[0].Status != "approval_required" || waiting.Plans[0].ExpiresAt != nil {
		t.Fatal("waiting queue differs after decisions", all.Waiting, len(waiting.Plans))
	}
}

func TestOperationOwnerPostingCannotBeClaimedByAnAgent(t *testing.T) {
	f, tenant, session, _, scope, p := ownerProposal(t)
	// An approval recorded in another transaction does not authorize writes.
	if _, err := f.service.DecideOperation(f.db.Context, session, tenant, decisionRequest(scope, p), nil); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Runtime.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.db.Context)
	if _, err = tx.Exec(f.db.Context, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true),set_config('vult.owner_posting',$5,true)", tenant, scope.BookID, scope.ActorID, scope.ConnectionID, p.ID); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err = tx.QueryRow(f.db.Context, "SELECT public.ledger_allowed($1,$2,true)", tenant, scope.BookID).Scan(&allowed); err != nil || allowed {
		t.Fatal("an agent claimed the owner's posting authority", err)
	}
}

func TestOperationOwnerRoleRequiresExactReview(t *testing.T) {
	f, tenant, session, _, scope, p := ownerProposal(t)
	pending, expired := ledger.ID(), ledger.ID()
	func() {
		ctx := f.db.Context
		tx, err := f.db.Admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true)", tenant, scope.BookID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO public.documents
		    (tenant_id,book_id,id,sequence,sha256,size_bytes,media_type,original_name,storage_id,key_id,state,actor_id,expires_at)
		    VALUES($1,$2,$3,101,repeat('a',64),1,'text/csv','example.csv',repeat('b',64),'test','pending',$5,clock_timestamp()+interval '15 minutes'),
		          ($1,$2,$4,102,repeat('c',64),1,'text/csv','example.csv',repeat('d',64),'test','expired',$5,clock_timestamp()-interval '1 minute')`,
			tenant, scope.BookID, pending, expired, scope.ActorID); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}()
	for _, mode := range []string{"without-approval", "different-request", "different-entry", "unrelated-original", "unrelated-upload", "revive-original"} {
		t.Run(mode, func(t *testing.T) {
			ctx := f.db.Context
			tx, err := f.owners.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(ctx, `SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin','https://ledger.example.com',true),
				set_config('vult.tenant_id',$2,true),set_config('vult.book_id',$3,true),set_config('vult.actor_id',$4,true),set_config('vult.connection_id',$5,true)`,
				access.Hash(session), tenant, scope.BookID, scope.ActorID, scope.ConnectionID); err != nil {
				t.Fatal(err)
			}
			if mode != "without-approval" {
				if _, err = tx.Exec(ctx, `INSERT INTO public.operation_decisions
					(tenant_id,book_id,proposal_id,revision,owner_id,owner_epoch,request_key,sha256,decision,reason)
					VALUES($1,$2,$3,1,access.require_owner($1,true),0,'review',$4,'approve','Example reviewed effects')`,
					tenant, scope.BookID, p.ID, p.SHA256); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, "SELECT set_config('vult.owner_posting',$1,true)", p.ID); err != nil {
					t.Fatal(err)
				}
				var allowed bool
				if err = tx.QueryRow(ctx, "SELECT public.ledger_allowed($1,$2,true)", tenant, scope.BookID).Scan(&allowed); err != nil || !allowed {
					t.Fatal("fixture did not authorize the exact proposal", err)
				}
			}
			if mode == "different-entry" {
				entry := ledger.Entry{ID: ledger.ID(), ReceiptID: ledger.ID(), ActorID: scope.ActorID,
					EconomicDate: p.Preview.EconomicDate, AccountingDate: p.Preview.AccountingDate, Description: "Changed after review"}
				err = tx.QueryRow(ctx, `INSERT INTO public.entries(tenant_id,book_id,id,receipt_id,actor_id,sequence,economic_date,accounting_date,description)
					VALUES($1,$2,$3,$4,$5,1,$6,$7,$8) RETURNING sequence,recorded_at`,
					tenant, scope.BookID, entry.ID, entry.ReceiptID, scope.ActorID, entry.EconomicDate, entry.AccountingDate, entry.Description).
					Scan(&entry.Sequence, &entry.RecordedAt)
				if err != nil {
					t.Fatal(err)
				}
				for i, planned := range p.Preview.Lines {
					line := ledger.Line{ID: ledger.ID(), AccountID: planned.AccountID, Side: planned.Side,
						AmountMinor: planned.AmountMinor, Memo: planned.Memo, Valuation: planned.Valuation}
					if _, err = tx.Exec(ctx, `INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor,memo,valuation)
						VALUES($1,$2,$3,$4,$5,$6,$7,$8::bigint,$9,$10)`,
						tenant, scope.BookID, line.ID, entry.ID, i+1, line.AccountID, line.Side, line.AmountMinor, line.Memo, line.Valuation); err != nil {
						t.Fatal(err)
					}
					entry.Lines = append(entry.Lines, line)
				}
				result, err := json.Marshal(entry)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, `INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
					VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, tenant, scope.BookID, entry.ReceiptID, scope.ActorID, p.OperationKey, p.Operation, []byte(p.Arguments), result); err != nil {
					t.Fatal("the unchanged request must reach the effect check", err)
				}
				_, err = tx.Exec(ctx, `INSERT INTO public.operation_consumptions(tenant_id,book_id,proposal_id,decision_revision,receipt_id,actor_id)
					VALUES($1,$2,$3,1,$4,access.require_owner($1,true))`, tenant, scope.BookID, p.ID, entry.ReceiptID)
				if err == nil {
					err = tx.Commit(ctx)
				}
				var denied *pgconn.PgError
				if !errors.As(err, &denied) || denied.Code != "23514" || denied.Message != "posted entry differs from review" {
					t.Fatal("owner role could substitute an entry under an unchanged request", err)
				}
				return
			}
			var documentCode string
			switch mode {
			case "unrelated-original":
				_, err = tx.Exec(ctx, `INSERT INTO public.documents
				    (tenant_id,book_id,id,sequence,sha256,size_bytes,media_type,original_name,storage_id,key_id,actor_id,expires_at)
				    VALUES($1,$2,$3,1,repeat('e',64),1,'text/csv','example.csv',repeat('f',64),'test',$4,clock_timestamp()+interval '15 minutes')`,
					tenant, scope.BookID, ledger.ID(), scope.ActorID)
				documentCode = "42501"
			case "unrelated-upload":
				_, err = tx.Exec(ctx, `INSERT INTO public.document_uploads
				    (tenant_id,book_id,id,document_id,storage_id,actor_id,connection_id,receipt_id,expires_at)
				    VALUES($1,$2,$3,$4,repeat('b',64),$5,$6,$7,clock_timestamp()+interval '15 minutes')`,
					tenant, scope.BookID, ledger.ID(), pending, scope.ActorID, scope.ConnectionID, ledger.ID())
				documentCode = "42501"
			case "revive-original":
				_, err = tx.Exec(ctx, "UPDATE public.documents SET state='pending',storage_id=repeat('e',64) WHERE id=$1", expired)
				documentCode = "23514"
			}
			if documentCode != "" {
				var denied *pgconn.PgError
				if !errors.As(err, &denied) || denied.Code != documentCode {
					t.Fatal("reviewed posting granted a separate document write", err)
				}
				return
			}
			_, err = tx.Exec(ctx, `INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
				VALUES($1,$2,$3,$4,'unrelated','post_journal',convert_to('{"request_key":"unrelated"}','UTF8'),convert_to('{}','UTF8'))`,
				tenant, scope.BookID, ledger.ID(), scope.ActorID)
			var denied *pgconn.PgError
			if !errors.As(err, &denied) || denied.Code != "42501" {
				t.Fatal("owner role could write a receipt outside the reviewed operation", err)
			}
		})
	}
	if countRows(t, f, "operation_decisions") != 0 || countRows(t, f, "entries") != 0 ||
		countRows(t, f, "documents") != 2 || countRows(t, f, "document_uploads") != 0 {
		t.Fatal("refused direct writes left recorded effects")
	}
}

func TestOperationOwnerFreshnessIsCheckedAfterDecisionLockWait(t *testing.T) {
	f, tenant, session, identity, scope, p := ownerProposal(t)
	q := decisionRequest(scope, p)
	var owner string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT id::text FROM access.identities WHERE issuer=$1 AND subject=$2", identity.Issuer, identity.Subject).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	key := "operation-decision:" + tenant + ":" + owner + ":" + q.RequestKey
	blocker, err := f.db.Admin.Acquire(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(f.db.Context, "SELECT pg_advisory_lock(hashtextextended($1,0))", key); err != nil {
		blocker.Release()
		t.Fatal(err)
	}
	var pid int
	if err = f.owners.QueryRow(f.db.Context, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		blocker.Exec(f.db.Context, "SELECT pg_advisory_unlock(hashtextextended($1,0))", key)
		blocker.Release()
		t.Fatal(err)
	}
	if _, err = f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '4 minutes 58 seconds' WHERE token_hash=$1", access.Hash(session)); err != nil {
		blocker.Exec(f.db.Context, "SELECT pg_advisory_unlock(hashtextextended($1,0))", key)
		blocker.Release()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var outcome error
	go func() { _, outcome = f.service.DecideOperation(f.db.Context, session, tenant, q, nil); close(done) }()
	released := false
	release := func() {
		if !released {
			_, err := blocker.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtextextended($1,0))", key)
			if err != nil {
				t.Error("synthetic decision lock cleanup failed")
			}
			released = true
		}
	}
	defer func() {
		release()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("synthetic decision request did not finish")
		}
		blocker.Release()
	}()
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err = f.db.Admin.QueryRow(f.db.Context, "SELECT cardinality(pg_blocking_pids($1))>0", pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-done:
			t.Fatal("decision did not wait for its serialization lock", outcome)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !blocked {
		t.Fatal("decision lock wait was not observed")
	}
	var initiallyFresh bool
	if err = f.db.Admin.QueryRow(f.db.Context, "SELECT authenticated_at>clock_timestamp()-interval '5 minutes' FROM access.sessions WHERE token_hash=$1", access.Hash(session)).Scan(&initiallyFresh); err != nil || !initiallyFresh {
		t.Fatal("fixture did not establish a fresh proof during the observed wait", err)
	}
	expired := false
	for time.Now().Before(deadline) {
		if err = f.db.Admin.QueryRow(f.db.Context, "SELECT authenticated_at<=clock_timestamp()-interval '5 minutes' FROM access.sessions WHERE token_hash=$1", access.Hash(session)).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !expired {
		t.Fatal("synthetic proof did not age past its boundary")
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("decision did not resume")
	}
	if !errors.Is(outcome, ledger.ErrFreshOwnerProof) {
		t.Fatal("aged proof did not produce the concrete reauthentication response", outcome)
	}
}

func TestOperationHelpersRequireAuthorityBeyondSelectedContext(t *testing.T) {
	f := fresh(t)
	a, sa, ia := f.owner(t, "first")
	b, sb, ib := f.owner(t, "second")
	ba, bb := f.book(t, a, sa), f.book(t, b, sb)
	connection := func(tenant, session, book string) ledger.Connection {
		t.Helper()
		var c ledger.Connection
		if err := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
			var err error
			c, err = owner.CreateConnection(f.db.Context, book, "Example writer", "write")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	ca, cb := connection(a, sa, ba.ID), connection(b, sb, bb.ID)
	for _, tc := range []struct {
		name, tenant, book string
		identity           access.Identity
		executor           ledger.Connection
		allowed            bool
	}{
		{"actual-authority", a, ba.ID, ia, ca, true},
		{"forged-context", b, bb.ID, ib, cb, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var owner string
			var epoch int64
			if err := f.db.Admin.QueryRow(f.db.Context, "SELECT id::text,epoch FROM access.identities WHERE issuer=$1 AND subject=$2", tc.identity.Issuer, tc.identity.Subject).Scan(&owner, &epoch); err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.Runtime.Begin(f.db.Context)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.db.Context)
			if _, err = tx.Exec(f.db.Context, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)", tc.tenant, tc.book, ca.ActorID, ca.ID); err != nil {
				t.Fatal(err)
			}
			var ownerAllowed, executorActive, held bool
			if err = tx.QueryRow(f.db.Context, "SELECT access.operation_owner_allowed($1,$2,$3,$4),access.operation_executor_active($1,$2,$5,$6),access.hold_operation_owner($1,$2,$3,$4)", tc.tenant, tc.book, owner, epoch, tc.executor.ID, tc.executor.ActorID).Scan(&ownerAllowed, &executorActive, &held); err != nil {
				t.Fatal(err)
			}
			if ownerAllowed != tc.allowed || executorActive != tc.allowed || held != tc.allowed {
				t.Fatal("selected context substituted for Book authority", ownerAllowed, executorActive, held)
			}
		})
	}
}

func TestOperationOwnerPostedHistorySurvivesRestore(t *testing.T) {
	f, tenant, session, _, scope, p := ownerProposal(t)
	out, err := f.service.DecideOperation(f.db.Context, session, tenant, decisionRequest(scope, p), nil)
	if err != nil {
		t.Fatal(err)
	}
	posted := postedEntry(t, out)
	before, err := f.db.Owner.Snapshot(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	if err = target.Owner.Restore(target.Context, before); err != nil {
		t.Fatal("owner-posted history restore failed", err)
	}
	after, err := target.Owner.Snapshot(target.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"operation_proposals", "operation_decisions", "operation_consumptions", "entries", "requests"} {
		if !bytes.Equal(before.Tables[table], after.Tables[table]) {
			t.Fatal("restored history changed", table)
		}
	}
	reader, err := target.Owner.CreateConnection(target.Context, scope.BookID, "Example recovery reader", "read")
	if err != nil {
		t.Fatal(err)
	}
	var state ledger.ProposalState
	readScope := ledger.Scope{TenantID: reader.TenantID, BookID: reader.BookID, ActorID: reader.ActorID, ConnectionID: reader.ID, Access: reader.Access}
	if json.Unmarshal(target.Call(t, readScope, "get_operation_proposal", ledger.ProposalQuery{ProposalID: p.ID}), &state) != nil || state.Status != "committed" || state.Decision == nil || state.Decision.Reason != "Example accepted effects" || state.Outcome == nil || !bytes.Equal(state.Outcome.Result, out.Outcome.Result) {
		t.Fatal("restored record lost its decision or posted entry", posted.ID)
	}
}
