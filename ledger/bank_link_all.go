package ledger

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SuggestedAccount is a Book account to create for a bank account that has
// none to link to. Its class and role follow the bank's type: a checking or
// savings account is cash (an asset), a card or a loan a liability.
type SuggestedAccount struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Class string `json:"class"`
	Role  string `json:"role"`
}

// roleClasses is the class of the Book account each bank role stands for.
var roleClasses = map[string]string{"cash": "asset", "card": "liability", "loan": "liability"}

// suggestBankCreate proposes the Book account to create for a bank account
// with nothing to link to: the starter chart's code for its type when that
// code is free, and the bank's own name for it.
func suggestBankCreate(ctx context.Context, tx pgx.Tx, s Scope, bankAccount, bankType string) (*SuggestedAccount, error) {
	var name, mask, kind string
	if err := tx.QueryRow(ctx, "SELECT name,mask,coalesce(kind,'') FROM public.bank_accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3",
		s.TenantID, s.BookID, bankAccount).Scan(&name, &mask, &kind); err != nil {
		return nil, dbError(err)
	}
	role, known := bankTypeRoles[bankType]
	if !known {
		role = kind
	}
	class, ok := roleClasses[role]
	if !ok {
		return nil, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = map[string]string{"cash": "Bank account", "card": "Credit card", "loan": "Loan"}[role]
	}
	if mask != "" {
		name += " ••" + mask
	}
	out := &SuggestedAccount{Name: clip(name, 200), Class: class, Role: role}
	var used []string
	rows, err := tx.Query(ctx, `SELECT r.code FROM public.accounts a JOIN LATERAL (SELECT x.code FROM public.account_revisions x
 WHERE x.tenant_id=a.tenant_id AND x.book_id=a.book_id AND x.account_id=a.id ORDER BY x.revision DESC LIMIT 1) r ON true
 WHERE a.tenant_id=$1 AND a.book_id=$2 AND r.code<>''`, s.TenantID, s.BookID)
	if err != nil {
		return nil, dbError(err)
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		used = append(used, code)
	}
	rows.Close()
	for _, code := range bankTypeCodes(bankType) {
		if !slices.Contains(used, code) {
			out.Code = code
			break
		}
	}
	return out, dbError(rows.Err())
}

// BankAccountLinkChoice links one bank account: to an existing Book
// account, or to a new one the owner accepted (Create).
type BankAccountLinkChoice struct {
	BankAccountID string            `json:"bank_account_id"`
	AccountID     string            `json:"account_id,omitempty"`
	Create        *SuggestedAccount `json:"create,omitempty"`
}

// BankAccountLinked is one bank account's link, with the Book account it
// now records to.
type BankAccountLinked struct {
	BankAccountID string `json:"bank_account_id"`
	AccountID     string `json:"account_id"`
	Created       bool   `json:"created"`
}

// LinkBankAccounts links several bank accounts at once, creating the Book
// accounts the owner accepted: all of them, or none. Each link and each new
// account is recorded as the owner's own change, as one at a time would be.
func (a TenantAdmin) LinkBankAccounts(ctx context.Context, book, actor string, choices []BankAccountLinkChoice) ([]BankAccountLinked, error) {
	if !ValidID(actor) || len(choices) < 1 || len(choices) > 50 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range choices {
		if !ValidID(c.BankAccountID) || seen[c.BankAccountID] || (c.AccountID == "") == (c.Create == nil) || c.AccountID != "" && !ValidID(c.AccountID) {
			return nil, ErrInvalid
		}
		if c.Create != nil && (strings.TrimSpace(c.Create.Name) == "" || roleClasses[c.Create.Role] != c.Create.Class) {
			return nil, ErrInvalid
		}
		seen[c.BankAccountID] = true
	}
	out := []BankAccountLinked{}
	err := a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		scope.ActorID = actor
		if err := setScope(ctx, tx, scope); err != nil {
			return dbError(err)
		}
		var allowed bool
		if err := tx.QueryRow(ctx, "SELECT public.ledger_import_owner() OR access.owner_decision_allowed($1,$2)", scope.TenantID, scope.BookID).Scan(&allowed); err != nil || !allowed {
			return ErrFreshOwnerProof
		}
		if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); err != nil {
			return dbError(err)
		}
		for _, c := range choices {
			linked := BankAccountLinked{BankAccountID: c.BankAccountID, AccountID: c.AccountID}
			if c.Create != nil {
				result, err := dispatch(ctx, tx, scope, "create_account", raw(CreateAccount{RequestKey: "link-all-account-" + ID(), Class: c.Create.Class,
					Code: c.Create.Code, Name: strings.TrimSpace(c.Create.Name), Role: c.Create.Role}), nil)
				if err != nil {
					return err
				}
				var created Account
				if json.Unmarshal(result, &created) != nil || created.ID == "" {
					return ErrUncertain
				}
				linked.AccountID, linked.Created = created.ID, true
			}
			if _, err := dispatch(ctx, tx, scope, "link_bank_account", raw(LinkBankAccountChange{RequestKey: "link-all-" + ID(),
				BankAccountID: c.BankAccountID, AccountID: linked.AccountID}), nil); err != nil {
				return err
			}
			out = append(out, linked)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
