package ledger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func ptr(s string) *string { return &s }

func TestUpgradeRefusesRolesOutsideTheVocabulary(t *testing.T) {
	for _, tc := range []struct{ name, class, role, message string }{
		{"unknown role", "asset", "savings", "outside the supported vocabulary"},
		{"wrong class", "liability", "cash", "wrong class"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewWithInitializer(t, releasedBookSchema)
			_, s := db.Book(t, "Example Book")
			account := db.Account(t, s, "1000", tc.class)
			// Store the value directly, as an older writer could have.
			tx, err := db.Admin.Begin(db.Context)
			succeeded(t, err)
			_, err = tx.Exec(db.Context, "SET LOCAL session_replication_role = replica")
			succeeded(t, err)
			_, err = tx.Exec(db.Context, "UPDATE public.account_revisions SET role=$2 WHERE account_id=$1", account.ID, tc.role)
			succeeded(t, err)
			_, err = tx.Exec(db.Context, "UPDATE public.accounts SET role=$2 WHERE id=$1", account.ID, tc.role)
			succeeded(t, err)
			succeeded(t, tx.Commit(db.Context))
			err = ledger.Upgrade(db.Context, db.Admin, db.Role)
			refused(t, err, tc.message)
			// The refusal reaches the operator with what to change, not as a
			// generic failure.
			var refusal *ledger.UpgradeRefusal
			if !errors.As(err, &refusal) || !strings.Contains(refusal.Error(), tc.message) || !strings.Contains(refusal.Error(), "nothing was changed") {
				t.Fatal("a deliberate upgrade refusal must keep its explanation", err)
			}
			var receipts int
			if db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.schema_receipts").Scan(&receipts) != nil || receipts != 6 {
				t.Fatal("a refused upgrade must leave the schema unchanged")
			}
		})
	}
}

func TestChartOfAccountsGuards(t *testing.T) {
	db := testutil.New(t)
	_, s := db.Book(t, "Example Book")
	_, other := db.Book(t, "Example other Book")
	bank := roleAccount(t, db, s, "1000", "asset", "cash")
	savings := roleAccount(t, db, s, "1010", "asset", "cash")
	assets := db.Account(t, s, "1", "asset")
	current := db.Account(t, s, "10", "asset")
	levels := []ledger.Account{db.Account(t, s, "100", "asset"), db.Account(t, s, "1001", "asset")}
	equity := db.Account(t, s, "3000", "equity")
	foreign := db.Account(t, other, "1000", "asset")
	revise := func(account string, parent *string, status string) error {
		return write(db, db.Runtime, s, func(tx pgx.Tx) error { return reviseAccount(db, tx, s, account, parent, status, nil) })
	}

	t.Run("parent rules", func(t *testing.T) {
		refused(t, revise(bank.ID, &equity.ID, "active"), "same class")
		refused(t, revise(bank.ID, &foreign.ID, "active"), "same Book")
		refused(t, revise(bank.ID, &bank.ID, "active"), "cycle")
		succeeded(t, revise(current.ID, &assets.ID, "active"))
		succeeded(t, revise(levels[0].ID, &current.ID, "active"))
		succeeded(t, revise(levels[1].ID, &levels[0].ID, "active"))
		refused(t, revise(assets.ID, &levels[1].ID, "active"), "cycle")
		refused(t, revise(bank.ID, &levels[1].ID, "active"), "four levels")
		// Moving a subtree checks the levels beneath it too.
		refused(t, revise(assets.ID, &savings.ID, "active"), "four levels")
		succeeded(t, revise(bank.ID, &levels[0].ID, "active"))
		var parent *string
		if db.Admin.QueryRow(db.Context, "SELECT parent_account_id::text FROM public.accounts WHERE id=$1", bank.ID).Scan(&parent) != nil || parent == nil || *parent != levels[0].ID {
			t.Fatal("parent was not recorded")
		}
		// A revision that doesn't name the parent keeps it.
		b, _ := json.Marshal(ledger.ReviseAccount{RequestKey: "rename", AccountID: bank.ID, ExpectedRevision: 2, Code: "1000", Name: "Example renamed"})
		if _, err := db.Store.Call(db.Context, s, "revise_account", b); err != nil {
			t.Fatal(err)
		}
		if db.Admin.QueryRow(db.Context, "SELECT parent_account_id::text FROM public.accounts WHERE id=$1", bank.ID).Scan(&parent) != nil || parent == nil || *parent != levels[0].ID {
			t.Fatal("a rename dropped the parent")
		}
	})

	t.Run("retirement", func(t *testing.T) {
		journal(t, db, s, "fund", "2026-01-02", savings.ID, equity.ID, "4000")
		err := revise(savings.ID, nil, "retired")
		refused(t, err, "still holds a balance")
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Detail != "book currency 4000, account currency 0" {
			t.Fatal("the refusal must state the remaining balance", err)
		}
		// Retire with the transfer of the exact balance in one request.
		refused(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
			entry, _, err := postInTransaction(db, tx, s, "2026-01-31", rawLine{bank.ID, "debit", 4000}, rawLine{equity.ID, "credit", 4000})
			if err != nil {
				return err
			}
			return reviseAccount(db, tx, s, savings.ID, nil, "retired", &entry)
		}), "still holds a balance")
		refused(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
			entry, _, err := postInTransaction(db, tx, s, "2026-01-31", rawLine{bank.ID, "debit", 3000}, rawLine{savings.ID, "credit", 3000})
			if err != nil {
				return err
			}
			return reviseAccount(db, tx, s, savings.ID, nil, "retired", &entry)
		}), "still holds a balance")
		succeeded(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
			entry, _, err := postInTransaction(db, tx, s, "2026-01-31", rawLine{bank.ID, "debit", 4000}, rawLine{savings.ID, "credit", 4000})
			if err != nil {
				return err
			}
			return reviseAccount(db, tx, s, savings.ID, nil, "retired", &entry)
		}))
		refused(t, rawJournal(db, s, "2026-02-01", savings.ID, equity.ID), "account is retired")
		refused(t, revise(levels[1].ID, &savings.ID, "active"), "retired account cannot gain children")
		// Reactivation is a new revision; then the account posts again.
		succeeded(t, revise(savings.ID, nil, "active"))
		journal(t, db, s, "reactivated", "2026-02-01", savings.ID, equity.ID, "100")
		// A transfer belongs only to a retirement.
		refused(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
			entry, _, err := postInTransaction(db, tx, s, "2026-02-02", rawLine{bank.ID, "debit", 1}, rawLine{equity.ID, "credit", 1})
			if err != nil {
				return err
			}
			return reviseAccount(db, tx, s, bank.ID, nil, "active", &entry)
		}), "recorded only with a retirement")
		// A zero-balance account retires without a transfer.
		empty := db.Account(t, s, "1020", "asset")
		succeeded(t, revise(empty.ID, nil, "retired"))
	})

	t.Run("role vocabulary", func(t *testing.T) {
		for _, tc := range []struct{ class, role string }{{"asset", "card"}, {"liability", "cash"}, {"asset", "savings"}, {"equity", "loan"}} {
			b, _ := json.Marshal(ledger.CreateAccount{RequestKey: "role-" + tc.class + tc.role, Class: tc.class, Code: "R" + tc.role, Name: "Example role", Role: tc.role})
			if _, err := db.Store.Call(db.Context, s, "create_account", b); !errors.Is(err, ledger.ErrInvalid) {
				t.Fatal("role outside the vocabulary or class was accepted", tc, err)
			}
		}
		for _, tc := range []struct{ class, role string }{{"liability", "card"}, {"liability", "loan"}, {"liability", "payable"}, {"asset", "fixed_asset"}, {"asset", "investment"}} {
			roleAccount(t, db, s, "V"+tc.role, tc.class, tc.role)
		}
		refused(t, write(db, db.Admin, s, func(tx pgx.Tx) error {
			_, err := tx.Exec(db.Context, "INSERT INTO public.account_revisions(tenant_id,book_id,account_id,revision,code,name,actor_id,role,role_revision) SELECT tenant_id,book_id,id,revision+1,code,name,$2,'savings',revision+1 FROM public.accounts WHERE id=$1", equity.ID, s.ActorID)
			return err
		}), "does not fit the account class")
	})
}

