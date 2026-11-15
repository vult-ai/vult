package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SubmitReport is the person's attestation, and for an owner who may
// approve their own reports, the approval in the same confirmation. Proof
// is the passkey assertion made for this revision and action.
type SubmitReport struct {
	Revision       int    `json:"revision"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
	Attestation    bool   `json:"attestation"`
	Approve        bool   `json:"approve"`
	ProofToken     string `json:"-"`
}

// ReportEvent is one step in a report's history.
type ReportEvent struct {
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Revision int       `json:"revision"`
	By       string    `json:"by"`
	Note     string    `json:"note,omitempty"`
}

const attestationText = "These are business costs I paid, and nobody has paid me back for them."

// ProofBinding is what a report's passkey proof is made for: this report,
// the revision by its own id and number, and its snapshot.
func ProofBinding(report, revisionID string, revision int, snapshot string) string {
	return report + ":" + revisionID + ":" + strconv.Itoa(revision) + ":" + snapshot
}

// ProofTarget is the revision a report's passkey proof binds to now.
type ProofTarget struct {
	Report, RevisionID string
	Revision           int
	SnapshotSHA256     string
}

// Binding is what a proof for this target is made for.
func (p ProofTarget) Binding() string {
	return ProofBinding(p.Report, p.RevisionID, p.Revision, p.SnapshotSHA256)
}

// ReportProofTarget is what a passkey proof for a report binds to now:
// the report, the revision being submitted or approved, and its snapshot.
func (a TenantAdmin) ReportProofTarget(ctx context.Context, book, reference string) (ProofTarget, error) {
	var out ProofTarget
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, false)
		if err != nil {
			return err
		}
		out.Report, out.RevisionID, out.Revision = r.ID, r.RevisionID, r.Revision
		if r.Workflow == "submitted" {
			return tx.QueryRow(ctx, "SELECT snapshot_sha256 FROM public.report_revisions WHERE tenant_id=$1 AND report_id=$2 AND revision=$3", s.TenantID, r.ID, r.Revision).Scan(&out.SnapshotSHA256)
		}
		_, out.SnapshotSHA256, err = snapshot(ctx, tx, s.TenantID, r)
		return err
	})
	return out, err
}

// SubmitReport freezes the draft as submitted, with the person's
// attestation, and approves it in the same confirmation where the policy
// lets the owner approve their own. identity is who confirmed.
func (a TenantAdmin) SubmitReport(ctx context.Context, book, actor, identity, reference string, q SubmitReport) ([]ReportEvent, error) {
	var events []ReportEvent
	if !q.Attestation || q.Revision < 1 || len(q.SnapshotSHA256) != 64 || !ValidID(identity) {
		return nil, ErrInvalid
	}
	err := a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if err := editable(r); err != nil {
			return err
		}
		data, sum, err := snapshot(ctx, tx, s.TenantID, r)
		if err != nil {
			return err
		}
		if r.Revision != q.Revision || sum != q.SnapshotSHA256 {
			return ErrRevisionChanged
		}
		var lines int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM public.report_lines WHERE tenant_id=$1 AND report_id=$2 AND revision=$3", s.TenantID, r.ID, r.Revision).Scan(&lines); err != nil {
			return err
		}
		if lines == 0 {
			return &RefusedError{Reason: "Add the costs to claim first."}
		}
		var missing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.report_lines l JOIN public.claims c ON c.tenant_id=l.tenant_id AND c.id=l.claim_id
 WHERE l.tenant_id=$1 AND l.report_id=$2 AND l.revision=$3 AND btrim(c.purpose)=''`, s.TenantID, r.ID, r.Revision).Scan(&missing); err != nil {
			return err
		}
		if missing > 0 {
			return &RefusedError{Reason: "Say what each cost was for before submitting."}
		}
		purpose := "submit"
		if q.Approve {
			purpose = "submit_approve"
		}
		proof := sha256.Sum256([]byte(q.ProofToken))
		var who string
		if err := tx.QueryRow(ctx, "SELECT public.report_spend_proof($1,$2,$3,$4,$5)::text", s.TenantID, s.BookID, hex.EncodeToString(proof[:]), purpose,
			ProofBinding(r.ID, r.RevisionID, r.Revision, sum)).Scan(&who); err != nil {
			return err
		}
		if who != identity {
			return ErrDenied
		}
		// The person attests in the words of the policy in force.
		policy, err := readPolicy(ctx, tx, s.TenantID, s.BookID, "")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE public.report_revisions SET snapshot=$4::jsonb,snapshot_sha256=$5,submitted_at=clock_timestamp(),submitted_by=$6,attestation=$7
 WHERE tenant_id=$1 AND report_id=$2 AND revision=$3`, s.TenantID, r.ID, r.Revision, string(data), sum, actor, policy.AttestationText); err != nil {
			return err
		}
		if err := assessClaims(ctx, tx, s, reference); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE public.expense_reports SET workflow='submitted' WHERE tenant_id=$1 AND id=$2", s.TenantID, r.ID); err != nil {
			return err
		}
		if err := reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, "submitted", actor, identity, policy.AttestationText); err != nil {
			return err
		}
		if q.Approve {
			if err := approve(ctx, tx, s, r, actor, identity, sum, hex.EncodeToString(proof[:]), true); err != nil {
				return err
			}
		}
		events, err = revisionEvents(ctx, tx, s.TenantID, r.ID, r.Revision)
		return err
	})
	return events, err
}

// ApproveReport is the approver's approval of the revision submitted,
// failing if it changed. proof is the passkey assertion for this revision.
func (a TenantAdmin) ApproveReport(ctx context.Context, book, actor, identity, reference string, revision int, snapshotSHA, proofToken string) ([]ReportEvent, error) {
	var events []ReportEvent
	if revision < 1 || len(snapshotSHA) != 64 || !ValidID(identity) {
		return nil, ErrInvalid
	}
	err := a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if r.Workflow != "submitted" {
			return &RefusedError{Reason: "Only a submitted report is approved."}
		}
		var sum string
		if err := tx.QueryRow(ctx, "SELECT snapshot_sha256 FROM public.report_revisions WHERE tenant_id=$1 AND report_id=$2 AND revision=$3", s.TenantID, r.ID, r.Revision).Scan(&sum); err != nil {
			return err
		}
		if r.Revision != revision || sum != snapshotSHA {
			return ErrRevisionChanged
		}
		proof := sha256.Sum256([]byte(proofToken))
		var who string
		if err := tx.QueryRow(ctx, "SELECT public.report_spend_proof($1,$2,$3,'approve',$4)::text", s.TenantID, s.BookID, hex.EncodeToString(proof[:]),
			ProofBinding(r.ID, r.RevisionID, r.Revision, sum)).Scan(&who); err != nil {
			return err
		}
		if who != identity {
			return ErrDenied
		}
		if err := approve(ctx, tx, s, r, actor, identity, sum, hex.EncodeToString(proof[:]), false); err != nil {
			return err
		}
		events, err = revisionEvents(ctx, tx, s.TenantID, r.ID, r.Revision)
		return err
	})
	return events, err
}

// approve records the approval of the submitted revision under the policy
// in force: the owner's own, flagged, where the policy permits it, or an
// independent approver who isn't the person claiming. Cash claims are
// accepted as owed, a distinct event, in the same confirmation.
func approve(ctx context.Context, tx pgx.Tx, s Scope, r reportRow, actor, identity, sum, proofHash string, self bool) error {
	// Who may approve is decided by the policy in force now; the approval
	// keeps that version.
	policy, err := readPolicy(ctx, tx, s.TenantID, s.BookID, "")
	if err != nil {
		return err
	}
	var claimant string
	if err := tx.QueryRow(ctx, "SELECT coalesce(identity_id::text,'') FROM public.people WHERE tenant_id=$1 AND id=$2", s.TenantID, r.PersonID).Scan(&claimant); err != nil {
		return err
	}
	if self && policy.ApprovalKind != "owner_self" {
		return &RefusedError{Reason: "This business needs someone other than the person claiming to approve it."}
	}
	authority, approverPerson := "self", ""
	switch {
	case policy.ApprovalKind == "owner_self":
		if !self && claimant != "" && claimant != identity {
			authority = "independent"
		}
	case identity == claimant:
		return &RefusedError{Reason: "This business needs someone other than the person claiming to approve it."}
	default:
		eligible := append([]PersonRef{}, policy.Approvers...)
		if policy.Fallback != nil {
			eligible = append(eligible, *policy.Fallback)
		}
		for _, p := range eligible {
			var who string
			if err := tx.QueryRow(ctx, "SELECT coalesce(identity_id::text,'') FROM public.people WHERE tenant_id=$1 AND id=$2", s.TenantID, p.ID).Scan(&who); err != nil {
				return err
			}
			if who != "" && who == identity && p.ID != r.PersonID {
				authority, approverPerson = "independent", p.ID
				break
			}
		}
		if approverPerson == "" {
			return &RefusedError{Reason: "Only this business's approver can approve it."}
		}
	}
	accepted, err := acceptCashClaims(ctx, tx, s, r, actor)
	if err != nil {
		return err
	}
	if accepted > 0 {
		if err := reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, "accepted", actor, identity, strconv.Itoa(accepted)+" cash costs recorded as owed"); err != nil {
			return err
		}
	}
	var version any
	if policy.Version > 0 {
		version = policy.Version
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.report_approvals(tenant_id,report_id,business_book_id,revision,snapshot_sha256,approver_identity_id,approver_person_id,authority,approval_policy_version,proof_hash)
 VALUES($1,$2,$3,$4,$5,$6,nullif($7,'')::uuid,$8,$9,$10)`, s.TenantID, r.ID, r.Business, r.Revision, sum, identity, approverPerson, authority, version, proofHash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE public.report_lines SET approved_minor=portion_minor WHERE tenant_id=$1 AND report_id=$2 AND revision=$3", s.TenantID, r.ID, r.Revision); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE public.expense_reports SET workflow='approved' WHERE tenant_id=$1 AND id=$2", s.TenantID, r.ID); err != nil {
		return err
	}
	note := ""
	if authority == "self" {
		note = "self-approved"
	}
	if err := reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, "approved", actor, identity, note); err != nil {
		return err
	}
	return amendmentEffect(ctx, tx, s, r, actor)
}

