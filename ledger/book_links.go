package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vult-ai/vult/internal/strictjson"
)

// LinkTerms is one direction of a link, from the reading Book's side: how a
// cost is recorded, and the account on each side.
type LinkTerms struct {
	Treatment      string `json:"treatment"`
	MyAccountID    string `json:"my_account_id"`
	TheirAccountID string `json:"their_account_id"`
	Active         bool   `json:"active"`
}

// LinkedBook is one of a Book's links, from that Book's side.
type LinkedBook struct {
	LinkID   string `json:"link_id"`
	Active   bool   `json:"active"`
	Revision int64  `json:"revision"`
	Other    struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Profile string `json:"profile"`
	} `json:"other"`
	PaysForThem  *LinkTerms `json:"pays_for_them"`
	TheyPayForMe *LinkTerms `json:"they_pay_for_me"`
}

func readLinkedBooks(ctx context.Context, tx pgx.Tx, s Scope) ([]LinkedBook, error) {
	var data []byte
	if err := tx.QueryRow(ctx, "SELECT public.book_links_for($1,$2)", s.TenantID, s.BookID).Scan(&data); err != nil {
		return nil, dbError(err)
	}
	out := []LinkedBook{}
	if data != nil && json.Unmarshal(data, &out) != nil {
		return nil, ErrUncertain
	}
	return out, nil
}

// BookLink is the single link an earlier agent reads with get_book_link: the
// first active link between this personal Book and a business it pays for,
// or that pays this business's costs.
type BookLink struct {
	Linked            bool   `json:"linked"`
	LinkedBookID      string `json:"linked_book_id,omitempty"`
	LinkedBookProfile string `json:"linked_book_profile,omitempty"`
	LinkedBookName    string `json:"linked_book_name,omitempty"`
	BusinessBookID    string `json:"business_book_id,omitempty"`
	PersonalBookID    string `json:"personal_book_id,omitempty"`
	Mode              string `json:"mode,omitempty"`
	BusinessAccountID string `json:"business_account_id,omitempty"`
	PersonalAccountID string `json:"personal_account_id,omitempty"`
	Active            bool   `json:"active,omitempty"`
	Revision          int64  `json:"revision,omitempty"`
}

func readBookLink(ctx context.Context, tx pgx.Tx, s Scope) (BookLink, error) {
	links, err := readLinkedBooks(ctx, tx, s)
	if err != nil {
		return BookLink{}, err
	}
	for _, l := range links {
		out := BookLink{Linked: true, LinkedBookID: l.Other.ID, LinkedBookProfile: l.Other.Profile, LinkedBookName: l.Other.Name, Active: l.Active, Revision: l.Revision}
		switch {
		case l.Other.Profile == "business" && l.PaysForThem != nil:
			out.BusinessBookID, out.PersonalBookID = l.Other.ID, s.BookID
			out.Mode, out.PersonalAccountID, out.BusinessAccountID = l.PaysForThem.Treatment, l.PaysForThem.MyAccountID, l.PaysForThem.TheirAccountID
			out.Active = out.Active && l.PaysForThem.Active
		case l.Other.Profile == "personal" && l.TheyPayForMe != nil:
			out.BusinessBookID, out.PersonalBookID = s.BookID, l.Other.ID
			out.Mode, out.BusinessAccountID, out.PersonalAccountID = l.TheyPayForMe.Treatment, l.TheyPayForMe.MyAccountID, l.TheyPayForMe.TheirAccountID
			out.Active = out.Active && l.TheyPayForMe.Active
		default:
			continue
		}
		return out, nil
	}
	return BookLink{}, nil
}

var pairKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validPairKey(key string) bool { return pairKeyPattern.MatchString(key) }

// errPairDirect refuses half of a pair outside a proposal: the owner
// decides a pair, as one, or records both halves together.
var errPairDirect = &RefusedError{Reason: "Pairs are always proposals: prepare it with prepare_operation."}

// errPairWaiting refuses approving the first half of a pair before the
// second arrives: alone it would record the cost in only one Book.
var errPairWaiting = &RefusedError{Reason: "This is half of a pair still waiting for its other half; it can be approved once both are here, or rejected."}

// errLinkOff refuses approving a pair whose link is turned off.
var errLinkOff = &RefusedError{Reason: "This link is turned off; the pair can only be rejected."}

// pairedArguments reports whether operation arguments carry a pair_key.
func pairedArguments(args []byte) bool {
	var q struct {
		PairKey string `json:"pair_key"`
	}
	return json.Unmarshal(args, &q) == nil && q.PairKey != ""
}

// pairError keeps the ledger's reason when it refuses half of a pair.
func pairError(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "VP020" {
		return &RefusedError{Reason: p.Message}
	}
	return dbError(err)
}

// OwnerLink is a link as the owner manages it: both Books, both directions'
// terms with account names, and its revision.
type OwnerLink struct {
	ID    string `json:"id"`
	Books []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Profile  string `json:"profile"`
		Currency string `json:"currency"`
	} `json:"books"`
	Terms []struct {
		PayerBookID       string `json:"payer_book_id"`
		BeneficiaryBookID string `json:"beneficiary_book_id"`
		Treatment         string `json:"treatment"`
		PayerAccount      struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"payer_account"`
		BeneficiaryAccount struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"beneficiary_account"`
		Active bool `json:"active"`
	} `json:"terms"`
	Active   bool  `json:"active"`
	Revision int64 `json:"revision"`
	// Allowed says, for each direction, the ways it may be recorded: how a
	// business pays its owner follows how the business is taxed.
	Allowed []AllowedTreatments `json:"allowed_treatments"`
}

// AllowedTreatments are the ways one direction of a link may be recorded.
type AllowedTreatments struct {
	PayerBookID       string   `json:"payer_book_id"`
	BeneficiaryBookID string   `json:"beneficiary_book_id"`
	Treatments        []string `json:"treatments"`
}

// allowedTreatments says how a payer Book may record what it pays for
// another: money the owner puts into a business is a contribution or owed;
// what a business pays for its owner is owed back or owner's equity (an
// owner's draw, a partner's draw, a shareholder distribution), except a C
// corporation's, which is only ever owed back; between two businesses or
// two personal Books, it is owed.
func allowedTreatments(payerProfile, payerTaxed, beneficiaryProfile string) []string {
	switch {
	case payerProfile == "personal" && beneficiaryProfile == "business":
		return []string{"contribution", "owed"}
	case payerProfile == "business" && beneficiaryProfile == "personal" && payerTaxed == TaxedCCorporation:
		return []string{"owed"}
	case payerProfile == "business" && beneficiaryProfile == "personal":
		return []string{"owed", "owner_equity"}
	}
	return []string{"owed"}
}

const ownerLinksSQL = `SELECT coalesce(jsonb_agg(jsonb_build_object('id', l.id, 'active', l.active, 'revision', l.revision,
 'books', (SELECT jsonb_agg(jsonb_build_object('id', k.id, 'name', k.name, 'profile', public.book_profile(k.tenant_id, k.id), 'currency', k.currency) ORDER BY k.id)
   FROM public.books k WHERE k.tenant_id = l.tenant_id AND k.id IN (l.book_a, l.book_b)),
 'terms', coalesce((SELECT jsonb_agg(jsonb_build_object('payer_book_id', x.payer_book_id, 'beneficiary_book_id', x.beneficiary_book_id, 'treatment', x.treatment, 'active', x.active,
     'payer_account', (SELECT jsonb_build_object('id', a.id, 'name', a.name) FROM public.accounts a WHERE a.tenant_id = x.tenant_id AND a.book_id = x.payer_book_id AND a.id = x.payer_account_id),
     'beneficiary_account', (SELECT jsonb_build_object('id', a.id, 'name', a.name) FROM public.accounts a WHERE a.tenant_id = x.tenant_id AND a.book_id = x.beneficiary_book_id AND a.id = x.beneficiary_account_id))
     ORDER BY x.payer_book_id) FROM public.book_link_terms x WHERE x.tenant_id = l.tenant_id AND x.link_id = l.id), '[]'::jsonb))
 ORDER BY l.created_at, l.id), '[]'::jsonb) FROM public.book_links l WHERE l.tenant_id = $1 AND ($2 = '' OR l.book_a::text = $2 OR l.book_b::text = $2)`