func TestPayeeGuards(t *testing.T) {
	db := testutil.New(t)
	_, s := db.Book(t, "Example Book")
	_, other := db.Book(t, "Example other Book")
	bank := roleAccount(t, db, s, "1000", "asset", "cash")
	expense := db.Account(t, s, "5000", "expense")
	entry := journal(t, db, s, "coffee", "2026-01-05", expense.ID, bank.ID, "450")
	createPayee := func(scope ledger.Scope, name string, withRevision bool) (string, error) {
		id := ledger.ID()
		return id, write(db, db.Runtime, scope, func(tx pgx.Tx) error {
			r, err := receipt(db, tx, scope, "create_payee", "payee-"+id)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(db.Context, "INSERT INTO public.payees(tenant_id,book_id,id,actor_id,receipt_id) VALUES($1,$2,$3,$4,$5)", scope.TenantID, scope.BookID, id, scope.ActorID, r); err != nil || !withRevision {
				return err
			}
			_, err = tx.Exec(db.Context, `INSERT INTO public.payee_revisions(tenant_id,book_id,payee_id,revision,display_name,kind,status,actor_id,receipt_id)
 VALUES($1,$2,$3,1,$4,'organization','active',$5,$6)`, scope.TenantID, scope.BookID, id, name, scope.ActorID, r)
			return err
		})
	}
	link := func(revision int, payee *string, counterpart, reason string) error {
		return write(db, db.Runtime, s, func(tx pgx.Tx) error {
			r, err := receipt(db, tx, s, "set_entry_payee", "link-"+ledger.ID())
			if err != nil {
				return err
			}
			_, err = tx.Exec(db.Context, `INSERT INTO public.entry_payees(tenant_id,book_id,entry_id,revision,counterpart,payee_id,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,$6::uuid,$7,$8,$9)`, s.TenantID, s.BookID, entry.ID, revision, counterpart, payee, s.ActorID, reason, r)
			return err
		})
	}
	_, err := createPayee(s, "Example Café", false)
	refused(t, err, "payee revision required")
	first, err := createPayee(s, "Example Café", true)
	succeeded(t, err)
	second, err := createPayee(s, "Example Café", true)
	succeeded(t, err)
	if first == second {
		t.Fatal("same-name payees must stay separate")
	}
	foreign, err := createPayee(other, "Example Café", true)
	succeeded(t, err)

	var links int
	if db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.entry_payees WHERE entry_id=$1", entry.ID).Scan(&links); links != 0 {
		t.Fatal("an unlinked entry must have no payee row")
	}
	refused(t, link(1, &foreign, "payee", ""), "payee is retired or unavailable")
	refused(t, link(1, nil, "payee", ""), "entry_payees_check")
	refused(t, link(2, &first, "payee", "skip"), "stale entry payee revision")
	succeeded(t, link(1, nil, "unknown", ""))
	refused(t, link(2, &first, "payee", ""), "entry_payees_check1")
	succeeded(t, link(2, &first, "payee", "Receipt names the café"))
	refused(t, write(db, db.Admin, s, func(tx pgx.Tx) error {
		_, err := tx.Exec(db.Context, "UPDATE public.entry_payees SET counterpart='unknown' WHERE entry_id=$1", entry.ID)
		return err
	}), "record is immutable")

	// A retired payee takes no new links; renaming is a new revision.
	succeeded(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
		r, err := receipt(db, tx, s, "revise_payee", "retire-"+second)
		if err != nil {
			return err
		}
		_, err = tx.Exec(db.Context, `INSERT INTO public.payee_revisions(tenant_id,book_id,payee_id,revision,display_name,kind,status,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,2,'Example Café','organization','retired',$4,'Closed',$5)`, s.TenantID, s.BookID, second, s.ActorID, r)
		return err
	}))
	refused(t, link(3, &second, "payee", "Other café"), "payee is retired")
	// A receipt for another operation can't record a payee decision.
	refused(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
		r, err := receipt(db, tx, s, "create_payee", "wrong-"+entry.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(db.Context, `INSERT INTO public.entry_payees(tenant_id,book_id,entry_id,revision,counterpart,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,3,'unknown',$4,'Unclear',$5)`, s.TenantID, s.BookID, entry.ID, s.ActorID, r)
		return err
	}), "decision receipt differs")
}

type statementFixture struct {
	db                     *testutil.Database
	s                      ledger.Scope
	bank, card, equity     ledger.Account
	income, expense, other ledger.Account
	document               string
}

