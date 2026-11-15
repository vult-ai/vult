package ledger

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// PersonSideItem is a cash claim a business accepted whose other side the
// person records in their own Book: what the business now owes them. A
// charge already fed into the account they paid from is offered to match;
// matching is always theirs to confirm.
type PersonSideItem struct {
	ClaimID        string `json:"claim_id"`
	Reference      string `json:"reference"`
	BusinessBookID string `json:"business_book_id"`
	BusinessName   string `json:"business_name"`
	Vendor         string `json:"vendor"`
	Date           string `json:"date"`
	// AmountMinor is what the business owes for it; PaidMinor what the
	// person paid, which a fed charge matches.
	AmountMinor       string `json:"amount_minor"`
	PaidMinor         string `json:"paid_minor"`
	Currency          string `json:"currency"`
	PaidFromAccountID string `json:"paid_from_account_id"`
	// HasFeed says the account paid from has a bank or card feed: a
	// match is waited for rather than a new entry recorded.
	HasFeed    bool                  `json:"has_feed"`
	Candidates []PersonSideCandidate `json:"candidates"`
}

// PersonSideCandidate is a fed charge of exactly what the person paid, in
// the claim's currency, within five days of it, with how it differs.
type PersonSideCandidate struct {
	BankItemID   string `json:"bank_item_id"`
	EntryID      string `json:"entry_id,omitempty"`
	Date         string `json:"date"`
	DaysApart    int    `json:"days_apart"`
	Description  string `json:"description"`
	Counterparty string `json:"counterparty,omitempty"`
	AmountMinor  string `json:"amount_minor"`
	// Recorded says the charge is already an entry, which is reclassified
	// rather than recorded again.
	Recorded bool `json:"recorded"`
}

// PersonSideItems lists what the person still records in this Book.
func (a TenantAdmin) PersonSideItems(ctx context.Context, book string) ([]PersonSideItem, error) {
	out := []PersonSideItem{}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = personSideItems(ctx, tx, s.TenantID, s.BookID, "")
		return err
	})
	return out, err
}

