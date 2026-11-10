package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RetireAccount retires an account. An account that still holds money names
// where it went: Vult records the transfer of the exact remaining balance and
// the retirement together, or neither.
type RetireAccount struct {
	RequestKey           string        `json:"request_key"`
	AccountID            string        `json:"account_id"`
	ExpectedRevision     int64         `json:"expected_revision"`
	DestinationAccountID string        `json:"destination_account_id,omitempty"`
	TransferDate         string        `json:"transfer_date,omitempty"`
	Description          string        `json:"description,omitempty"`
	Sources              []SourceInput `json:"sources,omitempty"`
}

// ReactivateAccount returns a retired account to use with a new revision.
type ReactivateAccount struct {
	RequestKey       string `json:"request_key"`
	AccountID        string `json:"account_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// RetiredAccount is the recorded retirement and the transfer, if any, that
// emptied the account.
type RetiredAccount struct {
	Account  Account `json:"account"`
	Transfer *Entry  `json:"transfer"`
}

// RemainingBalance is one unit of money an account still holds.
type RemainingBalance struct {
	Currency    string `json:"currency"`
	Scale       int    `json:"scale"`
	AmountMinor string `json:"amount_minor"`
}

// RemainingBalanceError refuses a retirement that doesn't say where the
// remaining money went. It is an invalid request with the amounts attached.
type RemainingBalanceError struct {
	AccountID string             `json:"account_id"`
	Remaining []RemainingBalance `json:"remaining"`
}

func (e *RemainingBalanceError) Error() string {
	parts := []string{}
	for _, r := range e.Remaining {
		parts = append(parts, formatMinor(r.AmountMinor, r.Scale)+" "+r.Currency)
	}
	return "This account still holds " + strings.Join(parts, " and ") +
		". Say where the money went: give destination_account_id and transfer_date, and Vult records the transfer with the retirement."
}

func (e *RemainingBalanceError) Unwrap() error { return ErrInvalid }

// formatMinor writes an integer minor-unit amount as a plain decimal.
func formatMinor(minor string, scale int) string {
	n, ok := new(big.Int).SetString(minor, 10)
	if !ok {
		return minor
	}
	sign := ""
	if n.Sign() < 0 {
		sign, n = "-", new(big.Int).Neg(n)
	}
	text := n.String()
	if scale <= 0 {
		return sign + text
	}
	if len(text) <= scale {
		text = strings.Repeat("0", scale-len(text)+1) + text
	}
	return sign + text[:len(text)-scale] + "." + text[len(text)-scale:]
}

func validRetirement(q RetireAccount) bool {
	// The transfer's receipt key extends the retirement's key.
	if !validKey(q.RequestKey) || len(q.RequestKey) > 119 || !ValidID(q.AccountID) || q.ExpectedRevision < 1 ||
		len(q.Description) > 4000 || validateSources(q.Sources) != nil {
		return false
	}
	if q.DestinationAccountID == "" {
		return q.TransferDate == "" && q.Description == "" && len(q.Sources) == 0
	}
	return ValidID(q.DestinationAccountID) && q.DestinationAccountID != q.AccountID && ValidDate(q.TransferDate)
}

// accountBalances returns an account's debit-positive balance in the Book
// currency and, for an account with its own currency, in that currency.
func accountBalances(ctx context.Context, tx pgx.Tx, s Scope, a Account) (*big.Int, *big.Int, error) {
	var book, native string
	var complete bool
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(CASE side WHEN 'debit' THEN amount_minor::numeric ELSE -amount_minor::numeric END),0)::text,
 coalesce(sum(CASE side WHEN 'debit' THEN native_amount_minor::numeric ELSE -native_amount_minor::numeric END),0)::text,
 coalesce(bool_and(native_amount_minor IS NOT NULL AND native_currency=$4),true)
 FROM public.lines WHERE tenant_id=$1 AND book_id=$2 AND account_id=$3`, s.TenantID, s.BookID, a.ID, a.Currency).Scan(&book, &native, &complete)
	if err != nil {
		return nil, nil, dbError(err)
	}
	b, _ := new(big.Int).SetString(book, 10)
	if a.Currency == "" {
		return b, nil, nil
	}
	if !complete {
		// Without every native movement the remaining native amount is unknown.
		return nil, nil, ErrInvalid
	}
	n, _ := new(big.Int).SetString(native, 10)
	return b, n, nil
}

func retireAccount(ctx context.Context, tx pgx.Tx, s Scope, q RetireAccount, receipt string) (json.RawMessage, error) {
	a, err := readAccount(ctx, tx, s, q.AccountID)
	if err != nil {
		return nil, dbError(err)
	}
	if a.Revision != q.ExpectedRevision {
		return nil, ErrConflict
	}
	if a.Status == "retired" {
		return nil, ErrInvalid
	}
	book, native, err := accountBalances(ctx, tx, s, a)
	if err != nil {
		return nil, err
	}
	header, err := readBook(ctx, tx, s)
	if err != nil {
		return nil, dbError(err)
	}
	empty := book.Sign() == 0 && (native == nil || native.Sign() == 0)
	out := RetiredAccount{}
	transfer := ""
	if !empty {
		if q.DestinationAccountID == "" {
			refusal := &RemainingBalanceError{AccountID: a.ID, Remaining: []RemainingBalance{}}
			if native != nil && native.Sign() != 0 {
				refusal.Remaining = append(refusal.Remaining, RemainingBalance{a.Currency, *a.Scale, native.String()})
			}
			if book.Sign() != 0 && (native == nil || a.Currency != header.Currency) {
				refusal.Remaining = append(refusal.Remaining, RemainingBalance{header.Currency, header.Scale, book.String()})
			}
			return nil, refusal
		}
		entry, err := postRetirementTransfer(ctx, tx, s, q, a, book, native)
		if err != nil {
			return nil, err
		}
		out.Transfer, transfer = &entry, entry.ID
	} else if q.DestinationAccountID != "" {
		// Nothing remains, so there is nothing to transfer.
		return nil, ErrInvalid
	}
	a.Revision++
	a.Status = "retired"
	if err = recordAccountRevision(ctx, tx, s, a, receipt, transfer); err != nil {
		return nil, err
	}
	out.Account = a
	return raw(out), nil
}

