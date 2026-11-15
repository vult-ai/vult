package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReportLine is one claim on a report's current revision.
type ReportLine struct {
	ClaimID            string     `json:"claim_id"`
	Position           int        `json:"position"`
	Kind               string     `json:"kind"`
	Date               string     `json:"date"`
	Vendor             string     `json:"vendor"`
	Purpose            string     `json:"purpose"`
	AmountMinor        string     `json:"amount_minor"`
	ApprovedMinor      *string    `json:"approved_minor"`
	OriginalMinor      string     `json:"original_amount_minor"`
	PaidMinor          string     `json:"paid_minor"`
	Lodging            bool       `json:"lodging"`
	HasReceipt         bool       `json:"has_receipt"`
	HasPaymentProof    bool       `json:"has_payment_proof"`
	ReceiptRequired    bool       `json:"receipt_required"`
	MissingExplanation string     `json:"missing_explanation,omitempty"`
	EvidenceReceivedOn string     `json:"evidence_received_on,omitempty"`
	Evidence           []Evidence `json:"evidence"`
	ProposedAccountID  string     `json:"proposed_account_id,omitempty"`
	RecordedAccountID  string     `json:"recorded_account_id,omitempty"`
	// AccountName is the recorded account's name, or the proposed one's
	// until it's recorded.
	AccountName         string `json:"account_name,omitempty"`
	ProposedAccountName string `json:"proposed_account_name,omitempty"`
	RecordedAccountName string `json:"recorded_account_name,omitempty"`
	AccountDiffers      bool   `json:"account_differs"`
	Recognized          bool   `json:"recognized"`
	PairKey             string `json:"pair_key,omitempty"`
	// PolicyVersion is the expense policy in force on the day of the cost;
	// 0 is none adopted.
	PolicyVersion int `json:"expense_policy_version"`
	// PriorReimbursedMinor is what earlier payments repaid of the cost
	// before it was claimed; the claim asks only for the rest.
	PriorReimbursedMinor string `json:"prior_reimbursed_minor"`
	// RuleProfile is the profile the policy applies to this cost, and
	// Assessment what it found at submission.
	RuleProfile string          `json:"rule_profile"`
	Assessment  json.RawMessage `json:"assessment,omitempty"`
	// SupplierInvoice is the supplier's invoice facts, as given.
	SupplierInvoice json.RawMessage `json:"supplier_invoice,omitempty"`
	// ReceiptRule says why a receipt is required: the profile's own check,
	// or the company's lower threshold.
	ReceiptRule        string `json:"receipt_rule,omitempty"`
	original           int64
	substantiationDays int
	// Capacity is the person's relationship to the business on the day of
	// the cost.
	Capacity   string `json:"capacity"`
	Provenance string `json:"provenance,omitempty"`
}

type Evidence struct {
	DocumentID string    `json:"document_id"`
	SHA256     string    `json:"sha256"`
	Kind       string    `json:"kind"`
	Supplement bool      `json:"supplement"`
	AddedAt    time.Time `json:"added_at"`
	// Derived says this is a redacted or focused copy, never the original:
	// what it came from and how.
	Derived                bool   `json:"derived"`
	RedactedFromDocumentID string `json:"redacted_from_document_id,omitempty"`
	RedactionProvenance    string `json:"redaction_provenance,omitempty"`
	RetentionUntil         string `json:"retention_until"`
}

// ReportNote is a note on a report for the audience it names.
type ReportNote struct {
	ID       string    `json:"id"`
	Audience string    `json:"audience"`
	Text     string    `json:"text"`
	AuthorID string    `json:"author_id"`
	At       time.Time `json:"at"`
}

// Warning is a reminder computed in one place: usually expected, never a
// verdict, and never blocking.
type Warning struct {
	Kind        string `json:"kind"`
	ClaimID     string `json:"claim_id,omitempty"`
	Days        int    `json:"days"`
	AmountMinor string `json:"amount_minor,omitempty"`
	Since       string `json:"since"`
}

type RevisionView struct {
	ID            string     `json:"id"`
	Revision      int        `json:"revision"`
	SubmittedAt   *time.Time `json:"submitted_at"`
	SnapshotSHA   string     `json:"snapshot_sha256,omitempty"`
	ApprovedAt    *time.Time `json:"approved_at"`
	SelfApproved  bool       `json:"self_approved"`
	PDFDocumentID string     `json:"pdf_document_id,omitempty"`
	Reason        string     `json:"reason,omitempty"`
}

type PaymentView struct {
	AllocationID string     `json:"allocation_id"`
	ClaimID      string     `json:"claim_id"`
	EntryID      string     `json:"entry_id"`
	Date         string     `json:"date"`
	AmountMinor  string     `json:"amount_minor"`
	Unapproved   bool       `json:"paid_before_approval"`
	RemovedAt    *time.Time `json:"removed_at,omitempty"`
}

type RecoverableView struct {
	ID          string `json:"id"`
	Capacity    string `json:"capacity"`
	AmountMinor string `json:"amount_minor"`
	Origin      string `json:"origin"`
	Since       string `json:"since"`
	// RecoveryStatus is how it's being recovered, and TaxStatus how the
	// accountant treats it.
	RecoveryStatus string `json:"recovery_status"`
	TaxStatus      string `json:"tax_status"`
	// Resolution is the recovery status once it's no longer outstanding.
	Resolution    *string             `json:"resolution"`
	TreatmentNote string              `json:"treatment_note,omitempty"`
	RecordedBy    string              `json:"recorded_by,omitempty"`
	RecordedAt    *time.Time          `json:"recorded_at,omitempty"`
	Events        []RecoverableLog    `json:"events"`
	Reviews       []RecoverableReview `json:"reviews"`
	// ReturnCandidates are entries where the person paid the business
	// back, for an open recoverable: by amount first.
	ReturnCandidates []ReturnCandidate `json:"return_candidates,omitempty"`
}

