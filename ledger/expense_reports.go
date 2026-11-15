package ledger

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Expense reports: a business pays back a person for costs they paid. A
// report has one reference, "ER-" and twelve random Crockford base32
// characters, that never changes and means nothing; claims that keep
// their identity across revisions; revisions frozen at submission and an
// approval bound to one of them; payments applied through the same stored
// allocations as every other repayment.

const referenceAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewReportReference makes a reference from a cryptographic source.
func NewReportReference() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	out := make([]byte, 12)
	for i, v := range b {
		out[i] = referenceAlphabet[int(v)%32]
	}
	return "ER-" + string(out[:4]) + "-" + string(out[4:8]) + "-" + string(out[8:]), nil
}

// NormalizeReportReference reads a reference as typed: any case, with or
// without dashes and spaces, O read as 0 and I or L as 1.
func NormalizeReportReference(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "", " ", "", "O", "0", "I", "1", "L", "1").Replace(s)
	if !strings.HasPrefix(s, "ER") || len(s) != 14 {
		return "", false
	}
	s = s[2:]
	for _, c := range s {
		if !strings.ContainsRune(referenceAlphabet, c) {
			return "", false
		}
	}
	return "ER-" + s[:4] + "-" + s[4:8] + "-" + s[8:], true
}

// ReportMemo is what the business writes on the payment: the payment
// already comes from the business's own account.
func ReportMemo(reference string) string {
	return reference + " Expense reimbursement"
}

// Person is the business's record of someone it pays back.
type Person struct {
	ID             string `json:"person_id"`
	BookID         string `json:"book_id"`
	Name           string `json:"name"`
	Capacity       string `json:"capacity"`
	EffectiveFrom  string `json:"effective_from,omitempty"`
	PersonalBookID string `json:"personal_book_id,omitempty"`
	Revision       int    `json:"revision"`
}

// PersonChange adds a person, or changes one at the revision read.
type PersonChange struct {
	ID       string `json:"person_id,omitempty"`
	Name     string `json:"name"`
	Capacity string `json:"capacity"`
	// EffectiveFrom is the day the capacity applies from; left out, from
	// the start.
	EffectiveFrom    string `json:"effective_from,omitempty"`
	PersonalBookID   string `json:"personal_book_id,omitempty"`
	ExpectedRevision int    `json:"expected_revision,omitempty"`
}

// capacities are how a business records someone it pays back.
// not_established says it hasn't settled the relationship: no employee
// guidance applies, and a person with no Book is fine.
var capacities = map[string]bool{"employee": true, "shareholder_employee": true, "partner": true, "sole_owner": true, "not_established": true}

const personColumns = `id::text,business_book_id::text,name,capacity,coalesce(effective_from::text,''),coalesce(personal_book_id::text,''),revision`

func scanPerson(row pgx.Row, p *Person) error {
	return row.Scan(&p.ID, &p.BookID, &p.Name, &p.Capacity, &p.EffectiveFrom, &p.PersonalBookID, &p.Revision)
}

// ReportPolicy is a business's expense policy in force: the version its
// owner adopted, with the words adopted. Version 0 is none adopted yet: the
// owner approves their own reports, flagged, under the profile's checks.
type ReportPolicy struct {
	Version               int         `json:"version"`
	EffectiveFrom         string      `json:"effective_from,omitempty"`
	AdoptedAt             *time.Time  `json:"adopted_at,omitempty"`
	AdoptedBy             string      `json:"adopted_by,omitempty"`
	AdoptedText           string      `json:"adopted_text,omitempty"`
	CoversAll             bool        `json:"covers_all"`
	Covered               []PersonRef `json:"covered"`
	Methods               []string    `json:"methods"`
	ApprovalKind          string      `json:"approval_kind"`
	SelfApprovalPermitted bool        `json:"self_approval_permitted"`
	Approvers             []PersonRef `json:"approvers"`
	Fallback              *PersonRef  `json:"fallback"`
	ReceiptThresholdMinor string      `json:"receipt_threshold_minor"`
	LodgingAlways         bool        `json:"lodging_always"`
	ExceptionAcceptors    []PersonRef `json:"exception_acceptors"`
	SubstantiationDays    int         `json:"substantiation_days"`
	ExcessDays            int         `json:"excess_days"`
	Reviewer              *PersonRef  `json:"reviewer"`
	ReimbursementCurrency string      `json:"reimbursement_currency,omitempty"`
	ConversionMethod      string      `json:"conversion_method"`
	RuleProfile           string      `json:"rule_profile"`
	RetentionYears        int         `json:"retention_years"`
	AttestationText       string      `json:"attestation_text"`
	AttestationVersion    int         `json:"attestation_version"`
}

type PersonRef struct {
	ID   string `json:"person_id"`
	Name string `json:"name"`
}

// PolicyAdoption is a new version of a business's expense policy, adopted
// by its owner: the words adopted and the rules taken from them.
type PolicyAdoption struct {
	EffectiveFrom              string   `json:"effective_from"`
	AdoptedText                string   `json:"adopted_text"`
	CoversAll                  *bool    `json:"covers_all,omitempty"`
	CoveredPersonIDs           []string `json:"covered_person_ids,omitempty"`
	Methods                    []string `json:"methods,omitempty"`
	ApprovalKind               string   `json:"approval_kind"`
	ApproverPersonIDs          []string `json:"approver_person_ids,omitempty"`
	FallbackPersonID           string   `json:"fallback_person_id,omitempty"`
	ReceiptThresholdMinor      string   `json:"receipt_threshold_minor,omitempty"`
	LodgingAlways              *bool    `json:"lodging_always,omitempty"`
	ExceptionAcceptorPersonIDs []string `json:"exception_acceptor_person_ids,omitempty"`
	SubstantiationDays         int      `json:"substantiation_days,omitempty"`
	ExcessDays                 int      `json:"excess_days,omitempty"`
	ReviewerPersonID           string   `json:"reviewer_person_id,omitempty"`
	ReimbursementCurrency      string   `json:"reimbursement_currency,omitempty"`
	ConversionMethod           string   `json:"conversion_method,omitempty"`
	RuleProfile                string   `json:"rule_profile,omitempty"`
	RetentionYears             int      `json:"retention_years,omitempty"`
	AttestationText            string   `json:"attestation_text,omitempty"`
}

// profileReceiptMinor is the profile's own receipt check: a receipt for
// any cost of $75 or more, and for lodging. A company policy never lowers
// it.
const profileReceiptMinor = 7500