// OwnerLinks lists the tenant's links, or one Book's when book is given.
func (a TenantAdmin) OwnerLinks(ctx context.Context, book string) ([]OwnerLink, error) {
	if book != "" && !ValidID(book) {
		return nil, ErrInvalid
	}
	read := book
	if read == "" {
		var err error
		if read, err = a.anyBook(ctx); err != nil || read == "" {
			return []OwnerLink{}, err
		}
	}
	var out []OwnerLink
	err := a.withBookRead(ctx, read, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readOwnerLinks(ctx, tx, s.TenantID, book)
		return err
	})
	return out, err
}

func readOwnerLinks(ctx context.Context, tx pgx.Tx, tenant, book string) ([]OwnerLink, error) {
	var data []byte
	if err := tx.QueryRow(ctx, ownerLinksSQL, tenant, book).Scan(&data); err != nil {
		return nil, dbError(err)
	}
	out := []OwnerLink{}
	if json.Unmarshal(data, &out) != nil {
		return nil, ErrUncertain
	}
	for i, l := range out {
		out[i].Allowed = []AllowedTreatments{}
		for _, payer := range l.Books {
			for _, other := range l.Books {
				if payer.ID == other.ID {
					continue
				}
				var taxed string
				if err := tx.QueryRow(ctx, "SELECT coalesce(public.book_taxed_as($1,$2),'')", tenant, payer.ID).Scan(&taxed); err != nil {
					return nil, dbError(err)
				}
				out[i].Allowed = append(out[i].Allowed, AllowedTreatments{PayerBookID: payer.ID, BeneficiaryBookID: other.ID,
					Treatments: allowedTreatments(payer.Profile, taxed, other.Profile)})
			}
		}
	}
	return out, nil
}

func (a TenantAdmin) anyBook(ctx context.Context) (string, error) {
	books, err := a.Books(ctx)
	if err != nil || len(books) == 0 {
		return "", err
	}
	return books[0].ID, nil
}

// LinkTermsChange sets one direction of a link: how a cost the payer Book
// pays for the other is recorded, and the account on each side.
type LinkTermsChange struct {
	BookIDs              [2]string `json:"book_ids"`
	PayerBookID          string    `json:"payer_book_id"`
	Treatment            string    `json:"treatment"`
	PayerAccountID       string    `json:"payer_account_id"`
	BeneficiaryAccountID string    `json:"beneficiary_account_id"`
	Active               bool      `json:"active"`
	// LinkActive turns the whole link on or off; omitted, it stays as it is
	// (on for a new link).
	LinkActive       *bool `json:"link_active,omitempty"`
	ExpectedRevision int64 `json:"expected_revision"`
}