// acceptCashClaims records each cash claim on the revision as owed on the
// business's side: its cost against what it owes the person. The person's
// side is theirs to record, from their own Book; it waits as something for
// them to do.
func acceptCashClaims(ctx context.Context, tx pgx.Tx, s Scope, r reportRow, actor string) (int, error) {
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.incurred_on::text,c.vendor,c.requested_minor::text,coalesce(c.proposed_account_id::text,''),c.paid_from_account_id::text,coalesce(p.personal_book_id::text,'')
 FROM public.report_lines l JOIN public.claims c ON c.tenant_id=l.tenant_id AND c.id=l.claim_id JOIN public.people p ON p.tenant_id=c.tenant_id AND p.id=c.person_id
 WHERE l.tenant_id=$1 AND l.report_id=$2 AND l.revision=$3 AND c.kind='cash' AND c.recognized_at IS NULL ORDER BY l.position`, s.TenantID, r.ID, r.Revision)
	if err != nil {
		return 0, err
	}
	type cash struct{ id, date, vendor, amount, account, from, personal string }
	var all []cash
	for rows.Next() {
		var c cash
		if err := rows.Scan(&c.id, &c.date, &c.vendor, &c.amount, &c.account, &c.from, &c.personal); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	l, err := readReportLink(ctx, tx, s.TenantID, r.LinkID)
	if err != nil {
		return 0, err
	}
	for _, c := range all {
		if c.account == "" {
			return 0, &RefusedError{Reason: "Choose the account for " + c.vendor + " before approving."}
		}
		key := "claim-" + c.id
		post := func(book string, lines []PostLine) (string, error) {
			bs := Scope{TenantID: s.TenantID, BookID: book, ActorID: actor}
			if err := setScope(ctx, tx, bs); err != nil {
				return "", err
			}
			result, err := dispatch(ctx, tx, bs, "post_journal", raw(PostJournal{RequestKey: key + "/" + book, EconomicDate: c.date, AccountingDate: c.date,
				Description: c.vendor + " (" + r.Reference + ")", Lines: lines}), nil)
			if err != nil {
				return "", err
			}
			entry := outcomeEntry(&Outcome{Result: result})
			if entry == "" {
				return "", ErrUncertain
			}
			// Each Book's own checks run while it is the one selected.
			if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
				return "", err
			}
			_, err = tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED")
			return entry, err
		}
		// The business's side only: its cost against what it owes the
		// person. The person records theirs in their own Book.
		business, err := post(l.Business, []PostLine{{AccountID: c.account, Side: "debit", AmountMinor: c.amount}, {AccountID: l.Payable, Side: "credit", AmountMinor: c.amount}})
		if err != nil {
			return 0, err
		}
		if err := setScope(ctx, tx, s); err != nil {
			return 0, err
		}
		var status string
		if err := tx.QueryRow(ctx, "SELECT public.accept_claim($1,$2,$3,$4)", s.TenantID, c.id, business, actor).Scan(&status); err != nil {
			return 0, err
		}
		if status == "pending" {
			var shown string
			if err := tx.QueryRow(ctx, "SELECT public.book_amount($1,$2,$3::bigint)", s.TenantID, l.Business, c.amount).Scan(&shown); err != nil {
				return 0, err
			}
			if err := ownerLoop(ctx, tx, s.TenantID, c.personal, actor, "claim-side-"+c.id, "claim:"+c.id,
				"Record your side of "+c.vendor+" ("+c.date+", "+shown+"), now owed to you by the business on "+r.Reference+"."); err != nil {
				return 0, err
			}
			if err := setScope(ctx, tx, s); err != nil {
				return 0, err
			}
		}
	}
	return len(all), nil
}

func revisionEvents(ctx context.Context, tx pgx.Tx, tenant, report string, revision int) ([]ReportEvent, error) {
	rows, err := tx.Query(ctx, `SELECT kind,at,revision,actor_id::text,coalesce(note,'') FROM public.report_events WHERE tenant_id=$1 AND report_id=$2 AND revision=$3
 AND kind IN ('submitted','accepted','approved') ORDER BY at,kind`, tenant, report, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReportEvent{}
	for rows.Next() {
		var e ReportEvent
		if err := rows.Scan(&e.Kind, &e.At, &e.Revision, &e.By, &e.Note); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReturnReport sends a submitted report back to the person with a note.
func (a TenantAdmin) ReturnReport(ctx context.Context, book, actor, identity, reference, note string) error {
	note = strings.TrimSpace(note)
	if note == "" || len(note) > 2000 {
		return ErrInvalid
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if r.Workflow != "submitted" {
			return &RefusedError{Reason: "Only a submitted report is returned."}
		}
		return nextRevision(ctx, tx, s, r, actor, identity, "returned", note)
	})
}

// WithdrawReport takes back a draft or submitted report no money is
// matched to; its claims are free for another report.
func (a TenantAdmin) WithdrawReport(ctx context.Context, book, actor, identity, reference, reason string) error {
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if r.Workflow != "draft" && r.Workflow != "submitted" && r.Workflow != "returned" {
			return &RefusedError{Reason: "An approved report isn't withdrawn; correct it instead."}
		}
		var paid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.allocations a JOIN public.report_lines l ON l.tenant_id=a.tenant_id AND l.claim_id=a.claim_id
 WHERE a.tenant_id=$1 AND l.report_id=$2 AND a.removed_at IS NULL)`, s.TenantID, r.ID).Scan(&paid); err != nil {
			return err
		}
		if paid {
			return &RefusedError{Reason: "Money is matched to this report, so it isn't withdrawn; correct it instead."}
		}
		if _, err := tx.Exec(ctx, "UPDATE public.expense_reports SET workflow='withdrawn' WHERE tenant_id=$1 AND id=$2", s.TenantID, r.ID); err != nil {
			return err
		}
		return reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, "withdrawn", actor, identity, strings.TrimSpace(reason))
	})
}

