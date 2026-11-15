//go:build devserver && browserpreview

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// A seed file of earlier bank activity for a Book the development Vult
// already has: per connected bank, items and the account each was recorded
// to, named by its full path in the chart ("Travel:Flights").
type historySeed struct {
	Book  string `json:"book"`
	Banks []struct {
		Institution string `json:"institution"`
		Account     string `json:"account"`
		History     []struct {
			ID           string `json:"id"`
			Date         string `json:"date"`
			Amount       int64  `json:"amount"`
			Description  string `json:"description"`
			Counterparty string `json:"counterparty"`
			To           string `json:"to"`
		} `json:"history"`
	} `json:"banks"`
}

// TestDevSeedHistory adds earlier bank activity to one Book of the
// development Vult and records each item to the account the seed names,
// through the ledger's own operations, so the Book has a past to read.
// VULT_DEV_SEED_FILE names the seed. Items already there are left alone.
func TestDevSeedHistory(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile(os.Getenv("VULT_DEV_SEED_FILE"))
	if err != nil {
		t.Fatal("VULT_DEV_SEED_FILE names the seed file: ", err)
	}
	var seed historySeed
	if err := json.Unmarshal(raw, &seed); err != nil || seed.Book == "" {
		t.Fatalf("the seed file can't be read: %v", err)
	}
	f := seedFixture(t, openDevEnv(t, false))
	defer func() { _ = f.sessions.EndSession(ctx, f.session) }()
	book, views, scope, byPath, done := devSeedBook(t, f, seed.Book)
	defer done()

	recorded, refused, unknown := 0, 0, 0
	for _, b := range seed.Banks {
		var login string
		for _, v := range views {
			if v.InstitutionName == b.Institution {
				login = v.ID
			}
		}
		if login == "" {
			t.Fatalf("%s has no connected bank named %q", seed.Book, b.Institution)
		}
		var cursor string
		if err := f.db.Admin.QueryRow(ctx, `SELECT sync_cursor FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3`, f.tenant, book.ID, login).Scan(&cursor); err != nil {
			t.Fatal(err)
		}
		target := ledger.BankSyncTarget{TenantID: f.tenant, BookID: book.ID, ConnectionID: login}
		const page = 200
		for start := 0; start < len(b.History); start += page {
			end := min(start+page, len(b.History))
			var added []ledger.BankSyncTransaction
			for _, x := range b.History[start:end] {
				added = append(added, ledger.BankSyncTransaction{ProviderAccountID: b.Account, ProviderTransactionID: x.ID, Date: x.Date, AmountMinor: x.Amount,
					Currency: book.Currency, Scale: 2, Description: x.Description, Counterparty: x.Counterparty})
			}
			next := fmt.Sprintf("history-%s-%d", b.Account, end)
			if _, err := f.db.Store.ApplyBankSync(ctx, target, ledger.BankSyncBatch{FromCursor: cursor, NextCursor: next, Complete: end == len(b.History), Added: added}); err != nil {
				t.Fatalf("adding %s history: %v", b.Institution, err)
			}
			cursor = next
		}
		ids := map[string]string{}
		rows, err := f.db.Admin.Query(ctx, `SELECT t.provider_transaction_id, t.id::text FROM public.bank_transactions t
 WHERE t.tenant_id=$1 AND t.book_id=$2 AND NOT EXISTS(SELECT 1 FROM public.bank_transaction_links k WHERE k.tenant_id=t.tenant_id AND k.book_id=t.book_id AND k.transaction_id=t.id AND k.revoked_at IS NULL)`, f.tenant, book.ID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var provider, id string
			if err := rows.Scan(&provider, &id); err != nil {
				t.Fatal(err)
			}
			ids[provider] = id
		}
		rows.Close()
		for _, x := range b.History {
			id, other := ids[x.ID], byPath[strings.ToLower(x.To)]
			if id == "" {
				continue
			}
			if other == "" {
				unknown++
				continue
			}
			q, _ := json.Marshal(ledger.RecordBankActivity{RequestKey: "history-" + x.ID, TransactionID: id, Description: x.Description, OtherAccountID: other})
			if _, err := f.db.Store.Call(ctx, scope, "record_bank_activity", q); err != nil {
				refused++
				if refused <= 5 {
					fmt.Printf("not recorded: %s %s to %s: %v\n", x.Date, x.Description, x.To, err)
				}
				continue
			}
			recorded++
		}
	}
	fmt.Printf("DEV HISTORY %s: recorded %d, not recorded %d, account not in the chart %d\n", seed.Book, recorded, refused, unknown)
}

