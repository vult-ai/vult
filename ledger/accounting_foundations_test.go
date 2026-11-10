package ledger_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// releasedBookSchema installs the six migrations and runtime grants of the
// previous release, so a Book can be recorded exactly as that release did.
func releasedBookSchema(ctx context.Context, pool *pgxpool.Pool, role string) error {
	if _, err := pool.Exec(ctx, "CREATE TABLE public.schema_receipts(filename text PRIMARY KEY,sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),applied_at timestamptz NOT NULL DEFAULT clock_timestamp()); REVOKE ALL ON public.schema_receipts FROM PUBLIC"); err != nil {
		return err
	}
	for _, name := range []string{"0001_ledger.sql", "0002_server_access.sql", "0003_documents.sql", "0004_recorded_book.sql", "0005_proposing_agents.sql", "0006_owner_recovery_codes.sql"} {
		data, err := os.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if _, err = pool.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", name, hex.EncodeToString(sum[:])); err != nil {
			return err
		}
	}
	r := pgx.Identifier{role}.Sanitize()
	statements := []string{
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC", "GRANT USAGE ON SCHEMA public TO " + r,
		"GRANT SELECT ON public.books,public.accounts,public.account_revisions,public.entries,public.lines,public.requests,public.closed_periods,public.connections,public.schema_receipts,public.book_revisions TO " + r,
		"GRANT INSERT ON public.accounts,public.account_revisions,public.entries,public.lines,public.requests TO " + r,
		"GRANT UPDATE(write_epoch) ON public.books TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_set_book_logo(uuid,uuid,text,text,text,bytea) TO " + r,
		"GRANT UPDATE(id) ON public.connections TO " + r,
		"GRANT SELECT,INSERT ON public.documents,public.document_uploads TO " + r,
		"GRANT UPDATE(state,storage_id,key_id,ready_at,expires_at) ON public.documents TO " + r,
		"GRANT UPDATE(used_at) ON public.documents TO " + r,
		"GRANT SELECT,INSERT ON public.source_citations TO " + r,
		"GRANT SELECT,INSERT ON public.evidence_links TO " + r,
		"GRANT SELECT ON public.search_words,public.search_totals TO " + r,
		"GRANT EXECUTE ON FUNCTION public.search_text(text),public.search_tokens(text) TO " + r,
		"GRANT SELECT,INSERT ON public.operation_proposals,public.operation_consumptions TO " + r,
		"GRANT SELECT ON public.operation_decisions TO " + r,
		"GRANT EXECUTE ON FUNCTION access.operation_owner_allowed(uuid,uuid,uuid,bigint),access.operation_executor_active(uuid,uuid,uuid,uuid),access.hold_operation_owner(uuid,uuid,uuid,bigint) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.operation_dependencies(uuid,uuid,text,jsonb,uuid),public.operation_dependencies_settled(jsonb) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.owner_posting_consumer(uuid,uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.document_use(uuid,uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_validate_balances(uuid,uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_native_position(uuid,uuid,uuid,text,bigint,bigint) TO " + r,
		"GRANT USAGE ON SCHEMA access TO " + r,
		"GRANT EXECUTE ON FUNCTION access.owner_allowed(uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.owner_write_allowed(uuid),access.owner_document_allowed(uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.delegation_allowed(uuid,boolean),access.require_agent(text,text,uuid,uuid,uuid,uuid,text) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_context(uuid,uuid),public.ledger_allowed(uuid,uuid,boolean),public.ledger_lock(uuid,uuid),public.ledger_import_owner() TO " + r,
	}
	_, err := pool.Exec(ctx, strings.Join(statements, ";\n"))
	return err
}

// write runs one transaction as the selected Book's connection, the way the
// store does. Pass the admin pool to act as a trusted operator instead.
func write(db *testutil.Database, pool *pgxpool.Pool, s ledger.Scope, apply func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
 set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID); err != nil {
		return err
	}
	if err = apply(tx); err != nil {
		return err
	}
	return tx.Commit(db.Context)
}

func refused(t *testing.T, err error, message string) {
	t.Helper()
	var pg *pgconn.PgError
	if err == nil {
		t.Fatalf("expected refusal %q, got success", message)
	}
	var upgrade *ledger.UpgradeRefusal
	if errors.As(err, &upgrade) && strings.Contains(upgrade.Message, message) {
		return
	}
	if !errors.As(err, &pg) || !strings.Contains(pg.Message, message) {
		t.Fatalf("expected refusal %q, got %v", message, err)
	}
}