type RecoverableLog struct {
	Track      string    `json:"track"`
	Status     string    `json:"status"`
	Resolution string    `json:"resolution"`
	Note       string    `json:"note,omitempty"`
	RecordedBy string    `json:"recorded_by"`
	RecordedAt time.Time `json:"recorded_at"`
}

// ReturnCandidate is an entry that reduced what the person owes the
// business back, not yet named as any recoverable's return.
type ReturnCandidate struct {
	EntryID     string `json:"entry_id"`
	Date        string `json:"date"`
	AmountMinor string `json:"amount_minor"`
	Description string `json:"description"`
	AmountFits  bool   `json:"amount_fits"`
}

// ResidualView is what a payment left over, and what its owner said it
// was.
type ResidualView struct {
	ID             string     `json:"id"`
	EntryID        string     `json:"entry_id"`
	AmountMinor    string     `json:"amount_minor"`
	Classification *string    `json:"classification"`
	Note           string     `json:"note,omitempty"`
	TargetReportID string     `json:"target_report_id,omitempty"`
	RecoverableID  string     `json:"recoverable_id,omitempty"`
	ClassifiedAt   *time.Time `json:"classified_at,omitempty"`
}

type StatementView struct {
	ID            string    `json:"id"`
	IssuedAt      time.Time `json:"issued_at"`
	PDFDocumentID string    `json:"pdf_document_id"`
}

// ReportDetail is everything about a report the screens and documents show.
type ReportDetail struct {
	ReportSummary
	BusinessBookID string            `json:"business_book_id"`
	BusinessName   string            `json:"business_name"`
	LinkID         string            `json:"link_id"`
	Person         Person            `json:"person"`
	Currency       string            `json:"currency"`
	Revision       int               `json:"revision"`
	RevisionID     string            `json:"revision_id"`
	Memo           string            `json:"memo"`
	SnapshotSHA256 string            `json:"snapshot_sha256"`
	Lines          []ReportLine      `json:"lines"`
	Revisions      []RevisionView    `json:"revisions"`
	Payments       []PaymentView     `json:"payments"`
	Recoverables   []RecoverableView `json:"recoverables"`
	Statements     []StatementView   `json:"statements"`
	Events         []ReportEvent     `json:"events"`
	// ReconciledAfterRestore is false for a report restored with money
	// still to pay, until its owner reconciles it.
	ReconciledAfterRestore bool `json:"reconciled_after_restore"`
	// Residuals are what payments left over, awaiting or given a
	// classification.
	Residuals []ResidualView `json:"residuals"`
	// Notes are the shared and company notes; personal notes stay in the
	// person's Book.
	Notes []ReportNote `json:"notes"`
	// Policy is the business's expense policy in force today.
	Policy        ReportPolicy `json:"policy"`
	Warnings      []Warning    `json:"warnings"`
	ReturnNote    string       `json:"return_note,omitempty"`
	ReferenceKind string       `json:"reference_source"`
}

// Reports lists the reports under a link, newest first.
func (a TenantAdmin) Reports(ctx context.Context, link string) ([]ReportSummary, error) {
	business, err := a.LinkBusiness(ctx, link)
	if err != nil {
		return nil, err
	}
	out := []ReportSummary{}
	err = a.withBookRead(ctx, business, func(tx pgx.Tx, s Scope, _ Book) error {
		rows, err := tx.Query(ctx, "SELECT reference FROM public.expense_reports WHERE tenant_id=$1 AND link_id=$2 ORDER BY created_at DESC,id", s.TenantID, link)
		if err != nil {
			return dbError(err)
		}
		var refs []string
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				rows.Close()
				return dbError(err)
			}
			refs = append(refs, r)
		}
		rows.Close()
		for _, ref := range refs {
			d, err := readDetail(ctx, tx, s, ref, false)
			if err != nil {
				return err
			}
			out = append(out, d.ReportSummary)
		}
		return nil
	})
	return out, err
}

// ExpenseReport reads one report in full.
func (a TenantAdmin) ExpenseReport(ctx context.Context, book, reference string) (ReportDetail, error) {
	var out ReportDetail
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readDetail(ctx, tx, s, reference, true)
		return err
	})
	return out, err
}

