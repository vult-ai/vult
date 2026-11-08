package ledger_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestSearchKeepsLargeBookTotalsExact(t *testing.T) {
	b := newSearchBook(t)
	b.post(t, "large", "2026-01-02", "Large allocation",
		line{"6000", "debit", "9223372036854775807", ""},
		line{"6010", "debit", "9223372036854775807", ""},
		line{"1000", "credit", "9223372036854775807", ""},
		line{"1010", "credit", "9223372036854775807", ""})
	found := b.search(t, "184467440737095516.14")
	if found.Transactions.Total != 1 || found.Transactions.Items[0].ID != b.entry["large"] || found.Transactions.Items[0].AmountMinor != "18446744073709551614" || found.Currency != "USD" || found.Scale != 2 {
		t.Fatal("entry search narrowed an exact numeric total")
	}
}

func TestSearchTyposNeverTreatAccountCodesAsNames(t *testing.T) {
	b := newSearchBook(t)
	var account ledger.Account
	if json.Unmarshal(b.db.Call(t, b.scope, "create_account", ledger.CreateAccount{RequestKey: "code-only", Class: "expense", Code: "OFFICE", Name: "Miscellaneous"}), &account) != nil {
		t.Fatal("account fixture failed")
	}
	b.account["OFFICE"] = account
	b.post(t, "name", "2026-01-02", "Name sample", expense("6000", "100", "")...)
	b.post(t, "code", "2026-01-02", "Code sample", expense("OFFICE", "100", "")...)
	found := b.search(t, "offce")
	if found.Transactions.Total != 1 || found.Transactions.Items[0].ID != b.entry["name"] || found.Accounts.Total != 1 || found.Accounts.Items[0].ID != b.account["6000"].ID {
		t.Fatal("account code gained name-only typo authority")
	}
	if found := b.search(t, "office"); found.Transactions.Total != 2 || found.Accounts.Total != 2 {
		t.Fatal("code exact/prefix matching was lost")
	}
}

func TestSearchZeroBookValueDoesNotInventNativeValue(t *testing.T) {
	db, book, scope, _, equity := setup(t)
	cash := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	q := measuredPost(cash, equity.ID, "tiny-acquisition", "1", "0")
	q.Description = "Tiny acquisition"
	quoteOn(&q, "0.001")
	var entry ledger.Entry
	if json.Unmarshal(db.Call(t, scope, "post_journal", q), &entry) != nil {
		t.Fatal("posting failed")
	}
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	found, err := owner.SearchBook(db.Context, book.ID, ledger.SearchQuery{Query: "tiny acquisition"})
	if err != nil || found.Transactions.Total != 1 || found.Transactions.Items[0].ID != entry.ID || found.Transactions.Items[0].AmountMinor != "0" || found.Transactions.Items[0].AmountRole != "book_debit_total" || found.Currency != "USD" || found.Scale != 2 {
		t.Fatal("rounded-zero Book value was lost or replaced by native money", err)
	}
	found, err = owner.SearchBook(db.Context, book.ID, ledger.SearchQuery{Query: "0"})
	if err != nil || found.Transactions.Total != 0 {
		t.Fatal("zero became an amount selector", err)
	}
	found, err = owner.SearchBook(db.Context, book.ID, ledger.SearchQuery{Query: "1"})
	if err != nil || found.Transactions.Total != 0 {
		t.Fatal("JPY 1 was misread as USD 1", err)
	}
	assertPosition(t, measuredBalance(t, db, scope, cash.ID), "1", "0")
}