func succeeded(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// receipt records a request receipt for a decision and returns its ID.
func receipt(db *testutil.Database, tx pgx.Tx, s ledger.Scope, operation, key string) (string, error) {
	id := ledger.ID()
	request, _ := json.Marshal(map[string]string{"request_key": key})
	_, err := tx.Exec(db.Context, `INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 VALUES($1,$2,$3,$4,$5,$6,$7,'{}')`, s.TenantID, s.BookID, id, s.ActorID, key, operation, request)
	return id, err
}

// reviseAccount records an account revision that sets its parent and status,
// with the receipt the revision trigger checks: a retirement or reactivation
// when the status changes, otherwise an ordinary revision.
func reviseAccount(db *testutil.Database, tx pgx.Tx, s ledger.Scope, account string, parentID *string, status string, transferID *string) error {
	key := "revise-" + ledger.ID()
	parent, transfer := "", ""
	if parentID != nil {
		parent = *parentID
	}
	if transferID != nil {
		transfer = *transferID
	}
	id := ledger.ID()
	_, err := tx.Exec(db.Context, `WITH a AS (SELECT * FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3),
 op AS (SELECT CASE WHEN a.status='active' AND $8='retired' THEN 'retire_account' WHEN a.status='retired' AND $8='active' THEN 'reactivate_account'
  ELSE 'revise_account' END AS name FROM a),
 saved AS (SELECT jsonb_build_object('class',a.class,'id',a.id,'revision',a.revision+1,'code',a.code,'name',a.name,'currency',a.currency,
   'scale',a.native_scale,'currency_definition',a.currency_definition,'role',a.role,'currency_revision',a.currency_revision,
   'native_origin_revision',a.native_origin_revision,'role_revision',a.role_revision,'parent_account_id',nullif($7::text,'')::uuid,
   'status',CASE WHEN $8='retired' THEN 'retired' END) AS account FROM a)
 INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 SELECT a.tenant_id,a.book_id,$4,$5,$6,op.name,
  convert_to(CASE WHEN op.name='revise_account' THEN jsonb_build_object('request_key',$6::text,'account_id',a.id,'expected_revision',a.revision,
   'code',a.code,'name',a.name,'parent_account_id',$7::text) ELSE jsonb_build_object('request_key',$6::text,'account_id',a.id,'expected_revision',a.revision) END::text,'UTF8'),
  convert_to(CASE WHEN op.name='retire_account' THEN jsonb_build_object('account',saved.account,'transfer',
   CASE WHEN $9='' THEN NULL ELSE jsonb_build_object('id',$9::text) END) ELSE saved.account END::text,'UTF8')
 FROM a,op,saved`, s.TenantID, s.BookID, account, id, s.ActorID, key, parent, status, transfer)
	if err != nil {
		return err
	}
	_, err = tx.Exec(db.Context, `INSERT INTO public.account_revisions(tenant_id,book_id,account_id,revision,code,name,actor_id,currency,native_scale,
 currency_definition,role,currency_revision,native_origin_revision,role_revision,receipt_id,parent_account_id,status,transfer_entry_id)
 SELECT a.tenant_id,a.book_id,a.id,a.revision+1,a.code,a.name,$4,a.currency,a.native_scale,a.currency_definition,a.role,a.currency_revision,
  a.native_origin_revision,a.role_revision,$5,nullif($6::text,'')::uuid,$7::text,nullif($8::text,'')::uuid
 FROM public.accounts a WHERE a.tenant_id=$1 AND a.book_id=$2 AND a.id=$3`, s.TenantID, s.BookID, account, s.ActorID, id, parent, status, transfer)
	return err
}

type rawLine struct {
	account, side string
	amount        int64
}

// postInTransaction posts an ordinary journal entry inside an open transaction,
// with the receipt the journal triggers compare, and returns its line IDs.
func postInTransaction(db *testutil.Database, tx pgx.Tx, s ledger.Scope, date string, lines ...rawLine) (string, []string, error) {
	return postWithSources(db, tx, s, date, nil, lines...)
}

// postWithSources also records each source citation with the entry.
func postWithSources(db *testutil.Database, tx pgx.Tx, s ledger.Scope, date string, sources []ledger.SourceInput, lines ...rawLine) (string, []string, error) {
	receiptID, entry := ledger.ID(), ledger.Entry{ID: ledger.ID(), ActorID: s.ActorID, EconomicDate: date, AccountingDate: date, Description: "Example transfer", Lines: []ledger.Line{}}
	entry.ReceiptID = receiptID
	if err := tx.QueryRow(db.Context, `INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description)
 VALUES($1,$2,$3,1,$4,$5,$6,$6,$7) RETURNING sequence,recorded_at`, s.TenantID, s.BookID, entry.ID, receiptID, s.ActorID, date, entry.Description).Scan(&entry.Sequence, &entry.RecordedAt); err != nil {
		return "", nil, err
	}
	ids := []string{}
	for i, l := range lines {
		line := ledger.Line{ID: ledger.ID(), AccountID: l.account, Side: l.side, AmountMinor: strconv.FormatInt(l.amount, 10)}
		// Use the book-value basis the store records for a Book-currency line.
		var valuation []byte
		if err := tx.QueryRow(db.Context, `INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor,valuation)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8::bigint,jsonb_build_object('basis','manual','policy','supplied-book-value-v1','input',
  jsonb_build_object('currency',b.currency,'scale',b.scale,'amount_minor',$8::bigint::text,'unit_definition','book')) FROM public.books b WHERE b.tenant_id=$1 AND b.id=$2
 RETURNING valuation::text`, s.TenantID, s.BookID, line.ID, entry.ID, i+1, l.account, l.side, l.amount).Scan(&valuation); err != nil {
			return "", nil, err
		}
		if json.Unmarshal(valuation, &line.Valuation) != nil {
			return "", nil, errors.New("invalid valuation")
		}
		entry.Lines = append(entry.Lines, line)
		ids = append(ids, line.ID)
	}
	result, _ := json.Marshal(entry)
	for i, source := range sources {
		location, _ := json.Marshal(source.Location)
		citation := ledger.ID()
		if _, err := tx.Exec(db.Context, `INSERT INTO public.source_citations(tenant_id,book_id,id,source_id,revision,sequence,entry_id,position,document_id,location,actor_id,receipt_id)
 VALUES($1,$2,$3,$3,1,1,$4,$5,$6,$7::jsonb,$8,$9)`, s.TenantID, s.BookID, citation, entry.ID, i+1, source.DocumentID, string(location), s.ActorID, receiptID); err != nil {
			return "", nil, err
		}
	}
	posted := ledger.PostJournal{RequestKey: "post-" + entry.ID, EconomicDate: date, AccountingDate: date, Description: entry.Description, Sources: sources}
	for _, l := range entry.Lines {
		posted.Lines = append(posted.Lines, ledger.PostLine{AccountID: l.AccountID, Side: l.Side, AmountMinor: l.AmountMinor})
	}
	request, _ := json.Marshal(posted)
	_, err := tx.Exec(db.Context, `INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 VALUES($1,$2,$3,$4,$5,'post_journal',$6,$7)`, s.TenantID, s.BookID, receiptID, s.ActorID, "post-"+entry.ID, request, result)
	return entry.ID, ids, err
}

// rawJournal posts one 100-unit entry through the database guards alone.
func rawJournal(db *testutil.Database, s ledger.Scope, date, debit, credit string) error {
	return write(db, db.Runtime, s, func(tx pgx.Tx) error {
		_, _, err := postInTransaction(db, tx, s, date, rawLine{debit, "debit", 100}, rawLine{credit, "credit", 100})
		return err
	})
}

func roleAccount(t *testing.T, db *testutil.Database, s ledger.Scope, code, class, role string) ledger.Account {
	t.Helper()
	var a ledger.Account
	if json.Unmarshal(db.Call(t, s, "create_account", ledger.CreateAccount{RequestKey: "create-" + code, Class: class, Code: code, Name: "Example " + code, Role: role}), &a) != nil {
		t.Fatal("invalid account result")
	}
	return a
}

func journal(t *testing.T, db *testutil.Database, s ledger.Scope, key, date, debit, credit, amount string) ledger.Entry {
	t.Helper()
	var e ledger.Entry
	q := ledger.PostJournal{RequestKey: key, EconomicDate: date, AccountingDate: date, Description: "Example " + key,
		Lines: []ledger.PostLine{{AccountID: debit, Side: "debit", AmountMinor: amount}, {AccountID: credit, Side: "credit", AmountMinor: amount}}}
	if json.Unmarshal(db.Call(t, s, "post_journal", q), &e) != nil {
		t.Fatal("invalid entry result")
	}
	return e
}

func tryJournal(db *testutil.Database, s ledger.Scope, key, date, debit, credit, amount string) error {
	q := ledger.PostJournal{RequestKey: key, EconomicDate: date, AccountingDate: date, Description: "Example " + key,
		Lines: []ledger.PostLine{{AccountID: debit, Side: "debit", AmountMinor: amount}, {AccountID: credit, Side: "credit", AmountMinor: amount}}}
	b, _ := json.Marshal(q)
	_, err := db.Store.Call(db.Context, s, "post_journal", b)
	return err
}

// frozenView reads a Book's recorded rows and balances with plain SQL, the
// same way before and after an upgrade. Columns the upgrade adds with their
// upgrade values are left out, so any other change shows.
func frozenView(t *testing.T, db *testutil.Database, book string) map[string]string {
	t.Helper()
	out := map[string]string{}
	added := "ARRAY['status','parent_account_id','transfer_entry_id','heading']"
	for _, table := range []string{"accounts", "account_revisions", "requests", "entries", "lines", "source_citations", "documents", "closed_periods"} {
		var rows []byte
		if err := db.Admin.QueryRow(db.Context, "SELECT coalesce(jsonb_agg(to_jsonb(t)-"+added+" ORDER BY (to_jsonb(t)-"+added+")::text),'[]')::text FROM public."+table+" t WHERE t.book_id=$1", book).Scan(&rows); err != nil {
			t.Fatal(table, err)
		}
		out[table] = string(rows)
	}
	var balances []byte
	if err := db.Admin.QueryRow(db.Context, `SELECT coalesce(jsonb_object_agg(account_id,balance),'{}')::text FROM (SELECT account_id::text,
 sum(CASE side WHEN 'debit' THEN amount_minor::numeric ELSE -amount_minor::numeric END)::text balance FROM public.lines WHERE book_id=$1 GROUP BY account_id) b`, book).Scan(&balances); err != nil {
		t.Fatal(err)
	}
	out["balances"] = string(balances)
	return out
}

// A Book recorded by the previous release keeps every row and balance, and
// reads the same through this release.
func TestFoundationsUpgradeKeepsAReleasedBookIdentical(t *testing.T) {
	db := testutil.NewWithInitializer(t, releasedBookSchema)
	vault, _ := restoreVault(t)
	db.Store.Documents = vault
	book, s := db.Book(t, "Example released Book")
	// Accounts and documents go through the store's own paths, which write
	// the released columns only; entries are written with plain SQL.
	bank := roleAccount(t, db, s, "1000", "asset", "cash")
	owed := roleAccount(t, db, s, "1200", "asset", "receivable")
	equity := db.Account(t, s, "3000", "equity")
	income := db.Account(t, s, "4000", "income")
	expense := db.Account(t, s, "5000", "expense")
	// The previous release accepted the earlier spelling of the cost-of-sales
	// role. Record the account with a role, then spell it as that release
	// stored it, in the account and in its receipt.
	goods := roleAccount(t, db, s, "5100", "expense", "cost_of_sales")
	succeeded(t, func() error {
		tx, err := db.Admin.Begin(db.Context)
		if err != nil {
			return err
		}
		defer tx.Rollback(db.Context)
		if _, err = tx.Exec(db.Context, "SET LOCAL session_replication_role = replica"); err != nil {
			return err
		}
		for _, q := range []string{
			"UPDATE public.account_revisions SET role='cost_of_goods_sold' WHERE account_id=$1",
			"UPDATE public.accounts SET role='cost_of_goods_sold' WHERE id=$1",
			`UPDATE public.requests SET
 request_bytes=convert_to(replace(convert_from(request_bytes,'UTF8'),'"cost_of_sales"','"cost_of_goods_sold"'),'UTF8'),
 result_bytes=convert_to(replace(convert_from(result_bytes,'UTF8'),'"cost_of_sales"','"cost_of_goods_sold"'),'UTF8')
 WHERE id=(SELECT receipt_id FROM public.account_revisions WHERE account_id=$1 AND revision=1)`,
		} {
			if _, err = tx.Exec(db.Context, q, goods.ID); err != nil {
				return err
			}
		}
		return tx.Commit(db.Context)
	}())
	original := readyOriginal(t, db, s, "receipt")
	for _, posting := range []struct {
		date    string
		sources []ledger.SourceInput
		lines   []rawLine
	}{
		{"2026-01-02", nil, []rawLine{{bank.ID, "debit", 100000}, {equity.ID, "credit", 100000}}},
		{"2026-01-20", nil, []rawLine{{owed.ID, "debit", 25000}, {income.ID, "credit", 25000}}},
		{"2026-02-03", []ledger.SourceInput{{DocumentID: original.ID, Location: ledger.Location{Page: locationNumber(1)}}}, []rawLine{{expense.ID, "debit", 4200}, {bank.ID, "credit", 4200}}},
		{"2026-02-10", nil, []rawLine{{goods.ID, "debit", 1000}, {bank.ID, "credit", 1000}}},
	} {
		succeeded(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
			_, _, err := postWithSources(db, tx, s, posting.date, posting.sources, posting.lines...)
			return err
		}))
	}
	if err := db.ClosePeriod(db.Context, book.ID, "2026-01-01", "2026-01-31"); err != nil {
		t.Fatal(err)
	}
	var closedID string
	var closedAt time.Time
	if err := db.Admin.QueryRow(db.Context, "SELECT id::text,closed_at FROM public.closed_periods").Scan(&closedID, &closedAt); err != nil {
		t.Fatal(err)
	}
	before := frozenView(t, db, book.ID)

	if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
		t.Fatal(err)
	}
	after := frozenView(t, db, book.ID)
	for table, rows := range before {
		if rows != after[table] {
			t.Fatal("upgrade changed recorded rows or balances", table)
		}
	}
	// This release reads the same accounts, entries, balances and closes.
	export, err := db.Owner.Export(db.Context, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var receipts []json.RawMessage
	succeeded(t, db.Admin.QueryRow(db.Context, `SELECT jsonb_agg(convert_from(r.result_bytes,'UTF8')::jsonb ORDER BY e.sequence) FROM public.entries e
 JOIN public.requests r ON r.id=e.receipt_id WHERE e.book_id=$1`, book.ID).Scan(&receipts))
	var exported []json.RawMessage
	for _, entry := range export.Entries {
		exported = append(exported, value(entry))
	}
	var want, got []any
	_ = json.Unmarshal(value(receipts), &want)
	_ = json.Unmarshal(value(exported), &got)
	if !bytes.Equal(value(want), value(got)) {
		t.Fatal("the export no longer matches the recorded journal receipts")
	}
	var recorded []map[string]any
	_ = json.Unmarshal([]byte(before["accounts"]), &recorded)
	if len(export.Accounts) != len(recorded) {
		t.Fatal("the export lost accounts")
	}
	for _, a := range export.Accounts {
		if a.Status != "" || a.ParentAccountID != "" {
			t.Fatal("an upgraded account must read active with no parent", a)
		}
	}
	var balances map[string]string
	_ = json.Unmarshal([]byte(before["balances"]), &balances)
	var trial ledger.Trial
	if json.Unmarshal(db.Call(t, s, "get_trial_balance", map[string]string{}), &trial) != nil {
		t.Fatal("invalid trial balance")
	}
	for _, b := range trial.Balances {
		if balances[b.AccountID] != b.DebitPositiveMinor && !(balances[b.AccountID] == "" && b.DebitPositiveMinor == "0") {
			t.Fatal("trial balance differs from the recorded lines", b)
		}
	}
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []reporting.Kind{reporting.BalanceSheet, reporting.ProfitLoss, reporting.TrialBalance, reporting.GeneralLedger, reporting.Journal, reporting.ChangesInEquity} {
		req := ledger.FinancialReportRequest{BookID: book.ID, Report: kind, From: "2026-01-01", To: "2026-12-31"}
		if kind == reporting.BalanceSheet || kind == reporting.TrialBalance {
			req = ledger.FinancialReportRequest{BookID: book.ID, Report: kind, AsOf: "2026-12-31"}
		}
		report, err := owner.FinancialReport(db.Context, req, false)
		if err != nil || report.Scope.HierarchyBasis != "flat_recorded_accounts" {
			t.Fatal("an upgraded Book without parents reports as before", kind, err)
		}
		// The earlier cost-of-goods role now shows Gross profit; the result
		// is unchanged.
		if kind == reporting.ProfitLoss && (report.Reconciliation["gross_profit"] != "24000" || report.Reconciliation["result"] != "19800") {
			t.Fatal("an upgraded cost-of-goods account shows gross profit with the same result", report.Reconciliation)
		}
	}
	var periods struct {
		Periods []ledger.ClosedPeriod `json:"periods"`
	}
	if json.Unmarshal(db.Call(t, s, "list_closed_periods", map[string]string{}), &periods) != nil || len(periods.Periods) != 1 || periods.Periods[0].ID != closedID {
		t.Fatal("the original close must still be listed")
	}
	var accounts, active, parents int
	if err := db.Admin.QueryRow(db.Context, `SELECT count(*),count(*) FILTER(WHERE status='active'),count(parent_account_id) FROM public.accounts`).Scan(&accounts, &active, &parents); err != nil || accounts != 6 || active != 6 || parents != 0 {
		t.Fatal("existing accounts must become active with no parent", err)
	}
	var revisions, activeRevisions int
	if err := db.Admin.QueryRow(db.Context, `SELECT count(*),count(*) FILTER(WHERE status='active' AND parent_account_id IS NULL) FROM public.account_revisions`).Scan(&revisions, &activeRevisions); err != nil || revisions != activeRevisions {
		t.Fatal("existing revisions must read active with no parent", err)
	}
	var periodID, event, origin string
	var actor *string
	var recordedAt time.Time
	if err := db.Admin.QueryRow(db.Context, `SELECT p.id::text,e.event,e.origin,e.actor_id::text,e.recorded_at FROM public.accounting_periods p
 JOIN public.accounting_period_events e ON e.period_id=p.id WHERE p.start_date='2026-01-01' AND p.end_date='2026-01-31'`).Scan(&periodID, &event, &origin, &actor, &recordedAt); err != nil {
		t.Fatal("the existing close did not become a period", err)
	}
	if periodID != closedID || event != "closed" || origin != "closed_period" || actor != nil || !recordedAt.Equal(closedAt) {
		t.Fatal("the kept close must keep its identity and time and invent no actor")
	}
	var links, statements int
	if err := db.Admin.QueryRow(db.Context, "SELECT (SELECT count(*) FROM public.entry_payees),(SELECT count(*) FROM public.statements)").Scan(&links, &statements); err != nil || links != 0 || statements != 0 {
		t.Fatal("upgrade invented payees or statements", err)
	}
	refused(t, rawJournal(db, s, "2026-01-15", expense.ID, bank.ID), "accounting period is closed")
	if err := tryJournal(db, s, "late", "2026-01-15", expense.ID, bank.ID, "100"); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("the store must refuse a posting into the kept close", err)
	}
	journal(t, db, s, "after", "2026-02-15", expense.ID, bank.ID, "100")

	// The upgraded Book round-trips through a portable archive, including its kept close.
	var archive bytes.Buffer
	if err := db.Owner.WriteArchive(db.Context, vault, &archive, ""); err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	targetVault, _ := restoreVault(t)
	if err := target.Owner.RestoreArchive(target.Context, targetVault, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("upgraded archive restore failed", err)
	}
	var restored bytes.Buffer
	if err := target.Owner.WriteArchive(target.Context, targetVault, &restored, ""); err != nil {
		t.Fatal(err)
	}
	first, second := archiveManifestOf(t, archive.Bytes()), archiveManifestOf(t, restored.Bytes())
	for _, table := range []string{"accounts", "account_revisions", "entries", "lines", "closed_periods", "accounting_periods", "accounting_period_events"} {
		if !bytes.Equal(first.Snapshot.Tables[table], second.Snapshot.Tables[table]) {
			t.Fatal("archive restore changed retained rows", table)
		}
	}
}