func (f statementFixture) importStatement(account ledger.Account, kind string, opening, closing int64, rows ...statementRow) (string, []string, error) {
	statement, ids := ledger.ID(), []string{}
	err := write(f.db, f.db.Runtime, f.s, func(tx pgx.Tx) error {
		r, err := receipt(f.db, tx, f.s, "import_statement", "statement-"+statement)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(f.db.Context, `INSERT INTO public.statements(tenant_id,book_id,id,account_id,kind,currency,scale,document_id,period_start,period_end,
 opening_balance_minor,closing_balance_minor,evidence_state,actor_id,receipt_id) VALUES($1,$2,$3,$4,$5,'USD',2,$6,'2026-03-01','2026-03-31',$7,$8,'complete',$9,$10)`,
			f.s.TenantID, f.s.BookID, statement, account.ID, kind, f.document, opening, closing, f.s.ActorID, r); err != nil {
			return err
		}
		for i, row := range rows {
			id := ledger.ID()
			if _, err = tx.Exec(f.db.Context, `INSERT INTO public.statement_rows(tenant_id,book_id,id,statement_id,row_number,location,occurred_on,amount_minor,description,lifecycle,raw_bytes)
 VALUES($1,$2,$3,$4,$5,jsonb_build_object('row',$5::integer),'2026-03-10',$6,$7,$8,convert_to($7,'UTF8'))`, f.s.TenantID, f.s.BookID, id, statement, i+1, row.amount, row.description, row.lifecycle); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	return statement, ids, err
}

type statementRow struct {
	amount      int64
	description string
	lifecycle   string
}

type matchPart struct {
	row, line string
	amount    int64
}

func (f statementFixture) match(account string, parts ...matchPart) (string, error) {
	id := ledger.ID()
	return id, write(f.db, f.db.Runtime, f.s, func(tx pgx.Tx) error {
		r, err := receipt(f.db, tx, f.s, "match_statement", "match-"+id)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(f.db.Context, "INSERT INTO public.statement_matches(tenant_id,book_id,id,account_id,actor_id,receipt_id) VALUES($1,$2,$3,$4,$5,$6)", f.s.TenantID, f.s.BookID, id, account, f.s.ActorID, r); err != nil {
			return err
		}
		for i, p := range parts {
			var row, line *string
			if p.row != "" {
				row = &p.row
			} else {
				line = &p.line
			}
			if _, err = tx.Exec(f.db.Context, "INSERT INTO public.statement_match_parts(tenant_id,book_id,match_id,part,row_id,line_id,amount_minor) VALUES($1,$2,$3,$4,$5::uuid,$6::uuid,$7)", f.s.TenantID, f.s.BookID, id, i+1, row, line, p.amount); err != nil {
				return err
			}
		}
		return nil
	})
}

func (f statementFixture) decide(table, operation, columns, values string, args ...any) error {
	return write(f.db, f.db.Runtime, f.s, func(tx pgx.Tx) error {
		r, err := receipt(f.db, tx, f.s, operation, operation+"-"+ledger.ID())
		if err != nil {
			return err
		}
		_, err = tx.Exec(f.db.Context, "INSERT INTO public."+table+"(tenant_id,book_id,actor_id,receipt_id,"+columns+") VALUES($1,$2,$3,$4,"+values+")", append([]any{f.s.TenantID, f.s.BookID, f.s.ActorID, r}, args...)...)
		return err
	})
}

// review records the owner's sign-off. The trusted operator connection stands
// in for the owner session here; the owner-only grants are checked separately.
func (f statementFixture) review(statement string, revision int, outcome string) error {
	return write(f.db, f.db.Admin, f.s, func(tx pgx.Tx) error {
		r, err := receipt(f.db, tx, f.s, "review_statement", "review-"+ledger.ID())
		if err != nil {
			return err
		}
		_, err = tx.Exec(f.db.Context, `INSERT INTO public.statement_reviews(tenant_id,book_id,statement_id,revision,outcome,report_bytes,actor_id,receipt_id)
 VALUES($1,$2,$3,$4,$5,'{"report":"example"}',$6,$7)`, f.s.TenantID, f.s.BookID, statement, revision, outcome, f.s.ActorID, r)
		return err
	})
}

func lineOn(t *testing.T, db *testutil.Database, entry ledger.Entry, account string) string {
	t.Helper()
	for _, l := range entry.Lines {
		if l.AccountID == account {
			return l.ID
		}
	}
	t.Fatal("entry has no line on the account")
	return ""
}

func TestStatementReconciliationGuards(t *testing.T) {
	db, s, _ := documentFixture(t)
	_, other := db.Book(t, "Example other Book")
	f := statementFixture{db: db, s: s, bank: roleAccount(t, db, s, "1000", "asset", "cash"), card: roleAccount(t, db, s, "2100", "liability", "card"),
		equity: db.Account(t, s, "3000", "equity"), income: db.Account(t, s, "4000", "income"), expense: db.Account(t, s, "5000", "expense"),
		other: db.Account(t, s, "1500", "asset")}
	f.document = readyOriginal(t, db, s, "march-statement").ID
	journal(t, db, s, "opening", "2026-02-28", f.bank.ID, f.equity.ID, "100000")
	sale := journal(t, db, s, "sale", "2026-03-05", f.bank.ID, f.income.ID, "20000")
	purchase := journal(t, db, s, "purchase", "2026-03-08", f.expense.ID, f.bank.ID, "6000")
	transfer := journal(t, db, s, "transfer", "2026-03-12", f.other.ID, f.bank.ID, "10000")
	saleLine, purchaseLine, transferLine := lineOn(t, db, sale, f.bank.ID), lineOn(t, db, purchase, f.bank.ID), lineOn(t, db, transfer, f.bank.ID)

	_, _, err := f.importStatement(f.expense, "cash", 0, 0)
	refused(t, err, "cash or card account")
	_, _, err = f.importStatement(f.card, "cash", 0, 0)
	refused(t, err, "cash or card account")
	statement, rows, err := f.importStatement(f.bank, "cash", 100000, 103500,
		statementRow{20000, "Example customer", "settled"}, statementRow{-6000, "Example supplier", "settled"},
		statementRow{-10000, "Own account transfer", "settled"}, statementRow{-500, "Monthly fee", "settled"},
		statementRow{-900, "Card hold", "pending"})
	succeeded(t, err)
	// The overlapping statement observes the same payment within its own
	// opening and closing balances; it does not add another posted movement.
	_, repeatedRows, err := f.importStatement(f.bank, "cash", 100000, 120000, statementRow{20000, "Example customer", "settled"})
	succeeded(t, err)
	income, spend, own, fee, pending, repeat := rows[0], rows[1], rows[2], rows[3], rows[4], repeatedRows[0]

	t.Run("rows belong to their statement", func(t *testing.T) {
		refused(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
			_, err := tx.Exec(db.Context, `INSERT INTO public.statement_rows(tenant_id,book_id,id,statement_id,row_number,location,occurred_on,amount_minor,lifecycle,raw_bytes)
 VALUES($1,$2,$3,$4,99,'{"row":99}','2026-03-30',1,'settled','x')`, s.TenantID, s.BookID, ledger.ID(), statement)
			return err
		}), "recorded with their statement")
		refused(t, write(db, db.Admin, s, func(tx pgx.Tx) error {
			_, err := tx.Exec(db.Context, "UPDATE public.statement_rows SET amount_minor=1 WHERE id=$1", income)
			return err
		}), "record is immutable")
	})

	t.Run("match conservation and sign", func(t *testing.T) {
		_, err := f.match(f.bank.ID, matchPart{row: income, amount: 20000}, matchPart{line: saleLine, amount: 19000})
		refused(t, err, "equal totals")
		_, err = f.match(f.bank.ID, matchPart{row: income, amount: 20000})
		refused(t, err, "joins statement rows and posted lines")
		_, err = f.match(f.bank.ID, matchPart{row: spend, amount: 6000}, matchPart{line: purchaseLine, amount: 6000})
		refused(t, err, "keeps the sign")
		_, err = f.match(f.bank.ID, matchPart{row: income, amount: -6000}, matchPart{line: purchaseLine, amount: -6000})
		refused(t, err, "keeps the sign")
		_, err = f.match(f.bank.ID, matchPart{row: spend, amount: -6000}, matchPart{line: saleLine, amount: -6000})
		refused(t, err, "keeps the sign")
		_, err = f.match(f.other.ID, matchPart{row: spend, amount: -6000}, matchPart{line: purchaseLine, amount: -6000})
		refused(t, err, "on the matched account")
		otherLine := lineOn(t, db, transfer, f.other.ID)
		_, err = f.match(f.bank.ID, matchPart{row: own, amount: -10000}, matchPart{line: otherLine, amount: -10000})
		refused(t, err, "on the matched account")
	})

	t.Run("allocation", func(t *testing.T) {
		_, err := f.match(f.bank.ID, matchPart{row: income, amount: 25000}, matchPart{line: saleLine, amount: 25000})
		refused(t, err, "beyond its amount")
		first, err := f.match(f.bank.ID, matchPart{row: income, amount: 12000}, matchPart{line: saleLine, amount: 12000})
		succeeded(t, err)
		_, err = f.match(f.bank.ID, matchPart{row: income, amount: 9000}, matchPart{line: saleLine, amount: 9000})
		refused(t, err, "beyond its amount")
		// Two identical rows can't both claim one posted line.
		_, err = f.match(f.bank.ID, matchPart{row: repeat, amount: 9000}, matchPart{line: saleLine, amount: 9000})
		refused(t, err, "posted line is allocated beyond its amount")
		// A revoked match releases its allocation without deleting history.
		succeeded(t, f.decide("statement_match_revocations", "revoke_statement_match", "match_id,reason", "$5,$6", first, "Split was wrong"))
		refused(t, f.decide("statement_match_revocations", "revoke_statement_match", "match_id,reason", "$5,$6", first, "Again"), "statement_match_revocations_pkey")
		_, err = f.match(f.bank.ID, matchPart{row: income, amount: 20000}, matchPart{line: saleLine, amount: 20000})
		succeeded(t, err)
		var revoked int
		if db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.statement_matches m JOIN public.statement_match_revocations r ON r.match_id=m.id").Scan(&revoked); revoked != 1 {
			t.Fatal("revoked match history must remain")
		}
	})

	t.Run("pending and duplicate rows", func(t *testing.T) {
		_, err := f.match(f.bank.ID, matchPart{row: pending, amount: -900}, matchPart{line: purchaseLine, amount: -900})
		refused(t, err, "pending, cancelled and duplicate")
		refused(t, f.decide("statement_duplicates", "mark_statement_duplicate", "row_id,canonical_row_id,reason", "$5,$6,$7", repeat, spend, "Same row"), "same account and amount")
		refused(t, f.decide("statement_duplicates", "mark_statement_duplicate", "row_id,canonical_row_id,reason", "$5,$6,$7", income, repeat, "Matched row"), "pending, cancelled and duplicate")
		succeeded(t, f.decide("statement_duplicates", "mark_statement_duplicate", "row_id,canonical_row_id,reason", "$5,$6,$7", repeat, income, "Imported twice"))
		// Valid, unallocated posted money isolates the duplicate prohibition
		// from sign and line-capacity failures, for partial and full amounts.
		available := journal(t, db, s, "duplicate-counterpart", "2026-03-10", f.bank.ID, f.income.ID, "20000")
		availableLine := lineOn(t, db, available, f.bank.ID)
		_, err = f.match(f.bank.ID, matchPart{row: repeat, amount: 1000}, matchPart{line: availableLine, amount: 1000})
		refused(t, err, "pending, cancelled and duplicate")
		_, err = f.match(f.bank.ID, matchPart{row: repeat, amount: 20000}, matchPart{line: availableLine, amount: 20000})
		refused(t, err, "pending, cancelled and duplicate")
	})

	t.Run("reconciled only when fully explained", func(t *testing.T) {
		_, err := f.match(f.bank.ID, matchPart{row: spend, amount: -6000}, matchPart{line: purchaseLine, amount: -6000})
		succeeded(t, err)
		_, err = f.match(f.bank.ID, matchPart{row: own, amount: -10000}, matchPart{line: transferLine, amount: -10000})
		succeeded(t, err)
		// The fee is on the statement but not yet in the Book.
		refused(t, f.review(statement, 1, "reconciled"), "not fully explained")
		succeeded(t, f.review(statement, 1, "needs_work"))
		refused(t, f.review(statement, 1, "needs_work"), "stale statement review")
		var feeEntry ledger.Entry
		feeEntry = journal(t, db, s, "fee", "2026-03-31", f.expense.ID, f.bank.ID, "500")
		wrong := journal(t, db, s, "wrong-fee", "2026-03-31", f.expense.ID, f.bank.ID, "400")
		refused(t, f.decide("statement_adjustments", "record_statement_adjustment", "row_id,entry_id", "$5,$6", fee, wrong.ID), "by the row amount")
		hold := journal(t, db, s, "hold", "2026-03-31", f.expense.ID, f.bank.ID, "900")
		refused(t, f.decide("statement_adjustments", "record_statement_adjustment", "row_id,entry_id", "$5,$6", pending, hold.ID), "pending, cancelled and duplicate")
		succeeded(t, f.decide("statement_adjustments", "record_statement_adjustment", "row_id,entry_id", "$5,$6", fee, feeEntry.ID))
		_, err = f.match(f.bank.ID, matchPart{row: fee, amount: -500}, matchPart{line: lineOn(t, db, feeEntry, f.bank.ID), amount: -500})
		refused(t, err, "match or an adjustment, not both")
		succeeded(t, f.review(statement, 2, "reconciled"))
		// A statement whose balances don't agree never reconciles.
		short, shortRows, err := f.importStatement(f.bank, "cash", 100000, 100001, statementRow{20000, "Example customer", "settled"})
		succeeded(t, err)
		_, err = f.match(f.bank.ID, matchPart{row: shortRows[0], amount: 20000}, matchPart{line: lineOn(t, db, journal(t, db, s, "sale-2", "2026-03-20", f.bank.ID, f.income.ID, "20000"), f.bank.ID), amount: 20000})
		succeeded(t, err)
		refused(t, f.review(short, 1, "reconciled"), "not fully explained")
	})

	t.Run("card signs", func(t *testing.T) {
		charge := journal(t, db, s, "card-charge", "2026-03-15", f.expense.ID, f.card.ID, "3000")
		payment := journal(t, db, s, "card-payment", "2026-03-25", f.card.ID, f.bank.ID, "3000")
		_, cardRows, err := f.importStatement(f.card, "card", 0, 0, statementRow{3000, "Example shop", "settled"}, statementRow{-3000, "Payment", "settled"})
		succeeded(t, err)
		_, err = f.match(f.card.ID, matchPart{row: cardRows[0], amount: 3000}, matchPart{line: lineOn(t, db, charge, f.card.ID), amount: 3000})
		succeeded(t, err)
		_, err = f.match(f.card.ID, matchPart{row: cardRows[1], amount: -3000}, matchPart{line: lineOn(t, db, payment, f.card.ID), amount: -3000})
		succeeded(t, err)
	})

	t.Run("cross-Book references", func(t *testing.T) {
		bank := roleAccount(t, db, other, "1000", "asset", "cash")
		otherEntry := journal(t, db, other, "opening", "2026-03-02", bank.ID, db.Account(t, other, "3000", "equity").ID, "100")
		_, err := f.match(f.bank.ID, matchPart{row: spend, amount: -1}, matchPart{line: lineOn(t, db, otherEntry, bank.ID), amount: -1})
		refused(t, err, "statement_match_parts_tenant_id_book_id_line_id_fkey")
		refused(t, write(db, db.Runtime, other, func(tx pgx.Tx) error {
			_, err := tx.Exec(db.Context, `INSERT INTO public.statement_rows(tenant_id,book_id,id,statement_id,row_number,location,occurred_on,amount_minor,lifecycle,raw_bytes)
 VALUES($1,$2,$3,$4,1,'{"row":1}','2026-03-30',1,'settled','x')`, other.TenantID, other.BookID, ledger.ID(), statement)
			return err
		}), "statement rows are recorded with their statement")
		// Another Book can't see this Book's statements.
		var visible int
		succeeded(t, write(db, db.Runtime, other, func(tx pgx.Tx) error {
			return tx.QueryRow(db.Context, "SELECT count(*) FROM public.statements").Scan(&visible)
		}))
		if visible != 0 {
			t.Fatal("row security exposed another Book's statements")
		}
	})

	t.Run("reversed entries", func(t *testing.T) {
		mistaken := journal(t, db, s, "mistaken", "2026-03-18", f.expense.ID, f.bank.ID, "777")
		var reversal ledger.Entry
		if json.Unmarshal(db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "undo-mistaken", EntryID: mistaken.ID, AccountingDate: "2026-03-19", Reason: "Posted twice"}), &reversal) != nil {
			t.Fatal("invalid reversal")
		}
		_, rows, err := f.importStatement(f.bank, "cash", 0, 0, statementRow{-777, "Example charge", "settled"}, statementRow{777, "Example refund", "settled"})
		succeeded(t, err)
		_, err = f.match(f.bank.ID, matchPart{row: rows[0], amount: -777}, matchPart{line: lineOn(t, db, mistaken, f.bank.ID), amount: -777})
		refused(t, err, "reversed or reversing entry cannot be matched")
		_, err = f.match(f.bank.ID, matchPart{row: rows[1], amount: 777}, matchPart{line: lineOn(t, db, reversal, f.bank.ID), amount: 777})
		refused(t, err, "reversed or reversing entry cannot be matched")
		refused(t, f.decide("statement_adjustments", "record_statement_adjustment", "row_id,entry_id", "$5,$6", rows[0], mistaken.ID), "reversed or reversing entry cannot adjust")
		// A matched entry is reversed only after the match is revoked.
		matched := journal(t, db, s, "matched", "2026-03-21", f.expense.ID, f.bank.ID, "333")
		_, more, err := f.importStatement(f.bank, "cash", 0, 0, statementRow{-333, "Example shop", "settled"})
		succeeded(t, err)
		match, err := f.match(f.bank.ID, matchPart{row: more[0], amount: -333}, matchPart{line: lineOn(t, db, matched, f.bank.ID), amount: -333})
		succeeded(t, err)
		refused(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
			_, err := tx.Exec(db.Context, `INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description,reversal_of)
 VALUES($1,$2,$3,1,$4,$5,'2026-03-21','2026-03-22','Example reversal',$6)`, s.TenantID, s.BookID, ledger.ID(), ledger.ID(), s.ActorID, matched.ID)
			return err
		}), "revoke the statement match before reversing")
		b, _ := json.Marshal(ledger.ReverseJournal{RequestKey: "undo-matched-early", EntryID: matched.ID, AccountingDate: "2026-03-22", Reason: "Wrong account"})
		if _, err := db.Store.Call(db.Context, s, "reverse_journal", b); err == nil {
			t.Fatal("the store reversed a matched entry")
		}
		succeeded(t, f.decide("statement_match_revocations", "revoke_statement_match", "match_id,reason", "$5,$6", match, "Entry is being corrected"))
		db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "undo-matched", EntryID: matched.ID, AccountingDate: "2026-03-22", Reason: "Wrong account"})
	})

	t.Run("archive round trip", func(t *testing.T) {
		var archive bytes.Buffer
		succeeded(t, db.Owner.WriteArchive(db.Context, db.Store.Documents, &archive, ""))
		target := testutil.New(t)
		vault, _ := restoreVault(t)
		if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil); err != nil {
			t.Fatal("archive with reconciliation history did not restore", err)
		}
		var restored bytes.Buffer
		succeeded(t, target.Owner.WriteArchive(target.Context, vault, &restored, ""))
		before, after := archiveManifestOf(t, archive.Bytes()), archiveManifestOf(t, restored.Bytes())
		for _, table := range []string{"accounts", "account_revisions", "statements", "statement_rows", "statement_duplicates", "statement_matches",
			"statement_match_parts", "statement_match_revocations", "statement_adjustments", "statement_reviews"} {
			if len(before.Snapshot.Tables[table]) < 3 || !bytes.Equal(before.Snapshot.Tables[table], after.Snapshot.Tables[table]) {
				t.Fatal("archive lost or changed reconciliation rows", table, string(before.Snapshot.Tables[table]), string(after.Snapshot.Tables[table]))
			}
		}
	})
}