// DeleteDraftReport throws away a draft that was never submitted, with
// the cash claims only it held. Anything ever submitted is withdrawn
// instead, and kept.
func (a TenantAdmin) DeleteDraftReport(ctx context.Context, book, actor, reference string) error {
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "SELECT public.delete_draft_report($1,$2,$3)", s.TenantID, r.Business, r.ID)
		return err
	})
}

// AmendReport opens an approved report as a new draft revision, keeping
// the approved one. Its lines start as the approved ones.
func (a TenantAdmin) AmendReport(ctx context.Context, book, actor, reference, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return &RefusedError{Reason: "Say why the report changes."}
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if r.Workflow != "approved" {
			return &RefusedError{Reason: "Only an approved report is corrected."}
		}
		return nextRevision(ctx, tx, s, r, actor, "", "amended", reason)
	})
}

// nextRevision opens the next revision as a draft (returned or amended),
// starting from the current lines.
func nextRevision(ctx context.Context, tx pgx.Tx, s Scope, r reportRow, actor, identity, kind, note string) error {
	next := r.Revision + 1
	if _, err := tx.Exec(ctx, "INSERT INTO public.report_revisions(tenant_id,id,report_id,business_book_id,revision,reason) VALUES($1,$2,$3,$4,$5,$6)", s.TenantID, ID(), r.ID, r.Business, next, note); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE public.expense_reports SET revision=$3,workflow=$4 WHERE tenant_id=$1 AND id=$2", s.TenantID, r.ID, next, map[string]string{"returned": "returned", "amended": "draft"}[kind]); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.report_lines(tenant_id,report_id,business_book_id,revision,claim_id,position,portion_minor)
 SELECT tenant_id,report_id,business_book_id,$3,claim_id,position,portion_minor FROM public.report_lines WHERE tenant_id=$1 AND report_id=$2 AND revision=$4`,
		s.TenantID, r.ID, next, r.Revision); err != nil {
		return err
	}
	return reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, kind, actor, identity, note)
}

// amendmentEffect settles a corrected report's money: where an approved
// claim now asks less than was already paid on it, the difference is
// recoverable and its payments stay as they were; where it asks more, the
// rest is owed.
func amendmentEffect(ctx context.Context, tx pgx.Tx, s Scope, r reportRow, actor string) error {
	if err := correctCashClaims(ctx, tx, s, r, actor); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT l.claim_id::text,c.incurred_on::text,coalesce(l.approved_minor,0),
 coalesce((SELECT sum(a.amount_minor) FROM public.allocations a WHERE a.tenant_id=l.tenant_id AND a.claim_id=l.claim_id AND a.source='report_payment' AND public.allocation_live(a.tenant_id,a)),0),
 coalesce((SELECT sum(x.amount_minor) FROM public.recoverables x WHERE x.tenant_id=l.tenant_id AND x.report_id=l.report_id AND x.origin='reduction'
   AND x.origin_entry_id IS NULL AND x.cost_date=c.incurred_on),0)
 FROM public.report_lines l JOIN public.claims c ON c.tenant_id=l.tenant_id AND c.id=l.claim_id
 WHERE l.tenant_id=$1 AND l.report_id=$2 AND l.revision=$3`, s.TenantID, r.ID, r.Revision)
	if err != nil {
		return err
	}
	type over struct {
		date   string
		amount int64
	}
	var overs []over
	for rows.Next() {
		var claim, date string
		var approved, paid, already int64
		if err := rows.Scan(&claim, &date, &approved, &paid, &already); err != nil {
			rows.Close()
			return err
		}
		if paid > approved && paid-approved > already {
			overs = append(overs, over{date, paid - approved - already})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Claims a correction removed keep their payments, and all of it is
	// recoverable.
	var removed int64
	var removedDate *string
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(a.amount_minor),0),min(c.incurred_on)::text FROM public.allocations a JOIN public.claims c ON c.tenant_id=a.tenant_id AND c.id=a.claim_id
 WHERE a.tenant_id=$1 AND a.source='report_payment' AND public.allocation_live(a.tenant_id,a)
   AND EXISTS (SELECT 1 FROM public.report_lines o WHERE o.tenant_id=a.tenant_id AND o.report_id=$2 AND o.claim_id=a.claim_id)
   AND NOT EXISTS (SELECT 1 FROM public.report_lines n WHERE n.tenant_id=a.tenant_id AND n.report_id=$2 AND n.revision=$3 AND n.claim_id=a.claim_id)`,
		s.TenantID, r.ID, r.Revision).Scan(&removed, &removedDate); err != nil {
		return err
	}
	if removed > 0 && removedDate != nil {
		overs = append(overs, over{*removedDate, removed})
	}
	for _, o := range overs {
		if _, err := tx.Exec(ctx, `INSERT INTO public.recoverables(tenant_id,id,business_book_id,report_id,person_id,amount_minor,origin,cost_date) VALUES($1,$2,$3,$4,$5,$6,'reduction',$7::date)`,
			s.TenantID, ID(), r.Business, r.ID, r.PersonID, o.amount, o.date); err != nil {
			return err
		}
		if err := reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, "recoverable_opened", actor, "", strconv.FormatInt(o.amount, 10)); err != nil {
			return err
		}
	}
	return nil
}

// ApplyReportPayment matches a payment from the business to the person:
// it pays this report's approved claims, oldest first unless amounts are
// given, then any other reports named; the rest is recoverable, owed back
// to the business. A payment recorded before the report was approved is
// flagged. entry is the payment's entry in the business's Book.
func (a TenantAdmin) ApplyReportPayment(ctx context.Context, book, actor, reference, entry string, amount int64, also []ReportPart) (PaymentResult, error) {
	var out PaymentResult
	if !ValidID(entry) || amount < 0 || len(also) > 20 {
		return out, ErrInvalid
	}
	err := a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('vult.allocating','on',true)"); err != nil {
			return err
		}
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if !r.Reconciled {
			return errNotReconciled
		}
		if r.Workflow == "withdrawn" {
			return &RefusedError{Reason: "This report was withdrawn."}
		}
		l, err := readReportLink(ctx, tx, s.TenantID, r.LinkID)
		if err != nil {
			return err
		}
		var reduced int64
		var date string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(CASE WHEN x.side='debit' THEN x.amount_minor ELSE -x.amount_minor END),0),min(e.accounting_date)::text
 FROM public.lines x JOIN public.entries e ON e.tenant_id=x.tenant_id AND e.book_id=x.book_id AND e.id=x.entry_id
 WHERE x.tenant_id=$1 AND x.book_id=$2 AND x.entry_id=$3 AND x.account_id=$4`, s.TenantID, l.Business, entry, l.Payable).Scan(&reduced, &date); err != nil {
			return err
		}
		if reduced <= 0 {
			return &RefusedError{Reason: "That entry isn't a payment of what the business owes this person."}
		}
		if amount == 0 || amount > reduced {
			amount = reduced
		}
		left := amount
		items := []map[string]any{}
		apply := func(report reportRow, limit int64) (int64, error) {
			rows, err := tx.Query(ctx, `SELECT l.claim_id::text,c.obligation_entry_id IS NOT NULL,coalesce(l.approved_minor,l.portion_minor)
 - coalesce((SELECT sum(a.amount_minor) FROM public.allocations a WHERE a.tenant_id=l.tenant_id AND a.claim_id=l.claim_id AND a.source='report_payment' AND public.allocation_live(a.tenant_id,a)),0)
 FROM public.report_lines l JOIN public.claims c ON c.tenant_id=l.tenant_id AND c.id=l.claim_id
 WHERE l.tenant_id=$1 AND l.report_id=$2 AND l.revision=$3 ORDER BY c.incurred_on,l.position`, s.TenantID, report.ID, report.Revision)
			if err != nil {
				return 0, err
			}
			defer rows.Close()
			used := int64(0)
			for rows.Next() && used < limit {
				var claim string
				var owed bool
				var due int64
				if err := rows.Scan(&claim, &owed, &due); err != nil {
					return 0, err
				}
				if !owed || due <= 0 {
					continue
				}
				part := min(due, limit-used)
				items = append(items, map[string]any{"claim_id": claim, "amount_minor": strconv.FormatInt(part, 10)})
				used += part
			}
			return used, rows.Err()
		}
		used, err := apply(r, left)
		if err != nil {
			return err
		}
		left -= used
		for _, p := range also {
			other, err := readReport(ctx, tx, s.TenantID, s.BookID, p.Reference, true)
			if err != nil {
				return err
			}
			limit := left
			if p.AmountMinor != "" {
				n, err := strconv.ParseInt(p.AmountMinor, 10, 64)
				if err != nil || n <= 0 {
					return ErrInvalid
				}
				limit = min(limit, n)
			}
			used, err := apply(other, limit)
			if err != nil {
				return err
			}
			left -= used
		}
		unapproved := r.Workflow != "approved"
		if len(items) > 0 {
			if _, err := tx.Exec(ctx, "SELECT public.report_settle($1,$2,$3,$4,$5::jsonb,$6)", s.TenantID, l.Business, entry, actor, string(raw(items)), unapproved); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE public.allocations SET removed_at=clock_timestamp(),removed_by=$4,removal_reason='Applied to an expense report.'
 WHERE tenant_id=$1 AND source_book_id=$2 AND source_entry_id=$3 AND removed_at IS NULL`, s.TenantID, l.Business, entry, actor); err != nil {
			return err
		}
		kind := "paid"
		if unapproved {
			kind = "paid_before_approval"
		}
		if err := reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, kind, actor, "", strconv.FormatInt(amount-left, 10)); err != nil {
			return err
		}
		if left > 0 {
			var cost string
			if err := tx.QueryRow(ctx, `SELECT coalesce(min(c.incurred_on),$3::date)::text FROM public.report_lines l JOIN public.claims c ON c.tenant_id=l.tenant_id AND c.id=l.claim_id
 WHERE l.tenant_id=$1 AND l.report_id=$2`, s.TenantID, r.ID, date).Scan(&cost); err != nil {
				return err
			}
			_ = cost
			// What's left waits for the owner to say what it is; the report
			// isn't enlarged to fit it.
			if err := tx.QueryRow(ctx, `INSERT INTO public.payment_residuals(tenant_id,id,business_book_id,report_id,person_id,source_entry_id,amount_minor)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (tenant_id,business_book_id,source_entry_id,report_id) DO UPDATE SET amount_minor=EXCLUDED.amount_minor
 RETURNING id::text`, s.TenantID, ID(), r.Business, r.ID, r.PersonID, entry, left).Scan(&out.ResidualID); err != nil {
				return err
			}
			if err := reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, "residual_opened", actor, "", strconv.FormatInt(left, 10)); err != nil {
				return err
			}
		}
		out.ExcessMinor = strconv.FormatInt(left, 10)
		rows, err := tx.Query(ctx, `SELECT id::text,claim_id::text,amount_minor::text FROM public.allocations WHERE tenant_id=$1 AND source_book_id=$2 AND source_entry_id=$3
 AND source='report_payment' AND removed_at IS NULL ORDER BY recorded_at,id`, s.TenantID, l.Business, entry)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Allocations = []AllocationView{}
		for rows.Next() {
			var v AllocationView
			if err := rows.Scan(&v.ID, &v.ClaimID, &v.AmountMinor); err != nil {
				return err
			}
			out.Allocations = append(out.Allocations, v)
		}
		return rows.Err()
	})
	return out, err
}

// ReportPart names another report a payment also pays, and how much.
type ReportPart struct {
	Reference   string `json:"reference"`
	AmountMinor string `json:"amount_minor,omitempty"`
}

// PaymentResult is what a matched payment paid.
type PaymentResult struct {
	Allocations []AllocationView `json:"allocations"`
	ExcessMinor string           `json:"excess_minor"`
	// ResidualID is what the payment left over, awaiting classification.
	ResidualID string `json:"residual_id,omitempty"`
}

type AllocationView struct {
	ID          string `json:"id"`
	ClaimID     string `json:"claim_id"`
	AmountMinor string `json:"amount_minor"`
}

// RemoveReportAllocation unmatches a payment from a claim. The payment and
// its bank movement stay; what it no longer pays is unapplied.
func (a TenantAdmin) RemoveReportAllocation(ctx context.Context, book, actor, allocation, reason string) error {
	reason = strings.TrimSpace(reason)
	if !ValidID(allocation) || reason == "" || len(reason) > 500 {
		return ErrInvalid
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		var report string
		var revision int
		err := tx.QueryRow(ctx, `SELECT coalesce((SELECT l.report_id::text FROM public.report_lines l WHERE l.tenant_id=a.tenant_id AND l.claim_id=a.claim_id ORDER BY l.revision DESC LIMIT 1),''),
 coalesce((SELECT r.revision FROM public.report_lines l JOIN public.expense_reports r ON r.tenant_id=l.tenant_id AND r.id=l.report_id WHERE l.tenant_id=a.tenant_id AND l.claim_id=a.claim_id ORDER BY l.revision DESC LIMIT 1),0)
 FROM public.allocations a WHERE a.tenant_id=$1 AND a.id=$2 AND a.debtor_book_id=$3`, s.TenantID, allocation, s.BookID).Scan(&report, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT public.remove_allocation($1,$2,$3,$4)", s.TenantID, allocation, actor, reason); err != nil {
			return err
		}
		if report != "" {
			return reportEvent(ctx, tx, s.TenantID, report, s.BookID, revision, "payment_removed", actor, "", reason)
		}
		return nil
	})
}

// Statuses a recoverable moves through: how it's recovered, and how it's
// taxed. The tax side is the accountant's; Vult only keeps what they say.
var recoveryStatuses = map[string]bool{"outstanding": true, "returned": true, "applied": true, "waived": true, "resolved": true}
var taxStatuses = map[string]bool{"not_determined": true, "review_required": true, "treatment_determined": true, "reported": true, "corrected": true}

// ResolveRecoverable moves a recoverable on one of its two tracks:
// recovery (returned with the payment that brought it back, applied to
// another report, waived or resolved with a note) or tax (as the
// accountant determines it, with a note). "flagged" and "treated" are the
// tax track's review_required and treatment_determined.
func (a TenantAdmin) ResolveRecoverable(ctx context.Context, book, actor, id, status, entry, target, note string) error {
	note = strings.TrimSpace(note)
	switch status {
	case "flagged":
		status = "review_required"
	case "treated":
		status = "treatment_determined"
	}
	track := "recovery"
	if taxStatuses[status] {
		track = "tax"
	}
	if !ValidID(id) || entry != "" && !ValidID(entry) || !recoveryStatuses[status] && !taxStatuses[status] || len(note) > 2000 {
		return ErrInvalid
	}
	switch {
	case status == "returned" && entry == "":
		return &RefusedError{Reason: "Choose the payment that returned it."}
	case status == "applied" && target == "":
		return &RefusedError{Reason: "Choose the report it pays."}
	case (status == "waived" || status == "resolved") && note == "":
		return &RefusedError{Reason: "Say why it's settled this way."}
	case track == "tax" && status != "not_determined" && status != "review_required" && note == "":
		return &RefusedError{Reason: "Write down how the accountant treated it."}
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		var report string
		var revision int
		if err := tx.QueryRow(ctx, `SELECT x.report_id::text,r.revision FROM public.recoverables x JOIN public.expense_reports r ON r.tenant_id=x.tenant_id AND r.id=x.report_id
 WHERE x.tenant_id=$1 AND x.business_book_id=$2 AND x.id=$3 FOR UPDATE OF x`, s.TenantID, s.BookID, id).Scan(&report, &revision); err != nil {
			return ErrNotFound
		}
		var targetID any
		if target != "" {
			t, err := readReport(ctx, tx, s.TenantID, s.BookID, target, false)
			if err != nil {
				return err
			}
			targetID = t.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.recoverable_events(tenant_id,id,recoverable_id,business_book_id,track,status,entry_id,target_report_id,note,recorded_by)
 VALUES($1,$2,$3,$4,$5,$6,nullif($7,'')::uuid,$8,nullif($9,''),$10)`, s.TenantID, ID(), id, s.BookID, track, status, entry, targetID, note, actor); err != nil {
			return err
		}
		column := "recovery_status"
		if track == "tax" {
			column = "tax_status"
		}
		if _, err := tx.Exec(ctx, "UPDATE public.recoverables SET "+column+"=$3 WHERE tenant_id=$1 AND id=$2", s.TenantID, id, status); err != nil {
			return err
		}
		return reportEvent(ctx, tx, s.TenantID, report, s.BookID, revision, "recoverable_resolved", actor, "", track+": "+status)
	})
}