// ReportSummary is a report in a link's list.
type ReportSummary struct {
	ID                 string    `json:"id"`
	Reference          string    `json:"reference"`
	CreatedAt          time.Time `json:"created_at"`
	Workflow           string    `json:"workflow"`
	Settlement         string    `json:"settlement"`
	RequestedMinor     string    `json:"requested_minor"`
	ApprovedMinor      string    `json:"approved_minor"`
	PaidMinor          string    `json:"paid_minor"`
	RemainingMinor     string    `json:"remaining_minor"`
	SelfApproved       bool      `json:"self_approved"`
	PaidBeforeApproval bool      `json:"paid_before_approval"`
}

// NewReport starts a draft for a person a business pays back. A reference
// given is one already sent, for a report made after the money was paid.
type NewReport struct {
	LinkID          string `json:"link_id"`
	PersonID        string `json:"person_id"`
	Reference       string `json:"reference,omitempty"`
	PreparedByAgent bool   `json:"-"`
}

// ClaimInput adds a cost to a draft: one already recorded as owed (by its
// pair), or a cash cost with its details.
type ClaimInput struct {
	// Kind is "cost" for a cost already recorded as owed (with its
	// pair_key) or "cash" for one the person paid that isn't recorded yet.
	// Left out, the pair_key decides.
	Kind         string `json:"kind,omitempty"`
	PairKey      string `json:"pair_key,omitempty"`
	Date         string `json:"date,omitempty"`
	Vendor       string `json:"vendor,omitempty"`
	AmountMinor  string `json:"amount_minor,omitempty"`
	PortionMinor string `json:"portion_minor,omitempty"`
	// BusinessPortionMinor is another name for PortionMinor.
	BusinessPortionMinor string `json:"business_portion_minor,omitempty"`
	Purpose              string `json:"purpose,omitempty"`
	Lodging              *bool  `json:"lodging,omitempty"`
	ProposedAccountID    string `json:"proposed_account_id,omitempty"`
	PaidFromAccountID    string `json:"paid_from_account_id,omitempty"`
	MissingExplanation   string `json:"missing_explanation,omitempty"`
	Provenance           string `json:"provenance,omitempty"`
	// SupplierInvoice is the supplier's invoice facts, kept exactly as
	// given.
	SupplierInvoice json.RawMessage `json:"supplier_invoice,omitempty"`
}

// normalize settles the claim's other names: the kind against the pair,
// and the business portion under either name.
func (q *ClaimInput) normalize(adding bool) error {
	if q.BusinessPortionMinor != "" {
		if q.PortionMinor != "" && q.PortionMinor != q.BusinessPortionMinor {
			return &RefusedError{Reason: "Give the business's portion once."}
		}
		q.PortionMinor, q.BusinessPortionMinor = q.BusinessPortionMinor, ""
	}
	switch q.Kind {
	case "":
	case "cost":
		if adding && q.PairKey == "" {
			return &RefusedError{Reason: "Choose the recorded cost this claim is for."}
		}
	case "cash":
		if q.PairKey != "" {
			return &RefusedError{Reason: "A cash claim isn't for a recorded cost: leave the pair out."}
		}
	default:
		return &RefusedError{Reason: "A claim is a recorded cost or a cash cost."}
	}
	q.Kind = ""
	return nil
}

// errReport turns the ledger's report refusals into sentences.
func errReport(err error) error {
	if err == nil {
		return nil
	}
	var refused *RefusedError
	if errors.As(err, &refused) {
		return err
	}
	var p *pgconn.PgError
	if errors.As(err, &p) {
		switch p.Code {
		case "VE409":
			return &ReportConflict{Reason: p.Message}
		case "VE412":
			return ErrRevisionChanged
		case "VE422", "VB401":
			return &RefusedError{Reason: p.Message}
		case "23505":
			if strings.Contains(p.ConstraintName, "reference") {
				return &RefusedError{Reason: "That reference is already used by another report."}
			}
			if p.ConstraintName == "claims_one_per_cost" {
				return &ReportConflict{Reason: "This cost is already on another report."}
			}
		}
	}
	return linkError(err)
}

// ReportConflict is a claim already on another report, or a change that
// raced another.
type ReportConflict struct{ Reason string }

func (e *ReportConflict) Error() string { return e.Reason }

// ErrRevisionChanged is a report that changed since it was read.
var ErrRevisionChanged = errors.New("This report changed since you opened it. Review it again.")

// withExpenseWriter runs a change to a business's reports as the owner.
// fresh asks for a recent sign-in.
func (a TenantAdmin) withExpenseWriter(ctx context.Context, book, actor string, fresh bool, call func(pgx.Tx, Scope) error) error {
	if !ValidID(actor) {
		return ErrDenied
	}
	return a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		scope.ActorID = actor
		if err := setScope(ctx, tx, scope); err != nil {
			return dbError(err)
		}
		if fresh {
			var ok bool
			if err := tx.QueryRow(ctx, "SELECT access.owner_write_allowed($1)", scope.TenantID).Scan(&ok); err != nil || !ok {
				return ErrFreshOwnerProof
			}
		}
		if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); err != nil {
			return dbError(err)
		}
		return errReport(call(tx, scope))
	})
}

// People lists the people a business pays back.
func (a TenantAdmin) People(ctx context.Context, book string) ([]Person, error) {
	out := []Person{}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		rows, err := tx.Query(ctx, `SELECT `+personColumns+`
 FROM public.people WHERE tenant_id=$1 AND business_book_id=$2 ORDER BY lower(name),id`, s.TenantID, s.BookID)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var p Person
			if err := scanPerson(rows, &p); err != nil {
				return dbError(err)
			}
			out = append(out, p)
		}
		return dbError(rows.Err())
	})
	return out, err
}