func TestPeriodCloseAndReopenGuards(t *testing.T) {
	db := testutil.New(t)
	book, s := db.Book(t, "Example Book")
	_, other := db.Book(t, "Example other Book")
	bank := roleAccount(t, db, s, "1000", "asset", "cash")
	expense := db.Account(t, s, "5000", "expense")
	succeeded(t, db.ClosePeriod(db.Context, book.ID, "2026-01-01", "2026-01-31"))
	var period string
	succeeded(t, db.Admin.QueryRow(db.Context, "SELECT id::text FROM public.accounting_periods WHERE book_id=$1", book.ID).Scan(&period))
	decide := func(scope ledger.Scope, revision int, event, reason string) error {
		return write(db, db.Admin, scope, func(tx pgx.Tx) error {
			operation := map[string]string{"closed": "close_period", "reopened": "reopen_period"}[event]
			r, err := receipt(db, tx, scope, operation, operation+"-"+ledger.ID())
			if err != nil {
				return err
			}
			_, err = tx.Exec(db.Context, `INSERT INTO public.accounting_period_events(tenant_id,book_id,period_id,revision,event,origin,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,'decision',$6,$7,$8)`, scope.TenantID, scope.BookID, period, revision, event, scope.ActorID, reason, r)
			return err
		})
	}

	refused(t, rawJournal(db, s, "2026-01-15", expense.ID, bank.ID), "accounting period is closed")
	refused(t, decide(s, 2, "reopened", ""), "violates check constraint")
	refused(t, decide(s, 2, "closed", "Again"), "alternate between closed and reopened")
	refused(t, decide(s, 3, "reopened", "Skip"), "alternate between closed and reopened")
	refused(t, decide(other, 1, "closed", "Wrong Book"), "accounting_period_events_tenant_id_book_id_period_id_fkey")
	succeeded(t, decide(s, 2, "reopened", "Missing supplier invoice"))
	journal(t, db, s, "reopened", "2026-01-15", expense.ID, bank.ID, "100")
	succeeded(t, decide(s, 3, "closed", "Invoice recorded"))
	refused(t, rawJournal(db, s, "2026-01-16", expense.ID, bank.ID), "accounting period is closed")
	journal(t, db, s, "open-month", "2026-02-01", expense.ID, bank.ID, "100")

	var history []string
	rows, err := db.Admin.Query(db.Context, "SELECT event||':'||origin FROM public.accounting_period_events WHERE period_id=$1 ORDER BY revision", period)
	succeeded(t, err)
	for rows.Next() {
		var item string
		succeeded(t, rows.Scan(&item))
		history = append(history, item)
	}
	rows.Close()
	if len(history) != 3 || history[0] != "closed:decision" || history[1] != "reopened:decision" || history[2] != "closed:decision" {
		t.Fatal("close and reopen history is incomplete", history)
	}

	// Periods never overlap, whichever path creates them.
	if err := db.ClosePeriod(db.Context, book.ID, "2026-03-01", "2026-03-31"); err != nil {
		t.Fatal(err)
	}
	refused(t, write(db, db.Admin, s, func(tx pgx.Tx) error {
		_, err := tx.Exec(db.Context, "INSERT INTO public.closed_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,'2026-01-20','2026-01-25')", s.TenantID, s.BookID, ledger.ID())
		return err
	}), "legacy close records are restored history")
	refused(t, write(db, db.Admin, s, func(tx pgx.Tx) error {
		id := ledger.ID()
		if _, err := tx.Exec(db.Context, "INSERT INTO public.accounting_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,'2026-01-31','2026-02-28')", s.TenantID, s.BookID, id); err != nil {
			return err
		}
		return nil
	}), "overlaps")
	// A period exists only with its first close.
	refused(t, write(db, db.Admin, s, func(tx pgx.Tx) error {
		_, err := tx.Exec(db.Context, "INSERT INTO public.accounting_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,'2026-05-01','2026-05-31')", s.TenantID, s.BookID, ledger.ID())
		return err
	}), "created by its first close")
	// No one can record a close without an actor unless it is the original record.
	refused(t, write(db, db.Admin, s, func(tx pgx.Tx) error {
		id := ledger.ID()
		if _, err := tx.Exec(db.Context, "INSERT INTO public.accounting_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,'2026-04-01','2026-04-30')", s.TenantID, s.BookID, id); err != nil {
			return err
		}
		_, err := tx.Exec(db.Context, "INSERT INTO public.accounting_period_events(tenant_id,book_id,period_id,revision,event,origin) VALUES($1,$2,$3,1,'closed','closed_period')", s.TenantID, s.BookID, id)
		return err
	}), "original close record")
	// A write agent can't reopen a period; only the owner can.
	refused(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
		r, err := receipt(db, tx, s, "reopen_period", "agent-reopen")
		if err != nil {
			return err
		}
		_, err = tx.Exec(db.Context, `INSERT INTO public.accounting_period_events(tenant_id,book_id,period_id,revision,event,origin,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,4,'reopened','decision',$4,'Agent',$5)`, s.TenantID, s.BookID, period, s.ActorID, r)
		return err
	}), "only the owner can reopen")
	var visible int
	succeeded(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
		return tx.QueryRow(db.Context, "SELECT count(*) FROM public.accounting_period_events").Scan(&visible)
	}))
	if visible != 4 {
		t.Fatal("agents must read the period history", visible)
	}
	refused(t, write(db, db.Admin, s, func(tx pgx.Tx) error {
		_, err := tx.Exec(db.Context, "DELETE FROM public.accounting_period_events WHERE period_id=$1", period)
		return err
	}), "record is immutable")

	// A ledger backup keeps the reopened history and the original close record.
	snapshot, err := db.Owner.Snapshot(db.Context)
	succeeded(t, err)
	if snapshot.Format != "vult-ledger-backup-v6" {
		t.Fatal("unexpected backup format", snapshot.Format)
	}
	target := testutil.New(t)
	succeeded(t, target.Owner.Restore(target.Context, snapshot))
	again, err := target.Owner.Snapshot(target.Context)
	succeeded(t, err)
	for _, table := range []string{"closed_periods", "accounting_periods", "accounting_period_events", "entries"} {
		if !bytes.Equal(snapshot.Tables[table], again.Tables[table]) {
			t.Fatal("backup restore changed period history", table)
		}
	}
}