// postRetirementTransfer posts the ordinary entry that moves the exact
// remaining balance to the destination, under its own journal receipt.
func postRetirementTransfer(ctx context.Context, tx pgx.Tx, s Scope, q RetireAccount, a Account, book, native *big.Int) (Entry, error) {
	destination, err := readAccount(ctx, tx, s, q.DestinationAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrInvalid
	}
	if err != nil {
		return Entry{}, dbError(err)
	}
	if destination.Status == "retired" || destination.Heading || destination.Currency != "" && destination.Currency != a.Currency {
		return Entry{}, ErrInvalid
	}
	// One line can't move the Book amount and the native amount in opposite
	// directions; such a balance needs an explicit correction first.
	direction := book.Sign()
	if native != nil && native.Sign() != 0 {
		if direction != 0 && direction != native.Sign() {
			return Entry{}, ErrInvalid
		}
		direction = native.Sign()
	}
	from, to := "credit", "debit"
	if direction < 0 {
		from, to = "debit", "credit"
	}
	description := q.Description
	if description == "" {
		description = "Transfer of the remaining balance of " + a.Name
	}
	amount := new(big.Int).Abs(book).String()
	retiring := PostLine{AccountID: a.ID, Side: from, AmountMinor: amount}
	receiving := PostLine{AccountID: destination.ID, Side: to, AmountMinor: amount}
	if native != nil {
		moved := new(big.Int).Abs(native).String()
		retiring.Native = &NativeMovement{Currency: a.Currency, Scale: a.Scale, AmountMinor: moved}
		if destination.Currency != "" {
			receiving.Native = &NativeMovement{Currency: a.Currency, Scale: a.Scale, AmountMinor: moved}
		}
	}
	post := PostJournal{RequestKey: q.RequestKey + "/transfer", EconomicDate: q.TransferDate, AccountingDate: q.TransferDate,
		Description: description, Lines: []PostLine{retiring, receiving}, Sources: q.Sources}
	if err = validatePost(post); err != nil {
		return Entry{}, err
	}
	request := raw(post)
	receipt := ID()
	v, err := insertJournal(ctx, tx, s, post, receipt)
	if err != nil {
		return Entry{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes) VALUES($1,$2,$3,$4,$5,'post_journal',$6,$7)",
		s.TenantID, s.BookID, receipt, s.ActorID, post.RequestKey, request, []byte(raw(v))); err != nil {
		return Entry{}, err
	}
	return v, nil
}

func reactivateAccount(ctx context.Context, tx pgx.Tx, s Scope, q ReactivateAccount, receipt string) (json.RawMessage, error) {
	a, err := readAccount(ctx, tx, s, q.AccountID)
	if err != nil {
		return nil, dbError(err)
	}
	if a.Revision != q.ExpectedRevision {
		return nil, ErrConflict
	}
	if a.Status != "retired" {
		return nil, ErrInvalid
	}
	a.Revision++
	a.Status = ""
	if err = recordAccountRevision(ctx, tx, s, a, receipt, ""); err != nil {
		return nil, err
	}
	return raw(a), nil
}

// AppliedStarterChart lists the accounts a starter chart created.
type AppliedStarterChart struct {
	Template string    `json:"template"`
	Accounts []Account `json:"accounts"`
}

type ApplyStarterChart struct {
	RequestKey string `json:"request_key"`
	Template   string `json:"template"`
}

func applyStarterChart(ctx context.Context, tx pgx.Tx, s Scope, q ApplyStarterChart, receipt string) (json.RawMessage, error) {
	chart, ok := StarterChart(q.Template)
	if !ok {
		return nil, ErrInvalid
	}
	var occupied bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.accounts WHERE tenant_id=$1 AND book_id=$2)", s.TenantID, s.BookID).Scan(&occupied); err != nil {
		return nil, dbError(err)
	}
	if occupied {
		return nil, ErrConflict
	}
	out := AppliedStarterChart{Template: chart.ID, Accounts: []Account{}}
	ids := map[string]string{}
	for _, item := range chart.Accounts {
		a := Account{ID: ID(), Class: item.Class, Code: item.Code, Name: item.Name, Revision: 1, Role: item.Role, ParentAccountID: ids[item.Parent], Heading: item.Heading}
		if item.Role != "" {
			a.RoleRevision = &a.Revision
		}
		if _, err := tx.Exec(ctx, "INSERT INTO public.accounts(tenant_id,book_id,id,class) VALUES($1,$2,$3,$4)", s.TenantID, s.BookID, a.ID, a.Class); err != nil {
			return nil, err
		}
		if err := recordAccountRevision(ctx, tx, s, a, receipt, ""); err != nil {
			return nil, err
		}
		ids[item.Code] = a.ID
		out.Accounts = append(out.Accounts, a)
	}
	return raw(out), nil
}

func validStarterChartRequest(q ApplyStarterChart) error {
	if !validKey(q.RequestKey) {
		return ErrInvalid
	}
	if _, ok := StarterChart(q.Template); !ok {
		return fmt.Errorf("%w: unknown starter chart", ErrInvalid)
	}
	return nil
}