// SetLinkTerms links two Books, or changes one direction of their link. The
// ledger checks the Books' profiles and currencies and each account by
// class and role; only the owner, signed in recently, may change it.
func (a TenantAdmin) SetLinkTerms(ctx context.Context, actor string, q LinkTermsChange) (OwnerLink, error) {
	var out OwnerLink
	x, y := q.BookIDs[0], q.BookIDs[1]
	if !ValidID(actor) || !ValidID(x) || !ValidID(y) || x == y || q.PayerBookID != x && q.PayerBookID != y ||
		!ValidID(q.PayerAccountID) || !ValidID(q.BeneficiaryAccountID) || q.ExpectedRevision < 0 ||
		q.Treatment != "contribution" && q.Treatment != "owed" && q.Treatment != "owner_equity" {
		return out, ErrInvalid
	}
	if y < x {
		x, y = y, x
	}
	beneficiary := x
	if q.PayerBookID == x {
		beneficiary = y
	}
	// Turning a link or a direction off narrows what can happen: any valid
	// sign-in may do it. The database refuses anything wider without a
	// recent one.
	owner := a.withRuleOwner
	if q.LinkActive != nil && !*q.LinkActive || !q.Active {
		owner = a.withAgentNarrowing
	}
	err := owner(ctx, q.PayerBookID, actor, func(tx pgx.Tx, s Scope) error {
		var link string
		var err error
		if q.ExpectedRevision == 0 {
			link = ID()
			active := q.LinkActive == nil || *q.LinkActive
			_, err = tx.Exec(ctx, `INSERT INTO public.book_links(tenant_id,id,book_a,book_b,active,revision,actor_id) VALUES($1,$2,$3,$4,$5,1,$6)`, s.TenantID, link, x, y, active, actor)
		} else {
			err = tx.QueryRow(ctx, `UPDATE public.book_links SET revision=revision+1,actor_id=$5,active=coalesce($6,active)
 WHERE tenant_id=$1 AND book_a=$2 AND book_b=$3 AND revision=$4 RETURNING id::text`, s.TenantID, x, y, q.ExpectedRevision, actor, q.LinkActive).Scan(&link)
			if errors.Is(err, pgx.ErrNoRows) {
				return &RefusedError{Reason: "This link changed since you opened it — reload to see it."}
			}
		}
		if err != nil {
			return linkError(err)
		}
		// How a business pays its owner follows how it is taxed. Turning a
		// direction off is always allowed.
		if q.Active {
			var payerProfile, payerTaxed, beneficiaryProfile, payerName, beneficiaryName string
			if err := tx.QueryRow(ctx, `SELECT coalesce(public.book_profile($1,$2),''),coalesce(public.book_taxed_as($1,$2),''),coalesce(public.book_profile($1,$3),''),
 coalesce((SELECT coalesce((SELECT r.name FROM public.book_revisions r WHERE r.tenant_id=k.tenant_id AND r.book_id=k.id ORDER BY r.revision DESC LIMIT 1),k.name) FROM public.books k WHERE k.tenant_id=$1 AND k.id=$2),''),
 coalesce((SELECT coalesce((SELECT r.name FROM public.book_revisions r WHERE r.tenant_id=k.tenant_id AND r.book_id=k.id ORDER BY r.revision DESC LIMIT 1),k.name) FROM public.books k WHERE k.tenant_id=$1 AND k.id=$3),'')`,
				s.TenantID, q.PayerBookID, beneficiary).Scan(&payerProfile, &payerTaxed, &beneficiaryProfile, &payerName, &beneficiaryName); err != nil {
				return dbError(err)
			}
			if q.Treatment == "owner_equity" && payerProfile == "business" && payerTaxed == TaxedCCorporation {
				return &RefusedError{Reason: payerName + " is taxed as a C corporation, so what it pays for " + beneficiaryName + " is owed back, never owner's equity. Choose owed back."}
			}
		}
		// The direction's terms change in place, or are set for the first
		// time; turning them off is checked as a narrowing change.
		tag, err := tx.Exec(ctx, `UPDATE public.book_link_terms SET treatment=$5,payer_account_id=$6,beneficiary_account_id=$7,active=$8,actor_id=$9
 WHERE tenant_id=$1 AND link_id=$2 AND payer_book_id=$3 AND beneficiary_book_id=$4`,
			s.TenantID, link, q.PayerBookID, beneficiary, q.Treatment, q.PayerAccountID, q.BeneficiaryAccountID, q.Active, actor)
		if err != nil {
			return linkError(err)
		}
		if tag.RowsAffected() == 0 {
			if _, err = tx.Exec(ctx, `INSERT INTO public.book_link_terms(tenant_id,link_id,payer_book_id,beneficiary_book_id,treatment,payer_account_id,beneficiary_account_id,active,actor_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, s.TenantID, link, q.PayerBookID, beneficiary, q.Treatment, q.PayerAccountID, q.BeneficiaryAccountID, q.Active, actor); err != nil {
				return linkError(err)
			}
		}
		links, err := readOwnerLinks(ctx, tx, s.TenantID, q.PayerBookID)
		if err != nil {
			return err
		}
		for _, l := range links {
			if l.ID == link {
				out = l
			}
		}
		return nil
	})
	return out, err
}

// DeleteBookLink removes a link with no pairs between its Books; a link
// with pairs is turned off instead.
func (a TenantAdmin) DeleteBookLink(ctx context.Context, actor, link string) error {
	if !ValidID(actor) || !ValidID(link) {
		return ErrInvalid
	}
	books, err := a.anyBook(ctx)
	if err != nil {
		return err
	}
	return a.withRuleOwner(ctx, books, actor, func(tx pgx.Tx, s Scope) error {
		// Half of a pair that still waits would otherwise be approved
		// later as an ordinary entry in one Book, with nothing in the
		// other. It is decided first.
		var waiting int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.operation_proposals p JOIN public.book_links l ON l.tenant_id=p.tenant_id AND p.book_id IN (l.book_a,l.book_b)
 WHERE p.tenant_id=$1 AND l.id=$2 AND (p.expires_at IS NULL OR p.expires_at>clock_timestamp()) AND `+proposalWaiting+`
 AND position('"pair_key"' IN convert_from(p.payload_bytes,'UTF8'))>0`, s.TenantID, link).Scan(&waiting); err != nil {
			return dbError(err)
		}
		if waiting > 0 {
			return &RefusedError{Reason: fmt.Sprintf("%d changes between these Books still wait for a decision. Approve or reject them first, or turn the link off instead.", waiting)}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM public.book_link_terms WHERE tenant_id=$1 AND link_id=$2", s.TenantID, link); err != nil {
			return linkError(err)
		}
		tag, err := tx.Exec(ctx, "DELETE FROM public.book_links WHERE tenant_id=$1 AND id=$2", s.TenantID, link)
		if err != nil {
			return linkError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

var overpaidRefusal = regexp.MustCompile(`^This pays (.+?) more than (.+?) owes \(.*?\)\. Set "(.+?) pays for (.+?)" in Links, and the extra is recorded there\.$`)

func linkError(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "VP020" {
		// Paying more than is owed, with nowhere yet to record the extra,
		// says the one step to take.
		if m := overpaidRefusal.FindStringSubmatch(p.Message); m != nil {
			return &RefusedError{Reason: m[3] + " paid " + m[4] + " " + m[1] + " more than it owes. Choose how to record the extra in Links."}
		}
		return &RefusedError{Reason: p.Message}
	}
	if errors.As(err, &p) && p.Code == "23505" {
		return &RefusedError{Reason: "These two Books are already linked; change that link instead."}
	}
	return bankError(err)
}

// ProposalPair is the other half of a paired proposal, as seen from one
// half: the partner's Book and proposal, and which side the partner is.
type ProposalPair struct {
	PairKey       string `json:"pair_key"`
	TransactionID string `json:"transaction_id"`
	BookID        string `json:"book_id"`
	BookName      string `json:"book_name"`
	ProposalID    string `json:"proposal_id"`
	// Side is the partner's side: payer (the Book whose bank item it is) or
	// beneficiary (the Book the cost is for).
	Side string `json:"side"`
	Kind string `json:"kind,omitempty"`
	// State is paired, or waiting_for_pair for a payer half whose other half
	// hasn't been prepared.
	State string `json:"state"`
	// LinkActive is false when the link or this direction is turned off: the
	// pair can then only be rejected.
	LinkActive bool `json:"link_active"`
}

func readProposalPair(ctx context.Context, tx pgx.Tx, s Scope, proposal string) (*ProposalPair, error) {
	var data []byte
	if err := tx.QueryRow(ctx, "SELECT public.proposal_pair_of($1,$2,$3)", s.TenantID, s.BookID, proposal).Scan(&data); err != nil {
		return nil, dbError(err)
	}
	if data == nil {
		return nil, nil
	}
	var out ProposalPair
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, ErrUncertain
	}
	return &out, nil
}

// EntryPair is the other entry of a decided pair, as seen from one entry.
type EntryPair struct {
	PairKey  string `json:"pair_key"`
	BookID   string `json:"book_id"`
	BookName string `json:"book_name"`
	EntryID  string `json:"entry_id"`
	// Side is the partner's side: payer or beneficiary.
	Side string `json:"side"`
	Kind string `json:"kind,omitempty"`
	// RepaidBy names the live settlement that repays this cost pair.
	RepaidBy string `json:"repaid_by,omitempty"`
	// Reconstructed says what repays this cost was worked out at the
	// upgrade from what had been repaid, not recorded one by one.
	Reconstructed bool `json:"reconstructed,omitempty"`
}

// errRepaid refuses undoing a cost a settlement repays: the settlement
// would then repay a cost that isn't there.
func errRepaid(p *EntryPair) error {
	return &RefusedError{Reason: "A settlement (" + p.RepaidBy + ") repays this cost; undo the settlement first."}
}

func readEntryPair(ctx context.Context, tx pgx.Tx, s Scope, entry string) (*EntryPair, error) {
	var data []byte
	if err := tx.QueryRow(ctx, "SELECT public.entry_pair_of($1,$2,$3)", s.TenantID, s.BookID, entry).Scan(&data); err != nil {
		return nil, dbError(err)
	}
	if data == nil {
		return nil, nil
	}
	var out EntryPair
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, ErrUncertain
	}
	return &out, nil
}

func pairUndoRefusal(partner *EntryPair) error {
	return &RefusedError{Reason: "This entry is half of a pair with " + partner.BookName + ", recorded together. The owner undoes it in Vult, which reverses both."}
}

type pairUndoKey struct{}

// withPairUndo marks the owner's undo of a pair, which reverses both entries.
func withPairUndo(ctx context.Context) context.Context {
	return context.WithValue(ctx, pairUndoKey{}, true)
}

func pairUndoing(ctx context.Context) bool {
	on, _ := ctx.Value(pairUndoKey{}).(bool)
	return on
}

// OwnerPair is the owner recording a cost one Book paid for a linked Book:
// the bank item in the Book that paid, and the account in the other Book
// the cost belongs to. Both entries are recorded at once, by the link's
// terms, as the owner's own action.
type OwnerPair struct {
	BankItemID           string `json:"bank_item_id"`
	BeneficiaryBookID    string `json:"beneficiary_book_id"`
	BeneficiaryAccountID string `json:"beneficiary_account_id"`
	Description          string `json:"description"`
}

// OwnerPairResult names the pair and its two entries.
type OwnerPairResult struct {
	PairKey            string `json:"pair_key"`
	PayerEntryID       string `json:"payer_entry_id"`
	BeneficiaryEntryID string `json:"beneficiary_entry_id"`
}

// RecordPair records both entries of a cost pair. The database checks the
// pair against the link's terms when it is recorded; undoing either entry
// later reverses both.
func (a TenantAdmin) RecordPair(ctx context.Context, actor string, q OwnerPair) (OwnerPairResult, error) {
	var out OwnerPairResult
	q.Description = strings.TrimSpace(q.Description)
	if !ValidID(actor) || !ValidID(q.BankItemID) || !ValidID(q.BeneficiaryBookID) || !ValidID(q.BeneficiaryAccountID) || q.Description == "" || len(q.Description) > 4000 {
		return out, ErrInvalid
	}
	err := a.withBookTransaction(ctx, q.BeneficiaryBookID, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		var payer, date string
		var amount int64
		err := tx.QueryRow(ctx, "SELECT book_id::text,occurred_on::text,abs(amount_minor) FROM public.bank_transactions WHERE tenant_id=$1 AND id=$2",
			scope.TenantID, q.BankItemID).Scan(&payer, &date, &amount)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return dbError(err)
		}
		if payer == q.BeneficiaryBookID {
			return &RefusedError{Reason: "A pair is between two Books; record this item in its own Book instead."}
		}
		payerScope := Scope{TenantID: scope.TenantID, BookID: payer, ActorID: actor}
		beneficiaryScope := Scope{TenantID: scope.TenantID, BookID: q.BeneficiaryBookID, ActorID: actor}
		var fresh bool
		if err := tx.QueryRow(ctx, "SELECT access.owner_write_allowed($1)", scope.TenantID).Scan(&fresh); err != nil || !fresh {
			return ErrFreshOwnerProof
		}
		var payerAccount, beneficiaryAccount *string
		if err := tx.QueryRow(ctx, "SELECT (x).payer_account_id::text,(x).beneficiary_account_id::text FROM (SELECT public.active_link_terms($1,$2,$3) AS x) v",
			scope.TenantID, payer, q.BeneficiaryBookID).Scan(&payerAccount, &beneficiaryAccount); err != nil {
			return dbError(err)
		}
		if payerAccount == nil || beneficiaryAccount == nil {
			return &RefusedError{Reason: "These Books aren't linked for this direction, or the link is turned off."}
		}
		books := []Scope{payerScope, beneficiaryScope}
		slices.SortFunc(books, func(x, y Scope) int { return strings.Compare(x.BookID, y.BookID) })
		for _, b := range books {
			if err := setScope(ctx, tx, b); err != nil {
				return dbError(err)
			}
			if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", b.TenantID, b.BookID); err != nil {
				return dbError(err)
			}
		}
		out.PairKey = "pair-" + ID()
		if err := setScope(ctx, tx, payerScope); err != nil {
			return dbError(err)
		}
		result, err := dispatch(ctx, tx, payerScope, "record_bank_activity", raw(RecordBankActivity{RequestKey: out.PairKey + "/payer", TransactionID: q.BankItemID,
			Description: q.Description, OtherAccountID: *payerAccount}), nil)
		if err != nil {
			return err
		}
		if out.PayerEntryID = outcomeEntry(&Outcome{Result: result}); out.PayerEntryID == "" {
			return ErrUncertain
		}
		// The payer's checks run in its Book; the beneficiary's wait until
		// its own entry is written.
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return dbError(err)
		}
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
			return dbError(err)
		}
		if err := setScope(ctx, tx, beneficiaryScope); err != nil {
			return dbError(err)
		}
		whole := strconv.FormatInt(amount, 10)
		result, err = dispatch(ctx, tx, beneficiaryScope, "post_journal", raw(PostJournal{RequestKey: out.PairKey + "/beneficiary", EconomicDate: date, AccountingDate: date,
			Description: q.Description, Lines: []PostLine{{AccountID: q.BeneficiaryAccountID, Side: "debit", AmountMinor: whole}, {AccountID: *beneficiaryAccount, Side: "credit", AmountMinor: whole}}}), nil)
		if err != nil {
			return err
		}
		if out.BeneficiaryEntryID = outcomeEntry(&Outcome{Result: result}); out.BeneficiaryEntryID == "" {
			return ErrUncertain
		}
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return dbError(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.entry_pairs(tenant_id,pair_key,kind,payer_book_id,payer_entry_id,beneficiary_book_id,beneficiary_entry_id)
 VALUES($1,$2,'cost',$3,$4,$5,$6)`, scope.TenantID, out.PairKey, payer, out.PayerEntryID, q.BeneficiaryBookID, out.BeneficiaryEntryID)
		if err != nil {
			return linkError(err)
		}
		return reviewTerms(ctx, tx, scope.TenantID, payer, q.BeneficiaryBookID, actor)
	})
	return out, err
}

// OwnerSettlement is the owner recording money one linked Book paid the
// other for what it owes: the debtor's bank item paying out, the creditor's
// taking in, and the cost pairs it repays.
type OwnerSettlement struct {
	DebtorItemID   string  `json:"debtor_item_id"`
	CreditorItemID string  `json:"creditor_item_id"`
	Repays         []Repay `json:"repays"`
}

// OwnerSettlementResult names the settlement and its two entries.
type OwnerSettlementResult struct {
	PairKey         string `json:"pair_key"`
	DebtorEntryID   string `json:"debtor_entry_id"`
	CreditorEntryID string `json:"creditor_entry_id"`
	// ExcessMinor is how much the payment was more than was owed, and
	// ExcessRecordedAs how the reverse direction recorded it: owner_draw
	// (to the paying business's owner's equity) or owed_back (owed to the
	// paying Book).
	ExcessMinor      string `json:"excess_minor"`
	ExcessRecordedAs string `json:"excess_recorded_as,omitempty"`
}

// RecordSettlement records both entries of a settlement at once, each
// item to the account the owed direction names in its Book. The database
// checks the amounts, dates, what is owed and the costs repaid.
func (a TenantAdmin) RecordSettlement(ctx context.Context, actor string, q OwnerSettlement) (OwnerSettlementResult, error) {
	var out OwnerSettlementResult
	if q.Repays == nil {
		q.Repays = []Repay{}
	}
	if !ValidID(actor) || !ValidID(q.DebtorItemID) || !ValidID(q.CreditorItemID) || !validSettlement(true, q.Repays, "settle") {
		return out, ErrInvalid
	}
	books, err := a.anyBook(ctx)
	if err != nil {
		return out, err
	}
	err = a.withBookTransaction(ctx, books, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		// The settlement records what its payment repays itself; the
		// plain-payment allocation stands aside for this transaction.
		if _, err := tx.Exec(ctx, "SELECT set_config('vult.allocating','on',true)"); err != nil {
			return dbError(err)
		}
		var fresh bool
		if err := tx.QueryRow(ctx, "SELECT access.owner_write_allowed($1)", scope.TenantID).Scan(&fresh); err != nil || !fresh {
			return ErrFreshOwnerProof
		}
		var d, c *string
		if err := tx.QueryRow(ctx, `SELECT (SELECT book_id::text FROM public.bank_transactions WHERE tenant_id=$1 AND id=$2),
 (SELECT book_id::text FROM public.bank_transactions WHERE tenant_id=$1 AND id=$3)`, scope.TenantID, q.DebtorItemID, q.CreditorItemID).Scan(&d, &c); err != nil {
			return dbError(err)
		}
		if d == nil || c == nil {
			return ErrNotFound
		}
		debtor, creditor := *d, *c
		if debtor == creditor {
			return &RefusedError{Reason: "A settlement is between two linked Books; this is a transfer within one Book."}
		}
		// One payment out of the paying Book, and its arrival in the other.
		var out1, out2 bool
		if err := tx.QueryRow(ctx, `SELECT coalesce(bool_or(x.id=$2 AND paid),false),coalesce(bool_or(x.id=$3 AND paid),false) FROM (SELECT t.id,
   coalesce((SELECT a.class FROM public.accounts a WHERE a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=coalesce(b.account_id,t.account_id))='liability',false)=(t.amount_minor>0) AS paid
 FROM public.bank_transactions t LEFT JOIN public.bank_accounts b ON b.tenant_id=t.tenant_id AND b.book_id=t.book_id AND b.id=t.bank_account_id
 WHERE t.tenant_id=$1 AND t.id IN ($2,$3)) x`, scope.TenantID, q.DebtorItemID, q.CreditorItemID).Scan(&out1, &out2); err != nil {
			return dbError(err)
		}
		switch {
		case out1 && out2:
			return &RefusedError{Reason: "Both items pay out; a settlement is one payment out and its arrival in the other Book."}
		case !out1 && !out2:
			return &RefusedError{Reason: "Both items are money in; a settlement is one payment out and its arrival in the other Book."}
		case !out1:
			return &RefusedError{Reason: "The paying item is money in; give the payment out as the debtor's item, and its arrival as the creditor's."}
		}
		var debtorAccount, creditorAccount *string
		if err := tx.QueryRow(ctx, `SELECT x.beneficiary_account_id::text,x.payer_account_id::text FROM public.book_link_terms x
 JOIN public.book_links l ON l.tenant_id=x.tenant_id AND l.id=x.link_id AND l.active
 WHERE x.tenant_id=$1 AND x.active AND x.treatment='owed' AND x.payer_book_id=$2 AND x.beneficiary_book_id=$3`, scope.TenantID, creditor, debtor).Scan(&debtorAccount, &creditorAccount); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return &RefusedError{Reason: "Nothing is owed this way between these Books: no active owed direction links them, or the link is turned off."}
			}
			return dbError(err)
		}
		debtorScope := Scope{TenantID: scope.TenantID, BookID: debtor, ActorID: actor}
		creditorScope := Scope{TenantID: scope.TenantID, BookID: creditor, ActorID: actor}
		locks := []Scope{debtorScope, creditorScope}
		slices.SortFunc(locks, func(x, y Scope) int { return strings.Compare(x.BookID, y.BookID) })
		for _, b := range locks {
			if err := setScope(ctx, tx, b); err != nil {
				return dbError(err)
			}
			if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", b.TenantID, b.BookID); err != nil {
				return dbError(err)
			}
		}
		out.PairKey = "settle-" + ID()
		// Any excess over what is owed goes to the reverse direction's
		// accounts, as its treatment says.
		var owed int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(CASE WHEN side='credit' THEN amount_minor ELSE -amount_minor END),0) FROM public.lines
 WHERE tenant_id=$1 AND book_id=$2 AND account_id=$3`, scope.TenantID, debtor, *debtorAccount).Scan(&owed); err != nil {
			return dbError(err)
		}
		var revTreatment, revDebtor, revCreditor *string
		if err := tx.QueryRow(ctx, "SELECT (x).treatment,(x).payer_account_id::text,(x).beneficiary_account_id::text FROM (SELECT public.active_link_terms($1,$2,$3) AS x) v",
			scope.TenantID, debtor, creditor).Scan(&revTreatment, &revDebtor, &revCreditor); err != nil {
			return dbError(err)
		}
		var excess int64
		record := func(s Scope, item, account, side string) (string, error) {
			if err := setScope(ctx, tx, s); err != nil {
				return "", dbError(err)
			}
			v, err := readBankItem(ctx, tx, s, item)
			if err != nil {
				return "", err
			}
			amount := v.amount
			if amount < 0 {
				amount = -amount
			}
			if side == "debtor" {
				excess = max(amount-max(owed, 0), 0)
			}
			change := RecordBankActivity{RequestKey: out.PairKey + "/" + side, TransactionID: item, Description: "Settlement between linked Books", OtherAccountID: account}
			if excess > 0 && revDebtor != nil {
				// The bank line for the whole amount, what was owed, and the excess.
				lines := itemLines(v, account)
				extra := *revDebtor
				if side == "creditor" {
					extra = *revCreditor
				}
				lines = lines[:1]
				if amount > excess {
					lines = append(lines, PostLine{AccountID: account, Side: map[string]string{"debtor": "debit", "creditor": "credit"}[side], AmountMinor: strconv.FormatInt(amount-excess, 10)})
				}
				lines = append(lines, PostLine{AccountID: extra, Side: map[string]string{"debtor": "debit", "creditor": "credit"}[side], AmountMinor: strconv.FormatInt(excess, 10)})
				change.OtherAccountID, change.Lines = "", lines
			}
			result, err := dispatch(ctx, tx, s, "record_bank_activity", raw(change), nil)
			if err != nil {
				return "", err
			}
			entry := outcomeEntry(&Outcome{Result: result})
			if entry == "" {
				return "", ErrUncertain
			}
			// Checks deferred in this Book run before the session moves on.
			if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
				return "", dbError(err)
			}
			_, err = tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED")
			return entry, dbError(err)
		}
		if out.DebtorEntryID, err = record(debtorScope, q.DebtorItemID, *debtorAccount, "debtor"); err != nil {
			return err
		}
		if out.CreditorEntryID, err = record(creditorScope, q.CreditorItemID, *creditorAccount, "creditor"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT public.settle_pair($1,$2,$3,$4,$5,$6,$7::jsonb)", scope.TenantID, out.PairKey, debtor, out.DebtorEntryID, creditor, out.CreditorEntryID, string(raw(q.Repays))); err != nil {
			return linkError(err)
		}
		out.ExcessMinor = strconv.FormatInt(excess, 10)
		if excess > 0 && revTreatment != nil {
			if out.ExcessRecordedAs, err = excessRecordedAs(ctx, tx, scope.TenantID, debtor, *revTreatment); err != nil {
				return err
			}
		}
		return remindReceipts(ctx, tx, scope.TenantID, debtor, out.DebtorEntryID, creditor, actor)
	})
	return out, err
}

