package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/ledger"
)

func TestCurrencyDatabaseRejectsUnrepresentableJSONAndMissingBasis(t *testing.T) {
	for _, mode := range []string{"valid", "numeric-context-amount", "numeric-source", "numeric-rate", "missing-basis", "missing-counterpart-basis"} {
		t.Run(mode, func(t *testing.T) {
			db, _, s, debit, credit := setup(t)
			tx := scopedTransaction(t, db, s, pgx.ReadCommitted)
			entry := ledger.Entry{ID: ledger.ID(), ReceiptID: ledger.ID(), ActorID: s.ActorID, EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example direct posting"}
			err := tx.QueryRow(db.Context, `INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description)
 VALUES($1,$2,$3,1,$4,$5,$6,$7,$8)RETURNING sequence,recorded_at`, s.TenantID, s.BookID, entry.ID, entry.ReceiptID, s.ActorID, entry.EconomicDate, entry.AccountingDate, entry.Description).Scan(&entry.Sequence, &entry.RecordedAt)
			if err != nil {
				t.Fatal(err)
			}
			request := map[string]any{"request_key": mode, "economic_date": entry.EconomicDate, "accounting_date": entry.AccountingDate, "description": entry.Description}
			var inputLines, resultLines []map[string]any
			for i, account := range []string{debit.ID, credit.ID} {
				side := []string{"debit", "credit"}[i]
				id := ledger.ID()
				var valuation, contexts any
				if mode != "missing-basis" && !(mode == "missing-counterpart-basis" && i == 1) {
					valuation = map[string]any{"basis": "manual", "policy": "supplied-book-value-v1", "input": map[string]any{"currency": "USD", "scale": 2, "amount_minor": "1", "unit_definition": "book"}}
				}
				if i == 0 && mode != "missing-basis" {
					amount := any("1")
					if mode == "numeric-context-amount" {
						amount = 1
					}
					source := any("Example invoice")
					if mode == "numeric-source" {
						source = 1
					}
					contexts = []any{map[string]any{"role": "invoice", "amount": map[string]any{"currency": "JPY", "scale": 0, "amount_minor": amount, "unit_definition": "iso4217-2026-09-17"}, "source": source}}
					if mode == "numeric-rate" {
						valuation = map[string]any{"basis": "applied_quote", "policy": "quoted-rate-half-even-v1", "input": map[string]any{"currency": "JPY", "scale": 0, "amount_minor": "1", "unit_definition": "iso4217-2026-09-17"}, "rate": map[string]any{"base": "JPY", "quote": "USD", "rate": 0.01, "requested_date": "2026-01-01", "observation_date": "2026-01-01", "provenance": "Hypothetical quote"}}
					}
				}
				if _, err = tx.Exec(db.Context, `INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor,contexts,valuation)
 VALUES($1,$2,$3,$4,$5,$6,$7,1,$8,$9)`, s.TenantID, s.BookID, id, entry.ID, i+1, account, side, contexts, valuation); err != nil {
					t.Fatal(err)
				}
				input := map[string]any{"account_id": account, "side": side, "amount_minor": "1"}
				if valuation != nil {
					input["valuation"] = valuation
				}
				if contexts != nil {
					input["contexts"] = contexts
				}
				result := map[string]any{}
				for k, v := range input {
					result[k] = v
				}
				result["id"] = id
				inputLines = append(inputLines, input)
				resultLines = append(resultLines, result)
			}
			request["lines"] = inputLines
			var result map[string]any
			if json.Unmarshal(value(entry), &result) != nil {
				t.Fatal("invalid entry")
			}
			result["lines"] = resultLines
			if _, err = tx.Exec(db.Context, `INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 VALUES($1,$2,$3,$4,$5,'post_journal',$6,$7)`, s.TenantID, s.BookID, entry.ReceiptID, s.ActorID, mode, []byte(value(request)), []byte(value(result))); err != nil {
				t.Fatal(err)
			}
			err = tx.Commit(db.Context)
			if mode == "valid" {
				if err != nil {
					t.Fatal("valid direct producer rejected", err)
				}
			} else {
				var failure *pgconn.PgError
				if !errors.As(err, &failure) || failure.Code != "23514" {
					t.Fatal("malformed stored measurement escaped database checks", mode, err)
				}
			}
		})
	}
}

func TestCurrencyAccountReceiptsBindOmissionsAndTarget(t *testing.T) {
	for _, mode := range []string{"currency", "role", "account", "revision"} {
		t.Run(mode, func(t *testing.T) {
			db, _, s, _, _ := setup(t)
			a := measuredAccount(t, db, s, "1200", "JPY", 0, true)
			q := ledger.ReviseAccount{RequestKey: "rename", AccountID: a.ID, ExpectedRevision: 1, Code: a.Code, Name: "Renamed account"}
			result := a
			result.Name = q.Name
			result.Revision = 2
			if mode == "currency" {
				result.Currency = "EUR"
				result.Scale = currencyScale(2)
				r := int64(2)
				result.CurrencyRevision = &r
				result.NativeOriginRevision = nil
			}
			if mode == "role" {
				result.Role = "receivable"
				r := int64(2)
				result.RoleRevision = &r
			}
			if mode == "account" {
				q.AccountID = ledger.ID()
			}
			if mode == "revision" {
				q.ExpectedRevision = 999
			}
			tx := scopedTransaction(t, db, s, pgx.ReadCommitted)
			receipt := ledger.ID()
			_, err := tx.Exec(db.Context, `INSERT INTO public.account_revisions(tenant_id,book_id,account_id,revision,code,name,actor_id,currency,native_scale,currency_definition,role,currency_revision,native_origin_revision,role_revision,receipt_id)
 VALUES($1,$2,$3,2,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, s.TenantID, s.BookID, a.ID, result.Code, result.Name, s.ActorID, result.Currency, result.Scale, result.CurrencyDefinition, result.Role, result.CurrencyRevision, result.NativeOriginRevision, result.RoleRevision, receipt)
			if err != nil {
				t.Fatal("test did not reach deferred receipt check", err)
			}
			_, err = tx.Exec(db.Context, `INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)VALUES($1,$2,$3,$4,'rename','revise_account',$5,$6)`, s.TenantID, s.BookID, receipt, s.ActorID, []byte(value(q)), []byte(value(result)))
			if err != nil {
				t.Fatal(err)
			}
			var failure *pgconn.PgError
			if err = tx.Commit(db.Context); !errors.As(err, &failure) || failure.Code != "23514" {
				t.Fatal("account receipt silently changed request meaning", mode, err)
			}
			tx.Rollback(context.Background())
		})
	}
}