func connectionScope(t *testing.T, db *testutil.Database, book, level string) ledger.Scope {
	t.Helper()
	c, err := db.Owner.CreateConnection(db.Context, book, "Example "+level+" connection", level)
	succeeded(t, err)
	return ledger.Scope{TenantID: c.TenantID, BookID: c.BookID, ActorID: c.ActorID, ConnectionID: c.ID, Access: c.Access}
}

// recordPayee creates a named payee in one request through the given pool.
func recordPayee(db *testutil.Database, pool *pgxpool.Pool, scope ledger.Scope, name string) (string, error) {
	id := ledger.ID()
	return id, write(db, pool, scope, func(tx pgx.Tx) error {
		r, err := receipt(db, tx, scope, "create_payee", "payee-"+id)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(db.Context, "INSERT INTO public.payees(tenant_id,book_id,id,actor_id,receipt_id) VALUES($1,$2,$3,$4,$5)", scope.TenantID, scope.BookID, id, scope.ActorID, r); err != nil {
			return err
		}
		_, err = tx.Exec(db.Context, `INSERT INTO public.payee_revisions(tenant_id,book_id,payee_id,revision,display_name,kind,status,actor_id,receipt_id)
 VALUES($1,$2,$3,1,$4,'organization','active',$5,$6)`, scope.TenantID, scope.BookID, id, name, scope.ActorID, r)
		return err
	})
}