func readDetail(ctx context.Context, tx pgx.Tx, s Scope, reference string, full bool) (ReportDetail, error) {
	var d ReportDetail
	r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, false)
	if err != nil {
		return d, err
	}
	d.ID, d.Reference, d.Workflow, d.BusinessBookID, d.LinkID, d.Currency, d.Revision, d.RevisionID = r.ID, r.Reference, r.Workflow, r.Business, r.LinkID, r.Currency, r.Revision, r.RevisionID
	d.ReconciledAfterRestore = r.Reconciled
	if err := tx.QueryRow(ctx, `SELECT r.created_at,r.reference_source,b.name,p.id::text,p.business_book_id::text,p.name,p.capacity,coalesce(p.effective_from::text,''),coalesce(p.personal_book_id::text,''),p.revision
 FROM public.expense_reports r JOIN public.books b ON b.tenant_id=r.tenant_id AND b.id=r.business_book_id JOIN public.people p ON p.tenant_id=r.tenant_id AND p.id=r.person_id
 WHERE r.tenant_id=$1 AND r.id=$2`, s.TenantID, r.ID).Scan(&d.CreatedAt, &d.ReferenceKind, &d.BusinessName, &d.Person.ID, &d.Person.BookID, &d.Person.Name,
		&d.Person.Capacity, &d.Person.EffectiveFrom, &d.Person.PersonalBookID, &d.Person.Revision); err != nil {
		return d, dbError(err)
	}
	d.Memo = ReportMemo(r.Reference)
	policies := map[int]ReportPolicy{}
	policyOf := func(version *int) (ReportPolicy, error) {
		v := 0
		if version != nil {
			v = *version
		}
		if p, ok := policies[v]; ok {
			return p, nil
		}
		p := defaultPolicy()
		if v > 0 {
			var err error
			if p, err = readPolicyVersion(ctx, tx, s.TenantID, s.BookID, v); err != nil {
				return p, err
			}
		}
		policies[v] = p
		return p, nil
	}
	if d.Policy, err = readPolicy(ctx, tx, s.TenantID, s.BookID, ""); err != nil {
		return d, err
	}
	versions := map[string]*int{}
	// The lines of the current revision.
	rows, err := tx.Query(ctx, `SELECT c.id::text,l.position,c.kind,c.incurred_on::text,c.vendor,c.purpose,l.portion_minor::text,l.approved_minor::text,c.original_amount_minor,
 c.lodging,coalesce(c.missing_explanation,''),coalesce(c.evidence_received_on::text,''),coalesce(c.proposed_account_id::text,''),
 coalesce((SELECT x.account_id::text FROM public.lines x WHERE x.tenant_id=c.tenant_id AND x.book_id=c.business_book_id AND x.entry_id=c.reclassified_entry_id AND x.side='debit' LIMIT 1),
   (SELECT x.account_id::text FROM public.lines x WHERE x.tenant_id=c.tenant_id AND x.book_id=c.obligation_book_id AND x.entry_id=c.obligation_entry_id AND x.side='debit'
   ORDER BY x.amount_minor DESC,x.position LIMIT 1),''),
 c.recognized_at IS NOT NULL,coalesce((SELECT p.pair_key FROM public.entry_pairs p WHERE p.tenant_id=c.tenant_id AND p.beneficiary_book_id=c.obligation_book_id AND p.beneficiary_entry_id=c.obligation_entry_id),''),
 coalesce(c.provenance,''),
 coalesce((SELECT sum(a.amount_minor) FROM public.allocations a WHERE a.tenant_id=c.tenant_id AND a.claim_id=c.id AND a.source='report_payment' AND public.allocation_live(a.tenant_id,a)),0)::text,
 (current_date - c.incurred_on),public.person_capacity_on(c.tenant_id,c.person_id,c.incurred_on),c.expense_policy_version,
 c.rule_profile,c.assessment::text,c.supplier_invoice::text,c.prior_reimbursed_minor::text
 FROM public.report_lines l JOIN public.claims c ON c.tenant_id=l.tenant_id AND c.id=l.claim_id
 WHERE l.tenant_id=$1 AND l.report_id=$2 AND l.revision=$3 ORDER BY l.position`, s.TenantID, r.ID, r.Revision)
	if err != nil {
		return d, dbError(err)
	}
	d.Lines = []ReportLine{}
	ages := map[string]int{}
	for rows.Next() {
		var x ReportLine
		var original int64
		var age int
		var version *int
		var assessment, invoice *string
		if err := rows.Scan(&x.ClaimID, &x.Position, &x.Kind, &x.Date, &x.Vendor, &x.Purpose, &x.AmountMinor, &x.ApprovedMinor, &original, &x.Lodging,
			&x.MissingExplanation, &x.EvidenceReceivedOn, &x.ProposedAccountID, &x.RecordedAccountID, &x.Recognized, &x.PairKey, &x.Provenance, &x.PaidMinor, &age, &x.Capacity, &version, &x.RuleProfile, &assessment, &invoice, &x.PriorReimbursedMinor); err != nil {
			rows.Close()
			return d, dbError(err)
		}
		x.OriginalMinor = strconv.FormatInt(original, 10)
		x.original = original
		if assessment != nil {
			x.Assessment = json.RawMessage(*assessment)
		}
		if invoice != nil {
			x.SupplierInvoice = json.RawMessage(*invoice)
		}
		versions[x.ClaimID] = version
		if version != nil {
			x.PolicyVersion = *version
		}
		x.AccountDiffers = x.ProposedAccountID != "" && x.RecordedAccountID != "" && x.ProposedAccountID != x.RecordedAccountID
		ages[x.ClaimID] = age
		d.Lines = append(d.Lines, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, dbError(err)
	}
	for i := range d.Lines {
		// The profile's check always runs; the company's policy in force on
		// the day of the cost can only ask for more.
		p, err := policyOf(versions[d.Lines[i].ClaimID])
		if err != nil {
			return d, err
		}
		x := &d.Lines[i]
		company, _ := strconv.ParseInt(p.ReceiptThresholdMinor, 10, 64)
		switch {
		case x.original >= profileReceiptMinor || x.Lodging:
			x.ReceiptRequired, x.ReceiptRule = true, "profile"
		case x.original >= company:
			x.ReceiptRequired, x.ReceiptRule = true, "company"
		}
		x.substantiationDays = p.SubstantiationDays
		name := func(account string, into *string) {
			if account != "" {
				_ = tx.QueryRow(ctx, "SELECT name FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, r.Business, account).Scan(into)
			}
		}
		name(d.Lines[i].ProposedAccountID, &d.Lines[i].ProposedAccountName)
		name(d.Lines[i].RecordedAccountID, &d.Lines[i].RecordedAccountName)
		d.Lines[i].AccountName = d.Lines[i].RecordedAccountName
		if d.Lines[i].AccountName == "" {
			d.Lines[i].AccountName = d.Lines[i].ProposedAccountName
		}
		ev, err := claimEvidence(ctx, tx, s.TenantID, d.Lines[i].ClaimID)
		if err != nil {
			return d, err
		}
		d.Lines[i].Evidence = ev
		// Only a receipt or an invoice is a receipt; a statement extract
		// proves payment, not what was bought.
		for _, e := range ev {
			switch e.Kind {
			case "receipt", "invoice":
				d.Lines[i].HasReceipt = true
			case "payment_proof_extract":
				d.Lines[i].HasPaymentProof = true
			}
		}
	}
	// Amounts and status, from the claims and their live allocations.
	var requested, approved, paid int64
	for _, x := range d.Lines {
		n, _ := strconv.ParseInt(x.AmountMinor, 10, 64)
		requested += n
		if x.ApprovedMinor != nil {
			m, _ := strconv.ParseInt(*x.ApprovedMinor, 10, 64)
			approved += m
		}
		p, _ := strconv.ParseInt(x.PaidMinor, 10, 64)
		paid += p
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.report_approvals WHERE tenant_id=$1 AND report_id=$2 AND authority='self'),
 EXISTS(SELECT 1 FROM public.allocations a JOIN public.report_lines l ON l.tenant_id=a.tenant_id AND l.claim_id=a.claim_id
   WHERE a.tenant_id=$1 AND l.report_id=$2 AND a.unapproved AND public.allocation_live(a.tenant_id,a))`, s.TenantID, r.ID).Scan(&d.SelfApproved, &d.PaidBeforeApproval); err != nil {
		return d, dbError(err)
	}
	due := approved
	if r.Workflow != "approved" {
		due = requested
	}
	d.RequestedMinor, d.ApprovedMinor, d.PaidMinor = strconv.FormatInt(requested, 10), strconv.FormatInt(approved, 10), strconv.FormatInt(paid, 10)
	d.RemainingMinor = strconv.FormatInt(max(due-paid, 0), 10)
	switch {
	case paid == 0:
		d.Settlement = "unsettled"
	case paid < due:
		d.Settlement = "partly_settled"
	default:
		d.Settlement = "settled"
	}
	if !full {
		return d, nil
	}
	if r.Workflow == "draft" || r.Workflow == "returned" {
		if _, d.SnapshotSHA256, err = snapshot(ctx, tx, s.TenantID, r); err != nil {
			return d, err
		}
	} else if err := tx.QueryRow(ctx, "SELECT coalesce(snapshot_sha256,'') FROM public.report_revisions WHERE tenant_id=$1 AND report_id=$2 AND revision=$3", s.TenantID, r.ID, r.Revision).Scan(&d.SnapshotSHA256); err != nil {
		return d, dbError(err)
	}
	if r.Workflow == "returned" {
		_ = tx.QueryRow(ctx, "SELECT coalesce(note,'') FROM public.report_events WHERE tenant_id=$1 AND report_id=$2 AND kind='returned' ORDER BY at DESC LIMIT 1", s.TenantID, r.ID).Scan(&d.ReturnNote)
	}
	if d.Revisions, err = readRevisions(ctx, tx, s.TenantID, r.ID); err != nil {
		return d, err
	}
	if d.Payments, err = readPayments(ctx, tx, s.TenantID, r.ID); err != nil {
		return d, err
	}
	if d.Recoverables, err = readRecoverables(ctx, tx, s.TenantID, r.ID); err != nil {
		return d, err
	}
	if d.Residuals, err = readResiduals(ctx, tx, s.TenantID, r.ID); err != nil {
		return d, err
	}
	if err := readReturnCandidates(ctx, tx, s.TenantID, r.LinkID, d.Recoverables); err != nil {
		return d, err
	}
	if d.Statements, err = readReportStatements(ctx, tx, s.TenantID, r.ID); err != nil {
		return d, err
	}
	if d.Events, err = readEvents(ctx, tx, s.TenantID, r.ID); err != nil {
		return d, err
	}
	if d.Notes, err = readNotes(ctx, tx, s.TenantID, r.Business, r.ID); err != nil {
		return d, err
	}
	d.Warnings = reportWarnings(d, ages)
	return d, nil
}

func claimEvidence(ctx context.Context, tx pgx.Tx, tenant, claim string) ([]Evidence, error) {
	rows, err := tx.Query(ctx, `SELECT document_id::text,sha256,kind,supplement,added_at,coalesce(redacted_from_document_id::text,''),coalesce(redaction_provenance,''),retention_until::text
 FROM public.claim_evidence WHERE tenant_id=$1 AND claim_id=$2 AND removed_at IS NULL ORDER BY added_at,id`, tenant, claim)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []Evidence{}
	for rows.Next() {
		var e Evidence
		if err := rows.Scan(&e.DocumentID, &e.SHA256, &e.Kind, &e.Supplement, &e.AddedAt, &e.RedactedFromDocumentID, &e.RedactionProvenance, &e.RetentionUntil); err != nil {
			return nil, dbError(err)
		}
		e.Derived = e.RedactedFromDocumentID != ""
		out = append(out, e)
	}
	return out, dbError(rows.Err())
}

// reportWarnings: a cost more than 60 days after it was paid still
// missing its purpose or a receipt it needs (an accepted explanation
// still warns); money owed back unresolved 120 days after the cost; a
// payment made before approval; and, under 60 days, a receipt still
// needed.
func reportWarnings(d ReportDetail, ages map[string]int) []Warning {
	out := []Warning{}
	for _, x := range d.Lines {
		age := ages[x.ClaimID]
		if x.Capacity == "not_established" {
			// No employee guidance for a relationship not established.
			if x.ReceiptRequired && !x.HasReceipt {
				out = append(out, Warning{Kind: "receipt_required", ClaimID: x.ClaimID, Days: age, Since: x.Date})
			}
			continue
		}
		incomplete := strings.TrimSpace(x.Purpose) == "" || x.ReceiptRequired && !x.HasReceipt
		days := x.substantiationDays
		if days == 0 {
			days = 60
		}
		switch {
		case incomplete && age > days:
			out = append(out, Warning{Kind: "substantiation_60", ClaimID: x.ClaimID, Days: age, Since: x.Date})
		case x.ReceiptRequired && !x.HasReceipt:
			out = append(out, Warning{Kind: "receipt_required", ClaimID: x.ClaimID, Days: age, Since: x.Date})
		}
	}
	for _, r := range d.Recoverables {
		if r.RecoveryStatus != "outstanding" || r.Capacity == "not_established" {
			continue
		}
		since, err := time.Parse("2006-01-02", r.Since)
		if err != nil {
			continue
		}
		if days := int(time.Since(since).Hours() / 24); days > d.Policy.ExcessDays {
			out = append(out, Warning{Kind: "excess_120", Days: days, AmountMinor: r.AmountMinor, Since: r.Since})
		}
	}
	if d.PaidBeforeApproval {
		out = append(out, Warning{Kind: "paid_before_approval"})
	}
	return out
}

func readRevisions(ctx context.Context, tx pgx.Tx, tenant, report string) ([]RevisionView, error) {
	rows, err := tx.Query(ctx, `SELECT v.id::text,v.revision,v.submitted_at,coalesce(v.snapshot_sha256,''),a.approved_at,coalesce(a.authority='self',false),coalesce(v.pdf_document_id::text,''),coalesce(v.reason,'')
 FROM public.report_revisions v LEFT JOIN public.report_approvals a ON a.tenant_id=v.tenant_id AND a.report_id=v.report_id AND a.revision=v.revision
 WHERE v.tenant_id=$1 AND v.report_id=$2 ORDER BY v.revision`, tenant, report)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []RevisionView{}
	for rows.Next() {
		var v RevisionView
		if err := rows.Scan(&v.ID, &v.Revision, &v.SubmittedAt, &v.SnapshotSHA, &v.ApprovedAt, &v.SelfApproved, &v.PDFDocumentID, &v.Reason); err != nil {
			return nil, dbError(err)
		}
		out = append(out, v)
	}
	return out, dbError(rows.Err())
}

func readPayments(ctx context.Context, tx pgx.Tx, tenant, report string) ([]PaymentView, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (a.id) a.id::text,a.claim_id::text,coalesce(a.source_entry_id::text,''),coalesce(e.accounting_date::text,''),a.amount_minor::text,a.unapproved,
 CASE WHEN a.removed_at IS NOT NULL THEN a.removed_at WHEN e.id IS NOT NULL AND public.entry_reversed(a.tenant_id,a.source_book_id,a.source_entry_id) THEN e.recorded_at END
 FROM public.allocations a JOIN public.report_lines l ON l.tenant_id=a.tenant_id AND l.claim_id=a.claim_id AND l.report_id=$2
 LEFT JOIN public.entries e ON e.tenant_id=a.tenant_id AND e.book_id=a.source_book_id AND e.id=a.source_entry_id
 WHERE a.tenant_id=$1 AND a.source='report_payment' ORDER BY a.id`, tenant, report)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []PaymentView{}
	for rows.Next() {
		var p PaymentView
		if err := rows.Scan(&p.AllocationID, &p.ClaimID, &p.EntryID, &p.Date, &p.AmountMinor, &p.Unapproved, &p.RemovedAt); err != nil {
			return nil, dbError(err)
		}
		out = append(out, p)
	}
	return out, dbError(rows.Err())
}

