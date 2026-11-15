//go:build devserver && browserpreview

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// TestDevMoreActivity adds bank activity to a Book the seed already
// connected to a bank, as a later page of the same sync, so there is new
// activity to sort. VULT_DEV_MORE_FILE names a file holding the Book's name,
// the bank's name and the activity.
func TestDevMoreActivity(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile(os.Getenv("VULT_DEV_MORE_FILE"))
	if err != nil {
		t.Fatal("VULT_DEV_MORE_FILE names the file of new activity: ", err)
	}
	var more struct {
		Book        string `json:"book"`
		Institution string `json:"institution"`
		Activity    []struct {
			Account      string `json:"account"`
			ID           string `json:"id"`
			Date         string `json:"date"`
			Amount       int64  `json:"amount"`
			Description  string `json:"description"`
			Counterparty string `json:"counterparty"`
		} `json:"activity"`
	}
	if err := json.Unmarshal(raw, &more); err != nil || more.Book == "" || len(more.Activity) == 0 {
		t.Fatalf("the file can't be read: %v", err)
	}
	e := openDevEnv(t, false)
	f := seedFixture(t, e)
	defer func() { _ = f.sessions.EndSession(ctx, f.session) }()

	var book ledger.Book
	var views []ledger.BankConnectionView
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
		books, err := o.Books(ctx)
		if err != nil {
			return err
		}
		for _, b := range books {
			if b.Name == more.Book {
				book = b
			}
		}
		if book.ID == "" {
			return fmt.Errorf("%s isn't here", more.Book)
		}
		views, err = o.BankConnections(ctx, book.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var login string
	for _, v := range views {
		if v.InstitutionName == more.Institution {
			login = v.ID
		}
	}
	if login == "" {
		t.Fatalf("%s has no bank named %s", more.Book, more.Institution)
	}
	var cursor string
	if err := f.db.Admin.QueryRow(ctx, "SELECT sync_cursor FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3", f.tenant, book.ID, login).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	var added []ledger.BankSyncTransaction
	for _, x := range more.Activity {
		added = append(added, ledger.BankSyncTransaction{ProviderAccountID: x.Account, ProviderTransactionID: x.ID, Date: x.Date, AmountMinor: x.Amount,
			Currency: book.Currency, Scale: 2, Description: x.Description, Counterparty: x.Counterparty})
	}
	target := ledger.BankSyncTarget{TenantID: f.tenant, BookID: book.ID, ConnectionID: login}
	if _, err := f.db.Store.ApplyBankSync(ctx, target, ledger.BankSyncBatch{FromCursor: cursor, NextCursor: fmt.Sprintf("%s-more-%s", cursor, added[0].ProviderTransactionID), Complete: true, Added: added}); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("added %d items to %s at %s\n", len(added), more.Book, more.Institution)
}