func personSideItems(ctx context.Context, tx pgx.Tx, tenant, book, only string) ([]PersonSideItem, error) {
	rows, err := tx.Query(ctx, `SELECT o.claim_id::text,coalesce((SELECT r.reference FROM public.report_lines l JOIN public.expense_reports r ON r.tenant_id=l.tenant_id AND r.id=l.report_id
   WHERE l.tenant_id=o.tenant_id AND l.claim_id=o.claim_id ORDER BY r.created_at DESC LIMIT 1),''),
 o.business_book_id::text,coalesce((SELECT coalesce((SELECT v.name FROM public.book_revisions v WHERE v.tenant_id=k.tenant_id AND v.book_id=k.id ORDER BY v.revision DESC LIMIT 1),k.name)
   FROM public.books k WHERE k.tenant_id=o.tenant_id AND k.id=o.business_book_id),''),
 c.vendor,c.incurred_on::text,o.amount_minor::text,c.original_amount_minor::text,c.original_currency,c.paid_from_account_id::text,
 EXISTS(SELECT 1 FROM public.bank_accounts x WHERE x.tenant_id=o.tenant_id AND x.book_id=o.person_book_id AND x.account_id=c.paid_from_account_id)
 FROM public.claim_obligations o JOIN public.claims c ON c.tenant_id=o.tenant_id AND c.id=o.claim_id
 WHERE o.tenant_id=$1 AND o.person_book_id=$2 AND o.person_status='pending' AND ($3='' OR o.claim_id::text=$3) ORDER BY c.incurred_on,o.claim_id`, tenant, book, only)
	if err != nil {
		return nil, dbError(err)
	}
	out := []PersonSideItem{}
	for rows.Next() {
		var x PersonSideItem
		if err := rows.Scan(&x.ClaimID, &x.Reference, &x.BusinessBookID, &x.BusinessName, &x.Vendor, &x.Date, &x.AmountMinor, &x.PaidMinor, &x.Currency, &x.PaidFromAccountID, &x.HasFeed); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		out = append(out, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	for i := range out {
		x := &out[i]
		x.Candidates = []PersonSideCandidate{}
		// Exactly the amount, the same currency, five days either side, on
		// the account paid from, and not already the other half of a pair
		// or another claim's match.
		cands, err := tx.Query(ctx, `SELECT t.id::text,coalesce(k.entry_id::text,''),t.occurred_on::text,abs(t.occurred_on-$4::date),t.description,t.counterparty,abs(t.amount_minor)::text
 FROM public.bank_transactions t JOIN public.bank_accounts ba ON ba.tenant_id=t.tenant_id AND ba.book_id=t.book_id AND ba.id=t.bank_account_id
 LEFT JOIN public.bank_transaction_links k ON k.tenant_id=t.tenant_id AND k.book_id=t.book_id AND k.transaction_id=t.id AND k.revoked_at IS NULL
 WHERE t.tenant_id=$1 AND t.book_id=$2 AND ba.account_id=$3 AND abs(t.amount_minor)=$5::bigint AND t.currency=$6
   AND t.occurred_on BETWEEN $4::date-5 AND $4::date+5
   AND NOT EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id=$1 AND (p.payer_book_id=$2 AND p.payer_entry_id=k.entry_id OR p.beneficiary_book_id=$2 AND p.beneficiary_entry_id=k.entry_id))
   AND NOT EXISTS (SELECT 1 FROM public.claim_obligations o WHERE o.tenant_id=$1 AND o.matched_bank_item_id=t.id)
 ORDER BY abs(t.occurred_on-$4::date),t.occurred_on,t.id LIMIT 10`, tenant, book, x.PaidFromAccountID, x.Date, x.PaidMinor, x.Currency)
		if err != nil {
			return nil, dbError(err)
		}
		for cands.Next() {
			var c PersonSideCandidate
			if err := cands.Scan(&c.BankItemID, &c.EntryID, &c.Date, &c.DaysApart, &c.Description, &c.Counterparty, &c.AmountMinor); err != nil {
				cands.Close()
				return nil, dbError(err)
			}
			c.Recorded = c.EntryID != ""
			x.Candidates = append(x.Candidates, c)
		}
		cands.Close()
		if err := cands.Err(); err != nil {
			return nil, dbError(err)
		}
	}
	return out, nil
}

// RecordPersonSide records the person's side of an accepted cash claim in
// their Book, as its owner confirms. With a fed charge chosen, that charge
// becomes what the business owes them: an entry already recorded is
// reclassified from where it was recorded, one not yet recorded is
// recorded so; the account paid from shows one outflow either way. With
// none, and only when the account paid from has no feed, a new entry
// records the cash paid.
func (a TenantAdmin) RecordPersonSide(ctx context.Context, book, actor, claim, bankItem string) (string, error) {
	var entry string
	if !ValidID(claim) || bankItem != "" && !ValidID(bankItem) {
		return "", ErrInvalid
	}
	err := a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		items, err := personSideItems(ctx, tx, s.TenantID, s.BookID, claim)
		if err != nil {
			return err
		}
		if len(items) != 1 {
			return &RefusedError{Reason: "There's nothing of this claim left to record here."}
		}
		x := items[0]
		var receivable string
		if err := tx.QueryRow(ctx, "SELECT (public.active_link_terms($1,$2,$3)).payer_account_id::text", s.TenantID, s.BookID, x.BusinessBookID).Scan(&receivable); err != nil || receivable == "" {
			return &RefusedError{Reason: "This Book records what the business owes it only through an owed link."}
		}
		key := "claim-side-" + claim
		post := func(lines []PostLine, description string) (string, error) {
			result, err := dispatch(ctx, tx, s, "post_journal", raw(PostJournal{RequestKey: key, EconomicDate: x.Date, AccountingDate: x.Date,
				Description: description, Lines: lines}), nil)
			if err != nil {
				return "", err
			}
			if e := outcomeEntry(&Outcome{Result: result}); e != "" {
				return e, nil
			}
			return "", ErrUncertain
		}
		method := "new_entry"
		var chosen *PersonSideCandidate
		for i := range x.Candidates {
			if x.Candidates[i].BankItemID == bankItem {
				chosen = &x.Candidates[i]
			}
		}
		switch {
		case bankItem != "" && chosen == nil:
			return &RefusedError{Reason: "That charge doesn't match this claim: the same amount and currency, within five days."}
		case chosen != nil && chosen.Recorded:
			// Move the charge from where it was recorded to what the
			// business owes: no second outflow.
			var from string
			if err := tx.QueryRow(ctx, `SELECT x.account_id::text FROM public.lines x WHERE x.tenant_id=$1 AND x.book_id=$2 AND x.entry_id=$3 AND x.side='debit'
 AND x.amount_minor=$4::bigint AND x.account_id<>$5 ORDER BY x.position LIMIT 1`, s.TenantID, s.BookID, chosen.EntryID, x.AmountMinor, receivable).Scan(&from); err != nil {
				return &RefusedError{Reason: "That charge is split across accounts; record your side by hand."}
			}
			method = "reclassified"
			entry, err = post([]PostLine{{AccountID: receivable, Side: "debit", AmountMinor: x.AmountMinor}, {AccountID: from, Side: "credit", AmountMinor: x.AmountMinor}},
				x.Vendor+" is owed by "+x.BusinessName+" ("+x.Reference+")")
		case chosen != nil && x.PaidMinor != x.AmountMinor:
			return &RefusedError{Reason: "Only part of this charge is the business's: record the charge first, then match it."}
		case chosen != nil:
			method = "matched_charge"
			var result []byte
			result, err = dispatch(ctx, tx, s, "record_bank_activity", raw(RecordBankActivity{RequestKey: key, TransactionID: chosen.BankItemID,
				Description: x.Vendor + " (" + x.Reference + ")", OtherAccountID: receivable, Reason: "Paid for " + x.BusinessName + ", owed back"}), nil)
			if err == nil {
				if entry = outcomeEntry(&Outcome{Result: result}); entry == "" {
					err = tx.QueryRow(ctx, "SELECT entry_id::text FROM public.bank_transaction_links WHERE tenant_id=$1 AND book_id=$2 AND transaction_id=$3 AND revoked_at IS NULL",
						s.TenantID, s.BookID, chosen.BankItemID).Scan(&entry)
				}
			}
		case x.HasFeed:
			return &RefusedError{Reason: "This account has a feed: wait for the charge to arrive, then match it."}
		default:
			entry, err = post([]PostLine{{AccountID: receivable, Side: "debit", AmountMinor: x.AmountMinor}, {AccountID: x.PaidFromAccountID, Side: "credit", AmountMinor: x.AmountMinor}},
				x.Vendor+" paid for "+x.BusinessName+" ("+x.Reference+")")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
			return err
		}
		var item any
		if chosen != nil {
			item = chosen.BankItemID
		}
		if _, err := tx.Exec(ctx, "SELECT public.record_person_side($1,$2,$3,$4,$5,$6)", s.TenantID, claim, entry, method, item, actor); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE public.agent_loops SET status='done',closed_reason='Recorded by the owner.',closed_at=clock_timestamp()
 WHERE tenant_id=$1 AND book_id=$2 AND subject_key=$3 AND status='open'`, s.TenantID, s.BookID, "claim:"+claim)
		return err
	})
	return entry, err
}

// PersonSideCorrection is an approved correction of a cash claim whose
// side the person already recorded: they record the same difference in
// their Book. DeltaMinor is positive when the business owes more.
type PersonSideCorrection struct {
	ID             string `json:"id"`
	ClaimID        string `json:"claim_id"`
	Reference      string `json:"reference"`
	Revision       int    `json:"revision"`
	BusinessBookID string `json:"business_book_id"`
	Vendor         string `json:"vendor"`
	DeltaMinor     string `json:"delta_minor"`
	// AccountID is where the difference goes in this Book unless another
	// is chosen: the account the person's side first moved it from.
	AccountID string `json:"account_id"`
}

// PersonSideCorrections lists the corrections this Book still records.
func (a TenantAdmin) PersonSideCorrections(ctx context.Context, book string) ([]PersonSideCorrection, error) {
	var out []PersonSideCorrection
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = personCorrections(ctx, tx, s)
		return err
	})
	return out, err
}

func personCorrections(ctx context.Context, tx pgx.Tx, s Scope) ([]PersonSideCorrection, error) {
	out := []PersonSideCorrection{}
	{
		rows, err := tx.Query(ctx, `SELECT a.id::text,a.claim_id::text,r.reference,a.to_revision,a.business_book_id::text,c.vendor,a.delta_minor::text,
 coalesce((SELECT x.account_id::text FROM public.lines x WHERE x.tenant_id=o.tenant_id AND x.book_id=o.person_book_id AND x.entry_id=o.person_entry_id
   AND x.account_id<>(public.active_link_terms(o.tenant_id,o.person_book_id,o.business_book_id)).payer_account_id ORDER BY x.position LIMIT 1),'')
 FROM public.claim_obligation_adjustments a JOIN public.claim_obligations o ON o.tenant_id=a.tenant_id AND o.claim_id=a.claim_id
 JOIN public.claims c ON c.tenant_id=a.tenant_id AND c.id=a.claim_id JOIN public.expense_reports r ON r.tenant_id=a.tenant_id AND r.id=a.report_id
 WHERE a.tenant_id=$1 AND a.person_book_id=$2 AND a.person_status='pending' ORDER BY a.created_at,a.id`, s.TenantID, s.BookID)
		if err != nil {
			return nil, dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var c PersonSideCorrection
			if err := rows.Scan(&c.ID, &c.ClaimID, &c.Reference, &c.Revision, &c.BusinessBookID, &c.Vendor, &c.DeltaMinor, &c.AccountID); err != nil {
				return nil, dbError(err)
			}
			out = append(out, c)
		}
		return out, dbError(rows.Err())
	}
}

// RecordPersonCorrection records a correction in the person's Book, as its
// owner confirms: what the business owes them moves by the difference,
// against the account given or the one their side first used.
func (a TenantAdmin) RecordPersonCorrection(ctx context.Context, book, actor, correction, account string) (string, error) {
	var entry string
	if !ValidID(correction) || account != "" && !ValidID(account) {
		return "", ErrInvalid
	}
	err := a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		var found *PersonSideCorrection
		items, err := personCorrections(ctx, tx, s)
		if err != nil {
			return err
		}
		for i := range items {
			if items[i].ID == correction {
				found = &items[i]
			}
		}
		if found == nil {
			return &RefusedError{Reason: "There's nothing of this correction left to record here."}
		}
		if account == "" {
			account = found.AccountID
		}
		if account == "" {
			return &RefusedError{Reason: "Choose the account the difference goes to."}
		}
		var receivable string
		if err := tx.QueryRow(ctx, "SELECT (public.active_link_terms($1,$2,$3)).payer_account_id::text", s.TenantID, s.BookID, found.BusinessBookID).Scan(&receivable); err != nil || receivable == "" {
			return &RefusedError{Reason: "This Book records what the business owes it only through an owed link."}
		}
		delta, _ := strconv.ParseInt(found.DeltaMinor, 10, 64)
		amount := strconv.FormatInt(max(delta, -delta), 10)
		lines := []PostLine{{AccountID: receivable, Side: "debit", AmountMinor: amount}, {AccountID: account, Side: "credit", AmountMinor: amount}}
		if delta < 0 {
			lines = []PostLine{{AccountID: account, Side: "debit", AmountMinor: amount}, {AccountID: receivable, Side: "credit", AmountMinor: amount}}
		}
		today := time.Now().Format("2006-01-02")
		result, err := dispatch(ctx, tx, s, "post_journal", raw(PostJournal{RequestKey: "claim-correction-side-" + correction, EconomicDate: today, AccountingDate: today,
			Description: found.Vendor + " corrected on " + found.Reference, Lines: lines}), nil)
		if err != nil {
			return err
		}
		if entry = outcomeEntry(&Outcome{Result: result}); entry == "" {
			return ErrUncertain
		}
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT public.record_person_adjustment($1,$2,$3,$4)", s.TenantID, correction, entry, actor); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE public.agent_loops SET status='done',closed_reason='Recorded by the owner.',closed_at=clock_timestamp()
 WHERE tenant_id=$1 AND book_id=$2 AND subject_key=$3 AND status='open'`, s.TenantID, s.BookID, correctionSubject(found.ClaimID, found.Revision))
		return err
	})
	return entry, err
}