// SavePerson adds a person or changes one, with a recent sign-in. Their
// capacity is what the business records; Vult never infers it.
func (a TenantAdmin) SavePerson(ctx context.Context, book, actor string, q PersonChange) (Person, error) {
	var out Person
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" || len([]rune(q.Name)) > 120 || !capacities[q.Capacity] || q.ID != "" && !ValidID(q.ID) || q.PersonalBookID != "" && !ValidID(q.PersonalBookID) ||
		q.EffectiveFrom != "" && !ValidDate(q.EffectiveFrom) {
		return out, ErrInvalid
	}
	err := a.withExpenseWriter(ctx, book, actor, true, func(tx pgx.Tx, s Scope) error {
		var personal any
		if q.PersonalBookID != "" {
			personal = q.PersonalBookID
		}
		var err error
		if q.ID == "" {
			q.ID = ID()
			_, err = tx.Exec(ctx, `INSERT INTO public.people(tenant_id,business_book_id,id,revision,name,capacity,effective_from,personal_book_id,actor_id)
 VALUES($1,$2,$3,1,$4,$5,nullif($6,'')::date,$7,$8)`, s.TenantID, s.BookID, q.ID, q.Name, q.Capacity, q.EffectiveFrom, personal, actor)
		} else {
			var tag pgconn.CommandTag
			tag, err = tx.Exec(ctx, `UPDATE public.people SET revision=revision+1,name=$4,capacity=$5,personal_book_id=$6,actor_id=$7,updated_at=clock_timestamp(),
 effective_from=nullif($9,'')::date WHERE tenant_id=$1 AND business_book_id=$2 AND id=$3 AND revision=$8`, s.TenantID, s.BookID, q.ID, q.Name, q.Capacity, personal, actor, q.ExpectedRevision, q.EffectiveFrom)
			if err == nil && tag.RowsAffected() == 0 {
				return &ReportConflict{Reason: "This person's record changed since you opened it."}
			}
		}
		if err != nil {
			return err
		}
		return scanPerson(tx.QueryRow(ctx, `SELECT `+personColumns+` FROM public.people WHERE tenant_id=$1 AND id=$2`, s.TenantID, q.ID), &out)
	})
	return out, err
}

// ReportPolicy is the business's policy in force today.
func (a TenantAdmin) ReportPolicy(ctx context.Context, book string) (ReportPolicy, error) {
	var out ReportPolicy
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readPolicy(ctx, tx, s.TenantID, s.BookID, "")
		return err
	})
	return out, err
}

// ReportPolicies is every version the business adopted, newest first.
func (a TenantAdmin) ReportPolicies(ctx context.Context, book string) ([]ReportPolicy, error) {
	out := []ReportPolicy{}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		rows, err := tx.Query(ctx, "SELECT version FROM public.report_policies WHERE tenant_id=$1 AND business_book_id=$2 ORDER BY version DESC", s.TenantID, s.BookID)
		if err != nil {
			return dbError(err)
		}
		var versions []int
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return dbError(err)
			}
			versions = append(versions, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return dbError(err)
		}
		for _, v := range versions {
			p, err := readPolicyVersion(ctx, tx, s.TenantID, s.BookID, v)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// defaultPolicy applies before a business adopts one.
func defaultPolicy() ReportPolicy {
	return ReportPolicy{CoversAll: true, Covered: []PersonRef{}, Methods: []string{"actual_expense", "cash"}, ApprovalKind: "owner_self", SelfApprovalPermitted: true,
		Approvers: []PersonRef{}, ReceiptThresholdMinor: strconv.Itoa(profileReceiptMinor), LodgingAlways: true, ExceptionAcceptors: []PersonRef{},
		SubstantiationDays: 60, ExcessDays: 120, ConversionMethod: "same_currency", RuleProfile: "us_actual_expense_v1", RetentionYears: 7,
		AttestationText: attestationText, AttestationVersion: 1}
}

// readPolicy is the version in force on a day ("" is today), or the
// default before one applies.
func readPolicy(ctx context.Context, tx pgx.Tx, tenant, book, on string) (ReportPolicy, error) {
	var version *int
	if err := tx.QueryRow(ctx, "SELECT public.policy_version_on($1,$2,coalesce(nullif($3,'')::date,current_date))", tenant, book, on).Scan(&version); err != nil {
		return ReportPolicy{}, dbError(err)
	}
	if version == nil {
		return defaultPolicy(), nil
	}
	return readPolicyVersion(ctx, tx, tenant, book, *version)
}

func readPolicyVersion(ctx context.Context, tx pgx.Tx, tenant, book string, version int) (ReportPolicy, error) {
	out := defaultPolicy()
	var covered, approvers, acceptors []string
	var fallback, reviewer string
	var threshold int64
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT p.version,p.effective_from::text,p.adopted_at,p.adopted_by::text,p.adopted_text,p.covers_all,p.covered_person_ids::text[],p.methods,
 p.approval_kind,p.approver_person_ids::text[],coalesce(p.fallback_person_id::text,''),p.receipt_threshold_minor,p.lodging_always,p.exception_acceptor_person_ids::text[],
 p.substantiation_days,p.excess_days,coalesce(p.reviewer_person_id::text,''),p.reimbursement_currency,p.conversion_method,p.rule_profile,p.retention_years,
 p.attestation_text,p.attestation_version
 FROM public.report_policies p WHERE p.tenant_id=$1 AND p.business_book_id=$2 AND p.version=$3`, tenant, book, version).Scan(&out.Version, &out.EffectiveFrom, &at,
		&out.AdoptedBy, &out.AdoptedText, &out.CoversAll, &covered, &out.Methods, &out.ApprovalKind, &approvers, &fallback, &threshold, &out.LodgingAlways, &acceptors,
		&out.SubstantiationDays, &out.ExcessDays, &reviewer, &out.ReimbursementCurrency, &out.ConversionMethod, &out.RuleProfile, &out.RetentionYears,
		&out.AttestationText, &out.AttestationVersion)
	if err != nil {
		return out, dbError(err)
	}
	out.AdoptedAt = &at
	out.SelfApprovalPermitted = out.ApprovalKind == "owner_self"
	out.ReceiptThresholdMinor = strconv.FormatInt(threshold, 10)
	names := func(ids []string) ([]PersonRef, error) {
		refs := []PersonRef{}
		for _, id := range ids {
			r := PersonRef{ID: id}
			if err := tx.QueryRow(ctx, "SELECT name FROM public.people WHERE tenant_id=$1 AND id=$2", tenant, id).Scan(&r.Name); err != nil {
				return nil, dbError(err)
			}
			refs = append(refs, r)
		}
		return refs, nil
	}
	if out.Covered, err = names(covered); err != nil {
		return out, err
	}
	if out.Approvers, err = names(approvers); err != nil {
		return out, err
	}
	if out.ExceptionAcceptors, err = names(acceptors); err != nil {
		return out, err
	}
	for id, into := range map[string]**PersonRef{fallback: &out.Fallback, reviewer: &out.Reviewer} {
		if id == "" {
			continue
		}
		refs, err := names([]string{id})
		if err != nil {
			return out, err
		}
		*into = &refs[0]
	}
	return out, nil
}

// AdoptReportPolicy records a new version of the business's expense
// policy, from the day given, as its owner adopts it with a recent
// sign-in. An agent never adopts one, nor changes who may approve.
func (a TenantAdmin) AdoptReportPolicy(ctx context.Context, book, actor, identity string, q PolicyAdoption) (ReportPolicy, error) {
	var out ReportPolicy
	q.AdoptedText = strings.TrimSpace(q.AdoptedText)
	ids := append(append(append([]string{}, q.CoveredPersonIDs...), q.ApproverPersonIDs...), q.ExceptionAcceptorPersonIDs...)
	for _, id := range append(ids, q.FallbackPersonID, q.ReviewerPersonID) {
		if id != "" && !ValidID(id) {
			return out, ErrInvalid
		}
	}
	if !ValidDate(q.EffectiveFrom) || q.AdoptedText == "" || len(q.AdoptedText) > 20000 || identity != "" && !ValidID(identity) ||
		q.ApprovalKind != "independent" && q.ApprovalKind != "owner_self" || q.ApprovalKind == "independent" && len(q.ApproverPersonIDs) == 0 ||
		q.ReceiptThresholdMinor != "" && !validNonNegative(q.ReceiptThresholdMinor) || q.SubstantiationDays < 0 || q.ExcessDays < 0 || q.RetentionYears < 0 ||
		len(q.AttestationText) > 2000 {
		return out, ErrInvalid
	}
	for _, m := range q.Methods {
		if m != "actual_expense" && m != "cash" {
			return out, ErrInvalid
		}
	}
	err := a.withExpenseWriter(ctx, book, actor, true, func(tx pgx.Tx, s Scope) error {
		prior, err := readPolicy(ctx, tx, s.TenantID, s.BookID, "")
		if err != nil {
			return err
		}
		var currency string
		if err := tx.QueryRow(ctx, "SELECT currency FROM public.books WHERE tenant_id=$1 AND id=$2", s.TenantID, s.BookID).Scan(&currency); err != nil {
			return dbError(err)
		}
		pick := func(given, fallback string) string {
			if given != "" {
				return given
			}
			return fallback
		}
		number := func(given, fallback int) int {
			if given > 0 {
				return given
			}
			return fallback
		}
		methods := q.Methods
		if len(methods) == 0 {
			methods = []string{"actual_expense", "cash"}
		}
		attestation := pick(strings.TrimSpace(q.AttestationText), prior.AttestationText)
		attestationVersion := prior.AttestationVersion
		if attestation != prior.AttestationText {
			attestationVersion++
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.report_policies(tenant_id,business_book_id,version,effective_from,adopted_by,adopted_identity_id,adopted_text,covers_all,covered_person_ids,methods,
 approval_kind,approver_person_ids,fallback_person_id,receipt_threshold_minor,lodging_always,exception_acceptor_person_ids,substantiation_days,excess_days,reviewer_person_id,
 reimbursement_currency,conversion_method,rule_profile,retention_years,attestation_text,attestation_version)
 SELECT $1,$2,coalesce(max(version),0)+1,$3::date,$4,nullif($5,'')::uuid,$6,$7,$8::uuid[],$9,$10,$11::uuid[],nullif($12,'')::uuid,$13,$14,$15::uuid[],$16,$17,nullif($18,'')::uuid,
 $19,$20,$21,$22,$23,$24 FROM public.report_policies WHERE tenant_id=$1 AND business_book_id=$2`,
			s.TenantID, s.BookID, q.EffectiveFrom, actor, identity, q.AdoptedText, q.CoversAll == nil || *q.CoversAll, nonNil(q.CoveredPersonIDs), methods,
			q.ApprovalKind, nonNil(q.ApproverPersonIDs), q.FallbackPersonID, pick(q.ReceiptThresholdMinor, strconv.Itoa(profileReceiptMinor)), q.LodgingAlways == nil || *q.LodgingAlways,
			nonNil(q.ExceptionAcceptorPersonIDs), number(q.SubstantiationDays, 60), number(q.ExcessDays, 120), q.ReviewerPersonID,
			pick(q.ReimbursementCurrency, currency), pick(q.ConversionMethod, "same_currency"), pick(q.RuleProfile, "us_actual_expense_v1"), number(q.RetentionYears, 7),
			attestation, attestationVersion)
		if err != nil {
			return err
		}
		var version int
		if err := tx.QueryRow(ctx, "SELECT max(version) FROM public.report_policies WHERE tenant_id=$1 AND business_book_id=$2", s.TenantID, s.BookID).Scan(&version); err != nil {
			return err
		}
		out, err = readPolicyVersion(ctx, tx, s.TenantID, s.BookID, version)
		return err
	})
	return out, err
}