func readRecoverables(ctx context.Context, tx pgx.Tx, tenant, report string) ([]RecoverableView, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,amount_minor::text,origin,cost_date::text,recovery_status,tax_status,public.person_capacity_on(tenant_id,person_id,cost_date)
 FROM public.recoverables WHERE tenant_id=$1 AND report_id=$2 ORDER BY opened_at,id`, tenant, report)
	if err != nil {
		return nil, dbError(err)
	}
	var out []RecoverableView
	for rows.Next() {
		var r RecoverableView
		if err := rows.Scan(&r.ID, &r.AmountMinor, &r.Origin, &r.Since, &r.RecoveryStatus, &r.TaxStatus, &r.Capacity); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		if r.RecoveryStatus != "outstanding" {
			status := r.RecoveryStatus
			r.Resolution = &status
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	for i := range out {
		events, err := tx.Query(ctx, "SELECT track,status,coalesce(note,''),recorded_by::text,recorded_at FROM public.recoverable_events WHERE tenant_id=$1 AND recoverable_id=$2 ORDER BY recorded_at,id", tenant, out[i].ID)
		if err != nil {
			return nil, dbError(err)
		}
		out[i].Events = []RecoverableLog{}
		for events.Next() {
			var e RecoverableLog
			if err := events.Scan(&e.Track, &e.Status, &e.Note, &e.RecordedBy, &e.RecordedAt); err != nil {
				events.Close()
				return nil, dbError(err)
			}
			e.Resolution = e.Status
			out[i].Events = append(out[i].Events, e)
			if e.Note != "" {
				out[i].TreatmentNote, out[i].RecordedBy = e.Note, e.RecordedBy
				at := e.RecordedAt
				out[i].RecordedAt = &at
			}
		}
		events.Close()
		if err := events.Err(); err != nil {
			return nil, dbError(err)
		}
		reviews, err := tx.Query(ctx, `SELECT id::text,coalesce(responsible_person_id::text,''),coalesce(basis_amount_minor::text,''),coalesce(timing,''),review_date::text,
 coalesce(outcome,''),coalesce(outcome_reference,''),recorded_by::text,recorded_at FROM public.recoverable_reviews WHERE tenant_id=$1 AND recoverable_id=$2 ORDER BY recorded_at,id`, tenant, out[i].ID)
		if err != nil {
			return nil, dbError(err)
		}
		out[i].Reviews = []RecoverableReview{}
		for reviews.Next() {
			var v RecoverableReview
			if err := reviews.Scan(&v.ID, &v.ResponsiblePersonID, &v.BasisAmountMinor, &v.Timing, &v.ReviewDate, &v.Outcome, &v.OutcomeReference, &v.RecordedBy, &v.RecordedAt); err != nil {
				reviews.Close()
				return nil, dbError(err)
			}
			out[i].Reviews = append(out[i].Reviews, v)
		}
		reviews.Close()
		if err := reviews.Err(); err != nil {
			return nil, dbError(err)
		}
	}
	if out == nil {
		out = []RecoverableView{}
	}
	return out, nil
}

func readResiduals(ctx context.Context, tx pgx.Tx, tenant, report string) ([]ResidualView, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,source_entry_id::text,amount_minor::text,classification,coalesce(note,''),coalesce(target_report_id::text,''),coalesce(recoverable_id::text,''),classified_at
 FROM public.payment_residuals WHERE tenant_id=$1 AND report_id=$2 ORDER BY created_at,id`, tenant, report)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []ResidualView{}
	for rows.Next() {
		var r ResidualView
		if err := rows.Scan(&r.ID, &r.EntryID, &r.AmountMinor, &r.Classification, &r.Note, &r.TargetReportID, &r.RecoverableID, &r.ClassifiedAt); err != nil {
			return nil, dbError(err)
		}
		out = append(out, r)
	}
	return out, dbError(rows.Err())
}

