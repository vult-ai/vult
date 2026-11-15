package ledger

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

// bankTypeRoles is the account role that stands for each kind of bank
// account the bank names.
var bankTypeRoles = map[string]string{"checking": "cash", "savings": "cash", "credit card": "card"}

// bankTypeCodes lists the codes the current starter charts give the account
// of each bank type, such as a chart's Checking or Savings.
func bankTypeCodes(bankType string) []string {
	var codes []string
	for _, chart := range currentStarterCharts {
		for _, a := range chart.Accounts {
			if a.BankType == bankType && !slices.Contains(codes, a.Code) {
				codes = append(codes, a.Code)
			}
		}
	}
	return codes
}

// suggestBankLink proposes the Book account a bank account not yet linked
// should be linked to: by the bank's type and the account's role and code,
// never by name. It prefers the starter chart's account of that bank type,
// then the first account of the matching role by code, and skips accounts
// already linked to a bank account, retired or headings. No suggestion is
// an empty account.
func suggestBankLink(ctx context.Context, tx pgx.Tx, s Scope, bankAccount string) (bankType, account string, err error) {
	var kind string
	if err = tx.QueryRow(ctx, `SELECT lower(coalesce(provider_subtype,'')),coalesce(kind,'') FROM public.bank_accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3`,
		s.TenantID, s.BookID, bankAccount).Scan(&bankType, &kind); err != nil {
		return "", "", dbError(err)
	}
	role, known := bankTypeRoles[bankType]
	if !known {
		bankType = ""
		role = kind
	}
	if role != "cash" && role != "card" && role != "loan" {
		return bankType, "", nil
	}
	err = tx.QueryRow(ctx, `SELECT coalesce((SELECT a.id::text FROM public.accounts a
 LEFT JOIN LATERAL (SELECT x.code FROM public.account_revisions x WHERE x.tenant_id=a.tenant_id AND x.book_id=a.book_id AND x.account_id=a.id ORDER BY x.revision DESC LIMIT 1) r ON true
 WHERE a.tenant_id=$1 AND a.book_id=$2 AND a.role=$3 AND a.status IS DISTINCT FROM 'retired' AND NOT a.heading
 AND NOT EXISTS(SELECT 1 FROM public.bank_accounts b WHERE b.tenant_id=a.tenant_id AND b.book_id=a.book_id AND b.account_id=a.id)
 ORDER BY coalesce(r.code,'')=ANY($4) DESC,coalesce(r.code,''),a.id LIMIT 1),'')`, s.TenantID, s.BookID, role, bankTypeCodes(bankType)).Scan(&account)
	return bankType, account, dbError(err)
}