func nonNil(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func validNonNegative(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n >= 0 && strconv.FormatInt(n, 10) == s
}

// reportLink is the owed direction a report belongs to: the business that
// pays back, the person's Book, and the accounts each keeps it in.
type reportLink struct {
	ID, Business, Personal, Payable, Receivable string
}

func readReportLink(ctx context.Context, tx pgx.Tx, tenant, link string) (reportLink, error) {
	var out reportLink
	err := tx.QueryRow(ctx, `SELECT x.link_id::text,x.beneficiary_book_id::text,x.payer_book_id::text,x.beneficiary_account_id::text,x.payer_account_id::text
 FROM public.book_link_terms x WHERE x.tenant_id=$1 AND x.link_id=$2 AND x.treatment='owed'
   AND public.book_profile($1,x.beneficiary_book_id)='business' AND public.book_profile($1,x.payer_book_id)='personal'`, tenant, link).
		Scan(&out.ID, &out.Business, &out.Personal, &out.Payable, &out.Receivable)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &RefusedError{Reason: "Expense reports are for a business paying back a person: link the business's Book to the person's, with costs owed back."}
	}
	return out, dbError(err)
}

// LinkBusiness is the business Book of a link that can have reports.
func (a TenantAdmin) LinkBusiness(ctx context.Context, link string) (string, error) {
	if !ValidID(link) {
		return "", ErrInvalid
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", dbError(err)
	}
	defer tx.Rollback(context.Background())
	l, err := readReportLink(ctx, tx, a.tenantID, link)
	return l.Business, err
}

