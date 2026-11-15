package ledger_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// releasedLinked is a Vult as 0.8 left it, with a studio that owes Kit:
// costs, an opening balance, a settlement that named its costs and one
// that repaid a whole cost, payments recorded as plain entries on what is
// owed (one later undone), an increase with no cost behind it, and the
// rows as 0.8 kept them.
func releasedLinked(t *testing.T) (*testutil.Database, map[string]string) {
	t.Helper()
	ids := map[string]string{}
	id := func(name string) string {
		if ids[name] == "" {
			ids[name] = ledger.ID()
		}
		return ids[name]
	}
	db := releasedThrough(t, "0032_carried_costs.sql", func(ctx context.Context, pool *pgxpool.Pool) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		tenant, kit, studio := id("tenant"), id("kit"), id("studio")
		exec := func(q string, args ...any) {
			if err == nil {
				_, err = tx.Exec(ctx, q, args...)
			}
		}
		exec("SET LOCAL session_replication_role = replica")
		exec("INSERT INTO public.tenants(id) VALUES($1)", tenant)
		for _, b := range []string{kit, studio} {
			exec("INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Example','USD',2)", tenant, b)
		}
		account := func(book, name, class string) string {
			a := id(book + name)
			exec("INSERT INTO public.accounts(tenant_id,book_id,id,class,code,name) VALUES($1,$2,$3,$4,$5,$6)", tenant, book, a, class, name, "Example "+name)
			return a
		}
		payable, expense, cash := account(studio, "2300", "liability"), account(studio, "6200", "expense"), account(studio, "1000", "asset")
		receivable, card, equity := account(kit, "1310", "asset"), account(kit, "2100", "liability"), account(kit, "3900", "equity")
		studioEquity := account(studio, "3900", "equity")
		ids["payable"] = payable
		link := id("link")
		a, b := kit, studio
		if b < a {
			a, b = b, a
		}
		exec("INSERT INTO public.book_links(tenant_id,id,book_a,book_b,active,revision,actor_id) VALUES($1,$2,$3,$4,true,1,$5)", tenant, link, a, b, id("actor"))
		exec("INSERT INTO public.book_link_terms(tenant_id,link_id,payer_book_id,beneficiary_book_id,treatment,payer_account_id,beneficiary_account_id,active,actor_id) VALUES($1,$2,$3,$4,'owed',$5,$6,true,$7)",
			tenant, link, kit, studio, receivable, payable, id("actor"))
		sequence := map[string]int{}
		entry := func(book, name, date string, reverses string, lines ...[3]any) string {
			e := id(name)
			sequence[book]++
			var reversal any
			if reverses != "" {
				reversal = id(reverses)
			}
			exec("INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description,reversal_of) VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8,$9)",
				tenant, book, e, sequence[book], ledger.ID(), id("actor"), date, name, reversal)
			for i, l := range lines {
				exec("INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor) VALUES($1,$2,$3,$4,$5,$6,$7,$8)",
					tenant, book, ledger.ID(), e, i+1, l[0], l[1], l[2])
			}
			return e
		}
		pair := func(key, kind, payerBook, payerEntry, beneficiaryBook, beneficiaryEntry string) {
			exec("INSERT INTO public.entry_pairs(tenant_id,pair_key,kind,payer_book_id,payer_entry_id,beneficiary_book_id,beneficiary_entry_id) VALUES($1,$2,$3,$4,$5,$6,$7)",
				tenant, key, kind, payerBook, payerEntry, beneficiaryBook, beneficiaryEntry)
		}
		cost := func(name, date string, amount int) {
			paid := entry(kit, name+" paid", date, "", [3]any{receivable, "debit", amount}, [3]any{card, "credit", amount})
			owed := entry(studio, name, date, "", [3]any{expense, "debit", amount}, [3]any{payable, "credit", amount})
			pair("pair-"+name, "cost", kit, paid, studio, owed)
		}
		// Owed from before the bank history.
		openingOwed := entry(studio, "Owed before", "2026-09-01", "", [3]any{studioEquity, "debit", 2000}, [3]any{payable, "credit", 2000})
		openingDue := entry(kit, "Owed before due", "2026-09-01", "", [3]any{receivable, "debit", 2000}, [3]any{equity, "credit", 2000})
		pair("pair-opening", "opening", studio, openingOwed, kit, openingDue)
		cost("Desk", "2026-10-01", 5000)
		cost("Lamp", "2026-10-05", 4000)
		cost("Chair", "2026-10-20", 3000)
		cost("Shelf", "2026-10-22", 2500)
		// A settlement that named the opening balance and the desk, and
		// paid 1000 more than the rows 0.8 kept for it.
		settled := entry(studio, "Settlement 1", "2026-10-10", "", [3]any{payable, "debit", 7000}, [3]any{cash, "credit", 7000})
		arrived := entry(kit, "Settlement 1 in", "2026-10-10", "", [3]any{card, "debit", 7000}, [3]any{receivable, "credit", 7000})
		pair("settle-1", "settlement", studio, settled, kit, arrived)
		exec("INSERT INTO public.settlement_repays(tenant_id,settlement_book_id,settlement_entry_id,cost_book_id,cost_entry_id,amount_minor) VALUES($1,$2,$3,$4,$5,4000)", tenant, studio, settled, studio, id("Desk"))
		// An early row repaid the whole cost: no amount.
		exec("INSERT INTO public.settlement_repays(tenant_id,settlement_book_id,settlement_entry_id,cost_book_id,cost_entry_id,amount_minor) VALUES($1,$2,$3,$4,$5,NULL)", tenant, studio, settled, kit, openingDue)
		// Plain payments on what is owed, one undone, and an increase with
		// no cost behind it.
		entry(studio, "Plain 1", "2026-10-12", "", [3]any{payable, "debit", 1500}, [3]any{cash, "credit", 1500})
		entry(studio, "Plain undone", "2026-10-13", "", [3]any{payable, "debit", 700}, [3]any{cash, "credit", 700})
		entry(studio, "Plain undone reversal", "2026-10-14", "Plain undone", [3]any{cash, "debit", 700}, [3]any{payable, "credit", 700})
		entry(studio, "Unpaired increase", "2026-10-15", "", [3]any{expense, "debit", 500}, [3]any{payable, "credit", 500})
		entry(studio, "Plain 2", "2026-10-25", "", [3]any{payable, "debit", 1200}, [3]any{cash, "credit", 1200})
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return db, ids
}

const openRead = `SELECT string_agg(e.description||'='||o.open, ', ' ORDER BY o.accounting_date, o.sequence)
 FROM public.open_costs($1,$2,$3,$4,NULL) o JOIN public.entries e ON e.tenant_id=$1 AND e.book_id=o.book_id AND e.id=o.entry_id`

// Upgrading stores what every payment repaid as 0.8 showed it, and nothing
// a person sees moves: each cost's open amount is the same, settlements
// keep their amounts (a whole cost included), and payments recorded as
// plain entries hold what the read gave them, in date order.
func TestUpgradeFreezesWhatWasRepaid(t *testing.T) {
	db, ids := releasedLinked(t)
	args := []any{ids["tenant"], ids["kit"], ids["studio"], ids["payable"]}
	var before string
	if err := db.Admin.QueryRow(db.Context, openRead, args...).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// 0.8's read: owed is 2000+5000+4000+3000+2500+500 less 7000, 1500 and
	// 1200 paid, so 7300, reaching back from the newest cost.
	if before != "Owed before due=0, Desk=0, Lamp=1800, Chair=3000, Shelf=2500" {
		t.Fatal("the released read", before)
	}
	if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := db.Admin.QueryRow(db.Context, openRead, args...).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("open amounts moved", before, after)
	}
	var rows string
	if err := db.Admin.QueryRow(db.Context, `SELECT string_agg(coalesce(s.description,'(none)')||'->'||o.description||'='||a.amount_minor||' '||a.source||'/'||a.provenance, ', ' ORDER BY a.source, s.description NULLS LAST, o.accounting_date)
 FROM public.allocations a JOIN public.entries o ON o.tenant_id=a.tenant_id AND o.book_id=a.obligation_book_id AND o.id=a.obligation_entry_id
 LEFT JOIN public.entries s ON s.tenant_id=a.tenant_id AND s.book_id=a.source_book_id AND s.id=a.source_entry_id`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	// The read left 3200 repaid beyond settlements' rows (desk 1000, lamp
	// 2200): Plain 1 gives 1500, Plain 2 its 1200, and the 500 the
	// settlement paid beyond its rows has no payment of its own.
	want := []string{
		"Plain 1->Desk=1000 posting/frozen_at_migration",
		"Plain 1->Lamp=500 posting/frozen_at_migration",
		"Plain 2->Lamp=1200 posting/frozen_at_migration",
		"(none)->Lamp=500 correction/frozen_at_migration",
		"Settlement 1->Owed before due=2000 settlement/migrated_settlement",
		"Settlement 1->Desk=4000 settlement/migrated_settlement",
	}
	for _, w := range want {
		if !strings.Contains(rows, w) {
			t.Fatal("allocations", rows, "missing", w)
		}
	}
	if strings.Contains(rows, "Plain undone") || strings.Count(rows, "=") != len(want) {
		t.Fatal("allocations", rows)
	}
	var unapplied int64
	if err := db.Admin.QueryRow(db.Context, "SELECT public.unapplied_amount($1,$2,$3,$4)", args...).Scan(&unapplied); err != nil || unapplied != 0 {
		t.Fatal("unapplied", unapplied, err)
	}
}

// A Book restored from its own download keeps its costs' pairs but not the
// link to the other Book. Upgrading still freezes what its payments
// repaid, along the account its costs were owed in, and nothing it shows
// moves.
func TestUpgradeFreezesABookWithoutItsLink(t *testing.T) {
	db, ids := releasedLinked(t)
	args := []any{ids["tenant"], ids["kit"], ids["studio"], ids["payable"]}
	var before string
	if err := db.Admin.QueryRow(db.Context, openRead, args...).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// As a restore of the one Book leaves it: the link isn't there.
	tx, err := db.Admin.Begin(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SET LOCAL session_replication_role = replica", "DELETE FROM public.book_link_terms", "DELETE FROM public.book_links"} {
		if _, err := tx.Exec(db.Context, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(db.Context); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := db.Admin.QueryRow(db.Context, openRead, args...).Scan(&after); err != nil {
		t.Fatal(err)
	}
	var frozen int
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.allocations WHERE provenance='frozen_at_migration'").Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	if after != before || frozen == 0 {
		t.Fatal("open amounts moved without the link", before, after, frozen)
	}
}