// devSeedBook finds the named Book of the development Vult and opens a
// connection that writes to it as its earlier books, with the chart by full
// path ("Travel:Flights"). done revokes the connection.
func devSeedBook(t *testing.T, f *documentServerFixture, name string) (ledger.Book, []ledger.BankConnectionView, ledger.Scope, map[string]string, func()) {
	t.Helper()
	ctx := context.Background()

	var book ledger.Book
	var views []ledger.BankConnectionView
	var c ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		books, err := o.Books(ctx)
		if err != nil {
			return err
		}
		for _, b := range books {
			if b.Name == name {
				if book.ID != "" {
					return fmt.Errorf("%q names more than one Book", name)
				}
				book = b
			}
		}
		if book.ID == "" {
			return fmt.Errorf("no Book is named %q", name)
		}
		if views, err = o.BankConnections(ctx, book.ID); err != nil {
			return err
		}
		// A run that was cut short left its connection; carry on with it.
		open, err := o.ActiveConnectionsBounded(ctx, 1000)
		if err != nil {
			return err
		}
		for _, x := range open {
			if x.BookID == book.ID && x.Name == "Earlier books" && x.Access == "write" {
				c = x
				return nil
			}
		}
		c, err = o.CreateConnection(ctx, book.ID, "Earlier books", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	done := func() {
		_ = f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error { return o.Revoke(ctx, c.ID) })
	}

	// The chart by full path, as the seed names accounts.
	var listed struct {
		Accounts []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Parent string `json:"parent_account_id"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(seedCall(t, f, scope, "list_accounts", map[string]any{}, name), &listed); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, a := range listed.Accounts {
		byID[a.ID] = i
	}
	byPath := map[string]string{}
	for _, a := range listed.Accounts {
		path, at := a.Name, a
		for at.Parent != "" {
			at = listed.Accounts[byID[at.Parent]]
			path = at.Name + ":" + path
		}
		byPath[strings.ToLower(path)] = a.ID
	}

	return book, views, scope, byPath, done
}

// A seed file of balances the earlier books ended with: each account, by full
// path in the chart, and its balance on a date, debits positive, in minor
// units. Into names the equity account that takes what the balances don't
// explain, such as earnings the Book's own history leaves out.
type balanceSeed struct {
	Book     string           `json:"book"`
	Through  string           `json:"through"`
	Into     string           `json:"into"`
	Balances map[string]int64 `json:"balances"`
}

// TestDevSeedBalances posts one entry on the seed's date that brings each
// account it names to that balance, through the ledger's own operations, so
// a Book seeded with only part of its past starts the rest from the right
// place. VULT_DEV_SEED_FILE names the seed; VULT_DEV_DRY=1 only prints the
// entry.
func TestDevSeedBalances(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile(os.Getenv("VULT_DEV_SEED_FILE"))
	if err != nil {
		t.Fatal("VULT_DEV_SEED_FILE names the seed file: ", err)
	}
	var seed balanceSeed
	if err := json.Unmarshal(raw, &seed); err != nil || seed.Book == "" || !ledger.ValidDate(seed.Through) || seed.Into == "" {
		t.Fatalf("the seed file can't be read: %v", err)
	}
	f := seedFixture(t, openDevEnv(t, false))
	defer func() { _ = f.sessions.EndSession(ctx, f.session) }()
	_, _, scope, byPath, done := devSeedBook(t, f, seed.Book)
	defer done()
	into := devAccount(byPath, seed.Into)
	if into == "" {
		t.Fatalf("no account is named %q", seed.Into)
	}
	// Balances carry over to the balance sheet only: never into income or
	// expenses, where they'd count as the period's earnings.
	var listed struct {
		Accounts []struct {
			ID    string `json:"id"`
			Class string `json:"class"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(seedCall(t, f, scope, "list_accounts", map[string]any{}, seed.Book), &listed); err != nil {
		t.Fatal(err)
	}
	class := map[string]string{}
	for _, a := range listed.Accounts {
		class[a.ID] = a.Class
	}
	if os.Getenv("VULT_DEV_REDO") == "1" && os.Getenv("VULT_DEV_DRY") != "1" {
		devUndoBalances(t, f, scope, seed)
	}

	var trial ledger.Trial
	if err := json.Unmarshal(seedCall(t, f, scope, "get_trial_balance", ledger.TrialQuery{Through: seed.Through}, seed.Book), &trial); err != nil {
		t.Fatal(err)
	}
	now := map[string]int64{}
	foreign := map[string]bool{}
	for _, b := range trial.Balances {
		v, err := strconv.ParseInt(b.DebitPositiveMinor, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		now[b.AccountID] = v
		foreign[b.AccountID] = b.Native != nil
	}
	var lines []ledger.PostLine
	var rest int64
	named := map[string]string{}
	paths := make([]string, 0, len(seed.Balances))
	for p := range seed.Balances {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		id := devAccount(byPath, p)
		if id == "" || id == into || foreign[id] || class[id] == "income" || class[id] == "expense" {
			why := "not in the chart"
			if foreign[id] {
				why = "kept in another currency"
			} else if id != "" && id != into {
				why = "the chart has it only as " + class[id]
			}
			fmt.Printf("left to %s: %s (%s)\n", seed.Into, p, why)
			continue
		}
		if other, twice := named[id]; twice {
			t.Fatalf("%q and %q are the same account here; the seed should name it once", other, p)
		}
		named[id] = p
		change := seed.Balances[p] - now[id]
		if change == 0 {
			continue
		}
		rest -= change
		fmt.Printf("%s: %d -> %d\n", p, now[id], seed.Balances[p])
		lines = append(lines, devLine(id, change))
	}
	if rest != 0 {
		lines = append(lines, devLine(into, rest))
	}
	fmt.Printf("DEV BALANCES %s on %s: %d accounts move, %d to %s\n", seed.Book, seed.Through, len(lines)-1, rest, seed.Into)
	if os.Getenv("VULT_DEV_DRY") == "1" || len(lines) < 2 {
		return
	}
	// The key follows the lines, so a corrected seed posts what's still owed.
	sum := sha256.Sum256(fmt.Appendf(nil, "%v", lines))
	seedCall(t, f, scope, "post_journal", ledger.PostJournal{RequestKey: "balances-" + seed.Through + "-" + hex.EncodeToString(sum[:6]), EconomicDate: seed.Through, AccountingDate: seed.Through,
		Description: balancesCarried, Lines: lines}, seed.Book)
	fmt.Printf("DEV BALANCES posted\n")
}

func devLine(account string, change int64) ledger.PostLine {
	if change > 0 {
		return ledger.PostLine{AccountID: account, Side: "debit", AmountMinor: strconv.FormatInt(change, 10)}
	}
	return ledger.PostLine{AccountID: account, Side: "credit", AmountMinor: strconv.FormatInt(-change, 10)}
}

// devAccount finds an account by its full path, or else by the one path that
// ends with it, or by the one account whose own name it ends with, as the
// earlier books may nest the chart differently.
func devAccount(byPath map[string]string, path string) string {
	path = strings.ToLower(path)
	if id := byPath[path]; id != "" {
		return id
	}
	leaf := path[strings.LastIndex(path, ":")+1:]
	var ends, leaves []string
	for k, id := range byPath {
		if strings.HasSuffix(k, ":"+path) {
			ends = append(ends, id)
		}
		if k == leaf || strings.HasSuffix(k, ":"+leaf) {
			leaves = append(leaves, id)
		}
	}
	if len(ends) == 1 {
		return ends[0]
	}
	if len(leaves) == 1 {
		return leaves[0]
	}
	return ""
}

const balancesCarried = "Balances carried over from the earlier books"

// devUndoBalances reverses every entry an earlier run of the balances seed
// posted, so the next run starts from the Book's own history again.
func devUndoBalances(t *testing.T, f *documentServerFixture, scope ledger.Scope, seed balanceSeed) {
	t.Helper()
	posted, reversed := map[string]bool{}, map[string]bool{}
	var after int64
	for {
		var page struct {
			Entries   []ledger.Entry `json:"entries"`
			NextAfter int64          `json:"next_after"`
		}
		if err := json.Unmarshal(seedCall(t, f, scope, "list_journals", ledger.ListQuery{After: after, Limit: 100}, seed.Book), &page); err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Entries {
			if e.ReversalOf != "" {
				reversed[e.ReversalOf] = true
			} else if e.Description == balancesCarried {
				posted[e.ID] = true
			}
		}
		if len(page.Entries) == 0 || page.NextAfter == after {
			break
		}
		after = page.NextAfter
	}
	undone := 0
	for id := range posted {
		if reversed[id] {
			continue
		}
		seedCall(t, f, scope, "reverse_journal", ledger.ReverseJournal{RequestKey: "undo-" + id, EntryID: id, AccountingDate: seed.Through, Reason: "Carried over again"}, seed.Book)
		undone++
	}
	fmt.Printf("DEV BALANCES undid %d earlier entries\n", undone)
}