// RecoverableReview is a review of what a recoverable means for the
// person, as the business records it.
type RecoverableReview struct {
	ID                  string    `json:"id,omitempty"`
	ResponsiblePersonID string    `json:"responsible_person_id,omitempty"`
	BasisAmountMinor    string    `json:"basis_amount_minor,omitempty"`
	Timing              string    `json:"timing,omitempty"`
	ReviewDate          string    `json:"review_date"`
	Outcome             string    `json:"outcome,omitempty"`
	OutcomeReference    string    `json:"outcome_reference,omitempty"`
	RecordedBy          string    `json:"recorded_by,omitempty"`
	RecordedAt          time.Time `json:"recorded_at,omitempty"`
}

// ReviewRecoverable keeps a review of a recoverable. Each is added; none
// changes.
func (a TenantAdmin) ReviewRecoverable(ctx context.Context, book, actor, id string, q RecoverableReview) error {
	if !ValidID(id) || !ValidDate(q.ReviewDate) || q.ResponsiblePersonID != "" && !ValidID(q.ResponsiblePersonID) || q.BasisAmountMinor != "" && !validNonNegative(q.BasisAmountMinor) ||
		len(q.Timing) > 500 || len(q.Outcome) > 2000 || len(q.OutcomeReference) > 200 {
		return ErrInvalid
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		tag, err := tx.Exec(ctx, `INSERT INTO public.recoverable_reviews(tenant_id,id,recoverable_id,business_book_id,responsible_person_id,basis_amount_minor,timing,review_date,outcome,outcome_reference,recorded_by)
 SELECT $1,$2,x.id,x.business_book_id,nullif($4,'')::uuid,nullif($5,'')::bigint,nullif(btrim($6),''),$7::date,nullif(btrim($8),''),nullif(btrim($9),''),$10
 FROM public.recoverables x WHERE x.tenant_id=$1 AND x.business_book_id=$11 AND x.id=$3`, s.TenantID, ID(), id, q.ResponsiblePersonID, q.BasisAmountMinor, q.Timing,
			q.ReviewDate, q.Outcome, q.OutcomeReference, actor, s.BookID)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
}

var residualClasses = map[string]bool{"another_report": true, "fee": true, "exchange_difference": true, "advance": true, "error": true, "unknown": true}

// ClassifyResidual says what a payment's leftover was. Only an error is
// owed back, as a recoverable from the day of the report's first cost;
// another report's share names that report, to be matched there.
func (a TenantAdmin) ClassifyResidual(ctx context.Context, book, actor, id, classification, target, note string) error {
	note = strings.TrimSpace(note)
	if !ValidID(id) || !residualClasses[classification] || len(note) > 2000 {
		return ErrInvalid
	}
	if classification == "another_report" && target == "" {
		return &RefusedError{Reason: "Choose the report it belongs to."}
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		var report, person, entry string
		var amount int64
		var classified bool
		if err := tx.QueryRow(ctx, `SELECT report_id::text,person_id::text,source_entry_id::text,amount_minor,classification IS NOT NULL FROM public.payment_residuals
 WHERE tenant_id=$1 AND business_book_id=$2 AND id=$3 FOR UPDATE`, s.TenantID, s.BookID, id).Scan(&report, &person, &entry, &amount, &classified); err != nil {
			return ErrNotFound
		}
		if classified {
			return &ReportConflict{Reason: "This leftover is already classified."}
		}
		var targetID, recoverable any
		if target != "" {
			t, err := readReport(ctx, tx, s.TenantID, s.BookID, target, false)
			if err != nil {
				return err
			}
			targetID = t.ID
		}
		var revision int
		if err := tx.QueryRow(ctx, "SELECT revision FROM public.expense_reports WHERE tenant_id=$1 AND id=$2", s.TenantID, report).Scan(&revision); err != nil {
			return err
		}
		if classification == "error" {
			id := ID()
			if _, err := tx.Exec(ctx, `INSERT INTO public.recoverables(tenant_id,id,business_book_id,report_id,person_id,amount_minor,origin,origin_entry_id,cost_date)
 SELECT $1,$2,$3,$4,$5,$6,'overpayment',$7,coalesce((SELECT min(c.incurred_on) FROM public.report_lines l JOIN public.claims c ON c.tenant_id=l.tenant_id AND c.id=l.claim_id
   WHERE l.tenant_id=$1 AND l.report_id=$4),current_date)`, s.TenantID, id, s.BookID, report, person, amount, entry); err != nil {
				return err
			}
			recoverable = id
			if err := reportEvent(ctx, tx, s.TenantID, report, s.BookID, revision, "recoverable_opened", actor, "", strconv.FormatInt(amount, 10)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE public.payment_residuals SET classification=$3,note=nullif($4,''),target_report_id=$5,recoverable_id=$6,classified_by=$7,classified_at=clock_timestamp()
 WHERE tenant_id=$1 AND id=$2`, s.TenantID, id, classification, note, targetID, recoverable, actor); err != nil {
			return err
		}
		return reportEvent(ctx, tx, s.TenantID, report, s.BookID, revision, "residual_classified", actor, "", classification)
	})
}

// ChangeClaimRecordedAccount moves a claimed cost to another account the
// way any correction does: a new entry moves the amount from the account
// it was recorded under to the new one. The recorded entry is never
// rewritten. Only while the report can change.
func (a TenantAdmin) ChangeClaimRecordedAccount(ctx context.Context, book, actor, reference, claim, account string) error {
	if !ValidID(claim) || !ValidID(account) {
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
		var current, date, vendor string
		var amount int64
		err = tx.QueryRow(ctx, `SELECT coalesce((SELECT x.account_id::text FROM public.lines x WHERE x.tenant_id=c.tenant_id AND x.book_id=c.business_book_id AND x.entry_id=c.reclassified_entry_id AND x.side='debit' LIMIT 1),
   (SELECT x.account_id::text FROM public.lines x WHERE x.tenant_id=c.tenant_id AND x.book_id=c.obligation_book_id AND x.entry_id=c.obligation_entry_id AND x.side='debit' ORDER BY x.amount_minor DESC,x.position LIMIT 1),''),
 c.incurred_on::text,c.vendor,c.requested_minor
 FROM public.claims c JOIN public.report_lines l ON l.tenant_id=c.tenant_id AND l.claim_id=c.id AND l.report_id=$3 AND l.revision=$4
 WHERE c.tenant_id=$1 AND c.id=$2 AND c.obligation_book_id=c.business_book_id`, s.TenantID, claim, r.ID, r.Revision).Scan(&current, &date, &vendor, &amount)
		if errors.Is(err, pgx.ErrNoRows) {
			return &RefusedError{Reason: "Only a cost already recorded in the business's Book is moved to another account."}
		}
		if err != nil {
			return err
		}
		if current == account {
			return nil
		}
		if current == "" {
			return &RefusedError{Reason: "This cost has no recorded account to move from."}
		}
		result, err := dispatch(ctx, tx, s, "post_journal", raw(PostJournal{RequestKey: "reclass-" + claim + "-" + account, EconomicDate: date, AccountingDate: date,
			Description: vendor + " moved to another account (" + r.Reference + ")",
			Lines:       []PostLine{{AccountID: account, Side: "debit", AmountMinor: strconv.FormatInt(amount, 10)}, {AccountID: current, Side: "credit", AmountMinor: strconv.FormatInt(amount, 10)}}}), nil)
		if err != nil {
			return err
		}
		entry := outcomeEntry(&Outcome{Result: result})
		if entry == "" {
			return ErrUncertain
		}
		_, err = tx.Exec(ctx, "UPDATE public.claims SET reclassified_entry_id=$3,proposed_account_id=$4 WHERE tenant_id=$1 AND id=$2", s.TenantID, claim, entry, account)
		return err
	})
}

// SetRevisionDocument records the PDF of an approved revision, once. A
// revision that already has one keeps it.
func (a TenantAdmin) SetRevisionDocument(ctx context.Context, book, actor, reference string, revision int, document string) error {
	if !ValidID(document) || revision < 1 {
		return ErrInvalid
	}
	return a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE public.report_revisions v SET pdf_document_id=$4,pdf_sha256=d.sha256 FROM public.documents d
 WHERE v.tenant_id=$1 AND v.report_id=$2 AND v.revision=$3 AND v.pdf_document_id IS NULL AND v.submitted_at IS NOT NULL
   AND d.tenant_id=v.tenant_id AND d.book_id=v.business_book_id AND d.id=$4`, s.TenantID, r.ID, revision, document)
		return err
	})
}

// RecordSettlementStatement records a dated statement of what was paid on
// a report, as the PDF stored for it, with the hash of the allocations it
// lists.
func (a TenantAdmin) RecordSettlementStatement(ctx context.Context, book, actor, reference, document, allocations string) (StatementView, error) {
	var out StatementView
	if !ValidID(document) || len(allocations) != 64 {
		return out, ErrInvalid
	}
	err := a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if !r.Reconciled {
			return errNotReconciled
		}
		out.ID = ID()
		if err := tx.QueryRow(ctx, `INSERT INTO public.settlement_statements(tenant_id,id,report_id,business_book_id,allocations_sha256,document_id,pdf_sha256,issued_by)
 SELECT $1,$2,$3,$4,$5,$6,d.sha256,$7 FROM public.documents d WHERE d.tenant_id=$1 AND d.book_id=$4 AND d.id=$6 RETURNING issued_at`,
			s.TenantID, out.ID, r.ID, r.Business, allocations, document, actor).Scan(&out.IssuedAt); err != nil {
			return err
		}
		out.PDFDocumentID = document
		return reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, "statement_issued", actor, "", "")
	})
	return out, err
}