// CreateReport starts a draft for a person, with a new reference or one
// already sent.
func (a TenantAdmin) CreateReport(ctx context.Context, actor string, q NewReport) (string, error) {
	if !ValidID(q.LinkID) || !ValidID(q.PersonID) {
		return "", ErrInvalid
	}
	reference, source := "", "generated"
	if q.Reference != "" {
		var ok bool
		if reference, ok = NormalizeReportReference(q.Reference); !ok {
			return "", &RefusedError{Reason: "That isn't a report reference. It looks like ER-7Q2M-K4HD-9W1C."}
		}
		source = "imported"
	}
	business, err := a.LinkBusiness(ctx, q.LinkID)
	if err != nil {
		return "", err
	}
	err = a.withExpenseWriter(ctx, business, actor, false, func(tx pgx.Tx, s Scope) error {
		l, err := readReportLink(ctx, tx, s.TenantID, q.LinkID)
		if err != nil {
			return err
		}
		var personal string
		if err := tx.QueryRow(ctx, "SELECT coalesce(personal_book_id::text,'') FROM public.people WHERE tenant_id=$1 AND business_book_id=$2 AND id=$3",
			s.TenantID, l.Business, q.PersonID).Scan(&personal); err != nil {
			return &RefusedError{Reason: "Add this person to the business first."}
		}
		if personal != l.Personal {
			return &RefusedError{Reason: "This person's record names another Book than this link's."}
		}
		if source == "imported" {
			var taken bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.expense_reports WHERE tenant_id=$1 AND reference=$2)", s.TenantID, reference).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return &RefusedError{Reason: reference + " is already used by another report."}
			}
		}
		for attempt := 0; ; attempt++ {
			if source == "generated" {
				if reference, err = NewReportReference(); err != nil {
					return err
				}
			}
			id := ID()
			_, err = tx.Exec(ctx, `INSERT INTO public.expense_reports(tenant_id,id,business_book_id,link_id,person_id,reference,reference_source,currency,created_by,prepared_by_agent)
 SELECT $1,$2,$3,$4,$5,$6,$7,b.currency,$8,$9 FROM public.books b WHERE b.tenant_id=$1 AND b.id=$3
 AND NOT EXISTS (SELECT 1 FROM public.expense_reports x WHERE x.tenant_id=$1 AND x.reference=$6)
 AND NOT EXISTS (SELECT 1 FROM public.retired_report_references x WHERE x.tenant_id=$1 AND x.reference=$6)
 ON CONFLICT (tenant_id,business_book_id,reference) DO NOTHING`, s.TenantID, id, l.Business, l.ID, q.PersonID, reference, source, actor, q.PreparedByAgent)
			if err != nil {
				return err
			}
			var created bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.expense_reports WHERE tenant_id=$1 AND id=$2)", s.TenantID, id).Scan(&created); err != nil {
				return err
			}
			if created {
				if _, err := tx.Exec(ctx, "INSERT INTO public.report_revisions(tenant_id,id,report_id,business_book_id,revision) VALUES($1,$2,$3,$4,1)", s.TenantID, ID(), id, l.Business); err != nil {
					return err
				}
				return reportEvent(ctx, tx, s.TenantID, id, l.Business, 1, "created", actor, "", "")
			}
			if source == "imported" {
				return &RefusedError{Reason: reference + " is already used by another report."}
			}
			if attempt > 5 {
				return errors.New("no free reference")
			}
		}
	})
	return reference, err
}

func reportEvent(ctx context.Context, tx pgx.Tx, tenant, report, book string, revision int, kind, actor, identity, note string) error {
	_, err := tx.Exec(ctx, `INSERT INTO public.report_events(tenant_id,id,report_id,business_book_id,revision,kind,actor_id,identity_id,note)
 VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,'')::uuid,nullif($9,''))`, tenant, ID(), report, book, revision, kind, actor, identity, note)
	return err
}

// reportRow is a report's own row.
type reportRow struct {
	ID, Reference, Business, LinkID, PersonID, Workflow, Currency, RevisionID string
	Revision                                                                  int
	Reconciled                                                                bool
}

func readReport(ctx context.Context, tx pgx.Tx, tenant, book, reference string, lock bool) (reportRow, error) {
	var r reportRow
	ref, ok := NormalizeReportReference(reference)
	if !ok {
		return r, ErrNotFound
	}
	q := `SELECT r.id::text,r.reference,r.business_book_id::text,r.link_id::text,r.person_id::text,r.workflow,r.currency,r.revision,coalesce(v.id::text,''),r.reconciled_after_restore
 FROM public.expense_reports r LEFT JOIN public.report_revisions v ON v.tenant_id=r.tenant_id AND v.report_id=r.id AND v.revision=r.revision
 WHERE r.tenant_id=$1 AND r.business_book_id=$2 AND r.reference=$3`
	if lock {
		q += " FOR UPDATE OF r"
	}
	err := tx.QueryRow(ctx, q, tenant, book, ref).Scan(&r.ID, &r.Reference, &r.Business, &r.LinkID, &r.PersonID, &r.Workflow, &r.Currency, &r.Revision, &r.RevisionID, &r.Reconciled)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, dbError(err)
}

// ReportBook is the business Book a reference belongs to, among the
// owner's Books.
func (a TenantAdmin) ReportBook(ctx context.Context, reference string) (string, error) {
	ref, ok := NormalizeReportReference(reference)
	if !ok {
		return "", ErrNotFound
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", dbError(err)
	}
	defer tx.Rollback(context.Background())
	var book string
	err = tx.QueryRow(ctx, "SELECT business_book_id::text FROM public.expense_reports WHERE tenant_id=$1 AND reference=$2", a.tenantID, ref).Scan(&book)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return book, dbError(err)
}

func editable(r reportRow) error {
	if r.Workflow != "draft" && r.Workflow != "returned" {
		return &RefusedError{Reason: "Only a draft or a returned report changes; correct an approved one instead."}
	}
	return nil
}