func TestSearchTyposUseNamesWithoutScanningEntryVocabulary(t *testing.T) {
	b := newSearchBook(t)
	b.post(t, "dictionary", "2026-01-02", "Dictionary sample", expense("6000", "100", "")...)
	if _, err := b.db.Admin.Exec(b.db.Context, `INSERT INTO public.search_words(tenant_id,book_id,entry_id,word)
 SELECT $1,$2,$3,'vocabulary'||lpad(g::text,8,'0') FROM generate_series(1,50001) g`, b.book.TenantID, b.book.ID, b.entry["dictionary"]); err != nil {
		t.Fatal(err)
	}
	// An unrelated large history is not a name dictionary. Neither a negative
	// typo query nor an account-name match should enumerate its vocabulary.
	if found, err := b.owner.SearchBook(b.db.Context, b.book.ID, ledger.SearchQuery{Query: "zzqq"}); err != nil || found.Transactions.Total != 0 {
		t.Fatal("typo matching scanned unrelated entry vocabulary", err)
	}
	if found, err := b.owner.SearchBook(b.db.Context, b.book.ID, ledger.SearchQuery{Query: "offce"}); err != nil || found.Transactions.Total != 1 {
		t.Fatal("large history prevented an account-name typo match", err)
	}
	if found, err := b.owner.Activity(b.db.Context, b.book.ID, ledger.ActivityPageQuery{ActivityFilters: ledger.ActivityFilters{Search: "offce"}, Limit: 25}); err != nil || found.Coverage.Count != 1 {
		t.Fatal("Activity used a different name-only typo population", err)
	}
	// A direct prefix remains bounded and does not need the typo vocabulary.
	if found, err := b.owner.SearchBook(b.db.Context, b.book.ID, ledger.SearchQuery{Query: "dictionary"}); err != nil || found.Transactions.Total != 1 {
		t.Fatal("direct matching unnecessarily used the typo scan", err)
	}
}

func searchIndexRows(t testing.TB, db *testutil.Database) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, table := range []string{"search_words", "search_totals"} {
		var rows []byte
		if err := db.Admin.QueryRow(db.Context, "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM public."+table+" t").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		out[table] = rows
	}
	return out
}

func TestSearchIndexBackfillAndRestorePreserveRecordedFacts(t *testing.T) {
	legacy := testutil.NewWithInitializer(t, releasedSchema)
	book, scope := legacy.Book(t, "Example search Book")
	debit := legacy.ReleasedAccount(t, scope, "6000", "expense")
	credit := legacy.ReleasedAccount(t, scope, "2000", "liability")
	q := post(debit.ID, credit.ID, "example-office")
	q.Description = "Office equipment"
	q.Lines[0].Memo = "Replacement supplies"
	receipt := legacy.ReleasedPost(t, scope, q)
	var entry ledger.Entry
	if json.Unmarshal(receipt, &entry) != nil {
		t.Fatal("invalid released posting fixture")
	}
	prior := legacy.ReleasedSnapshot(t)
	if err := ledger.Upgrade(legacy.Context, legacy.Admin, legacy.Role); err != nil {
		t.Fatal("search upgrade failed", err)
	}
	before, err := legacy.Owner.Snapshot(legacy.Context)
	if err != nil {
		t.Fatal(err)
	}
	testutil.AssertReleasedSnapshotUpgrade(t, prior, before, book)
	index := searchIndexRows(t, legacy)
	for table := range index {
		if _, exported := before.Tables[table]; exported {
			t.Fatal("derived index became an authoritative backup table")
		}
		if bytes.Equal(index[table], []byte("[]")) {
			t.Fatal("search upgrade failed to index the released entry", table)
		}
	}
	fresh := testutil.New(t)
	if err := fresh.Owner.Restore(fresh.Context, before); err != nil {
		t.Fatal("snapshot restore failed", err)
	}
	if got := searchIndexRows(t, fresh); !bytes.Equal(got["search_words"], index["search_words"]) || !bytes.Equal(got["search_totals"], index["search_totals"]) {
		t.Fatal("restore failed to rebuild the derived index")
	}
	owner, err := fresh.Owner.ForTenant(fresh.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := owner.SearchBook(fresh.Context, book.ID, ledger.SearchQuery{Query: "supplies"}); err != nil ||
		result.Transactions.Total != 1 || len(result.Transactions.Items) != 1 || result.Transactions.Items[0].ID != entry.ID {
		t.Fatal("restored index is not usable", err)
	}
	var writable, visible bool
	if err := fresh.Runtime.QueryRow(fresh.Context, `SELECT
 has_table_privilege(current_user,'public.search_words','INSERT,UPDATE,DELETE') OR has_table_privilege(current_user,'public.search_totals','INSERT,UPDATE,DELETE'),
 EXISTS(SELECT 1 FROM public.search_words) OR EXISTS(SELECT 1 FROM public.search_totals)`).Scan(&writable, &visible); err != nil || writable || visible {
		t.Fatal("restricted index grants or unscoped row isolation changed", err)
	}
}
