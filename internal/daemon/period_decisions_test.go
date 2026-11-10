package daemon

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestNativePeriodCloseRecordsAnAttributedDecision(t *testing.T) {
	db := testutil.New(t)
	book, scope := db.Book(t, "Example Book")
	actor := ledger.ID()
	if _, err := db.Admin.Exec(db.Context, "INSERT INTO access.identities(id,issuer,subject) VALUES($1,$2,$3)", actor, access.BuiltinIssuer, localOwnerSubject); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(db.Context, "INSERT INTO access.owners(tenant_id,identity_id) VALUES($1,$2)", book.TenantID, actor); err != nil {
		t.Fatal(err)
	}
	bank, fees := db.Account(t, scope, "1000", "asset"), db.Account(t, scope, "6600", "expense")
	db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "fee", EconomicDate: "2026-01-12", AccountingDate: "2026-01-12", Description: "Example bank fee", Lines: []ledger.PostLine{
		{AccountID: fees.ID, Side: "debit", AmountMinor: "100"}, {AccountID: bank.ID, Side: "credit", AmountMinor: "100"},
	}})
	s := &Server{Admin: db.Owner}
	call := func(reason string) (json.RawMessage, error) {
		payload := marshal(local.PeriodCloseRequest{BookID: book.ID, RequestKey: "native-close", From: "2026-01-01", Through: "2026-01-31", Reason: reason})
		grant, err := s.authorize(0, marshal(local.Authorization{Action: "period_close", Digest: local.Digest(payload), UID: 501}))
		if err != nil {
			return nil, err
		}
		var authorization struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(grant, &authorization); err != nil {
			return nil, err
		}
		return s.Handle(db.Context, 501, local.Request{Method: "period_close", Authorization: authorization.Token, Payload: payload})
	}
	var refused *ledger.RefusedError
	if _, err := call(""); !errors.As(err, &refused) {
		t.Fatal("native close accepted open review items without a reason", err)
	}
	result, err := call("The supporting document is expected next week")
	if err != nil {
		t.Fatal(err)
	}
	var period ledger.AccountingPeriod
	if json.Unmarshal(result, &period) != nil || len(period.Events) != 1 || period.Events[0].ActorID != actor ||
		period.Events[0].BeforeFoundations || period.Events[0].Reason == "" || period.Events[0].OpenItems["entries_without_evidence"] != 1 || period.Events[0].ReportSHA256 == "" {
		t.Fatal("native close omitted the owner's decision and review", period)
	}
	var legacy int
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.closed_periods").Scan(&legacy); err != nil || legacy != 0 {
		t.Fatal("native close recorded a historical close", legacy, err)
	}
}