// remindReceipts asks the owner, in the paying business's Book, for the
// receipt of each cost a settlement repays when an accountable plan needs
// it: a business repaying its owner with no owner's-equity direction.
func remindReceipts(ctx context.Context, tx pgx.Tx, tenant, debtor, settlement, creditor, owner string) error {
	// The owner writes the reminder, after an approval posted as the agent.
	if _, err := tx.Exec(ctx, "SELECT set_config('vult.connection_id','',true),set_config('vult.owner_posting','',true)"); err != nil {
		return dbError(err)
	}
	if err := setScope(ctx, tx, Scope{TenantID: tenant, BookID: debtor, ActorID: owner}); err != nil {
		return dbError(err)
	}
	rows, err := tx.Query(ctx, `SELECT e.id::text,e.description,e.accounting_date::text,public.book_amount($1,$2,
   (SELECT sum(z.amount_minor) FROM public.lines z WHERE z.tenant_id=$1 AND z.book_id=$2 AND z.entry_id=e.id AND z.side='credit')::bigint)
 FROM public.allocations r JOIN public.entries e ON e.tenant_id=r.tenant_id AND e.book_id=r.obligation_book_id AND e.id=r.obligation_entry_id
 JOIN public.entry_pairs p ON p.tenant_id=r.tenant_id AND p.beneficiary_book_id=r.obligation_book_id AND p.beneficiary_entry_id=r.obligation_entry_id
 WHERE r.tenant_id=$1 AND r.source_book_id=$2 AND r.source_entry_id=$3 AND r.removed_at IS NULL AND p.kind='cost'
   AND public.book_profile($1,$4)='personal' AND public.book_profile($1,$2)='business'
   AND NOT EXISTS (SELECT 1 FROM public.evidence_links v WHERE v.tenant_id=$1
       AND (v.book_id=p.beneficiary_book_id AND v.entry_id=p.beneficiary_entry_id OR v.book_id=p.payer_book_id AND v.entry_id=p.payer_entry_id)
       AND v.action='attach' AND v.role IN ('receipt','invoice')
       AND NOT EXISTS (SELECT 1 FROM public.evidence_links w WHERE w.tenant_id=v.tenant_id AND w.book_id=v.book_id AND w.link_id=v.link_id AND w.revision>v.revision))
   AND NOT EXISTS (SELECT 1 FROM public.source_citations c WHERE c.tenant_id=$1
       AND (c.book_id=p.beneficiary_book_id AND c.entry_id=p.beneficiary_entry_id OR c.book_id=p.payer_book_id AND c.entry_id=p.payer_entry_id))
 ORDER BY e.accounting_date,e.sequence`, tenant, debtor, settlement, creditor)
	if err != nil {
		return dbError(err)
	}
	type missing struct{ entry, description, date, amount string }
	var due []missing
	for rows.Next() {
		var m missing
		if err := rows.Scan(&m.entry, &m.description, &m.date, &m.amount); err != nil {
			rows.Close()
			return dbError(err)
		}
		due = append(due, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dbError(err)
	}
	for _, m := range due {
		if _, err := tx.Exec(ctx, `INSERT INTO public.agent_loops(tenant_id,book_id,id,actor_id,request_key,kind,subject_kind,subject_key,text)
 VALUES($1,$2,$3,$4,$5,'reminder','other',$6,$7) ON CONFLICT (tenant_id,book_id,actor_id,request_key) DO NOTHING`,
			tenant, debtor, ID(), owner, "receipt-"+m.entry, "receipt:"+m.entry,
			clip("Add the receipt for "+m.description+" ("+m.date+", "+m.amount+"), so the repayment is documented.", 1000)); err != nil {
			return noteError(err)
		}
	}
	// A partnership's repayment to a partner needs a look.
	var taxed string
	if err := tx.QueryRow(ctx, "SELECT coalesce(public.book_taxed_as($1,$2),'')", tenant, debtor).Scan(&taxed); err != nil {
		return dbError(err)
	}
	if taxed == TaxedPartnership && creditorPersonal(ctx, tx, tenant, creditor) {
		if err := ownerLoop(ctx, tx, tenant, debtor, owner, "partnership-repayment-"+debtor+"-"+creditor, "partnership:"+creditor,
			"Partnership repayments need a look: how partners are repaid depends on the partnership agreement."); err != nil {
			return err
		}
	}
	return reviewTerms(ctx, tx, tenant, debtor, creditor, owner)
}

func creditorPersonal(ctx context.Context, tx pgx.Tx, tenant, book string) bool {
	var personal bool
	_ = tx.QueryRow(ctx, "SELECT coalesce(public.book_profile($1,$2)='personal',false)", tenant, book).Scan(&personal)
	return personal
}

// ownerLoop opens something for the owner to look at in a Book, once.
func ownerLoop(ctx context.Context, tx pgx.Tx, tenant, book, owner, key, subject, text string) error {
	// The owner writes it, in that Book, whoever's change opened it.
	if _, err := tx.Exec(ctx, "SELECT set_config('vult.connection_id','',true),set_config('vult.owner_posting','',true)"); err != nil {
		return dbError(err)
	}
	if err := setScope(ctx, tx, Scope{TenantID: tenant, BookID: book, ActorID: owner}); err != nil {
		return dbError(err)
	}
	_, err := tx.Exec(ctx, `INSERT INTO public.agent_loops(tenant_id,book_id,id,actor_id,request_key,kind,subject_kind,subject_key,text)
 VALUES($1,$2,$3,$4,$5,'reminder','other',$6,$7) ON CONFLICT (tenant_id,book_id,actor_id,request_key) DO NOTHING`,
		tenant, book, ID(), owner, clip(key, 128), clip(subject, 200), clip(text, 1000))
	return noteError(err)
}

// reviewTerms asks the owner to look at a business's terms for paying its
// owner when they don't fit how the business is taxed: a C corporation's
// payments to its owner are owed back, and the others' are owner's equity.
// It never refuses anything.
func reviewTerms(ctx context.Context, tx pgx.Tx, tenant, business, personal, owner string) error {
	var profile, taxed, treatment, name, other string
	if err := tx.QueryRow(ctx, `SELECT coalesce(public.book_profile($1,$2),''),coalesce(public.book_taxed_as($1,$2),''),
 coalesce((public.active_link_terms($1,$2,$3)).treatment,''),
 coalesce((SELECT coalesce((SELECT r.name FROM public.book_revisions r WHERE r.tenant_id=k.tenant_id AND r.book_id=k.id ORDER BY r.revision DESC LIMIT 1),k.name) FROM public.books k WHERE k.tenant_id=$1 AND k.id=$2),''),
 coalesce((SELECT coalesce((SELECT r.name FROM public.book_revisions r WHERE r.tenant_id=k.tenant_id AND r.book_id=k.id ORDER BY r.revision DESC LIMIT 1),k.name) FROM public.books k WHERE k.tenant_id=$1 AND k.id=$3),'')`,
		tenant, business, personal).Scan(&profile, &taxed, &treatment, &name, &other); err != nil {
		return dbError(err)
	}
	if profile != "business" || !creditorPersonal(ctx, tx, tenant, personal) || treatment == "" {
		return nil
	}
	if name == "" {
		name = "This business"
	}
	if other == "" {
		other = "its owner"
	}
	// Only a C corporation's owner's-equity terms don't fit: it never pays
	// its owner draws.
	if taxed != TaxedCCorporation || treatment != "owner_equity" {
		return nil
	}
	words := "owed back"
	return ownerLoop(ctx, tx, tenant, business, owner, "terms-"+business+"-"+personal+"-"+taxed, "terms:"+personal,
		name+" is taxed as a C corporation, so what it pays for "+other+" is "+words+", never a draw. Switch \""+name+" pays for "+other+"\" to "+words+" in Links.")
}

// LinkedBalances is who owes whom between linked Books, and each Book's
// other liabilities. Amounts are in minor units, as text.
type LinkedBalances struct {
	Links []struct {
		LinkID             string   `json:"link_id"`
		DebtorBookID       string   `json:"debtor_book_id"`
		CreditorBookID     string   `json:"creditor_book_id"`
		AmountMinor        string   `json:"amount_minor"`
		Currency           string   `json:"currency"`
		Matches            bool     `json:"matches"`
		MismatchedEntryIDs []string `json:"mismatched_entry_ids"`
		OpenPairs          []struct {
			PairKey string `json:"pair_key"`
			// Kind is cost, or opening for an opening balance.
			Kind              string `json:"kind"`
			PayerBookID       string `json:"payer_book_id"`
			BeneficiaryBookID string `json:"beneficiary_book_id"`
			Date              string `json:"date"`
			Description       string `json:"description"`
			AmountMinor       string `json:"amount_minor"`
			// OpenMinor is what is still owed of it; a cost repaid in part
			// stays open for the rest.
			OpenMinor    string `json:"open_minor"`
			Repaid       bool   `json:"repaid"`
			PartlyRepaid bool   `json:"partly_repaid"`
			HasReceipt   bool   `json:"has_receipt"`
		} `json:"open_pairs"`
	} `json:"links"`
	Books []struct {
		BookID      string `json:"book_id"`
		Name        string `json:"name"`
		Liabilities []struct {
			AccountID    string `json:"account_id"`
			Name         string `json:"name"`
			Role         string `json:"role"`
			BalanceMinor string `json:"balance_minor"`
		} `json:"liabilities"`
	} `json:"books"`
}

// readLinkedBalances reads the balances of every link (book empty, for the
// owner) or of one Book's links.
func readLinkedBalances(ctx context.Context, tx pgx.Tx, tenant, book string) (LinkedBalances, error) {
	var out LinkedBalances
	var data []byte
	var b *string
	if book != "" {
		b = &book
	}
	if err := tx.QueryRow(ctx, "SELECT public.linked_balances($1,$2)", tenant, b).Scan(&data); err != nil {
		return out, dbError(err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, ErrUncertain
	}
	return out, nil
}

// OwnerLinkedBalances is who owes whom across the owner's linked Books.
func (a TenantAdmin) OwnerLinkedBalances(ctx context.Context) (LinkedBalances, error) {
	var out LinkedBalances
	book, err := a.anyBook(ctx)
	if err != nil || book == "" {
		return out, err
	}
	err = a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readLinkedBalances(ctx, tx, s.TenantID, "")
		return err
	})
	return out, err
}