func readReportStatements(ctx context.Context, tx pgx.Tx, tenant, report string) ([]StatementView, error) {
	rows, err := tx.Query(ctx, "SELECT id::text,issued_at,document_id::text FROM public.settlement_statements WHERE tenant_id=$1 AND report_id=$2 ORDER BY issued_at,id", tenant, report)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []StatementView{}
	for rows.Next() {
		var v StatementView
		if err := rows.Scan(&v.ID, &v.IssuedAt, &v.PDFDocumentID); err != nil {
			return nil, dbError(err)
		}
		out = append(out, v)
	}
	return out, dbError(rows.Err())
}

func readEvents(ctx context.Context, tx pgx.Tx, tenant, report string) ([]ReportEvent, error) {
	rows, err := tx.Query(ctx, "SELECT kind,at,revision,actor_id::text,coalesce(note,'') FROM public.report_events WHERE tenant_id=$1 AND report_id=$2 ORDER BY at,id", tenant, report)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []ReportEvent{}
	for rows.Next() {
		var e ReportEvent
		if err := rows.Scan(&e.Kind, &e.At, &e.Revision, &e.By, &e.Note); err != nil {
			return nil, dbError(err)
		}
		out = append(out, e)
	}
	return out, dbError(rows.Err())
}

// readReturnCandidates lists, for each recoverable still open, the
// entries on or after its date that put money back against what the
// business owes the person: not a cost recorded as owed, and not taken
// already as any recoverable's return.
func readReturnCandidates(ctx context.Context, tx pgx.Tx, tenant, link string, recoverables []RecoverableView) error {
	open := false
	for _, x := range recoverables {
		open = open || x.RecoveryStatus == "outstanding"
	}
	if !open {
		return nil
	}
	l, err := readReportLink(ctx, tx, tenant, link)
	if err != nil {
		return err
	}
	for i := range recoverables {
		x := &recoverables[i]
		if x.RecoveryStatus != "outstanding" {
			continue
		}
		rows, err := tx.Query(ctx, `SELECT e.id::text,e.accounting_date::text,sum(CASE WHEN l.side='credit' THEN l.amount_minor ELSE -l.amount_minor END)::text,e.description
 FROM public.lines l JOIN public.entries e ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.account_id=$3 AND e.accounting_date>=$4::text::date AND e.reversal_of IS NULL
   AND NOT public.entry_reversed(e.tenant_id,e.book_id,e.id)
   AND NOT EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id=$1 AND (p.beneficiary_book_id=$2 AND p.beneficiary_entry_id=e.id OR p.payer_book_id=$2 AND p.payer_entry_id=e.id))
   AND NOT EXISTS (SELECT 1 FROM public.recoverable_events v WHERE v.tenant_id=$1 AND v.business_book_id=$2 AND v.track='recovery' AND v.status='returned' AND v.entry_id=e.id)
 GROUP BY e.id,e.accounting_date,e.sequence,e.description
 HAVING sum(CASE WHEN l.side='credit' THEN l.amount_minor ELSE -l.amount_minor END)>0
 ORDER BY sum(CASE WHEN l.side='credit' THEN l.amount_minor ELSE -l.amount_minor END)=$5::text::bigint DESC,e.accounting_date,e.sequence LIMIT 20`,
			tenant, l.Business, l.Payable, x.Since, x.AmountMinor)
		if err != nil {
			return dbError(err)
		}
		for rows.Next() {
			var c ReturnCandidate
			if err := rows.Scan(&c.EntryID, &c.Date, &c.AmountMinor, &c.Description); err != nil {
				rows.Close()
				return dbError(err)
			}
			c.AmountFits = c.AmountMinor == x.AmountMinor
			x.ReturnCandidates = append(x.ReturnCandidates, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return dbError(err)
		}
	}
	return nil
}

// Candidate is a cost a business owes a person that a report can claim.
type Candidate struct {
	PairKey           string `json:"pair_key"`
	ObligationEntryID string `json:"obligation_entry_id"`
	Date              string `json:"date"`
	Vendor            string `json:"vendor"`
	Description       string `json:"description"`
	AmountMinor       string `json:"amount_minor"`
	OpenMinor         string `json:"open_minor"`
	RecordedAccountID string `json:"recorded_account_id,omitempty"`
	OnReportID        string `json:"on_report_id,omitempty"`
	OnReportReference string `json:"on_report_reference,omitempty"`
	HasReceipt        bool   `json:"has_receipt"`
	HasPaymentProof   bool   `json:"has_payment_proof"`
	AgeDays           int    `json:"age_days"`
	Lodging           bool   `json:"lodging"`
	Kind              string `json:"kind"`
}

// ReportCandidates lists the costs still open under a link, with whether
// each is on a live report already and has a receipt.
func (a TenantAdmin) ReportCandidates(ctx context.Context, link string) ([]Candidate, error) {
	business, err := a.LinkBusiness(ctx, link)
	if err != nil {
		return nil, err
	}
	out := []Candidate{}
	err = a.withBookRead(ctx, business, func(tx pgx.Tx, s Scope, _ Book) error {
		l, err := readReportLink(ctx, tx, s.TenantID, link)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT o.pair_key,o.entry_id::text,o.accounting_date::text,o.description,o.amount::text,o.open::text,o.kind,
 coalesce((SELECT x.account_id::text FROM public.lines x WHERE x.tenant_id=$1 AND x.book_id=o.book_id AND x.entry_id=o.entry_id AND x.side='debit' ORDER BY x.amount_minor DESC,x.position LIMIT 1),''),
 coalesce(r.id::text,''),coalesce(r.reference,''),coalesce(c.lodging,false),
 EXISTS(SELECT 1 FROM public.claim_evidence v WHERE v.tenant_id=$1 AND v.claim_id=c.id AND v.removed_at IS NULL AND v.kind IN ('receipt','invoice'))
   OR EXISTS(SELECT 1 FROM public.evidence_links v WHERE v.tenant_id=$1 AND v.action='attach' AND v.role IN ('receipt','invoice')
     AND (v.book_id=o.payer_book_id AND v.entry_id=o.payer_entry_id OR v.book_id=o.beneficiary_book_id AND v.entry_id=o.beneficiary_entry_id)
     AND NOT EXISTS (SELECT 1 FROM public.evidence_links w WHERE w.tenant_id=v.tenant_id AND w.book_id=v.book_id AND w.link_id=v.link_id AND w.revision>v.revision)),
 EXISTS(SELECT 1 FROM public.claim_evidence v WHERE v.tenant_id=$1 AND v.claim_id=c.id AND v.removed_at IS NULL AND v.kind='payment_proof_extract')
   OR EXISTS(SELECT 1 FROM public.source_citations x WHERE x.tenant_id=$1
     AND (x.book_id=o.payer_book_id AND x.entry_id=o.payer_entry_id OR x.book_id=o.beneficiary_book_id AND x.entry_id=o.beneficiary_entry_id)),
 (current_date - o.accounting_date)
 FROM public.open_costs($1,$2,$3,$4,NULL) o
 LEFT JOIN public.claims c ON c.tenant_id=$1 AND c.obligation_book_id=o.book_id AND c.obligation_entry_id=o.entry_id
 LEFT JOIN LATERAL (SELECT r.id,r.reference FROM public.report_lines rl JOIN public.expense_reports r ON r.tenant_id=rl.tenant_id AND r.id=rl.report_id AND r.revision=rl.revision
   WHERE rl.tenant_id=$1 AND rl.claim_id=c.id AND r.workflow NOT IN ('withdrawn','superseded') ORDER BY r.created_at DESC LIMIT 1) r ON true
 WHERE o.open > 0 ORDER BY o.accounting_date,o.sequence,o.pair_key`, s.TenantID, l.Personal, l.Business, l.Payable)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var c Candidate
			if err := rows.Scan(&c.PairKey, &c.ObligationEntryID, &c.Date, &c.Description, &c.AmountMinor, &c.OpenMinor, &c.Kind, &c.RecordedAccountID,
				&c.OnReportID, &c.OnReportReference, &c.Lodging, &c.HasReceipt, &c.HasPaymentProof, &c.AgeDays); err != nil {
				return dbError(err)
			}
			c.Vendor = c.Description
			out = append(out, c)
		}
		return dbError(rows.Err())
	})
	return out, err
}

// MatchProposal is a payment from the business to the person that may pay
// a report: by the reference in its memo, or by amount and payee only.
type MatchProposal struct {
	BankItemID  string          `json:"bank_item_id,omitempty"`
	EntryID     string          `json:"entry_id,omitempty"`
	Date        string          `json:"date"`
	AmountMinor string          `json:"amount_minor"`
	Description string          `json:"description"`
	Reason      string          `json:"reason"`
	Checks      map[string]bool `json:"checks"`
}

// ReportMatches proposes payments for a report: any in the business's
// Book whose memo carries the reference first, checked against business,
// payee, currency and amount; then any of the amount left that reduced
// what the business owes this person.
func (a TenantAdmin) ReportMatches(ctx context.Context, book, reference string) (string, []MatchProposal, error) {
	var left string
	out := []MatchProposal{}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		d, err := readDetail(ctx, tx, s, reference, false)
		if err != nil {
			return err
		}
		left = d.RemainingMinor
		l, err := readReportLink(ctx, tx, s.TenantID, d.LinkID)
		if err != nil {
			return err
		}
		token := strings.ReplaceAll(d.Reference, "-", "")
		rows, err := tx.Query(ctx, `SELECT t.id::text,coalesce(k.entry_id::text,''),t.occurred_on::text,(-t.amount_minor)::text,t.description,t.currency,
 upper(regexp_replace(t.description||' '||coalesce(t.counterparty,''),'[^0-9A-Za-z]','','g')) LIKE '%'||$3||'%',
 coalesce(k.entry_id IS NOT NULL AND EXISTS(SELECT 1 FROM public.lines x WHERE x.tenant_id=$1 AND x.book_id=$2 AND x.entry_id=k.entry_id AND x.account_id=$4 AND x.side='debit'),false),
 coalesce(EXISTS(SELECT 1 FROM public.allocations a WHERE a.tenant_id=$1 AND a.source_entry_id=k.entry_id AND a.source='report_payment' AND a.removed_at IS NULL),false)
 FROM public.bank_transactions t
 LEFT JOIN public.bank_transaction_links k ON k.tenant_id=t.tenant_id AND k.book_id=t.book_id AND k.transaction_id=t.id AND k.revoked_at IS NULL
 WHERE t.tenant_id=$1 AND t.book_id=$2 AND t.amount_minor<0
 ORDER BY t.occurred_on DESC,t.id LIMIT 500`, s.TenantID, l.Business, strings.ToUpper(token), l.Payable)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		var byReference, byAmount []MatchProposal
		for rows.Next() {
			var p MatchProposal
			var currency string
			var hasRef, payee, applied bool
			if err := rows.Scan(&p.BankItemID, &p.EntryID, &p.Date, &p.AmountMinor, &p.Description, &currency, &hasRef, &payee, &applied); err != nil {
				return dbError(err)
			}
			if applied {
				continue
			}
			p.Checks = map[string]bool{"business": true, "payee": payee, "currency": currency == d.Currency, "amount": p.AmountMinor == left}
			switch {
			case hasRef:
				p.Reason = "reference"
				byReference = append(byReference, p)
			case payee && p.AmountMinor == left && left != "0":
				p.Reason = "amount_payee"
				byAmount = append(byAmount, p)
			}
		}
		out = append(append(out, byReference...), byAmount...)
		return dbError(rows.Err())
	})
	return left, out, err
}

// Reconciliation is one person's running balance with a business, from
// claims and allocations only.
type Reconciliation struct {
	Currency              string `json:"currency"`
	RecognizedNotReported string `json:"recognized_not_reported_minor"`
	Reported              string `json:"reported_minor"`
	Applied               string `json:"applied_minor"`
	RemainingPayable      string `json:"remaining_payable_minor"`
	Recoverable           string `json:"recoverable_minor"`
	Unapplied             string `json:"unapplied_minor"`
}

// ReportReconciliation reconciles what a business owes a person under a
// link: recorded as owed but on no report, on reports, paid, still owed,
// owed back, and paid but applied to nothing.
func (a TenantAdmin) ReportReconciliation(ctx context.Context, link string) (Reconciliation, error) {
	var out Reconciliation
	business, err := a.LinkBusiness(ctx, link)
	if err != nil {
		return out, err
	}
	err = a.withBookRead(ctx, business, func(tx pgx.Tx, s Scope, b Book) error {
		l, err := readReportLink(ctx, tx, s.TenantID, link)
		if err != nil {
			return err
		}
		out.Currency = b.Currency
		var notReported, reported, applied, remaining, recoverable, unapplied int64
		err = tx.QueryRow(ctx, `WITH live AS (
  SELECT DISTINCT l.claim_id FROM public.report_lines l JOIN public.expense_reports r ON r.tenant_id=l.tenant_id AND r.id=l.report_id AND r.revision=l.revision
  WHERE l.tenant_id=$1 AND r.link_id=$5 AND r.workflow NOT IN ('withdrawn','superseded'))
 SELECT
 coalesce((SELECT sum(o.open) FROM public.open_costs($1,$2,$3,$4,NULL) o WHERE NOT EXISTS (SELECT 1 FROM public.claims c JOIN live ON live.claim_id=c.id
   WHERE c.tenant_id=$1 AND c.obligation_book_id=o.book_id AND c.obligation_entry_id=o.entry_id)),0),
 coalesce((SELECT sum(c.requested_minor) FROM public.claims c JOIN live ON live.claim_id=c.id WHERE c.tenant_id=$1),0),
 coalesce((SELECT sum(a.amount_minor) FROM public.allocations a WHERE a.tenant_id=$1 AND a.debtor_book_id=$3 AND a.creditor_book_id=$2 AND public.allocation_live(a.tenant_id,a)),0),
 coalesce((SELECT sum(o.open) FROM public.open_costs($1,$2,$3,$4,NULL) o),0),
 coalesce((SELECT sum(x.amount_minor) FROM public.recoverables x JOIN public.expense_reports r ON r.tenant_id=x.tenant_id AND r.id=x.report_id
   WHERE x.tenant_id=$1 AND r.link_id=$5 AND x.recovery_status='outstanding'),0),
 public.unapplied_amount($1,$2,$3,$4)`, s.TenantID, l.Personal, l.Business, l.Payable, link).Scan(&notReported, &reported, &applied, &remaining, &recoverable, &unapplied)
		if err != nil {
			return dbError(err)
		}
		f := func(n int64) string { return strconv.FormatInt(n, 10) }
		out.RecognizedNotReported, out.Reported, out.Applied, out.RemainingPayable, out.Recoverable, out.Unapplied = f(notReported), f(reported), f(applied), f(remaining), f(recoverable), f(unapplied)
		return nil
	})
	return out, err
}

// errNoReport is a reference no report of this Book has.
var errNoReport = errors.New("no such report")

func readNotes(ctx context.Context, tx pgx.Tx, tenant, book, report string) ([]ReportNote, error) {
	rows, err := tx.Query(ctx, "SELECT id::text,audience,text,author_id::text,at FROM public.report_notes WHERE tenant_id=$1 AND book_id=$2 AND report_id=$3 ORDER BY at,id", tenant, book, report)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []ReportNote{}
	for rows.Next() {
		var n ReportNote
		if err := rows.Scan(&n.ID, &n.Audience, &n.Text, &n.AuthorID, &n.At); err != nil {
			return nil, dbError(err)
		}
		out = append(out, n)
	}
	return out, dbError(rows.Err())
}