// closePeriod records a new period and its first close in one request.
func closePeriod(db *testutil.Database, pool *pgxpool.Pool, scope ledger.Scope, first, last, reason string) (string, error) {
	id := ledger.ID()
	return id, write(db, pool, scope, func(tx pgx.Tx) error {
		r, err := receipt(db, tx, scope, "close_period", "close-"+id)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(db.Context, "INSERT INTO public.accounting_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,$4,$5)", scope.TenantID, scope.BookID, id, first, last); err != nil {
			return err
		}
		_, err = tx.Exec(db.Context, `INSERT INTO public.accounting_period_events(tenant_id,book_id,period_id,revision,event,origin,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,1,'closed','decision',$4,$5,$6)`, scope.TenantID, scope.BookID, id, scope.ActorID, reason, r)
		return err
	})
}

func TestFoundationAccessFollowsConnectionLevels(t *testing.T) {
	db, writer, _ := documentFixture(t)
	book := writer.BookID
	proposer, reader := connectionScope(t, db, book, "propose"), connectionScope(t, db, book, "read")
	bank := roleAccount(t, db, writer, "1000", "asset", "cash")
	equity := db.Account(t, writer, "3000", "equity")
	document := readyOriginal(t, db, writer, "april-statement").ID
	f := statementFixture{db: db, s: writer, bank: bank, equity: equity, document: document}

	t.Run("write agent does routine work directly", func(t *testing.T) {
		_, err := recordPayee(db, db.Runtime, writer, "Example bank")
		succeeded(t, err)
		statement, _, err := f.importStatement(bank, "cash", 0, 0)
		succeeded(t, err)
		succeeded(t, f.review(statement, 1, "needs_work"))
		succeeded(t, write(db, db.Runtime, writer, func(tx pgx.Tx) error {
			r, err := receipt(db, tx, writer, "review_statement", "agent-review")
			if err != nil {
				return err
			}
			_, err = tx.Exec(db.Context, `INSERT INTO public.statement_reviews(tenant_id,book_id,statement_id,revision,outcome,report_bytes,actor_id,receipt_id)
 VALUES($1,$2,$3,2,'reconciled','{"report":"example"}',$4,$5)`, writer.TenantID, writer.BookID, statement, writer.ActorID, r)
			return err
		}))
		// A period with nothing left to review closes without the owner.
		_, err = closePeriod(db, db.Runtime, writer, "2025-01-01", "2025-01-31", "")
		succeeded(t, err)
	})

	t.Run("open review items and reopening need the owner", func(t *testing.T) {
		fees := db.Account(t, writer, "6600", "expense")
		journal(t, db, writer, "unexplained", "2025-02-10", fees.ID, bank.ID, "500")
		_, err := closePeriod(db, db.Runtime, writer, "2025-02-01", "2025-02-28", "Agent wants it closed")
		refused(t, err, "only the owner can close a period with open review items")
		_, err = closePeriod(db, db.Admin, writer, "2025-02-01", "2025-02-28", "")
		refused(t, err, "needs a reason")
		period, err := closePeriod(db, db.Admin, writer, "2025-02-01", "2025-02-28", "Receipt requested from the bank")
		succeeded(t, err)
		var items map[string]int
		succeeded(t, db.Admin.QueryRow(db.Context, "SELECT open_items FROM public.accounting_period_events WHERE period_id=$1", period).Scan(&items))
		if items["entries_without_evidence"] != 1 || items["entries_without_payee"] != 1 || items["unreconciled_statements"] != 0 {
			t.Fatal("the close did not record its open review items", items)
		}
		refused(t, write(db, db.Runtime, writer, func(tx pgx.Tx) error {
			r, err := receipt(db, tx, writer, "reopen_period", "agent-reopen")
			if err != nil {
				return err
			}
			_, err = tx.Exec(db.Context, `INSERT INTO public.accounting_period_events(tenant_id,book_id,period_id,revision,event,origin,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,2,'reopened','decision',$4,'Agent',$5)`, writer.TenantID, writer.BookID, period, writer.ActorID, r)
			return err
		}), "only the owner can reopen")
	})

	t.Run("propose agent records only proposals", func(t *testing.T) {
		_, err := recordPayee(db, db.Runtime, proposer, "Example shop")
		refused(t, err, "row-level security")
		_, err = closePeriod(db, db.Runtime, proposer, "2025-03-01", "2025-03-31", "")
		refused(t, err, "row-level security")
		arguments := map[string]string{"request_key": "propose-close", "start_date": "2025-03-01", "end_date": "2025-03-31"}
		payload, _ := json.Marshal(map[string]any{"version": 1, "operation": "close_period", "arguments": arguments,
			"dependencies": map[string]any{"accounts": []any{}, "documents": []any{}, "closed_periods": []any{}, "reversed": false}})
		proposal := ledger.ID()
		refused(t, write(db, db.Runtime, proposer, func(tx pgx.Tx) error {
			if _, err := tx.Exec(db.Context, "SELECT set_config('vult.proposing','record',true)"); err != nil {
				return err
			}
			_, err := tx.Exec(db.Context, "INSERT INTO public.payees(tenant_id,book_id,id,actor_id,receipt_id) VALUES($1,$2,$3,$4,$5)", proposer.TenantID, proposer.BookID, ledger.ID(), proposer.ActorID, ledger.ID())
			return err
		}), "a proposal never records changes")
		succeeded(t, write(db, db.Runtime, proposer, func(tx pgx.Tx) error {
			if _, err := tx.Exec(db.Context, "SELECT set_config('vult.proposing','record',true)"); err != nil {
				return err
			}
			_, err := tx.Exec(db.Context, `INSERT INTO public.operation_proposals(tenant_id,book_id,id,sequence,actor_id,connection_id,request_key,operation,operation_key,payload_bytes)
 VALUES($1,$2,$3,1,$4,$5,'proposal-close','close_period','propose-close',$6)`, proposer.TenantID, proposer.BookID, proposal, proposer.ActorID, proposer.ConnectionID, payload)
			return err
		}))
		// The owner's approval runs the proposed close with the proposing
		// agent as its actor, consuming the approval in the same transaction.
		owner := ledger.ID()
		succeeded(t, write(db, db.Admin, proposer, func(tx pgx.Tx) error {
			if _, err := tx.Exec(db.Context, `INSERT INTO public.operation_decisions(tenant_id,book_id,proposal_id,revision,owner_id,owner_epoch,request_key,sha256,decision,reason)
 SELECT tenant_id,book_id,id,1,$2,0,'approve-close',sha256,'approve','Month is complete' FROM public.operation_proposals WHERE id=$1`, proposal, owner); err != nil {
				return err
			}
			request, _ := json.Marshal(arguments)
			receiptID := ledger.ID()
			if _, err := tx.Exec(db.Context, `INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 VALUES($1,$2,$3,$4,'propose-close','close_period',$5,'{}')`, proposer.TenantID, proposer.BookID, receiptID, proposer.ActorID, request); err != nil {
				return err
			}
			period := ledger.ID()
			if _, err := tx.Exec(db.Context, "INSERT INTO public.accounting_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,'2025-03-01','2025-03-31')", proposer.TenantID, proposer.BookID, period); err != nil {
				return err
			}
			if _, err := tx.Exec(db.Context, `INSERT INTO public.accounting_period_events(tenant_id,book_id,period_id,revision,event,origin,actor_id,receipt_id)
 VALUES($1,$2,$3,1,'closed','decision',$4,$5)`, proposer.TenantID, proposer.BookID, period, proposer.ActorID, receiptID); err != nil {
				return err
			}
			_, err := tx.Exec(db.Context, `INSERT INTO public.operation_consumptions(tenant_id,book_id,proposal_id,decision_revision,receipt_id,actor_id)
 VALUES($1,$2,$3,1,$4,$5)`, proposer.TenantID, proposer.BookID, proposal, receiptID, owner)
			return err
		}))
		var closes int
		succeeded(t, db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.accounting_period_events WHERE actor_id=$1", proposer.ActorID).Scan(&closes))
		if closes != 1 {
			t.Fatal("the approved proposal did not close the period")
		}
	})

	t.Run("read agent only reads", func(t *testing.T) {
		_, err := recordPayee(db, db.Runtime, reader, "Example reader payee")
		refused(t, err, "row-level security")
		_, _, err = statementFixture{db: db, s: reader, document: document}.importStatement(bank, "cash", 0, 0)
		refused(t, err, "row-level security")
		var statements, periods int
		succeeded(t, write(db, db.Runtime, reader, func(tx pgx.Tx) error {
			return tx.QueryRow(db.Context, "SELECT (SELECT count(*) FROM public.statements),(SELECT count(*) FROM public.accounting_periods)").Scan(&statements, &periods)
		}))
		if statements != 1 || periods != 3 {
			t.Fatal("a read agent must see the Book's statements and periods", statements, periods)
		}
	})
}

// Roles an earlier release accepted keep upgrading, unchanged, with the
// meaning that release's reports gave them.
func TestUpgradeKeepsRolesAnEarlierReleaseAccepted(t *testing.T) {
	db := testutil.NewWithInitializer(t, releasedBookSchema)
	book, s := db.Book(t, "Example Book")
	bank := db.Account(t, s, "1000", "asset")
	interest := db.Account(t, s, "4900", "income")
	sales := db.Account(t, s, "4000", "income")
	goods := db.Account(t, s, "5000", "expense")
	penalty := db.Account(t, s, "6900", "expense")
	// Store the roles as that release's writer did.
	tx, err := db.Admin.Begin(db.Context)
	succeeded(t, err)
	_, err = tx.Exec(db.Context, "SET LOCAL session_replication_role = replica")
	succeeded(t, err)
	for id, role := range map[string]string{interest.ID: "other_income", goods.ID: "cost_of_goods_sold", penalty.ID: "other_expense"} {
		for _, table := range []string{"accounts", "account_revisions"} {
			column := "id"
			if table == "account_revisions" {
				column = "account_id"
			}
			_, err = tx.Exec(db.Context, "UPDATE public."+table+" SET role=$2,role_revision=1 WHERE "+column+"=$1", id, role)
			succeeded(t, err)
		}
	}
	succeeded(t, tx.Commit(db.Context))
	for _, p := range []struct {
		debit, credit string
		amount        int64
	}{{bank.ID, sales.ID, 1000}, {bank.ID, interest.ID, 50}, {goods.ID, bank.ID, 400}, {penalty.ID, bank.ID, 30}} {
		succeeded(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
			_, _, err := postInTransaction(db, tx, s, "2026-02-01", rawLine{p.debit, "debit", p.amount}, rawLine{p.credit, "credit", p.amount})
			return err
		}))
	}
	var before string
	succeeded(t, db.Admin.QueryRow(db.Context, "SELECT string_agg(id::text||role,',' ORDER BY id) FROM public.accounts WHERE role IS NOT NULL").Scan(&before))
	succeeded(t, ledger.Upgrade(db.Context, db.Admin, db.Role))
	var after string
	succeeded(t, db.Admin.QueryRow(db.Context, "SELECT string_agg(id::text||role,',' ORDER BY id) FROM public.accounts WHERE role IS NOT NULL").Scan(&after))
	if before != after {
		t.Fatal("upgrade rewrote stored roles")
	}
	r := report(t, db, book, ledger.FinancialReportRequest{Report: reporting.ProfitLoss, From: "2026-01-01", To: "2026-12-31"})
	if r.Reconciliation["income"] != "1050" || r.Reconciliation["expenses"] != "430" || r.Reconciliation["gross_profit"] != "650" || r.Reconciliation["result"] != "620" {
		t.Fatal("earlier roles lost their report meaning", r.Reconciliation)
	}
	// New writes use the current vocabulary only.
	for _, role := range []string{"other_income", "cost_of_goods_sold"} {
		b, _ := json.Marshal(ledger.CreateAccount{RequestKey: "new-" + role, Class: "income", Code: "R" + role, Name: "Example", Role: role})
		if _, err := db.Store.Call(db.Context, s, "create_account", b); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatal("a new account used an earlier role", role, err)
		}
	}
	// An account keeping an earlier role can still be renamed.
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: interest.ID, ExpectedRevision: 1, Code: "4900", Name: "Example interest"})
}
