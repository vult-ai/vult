package ledger

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const accountColumns = `id::text,class,code,name,revision,coalesce(currency,''),native_scale,
 coalesce(currency_definition,''),coalesce(role,''),currency_revision,native_origin_revision,role_revision,
 coalesce(parent_account_id::text,''),CASE status WHEN 'active' THEN '' ELSE status END,heading`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Class, &a.Code, &a.Name, &a.Revision, &a.Currency, &a.Scale,
		&a.CurrencyDefinition, &a.Role, &a.CurrencyRevision, &a.NativeOriginRevision, &a.RoleRevision, &a.ParentAccountID, &a.Status, &a.Heading)
	return a, err
}

func readAccount(ctx context.Context, tx pgx.Tx, s Scope, id string) (Account, error) {
	return scanAccount(tx.QueryRow(ctx, "SELECT "+accountColumns+" FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, id))
}

func validAccountRole(class, role string) bool {
	switch role {
	case "":
		return true
	case "cash", "receivable", "fixed_asset", "investment":
		return class == "asset"
	case "card", "loan", "payable":
		return class == "liability"
	case "cost_of_sales":
		return class == "expense"
	case "opening_balance":
		return class == "equity"
	}
	return false
}

func createAccount(ctx context.Context, tx pgx.Tx, s Scope, q CreateAccount, receipt string) (json.RawMessage, error) {
	if (q.Currency == "") != (q.Scale == nil) || q.Currency != "" && !validNativeUnit(q.Currency, q.Scale) ||
		!validAccountRole(q.Class, q.Role) || q.NativeOrigin != "" && q.NativeOrigin != "empty_ledger" ||
		q.NativeOrigin != "" && q.Currency == "" {
		return nil, ErrInvalid
	}
	a := Account{ID: ID(), Class: q.Class, Code: q.Code, Name: q.Name, Revision: 1,
		Currency: q.Currency, Scale: q.Scale, Role: q.Role}
	if q.Currency != "" {
		a.CurrencyDefinition, a.CurrencyRevision = currencyUnitVersion, &a.Revision
		if q.NativeOrigin == "empty_ledger" {
			a.NativeOriginRevision = &a.Revision
		}
	}
	if q.Role != "" {
		a.RoleRevision = &a.Revision
	}
	if _, err := tx.Exec(ctx, "INSERT INTO public.accounts(tenant_id,book_id,id,class) VALUES($1,$2,$3,$4)", s.TenantID, s.BookID, a.ID, a.Class); err != nil {
		return nil, err
	}
	return insertAccountRevision(ctx, tx, s, a, receipt)
}

func reviseAccount(ctx context.Context, tx pgx.Tx, s Scope, q ReviseAccount, receipt string) (json.RawMessage, error) {
	a, err := readAccount(ctx, tx, s, q.AccountID)
	if err != nil {
		return nil, err
	}
	if a.Revision != q.ExpectedRevision {
		return nil, ErrConflict
	}
	if q.Currency == nil && q.Scale != nil || q.Currency != nil && !validNativeUnit(*q.Currency, q.Scale) ||
		q.Role != nil && !validAccountRole(a.Class, *q.Role) {
		return nil, ErrInvalid
	}
	a.Revision++
	a.Code, a.Name = q.Code, q.Name
	if q.Currency != nil && (*q.Currency != a.Currency || !equalScale(q.Scale, a.Scale)) {
		a.Currency, a.Scale, a.CurrencyDefinition = *q.Currency, q.Scale, currencyUnitVersion
		r := a.Revision
		a.CurrencyRevision, a.NativeOriginRevision = &r, nil
	}
	if q.ParentAccountID != nil {
		if *q.ParentAccountID != "" && !ValidID(*q.ParentAccountID) {
			return nil, ErrInvalid
		}
		a.ParentAccountID = *q.ParentAccountID
	}
	if q.Heading != nil {
		a.Heading = *q.Heading
	}
	if q.Role != nil && *q.Role != a.Role {
		a.Role = *q.Role
		r := a.Revision
		a.RoleRevision = &r
	}
	return insertAccountRevision(ctx, tx, s, a, receipt)
}

func insertAccountRevision(ctx context.Context, tx pgx.Tx, s Scope, a Account, receipt string) (json.RawMessage, error) {
	return raw(a), recordAccountRevision(ctx, tx, s, a, receipt, "")
}

// recordAccountRevision stores one revision. Parent, status and transfer are
// written only when set, so an unchanged account keeps its original shape.
func recordAccountRevision(ctx context.Context, tx pgx.Tx, s Scope, a Account, receipt, transfer string) error {
	columns := "tenant_id,book_id,account_id,revision,code,name,actor_id,currency,native_scale,currency_definition,role,currency_revision,native_origin_revision,role_revision,receipt_id"
	values := "$1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,nullif($10,''),nullif($11,''),$12,$13,$14,$15"
	args := []any{s.TenantID, s.BookID, a.ID, a.Revision, a.Code, a.Name, s.ActorID, a.Currency, a.Scale,
		a.CurrencyDefinition, a.Role, a.CurrencyRevision, a.NativeOriginRevision, a.RoleRevision, receipt}
	status := a.Status
	if status == "" {
		status = "active"
	}
	for _, extra := range []struct {
		column, value, cast string
		set                 bool
	}{{"parent_account_id", a.ParentAccountID, "uuid", a.ParentAccountID != "" || a.Revision > 1},
		{"status", status, "text", status != "active" || a.Revision > 1},
		{"transfer_entry_id", transfer, "uuid", transfer != ""},
		{"heading", fmt.Sprint(a.Heading), "boolean", a.Heading || a.Revision > 1}} {
		if !extra.set {
			continue
		}
		args = append(args, extra.value)
		columns += "," + extra.column
		values += fmt.Sprintf(",nullif($%d,'')::%s", len(args), extra.cast)
	}
	_, err := tx.Exec(ctx, "INSERT INTO public.account_revisions("+columns+") VALUES("+values+")", args...)
	return err
}