// RecordReportPayment is the entry for a bank payment from the business to
// the person: the one it was recorded with, or, if it isn't recorded yet,
// a new one paying what the business owes the person.
func (a TenantAdmin) RecordReportPayment(ctx context.Context, book, actor, reference, item string) (string, error) {
	var entry string
	if !ValidID(item) {
		return "", ErrInvalid
	}
	err := a.withExpenseWriter(ctx, book, actor, false, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, false)
		if err != nil {
			return err
		}
		if !r.Reconciled {
			return errNotReconciled
		}
		l, err := readReportLink(ctx, tx, s.TenantID, r.LinkID)
		if err != nil {
			return err
		}
		var amount int64
		var description string
		if err := tx.QueryRow(ctx, "SELECT amount_minor,description FROM public.bank_transactions WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, item).Scan(&amount, &description); err != nil {
			return &RefusedError{Reason: "That payment isn't in this business's bank activity."}
		}
		if amount >= 0 {
			return &RefusedError{Reason: "That isn't a payment out of the business."}
		}
		err = tx.QueryRow(ctx, "SELECT entry_id::text FROM public.bank_transaction_links WHERE tenant_id=$1 AND book_id=$2 AND transaction_id=$3 AND revoked_at IS NULL", s.TenantID, s.BookID, item).Scan(&entry)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('vult.allocating','on',true)"); err != nil {
			return err
		}
		result, err := dispatch(ctx, tx, s, "record_bank_activity", raw(RecordBankActivity{RequestKey: "report-payment-" + item, TransactionID: item,
			Description: "Expense reimbursement " + r.Reference, OtherAccountID: l.Payable, Reason: "Paid back on " + r.Reference}), nil)
		if err != nil {
			return err
		}
		if entry = outcomeEntry(&Outcome{Result: result}); entry == "" {
			return ErrUncertain
		}
		return nil
	})
	return entry, err
}