// AddReportClaim adds a cost to a draft: one already recorded as owed, by
// its pair, or a cash cost. A cost recorded as owed gets no second expense
// or payable; a cash cost is owed only once the business accepts it.
func (a TenantAdmin) AddReportClaim(ctx context.Context, book, actor, reference string, q ClaimInput) (string, error) {
	var claim string
	if err := q.normalize(true); err != nil {
		return "", err
	}
	err := a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if err := editable(r); err != nil {
			return err
		}
		l, err := readReportLink(ctx, tx, s.TenantID, r.LinkID)
		if err != nil {
			return err
		}
		if q.PairKey != "" {
			claim, err = costClaim(ctx, tx, s, r, l, actor, q)
		} else {
			claim, err = cashClaim(ctx, tx, s, r, l, actor, q)
		}
		if err != nil {
			return err
		}
		portion := ""
		if err := tx.QueryRow(ctx, "SELECT requested_minor::text FROM public.claims WHERE tenant_id=$1 AND id=$2", s.TenantID, claim).Scan(&portion); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.report_lines(tenant_id,report_id,business_book_id,revision,claim_id,position,portion_minor)
 SELECT $1,$2,$3,$4,$5,coalesce(max(position),0)+1,$6 FROM public.report_lines WHERE tenant_id=$1 AND report_id=$2 AND revision=$4`,
			s.TenantID, r.ID, r.Business, r.Revision, claim, portion)
		if err != nil || len(q.SupplierInvoice) == 0 {
			return err
		}
		// Checked once the claim is on this report, so its reference is
		// known.
		return updateClaim(ctx, tx, s, claim, ClaimInput{SupplierInvoice: q.SupplierInvoice})
	})
	return claim, err
}

// costClaim is the claim for a cost the person paid that the business
// already records as owed (or an opening balance, with its provenance).
// The cost keeps one claim across reports.
func costClaim(ctx context.Context, tx pgx.Tx, s Scope, r reportRow, l reportLink, actor string, q ClaimInput) (string, error) {
	var kind, payer, beneficiary, entry, date, description string
	var amount, open int64
	err := tx.QueryRow(ctx, `SELECT o.kind,o.payer_book_id::text,o.beneficiary_book_id::text,o.entry_id::text,o.accounting_date::text,o.description,o.amount,o.open
 FROM public.open_costs($1,$2,$3,$4,NULL) o WHERE o.pair_key=$5`, s.TenantID, l.Personal, l.Business, l.Payable, q.PairKey).
		Scan(&kind, &payer, &beneficiary, &entry, &date, &description, &amount, &open)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", &RefusedError{Reason: "That cost isn't one this business owes this person."}
	}
	if err != nil {
		return "", err
	}
	_ = payer
	obligationBook := beneficiary
	claimKind := "cost"
	if kind == "opening" {
		claimKind = "opening"
		if strings.TrimSpace(q.Provenance) == "" {
			q.Provenance = "Owed before this Vult's records began, as entered on " + date + "."
		}
	}
	var claim string
	err = tx.QueryRow(ctx, "SELECT id::text FROM public.claims WHERE tenant_id=$1 AND obligation_book_id=$2 AND obligation_entry_id=$3", s.TenantID, obligationBook, entry).Scan(&claim)
	if err == nil {
		return claim, updateClaim(ctx, tx, s, claim, q)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	portion := amount
	if q.PortionMinor != "" {
		if portion, err = strconv.ParseInt(q.PortionMinor, 10, 64); err != nil || portion <= 0 || portion > amount {
			return "", &RefusedError{Reason: "The business part is at most the cost."}
		}
	}
	// What earlier payments already repaid of the cost isn't asked for
	// again: the claim requests what's still open of its part.
	prior := amount - open
	if open <= 0 {
		return "", &RefusedError{Reason: "This cost is already repaid."}
	}
	requested := min(portion, open)
	vendor := strings.TrimSpace(q.Vendor)
	if vendor == "" {
		vendor = strings.TrimSpace(description)
	}
	if vendor == "" {
		vendor = "Cost on " + date
	}
	if len([]rune(vendor)) > 200 {
		vendor = string([]rune(vendor)[:200])
	}
	lodging := q.Lodging != nil && *q.Lodging
	claim = ID()
	var provenance any
	if claimKind == "opening" {
		provenance = strings.TrimSpace(q.Provenance)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.claims(tenant_id,id,business_book_id,person_id,kind,obligation_book_id,obligation_entry_id,incurred_on,paid_on,
 vendor,original_amount_minor,original_currency,business_portion_minor,requested_minor,purpose,lodging,proposed_account_id,missing_explanation,provenance,recognized_at,recognized_by,created_by,
 prior_reimbursed_minor)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::date,$8::date,$9,$10,$11,$12,$19,$13,$14,nullif($15,'')::uuid,nullif($16,''),$17,clock_timestamp(),$18,$18,$20)`,
		s.TenantID, claim, l.Business, r.PersonID, claimKind, obligationBook, entry, date, vendor, amount, r.Currency, portion,
		strings.TrimSpace(q.Purpose), lodging, q.ProposedAccountID, strings.TrimSpace(q.MissingExplanation), provenance, actor, requested, prior); err != nil {
		return "", err
	}
	return claim, nil
}

// cashClaim is a cash cost the person paid with no bank line: a draft
// claim, owed only once the business accepts it.
func cashClaim(ctx context.Context, tx pgx.Tx, s Scope, r reportRow, l reportLink, actor string, q ClaimInput) (string, error) {
	if !ValidDate(q.Date) || strings.TrimSpace(q.Vendor) == "" || len([]rune(q.Vendor)) > 200 || !validAmount(q.AmountMinor) || !ValidID(q.PaidFromAccountID) ||
		q.ProposedAccountID != "" && !ValidID(q.ProposedAccountID) || len(q.Purpose) > 2000 {
		return "", ErrInvalid
	}
	var fromBook string
	if err := tx.QueryRow(ctx, "SELECT book_id::text FROM public.accounts WHERE tenant_id=$1 AND id=$2", s.TenantID, q.PaidFromAccountID).Scan(&fromBook); err != nil || fromBook != l.Personal {
		return "", &RefusedError{Reason: "Choose the account in the person's Book they paid from."}
	}
	portion := q.AmountMinor
	if q.PortionMinor != "" {
		portion = q.PortionMinor
	}
	claim := ID()
	_, err := tx.Exec(ctx, `INSERT INTO public.claims(tenant_id,id,business_book_id,person_id,kind,incurred_on,paid_on,vendor,original_amount_minor,original_currency,
 business_portion_minor,requested_minor,purpose,lodging,proposed_account_id,paid_from_account_id,missing_explanation,created_by)
 VALUES($1,$2,$3,$4,'cash',$5::date,$5::date,$6,$7::bigint,$8,$9::bigint,$9::bigint,$10,$11,nullif($12,'')::uuid,$13,nullif($14,''),$15)`,
		s.TenantID, claim, l.Business, r.PersonID, q.Date, strings.TrimSpace(q.Vendor), q.AmountMinor, r.Currency, portion,
		strings.TrimSpace(q.Purpose), q.Lodging != nil && *q.Lodging, q.ProposedAccountID, q.PaidFromAccountID, strings.TrimSpace(q.MissingExplanation), actor)
	if err != nil {
		var p *pgconn.PgError
		if errors.As(err, &p) && p.Code == "23514" {
			return "", &RefusedError{Reason: "The business part is at most what was paid."}
		}
		return "", err
	}
	return claim, nil
}

