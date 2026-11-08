package ledger

import (
	"context"
	"math/big"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type AccountDetailQuery struct {
	AccountIDs               []string
	From, To, Mode, Revision string
	Offset, Limit            int
}
type AccountReadLine struct {
	ID           string `json:"id"`
	EntryID      string `json:"transaction_id"`
	Date         string `json:"date"`
	EconomicDate string `json:"economic_date"`
	PostingDate  string `json:"posting_date"`
	RecordedAt   string `json:"recorded_at"`
	Sequence     int64  `json:"sequence"`
	Position     int    `json:"position"`
	Description  string `json:"description"`
	Memo         string `json:"memo"`
	Debit        string `json:"debit_minor"`
	Credit       string `json:"credit_minor"`
	Balance      string `json:"balance_minor"`
}
type AccountReadLedger struct {
	AccountID    string            `json:"account_code"`
	PositiveSide string            `json:"positive_side"`
	Opening      string            `json:"opening_minor"`
	OpeningBasis string            `json:"opening_basis"`
	Debits       string            `json:"debits_minor"`
	Credits      string            `json:"credits_minor"`
	Closing      string            `json:"closing_minor"`
	PeriodChange string            `json:"period_change_minor"`
	Lines        []AccountReadLine `json:"lines"`
}
type AccountReadEntry struct {
	ID       string `json:"id"`
	Date     string `json:"date"`
	Accounts string `json:"accounts"`
	Amount   string `json:"amount_minor"`
}
type AccountDetailRead struct {
	RecordedState
	SchemaVersion  int                  `json:"schema_version"`
	Version        int                  `json:"version"`
	BookID         string               `json:"book_id"`
	Currency       string               `json:"currency"`
	Scale          int                  `json:"scale"`
	Codes          []string             `json:"account_codes"`
	From           string               `json:"from"`
	To             string               `json:"to"`
	Basis          string               `json:"basis"`
	Total          string               `json:"total_minor"`
	Earliest       string               `json:"earliest_date"`
	Revision       string               `json:"revision"`
	Offset         int                  `json:"offset"`
	Limit          int                  `json:"limit"`
	Count          int                  `json:"count"`
	Entries        []AccountReadEntry   `json:"entries"`
	Ledger         *AccountReadLedger   `json:"ledger,omitempty"`
	Activity       *AccountActivityRead `json:"activity,omitempty"`
	SourceCoverage string               `json:"source_coverage"`
}

func (a TenantAdmin) AccountDetail(ctx context.Context, book string, q AccountDetailQuery) (AccountDetailRead, error) {
	ids, err := accountIDs(q.AccountIDs)
	if err != nil {
		return AccountDetailRead{}, err
	}
	if len(ids) == 0 || !ValidDate(q.From) || !ValidDate(q.To) || q.From > q.To || q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > reportPostingLimit || !validViewRevision(q.Revision) || q.Offset > 0 && q.Revision == "" {
		return AccountDetailRead{}, ErrInvalid
	}
	if _, err := activityStart(q.To); err != nil {
		return AccountDetailRead{}, err
	}
	if q.Mode == "" {
		q.Mode = "group"
		if len(ids) == 1 {
			q.Mode = "ledger"
		}
	}
	if q.Mode != "group" && q.Mode != "ledger" || q.Mode == "ledger" && len(ids) != 1 {
		return AccountDetailRead{}, ErrInvalid
	}
	var out AccountDetailRead
	err = a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) error {
		in, err := readAccountHistory(ctx, tx, scope, header, q.To, ids, true)
		if err != nil {
			return err
		}
		out, err = buildAccountDetail(in, q, ids)
		return err
	})
	if err != nil {
		return AccountDetailRead{}, err
	}
	return out, nil
}

