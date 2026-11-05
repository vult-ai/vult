package server

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/ledger"
)

func TestRemoteSingleBalanceWorksBeyondWholeBookListLimit(t *testing.T) {
	f := documentServer(t)
	ctx := f.db.Context
	tx, err := f.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true)", f.tenant, f.book.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO public.accounts(tenant_id,book_id,id,class)
 SELECT $1,$2,gen_random_uuid(),'asset' FROM generate_series(1,2001)`, f.tenant, f.book.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO public.account_revisions(tenant_id,book_id,account_id,revision,code,name,actor_id)
 SELECT tenant_id,book_id,id,1,'example-'||(row_number() OVER(ORDER BY id))::text,'Example account',gen_random_uuid()
 FROM public.accounts WHERE book_id=$1 AND revision=0`, f.book.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.db.Admin.QueryRow(ctx, "SELECT id::text FROM public.accounts WHERE book_id=$1 ORDER BY id LIMIT 1", f.book.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	client := f.client(t, f.token(t, "read"))
	var result ledger.Balance
	documentTool(t, ctx, client, "get_balance", ledger.AccountQuery{AccountID: id}, &result)
	if result.AccountID != id || result.DebitPositiveMinor != "0" {
		t.Fatal("single balance was constrained by unrelated accounts")
	}
	for _, method := range []string{"list_accounts", "get_trial_balance"} {
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: method, Arguments: struct{}{}})
		if err != nil || !out.IsError {
			t.Fatal("whole-Book reply limit was lost", method, err)
		}
	}
}