// OpeningBalanceHalf is an agent's half of an opening balance between
// linked Books, from this Book's side.
type OpeningBalanceHalf struct {
	RequestKey  string `json:"request_key"`
	OtherBookID string `json:"other_book_id"`
	OwedBy      string `json:"owed_by"`
	AsOf        string `json:"as_of"`
	AmountMinor string `json:"amount_minor"`
	PairKey     string `json:"pair_key"`
	Description string `json:"description,omitempty"`
	Reason      string `json:"reason"`
}

// openingLines are one Book's lines of an opening balance: the owed
// direction's account against its opening-balance equity account.
func openingLines(ctx context.Context, tx pgx.Tx, s Scope, other string, owes bool, amount string) ([]PostLine, error) {
	var owedAccount, equity *string
	query := "SELECT (public.active_link_terms($1,$3,$2)).beneficiary_account_id::text,public.opening_equity_account($1,$2)::text"
	if !owes {
		query = "SELECT (public.active_link_terms($1,$2,$3)).payer_account_id::text,public.opening_equity_account($1,$2)::text"
	}
	if err := tx.QueryRow(ctx, query, s.TenantID, s.BookID, other).Scan(&owedAccount, &equity); err != nil {
		return nil, dbError(err)
	}
	if owedAccount == nil {
		var linked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.book_links l WHERE l.tenant_id=$1 AND l.active
 AND (l.book_a=$2 AND l.book_b=$3 OR l.book_a=$3 AND l.book_b=$2))`, s.TenantID, s.BookID, other).Scan(&linked); err != nil {
			return nil, dbError(err)
		}
		if !linked {
			return nil, &RefusedError{Reason: "These Books aren't linked: link them in Links, then enter the opening balance."}
		}
		return nil, &RefusedError{Reason: "Nothing is owed this way between these Books: set the direction to owed back in Links, then enter the opening balance."}
	}
	if equity == nil {
		var name string
		if err := tx.QueryRow(ctx, "SELECT name FROM public.books WHERE tenant_id=$1 AND id=$2", s.TenantID, s.BookID).Scan(&name); err != nil {
			return nil, dbError(err)
		}
		return nil, &RefusedError{Reason: "Add an opening balances account to " + name + " (an equity account for opening balances), then enter the opening balance."}
	}
	if owes {
		return []PostLine{{AccountID: *equity, Side: "debit", AmountMinor: amount}, {AccountID: *owedAccount, Side: "credit", AmountMinor: amount}}, nil
	}
	return []PostLine{{AccountID: *owedAccount, Side: "debit", AmountMinor: amount}, {AccountID: *equity, Side: "credit", AmountMinor: amount}}, nil
}

func validAmount(amount string) bool {
	n, err := strconv.ParseInt(amount, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == amount
}

// prepareOpeningBalance prepares this Book's half as a journal for the
// owner's approval; the other Book's half is prepared through its own
// connection, with the same pair key.
func (s Store) prepareOpeningBalance(ctx context.Context, scope Scope, input []byte) (json.RawMessage, error) {
	var q OpeningBalanceHalf
	if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || len(q.RequestKey) > 120 || !ValidID(q.OtherBookID) || q.OtherBookID == scope.BookID ||
		q.OwedBy != "this_book" && q.OwedBy != "other_book" || !ValidDate(q.AsOf) || !validAmount(q.AmountMinor) || !validPairKey(q.PairKey) ||
		strings.TrimSpace(q.Reason) == "" || len(q.Reason) > 4000 || len(q.Description) > 4000 {
		return nil, ErrInvalid
	}
	var lines []PostLine
	_, err := s.transact(ctx, scope, "get_book_links", nil, func(tx pgx.Tx) (json.RawMessage, error) {
		var err error
		lines, err = openingLines(ctx, tx, scope, q.OtherBookID, q.OwedBy == "this_book", q.AmountMinor)
		return nil, err
	})
	if err != nil {
		return nil, err
	}
	description := strings.TrimSpace(q.Description)
	if description == "" {
		description = "Opening balance with a linked Book"
	}
	args := raw(PostJournal{RequestKey: q.RequestKey + "/entry", EconomicDate: q.AsOf, AccountingDate: q.AsOf, Description: description, Lines: lines,
		PairKey: q.PairKey, Reason: q.Reason, Opening: true, OtherBookID: q.OtherBookID})
	return s.prepareOperation(ctx, scope, raw(PrepareOperation{RequestKey: q.RequestKey, Operation: "post_journal", Arguments: args}))
}

// OwnerOpeningBalance is the owner entering, at once in both Books, what one
// linked Book owed the other as of a date.
type OwnerOpeningBalance struct {
	DebtorBookID   string `json:"debtor_book_id"`
	CreditorBookID string `json:"creditor_book_id"`
	AsOf           string `json:"as_of"`
	AmountMinor    string `json:"amount_minor"`
	Description    string `json:"description,omitempty"`
}

// OwnerOpeningBalanceResult names the opening balance and its entries.
type OwnerOpeningBalanceResult struct {
	PairKey         string `json:"pair_key"`
	DebtorEntryID   string `json:"debtor_entry_id"`
	CreditorEntryID string `json:"creditor_entry_id"`
}

// RecordOpeningBalance posts the mirrored owed balance in both Books at
// once; undoing either entry reverses both.
func (a TenantAdmin) RecordOpeningBalance(ctx context.Context, actor string, q OwnerOpeningBalance) (OwnerOpeningBalanceResult, error) {
	var out OwnerOpeningBalanceResult
	q.Description = strings.TrimSpace(q.Description)
	if q.Description == "" {
		q.Description = "Opening balance with a linked Book"
	}
	if !ValidID(actor) || !ValidID(q.DebtorBookID) || !ValidID(q.CreditorBookID) || q.DebtorBookID == q.CreditorBookID || !ValidDate(q.AsOf) ||
		!validAmount(q.AmountMinor) || len(q.Description) > 4000 {
		return out, ErrInvalid
	}
	err := a.withBookTransaction(ctx, q.DebtorBookID, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		var fresh bool
		if err := tx.QueryRow(ctx, "SELECT access.owner_write_allowed($1)", scope.TenantID).Scan(&fresh); err != nil || !fresh {
			return ErrFreshOwnerProof
		}
		debtor := Scope{TenantID: scope.TenantID, BookID: q.DebtorBookID, ActorID: actor}
		creditor := Scope{TenantID: scope.TenantID, BookID: q.CreditorBookID, ActorID: actor}
		locks := []Scope{debtor, creditor}
		slices.SortFunc(locks, func(x, y Scope) int { return strings.Compare(x.BookID, y.BookID) })
		for _, b := range locks {
			if err := setScope(ctx, tx, b); err != nil {
				return dbError(err)
			}
			if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", b.TenantID, b.BookID); err != nil {
				return dbError(err)
			}
		}
		out.PairKey = "opening-" + ID()
		post := func(s Scope, other string, owes bool, side string) (string, error) {
			if err := setScope(ctx, tx, s); err != nil {
				return "", dbError(err)
			}
			var closedThrough *string
			if err := tx.QueryRow(ctx, `SELECT max(x.end_date)::text FROM public.accounting_periods x WHERE x.tenant_id=$1 AND x.book_id=$2
 AND $3::date BETWEEN x.start_date AND x.end_date AND public.ledger_period_closed($1,$2,$3::date)`, s.TenantID, s.BookID, q.AsOf).Scan(&closedThrough); err != nil {
				return "", dbError(err)
			}
			if closedThrough != nil {
				var name string
				if err := tx.QueryRow(ctx, "SELECT name FROM public.books WHERE tenant_id=$1 AND id=$2", s.TenantID, s.BookID).Scan(&name); err != nil {
					return "", dbError(err)
				}
				return "", &RefusedError{Reason: name + "'s period through " + *closedThrough + " is closed; reopen it, or enter the opening balance as of a date after it."}
			}
			lines, err := openingLines(ctx, tx, s, other, owes, q.AmountMinor)
			if err != nil {
				return "", err
			}
			result, err := dispatch(ctx, tx, s, "post_journal", raw(PostJournal{RequestKey: out.PairKey + "/" + side, EconomicDate: q.AsOf, AccountingDate: q.AsOf,
				Description: q.Description, Lines: lines}), nil)
			if err != nil {
				return "", err
			}
			entry := outcomeEntry(&Outcome{Result: result})
			if entry == "" {
				return "", ErrUncertain
			}
			if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
				return "", dbError(err)
			}
			_, err = tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED")
			return entry, dbError(err)
		}
		var err error
		if out.DebtorEntryID, err = post(debtor, q.CreditorBookID, true, "debtor"); err != nil {
			return err
		}
		if out.CreditorEntryID, err = post(creditor, q.DebtorBookID, false, "creditor"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "SELECT public.open_link_balance($1,$2,$3,$4,$5,$6)", scope.TenantID, out.PairKey, q.DebtorBookID, out.DebtorEntryID, q.CreditorBookID, out.CreditorEntryID)
		return linkError(err)
	})
	return out, err
}

// SettlementCost is one cost a repayment would cover: how much of it, what
// was open of it, and what is left after.
type SettlementCost struct {
	PairKey        string `json:"pair_key"`
	AmountMinor    string `json:"amount_minor"`
	OpenMinor      string `json:"open_minor"`
	LeftAfterMinor string `json:"left_after_minor"`
}

// SettlementPreview is what a repayment would record: unless the owner
// chose, the oldest open costs first, the last one in part. NamesExpected
// says the paying Book's records expect the costs named; it never blocks.
type SettlementPreview struct {
	RepayingMinor    string           `json:"repaying_minor"`
	ExcessMinor      string           `json:"excess_minor"`
	ExcessRecordedAs string           `json:"excess_recorded_as,omitempty"`
	NamesExpected    bool             `json:"names_expected"`
	Costs            []SettlementCost `json:"costs"`
}

// PreviewSettlement says what RecordSettlement would record for the same
// request, without recording anything.
func (a TenantAdmin) PreviewSettlement(ctx context.Context, q OwnerSettlement) (SettlementPreview, error) {
	var out SettlementPreview
	if q.Repays == nil {
		q.Repays = []Repay{}
	}
	if !ValidID(q.DebtorItemID) || !ValidID(q.CreditorItemID) || !validSettlement(true, q.Repays, "settle") {
		return out, ErrInvalid
	}
	books, err := a.anyBook(ctx)
	if err != nil {
		return out, err
	}
	err = a.withBookTransaction(ctx, books, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		var found int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM public.bank_transactions WHERE tenant_id=$1 AND id IN ($2,$3)", scope.TenantID, q.DebtorItemID, q.CreditorItemID).Scan(&found); err != nil {
			return dbError(err)
		}
		if found != 2 {
			return ErrNotFound
		}
		var preview []byte
		if err := tx.QueryRow(ctx, "SELECT public.settlement_preview($1,$2,$3,$4::jsonb)", scope.TenantID, q.DebtorItemID, q.CreditorItemID, string(raw(q.Repays))).Scan(&preview); err != nil {
			return linkError(err)
		}
		if json.Unmarshal(preview, &out) != nil {
			return ErrUncertain
		}
		// How the paying Book is taxed names the extra, and whether its
		// records expect the costs named (an accountable plan).
		var debtor, creditor, debtorTaxed, creditorProfile, reverse string
		if err := tx.QueryRow(ctx, `SELECT d.book_id::text,c.book_id::text,coalesce(public.book_taxed_as($1,d.book_id),''),coalesce(public.book_profile($1,c.book_id),''),
 coalesce((public.active_link_terms($1,d.book_id,c.book_id)).treatment,'')
 FROM public.bank_transactions d, public.bank_transactions c WHERE d.tenant_id=$1 AND d.id=$2 AND c.tenant_id=$1 AND c.id=$3`,
			scope.TenantID, q.DebtorItemID, q.CreditorItemID).Scan(&debtor, &creditor, &debtorTaxed, &creditorProfile, &reverse); err != nil {
			return dbError(err)
		}
		out.NamesExpected = creditorProfile == "personal" && (debtorTaxed == TaxedSCorporation || debtorTaxed == TaxedCCorporation)
		if out.ExcessMinor != "0" && out.ExcessRecordedAs != "" {
			var err error
			if out.ExcessRecordedAs, err = excessRecordedAs(ctx, tx, scope.TenantID, debtor, reverse); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// excessRecordedAs names how the extra of a repayment is recorded, by the
// reverse direction's terms and how the paying Book is taxed: a business
// paying its owner more than it owes makes a shareholder distribution (an
// S corporation), a partner's draw (a partnership) or an owner's draw;
// otherwise the extra is owed back.
func excessRecordedAs(ctx context.Context, tx pgx.Tx, tenant, debtor, reverse string) (string, error) {
	if reverse != "owner_equity" {
		return "owed_back", nil
	}
	var taxed string
	if err := tx.QueryRow(ctx, "SELECT coalesce(public.book_taxed_as($1,$2),'')", tenant, debtor).Scan(&taxed); err != nil {
		return "", dbError(err)
	}
	switch taxed {
	case TaxedSCorporation:
		return "shareholder_distribution", nil
	case TaxedPartnership:
		return "partner_draw", nil
	}
	return "owner_draw", nil
}

// RepayKeys repays each cost named by its key: what is left of each.
func RepayKeys(keys ...string) []Repay {
	out := make([]Repay, 0, len(keys))
	for _, k := range keys {
		out = append(out, Repay{PairKey: k})
	}
	return out
}

// reviewTaxedLinks opens, for each active link whose terms no longer fit
// how the Book is now taxed, something for the owner to switch.
func reviewTaxedLinks(ctx context.Context, tx pgx.Tx, tenant, book, owner string) error {
	rows, err := tx.Query(ctx, `SELECT x.beneficiary_book_id::text FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id=x.tenant_id AND l.id=x.link_id
 WHERE x.tenant_id=$1 AND x.payer_book_id=$2 AND x.active AND l.active AND x.treatment='owner_equity'`, tenant, book)
	if err != nil {
		return dbError(err)
	}
	var others []string
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			rows.Close()
			return dbError(err)
		}
		others = append(others, other)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dbError(err)
	}
	for _, other := range others {
		if err := reviewTerms(ctx, tx, tenant, book, other, owner); err != nil {
			return err
		}
	}
	return nil
}

// drawAccountName is what Vult names a business's equity account for what
// it pays its owner, by how the business is taxed; "" leaves it as it is.
func drawAccountName(taxed, owner string) string {
	switch taxed {
	case TaxedSCorporation:
		return "Shareholder distributions to " + owner
	case TaxedPartnership:
		return "Partner draws to " + owner
	case TaxedSoleOwner:
		return "Owner draws to " + owner
	}
	return ""
}

// renameDrawAccounts names the equity accounts behind a business's
// owner's-equity link terms for how it is now taxed, through an ordinary
// account revision, only where the name is still one Vult gave it.
func renameDrawAccounts(ctx context.Context, tx pgx.Tx, tenant, book, actor, taxed string) error {
	rows, err := tx.Query(ctx, `SELECT x.payer_account_id::text,r.code,r.name,r.revision,
 coalesce((SELECT coalesce((SELECT v.name FROM public.book_revisions v WHERE v.tenant_id=k.tenant_id AND v.book_id=k.id ORDER BY v.revision DESC LIMIT 1),k.name)
   FROM public.books k WHERE k.tenant_id=x.tenant_id AND k.id=x.beneficiary_book_id),'')
 FROM public.book_link_terms x JOIN LATERAL (SELECT a.code,a.name,a.revision FROM public.account_revisions a
   WHERE a.tenant_id=x.tenant_id AND a.book_id=x.payer_book_id AND a.account_id=x.payer_account_id ORDER BY a.revision DESC LIMIT 1) r ON true
 WHERE x.tenant_id=$1 AND x.payer_book_id=$2 AND x.active AND x.treatment='owner_equity'`, tenant, book)
	if err != nil {
		return dbError(err)
	}
	type rename struct {
		account, code, name string
		revision            int64
	}
	var due []rename
	for rows.Next() {
		var account, code, name, owner string
		var revision int64
		if err := rows.Scan(&account, &code, &name, &revision, &owner); err != nil {
			rows.Close()
			return dbError(err)
		}
		target := drawAccountName(taxed, owner)
		generated := name == "Draws to "+owner || name == drawAccountName(TaxedSCorporation, owner) ||
			name == drawAccountName(TaxedPartnership, owner) || name == drawAccountName(TaxedSoleOwner, owner)
		if owner == "" || target == "" || !generated || name == target {
			continue
		}
		due = append(due, rename{account, code, target, revision})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dbError(err)
	}
	if len(due) == 0 {
		return nil
	}
	scope := Scope{TenantID: tenant, BookID: book, ActorID: actor}
	if err := setScope(ctx, tx, scope); err != nil {
		return dbError(err)
	}
	for _, r := range due {
		if _, err := dispatch(ctx, tx, scope, "revise_account", raw(ReviseAccount{RequestKey: "taxed-name-" + r.account + "-" + strconv.FormatInt(r.revision, 10),
			AccountID: r.account, ExpectedRevision: r.revision, Code: r.code, Name: r.name}), nil); err != nil {
			return err
		}
	}
	return nil
}