func buildAccountDetail(in accountHistory, q AccountDetailQuery, ids []string) (AccountDetailRead, error) {
	class := in.Accounts[0].Kind
	for _, account := range in.Accounts {
		if account.Kind != class {
			return AccountDetailRead{}, ErrInvalid
		}
	}
	basis, side, sign := "balance", "debit", int64(1)
	if class == "income" || class == "expense" {
		basis = "period"
	}
	if class == "liability" || class == "equity" || class == "income" {
		side, sign = "credit", -1
	}
	revision := archiveHash(raw(struct{ Population, From, Mode string }{in.Revision, q.From, q.Mode}))
	if q.Revision != "" && q.Revision != revision {
		return AccountDetailRead{}, ErrConflict
	}
	out := AccountDetailRead{RecordedState: in.State, SchemaVersion: 1, Version: 1, BookID: in.Book.ID, Currency: in.Book.Currency, Scale: in.Book.Scale, Codes: ids, From: q.From, To: q.To, Basis: basis, Revision: revision, Offset: q.Offset, Limit: q.Limit, Entries: []AccountReadEntry{}, SourceCoverage: "unknown"}
	opening, debits, credits, running := new(big.Int), new(big.Int), new(big.Int), new(big.Int)
	names := map[string]string{}
	for _, account := range in.Accounts {
		names[account.ID] = account.Name
	}
	groupAmounts := map[string]*big.Int{}
	groupEntries := map[string]AccountReadEntry{}
	groupNames := map[string]map[string]bool{}
	lines := []AccountReadLine{}
	for _, line := range in.Lines {
		amount, ok := new(big.Int).SetString(line.Amount, 10)
		if !ok || amount.Sign() < 0 {
			return AccountDetailRead{}, ErrDenied
		}
		effect := new(big.Int).Set(amount)
		if line.Side == "credit" {
			effect.Neg(effect)
		} else if line.Side != "debit" {
			return AccountDetailRead{}, ErrDenied
		}
		effect.Mul(effect, big.NewInt(sign))
		running.Add(running, effect)
		if out.Earliest == "" || line.Date < out.Earliest {
			out.Earliest = line.Date
		}
		if line.Date < q.From {
			opening.Add(opening, effect)
		} else {
			if line.Side == "debit" {
				debits.Add(debits, amount)
			} else {
				credits.Add(credits, amount)
			}
			if q.Mode == "ledger" {
				row := AccountReadLine{ID: line.ID, EntryID: line.EntryID, Date: line.Date, EconomicDate: line.EconomicDate, PostingDate: line.Date, RecordedAt: line.RecordedAt, Sequence: line.Sequence, Position: line.Position, Description: line.Description, Memo: line.Memo, Debit: "0", Credit: "0", Balance: running.String()}
				if line.Side == "debit" {
					row.Debit = line.Amount
				} else {
					row.Credit = line.Amount
				}
				lines = append(lines, row)
			}
		}
		if q.Mode == "group" && (basis == "balance" || line.Date >= q.From) {
			if groupAmounts[line.EntryID] == nil {
				groupAmounts[line.EntryID] = new(big.Int)
				groupEntries[line.EntryID] = AccountReadEntry{ID: line.EntryID, Date: line.Date}
				groupNames[line.EntryID] = map[string]bool{}
			}
			groupAmounts[line.EntryID].Add(groupAmounts[line.EntryID], effect)
			groupNames[line.EntryID][names[line.AccountID]] = true
		}
	}
	period := new(big.Int).Sub(running, opening)
	out.Total = running.String()
	if basis == "period" {
		out.Total = period.String()
	}
	if q.Mode == "ledger" {
		out.Count = len(lines)
		if q.Offset > out.Count {
			return AccountDetailRead{}, ErrInvalid
		}
		out.Ledger = &AccountReadLedger{AccountID: ids[0], PositiveSide: side, Opening: opening.String(), OpeningBasis: "recorded_prior_postings", Debits: debits.String(), Credits: credits.String(), Closing: running.String(), PeriodChange: period.String(), Lines: lines[q.Offset:min(out.Count, q.Offset+q.Limit)]}
	} else {
		entries := make([]AccountReadEntry, 0, len(groupEntries))
		for id, row := range groupEntries {
			labels := make([]string, 0, len(groupNames[id]))
			for name := range groupNames[id] {
				labels = append(labels, name)
			}
			sort.Strings(labels)
			row.Amount = groupAmounts[id].String()
			row.Accounts = strings.Join(labels, " · ")
			entries = append(entries, row)
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Date != entries[j].Date {
				return entries[i].Date > entries[j].Date
			}
			return entries[i].ID < entries[j].ID
		})
		out.Count = len(entries)
		if q.Offset > out.Count {
			return AccountDetailRead{}, ErrInvalid
		}
		out.Entries = entries[q.Offset:min(out.Count, q.Offset+q.Limit)]
	}
	if q.Offset == 0 {
		activity, err := buildAccountActivity(in)
		if err != nil {
			return AccountDetailRead{}, err
		}
		out.Activity = &activity
	}
	return out, nil
}