// supplierInvoice checks the supplier's invoice facts, which are kept as
// given: an object, of bounded size, and never holding the reference of a
// report the claim is on. "null" clears them.
func supplierInvoice(ctx context.Context, tx pgx.Tx, s Scope, claim string, given json.RawMessage) (any, error) {
	text := strings.TrimSpace(string(given))
	if text == "null" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if len(text) > 16000 || json.Unmarshal([]byte(text), &object) != nil || object == nil {
		return nil, &RefusedError{Reason: "The supplier's invoice is given as its facts: number, supplier, customer, dates and tax."}
	}
	var held bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.report_lines l JOIN public.expense_reports r ON r.tenant_id=l.tenant_id AND r.id=l.report_id
 WHERE l.tenant_id=$1 AND l.claim_id=$2 AND (strpos(upper($3),r.reference)>0 OR strpos(upper($3),replace(r.reference,'-',''))>0))`, s.TenantID, claim, text).Scan(&held); err != nil {
		return nil, err
	}
	if held {
		return nil, &RefusedError{Reason: "The supplier's invoice is the supplier's: the report's reference never goes in it."}
	}
	return text, nil
}

func updateClaim(ctx context.Context, tx pgx.Tx, s Scope, claim string, q ClaimInput) error {
	set := []string{}
	args := []any{s.TenantID, claim}
	add := func(column string, v any) {
		args = append(args, v)
		set = append(set, column+"=$"+strconv.Itoa(len(args)))
	}
	if q.Purpose != "" {
		add("purpose", strings.TrimSpace(q.Purpose))
	}
	if q.Lodging != nil {
		add("lodging", *q.Lodging)
	}
	if q.ProposedAccountID != "" {
		if !ValidID(q.ProposedAccountID) {
			return ErrInvalid
		}
		add("proposed_account_id", q.ProposedAccountID)
	}
	if q.MissingExplanation != "" {
		add("missing_explanation", strings.TrimSpace(q.MissingExplanation))
	}
	if q.PortionMinor != "" {
		if !validAmount(q.PortionMinor) {
			return ErrInvalid
		}
		args = append(args, q.PortionMinor)
		set = append(set, "business_portion_minor=$"+strconv.Itoa(len(args))+"::bigint", "requested_minor=$"+strconv.Itoa(len(args))+"::bigint")
	}
	if len(q.SupplierInvoice) > 0 {
		invoice, err := supplierInvoice(ctx, tx, s, claim, q.SupplierInvoice)
		if err != nil {
			return err
		}
		args = append(args, invoice)
		set = append(set, "supplier_invoice=$"+strconv.Itoa(len(args)))
	}
	if len(set) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, "UPDATE public.claims SET revision=revision+1,"+strings.Join(set, ",")+" WHERE tenant_id=$1 AND id=$2", args...)
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23514" {
		return &RefusedError{Reason: "The business part is at most what was paid."}
	}
	return err
}

// EditReportClaim changes a claim on a draft.
func (a TenantAdmin) EditReportClaim(ctx context.Context, book, actor, reference, claim string, q ClaimInput) error {
	if !ValidID(claim) {
		return ErrInvalid
	}
	if err := q.normalize(false); err != nil {
		return err
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if err := editable(r); err != nil {
			return err
		}
		var on bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.report_lines WHERE tenant_id=$1 AND report_id=$2 AND revision=$3 AND claim_id=$4)",
			s.TenantID, r.ID, r.Revision, claim).Scan(&on); err != nil || !on {
			return ErrNotFound
		}
		if err := updateClaim(ctx, tx, s, claim, q); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE public.report_lines l SET portion_minor=c.requested_minor FROM public.claims c
 WHERE l.tenant_id=$1 AND l.report_id=$2 AND l.revision=$3 AND l.claim_id=$4 AND c.tenant_id=l.tenant_id AND c.id=l.claim_id`, s.TenantID, r.ID, r.Revision, claim)
		return err
	})
}