// assessClaims keeps, on each claim submitted, what its rule profile found:
// purpose, receipt and substantiation for the US actual-expense profile.
// A profile not assessed, a relationship not established or an opening
// balance reads "not assessed"; Vult never decides how it's taxed.
func assessClaims(ctx context.Context, tx pgx.Tx, s Scope, reference string) error {
	d, err := readDetail(ctx, tx, s, reference, false)
	if err != nil {
		return err
	}
	for _, x := range d.Lines {
		var profile string
		if err := tx.QueryRow(ctx, "SELECT rule_profile FROM public.claims WHERE tenant_id=$1 AND id=$2", s.TenantID, x.ClaimID).Scan(&profile); err != nil {
			return err
		}
		a := map[string]any{"profile": profile, "relationship": x.Capacity}
		switch {
		case profile != "us_actual_expense_v1":
			a["status"], a["reason"] = "not_assessed", "The policy assesses no rule profile."
		case x.Capacity == "not_established":
			a["status"], a["reason"] = "not_assessed", "The relationship isn't established."
		case x.Kind == "opening":
			a["status"], a["reason"] = "not_assessed", "An opening balance carried from earlier records."
		default:
			a["status"] = "assessed"
			a["business_purpose"] = map[bool]string{true: "stated", false: "missing"}[strings.TrimSpace(x.Purpose) != ""]
			receipt := "not_required"
			if x.ReceiptRequired {
				switch {
				case x.HasReceipt:
					receipt = "attached"
				case x.MissingExplanation != "":
					receipt = "explained"
				default:
					receipt = "missing"
				}
				a["receipt_rule"] = x.ReceiptRule
			}
			a["receipt"] = receipt
			if x.EvidenceReceivedOn != "" {
				from, e1 := time.Parse("2006-01-02", x.Date)
				to, e2 := time.Parse("2006-01-02", x.EvidenceReceivedOn)
				if e1 == nil && e2 == nil {
					a["substantiated_after_days"] = int(to.Sub(from).Hours() / 24)
				}
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE public.claims SET assessment=$3::jsonb WHERE tenant_id=$1 AND id=$2", s.TenantID, x.ClaimID, string(raw(a))); err != nil {
			return err
		}
	}
	return nil
}

// errNotReconciled refuses matching or settling a report restored with
// money still to pay, until its owner reconciles it.
var errNotReconciled = &RefusedError{Reason: "This report came back from a backup with money still to pay. Review its payments and settlements since, then confirm it's reconciled before matching a payment or issuing a statement."}

// ReconcileReport is the owner's word that a restored report's cash and
// settlement history were reviewed: payments can be matched to it again.
func (a TenantAdmin) ReconcileReport(ctx context.Context, book, actor, reference, note string) error {
	note = strings.TrimSpace(note)
	if note == "" || len(note) > 2000 {
		return &RefusedError{Reason: "Say what you checked."}
	}
	return a.withExpenseWriter(ctx, book, actor, true, func(tx pgx.Tx, s Scope) error {
		r, err := readReport(ctx, tx, s.TenantID, s.BookID, reference, true)
		if err != nil {
			return err
		}
		if r.Reconciled {
			return nil
		}
		if _, err := tx.Exec(ctx, "UPDATE public.expense_reports SET reconciled_after_restore=true WHERE tenant_id=$1 AND id=$2", s.TenantID, r.ID); err != nil {
			return err
		}
		return reportEvent(ctx, tx, s.TenantID, r.ID, r.Business, r.Revision, "reconciled", actor, "", note)
	})
}

// correctCashClaims posts, when a corrected revision is approved, what
// changed in its accepted cash claims: a correcting entry for the
// difference, dated today, against what the business owes the person,
// never an edit of the entry first recorded. A claim the correction
// removed is corrected to nothing. The person records the same difference
// in their Book if they had recorded their side.
func correctCashClaims(ctx context.Context, tx pgx.Tx, s Scope, r reportRow, actor string) error {
	if r.Revision < 2 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT o.claim_id::text,o.amount_minor,coalesce(n.approved_minor,0),c.vendor,
 (SELECT x.account_id::text FROM public.lines x WHERE x.tenant_id=o.tenant_id AND x.book_id=o.business_book_id AND x.entry_id=o.business_entry_id AND x.side='debit'
   ORDER BY x.amount_minor DESC,x.position LIMIT 1),coalesce(o.person_book_id::text,'')
 FROM public.claim_obligations o JOIN public.claims c ON c.tenant_id=o.tenant_id AND c.id=o.claim_id
 LEFT JOIN public.report_lines n ON n.tenant_id=o.tenant_id AND n.report_id=$2 AND n.revision=$3 AND n.claim_id=o.claim_id
 WHERE o.tenant_id=$1 AND EXISTS (SELECT 1 FROM public.report_lines l WHERE l.tenant_id=o.tenant_id AND l.report_id=$2 AND l.claim_id=o.claim_id)
 ORDER BY c.incurred_on,o.claim_id`, s.TenantID, r.ID, r.Revision)
	if err != nil {
		return err
	}
	type change struct {
		claim, vendor, account, personal string
		owed, now                        int64
	}
	var changes []change
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.claim, &c.owed, &c.now, &c.vendor, &c.account, &c.personal); err != nil {
			rows.Close()
			return err
		}
		if c.now != c.owed {
			changes = append(changes, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(changes) == 0 {
		return err
	}
	l, err := readReportLink(ctx, tx, s.TenantID, r.LinkID)
	if err != nil {
		return err
	}
	for _, c := range changes {
		delta := c.now - c.owed
		amount := strconv.FormatInt(max(delta, -delta), 10)
		lines := []PostLine{{AccountID: c.account, Side: "debit", AmountMinor: amount}, {AccountID: l.Payable, Side: "credit", AmountMinor: amount}}
		if delta < 0 {
			lines = []PostLine{{AccountID: l.Payable, Side: "debit", AmountMinor: amount}, {AccountID: c.account, Side: "credit", AmountMinor: amount}}
		}
		bs := Scope{TenantID: s.TenantID, BookID: l.Business, ActorID: actor}
		if err := setScope(ctx, tx, bs); err != nil {
			return err
		}
		result, err := dispatch(ctx, tx, bs, "post_journal", raw(PostJournal{RequestKey: "claim-correction-" + c.claim + "-" + strconv.Itoa(r.Revision),
			EconomicDate: time.Now().Format("2006-01-02"), AccountingDate: time.Now().Format("2006-01-02"),
			Description: c.vendor + " corrected on " + r.Reference + " revision " + strconv.Itoa(r.Revision), Lines: lines}), nil)
		if err != nil {
			return err
		}
		entry := outcomeEntry(&Outcome{Result: result})
		if entry == "" {
			return ErrUncertain
		}
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, "SELECT public.adjust_claim_obligation($1,$2,$3,$4,$5,$6,$7,$8)", s.TenantID, c.claim, r.ID, r.Revision-1, r.Revision, delta, entry, actor).Scan(&status); err != nil {
			return err
		}
		if status == "pending" {
			var shown string
			if err := tx.QueryRow(ctx, "SELECT public.book_amount($1,$2,$3::bigint)", s.TenantID, l.Business, amount).Scan(&shown); err != nil {
				return err
			}
			more := map[bool]string{true: "more", false: "less"}[delta > 0]
			if err := ownerLoop(ctx, tx, s.TenantID, c.personal, actor, "claim-correction-"+c.claim+"-"+strconv.Itoa(r.Revision), correctionSubject(c.claim, r.Revision),
				"Record the correction of "+c.vendor+" on "+r.Reference+": the business now owes you "+shown+" "+more+"."); err != nil {
				return err
			}
		}
		if err := setScope(ctx, tx, s); err != nil {
			return err
		}
	}
	return nil
}

func correctionSubject(claim string, revision int) string {
	return "correction:" + claim + ":" + strconv.Itoa(revision)
}