// RemoveReportClaim takes a claim off a draft. A cash claim no report uses
// is removed with it.
func (a TenantAdmin) RemoveReportClaim(ctx context.Context, book, actor, reference, claim string) error {
	if !ValidID(claim) {
		return ErrInvalid
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if err := editable(r); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM public.report_lines WHERE tenant_id=$1 AND report_id=$2 AND revision=$3 AND claim_id=$4", s.TenantID, r.ID, r.Revision, claim)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `DELETE FROM public.claims c WHERE c.tenant_id=$1 AND c.id=$2 AND c.kind='cash' AND c.recognized_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM public.report_lines l WHERE l.tenant_id=c.tenant_id AND l.claim_id=c.id)
 AND NOT EXISTS (SELECT 1 FROM public.claim_evidence v WHERE v.tenant_id=c.tenant_id AND v.claim_id=c.id)`, s.TenantID, claim)
		return err
	})
}

// EvidenceInput says what an attached document is, and for a redacted
// or focused copy, what it came from and how.
type EvidenceInput struct {
	Kind                   string `json:"kind,omitempty"`
	RedactedFromDocumentID string `json:"redacted_from_document_id,omitempty"`
	RedactionProvenance    string `json:"redaction_provenance,omitempty"`
}

var evidenceKinds = map[string]bool{"receipt": true, "invoice": true, "payment_proof_extract": true, "calculation": true}

// AddClaimEvidence attaches a receipt stored in the business's Book to a
// claim. Evidence added after submission is a supplement, with its own
// date; the claim's evidence date is the first receipt's. It's kept until
// the policy's retention years have passed.
func (a TenantAdmin) AddClaimEvidence(ctx context.Context, book, actor, claim, document string, e EvidenceInput) error {
	if e.Kind == "" {
		e.Kind = "receipt"
	}
	e.RedactionProvenance = strings.TrimSpace(e.RedactionProvenance)
	if !ValidID(claim) || !ValidID(document) || !evidenceKinds[e.Kind] || e.RedactedFromDocumentID != "" && !ValidID(e.RedactedFromDocumentID) ||
		(e.RedactedFromDocumentID == "") != (e.RedactionProvenance == "") || len(e.RedactionProvenance) > 1000 {
		return ErrInvalid
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		var sha string
		if err := tx.QueryRow(ctx, "SELECT sha256 FROM public.documents WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, document).Scan(&sha); err != nil {
			return &RefusedError{Reason: "Add the receipt to the business's Documents first."}
		}
		var submitted bool
		var report, reportBook string
		var revision int
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.report_lines l JOIN public.report_revisions v ON v.tenant_id=l.tenant_id AND v.report_id=l.report_id AND v.revision=l.revision
   WHERE l.tenant_id=$1 AND l.claim_id=$2 AND v.submitted_at IS NOT NULL),
 coalesce((SELECT r.id::text FROM public.report_lines l JOIN public.expense_reports r ON r.tenant_id=l.tenant_id AND r.id=l.report_id AND r.revision=l.revision
   WHERE l.tenant_id=$1 AND l.claim_id=$2 ORDER BY r.created_at DESC LIMIT 1),''),
 coalesce((SELECT r.revision FROM public.report_lines l JOIN public.expense_reports r ON r.tenant_id=l.tenant_id AND r.id=l.report_id AND r.revision=l.revision
   WHERE l.tenant_id=$1 AND l.claim_id=$2 ORDER BY r.created_at DESC LIMIT 1),0)
 FROM public.claims c WHERE c.tenant_id=$1 AND c.business_book_id=$3 AND c.id=$2`, s.TenantID, claim, s.BookID).Scan(&submitted, &report, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		reportBook = s.BookID
		policy, err := readPolicy(ctx, tx, s.TenantID, s.BookID, "")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.claim_evidence(tenant_id,id,claim_id,business_book_id,document_id,sha256,supplement,added_by,kind,redacted_from_document_id,redaction_provenance,retention_until)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,nullif($10,'')::uuid,nullif($11,''),(current_date + make_interval(years => $12))::date)
 ON CONFLICT (tenant_id,claim_id,document_id) DO NOTHING`, s.TenantID, ID(), claim, s.BookID, document, sha, submitted, actor, e.Kind, e.RedactedFromDocumentID, e.RedactionProvenance,
			policy.RetentionYears); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE public.claims SET evidence_received_on=coalesce(evidence_received_on,current_date) WHERE tenant_id=$1 AND id=$2", s.TenantID, claim); err != nil {
			return err
		}
		if submitted && report != "" {
			return reportEvent(ctx, tx, s.TenantID, report, reportBook, revision, "evidence_added", actor, "", "")
		}
		return nil
	})
}

// snapshotLine is one line as a revision freezes it.
type snapshotLine struct {
	ClaimID            string   `json:"claim_id"`
	Kind               string   `json:"kind"`
	IncurredOn         string   `json:"incurred_on"`
	Vendor             string   `json:"vendor"`
	Purpose            string   `json:"purpose"`
	PortionMinor       string   `json:"portion_minor"`
	OriginalMinor      string   `json:"original_amount_minor"`
	Lodging            bool     `json:"lodging"`
	Evidence           []string `json:"evidence"`
	MissingExplanation string   `json:"missing_explanation,omitempty"`
	ProposedAccountID  string   `json:"proposed_account_id,omitempty"`
	Provenance         string   `json:"provenance,omitempty"`
	RuleProfile        string   `json:"rule_profile"`
	SupplierInvoice    *string  `json:"supplier_invoice,omitempty"`
}

type reportSnapshot struct {
	Reference string         `json:"reference"`
	Revision  int            `json:"revision"`
	Currency  string         `json:"currency"`
	Person    Person         `json:"person"`
	Lines     []snapshotLine `json:"lines"`
}

// snapshot is the current revision as it would be frozen, and its hash.
func snapshot(ctx context.Context, tx pgx.Tx, tenant string, r reportRow) (json.RawMessage, string, error) {
	snap := reportSnapshot{Reference: r.Reference, Revision: r.Revision, Currency: r.Currency, Lines: []snapshotLine{}}
	if err := scanPerson(tx.QueryRow(ctx, "SELECT "+personColumns+" FROM public.people WHERE tenant_id=$1 AND id=$2", tenant, r.PersonID), &snap.Person); err != nil {
		return nil, "", dbError(err)
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.kind,c.incurred_on::text,c.vendor,c.purpose,l.portion_minor::text,c.original_amount_minor::text,c.lodging,
 coalesce((SELECT array_agg(v.sha256 ORDER BY v.sha256) FROM public.claim_evidence v WHERE v.tenant_id=c.tenant_id AND v.claim_id=c.id AND v.removed_at IS NULL),'{}'),
 coalesce(c.missing_explanation,''),coalesce(c.proposed_account_id::text,''),coalesce(c.provenance,''),c.rule_profile,c.supplier_invoice::text
 FROM public.report_lines l JOIN public.claims c ON c.tenant_id=l.tenant_id AND c.id=l.claim_id
 WHERE l.tenant_id=$1 AND l.report_id=$2 AND l.revision=$3 ORDER BY l.position`, tenant, r.ID, r.Revision)
	if err != nil {
		return nil, "", dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var x snapshotLine
		if err := rows.Scan(&x.ClaimID, &x.Kind, &x.IncurredOn, &x.Vendor, &x.Purpose, &x.PortionMinor, &x.OriginalMinor, &x.Lodging, &x.Evidence,
			&x.MissingExplanation, &x.ProposedAccountID, &x.Provenance, &x.RuleProfile, &x.SupplierInvoice); err != nil {
			return nil, "", dbError(err)
		}
		snap.Lines = append(snap.Lines, x)
	}
	if err := rows.Err(); err != nil {
		return nil, "", dbError(err)
	}
	data := raw(snap)
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

var noteAudiences = map[string]bool{"personal": true, "shared": true, "company": true}

// AddReportNote adds a note for an audience. A personal note is written in
// the person's own Book, never the business's.
func (a TenantAdmin) AddReportNote(ctx context.Context, book, actor, reference, audience, text string) (ReportNote, error) {
	var out ReportNote
	text = strings.TrimSpace(text)
	if !noteAudiences[audience] || text == "" || len(text) > 4000 {
		return out, ErrInvalid
	}
	err := a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, false)
		if err != nil {
			return err
		}
		target := r.Business
		if audience == "personal" {
			if err := tx.QueryRow(ctx, "SELECT coalesce(personal_book_id::text,'') FROM public.people WHERE tenant_id=$1 AND id=$2", s.TenantID, r.PersonID).Scan(&target); err != nil {
				return err
			}
			if target == "" {
				return &RefusedError{Reason: "A personal note is kept in the person's Book, and this person has none here."}
			}
			if err := setScope(ctx, tx, Scope{TenantID: s.TenantID, BookID: target, ActorID: actor}); err != nil {
				return err
			}
		}
		out = ReportNote{ID: ID(), Audience: audience, Text: text, AuthorID: actor}
		return tx.QueryRow(ctx, "INSERT INTO public.report_notes(tenant_id,id,book_id,report_id,audience,text,author_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING at",
			s.TenantID, out.ID, target, r.ID, audience, text, actor).Scan(&out.At)
	})
	return out, err
}

// PersonalReportNotes reads the person's own notes on a report, from their
// Book.
func (a TenantAdmin) PersonalReportNotes(ctx context.Context, book, reference string) ([]ReportNote, error) {
	out := []ReportNote{}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, false)
		if err != nil {
			return err
		}
		var personal string
		if err := tx.QueryRow(ctx, "SELECT coalesce(personal_book_id::text,'') FROM public.people WHERE tenant_id=$1 AND id=$2", s.TenantID, r.PersonID).Scan(&personal); err != nil || personal == "" {
			return dbError(err)
		}
		out, err = readNotes(ctx, tx, s.TenantID, personal, r.ID)
		return err
	})
	return out, err
}
